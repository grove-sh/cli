package lease_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/lease"
)

func TestMemoryRoundTripsThroughAFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "ports.json")

	writer, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Remember("app1-feat1", "db", 20533); err != nil {
		t.Fatal(err)
	}

	reader, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	port, ok := reader.Port("app1-feat1", "db")
	if !ok || port != 20533 {
		t.Errorf("Port() = %d, %v", port, ok)
	}
	// An entry nobody wrote has no answer, which is what sends it to the hash.
	if _, ok := reader.Port("app1-feat1", "shadow"); ok {
		t.Error("an unrecorded entry came back with a port")
	}
}

// The record is derivable, so a file that cannot be parsed is worth starting
// over from rather than refusing to run a daemon for.
func TestMemoryStartsOverOnAFileItCannotParse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatalf("a broken record should not stop a daemon: %v", err)
	}
	if _, ok := m.Port("app1", "db"); ok {
		t.Error("something was read out of a file that does not parse")
	}
	if err := m.Remember("app1", "db", 20500); err != nil {
		t.Fatal(err)
	}
	if _, err := lease.OpenMemory(path); err != nil {
		t.Errorf("the file was not replaced with one that reads: %v", err)
	}
}

// Rewriting has to leave the previous record in place if it fails, so the file
// is replaced whole rather than edited.
func TestMemoryLeavesNoHalfWrittenFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")

	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{20500, 20501, 20502} {
		if err := m.Remember("app1", "db", port); err != nil {
			t.Fatal(err)
		}
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("temporary files were left behind: %v", entries)
	}
	reread, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if port, _ := reread.Port("app1", "db"); port != 20502 {
		t.Errorf("the file carries %d, not the last port written", port)
	}
}

func TestMemoryFindsAnEntryByItsPort(t *testing.T) {
	m, err := lease.OpenMemory(filepath.Join(t.TempDir(), "ports.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remember("app1", "db", 20040); err != nil {
		t.Fatal(err)
	}

	slug, service, ok := m.Owner(20040)
	if !ok || slug != "app1" || service != "db" {
		t.Errorf("Owner(20040) = %q, %q, %v", slug, service, ok)
	}
	if _, _, ok := m.Owner(20041); ok {
		t.Error("a port nobody holds came back with an owner")
	}
}

// The last entry of a context takes the context with it, so the file does not
// fill with empty objects for worktrees that are gone.
func TestMemoryForgetsThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remember("app1", "db", 20040); err != nil {
		t.Fatal(err)
	}
	if err := m.Forget("app1", "db"); err != nil {
		t.Fatal(err)
	}

	reread, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reread.Port("app1", "db"); ok {
		t.Error("the forgotten entry is still in the file")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "app1") {
		t.Errorf("the file still names the context:\n%s", body)
	}
}
