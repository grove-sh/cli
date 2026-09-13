// Package identity resolves the grove context for a directory.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// A DNS label may not exceed 63 bytes, and neither may a postgres identifier,
// so one cap covers both.
const maxLabel = 63

var ErrBareRepository = errors.New("identity: a bare repository is not a context; cd into one of its worktrees, see git worktree list")

type Source string

const (
	FromGit      Source = "git"
	FromDir      Source = "dir"
	FromOverride Source = "override"
)

type Context struct {
	Project  string `json:"project"`
	Variant  string `json:"variant,omitempty"`
	Slug     string `json:"slug"`
	Root     string `json:"root"`
	MainRoot string `json:"main_root,omitempty"`
	BareRoot string `json:"bare_root,omitempty"`

	// The context that is the project itself: the main clone where there is
	// one, a worktree of a bare layout where there is not. A main_root does
	// not follow from it; the dir and override sources have none either.
	IsMain bool   `json:"is_main"`
	Source Source `json:"source"`
}

func (c Context) Host(domain string) string {
	return c.Slug + "." + domain
}

// A name in grove.toml wins over the directory the repository sits in, but not
// over GROVE_CONTEXT_OVERRIDE, which replaces the whole context.
func (c Context) WithProject(name string) (Context, error) {
	if name == "" || c.Source == FromOverride {
		return c, nil
	}
	normalized, err := ValidateLabel(name)
	if err != nil {
		return Context{}, err
	}
	c.Project = normalized
	c.Slug = composeSlug(normalized, c.Variant)
	return c, nil
}

func composeSlug(project, variant string) string {
	if variant == "" {
		return cap63(project)
	}
	return cap63(project + "-" + variant)
}

// GROVE_CONTEXT_OVERRIDE, not GROVE_CONTEXT: the plain name is exported into
// every command, so an override sharing it would follow a process tree into
// other worktrees and quietly answer for them.
func Resolve(dir string) (Context, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Context{}, err
	}

	if override := os.Getenv("GROVE_CONTEXT_OVERRIDE"); override != "" {
		slug, err := validateExplicit(override)
		if err != nil {
			return Context{}, err
		}
		return Context{
			Project: slug,
			Slug:    slug,
			Root:    abs,
			IsMain:  true,
			Source:  FromOverride,
		}, nil
	}

	repo, err := findRepo(abs)
	if err != nil {
		if !errors.Is(err, errNotARepository) {
			return Context{}, err
		}
		project := slugify(filepath.Base(abs))
		if project == "" {
			return Context{}, fmt.Errorf("identity: %q does not slugify to a usable name", abs)
		}
		return Context{
			Project: project,
			Slug:    cap63(project),
			Root:    abs,
			IsMain:  true,
			Source:  FromDir,
		}, nil
	}

	origin, project := repo.main, ""
	if repo.bare != "" {
		origin, project = repo.bare, projectFromBareDir(repo.bare)
	} else {
		project = slugify(filepath.Base(repo.main))
	}
	if project == "" {
		return Context{}, fmt.Errorf("identity: %q does not slugify to a usable project name", origin)
	}

	ctx := Context{
		Project:  project,
		Root:     repo.current,
		MainRoot: repo.main,
		BareRoot: repo.bare,
		// A bare repository owns no worktree, so which linked one stands in
		// for the main one is settled below.
		IsMain: repo.bare == "" && repo.current == repo.main,
		Source: FromGit,
	}
	if !ctx.IsMain {
		ctx.Variant = slugify(filepath.Base(repo.current))
		if ctx.Variant == "" {
			return Context{}, fmt.Errorf("identity: %q does not slugify to a usable worktree name", repo.current)
		}
		if repo.bare != "" && isTheProjectItself(repo) {
			ctx.Variant = ""
			ctx.IsMain = true
		}
	}
	ctx.Slug = composeSlug(project, ctx.Variant)
	return ctx, nil
}

var errNotARepository = errors.New("identity: not a git repository")

