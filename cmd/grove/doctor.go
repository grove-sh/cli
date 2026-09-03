package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/service"
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
		Short: "Check DNS, trust, the daemon, and port 443",
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
				checkService(),
				checkPort443(running),
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
	f := finding{name: "daemon"}

	client, err := daemon.Dial(socket)
	if err != nil {
		f.state = warn
		f.detail = "not running at " + socket
		f.advice = "Start one with grove restart, which puts it in the background."
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
		f.advice = "Refresh the copy the service runs with grove install, then grove restart. Detached ports need a grove sync afterwards, since a restart drops them."
	}
	return &status, f
}

// staleDaemon describes the running daemon's build when it is worth mentioning,
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
	return "built from " + daemonBuild + ", not " + cliBuild
}

func checkService() finding {
	f := finding{name: "service"}

	state := service.Status()
	switch {
	case !state.Supported:
		f.state = warn
		f.detail = "nothing here restarts the daemon for you"
		f.advice = state.Reason
		return f
	case !state.Installed:
		f.state = warn
		f.detail = "not installed, so the daemon will not come back after a reboot"
		f.advice = "Run grove install."
		return f
	case !state.Enabled:
		f.state = warn
		f.detail = "installed but not enabled"
		f.advice = "Run: systemctl --user enable grove"
		return f
	case !state.Lingering:
		f.state = warn
		f.detail = "enabled, but your user manager does not linger, so it stops at logout"
		f.advice = "Run: loginctl enable-linger $USER"
		return f
	case !state.Active:
		f.state = bad
		f.detail = "enabled but not running"
		f.advice = "Look at why with: systemctl --user status grove"
		return f
	}

	f.state = ok
	f.detail = "enabled, running, and lingering"
	return f
}

func checkPort443(running *daemon.Status) finding {
	f := finding{name: "port 443"}

	const address = "127.0.0.1:443"
	if running != nil && running.Listen == address {
		f.state = ok
		f.detail = fmt.Sprintf("held by grove's own daemon, pid %d", running.PID)
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
		f.advice = "Held by " + whoHolds443() + "."
	}
	return f
}

// whoHolds443 asks docker, since ss cannot name a process owned by root and a
// container publishing the port is the usual culprit.
func whoHolds443() string {
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
		if found && strings.Contains(ports, ":"+strconv.Itoa(443)+"->") {
			return "the container " + name
		}
	}
	return "another process, not a docker container"
}
