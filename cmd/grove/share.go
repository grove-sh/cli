package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/tailnet"
)

const adminDNS = "https://login.tailscale.com/admin/dns"

func newShareCommand() *cobra.Command {
	var stateDir string
	var off, yes bool

	cmd := &cobra.Command{
		Use:   "share",
		Short: "Make this machine's contexts reachable from your other devices on Tailscale",
		Long: `Make this machine's contexts reachable from your other devices on Tailscale.

Sharing ties this machine to the tailnet it is on now. Every context's hostname
gains the machine's name, so app.grov.site becomes app.<host>.grov.site, and
the same worktree checked out on two machines is two hostnames. The names stay
that way off the tailnet too, where they still resolve to this machine, so a
running dev server's URL never changes under it.

While the machine is on that tailnet, the daemon answers DNS for *.<host>.grov.site
on its tailnet address and forwards tailnet port 443 to itself through Tailscale
Serve. On any other tailnet it stands down, so joining someone else's does not
offer them your dev servers. It never takes port 443 from another Serve handler;
it says which one to move instead.

One step is left to you: a split DNS entry in the Tailscale admin console that
sends *.<host>.grov.site to this machine. grove share prints it. Answering on
port 53 also means lowering the unprivileged port floor, which is printed in
full and run only when you say so, as grove install does.

Each device that opens these hostnames has to trust this machine's root.
To stop sharing with --off, connect to the recorded tailnet first.

Grove needs Tailscale operator access to configure Serve as your user. If it
reports access denied, run sudo tailscale set --operator="$USER", then try again.

Linux only for now.`,
		Example: `  grove share         # share on the tailnet this machine is on
  grove share --off   # stop sharing and go back to <context>.grov.site`,
		Args: usageArgs(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client := tailnet.New(platform.TailscaleSocket())
			if off {
				return unshare(cmd.OutOrStdout(), client, stateDir)
			}
			return share(cmd.OutOrStdout(), cmd.ErrOrStderr(), cmd.InOrStdin(), client, stateDir, yes)
		},
	}
	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA and the share record")
	cmd.Flags().BoolVar(&off, "off", false, "stop sharing")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "run the privileged step without asking")
	return cmd
}

func share(out, errOut io.Writer, in io.Reader, client *tailnet.Client, stateDir string, yes bool) error {
	paint := styles(out)
	if platform.TailscaleSocket() == "" {
		return errors.New("sharing on a tailnet is Linux-only for now")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	status, err := client.Status(ctx)
	switch {
	case errors.Is(err, tailnet.ErrUnavailable):
		return errors.New("tailscaled is not answering, so there is no tailnet to share on; check `tailscale status`")
	case err != nil:
		return err
	case !status.Running:
		return errors.New("tailscale is not connected; run `tailscale up`, then " + invocation() + " share again")
	case status.Tailnet == "" || status.Host == "" || !status.IPv4.IsValid():
		return errors.New("tailscale reports no tailnet name, MagicDNS name, or IPv4 address for this machine; turn on MagicDNS in the admin console")
	}

	previous, had, err := tailnet.LoadShare(stateDir)
	if err != nil {
		return err
	}
	record := tailnet.Share{Tailnet: status.Tailnet, Host: status.Host}
	if err := tailnet.SaveShare(stateDir, record); err != nil {
		return err
	}
	zone := tailnet.Zone(defaultDomain, record, true)

	port443, tone := claimNow(ctx, client)

	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "tailnet\t%s\n", status.Tailnet)
	fmt.Fprintf(w, "hostnames\thttps://<context>.%s\n", zone)
	fmt.Fprintf(w, "address\t%s\n", status.IPv4)
	fmt.Fprintf(w, "port 443\t%s\n", paint.paint(tone)(port443))
	w.Flush()
	if had && previous.Tailnet != record.Tailnet {
		fmt.Fprintf(out, "\n%s\n", paint.warn("moved from "+previous.Tailnet+": this machine shares on one tailnet at a time"))
	}

	plan, err := platform.PrepareResolver(stateDir)
	if err != nil {
		fmt.Fprintf(out, "\n%s\n", paint.bad("grove could not prepare the change to port 53: "+err.Error()))
	} else if _, err := elevationFor(yes, in, out, errOut).offer(plan); err != nil {
		return err
	}

	fmt.Fprintf(out, "\nOne step is yours, once per tailnet, in %s:\n\n", adminDNS)
	fmt.Fprintf(out, "  Add nameserver > Custom: %s\n", status.IPv4)
	fmt.Fprintf(out, "  Restrict to domain:      %s\n", zone)
	if !had || previous != record {
		fmt.Fprintf(out, "\n%s\n", paint.warn("Every hostname on this machine has changed. Restart the daemon, then any dev\n"+
			"servers it was serving, so their URLs follow:"))
		fmt.Fprintf(out, "\n%s\n", paint.cmd("  "+invocation()+" restart"))
	}
	return nil
}

