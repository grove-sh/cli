package main

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/identity"
	"github.com/grove-sh/cli/internal/trust"
)

func newExecCommand() *cobra.Command {
	var socket, service string

	cmd := &cobra.Command{
		Use:   "exec [flags] -- command [args...]",
		Short: "Run a command with this context's port and hostname in its environment",
		Long: `Run a command with this context's port and hostname in its environment.

grove leases a port, routes this context's hostname to it, and holds that lease
for as long as the command runs. Signals and the exit code pass through.`,
		Example: "  grove exec -- pnpm dev\n  grove exec -s api -- pnpm dev:api",
		Args:    usageArgs(cobra.MinimumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			context, err := identity.Resolve(dir)
			if err != nil {
				return err
			}

			client, err := daemon.Dial(socket)
			if err != nil {
				return err
			}
			defer client.Close()

			grant, err := client.Acquire(context.Slug, service, context.Root)
			if err != nil {
				return err
			}

			return runChild(args, grant, trust.Env(daemon.StateDir()))
		},
	}

	// Stop flag parsing at the command name so its own flags reach the child.
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().StringVarP(&service, "service", "s", "", "service to bind (default is the context's bare hostname)")
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

func runChild(args []string, grant daemon.Grant, caEnv []string) error {
	port := strconv.Itoa(grant.Port)

	// Inheriting the parent's descriptors keeps the child on the same terminal,
	// so it still detects a tty and keeps its colors and prompts.
	child := exec.Command(args[0], args[1:]...)
	child.Stdin, child.Stdout, child.Stderr = os.Stdin, os.Stdout, os.Stderr
	child.Env = append(os.Environ(),
		"PORT="+port,
		"GROVE_PORT="+port,
		"GROVE_HOST="+grant.Host,
		"GROVE_URL="+grant.URL,
	)
	child.Env = append(child.Env, caEnv...)

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
