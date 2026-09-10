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
			case all:
				return listLeases(cmd, socket)
			case errors.Is(err, config.ErrNotFound):
				// The same table --all prints, but nobody asked for it. Left
				// unsaid, the columns change with the directory and nothing
				// explains why, which is a fallback pretending to be an answer.
				// On stderr, so a redirected table stays a table.
				fmt.Fprintln(cmd.ErrOrStderr(), "grove: no grove.toml here, so this is every lease on the machine")
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

// What the table says when it has nothing to report. Named because the pass
// that dims them has to match them exactly, and a literal in two places drifts
// until one of them stops being dimmed for no visible reason.
const (
	dash         = "-"
	stateIdle    = "idle"
	stateClaimed = "claimed"
	stateRunning = "running"
)

// The same word in both tables, decided the same way. An attached lease is
// running by definition; a detached one stands for something grove cannot see,
// and a stopped stack keeps its ports until released, so ask whether anything
// answers on it.
func leaseState(detached bool, port int) string {
	if !detached || answering(port) {
		return stateRunning
	}
	return stateClaimed
}

// A path under home reads as ~ the way a shell writes it, which is worth more
// in a column where every row tends to share the prefix. Home is passed in so
// the shortening is testable without touching the environment.
func shortenHome(dir, home string) string {
	switch {
	case home == "", dir == "":
		return dir
	case dir == home:
		return "~"
	case strings.HasPrefix(dir, home+"/"):
		return "~" + strings.TrimPrefix(dir, home)
	}
	return dir
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
		port, state := dash, stateIdle
		if held, ok := live[entry.Name]; ok {
			port = strconv.Itoa(held.Port)
			state = leaseState(held.Detached, held.Port)
		}

		// A route's URL is worth listing whatever its state. An idle bare port
		// is only a guess, while a held one is how a stopped stack still
		// holding its ports tells itself apart from one nobody has taken.
		if entry.Kind != config.KindRoute && state == stateIdle {
			continue
		}

		url := dash
		if entry.Kind == config.KindRoute {
			url = "https://" + identity.ComposeLabel(context.Slug, entry.Label) + "." + defaultDomain
			switch {
			case problem != "":
				painted[url] = paint.bad(url)
			case state == stateRunning:
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
	fmt.Fprint(cmd.OutOrStdout(), styleCells(laid, paint.dim, paint.bold))

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

	// Home is read once: it cannot change under a single listing, and asking
	// per row would be a syscall for an answer already known.
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	// Into a buffer, so the quiet states can be dimmed once the widths are
	// settled: a tabwriter measures a cell by its bytes.
	var table bytes.Buffer
	w := tabwriter.NewWriter(&table, 0, 0, 2, ' ', 0)
	// The context and the entry get a column each. Held together they made one
	// column carrying two notations, a slug:service pair or a bare hostname,
	// so it was sized by the longest hostname while half its rows were half
	// that long. Apart, each column is only as wide as what it holds, and ROUTE
	// is the column the project listing already has.
	fmt.Fprintln(w, "CONTEXT\tROUTE\tPORT\tSTATE\tDIRECTORY")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Slug, e.Service, strconv.Itoa(e.Port),
			leaseState(e.Detached, e.Port), shortenHome(e.Worktree, home))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	paint := styles(cmd.OutOrStdout())
	fmt.Fprint(cmd.OutOrStdout(), styleCells(table.String(), paint.dim, paint.bold))
	return nil
}

// A refused connection comes back at once, so this costs nothing measurable.
// What has nothing to report reads quieter, and the one state worth acting on
// reads louder: a dash and a state that is not running dim, running bold. After
// the layout for the same reason the URLs are, since a tabwriter measures a
// cell by its bytes and cannot be told an escape code takes none.
func styleCells(laid string, dim, bold func(string) string) string {
	lines := strings.Split(laid, "\n")
	for i, line := range lines {
		// A row carries one state, so at most one of these finds anything.
		line = styleState(line, stateIdle, dim)
		line = styleState(line, stateClaimed, dim)
		line = styleState(line, stateRunning, bold)
		lines[i] = dimDashes(line, dim)
	}
	return strings.Join(lines, "\n")
}

// A state is a whole cell, so it sits behind at least the padding a tabwriter
// leaves and then ends the line or the next gap. Found by that padding rather
// than by the end of the line, so it does not depend on being the last column.
// Two spaces rather than one, because a directory that happens to contain the
// word would not have them.
func styleState(line, state string, style func(string) string) string {
	at := strings.Index(line, state)
	if at < 1 || !strings.HasSuffix(line[:at], "  ") {
		return line
	}
	rest := line[at+len(state):]
	if rest == "" || strings.HasPrefix(rest, "  ") {
		return line[:at] + style(state) + rest
	}
	return line
}

// A placeholder is a whole cell, so it is the only dash with space on both
// sides. The ones inside a hostname never are, which is what keeps this from
// taking apart apax-turborepo-supabase.
func dimDashes(line string, dim func(string) string) string {
	var out strings.Builder
	for i := 0; i < len(line); i++ {
		spaced := (i == 0 || line[i-1] == ' ') && (i == len(line)-1 || line[i+1] == ' ')
		if line[i] == '-' && spaced {
			out.WriteString(dim(dash))
			continue
		}
		out.WriteByte(line[i])
	}
	return out.String()
}

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
