package main

import (
	"io"
	"os"
)

// Colour is for a person reading a terminal, and nothing else. These commands
// are routinely piped, eval'd, or captured by a test, and an escape sequence in
// any of those is corruption rather than decoration, so a writer that is not a
// terminal gets plain text. NO_COLOR turns it off even on one, as no-color.org
// asks, and so does TERM=dumb.
//
// Nothing is ever said in colour alone. A line that is yellow because something
// needs attention says so in words too, or piping the output would throw the
// meaning away and leave only the facts.
type palette struct {
	good, warn, bad, cmd, dim func(string) string
}

func styles(w io.Writer) palette {
	plain := func(s string) string { return s }
	if !isTerminal(w) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return palette{plain, plain, plain, plain, plain}
	}
	return palette{
		good: wrap("\x1b[32m"),
		warn: wrap("\x1b[33m"),
		bad:  wrap("\x1b[31m"),
		cmd:  wrap("\x1b[36m"),
		dim:  wrap("\x1b[2m"),
	}
}

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
	return func(s string) string { return code + s + "\x1b[0m" }
}

func isTerminal(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
