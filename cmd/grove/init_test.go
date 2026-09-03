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
	// The derived name is reported, not written into the file: a fork tree
	// shares one grove.toml, and a per-clone comment would conflict on every
	// merge from upstream.
	if !strings.Contains(stdout, "the name app1") {
		t.Errorf("output does not report the derived name:\n%s", stdout)
	}
	if strings.Contains(generated(t, repo), `name = "app1"`) {
		t.Error("the derived name was written into the file")
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
	// The URL goes in [env], not on the route: a build binds nothing, and a
	// route's own variables apply only while that route is bound.
	if got := cfg.Env["NEXT_PUBLIC_SITE_URL"]; got != "{routes.web.url}" {
		t.Errorf("[env] NEXT_PUBLIC_SITE_URL = %q", got)
	}
	if route.Env["NEXT_PUBLIC_SITE_URL"] != "" {
		t.Errorf("the URL stayed on the route, where a build cannot see it: %v", route.Env)
	}
	if route.Env["PORT"] == "" {
		t.Errorf("the port left the route, where it belongs: %v", route.Env)
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
	writeStack(t, filepath.Join(repo, "packages", "db", "supabase"), disabledStack)
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	allocated := map[string]bool{}
	for _, entry := range cfg.All() {
		allocated[entry.Name] = true
	}
	for _, name := range []string{"api", "studio", "mail", "db", "shadow", "pooler", "analytics"} {
		if !allocated[name] {
			t.Errorf("nothing allocated for %q, though supabase may publish its port either way", name)
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

// A hostname is worth minting only for something that will answer on it. The
// port is still allocated, since that is what keeps two worktrees apart.
func TestInitGivesNoHostnameToADisabledService(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "packages", "db", "supabase"), disabledStack)
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	for _, name := range []string{"api", "mail"} {
		if _, ok := cfg.Routes[name]; ok {
			t.Errorf("%q got a hostname, though the stack has it turned off", name)
		}
		if _, ok := cfg.Ports[name]; !ok {
			t.Errorf("%q lost its allocation along with its hostname", name)
		}
	}
	// This one the config says nothing about, so it keeps its hostname.
	if _, ok := cfg.Routes["studio"]; !ok {
		t.Error("studio lost its hostname, though nothing turned it off")
	}
}

// The port this names has to be the one the api actually holds, since the
// whole point is to get bucket seeding off the 54321 the CLI would otherwise
// dial. Resolving it is the only way to know the reference is right.
func TestInitPointsBucketSeedingAtTheAPIPort(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "supabase"), "project_id = \"x\"\n")
	t.Chdir(repo)

	exercise(t, "init")
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}
	_, stdout, _ := exercise(t, "env", "--socket", socket)

	env := exported(stdout)
	if want := "http://127.0.0.1:" + env["SUPABASE_API_PORT"]; env["SUPABASE_API_EXTERNAL_URL"] != want {
		t.Errorf("SUPABASE_API_EXTERNAL_URL = %q, want %q", env["SUPABASE_API_EXTERNAL_URL"], want)
	}
}

// A disabled api is a bare port rather than a route, and the reference has to
// follow it there: {routes.api.port} would name a section init never wrote.
func TestInitPointsBucketSeedingAtADemotedAPI(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "supabase"), disabledStack)
	t.Chdir(repo)

	exercise(t, "init")
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}
	_, stdout, _ := exercise(t, "env", "--socket", socket)

	if got := generated(t, repo); !strings.Contains(got, `"http://127.0.0.1:{ports.api}"`) {
		t.Errorf("the reference does not follow the demoted api:\n%s", got)
	}
	env := exported(stdout)
	if want := "http://127.0.0.1:" + env["SUPABASE_API_PORT"]; env["SUPABASE_API_EXTERNAL_URL"] != want {
		t.Errorf("SUPABASE_API_EXTERNAL_URL = %q, want %q", env["SUPABASE_API_EXTERNAL_URL"], want)
	}
}

