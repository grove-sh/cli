package main

import (
	"fmt"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/daemon"
)

func newLsCommand() *cobra.Command {
	var socket string

	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the contexts that are running",
		Long: `List the contexts that are running.

Only live leases appear. With the daemon down nothing is routed and nothing is
running, so the list is empty rather than stale.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
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
		},
	}

	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}
