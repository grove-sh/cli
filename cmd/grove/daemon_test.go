package main

import (
	"path/filepath"
	"strings"
	"testing"
)

// A redirected service directory is how grove stays away from the real service
// manager, and the whole reason a test can run this at all. Deferring to it
// there would restart nothing, since an unmanaged systemctl call is a no-op,
// and then wait fifteen seconds for a socket that is not coming.
func TestRestartIgnoresTheServiceWhenItIsNotManaged(t *testing.T) {
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(t.TempDir(), "units"))

	if serviceOwnsDaemon(defaultDaemonOptions()) {
		t.Error("grove would restart a service manager it has been told to stay away from")
	}
}

// Options the unit does not describe are a request for a different daemon, and
// restarting the unit would quietly serve the old ones instead.
func TestRestartSpawnsWhenAskedForSomethingTheUnitIsNot(t *testing.T) {
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(t.TempDir(), "units"))

	elsewhere := func(change func(*daemonOptions)) daemonOptions {
		opts := defaultDaemonOptions()
		change(&opts)
		return opts
	}
	for _, opts := range []daemonOptions{
		elsewhere(func(o *daemonOptions) { o.listen = "127.0.0.1:9999" }),
		elsewhere(func(o *daemonOptions) { o.socket = "/tmp/elsewhere.sock" }),
		elsewhere(func(o *daemonOptions) { o.domain = "example.test" }),
		elsewhere(func(o *daemonOptions) { o.caDir = "/tmp/elsewhere" }),
	} {
		if serviceOwnsDaemon(opts) {
			t.Errorf("%+v: restarting the unit would ignore what was asked for", opts)
		}
	}
}

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

	code, stdout, stderr := exercise(t, "daemon", "stop", "--socket", socket)

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	for _, want := range []string{"stopped", "lease", "context", "unrouted"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stop said %q, which does not mention %q", stdout, want)
		}
	}
}

// Nothing held is the ordinary case for a daemon someone just started, and
// there is nothing to warn about.
func TestStopIsQuietWhenNothingIsHeld(t *testing.T) {
	socket := startDaemon(t)

	_, stdout, _ := exercise(t, "daemon", "stop", "--socket", socket)

	if strings.Contains(stdout, "released") {
		t.Errorf("stop reported losses it did not cause: %q", stdout)
	}
}

// One question, where doctor answers six, and a non-zero exit when nothing
// answers so a script can ask without parsing.
func TestDaemonStatusAnswersOneQuestion(t *testing.T) {
	socket := startDaemon(t)

	code, stdout, _ := exercise(t, "daemon", "status", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	for _, want := range []string{"running on", "pid", "held"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status said %q, which does not mention %q", stdout, want)
		}
	}

	if code, _, _ := exercise(t, "daemon", "status", "--socket", "/nonexistent/grove.sock"); code == 0 {
		t.Error("status exited zero with no daemon running")
	}
}
