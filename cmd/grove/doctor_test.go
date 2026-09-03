package main

import (
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
