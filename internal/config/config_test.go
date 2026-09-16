package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/config"
)

// Two apps on their own hostnames alongside a supabase stack whose ports
// outlive the command that starts it, which exercises every part of the schema.
const myappConfig = `
name = "myapp"
env_files = [".env"]

[routes.web]
dir = "apps/web"
label = ""
env = { PORT = "{port}", NEXT_PUBLIC_SITE_URL = "{url}" }

[routes.admin]
dir = "apps/admin"
env = { PORT = "{port}", VITE_SITE_URL = "{url}" }

[routes.studio]
detached = true
env = { SUPABASE_STUDIO_PORT = "{port}" }

[ports.db]
detached = true
env = { SUPABASE_DB_PORT = "{port}" }

[env]
SUPABASE_PROJECT_ID = "{context.slug}"
POSTGRES_URL = "postgresql://postgres:postgres@127.0.0.1:{db.port}/postgres"
`

func write(t *testing.T, dir, name, body string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, body string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, config.FileName, body)
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func loadErr(t *testing.T, body string) error {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, config.FileName, body)
	_, err := config.Load(dir)
	if err == nil {
		t.Fatal("expected an error")
	}
	return err
}

func TestLoadsTheProjectShape(t *testing.T) {
	cfg := load(t, myappConfig)

	if cfg.Name != "myapp" {
		t.Errorf("name = %q", cfg.Name)
	}
	if len(cfg.Routes) != 3 || len(cfg.Ports) != 1 {
		t.Fatalf("got %d routes and %d ports", len(cfg.Routes), len(cfg.Ports))
	}
	if label := cfg.Routes["web"].Label; label != "" {
		t.Errorf("label = %q, want the context's own hostname", label)
	}
	if label := cfg.Routes["admin"].Label; label != "admin" {
		t.Errorf("label = %q, want the route name", label)
	}
	if !cfg.Routes["studio"].Detached || cfg.Routes["web"].Detached {
		t.Error("detached did not survive the round trip")
	}
}

func TestFindWalksUp(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	deep := filepath.Join(root, "apps", "web", "src")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(deep)
	if err != nil {
		t.Fatal(err)
	}

	// Config reports resolved paths, as git does, so the expectation resolves
	// too. On macOS every temporary directory arrives through a symlink.
	want, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dir != want {
		t.Errorf("Dir = %q, want %q", cfg.Dir, want)
	}
}

func TestMissingConfig(t *testing.T) {
	if _, err := config.Load(t.TempDir()); !errors.Is(err, config.ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

// A silently ignored typo in a variable name is the drift grove exists to kill,
// so an unknown key is a failure to load, not a warning.
func TestUnknownKeysAreRejected(t *testing.T) {
	err := loadErr(t, `
[routes.web]
directory = "apps/web"
`)
	if !strings.Contains(err.Error(), "directory") {
		t.Errorf("error does not name the key: %v", err)
	}
}

func TestTwoRoutesCannotShareALabel(t *testing.T) {
	err := loadErr(t, `
[routes.web]
label = "app"

[routes.admin]
label = "app"
`)
	if !strings.Contains(err.Error(), "label") {
		t.Errorf("error does not explain the clash: %v", err)
	}
}

func TestPortsCannotHaveALabel(t *testing.T) {
	err := loadErr(t, `
[ports.db]
label = "db"
`)
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("error does not explain why: %v", err)
	}
}

func TestLabelOverridesTheRouteName(t *testing.T) {
	cfg := load(t, "[routes.dashboard]\nlabel = \"ui\"\n")

	if label := cfg.Routes["dashboard"].Label; label != "ui" {
		t.Errorf("label = %q, want ui", label)
	}
}

func TestExplicitNamesAreValidated(t *testing.T) {
	if err := loadErr(t, "[routes.web]\nlabel = \"app.one\"\n"); !strings.Contains(err.Error(), "dot") {
		t.Errorf("a dotted label was not rejected clearly: %v", err)
	}
}

func TestSelectByDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	// These have to exist: you can only run a command in a directory that
	// does, and resolving a path that does not is a different question.
	for _, dir := range []string{"apps/web/src/app", "apps/admin", "packages/db"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	for dir, want := range map[string]string{
		"apps/web":         "web",
		"apps/web/src/app": "web",
		"apps/admin":       "admin",
		"packages/db":      "",
		".":                "",
	} {
		entry, err := cfg.Select(filepath.Join(root, dir), "")
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case want == "" && entry != nil:
			t.Errorf("%s selected %q, want nothing", dir, entry.Name)
		case want != "" && (entry == nil || entry.Name != want):
			t.Errorf("%s selected %v, want %q", dir, entry, want)
		}
	}
}

