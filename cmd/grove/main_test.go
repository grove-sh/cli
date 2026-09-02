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
