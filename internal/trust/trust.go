// Package trust installs grove's root into the stores that browsers and
// language runtimes actually read.
package trust

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/smallstep/truststore"
)

const (
	RootFile   = "root.crt"
	BundleFile = "ca-bundle.pem"
)

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

// The order Go itself reads them in when SSL_CERT_FILE is unset. macOS has the
// last one, which matters: the file is there, and its trust install ignores it.
var systemBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// SystemBundle reports the file the operating system keeps its roots in, or
// nothing when it keeps them somewhere that is not a file. GROVE_SYSTEM_BUNDLE
// overrides the search, for a layout grove does not know and for tests.
func SystemBundle() string {
	if override := os.Getenv("GROVE_SYSTEM_BUNDLE"); override != "" {
		return override
	}
	for _, path := range systemBundles {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// SystemBundleTrusts reports whether the system bundle carries grove's root,
// which is what Bundle decides on.
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

// Bundle reports the file to point a runtime's trust store at, and whether it
// is grove's own copy. Installing into the OS store rewrites the system bundle
// on Linux, so there the system's own file is the answer and grove keeps no
// copy. macOS installs into the keychain and never touches its bundle file, so
// there grove has to merge one.
func Bundle(stateDir string) (path string, merged bool) {
	if own := existing(filepath.Join(stateDir, BundleFile)); own != "" {
		return own, true
	}
	return SystemBundle(), false
}

// WriteBundle merges the system roots with grove's own into one file. Use it
// only when the system file will not do: a copy of a trust store goes stale as
// the real one gains roots.
func WriteBundle(stateDir string, rootPEM []byte) (string, error) {
	system := SystemBundle()
	if system == "" {
		return "", nil
	}
	roots, err := os.ReadFile(system)
	if err != nil {
		return "", err
	}

	var joined bytes.Buffer
	joined.Write(bytes.TrimRight(roots, "\n"))
	joined.WriteString("\n")
	joined.Write(rootPEM)

	dest := filepath.Join(stateDir, BundleFile)
	temp := dest + ".tmp"
	if err := os.WriteFile(temp, joined.Bytes(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(temp, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// RemoveBundle drops grove's copy, which is what to do once the system's own
// file carries the root: one source of truth beats two, and the copy is the
// one that can go stale.
func RemoveBundle(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, BundleFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// BundleStale reports whether grove's copy predates the system roots it was
// merged from, which means it is missing whatever the system has gained.
func BundleStale(stateDir string) bool {
	own, err := os.Stat(filepath.Join(stateDir, BundleFile))
	if err != nil {
		return false
	}
	system := SystemBundle()
	if system == "" {
		return false
	}
	roots, err := os.Stat(system)
	if err != nil {
		return false
	}
	return roots.ModTime().After(own.ModTime())
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
	// variables replace a trust store rather than adding to it, so they get a
	// whole bundle: the system's own where the install updates it, and grove's
	// merged copy where it does not.
	bundle, _ := Bundle(stateDir)
	add("REQUESTS_CA_BUNDLE", bundle)
	add("DENO_CERT", bundle)
	return out
}

func existing(path string) string {
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}
