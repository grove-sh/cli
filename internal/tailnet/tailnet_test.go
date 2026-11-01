package tailnet_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/tailnet"
)

const target = "127.0.0.4:443"

type fakeTailscaled struct {
	mu        sync.Mutex
	status    string
	serve     string
	etag      int
	denyWrite bool
	// What Taildrop was handed: the path names the device and the file.
	sentPath string
	sentBody string
	// A phone that never takes the file: the request is held until the client
	// gives up.
	neverAccept bool
}

func (f *fakeTailscaled) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.Header.Get("Sec-Tailscale") != "localapi" {
		http.Error(w, "missing Sec-Tailscale", http.StatusForbidden)
		return
	}
	switch r.URL.Path {
	case "/localapi/v0/status":
		io.WriteString(w, f.status)
	case "/localapi/v0/serve-config":
		if r.Method == http.MethodPost {
			if f.denyWrite {
				http.Error(w, "serve config denied", http.StatusForbidden)
				return
			}
			if r.Header.Get("If-Match") != f.tag() {
				http.Error(w, "etag mismatch", http.StatusPreconditionFailed)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.serve = string(body)
			f.etag++
			return
		}
		w.Header().Set("Etag", f.tag())
		io.WriteString(w, f.serve)
	default:
		if strings.HasPrefix(r.URL.Path, "/localapi/v0/file-put/") && r.Method == http.MethodPut {
			if f.neverAccept {
				f.mu.Unlock()
				<-r.Context().Done()
				f.mu.Lock()
				return
			}
			if f.denyWrite {
				http.Error(w, "file access denied", http.StatusForbidden)
				return
			}
			body, _ := io.ReadAll(r.Body)
			f.sentPath, f.sentBody = r.URL.EscapedPath(), string(body)
			return
		}
		http.NotFound(w, r)
	}
}

func (f *fakeTailscaled) tag() string { return strings.Repeat("e", f.etag+1) }

func startFake(t *testing.T, f *fakeTailscaled) *tailnet.Client {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "grove")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	socket := filepath.Join(dir, "ts.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: f}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return tailnet.New(socket)
}

func TestStatusReadsTailnetHostAndAddresses(t *testing.T) {
	c := startFake(t, &fakeTailscaled{status: `{
		"BackendState": "Running",
		"CurrentTailnet": {"Name": "dreamops.dev"},
		"Self": {"DNSName": "Capella.tail0af9ab.ts.net.", "TailscaleIPs": ["100.76.35.70", "fd7a:115c:a1e0::2039:2347"]}
	}`})
	s, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Running || s.Tailnet != "dreamops.dev" || s.Host != "capella" ||
		s.IPv4.String() != "100.76.35.70" || s.IPv6.String() != "fd7a:115c:a1e0::2039:2347" {
		t.Errorf("got %+v", s)
	}
}

func TestStatusWhileLoggedOut(t *testing.T) {
	c := startFake(t, &fakeTailscaled{status: `{"BackendState": "NeedsLogin"}`})
	s, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Running || s.IPv4.IsValid() {
		t.Errorf("got %+v, want not running with no address", s)
	}
}

func TestNoSocketIsUnavailable(t *testing.T) {
	_, err := tailnet.New("").Status(context.Background())
	if !errors.Is(err, tailnet.ErrUnavailable) {
		t.Errorf("got %v, want ErrUnavailable", err)
	}
	_, err = tailnet.New("/nonexistent/ts.sock").Status(context.Background())
	if !errors.Is(err, tailnet.ErrUnavailable) {
		t.Errorf("missing socket: got %v, want ErrUnavailable", err)
	}
}

func TestClaimRefusesAPortSomeoneElseServes(t *testing.T) {
	const t3 = `{"TCP":{"443":{"HTTPS":true}},"Web":{"capella.tail0af9ab.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3773"}}}}}`
	f := &fakeTailscaled{serve: t3}
	c := startFake(t, f)
	cfg, err := c.ServeConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Claim443(target); !tailnet.IsHeld(err) {
		t.Fatalf("got %v, want a held port", err)
	}
	if f.serve != t3 {
		t.Errorf("config changed to %s", f.serve)
	}
}

