package main

import (
	"cmp"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/daemon"
)

func newSyncCommand() *cobra.Command {
	var socket string

	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Re-register this context's detached ports and hostnames",
		Long: `Re-register this context's detached ports and hostnames.

A restarted daemon knows nothing, since leases live in its memory. The ports
themselves are derived from the context, so a stack that is still running is
still on the right ones, but its hostname has nowhere to route until something
says the context exists. This is that something.

Attached ports are not restored, because they belong to a command that has to
be running for the port to mean anything.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return syncContext(cmd.OutOrStdout(), socket, true)
		},
	}

	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

// syncContext asserts the detached entries of whichever context the working
// directory belongs to. Quiet callers, restart among them, skip the report and
// treat a project without detached entries as nothing to do.
func syncContext(out io.Writer, socket string, report bool) error {
	dir, err := os.Getwd()
	if err != nil {
		return err
	}

	cfg, err := config.Load(dir)
	if err != nil {
		if errors.Is(err, config.ErrNotFound) && !report {
			return nil
		}
		return err
	}

	detached := cfg.Detached()
	if len(detached) == 0 {
		if report {
			fmt.Fprintln(out, "this project declares no detached ports")
		}
		return nil
	}

	context, err := resolveContext(dir, cfg)
	if err != nil {
		return err
	}

	entries := make([]daemon.Entry, 0, len(detached))
	for _, entry := range detached {
		entries = append(entries, daemon.Entry{
			Name:     entry.Name,
			Label:    entry.Label,
			Routed:   entry.Kind == config.KindRoute,
			Detached: true,
		})
	}

	client, err := daemon.Dial(socket)
	if err != nil {
		return err
	}
	defer client.Close()

	grants, err := client.Acquire(context.Slug, context.Root, entries)
	if err != nil {
		return err
	}
	if !report {
		return nil
	}

	names := make([]string, 0, len(grants))
	for name := range grants {
		names = append(names, name)
	}
	slices.SortFunc(names, func(a, b string) int { return cmp.Compare(a, b) })

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, name := range names {
		grant := grants[name]
		shown := grant.Host
		if shown == "" {
			shown = context.Slug + ":" + name
		}
		fmt.Fprintf(w, "%s\t%s\n", shown, strconv.Itoa(grant.Port))
	}
	return w.Flush()
}
