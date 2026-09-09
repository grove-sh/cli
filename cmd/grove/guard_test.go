package main

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/daemon"
)

// A stopped stack keeps its ports, so a detached lease nothing answers on is
// the one case where the route was already going nowhere.
func TestStopAllowsDetachedPortsThatAnswerNothing(t *testing.T) {
	held := []daemon.Live{
		{Slug: "app1", Service: "db", Worktree: "/w/app1", Port: freePort(t), Detached: true},
		{Slug: "app1", Service: "api", Worktree: "/w/app1", Port: freePort(t), Detached: true},
	}

	if refusal := refuseToStop(held); refusal != "" {
		t.Errorf("stop refused over ports nothing is using:\n%s", refusal)
	}
}

// What 'grove ls' calls running: a supabase stack answers on its ports long
// after the command that started it returned, and its URLs work until grove
// stops. Only the recovery is cheaper than an attached lease, not the loss.
func TestStopRefusesWhileADetachedPortIsAnswering(t *testing.T) {
	held := []daemon.Live{
		{Slug: "app1", Service: "db", Worktree: "/w/app1", Port: listeningPort(t), Detached: true},
	}

	refusal := refuseToStop(held)

	if refusal == "" {
		t.Fatal("stop dropped the route to a live service without saying so")
	}
	for _, want := range []string{"app1", "/w/app1", "hold", "--force"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, refusal)
		}
	}
	if strings.Contains(refusal, "run again") {
		t.Errorf("refusal asks for a rerun of a command that is not running:\n%s", refusal)
	}
}

// One daemon serves the machine, so the refusal has to name whose work is at
// stake rather than only that some is.
func TestStopRefusesWhileACommandIsStillRunning(t *testing.T) {
	held := []daemon.Live{
		{Slug: "quiet", Service: "db", Worktree: "/w/quiet", Port: freePort(t), Detached: true},
		{Slug: "other", Service: "web", Worktree: "/w/other", Port: listeningPort(t)},
	}

	refusal := refuseToStop(held)

	if refusal == "" {
		t.Fatal("stop dropped a running command's route without saying so")
	}
	for _, want := range []string{"other", "/w/other", "run again", "--force"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, refusal)
		}
	}
	if strings.Contains(refusal, "quiet") {
		t.Errorf("refusal blames a port nothing answers on:\n%s", refusal)
	}
}

func TestUninstallRefusesWhileSomethingIsBeingServed(t *testing.T) {
	held := []daemon.Live{{Slug: "app1", Worktree: "/w/app1", Port: listeningPort(t)}}

	refusal := refuseToUninstall(held)

	if refusal == "" {
		t.Fatal("uninstall untrusted a root under a live route silently")
	}
	for _, want := range []string{"app1", "stops grove", "--force"} {
		if !strings.Contains(refusal, want) {
			t.Errorf("refusal does not mention %q:\n%s", want, refusal)
		}
	}
}

// The refusal is a paragraph, and run() prefixes a returned error with "grove: "
// on its first line only. Exit stays non-zero so a script still sees the stop
// did not happen.
func TestStopRefusesWithoutWearingThePrefix(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(tempRepo(t, "app1"))
	if code, _, stderr := exercise(t, "hold", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}
	held := whatItHolds(socket)
	if len(held) == 0 {
		t.Fatal("nothing was held, so there is nothing to answer on")
	}
	// Whatever the hash handed out, answered so the guard sees a live service.
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(held[0].Port)))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	code, stdout, stderr := exercise(t, "stop", "--socket", socket)

	if code == 0 {
		t.Fatalf("stop went ahead: %s%s", stdout, stderr)
	}
	if strings.Contains(stderr, "grove:") {
		t.Errorf("the refusal wears a prefix:\n%s", stderr)
	}
	if !strings.HasPrefix(stderr, "Something is still serving") {
		t.Errorf("stop said something else:\n%s", stderr)
	}
	if !daemonAnswers(socket) {
		t.Error("stop refused and stopped anyway")
	}
}

// A real listener, since the guard asks the port itself rather than the daemon.
func listeningPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// Uninstall reads as final, so it is: a trusted root and a bound proxy are both
// things it borrowed.
func TestUninstallStopsGrove(t *testing.T) {
	dir := t.TempDir()
	if _, _, stderr := exercise(t, "install", "--state-dir", dir, "--trust=false"); stderr != "" {
		t.Fatal(stderr)
	}
	socket := startDaemon(t)

	code, stdout, stderr := exercise(t, "uninstall", "--state-dir", dir, "--trust=false", "--socket", socket)

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "STOPPED") {
		t.Errorf("uninstall left grove running: %q", stdout)
	}
	if daemonAnswers(socket) {
		t.Error("uninstall reported a stop that did not happen")
	}
}

func daemonAnswers(socket string) bool {
	client, err := daemon.Dial(socket)
	if err != nil {
		return false
	}
	client.Close()
	return true
}

// Nothing started, so the word that means something did must not appear.
func TestStartOnALiveDaemonDoesNotClaimToHaveStartedOne(t *testing.T) {
	socket := startDaemon(t)

	code, stdout, stderr := exercise(t, "start", "--socket", socket)

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "already running") {
		t.Errorf("start said %q", stdout)
	}
	for _, forbidden := range []string{"Listening on", "STARTED"} {
		if strings.Contains(stdout, forbidden) {
			t.Errorf("start said %q, which claims %q", stdout, forbidden)
		}
	}
}

// Restart drops an attached lease and does not refuse first, so the count is
// the only warning its command will not come back on its own.
func TestRestartSaysWhichLeasesWillNotComeBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		ated int
		want string
	}{
		{"nothing attached", 0, ""},
		{"one", 1, "Dropped 1 lease whose command needs running again"},
		{"several", 3, "Dropped 3 leases whose commands need running again"},
	} {
		before := []daemon.Live{
			{Slug: "app1", Service: "db", Worktree: "/w/app1", Port: 20402, Detached: true},
		}
		for i := 0; i < tc.ated; i++ {
			before = append(before, daemon.Live{Slug: "app1", Service: "web", Worktree: "/w/app1"})
		}

		if got := droppedTally(before); got != tc.want {
			t.Errorf("%s: droppedTally = %q, want %q", tc.name, got, tc.want)
		}
	}
}
