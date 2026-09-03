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
	"time"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/identity"
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

	listen  string
	started time.Time

	mu      sync.Mutex
	control net.Listener
	conns   map[net.Conn]struct{}
	routed  map[routeKey]string
	closing bool
}

type routeKey struct {
	slug    string
	service string
}

func New(cfg Config) (*Server, error) {
	if cfg.Domain == "" {
		return nil, errors.New("daemon: no domain configured")
	}
	root, err := ca.Open(cfg.CADir)
	if err != nil {
		if errors.Is(err, ca.ErrNoAuthority) {
			return nil, fmt.Errorf("%w; run 'grove install' first", err)
		}
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
		routed:   make(map[routeKey]string),
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
	// The limit is around 104 bytes on macOS and 108 on Linux, and the kernel
	// reports it as nothing more helpful than an invalid argument.
	if len(socket) > 100 {
		return nil, fmt.Errorf("%w\nthat path is %d bytes, and a unix socket allows about 104; set GROVE_SOCKET to something shorter", err, len(socket))
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
	s.listen = https.Addr().String()
	s.started = time.Now()
	s.mu.Unlock()

	failed := make(chan error, 2)
	go func() { failed <- s.serveControl(control) }()
	go func() { failed <- s.proxy.Serve(https, s.cert) }()

	Ready()

	err := <-failed
	Stopping()
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
	if req.Version != Version {
		enc.Encode(Response{Version: Version, Error: fmt.Sprintf(
			"this grove speaks control protocol v%d and the running daemon speaks v%d; stop the daemon and start it again",
			req.Version, Version)})
		return
	}

	switch req.Op {
	case OpList:
		enc.Encode(Response{Version: Version, Leases: s.entries()})
	case OpStatus:
		enc.Encode(Response{Version: Version, Status: s.status()})
	case OpRelease:
		enc.Encode(Response{Version: Version, Released: s.release(req)})
	case OpStop:
		enc.Encode(Response{Version: Version})
		go s.Shutdown()
	case OpAcquire:
		s.acquire(conn, enc, req)
	default:
		enc.Encode(Response{Version: Version, Error: fmt.Sprintf("daemon: unknown op %q", req.Op)})
	}
}

func (s *Server) acquire(conn net.Conn, enc *json.Encoder, req Request) {
	grants := make(map[string]Grant, len(req.Entries))
	var attached []*lease.Lease

	releaseAttached := func() {
		if len(attached) == 0 {
			return
		}
		s.mu.Lock()
		for _, held := range attached {
			delete(s.routed, routeKey{slug: held.Slug, service: held.Service})
		}
		s.mu.Unlock()
		for _, held := range attached {
			held.Release()
		}
		s.syncRoutes()
	}

	for _, entry := range req.Entries {
		held, err := s.registry.Acquire(lease.Request{
			Slug:     req.Slug,
			Service:  entry.Name,
			Worktree: req.Worktree,
			Detached: entry.Detached,
			PID:      req.PID,
		})
		if err != nil {
			releaseAttached()
			enc.Encode(Response{Version: Version, Error: err.Error()})
			return
		}
		if !entry.Detached {
			attached = append(attached, held)
		}

		grant := Grant{Port: held.Port}
		if entry.Routed {
			grant.Host = s.host(req.Slug, entry.Label)
			grant.URL = "https://" + grant.Host
			s.mu.Lock()
			s.routed[routeKey{slug: req.Slug, service: entry.Name}] = grant.Host
			s.mu.Unlock()
		}
		grants[entry.Name] = grant
	}

	defer releaseAttached()
	s.syncRoutes()

	if err := enc.Encode(Response{Version: Version, Grants: grants}); err != nil {
		return
	}

	// The attached leases last as long as the connection. The kernel reports
	// the close whether the client exited, crashed, or was killed outright.
	io.Copy(io.Discard, conn)
}

func (s *Server) status() *Status {
	s.mu.Lock()
	listen := s.listen
	s.mu.Unlock()

	return &Status{
		Version: Version,
		PID:     os.Getpid(),
		Listen:  listen,
		Leases:  len(s.registry.List()),
	}
}

// release ends detached leases, which is the only way one goes away short of
// stopping the daemon.
func (s *Server) release(req Request) []string {
	wanted := req.Names
	if len(wanted) == 0 {
		for _, live := range s.registry.List() {
			if live.Slug == req.Slug && live.Detached {
				wanted = append(wanted, live.Service)
			}
		}
	}

	var released []string
	for _, name := range wanted {
		if !s.registry.ReleaseNamed(req.Slug, name) {
			continue
		}
		s.mu.Lock()
		delete(s.routed, routeKey{slug: req.Slug, service: name})
		s.mu.Unlock()
		released = append(released, name)
	}
	if len(released) > 0 {
		s.syncRoutes()
	}
	return released
}

func (s *Server) syncRoutes() {
	live := s.registry.List()

	s.mu.Lock()
	routes := make([]proxy.Route, 0, len(live))
	for _, l := range live {
		host, routed := s.routed[routeKey{slug: l.Slug, service: l.Service}]
		if !routed {
			continue
		}
		routes = append(routes, proxy.Route{
			Host:     host,
			Upstream: net.JoinHostPort("127.0.0.1", strconv.Itoa(l.Port)),
		})
	}
	s.mu.Unlock()

	s.proxy.SetRoutes(routes)
}

func (s *Server) entries() []Live {
	live := s.registry.List()

	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]Live, 0, len(live))
	for _, l := range live {
		entry := Live{
			Slug:     l.Slug,
			Service:  l.Service,
			Worktree: l.Worktree,
			Port:     l.Port,
			Host:     s.routed[routeKey{slug: l.Slug, service: l.Service}],
			Detached: l.Detached,
		}
		// Only an attached lease has a process holding it. The command that
		// asserted a detached one has exited by definition, so naming its pid
		// would point at a process that is not there.
		if !l.Detached {
			entry.PID = l.PID
		}
		out = append(out, entry)
	}
	return out
}

// An empty label means the context's own hostname. Capping happens on the
// composed label, since that is what has to fit in 63 bytes.
func (s *Server) host(slug, label string) string {
	return identity.ComposeLabel(slug, label) + "." + s.domain
}
