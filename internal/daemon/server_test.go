package daemon_test

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
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

// socketDir is a short directory, because t.TempDir on macOS returns a
// /var/folders path long enough on its own to blow the unix socket path limit.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "grove")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
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
	control, err := daemon.Listen(filepath.Join(socketDir(t), "control.sock"))
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

// routed asks for one entry that gets a hostname, which is the common case.
func routed(name, label string) []daemon.Entry {
	return []daemon.Entry{{Name: name, Label: label, Routed: true}}
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

	grants, err := h.dial(t).Acquire("app1-feat1", "/src/feat1", routed("web", ""))
	if err != nil {
		t.Fatal(err)
	}
	grant := grants["web"]
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

	grants, err := h.dial(t).Acquire("app1", "/src/app1", routed("api", "api"))
	if err != nil {
		t.Fatal(err)
	}

	if grant := grants["api"]; grant.Host != "app1-api."+domain {
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
	grants, err := client.Acquire("app1", "/src/app1", routed("web", ""))
	if err != nil {
		t.Fatal(err)
	}
	grant := grants["web"]
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
	if _, err := h.dial(t).Acquire("app1", "/src/app1", routed("web", "")); err != nil {
		t.Fatal(err)
	}
	if _, err := h.dial(t).Acquire("app2", "/src/app2", routed("web", "web")); err != nil {
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
	if _, err := h.dial(t).Acquire("app1-feat1", "/src/feat.1", routed("web", "")); err != nil {
		t.Fatal(err)
	}

	_, err := h.dial(t).Acquire("app1-feat1", "/src/feat-1", routed("web", ""))

	if err == nil {
		t.Fatal("second worktree acquired the same context")
	}
	for _, want := range []string{"/src/feat.1", "/src/feat-1", "GROVE_CONTEXT"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// supabase start returns as soon as its containers are up, so the ports it was
// given have to survive the command that asked for them.
func TestDetachedLeaseOutlivesTheClient(t *testing.T) {
	h := start(t)
	client, err := daemon.Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	grants, err := client.Acquire("app1", "/src/app1", []daemon.Entry{
		{Name: "studio", Label: "studio", Routed: true, Detached: true},
		{Name: "db", Detached: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	studio := grants["studio"]
	serveOn(t, studio.Port, "studio")

	client.Close()

	// Give the daemon the chance to drop it that an attached lease would take.
	eventually(t, "the lease to settle", func() bool {
		live, err := h.dial(t).List()
		return err == nil && len(live) == 2
	})
	if code, _ := h.status(t, studio.Host); code != 200 {
		t.Errorf("status = %d after the client closed, want 200", code)
	}
	if grants["db"].Host != "" {
		t.Errorf("a bare port got the hostname %q", grants["db"].Host)
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
	socket := filepath.Join(socketDir(t), "control.sock")
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
	socket := filepath.Join(socketDir(t), "control.sock")
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

func TestServerRejectsAnUnknownProtocolVersion(t *testing.T) {
	h := start(t)
	conn, err := net.Dial("unix", h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if err := json.NewEncoder(conn).Encode(map[string]any{"version": 99, "op": "list"}); err != nil {
		t.Fatal(err)
	}
	var resp daemon.Response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(resp.Error, "protocol") {
		t.Errorf("error = %q, want it to name the mismatch", resp.Error)
	}
	if resp.Version != daemon.Version {
		t.Errorf("response version = %d, want %d", resp.Version, daemon.Version)
	}
}

// A daemon built before versions existed answers without one. That is the case
// worth naming, since grove gets rebuilt far more often than it gets restarted.
func TestClientNamesAnOlderDaemon(t *testing.T) {
	socket := filepath.Join(socketDir(t), "old.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		json.NewDecoder(conn).Decode(new(map[string]any))
		json.NewEncoder(conn).Encode(map[string]any{"leases": []any{}})
	}()

	client, err := daemon.Dial(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	_, err = client.List()

	var mismatch *daemon.VersionError
	if !errors.As(err, &mismatch) {
		t.Fatalf("err = %v, want VersionError", err)
	}
	if !strings.Contains(err.Error(), "start it again") {
		t.Errorf("error does not say what to do: %v", err)
	}
}

func TestReleaseEndsDetachedLeasesOnly(t *testing.T) {
	h := start(t)
	client, err := daemon.Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Acquire("app1", "/src/app1", []daemon.Entry{
		{Name: "studio", Label: "studio", Routed: true, Detached: true},
		{Name: "db", Detached: true},
		{Name: "web", Label: "", Routed: true},
	}); err != nil {
		t.Fatal(err)
	}

	released, err := h.dial(t).Release("app1", "/src/app1", nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(released) != 2 {
		t.Errorf("released %v, want the two detached entries", released)
	}
	live, err := h.dial(t).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 || live[0].Service != "web" {
		t.Errorf("live = %v, want the attached lease to survive", live)
	}
}

func TestReleaseByName(t *testing.T) {
	h := start(t)
	client := h.dial(t)
	if _, err := client.Acquire("app1", "/src/app1", []daemon.Entry{
		{Name: "studio", Detached: true},
		{Name: "db", Detached: true},
	}); err != nil {
		t.Fatal(err)
	}

	released, err := h.dial(t).Release("app1", "/src/app1", []string{"db"})
	if err != nil {
		t.Fatal(err)
	}

	if len(released) != 1 || released[0] != "db" {
		t.Errorf("released %v, want just db", released)
	}
}

func TestStatusDescribesTheDaemon(t *testing.T) {
	h := start(t)

	status, err := h.dial(t).Status()
	if err != nil {
		t.Fatal(err)
	}

	if status.PID != os.Getpid() {
		t.Errorf("pid = %d, want this process %d", status.PID, os.Getpid())
	}
	if !strings.HasPrefix(status.Listen, "127.0.0.1:") {
		t.Errorf("listen = %q", status.Listen)
	}
	if status.Version != daemon.Version {
		t.Errorf("version = %d", status.Version)
	}
}

func TestStopShutsTheDaemonDown(t *testing.T) {
	h := start(t)

	if err := h.dial(t).Stop(); err != nil {
		t.Fatal(err)
	}

	eventually(t, "the socket to stop answering", func() bool {
		client, err := daemon.Dial(h.socket)
		if err != nil {
			return true
		}
		client.Close()
		return false
	})
}

// The pid answers "what is holding this", so a detached lease reports none:
// whatever asserted it has exited, and the containers it stood for are not
// grove's to name.
func TestDetachedLeasesReportNoHolder(t *testing.T) {
	h := start(t)
	client := h.dial(t)
	if _, err := client.Acquire("app1", "/src/app1", []daemon.Entry{
		{Name: "studio", Detached: true},
		{Name: "web", Routed: true},
	}); err != nil {
		t.Fatal(err)
	}

	live, err := h.dial(t).List()
	if err != nil {
		t.Fatal(err)
	}

	for _, held := range live {
		switch {
		case held.Detached && held.PID != 0:
			t.Errorf("detached %q reports pid %d", held.Service, held.PID)
		case !held.Detached && held.PID == 0:
			t.Errorf("attached %q reports no pid, though a command holds it", held.Service)
		}
	}
}
