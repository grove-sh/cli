package identity_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/identity"
)

func gitRepo(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func addWorktree(t *testing.T, repo, path, branch string) {
	t.Helper()
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "-q", "-b", branch, path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("worktree add: %v\n%s", err, out)
	}
}

func resolve(t *testing.T, dir string) identity.Context {
	t.Helper()
	ctx, err := identity.Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestMainWorktreeTakesNoSuffix(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "app1")
	gitRepo(t, repo)

	ctx := resolve(t, repo)

	if ctx.Project != "app1" || ctx.Variant != "" || ctx.Slug != "app1" {
		t.Errorf("got project=%q variant=%q slug=%q", ctx.Project, ctx.Variant, ctx.Slug)
	}
	if !ctx.IsMain {
		t.Error("IsMain = false in the main worktree")
	}
	if ctx.Source != identity.FromGit {
		t.Errorf("Source = %q, want git", ctx.Source)
	}
	if got := ctx.Host("grov.site"); got != "app1.grov.site" {
		t.Errorf("Host = %q", got)
	}
}

func TestLinkedWorktreeGetsSuffix(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app1")
	gitRepo(t, repo)
	wt := filepath.Join(base, "feat1")
	addWorktree(t, repo, wt, "feat1")

	ctx := resolve(t, wt)

	if ctx.Project != "app1" || ctx.Variant != "feat1" {
		t.Errorf("got project=%q variant=%q", ctx.Project, ctx.Variant)
	}
	if ctx.Slug != "app1-feat1" {
		t.Errorf("slug = %q, want app1-feat1", ctx.Slug)
	}
	if ctx.IsMain {
		t.Error("IsMain = true in a linked worktree")
	}
	if ctx.MainRoot == ctx.Root {
		t.Error("MainRoot and Root are the same in a linked worktree")
	}
}

func TestSubdirectoryResolvesToItsWorktree(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app1")
	gitRepo(t, repo)
	wt := filepath.Join(base, "feat1")
	addWorktree(t, repo, wt, "feat1")

	deep := filepath.Join(wt, "packages", "web")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	if ctx := resolve(t, deep); ctx.Slug != "app1-feat1" {
		t.Errorf("slug = %q, want app1-feat1", ctx.Slug)
	}
}

// The main clone cannot collide with a linked worktree, because a linked
// worktree always appends its variant. app1 and app1-app1 are distinct.
func TestMainAndLinkedNeverCollide(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "main", "app1")
	gitRepo(t, repo)
	wt := filepath.Join(base, "linked", "app1")
	addWorktree(t, repo, wt, "feat1")

	if mainSlug, linkedSlug := resolve(t, repo).Slug, resolve(t, wt).Slug; mainSlug == linkedSlug {
		t.Errorf("both resolved to %q", mainSlug)
	} else if linkedSlug != "app1-app1" {
		t.Errorf("linked slug = %q, want app1-app1", linkedSlug)
	}
}

// Two worktree directories that slugify the same way do collide. Identity
// cannot resolve that alone, so it must expose enough for the registry to.
func TestSlugifiedWorktreeNamesCanCollide(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "app1")
	gitRepo(t, repo)
	dotted := filepath.Join(base, "feat.1")
	hyphened := filepath.Join(base, "feat-1")
	addWorktree(t, repo, dotted, "dotted")
	addWorktree(t, repo, hyphened, "hyphened")

	first, second := resolve(t, dotted), resolve(t, hyphened)

	if first.Slug != second.Slug {
		t.Fatalf("expected a collision, got %q and %q", first.Slug, second.Slug)
	}
	if first.Root == second.Root {
		t.Error("Root is identical, so a caller cannot tell the two apart")
	}
}

func TestDerivedNamesAreSlugifiedQuietly(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "My_App.v2")
	gitRepo(t, repo)
	wt := filepath.Join(base, "Feature/One")
	addWorktree(t, repo, wt, "feature-one")

	if ctx := resolve(t, repo); ctx.Slug != "my-app-v2" {
		t.Errorf("project slug = %q, want my-app-v2", ctx.Slug)
	}
	if ctx := resolve(t, wt); ctx.Slug != "my-app-v2-one" {
		t.Errorf("worktree slug = %q, want my-app-v2-one", ctx.Slug)
	}
}

func TestLongSlugIsTruncatedWithAHash(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, strings.Repeat("a", 40))
	gitRepo(t, repo)
	wt := filepath.Join(base, strings.Repeat("b", 40))
	addWorktree(t, repo, wt, "long")

	ctx := resolve(t, wt)

	if len(ctx.Slug) > 63 {
		t.Errorf("slug is %d bytes: %q", len(ctx.Slug), ctx.Slug)
	}
	if !strings.HasPrefix(ctx.Slug, strings.Repeat("a", 40)+"-") {
		t.Errorf("slug lost its readable prefix: %q", ctx.Slug)
	}
}

