package main

import (
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/tailnet"
)

var tailnetIP = netip.MustParseAddr("100.76.35.70")

func healthyShare() shareProbe {
	return shareProbe{
		record:   tailnet.Share{Tailnet: "dreamops.dev", Host: "capella"},
		shared:   true,
		status:   tailnet.Status{Running: true, Tailnet: "dreamops.dev", Host: "capella", IPv4: tailnetIP},
		holder:   tailnet.Ours,
		resolver: []netip.Addr{tailnetIP},
		system:   []netip.Addr{tailnetIP},
	}
}

func TestSharingFindings(t *testing.T) {
	cases := []struct {
		name   string
		change func(*shareProbe)
		state  string
		detail string
		advice string
	}{
		{"healthy", func(*shareProbe) {}, ok, "*.capella.grov.site on dreamops.dev", ""},
		{"unreadable record", func(p *shareProbe) { p.recordErr = errors.New("not a share record") }, bad, "cannot be read", "share --off"},
		{"tailscaled down", func(p *shareProbe) { p.statusErr = tailnet.ErrUnavailable }, warn, "not answering", "tailscale status"},
		{"logged out", func(p *shareProbe) { p.status.Running = false }, warn, "not connected", "tailscale up"},
		{"another tailnet", func(p *shareProbe) { p.status.Tailnet = "client.example" }, warn, "paused on client.example", "share"},
		{"resolver silent", func(p *shareProbe) { p.resolver, p.resolverErr = nil, errors.New("refused") }, bad, "nothing answers DNS", "port floor"},
		// The public wildcard answering means the split DNS entry is missing.
		{"no split DNS", func(p *shareProbe) { p.system = []netip.Addr{netip.MustParseAddr("127.0.0.4")} }, warn, "does not resolve to 100.76.35.70", "restricted to capella.grov.site"},
		{"443 held", func(p *shareProbe) { p.holder = tailnet.Other }, warn, "served by something else", "t3code"},
		{"443 free", func(p *shareProbe) { p.holder = tailnet.Free }, warn, "not forwarded", "restart"},
		{"443 free, maybe denied", func(p *shareProbe) { p.holder = tailnet.Free }, warn, "not forwarded", tailnet.OperatorFix},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := healthyShare()
			c.change(&p)
			f := judgeSharing(p)
			if f.state != c.state || !strings.Contains(f.detail, c.detail) || !strings.Contains(f.advice, c.advice) {
				t.Errorf("got state=%s detail=%q advice=%q", f.state, f.detail, f.advice)
			}
		})
	}
}

func TestSharingSilentWhenNotShared(t *testing.T) {
	if _, worth := checkSharing(t.TempDir(), "127.0.0.4:443"); worth {
		t.Error("reported on a machine that never shared")
	}
}

type fakeServe struct {
	serve      string
	denied     bool
	unreadable bool
}

func (f *fakeServe) Status(context.Context) (tailnet.Status, error) {
	return tailnet.Status{Running: true, Tailnet: "dreamops.dev"}, nil
}

func (f *fakeServe) ServeConfig(context.Context) (*tailnet.ServeConfig, error) {
	if f.unreadable {
		return nil, tailnet.ErrUnavailable
	}
	return tailnet.ParseServeConfig([]byte(f.serve), "")
}

func (f *fakeServe) SetServeConfig(_ context.Context, cfg *tailnet.ServeConfig) error {
	if f.denied {
		return tailnet.ErrDenied
	}
	raw, err := cfg.MarshalJSON()
	f.serve = string(raw)
	return err
}

func TestReleaseSaysWhatBecameOfTheMapping(t *testing.T) {
	ours := `{"TCP":{"443":{"TCPForward":"` + defaultDaemonOptions().listen + `"}}}`
	cases := []struct {
		name   string
		f      *fakeServe
		tone   string
		said   string
		serves bool
	}{
		{"released", &fakeServe{serve: ours}, ok, "released", false},
		{"someone else's", &fakeServe{serve: `{"TCP":{"443":{"HTTPS":true}}}`}, ok, "was not grove's", true},
		{"denied", &fakeServe{serve: ours, denied: true}, bad, tailnet.OperatorFix, true},
		{"unreadable", &fakeServe{serve: ours, unreadable: true}, bad, "cannot tell", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			said, tone, _ := releaseNow(context.Background(), c.f)
			if tone != c.tone || !strings.Contains(said, c.said) {
				t.Errorf("got tone=%s said=%q", tone, said)
			}
			if got := strings.Contains(c.f.serve, `"443"`); got != c.serves {
				t.Errorf("443 still mapped = %v, want %v: %s", got, c.serves, c.f.serve)
			}
		})
	}
}

