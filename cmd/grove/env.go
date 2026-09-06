package main

import (
	"encoding/json"
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
			layered, err := layer(cfg, context, resolved, active, grantsFrom(live))
			if err != nil {
				return err
			}

			reportSkipped(cmd.ErrOrStderr(), append(skipped, unbound(active, layered)...))
			return writeEnv(cmd.OutOrStdout(), format, layered)
		},
	}

	cmd.Flags().StringVarP(&service, "service", "s", "", "route or port to report on, overriding the directory")
	cmd.Flags().StringVar(&format, "format", "shell", "shell or json")
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	return cmd
}

// reportSkipped says what is not set and why, once.
//
// One unheld entry is usually several unset variables, and printing the same
// sentence for each buries the one line that is different: a reference to an
// entry that does not exist is a mistake, while a port nobody holds is a state
// that ends when something holds it. So the waits are grouped under the entry
// they wait on, and everything else gets its own line.
func reportSkipped(out io.Writer, skipped []config.Skipped) {
	if len(skipped) == 0 {
		return
	}
	name, detail := styles(out)

	waiting := map[string][]string{}
	var refs []string
	for _, miss := range skipped {
		if miss.Ref == "" {
			fmt.Fprintf(out, "grove: %s is not set, %s\n", name(miss.Name), detail(miss.Reason))
			continue
		}
		if _, seen := waiting[miss.Ref]; !seen {
			refs = append(refs, miss.Ref)
		}
		waiting[miss.Ref] = append(waiting[miss.Ref], miss.Name)
	}
	if len(refs) == 0 {
		return
	}

	slices.Sort(refs)
	width := 0
	for _, ref := range refs {
		if len(ref) > width {
			width = len(ref)
		}
	}
	fmt.Fprintf(out, "grove: %s\n", detail("waiting on ports nothing holds yet"))
	for _, ref := range refs {
		names := waiting[ref]
		slices.Sort(names)
		fmt.Fprintf(out, "  %-*s  %s\n", width, detail(ref), name(strings.Join(names, ", ")))
	}
}

// unbound reports the variables grove would have set itself, had anything been
// holding the entry they describe.
func unbound(active *config.Entry, env map[string]string) []config.Skipped {
	if active == nil {
		return nil
	}
	if _, bound := env["GROVE_PORT"]; bound {
		return nil
	}

	ref := active.Ref()
	missing := []config.Skipped{{Name: "GROVE_PORT", Ref: ref}}
	if active.Kind == config.KindRoute {
		missing = append(missing,
			config.Skipped{Name: "GROVE_HOST", Ref: ref},
			config.Skipped{Name: "GROVE_URL", Ref: ref},
		)
	}
	return missing
}

// liveBindings turns what the context holds into template values.
func liveBindings(socket, slug string) (map[string]config.Binding, error) {
	leases, _, err := liveLeases(socket, slug)
	if err != nil {
		return nil, err
	}

	out := make(map[string]config.Binding, len(leases))
	for name, held := range leases {
		binding := config.Binding{Port: held.Port, Host: held.Host}
		if held.Host != "" {
			binding.URL = "https://" + held.Host
		}
		out[name] = binding
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
	case "json":
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(env)
	default:
		return usageErrorf("unknown format %q; use shell or json", format)
	}
	return nil
}

// shellQuote makes a value safe to eval. Single quotes take everything
// literally, so only a single quote in the value needs work.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
