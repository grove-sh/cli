package tailnet_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/tailnet"
)

func resolverAt(t *testing.T, r tailnet.Resolver) *net.Resolver {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go r.Serve(conn)
	return &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", conn.LocalAddr().String())
		},
	}
}

func TestResolverAnswersEveryNameInTheZone(t *testing.T) {
	r := resolverAt(t, tailnet.Resolver{
		Zone: "capella.grov.site",
		IPv4: netip.MustParseAddr("100.76.35.70"),
		IPv6: netip.MustParseAddr("fd7a:115c:a1e0::2039:2347"),
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, name := range []string{"app.capella.grov.site", "APP-feat.Capella.grov.site", "capella.grov.site"} {
		addrs, err := r.LookupNetIP(ctx, "ip", name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := map[string]bool{}
		for _, a := range addrs {
			got[a.String()] = true
		}
		if !got["100.76.35.70"] || !got["fd7a:115c:a1e0::2039:2347"] {
			t.Errorf("%s resolved to %v, want both tailnet addresses", name, addrs)
		}
	}
}

func TestResolverRefusesNamesOutsideTheZone(t *testing.T) {
	r := resolverAt(t, tailnet.Resolver{Zone: "capella.grov.site", IPv4: netip.MustParseAddr("100.76.35.70")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, name := range []string{"example.com", "regulus.grov.site", "app.notcapella.grov.site"} {
		_, err := r.LookupNetIP(ctx, "ip4", name)
		var dnsErr *net.DNSError
		if !errors.As(err, &dnsErr) || !dnsErr.IsNotFound {
			t.Errorf("%s: got %v, want not found", name, err)
		}
	}
}

func TestResolverAnswersEmptyForAMissingFamily(t *testing.T) {
	r := resolverAt(t, tailnet.Resolver{Zone: "capella.grov.site", IPv4: netip.MustParseAddr("100.76.35.70")})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addrs, err := r.LookupNetIP(ctx, "ip", "app.capella.grov.site")
	if err != nil {
		t.Fatal(err)
	}
	if len(addrs) != 1 || addrs[0].String() != "100.76.35.70" {
		t.Errorf("got %v, want only the IPv4 address", addrs)
	}
}

func TestResolverIgnoresGarbage(t *testing.T) {
	r := tailnet.Resolver{Zone: "capella.grov.site", IPv4: netip.MustParseAddr("100.76.35.70")}
	for _, packet := range [][]byte{nil, {1, 2, 3}, make([]byte, 11)} {
		if reply := r.Answer(packet); reply != nil {
			t.Errorf("answered %v", packet)
		}
	}
	truncated := []byte{0, 1, 1, 0, 0, 1, 0, 0, 0, 0, 0, 0, 7, 'c', 'a', 'p'}
	reply := r.Answer(truncated)
	if reply == nil || reply[3]&0x0f != 1 {
		t.Errorf("truncated name: got %v, want FORMERR", reply)
	}
}

func TestResolverAnswersOverTCP(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go tailnet.Resolver{Zone: "capella.grov.site", IPv4: netip.MustParseAddr("100.76.35.70")}.ServeTCP(ln)
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", ln.Addr().String())
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	addrs, err := r.LookupNetIP(ctx, "ip4", "app.capella.grov.site")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "100.76.35.70" {
		t.Fatalf("got %v %v", addrs, err)
	}
	if _, err := r.LookupNetIP(ctx, "ip4", "example.com"); err == nil {
		t.Error("answered a name outside the zone over TCP")
	}
}
