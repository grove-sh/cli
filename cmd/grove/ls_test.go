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

// A bare port is a number grove writes into an env var, so one that nothing is
// serving is noise in a table about what you can reach.
func TestLsLeavesOutAPortNothingIsServing(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	_, stdout, _ := exercise(t, "ls", "--socket", socket)

	if lineFor(stdout, "db", "") {
		t.Errorf("a claimed port nothing answers on was listed:\n%s", stdout)
	}
	if !lineFor(stdout, "web", "idle") {
		t.Errorf("the route went missing with it:\n%s", stdout)
	}
	// It is still held, which is what the machine-wide view is for.
	if _, all, _ := exercise(t, "ls", "--socket", socket, "--all"); !strings.Contains(all, "app1:db") {
		t.Errorf("--all does not show the port grove is holding:\n%s", all)
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

// Serving something on a held port is what brings it into the table, so the
// listing follows the stack coming up rather than the lease being taken.
func TestLsListsAPortOnceSomethingAnswersOnIt(t *testing.T) {
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
	if lineFor(stopped, "db", "") {
		t.Errorf("db outlived the thing serving it:\n%s", stopped)
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
