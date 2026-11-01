package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/proxy"
	"github.com/grove-sh/cli/internal/tailnet"
	"github.com/grove-sh/cli/internal/trust"
)

// Peer roots live beside this machine's own, one file per host, so remove and
// uninstall take back exactly what trust added and nothing the machine's owner
// trusted some other way.
const peersDir = "peers"

// A tailnet device name is one DNS label. Checked before it becomes part of a
// path under the state directory.
var hostLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func newTrustCommand() *cobra.Command {
	var stateDir, send string
	var remove bool

	cmd := &cobra.Command{
		Use:   "trust [host]",
		Short: "Trust another machine's grove root, or send this machine's to a device",
		Long: `Trust another machine's grove root, or send this machine's to a device.

Each machine that runs grove has its own root, so opening another machine's
hostnames means trusting its root too. grove trust <host> fetches the root of a
machine sharing on your tailnet and installs it into this machine's trust
stores, which may ask for your password. It connects to that machine's tailnet
address, taken from Tailscale rather than DNS, and Tailscale only lets that
machine answer there. Grove also checks that the certificate it was served
chains to the root it received, and prints the fingerprint.

grove trust --send <device> sends this machine's root to a phone or another
device over Taildrop, and says how to install it there. Run it on each machine
whose hostnames the device should open.

With no arguments, it prints this machine's root fingerprint and the roots it
trusts from other machines. Linux only for now, apart from that listing.`,
		Example: `  grove trust                  # this machine's fingerprint, and the peers it trusts
  grove trust capella          # trust capella's root here
  grove trust --send pixel-9   # send this machine's root to a phone
  grove trust --remove capella # stop trusting capella's root`,
		Args: usageArgs(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			client := tailnet.New(platform.TailscaleSocket())
			switch {
			case send != "" && (len(args) > 0 || remove):
				return usageError{errors.New("--send takes the device on its own, with no host and no --remove")}
			case send != "":
				return sendRoot(out, client, stateDir, send)
			case remove && len(args) == 0:
				return usageError{errors.New("--remove needs the host to stop trusting")}
			case remove:
				return untrustPeer(out, stateDir, args[0])
			case len(args) == 1:
				return trustPeer(out, client, stateDir, args[0])
			}
			return listTrust(out, stateDir)
		},
	}
	cmd.Flags().StringVar(&stateDir, "state-dir", daemon.StateDir(), "directory holding the CA and trusted peer roots")
	cmd.Flags().StringVar(&send, "send", "", "send this machine's root to a device over Taildrop")
	cmd.Flags().BoolVar(&remove, "remove", false, "stop trusting the named host's root")
	return cmd
}

func fingerprint(cert *x509.Certificate) string {
	sum := sha256.Sum256(cert.Raw)
	return hex.EncodeToString(sum[:])
}

func peerFile(stateDir, host string) string {
	return filepath.Join(stateDir, peersDir, host+".crt")
}

func checkHost(host string) (string, error) {
	host = strings.ToLower(host)
	if !hostLabel.MatchString(host) {
		return "", fmt.Errorf("%q is not a tailnet device name; tailscale status lists them", host)
	}
	return host, nil
}

func listTrust(out io.Writer, stateDir string) error {
	root, err := ca.Open(stateDir)
	if err != nil {
		if errors.Is(err, ca.ErrNoAuthority) {
			return fmt.Errorf("%w; run %s install first", err, invocation())
		}
		return err
	}
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "this machine\t%s\n", fingerprint(root.Certificate()))
	for _, peer := range trustedPeers(stateDir) {
		state := fingerprint(peer.cert)
		if !trust.Trusted(peer.cert) {
			state += "  (no longer in the trust store; run " + invocation() + " trust " + peer.host + " again)"
		}
		fmt.Fprintf(w, "%s\t%s\n", peer.host, state)
	}
	return w.Flush()
}

type trustedPeer struct {
	host string
	cert *x509.Certificate
}