func TestClaimAndReleaseKeepEverythingElse(t *testing.T) {
	f := &fakeTailscaled{serve: `{"TCP":{"8443":{"HTTPS":true}},"Web":{"capella.tail0af9ab.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3773"}}}},"Future":{"kept":true}}`}
	c := startFake(t, f)
	ctx := context.Background()

	cfg, err := c.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := cfg.Claim443(target)
	if err != nil || !changed {
		t.Fatalf("claim: changed=%v err=%v", changed, err)
	}
	if err := c.SetServeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	var doc struct {
		TCP    map[string]map[string]any
		Web    map[string]any
		Future map[string]bool
	}
	if err := json.Unmarshal([]byte(f.serve), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.TCP["443"]["TCPForward"] != target || doc.TCP["8443"]["HTTPS"] != true ||
		doc.Web["capella.tail0af9ab.ts.net:8443"] == nil || !doc.Future["kept"] {
		t.Errorf("after claim: %s", f.serve)
	}

	cfg, _ = c.ServeConfig(ctx)
	if changed, err := cfg.Claim443(target); err != nil || changed {
		t.Errorf("second claim: changed=%v err=%v, want a no-op", changed, err)
	}
	if changed, err := cfg.Release443(target); err != nil || !changed {
		t.Fatalf("release: changed=%v err=%v", changed, err)
	}
	if err := c.SetServeConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.serve, `"443"`) || !strings.Contains(f.serve, `"8443"`) {
		t.Errorf("after release: %s", f.serve)
	}
}

func TestReleaseLeavesSomeoneElsesPort(t *testing.T) {
	c := startFake(t, &fakeTailscaled{serve: `{"TCP":{"443":{"HTTPS":true}}}`})
	cfg, _ := c.ServeConfig(context.Background())
	if changed, err := cfg.Release443(target); err != nil || changed {
		t.Errorf("changed=%v err=%v, want untouched", changed, err)
	}
}

func TestWriteAfterSomeoneElsesChangeIsRefused(t *testing.T) {
	f := &fakeTailscaled{serve: `{}`}
	c := startFake(t, f)
	ctx := context.Background()
	cfg, _ := c.ServeConfig(ctx)
	if _, err := cfg.Claim443(target); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.serve = `{"TCP":{"443":{"HTTPS":true}}}`
	f.etag++
	f.mu.Unlock()
	if err := c.SetServeConfig(ctx, cfg); !errors.Is(err, tailnet.ErrChanged) {
		t.Errorf("got %v, want ErrChanged", err)
	}
}

func TestEmptyServeConfig(t *testing.T) {
	for _, body := range []string{"", "null", "{}"} {
		c := startFake(t, &fakeTailscaled{serve: body})
		cfg, err := c.ServeConfig(context.Background())
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if h, err := cfg.Port443(target); err != nil || h != tailnet.Free {
			t.Errorf("%q: holder=%v err=%v, want free", body, h, err)
		}
	}
}

func TestServeWriteDeniedIsErrDenied(t *testing.T) {
	f := &fakeTailscaled{serve: `{}`, denyWrite: true}
	c := startFake(t, f)
	ctx := context.Background()
	cfg, err := c.ServeConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cfg.Claim443(target); err != nil {
		t.Fatal(err)
	}
	if err := c.SetServeConfig(ctx, cfg); !errors.Is(err, tailnet.ErrDenied) {
		t.Fatalf("got %v, want ErrDenied", err)
	}
	if f.serve != `{}` {
		t.Fatalf("denied write changed config to %s", f.serve)
	}
}

