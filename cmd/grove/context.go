package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/identity"
)

func newContextCommand() *cobra.Command {
	var asJSON bool

	cmd := &cobra.Command{
		Use:   "context",
		Short: "Print the grove context resolved for this directory",
		Long: `Print the grove context resolved for the current directory.

The context is derived from the git worktree you are standing in, and names the
hostname, database, and ports grove allocates. GROVE_CONTEXT overrides it.

--json is the stable contract other tools should read.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			ctx, err := identity.Resolve(dir)
			if err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd, ctx)
			}
			return writeTable(cmd, ctx)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print as JSON")
	return cmd
}

func writeJSON(cmd *cobra.Command, ctx identity.Context) error {
	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		identity.Context
		Host string `json:"host"`
	}{ctx, ctx.Host(defaultDomain)})
}

func writeTable(cmd *cobra.Command, ctx identity.Context) error {
	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	rows := [][2]string{
		{"host", ctx.Host(defaultDomain)},
		{"project", ctx.Project},
		{"variant", ctx.Variant},
		{"slug", ctx.Slug},
		{"worktree", ctx.Root},
		{"resolved from", string(ctx.Source)},
	}
	for _, row := range rows {
		if row[1] == "" {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\n", row[0], row[1])
	}
	return w.Flush()
}
