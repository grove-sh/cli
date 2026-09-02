// Package identity resolves the grove context for a directory: which project
// it belongs to, which worktree it is, and the DNS label that names it.
package identity

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	IsMain   bool   `json:"is_main"`
	Source   Source `json:"source"`
}

func (c Context) Host(domain string) string {
	return c.Slug + "." + domain
}

// Resolve reports the context for dir. GROVE_CONTEXT overrides everything.
func Resolve(dir string) (Context, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Context{}, err
	}

	if override := os.Getenv("GROVE_CONTEXT"); override != "" {
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
		// A bare repository has no worktree of its own, so every worktree
		// hanging off it is a linked one and carries a variant.
		IsMain: repo.bare == "" && repo.current == repo.main,
		Source: FromGit,
	}
	slug := project
	if !ctx.IsMain {
		ctx.Variant = slugify(filepath.Base(repo.current))
		if ctx.Variant == "" {
			return Context{}, fmt.Errorf("identity: %q does not slugify to a usable worktree name", repo.current)
		}
		slug = project + "-" + ctx.Variant
	}
	ctx.Slug = cap63(slug)
	return ctx, nil
}

var errNotARepository = errors.New("identity: not a git repository")

type repo struct {
	current string // worktree the caller is standing in
	main    string // main worktree, empty when the repository is bare
	bare    string // the bare repository, empty when it is not bare
}

// findRepo takes both paths from git so they stay comparable. Mixing in
// os.Getwd would break on macOS, where /tmp resolves through a symlink.
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

	// The main worktree is listed first. A bare repository takes that slot too,
	// marked "bare", and owns no working tree.
	first, _, _ := strings.Cut(list, "\n\n")
	rest, ok := strings.CutPrefix(first, "worktree ")
	if !ok {
		return repo{}, errors.New("identity: git worktree list named no worktree")
	}
	path, _, _ := strings.Cut(rest, "\n")
	root := filepath.Clean(path)

	for line := range strings.SplitSeq(first, "\n") {
		if line == "bare" {
			r.bare = root
			return r, nil
		}
	}
	r.main = root
	return r, nil
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

func cap63(slug string) string {
	if len(slug) <= maxLabel {
		return slug
	}
	sum := sha256.Sum256([]byte(slug))
	return strings.TrimRight(slug[:maxLabel-7], "-") + "-" + hex.EncodeToString(sum[:])[:6]
}
