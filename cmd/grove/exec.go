package main

import (
	"errors"
	"fmt"
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
	var autostart, optional bool

	cmd := &cobra.Command{
		Use:   "exec [flags] -- command [args...]",
		Short: "Run a command with this context's ports and hostnames in its environment",
		Long: `Run a command with this context's ports and hostnames in its environment.

grove leases a port for the route this directory belongs to, routes the route's
hostname to it, and holds that lease for as long as the command runs. Entries
marked detached are always active, since whatever binds them outlives the
command. Signals and the exit code pass through.

With CI set in the environment and no daemon to talk to, the command runs with
its environment untouched instead of failing, because a build service is the
authority on its own environment and grove is not running there. --if-available
asks for the same tolerance anywhere.`,
		Example: "  grove exec -- pnpm dev\n  grove exec -s admin -- pnpm dev",
		Args:    usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			// Before anything else, since a command grove is not going to
			// touch should not pay for reading git and the config, and a
			// project whose config has a typo should not fail a deploy over a
			// tool that is doing nothing there.
			client, err := connect(socket, autostart, optional)
			if err != nil {
				return err
			}
			if client == nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "grove: no daemon, so running %s with the environment as it stands\n", args[0])
				return runChild(args, os.Environ())
			}
			defer client.Close()

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
	cmd.Flags().BoolVar(&optional, "if-available", false, "run the command unchanged when no daemon is running, rather than failing")
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

// valuesFrom assembles what templates resolve against. Every route's hostname
// is known from the context and the config, with no lease involved, which is
// what lets a command that binds nothing still name a route's URL. Only ports
// come from an allocation.
func valuesFrom(cfg *config.Config, context identity.Context, allocated map[string]config.Binding) config.Values {
	values := config.Values{
		Context: config.Context{
			Slug:    context.Slug,
			Project: context.Project,
			Variant: context.Variant,
		},
		Routes: make(map[string]config.Binding, len(cfg.Routes)),
		Ports:  make(map[string]config.Binding, len(cfg.Ports)),
	}

	for name, route := range cfg.Routes {
		host := identity.ComposeLabel(context.Slug, route.Label) + "." + defaultDomain
		values.Routes[name] = config.Binding{Host: host, URL: "https://" + host}
	}

	for name, binding := range allocated {
		if _, isRoute := cfg.Routes[name]; isRoute {
			held := values.Routes[name]
			held.Port = binding.Port
			values.Routes[name] = held
			continue
		}
		values.Ports[name] = binding
	}
	return values
}

func bindings(grants map[string]daemon.Grant) map[string]config.Binding {
	out := make(map[string]config.Binding, len(grants))
	for name, grant := range grants {
		out[name] = config.Binding{Port: grant.Port, Host: grant.Host, URL: grant.URL}
	}
	return out
}

func environment(cfg *config.Config, context identity.Context, active *config.Entry, grants map[string]daemon.Grant) ([]string, error) {
	values := valuesFrom(cfg, context, bindings(grants))

	resolved, err := cfg.Environment(active, values)
	if err != nil {
		return nil, err
	}

	layered, err := layer(cfg, resolved, active, grants)
	if err != nil {
		return nil, err
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

// layer puts grove's own values last, because a port hand copied into a
// checked-in .env is the drift grove exists to remove.
func layer(cfg *config.Config, resolved map[string]string, active *config.Entry, grants map[string]daemon.Grant) (map[string]string, error) {
	fromFiles, err := cfg.LoadEnvFiles()
	if err != nil {
		return nil, err
	}

	layered := make(map[string]string, len(fromFiles)+len(resolved)+4)
	for _, entry := range trust.Env(daemon.StateDir()) {
		name, value, _ := strings.Cut(entry, "=")
		layered[name] = value
	}
	// An env_file yields to the environment grove was invoked from, because an
	// inline override is the most deliberate thing in the chain and a checked
	// in file is the least. Grove's own resolved values still win over both:
	// the route points at the port it leased, so letting anything else name
	// that port would make the routing a lie.
	for name, value := range fromFiles {
		if _, inherited := os.LookupEnv(name); inherited {
			continue
		}
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
	return layered, nil
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
	//
	// Asking twice means it. A child that hangs mid shutdown would otherwise
	// hold its lease for as long as it stays alive, and since grove relays
	// rather than exits, signalling grove could not break that either: the
	// port stays claimed by something no longer listening on it.
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	done := make(chan struct{})
	defer close(done)
	go func() {
		asked := 0
		for {
			select {
			case sig := <-signals:
				asked++
				if asked == 1 {
					child.Process.Signal(sig)
					continue
				}
				fmt.Fprintf(os.Stderr, "\ngrove: %s again, so killing %s and releasing the port\n", sig, args[0])
				child.Process.Kill()
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