type repo struct {
	current string // worktree the caller is standing in
	main    string // main worktree, empty when the repository is bare
	bare    string // the bare repository, empty when it is not bare
}

// Both paths come from git so they stay comparable: mixing in os.Getwd breaks
// on macOS, where /tmp resolves through a symlink.
func findRepo(dir string) (repo, error) {
	out, err := git(dir, "rev-parse", "--path-format=absolute", "--show-toplevel")
	if err != nil {
		if bare, bareErr := git(dir, "rev-parse", "--is-bare-repository"); bareErr == nil && bare == "true" {
			return repo{}, ErrBareRepository
		}
		return repo{}, errNotARepository
	}
	r := repo{current: filepath.Clean(out)}

	list, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return repo{}, err
	}
	found := parseWorktrees(list)
	if len(found) == 0 {
		return repo{}, errors.New("identity: git worktree list named no worktree")
	}
	if first := found[0]; first.bare {
		r.bare = first.path
	} else {
		r.main = first.path
	}
	return r, nil
}

type worktree struct {
	path string
	bare bool
}

// The main worktree comes first in git worktree list --porcelain, and a bare
// repository takes that slot while owning no working tree, which bare marks.
func parseWorktrees(list string) []worktree {
	var found []worktree
	for _, block := range strings.Split(strings.TrimSpace(list), "\n\n") {
		lines := strings.Split(block, "\n")
		path, ok := strings.CutPrefix(lines[0], "worktree ")
		if !ok {
			continue
		}
		found = append(found, worktree{path: filepath.Clean(path), bare: slices.Contains(lines, "bare")})
	}
	return found
}

// projectFromBareDir handles both layouts in the wild, which put the name one
// level out: app1.git, and app1/.bare beside the worktrees.
func projectFromBareDir(dir string) string {
	name := strings.TrimSuffix(filepath.Base(dir), ".git")
	if name == "" || strings.HasPrefix(name, ".") {
		name = filepath.Base(filepath.Dir(dir))
	}
	return slugify(name)
}

// A bare layout has no main clone to be the project itself, so one of its
// worktrees is. grove.mainWorktree names it, and lives in the repository's
// config rather than a file in one of them: every worktree reads the one value
// there, where a grove.toml committed to two branches could name two different
// worktrees and hand both one hostname and one set of ports.
func isTheProjectItself(r repo) bool {
	chosen, err := git(r.current, "config", "--get", "grove.mainWorktree")
	if err != nil || chosen == "" {
		return onDefaultBranch(r.bare, r.current)
	}
	// Slugified, not validated: it names a directory, and a worktree called
	// v1.2 is a real one whose variant is v1-2.
	return slugify(chosen) == slugify(filepath.Base(r.current))
}

// MainWorktree describes which worktree of a bare layout is the project itself
// and what is wrong with that. Resolve asks the narrower question of whether
// the worktree it stands in is the one, and answers it without listing
// anything.
type MainWorktree struct {
	// False for a repository with a main clone, which is always the project.
	Bare bool

	// grove.mainWorktree, empty when it is not set.
	Setting string

	// The directory that is the project. Empty when none is: a setting naming
	// no worktree, or a default branch checked out in none of them.
	Worktree string

	// The worktree directories, listed only when none of them is the project,
	// since naming one is the fix.
	Candidates []string

	// What decided it: grove.mainWorktree, origin/HEAD, or HEAD.
	From string

	// A setting in this repository's own config that it has a main clone and
	// so never reads. Not said of a global one, which is a preference about
	// bare layouts rather than a mistake about this repository.
	Ignored bool
}

