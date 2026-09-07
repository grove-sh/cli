package main

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/platform"
)

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

// A nil client with a nil error means grove has nothing to add and the caller
// should run the command as it stands. Only exec autostarts: a daemon appearing
// because you asked what was running would be a surprise.
func connect(socket string, autostart, optional bool) (*daemon.Client, error) {
	client, err := daemon.Dial(socket)
	if err == nil {
		return client, nil
	}
	var down *daemon.NotRunningError
	if !errors.As(err, &down) {
		return nil, err
	}

	// A build server is the authority on its own environment, so getting out of
	// the way beats injecting half an answer or failing a deploy.
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

// This only decides what happens when no daemon answered. One deliberately
// running in CI is used like any other.
func underCI() bool {
	switch os.Getenv("CI") {
	case "", "0", "false":
		return false
	}
	return true
}

func ensureDaemon(socket string) error {
	opts := defaultDaemonOptions()
	opts.socket = socket
	return spawnDaemon(opts)
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

This is what the service manager invokes, and it is hidden from the command
list because it is not how anyone starts a daemon for themselves: grove start
does that, in the background, where the service manager keeps it.

One daemon serves every context on the machine. It terminates TLS for each
context's hostname, routes it to the port that context leased, and holds the
leases. A lease lasts exactly as long as the 'grove exec' connection that asked
for it, so stopping the daemon drops all of them at once.`,
		Hidden: true,
		Args:   usageArgs(cobra.NoArgs),
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

			// Best effort: 80 only ever redirects to https, so a machine that
			// will not hand it over costs a convenience, not a feature.
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

			fmt.Fprintf(cmd.OutOrStdout(), "grove on %s, control socket %s\n", opts.listen, opts.socket)
			return server.Serve(control, https, http)
		},
	}

	opts.bind(cmd)
	return cmd
}

func newStartCommand() *cobra.Command {
	var opts daemonOptions

	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the daemon in the background, if it is not already running",
		Long: `Start the daemon in the background, if it is not already running.

Running commands through grove exec starts one on its own, so this is for
bringing the proxy up without running anything: a hostname you have bookmarked
answers again without touching the project it belongs to.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if client, err := daemon.Dial(opts.socket); err == nil {
				defer client.Close()
				status, err := client.Status()
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "already running on %s, pid %d\n", status.Listen, status.PID)
				return nil
			}
			if err := ensureDaemon(opts.socket); err != nil {
				return err
			}
			return reportStatus(cmd, opts.socket)
		},
	}

	opts.bind(cmd)
	return cmd
}

func reportStatus(cmd *cobra.Command, socket string) error {
	client, err := daemon.Dial(socket)
	if err != nil {
		return err
	}
	defer client.Close()

	status, err := client.Status()
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "grove on %s, pid %d\n", status.Listen, status.PID)
	return nil
}

func newStopCommand() *cobra.Command {
	var socket string

	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop the daemon, and every lease on the machine with it",
		Long: `Stop the running daemon.

Every lease goes with it, detached ones included, since nothing about them
survives the process. Stopping a daemon that is not running is not an error.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := daemon.Dial(socket)
			if err != nil {
				var down *daemon.NotRunningError
				if errors.As(err, &down) {
					fmt.Fprintln(cmd.OutOrStdout(), "grove is not running")
					return nil
				}
				return err
			}
			defer client.Close()

			// Read the table before ending it, so the report can say what a
			// machine-wide act with a small name just cost.
			held := whatItHolds(socket)
			if err := client.Stop(); err != nil {
				return err
			}
			waitForSocketGone(socket, 5*time.Second)
			fmt.Fprintln(cmd.OutOrStdout(), "stopped"+summarize(held))
			return nil
		},
	}

	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

// Nothing supervises the daemon, so stop-and-spawn is the whole of a restart.
func restartDaemon(opts daemonOptions) error {
	if client, err := daemon.Dial(opts.socket); err == nil {
		client.Stop()
		client.Close()
		waitForSocketGone(opts.socket, 5*time.Second)
	}
	return spawnDaemon(opts)
}

func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// Each project is asked rather than the table replayed, so one that has changed
// since gets what it asks for now. Attached leases are left out: one belongs to
// a command still running and no longer connected, with nothing to reconnect
// it to, so that command has to be run again.
func restoreContexts(cmd *cobra.Command, socket string, before []daemon.Live) {
	worktrees := map[string]string{}
	for _, lease := range before {
		if lease.Detached && lease.Worktree != "" {
			worktrees[lease.Worktree] = lease.Slug
		}
	}
	if len(worktrees) == 0 {
		return
	}

	dirs := make([]string, 0, len(worktrees))
	for dir := range worktrees {
		dirs = append(dirs, dir)
	}
	slices.Sort(dirs)

	var restored []string
	for _, dir := range dirs {
		if err := holdContext(io.Discard, socket, dir, false); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "grove: %s did not come back: %v\n", worktrees[dir], err)
			continue
		}
		restored = append(restored, worktrees[dir])
	}
	if len(restored) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "held again: %s\n", strings.Join(restored, ", "))
	}
}

// On a connection of its own, since one connection carries exactly one request
// and the caller's is about to carry the stop.
func whatItHolds(socket string) []daemon.Live {
	client, err := daemon.Dial(socket)
	if err != nil {
		return nil
	}
	defer client.Close()

	held, err := client.List()
	if err != nil {
		return nil
	}
	return held
}

// Nothing about a lease survives the process, so a running stack keeps its
// ports and loses its hostname until something holds it again.
func summarize(held []daemon.Live) string {
	if len(held) == 0 {
		return ""
	}
	contexts := map[string]bool{}
	for _, lease := range held {
		contexts[lease.Slug] = true
	}
	return fmt.Sprintf("; %s across %s released, and anything still running is unrouted until it holds again",
		count(len(held), "lease"), count(len(contexts), "context"))
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
			// A snapshot this fresh cannot describe a stack that has since gone
			// away, which is the objection to writing leases down at all.
			before := whatItHolds(opts.socket)

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
			fmt.Fprintf(cmd.OutOrStdout(), "grove on %s, pid %d\n", status.Listen, status.PID)

			restoreContexts(cmd, opts.socket, before)

			// And this one, which may have been holding nothing yet: restarting
			// from inside the project you are working on is the usual case.
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
