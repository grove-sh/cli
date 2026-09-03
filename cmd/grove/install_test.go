package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/trust"
)

// Every case passes --trust=false and redirects the unit directory. A test run
// must not touch the system trust store, prompt for a password, or register
// anything with a service manager.
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

	// Which file it is depends on the platform. What has to hold everywhere is
	// that a runtime pointed at it trusts grove.
	if trust.SystemBundle() == "" {
		return
	}
	if lines[1] == "" {
		t.Fatal("REQUESTS_CA_BUNDLE was not set")
	}
	bundle, err := os.ReadFile(lines[1])
	if err != nil {
		t.Fatal(err)
	}
	rootPEM, err := os.ReadFile(filepath.Join(state, trust.RootFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bundle), string(rootPEM)) {
		t.Errorf("%s does not carry grove's root", lines[1])
	}
}

// The macOS shape, reproduced anywhere: a system bundle file exists and the
// trust install does not write to it, so grove has to merge its own or those
// runtimes never trust it.
func TestInstallMergesABundleWhenTheSystemFileWillNotDo(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(dir, "units"))
	system := filepath.Join(t.TempDir(), "system-roots.pem")
	if err := os.WriteFile(system, []byte("-----BEGIN CERTIFICATE-----\nsystem\n-----END CERTIFICATE-----\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_SYSTEM_BUNDLE", system)

	code, stdout, stderr := exercise(t, "install", "--state-dir", dir, "--trust=false", "--service=false")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	merged := filepath.Join(dir, trust.BundleFile)
	if _, err := os.Stat(merged); err != nil {
		t.Fatalf("no merged bundle: %v", err)
	}
	if !strings.Contains(stdout, "merged because") {
		t.Errorf("output does not explain the merge:\n%s", stdout)
	}
	if !strings.Contains(stdout, merged) {
		t.Errorf("output does not name the bundle:\n%s", stdout)
	}
}

// And the Linux shape: the trust install rewrote the system file, so any copy
// grove made before is deleted rather than left to rot.
func TestInstallDropsItsCopyOnceTheSystemFileCarriesTheRoot(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(dir, "units"))
	system := filepath.Join(t.TempDir(), "system-roots.pem")
	if err := os.WriteFile(system, []byte("placeholder\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_SYSTEM_BUNDLE", system)

	// First install has nothing to work with, so it merges a copy.
	exercise(t, "install", "--state-dir", dir, "--trust=false", "--service=false")
	merged := filepath.Join(dir, trust.BundleFile)
	if _, err := os.Stat(merged); err != nil {
		t.Fatalf("expected a merged bundle first: %v", err)
	}

	// Now the system file carries the root, as update-ca-trust leaves it.
	rootPEM, err := os.ReadFile(filepath.Join(dir, trust.RootFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(system, append([]byte("placeholder\n"), rootPEM...), 0o644); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exercise(t, "install", "--state-dir", dir, "--trust=false", "--service=false")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	if _, err := os.Stat(merged); !os.IsNotExist(err) {
		t.Errorf("grove's copy survived: %v", err)
	}
	if !strings.Contains(stdout, "which carries this root") {
		t.Errorf("output does not name the system file:\n%s", stdout)
	}
}