func TestUnshareRetryAfterDenialReleases(t *testing.T) {
	dir := t.TempDir()
	if err := tailnet.SaveShare(dir, tailnet.Share{Tailnet: "dreamops.dev", Host: "capella"}); err != nil {
		t.Fatal(err)
	}
	f := &fakeServe{serve: `{"TCP":{"443":{"TCPForward":"` + defaultDaemonOptions().listen + `"}}}`, denied: true}

	var out bytes.Buffer
	var exit *exitError
	if err := unshare(&out, f, dir); !errors.As(err, &exit) || exit.code != 1 {
		t.Fatalf("denied: got %v, want exit 1\n%s", err, out.String())
	}
	if _, ok, _ := tailnet.LoadShare(dir); ok {
		t.Error("record kept after --off")
	}
	if !strings.Contains(out.String(), "share --off again") {
		t.Errorf("denied output does not ask for the retry:\n%s", out.String())
	}

	f.denied = false
	out.Reset()
	if err := unshare(&out, f, dir); err != nil {
		t.Fatalf("retry: %v\n%s", err, out.String())
	}
	if strings.Contains(f.serve, `"443"`) || !strings.Contains(out.String(), "released") {
		t.Errorf("retry left the mapping: %s\n%s", f.serve, out.String())
	}

	out.Reset()
	if err := unshare(&out, f, dir); err != nil || !strings.Contains(out.String(), "not sharing") {
		t.Errorf("third run: err=%v\n%s", err, out.String())
	}
}

func TestUnshareWithAnUnreadableRecord(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "share.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeServe{serve: `{"TCP":{"443":{"TCPForward":"` + defaultDaemonOptions().listen + `"}}}`}
	var out bytes.Buffer
	if err := unshare(&out, f, dir); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "share.json")); !os.IsNotExist(err) {
		t.Errorf("damaged record left behind: %v", err)
	}
	if strings.Contains(f.serve, `"443"`) {
		t.Errorf("mapping left: %s", f.serve)
	}
}

// Serve mappings persist separately for each logged-in profile.
type profileServe struct {
	current   string
	profiles  map[string]*fakeServe
	statusErr error
	stopped   bool
}

func (f *profileServe) Status(context.Context) (tailnet.Status, error) {
	return tailnet.Status{Running: !f.stopped, Tailnet: f.current}, f.statusErr
}
func (f *profileServe) ServeConfig(ctx context.Context) (*tailnet.ServeConfig, error) {
	return f.profiles[f.current].ServeConfig(ctx)
}
func (f *profileServe) SetServeConfig(ctx context.Context, cfg *tailnet.ServeConfig) error {
	return f.profiles[f.current].SetServeConfig(ctx, cfg)
}

func TestUnshareRequiresRecordedTailnet(t *testing.T) {
	for _, state := range []string{"other tailnet", "unavailable", "stopped"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			record := tailnet.Share{Tailnet: "dreamops.dev", Host: "capella"}
			if err := tailnet.SaveShare(dir, record); err != nil {
				t.Fatal(err)
			}
			ours := `{"TCP":{"443":{"TCPForward":"` + defaultDaemonOptions().listen + `"}}}`
			f := &profileServe{current: "dreamops.dev", profiles: map[string]*fakeServe{
				"dreamops.dev": {serve: ours}, "client.example": {serve: `{}`},
			}}
			switch state {
			case "other tailnet":
				f.current = "client.example"
			case "unavailable":
				f.statusErr = tailnet.ErrUnavailable
			case "stopped":
				f.stopped = true
			}
			var out bytes.Buffer
			if err := unshare(&out, f, dir); err == nil || !strings.Contains(err.Error(), "connect to that tailnet") {
				t.Fatalf("got %v, want reconnect instruction", err)
			}
			if got, ok, err := tailnet.LoadShare(dir); err != nil || !ok || got != record {
				t.Fatalf("record lost: %v %v %v", got, ok, err)
			}
			if f.profiles["dreamops.dev"].serve != ours || f.profiles["client.example"].serve != `{}` {
				t.Fatal("changed Serve config before reconnecting")
			}
			f.current, f.statusErr, f.stopped = "dreamops.dev", nil, false
			if err := unshare(&out, f, dir); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(f.profiles["dreamops.dev"].serve, `"443"`) {
				t.Fatal("recorded tailnet still forwards after retry")
			}
			if _, ok, err := tailnet.LoadShare(dir); ok || err != nil {
				t.Fatalf("record left: %v %v", ok, err)
			}
		})
	}
}