func TestSelectByName(t *testing.T) {
	cfg := load(t, myappConfig)

	entry, err := cfg.Select(cfg.Dir, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Name != "admin" {
		t.Fatalf("got %v", entry)
	}

	if _, err := cfg.Select(cfg.Dir, "nope"); err == nil {
		t.Error("an unknown name was accepted")
	}
}

// A detached entry is never selected by directory: it is always active, so
// selecting it would double count it.
func TestDetachedEntriesAreNotSelectedByDirectory(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, `
[routes.studio]
dir = "packages/db"
detached = true
`)
	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	entry, err := cfg.Select(filepath.Join(root, "packages", "db"), "")
	if err != nil {
		t.Fatal(err)
	}
	if entry != nil {
		t.Errorf("selected %q", entry.Name)
	}
	if len(cfg.Detached()) != 1 {
		t.Errorf("Detached() = %v", cfg.Detached())
	}
}

func TestLocalFileOverrides(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	write(t, root, config.LocalName, `
[env]
POSTGRES_URL = "postgresql://postgres@127.0.0.1:5433/myapp"
`)

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	if got := cfg.Env["POSTGRES_URL"]; !strings.Contains(got, "5433") {
		t.Errorf("POSTGRES_URL = %q, want the local override", got)
	}
	if _, ok := cfg.Env["SUPABASE_PROJECT_ID"]; !ok {
		t.Error("the local file replaced [env] instead of merging into it")
	}
}

// A path reached through a symlink has to resolve to the same place as the one
// git reports, or a config path and a worktree path can never be compared.
// macOS reaches every temporary directory this way, since /var is a symlink.
func TestPathsResolveThroughSymlinks(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	write(t, real, config.FileName, myappConfig)
	if err := os.MkdirAll(filepath.Join(real, "apps", "web"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink here: %v", err)
	}

	cfg, err := config.Load(filepath.Join(link, "apps", "web"))
	if err != nil {
		t.Fatal(err)
	}

	resolved, err := filepath.EvalSymlinks(real)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dir != resolved {
		t.Errorf("Dir = %q, want %q", cfg.Dir, resolved)
	}

	// Selection has to work through the symlink too, since that is the path
	// the caller is standing in.
	entry, err := cfg.Select(filepath.Join(link, "apps", "web"), "")
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.Name != "web" {
		t.Errorf("selected %v, want web", entry)
	}
}

// A file from a later grove is expected to use keys this one has never heard
// of, so the schema is checked before they are held against it. Otherwise the
// message is a list of typos that are not typos.
func TestAFileFromTheFutureSaysSoRatherThanListingKeys(t *testing.T) {
	err := loadErr(t, "schema = 99\n\n[routes.web]\nsomething_new = true\n")

	if !strings.Contains(err.Error(), "upgrade grove") {
		t.Errorf("error does not say what to do: %v", err)
	}
	if strings.Contains(err.Error(), "something_new") {
		t.Errorf("error blames a key that is only unknown because grove is old: %v", err)
	}
}

// A file that declares nothing is a file from before the key existed, and
// those are the four that already exist.
func TestAFileWithNoSchemaStillLoads(t *testing.T) {
	cfg := load(t, "[routes.web]\nenv = { PORT = \"{port}\" }\n")

	if _, ok := cfg.Routes["web"]; !ok {
		t.Error("a file with no schema key did not load")
	}
}

// A single app whose directory is the repository has no dir to name, and init
// omits the key for exactly that shape. Before, such a route could never be
// selected by directory, so grove exec asked for nothing and failed.
func TestAnEntryWithNoDirCoversTheWholeProject(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, config.FileName, "[routes.web]\nlabel = \"\"\nenv.PORT = \"{port}\"\n")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, cwd := range []string{dir, filepath.Join(dir, "src", "deep")} {
		entry, err := cfg.Select(cwd, "")
		if err != nil {
			t.Fatal(err)
		}
		if entry == nil || entry.Name != "web" {
			t.Errorf("from %s: selected %v, want web", cwd, entry)
		}
	}
}

