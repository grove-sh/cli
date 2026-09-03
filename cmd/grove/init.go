package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/identity"
)

func newInitCommand() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a grove.toml for this project",
		Long: `Write a grove.toml at the root of this worktree.

What it finds becomes what it writes: a route per app, and the supabase stack's
ports if there is one. Read it before trusting it, since guessing which
variable an app reads its URL from is exactly that.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			root, project := projectRoot(dir)

			path := filepath.Join(root, config.FileName)
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%s already exists; pass --force to overwrite it", path)
			}

			contents, found := scaffold(root, project)
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "wrote %s\n", path)
			for _, note := range found {
				fmt.Fprintf(out, "  %s\n", note)
			}
			fmt.Fprintf(out, "\nRead it, then: grove exec -- <your dev command>\n")
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing grove.toml")
	return cmd
}

// projectRoot is the worktree root, so running init from a subdirectory still
// puts the file where every relative path in it resolves against, along with
// the name grove derives for it.
func projectRoot(dir string) (root, project string) {
	context, err := identity.Resolve(dir)
	if err != nil || context.Root == "" {
		return dir, filepath.Base(dir)
	}
	return context.Root, context.Project
}

type app struct {
	name   string
	dir    string
	urlFor string
}

func scaffold(root, project string) (string, []string) {
	var notes []string
	var b strings.Builder

	b.WriteString(`# grove.toml
# Every route below gets its own hostname, one per worktree:
#   <context>.grov.site, or <context>-<label>.grov.site
#
# name is what grove derived from this directory. Set it when they differ.
# name = "` + project + `"
`)

	if _, err := os.Stat(filepath.Join(root, ".env")); err == nil {
		b.WriteString("\nenv_files = [\".env\"]\n")
		notes = append(notes, ".env, which grove will load in place of dotenv")
	}

	stack, stackDir := findSupabase(root)
	if stack != nil {
		b.WriteString(`
# The supabase CLI reads these on its own, so its config.toml needs no edit.
# They are its automatic bindings: SUPABASE_ plus the config key path. A key
# renamed in a later CLI is a one line change here.
[env]
SUPABASE_PROJECT_ID = "{context.slug}"
`)
		notes = append(notes, "a supabase stack in "+stackDir+", whose ports grove will allocate")
	}

	apps, unsure := findApps(root)
	for _, found := range apps {
		fmt.Fprintf(&b, "\n[routes.%s]\n", found.name)
		if found.dir != "." {
			fmt.Fprintf(&b, "dir = %q\n", found.dir)
		}
		if len(apps) == 1 {
			b.WriteString("label = \"\"\n")
		}
		if found.urlFor == "" {
			fmt.Fprintf(&b, "env = { PORT = \"{port}\" }\n")
			fmt.Fprintf(&b, "# Add the variable this app reads its own URL from, if it has one:\n")
			fmt.Fprintf(&b, "# env = { PORT = \"{port}\", PUBLIC_SITE_URL = \"{url}\" }\n")
		} else {
			fmt.Fprintf(&b, "env = { PORT = \"{port}\", %s = \"{url}\" }\n", found.urlFor)
		}
		notes = append(notes, "an app in "+found.dir+", as route "+found.name)
	}

	for _, dir := range unsure {
		fmt.Fprintf(&b, "\n# %s has a dev script, but nothing grove recognises as a server.\n", dir)
		fmt.Fprintf(&b, "# Give it a route if it listens on a port:\n")
		fmt.Fprintf(&b, "# [routes.%s]\n# dir = %q\n# env = { PORT = \"{port}\" }\n", filepath.Base(dir), dir)
		notes = append(notes, dir+", which grove could not identify, left commented out")
	}

	if len(apps) == 0 && len(unsure) == 0 {
		b.WriteString(`
