// Package proxy routes HTTPS requests by hostname to loopback ports.
package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

type Route struct {
	Host     string
	Upstream string
}

type Server struct {
	routes atomic.Pointer[map[string]*httputil.ReverseProxy]
	http   *http.Server
}

func New() *Server {
	s := &Server{}
	s.SetRoutes(nil)
	// No read or write timeout: websockets and SSE are the normal case here. A
	// header timeout is still safe and keeps a stalled handshake from pinning a
	// connection.
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second}
	return s
}

// SetRoutes replaces the whole table. Requests in flight keep the table they
// started with, so a swap needs no coordination with them.
func (s *Server) SetRoutes(routes []Route) {
	table := make(map[string]*httputil.ReverseProxy, len(routes))
	for _, r := range routes {
		table[strings.ToLower(r.Host)] = newReverseProxy(r.Upstream)
	}
	s.routes.Store(&table)
}

func (s *Server) Serve(ln net.Listener, cert *tls.Certificate) error {
	s.http.TLSConfig = &tls.Config{
		MinVersion: tls.VersionTLS12,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return cert, nil
		},
	}
	return s.http.ServeTLS(ln, "", "")
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.http.Shutdown(ctx)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host := hostOnly(r.Host)
	rp, ok := (*s.routes.Load())[host]
	if !ok {
		unavailable(w, host, "No grove context is bound to this hostname.")
		return
	}
	rp.ServeHTTP(w, r)
}

func newReverseProxy(upstream string) *httputil.ReverseProxy {
	target := &url.URL{Scheme: "http", Host: upstream}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(target)
			// Dev servers check Host against an allowlist, Vite's allowedHosts
			// among them, so the grove hostname has to survive the hop.
			r.Out.Host = r.In.Host
			r.SetXForwarded()
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			unavailable(w, hostOnly(r.Host), "Nothing is listening on the port grove leased.")
		},
	}
}

func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return strings.ToLower(h)
	}
	return strings.ToLower(hostport)
}

func unavailable(w http.ResponseWriter, host, detail string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusServiceUnavailable)
	fmt.Fprintf(w, unavailablePage, html.EscapeString(host), html.EscapeString(detail))
}

const unavailablePage = `<!doctype html>
<meta charset="utf-8">
<title>grove: nothing bound</title>
<body style="font:14px/1.6 ui-monospace,SFMono-Regular,monospace;max-width:40rem;margin:4rem auto;padding:0 1rem">
<h1 style="font-size:1rem;font-weight:600">503 &middot; %s</h1>
<p>%s</p>
<p>Run <code>grove exec -- &lt;dev command&gt;</code> in that worktree, or <code>grove ls</code> to see what is running.</p>
`
