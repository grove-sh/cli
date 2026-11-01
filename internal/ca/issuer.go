package ca

import (
	"crypto/tls"
	"fmt"
	"strings"
	"sync"
)

// Issuer hands out a leaf per hostname instead of one wildcard, so a context can
// sit at any depth under the domain. A wildcard certificate matches exactly one
// label (RFC 6125), which is what confined grove to <context>.<domain>; DNS has
// no such limit and already resolves deeper names.
type Issuer struct {
	root   *CA
	domain string
	// What hostnames sit one label under: the domain, or <host>.<domain> on a
	// machine that shares. Its own wildcard covers that machine's unrouted
	// names, which the domain's cannot reach two labels deep.
	zone   string
	routed func(name string) bool

	mu     sync.Mutex
	leaves map[string]*tls.Certificate
	minted int
}

// Only names routed asks about get a leaf of their own. Once the daemon answers
// on the tailnet, minting for any name a client offers would let a peer fill
// memory with one signed leaf per invented hostname.
// An empty zone means the domain itself.
func NewIssuer(root *CA, domain, zone string, routed func(name string) bool) *Issuer {
	domain = strings.ToLower(domain)
	zone = strings.ToLower(zone)
	if zone == "" {
		zone = domain
	}
	return &Issuer{
		root:   root,
		domain: domain,
		zone:   zone,
		routed: routed,
		leaves: map[string]*tls.Certificate{},
	}
}

// GetCertificate satisfies tls.Config. An empty ServerName means a client that
// dialled the address rather than a name, which still gets the wildcard.
func (i *Issuer) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	name := strings.ToLower(strings.TrimSuffix(hello.ServerName, "."))
	wildcard := "*." + i.domain
	if name == "" {
		name = wildcard
	}
	// Minting for whatever is asked would make a root the machine trusts into a
	// signing oracle for names grove has no claim to.
	if name != i.domain && !strings.HasSuffix(name, "."+i.domain) && name != wildcard {
		return nil, fmt.Errorf("ca: %q is not under %s", name, i.domain)
	}
	// An unrouted name still gets a wildcard, so it reaches the proxy's 503
	// page, which says what to run, rather than a handshake failure. On a
	// machine that shares, every name is two labels deep and only the zone's
	// wildcard covers it. Two wildcards at most, so a peer offering invented
	// names still costs nothing beyond them.
	zoneWildcard := "*." + i.zone
	if name != wildcard && name != zoneWildcard && !i.routed(name) {
		sub, underZone := strings.CutSuffix(name, "."+i.zone)
		if underZone && i.zone != i.domain && !strings.Contains(sub, ".") {
			name = zoneWildcard
		} else {
			name = wildcard
		}
	}

	i.mu.Lock()
	defer i.mu.Unlock()
	if leaf, ok := i.leaves[name]; ok {
		return leaf, nil
	}
	names := []string{name}
	if name == wildcard {
		names = append(names, i.domain)
	}
	leaf, err := i.root.Leaf(names...)
	if err != nil {
		return nil, err
	}
	i.leaves[name] = leaf
	i.minted++
	return leaf, nil
}

// Minted reports how many leaves have been signed, so tests can see the cache
// and the routed check at work.
func (i *Issuer) Minted() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.minted
}
