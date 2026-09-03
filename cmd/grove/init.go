package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
	name string
	dir  string

	// prefix is what this app's framework puts on a browser-visible variable,
	// so the same one names both its own URL and the API's.
	prefix string
}

func (a app) siteURLVar() string {
	if a.prefix == "" {
		return ""
	}
	return a.prefix + "SITE_URL"
}

func (a app) supabaseURLVar() string {
	if a.prefix == "" {
		return ""
	}
	return a.prefix + "SUPABASE_URL"
}

func scaffold(root, project string) (string, []string) {
	var notes []string
	var b strings.Builder

	b.WriteString(`# grove.toml
# Every route below gets its own hostname, one per worktree:
#   <context>.grov.site, or <context>-<label>.grov.site
#
# The project name comes from this directory. Uncomment to override it, which
# is worth doing when the directory and the project disagree.
# name = "example"
`)

	if _, err := os.Stat(filepath.Join(root, ".env")); err == nil {
		b.WriteString("\nenv_files = [\".env\"]\n")
		notes = append(notes, ".env, which grove will load in place of dotenv")
	}

	notes = append(notes, "the name "+project+", from this directory")

	apps, unsure := findApps(root)
	stack, stackDir := findSupabase(root)

	// A URL belongs in [env] rather than on its route: [env] applies to every
	// command, while a route's own variables apply only when that route is the
	// one being bound, and a build binds nothing. Ports stay on the route,
	// since a port means nothing without a lease.
	urls := map[string]string{}
	shared := map[string]bool{}
	claim := func(name, template string) {
		if name == "" {
			return
		}
		if existing, taken := urls[name]; taken {
			if existing != template {
				shared[name] = true
			}
			return
		}
		urls[name] = template
	}
	for _, found := range apps {
		claim(found.siteURLVar(), "{routes."+found.name+".url}")
		// Every app in a project talks to the same stack, so they agree on
		// this one and it does not count as a clash.
		if stack {
			claim(found.supabaseURLVar(), "{routes.api.url}")
		}
	}

	if stack || len(urls) > 0 {
		b.WriteString("\n[env]\n")
	}
	if stack {
		b.WriteString(`# The supabase CLI reads these on its own, so its config.toml needs no edit.
# They are its automatic bindings: SUPABASE_ plus the config key path. A key
# renamed in a later CLI is a one line change here.
SUPABASE_PROJECT_ID = "{context.slug}"

# The stack brings its own postgres, on the port grove allocated for it. The
# password is supabase's local default rather than a secret.
POSTGRES_URL = "postgres://postgres:postgres@localhost:{ports.db}/postgres"
`)
		notes = append(notes, "a supabase stack in "+stackDir+", whose ports grove will allocate")
	}
	for _, name := range sortedKeys(urls) {
		if shared[name] {
			continue
		}
		fmt.Fprintf(&b, "%s = %q\n", name, urls[name])
	}

	for _, found := range apps {
		fmt.Fprintf(&b, "\n[routes.%s]\n", found.name)
		if found.dir != "." {
			fmt.Fprintf(&b, "dir = %q\n", found.dir)
		}
		if len(apps) == 1 {
			b.WriteString("label = \"\"\n")
		}
		switch {
		case found.siteURLVar() == "":
			fmt.Fprintf(&b, "env = { PORT = \"{port}\" }\n")
			fmt.Fprintf(&b, "# Add the variable this app reads its own URL from, if it has one:\n")
			fmt.Fprintf(&b, "# %s = \"{routes.%s.url}\" under [env] above\n", "PUBLIC_SITE_URL", found.name)
		case shared[found.siteURLVar()]:
			// Two apps naming the same variable cannot both put it in [env].
			fmt.Fprintf(&b, "env = { PORT = \"{port}\", %s = \"{url}\" }\n", found.siteURLVar())
			fmt.Fprintf(&b, "# %s is shared with another app, so it stays here rather than\n", found.siteURLVar())
			fmt.Fprintf(&b, "# in [env], and only applies while this route is the one bound.\n")
		default:
			fmt.Fprintf(&b, "env = { PORT = \"{port}\" }\n")
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

	if stack {
		b.WriteString(supabaseEntries())
	}
	return b.String(), notes
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
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
			serves, prefix := serves(filepath.Join(root, dir))
			switch {
			case serves:
				found = append(found, app{name: entry.Name(), dir: dir, prefix: prefix})
			case hasDevScript(filepath.Join(root, dir)):
				unsure = append(unsure, dir)
			}
		}
	}

	if len(found) == 0 && len(unsure) == 0 {
		if serves, prefix := serves(root); serves {
			found = append(found, app{name: "web", dir: ".", prefix: prefix})
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

// frameworks that serve HTTP, and the prefix each puts on a variable it will
// expose to the browser. Guessing this is why init tells you to read what it
// wrote.
var frameworks = map[string]string{
	"next":           "NEXT_PUBLIC_",
	"nuxt":           "NUXT_PUBLIC_",
	"vite":           "VITE_",
	"astro":          "PUBLIC_",
	"@remix-run/dev": "PUBLIC_",
	"@sveltejs/kit":  "PUBLIC_",
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
			if prefix, isFramework := frameworks[name]; isFramework {
				return true, prefix
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

// findSupabase reports the stack directory, looking at the root and one level
// under apps/ and packages/, which is where every layout puts it.
func findSupabase(root string) (found bool, dir string) {
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
		if _, err := os.Stat(filepath.Join(root, candidate, "supabase", "config.toml")); err == nil {
			return true, filepath.Join(candidate, "supabase")
		}
	}
	return false, ""
}

// supabaseEntries allocates for every port the stack can publish, without
// consulting the enabled flags in its config. Kong publishes its port even
// with "[api] enabled = false", so a flag is not a reliable signal, and the
// costs are not symmetric: a spare allocation is one number out of a thousand,
// while a missing one collides with whatever worktree started first.
func supabaseEntries() string {
	return `
# Ports for the supabase stack. Grove allocates all of them, because supabase
# publishes some regardless of the enabled flags in its config, and an unused
# allocation costs nothing next to a collision between two worktrees. The
# variable names are the supabase CLI's own automatic bindings, so its
# config.toml needs no edit.

[routes.api]
detached = true
env = { SUPABASE_API_PORT = "{port}" }

[routes.studio]
detached = true
env = { SUPABASE_STUDIO_PORT = "{port}" }

[routes.mail]
detached = true
# The key was renamed at CLI 2.108, and each version binds only the one it
# knows, so both spellings are harmless and one of them lands.
env = { SUPABASE_INBUCKET_PORT = "{port}", SUPABASE_LOCAL_SMTP_PORT = "{port}" }

[ports.db]
detached = true
env = { SUPABASE_DB_PORT = "{port}" }

[ports.shadow]
detached = true
env = { SUPABASE_DB_SHADOW_PORT = "{port}" }

[ports.pooler]
detached = true
env = { SUPABASE_DB_POOLER_PORT = "{port}" }

[ports.analytics]
detached = true
env = { SUPABASE_ANALYTICS_PORT = "{port}" }
`
}
