package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("tailnet: tailscaled is not reachable")

// Linux permits Serve writes only for root and the configured operator.
var ErrDenied = errors.New("tailnet: this user may not change Tailscale Serve")

const OperatorFix = `sudo tailscale set --operator="$USER"`

var ErrChanged = errors.New("tailnet: the Serve config changed underneath grove")

// ErrNotAccepted means tailscaled took the file but the device never did,
// usually a phone with Tailscale asleep in the background.
var ErrNotAccepted = errors.New("tailnet: the device did not accept the file")

type Status struct {
	Running bool
	Tailnet string
	// Use the MagicDNS label so split DNS and host-qualified names agree.
	Host string
	IPv4 netip.Addr
	IPv6 netip.Addr
	// The other devices on the tailnet, which grove trust fetches roots from
	// and sends this machine's root to.
	Peers []Peer
}

type Peer struct {
	// Tailscale's stable node ID, which is what Taildrop addresses.
	ID string
	// The first label of the peer's MagicDNS name, as Status.Host is for self.
	Host   string
	OS     string
	IPv4   netip.Addr
	Online bool
}

// Peer finds a device by the name it has on the tailnet.
func (s Status) Peer(host string) (Peer, bool) {
	host = strings.ToLower(host)
	for _, p := range s.Peers {
		if p.Host == host {
			return p, true
		}
	}
	return Peer{}, false
}

type Client struct {
	http     *http.Client
	transfer *http.Client
}

func New(socket string) *Client {
	if socket == "" {
		return &Client{}
	}
	dialer := net.Dialer{Timeout: 2 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socket)
		},
	}
	return &Client{
		http: &http.Client{Timeout: 5 * time.Second, Transport: transport},
		// A Taildrop send holds the request open until the device has the
		// file, which a phone with Tailscale in the background can take far
		// longer than five seconds to do. The caller's context bounds it.
		transfer: &http.Client{Transport: transport},
	}
}

func (c *Client) Status(ctx context.Context) (Status, error) {
	var raw struct {
		BackendState   string
		CurrentTailnet *struct{ Name string }
		Self           *struct {
			DNSName      string
			TailscaleIPs []string
		}
		Peer map[string]struct {
			ID           string
			DNSName      string
			OS           string
			Online       bool
			TailscaleIPs []string
		}
	}
	body, _, err := c.get(ctx, "status")
	if err != nil {
		return Status{}, err
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Status{}, fmt.Errorf("tailnet: reading status: %w", err)
	}
	s := Status{Running: raw.BackendState == "Running"}
	if raw.CurrentTailnet != nil {
		s.Tailnet = raw.CurrentTailnet.Name
	}
	if raw.Self != nil {
		s.Host = firstLabel(raw.Self.DNSName)
		for _, ip := range raw.Self.TailscaleIPs {
			addr, err := netip.ParseAddr(ip)
			switch {
			case err != nil:
			case addr.Is4() && !s.IPv4.IsValid():
				s.IPv4 = addr
			case addr.Is6() && !s.IPv6.IsValid():
				s.IPv6 = addr
			}
		}
	}
	for _, p := range raw.Peer {
		peer := Peer{ID: p.ID, Host: firstLabel(p.DNSName), OS: p.OS, Online: p.Online}
		for _, ip := range p.TailscaleIPs {
			if addr, err := netip.ParseAddr(ip); err == nil && addr.Is4() {
				peer.IPv4 = addr
				break
			}
		}
		if peer.Host != "" {
			s.Peers = append(s.Peers, peer)
		}
	}
	return s, nil
}

func firstLabel(dnsName string) string {
	label, _, _ := strings.Cut(strings.TrimSuffix(dnsName, "."), ".")
	return strings.ToLower(label)
}

