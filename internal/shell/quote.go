// Package shell quotes values for text a shell will run.
package shell

import "strings"

// Single quotes take everything literally, so only a single quote needs work.
// Not optional for paths: grove's state directory on macOS is ~/Library/
// Application Support/grove, and unquoted that is two arguments.
func Quote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
