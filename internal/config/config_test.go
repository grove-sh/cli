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
POSTGRES_URL = "postgresql://postgres:postgres@127.0.0.1:{ports.db}/postgres"
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
