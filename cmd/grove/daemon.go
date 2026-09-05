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
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/service"
)

// daemonOptions are shared by the command that runs a daemon and the one that
// starts a fresh one.
type daemonOptions struct {
	socket     string
	listen     string
	httpListen string
	domain     string
	caDir      string
}

func defaultDaemonOptions() daemonOptions {
	listen := os.Getenv("GROVE_LISTEN")
	if listen == "" {
		listen = platform.DefaultListen()
	}
	httpListen := os.Getenv("GROVE_HTTP_LISTEN")
	if httpListen == "" {
		httpListen = platform.DefaultHTTPListen()
	}
	return daemonOptions{
		socket:     daemon.DefaultSocket(),
		listen:     listen,
		httpListen: httpListen,
		domain:     defaultDomain,
		caDir:      daemon.StateDir(),
	}
}

func (o *daemonOptions) bind(cmd *cobra.Command) {
	defaults := defaultDaemonOptions()
	cmd.Flags().StringVar(&o.socket, "socket", defaults.socket, "control socket path")
	cmd.Flags().StringVar(&o.listen, "listen", defaults.listen, "address to serve HTTPS on")
	cmd.Flags().StringVar(&o.httpListen, "http-listen", defaults.httpListen, "address to answer plain HTTP on, redirecting to HTTPS")
	cmd.Flags().StringVar(&o.domain, "domain", defaults.domain, "domain every context lives under")
	cmd.Flags().StringVar(&o.caDir, "ca-dir", defaults.caDir, "directory holding the local CA")
}

// connect reaches the daemon, starting one when nothing answers. A nil client
// with a nil error means grove has nothing to add and the caller should run the
// command as it stands.
//
// Only exec does any of this: a daemon appearing because you asked what was
// running, or because you asked to stop it, would be a surprise.
func connect(socket string, autostart, optional bool) (*daemon.Client, error) {
	client, err := daemon.Dial(socket)
	if err == nil {
		return client, nil
	}
	var down *daemon.NotRunningError
	if !errors.As(err, &down) {
		return nil, err
	}

	// A build server has its own environment and grove is not the authority
	// there, so with nothing to talk to the honest move is to get out of the
	// way rather than to inject half an answer or to fail a deploy.
	if optional || underCI() {
		return nil, nil
	}
	if !autostart {
		return nil, err
	}
	if err := ensureDaemon(socket); err != nil {
		return nil, err
	}
	return daemon.Dial(socket)
}

// underCI follows the convention every build service shares. It only decides
// what happens when no daemon answered: a daemon deliberately running in CI is
// used like any other.
func underCI() bool {
	switch os.Getenv("CI") {
	case "", "0", "false":
		return false
	}
	return true
}

// ensureDaemon prefers the service manager when there is a unit for it to
// manage, so a daemon grove starts is one the manager knows about. That only
// works for the standard socket: the registered daemon answers there, and
// starting it would leave a caller who named its own socket waiting on one
// nobody is listening to.
func ensureDaemon(socket string) error {
	if usesServiceSocket(socket) {
		if state := service.Status(); state.Supported && state.Installed {
			if err := service.Start(); err == nil {
				return waitForSocket(socket, 15*time.Second)
			}
		}
	}
	opts := defaultDaemonOptions()
	opts.socket = socket
	return spawnDaemon(opts)
}

func usesServiceSocket(socket string) bool {
	return os.Getenv("GROVE_SOCKET") == "" && socket == daemon.DefaultSocket()
}

func (o daemonOptions) args() []string {
	return []string{
		"--socket", o.socket, "--listen", o.listen, "--http-listen", o.httpListen,
		"--domain", o.domain, "--ca-dir", o.caDir,
	}
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
			server, err := daemon.New(daemon.Config{Domain: opts.domain, CADir: opts.caDir, Version: resolveVersion()})
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

			// Port 80 is best effort. It only ever answers with a redirect to
			// https, so a machine that will not hand it over costs a
			// convenience rather than a feature, and saying so beats failing to
			// start over it.
			http, err := net.Listen("tcp", opts.httpListen)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "grove: no plain http redirect on %s: %v\n", opts.httpListen, err)
			}

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			go func() {
				<-ctx.Done()
				server.Shutdown()
			}()

			fmt.Fprintf(cmd.OutOrStdout(), "grove daemon on %s, control socket %s\n", opts.listen, opts.socket)
			return server.Serve(control, https, http)
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

// restartDaemon puts the daemon back the way this machine keeps it.
//
// A daemon the service manager owns has to come back through the service
// manager. Stopping it and spawning a replacement works, right up until the
// next reboot: the unit is left dead, so nothing starts grove at login, and
// the machine looks fine until the morning it does not.
func restartDaemon(opts daemonOptions) error {
	// Whatever is listening goes first, whoever started it. A daemon spawned
	// outside the service still holds the port the service's own would want,
	// and systemd would report a start failure that is really a collision with
	// grove itself.
	if client, err := daemon.Dial(opts.socket); err == nil {
		client.Stop()
		client.Close()
		waitForSocketGone(opts.socket, 5*time.Second)
	}

	if serviceOwnsDaemon(opts) {
		if err := service.Restart(); err != nil {
			return err
		}
		return waitForSocket(opts.socket, 15*time.Second)
	}
	return spawnDaemon(opts)
}

// serviceOwnsDaemon reports whether the service manager is running the daemon
// this command is about.
//
// Every clause is a way it might not be. Options that differ from the defaults
// are asking for a daemon the unit does not describe, so restarting the unit
// would quietly ignore them. An unmanaged service directory means grove is
// staying away from the real service manager, which is what keeps a test from
// reaching this machine's systemd.
func serviceOwnsDaemon(opts daemonOptions) bool {
	if !usesServiceSocket(opts.socket) || opts != defaultDaemonOptions() || !service.Managed() {
		return false
	}
	state := service.Status()
	return state.Supported && state.Installed
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
			if err := restartDaemon(opts); err != nil {
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

			// A fresh daemon knows nothing. Restore this context at least,
			// since restarting from inside the project you are working on is
			// the usual case. Other contexts need their own grove sync.
			if err := syncContext(cmd.OutOrStdout(), opts.socket, false); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "grove: could not restore this context: %v\n", err)
			}
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
