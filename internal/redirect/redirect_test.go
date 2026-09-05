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

// Both ports the daemon can actually bind, and both have to stay clear of the
// range leases come from, or a redirect target could be handed out as someone's
// port.
func TestAnchorSendsBothPortsToTheDaemon(t *testing.T) {
	rules := redirect.Anchor(redirect.Port, redirect.HTTPPort)

	for _, want := range []string{
		"port = 443 -> 127.0.0.1 port 10443",
		"port = 80 -> 127.0.0.1 port 10080",
	} {
		if !strings.Contains(rules, want) {
			t.Errorf("no rule for %q:\n%s", want, rules)
		}
	}
	for _, port := range []int{redirect.Port, redirect.HTTPPort} {
		if port >= 20000 && port <= 20999 {
			t.Errorf("port %d is inside the lease range, so it could be handed out", port)
		}
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
		staged.Anchor: redirect.Anchor(redirect.Port, redirect.HTTPPort),
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

// A report that says no without saying what would make it yes leaves the
// caller printing nothing useful. This ran only on macOS once, and only CI
// found it; it runs everywhere now.
func TestAccessAlwaysSaysWhatWouldMakeItYes(t *testing.T) {
	whole := redirect.State{Referenced: true, Anchor: true, Boot: true}
	for _, state := range []redirect.State{
		{},
		{Referenced: true},
		{Referenced: true, Anchor: true},
		{Anchor: true, Boot: true},
		{Referenced: true, Boot: true},
		whole,
	} {
		allowed, detail, advice := redirect.Access(state)
		if detail == "" {
			t.Errorf("%+v: no detail", state)
		}
		if allowed != (state == whole) {
			t.Errorf("%+v: allowed = %v", state, allowed)
		}
		if !allowed && advice == "" {
			t.Errorf("%+v: says no without saying what to do", state)
		}
		if allowed && advice != "" {
			t.Errorf("%+v: nothing to advise, but advised anyway: %q", state, advice)
		}
	}
}

// Each missing piece has to name itself, or the report says "something is
// wrong" and leaves you to find out which.
func TestAccessNamesThePieceThatIsMissing(t *testing.T) {
	for _, tc := range []struct {
		state redirect.State
		want  string
	}{
		{redirect.State{Anchor: true, Boot: true}, "nothing redirects it yet"},
		{redirect.State{Referenced: true, Boot: true}, redirect.AnchorPath},
		{redirect.State{Referenced: true, Anchor: true}, "after a reboot"},
	} {
		_, detail, _ := redirect.Access(tc.state)
		if !strings.Contains(detail, tc.want) {
			t.Errorf("%+v: detail does not mention %q: %s", tc.state, tc.want, detail)
		}
	}
}
