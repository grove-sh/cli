package shell

import (
	"os"
	"strings"
)

// Invocation is grove's own name, spelled the way the reader can run it. A
// project depending on @grove-sh/cli has no grove on PATH, so an error naming
// one is naming a command they do not have.
func Invocation() string {
	self, err := os.Executable()
	if err != nil {
		return "grove"
	}
	return InvocationFrom(self)
}

// Both halves have to hold. A global "npm install -g" also lands under
// node_modules, so the path alone does not mean grove is unreachable by name,
// and an unset agent means no package manager ran us, which is the same as
// saying whatever the reader typed was already on PATH.
func InvocationFrom(self string) string {
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
