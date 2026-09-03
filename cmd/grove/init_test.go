package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/config"
)

func writeApp(t *testing.T, dir, manifest string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
}

func generated(t *testing.T, repo string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(repo, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// What init writes has to load, or it is worse than nothing.
func TestInitWritesLoadableConfig(t *testing.T) {
	repo := tempRepo(t, "app1")
	if err := os.Remove(filepath.Join(repo, config.FileName)); err != nil {
		t.Fatal(err)
	}
	writeApp(t, filepath.Join(repo, "apps", "web"), `{"scripts":{"dev":"next dev"},"dependencies":{"next":"15"}}`)
	t.Chdir(repo)

	code, stdout, stderr := exercise(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "apps/web") {
		t.Errorf("output does not say what it found:\n%s", stdout)
	}

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	route, ok := cfg.Routes["web"]
	if !ok {
		t.Fatalf("no route for the app: %v", cfg.Routes)
	}
	if route.Dir != filepath.Join("apps", "web") {
		t.Errorf("dir = %q", route.Dir)
	}
	// One app, so it takes the context's own hostname rather than a suffix.
	if route.Label != "" {
		t.Errorf("label = %q, want the bare hostname", route.Label)
	}
	if route.Env["NEXT_PUBLIC_SITE_URL"] == "" {
		t.Errorf("nothing carries the URL: %v", route.Env)
	}
}

func TestInitWritesFromTheRootWhateverDirectoryYouAreIn(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	deep := filepath.Join(repo, "apps", "web", "src")
	writeApp(t, filepath.Join(repo, "apps", "web"), `{"scripts":{"dev":"vite dev"},"devDependencies":{"vite":"6"}}`)
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)

	if code, _, stderr := exercise(t, "init"); code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	if _, err := os.Stat(filepath.Join(repo, config.FileName)); err != nil {
		t.Errorf("not written at the worktree root: %v", err)
	}
	if body := generated(t, repo); !strings.Contains(body, "VITE_SITE_URL") {
		t.Errorf("did not read the framework from package.json:\n%s", body)
	}
}

func TestInitKeepsTwoAppsApart(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "web"), `{"scripts":{"dev":"next dev"},"dependencies":{"next":"15"}}`)
	writeApp(t, filepath.Join(repo, "apps", "admin"), `{"scripts":{"dev":"vite dev"},"devDependencies":{"vite":"6"}}`)
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("routes = %v", cfg.Routes)
	}
	// With two apps neither can take the bare hostname, so each keeps its name.
	for name, route := range cfg.Routes {
		if route.Label != name {
			t.Errorf("route %q has label %q", name, route.Label)
		}
	}
}

// The enabled flags are not a signal for whether a port gets published: kong
// binds its port with "[api] enabled = false", which is how two worktrees
// collided on 54321. So every port the stack can publish gets an allocation,
// because a spare one costs a number and a missing one costs a collision.
func TestInitAllocatesEverySupabasePort(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	stack := filepath.Join(repo, "packages", "db", "supabase")
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stack, "config.toml"), []byte(`
project_id = "whatever"

[api]
enabled = false

[local_smtp]
enabled = false
`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	for _, name := range []string{"api", "studio", "mail"} {
		if _, ok := cfg.Routes[name]; !ok {
			t.Errorf("no route for %q, though supabase may publish its port either way", name)
		}
	}
	for _, name := range []string{"db", "shadow", "pooler", "analytics"} {
		if _, ok := cfg.Ports[name]; !ok {
			t.Errorf("no port for %q", name)
		}
	}
	if cfg.Env["SUPABASE_PROJECT_ID"] == "" {
		t.Error("nothing varies project_id, so two worktrees would collide on container names")
	}
	// The database URL has to name the port grove allocated, not the one the
	// stack's config pins.
	if url := cfg.Env["POSTGRES_URL"]; !strings.Contains(url, "{ports.db}") {
		t.Errorf("POSTGRES_URL = %q", url)
	}
	// Every one of them outlives the command that starts the stack.
	for _, entry := range cfg.All() {
		if !entry.Detached {
			t.Errorf("%s is attached, but supabase start exits and leaves it running", entry.Ref())
		}
	}
}

// Both mail spellings are emitted, since the key was renamed at CLI 2.108 and
// each version binds only the one it knows.
func TestInitEmitsBothMailBindings(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	stack := filepath.Join(repo, "supabase")
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stack, "config.toml"), []byte("project_id = \"x\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	mail := cfg.Routes["mail"]
	if mail == nil {
		t.Fatal("no mail route")
	}
	for _, name := range []string{"SUPABASE_INBUCKET_PORT", "SUPABASE_LOCAL_SMTP_PORT"} {
		if mail.Env[name] == "" {
			t.Errorf("mail route does not set %s: %v", name, mail.Env)
		}
	}
}

func TestInitRefusesToClobber(t *testing.T) {
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	code, _, stderr := exercise(t, "init")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "--force") {
		t.Errorf("stderr does not name the way past it: %q", stderr)
	}
}

// A library's dev script is a watching compiler, not a server. Judging by the
// presence of a dev script gave every package in a monorepo a hostname.
func TestInitIgnoresLibraries(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "nextjs"), `{"scripts":{"dev":"next dev"},"dependencies":{"next":"15"}}`)
	for _, library := range []string{"common", "db", "ui", "validators"} {
		writeApp(t, filepath.Join(repo, "packages", library), `{"scripts":{"dev":"tsc"},"devDependencies":{"typescript":"5"}}`)
	}
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 1 {
		t.Fatalf("routes = %v, want only the app", cfg.Routes)
	}
	route, ok := cfg.Routes["nextjs"]
	if !ok {
		t.Fatalf("routes = %v", cfg.Routes)
	}
	// The only app, so it takes the context's own hostname.
	if route.Label != "" {
		t.Errorf("label = %q, want the bare hostname", route.Label)
	}
}

// A dependency whose name merely contains a framework's is not that framework.
func TestInitDoesNotMatchAFrameworkBySubstring(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "thing"),
		`{"scripts":{"dev":"node build.js"},"devDependencies":{"@repo/next-config":"*","vitest":"2"}}`)
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 0 {
		t.Errorf("routes = %v, want none: next-config is not next and vitest is not vite", cfg.Routes)
	}
}

// Something under apps/ with a dev script that grove cannot identify is worth
// saying out loud, rather than silently omitting or silently guessing.
func TestInitReportsAnAppItCannotIdentify(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "worker"), `{"scripts":{"dev":"node worker.js"}}`)
	t.Chdir(repo)

	code, stdout, stderr := exercise(t, "init")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	if !strings.Contains(stdout, "could not identify") {
		t.Errorf("output does not mention it:\n%s", stdout)
	}
	body := generated(t, repo)
	if !strings.Contains(body, "# [routes.worker]") {
		t.Errorf("no commented route to start from:\n%s", body)
	}
	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routes) != 0 {
		t.Errorf("routes = %v, want the guess left to the reader", cfg.Routes)
	}
}
