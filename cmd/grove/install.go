package main

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/service"
	"github.com/grove-sh/cli/internal/trust"
)

func newInstallCommand() *cobra.Command {
	var stateDir, listen string
	var installTrust, installService bool

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
			fmt.Fprintf(w, "runtime bundle\t%s\n", settleBundle(stateDir, root.Certificate(), root.RootPEM()))
			w.Flush()

			reportService(out, listen, installService)
			reportPrivilegedPorts(out)
			return nil
		},
	}

	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA and bundle")
	cmd.Flags().BoolVar(&installTrust, "trust", true, "install the root into the system trust stores")
	cmd.Flags().BoolVar(&installService, "service", true, "install and start the daemon as a systemd user unit")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:443", "address the installed service serves HTTPS on")
	return cmd
}

// settleBundle decides what python's requests and Deno should be pointed at.
// Installing into the OS store rewrites the system bundle on Linux, so there
// the system's own file is the answer and any copy grove made is deleted. The
// keychain install on macOS leaves that file untouched, so there grove merges
// its own copy, which is the only way those runtimes can trust it at all.
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

// reportService registers the daemon with whatever keeps processes running
// here, and says why not when there is nothing to register with.
func reportService(out io.Writer, listen string, wanted bool) {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	defer w.Flush()

	if !wanted {
		fmt.Fprintf(w, "service\tskipped by --service=false\n")
		return
	}
	if supported, reason := service.Supported(); !supported {
		fmt.Fprintf(w, "service\t%s\n", reason)
		return
	}

	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(w, "service\t%v\n", err)
		return
	}
	path, err := service.Install(executable, listen)
	if err != nil {
		fmt.Fprintf(w, "service\t%v\n", err)
		return
	}
	fmt.Fprintf(w, "service\t%s\n", path)

	// Redirecting the definition means the real service manager is off limits,
	// so say that rather than claiming work nobody did.
	if !service.Managed() {
		fmt.Fprintf(w, "service\twritten only, GROVE_SERVICE_DIR keeps grove away from the service manager\n")
		return
	}

	if err := service.Enable(); err != nil {
		fmt.Fprintf(w, "service\tcould not enable it: %v\n", err)
		return
	}

	// Starting a daemon that cannot bind its port leaves a Type=notify service
	// waiting for a readiness notification that never comes.
	if held := whatHolds(listen); held != "" {
		fmt.Fprintf(w, "service\tenabled, not started yet: %s\n", held)
		return
	}
	if err := service.Start(); err != nil {
		fmt.Fprintf(w, "service\tenabled, but it did not start: %v\n", err)
		return
	}
	fmt.Fprintf(w, "service\tenabled and running\n")

	if service.Lingering() {
		fmt.Fprintf(w, "lingering\talready on, so it survives logout\n")
		return
	}
	if err := service.EnableLingering(); err != nil {
		fmt.Fprintf(w, "lingering\toff, and grove could not turn it on: %v\n", err)
		fmt.Fprintf(w, "lingering\trun: loginctl enable-linger $USER\n")
		return
	}
	fmt.Fprintf(w, "lingering\tturned on, so it survives logout\n")
}

// whatHolds reports why an address cannot be bound, or nothing when it can.
func whatHolds(address string) string {
	ln, err := net.Listen("tcp", address)
	if err == nil {
		ln.Close()
		return ""
	}
	switch {
	case errors.Is(err, syscall.EACCES):
		return address + " needs privileges grove does not have yet"
	case errors.Is(err, syscall.EADDRINUSE):
		return address + " is already in use"
	}
	return err.Error()
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
	access := platform.PrivilegedPorts()
	if access.Allowed {
		fmt.Fprintf(out, "\n%s\n", access.Detail)
		return
	}
	fmt.Fprintf(out, "\n%s.\n", access.Detail)
	if access.Advice != "" {
		fmt.Fprintf(out, "\n%s\n", access.Advice)
	}
}
