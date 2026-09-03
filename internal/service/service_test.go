package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/service"
)

func TestUnitDescribesTheDaemon(t *testing.T) {
	unit := service.Unit("/usr/local/bin/grove", "127.0.0.1:443")

	for _, want := range []string{
		"Type=notify",
		"ExecStart=/usr/local/bin/grove daemon --listen 127.0.0.1:443",
		"WantedBy=default.target",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("unit is missing %q:\n%s", want, unit)
		}
	}
	// Without this, a daemon that cannot bind leaves systemd waiting out its
	// default readiness timeout on every restart.
	if !strings.Contains(unit, "TimeoutStartSec=") {
		t.Error("unit has no start timeout")
	}
}

// GROVE_SERVICE_DIR writes a unit somewhere harmless and keeps this package
// away from the real service manager, which is what makes it safe to exercise.
func TestRedirectedDirectoryIsUnmanaged(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("GROVE_SERVICE_DIR", dir)

	if service.Managed() {
		t.Error("Managed() is true with the directory redirected")
	}
	path, err := service.Write("/bin/grove", "127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dir, service.UnitName) {
		t.Errorf("wrote to %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}

	for name, call := range map[string]func() error{
		"Enable": service.Enable, "Start": service.Start, "Restart": service.Restart,
		"EnableLingering": service.EnableLingering,
	} {
		if err := call(); err != nil {
			t.Errorf("%s reached the service manager: %v", name, err)
		}
	}
	if service.Lingering() {
		t.Error("Lingering() consulted the real user manager")
	}
}