// The scheme follows api.tls, so hardcoding http where the stack serves https
// would be a downgrade. Better to say nothing.
func TestInitNamesNoAPIURLForATLSStack(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "supabase"), "project_id = \"x\"\n\n[api.tls]\nenabled = true\n")
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	if got := cfg.Env["SUPABASE_API_EXTERNAL_URL"]; got != "" {
		t.Errorf("SUPABASE_API_EXTERNAL_URL = %q, but the stack serves https", got)
	}
}

// The supabase URL names the api route, so a disabled api has to leave it
// unset: naming a route that was never written would not load.
func TestInitNamesNoSupabaseURLForADisabledAPI(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "web"), `{"scripts":{"dev":"next dev"},"dependencies":{"next":"15"}}`)
	writeStack(t, filepath.Join(repo, "packages", "db", "supabase"), disabledStack)
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	if got := cfg.Env["NEXT_PUBLIC_SUPABASE_URL"]; got != "" {
		t.Errorf("NEXT_PUBLIC_SUPABASE_URL = %q, but the api has no hostname", got)
	}
	if got := cfg.Env["NEXT_PUBLIC_SITE_URL"]; got != "{routes.web.url}" {
		t.Errorf("the app lost its own URL with it: %q", got)
	}
}

// Both mail spellings are emitted, since the key was renamed at CLI 2.108 and
// each version binds only the one it knows.
func TestInitEmitsBothMailBindings(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "supabase"), "project_id = \"x\"\n")
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

// Two apps naming the same URL variable cannot both claim it in [env], so it
// stays on each route, where it applies only while that route is bound.
func TestInitLeavesASharedURLVariableOnItsRoutes(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	for _, name := range []string{"one", "two"} {
		writeApp(t, filepath.Join(repo, "apps", name),
			`{"scripts":{"dev":"astro dev"},"devDependencies":{"astro":"5"}}`)
	}
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	if cfg.Env["PUBLIC_SITE_URL"] != "" {
		t.Errorf("[env] claims a variable two apps disagree about: %q", cfg.Env["PUBLIC_SITE_URL"])
	}
	for _, name := range []string{"one", "two"} {
		if cfg.Routes[name].Env["PUBLIC_SITE_URL"] == "" {
			t.Errorf("route %q lost its URL: %v", name, cfg.Routes[name].Env)
		}
	}
}

// Every app in a project talks to the same stack, so the API's URL is worth
// setting for them, under whatever prefix each framework exposes.
func TestInitNamesTheSupabaseAPIForTheApp(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "web"), `{"scripts":{"dev":"next dev"},"dependencies":{"next":"15"}}`)
	writeStack(t, filepath.Join(repo, "packages", "db", "supabase"), "project_id = \"x\"\n")
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	if got := cfg.Env["NEXT_PUBLIC_SUPABASE_URL"]; got != "{routes.api.url}" {
		t.Errorf("NEXT_PUBLIC_SUPABASE_URL = %q", got)
	}
	// Both names come from one prefix, so they cannot drift apart.
	if got := cfg.Env["NEXT_PUBLIC_SITE_URL"]; got != "{routes.web.url}" {
		t.Errorf("NEXT_PUBLIC_SITE_URL = %q", got)
	}
}

// Without a stack there is no API to name, so nothing is set.
func TestInitNamesNoSupabaseURLWithoutAStack(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "web"), `{"scripts":{"dev":"vite dev"},"devDependencies":{"vite":"6"}}`)
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Env["VITE_SUPABASE_URL"]; got != "" {
		t.Errorf("VITE_SUPABASE_URL = %q, but there is no stack", got)
	}
}

// disabledStack is the shape that started all this: kong publishes 54321 with
// the api turned off, and nothing at all answers for mail.
const disabledStack = `
project_id = "whatever"

[api]
enabled = false

[local_smtp]
enabled = false
`

func writeStack(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// exported reads shell-format env output back into a map.
func exported(stdout string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(stdout, "\n") {
		name, value, found := strings.Cut(strings.TrimPrefix(line, "export "), "=")
		if found {
			out[name] = strings.Trim(value, "'")
		}
	}
	return out
}
