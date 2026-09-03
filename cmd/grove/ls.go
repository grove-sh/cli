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
	"github.com/grove-sh/cli/internal/lease"
)

func newLsCommand() *cobra.Command {
	var socket string
	var all bool

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List this context's routes, or every lease on the machine",
		Long: `List the routes and ports this context declares, live or not.

Every hostname comes from the config rather than from a lease, so a route that
nothing is serving still has a URL, marked idle. Its port is where allocation
would put it: that is a prediction, since a port already in use sends an
attached lease further along the range.

A port grove has handed out reads running when something answers on it and
claimed when nothing does, which is what a stopped stack looks like: its ports
stay allocated until grove release hands them back.

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
		url := "-"
		if entry.Kind == config.KindRoute {
			url = "https://" + identity.ComposeLabel(context.Slug, entry.Label) + "." + defaultDomain
		}

		port, state, holder := lease.PredictPort(lease.PortRange{}, context.Slug, entry.Name), "idle", "-"
		if held, ok := live[entry.Name]; ok {
			port = held.Port
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
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", entry.Name, url, strconv.Itoa(port), state, holder)
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
