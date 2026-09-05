package daemon_test

import (
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
)

// Port 80 exists only to send the browser to https, and the hostname it was
// asked for has to survive the trip, since that hostname is the whole point of
// grove.
func TestPlainHTTPIsSentToHTTPS(t *testing.T) {
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
	plain, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	served := make(chan error, 1)
	go func() { served <- server.Serve(control, https, plain) }()
	t.Cleanup(func() {
		server.Shutdown()
		<-served
	})

	request, err := http.NewRequest("GET", "http://"+plain.Addr().String()+"/deep/link?q=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = "app1." + domain

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if want := "https://app1." + domain + "/deep/link?q=1"; resp.Header.Get("Location") != want {
		t.Errorf("Location = %q, want %q", resp.Header.Get("Location"), want)
	}
	// A permanent redirect is cached hard by browsers, on the one kind of
	// machine where someone will later want plain http on that hostname.
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusFound)
	}
}

// Nothing hands grove port 80 on most machines, and that is not a failure.
func TestServeRunsWithoutAPlainHTTPListener(t *testing.T) {
	h := start(t)

	client, err := daemon.Dial(h.socket)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if _, err := client.Status(); err != nil {
		t.Errorf("a daemon with no http listener is not serving: %v", err)
	}
}