// ReadMainWorktree resolves every worktree rather than re-deciding which is
// the project, so what doctor reports cannot drift from what grove does.
func ReadMainWorktree(dir string) MainWorktree {
	var m MainWorktree

	list, err := git(dir, "worktree", "list", "--porcelain")
	if err != nil {
		return m
	}
	m.Setting, _ = git(dir, "config", "--get", "grove.mainWorktree")

	var paths []string
	bare := ""
	for _, w := range parseWorktrees(list) {
		if w.bare {
			m.Bare = true
			bare = w.path
			continue
		}
		paths = append(paths, w.path)
	}

	if !m.Bare {
		local, err := git(dir, "config", "--local", "--get", "grove.mainWorktree")
		m.Ignored = err == nil && local != ""
		return m
	}

	m.From = "grove.mainWorktree"
	if m.Setting == "" {
		_, m.From = defaultBranch(bare)
	}

	for _, path := range paths {
		// FromGit only: under GROVE_CONTEXT_OVERRIDE every directory resolves
		// to the same pinned context, which would name the first one asked.
		if ctx, err := Resolve(path); err == nil && ctx.Source == FromGit && ctx.IsMain {
			m.Worktree = filepath.Base(path)
			return m
		}
	}
	for _, path := range paths {
		m.Candidates = append(m.Candidates, filepath.Base(path))
	}
	return m
}

func onDefaultBranch(bare, worktree string) bool {
	branch, _ := defaultBranch(bare)
	if branch == "" {
		return false
	}
	// --quiet: a detached worktree is not on the default branch, which is an
	// answer rather than a failure worth a line on stderr.
	current, err := git(worktree, "symbolic-ref", "--quiet", "HEAD")
	return err == nil && current == branch
}

// defaultBranch names the branch a bare repository defaults to, and what said
// so. origin/HEAD is the remote's default as the clone recorded it, and costs
// no network call. HEAD is only loosely the same thing: git clone --bare
// copies it from the remote, but a repository converted in place keeps
// whatever its working checkout was on, so it is the fallback and not the
// answer.
func defaultBranch(bare string) (branch, source string) {
	const remote = "refs/remotes/origin/"
	if ref, err := git(bare, "symbolic-ref", "--quiet", remote+"HEAD"); err == nil {
		if name, ok := strings.CutPrefix(ref, remote); ok {
			return "refs/heads/" + name, "origin/HEAD"
		}
	}
	head, err := git(bare, "symbolic-ref", "--quiet", "HEAD")
	if err != nil {
		return "", ""
	}
	return head, "HEAD"
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}

// slugify maps a name grove derived itself onto a DNS label, quietly. Names the
// user typed go through validateExplicit and are rejected instead.
func slugify(s string) string {
	var b strings.Builder
	dashed := true
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dashed = false
		case !dashed:
			b.WriteByte('-')
			dashed = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func validateExplicit(v string) (string, error) {
	return ValidateLabel(v)
}

// ValidateLabel checks a name the user typed, rather than one grove derived.
// Explicit names are rejected on anything questionable; derived ones go through
// slugify and are cleaned up quietly.
func ValidateLabel(v string) (string, error) {
	if strings.Contains(v, ".") {
		return "", fmt.Errorf("identity: %q contains a dot, but a grove hostname is one label under the domain", v)
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return "", fmt.Errorf("identity: %q contains %q; use letters, digits, hyphen or underscore", v, r)
		}
	}
	s := strings.ToLower(strings.ReplaceAll(v, "_", "-"))
	if s == "" || strings.HasPrefix(s, "-") || strings.HasSuffix(s, "-") {
		return "", fmt.Errorf("identity: %q is not a usable DNS label", v)
	}
	if len(s) > maxLabel {
		return "", fmt.Errorf("identity: %q is %d bytes; a DNS label allows %d", v, len(s), maxLabel)
	}
	return s, nil
}

// ComposeLabel joins a context slug and a route's label into the single DNS
// label a hostname uses. The cap belongs here rather than on the slug alone,
// since it is the composed value that has to fit in 63 bytes.
func ComposeLabel(slug, label string) string {
	if label == "" {
		return cap63(slug)
	}
	return cap63(slug + "-" + label)
}

func cap63(slug string) string {
	if len(slug) <= maxLabel {
		return slug
	}
	sum := sha256.Sum256([]byte(slug))
	return strings.TrimRight(slug[:maxLabel-7], "-") + "-" + hex.EncodeToString(sum[:])[:6]
}
