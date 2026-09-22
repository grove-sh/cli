package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
)

// The daemon reads the file once at startup, so a record has to be on disk
// before it opens rather than written underneath it.
func seedRecord(t *testing.T, stateDir string, ports map[string]map[string]int, worktrees map[string]string) {
	t.Helper()
	body, err := json.Marshal(struct {
		Ports     map[string]map[string]int `json:"ports"`
		Worktrees map[string]string         `json:"worktrees,omitempty"`
	}{Ports: ports, Worktrees: worktrees})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stateDir, "ports.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readRecord(t *testing.T, stateDir string) map[string]map[string]int {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(stateDir, "ports.json"))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Ports map[string]map[string]int `json:"ports"`
	}
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	return stored.Ports
}

func TestForgetDropsAContextWhoseWorktreeIsGone(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "deleted")
	socket, state := recordingDaemonSeeded(t,
		map[string]map[string]int{"app1": {"db": 20040, "api": 20041}},
		map[string]string{"app1": gone})

	code, stdout, stderr := exercise(t, "forget", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "forgot") || !strings.Contains(stdout, "app1") {
		t.Errorf("nothing was said about app1:\n%s", stdout)
	}
	if _, held := readRecord(t, state)["app1"]; held {
		t.Error("app1 is still in the file")
	}
}

// The sweep is for worktrees that are gone. One that is merely not running is
// the ordinary case and has to survive it.
func TestForgetLeavesAContextWhoseWorktreeIsStillThere(t *testing.T) {
	here := t.TempDir()
	socket, state := recordingDaemonSeeded(t,
		map[string]map[string]int{"app1": {"db": 20040}},
		map[string]string{"app1": here})

	code, stdout, stderr := exercise(t, "forget", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "nothing to forget") {
		t.Errorf("stdout = %q, want it to say there was nothing to do", stdout)
	}
	if _, held := readRecord(t, state)["app1"]; !held {
		t.Error("a worktree that is still there lost its record")
	}
}

// Dropping the record of a stack that is up is the thing the record exists to
// prevent, so a port that answers keeps it even when the worktree is gone.
func TestForgetKeepsARecordWhosePortAnswers(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port, err := strconv.Atoi(strings.TrimPrefix(listener.Addr().String(), "127.0.0.1:"))
	if err != nil {
		t.Fatal(err)
	}

	socket, state := recordingDaemonSeeded(t,
		map[string]map[string]int{"app1": {"db": port}},
		map[string]string{"app1": filepath.Join(t.TempDir(), "deleted")})

	code, stdout, stderr := exercise(t, "forget", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "kept") || !strings.Contains(stdout, strconv.Itoa(port)) {
		t.Errorf("the kept record does not say why:\n%s", stdout)
	}
	if _, held := readRecord(t, state)["app1"]; !held {
		t.Error("a record with something answering on its port was dropped")
	}
}

// A record predating the worktree key has no path to look for, and guessing
// about it is how a sweep drops a context whose stack is merely stopped.
func TestForgetLeavesARecordWithNoWorktreeAlone(t *testing.T) {
	socket, state := recordingDaemonSeeded(t,
		map[string]map[string]int{"app1": {"db": 20040}}, nil)

	if code, _, stderr := exercise(t, "forget", "--socket", socket); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, held := readRecord(t, state)["app1"]; !held {
		t.Error("a record with no worktree was swept")
	}
}

// Naming one is the way past the worktree check, for a record whose path is
// unknown or whose worktree is still sitting there.
func TestForgetTakesANamedContextWhateverItsWorktree(t *testing.T) {
	here := t.TempDir()
	socket, state := recordingDaemonSeeded(t,
		map[string]map[string]int{"app1": {"db": 20040}},
		map[string]string{"app1": here})

	if code, _, stderr := exercise(t, "forget", "app1", "--socket", socket); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if _, held := readRecord(t, state)["app1"]; held {
		t.Error("a named context kept its record")
	}
}

func TestForgetNamesAContextItHasNoRecordFor(t *testing.T) {
	socket, _ := recordingDaemonSeeded(t, map[string]map[string]int{"app1": {"db": 20040}}, nil)

	code, _, stderr := exercise(t, "forget", "app2", "--socket", socket)
	if code == 0 {
		t.Fatal("forgetting a context with no record succeeded")
	}
	if !strings.Contains(stderr, "app2") {
		t.Errorf("stderr = %q, and does not name what was not found", stderr)
	}
}

// Seeds the file before the daemon starts, since that is when it is read.
func recordingDaemonSeeded(t *testing.T, ports map[string]map[string]int, worktrees map[string]string) (socket, stateDir string) {
	t.Helper()
	stateDir = t.TempDir()
	if _, err := ca.OpenOrCreate(stateDir); err != nil {
		t.Fatal(err)
	}
	seedRecord(t, stateDir, ports, worktrees)

	server, err := daemon.New(daemon.Config{Domain: defaultDomain, CADir: stateDir})
	if err != nil {
		t.Fatal(err)
	}
	control, err := daemon.Listen(filepath.Join(socketDir(t), "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	https, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- server.Serve(control, https, nil) }()
	t.Cleanup(func() {
		server.Shutdown()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return control.Addr().String(), stateDir
}

// A lease with nothing listening yet is the case the port probe cannot see:
// grove hold on a stack that was never started. It has to read as kept rather
// than abort the sweep, which is what the help text promises.
func TestForgetKeepsAContextHoldingALeaseWithNoListener(t *testing.T) {
	socket, state := recordingDaemonSeeded(t, nil, nil)

	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "hold", "--socket", socket); code != 0 {
		t.Fatalf("hold: exit = %d: %s", code, stderr)
	}
	// Deleted out from under the lease, which is the only way a held context
	// has a worktree that is gone: taking the lease records where it was.
	t.Chdir(t.TempDir())
	if err := os.RemoveAll(repo); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := exercise(t, "forget", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "kept") || !strings.Contains(stdout, "lease") {
		t.Errorf("a leased context was not reported as kept:\n%s", stdout)
	}
	if _, held := readRecord(t, state)["app1"]; !held {
		t.Error("a leased context lost its record")
	}
}

// Naming several, one of which is in use, does the rest and says so. It used
// to abort on the one in use, after writing out the names before it, so the
// report claimed nothing went when something had.
func TestForgetDoesTheRestWhenOneNamedContextIsKept(t *testing.T) {
	socket, state := recordingDaemonSeeded(t,
		map[string]map[string]int{"app1": {"db": 20040}, "app2": {"db": 20041}}, nil)

	repo := tempRepo(t, "app2")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "hold", "--socket", socket); code != 0 {
		t.Fatalf("hold: exit = %d: %s", code, stderr)
	}

	code, stdout, stderr := exercise(t, "forget", "app1", "app2", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "forgot") || !strings.Contains(stdout, "app1") {
		t.Errorf("app1 was not reported as forgotten:\n%s", stdout)
	}
	record := readRecord(t, state)
	if _, held := record["app1"]; held {
		t.Error("app1 kept its record")
	}
	if _, held := record["app2"]; !held {
		t.Error("app2 holds a lease and lost its record anyway")
	}
}
