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
	advice string
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
			// The daemon check comes first so the port check can tell grove's
			// own listener apart from something else holding the address.
			running, daemonFinding := checkDaemon(socket)
			findings := []finding{
				checkDNS(domain),
				checkAuthority(stateDir),
				checkBundle(stateDir),
				daemonFinding,
				checkPort443(running, stateDir, domain),
				checkHTTPRedirect(running, domain),
			}

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			failed := false
			for _, f := range findings {
				fmt.Fprintf(w, "%s\t%s\t%s\n", f.state, f.name, f.detail)
				if f.state == bad {
					failed = true
				}
			}
			w.Flush()

			for _, f := range findings {
				if f.advice != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "\n%s: %s\n", f.name, f.advice)
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

func checkDNS(domain string) finding {
	host := "grove-doctor." + domain
	f := finding{name: "dns"}

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
	f := finding{name: "authority"}

	root, err := ca.Open(stateDir)
	if err != nil {
		f.state = bad
		f.detail = err.Error()
		if errors.Is(err, ca.ErrNoAuthority) {
			f.advice = "Run grove install."
		}
		return f
	}
	if !trust.Trusted(root.Certificate()) {
		f.state = bad
		f.detail = "the root exists but this machine does not trust it"
		f.advice = "Run grove install to add it to the system trust stores."
		return f
	}
	f.state = ok
	f.detail = "trusted, expires " + root.Certificate().NotAfter.Format(time.DateOnly)
	return f
}

func checkBundle(stateDir string) finding {
	f := finding{name: "runtime bundle"}

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
		f.advice = "Run grove install, which adds it to the OS store or merges a bundle, whichever this system needs."
		return f
	case merged && trust.BundleStale(stateDir):
		f.state = warn
		f.detail = bundle + " is older than the system roots it was merged from"
		f.advice = "Run grove install to rebuild it."
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
	f := finding{name: "grove"}

	client, err := daemon.Dial(socket)
	if err != nil {
		f.state = warn
		f.detail = "not running at " + socket
		f.advice = "Start one with grove start, which puts it in the background."
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
	f.detail = fmt.Sprintf("on %s, pid %d, %d lease(s)", status.Listen, status.PID, status.Leases)
	if stale := staleDaemon(status.Grove, resolveVersion()); stale != "" {
		f.state = warn
		f.detail += ", " + stale
		f.advice = "Run grove restart to pick up the build you have installed. Attached ports need their commands run again, since a restart drops them."
	}
	return &status, f
}

// staleDaemon describes the running grove's build when it is worth mentioning,
// which is only when it is not this one: the service runs a copy taken at
// install time, so upgrading the package leaves the old one serving. An empty
// version comes from a daemon built before it could report one, which says the
// same thing more loudly.
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
	f := finding{name: "port 443"}

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
// does if grove could bind port 80. That is allowed to fail, so the only sign
// is a line in the daemon's own log, which nobody reads. Hence this.
func checkHTTPRedirect(running *daemon.Status, domain string) finding {
	f := finding{name: "http redirect"}

	if running == nil {
		f.state = warn
		f.detail = "cannot tell while grove is not running"
		return f
	}
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
