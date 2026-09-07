package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/trust"
)

type finding struct {
	name   string
	state  string
	detail string

	// advice is what to do, when nothing else will be saying it. fix names a
	// command instead, and several findings naming the same one are answered
	// once: two problems with a single remedy is one thing to do, not two.
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
			// Grove's own check comes first so the port check can tell its
			// listener apart from something else holding the address.
			running, groveFinding := checkDaemon(socket)
			findings := []finding{
				groveFinding,
				checkAuthority(stateDir),
				checkBundle(stateDir),
			}
			// These say something only when they are not the ordinary case: a
			// resolver that answers and a port grove itself holds are both
			// already implied by the lines above.
			// With grove down there is nothing to ask port 80, and the line
			// above has already said why.
			ports := []finding{checkDNS(domain), checkPort443(running, stateDir, domain)}
			if running != nil {
				ports = append(ports, checkHTTPRedirect(running, domain))
			}
			for _, f := range ports {
				if f.state != ok {
					findings = append(findings, f)
				}
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
			// Not checks, but the two things every bug report needs and nobody
			// can produce from memory.
			fmt.Fprintf(w, "Version\t%s\n", paint.dim(resolveVersion()))
			fmt.Fprintf(w, "Platform\t%s\n", paint.dim(platformName()))
			w.Flush()

			said := map[string]bool{}
			for _, f := range findings {
				switch {
				case f.fix != "" && !said[f.fix]:
					said[f.fix] = true
					fmt.Fprintf(out, "\n%s\n", paint.paint(f.state)(remedy[f.fix]))
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

// remedy is what to run, said once however many findings ask for it.
var remedy = map[string]string{
	"grove install": "Run grove install to generate the authority, trust it on this machine, and put its root where runtimes look.",
	"grove start":   "Start it with grove start, which puts it in the background.",
	"grove restart": "Run grove restart to pick up the build you have installed. Attached ports need their commands run again, since a restart drops them.",
}

// platformName says which build this is in the words the release uses, so a
// bug report names something that can be looked up.
func platformName() string {
	arch := runtime.GOARCH
	if arch == "amd64" {
		arch = "x64"
	}
	return runtime.GOOS + "-" + arch
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

// checkDaemon returns the daemon's own account of itself, or nil when nothing
// answers.
func checkDaemon(socket string) (*daemon.Status, finding) {
	f := finding{name: "Grove"}

	client, err := daemon.Dial(socket)
	if err != nil {
		f.state = warn
		f.detail = "not running"
		f.fix = "grove start"
		return nil, f
	}
	defer client.Close()

	status, err := client.Status()
	if err != nil {
		f.state = bad
		f.detail = err.Error()
		return nil, f
	}
	f.state = ok
	f.detail = "running"
	if stale := staleDaemon(status.Grove, resolveVersion()); stale != "" {
		f.state = warn
		f.detail += ", " + stale
		f.fix = "grove restart"
	}
	return &status, f
}

// staleDaemon describes the running grove's build when it is worth mentioning,
// which is only when it is not this one. A daemon outlives the command that
// started it, so upgrading grove while one is running leaves the old build
// serving until something restarts it. An empty version comes from a daemon
// built before it could report one, which says the same thing more loudly.
func staleDaemon(daemonBuild, cliBuild string) string {
	switch {
	case daemonBuild == cliBuild:
		return ""
	case daemonBuild == "":
		return "built before it could report its version"
	}
	return "the one running was built from " + daemonBuild + ", and this one from " + cliBuild
}

func checkPort443(running *daemon.Status, stateDir, domain string) finding {
	f := finding{name: "Port 443"}

	const address = "127.0.0.1:443"
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
		f.advice = platform.PrivilegedPorts().Advice
	case strings.Contains(err.Error(), "address already in use"):
		f.advice = "Held by " + whoHolds(443) + "."
	}
	return f
}

// whoHolds443 asks docker, since ss cannot name a process owned by root and a
// container publishing the port is the usual culprit.// checkHTTPRedirect reports whether plain http reaches grove, which it only
// does if grove could bind port 80. Only asked while grove is running. That is allowed to fail, so the only sign
// is a line in the daemon's own log, which nobody reads. Hence this.
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

// redirectFrom80 asks port 80 for a hostname only grove would answer for, and
// reports where it was sent. Something else on the port cannot pass this by
// accident, and a grove one build behind still answers it, which is what a
// field in the status could not manage.
func redirectFrom80(domain string) (string, bool) {
	host := "doctor." + domain
	request, err := http.NewRequest("GET", "http://127.0.0.1:80/", nil)
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
		"tcp", "127.0.0.1:443",
		&tls.Config{ServerName: "doctor." + domain, RootCAs: pool},
	)
	if err != nil {
		return false
	}
	conn.Close()
	return true
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
