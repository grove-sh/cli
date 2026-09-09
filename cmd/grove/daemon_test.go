package main

import (
	"bytes"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/spf13/cobra"
	"path/filepath"
	"strings"
	"testing"
)

// Stopping is machine wide and reads as small, so it says what it dropped.
// Reading the table needs its own connection, since the one carrying the stop
// has room for a single request: doing both on one is a broken pipe.
func TestStopReportsWhatItDropped(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "hold", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	code, stdout, stderr := exercise(t, "stop", "--socket", socket)

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	for _, want := range []string{"Released", "lease", "context", "STOPPED"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stop said %q, which does not mention %q", stdout, want)
		}
	}
}

// Nothing held is the ordinary case for a daemon someone just started, and
// there is nothing to warn about.
func TestStopIsQuietWhenNothingIsHeld(t *testing.T) {
	socket := startDaemon(t)

	_, stdout, _ := exercise(t, "stop", "--socket", socket)

	if strings.Contains(stdout, "Released") {
		t.Errorf("stop reported losses it did not cause: %q", stdout)
	}
	if !strings.Contains(stdout, "STOPPED") {
		t.Errorf("stop did not say it stopped: %q", stdout)
	}
}

// Restarting used to leave every other project's hostnames unrouted until
// someone visited each one. The table is read a moment before it is dropped,
// and each worktree is then asked what it wants, so a project that changed in
// the meantime gets what it asks for now.
func TestRestoreHoldsEveryContextTheDaemonWasHolding(t *testing.T) {
	socket := startDaemon(t)
	first, second := tempRepo(t, "app1"), tempRepo(t, "app2")

	out, errs := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(errs)

	restoreContexts(cmd, socket, []daemon.Live{
		{Slug: "app1", Service: "db", Worktree: first, Detached: true},
		{Slug: "app2", Service: "db", Worktree: second, Detached: true},
		// An attached lease belongs to a command that is no longer connected,
		// and there is nothing to reconnect it to.
		{Slug: "app1", Service: "web", Worktree: first},
	})

	client, err := daemon.Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	held, err := client.List()
	if err != nil {
		t.Fatal(err)
	}

	holding := map[string]bool{}
	for _, lease := range held {
		holding[lease.Slug+":"+lease.Service] = true
	}
	for _, want := range []string{"app1:db", "app2:db"} {
		if !holding[want] {
			t.Errorf("%s did not come back:\n%s%s", want, out, errs)
		}
	}
	if holding["app1:web"] {
		t.Error("an attached lease was restored, but its command is gone")
	}
}

// A worktree that has been deleted since is not a reason to abandon the rest.
func TestRestoreCarriesOnPastAWorktreeThatIsGone(t *testing.T) {
	socket := startDaemon(t)
	alive := tempRepo(t, "app1")

	out, errs := &bytes.Buffer{}, &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(errs)

	restoreContexts(cmd, socket, []daemon.Live{
		{Slug: "gone", Service: "db", Worktree: filepath.Join(t.TempDir(), "deleted"), Detached: true},
		{Slug: "app1", Service: "db", Worktree: alive, Detached: true},
	})

	if !holds(t, socket, "app1", "db") {
		t.Errorf("the live context did not come back:\n%s%s", out, errs)
	}
	if !strings.Contains(errs.String(), "gone") {
		t.Errorf("nothing said which context could not: %q", errs)
	}
}

func holds(t *testing.T, socket, slug, service string) bool {
	t.Helper()

	client, err := daemon.Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	held, err := client.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, lease := range held {
		if lease.Slug == slug && lease.Service == service {
			return true
		}
	}
	return false
}