// Scoped beats unscoped, or adding a second app would silently move what a
// command in the first one binds.
func TestAScopedEntryBeatsOneCoveringTheProject(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, config.FileName, "[routes.web]\nlabel = \"\"\n\n[routes.admin]\ndir = \"apps/admin\"\n")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	inside, err := cfg.Select(filepath.Join(dir, "apps", "admin"), "")
	if err != nil {
		t.Fatal(err)
	}
	if inside == nil || inside.Name != "admin" {
		t.Errorf("inside apps/admin, selected %v, want admin", inside)
	}
	outside, err := cfg.Select(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if outside == nil || outside.Name != "web" {
		t.Errorf("at the root, selected %v, want web", outside)
	}
}

// Two entries claiming everything is a question grove cannot answer, and
// picking one would be picking at random.
func TestTwoEntriesCoveringTheProjectIsAnError(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, config.FileName, "[routes.web]\nlabel = \"\"\n\n[routes.admin]\nlabel = \"admin\"\n")
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}

	_, err = cfg.Select(dir, "")

	if err == nil || !strings.Contains(err.Error(), "-s") {
		t.Errorf("err = %v, want one naming the way out", err)
	}
}

// Overriding one variable locally used to replace the whole entry, taking the
// dir, label and detached from grove.toml with it.
func TestALocalEntryMergesIntoTheOneItNames(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	write(t, root, config.LocalName, `
[routes.admin]
env = { VITE_SITE_URL = "http://localhost:5173" }
`)

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	admin := cfg.Routes["admin"]
	if admin.Dir != "apps/admin" {
		t.Errorf("dir = %q, want the one from grove.toml", admin.Dir)
	}
	if admin.Detached {
		t.Error("detached came back true")
	}
	if got := admin.Env["VITE_SITE_URL"]; got != "http://localhost:5173" {
		t.Errorf("VITE_SITE_URL = %q, want the local override", got)
	}
	if _, ok := admin.Env["PORT"]; !ok {
		t.Error("the local entry replaced env instead of merging into it")
	}
}

// [routes.web] takes the context's own hostname with label = "", and a replaced
// entry read that as an omitted key and fell back to the name, moving the site
// to web.<context>.
func TestALocalOverrideKeepsAnEmptyLabel(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	write(t, root, config.LocalName, "[routes.web]\nenv = { PORT = \"3100\" }\n")

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	web := cfg.Routes["web"]
	if web.Label != "" {
		t.Errorf("label = %q, want the empty one from grove.toml", web.Label)
	}
	if web.Dir != "apps/web" {
		t.Errorf("dir = %q, want the one from grove.toml", web.Dir)
	}
	if got := web.Env["PORT"]; got != "3100" {
		t.Errorf("PORT = %q, want the local override", got)
	}
}

func TestALocalLabelLeavesTheRestOfTheEntry(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	write(t, root, config.LocalName, "[routes.studio]\nlabel = \"lab\"\n")

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	studio := cfg.Routes["studio"]
	if studio.Label != "lab" {
		t.Errorf("label = %q, want lab", studio.Label)
	}
	if !studio.Detached {
		t.Error("detached did not survive the local label")
	}
	if got := studio.Env["SUPABASE_STUDIO_PORT"]; got != "{port}" {
		t.Errorf("SUPABASE_STUDIO_PORT = %q, want the one from grove.toml", got)
	}
}

