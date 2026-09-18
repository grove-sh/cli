package shell_test

import (
	"testing"

	"github.com/grove-sh/cli/internal/shell"
)

// Someone whose only grove is a project dependency has no grove on PATH, so
// naming one is naming a command they cannot run.
func TestInvocationFollowsHowGroveWasReached(t *testing.T) {
	for _, tc := range []struct {
		name, agent, want string
	}{
		{"pnpm", "pnpm/11.22.0 npm/? node/v26.0.0 linux x64", "pnpm grove"},
		{"yarn", "yarn/4.6.0 npm/? node/v22.0.0 linux x64", "yarn grove"},
		{"bun", "bun/1.2.0", "bunx grove"},
		{"npm", "npm/10.9.0 node/v22.0.0 linux x64", "npx grove"},
		// A global install lives under node_modules too, and the reader who
		// typed "grove" there does not want to be told to run npx.
		{"nothing said", "", "grove"},
		{"unknown manager", "deno/2.1.0", "grove"},
	} {
		t.Setenv("npm_config_user_agent", tc.agent)
		if got := shell.InvocationFrom("/x/node_modules/@grove-sh/cli-linux-x64/bin/grove"); got != tc.want {
			t.Errorf("%s: invocation = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A grove on PATH is reached by name, whatever ran it.
func TestInvocationIsPlainOutsideNodeModules(t *testing.T) {
	t.Setenv("npm_config_user_agent", "pnpm/11.22.0 npm/? node/v26.0.0 linux x64")
	if got := shell.InvocationFrom("/home/x/go/bin/grove"); got != "grove" {
		t.Errorf("invocation = %q, want %q", got, "grove")
	}
}
