package ca_test

import (
	"crypto/x509"
	"os"
	"path/filepath"
	"testing"

	"github.com/grove-sh/cli/internal/ca"
)

func TestOpenGeneratesThenReuses(t *testing.T) {
	dir := t.TempDir()

	first, err := ca.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ca.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if string(first.RootPEM()) != string(second.RootPEM()) {
		t.Error("second Open generated a new root; trust stores would fill up with stale CAs")
	}

	info, err := os.Stat(filepath.Join(dir, "root.key"))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("root.key mode = %o, want 600", perm)
	}
}

func TestLeafCoversOneLabelOnly(t *testing.T) {
	root, err := ca.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := root.Leaf("*.grov.site", "grov.site")
	if err != nil {
		t.Fatal(err)
	}

	for _, host := range []string{"app1.grov.site", "app1-feat1.grov.site", "grov.site"} {
		if err := leaf.Leaf.VerifyHostname(host); err != nil {
			t.Errorf("%s: %v", host, err)
		}
	}
	if err := leaf.Leaf.VerifyHostname("api.app1.grov.site"); err == nil {
		t.Error("api.app1.grov.site validated; a wildcard covers one label, so subdomains must fail")
	}
}

func TestLeafChainsToRoot(t *testing.T) {
	root, err := ca.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := root.Leaf("*.grov.site", "grov.site")
	if err != nil {
		t.Fatal(err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(root.RootPEM()) {
		t.Fatal("RootPEM did not parse")
	}
	if _, err := leaf.Leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		DNSName:   "app1-feat1.grov.site",
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}); err != nil {
		t.Fatal(err)
	}
}
