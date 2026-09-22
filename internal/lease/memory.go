package lease

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
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
	Remember(slug, service string, port int) error
	Forget(slug, service string) error
}

// Every collision resolved again from scratch.
type noMemory struct{}

func (noMemory) Port(string, string) (int, bool)    { return 0, false }
func (noMemory) Owner(int) (string, string, bool)   { return "", "", false }
func (noMemory) Remember(string, string, int) error { return nil }
func (noMemory) Forget(string, string) error        { return nil }

type FileMemory struct {
	path string

	mu    sync.Mutex
	ports map[string]map[string]int
}

// An unparseable file is started over rather than refused: every entry can be
// derived again, so a broken record costs a reshuffle and not a daemon. One
// that cannot be read at all is a different thing, and says so.
func OpenMemory(path string) (*FileMemory, error) {
	m := &FileMemory{path: path, ports: map[string]map[string]int{}}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return m, nil
	case err != nil:
		return nil, err
	}

	var stored struct {
		Ports map[string]map[string]int `json:"ports"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		return m, nil
	}
	if stored.Ports != nil {
		m.ports = stored.Ports
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

func (m *FileMemory) Remember(slug, service string, port int) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.ports[slug][service] == port {
		return nil
	}
	if m.ports[slug] == nil {
		m.ports[slug] = map[string]int{}
	}
	m.ports[slug][service] = port
	return m.write()
}

func (m *FileMemory) Forget(slug, service string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, ok := m.ports[slug][service]; !ok {
		return nil
	}
	delete(m.ports[slug], service)
	if len(m.ports[slug]) == 0 {
		delete(m.ports, slug)
	}
	return m.write()
}

// By rename, so a daemon dying mid-write leaves the previous record rather than
// half of a new one.
func (m *FileMemory) write() error {
	body, err := json.MarshalIndent(struct {
		Ports map[string]map[string]int `json:"ports"`
	}{Ports: m.ports}, "", "  ")
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
