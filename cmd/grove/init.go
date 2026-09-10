package main

import (
	"encoding/json"
	"fmt"
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

// The worktree root, so init from a subdirectory writes where relative paths resolve.
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

	// One prefix names both this app's URL variable and the API's.
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

	b.WriteString(`# The project name comes from this directory. This overrides it
# name = "example"
`)

	if _, err := os.Stat(filepath.Join(root, ".env")); err == nil {
		b.WriteString("\n# Loaded before the dynamic overrides below\nenv_files = [\".env\"]\n")
		notes = append(notes, ".env, which grove will load in place of dotenv")
	}

	notes = append(notes, "the name "+project+", from this directory")

	apps, unsure := findApps(root)
	stack, stackDir := findSupabase(root)
	var services []supabaseService
	var demoted []string
	var flags supabaseFlags
	if stack {
		flags = readSupabaseFlags(filepath.Join(root, stackDir, "config.toml"))
		services, demoted = supabaseServices(flags)
	}
	routed := func(name string) bool {
		for _, service := range services {
			if service.name == name {
				return service.routed
			}
		}
		return false
	}

	// Everything that is always set goes in [env], and only what is scoped
	// stays on an entry. An env block on a route applies while that route is
	// bound and a build binds nothing, which is why URLs cannot live there. An
	// env block on a detached entry applies always, which reads as scoped and
	// is not, so those go up too and leave the entry declaring a port and
	// nothing else. What is left on a route is its own port, which is the one
	// variable that really does belong to one command.
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
		claim(found.siteURLVar(), "{"+found.name+".url}")
		// Every app talks to the same stack, so agreeing here is not a clash.
		// A disabled api has no hostname to point at.
		if routed("api") {
			claim(found.supabaseURLVar(), "{api.url}")
		}
	}

	if stack || len(urls) > 0 {
		b.WriteString("\n# Set for every command in this project, whatever it runs\n[env]\n")
	}

	// Two groups, because they are read by different things. These are the
	// project's own variables, the ones its code goes looking for, and they say
	// nothing about which tool publishes the ports behind them.
	mine := map[string]string{}
	if stack {
		mine["POSTGRES_URL"] = "postgres://postgres:postgres@localhost:{db.port}/postgres"
	}
	for name, template := range urls {
		if !shared[name] {
			mine[name] = template
		}
	}
	for _, name := range sortedKeys(mine) {
		fmt.Fprintf(&b, "%s = %q\n", name, mine[name])
	}

	// And these are the supabase CLI's, which nothing in the project reads:
	// they are how the stack is told which ports to publish. Kept in the order
	// the entries appear below, so the two halves of the file agree.
	if stack {
		if len(mine) > 0 {
			b.WriteString("\n")
		}
		b.WriteString("SUPABASE_PROJECT_ID = \"{context.slug}\"\n")
		for _, service := range services {
			for _, name := range service.ports {
				fmt.Fprintf(&b, "%s = %q\n", name, "{"+service.name+".port}")
			}
		}
		notes = append(notes, "a supabase stack in "+stackDir+", whose ports grove will allocate")
		if len(demoted) > 0 {
			notes = append(notes, "no hostname for "+strings.Join(demoted, ", ")+", which the stack has turned off")
		}
	}

	for _, found := range apps {
		// The only app takes the context's own hostname; two of them cannot.
		label := found.name
		if len(apps) == 1 {
			label = ""
		}
		b.WriteString("\n" + routeURL(label))
		fmt.Fprintf(&b, "[routes.%s]\n", found.name)
		if found.dir != "." {
			fmt.Fprintf(&b, "dir = %q\n", found.dir)
		}
		if len(apps) == 1 {
			b.WriteString("label = \"\"\n")
		}
		switch {
		case found.siteURLVar() == "":
			b.WriteString("env.PORT = \"{port}\"\n")
			fmt.Fprintf(&b, "# This app's own URL, if it reads one: PUBLIC_SITE_URL = \"{%s.url}\" under [env]\n", found.name)
		case shared[found.siteURLVar()]:
			// Two apps naming the same variable cannot both put it in [env].
			fmt.Fprintf(&b, "# %s is shared with another app, so it stays here: it applies only while this route is bound.\n", found.siteURLVar())
			b.WriteString("env.PORT = \"{port}\"\n")
			fmt.Fprintf(&b, "env.%s = \"{url}\"\n", found.siteURLVar())
		default:
			b.WriteString("env.PORT = \"{port}\"\n")
		}
		notes = append(notes, "an app in "+found.dir+", as route "+found.name)
	}

	for _, dir := range unsure {
		fmt.Fprintf(&b, "\n# %s has a dev script, but nothing grove recognises as a server. If it listens:\n", dir)
		b.WriteString(routeURL(filepath.Base(dir)))
		fmt.Fprintf(&b, "# [routes.%s]\n# dir = %q\n# env.PORT = \"{port}\"\n", filepath.Base(dir), dir)
		notes = append(notes, dir+", which grove could not identify, left commented out")
	}

	if len(apps) == 0 && len(unsure) == 0 {
		b.WriteString("\n# No app turned up, so here is the shape of one.\n")
		b.WriteString(routeURL(""))
		b.WriteString(`# [routes.web]
# dir = "."
# label = ""
# env.PORT = "{port}"
`)
		notes = append(notes, "no app, so the route is commented out")
	}

	if stack {
		b.WriteString(supabaseEntries(services))
	}
	return b.String(), notes
}

