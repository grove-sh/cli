package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"slices"
	"time"

	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/tailnet"
)

type shareProbe struct {
	record    tailnet.Share
	shared    bool
	recordErr error

	status    tailnet.Status
	statusErr error

	holder    tailnet.Holder
	holderErr error
	// Queried directly to distinguish a missing resolver from missing split DNS.
	resolver    []netip.Addr
	resolverErr error
	system      []netip.Addr
}

func checkSharing(stateDir, listen string) (finding, bool) {
	p := shareProbe{}
	p.record, p.shared, p.recordErr = tailnet.LoadShare(stateDir)
	if !p.shared && p.recordErr == nil {
		return finding{}, false
	}
	if p.recordErr == nil {
		gatherShare(&p, listen)
	}
	return judgeSharing(p), true
}

func gatherShare(p *shareProbe, listen string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := tailnet.New(platform.TailscaleSocket())
	p.status, p.statusErr = client.Status(ctx)
	if p.statusErr != nil || !p.status.Running || p.status.Tailnet != p.record.Tailnet || !p.status.IPv4.IsValid() {
		return
	}
	if cfg, err := client.ServeConfig(ctx); err != nil {
		p.holderErr = err
	} else {
		p.holder, p.holderErr = cfg.Port443(listen)
	}

	name := "grove-doctor." + tailnet.Zone(defaultDomain, p.record, true)
	direct := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "udp", net.JoinHostPort(p.status.IPv4.String(), "53"))
	}}
	p.resolver, p.resolverErr = direct.LookupNetIP(ctx, "ip4", name)
	p.system, _ = net.DefaultResolver.LookupNetIP(ctx, "ip4", name)
}

func judgeSharing(p shareProbe) finding {
	f := finding{name: "Sharing"}
	if p.recordErr != nil {
		f.state = bad
		f.detail = "the share record cannot be read"
		f.advice = p.recordErr.Error() + ". Run " + invocation() + " share to write it again, or " + invocation() + " share --off."
		return f
	}
	zone := tailnet.Zone(defaultDomain, p.record, true)
	switch {
	case errors.Is(p.statusErr, tailnet.ErrUnavailable):
		f.state = warn
		f.detail = "paused, since tailscaled is not answering"
		f.advice = "Hostnames stay *." + zone + " and resolve to this machine only. Check tailscale status."
		return f
	case p.statusErr != nil:
		f.state = warn
		f.detail = "paused: " + p.statusErr.Error()
		return f
	case !p.status.Running:
		f.state = warn
		f.detail = "paused, since tailscale is not connected"
		f.advice = "Run tailscale up and grove picks sharing back up on its own."
		return f
	case p.status.Tailnet != p.record.Tailnet:
		f.state = warn
		f.detail = fmt.Sprintf("paused on %s; this machine shares on %s", p.status.Tailnet, p.record.Tailnet)
		f.advice = "Nothing is offered on this tailnet. To share here instead, run " + invocation() + " share."
		return f
	case !p.status.IPv4.IsValid():
		f.state = warn
		f.detail = "paused, since tailscale reports no IPv4 address"
		return f
	}

	ip := p.status.IPv4
	if p.resolverErr != nil || !slices.Contains(p.resolver, ip) {
		f.state = bad
		f.detail = fmt.Sprintf("nothing answers DNS for *.%s on %s:53", zone, ip)
		f.advice = "The daemon answers there while it shares. If it is running an older build, run " + invocation() + " restart. " +
			"If port 53 is refused, run " + invocation() + " share again, which offers to lower the port floor. Its log, daemon.log in the state directory, says which."
		return f
	}
	if !slices.Contains(p.system, ip) {
		f.state = warn
		f.detail = fmt.Sprintf("*.%s does not resolve to %s through this machine's DNS", zone, ip)
		f.advice = "Add the split DNS entry in " + adminDNS + ": a custom nameserver " + ip.String() + ", restricted to " + zone + ". Each device also needs Use Tailscale DNS turned on."
		return f
	}
	switch {
	case p.holderErr != nil:
		f.state = warn
		f.detail = "cannot tell who holds tailnet port 443: " + p.holderErr.Error()
		return f
	case p.holder == tailnet.Other:
		f.state = warn
		f.detail = "tailnet port 443 is served by something else"
		f.advice = "Other devices resolve these hostnames and then reach that instead. Move it to another port (t3code: Settings, Tailscale Serve port) and grove takes 443 within ten seconds."
		return f
	case p.holder == tailnet.Free:
		f.state = warn
		f.detail = "tailnet port 443 is not forwarded to grove yet"
		f.advice = "The daemon claims it while it shares. If it is running an older build, run " + invocation() + " restart. " +
			"If this user may not change Tailscale Serve, run " + tailnet.OperatorFix + "; daemon.log in the state directory says which."
		return f
	}
	f.state = ok
	f.detail = fmt.Sprintf("*.%s on %s, at %s", zone, p.record.Tailnet, ip)
	return f
}
