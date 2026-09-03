package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/trust"
)

// Every case passes --trust=false. Installing for real would touch the system
// trust store and ask for a password.
// Every case redirects the unit directory, which puts internal/service in
// unmanaged mode. A test run must not enable anything on the machine.
func TestInstallCreatesTheAuthority(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(dir, "units"))

	code, stdout, stderr := exercise(t, "install", "--state-dir", dir, "--trust=false")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	key, err := os.Stat(filepath.Join(dir, "root.key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := key.Mode().Perm(); perm != 0o600 {
		t.Errorf("root.key mode = %#o, want 0600", perm)
	}
	state, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := state.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir mode = %#o, want 0700", perm)
	}
	if !strings.Contains(stdout, filepath.Join(dir, "root.crt")) {
		t.Errorf("output does not name the authority:\n%s", stdout)
	}
}

func TestInstallTightensALooseStateDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(dir, "units"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exercise(t, "install", "--state-dir", dir, "--trust=false")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("state dir mode = %#o, want 0700", perm)
	}
	if !strings.Contains(stdout, "tightened") {
		t.Errorf("output does not mention the change:\n%s", stdout)
	}
}

// Re-running must not mint a second root, or trust stores fill up with stale
// authorities.
func TestInstallReusesTheRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(dir, "units"))

	exercise(t, "install", "--state-dir", dir, "--trust=false")
	first, err := os.ReadFile(filepath.Join(dir, "root.crt"))
	if err != nil {
		t.Fatal(err)
	}
	exercise(t, "install", "--state-dir", dir, "--trust=false")
	second, err := os.ReadFile(filepath.Join(dir, "root.crt"))
	if err != nil {
		t.Fatal(err)
	}

	if string(first) != string(second) {
		t.Error("the second install generated a new root")
	}
}

func TestExecPassesTheCAEnvironmentToTheChild(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(state, "units"))
	if _, _, stderr := exercise(t, "install", "--state-dir", state, "--trust=false"); stderr != "" {
		t.Fatal(stderr)
	}
	t.Setenv("GROVE_STATE_DIR", state)

	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--",
		"sh", "-c", `printf '%s\n%s\n' "$NODE_EXTRA_CA_CERTS" "$REQUESTS_CA_BUNDLE" > ca.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "ca.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if lines[0] != filepath.Join(state, trust.RootFile) {
		t.Errorf("NODE_EXTRA_CA_CERTS = %q", lines[0])
	}
	if trust.SystemBundle() != "" && lines[1] != trust.SystemBundle() {
		t.Errorf("REQUESTS_CA_BUNDLE = %q, want the system bundle", lines[1])
	}
}
