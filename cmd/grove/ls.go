package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/identity"
)

func newLsCommand() *cobra.Command {
	var socket string
	var all bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List this context's routes, or every lease on the machine",
		Long: `List the routes this context declares, live or not, and the ports it is serving.

Every hostname comes from the config rather than from a lease, so a route that
nothing is serving still has a URL, marked idle. It has no port, because a port
exists only while something holds one.

A route grove has handed a port to reads running when something answers on it
and claimed when nothing does, which is what a stopped stack looks like: its
ports stay allocated until grove release hands them back.

A bare port appears once grove has handed it out, since there is nothing to
open and the number only means something when something holds it. Until then
it is left out rather than guessed at.

Outside a project, and with --all, this lists every live lease on the machine
instead.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}

			cfg, err := config.Load(dir)
			switch {
			case all, errors.Is(err, config.ErrNotFound):
				return listLeases(cmd, socket)
			case err != nil:
				return err
			}
			return listRoutes(cmd, socket, dir, cfg)
		},
	}

	cmd.Flags().BoolVar(&all, "all", false, "list every live lease on the machine instead")
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

func listRoutes(cmd *cobra.Command, socket, dir string, cfg *config.Config) error {
	context, err := resolveContext(dir, cfg)
	if err != nil {
		return err
	}

	live, running, err := liveLeases(socket, context.Slug)
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintln(cmd.ErrOrStderr(), "grove: no daemon is running, so nothing here is being served")
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROUTE\tURL\tPORT\tSTATE\tPID")
	for _, entry := range cfg.All() {
		// A port is a fact about a lease, so an entry without one has no number
		// to show. Allocation is a hash and could be run ahead of time, but a
		// guess printed in the same column as an allocation reads as one.
		port, state, holder := "-", "idle", "-"
		if held, ok := live[entry.Name]; ok {
			port = strconv.Itoa(held.Port)
			// An attached lease is held by a command grove is watching, so it
			// is running by definition. A detached one stands for something
			// grove cannot see, and whether anything answers is the question
			// worth asking: a stopped stack keeps its ports until released.
			switch {
			case !held.Detached, answering(held.Port):
				state = "running"
			default:
				state = "claimed"
			}
			if held.PID != 0 {
				holder = strconv.Itoa(held.PID)
			}
		}

		// A route is worth listing whatever its state, since its URL is the
		// thing you would go and open. A bare port has no URL, so an idle one
		// is only a guess at where allocation would put it, and guesses are
		// noise. A held one is a fact, and the fact worth seeing: it is how a
		// stopped stack still holding its ports tells itself apart from a port
		// nobody has taken.
		if entry.Kind != config.KindRoute && state == "idle" {
			continue
		}

		url := "-"
		if entry.Kind == config.KindRoute {
			url = "https://" + identity.ComposeLabel(context.Slug, entry.Label) + "." + defaultDomain
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", entry.Name, url, port, state, holder)
	}
	return w.Flush()
}

func listLeases(cmd *cobra.Command, socket string) error {
	client, err := daemon.Dial(socket)
	if err != nil {
		return err
	}
	defer client.Close()

	entries, err := client.List()
	if err != nil || len(entries) == 0 {
		return err
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPORT\tHELD\tWORKTREE")
	for _, e := range entries {
		name := e.Host
		if name == "" {
			name = e.Slug + ":" + e.Service
		}
		held := "command"
		if e.Detached {
			held = "detached"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, strconv.Itoa(e.Port), held, e.Worktree)
	}
	return w.Flush()
}

// answering reports whether anything is listening on a loopback port. A
// refused connection comes back at once, so this costs nothing worth measuring.
func answering(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// liveLeases reports what one context holds, and whether a daemon answered at
// all. Nothing running is a true answer rather than a failure, since the routes
// are worth listing either way.
func liveLeases(socket, slug string) (map[string]daemon.Live, bool, error) {
	client, err := daemon.Dial(socket)
	if err != nil {
		var down *daemon.NotRunningError
		if errors.As(err, &down) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer client.Close()

	leases, err := client.List()
	if err != nil {
		return nil, false, err
	}

	out := make(map[string]daemon.Live)
	for _, held := range leases {
		if held.Slug == slug {
			out[held.Service] = held
		}
	}
	return out, true, nil
}
