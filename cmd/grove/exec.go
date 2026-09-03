package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/identity"
	"github.com/grove-sh/cli/internal/trust"
)

func newExecCommand() *cobra.Command {
	var socket, service string
	var autostart bool

	cmd := &cobra.Command{
		Use:   "exec [flags] -- command [args...]",
		Short: "Run a command with this context's ports and hostnames in its environment",
		Long: `Run a command with this context's ports and hostnames in its environment.

grove leases a port for the route this directory belongs to, routes the route's
hostname to it, and holds that lease for as long as the command runs. Entries
marked detached are always active, since whatever binds them outlives the
command. Signals and the exit code pass through.`,
		Example: "  grove exec -- pnpm dev\n  grove exec -s admin -- pnpm dev",
		Args:    usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			context, err := resolveContext(dir, cfg)
			if err != nil {
				return err
			}
			active, err := cfg.Select(dir, service)
			if err != nil {
				return err
			}

			client, err := dialOrStart(socket, autostart)
			if err != nil {
				return err
			}
			defer client.Close()

			grants, err := client.Acquire(context.Slug, context.Root, entriesToLease(cfg, active))
			if err != nil {
				return err
			}

			env, err := environment(cfg, context, active, grants)
			if err != nil {
				return err
			}
			return runChild(args, env)
		},
	}

	// Stop flag parsing at the command name so its own flags reach the child.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVarP(&service, "service", "s", "", "route or port to bind, overriding the directory")
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	cmd.Flags().BoolVar(&autostart, "autostart", true, "start a daemon if none is running")
	return cmd
}

func entriesToLease(cfg *config.Config, active *config.Entry) []daemon.Entry {
	var out []daemon.Entry
	add := func(entry *config.Entry) {
		out = append(out, daemon.Entry{
			Name:     entry.Name,
			Label:    entry.Label,
			Routed:   entry.Kind == config.KindRoute,
			Detached: entry.Detached,
		})
	}

	for _, entry := range cfg.Detached() {
		add(entry)
	}
	if active != nil && !active.Detached {
		add(active)
	}
	return out
}

// environment layers what the child runs with, most specific last: the parent's
// environment, the CA variables for runtimes that ignore the OS trust store,
// the project's env_files, and finally grove's own resolved values, which win
// because a hand-copied port in a checked-in .env is the drift grove exists to
// remove.
func environment(cfg *config.Config, context identity.Context, active *config.Entry, grants map[string]daemon.Grant) ([]string, error) {
	values := config.Values{
		Context: config.Context{
			Slug:    context.Slug,
			Project: context.Project,
			Variant: context.Variant,
		},
		Routes: make(map[string]config.Binding, len(grants)),
		Ports:  make(map[string]config.Binding, len(grants)),
	}
	for name, grant := range grants {
		binding := config.Binding{Port: grant.Port, Host: grant.Host, URL: grant.URL}
		if _, isRoute := cfg.Routes[name]; isRoute {
			values.Routes[name] = binding
		} else {
			values.Ports[name] = binding
		}
	}

	resolved, err := cfg.Environment(active, values)
	if err != nil {
		return nil, err
	}

	fromFiles, err := cfg.LoadEnvFiles()
	if err != nil {
		return nil, err
	}

	layered := make(map[string]string, len(fromFiles)+len(resolved)+4)
	for _, entry := range trust.Env(daemon.StateDir()) {
		name, value, _ := strings.Cut(entry, "=")
		layered[name] = value
	}
	for name, value := range fromFiles {
		layered[name] = value
	}
	if active != nil {
		if grant, ok := grants[active.Name]; ok {
			layered["GROVE_PORT"] = strconv.Itoa(grant.Port)
			if grant.Host != "" {
				layered["GROVE_HOST"] = grant.Host
				layered["GROVE_URL"] = grant.URL
			}
		}
	}
	for name, value := range resolved {
		layered[name] = value
	}

	env := os.Environ()
	names := make([]string, 0, len(layered))
	for name := range layered {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		env = append(env, name+"="+layered[name])
	}
	return env, nil
}

func runChild(args []string, env []string) error {
	// Inheriting the parent's descriptors keeps the child on the same terminal,
	// so it still detects a tty and keeps its colors and prompts.
	child := exec.Command(args[0], args[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = env

	if err := child.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return &exitError{code: 127, err: err}
		}
		return err
	}

	// The child shares grove's process group, so a terminal delivers Ctrl-C to
	// both. Relaying anyway covers the case of a signal sent to grove alone.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	done := make(chan struct{})
	defer close(done)
	go func() {
		for {
			select {
			case sig := <-signals:
				child.Process.Signal(sig)
			case <-done:
				return
			}
		}
	}()

	return childExit(child.Wait())
}

func childExit(err error) error {
	if err == nil {
		return nil
	}
	var exited *exec.ExitError
	if !errors.As(err, &exited) {
		return err
	}
	if code := exited.ExitCode(); code >= 0 {
		return &exitError{code: code}
	}
	if status, ok := exited.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return &exitError{code: 128 + int(status.Signal())}
	}
	return &exitError{code: 1}
}
