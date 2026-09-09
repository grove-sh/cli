// Command grove manages local HTTPS hostnames, ports, and env vars per git
// worktree.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"

	"github.com/spf13/cobra"
)

// Never set in this repo. The release stamps it in:
//
//	go build -ldflags '-X main.customVersion=v0.1.0' ./cmd/grove
var customVersion string

// Paired with platform.Address, which its wildcard points at. See there.
//
// TODO: comes from ~/.config/grove/config.toml once config parsing lands.
const defaultDomain = "grov.site"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	root := newRootCommand()
	root.SetArgs(args)
	root.SetOut(stdout)
	root.SetErr(stderr)

	err := root.Execute()
	if err == nil {
		return 0
	}

	// A child process that failed has already said so on its own stderr.
	var exit *exitError
	if errors.As(err, &exit) {
		if exit.err != nil {
			fmt.Fprintf(stderr, "grove: %v\n", exit.err)
		}
		return exit.code
	}

	fmt.Fprintf(stderr, "grove: %v\n", err)
	var usage usageError
	if errors.As(err, &usage) {
		fmt.Fprintln(stderr, "Run 'grove --help' for usage.")
		return 2
	}
	return 1
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "grove",
		Short: "Local HTTPS hostnames, ports, and env vars, scoped per git worktree",
		Long: `grove gives every git worktree its own hostname, port, and environment.

Each project and each of its worktrees resolves to a unique context, and grove
maps that context onto a local HTTPS hostname and the env vars your tooling
already reads.`,
		SilenceUsage: true,
		// run prints errors itself, on the stream it chose.
		SilenceErrors: true,
		Version:       resolveVersion(),
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return usageErrorf("unknown command %q", args[0])
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	root.SetVersionTemplate("grove {{.Version}}\n")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return usageError{err}
	})
	root.AddCommand(
		newContextCommand(),
		newDaemonCommand(),
		newRestartCommand(),
		newStartCommand(),
		newStopCommand(),
		newExecCommand(),
		newDoctorCommand(),
		newEnvCommand(),
		newInitCommand(),
		newInstallCommand(),
		newReleaseCommand(),
		newHoldCommand(),
		newUninstallCommand(),
		newLsCommand(),
	)
	return root
}

// A nil err means the command already reported the failure, as a child does.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return fmt.Sprintf("exit status %d", e.code)
}

func (e *exitError) Unwrap() error { return e.err }

type usageError struct{ err error }

func (e usageError) Error() string { return e.err.Error() }
func (e usageError) Unwrap() error { return e.err }

func usageErrorf(format string, a ...any) usageError {
	return usageError{fmt.Errorf(format, a...)}
}

// So a bad invocation exits 2 rather than looking like a runtime failure.
func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}

// A project depending on @grove-sh/cli has no grove on PATH, so naming one is
// naming a command the reader does not have.
func invocation() string {
	self, err := os.Executable()
	if err != nil {
		return "grove"
	}
	return invocationFrom(self)
}

// Both halves have to hold. A global "npm install -g" also lands under
// node_modules, so the path alone does not mean grove is unreachable by name,
// and an unset agent means no package manager ran us, which is the same as
// saying whatever the reader typed was already on PATH.
func invocationFrom(self string) string {
	if !strings.Contains(self, "node_modules") {
		return "grove"
	}
	// Set by every package manager for the commands it runs, and grove is one.
	manager, _, _ := strings.Cut(os.Getenv("npm_config_user_agent"), "/")
	switch manager {
	case "pnpm", "yarn":
		return manager + " grove"
	case "bun":
		return "bunx grove"
	case "npm":
		return "npx grove"
	}
	return "grove"
}

// Go derives a version from VCS in a git checkout, so an unstamped build still
// reports one. "(devel)" means it had nothing to go on, as under "go run".
func resolveVersion() string {
	if customVersion != "" {
		return customVersion
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return "unknown"
}