// The case pointer fields exist for: a plain bool cannot say "false" in a way
// that is distinguishable from saying nothing at all.
func TestALocalFileCanTurnDetachedOff(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	write(t, root, config.LocalName, "[ports.db]\ndetached = false\n")

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Ports["db"].Detached {
		t.Error("detached = false in the local file did nothing")
	}
	if got := cfg.Ports["db"].Env["SUPABASE_DB_PORT"]; got != "{port}" {
		t.Errorf("SUPABASE_DB_PORT = %q, want the one from grove.toml", got)
	}
}

// How a local file adds the one service only this machine runs.
func TestALocalEntryWithANewNameIsAdded(t *testing.T) {
	root := t.TempDir()
	write(t, root, config.FileName, myappConfig)
	write(t, root, config.LocalName, `
[ports.mailhog]
detached = true
env = { MAILHOG_PORT = "{port}" }
`)

	cfg, err := config.Load(root)
	if err != nil {
		t.Fatal(err)
	}

	mailhog, ok := cfg.Ports["mailhog"]
	if !ok {
		t.Fatal("the local-only port was dropped")
	}
	if !mailhog.Detached || mailhog.Env["MAILHOG_PORT"] != "{port}" {
		t.Errorf("mailhog = %+v", mailhog)
	}
	if _, ok := cfg.Ports["db"]; !ok {
		t.Error("adding a port replaced the ones in grove.toml")
	}
}

// Pointer fields still have to decode, or every key in them would land in
// md.Undecoded() and read as a typo.
func TestPointerFieldsStillRejectOnlyRealTypos(t *testing.T) {
	cfg := load(t, "[routes.web]\ndir = \"apps/web\"\ndetached = true\n")

	if cfg.Routes["web"].Dir != "apps/web" || !cfg.Routes["web"].Detached {
		t.Errorf("web = %+v", cfg.Routes["web"])
	}
	if err := loadErr(t, "[routes.web]\ndetatched = true\n"); !strings.Contains(err.Error(), "detatched") {
		t.Errorf("error does not name the key: %v", err)
	}
}

// A project that lists .env.local itself has said which tier it belongs in, so
// grove leaves it there rather than promoting it and changing what a file
// already in use means.
func TestAnOverrideFileTheProjectListsIsNotPromoted(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, config.FileName, "env_files = [\".env\", \".env.local\"]\n")
	write(t, dir, ".env.local", "API_KEY=from-file\n")

	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if names := cfg.OverrideFiles(); len(names) != 0 {
		t.Errorf("OverrideFiles = %v, want none", names)
	}

	overrides, err := cfg.LoadOverrideEnvFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(overrides) != 0 {
		t.Errorf("override tier = %v, want nothing", overrides)
	}
	files, err := cfg.LoadEnvFiles()
	if err != nil {
		t.Fatal(err)
	}
	if files["API_KEY"] != "from-file" {
		t.Errorf("API_KEY = %q, want the env_files tier to still read it", files["API_KEY"])
	}
}

// Nobody is required to have one, so its absence reads like a missing .env.
func TestAMissingOverrideFileIsNotAnError(t *testing.T) {
	cfg := load(t, myappConfig)

	overrides, err := cfg.LoadOverrideEnvFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(overrides) != 0 {
		t.Errorf("override tier = %v, want nothing", overrides)
	}
}

// Forging one of grove's own names in the tier that beats grove would have
// grove report a lease it does not hold, so the file is refused rather than
// half applied.
func TestTheOverrideFileCannotSetGrovesOwnNames(t *testing.T) {
	for _, name := range config.ReservedNames {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, config.FileName, "")
			write(t, dir, config.OverrideName, name+"=forged\n")

			cfg, err := config.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, err = cfg.LoadOverrideEnvFiles()
			if err == nil {
				t.Fatal("expected an error")
			}
			for _, want := range []string{config.OverrideName, name} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error does not name %s: %v", want, err)
				}
			}
		})
	}
}
