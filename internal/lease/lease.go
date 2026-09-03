// Package lease hands out loopback ports to contexts and tracks which contexts
// are live. A lease exists only while something holds it, so nothing survives a
// daemon restart and there is no state to garbage collect.
package lease

import (
	"cmp"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"slices"
	"strconv"
	"sync"
)

// Below Linux's default ephemeral range of 32768-60999, so grove never contends
// with ports the kernel hands to outgoing connections.
var DefaultRange = PortRange{Low: 20000, High: 20999}

type PortRange struct {
	Low  int
	High int
}

func (r PortRange) size() int { return r.High - r.Low + 1 }

func (r PortRange) String() string { return fmt.Sprintf("%d-%d", r.Low, r.High) }

type Options struct {
	// Range to allocate from. The zero value means DefaultRange.
	Range PortRange

	// Free reports whether a port can be bound right now. Tests replace it.
	Free func(port int) bool
}

type Registry struct {
	mu     sync.Mutex
	rng    PortRange
	free   func(int) bool
	leases map[key]*Lease
	ports  map[int]key
	owners map[string]string // slug to worktree, while that slug holds a lease
}

type key struct {
	slug    string
	service string
}

type Lease struct {
	Slug     string
	Service  string
	Worktree string
	Port     int

	// Detached says the port belongs to something that outlives the command
	// that asked for it, so closing that command's connection must not end it.
	Detached bool

	registry *Registry
	released bool
}

type Request struct {
	Slug     string
	Service  string
	Worktree string
	Detached bool
}

func New(opts Options) (*Registry, error) {
	rng := opts.Range
	if rng == (PortRange{}) {
		rng = DefaultRange
	}
	if rng.Low < 1 || rng.High > 65535 || rng.Low > rng.High {
		return nil, fmt.Errorf("lease: port range %s is not usable", rng)
	}
	free := opts.Free
	if free == nil {
		free = freeOnLoopback
	}
	return &Registry{
		rng:    rng,
		free:   free,
		leases: make(map[key]*Lease),
		ports:  make(map[int]key),
		owners: make(map[string]string),
	}, nil
}

// Acquire leases a port for one service of one context. An attached lease lasts
// as long as the caller holds it. A detached one is idempotent, since every
// command in the context re-asserts the same allocation.
func (r *Registry) Acquire(req Request) (*Lease, error) {
	if req.Slug == "" {
		return nil, errors.New("lease: acquire needs a slug")
	}
	if req.Worktree == "" {
		return nil, errors.New("lease: acquire needs a worktree path")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if held, ok := r.owners[req.Slug]; ok && held != req.Worktree {
		return nil, &CollisionError{Slug: req.Slug, Held: held, Wanted: req.Worktree}
	}

	k := key{slug: req.Slug, service: req.Service}
	if existing, ok := r.leases[k]; ok {
		if existing.Detached && req.Detached {
			return existing, nil
		}
		return nil, &BusyError{Slug: req.Slug, Service: req.Service, Port: existing.Port}
	}

	port, err := r.pick(k, req.Detached)
	if err != nil {
		return nil, err
	}

	l := &Lease{
		Slug:     req.Slug,
		Service:  req.Service,
		Worktree: req.Worktree,
		Port:     port,
		Detached: req.Detached,
		registry: r,
	}
	r.leases[k] = l
	r.ports[port] = k
	r.owners[req.Slug] = req.Worktree
	return l, nil
}

// Release ends a lease. It is safe to call more than once, and safe on the
// copies List returns, where it does nothing.
func (l *Lease) Release() {
	if l == nil || l.registry == nil {
		return
	}
	r := l.registry

	r.mu.Lock()
	defer r.mu.Unlock()
	if l.released {
		return
	}
	l.released = true
	delete(r.leases, key{slug: l.Slug, service: l.Service})
	delete(r.ports, l.Port)

	for k := range r.leases {
		if k.slug == l.Slug {
			return
		}
	}
	delete(r.owners, l.Slug)
}

// ReleaseNamed ends one lease and reports whether it was there to end. Only
// detached leases are eligible: an attached one belongs to a live connection,
// and pulling it out from under that process would drop its route while it runs.
func (r *Registry) ReleaseNamed(slug, service string) bool {
	r.mu.Lock()
	held, ok := r.leases[key{slug: slug, service: service}]
	r.mu.Unlock()

	if !ok || !held.Detached {
		return false
	}
	held.Release()
	return true
}

// List reports the live leases, sorted. The copies cannot be released.
func (r *Registry) List() []Lease {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Lease, 0, len(r.leases))
	for _, l := range r.leases {
		copied := *l
		copied.registry = nil
		out = append(out, copied)
	}
	slices.SortFunc(out, func(a, b Lease) int {
		return cmp.Or(cmp.Compare(a.Slug, b.Slug), cmp.Compare(a.Service, b.Service))
	})
	return out
}

// pick starts from a hash of the context so the same context tends to get the
// same port on any machine, with no state on disk, then walks the range.
//
// A detached port stops at the hash. Grove's own child is not the one binding
// it, so a port already in use is the expected case, and walking past it would
// hand back a number nothing is listening on. Stopping there is also what lets
// a restarted daemon re-adopt a running stack instead of allocating beside it.
func (r *Registry) pick(k key, detached bool) (int, error) {
	size := r.rng.size()
	h := fnv.New32a()
	h.Write([]byte(k.slug))
	h.Write([]byte{0})
	h.Write([]byte(k.service))
	offset := int(h.Sum32() % uint32(size))

	if detached {
		port := r.rng.Low + offset
		if _, taken := r.ports[port]; taken {
			return 0, &ExhaustedError{Range: r.rng}
		}
		return port, nil
	}

	for i := 0; i < size; i++ {
		port := r.rng.Low + (offset+i)%size
		if _, taken := r.ports[port]; taken {
			continue
		}
		if !r.free(port) {
			continue
		}
		return port, nil
	}
	return 0, &ExhaustedError{Range: r.rng}
}

// freeOnLoopback reports that a port was free a moment ago. The child process
// is what actually binds it, so this is advice, not a reservation.
func freeOnLoopback(port int) bool {
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return false
	}
	ln.Close()
	return true
}

type CollisionError struct {
	Slug   string
	Held   string
	Wanted string
}

func (e *CollisionError) Error() string {
	return fmt.Sprintf("lease: context %q is already held by %s, and %s resolves to the same name; rename one worktree directory, or give one of them a distinct GROVE_CONTEXT",
		e.Slug, e.Held, e.Wanted)
}

type BusyError struct {
	Slug    string
	Service string
	Port    int
}

func (e *BusyError) Error() string {
	return fmt.Sprintf("lease: %s is already running on port %d", describe(e.Slug, e.Service), e.Port)
}

type ExhaustedError struct {
	Range PortRange
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("lease: no free port in %s", e.Range)
}

func describe(slug, service string) string {
	if service == "" {
		return slug
	}
	return slug + " " + service
}
