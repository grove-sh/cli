package daemon

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/tailnet"
)

type fakeTailscale struct {
	mu     sync.Mutex
	status tailnet.Status
	serve  string
	writes int
	denied bool
}

func (f *fakeTailscale) Status(context.Context) (tailnet.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, nil
}

func (f *fakeTailscale) ServeConfig(context.Context) (*tailnet.ServeConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return tailnet.ParseServeConfig([]byte(f.serve), "")
}

func (f *fakeTailscale) SetServeConfig(_ context.Context, cfg *tailnet.ServeConfig) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.denied {
		return tailnet.ErrDenied
	}
	raw, err := cfg.MarshalJSON()
	f.serve = string(raw)
	f.writes++
	return err
}

func (f *fakeTailscale) set(fn func(*fakeTailscale)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).Port
}

// Use loopback so these tests need no Tailscale interface.
func newSharing(t *testing.T, f *fakeTailscale) (*sharing, *[]string) {
	t.Helper()
	var logged []string
	share := tailnet.Share{Tailnet: "dreamops.dev", Host: "capella"}
	dir := t.TempDir()
	if err := tailnet.SaveShare(dir, share); err != nil {
		t.Fatal(err)
	}
	s := &sharing{
		api:      f,
		share:    share,
		stateDir: dir,
		zone:     "capella.grov.site",
		target:   "127.0.0.4:443",
		dnsPort:  freeUDPPort(t),
		logf: func(format string, args ...any) {
			logged = append(logged, fmt.Sprintf(format, args...))
		},
	}
	t.Cleanup(s.stopResolver)
	return s, &logged
}

func onTailnet(name string) tailnet.Status {
	return tailnet.Status{Running: true, Tailnet: name, Host: "capella", IPv4: netip.MustParseAddr("127.0.0.1")}
}

func lookup(t *testing.T, port int, name string) ([]netip.Addr, error) {
	t.Helper()
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return r.LookupNetIP(ctx, "ip4", name)
}

func TestSharingAnswersAndClaimsOnItsTailnet(t *testing.T) {
	f := &fakeTailscale{status: onTailnet("dreamops.dev"), serve: `{"TCP":{"8443":{"HTTPS":true}}}`}
	s, _ := newSharing(t, f)
	s.tick(context.Background())

	addrs, err := lookup(t, s.dnsPort, "app.capella.grov.site")
	if err != nil || len(addrs) != 1 || addrs[0].String() != "127.0.0.1" {
		t.Fatalf("lookup: %v %v", addrs, err)
	}
	if !strings.Contains(f.serve, `"443":{"TCPForward":"127.0.0.4:443"}`) || !strings.Contains(f.serve, `"8443"`) {
		t.Errorf("serve config %s", f.serve)
	}

	s.tick(context.Background())
	if f.writes != 1 {
		t.Errorf("%d writes after two ticks, want 1: an unchanged claim writes nothing", f.writes)
	}
}

func TestSharingStandsDownOnAnotherTailnet(t *testing.T) {
	f := &fakeTailscale{status: onTailnet("dreamops.dev"), serve: `{}`}
	s, logged := newSharing(t, f)
	s.tick(context.Background())

	f.set(func(f *fakeTailscale) { f.status = onTailnet("client.example") })
	s.tick(context.Background())

	if _, err := lookup(t, s.dnsPort, "app.capella.grov.site"); err == nil {
		t.Error("resolver still answering on another tailnet")
	}
	if strings.Contains(f.serve, `"443"`) {
		t.Errorf("grove's mapping left in place: %s", f.serve)
	}
	if last := (*logged)[len(*logged)-1]; !strings.Contains(last, "client.example") {
		t.Errorf("last log %q does not name the tailnet it stood down on", last)
	}

	f.set(func(f *fakeTailscale) { f.status = onTailnet("dreamops.dev") })
	s.tick(context.Background())
	if _, err := lookup(t, s.dnsPort, "app.capella.grov.site"); err != nil {
		t.Errorf("not answering after switching back: %v", err)
	}
}

