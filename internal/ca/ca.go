// Package ca issues the local root and the wildcard leaf grove serves.
package ca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const (
	rootValidity = 10 * 365 * 24 * time.Hour

	// Platforms cap how long a server certificate may be valid. Renewal is free
	// with a local CA, so stay well inside every limit.
	leafValidity = 395 * 24 * time.Hour
)

type CA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// ErrNoAuthority means no CA has been generated yet. Only OpenOrCreate makes
// one, so every other caller reports this rather than quietly minting a root
// that nothing on the machine trusts.
var ErrNoAuthority = errors.New("ca: no certificate authority yet")

// Open loads an existing root.
func Open(dir string) (*CA, error) {
	certPEM, certErr := os.ReadFile(filepath.Join(dir, "root.crt"))
	keyPEM, keyErr := os.ReadFile(filepath.Join(dir, "root.key"))
	switch {
	case certErr == nil && keyErr == nil:
		return parse(certPEM, keyPEM)
	case errors.Is(certErr, fs.ErrNotExist) || errors.Is(keyErr, fs.ErrNotExist):
		return nil, fmt.Errorf("%w in %s", ErrNoAuthority, dir)
	default:
		return nil, errors.Join(certErr, keyErr)
	}
}

// OpenOrCreate loads the root, generating one on first use.
func OpenOrCreate(dir string) (*CA, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	root, err := Open(dir)
	if err == nil {
		return root, nil
	}
	if !errors.Is(err, ErrNoAuthority) {
		return nil, err
	}
	return generate(filepath.Join(dir, "root.crt"), filepath.Join(dir, "root.key"))
}

func parse(certPEM, keyPEM []byte) (*CA, error) {
	certBlock, _ := pem.Decode(certPEM)
	keyBlock, _ := pem.Decode(keyPEM)
	if certBlock == nil || keyBlock == nil {
		return nil, errors.New("ca: malformed PEM on disk")
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, err
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key}, nil
}

func generate(certPath, keyPath string) (*CA, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          sn,
		Subject:               pkix.Name{CommonName: "grove local CA", Organization: []string{"grove"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(rootValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	return &CA{cert: cert, key: key}, nil
}

func (c *CA) Certificate() *x509.Certificate { return c.cert }

func (c *CA) RootPEM() []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw})
}

// Leaf issues a server certificate. A wildcard covers one label and does not
// cover the domain itself, so callers pass both.
func (c *CA) Leaf(names ...string) (*tls.Certificate, error) {
	if len(names) == 0 {
		return nil, errors.New("ca: no names for leaf")
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	sn, err := serial()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		return nil, err
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, nil
}

func serial() (*big.Int, error) {
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("ca: serial number: %w", err)
	}
	return sn, nil
}
