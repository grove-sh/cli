// Package shell quotes values that grove puts into text a shell will run or
// evaluate.
package shell

import "strings"

// Quote makes a value safe to paste into a shell. Single quotes take everything
// literally, so only a single quote in the value needs work.
//
// macOS is why this is not optional for paths: the state directory there is
// ~/Library/Application Support/grove, and an unquoted path with a space in it
// becomes two arguments in whatever command grove printed.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
