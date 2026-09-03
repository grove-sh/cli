// Package trust installs grove's root into the stores that browsers and
// language runtimes actually read.
package trust

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"

	"github.com/smallstep/truststore"
)

const RootFile = "root.crt"

// Install writes the root into the OS, Firefox, and Java stores. On Linux and
// macOS this shells out to sudo, so callers warn before calling it.
func Install(root *x509.Certificate) error {
	return truststore.Install(root, truststore.WithFirefox(), truststore.WithJava())
}

func Uninstall(root *x509.Certificate) error {
	return truststore.Uninstall(root, truststore.WithFirefox(), truststore.WithJava())
}

// Trusted reports whether the system already accepts the root, so grove can
// skip a password prompt it does not need.
func Trusted(root *x509.Certificate) bool {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return false
	}
	_, err = root.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
	})
	return err == nil
}

// Go reads these in order when SSL_CERT_FILE is unset. macOS has no such file,
// which is why WriteBundle can come back empty there.
var systemBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

func SystemBundle() string {
	for _, path := range systemBundles {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// SystemBundleTrusts reports whether the system bundle carries grove's root.
// Installing into the OS trust store rewrites that bundle on Linux, so after a
// successful install the answer is yes and grove needs no copy of its own.
func SystemBundleTrusts(root *x509.Certificate) bool {
	path := SystemBundle()
	if path == "" {
		return false
	}
	rest, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return false
		}
		if bytes.Equal(block.Bytes, root.Raw) {
			return true
		}
	}
}

// Env reports the variables a child needs for runtimes that ignore the OS trust
// store. Anything reading the OS store already works once the root is installed,
// so nothing points at a private copy that could go stale. A variable the caller
// already set is left alone, since overwriting it would break a machine that
// points it somewhere deliberately.
func Env(stateDir string) []string {
	root := existing(filepath.Join(stateDir, RootFile))
	if root == "" {
		return nil
	}

	var out []string
	add := func(name, path string) {
		if path == "" {
			return
		}
		if _, ok := os.LookupEnv(name); ok {
			return
		}
		out = append(out, name+"="+path)
	}

	// Node compiles in its own roots. This one is additive, so it takes the
	// root by itself.
	add("NODE_EXTRA_CA_CERTS", root)

	// Python's requests and Deno carry their own bundles too, but these
	// variables replace a trust store rather than adding to it, so they get the
	// system bundle, which the install added grove's root to.
	system := SystemBundle()
	add("REQUESTS_CA_BUNDLE", system)
	add("DENO_CERT", system)
	return out
}

func existing(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}
