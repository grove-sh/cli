package service_test

import (
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
	// full readiness timeout on every restart.
	if !strings.Contains(unit, "TimeoutStartSec=") {
		t.Error("unit has no start timeout")
	}
}
