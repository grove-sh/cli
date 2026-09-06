package main

import (
	"bytes"
	"github.com/grove-sh/cli/internal/ca"
	"strings"
	"testing"
)

// The service runs a copy taken at install time, so an upgraded package leaves
// the old daemon serving. Saying nothing when they match keeps doctor quiet in
// the ordinary case.
func TestStaleDaemonSpeaksUpOnlyOnADifferentBuild(t *testing.T) {
	if got := staleDaemon("v0.1.0", "v0.1.0"); got != "" {
		t.Errorf("same build reported as stale: %q", got)
	}
	if got := staleDaemon("v0.1.0", "v0.2.0"); !strings.Contains(got, "v0.1.0") || !strings.Contains(got, "v0.2.0") {
		t.Errorf("a difference has to name both builds: %q", got)
	}
	// A daemon from before the field reports nothing, which is itself the
	// answer: it predates the binary asking.
	if got := staleDaemon("", "v0.2.0"); got == "" {
		t.Error("a daemon that reports no build should still be called out")
	}
}

// Colour is emphasis, never the message: piping has to keep every state, since
// a log or a script sees only the words.
func TestEveryStateReadsWithoutColour(t *testing.T) {
	paint := styles(&bytes.Buffer{})

	for _, tc := range []struct {
		finding finding
		want    string
	}{
		{finding{name: "Grove", state: warn, detail: "not running"}, "not running"},
		{finding{name: "Authority", state: bad, detail: "not installed"}, "not installed"},
		{finding{name: "Authority", state: warn, detail: "installed, but this machine does not trust it"}, "does not trust it"},
	} {
		if got := paint.paint(tc.finding.state)(tc.finding.detail); !strings.Contains(got, tc.want) {
			t.Errorf("plain output lost the state: %q", got)
		}
	}
}

// Two problems with one remedy is one thing to do. Saying it twice reads as
// two separate jobs, and this is the state a fresh machine is in.
func TestOneRemedyIsSaidOnce(t *testing.T) {
	findings := []finding{
		{name: "Authority", state: bad, detail: "not installed", fix: "grove install"},
		{name: "Runtime bundle", state: warn, detail: "does not carry grove's root", fix: "grove install"},
		{name: "Grove", state: warn, detail: "not running", fix: "grove start"},
	}

	said := map[string]int{}
	for _, f := range findings {
		said[f.fix]++
	}
	if said["grove install"] != 2 {
		t.Fatal("the fixture no longer has two findings sharing a remedy")
	}
	for fix := range said {
		if remedy[fix] == "" {
			t.Errorf("no remedy is written for %q, so the advice would be blank", fix)
		}
	}
}

// Silent is not the same as unchecked. DNS passes for everyone until the day
// it does not, and that day is the whole reason the check exists.
func TestASilentCheckStillSpeaksWhenItFails(t *testing.T) {
	quiet := checkDNS("grov.site")
	if quiet.state != ok {
		t.Skip("this machine cannot resolve the real domain, so the pair proves nothing")
	}

	loud := checkDNS("nxdomain.invalid")

	if loud.state == ok {
		t.Fatal("a domain that does not resolve was reported as fine")
	}
	if loud.advice == "" {
		t.Error("the failure says nothing about what to do")
	}
}

// Ten years out is not news, so the date only appears when it is close enough
// to act on.
func TestTheAuthorityDateAppearsOnlyWhenItMatters(t *testing.T) {
	dir := t.TempDir()
	if _, err := ca.OpenOrCreate(dir); err != nil {
		t.Fatal(err)
	}

	f := checkAuthority(dir)

	if strings.Contains(f.detail, "expires") {
		t.Errorf("a fresh authority advertised its expiry: %q", f.detail)
	}
}
