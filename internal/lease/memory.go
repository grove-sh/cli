package lease

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// Memory remembers where a detached lease landed, so a daemon that starts again
// hands the same entry the same port.
//
// It only ever matters for a port that could not be the hashed one. Two entries
// whose hashes collide have to be resolved in some order, and that order is
// whichever context asks first, which is not stable across a restart. With no
// record, the entry that walked last time can win the race next time, take the
// hashed port, and be handed the port the other context's stack is actually
// listening on: a database URL pointing at another worktree's data. Writing the
// exception down is what makes walking safe.
type Memory interface {
	// Port reports a remembered port, and whether there was one.
	Port(slug, service string) (int, bool)

	// Remember records where an entry landed.
	Remember(slug, service string, port int) error
}

// noMemory forgets everything, which is what a registry with no record keeping
// does: every collision is resolved again from scratch.
type noMemory struct{}

func (noMemory) Port(string, string) (int, bool)    { return 0, false }
func (noMemory) Remember(string, string, int) error { return nil }

// FileMemory keeps the record in a JSON file. Only exceptions are written, so
// the file stays a short list of the entries whose port is not the one
// arithmetic gives, rather than a copy of every allocation.
type FileMemory struct {
	path string

	mu    sync.Mutex
	ports map[string]map[string]int
}

// OpenMemory reads an existing record, or starts an empty one. A file that
// cannot be parsed is started over rather than refused: every entry in it can
// be derived again by resolving the collisions once more, so a broken record
// costs a reshuffle and not a daemon. A file that cannot be read at all is a
// different thing, and says so.
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

// write replaces the file by rename, so a daemon that dies mid-write leaves the
// previous record rather than half of a new one.
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