// The hostname a route will answer on, written above it. The context is the
// one part init cannot fill in, since every worktree gets its own; the rest is
// composed the way exec composes it, so the comment cannot promise a URL that
// differs from the one the route answers on.
func routeURL(label string) string {
	return fmt.Sprintf("# https://%s.%s\n", identity.ComposeLabel("<context>", label), defaultDomain)
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	slices.Sort(out)
	return out
}

// packages/ is skipped: a library's dev script is a watching compiler, not a server.
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

// HTTP-serving frameworks, and the prefix each puts on a browser-visible variable.
var frameworks = map[string]string{
	"next":           "NEXT_PUBLIC_",
	"nuxt":           "NUXT_PUBLIC_",
	"vite":           "VITE_",
	"astro":          "PUBLIC_",
	"@remix-run/dev": "PUBLIC_",
	"@sveltejs/kit":  "PUBLIC_",
}

// Servers with no convention for naming their own URL, so they get only a port.
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

// Judged by dependencies rather than by a dev script, which a library has too.
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

// routed says the service earns a hostname, which only a browser-facing one does.
type supabaseService struct {
	name   string
	routed bool

	// The hostname this service answers on, where the entry name would read
	// worse. Empty means the entry name, which is what grove defaults to.
	label string

	// Variable names only. Every one of these takes this entry's port, and the
	// entries are all detached, so they are written into [env] rather than onto
	// the entry: see the note there about which scope an env block has.
	ports []string
}

// Every port is allocated even for a service the config disables: kong
// publishes its own with "[api] enabled = false", and a spare port costs less
// than a collision. Hostnames are the reverse, so a disabled service loses one.
func supabaseServices(flags supabaseFlags) (services []supabaseService, demoted []string) {
	services = []supabaseService{
		{name: "api", routed: true, ports: []string{"SUPABASE_API_PORT"}},
		// "studio" is what supabase calls it; "supabase" is what someone
		// opening the hostname is looking for.
		{name: "studio", routed: true, label: "supabase", ports: []string{"SUPABASE_STUDIO_PORT"}},
		{name: "mail", routed: true, ports: []string{"SUPABASE_LOCAL_SMTP_PORT"}},
		{name: "db", ports: []string{"SUPABASE_DB_PORT"}},
		{name: "shadow", ports: []string{"SUPABASE_DB_SHADOW_PORT"}},
		{name: "pooler", ports: []string{"SUPABASE_DB_POOLER_PORT"}},
		{name: "analytics", ports: []string{"SUPABASE_ANALYTICS_PORT"}},
	}

	for i := range services {
		if services[i].routed && flags.off[services[i].name] {
			services[i].routed = false
			demoted = append(demoted, services[i].name)
		}
	}
	return services, demoted
}

type supabaseFlags struct {
	off map[string]bool
}

// Only an explicit false turns a service off, so an unreadable config changes nothing.
func readSupabaseFlags(path string) supabaseFlags {
	var raw struct {
		API      struct{ Enabled *bool } `toml:"api"`
		Studio   struct{ Enabled *bool } `toml:"studio"`
		Inbucket struct{ Enabled *bool } `toml:"inbucket"`
		// The section was renamed along with the port key at CLI 2.108.
		LocalSMTP struct{ Enabled *bool } `toml:"local_smtp"`
	}
	if _, err := toml.DecodeFile(path, &raw); err != nil {
		return supabaseFlags{}
	}

	flags := supabaseFlags{off: map[string]bool{}}
	disabled := func(name string, flag *bool) {
		if flag != nil && !*flag {
			flags.off[name] = true
		}
	}
	disabled("api", raw.API.Enabled)
	disabled("studio", raw.Studio.Enabled)
	disabled("mail", raw.Inbucket.Enabled)
	disabled("mail", raw.LocalSMTP.Enabled)
	return flags
}

func supabaseEntries(services []supabaseService) string {
	b := &strings.Builder{}
	for _, service := range services {
		section := "ports"
		if service.routed {
			section = "routes"
		}
		// A label only means anything on a route: a demoted service has no
		// hostname, and grove rejects one that claims a label anyway.
		label := service.label
		if label == "" {
			label = service.name
		}

		b.WriteString("\n")
		if service.routed {
			b.WriteString(routeURL(label))
		}
		fmt.Fprintf(b, "[%s.%s]\ndetached = true\n", section, service.name)
		if service.routed && service.label != "" {
			fmt.Fprintf(b, "label = %q\n", service.label)
		}
	}
	return b.String()
}
