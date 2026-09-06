package service_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/service"
)

// launchd rejects a plist it cannot parse and says very little about why, so
// the parser gets a say here rather than on someone's machine.
func TestAgentIsAPlistLaunchdWouldAccept(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sh.grove.daemon.plist")
	if err := os.WriteFile(path, []byte(service.Agent("/usr/local/bin/grove", "127.0.0.1:10443")), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("plutil", "-lint", path).CombinedOutput()
	if err != nil {
		t.Fatalf("plutil rejected it: %v\n%s", err, out)
	}
}

// The agent has to name the binary and the port, and come back after a crash
// without coming back after a deliberate stop.
func TestAgentRunsTheDaemonAndRestartsOnlyOnFailure(t *testing.T) {
	plist := service.Agent("/opt/grove/bin/grove", "127.0.0.1:10443")

	for _, want := range []string{
		"<string>sh.grove.daemon</string>",
		"<string>/opt/grove/bin/grove</string>",
		"<string>daemon</string>",
		"<string>--listen</string>",
		"<string>127.0.0.1:10443</string>",
		"<key>RunAtLoad</key>",
		"<key>KeepAlive</key>",
		"<key>SuccessfulExit</key>",
	} {
		if !strings.Contains(plist, want) {
			t.Errorf("the agent is missing %q:\n%s", want, plist)
		}
	}
	// KeepAlive as a bare true would restart the daemon after grove stop,
	// which turns stopping it into a fight.
	if strings.Contains(plist, "<key>KeepAlive</key>\n\t<true/>") {
		t.Error("KeepAlive is unconditional, so a deliberate stop would be undone")
	}
}

// An agent that binds 443 would be an agent that cannot start, since a process
// running as you may not have it. pf carries 443 to the port it does bind.
func TestAgentDoesNotAskForAPrivilegedPort(t *testing.T) {
	plist := service.Agent("/usr/local/bin/grove", "127.0.0.1:10443")

	if strings.Contains(plist, "127.0.0.1:443") {
		t.Errorf("the agent asks for 443, which it will not get:\n%s", plist)
	}
}

// A redirected service directory has to keep grove away from launchd, which is
// what lets this run at all.
func TestInstallWritesWithoutTouchingLaunchd(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agents")
	t.Setenv("GROVE_SERVICE_DIR", dir)

	path, err := service.Install("/usr/local/bin/grove", "127.0.0.1:10443")
	if err != nil {
		t.Fatal(err)
	}

	if path != filepath.Join(dir, service.FileName) {
		t.Errorf("path = %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if state := service.Status(); state.Enabled || state.Active {
		t.Error("grove asked launchd about a service it was told not to register")
	}
}