func TestSharingNeverTakesAPortSomeoneElseServes(t *testing.T) {
	const t3 = `{"TCP":{"443":{"HTTPS":true}}}`
	f := &fakeTailscale{status: onTailnet("dreamops.dev"), serve: t3}
	s, logged := newSharing(t, f)
	s.tick(context.Background())
	s.tick(context.Background())

	if f.serve != t3 || f.writes != 0 {
		t.Errorf("wrote %d times, config now %s", f.writes, f.serve)
	}
	held := 0
	for _, line := range *logged {
		if strings.Contains(line, "served by something else") {
			held++
		}
	}
	if held != 1 {
		t.Errorf("held port reported %d times over two ticks, want once: %q", held, *logged)
	}
	if _, err := lookup(t, s.dnsPort, "app.capella.grov.site"); err != nil {
		t.Errorf("resolver down while 443 is held: %v", err)
	}
}

func TestSharingPausesWhileDisconnected(t *testing.T) {
	f := &fakeTailscale{status: onTailnet("dreamops.dev"), serve: `{}`}
	s, _ := newSharing(t, f)
	s.tick(context.Background())

	f.set(func(f *fakeTailscale) { f.status = tailnet.Status{} })
	s.tick(context.Background())
	if _, err := lookup(t, s.dnsPort, "app.capella.grov.site"); err == nil {
		t.Error("resolver still answering while tailscale is down")
	}
}

func TestSharingNamesTheOperatorFixWhenDenied(t *testing.T) {
	f := &fakeTailscale{status: onTailnet("dreamops.dev"), serve: `{}`, denied: true}
	s, logged := newSharing(t, f)
	s.tick(context.Background())

	last := (*logged)[len(*logged)-1]
	if !strings.Contains(last, tailnet.OperatorFix) || !strings.Contains(last, "next check") {
		t.Errorf("logged %q", last)
	}
	if _, err := lookup(t, s.dnsPort, "app.capella.grov.site"); err != nil {
		t.Errorf("resolver down while Serve is denied: %v", err)
	}
}

// Removing the record must prevent the daemon from reclaiming the released port.
func TestSharingStopsWhenTheRecordGoes(t *testing.T) {
	f := &fakeTailscale{status: onTailnet("dreamops.dev"), serve: `{}`}
	s, logged := newSharing(t, f)
	s.tick(context.Background())
	if !strings.Contains(f.serve, `"443"`) {
		t.Fatalf("not claimed to begin with: %s", f.serve)
	}

	if err := tailnet.RemoveShare(s.stateDir); err != nil {
		t.Fatal(err)
	}
	s.tick(context.Background())
	s.tick(context.Background())

	if strings.Contains(f.serve, `"443"`) {
		t.Errorf("claimed again after the record went: %s", f.serve)
	}
	if _, err := lookup(t, s.dnsPort, "app.capella.grov.site"); err == nil {
		t.Error("resolver still answering after the record went")
	}
	if last := (*logged)[len(*logged)-1]; !strings.Contains(last, "restart") {
		t.Errorf("last log %q does not say to restart", last)
	}
}

// Resolvers fall back to TCP, so the daemon binds both and takes both down.
func TestSharingAnswersOverTCPToo(t *testing.T) {
	f := &fakeTailscale{status: onTailnet("dreamops.dev"), serve: `{}`}
	s, _ := newSharing(t, f)
	s.tick(context.Background())

	tcpLookup := func() error {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(s.dnsPort)))
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := r.LookupNetIP(ctx, "ip4", "app.capella.grov.site")
		return err
	}
	if err := tcpLookup(); err != nil {
		t.Fatalf("over TCP: %v", err)
	}
	f.set(func(f *fakeTailscale) { f.status = onTailnet("client.example") })
	s.tick(context.Background())
	if err := tcpLookup(); err == nil {
		t.Error("still answering over TCP on another tailnet")
	}
}
