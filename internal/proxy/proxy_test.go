package proxy_test

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/proxy"
)

type harness struct {
	server  *proxy.Server
	addr    string
	caDir   string
	rootPEM []byte
}

func newHarness(t *testing.T, routes []proxy.Route) *harness {
	t.Helper()

	dir := t.TempDir()
	root, err := ca.OpenOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := root.Leaf("*.grov.site", "grov.site")
	if err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	s := proxy.New()
	s.SetRoutes(routes)
	go func() {
		if err := s.Serve(ln, cert); err != nil && err != http.ErrServerClosed {
			t.Log(err)
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.Shutdown(ctx)
	})

	return &harness{server: s, addr: ln.Addr().String(), caDir: dir, rootPEM: root.RootPEM()}
}

// client resolves every hostname to the proxy, the way the wildcard A record
// for grov.site does on a real machine.
func (h *harness) client(t *testing.T) *http.Client {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(h.rootPEM) {
		t.Fatal("root PEM did not parse")
	}
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, h.addr)
			},
		},
	}
}

func upstream(t *testing.T, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
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

func TestRoutesByHostname(t *testing.T) {
	one := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "app1")
	}))
	two := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "app2")
	}))

	h := newHarness(t, []proxy.Route{
		{Host: "app1.grov.site", Upstream: one},
		{Host: "app2-feat1.grov.site", Upstream: two},
	})
	c := h.client(t)

	if code, body := get(t, c, "https://app1.grov.site/"); code != 200 || body != "app1" {
		t.Errorf("app1: got %d %q", code, body)
	}
	if code, body := get(t, c, "https://app2-feat1.grov.site/"); code != 200 || body != "app2" {
		t.Errorf("app2-feat1: got %d %q", code, body)
	}
}

func TestPreservesHostAndSignalsTLS(t *testing.T) {
	up := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "%s|%s", r.Host, r.Header.Get("X-Forwarded-Proto"))
	}))
	h := newHarness(t, []proxy.Route{{Host: "app1.grov.site", Upstream: up}})

	_, body := get(t, h.client(t), "https://app1.grov.site/")
	if body != "app1.grov.site|https" {
		t.Errorf("upstream saw %q, want %q", body, "app1.grov.site|https")
	}
}

func TestUnknownHostGets503(t *testing.T) {
	h := newHarness(t, nil)

	code, body := get(t, h.client(t), "https://nope.grov.site/")
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if !strings.Contains(body, "nope.grov.site") || !strings.Contains(body, "grove exec") {
		t.Errorf("page does not name the host and the fix:\n%s", body)
	}
}

func TestDeadUpstreamGets503NotBadGateway(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dead := ln.Addr().String()
	ln.Close()

	h := newHarness(t, []proxy.Route{{Host: "app1.grov.site", Upstream: dead}})
	code, body := get(t, h.client(t), "https://app1.grov.site/")
	if code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", code)
	}
	if !strings.Contains(body, "Nothing is listening") {
		t.Errorf("page does not explain a dead lease:\n%s", body)
	}
}

func TestRoutesSwapWhileServing(t *testing.T) {
	one := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "first")
	}))
	two := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "second")
	}))

	h := newHarness(t, []proxy.Route{{Host: "app1.grov.site", Upstream: one}})
	c := h.client(t)
	if _, body := get(t, c, "https://app1.grov.site/"); body != "first" {
		t.Fatalf("before swap: %q", body)
	}

	h.server.SetRoutes([]proxy.Route{{Host: "app1.grov.site", Upstream: two}})
	if _, body := get(t, c, "https://app1.grov.site/"); body != "second" {
		t.Errorf("after swap: %q", body)
	}
}

func TestWebSocketUpgradePassesThrough(t *testing.T) {
	up := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			http.Error(w, "not an upgrade", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.Close()
		buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n\r\n")
		buf.Flush()
		io.Copy(conn, buf)
	}))

	h := newHarness(t, []proxy.Route{{Host: "app1.grov.site", Upstream: up}})

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(h.rootPEM)
	conn, err := tls.Dial("tcp", h.addr, &tls.Config{RootCAs: pool, ServerName: "app1.grov.site"})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	fmt.Fprint(conn, "GET /hmr HTTP/1.1\r\nHost: app1.grov.site\r\n"+
		"Connection: Upgrade\r\nUpgrade: websocket\r\n"+
		"Sec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n")

	br := bufio.NewReader(conn)
	req, _ := http.NewRequest(http.MethodGet, "https://app1.grov.site/hmr", nil)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("status = %d, want 101", resp.StatusCode)
	}

	fmt.Fprint(conn, "ping")
	echo := make([]byte, 4)
	if _, err := io.ReadFull(br, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != "ping" {
		t.Errorf("echo = %q, want %q", echo, "ping")
	}
}

func TestUnavailablePageEscapesHost(t *testing.T) {
	s := proxy.New()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = `x<script>alert(1)</script>`
	w := httptest.NewRecorder()

	s.ServeHTTP(w, r)

	if body := w.Body.String(); strings.Contains(body, "<script>") {
		t.Errorf("host reflected unescaped:\n%s", body)
	}
}

func TestRealCurlAcceptsTheChain(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl not installed")
	}

	up := upstream(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "hello from app1")
	}))
	h := newHarness(t, []proxy.Route{{Host: "app1.grov.site", Upstream: up}})
	_, port, err := net.SplitHostPort(h.addr)
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(curl, "-sS", "--fail",
		"--cacert", filepath.Join(h.caDir, "root.crt"),
		"--resolve", "app1.grov.site:"+port+":127.0.0.1",
		"https://app1.grov.site:"+port+"/",
	).CombinedOutput()
	if err != nil {
		t.Fatalf("curl: %v\n%s", err, out)
	}
	if string(out) != "hello from app1" {
		t.Errorf("curl body = %q", out)
	}

	version, err := exec.Command(curl, "-sS", "-o", "/dev/null", "-w", "%{http_version}",
		"--cacert", filepath.Join(h.caDir, "root.crt"),
		"--resolve", "app1.grov.site:"+port+":127.0.0.1",
		"https://app1.grov.site:"+port+"/",
	).Output()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("curl negotiated HTTP/%s", version)
}
