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
	if err := writer.Remember("app1-feat1", "db", "/src/app1-feat1", 20533); err != nil {
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
	if err := m.Remember("app1", "db", "/src/app1", 20500); err != nil {
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
		if err := m.Remember("app1", "db", "/src/app1", port); err != nil {
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
	if err := m.Remember("app1", "db", "/src/app1", 20040); err != nil {
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
	if err := m.Remember("app1", "db", "/src/app1", 20040); err != nil {
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

func TestMemoryRoundTripsTheWorktree(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	writer, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Remember("app1", "db", "/src/app1", 20040); err != nil {
		t.Fatal(err)
	}

	reader, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	records := reader.Records()
	if len(records) != 1 {
		t.Fatalf("records = %v, want one", records)
	}
	if records[0].Worktree != "/src/app1" {
		t.Errorf("worktree = %q, want the path it was written for", records[0].Worktree)
	}
}

// The key arrived after the file did, so every record already on disk has to
// keep its ports and simply report no path.
func TestMemoryReadsAFileWithNoWorktrees(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	if err := os.WriteFile(path, []byte(`{"ports":{"app1":{"db":20040}}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if port, ok := m.Port("app1", "db"); !ok || port != 20040 {
		t.Errorf("Port() = %d, %v; an older file lost its ports", port, ok)
	}
	if records := m.Records(); len(records) != 1 || records[0].Worktree != "" {
		t.Errorf("records = %v, want one with no path", records)
	}
}

// A worktree is deleted whole, so its entries go together rather than one
// command per port.
func TestMemoryForgetsAWholeContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range []string{"db", "api"} {
		if err := m.Remember("app1", service, "/src/app1", 20040+len(service)); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Remember("app2", "db", "/src/app2", 20099); err != nil {
		t.Fatal(err)
	}

	gone, err := m.ForgetContext("app1")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 2 {
		t.Errorf("gone = %v, want both of app1's entries", gone)
	}

	reread, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if records := reread.Records(); len(records) != 1 || records[0].Slug != "app2" {
		t.Errorf("records = %v, want app2 alone", records)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "/src/app1") {
		t.Errorf("the forgotten context left its path behind:\n%s", body)
	}
}

func TestMemoryForgettingAContextItDoesNotHoldIsNotAnError(t *testing.T) {
	m, err := lease.OpenMemory(filepath.Join(t.TempDir(), "ports.json"))
	if err != nil {
		t.Fatal(err)
	}
	gone, err := m.ForgetContext("app1")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Errorf("gone = %v, want nothing", gone)
	}
}

// A path is only ever written beside a port, so one on its own is a hand
// edit. Records and ForgetContext have to agree about it rather than one of
// them seeing a context the other does not.
func TestMemoryDropsAWorktreeWithNoPortsBehindIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ports.json")
	body := `{"ports":{"app1":{"db":20040}},"worktrees":{"app1":"/src/app1","ghost":"/src/ghost"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range m.Records() {
		if r.Slug == "ghost" {
			t.Errorf("a context with no ports came back from Records: %v", r)
		}
	}
	gone, err := m.ForgetContext("ghost")
	if err != nil {
		t.Fatal(err)
	}
	if len(gone) != 0 {
		t.Errorf("ForgetContext found %v for a context Records does not hold", gone)
	}
}

// A directory nothing can be created in, which is how a write fails without
// the file itself being unreadable.
func sealed(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dir, 0o700) })
}

// The maps are a reading of the file, so a failed write must not leave the
// daemon answering with something the file does not carry.
func TestMemoryKeepsTheRecordWhenAWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remember("app1", "db", "/src/app1", 20040); err != nil {
		t.Fatal(err)
	}
	sealed(t, dir)

	if err := m.Remember("app1", "db", "/src/moved", 20099); err == nil {
		t.Fatal("a write into a sealed directory succeeded")
	}

	port, ok := m.Port("app1", "db")
	if !ok || port != 20040 {
		t.Errorf("Port() = %d, %v; memory moved on without the file", port, ok)
	}
	if records := m.Records(); len(records) != 1 || records[0].Worktree != "/src/app1" {
		t.Errorf("records = %v, want the path the file still carries", records)
	}
}

// ForgetContext reports what went, so a failed write has to report nothing
// rather than name records still on disk.
func TestMemoryForgetContextKeepsTheRecordWhenAWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remember("app1", "db", "/src/app1", 20040); err != nil {
		t.Fatal(err)
	}
	sealed(t, dir)

	gone, err := m.ForgetContext("app1")
	if err == nil {
		t.Fatal("a write into a sealed directory succeeded")
	}
	if len(gone) != 0 {
		t.Errorf("gone = %v, and names records that are still in the file", gone)
	}
	if _, ok := m.Port("app1", "db"); !ok {
		t.Error("the context went from memory while the file still holds it")
	}
}

func TestMemoryForgetKeepsTheRecordWhenAWriteFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.json")
	m, err := lease.OpenMemory(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Remember("app1", "db", "/src/app1", 20040); err != nil {
		t.Fatal(err)
	}
	sealed(t, dir)

	if err := m.Forget("app1", "db"); err == nil {
		t.Fatal("a write into a sealed directory succeeded")
	}
	if _, ok := m.Port("app1", "db"); !ok {
		t.Error("the entry went from memory while the file still holds it")
	}
	if _, _, ok := m.Owner(20040); !ok {
		t.Error("the port lost its owner in memory while the file still names one")
	}
}
