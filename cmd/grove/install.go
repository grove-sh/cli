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
	var installTrust, yes bool

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Generate grove's certificate authority and trust it on this machine",
		Long: `Generate grove's certificate authority and trust it on this machine.

Installing into the system trust store needs elevated privileges, so this may
ask for your password. Everything else is written under your own state
directory. Re-running is safe: an existing CA is reused, and an already trusted
root is left alone.

Reaching port 443 is a machine wide change, a sysctl on Linux and a pf redirect
on macOS, so it is printed in full and run only when you say so: at the prompt,
or in advance with --yes. Where there is no terminal to ask, the steps are
printed and left to you.

Nothing here starts a daemon or arranges for one to start later. Grove runs
while you are using it: any grove exec starts one, and grove start does it on
its own.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			paint := styles(out)

			if err := makePrivate(stateDir, out); err != nil {
				return err
			}
			root, err := ca.OpenOrCreate(stateDir)
			if err != nil {
				return err
			}

			trusted := trust.Trusted(root.Certificate())
			state, tone := "already trusted", ok
			switch {
			case !trusted && !installTrust:
				state, tone = "not trusted, skipped by --trust=false", warn
			case !trusted:
				fmt.Fprintln(out, paint.dim("installing the root into your trust stores, sudo may ask for your password"))
				if err := trust.Install(root.Certificate()); err != nil {
					return err
				}
				state = "installed"
			}
			bundle, bundleTone := settleBundle(stateDir, root.Certificate(), root.RootPEM())
			access := platform.PrivilegedPorts()

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "authority\t%s\n", filepath.Join(stateDir, trust.RootFile))
			fmt.Fprintf(w, "trust store\t%s\n", paint.paint(tone)(state))
			fmt.Fprintf(w, "runtime bundle\t%s\n", paint.paint(bundleTone)(bundle))
			fmt.Fprintf(w, "port 443\t%s\n", paint.paint(portTone(access))(access.Detail))
			w.Flush()

			return offerPorts(out, cmd.ErrOrStderr(), cmd.InOrStdin(), stateDir, access, yes)
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA and bundle")
	cmd.Flags().BoolVar(&installTrust, "trust", true, "install the root into the system trust stores")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "run the privileged step without asking")
	return cmd
}

// Allowed with advice left is a yes with something still to do, so it reads
// as a caution rather than a pass.
func portTone(access platform.PortAccess) string {
	if access.Allowed && access.Advice == "" {
		return ok
	}
	return warn
}

// A platform with files to stage knows more than the static advice, and
// failing to stage beats falling back to advice that names nothing.
func offerPorts(out, errOut io.Writer, in io.Reader, stateDir string, access platform.PortAccess, yes bool) error {
	paint := styles(out)
	if access.Allowed && access.Advice == "" {
		return nil
	}
	plan, err := platform.PreparePorts(stateDir)
	if err != nil {
		fmt.Fprintf(out, "\n%s\n", paint.bad("grove could not prepare the change to port 443: "+err.Error()))
		return nil
	}
	if plan.Empty() {
		if access.Advice != "" {
			fmt.Fprintf(out, "\n%s\n", paint.warn(access.Advice))
		}
		return nil
	}

	applied, err := elevationFor(yes, in, out, errOut).offer(plan)
	if err != nil || !applied {
		return err
	}
	// Asked again rather than assumed: the platform reads the machine, so
	// this line is what actually changed and not what was meant to.
	after := platform.PrivilegedPorts()
	fmt.Fprintf(out, "\n%s\n", paint.paint(portTone(after))(after.Detail))
	return nil
}

// Exactly one bundle in play, so grove's copy goes the moment the system file
// will do. trust.Bundle explains which is which.
func settleBundle(stateDir string, root *x509.Certificate, rootPEM []byte) (string, string) {
	system := trust.SystemBundle()
	if system == "" {
		return "no bundle on this system, so runtimes carrying their own will not trust grove", warn
	}

	if trust.SystemBundleTrusts(root) {
		if err := trust.RemoveBundle(stateDir); err != nil {
			return fmt.Sprintf("%s carries this root, but grove's stale copy remains: %v", system, err), warn
		}
		return system + ", which carries this root", ok
	}

	merged, err := trust.WriteBundle(stateDir, rootPEM)
	if err != nil {
		return fmt.Sprintf("could not merge one: %v", err), bad
	}
	return merged + ", merged because " + system + " does not carry this root", ok
}

func newUninstallCommand() *cobra.Command {
	var stateDir, socket string
	var removeTrust, force, yes bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Stop grove and remove its root from this machine's trust stores",
		Long: `Remove grove's root from this machine's trust stores.

