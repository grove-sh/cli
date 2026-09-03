package main

import (
	"crypto/x509"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/trust"
)

const unprivilegedPortsFile = "/proc/sys/net/ipv4/ip_unprivileged_port_start"

func newInstallCommand() *cobra.Command {
	var stateDir string
	var installTrust bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Generate grove's certificate authority and trust it on this machine",
		Long: `Generate grove's certificate authority and trust it on this machine.

Installing into the system trust store needs elevated privileges, so this may
ask for your password. Everything else is written under your own state
directory. Re-running is safe: an existing CA is reused, and an already trusted
root is left alone.

Binding port 443 is reported rather than changed, since that is a machine wide
setting you should apply yourself.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			if err := makePrivate(stateDir, out); err != nil {
				return err
			}
			root, err := ca.OpenOrCreate(stateDir)
			if err != nil {
				return err
			}

			trusted := trust.Trusted(root.Certificate())
			state := "already trusted"
			switch {
			case !trusted && !installTrust:
				state = "not trusted, skipped by --trust=false"
			case !trusted:
				fmt.Fprintln(out, "installing the root into your trust stores, sudo may ask for your password")
				if err := trust.Install(root.Certificate()); err != nil {
					return err
				}
				state = "installed"
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "authority\t%s\n", filepath.Join(stateDir, trust.RootFile))
			fmt.Fprintf(w, "trust store\t%s\n", state)
			fmt.Fprintf(w, "system bundle\t%s\n", bundleState(root.Certificate()))
			w.Flush()

			reportPrivilegedPorts(out)
			return nil
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA and bundle")
	cmd.Flags().BoolVar(&installTrust, "trust", true, "install the root into the system trust stores")
	return cmd
}

// Installing into the OS store rewrites the system bundle on Linux, which is
// what python's requests and Deno are pointed at.
func bundleState(root *x509.Certificate) string {
	switch {
	case trust.SystemBundle() == "":
		return "none on this system, so runtimes with their own bundles will not trust grove"
	case trust.SystemBundleTrusts(root):
		return trust.SystemBundle() + " carries this root"
	default:
		return trust.SystemBundle() + " does not carry this root yet"
	}
}

func newUninstallCommand() *cobra.Command {
	var stateDir string

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove grove's root from this machine's trust stores",
		Long: `Remove grove's root from this machine's trust stores.

The CA files stay on disk, so a later 'grove install' trusts the same root
rather than generating another one. Certificates already issued keep working
for anything that still trusts the root.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := ca.Open(stateDir)
			if err != nil {
				return err
			}
			if !trust.Trusted(root.Certificate()) {
				fmt.Fprintln(cmd.OutOrStdout(), "this root is not in the system trust store")
				return nil
			}
			return trust.Uninstall(root.Certificate())
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA")
	return cmd
}

// makePrivate creates the state directory, and tightens one that already exists
// with looser permissions, since the CA's private key lives there.
func makePrivate(dir string, out io.Writer) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if loose := info.Mode().Perm() &^ fs.FileMode(0o700); loose != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
		fmt.Fprintf(out, "tightened %s from %#o to 0700\n", dir, info.Mode().Perm())
	}
	return nil
}

func reportPrivilegedPorts(out io.Writer) {
	if runtime.GOOS != "linux" {
		fmt.Fprintf(out, "\nBinding port 443 on %s needs a root LaunchDaemon, which grove does not install yet.\nUntil then run: grove daemon --listen 127.0.0.1:8443\n", runtime.GOOS)
		return
	}

	start, err := os.ReadFile(unprivilegedPortsFile)
	if err == nil {
		if from, convErr := strconv.Atoi(strings.TrimSpace(string(start))); convErr == nil && from <= 443 {
			fmt.Fprintf(out, "\nThis kernel already lets you bind 443, so 'grove daemon' can take it.\n")
			return
		}
	}

	fmt.Fprint(out, `
Port 443 still needs privileges grove does not have. Lowering the unprivileged
port floor is one line, survives every reinstall, and leaves the daemon running
as you rather than as root:

  echo 'net.ipv4.ip_unprivileged_port_start=443' | sudo tee /etc/sysctl.d/60-grove.conf
  sudo sysctl --system

Until then: grove daemon --listen 127.0.0.1:8443
`)
}
