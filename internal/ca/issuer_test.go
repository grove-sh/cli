package ca_test

import (
	"bufio"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/proxy"
)

const domain = "grov.site"

func everyName(string) bool { return true }

func issuer(t *testing.T) (*ca.Issuer, *x509.CertPool) {
	t.Helper()
	root, err := ca.OpenOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root.RootPEM()) {
		t.Fatal("root not added to pool")
	}
	return ca.NewIssuer(root, domain, everyName), pool
}

func TestIssuerServesAnyDepthUnderTheDomain(t *testing.T) {
	iss, pool := issuer(t)

	for _, name := range []string{
		"app." + domain,
		"my-app.org." + domain,
		"web.my-app.org." + domain,
		domain,
	} {
		leaf, err := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := leaf.Leaf.VerifyHostname(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
			DNSName: name, Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		}); err != nil {
			t.Errorf("%s does not chain to the root: %v", name, err)
		}
	}
}

func TestIssuerRefusesNamesItHasNoClaimTo(t *testing.T) {
	iss, _ := issuer(t)

	for _, name := range []string{"example.com", "grov.site.evil.com", "notgrov.site"} {
		if _, err := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: name}); err == nil {
			t.Errorf("%s: minted a leaf for a name outside the domain", name)
		}
	}
}

func TestIssuerCachesPerName(t *testing.T) {
	iss, _ := issuer(t)

	for i := 0; i < 5; i++ {
		if _, err := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: "app." + domain}); err != nil {
			t.Fatal(err)
		}
	}
	if got := iss.Minted(); got != 1 {
		t.Errorf("minted %d leaves for one name, want 1", got)
	}
}

// Over the tailnet any peer can offer any hostname, so only routed names may
// cost a signature; the rest share the wildcard.
func TestIssuerMintsOnlyForRoutedNames(t *testing.T) {
	root, err := ca.OpenOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	const routed = "app.capella." + domain
	iss := ca.NewIssuer(root, domain, func(name string) bool { return name == routed })

	leaf, err := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: routed})
	if err != nil {
		t.Fatal(err)
	}
	if err := leaf.Leaf.VerifyHostname(routed); err != nil {
		t.Errorf("routed name got a leaf that does not cover it: %v", err)
	}

	for i := 0; i < 50; i++ {
		name := fmt.Sprintf("x%d.capella.%s", i, domain)
		leaf, err := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := leaf.Leaf.VerifyHostname("anything." + domain); err != nil {
			t.Errorf("%s: unrouted name did not get the wildcard: %v", name, err)
		}
	}
	leaf, err = iss.GetCertificate(&tls.ClientHelloInfo{ServerName: domain})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{domain, "anything." + domain} {
		if err := leaf.Leaf.VerifyHostname(name); err != nil {
			t.Errorf("unrouted apex did not get the shared wildcard covering %s: %v", name, err)
		}
	}
	if got := iss.Minted(); got != 2 {
		t.Errorf("minted %d leaves, want 2: one routed name and the shared wildcard", got)
	}
}

// The point of the spike: a real handshake and a routed response on a hostname
// two labels deep, which a wildcard leaf cannot cover.
func TestMultiLevelHostnameHandshakesAndRoutes(t *testing.T) {
	root, err := ca.OpenOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(root.RootPEM())

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "served %s", r.Host)
	}))
	t.Cleanup(upstream.Close)

	const host = "my-app.org." + domain
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := proxy.New()
	p.SetRoutes([]proxy.Route{{Host: host, Upstream: strings.TrimPrefix(upstream.URL, "http://")}})
	iss := ca.NewIssuer(root, domain, p.Routes)
	go p.Serve(ln, iss.GetCertificate)
	t.Cleanup(func() { ln.Close() })

	conn, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: host, RootCAs: pool})
	if err != nil {
		t.Fatalf("handshake for %s: %v", host, err)
	}
	defer conn.Close()

	req, _ := http.NewRequest("GET", "https://"+host+"/", nil)
	if err := req.Write(conn); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
}

func BenchmarkIssueColdLeaf(b *testing.B) {
	root, err := ca.OpenOrCreate(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	iss := ca.NewIssuer(root, domain, everyName)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		name := fmt.Sprintf("c%d.app.%s", i, domain)
		if _, err := iss.GetCertificate(&tls.ClientHelloInfo{ServerName: name}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkIssueCachedLeaf(b *testing.B) {
	root, err := ca.OpenOrCreate(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	iss := ca.NewIssuer(root, domain, everyName)
	hello := &tls.ClientHelloInfo{ServerName: "app." + domain}
	if _, err := iss.GetCertificate(hello); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := iss.GetCertificate(hello); err != nil {
			b.Fatal(err)
		}
	}
}