The CA files stay on disk, so a later 'grove install' trusts the same root
rather than generating another one. Certificates already issued keep working
for anything that still trusts the root.

Where a platform needed a privileged step to reach port 443, this prints the
step that undoes it and offers to run it, the same way install did the one that
set it up. --yes answers in advance.

Grove is stopped too, since a root the machine no longer trusts leaves nothing
worth serving. Every lease on the machine goes with it, so this refuses while
anything is answering on a port grove leased.`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			paint := styles(out)
			held, unread := whatItHolds(socket)
			if !force {
				if unread != nil {
					return bareError{cannotTell(unread, "uninstall")}
				}
				if refusal := refuseToUninstall(held); refusal != "" {
					return bareError{refusal}
				}
			}

			root, err := ca.Open(stateDir)
			if err != nil {
				return err
			}
			state, tone := "removed", ok
			switch {
			case !removeTrust:
				state, tone = "left alone by --trust=false", warn
			case !trust.Trusted(root.Certificate()):
				state, tone = "this root is not in the system trust store", ok
			default:
				fmt.Fprintln(out, paint.dim("removing the root from your trust stores, this may ask for your password or authorization"))
				if err := trust.Uninstall(root.Certificate()); err != nil {
					return err
				}
			}

			// After the trust store, which is what someone came for. The
			// redirect outlives this process either way.
			plan, planErr := platform.RemovePorts(stateDir)
			ports, portsTone := "nothing of grove's left in place", ok
			switch {
			case planErr != nil:
				ports, portsTone = "grove could not stage the removal: "+planErr.Error(), bad
			case !plan.Empty():
				ports, portsTone = plan.Summary, warn
			}

			w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "trust store\t%s\n", paint.paint(tone)(state))
			fmt.Fprintf(w, "port 443\t%s\n", paint.paint(portsTone)(ports))
			w.Flush()

			applied, err := elevationFor(yes, cmd.InOrStdin(), out, cmd.ErrOrStderr()).offer(plan)
			if err != nil {
				return err
			}
			if applied {
				fmt.Fprintf(out, "\n%s\n", paint.good("port 443 handed back"))
			}

			// Last, so a trust store that would not budge leaves grove serving
			// rather than stopped for nothing. The guard above already refused
			// if this would cost a running command its route.
			client, err := daemon.Dial(socket)
			if err != nil {
				return nil
			}
			defer client.Close()
			fmt.Fprintln(out)
			dropping, _ := whatItHolds(socket)
			return stopWith(out, client, socket, dropping)
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA")
	// Untrusting a root asks macOS for authorization nobody can give on a
	// runner, so the keychain half has to be skippable for CI to run the
	// removal this offers.
	cmd.Flags().BoolVar(&removeTrust, "trust", true, "remove the root from the system trust stores")
	cmd.Flags().StringVar(&socket, "socket", daemon.DefaultSocket(), "control socket path")
	cmd.Flags().BoolVar(&force, "force", false, "untrust even while a running command is served over https")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "run the privileged step without asking")
	return cmd
}

// Tightens an existing directory too, since the CA's private key lives there.
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
