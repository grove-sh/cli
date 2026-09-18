package main

import (
	"cmp"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/config"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/identity"
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/trust"
)

type finding struct {
	name   string
	state  string
	detail string

	// advice is said as-is; fix names a command, and several findings naming
	// the same one are answered once rather than repeated.
	advice string
	fix    string
}

const (
	ok   = "ok"
	warn = "warn"
	bad  = "fail"
)

func newDoctorCommand() *cobra.Command {
	var stateDir, socket, domain string

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check DNS, trust, grove itself, and port 443",
		Long: `Check the things that have to be true before grove can serve a hostname.

Each check reports on its own. A failure exits non-zero so this can gate a
script; a warning does not.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := os.Getwd()
			if err != nil {
				return err
			}
			// Grove's own check comes first so the port check can tell its
			// listener apart from something else holding the address.
			running, answered, groveFinding := checkDaemon(socket)
			findings := []finding{
				groveFinding,
				checkAuthority(stateDir),
				checkBundle(stateDir),
			}
			// Said only when not ordinary: a resolver that answers and a port
			// grove holds are already implied above, and with grove down there
			// is nothing to ask port 80.
			quiet := []finding{checkDNS(domain), checkPort443(running, answered, stateDir, domain)}
			if running != nil {
				quiet = append(quiet, checkHTTPRedirect(running, domain))
			}
			for _, f := range quiet {
				if f.state != ok {
					findings = append(findings, f)
				}
			}
			// Reported even when fine, unlike those: which worktree is the
			// project answers why a hostname is what it is.
			if f, worth := checkMainWorktree(dir); worth {
				findings = append(findings, f)
			}
			// Only where the daemon answered: the check above already reports
			// one that is down, and a second dial to learn the same is a
			// connection nobody reads the result of.
			var leases []daemon.Live
			if answered {
				leases = allLeases(socket)
			}
			if f, worth := checkContextMoved(dir, leases); worth {
				findings = append(findings, f)
			}
			if f, worth := checkProjectGrove(dir, resolveVersion()); worth {
				findings = append(findings, f)
			}
			// Read here rather than inside the check that wants it: a check
			// loading its own config cannot tell a directory with no project
			// from a project whose config will not parse, and goes quiet for
			// both. Quiet is right for one and a lie about the other.
			cfg, cfgErr := config.Load(dir)
			if f, worth := checkProject(cfgErr); worth {
				findings = append(findings, f)
			}
			if f, worth := checkOverrideFile(cfg); worth {
				findings = append(findings, f)
			}

			out := cmd.OutOrStdout()
			paint := styles(out)
			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			failed := false
			for _, f := range findings {
				fmt.Fprintf(w, "%s\t%s\n", f.name, paint.paint(f.state)(f.detail))
				if f.state == bad {
					failed = true
				}
			}
			// Not checks: the two things every bug report needs and nobody can
			// produce from memory.
			fmt.Fprintf(w, "Version\t%s\n", paint.dim(resolveVersion()))
			fmt.Fprintf(w, "Platform\t%s\n", paint.dim(platformName()))
			w.Flush()

			said := map[string]bool{}
			for _, f := range findings {
				switch {
				case f.fix != "" && !said[f.fix]:
					said[f.fix] = true
					fmt.Fprintf(out, "\n%s\n", paint.paint(f.state)(remedy(f.fix)))
				case f.fix == "" && f.advice != "":
					fmt.Fprintf(out, "\n%s\n", paint.paint(f.state)(f.advice))
				}
			}
			if failed {
				return &exitError{code: 1}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA")
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	cmd.Flags().StringVar(&domain, "domain", defaultDomain, "domain every context lives under")
	return cmd
}

// Said once however many findings ask for it, and spelled the way this caller
// can actually run it.
func remedy(fix string) string {
	grove := invocation()
	switch fix {
	case "grove install":
		return "Run " + grove + " install to generate the authority, trust it on this machine, and put its root where runtimes look."
	case "grove start":
		return "Start it with " + grove + " start, which puts it in the background."
	case "grove restart":
		return "Run " + grove + " restart to pick up the build you have installed. Attached ports need their commands run again, since a restart drops them."
	}
	return ""
}

// In the words the release uses, so a bug report names something lookupable.
func platformName() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return runtime.GOOS + "-" + arch
}

// The one check about the repository the caller stands in rather than the
// machine. False where there is nothing to say at all, as in a repository with
// a main clone, which is always the project itself.
func checkMainWorktree(dir string) (finding, bool) {
	f := finding{name: "Main worktree", state: ok}
	m := identity.ReadMainWorktree(dir)

	switch {
	case !m.Bare:
		if !m.Ignored {
			return f, false
		}
		f.state = warn
		f.detail = fmt.Sprintf("grove.mainWorktree names %q, and is ignored here", m.Setting)
		f.advice = "It picks which worktree of a bare repository serves the hostname with no suffix. This repository has a main clone, which is always the one."
	case m.Worktree != "":
		f.detail = m.Worktree + ", from " + m.From
	case len(m.Candidates) == 0:
		// No worktrees at all: nothing is the project, and nothing to name.
		return f, false
	case m.Setting != "":
		f.state = warn
		f.detail = fmt.Sprintf("grove.mainWorktree names %q, which is not a worktree here", m.Setting)
		f.advice = "Worktrees to name instead: " + strings.Join(m.Candidates, ", ") + ". Until it names one, every worktree keeps its suffix."
	default:
		f.state = warn
		f.detail = "none, since no worktree is on the branch " + m.From + " names"
		if m.From == "" {
			f.detail = "none, since nothing here names a default branch"
		}
		f.advice = "Nothing serves the hostname with no suffix. Name the worktree that should with git config grove.mainWorktree, from: " + strings.Join(m.Candidates, ", ") + "."
	}
	return f, true
}

// A context is derived, not recorded, so a grove that derives it differently
// renames this worktree without anything saying so: the ports move with the
// slug, and the stack carries on listening on the old set under the old name.
// The daemon is the one place both spellings exist at once, since the leases it
// is holding carry the worktree they were taken for.
func checkContextMoved(dir string, leases []daemon.Live) (finding, bool) {
	context, err := identity.Resolve(dir)
	if err != nil {
		return finding{}, false
	}

	var under []string
	for _, held := range leases {
		if sameWorktree(held.Worktree, context.Root) && held.Slug != context.Slug && !slices.Contains(under, held.Slug) {
			under = append(under, held.Slug)
		}
	}
	if len(under) == 0 {
		return finding{}, false
	}
	slices.Sort(under)

	grove := invocation()
	return finding{
		name:   "Context",
		state:  warn,
		detail: "holding ports as " + strings.Join(under, ", ") + ", and resolving " + context.Slug + " now",
		advice: "Ports are derived from the context, so those sets do not overlap and whatever is running is on the other one. Usually another grove on this machine derives the context differently: run " + grove + " doctor there, stop the stack, then " + grove + " hold here.",
	}, true
}

// The two spellings come from two builds, which is the whole premise, so they
// need not agree on how a path is written: one may not have resolved the
// symlinks the other did, and macOS reaches every temporary directory through
// one. A path that will not resolve is compared as it came.
func sameWorktree(a, b string) bool {
	if a == b {
		return true
	}
	return resolvedPath(a) == resolvedPath(b)
}

func resolvedPath(path string) string {
	if out, err := filepath.EvalSymlinks(path); err == nil {
		return out
	}
	return path
}

// Every lease on the machine, since the question is which of them name this
// worktree rather than which name this context.
//
// Whatever protocol the daemon speaks, because the daemon running is quite
// likely the other build's: a check about two groves on one machine that goes
// quiet as soon as they disagree on the wire is quiet exactly when it is
// needed. Safe here for the reason ListAnyVersion asks its callers to have: an
// older payload leaves what it does not carry zero, and an empty worktree
// matches nothing, so a field that moved costs the finding rather than
// inventing one.
func allLeases(socket string) []daemon.Live {
	client, err := daemon.Dial(socket)
	if err != nil {
		return nil
	}
	defer client.Close()

	leases, err := client.ListAnyVersion()
	if err != nil {
		return nil
	}
	return leases
}

// No grove.toml is the ordinary case, doctor being mostly about the machine.
// One that will not parse is the opposite: every grove command in this
// directory reads it first, so none of them run at all, and a run that only
// reported on DNS and trust would look like a clean bill of health.
func checkProject(err error) (finding, bool) {
	if err == nil || errors.Is(err, config.ErrNotFound) {
		return finding{}, false
	}
	return finding{
		name:   "Project",
		state:  bad,
		detail: err.Error(),
		advice: "Every grove command here reads " + config.FileName + ", and " + config.LocalName + " beside it, before it does anything else. Nothing in this directory runs until that parses.",
	}, true
}

// The override file is one person's, so a tracked one overrides for everyone:
// the drift grove exists to remove, wearing the clothes of a fix. Nothing to
// say where there is no project, no such file, or an untracked one, which is
// every project that has not made the mistake.
func checkOverrideFile(cfg *config.Config) (finding, bool) {
	if cfg == nil {
		return finding{}, false
	}
	for _, name := range cfg.OverrideFiles() {
		if _, err := os.Stat(filepath.Join(cfg.Dir, name)); err != nil {
			continue
		}
		if !trackedByGit(cfg.Dir, name) {
			continue
		}
		return finding{
			name:   "Env override",
			state:  warn,
			detail: name + " is tracked by git, so it overrides for everyone",
			advice: "That file is the one tier above everything grove resolves, and a shared one hides the values grove leased. Untrack it with git rm --cached " + name + " and gitignore it, or move what the whole project needs into grove.toml.",
		}, true
	}
	return finding{}, false
}

// A zero exit means git knows the path, staged as much as committed, which is
// why the finding says tracked rather than committed: git add is where the
// mistake is made, and the warning is worth more before the push than after.
// Run in the project directory, since that is where the file is and where git
// has to be asked about it.
func trackedByGit(dir, name string) bool {
	cmd := exec.Command("git", "ls-files", "--error-unmatch", "--", name)
	cmd.Dir = dir
	return cmd.Run() == nil
}

func checkDNS(domain string) finding {
	host := "grove-doctor." + domain
	f := finding{name: "DNS"}

	addrs, err := net.LookupHost(host)
	if err != nil {
		f.state = bad
		f.detail = fmt.Sprintf("%s does not resolve", host)
		f.advice = "The wildcard record points into loopback, which some resolvers strip. Pi-hole, NextDNS, dnsmasq with stop-dns-rebind, and several routers do this. Allowlist " + domain + ", or fall back to hosts entries."
		return f
	}
	for _, addr := range addrs {
		if ip := net.ParseIP(addr); ip == nil || !ip.IsLoopback() {
			f.state = bad
			f.detail = fmt.Sprintf("%s resolves to %s", host, strings.Join(addrs, ", "))
			f.advice = "Something is answering for " + domain + " with an address that is not loopback."
			return f
		}
	}
	// Loopback is not enough: a domain pointing at the wrong loopback address
	// means the domain and the grove reading it came from different versions.
	if !slices.Contains(addrs, platform.Address) {
		f.state = bad
		f.detail = fmt.Sprintf("%s resolves to %s, and grove serves %s", host, strings.Join(addrs, ", "), platform.Address)
		f.advice = "That domain belongs to a different version of grove than this one. Check which grove you are running."
		return f
	}
	f.state = ok
	f.detail = fmt.Sprintf("*.%s resolves to %s", domain, strings.Join(addrs, ", "))
	return f
}

func checkAuthority(stateDir string) finding {
	f := finding{name: "Authority"}

	root, err := ca.Open(stateDir)
	if err != nil {
		f.state = bad
		f.detail = err.Error()
		if errors.Is(err, ca.ErrNoAuthority) {
			f.detail = "not installed"
			f.fix = "grove install"
		}
		return f
	}
	if !trust.Trusted(root.Certificate()) {
		f.state = bad
		f.detail = "installed, but this machine does not trust it"
		f.fix = "grove install"
		return f
	}
	f.state = ok
	f.detail = "installed and trusted"
	if expiry := root.Certificate().NotAfter; time.Until(expiry) < 30*24*time.Hour {
		f.state = warn
		f.detail = "installed and trusted, but expires " + expiry.Format(time.DateOnly)
		f.advice = "Run grove uninstall, then grove install, to issue a new one."
	}
	return f
}

func checkBundle(stateDir string) finding {
	f := finding{name: "Runtime bundle"}

	if trust.SystemBundle() == "" {
		f.state = warn
		f.detail = "this system keeps its roots outside a bundle file"
		f.advice = "Node still works through NODE_EXTRA_CA_CERTS, but python's requests and Deno will not trust grove here."
		return f
	}
	root, err := ca.Open(stateDir)
	if err != nil {
		f.state = warn
		f.detail = "no CA to look for"
		return f
	}

	bundle, merged := trust.Bundle(stateDir)
	switch {
	case !merged && !trust.SystemBundleTrusts(root.Certificate()):
		f.state = warn
		f.detail = bundle + " does not carry grove's root"
		f.fix = "grove install"
		return f
	case merged && trust.BundleStale(stateDir):
		f.state = warn
		f.detail = bundle + " is older than the system roots it was merged from"
		f.fix = "grove install"
		return f
	case merged:
		f.state = ok
		f.detail = bundle + ", merged by grove"
		return f
	}

	f.state = ok
	f.detail = bundle + ", which carries grove's root"
	return f
}

// The nil status and the bool are separate answers: a daemon can be running and
// still be one this build cannot read, and then the port it holds has an owner
// grove knows without having to go looking for it.
func checkDaemon(socket string) (*daemon.Status, bool, finding) {
	f := finding{name: "Grove"}

	client, err := daemon.Dial(socket)
	if err != nil {
		f.state = warn
		f.detail = "not running"
		f.fix = "grove start"
		return nil, false, f
	}
	defer client.Close()

	status, err := client.Status()
	if err != nil {
		f.state = bad
		f.detail = err.Error()
		return nil, true, f
	}
	f.state = ok
	f.detail = "running"
	if stale := staleDaemon(status.Grove, resolveVersion()); stale != "" {
		f.state = warn
		f.detail += ", " + stale
		f.fix = "grove restart"
	}
	return &status, true, f
}

// A daemon outlives the command that started it, so upgrading grove leaves the
// old build serving until something restarts it. An empty version comes from
// one built before it could report a version, which says the same more loudly.
func staleDaemon(daemonBuild, cliBuild string) string {
	switch {
	case daemonBuild == cliBuild:
		return ""
	case daemonBuild == "":
		return "built before it could report its version"
	}
	return "the one running was built from " + daemonBuild + ", and this one from " + cliBuild
}

// The other version skew, and the one nothing reports: a project installs grove
// as a dependency, so a command run through its scripts can be a different
// build from the one on the path. Two builds either side of a change to how a
// context is derived put the same worktree on two slugs, which moves every port
// and hostname, and every symptom of it reads as grove losing track of a stack
// that is plainly running.
func checkProjectGrove(dir, running string) (finding, bool) {
	root, err := config.Find(dir)
	if err != nil {
		return finding{}, false
	}
	installed := installedGroves(filepath.Dir(root))
	if len(installed) == 0 {
		return finding{}, false
	}

	f := finding{name: "Project grove"}
	// The project disagreeing with itself is the whole finding, and it holds
	// whatever grove is running it: the build that starts the stack is the one
	// under the package whose script does it, not the one anybody types.
	if where := byVersion(installed); len(where) > 1 {
		f.state = warn
		f.detail = strings.Join(where, "; ")
		f.advice = "Commands run through this project reach whichever copy is nearest, so two of them derive this worktree's context differently and hand out different ports. Install one version throughout, then restart whatever is running on the other's ports."
		return f, true
	}

	// One version throughout, so the only question left is whether it is this
	// one. Asked only of a released build, since a build from a working tree
	// differs from every published version by definition.
	if !released(running) || installed[0].version == strings.TrimPrefix(running, "v") {
		return finding{}, false
	}
	// A row and nothing more. Two versions matter only where they derive a
	// context differently, which this cannot tell and which most pairs do not,
	// so a warning here would cry wolf on every project not yet upgraded and a
	// paragraph would spend three lines saying it might be nothing. Stated as a
	// fact, it is the clue to reach for when something later does not line up.
	f.state = ok
	f.detail = installed[0].version + " throughout, and this is " + running
	return f, true
}

// Grouped by version rather than listed by directory: which copies are the odd
// ones out is the question, and a flat list of every package leaves the reader
// to work that out.
func byVersion(installed []installedGrove) []string {
	var versions []string
	where := map[string][]string{}
	for _, one := range installed {
		if _, seen := where[one.version]; !seen {
			versions = append(versions, one.version)
		}
		where[one.version] = append(where[one.version], one.where)
	}
	slices.Sort(versions)

	out := make([]string, 0, len(versions))
	for _, version := range versions {
		out = append(out, version+" in "+strings.Join(where[version], ", "))
	}
	return out
}

// A build stamped from a working tree carries +dirty, one from a clean checkout
// past the last tag carries a pseudo-version ending in a timestamp and a
// commit, and one that cannot name itself says unknown. No project could have
// installed any of them, so comparing one to a project would warn about every
// project on the machine.
func released(version string) bool {
	return version != "unknown" && !strings.Contains(version, "+") && !pseudoVersion.MatchString(version)
}

// The tail go stamps on a build of an untagged commit, as in
// v0.5.0-beta.1.0.20260918153512-01613716023d.
var pseudoVersion = regexp.MustCompile(`[0-9]{14}-[0-9a-f]{12}$`)

type installedGrove struct {
	where   string
	version string
}

// Every copy the project carries, not only the one a command here would
// resolve: the install that matters is usually the one under the package whose
// script starts the stack, which is not the directory anybody runs doctor from.
// node_modules is checked and then skipped, so this walks the source tree
// rather than its dependencies.
func installedGroves(root string) []installedGrove {
	var found []installedGrove
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		// By name before kind, since yarn and nix layouts link node_modules
		// rather than create it, and a link is not a directory to a walk.
		if d.Name() == "node_modules" {
			if version, ok := groveVersion(filepath.Join(path, "@grove-sh", "cli", "package.json")); ok {
				where := filepath.Dir(path)
				if where == root {
					where = "this project"
				} else if rel, relErr := filepath.Rel(root, where); relErr == nil {
					where = rel
				}
				found = append(found, installedGrove{where: where, version: version})
			}
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.IsDir() {
			return nil
		}
		// A dot directory holds no packages, and .git in a large repository is
		// most of what a walk would otherwise read.
		if name := d.Name(); name != "." && strings.HasPrefix(name, ".") {
			return fs.SkipDir
		}
		return nil
	})
	slices.SortFunc(found, func(a, b installedGrove) int { return cmp.Compare(a.where, b.where) })
	return found
}

func groveVersion(manifest string) (string, bool) {
	data, err := os.ReadFile(manifest)
	if err != nil {
		return "", false
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil || pkg.Version == "" {
		return "", false
	}
	return pkg.Version, true
}

func checkPort443(running *daemon.Status, answered bool, stateDir, domain string) finding {
	return port443(running, answered, stateDir, domain, platform.DefaultListen(), platform.PrivilegedPorts())
}

// Takes the platform's answer rather than asking, so a machine that redirects
// the port is testable on one that binds it.
func port443(running *daemon.Status, answered bool, stateDir, domain, listen string, access platform.PortAccess) finding {
	f := finding{name: "Port 443"}

	address := platform.Address + ":443"
	if running != nil && running.Listen == address {
		f.state = ok
		f.detail = fmt.Sprintf("held by grove itself, pid %d", running.PID)
		return f
	}

	// A daemon serving somewhere else can still be what 443 reaches, which is
	// the only way macOS works at all: nothing binds 443 there, pf sends it to
	// the port the daemon could bind.
	if running != nil && answersOn443(stateDir, domain) {
		f.state = ok
		f.detail = fmt.Sprintf("reaches grove on %s, redirected", running.Listen)
		return f
	}

	// When grove listens somewhere else, nothing binds 443 here and trying
	// reports permission denied however well the machine is arranged. The
	// redirect is the question, and the platform answers it with no daemon.
	if listen != address {
		f.state = ok
		f.detail = access.Detail
		if !access.Allowed {
			f.state = bad
			f.fix = "grove install"
		}
		return f
	}

	ln, err := net.Listen("tcp", address)
	if err == nil {
		ln.Close()
		f.state = ok
		f.detail = "bindable"
		return f
	}

	f.state = bad
	f.detail = err.Error()
	switch {
	case strings.Contains(err.Error(), "permission denied"):
		f.advice = access.Advice
	case strings.Contains(err.Error(), "address already in use"):
		// Only an unreadable daemon leaves grove as the unnamed holder. One
		// that answered said where it listens, and if that were this port the
		// check would have finished above.
		f.advice = holder(running == nil && answered)
	}
	return f
}

// Binding 80 is allowed to fail, so the only other sign is a line in the
// daemon's log that nobody reads.
func checkHTTPRedirect(running *daemon.Status, domain string) finding {
	f := finding{name: "HTTP redirect"}

	if to, answered := redirectFrom80(domain); answered {
		f.state = ok
		f.detail = "80 sends " + to + " to https"
		return f
	}

	f.state = warn
	f.detail = "80 does not reach grove, so http:// will not either"
	f.advice = platform.PrivilegedPorts().Advice
	if f.advice == "" {
		f.advice = "Held by " + whoHolds(80) + ", or grove could not bind it."
	}
	return f
}

// Asking for a hostname only grove answers for means something else on the port
// cannot pass by accident, and a grove one build behind still answers, which a
// field in the status could not manage.
func redirectFrom80(domain string) (string, bool) {
	host := "doctor." + domain
	request, err := http.NewRequest("GET", "http://"+platform.Address+":80/", nil)
	if err != nil {
		return "", false
	}
	request.Host = host

	client := &http.Client{
		Timeout:       2 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(request)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusFound {
		return "", false
	}
	if !strings.HasPrefix(resp.Header.Get("Location"), "https://"+host) {
		return "", false
	}
	return host, true
}

// answersOn443 reports whether grove is what a connection to 443 reaches.
//
// Asking whether grove can bind 443 answers the wrong question on macOS, where
// nothing binds it and pf does the work. Asking whose certificate comes back
// answers it everywhere, and cannot be fooled by something else holding the
// port: only grove's own authority signs for this domain.
func answersOn443(stateDir, domain string) bool {
	root, err := ca.Open(stateDir)
	if err != nil {
		return false
	}
	pool := x509.NewCertPool()
	pool.AddCert(root.Certificate())

	conn, err := tls.DialWithDialer(
		&net.Dialer{Timeout: 2 * time.Second},
		"tcp", platform.Address+":443",
		&tls.Config{ServerName: "doctor." + domain, RootCAs: pool},
	)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Asks docker, since ss cannot name a process owned by root and a container
// publishing the port is the usual culprit.
// Reported rather than looked up when a daemon answered the control socket:
// grove is what holds the port, however little it could say about itself. Kept
// apart from the bind attempt so the answer is testable on a machine whose 443
// is already taken, which is the machine this matters on.
func holder(answered bool) string {
	if answered {
		return "Held by grove's own daemon, which this build cannot speak to."
	}
	return "Held by " + whoHolds(443) + "."
}

func whoHolds(port int) string {
	if _, err := exec.LookPath("docker"); err != nil {
		return "another process"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", "ps", "--format", "{{.Names}}\t{{.Ports}}").Output()
	if err != nil {
		return "another process"
	}
	for line := range strings.SplitSeq(string(out), "\n") {
		name, ports, found := strings.Cut(line, "\t")
		if found && strings.Contains(ports, ":"+strconv.Itoa(port)+"->") {
			return "the container " + name
		}
	}
	return "another process, not a docker container"
}
