package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/identity"
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
	if got := cfg.Env["NEXT_PUBLIC_SITE_URL"]; got != "{web.url}" {
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
	if url := cfg.Env["POSTGRES_URL"]; !strings.Contains(url, "{db.port}") {
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
	if got := cfg.Env["NEXT_PUBLIC_SITE_URL"]; got != "{web.url}" {
		t.Errorf("the app lost its own URL with it: %q", got)
	}
}

// A detached entry's env block applies whatever the caller is running, which
// reads as scoped and is not. So the stack's port variables are written into
// [env], where being always set is what the section means, and the entries are
// left declaring a port and nothing else.
func TestInitPutsDetachedPortsInEnv(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "supabase"), "project_id = \"x\"\n")
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}

	// Named per entry rather than self-referential, since [env] has no self.
	for name, want := range map[string]string{
		"SUPABASE_LOCAL_SMTP_PORT": "{mail.port}",
		"SUPABASE_DB_PORT":         "{db.port}",
		"SUPABASE_API_PORT":        "{api.port}",
	} {
		if got := cfg.Env[name]; got != want {
			t.Errorf("[env] %s = %q, want %q", name, got, want)
		}
	}

	// And nothing was left behind on the entries to be misread as scoped.
	for _, entry := range cfg.All() {
		if entry.Detached && len(entry.Env) > 0 {
			t.Errorf("%s carries env that applies everywhere: %v", entry.Ref(), entry.Env)
		}
	}

	// The spelling supabase dropped is gone rather than emitted alongside.
	if _, still := cfg.Env["SUPABASE_INBUCKET_PORT"]; still {
		t.Error("[env] still sets the renamed inbucket variable")
	}
}

// A route's env applies only while that route is bound, so its own port is the
// one variable that belongs on the entry rather than above it.
func TestInitLeavesARoutesOwnPortOnTheRoute(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	if err := os.WriteFile(filepath.Join(repo, "package.json"),
		[]byte(`{"name":"web","dependencies":{"next":"15"},"scripts":{"dev":"next dev"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	web := cfg.Routes["web"]
	if web == nil {
		t.Fatalf("no web route: %v", cfg.Routes)
	}
	if got := web.Env["PORT"]; got != "{port}" {
		t.Errorf("route PORT = %q, want the self-reference {port}", got)
	}
	if _, hoisted := cfg.Env["PORT"]; hoisted {
		t.Error("[env] sets PORT, which would hand every command the web server's port")
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
	if got := cfg.Env["NEXT_PUBLIC_SUPABASE_URL"]; got != "{api.url}" {
		t.Errorf("NEXT_PUBLIC_SUPABASE_URL = %q", got)
	}
	// Both names come from one prefix, so they cannot drift apart.
	if got := cfg.Env["NEXT_PUBLIC_SITE_URL"]; got != "{web.url}" {
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

[storage.buckets.app]
public = false
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

// The comment above a route is the only place its hostname appears before
// anything runs, so it has to be the hostname the route will really answer on.
// One pointing somewhere nothing serves is worse than no comment at all.
func TestInitCommentsEachRouteWithItsHostname(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeApp(t, filepath.Join(repo, "apps", "web"), `{"scripts":{"dev":"next dev"},"dependencies":{"next":"15"}}`)
	writeStack(t, filepath.Join(repo, "supabase"), "project_id = \"x\"\n")
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("what init wrote does not load: %v\n%s", err, generated(t, repo))
	}
	lines := strings.Split(generated(t, repo), "\n")
	// The one app takes the bare hostname and the stack's services take a
	// suffix, so both shapes are covered here.
	for name, route := range cfg.Routes {
		header := "[routes." + name + "]"
		at := slices.Index(lines, header)
		if at < 1 {
			t.Errorf("no %s in what init wrote:\n%s", header, generated(t, repo))
			continue
		}
		want := "# https://" + identity.ComposeLabel("<context>", route.Label) + "." + defaultDomain
		if lines[at-1] != want {
			t.Errorf("above %s: %q, want %q", header, lines[at-1], want)
		}
	}
	// A port never gets a hostname, so nothing above one may offer it a URL.
	for name := range cfg.Ports {
		at := slices.Index(lines, "[ports."+name+"]")
		if at > 0 && strings.HasPrefix(lines[at-1], "# https://") {
			t.Errorf("[ports.%s] is promised a hostname it will never have: %q", name, lines[at-1])
		}
	}
}

// "studio" is supabase's word for it. Someone opening the hostname is looking
// for supabase, so the route says so rather than making them learn the
// difference.
func TestInitLabelsStudioAsSupabase(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "supabase"), "project_id = \"x\"\n")
	t.Chdir(repo)

	exercise(t, "init")

	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatal(err)
	}
	studio := cfg.Routes["studio"]
	if studio == nil {
		t.Fatalf("no studio route: %v", cfg.Routes)
	}
	if studio.Label != "supabase" {
		t.Errorf("studio label = %q, want %q", studio.Label, "supabase")
	}
}

// A service the stack turns off becomes a bare port, and grove refuses a label
// on one, so the label has to go with the hostname rather than the entry.
func TestInitDropsTheLabelWithTheHostname(t *testing.T) {
	repo := tempRepo(t, "app1")
	os.Remove(filepath.Join(repo, config.FileName))
	writeStack(t, filepath.Join(repo, "supabase"), "project_id = \"x\"\n[studio]\nenabled = false\n")
	t.Chdir(repo)

	exercise(t, "init")

	// Loading is the assertion: a label under [ports.studio] is a config error.
	cfg, err := config.Load(repo)
	if err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
	if cfg.Ports["studio"] == nil {
		t.Errorf("a disabled studio kept its hostname: %v", cfg.Routes)
	}
}
