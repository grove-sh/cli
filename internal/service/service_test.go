package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/grove-sh/cli/internal/service"
)

// GROVE_SERVICE_DIR writes the definition somewhere harmless and keeps this
// package away from the real service manager, which is what makes it safe for
// a test run to exercise the install path at all.
func TestRedirectedDirectoryIsUnmanaged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROVE_SERVICE_DIR", dir)

	if service.Managed() {
		t.Error("Managed() is true with the directory redirected")
	}
	if got := service.Dir(); got != dir {
		t.Errorf("Dir() = %q, want %q", got, dir)
	}
	if want := filepath.Join(dir, service.FileName); service.Path() != want {
		t.Errorf("Path() = %q, want %q", service.Path(), want)
	}
	if service.Lingering() {
		t.Error("Lingering() consulted the real user manager")
	}
	if err := service.EnableLingering(); err != nil {
		t.Errorf("EnableLingering reached the real user manager: %v", err)
	}
}

func TestStatusReportsWhereTheDefinitionBelongs(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROVE_SERVICE_DIR", dir)

	state := service.Status()

	if state.Installed {
		t.Error("Installed is true with nothing written")
	}
	if state.Path != filepath.Join(dir, service.FileName) {
		t.Errorf("Path = %q", state.Path)
	}

	if _, err := service.Install("/bin/grove", "127.0.0.1:8443"); err != nil {
		t.Skipf("no service definition on this platform: %v", err)
	}
	if !service.Status().Installed {
		t.Error("Installed is false after Install wrote one")
	}
	if _, err := os.Stat(state.Path); err != nil {
		t.Error(err)
	}
}
