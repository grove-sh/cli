package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/daemon"
)

func newEnvCommand() *cobra.Command {
	var socket, service, format string

	cmd := &cobra.Command{
		Use:   "env",
		Short: "Print the environment grove would give a command here",
		Long: `Print the environment grove would give a command here.

Ports come from the leases that exist right now, so this reports rather than
allocates: a variable naming a port nobody holds is left out, and named on
stderr. Run the command under grove exec, or grove sync, to bring those into
being.`,
		Example: "  eval \"$(grove env)\"\n  grove env --format json",
		Args:    usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			cfg, err := config.Load(dir)
			if err != nil {
				return err
			}
			context, err := resolveContext(dir, cfg)
			if err != nil {
				return err
			}
			active, err := cfg.Select(dir, service)
			if err != nil {
				return err
			}

			live, err := liveBindings(socket, context.Slug)
			if err != nil {
				return err
			}

			resolved, skipped := cfg.EnvironmentSkipping(active, valuesFrom(cfg, context, live))
			layered, err := layer(cfg, resolved, active, grantsFrom(live))
			if err != nil {
				return err
			}

			for _, miss := range skipped {
				fmt.Fprintf(cmd.ErrOrStderr(), "grove: %s is not set, %s\n", miss.Name, miss.Reason)
			}
			return writeEnv(cmd.OutOrStdout(), format, layered)
		},
	}

	cmd.Flags().StringVarP(&service, "service", "s", "", "route or port to report on, overriding the directory")
	cmd.Flags().StringVar(&format, "format", "shell", "shell, dotenv, or json")
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

// liveBindings reports what this context currently holds. A daemon that is not
// running holds nothing, which is a true answer rather than a failure.
func liveBindings(socket, slug string) (map[string]config.Binding, error) {
	client, err := daemon.Dial(socket)
	if err != nil {
		var down *daemon.NotRunningError
		if errors.As(err, &down) {
			return nil, nil
		}
		return nil, err
	}
	defer client.Close()

	leases, err := client.List()
	if err != nil {
		return nil, err
	}

	out := make(map[string]config.Binding)
	for _, lease := range leases {
		if lease.Slug != slug {
			continue
		}
		binding := config.Binding{Port: lease.Port, Host: lease.Host}
		if lease.Host != "" {
			binding.URL = "https://" + lease.Host
		}
		out[lease.Service] = binding
	}
	return out, nil
}

func grantsFrom(live map[string]config.Binding) map[string]daemon.Grant {
	out := make(map[string]daemon.Grant, len(live))
	for name, binding := range live {
		out[name] = daemon.Grant{Port: binding.Port, Host: binding.Host, URL: binding.URL}
	}
	return out
}

func writeEnv(out io.Writer, format string, env map[string]string) error {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)

	switch format {
	case "shell":
		for _, name := range names {
			fmt.Fprintf(out, "export %s=%s\n", name, shellQuote(env[name]))
		}
	case "dotenv":
		for _, name := range names {
			fmt.Fprintf(out, "%s=%s\n", name, env[name])
		}
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(env)
	default:
		return usageErrorf("unknown format %q; use shell, dotenv, or json", format)
	}
	return nil
}

// shellQuote makes a value safe to eval. Single quotes take everything
// literally, so only a single quote in the value needs work.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
