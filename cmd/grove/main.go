// Command grove manages local HTTPS hostnames, ports, and env vars per git
// worktree.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// customVersion is never set in this repo. Downstream packagers stamp it in:
//
//	go build -ldflags '-X main.customVersion=v0.1.0' ./cmd/grove
var customVersion string

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
		newExecCommand(),
		newDoctorCommand(),
		newInstallCommand(),
		newReleaseCommand(),
		newRestartCommand(),
		newStopCommand(),
		newSyncCommand(),
		newUninstallCommand(),
		newLsCommand(),
	)
	return root
}

// exitError carries an exit code out to run. A nil err means the command has
// already reported the failure, as a child process does.
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

// usageArgs marks a cobra argument validator's failures as usage errors, so a
// bad invocation exits 2 rather than looking like a runtime failure.
func usageArgs(validate cobra.PositionalArgs) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := validate(cmd, args); err != nil {
			return usageError{err}
		}
		return nil
	}
}

// resolveVersion reports the version Go recorded in the binary. The go command
// derives it from VCS in a git checkout, so nothing stamps it in at build time.
// "(devel)" means it had nothing to go on, as with "go run" or -buildvcs=false.
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