func TestOverrideWins(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "app1")
	gitRepo(t, repo)
	t.Setenv("GROVE_CONTEXT", "Custom_Name")

	ctx := resolve(t, repo)

	if ctx.Slug != "custom-name" {
		t.Errorf("slug = %q, want custom-name", ctx.Slug)
	}
	if ctx.Source != identity.FromOverride {
		t.Errorf("Source = %q, want override", ctx.Source)
	}
}

func TestExplicitNamesAreRejectedNotSlugified(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "app1")
	gitRepo(t, repo)

	for _, override := range []string{"app.one", "app one", "-app", strings.Repeat("a", 64)} {
		t.Run(override, func(t *testing.T) {
			t.Setenv("GROVE_CONTEXT", override)
			if _, err := identity.Resolve(repo); err == nil {
				t.Errorf("%q was accepted", override)
			}
		})
	}
}

func TestOutsideAGitRepository(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "loose-project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	ctx := resolve(t, dir)

	if ctx.Slug != "loose-project" {
		t.Errorf("slug = %q", ctx.Slug)
	}
	if ctx.Source != identity.FromDir {
		t.Errorf("Source = %q, want dir", ctx.Source)
	}
}

func bareClone(t *testing.T, dest string) {
	t.Helper()
	seed := filepath.Join(t.TempDir(), "seed")
	gitRepo(t, seed)
	if out, err := exec.Command("git", "clone", "-q", "--bare", seed, dest).CombinedOutput(); err != nil {
		t.Fatalf("clone --bare: %v\n%s", err, out)
	}
}

func TestBareCloneWithWorktrees(t *testing.T) {
	base := t.TempDir()
	bare := filepath.Join(base, "app1.git")
	bareClone(t, bare)
	wt := filepath.Join(base, "feat1")
	addWorktree(t, bare, wt, "feat1")

	ctx := resolve(t, wt)

	if ctx.Project != "app1" {
		t.Errorf("project = %q, want app1; the .git suffix must not become part of the name", ctx.Project)
	}
	if ctx.Slug != "app1-feat1" {
		t.Errorf("slug = %q, want app1-feat1", ctx.Slug)
	}
	if ctx.IsMain {
		t.Error("IsMain = true, but a bare repository owns no worktree")
	}
	// git reports resolved paths, and on macOS /var is a symlink to
	// /private/var, so the expectation has to be resolved too.
	wantBare, err := filepath.EvalSymlinks(bare)
	if err != nil {
		t.Fatal(err)
	}
	if ctx.BareRoot != wantBare {
		t.Errorf("BareRoot = %q, want %q", ctx.BareRoot, wantBare)
	}
	if ctx.MainRoot != "" {
		t.Errorf("MainRoot = %q, want empty for a bare repository", ctx.MainRoot)
	}
}

// The other common layout keeps the bare repository beside its worktrees, so
// the project name is one level out.
func TestDotBareLayout(t *testing.T) {
	base := filepath.Join(t.TempDir(), "app1")
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	bareClone(t, filepath.Join(base, ".bare"))
	if err := os.WriteFile(filepath.Join(base, ".git"), []byte("gitdir: ./.bare\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(base, "feat1")
	addWorktree(t, base, wt, "feat1")

	if ctx := resolve(t, wt); ctx.Slug != "app1-feat1" {
		t.Errorf("slug = %q, want app1-feat1", ctx.Slug)
	}
}

func TestBareRepositoryIsAnError(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "app1.git")
	if out, err := exec.Command("git", "init", "-q", "--bare", dir).CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	if _, err := identity.Resolve(dir); !errors.Is(err, identity.ErrBareRepository) {
		t.Errorf("err = %v, want ErrBareRepository", err)
	}
}

// A name in grove.toml is the project's own idea of what it is called, which
// beats whatever the directory happens to be named.
func TestWithProjectRenamesTheContext(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "myapp-repo")
	gitRepo(t, repo)
	wt := filepath.Join(base, "feat1")
	addWorktree(t, repo, wt, "feat1")

	renamed, err := resolve(t, wt).WithProject("myapp")
	if err != nil {
		t.Fatal(err)
	}

	if renamed.Project != "myapp" || renamed.Slug != "myapp-feat1" {
		t.Errorf("project = %q, slug = %q", renamed.Project, renamed.Slug)
	}
	if renamed.Variant != "feat1" {
		t.Errorf("variant = %q, want the worktree to survive the rename", renamed.Variant)
	}
}

func TestWithProjectYieldsToTheEnvironmentOverride(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "myapp-repo")
	gitRepo(t, repo)
	t.Setenv("GROVE_CONTEXT", "pinned")

	renamed, err := resolve(t, repo).WithProject("myapp")
	if err != nil {
		t.Fatal(err)
	}

	if renamed.Slug != "pinned" {
		t.Errorf("slug = %q, want GROVE_CONTEXT to keep winning", renamed.Slug)
	}
}

func TestWithProjectRejectsAnUnusableName(t *testing.T) {
	repo := filepath.Join(t.TempDir(), "myapp")
	gitRepo(t, repo)

	if _, err := resolve(t, repo).WithProject("my.app"); err == nil {
		t.Error("a dotted name was accepted")
	}
}