func TestShareRecordRoundTrips(t *testing.T) {
	dir := t.TempDir()
	if _, ok, err := tailnet.LoadShare(dir); ok || err != nil {
		t.Fatalf("fresh dir: ok=%v err=%v", ok, err)
	}
	want := tailnet.Share{Tailnet: "dreamops.dev", Host: "capella"}
	if err := tailnet.SaveShare(dir, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := tailnet.LoadShare(dir)
	if err != nil || !ok || got != want {
		t.Fatalf("got %+v ok=%v err=%v", got, ok, err)
	}
	if zone := tailnet.Zone("grov.site", got, ok); zone != "capella.grov.site" {
		t.Errorf("zone %q", zone)
	}
	if err := tailnet.RemoveShare(dir); err != nil {
		t.Fatal(err)
	}
	if err := tailnet.RemoveShare(dir); err != nil {
		t.Errorf("removing twice: %v", err)
	}
	if zone := tailnet.Zone("grov.site", tailnet.Share{}, false); zone != "grov.site" {
		t.Errorf("unshared zone %q", zone)
	}
}

func TestStatusListsPeers(t *testing.T) {
	c := startFake(t, &fakeTailscaled{status: `{
		"BackendState": "Running",
		"Self": {"DNSName": "regulus.tail0af9ab.ts.net."},
		"Peer": {
			"nodekey:a": {"ID": "nMGCzXpjyW11CNTRL", "DNSName": "pixel-9.tail0af9ab.ts.net.", "OS": "android", "Online": true, "TailscaleIPs": ["fd7a::1", "100.70.52.30"]},
			"nodekey:b": {"ID": "neHR65UvQX11CNTRL", "DNSName": "Capella.tail0af9ab.ts.net.", "OS": "linux", "TailscaleIPs": ["100.76.35.70"]}
		}
	}`})
	s, err := c.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	phone, ok := s.Peer("pixel-9")
	if !ok || phone.ID != "nMGCzXpjyW11CNTRL" || phone.OS != "android" || !phone.Online || phone.IPv4.String() != "100.70.52.30" {
		t.Errorf("pixel-9: %+v ok=%v", phone, ok)
	}
	if capella, ok := s.Peer("CAPELLA"); !ok || capella.Host != "capella" || capella.Online {
		t.Errorf("capella: %+v ok=%v", capella, ok)
	}
	if _, ok := s.Peer("regulus"); ok {
		t.Error("found this machine among its own peers")
	}
}

func TestSendFileGoesToTheDeviceByStableID(t *testing.T) {
	f := &fakeTailscaled{}
	c := startFake(t, f)
	phone := tailnet.Peer{ID: "nMGCzXpjyW11CNTRL", Host: "pixel-9"}
	if err := c.SendFile(context.Background(), phone, "grove-regulus-root.crt", []byte("PEM")); err != nil {
		t.Fatal(err)
	}
	if f.sentPath != "/localapi/v0/file-put/nMGCzXpjyW11CNTRL/grove-regulus-root.crt" || f.sentBody != "PEM" {
		t.Errorf("sent %q %q", f.sentPath, f.sentBody)
	}

	f.denyWrite = true
	if err := c.SendFile(context.Background(), phone, "x.crt", nil); !errors.Is(err, tailnet.ErrDenied) {
		t.Errorf("denied: got %v, want ErrDenied", err)
	}
}

// A device that does not take the file is a different failure from tailscaled
// not answering, and taking longer than the status timeout is no failure at all.
func TestSendFileToADeviceThatNeverAccepts(t *testing.T) {
	c := startFake(t, &fakeTailscaled{neverAccept: true})
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	start := time.Now()
	err := c.SendFile(ctx, tailnet.Peer{ID: "n1", Host: "pixel-9"}, "x.crt", []byte("PEM"))
	if !errors.Is(err, tailnet.ErrNotAccepted) {
		t.Fatalf("got %v, want ErrNotAccepted", err)
	}
	if waited := time.Since(start); waited < 5500*time.Millisecond {
		t.Errorf("gave up after %v, before the caller's deadline; the status timeout is cutting sends short", waited)
	}
}