func trustedPeers(stateDir string) []trustedPeer {
	entries, _ := os.ReadDir(filepath.Join(stateDir, peersDir))
	var peers []trustedPeer
	for _, e := range entries {
		host, ok := strings.CutSuffix(e.Name(), ".crt")
		if !ok {
			continue
		}
		cert, err := readCert(filepath.Join(stateDir, peersDir, e.Name()))
		if err != nil {
			continue
		}
		peers = append(peers, trustedPeer{host: host, cert: cert})
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].host < peers[j].host })
	return peers
}

func readCert(path string) (*x509.Certificate, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parseRoot(raw)
}

// parseRoot accepts one CA certificate and nothing else, so a peer cannot hand
// over a leaf or a bundle and have it installed as a root.
func parseRoot(raw []byte) (*x509.Certificate, error) {
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(strings.TrimSpace(string(rest))) > 0 {
		return nil, errors.New("not a single PEM certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	if !cert.IsCA || !cert.BasicConstraintsValid {
		return nil, errors.New("not a certificate authority")
	}
	return cert, nil
}

func trustPeer(out io.Writer, client *tailnet.Client, stateDir, host string) error {
	paint := styles(out)
	host, err := checkHost(host)
	if err != nil {
		return err
	}
	if platform.TailscaleSocket() == "" {
		return errors.New("trusting another machine's root is Linux-only for now")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	status, err := client.Status(ctx)
	switch {
	case errors.Is(err, tailnet.ErrUnavailable):
		return errors.New("tailscaled is not answering, so grove cannot reach " + host + "; check tailscale status")
	case err != nil:
		return err
	case !status.Running:
		return errors.New("tailscale is not connected; run tailscale up first")
	}
	peer, ok := status.Peer(host)
	if !ok || !peer.IPv4.IsValid() {
		return fmt.Errorf("no device called %s on %s; tailscale status lists them", host, status.Tailnet)
	}

	cert, raw, err := fetchRoot(ctx, host+"."+defaultDomain, net.JoinHostPort(peer.IPv4.String(), "443"))
	if err != nil {
		return fmt.Errorf("fetching %s's root: %w\nis %s sharing? run %s share there", host, err, host, invocation())
	}
	if own, err := ca.Open(stateDir); err == nil && fingerprint(own.Certificate()) == fingerprint(cert) {
		return fmt.Errorf("%s served this machine's own root; nothing to add", host)
	}

	fmt.Fprintf(out, "%s's root  %s\n", host, fingerprint(cert))
	if trust.Trusted(cert) {
		fmt.Fprintln(out, paint.good("already trusted"))
	} else {
		fmt.Fprintln(out, paint.dim("installing it into your trust stores, sudo may ask for your password"))
		if err := trust.Install(cert); err != nil {
			return err
		}
		fmt.Fprintln(out, paint.good("installed"))
	}
	if err := os.MkdirAll(filepath.Join(stateDir, peersDir), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(peerFile(stateDir, host), raw, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nhttps://<context>.%s.%s now opens here without a warning.\n", host, defaultDomain)
	return nil
}

// fetchRoot asks the daemon at addr for its root and refuses one that the
// connection's own certificate does not chain to.
//
// Verification is skipped for this one request because nothing here trusts the
// peer's root yet; it is the thing being fetched. What stands in for it is the
// address: addr is the peer's tailnet IP from Tailscale, never a DNS answer, and
// WireGuard only lets that peer's node key answer there.
func fetchRoot(ctx context.Context, host, addr string) (*x509.Certificate, []byte, error) {
	var dialer net.Dialer
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, "tcp", addr)
			},
			TLSClientConfig: &tls.Config{ServerName: host, InsecureSkipVerify: true},
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+host+proxy.RootPath, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%s answered %s, so it may run a grove without grove trust", host, resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return nil, nil, err
	}
	cert, err := parseRoot(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("what %s served is %v", host, err)
	}
	if resp.TLS == nil || len(resp.TLS.PeerCertificates) == 0 {
		return nil, nil, errors.New("the connection carried no certificate")
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	intermediates := x509.NewCertPool()
	for _, c := range resp.TLS.PeerCertificates[1:] {
		intermediates.AddCert(c)
	}
	if _, err := resp.TLS.PeerCertificates[0].Verify(x509.VerifyOptions{
		DNSName: host, Roots: pool, Intermediates: intermediates,
	}); err != nil {
		return nil, nil, fmt.Errorf("the certificate %s served does not chain to the root it offered: %w", host, err)
	}
	return cert, raw, nil
}

func untrustPeer(out io.Writer, stateDir, host string) error {
	host, err := checkHost(host)
	if err != nil {
		return err
	}
	cert, err := readCert(peerFile(stateDir, host))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("grove trusts no root from %s; %s trust lists the ones it does", host, invocation())
	} else if err != nil {
		return err
	}
	fmt.Fprintln(out, styles(out).dim("removing "+host+"'s root from your trust stores, sudo may ask for your password"))
	if err := trust.Uninstall(cert); err != nil {
		return err
	}
	if err := os.Remove(peerFile(stateDir, host)); err != nil {
		return err
	}
	fmt.Fprintf(out, "stopped trusting %s's root %s\n", host, fingerprint(cert))
	return nil
}

func sendRoot(out io.Writer, client *tailnet.Client, stateDir, device string) error {
	device, err := checkHost(device)
	if err != nil {
		return err
	}
	if platform.TailscaleSocket() == "" {
		return errors.New("sending the root over Taildrop is Linux-only for now")
	}
	root, err := ca.Open(stateDir)
	if err != nil {
		if errors.Is(err, ca.ErrNoAuthority) {
			return fmt.Errorf("%w; run %s install first", err, invocation())
		}
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status, err := client.Status(ctx)
	switch {
	case errors.Is(err, tailnet.ErrUnavailable):
		return errors.New("tailscaled is not answering; check tailscale status")
	case err != nil:
		return err
	case !status.Running:
		return errors.New("tailscale is not connected; run tailscale up first")
	}
	peer, ok := status.Peer(device)
	if !ok {
		return fmt.Errorf("no device called %s on %s; tailscale status lists them", device, status.Tailnet)
	}
	if !peer.Online {
		return fmt.Errorf("%s is offline; open Tailscale on it, then try again", device)
	}

	// Named for the machine it vouches for, since a phone collects one per
	// machine and the names are all the install screen shows.
	self := status.Host
	if record, shared, err := tailnet.LoadShare(stateDir); err == nil && shared {
		self = record.Host
	}
	name := "grove-" + self + "-root.crt"
	fmt.Fprintln(out, styles(out).dim("sending "+name+" to "+device+"; if this waits, open Tailscale on "+device))
	sendCtx, cancelSend := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelSend()
	err = client.SendFile(sendCtx, peer, name, root.RootPEM())
	switch {
	case errors.Is(err, tailnet.ErrNotAccepted):
		return fmt.Errorf("%s did not take the file within two minutes; open Tailscale on it so it can receive, then try again", device)
	case errors.Is(err, tailnet.ErrDenied):
		return errors.New("this user may not send files over Tailscale; run " + tailnet.OperatorFix + ", then try again")
	case err != nil:
		return err
	}

	fmt.Fprintf(out, "sent %s to %s\nfingerprint  %s\n\n", name, device, fingerprint(root.Certificate()))
	switch strings.ToLower(peer.OS) {
	case "android":
		fmt.Fprintf(out, "On the phone, Tailscale saves it to Downloads. Install it from Settings,\n"+
			"Security, Encryption & credentials, Install a certificate, CA certificate,\n"+
			"and pick %s.\n", name)
	case "ios":
		fmt.Fprintf(out, "On the phone, Taildrop saves it in Files, under Tailscale. Tap %s,\n"+
			"install the profile in Settings, General, VPN & Device Management, then turn\n"+
			"on full trust in Settings, General, About, Certificate Trust Settings.\n", name)
	default:
		fmt.Fprintf(out, "On %s, add %s to its trust store, or run %s trust %s there\n"+
			"if it runs grove.\n", device, name, invocation(), self)
	}
	return nil
}
