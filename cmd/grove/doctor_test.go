package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/identity"
	"github.com/grove-sh/cli/internal/platform"
)

// A daemon outlives the command that started it, so an upgrade leaves the old
// build serving. Saying nothing when they match keeps doctor quiet in the
// ordinary case.
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

// The row is there while it is going well, since which worktree is the project
// is a choice. It is also the one thing here that can be wrong with nothing
// else noticing: a setting matching no worktree leaves every worktree with the
// suffix it was set to drop, and no error anywhere.
func TestMainWorktreeCheckReportsWhoTheProjectIs(t *testing.T) {
	base := t.TempDir()
	bare := filepath.Join(base, "app1.git")
	seed := filepath.Join(base, "seed")
	main := filepath.Join(base, "main")
	dev := filepath.Join(base, "dev")
	for _, args := range [][]string{
		{"-C", mkdir(t, seed), "init", "-q", "-b", "main"},
		{"-C", seed, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
		{"clone", "-q", "--bare", seed, bare},
		{"-C", bare, "worktree", "add", "-q", main, "main"},
		{"-C", bare, "worktree", "add", "-q", "-b", "dev", dev},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	config := func(t *testing.T, dir, value string) {
		t.Helper()
		args := []string{"-C", dir, "config", "grove.mainWorktree", value}
		if value == "" {
			args = []string{"-C", dir, "config", "--unset", "grove.mainWorktree"}
		}
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git config: %v\n%s", err, out)
		}
	}

	// Asked from the worktree that is not the one: the row names the worktree
	// that is, not whichever the caller happens to stand in.
	f, worth := checkMainWorktree(dev)
	if !worth || f.state != ok {
		t.Fatalf("worth = %v, state = %q, want it reported as fine", worth, f.state)
	}
	if !strings.Contains(f.detail, "main") || !strings.Contains(f.detail, "HEAD") {
		t.Errorf("detail = %q, want main, from the repository's HEAD", f.detail)
	}
	if f.advice != "" {
		t.Errorf("advice = %q, want nothing said below the table while it is fine", f.advice)
	}

	// origin/HEAD is the remote's default branch, and outranks whatever this
	// repository was left on, so the row has to say which of them it read.
	for _, args := range [][]string{
		{"update-ref", "refs/remotes/origin/dev", "refs/heads/dev"},
		{"symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/dev"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", bare}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if f, _ := checkMainWorktree(dev); f.state != ok || !strings.Contains(f.detail, "dev, from origin/HEAD") {
		t.Errorf("state = %q, detail = %q, want dev named along with origin/HEAD", f.state, f.detail)
	}

	config(t, dev, "dev")
	if f, _ := checkMainWorktree(dev); f.state != ok || !strings.Contains(f.detail, "dev, from grove.mainWorktree") {
		t.Errorf("state = %q, detail = %q, want dev named along with what decided it", f.state, f.detail)
	}

	config(t, dev, "devv")
	f, _ = checkMainWorktree(dev)
	if f.state == ok {
		t.Fatal("a setting naming no worktree was not reported")
	}
	if !strings.Contains(f.detail, "devv") || !strings.Contains(f.advice, "dev, main") {
		t.Errorf("detail = %q, advice = %q, want the typo and the worktrees to name instead", f.detail, f.advice)
	}

	// A repository with a main clone ignores the setting, and being ignored is
	// worth a word: it is why nothing changed.
	plain := filepath.Join(base, "app2")
	for _, args := range [][]string{
		{"-C", mkdir(t, plain), "init", "-q", "-b", "main"},
		{"-C", plain, "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if f, worth := checkMainWorktree(plain); worth {
		t.Errorf("detail = %q, want nothing said where there is a main clone", f.detail)
	}
	config(t, plain, "app2")
	if f, worth := checkMainWorktree(plain); !worth || f.state == ok || !strings.Contains(f.advice, "main clone") {
		t.Errorf("state = %q, advice = %q, want the setting called out as ignored", f.state, f.advice)
	}

	// A bare repository with no worktrees has nothing to choose between, and
	// an empty list is not advice.
	lonely := filepath.Join(base, "app3.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", lonely).CombinedOutput(); err != nil {
		t.Fatalf("init --bare: %v\n%s", err, out)
	}
	if f, worth := checkMainWorktree(lonely); worth {
		t.Errorf("detail = %q, advice = %q, want nothing said where there are no worktrees", f.detail, f.advice)
	}

	// Set globally it means "my bare layouts call it dev", and repeating that
	// it does nothing here, in every plain clone on the machine, is noise.
	global := filepath.Join(base, "gitconfig")
	if err := os.WriteFile(global, []byte("[grove]\n\tmainWorktree = dev\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	// Put the default branch back to the one HEAD names, so what follows is
	// the global setting deciding and not origin/HEAD agreeing by accident.
	if out, err := exec.Command("git", "-C", bare, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD").CombinedOutput(); err != nil {
		t.Fatalf("delete origin/HEAD: %v\n%s", err, out)
	}
	config(t, plain, "")
	config(t, dev, "")
	if f, worth := checkMainWorktree(plain); worth {
		t.Errorf("detail = %q, want a global setting to pass without comment", f.detail)
	}
	// And it still decides a bare layout, which is the whole point of setting
	// it once for the machine.
	if ctx, err := identity.Resolve(dev); err != nil {
		t.Fatal(err)
	} else if ctx.Slug != "app1" {
		t.Errorf("slug = %q, want the global setting to name dev the project", ctx.Slug)
	}
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
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
		if remedy(fix) == "" {
			t.Errorf("no remedy is written for %q, so the advice would be blank", fix)
		}
	}
}

// Silent is not the same as unchecked. DNS passes for everyone until the day
// it does not, and that day is the whole reason the check exists.
func TestASilentCheckStillSpeaksWhenItFails(t *testing.T) {
	quiet := checkDNS(defaultDomain)
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

// Nothing binds 443 on macOS, so attempting it reports permission denied
// however well the machine is set up. A correctly installed Mac with grove
// stopped once read as a red failure, which is the state it is in for as long
// as it is not running.
func TestPort443TrustsTheRedirectWhenNothingBindsIt(t *testing.T) {
	access := platform.PortAccess{
		Allowed: true,
		Detail:  "pf sends 443 to 10443, so grove serves it without root",
	}

	f := port443(nil, false, t.TempDir(), "grov.site", "127.0.0.1:10443", access)

	if f.state != ok {
		t.Errorf("state = %q with the redirect installed, want %q", f.state, ok)
	}
	if !strings.Contains(f.detail, "pf sends 443") {
		t.Errorf("detail = %q, which does not report the redirect", f.detail)
	}
}

// A Mac that never took the privileged step is a real failure, and the remedy
// is the install that stages the pf files.
func TestPort443ReportsAMissingRedirect(t *testing.T) {
	access := platform.PortAccess{Detail: "443 needs root here, and nothing redirects it yet"}

	f := port443(nil, false, t.TempDir(), "grov.site", "127.0.0.1:10443", access)

	if f.state != bad {
		t.Errorf("state = %q with no redirect, want %q", f.state, bad)
	}
	if f.fix != "grove install" {
		t.Errorf("fix = %q, want the install that stages the pf files", f.fix)
	}
}

// A daemon that answers the socket but speaks another protocol version is
// still what holds the port. Reporting an unknown process there sends someone
// hunting for a culprit grove named a line above.
func TestPortHolderNamesTheDaemonItCannotRead(t *testing.T) {
	said := holder(true)

	if !strings.Contains(said, "grove's own daemon") {
		t.Errorf("advice = %q, which does not name the daemon that answered", said)
	}
	if !strings.Contains(said, "cannot speak to") {
		t.Errorf("advice = %q, which does not say why it went unread", said)
	}
}
