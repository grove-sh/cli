package lease

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Only matters for a port that could not be the hashed one. Colliding entries
// are resolved in whichever order they ask, which is not stable across a
// restart, so with no record the entry that walked last time can take the
// hashed port next time and be handed the port another worktree's stack is
// listening on: a database URL pointing at someone else's data.
type Memory interface {
	Port(slug, service string) (int, bool)
	Remember(slug, service string, port int) error
}

// Every collision resolved again from scratch.
type noMemory struct{}

func (noMemory) Port(string, string) (int, bool)    { return 0, false }
func (noMemory) Remember(string, string, int) error { return nil }

// Only exceptions are written, so the file stays a short list of entries whose
// port is not the one arithmetic gives, not a copy of every allocation.
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