# No app turned up, so here is the shape of one.
# [routes.web]
# dir = "."
# label = ""
# env = { PORT = "{port}" }
`)
		notes = append(notes, "no app, so the route is commented out")
	}

	if stack != nil {
		b.WriteString(stack.entries())
	}
	return b.String(), notes
}

// findApps looks under apps/, which is where a monorepo puts the things that
// get deployed, and at the root for a single app repository. Libraries under
// packages/ are not apps: a watching type checker has a dev script too, and it
// serves nothing.
func findApps(root string) (found []app, unsure []string) {
	entries, err := os.ReadDir(filepath.Join(root, "apps"))
	if err == nil {
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			dir := filepath.Join("apps", entry.Name())
			serves, urlFor := serves(filepath.Join(root, dir))
			switch {
			case serves:
				found = append(found, app{name: entry.Name(), dir: dir, urlFor: urlFor})
			case hasDevScript(filepath.Join(root, dir)):
				unsure = append(unsure, dir)
			}
		}
	}

	if len(found) == 0 && len(unsure) == 0 {
		if serves, urlFor := serves(root); serves {
			found = append(found, app{name: "web", dir: ".", urlFor: urlFor})
		}
	}

	slices.SortFunc(found, func(a, b app) int { return strings.Compare(a.dir, b.dir) })
	slices.Sort(unsure)
	return found, unsure
}

type manifest struct {
	Scripts         map[string]string `json:"scripts"`
	Dependencies    map[string]string `json:"dependencies"`
	DevDependencies map[string]string `json:"devDependencies"`
}

// frameworks that serve HTTP, and the variable each conventionally reads its
// own public URL from. Guessing this is why init tells you to read what it
// wrote.
var frameworks = map[string]string{
	"next":           "NEXT_PUBLIC_SITE_URL",
	"nuxt":           "NUXT_PUBLIC_SITE_URL",
	"vite":           "VITE_SITE_URL",
	"astro":          "PUBLIC_SITE_URL",
	"@remix-run/dev": "PUBLIC_SITE_URL",
	"@sveltejs/kit":  "PUBLIC_SITE_URL",
}

// servers that listen without a convention for their own URL, so they get a
// port and nothing else.
var servers = []string{"express", "fastify", "hono", "koa", "@nestjs/core"}

func readManifest(dir string) (manifest, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return manifest{}, false
	}
	var parsed manifest
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return manifest{}, false
	}
	return parsed, true
}

// serves reports whether a directory holds something that listens on a port,
// judged by what it depends on rather than by having a dev script, since a
// library's dev script is a watching compiler.
func serves(dir string) (bool, string) {
	parsed, ok := readManifest(dir)
	if !ok {
		return false, ""
	}
	for _, deps := range []map[string]string{parsed.Dependencies, parsed.DevDependencies} {
		for name := range deps {
			if urlFor, isFramework := frameworks[name]; isFramework {
				return true, urlFor
			}
			if slices.Contains(servers, name) {
				return true, ""
			}
		}
	}
	return false, ""
}

func hasDevScript(dir string) bool {
	parsed, ok := readManifest(dir)
	return ok && parsed.Scripts["dev"] != ""
}

// supabase is what grove needs from a stack's config: which services are on,
// since a disabled one publishes no port.
type supabase struct {
	API       section `toml:"api"`
	DB        db      `toml:"db"`
	Studio    section `toml:"studio"`
	Analytics section `toml:"analytics"`
	Inbucket  section `toml:"inbucket"`
	LocalSMTP section `toml:"local_smtp"`
}

// A pointer distinguishes "off" from "not mentioned", and supabase's own
// defaults turn most of these on when the section is absent.
type section struct {
	Enabled *bool `toml:"enabled"`
}

type db struct {
	Pooler section `toml:"pooler"`
}

func (s section) on() bool { return s.Enabled == nil || *s.Enabled }

// findSupabase looks for a supabase directory at the root or one level under
// apps/ and packages/, which is where every layout puts it.
func findSupabase(root string) (*supabase, string) {
	candidates := []string{"."}
	for _, parent := range []string{"apps", "packages"} {
		entries, err := os.ReadDir(filepath.Join(root, parent))
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if entry.IsDir() {
				candidates = append(candidates, filepath.Join(parent, entry.Name()))
			}
		}
	}

	for _, candidate := range candidates {
		path := filepath.Join(root, candidate, "supabase", "config.toml")
		var parsed supabase
		if _, err := toml.DecodeFile(path, &parsed); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				continue
			}
			continue
		}
		return &parsed, filepath.Join(candidate, "supabase")
	}
	return nil, ""
}

// entries writes a route for each service that speaks HTTP and a bare port for
// each that does not, since routing a hostname at postgres would only produce
// a confusing failure.
func (s *supabase) entries() string {
	var b strings.Builder

	routes := []struct {
		name    string
		on      bool
		envName string
	}{
		{"studio", s.Studio.on(), "SUPABASE_STUDIO_PORT"},
		{"api", s.API.on(), "SUPABASE_API_PORT"},
		{"mail", s.Inbucket.on() && s.LocalSMTP.on(), "SUPABASE_INBUCKET_PORT"},
	}
	for _, route := range routes {
		if !route.on {
			continue
		}
		fmt.Fprintf(&b, "\n[routes.%s]\ndetached = true\n", route.name)
		if route.name == "mail" {
			// The key was renamed at CLI 2.108, and each version binds only
			// the one it knows, so both spellings are harmless and one works.
			fmt.Fprintf(&b, "env = { SUPABASE_INBUCKET_PORT = \"{port}\", SUPABASE_LOCAL_SMTP_PORT = \"{port}\" }\n")
			continue
		}
		fmt.Fprintf(&b, "env = { %s = \"{port}\" }\n", route.envName)
	}

	ports := []struct {
		name    string
		on      bool
		envName string
	}{
		{"db", true, "SUPABASE_DB_PORT"},
		{"shadow", true, "SUPABASE_DB_SHADOW_PORT"},
		// Unlike the rest, supabase ships this one off, so it takes an
		// explicit enabled = true.
		{"pooler", s.DB.Pooler.Enabled != nil && *s.DB.Pooler.Enabled, "SUPABASE_DB_POOLER_PORT"},
		{"analytics", s.Analytics.on(), "SUPABASE_ANALYTICS_PORT"},
	}
	for _, port := range ports {
		if !port.on {
			continue
		}
		fmt.Fprintf(&b, "\n[ports.%s]\ndetached = true\nenv = { %s = \"{port}\" }\n", port.name, port.envName)
	}
	return b.String()
}
