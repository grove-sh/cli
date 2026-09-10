package main

import (
	"io"
	"os"
)

// These commands are routinely piped, eval'd, or captured by a test, where an
// escape sequence is corruption rather than decoration. So nothing is ever said
// in colour alone: a line that is yellow because it needs attention says so in
// words too, or piping would throw the meaning away and leave only the facts.
type palette struct {
	good, warn, bad, cmd, dim, bold func(string) string
}

func styles(w io.Writer) palette { return build(w, wrap) }

func build(w io.Writer, with func(string) func(string) string) palette {
	plain := func(s string) string { return s }
	if !isTerminal(w) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return palette{plain, plain, plain, plain, plain, plain}
	}
	return palette{
		good: with(green),
		warn: with(yellow),
		bad:  with(red),
		cmd:  with(cyan),
		dim:  with(faint),
		bold: with(strong),
	}
}

const (
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	red    = "\x1b[31m"
	cyan   = "\x1b[36m"
	strong = "\x1b[1m"
	faint  = "\x1b[2m"
	reset  = "\x1b[0m"
)

// paint returns the colour a finding's state calls for.
func (p palette) paint(state string) func(string) string {
	switch state {
	case ok:
		return p.good
	case bad:
		return p.bad
	}
	return p.warn
}

func wrap(code string) func(string) string {
	return func(s string) string { return code + s + reset }
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
