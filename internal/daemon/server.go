package daemon

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/lease"
	"github.com/grove-sh/cli/internal/proxy"
)

type Config struct {
	Domain string
	CADir  string
	Range  lease.PortRange
}

type Server struct {
	domain   string
	registry *lease.Registry
	proxy    *proxy.Server
	cert     *tls.Certificate
	root     []byte

	mu      sync.Mutex
	control net.Listener
	conns   map[net.Conn]struct{}
	closing bool
}

func New(cfg Config) (*Server, error) {
	if cfg.Domain == "" {
		return nil, errors.New("daemon: no domain configured")
	}
	root, err := ca.Open(cfg.CADir)
	if err != nil {
		return nil, err
	}
	cert, err := root.Leaf("*."+cfg.Domain, cfg.Domain)
	if err != nil {
		return nil, err
	}
	registry, err := lease.New(lease.Options{Range: cfg.Range})
	if err != nil {
		return nil, err
	}
	return &Server{
		domain:   cfg.Domain,
		registry: registry,
		proxy:    proxy.New(),
		cert:     cert,
		root:     root.RootPEM(),
		conns:    make(map[net.Conn]struct{}),
	}, nil
}

func (s *Server) RootPEM() []byte { return s.root }

// Listen binds the control socket, replacing one a dead daemon left behind.
func Listen(socket string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", socket)
	if err == nil {
		return ln, nil
	}
	// Something holds the path. If nothing answers on it, it is a leftover.
	if probe, dialErr := net.Dial("unix", socket); dialErr == nil {
		probe.Close()
		return nil, fmt.Errorf("daemon: another grove daemon is already listening at %s", socket)
	}
	if rmErr := os.Remove(socket); rmErr != nil {
		return nil, err
	}
	return net.Listen("unix", socket)
}

// Serve runs the control socket and the HTTPS proxy until one of them fails.
func (s *Server) Serve(control, https net.Listener) error {
	s.mu.Lock()
	s.control = control
	s.mu.Unlock()

	failed := make(chan error, 2)
	go func() { failed <- s.serveControl(control) }()
	go func() { failed <- s.proxy.Serve(https, s.cert) }()

	err := <-failed
	s.Shutdown()
	if s.isClosing() {
		return nil
	}
	return err
}

func (s *Server) Shutdown() {
	s.mu.Lock()
	if s.closing {
		s.mu.Unlock()
		return
	}
	s.closing = true
	control := s.control
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	s.mu.Unlock()

	if control != nil {
		control.Close()
	}
	// Closing a connection ends the lease it holds.
	for _, conn := range conns {
		conn.Close()
	}
	s.proxy.Shutdown(context.Background())
}

func (s *Server) isClosing() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closing
}

func (s *Server) serveControl(ln net.Listener) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return err
		}
		s.track(conn)
		go func() {
			defer s.untrack(conn)
			s.handle(conn)
		}()
	}
}

func (s *Server) track(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns[conn] = struct{}{}
}

func (s *Server) untrack(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()

	enc := json.NewEncoder(conn)
	var req Request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		return
	}

	switch req.Op {
	case OpList:
		enc.Encode(Response{Leases: s.entries()})
	case OpAcquire:
		s.acquire(conn, enc, req)
	default:
		enc.Encode(Response{Error: fmt.Sprintf("daemon: unknown op %q", req.Op)})
	}
}

func (s *Server) acquire(conn net.Conn, enc *json.Encoder, req Request) {
	held, err := s.registry.Acquire(req.Slug, req.Service, req.Worktree)
	if err != nil {
		enc.Encode(Response{Error: err.Error()})
		return
	}
	defer func() {
		held.Release()
		s.syncRoutes()
	}()
	s.syncRoutes()

	host := s.host(held.Slug, held.Service)
	grant := Grant{Port: held.Port, Host: host, URL: "https://" + host}
	if err := enc.Encode(Response{Grant: &grant}); err != nil {
		return
	}

	// The lease lasts as long as the connection. The kernel reports the close
	// whether the client exited, crashed, or was killed outright.
	io.Copy(io.Discard, conn)
}

func (s *Server) syncRoutes() {
	live := s.registry.List()
	routes := make([]proxy.Route, 0, len(live))
	for _, l := range live {
		routes = append(routes, proxy.Route{
			Host:     s.host(l.Slug, l.Service),
			Upstream: net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port)),
		})
	}
	s.proxy.SetRoutes(routes)
}

func (s *Server) entries() []Entry {
	live := s.registry.List()
	out := make([]Entry, 0, len(live))
	for _, l := range live {
		out = append(out, Entry{
			Slug:     l.Slug,
			Service:  l.Service,
			Worktree: l.Worktree,
			Port:     l.Port,
			Host:     s.host(l.Slug, l.Service),
		})
	}
	return out
}

// The default service takes the bare context hostname. Others get a suffix,
// since a wildcard certificate covers only one label.
func (s *Server) host(slug, service string) string {
	if service != "" {
		slug += "-" + service
	}
	return slug + "." + s.domain
}
