package main

import (
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/daemon"
)

func newForgetCommand() *cobra.Command {
	var socket string

	cmd := &cobra.Command{
		Use:   "forget [context...]",
		Short: "Drop the recorded ports of a context whose worktree is gone",
		Long: `Drop the recorded ports of a context whose worktree is gone.

Every detached port is written down, so a stack keeps its port when the daemon
goes away and comes back. That record outlives the worktree, which is what
hands the ports back if you make the checkout again under the same name.
Nothing else removes one.

With no names, every context whose worktree is no longer on disk is forgotten.
Naming contexts forgets those instead, wherever their worktree is.

A context is kept, and said so, while it holds a lease or while something is
answering on one of its ports: the record is what stops another worktree being
handed that port, so dropping it while a stack is up is the thing this record
exists to prevent. Being kept is reported rather than failed, so naming one
that turns out to be in use still exits zero.`,
		Example: "  grove forget\n  grove forget mass-lodge-old-branch",
		Args:    usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			records, err := readRecords(socket)
			if err != nil {
				return err
			}

			wanted, unknown := choose(records, args)
			if len(unknown) > 0 {
				return bareError{fmt.Sprintf("no record for %s; 'grove ls' lists leases, and a context grove never leased a detached port for has nothing written down", strings.Join(unknown, ", "))}
			}

			out := cmd.OutOrStdout()
			if len(wanted) == 0 {
				fmt.Fprintln(out, "nothing to forget")
				return nil
			}

			leased, err := leasedContexts(socket)
			if err != nil {
				return err
			}

			kept, drop := keeping(records, wanted, leased)
			var gone []daemon.Record
			if len(drop) > 0 {
				// The records come back even on a failure partway, and saying
				// only the error would leave those unaccounted for.
				gone, err = forgetRecords(socket, drop)
				if err != nil {
					report(out, gone, kept)
					return err
				}
			}
			report(out, gone, kept)
			return nil
		},
	}

	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

// One connection carries one request, so reading the record and dropping from
// it are two dials rather than two calls on the same client.
func readRecords(socket string) ([]daemon.Record, error) {
	client, err := daemon.Dial(socket)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.Records()
}

func forgetRecords(socket string, slugs []string) ([]daemon.Record, error) {
	client, err := daemon.Dial(socket)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.Forget(slugs)
}

func leasedContexts(socket string) (map[string]bool, error) {
	client, err := daemon.Dial(socket)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	live, err := client.List()
	if err != nil {
		return nil, err
	}
	held := map[string]bool{}
	for _, l := range live {
		held[l.Slug] = true
	}
	return held, nil
}

// A record written before the daemon kept paths has no worktree to look for,
// so an unnamed sweep leaves it alone rather than guessing about it.
func choose(records []daemon.Record, args []string) (wanted []string, unknown []string) {
	held := map[string]bool{}
	for _, r := range records {
		held[r.Slug] = true
	}
	if len(args) > 0 {
		for _, slug := range args {
			if !held[slug] {
				unknown = append(unknown, slug)
				continue
			}
			if !slices.Contains(wanted, slug) {
				wanted = append(wanted, slug)
			}
		}
		return wanted, unknown
	}
	for _, r := range records {
		if r.Worktree == "" || slices.Contains(wanted, r.Slug) {
			continue
		}
		if _, err := os.Stat(r.Worktree); os.IsNotExist(err) {
			wanted = append(wanted, r.Slug)
		}
	}
	return wanted, nil
}

type keeper struct {
	slug string
	why  string
}

// A lease and a port that answers are two different ways of being in use: a
// context held but never started has no listener, and one whose daemon died
// has a listener and no lease. The port question is asked here rather than in
// the daemon, which sees the record and not the machine, with the same probe
// ls uses to call a port running.
func keeping(records []daemon.Record, wanted []string, leased map[string]bool) (kept []keeper, drop []string) {
	for _, slug := range wanted {
		if leased[slug] {
			kept = append(kept, keeper{slug: slug, why: "it holds a lease"})
			continue
		}
		var busy int
		for _, r := range records {
			if r.Slug == slug && answering(r.Port) {
				busy = r.Port
				break
			}
		}
		if busy != 0 {
			kept = append(kept, keeper{slug: slug, why: fmt.Sprintf("something is answering on %d", busy)})
			continue
		}
		drop = append(drop, slug)
	}
	return kept, drop
}

func report(out io.Writer, gone []daemon.Record, kept []keeper) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, r := range gone {
		fmt.Fprintf(w, "forgot\t%s\t%s\t%d\n", r.Slug, r.Service, r.Port)
	}
	for _, k := range kept {
		fmt.Fprintf(w, "kept\t%s\t\t%s\n", k.slug, k.why)
	}
	w.Flush()
}
