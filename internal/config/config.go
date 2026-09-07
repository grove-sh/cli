// Package config reads grove.toml.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/grove-sh/cli/internal/identity"
)

const (
	FileName  = "grove.toml"
	LocalName = "grove.local.toml"
)

var ErrNotFound = errors.New("config: no grove.toml in this directory or any parent")

type Kind string

const (
	KindRoute Kind = "route"
	KindPort  Kind = "port"
)

type Config struct {
	// Every relative path resolves against this, not the caller's cwd.
	Dir      string
	Name     string
	EnvFiles []string
	Routes   map[string]*Entry
	Ports    map[string]*Entry
	Env      map[string]string
}

type Entry struct {
	Name string
	Kind Kind

	// Scopes an attached entry to part of the tree, so a command run inside it
	// selects this entry with no flag.
	Dir string

	// Hostname suffix, routes only. Empty means the context's own hostname.
	Label string

	// Whatever binds this port outlives the command that asked for it, as
	// containers started by "supabase start" do.
	Detached bool

	Env map[string]string
}

func (e *Entry) Ref() string { return string(e.Kind) + "s." + e.Name }

// Schema exists before anything writes it, because it cannot arrive late:
// unknown keys are an error, so a grove predating the key would choke on the
// first file that carried one.
const Schema = 1

type file struct {
	Schema   int               `toml:"schema"`
	Name     string            `toml:"name"`
	EnvFiles []string          `toml:"env_files"`
	Routes   map[string]*entry `toml:"routes"`
	Ports    map[string]*entry `toml:"ports"`
	Env      map[string]string `toml:"env"`
}

type entry struct {
	Dir      string            `toml:"dir"`
	Label    *string           `toml:"label"`
	Detached bool              `toml:"detached"`
	Env      map[string]string `toml:"env"`
}

// Find resolves the path it reports, because git resolves the paths it reports
// too and on macOS /var is a symlink. Resolving at both boundaries is what lets
// a config path and a worktree path be compared.
func Find(dir string) (string, error) {
	abs, err := resolvePath(dir)
	if err != nil {
		return "", err
	}
	for {
		candidate := filepath.Join(abs, FileName)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return "", ErrNotFound
		}
		abs = parent
	}
}

// Load also reads an uncommitted grove.local.toml beside the file it finds.
func Load(dir string) (*Config, error) {
	path, err := Find(dir)
	if err != nil {
		return nil, err
	}

	primary, err := decode(path)
	if err != nil {
		return nil, err
	}

	local, err := decode(filepath.Join(filepath.Dir(path), LocalName))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if local != nil {
		merge(primary, local)
	}

	return build(filepath.Dir(path), primary)
}

func decode(path string) (*file, error) {
	var decoded file
	md, err := toml.DecodeFile(path, &decoded)
	if err != nil {
		return nil, err
	}
	// Before the unknown-key check: a file from a later grove is expected to
	// carry keys this one has never heard of.
	if decoded.Schema > Schema {
		return nil, fmt.Errorf("%s: declares schema %d, and this grove understands %d; upgrade grove",
			filepath.Base(path), decoded.Schema, Schema)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, 0, len(undecoded))
		for _, key := range undecoded {
			keys = append(keys, key.String())
		}
		slices.Sort(keys)
		return nil, fmt.Errorf("%s: unknown %s: %s", filepath.Base(path), plural("key", len(keys)), strings.Join(keys, ", "))
	}
	return &decoded, nil
}

// Whole entries and individual env values, which is what a machine-specific
// difference looks like in practice.
func merge(base, local *file) {
	if local.Name != "" {
		base.Name = local.Name
	}
	if local.EnvFiles != nil {
		base.EnvFiles = local.EnvFiles
	}
	for name, e := range local.Routes {
		if base.Routes == nil {
			base.Routes = map[string]*entry{}
		}
		base.Routes[name] = e
	}
	for name, e := range local.Ports {
		if base.Ports == nil {
			base.Ports = map[string]*entry{}
		}
		base.Ports[name] = e
	}
	for key, value := range local.Env {
		if base.Env == nil {
			base.Env = map[string]string{}
		}
		base.Env[key] = value
	}
}

