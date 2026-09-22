package lease

import (
	"cmp"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

// Where every detached entry landed. Colliding entries are resolved in
// whichever order they ask, which is not stable across a restart, so with no
// record the entry that walked last time can take the hashed port next time
// and be handed the port another worktree's stack is listening on: a database
// URL pointing at someone else's data.
//
// Every entry and not only the walked ones, because the entry that kept its
// hashed port is the one with a stack on it. Once its daemon is gone, a reboot
// having restarted the containers but not grove, nothing else names that port
// and the entry it collides with takes it.
type Memory interface {
	Port(slug, service string) (int, bool)
	Owner(port int) (slug, service string, ok bool)
	Remember(slug, service, worktree string, port int) error
	Forget(slug, service string) error
}

// One entry's line in the record. Worktree is empty for a line written before
// the daemon kept paths, which reads the same as a path nobody can check.
type Record struct {
	Slug     string
	Service  string
	Port     int
	Worktree string
}

// Every collision resolved again from scratch.
type noMemory struct{}

func (noMemory) Port(string, string) (int, bool)            { return 0, false }
func (noMemory) Owner(int) (string, string, bool)           { return "", "", false }
func (noMemory) Remember(string, string, string, int) error { return nil }
func (noMemory) Forget(string, string) error                { return nil }

type FileMemory struct {
	path string

	mu        sync.Mutex
	ports     map[string]map[string]int
	worktrees map[string]string
}

// An unparseable file is started over rather than refused: every entry can be
// derived again, so a broken record costs a reshuffle and not a daemon. One
// that cannot be read at all is a different thing, and says so.
func OpenMemory(path string) (*FileMemory, error) {
	m := &FileMemory{path: path, ports: map[string]map[string]int{}, worktrees: map[string]string{}}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return m, nil
	case err != nil:
		return nil, err
	}

	var stored struct {
		Ports map[string]map[string]int `json:"ports"`
		// A sibling key rather than a reshaping, so a grove that predates it
		// still reads the ports out and only drops the paths when it writes.
		Worktrees map[string]string `json:"worktrees"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return m, nil
	}
	if stored.Ports != nil {
		m.ports = stored.Ports
	}
	if stored.Worktrees != nil {
		m.worktrees = stored.Worktrees
	}
	// A path is only ever written beside a port, so one on its own came from a
	// hand-edited file. Dropping it on the way in keeps Records and
	// ForgetContext agreeing about which contexts the file holds.
	for slug := range m.worktrees {
		if len(m.ports[slug]) == 0 {
			delete(m.worktrees, slug)
		}
	}
	return m, nil
}

func (m *FileMemory) Port(slug, service string) (int, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	port, ok := m.ports[slug][service]
	return port, ok
}

// A scan rather than a second index: tens of entries, asked once per candidate
// port in a walk.
func (m *FileMemory) Owner(port int) (string, string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for slug, services := range m.ports {
		for service, held := range services {
			if held == port {
				return slug, service, true
			}
		}
	}
	return "", "", false
}

// The maps are a reading of the file, so a write that fails puts back what
// they held: a daemon answering from memory with something the file does not
// carry outlives the error and survives no restart. Same in Forget and
// ForgetContext.
func (m *FileMemory) Remember(slug, service, worktree string, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	was, held := m.ports[slug][service]
	wasTree, heldTree := m.worktrees[slug]
	if held && was == port && wasTree == worktree {
		return nil
	}
	if m.ports[slug] == nil {
		m.ports[slug] = map[string]int{}
	}
	m.ports[slug][service] = port
	// A context that moved is the same context: the path is where it was last
	// seen, which is the only thing that can say later whether it is still
	// there.
	if worktree != "" {
		m.worktrees[slug] = worktree
	}

	if err := m.write(); err != nil {
		if held {
			m.ports[slug][service] = was
		} else {
			delete(m.ports[slug], service)
			if len(m.ports[slug]) == 0 {
				delete(m.ports, slug)
			}
		}
		if heldTree {
			m.worktrees[slug] = wasTree
		} else {
			delete(m.worktrees, slug)
		}
		return err
	}
	return nil
}

// Records answers with the whole file, since every caller of it is reporting
// on or pruning the record rather than allocating out of it.
func (m *FileMemory) Records() []Record {
	m.mu.Lock()
	defer m.mu.Unlock()

	var out []Record
	for slug, services := range m.ports {
		for service, port := range services {
			out = append(out, Record{Slug: slug, Service: service, Port: port, Worktree: m.worktrees[slug]})
		}
	}
	slices.SortFunc(out, func(a, b Record) int {
		return cmp.Or(cmp.Compare(a.Slug, b.Slug), cmp.Compare(a.Service, b.Service))
	})
	return out
}

// ForgetContext takes the whole context, which is what a deleted worktree
// leaves behind: entries are only ever recorded together.
func (m *FileMemory) ForgetContext(slug string) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	services, ok := m.ports[slug]
	if !ok {
		return nil, nil
	}
	wasTree := m.worktrees[slug]
	var gone []Record
	for service, port := range services {
		gone = append(gone, Record{Slug: slug, Service: service, Port: port, Worktree: wasTree})
	}
	slices.SortFunc(gone, func(a, b Record) int { return cmp.Compare(a.Service, b.Service) })

	delete(m.ports, slug)
	delete(m.worktrees, slug)

	if err := m.write(); err != nil {
		m.ports[slug] = services
		if wasTree != "" {
			m.worktrees[slug] = wasTree
		}
		// Nothing went, so naming what would have gone would be the same lie
		// the caller's report was about to tell.
		return nil, err
	}
	return gone, nil
}

func (m *FileMemory) Forget(slug, service string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	was, held := m.ports[slug][service]
	if !held {
		return nil
	}
	services, wasTree := m.ports[slug], m.worktrees[slug]
	delete(services, service)
	emptied := len(services) == 0
	if emptied {
		delete(m.ports, slug)
		delete(m.worktrees, slug)
	}

	if err := m.write(); err != nil {
		services[service] = was
		if emptied {
			m.ports[slug] = services
			if wasTree != "" {
				m.worktrees[slug] = wasTree
			}
		}
		return err
	}
	return nil
}

// By rename, so a daemon dying mid-write leaves the previous record rather than
// half of a new one.
func (m *FileMemory) write() error {
	body, err := json.MarshalIndent(struct {
		Ports     map[string]map[string]int `json:"ports"`
		Worktrees map[string]string         `json:"worktrees,omitempty"`
	}{Ports: m.ports, Worktrees: m.worktrees}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.path), 0o700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(m.path), ".ports-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(append(body, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), m.path)
}
