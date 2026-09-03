package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
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
			findings := []finding{
				checkDNS(domain),
				checkAuthority(stateDir),
				checkBundle(stateDir),
				checkDaemon(socket),
				checkPort443(),
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
	f := finding{name: "system bundle"}

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
	if !trust.SystemBundleTrusts(root.Certificate()) {
		f.state = warn
		f.detail = trust.SystemBundle() + " does not carry grove's root"
		f.advice = "Run grove install; adding the root to the OS store rewrites that bundle."
		return f
	}
	f.state = ok
	f.detail = trust.SystemBundle() + " carries grove's root"
	return f
}

func checkDaemon(socket string) finding {
	f := finding{name: "daemon"}

	client, err := daemon.Dial(socket)
	if err != nil {
		f.state = warn
		f.detail = "not running at " + socket
		f.advice = "Start one with grove daemon."
		return f
	}
	defer client.Close()

	entries, err := client.List()
	if err != nil {
		f.state = bad
		f.detail = err.Error()
		return f
	}
	f.state = ok
	f.detail = fmt.Sprintf("listening, %d context(s) running", len(entries))
	return f
}

func checkPort443() finding {
	f := finding{name: "port 443"}

	ln, err := net.Listen("tcp", "127.0.0.1:443")
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
		f.advice = privilegeAdvice()
	case strings.Contains(err.Error(), "address already in use"):
		f.advice = "Held by " + whoHolds443() + ". This is expected if grove's own daemon has it."
	}
	return f
}

func privilegeAdvice() string {
	if runtime.GOOS != "linux" {
		return "Binding a port below 1024 on " + runtime.GOOS + " needs a root LaunchDaemon, which grove does not install yet. Until then use grove daemon --listen 127.0.0.1:8443."
	}
	current := "1024"
	if raw, err := os.ReadFile(unprivilegedPortsFile); err == nil {
		current = strings.TrimSpace(string(raw))
	}
	return "The unprivileged port floor is " + current + ". Lower it with: echo 'net.ipv4.ip_unprivileged_port_start=443' | sudo tee /etc/sysctl.d/60-grove.conf && sudo sysctl --system"
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
