package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
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
		fmt.Fprintln(cmd.ErrOrStderr(), "grove is not running, so nothing here is being served")
	}

	// The URL carries how much to believe it. The STATE column and the line
	// under the table say the same in words, since piping keeps only those.
	paint := styles(cmd.OutOrStdout())
	problem := urlProblem(daemon.StateDir())

	// Coloured after the layout, not during it: a tabwriter measures a cell by
	// its bytes and cannot be told an escape code takes no width.
	var table bytes.Buffer
	painted := map[string]string{}
	w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ROUTE\tURL\tPORT\tSTATE")
	routed := false
	for _, entry := range cfg.All() {
		// Allocation is a hash and could be run ahead, but a guess printed in
		// the same column as a real allocation reads as one.
		port, state := "-", "idle"
		if held, ok := live[entry.Name]; ok {
			port = strconv.Itoa(held.Port)
			// An attached lease is running by definition. A detached one stands
			// for something grove cannot see, and a stopped stack keeps its
			// ports until released, so ask whether anything answers.
			switch {
			case !held.Detached, answering(held.Port):
				state = "running"
			default:
				state = "claimed"
			}
		}

		// A route's URL is worth listing whatever its state. An idle bare port
		// is only a guess, while a held one is how a stopped stack still
		// holding its ports tells itself apart from one nobody has taken.
		if entry.Kind != config.KindRoute && state == "idle" {
			continue
		}

		url := "-"
		if entry.Kind == config.KindRoute {
			url = "https://" + identity.ComposeLabel(context.Slug, entry.Label) + "." + defaultDomain
			switch {
			case problem != "":
				painted[url] = paint.bad(url)
			case state == "running":
				painted[url] = paint.good(url)
			default:
				painted[url] = paint.warn(url)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", entry.Name, url, port, state)
		routed = routed || entry.Kind == config.KindRoute
	}
	if err := w.Flush(); err != nil {
		return err
	}
	laid := table.String()
	for plain, colour := range painted {
		laid = strings.Replace(laid, plain, colour, 1)
	}
	fmt.Fprint(cmd.OutOrStdout(), laid)

	// A URL in a table reads as a promise, and someone whose browser refuses it
	// concludes their app is broken rather than that grove is unfinished.
	if routed && problem != "" {
		say := styles(cmd.ErrOrStderr())
		fmt.Fprintln(cmd.ErrOrStderr(), say.warn(fmt.Sprintf("%s, so those URLs will not open. Run %s doctor.",
			problem, invocation())))
	}
	return nil
}

func listLeases(cmd *cobra.Command, socket string) error {
	// Nothing running is a true answer here too. Which listing runs depends on
	// finding a grove.toml, so disagreeing made ls differ by one directory.
	client, err := daemon.Dial(socket)
	if err != nil {
		var down *daemon.NotRunningError
		if !errors.As(err, &down) {
			return err
		}
		fmt.Fprintln(cmd.ErrOrStderr(), "grove is not running, so nothing is leased")
		return nil
	}
	defer client.Close()

	entries, err := client.List()
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		// Saying so, because an empty table and a stopped daemon printed the
		// same nothing.
		fmt.Fprintln(cmd.ErrOrStderr(), "grove is running and holding nothing")
		return nil
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

// A refused connection comes back at once, so this costs nothing measurable.
func answering(port int) bool {
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Nothing running is a true answer rather than a failure: the routes are worth
// listing either way.
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
