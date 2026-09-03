package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grove-sh/cli/internal/service"
)

// The whole point is that the unit stops depending on where the binary came
// from, so the copy has to be a real file grove owns, and runnable.
func TestAnchorCopiesTheBinarySomewhereItOwns(t *testing.T) {
	source := filepath.Join(t.TempDir(), "grove")
	if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "bin")

	target, err := service.Anchor(source, dir)
	if err != nil {
		t.Fatal(err)
	}

	if target != filepath.Join(dir, "grove") {
		t.Errorf("target = %q", target)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the copy is not executable: %v", info.Mode())
	}
	copied, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(copied) != "#!/bin/sh\nexit 0\n" {
		t.Errorf("the copy is not the binary: %q", copied)
	}
}

// Upgrading means anchoring over a copy a daemon may be running from. The
// rename leaves that process on the inode it started with, so the file it is
// executing never changes underneath it.
func TestAnchorReplacesAnOlderCopyWithoutTouchingIt(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "grove")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	running, err := os.Open(target)
	if err != nil {
		t.Fatal(err)
	}
	defer running.Close()

	source := filepath.Join(t.TempDir(), "grove")
	if err := os.WriteFile(source, []byte("new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Anchor(source, dir); err != nil {
		t.Fatal(err)
	}

	if now, _ := os.ReadFile(target); string(now) != "new" {
		t.Errorf("the path still serves %q", now)
	}
	// What the open handle sees is what a running daemon executes.
	held := make([]byte, 3)
	if _, err := running.ReadAt(held, 0); err != nil {
		t.Fatal(err)
	}
	if string(held) != "old" {
		t.Errorf("the running copy changed underneath it: %q", held)
	}
}

// Re-running install from the anchored binary would otherwise copy a file over
// itself, which truncates it.
func TestAnchorLeavesTheBinaryAloneWhenItIsAlreadyTheCopy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "grove")
	if err := os.WriteFile(target, []byte("grove"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Anchor(target, dir); err != nil {
		t.Fatal(err)
	}

	if now, _ := os.ReadFile(target); string(now) != "grove" {
		t.Errorf("anchoring onto itself left %q", now)
	}
}

// A failure has to leave the previous copy serving rather than a half written
// one, and must not litter the directory with the attempt.
func TestAnchorKeepsTheOldCopyWhenTheSourceIsGone(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "grove")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, err := service.Anchor(filepath.Join(t.TempDir(), "missing"), dir); err == nil {
		t.Fatal("anchoring a binary that is not there should fail")
	}

	if now, _ := os.ReadFile(target); string(now) != "old" {
		t.Errorf("the old copy is gone: %q", now)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the failed attempt was left behind: %v", entries)
	}
}
