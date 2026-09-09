package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain lets the test binary stand in for the grove binary, which is what
// autostart re-executes. Without it, os.Executable() under go test points at
// the test binary and the spawned "daemon" is parsed as test flags.
func TestMain(m *testing.M) {
	if os.Getenv("GROVE_TEST_RUN_CLI") == "1" {
		os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

func exercise(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, err bytes.Buffer
	code = run(args, &out, &err)
	return code, out.String(), err.String()
}

func TestVersionFlag(t *testing.T) {
	// pflag reads a single dash as shorthand, so "-version" is no longer the
	// same as "--version" the way it was under the flag package.
	for _, arg := range []string{"-v", "--version"} {
		code, stdout, stderr := exercise(t, arg)
		if code != 0 {
			t.Errorf("%s: exit = %d, want 0 (stderr: %s)", arg, code, stderr)
		}
		if !strings.HasPrefix(stdout, "grove ") {
			t.Errorf("%s: stdout = %q, want a \"grove \" prefix", arg, stdout)
		}
		if stderr != "" {
			t.Errorf("%s: stderr = %q, want empty", arg, stderr)
		}
	}
}

func TestNoArgumentsPrintsHelp(t *testing.T) {
	code, stdout, _ := exercise(t)
	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("stdout does not look like help:\n%s", stdout)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	for name, args := range map[string][]string{
		"unknown flag":    {"--nope"},
		"unknown command": {"bogus"},
		"extra argument":  {"context", "extra"},
	} {
		t.Run(name, func(t *testing.T) {
			code, _, stderr := exercise(t, args...)
			if code != 2 {
				t.Errorf("exit = %d, want 2", code)
			}
			if !strings.Contains(stderr, "grove --help") {
				t.Errorf("stderr does not point at help:\n%s", stderr)
			}
		})
	}
}

func TestContextJSONInAWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app1")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"worktree", "add", "-q", "-b", "feat1", filepath.Join(base, "feat1")},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	t.Chdir(filepath.Join(base, "feat1"))

	code, stdout, stderr := exercise(t, "context", "--json")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	var got struct {
		Slug    string `json:"slug"`
		Host    string `json:"host"`
		Variant string `json:"variant"`
		IsMain  bool   `json:"is_main"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if got.Slug != "app1-feat1" {
		t.Errorf("slug = %q, want app1-feat1", got.Slug)
	}
	if got.Host != "app1-feat1."+defaultDomain {
		t.Errorf("host = %q", got.Host)
	}
	if got.Variant != "feat1" || got.IsMain {
		t.Errorf("variant = %q, is_main = %v", got.Variant, got.IsMain)
	}
}

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
		if got := invocationFrom("/x/node_modules/@grove-sh/cli-linux-x64/bin/grove"); got != tc.want {
			t.Errorf("%s: invocation = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A grove on PATH is reached by name, whatever ran it.
func TestInvocationIsPlainOutsideNodeModules(t *testing.T) {
	t.Setenv("npm_config_user_agent", "pnpm/11.22.0 npm/? node/v26.0.0 linux x64")
	if got := invocationFrom("/home/x/go/bin/grove"); got != "grove" {
		t.Errorf("invocation = %q, want %q", got, "grove")
	}
}
