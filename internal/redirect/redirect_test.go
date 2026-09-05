package redirect_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/redirect"
)

// The stock file, which is what nearly every Mac has.
const apple = `#
# com.apple anchor point
#
scrub-anchor "com.apple/*"
nat-anchor "com.apple/*"
rdr-anchor "com.apple/*"
dummynet-anchor "com.apple/*"
anchor "com.apple/*"
load anchor "com.apple" from "/etc/pf.anchors/com.apple"
`

// pf reads translation rules before filter rules and rejects a file that mixes
// the order, so where the line lands is the whole game. Appending it is a parse
// error, which is a worse failure than it sounds: pfctl refuses the file, and
// the machine keeps whatever rules it already had.
func TestConfPutsTheReferenceBeforeTheFilterAnchor(t *testing.T) {
	out, changed, err := redirect.Conf(apple)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Error("a stock pf.conf needs the reference added")
	}

	lines := strings.Split(out, "\n")
	var ours, filter int
	for i, line := range lines {
		switch strings.TrimSpace(line) {
		case `rdr-anchor "grove"`:
			ours = i
		case `anchor "com.apple/*"`:
			filter = i
		}
	}
	if ours == 0 {
		t.Fatalf("no reference in:\n%s", out)
	}
	if ours > filter {
		t.Errorf("the reference is after the filter anchor, which pf will not parse:\n%s", out)
	}
	if !strings.Contains(out, `load anchor "grove" from "/etc/pf.anchors/grove"`) {
		t.Errorf("nothing loads the anchor:\n%s", out)
	}
}

// Apple's rules are the machine's, not grove's.
func TestConfKeepsEveryLineItWasGiven(t *testing.T) {
	out, _, err := redirect.Conf(apple)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(apple), "\n") {
		if !strings.Contains(out, line) {
			t.Errorf("dropped %q", line)
		}
	}
}

// Install is a thing people re-run, and a second pass must not stack up a
// second reference.
func TestConfIsIdempotent(t *testing.T) {
	once, _, err := redirect.Conf(apple)
	if err != nil {
		t.Fatal(err)
	}
	twice, changed, err := redirect.Conf(once)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a file that already refers to the anchor was reported as changed")
	}
	if twice != once {
		t.Errorf("running twice differs from running once:\n%s", twice)
	}
	if got := strings.Count(twice, `rdr-anchor "grove"`); got != 1 {
		t.Errorf("the reference appears %d times", got)
	}
}

// A pf.conf someone has already made their own is not a file to guess at.
func TestConfRefusesAFileItDoesNotRecognise(t *testing.T) {
	_, _, err := redirect.Conf("# a firewall someone else configured\nblock all\n")

	if !errors.Is(err, redirect.ErrUnknownConf) {
		t.Errorf("err = %v, want ErrUnknownConf", err)
	}
}

// The port is the one the daemon can actually bind, and has to stay clear of
// the range leases come from.
func TestAnchorSendsPortToTheDaemon(t *testing.T) {
	rule := redirect.Anchor(redirect.Port)

	if !strings.Contains(rule, "port = 443 ->") {
		t.Errorf("the rule does not redirect 443: %q", rule)
	}
	if !strings.Contains(rule, "127.0.0.1 port 10443") {
		t.Errorf("the rule does not name the daemon's port: %q", rule)
	}
	if redirect.Port >= 20000 && redirect.Port <= 20999 {
		t.Errorf("port %d is inside the lease range, so it could be handed out", redirect.Port)
	}
}

// The privileged step is a copy, so the files being copied have to exist, be
// readable, and be the ones the advice names.
func TestStageWritesBothFilesWhereItSaysItDid(t *testing.T) {
	dir := t.TempDir()
	merged, _, err := redirect.Conf(apple)
	if err != nil {
		t.Fatal(err)
	}

	staged, err := redirect.Stage(dir, merged)
	if err != nil {
		t.Fatal(err)
	}

	for path, want := range map[string]string{
		staged.Anchor: redirect.Anchor(redirect.Port),
		staged.Conf:   merged,
		staged.Plist:  redirect.Plist(),
	} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(body) != want {
			t.Errorf("%s is not what it should carry:\n%s", path, body)
		}
	}
	// Root reads these, so they cannot be private to the user who wrote them.
	for _, path := range []string{staged.Anchor, staged.Conf, staged.Plist} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o044 == 0 {
			t.Errorf("%s is unreadable to anyone else: %v", path, info.Mode())
		}
	}
}

// launchd is strict about plists, and the job has to both enable pf and put the
// rules back, since macOS loads pf.conf at boot with pf switched off.
func TestPlistEnablesAndLoadsAtBoot(t *testing.T) {
	body := redirect.Plist()

	for _, want := range []string{
		"<key>Label</key>",
		"<string>" + redirect.PlistLabel + "</string>",
		"<string>/sbin/pfctl</string>",
		"<string>-E</string>",
		"<string>" + redirect.ConfPath + "</string>",
		"<key>RunAtLoad</key>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the plist is missing %q:\n%s", want, body)
		}
	}
	// -e fails when pf is already on, which would log a failed job every boot.
	if strings.Contains(body, "<string>-e</string>") {
		t.Error("the job uses -e, which fails when pf is already enabled")
	}
}