// SendFile delivers body to a device over Taildrop, which Tailscale encrypts
// and authenticates end to end, so the device knows which machine sent it.
func (c *Client) SendFile(ctx context.Context, to Peer, name string, body []byte) error {
	if c.http == nil {
		return ErrUnavailable
	}
	target := "http://local-tailscaled.sock/localapi/v0/file-put/" + url.PathEscape(to.ID) + "/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Sec-Tailscale", "localapi")
	req.ContentLength = int64(len(body))
	resp, err := c.transfer.Do(req)
	switch {
	case err != nil && errors.Is(err, context.DeadlineExceeded):
		return fmt.Errorf("%w: %s", ErrNotAccepted, to.Host)
	case err != nil:
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrDenied, strings.TrimSpace(string(msg)))
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("tailnet: sending %s to %s: %s: %s", name, to.Host, resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

// Preserve unknown handlers when updating the Serve config.
type ServeConfig struct {
	doc  map[string]json.RawMessage
	etag string
}

func (c *Client) ServeConfig(ctx context.Context) (*ServeConfig, error) {
	body, etag, err := c.get(ctx, "serve-config")
	if err != nil {
		return nil, err
	}
	return ParseServeConfig(body, etag)
}

func ParseServeConfig(body []byte, etag string) (*ServeConfig, error) {
	doc := map[string]json.RawMessage{}
	if trimmed := bytes.TrimSpace(body); len(trimmed) > 0 && string(trimmed) != "null" {
		if err := json.Unmarshal(trimmed, &doc); err != nil {
			return nil, fmt.Errorf("tailnet: reading the Serve config: %w", err)
		}
	}
	return &ServeConfig{doc: doc, etag: etag}, nil
}

func (cfg *ServeConfig) MarshalJSON() ([]byte, error) { return json.Marshal(cfg.doc) }

func (c *Client) SetServeConfig(ctx context.Context, cfg *ServeConfig) error {
	if c.http == nil {
		return ErrUnavailable
	}
	body, err := cfg.MarshalJSON()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://local-tailscaled.sock/localapi/v0/serve-config", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Sec-Tailscale", "localapi")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", cfg.etag)
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	switch {
	case resp.StatusCode == http.StatusPreconditionFailed:
		return ErrChanged
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s", ErrDenied, strings.TrimSpace(string(msg)))
	case resp.StatusCode/100 != 2:
		return fmt.Errorf("tailnet: writing the Serve config: %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

type Holder int

const (
	Free Holder = iota
	Ours
	Other
)

type tcpHandler struct {
	HTTPS      bool   `json:",omitempty"`
	TCPForward string `json:",omitempty"`
}

func (cfg *ServeConfig) tcp() (map[string]json.RawMessage, error) {
	ports := map[string]json.RawMessage{}
	if raw, ok := cfg.doc["TCP"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &ports); err != nil {
			return nil, fmt.Errorf("tailnet: reading the Serve config's TCP ports: %w", err)
		}
	}
	return ports, nil
}

func (cfg *ServeConfig) Port443(target string) (Holder, error) {
	// Foreground sessions own their listeners too, even though grove only
	// writes background mappings.
	var foreground map[string]struct{ TCP map[string]json.RawMessage }
	if raw, ok := cfg.doc["Foreground"]; ok {
		if err := json.Unmarshal(raw, &foreground); err != nil {
			return Other, fmt.Errorf("tailnet: reading foreground Serve handlers: %w", err)
		}
	}
	for _, session := range foreground {
		if _, ok := session.TCP["443"]; ok {
			return Other, nil
		}
	}
	ports, err := cfg.tcp()
	if err != nil {
		return Other, err
	}
	raw, ok := ports["443"]
	if !ok {
		return Free, nil
	}
	var h tcpHandler
	if err := json.Unmarshal(raw, &h); err != nil {
		return Other, nil
	}
	if !h.HTTPS && h.TCPForward == target {
		return Ours, nil
	}
	return Other, nil
}

// Forward raw TCP so TLS reaches grove with its original server name.
func (cfg *ServeConfig) Claim443(target string) (changed bool, err error) {
	holder, err := cfg.Port443(target)
	if err != nil || holder == Other {
		return false, errors.Join(err, errHeld)
	}
	if holder == Ours {
		return false, nil
	}
	ports, _ := cfg.tcp()
	entry, _ := json.Marshal(tcpHandler{TCPForward: target})
	ports["443"] = entry
	return true, cfg.setTCP(ports)
}

func (cfg *ServeConfig) Release443(target string) (changed bool, err error) {
	holder, err := cfg.Port443(target)
	if err != nil || holder != Ours {
		return false, err
	}
	ports, _ := cfg.tcp()
	delete(ports, "443")
	return true, cfg.setTCP(ports)
}

var errHeld = errors.New("tailnet: port 443 on the tailnet is served by something other than grove")

func IsHeld(err error) bool { return errors.Is(err, errHeld) }

func (cfg *ServeConfig) setTCP(ports map[string]json.RawMessage) error {
	if len(ports) == 0 {
		delete(cfg.doc, "TCP")
		return nil
	}
	raw, err := json.Marshal(ports)
	if err != nil {
		return err
	}
	cfg.doc["TCP"] = raw
	return nil
}

func (c *Client) get(ctx context.Context, endpoint string) ([]byte, string, error) {
	if c.http == nil {
		return nil, "", ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://local-tailscaled.sock/localapi/v0/"+endpoint, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Sec-Tailscale", "localapi")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode/100 != 2 {
		return nil, "", fmt.Errorf("tailnet: %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}
	return body, resp.Header.Get("Etag"), nil
}
