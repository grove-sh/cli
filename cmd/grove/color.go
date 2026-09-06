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
func styles(w io.Writer) (name, detail func(string) string) {
	plain := func(s string) string { return s }
	if !isTerminal(w) || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return plain, plain
	}
	return wrap("\x1b[33m"), wrap("\x1b[2m")
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
