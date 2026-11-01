package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/proxy"
)

// servePeer stands up what a sharing machine's daemon runs: a proxy answering
// the root on its zone's bare hostname, with leaves from signer. offered is the
// root it hands out, which an impostor makes differ from signer.
func servePeer(t *testing.T, signer, offered *ca.CA, host string) string {
	t.Helper()
	p := proxy.New()
	p.ServeRoot(host, offered.RootPEM())
	iss := ca.NewIssuer(signer, "grov.site", "", p.Routes)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go p.Serve(ln, iss.GetCertificate)
	t.Cleanup(func() { p.Shutdown(context.Background()) })
	return ln.Addr().String()
}

func newCA(t *testing.T) *ca.CA {
	t.Helper()
	root, err := ca.OpenOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestFetchRootFromASharingPeer(t *testing.T) {
	root := newCA(t)
	addr := servePeer(t, root, root, "capella.grov.site")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cert, raw, err := fetchRoot(ctx, "capella.grov.site", addr)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint(cert) != fingerprint(root.Certificate()) || !bytes.Equal(raw, root.RootPEM()) {
		t.Error("fetched a different root than the peer holds")
	}
}

// The check that makes the fetch worth trusting beyond the tailnet address: a
// root the connection's certificate does not chain to is refused.
func TestFetchRootRefusesARootTheConnectionDoesNotChainTo(t *testing.T) {
	signer, offered := newCA(t), newCA(t)
	addr := servePeer(t, signer, offered, "capella.grov.site")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, _, err := fetchRoot(ctx, "capella.grov.site", addr); err == nil || !strings.Contains(err.Error(), "does not chain") {
		t.Errorf("got %v, want a refusal", err)
	}
}

// A grove from before trust answers the path with its 503 page.
func TestFetchRootFromAGroveWithoutTrust(t *testing.T) {
	root := newCA(t)
	addr := servePeer(t, root, root, "elsewhere.grov.site")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if _, _, err := fetchRoot(ctx, "capella.grov.site", addr); err == nil || !strings.Contains(err.Error(), "503") {
		t.Errorf("got %v, want the status named", err)
	}
}

func TestParseRootTakesOneAuthorityOnly(t *testing.T) {
	root := newCA(t)
	if _, err := parseRoot(root.RootPEM()); err != nil {
		t.Fatalf("root: %v", err)
	}
	leaf, err := root.Leaf("app.capella.grov.site")
	if err != nil {
		t.Fatal(err)
	}
	leafPEM := pemOf(leaf)
	if _, err := parseRoot(leafPEM); err == nil {
		t.Error("accepted a leaf as a root")
	}
	if _, err := parseRoot(append(root.RootPEM(), root.RootPEM()...)); err == nil {
		t.Error("accepted a bundle")
	}
	if _, err := parseRoot([]byte("<html>503</html>")); err == nil {
		t.Error("accepted something that is not PEM")
	}
}

func pemOf(cert *tls.Certificate) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
}

// The host becomes a file name under the state directory.
func TestTrustRefusesNamesThatAreNotDeviceNames(t *testing.T) {
	for _, host := range []string{"../root", "a/b", "", "-capella", "capella.example"} {
		if _, err := checkHost(host); err == nil {
			t.Errorf("%q accepted", host)
		}
	}
	for _, host := range []string{"capella", "pixel-9", "Regulus"} {
		if _, err := checkHost(host); err != nil {
			t.Errorf("%q refused: %v", host, err)
		}
	}
}

func TestRemoveWithNothingTrusted(t *testing.T) {
	var out bytes.Buffer
	err := untrustPeer(&out, t.TempDir(), "capella")
	if err == nil || !strings.Contains(err.Error(), "trusts no root from capella") {
		t.Errorf("got %v", err)
	}
}

func TestListShowsThisMachineAndItsPeers(t *testing.T) {
	dir := t.TempDir()
	root, err := ca.OpenOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	peer := newCA(t)
	if err := os.MkdirAll(dir+"/peers", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peerFile(dir, "capella"), peer.RootPEM(), 0o644); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := listTrust(&out, dir); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), fingerprint(root.Certificate())) ||
		!strings.Contains(out.String(), "capella") || !strings.Contains(out.String(), fingerprint(peer.Certificate())) {
		t.Errorf("listing:\n%s", out.String())
	}
}
