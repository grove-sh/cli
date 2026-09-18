// Package shell writes text a shell will run: quoting the values that go into
// a command, and naming grove itself the way the reader can run it.
package shell

import "strings"

// Single quotes take everything literally, so only a single quote needs work.
// Not optional for paths: grove's state directory on macOS is ~/Library/
// Application Support/grove, and unquoted that is two arguments.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