// Claim now so the command reports the result before the daemon starts retrying.
func claimNow(ctx context.Context, client *tailnet.Client) (string, string) {
	cfg, err := client.ServeConfig(ctx)
	if err != nil {
		return "unknown: " + err.Error(), warn
	}
	changed, err := cfg.Claim443(defaultDaemonOptions().listen)
	switch {
	case tailnet.IsHeld(err):
		return "served by something else; move it to another port (t3code: Settings, Tailscale Serve port) and grove takes it", warn
	case err != nil:
		return err.Error(), bad
	case !changed:
		return "forwarded to grove by Tailscale Serve", ok
	}
	if err := client.SetServeConfig(ctx, cfg); errors.Is(err, tailnet.ErrDenied) {
		return "not claimed, since this user may not change Tailscale Serve; run " + tailnet.OperatorFix + " and the daemon claims it", warn
	} else if err != nil {
		return "not claimed: " + err.Error(), warn
	}
	return "forwarded to grove by Tailscale Serve", ok
}

type serveEditor interface {
	ServeConfig(context.Context) (*tailnet.ServeConfig, error)
	SetServeConfig(context.Context, *tailnet.ServeConfig) error
}

// An uncertain release must fail: a successful --off must not leave forwarding active.
func releaseNow(ctx context.Context, client serveEditor) (said, tone string, released bool) {
	retry := invocation() + " share --off again"
	cfg, err := client.ServeConfig(ctx)
	if err != nil {
		return "cannot tell whether tailnet port 443 still forwards to grove: " + err.Error() + "; once tailscaled answers, run " + retry, bad, false
	}
	changed, err := cfg.Release443(defaultDaemonOptions().listen)
	switch {
	case err != nil:
		return "cannot tell whether tailnet port 443 still forwards to grove: " + err.Error() + "; run " + retry, bad, false
	case !changed:
		return "tailnet port 443 was not grove's, so it is left as it was", ok, false
	}
	err = client.SetServeConfig(ctx, cfg)
	switch {
	case errors.Is(err, tailnet.ErrDenied):
		return "tailnet port 443 still forwards to grove, since this user may not change Tailscale Serve; run " +
			tailnet.OperatorFix + ", then " + retry, bad, false
	case err != nil:
		return "tailnet port 443 still forwards to grove: " + err.Error() + "; run " + retry, bad, false
	}
	return "tailnet port 443 released", ok, true
}

type shareEditor interface {
	serveEditor
	Status(context.Context) (tailnet.Status, error)
}

// Remove the record first so the daemon stops reclaiming 443, even if release fails.
func unshare(out io.Writer, client shareEditor, stateDir string) error {
	paint := styles(out)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	record, had, loadErr := tailnet.LoadShare(stateDir)
	if had {
		// Releasing another profile would abandon the recorded tailnet's forwarding.
		status, err := client.Status(ctx)
		if err != nil || !status.Running || status.Tailnet != record.Tailnet {
			return fmt.Errorf("sharing is still configured on %s; connect to that tailnet, then run %s share --off again", record.Tailnet, invocation())
		}
	}
	if had || loadErr != nil {
		if err := tailnet.RemoveShare(stateDir); err != nil {
			return err
		}
	}

	released, tone, removed := releaseNow(ctx, client)

	switch {
	case !had && loadErr == nil && tone == ok && !removed:
		fmt.Fprintln(out, "this machine is not sharing")
		return nil
	case !had && loadErr == nil:
		fmt.Fprintln(out, paint.paint(tone)(released))
	default:
		fmt.Fprintf(out, "stopped sharing\n%s\n", paint.paint(tone)(released))
		if had {
			fmt.Fprintf(out, "\nThe split DNS entry for %s can go too, in %s.\n", tailnet.Zone(defaultDomain, record, true), adminDNS)
		}
		fmt.Fprintf(out, "\n%s\n", paint.warn("Hostnames go back to <context>."+defaultDomain+". Restart the daemon, then any dev\nservers it was serving:"))
		fmt.Fprintf(out, "\n%s\n", paint.cmd("  "+invocation()+" restart"))
	}
	if tone == bad {
		return &exitError{code: 1}
	}
	return nil
}

// The daemon reports unreadable records at startup; hostname lookups use the default meanwhile.
func zone() string {
	record, ok, err := tailnet.LoadShare(daemon.StateDir())
	if err != nil {
		return defaultDomain
	}
	return tailnet.Zone(defaultDomain, record, ok)
}
