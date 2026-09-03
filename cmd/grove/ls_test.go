package main

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/identity"
	"github.com/grove-sh/cli/internal/lease"
)

// The URL of a route is knowable without anything running, which is the whole
// reason to list from config rather than from leases.
func TestLsListsIdleRoutesWithTheirURLs(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	code, stdout, stderr := exercise(t, "ls", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	if !strings.Contains(stdout, "https://app1."+defaultDomain) {
		t.Errorf("no URL for the idle route:\n%s", stdout)
	}
	if !strings.Contains(stdout, "idle") {
		t.Errorf("the idle route is not marked:\n%s", stdout)
	}
	// A bare port has no hostname, so claiming one would be a lie.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "db") && strings.Contains(line, "https://") {
			t.Errorf("a bare port was given a URL: %q", line)
		}
	}
}

// The predicted port has to be the one allocation actually hands out, or it is
// worse than showing nothing.
func TestLsPredictsThePortAllocationWouldGive(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	_, stdout, _ := exercise(t, "ls", "--socket", socket)

	want := lease.PredictPort(lease.PortRange{}, "app1", "web")
	if !strings.Contains(stdout, strconv.Itoa(want)) {
		t.Errorf("ls does not show the port allocation would give (%d):\n%s", want, stdout)
	}
}

// A detached port nothing answers on reads claimed, which is what a stopped
// stack looks like: grove handed the port out and is still holding it.
func TestLsTellsAClaimedPortFromAnIdleRoute(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	_, stdout, _ := exercise(t, "ls", "--socket", socket)

	var claimed, idle bool
	for _, line := range strings.Split(stdout, "\n") {
		switch {
		case strings.HasPrefix(line, "db"):
			claimed = strings.Contains(line, "claimed")
		case strings.HasPrefix(line, "web"):
			idle = strings.Contains(line, "idle")
		}
	}
	if !claimed {
		t.Errorf("the synced port is not held:\n%s", stdout)
	}
	if !idle {
		t.Errorf("the unheld route is not idle:\n%s", stdout)
	}
}

// Listing routes is worth doing without a daemon, since the hostnames do not
// depend on one. Saying so keeps it from looking like everything is fine.
func TestLsWithoutADaemonStillListsRoutes(t *testing.T) {
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	code, stdout, stderr := exercise(t, "ls", "--socket", "/nonexistent/grove.sock")

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "https://app1."+defaultDomain) {
		t.Errorf("no routes listed:\n%s", stdout)
	}
	if !strings.Contains(stderr, "no daemon") {
		t.Errorf("stderr does not mention it: %q", stderr)
	}
}

func TestLsAllReportsEveryContext(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	exercise(t, "sync", "--socket", socket)

	_, stdout, _ := exercise(t, "ls", "--socket", socket, "--all")

	// The cross-context view is keyed by worktree, not by route name.
	if !strings.Contains(stdout, "WORKTREE") {
		t.Errorf("--all did not switch views:\n%s", stdout)
	}
	if !strings.Contains(stdout, repo) {
		t.Errorf("--all does not name the worktree:\n%s", stdout)
	}
}

func TestLsOutsideAProjectFallsBackToLeases(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(t.TempDir())

	code, stdout, stderr := exercise(t, "ls", "--socket", socket)

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "ROUTE") {
		t.Errorf("listed routes with no project to read them from:\n%s", stdout)
	}
}

// A label the config overrides has to reach the URL, since that is the only
// place the hostname is assembled.
func TestLsUsesTheConfiguredLabel(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	writeConfig(t, repo, "[routes.admin]\nlabel = \"ops\"\n")
	t.Chdir(repo)

	_, stdout, _ := exercise(t, "ls", "--socket", socket)

	want := "https://" + identity.ComposeLabel("app1", "ops") + "." + defaultDomain
	if !strings.Contains(stdout, want) {
		t.Errorf("want %s in:\n%s", want, stdout)
	}
}

// A detached port is claimed until something answers on it, which is the
// difference between a stack that is running and one that was stopped without
// releasing its ports.
func TestLsReportsADetachedPortAsRunningOnlyWhenSomethingAnswers(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	_, before, _ := exercise(t, "ls", "--socket", socket)
	port := portOf(t, before, "db")

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("could not stand on port %d: %v", port, err)
	}
	_, after, _ := exercise(t, "ls", "--socket", socket)
	ln.Close()

	if !lineFor(before, "db", "claimed") {
		t.Errorf("with nothing listening, db is not claimed:\n%s", before)
	}
	if !lineFor(after, "db", "running") {
		t.Errorf("with something listening, db is not running:\n%s", after)
	}
}

func portOf(t *testing.T, table, route string) int {
	t.Helper()
	for _, line := range strings.Split(table, "\n") {
		if !strings.HasPrefix(line, route+" ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if port, err := strconv.Atoi(field); err == nil {
				return port
			}
		}
	}
	t.Fatalf("no port for %q in:\n%s", route, table)
	return 0
}

func lineFor(table, route, want string) bool {
	for _, line := range strings.Split(table, "\n") {
		if strings.HasPrefix(line, route+" ") {
			return strings.Contains(line, want)
		}
	}
	return false
}
