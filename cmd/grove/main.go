// Command grove manages local HTTPS hostnames, ports, and env vars per git
// worktree.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// customVersion is never set in this repo. Downstream packagers stamp it in:
//
//	go build -ldflags '-X main.customVersion=v0.1.0' ./cmd/grove
var customVersion string

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("grove", flag.ContinueOnError)

	// Silence flag's own error and usage output so every stream below is ours.
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}

	showVersion := fs.Bool("v", false, "print the grove version and exit")
	fs.BoolVar(showVersion, "version", false, "print the grove version and exit")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		fmt.Fprintf(stderr, "grove: %v\n\n", err)
		usage(stderr)
		return 2
	}

	if *showVersion {
		fmt.Fprintf(stdout, "grove %s\n", resolveVersion())
		return 0
	}

	if fs.NArg() == 0 {
		usage(stdout)
		return 0
	}

	fmt.Fprintf(stderr, "grove: unknown command %q\n\n", fs.Arg(0))
	usage(stderr)
	return 2
}

func usage(w io.Writer) {
	fmt.Fprint(w, `grove manages local HTTPS hostnames, ports, and env vars per git worktree.

usage:
  grove [flags]

flags:
  -v, -version   print the grove version and exit
  -h, -help      print this help and exit
`)
}

// The go command derives the version from VCS in a git checkout, so nothing
// stamps it in at build time. "(devel)" means it had nothing to go on, as with
// "go run" or -buildvcs=false.
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
