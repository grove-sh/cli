package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/grove-sh/cli/internal/tailnet"
)

// Bound the delay before a tailnet switch closes the resolver on the old address.
const shareInterval = 10 * time.Second

type Tailscale interface {
	Status(context.Context) (tailnet.Status, error)
	ServeConfig(context.Context) (*tailnet.ServeConfig, error)
	SetServeConfig(context.Context, *tailnet.ServeConfig) error
}

type sharing struct {
	api    Tailscale
	share  tailnet.Share
	zone   string
	target string
	// Re-read each tick so --off stops reclamation without waiting for a restart.
	stateDir string
	logf     func(string, ...any)
	dnsPort  int

	mu       sync.Mutex
	resolver net.PacketConn
	bound    netip.Addr
	// Suppress repeated log entries while the sharing state is unchanged.
	said string
}

func (s *sharing) run(ctx context.Context) {
	ticker := time.NewTicker(shareInterval)
	defer ticker.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			s.stopResolver()
			return
		case <-ticker.C:
		}
	}
}

func (s *sharing) tick(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if record, ok, err := tailnet.LoadShare(s.stateDir); err != nil || !ok || record != s.share {
		s.stopResolver()
		s.release(ctx)
		s.say("sharing stopped: the share record changed since this daemon started; restart it so hostnames follow")
		return
	}

	status, err := s.api.Status(ctx)
	switch {
	case err != nil:
		s.stopResolver()
		s.say("sharing paused: %v", err)
		return
	case !status.Running:
		s.stopResolver()
		s.say("sharing paused: tailscale is not connected")
		return
	case status.Tailnet != s.share.Tailnet:
		s.stopResolver()
		s.release(ctx)
		s.say("sharing paused: on tailnet %s, and this machine shares on %s", status.Tailnet, s.share.Tailnet)
		return
	case !status.IPv4.IsValid():
		s.stopResolver()
		s.say("sharing paused: tailscale reports no IPv4 address yet")
		return
	}

	if err := s.bindResolver(status); err != nil {
		s.say("sharing: cannot answer DNS on %s: %v", net.JoinHostPort(status.IPv4.String(), strconv.Itoa(s.dnsPort)), err)
		return
	}
	if err := s.claim(ctx); err != nil {
		s.say("sharing: %v", err)
		return
	}
	s.say("sharing *.%s on %s", s.zone, status.IPv4)
}

func (s *sharing) bindResolver(status tailnet.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolver != nil && s.bound == status.IPv4 {
		return nil
	}
	if s.resolver != nil {
		s.resolver.Close()
		s.resolver = nil
	}
	conn, err := net.ListenPacket("udp", net.JoinHostPort(status.IPv4.String(), strconv.Itoa(s.dnsPort)))
	if err != nil {
		return err
	}
	s.resolver, s.bound = conn, status.IPv4
	go tailnet.Resolver{Zone: s.zone, IPv4: status.IPv4, IPv6: status.IPv6}.Serve(conn)
	return nil
}

func (s *sharing) stopResolver() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resolver != nil {
		s.resolver.Close()
		s.resolver = nil
		s.bound = netip.Addr{}
	}
}

func (s *sharing) claim(ctx context.Context) error {
	cfg, err := s.api.ServeConfig(ctx)
	if err != nil {
		return err
	}
	changed, err := cfg.Claim443(s.target)
	if tailnet.IsHeld(err) {
		return errors.New("tailnet port 443 is served by something else, so other devices cannot reach grove; " +
			"move it to another port (t3code: Settings, Tailscale Serve port) and grove takes 443 on its next check")
	}
	if err != nil || !changed {
		return err
	}
	err = s.api.SetServeConfig(ctx, cfg)
	if errors.Is(err, tailnet.ErrDenied) {
		return errors.New("this user may not change Tailscale Serve, so tailnet port 443 stays unclaimed; run " +
			tailnet.OperatorFix + " and grove claims it on its next check")
	}
	return err
}

// Remove any visible forward to grove rather than exposing dev servers on another tailnet.
func (s *sharing) release(ctx context.Context) {
	cfg, err := s.api.ServeConfig(ctx)
	if err != nil {
		return
	}
	if changed, err := cfg.Release443(s.target); err == nil && changed {
		s.api.SetServeConfig(ctx, cfg)
	}
}

func (s *sharing) say(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := fmt.Sprintf(format, args...)
	if msg == s.said {
		return
	}
	s.said = msg
	s.logf("%s", msg)
}
