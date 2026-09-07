// Package lease hands out loopback ports to contexts. A lease exists only while
// something holds it, so nothing survives a restart and there is nothing to
// garbage collect.
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

func (r PortRange) Holds(port int) bool { return port >= r.Low && port <= r.High }

func (r PortRange) String() string { return fmt.Sprintf("%d-%d", r.Low, r.High) }

type Options struct {
	// Zero means DefaultRange.
	Range PortRange

	// Tests replace this.
	Free func(port int) bool

	// Nil forgets where a detached lease landed, which resolves every
	// collision again on each restart. See Memory for why that is not enough.
	Memory Memory
}

type Registry struct {
	mu     sync.Mutex
	rng    PortRange
	free   func(int) bool
	memory Memory
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

	// The port belongs to something that outlives the command that asked for
	// it, so closing that command's connection must not end the lease.
	Detached bool

	PID int

	registry *Registry
	released bool
}

type Request struct {
	Slug     string
	Service  string
	Worktree string
	Detached bool

	// So a clash can name what to look at: a lease outlives its listener when a
	// command hangs mid shutdown, and the port alone cannot identify the holder.
	PID int
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
	memory := opts.Memory
	if memory == nil {
		memory = noMemory{}
	}
	return &Registry{
		rng:    rng,
		free:   free,
		memory: memory,
		leases: make(map[key]*Lease),
		ports:  make(map[int]key),
		owners: make(map[string]string),
	}, nil
}

// Acquire is idempotent for a detached lease, since every command in the
// context re-asserts the same allocation. An attached one lasts as long as the
// caller holds it.
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
		return nil, &BusyError{
			Slug:     req.Slug,
			Service:  req.Service,
			Port:     existing.Port,
			PID:      existing.PID,
			Detached: existing.Detached,
		}
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
		PID:      req.PID,
		registry: r,
	}
	r.leases[k] = l
	r.ports[port] = k
	r.owners[req.Slug] = req.Worktree
	return l, nil
}

// Release is safe more than once, and on the copies List returns it does nothing.
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

// ReleaseNamed refuses an attached lease: it belongs to a live connection, and
// pulling it would drop that process's route while it runs.
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

// List returns sorted copies, which cannot be released.
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

// PredictPort is knowable because allocation hashes rather than counts. A port
// already in use sends an attached lease further along, so this is where one
// would go rather than a promise of where it went.
func PredictPort(rng PortRange, slug, service string) int {
	if rng == (PortRange{}) {
		rng = DefaultRange
	}
	return rng.Low + hashOffset(rng, slug, service)
}

func hashOffset(rng PortRange, slug, service string) int {
	h := fnv.New32a()
	h.Write([]byte(slug))
	h.Write([]byte{0})
	h.Write([]byte(service))
	return int(h.Sum32() % uint32(rng.size()))
}

// Starting from a hash means the same context tends to get the same port on any
// machine. A detached port never asks whether it is free, since a port already
// in use is the expected case: usually the stack this entry describes, still
// running. It must still avoid a port another entry holds, and hashes collide
// roughly once in a few hundred entries. Walking past one is only safe because
// where it landed is written down: see Memory for what happens otherwise.
func (r *Registry) pick(k key, detached bool) (int, error) {
	size := r.rng.size()
	offset := hashOffset(r.rng, k.slug, k.service)

	if detached {
		// Where it landed before comes first, since a stack is published on it
		// already. A changed range or a since-taken port falls back to the walk.
		if port, ok := r.memory.Port(k.slug, k.service); ok && r.rng.Holds(port) {
			if _, taken := r.ports[port]; !taken {
				return port, nil
			}
		}
		for i := 0; i < size; i++ {
			port := r.rng.Low + (offset+i)%size
			if _, taken := r.ports[port]; taken {
				continue
			}
			if i > 0 {
				// Failing to write down an exception costs a reshuffle next
				// restart, which is worth less than refusing the lease.
				_ = r.memory.Remember(k.slug, k.service, port)
			}
			return port, nil
		}
		return 0, &ExhaustedError{Range: r.rng}
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

// The child process is what actually binds it, so this is advice, not a
// reservation.
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
	return fmt.Sprintf("lease: context %q is already held by %s, and %s resolves to the same name; rename one worktree directory, or give one of them a distinct GROVE_CONTEXT_OVERRIDE",
		e.Slug, e.Held, e.Wanted)
}

type BusyError struct {
	Slug     string
	Service  string
	Port     int
	PID      int
	Detached bool
}

func (e *BusyError) Error() string {
	held := fmt.Sprintf("lease: %s is already running on port %d", describe(e.Slug, e.Service), e.Port)
	switch {
	case e.Detached:
		// Nothing holds a detached lease in process terms: no pid to name.
		return fmt.Sprintf("%s, detached; end it with 'grove release %s'", held, e.Service)
	case e.PID != 0:
		// The port alone is no help when the holder stopped listening without
		// exiting, which is exactly when this shows up.
		return fmt.Sprintf("%s, held by pid %d", held, e.PID)
	}
	return held
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
