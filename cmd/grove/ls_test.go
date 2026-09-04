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

// A route nothing answers on reads claimed once grove has handed it a port,
// which is what a stopped stack looks like: the port is still held. A route
// with no lease at all is a different thing, and reads idle.
func TestLsTellsAClaimedRouteFromAnIdleOne(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	writeConfig(t, repo, "[routes.web]\n\n[routes.admin]\ndetached = true\n")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	_, stdout, _ := exercise(t, "ls", "--socket", socket)

	if !lineFor(stdout, "admin", "claimed") {
		t.Errorf("the synced route is not held:\n%s", stdout)
	}
	if !lineFor(stdout, "web", "idle") {
		t.Errorf("the unheld route is not idle:\n%s", stdout)
	}
}

// Nothing holds an unleased port, so its number is a guess at where allocation
// would put it. A route in the same state still earns its row, because the URL
// is knowable either way.
func TestLsLeavesOutAPortNothingHolds(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	_, stdout, _ := exercise(t, "ls", "--socket", socket)

	if lineFor(stdout, "db", "") {
		t.Errorf("a port nobody holds was listed:\n%s", stdout)
	}
	if !lineFor(stdout, "web", "idle") {
		t.Errorf("the route went missing with it:\n%s", stdout)
	}
}

// A held port nothing answers on is exactly what a stopped stack looks like,
// and the only way to tell it apart from a port nobody has taken. Hiding it is
// what made a colliding allocation invisible.
func TestLsShowsAPortItIsHolding(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	_, stdout, _ := exercise(t, "ls", "--socket", socket)

	if !lineFor(stdout, "db", "claimed") {
		t.Errorf("the port grove handed out is not listed as claimed:\n%s", stdout)
	}
	// The port it was handed is the one to act on, so it has to be the real
	// one rather than the prediction.
	if got := portOf(t, stdout, "db"); got != portOf(t, mustAll(t, socket), "app1:db") {
		t.Errorf("ls shows %d, not the port the daemon handed out", got)
	}
}

func mustAll(t *testing.T, socket string) string {
	t.Helper()
	code, stdout, stderr := exercise(t, "ls", "--socket", socket, "--all")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	return stdout
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

// Whether anything answers is the difference between a stack that is up and
// one that was stopped without releasing its ports.
func TestLsTellsAClaimedPortFromARunningOne(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	_, held, _ := exercise(t, "ls", "--socket", socket, "--all")
	port := portOf(t, held, "app1:db")

	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skipf("could not stand on port %d: %v", port, err)
	}
	_, serving, _ := exercise(t, "ls", "--socket", socket)
	ln.Close()
	_, stopped, _ := exercise(t, "ls", "--socket", socket)

	if !lineFor(serving, "db", "running") {
		t.Errorf("with something listening, db is not running:\n%s", serving)
	}
	// A bare port has no hostname, so claiming one would be a lie.
	if lineFor(serving, "db", "https://") {
		t.Errorf("a bare port was given a URL:\n%s", serving)
	}
	// The lease outlives whatever was listening, and so does the row.
	if !lineFor(stopped, "db", "claimed") {
		t.Errorf("db went quiet and lost its row with it:\n%s", stopped)
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

// lineFor reports whether a row for route says want. An empty want asks only
// whether the row is there at all, which is now half of what ls decides.
func lineFor(table, route, want string) bool {
	for _, line := range strings.Split(table, "\n") {
		if strings.HasPrefix(line, route+" ") {
			return strings.Contains(line, want)
		}
	}
	return false
}
