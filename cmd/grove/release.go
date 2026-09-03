package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/daemon"
)

func newReleaseCommand() *cobra.Command {
	var socket string

	cmd := &cobra.Command{
		Use:   "release [name...]",
		Short: "Release this context's detached ports",
		Long: `Release this context's detached ports.

A detached port outlives the command that asked for it, because a container
stack holds it rather than a child of grove. Releasing one hands the number
back and drops its hostname. Attached ports are untouched: they belong to a
running command and end with it.

With no names, every detached port this context holds is released.`,
		Example: "  grove release\n  grove release studio db",
		Args:    usageArgs(cobra.ArbitraryArgs),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := config.Load(dir)
			if err != nil && !errors.Is(err, config.ErrNotFound) {
				return err
			}
			context, err := resolveContext(dir, cfg)
			if err != nil {
				return err
			}

			client, err := daemon.Dial(socket)
			if err != nil {
				return err
			}
			defer client.Close()

			released, err := client.Release(context.Slug, context.Root, args)
			if err != nil {
				return err
			}
			if len(released) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "%s holds no detached ports\n", context.Slug)
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "released %s\n", strings.Join(released, ", "))
			return nil
		},
	}

	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}
