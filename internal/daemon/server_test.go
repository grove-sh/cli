package daemon_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
)

const domain = "grov.site"

type harness struct {
	socket string
	client *http.Client
}

func start(t *testing.T) *harness {
	t.Helper()

	dir := t.TempDir()
	if _, err := ca.OpenOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	server, err := daemon.New(daemon.Config{Domain: domain, CADir: dir})
	if err != nil {
		t.Fatal(err)
	}
	control, err := daemon.Listen(filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	https, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	served := make(chan error, 1)
	go func() { served <- server.Serve(control, https) }()
	t.Cleanup(func() {
		server.Shutdown()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(server.RootPEM()) {
		t.Fatal("root PEM did not parse")
	}
	proxyAddr := https.Addr().String()

	return &harness{
		socket: control.Addr().String(),
		client: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{RootCAs: pool},
				DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, network, proxyAddr)
				},
			},
		},
	}
}

func (h *harness) dial(t *testing.T) *daemon.Client {
	t.Helper()
	c, err := daemon.Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func (h *harness) status(t *testing.T, host string) (int, string) {
	t.Helper()
	resp, err := h.client.Get("https://" + host + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

// serveOn stands in for a dev server that binds the port grove leased.
func serveOn(t *testing.T, port int, body string) {
	t.Helper()
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Fatalf("binding the leased port %d: %v", port, err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	})}
	go server.Serve(ln)
	t.Cleanup(func() { server.Close() })
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLeasedPortIsReachableByHostname(t *testing.T) {
	h := start(t)

	grant, err := h.dial(t).Acquire("app1-feat1", "", "/src/feat1")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Host != "app1-feat1."+domain {
		t.Errorf("host = %q", grant.Host)
	}
	if grant.URL != "https://"+grant.Host {
		t.Errorf("url = %q", grant.URL)
	}

	serveOn(t, grant.Port, "hello from feat1")

	code, body := h.status(t, grant.Host)
	if code != 200 || body != "hello from feat1" {
		t.Errorf("got %d %q", code, body)
	}
}

func TestNamedServiceGetsASuffixedHostname(t *testing.T) {
	h := start(t)

	grant, err := h.dial(t).Acquire("app1", "api", "/src/app1")
	if err != nil {
		t.Fatal(err)
	}

	if grant.Host != "app1-api."+domain {
		t.Errorf("host = %q, want a suffix rather than a subdomain", grant.Host)
	}
}

// The lease is the connection. Closing it releases the port and the route, with
// no reaper and no cleanup pass.
func TestClosingTheClientEndsTheLease(t *testing.T) {
	h := start(t)
	client, err := daemon.Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := client.Acquire("app1", "", "/src/app1")
	if err != nil {
		t.Fatal(err)
	}
	serveOn(t, grant.Port, "up")
	if code, _ := h.status(t, grant.Host); code != 200 {
		t.Fatalf("status = %d before close, want 200", code)
	}

	client.Close()

	eventually(t, "the route to disappear", func() bool {
		code, _ := h.status(t, grant.Host)
		return code == http.StatusServiceUnavailable
	})
	if entries, err := h.dial(t).List(); err != nil || len(entries) != 0 {
		t.Errorf("List = %v (err %v), want empty", entries, err)
	}
}

func TestListReportsLiveLeases(t *testing.T) {
	h := start(t)
	if _, err := h.dial(t).Acquire("app1", "", "/src/app1"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dial(t).Acquire("app2", "web", "/src/app2"); err != nil {
		t.Fatal(err)
	}

	entries, err := h.dial(t).List()
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	if entries[0].Host != "app1."+domain || entries[1].Host != "app2-web."+domain {
		t.Errorf("hosts = %q, %q", entries[0].Host, entries[1].Host)
	}
	if entries[0].Worktree != "/src/app1" {
		t.Errorf("worktree = %q", entries[0].Worktree)
	}
}

func TestCollisionReachesTheClient(t *testing.T) {
	h := start(t)
	if _, err := h.dial(t).Acquire("app1-feat1", "", "/src/feat.1"); err != nil {
		t.Fatal(err)
	}

	_, err := h.dial(t).Acquire("app1-feat1", "", "/src/feat-1")

	if err == nil {
		t.Fatal("second worktree acquired the same context")
	}
	for _, want := range []string{"/src/feat.1", "/src/feat-1", "GROVE_CONTEXT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestUnknownHostGetsThe503Page(t *testing.T) {
	h := start(t)

	code, body := h.status(t, "nothing-here."+domain)

	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if !strings.Contains(body, "grove exec") {
		t.Errorf("page does not name the fix:\n%s", body)
	}
}

func TestListenReplacesAStaleSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "control.sock")
	if err := os.WriteFile(socket, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	ln, err := daemon.Listen(socket)
	if err != nil {
		t.Fatalf("a leftover socket file blocked startup: %v", err)
	}
	ln.Close()
}

func TestListenRefusesASecondDaemon(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "control.sock")
	first, err := daemon.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()

	_, err = daemon.Listen(socket)

	if err == nil {
		t.Fatal("a second daemon bound the same socket")
	}
	if !strings.Contains(err.Error(), "already listening") {
		t.Errorf("error does not explain the conflict: %v", err)
	}
}
