// Package config reads grove.toml: the routes a project serves, the ports it
// needs, and the environment those map onto.
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
	// Dir is the directory holding grove.toml. Every relative path resolves
	// against it, not against the caller's working directory.
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

	// Dir scopes an attached entry to one part of the tree, so running a dev
	// command inside it selects this entry with no flag.
	Dir string

	// Label is the hostname suffix. Routes only, empty means the context's own
	// hostname with no suffix.
	Label string

	// Detached says whatever binds this port outlives the command that asked
	// for it, as containers started by "supabase start" do.
	Detached bool

	Env map[string]string
}

func (e *Entry) Ref() string { return string(e.Kind) + "s." + e.Name }

type file struct {
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

// Find walks up from dir looking for grove.toml, the way git looks for .git.
// The path it reports is resolved, because git resolves the paths it reports
// too, and on macOS /var is a symlink to /private/var. Resolving at both
// boundaries is what lets a config path and a worktree path be compared.
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

// Load reads the grove.toml covering dir, along with an uncommitted
// grove.local.toml beside it.
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

// merge lets the uncommitted file override whole entries and individual env
// values, which is what machine specific differences look like in practice.
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

// All reports every entry, routes first, each sorted by name.
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

// Detached reports the entries that are always active, whatever the caller is
// running: their ports belong to something that outlives a single command.
func (c *Config) Detached() []*Entry {
	var out []*Entry
	for _, entry := range c.All() {
		if entry.Detached {
			out = append(out, entry)
		}
	}
	return out
}

// Select reports the attached entry a command should bind: the one named by
// -s, else the one whose dir contains cwd. Neither is an error; plenty of
// commands need no port at all.
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
	for _, entry := range c.All() {
		if entry.Detached || entry.Dir == "" || !within(c.Dir, entry.Dir, abs) {
			continue
		}
		// The deepest dir wins, so a nested app beats its parent.
		if best == nil || len(entry.Dir) > len(best.Dir) {
			best = entry
		}
	}
	return best, nil
}

// resolvePath reports an absolute path with symlinks followed. EvalSymlinks
// needs the whole path to exist, so this resolves the deepest part that does
// and keeps the rest: half-resolving would be worse than not resolving, since
// a resolved config directory would stop matching an unresolved one below it.
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
