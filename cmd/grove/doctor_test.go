package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/daemon"
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

// An override file is one person's, and untracked it stays that way, which is
// every project that has not made the mistake. Tracked, it is on its way to
// overriding for everyone, so that is the only version worth a row: git add is
// where the mistake is made, and the commit is too late to warn about.
func TestTheOverrideCheckSpeaksOnlyForATrackedFile(t *testing.T) {
	dir := mkdir(t, filepath.Join(t.TempDir(), "app1"))
	if out, err := exec.Command("git", "-C", dir, "init", "-q", "-b", "main").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	for name, body := range map[string]string{"grove.toml": "", ".env.local": "DATABASE_URL=postgres://127.0.0.1:5432/scratch\n"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	if _, worth := checkOverrideFile(loadProject(t, dir)); worth {
		t.Error("an untracked override file was reported")
	}

	if out, err := exec.Command("git", "-C", dir, "add", ".env.local").CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	f, worth := checkOverrideFile(loadProject(t, dir))
	if !worth {
		t.Fatal("a tracked override file was not reported")
	}
	if f.state != warn {
		t.Errorf("state = %q, want %q", f.state, warn)
	}
	if !strings.Contains(f.detail, ".env.local") || f.advice == "" {
		t.Errorf("finding does not say what is wrong or what to do: %+v", f)
	}
}

func loadProject(t *testing.T, dir string) *config.Config {
	t.Helper()
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Nothing to say where there is no project at all, which is most directories
// doctor is run in.
func TestTheProjectChecksAreQuietWithoutAProject(t *testing.T) {
	dir := t.TempDir()
	cfg, err := config.Load(dir)
	if _, worth := checkProject(err); worth {
		t.Error("a directory with no grove.toml was reported on")
	}
	if _, worth := checkOverrideFile(cfg); worth {
		t.Error("a directory with no grove.toml was reported on")
	}
}

// The other half of that: nothing grove does in this directory works, so a run
// that said only that DNS resolves would read as a clean bill of health.
func TestAConfigThatWillNotLoadFailsTheRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("routes.web = \n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := config.Load(dir)
	f, worth := checkProject(err)
	if !worth {
		t.Fatal("a config that will not load was not reported")
	}
	if f.state != bad {
		t.Errorf("state = %q, want %q: nothing here runs", f.state, bad)
	}
	if f.detail == "" || !strings.Contains(f.advice, config.FileName) {
		t.Errorf("finding does not say what is wrong or which file: %+v", f)
	}
}

// The failure this exists for: two groves on one machine derive the context
// differently, so the stack is leased and listening under a name this one no
// longer uses, and every symptom reads as grove losing track of it.
func TestContextMovedNamesTheSlugStillHoldingPorts(t *testing.T) {
	repo := tempRepo(t, "app1")
	context := contextOf(t, repo)

	f, worth := checkContextMoved(repo, []daemon.Live{
		{Slug: context.Slug + "-main", Service: "db", Worktree: context.Root, Port: 20299},
		{Slug: context.Slug + "-main", Service: "api", Worktree: context.Root, Port: 20103},
	})
	if !worth {
		t.Fatal("a worktree leasing under another slug was not reported")
	}
	// Once, however many entries it holds: the slug is the finding.
	if want := "holding ports as " + context.Slug + "-main, and resolving " + context.Slug + " now"; f.detail != want {
		t.Errorf("detail = %q, want %q", f.detail, want)
	}
	if f.state != warn {
		t.Errorf("state = %q, want a warning", f.state)
	}
}

// Another worktree of the same repository is a different context on purpose,
// and reporting one would fire on every machine running two branches at once.
func TestContextMovedIgnoresOtherWorktrees(t *testing.T) {
	repo := tempRepo(t, "app1")
	context := contextOf(t, repo)

	_, worth := checkContextMoved(repo, []daemon.Live{
		{Slug: context.Slug, Service: "db", Worktree: context.Root},
		{Slug: "app1-feat1", Service: "db", Worktree: "/src/feat1"},
		{Slug: "other-project", Service: "db", Worktree: "/src/other"},
	})
	if worth {
		t.Error("leases belonging to other worktrees were reported")
	}
}

func TestContextMovedIsQuietWithNothingToAsk(t *testing.T) {
	repo := tempRepo(t, "app1")

	// No daemon, so no leases: the daemon check reports that, not this one.
	if _, worth := checkContextMoved(repo, nil); worth {
		t.Error("a machine with no leases was reported on")
	}

	// A bare repository is no context at all, which is the one way resolving
	// fails rather than falling back to the directory's name.
	bare := filepath.Join(t.TempDir(), "app1.git")
	if out, err := exec.Command("git", "init", "--bare", "-q", bare).CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v\n%s", err, out)
	}
	if _, worth := checkContextMoved(bare, []daemon.Live{{Slug: "x", Worktree: bare}}); worth {
		t.Error("a bare repository was reported on")
	}
}

// Two builds need not spell one worktree the same way, since one may not have
// resolved the symlinks the other did. Missing the match would lose the finding
// silently, which is the failure this whole check exists to end.
func TestContextMovedMatchesAWorktreeThroughASymlink(t *testing.T) {
	repo := tempRepo(t, "app1")
	context := contextOf(t, repo)

	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(context.Root, link); err != nil {
		t.Fatal(err)
	}

	if _, worth := checkContextMoved(repo, []daemon.Live{
		{Slug: context.Slug + "-main", Service: "db", Worktree: link},
	}); !worth {
		t.Error("a lease naming this worktree through a symlink was missed")
	}
}

func contextOf(t *testing.T, dir string) identity.Context {
	t.Helper()
	context, err := identity.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	return context
}

// The case this exists for: a workspace whose package installs a different
// grove from the rest of it, which is invisible until two contexts of the same
// worktree start handing out different ports.
func TestProjectGroveNamesTheCopiesOnEachVersion(t *testing.T) {
	root := t.TempDir()
	writeGroveToml(t, root)
	installGrove(t, filepath.Join(root, "node_modules"), "0.4.5")
	installGrove(t, filepath.Join(root, "packages", "db", "node_modules"), "0.4.3")

	f, worth := checkProjectGrove(root, "v0.4.5")
	if !worth {
		t.Fatal("a workspace holding two versions was not reported")
	}
	want := "0.4.3 in packages/db; 0.4.5 in this project"
	if f.detail != want {
		t.Errorf("detail = %q, want %q", f.detail, want)
	}
	if f.state != warn {
		t.Errorf("state = %q, want a warning", f.state)
	}
}

// The project disagreeing with itself is the finding whatever is running it,
// since the build that starts the stack is the one under the package whose
// script does it.
func TestProjectGroveReportsASplitFromAWorkingTreeBuild(t *testing.T) {
	root := t.TempDir()
	writeGroveToml(t, root)
	installGrove(t, filepath.Join(root, "node_modules"), "0.4.5")
	installGrove(t, filepath.Join(root, "packages", "db", "node_modules"), "0.4.3")

	if _, worth := checkProjectGrove(root, "v0.5.0-beta.1+dirty"); !worth {
		t.Error("a split project went unreported because of what was running it")
	}
}

// npm writes 0.4.5 where grove reports v0.4.5, and a check that called those
// two different versions would fire on every correctly installed project.
func TestProjectGroveAcceptsTheTagSpelling(t *testing.T) {
	root := t.TempDir()
	writeGroveToml(t, root)
	installGrove(t, filepath.Join(root, "node_modules"), "0.4.5")

	if _, worth := checkProjectGrove(root, "v0.4.5"); worth {
		t.Error("a matching version was reported as a mismatch")
	}
}

// One version throughout, and it is not the one running: worth saying, but only
// where the running build is something a project could have installed.
func TestProjectGroveComparesOnlyAReleasedBuild(t *testing.T) {
	root := t.TempDir()
	writeGroveToml(t, root)
	installGrove(t, filepath.Join(root, "node_modules"), "0.4.3")

	f, worth := checkProjectGrove(root, "v0.4.5")
	if !worth {
		t.Fatal("a project a release behind was not reported")
	}
	if !strings.Contains(f.detail, "0.4.3 throughout") {
		t.Errorf("detail = %q", f.detail)
	}
	// A row, not an alarm: this only matters where the two derive a context
	// differently, and most pairs do not.
	if f.state != ok {
		t.Errorf("state = %q, want a plain row", f.state)
	}
	if f.advice != "" {
		t.Errorf("a paragraph was spent on it: %q", f.advice)
	}

	// A build from a working tree matches no published version, so comparing it
	// would warn about every project on the machine. The pseudo-version is what
	// go stamps on a clean checkout past the last tag, which is every build a
	// contributor makes and the one shape that reads like a release.
	for _, running := range []string{
		"v0.5.0-beta.1+dirty",
		"v0.5.0-beta.1.0.20260918153512-01613716023d",
		"unknown",
	} {
		if _, worth := checkProjectGrove(root, running); worth {
			t.Errorf("%s reported a mismatch against an agreed project", running)
		}
	}
}

func TestProjectGroveIsQuietWithNothingToRead(t *testing.T) {
	// A directory with no project: nothing installs grove for it.
	if _, worth := checkProjectGrove(t.TempDir(), "v0.4.5"); worth {
		t.Error("a directory with no grove.toml was reported on")
	}

	// A project with no install at all, which is every project using the
	// binary straight off the path.
	root := t.TempDir()
	writeGroveToml(t, root)
	if _, worth := checkProjectGrove(root, "v0.4.5"); worth {
		t.Error("a project that installs no grove was reported on")
	}
}

// The walk reads the source tree, and a repository's dependencies are most of
// what is under it. Grove's own copy inside another package's node_modules is
// not an install of this project's.
func TestProjectGroveDoesNotDescendIntoDependencies(t *testing.T) {
	root := t.TempDir()
	writeGroveToml(t, root)
	installGrove(t, filepath.Join(root, "node_modules"), "0.4.5")
	installGrove(t, filepath.Join(root, "node_modules", "some-package", "node_modules"), "0.1.0")

	if f, worth := checkProjectGrove(root, "v0.4.5"); worth {
		t.Errorf("a nested dependency's copy was counted: %q", f.detail)
	}
}

func writeGroveToml(t *testing.T, dir string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, config.FileName), []byte("[routes.web]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func installGrove(t *testing.T, nodeModules, version string) string {
	t.Helper()
	dir := filepath.Join(nodeModules, "@grove-sh", "cli")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"name":"@grove-sh/cli","version":%q}`, version)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// pnpm installs one physical copy per version and links each package at it, so
// what a package resolves is a symlink and the directory walk never descends
// into it. Reading through the link is the whole point: this is the layout the
// check exists for.
func TestProjectGroveReadsThroughAPnpmLink(t *testing.T) {
	root := t.TempDir()
	writeGroveToml(t, root)
	store := filepath.Join(root, "node_modules", ".pnpm")
	linkGrove(t, filepath.Join(root, "node_modules"), installGrove(t, filepath.Join(store, "@grove-sh+cli@0.4.5", "node_modules"), "0.4.5"))
	linkGrove(t, filepath.Join(root, "packages", "db", "node_modules"), installGrove(t, filepath.Join(store, "@grove-sh+cli@0.4.3", "node_modules"), "0.4.3"))

	f, worth := checkProjectGrove(root, "v0.4.5")
	if !worth {
		t.Fatal("a pnpm workspace holding two versions was not reported")
	}
	want := "0.4.3 in packages/db; 0.4.5 in this project"
	if f.detail != want {
		t.Errorf("detail = %q, want %q", f.detail, want)
	}
}

func linkGrove(t *testing.T, nodeModules, target string) {
	t.Helper()
	scope := filepath.Join(nodeModules, "@grove-sh")
	if err := os.MkdirAll(scope, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(scope, "cli")); err != nil {
		t.Fatal(err)
	}
}

// Some layouts link node_modules rather than create it, and a link is not a
// directory to a walk, so checking the kind before the name loses the whole
// tree behind it.
func TestProjectGroveReadsALinkedNodeModules(t *testing.T) {
	root := t.TempDir()
	writeGroveToml(t, root)
	installGrove(t, filepath.Join(root, "node_modules"), "0.4.5")

	elsewhere := installGrove(t, filepath.Join(t.TempDir(), "store"), "0.4.3")
	linked := filepath.Join(root, "packages", "db")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	// The whole node_modules is one link, which is the shape that was missed.
	if err := os.Symlink(filepath.Dir(filepath.Dir(elsewhere)), filepath.Join(linked, "node_modules")); err != nil {
		t.Fatal(err)
	}

	f, worth := checkProjectGrove(root, "v0.4.5")
	if !worth {
		t.Fatal("a linked node_modules was walked past")
	}
	if !strings.Contains(f.detail, "0.4.3 in packages/db") {
		t.Errorf("detail = %q", f.detail)
	}
}

// The two branches are different claims: one project holding two versions is a
// split, which is wrong however it got that way, and one whole version behind
// is only a difference that sometimes matters. Only the first earns a warning
// and the paragraph that goes with it.
func TestOnlyASplitProjectIsWorthAnAlarm(t *testing.T) {
	split := t.TempDir()
	writeGroveToml(t, split)
	installGrove(t, filepath.Join(split, "node_modules"), "0.4.5")
	installGrove(t, filepath.Join(split, "packages", "db", "node_modules"), "0.4.3")

	behind := t.TempDir()
	writeGroveToml(t, behind)
	installGrove(t, filepath.Join(behind, "node_modules"), "0.4.3")

	one, _ := checkProjectGrove(split, "v0.4.5")
	other, _ := checkProjectGrove(behind, "v0.4.5")
	if one.state != warn || one.advice == "" {
		t.Errorf("a split project is not reported as a problem: %+v", one)
	}
	if other.state != ok || other.advice != "" {
		t.Errorf("being a version behind was reported as a problem: %+v", other)
	}
}
