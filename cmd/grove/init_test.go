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

// A disabled service publishes no port, so a port for it would be a lie. This
// config has the api off and analytics on, and never mentions the pooler,
// which supabase ships disabled.
func TestInitReadsWhichSupabaseServicesAreOn(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	stack := filepath.Join(repo, "packages", "db", "supabase")
	if err := os.MkdirAll(stack, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stack, "config.toml"), []byte(`
project_id = "whatever"

[studio]
port = 54323

[db]
port = 54322

[analytics]
enabled = true
port = 54327

[api]
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
	if _, ok := cfg.Routes["studio"]; !ok {
		t.Error("no route for studio, which is enabled")
	}
	if _, ok := cfg.Routes["api"]; ok {
		t.Error("a route for the api, which is disabled")
	}
	for _, want := range []string{"db", "shadow", "analytics"} {
		if _, ok := cfg.Ports[want]; !ok {
			t.Errorf("no port for %q", want)
		}
	}
	if _, ok := cfg.Ports["pooler"]; ok {
		t.Error("a port for the pooler, which supabase ships disabled")
	}
	if cfg.Env["SUPABASE_PROJECT_ID"] == "" {
		t.Error("nothing varies project_id, so two worktrees would collide on container names")
	}
	// Every service grove allocates for outlives the command that starts it.
	for _, entry := range cfg.All() {
		if !entry.Detached {
			t.Errorf("%s is attached, but supabase start exits and leaves it running", entry.Ref())
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
