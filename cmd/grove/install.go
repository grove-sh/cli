package main

import (
	"crypto/x509"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/trust"
)

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
setting you should apply yourself.

Nothing here starts a daemon or arranges for one to start later. Grove runs
while you are using it: any grove exec starts one, and grove start does it on
its own.`,
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
			fmt.Fprintf(w, "runtime bundle\t%s\n", settleBundle(stateDir, root.Certificate(), root.RootPEM()))
			w.Flush()

			reportPrivilegedPorts(out, stateDir)
			return nil
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA and bundle")
	cmd.Flags().BoolVar(&installTrust, "trust", true, "install the root into the system trust stores")
	return cmd
}

// settleBundle leaves exactly one bundle in play for the runtimes that carry
// their own roots, deleting grove's copy the moment the system file will do.
// trust.Bundle explains which is which.
func settleBundle(stateDir string, root *x509.Certificate, rootPEM []byte) string {
	system := trust.SystemBundle()
	if system == "" {
		return "no bundle on this system, so runtimes carrying their own will not trust grove"
	}

	if trust.SystemBundleTrusts(root) {
		if err := trust.RemoveBundle(stateDir); err != nil {
			return fmt.Sprintf("%s carries this root, but grove's stale copy remains: %v", system, err)
		}
		return system + ", which carries this root"
	}

	merged, err := trust.WriteBundle(stateDir, rootPEM)
	if err != nil {
		return fmt.Sprintf("could not merge one: %v", err)
	}
	return merged + ", merged because " + system + " does not carry this root"
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
func reportPrivilegedPorts(out io.Writer, stateDir string) {
	access := platform.PrivilegedPorts()
	if access.Allowed {
		fmt.Fprintf(out, "\n%s\n", access.Detail)
		// A yes can still leave something worth doing, since a floor that
		// clears 443 but not 80 costs only the http redirect.
		if access.Advice != "" {
			fmt.Fprintf(out, "\n%s\n", access.Advice)
		}
		return
	}
	fmt.Fprintf(out, "\n%s.\n", access.Detail)

	// A platform with files to stage says what to do with them, and knows more
	// than the static advice does. Failing to stage them is worth saying out
	// loud rather than falling back to advice that names nothing.
	advice, err := platform.PrepareRedirect(stateDir)
	switch {
	case err != nil:
		fmt.Fprintf(out, "\ngrove could not prepare the redirect: %v\n", err)
		return
	case advice != "":
		fmt.Fprintf(out, "\n%s\n", advice)
		return
	}
	if access.Advice != "" {
		fmt.Fprintf(out, "\n%s\n", access.Advice)
	}
}
