package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/daemon"
)

// daemonOptions are shared by the command that runs a daemon and the one that
// starts a fresh one.
type daemonOptions struct {
	socket string
	listen string
	domain string
	caDir  string
}

func (o *daemonOptions) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&o.socket, "socket", daemon.DefaultSocket(), "control socket path")
	cmd.Flags().StringVar(&o.listen, "listen", "127.0.0.1:443", "address to serve HTTPS on")
	cmd.Flags().StringVar(&o.domain, "domain", defaultDomain, "domain every context lives under")
	cmd.Flags().StringVar(&o.caDir, "ca-dir", daemon.StateDir(), "directory holding the local CA")
}

func (o daemonOptions) args() []string {
	return []string{"--socket", o.socket, "--listen", o.listen, "--domain", o.domain, "--ca-dir", o.caDir}
}

func newDaemonCommand() *cobra.Command {
	var opts daemonOptions

	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the grove proxy and lease registry in the foreground",
		Long: `Run the grove daemon in the foreground.

The daemon terminates TLS for every context's hostname, routes each one to the
port it leased, and holds the leases. A lease lasts exactly as long as the
'grove exec' connection that asked for it.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			server, err := daemon.New(daemon.Config{Domain: opts.domain, CADir: opts.caDir})
			if err != nil {
				return err
			}

			control, err := daemon.Listen(opts.socket)
			if err != nil {
				return err
			}
			defer os.Remove(opts.socket)

			https, err := net.Listen("tcp", opts.listen)
			if err != nil {
				return bindHint(opts.listen, err)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				server.Shutdown()
			}()

			fmt.Fprintf(cmd.OutOrStdout(), "grove daemon on %s, control socket %s\n", opts.listen, opts.socket)
			return server.Serve(control, https)
		},
	}

	opts.bind(cmd)
	return cmd
}

func newStopCommand() *cobra.Command {
	var socket string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the running daemon",
		Long: `Stop the running daemon.

Every lease goes with it, detached ones included, since nothing about them
survives the process. Stopping a daemon that is not running is not an error.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := daemon.Dial(socket)
			if err != nil {
				var down *daemon.NotRunningError
				if errors.As(err, &down) {
					fmt.Fprintln(cmd.OutOrStdout(), "no daemon is running")
					return nil
				}
				return err
			}
			defer client.Close()

			if err := client.Stop(); err != nil {
				return err
			}
			waitForSocketGone(socket, 5*time.Second)
			fmt.Fprintln(cmd.OutOrStdout(), "stopped")
			return nil
		},
	}

	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

func newRestartCommand() *cobra.Command {
	var opts daemonOptions

	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Stop the daemon if it is running, then start a fresh one in the background",
		Long: `Stop the daemon if it is running, then start a fresh one in the background.

This is what to run after rebuilding grove, since the daemon keeps speaking the
protocol it was built with. Its output goes to daemon.log in the state
directory.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if client, err := daemon.Dial(opts.socket); err == nil {
				client.Stop()
				client.Close()
				waitForSocketGone(opts.socket, 5*time.Second)
			}

			if err := spawnDaemon(opts); err != nil {
				return err
			}

			client, err := daemon.Dial(opts.socket)
			if err != nil {
				return err
			}
			defer client.Close()

			status, err := client.Status()
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "grove daemon on %s, pid %d\n", status.Listen, status.PID)
			return nil
		},
	}

	opts.bind(cmd)
	return cmd
}

func bindHint(address string, err error) error {
	switch {
	case errors.Is(err, syscall.EACCES):
		return fmt.Errorf("%w\nBinding a port below 1024 needs privileges grove does not have yet. Try --listen 127.0.0.1:8443", err)
	case errors.Is(err, syscall.EADDRINUSE):
		return fmt.Errorf("%w\nSomething already holds %s. If you use lando, 'lando poweroff' releases it, or try --listen 127.0.0.1:8443", err, address)
	}
	return err
}