func build(dir string, f *file) (*Config, error) {
	cfg := &Config{
		Dir:      dir,
		Name:     f.Name,
		EnvFiles: f.EnvFiles,
		Env:      f.Env,
		Routes:   make(map[string]*Entry, len(f.Routes)),
		Ports:    make(map[string]*Entry, len(f.Ports)),
	}

	if cfg.Name != "" {
		normalized, err := identity.ValidateLabel(cfg.Name)
		if err != nil {
			return nil, err
		}
		cfg.Name = normalized
	}

	labels := map[string]string{}
	for name, raw := range f.Routes {
		if err := usableName(name); err != nil {
			return nil, err
		}
		built, err := buildEntry(name, KindRoute, raw)
		if err != nil {
			return nil, err
		}
		if owner, taken := labels[built.Label]; taken {
			return nil, fmt.Errorf("config: routes %q and %q both claim the hostname label %q", owner, name, built.Label)
		}
		labels[built.Label] = name
		cfg.Routes[name] = built
	}
	for name, raw := range f.Ports {
		if err := usableName(name); err != nil {
			return nil, err
		}
		if _, clash := cfg.Routes[name]; clash {
			return nil, fmt.Errorf("config: %q is both a route and a port, and a template naming it could mean either", name)
		}
		built, err := buildEntry(name, KindPort, raw)
		if err != nil {
			return nil, err
		}
		if raw.Label != nil {
			return nil, fmt.Errorf("config: [ports.%s] has a label, but only routes get a hostname", name)
		}
		cfg.Ports[name] = built
	}

	return cfg, validate(cfg)
}

// A template says {<name>.<field>}, so an entry called context would make
// {context.slug} mean two things.
func usableName(name string) error {
	if name == "context" {
		return fmt.Errorf("config: %q cannot be an entry name, since {context.slug} already means something", name)
	}
	return nil
}

func buildEntry(name string, kind Kind, raw *entry) (*Entry, error) {
	built := &Entry{
		Name:     name,
		Kind:     kind,
		Dir:      filepath.Clean(raw.Dir),
		Detached: raw.Detached,
		Env:      raw.Env,
	}
	if raw.Dir == "" {
		built.Dir = ""
	}

	label := name
	if raw.Label != nil {
		label = *raw.Label
	}
	if kind == KindRoute && label != "" {
		normalized, err := identity.ValidateLabel(label)
		if err != nil {
			return nil, fmt.Errorf("[routes.%s]: %w", name, err)
		}
		label = normalized
	}
	built.Label = label
	return built, nil
}

func validate(cfg *Config) error {
	for name := range cfg.Routes {
		if _, clash := cfg.Ports[name]; clash {
			return fmt.Errorf("config: %q is both a route and a port; each name leases its own port, so they cannot share one", name)
		}
	}
	for _, entry := range cfg.All() {
		if strings.HasPrefix(entry.Dir, "..") {
			return fmt.Errorf("config: [%s] dir %q points outside the project", entry.Ref(), entry.Dir)
		}
	}
	return checkTemplates(cfg)
}

// All puts routes first, each group sorted by name.
func (c *Config) All() []*Entry {
	out := make([]*Entry, 0, len(c.Routes)+len(c.Ports))
	for _, group := range []map[string]*Entry{c.Routes, c.Ports} {
		names := make([]string, 0, len(group))
		for name := range group {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			out = append(out, group[name])
		}
	}
	return out
}

// Detached entries are always active, whatever the caller is running.
func (c *Config) Detached() []*Entry {
	var out []*Entry
	for _, entry := range c.All() {
		if entry.Detached {
			out = append(out, entry)
		}
	}
	return out
}

// Select prefers -s over the entry whose dir contains cwd. Finding neither is
// not an error: plenty of commands need no port at all.
func (c *Config) Select(cwd, named string) (*Entry, error) {
	if named != "" {
		if entry, ok := c.Routes[named]; ok {
			return entry, nil
		}
		if entry, ok := c.Ports[named]; ok {
			return entry, nil
		}
		return nil, fmt.Errorf("config: no route or port named %q", named)
	}

	abs, err := resolvePath(cwd)
	if err != nil {
		return nil, err
	}
	var best *Entry
	var unscoped []*Entry
	for _, entry := range c.All() {
		if entry.Detached {
			continue
		}
		// No dir means the whole project, as a single app at the repository
		// root has. It applies only where nothing scoped does.
		if entry.Dir == "" {
			unscoped = append(unscoped, entry)
			continue
		}
		if !within(c.Dir, entry.Dir, abs) {
			continue
		}
		// The deepest dir wins, so a nested app beats its parent.
		if best == nil || len(entry.Dir) > len(best.Dir) {
			best = entry
		}
	}
	if best != nil {
		return best, nil
	}
	switch len(unscoped) {
	case 0:
		return nil, nil
	case 1:
		return unscoped[0], nil
	}
	// Two entries claiming the whole project: picking one would be at random.
	names := make([]string, 0, len(unscoped))
	for _, entry := range unscoped {
		names = append(names, entry.Name)
	}
	return nil, fmt.Errorf("config: %s all have no dir, so each claims this whole project; name one with -s", strings.Join(names, ", "))
}

// EvalSymlinks needs the whole path to exist, so this resolves the deepest part
// that does and keeps the rest. Half-resolving would be worse than not
// resolving: a resolved config dir would stop matching an unresolved one below.
func resolvePath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}

	remainder := ""
	for current := abs; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remainder), nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			return abs, nil
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

func within(root, relative, target string) bool {
	rel, err := filepath.Rel(filepath.Join(root, relative), target)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

func plural(word string, n int) string {
	if n == 1 {
		return word
	}
	return word + "s"
}
