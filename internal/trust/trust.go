// Package trust installs grove's root into the stores browsers and language
// runtimes actually read.
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

// Install shells out to sudo on Linux and macOS, so callers warn first.
func Install(root *x509.Certificate) error {
	return truststore.Install(root, truststore.WithFirefox(), truststore.WithJava())
}

func Uninstall(root *x509.Certificate) error {
	return truststore.Uninstall(root, truststore.WithFirefox(), truststore.WithJava())
}

// Trusted lets grove skip a password prompt it does not need.
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

// The order Go reads them in when SSL_CERT_FILE is unset. macOS has the last
// one, which matters: the file is there, and its trust install ignores it.
var systemBundles = []string{
	"/etc/ssl/certs/ca-certificates.crt",
	"/etc/pki/tls/certs/ca-bundle.crt",
	"/etc/ssl/ca-bundle.pem",
	"/etc/pki/tls/cacert.pem",
	"/etc/pki/ca-trust/extracted/pem/tls-ca-bundle.pem",
	"/etc/ssl/cert.pem",
}

// SystemBundle is empty when the OS keeps its roots somewhere that is not a
// file. GROVE_SYSTEM_BUNDLE overrides the search, for tests and unknown layouts.
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

// Installing into the OS store rewrites the system bundle on Linux, so there
// the system's own file is the answer. macOS installs into the keychain and
// never touches its bundle file, so there grove has to merge one.
func Bundle(stateDir string) (path string, merged bool) {
	if own := existing(filepath.Join(stateDir, BundleFile)); own != "" {
		return own, true
	}
	return SystemBundle(), false
}

// WriteBundle is for when the system file will not do: a copy of a trust store
// goes stale as the real one gains roots.
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

// RemoveBundle once the system's own file carries the root: the copy is the one
// that can go stale.
func RemoveBundle(stateDir string) error {
	err := os.Remove(filepath.Join(stateDir, BundleFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// A copy predating the roots it was merged from is missing what they gained.
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

// Only for runtimes that ignore the OS store: anything reading it already works,
// so nothing is pointed at a private copy that could go stale. A variable the
// caller already set is left alone.
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

	// Node compiles in its own roots, and this variable adds rather than
	// replaces, so it takes the root alone.
	add("NODE_EXTRA_CA_CERTS", root)

	// These replace a trust store rather than adding to it, so they need a whole
	// bundle: the system's own where the install updates it, grove's where not.
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
