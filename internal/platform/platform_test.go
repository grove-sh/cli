package platform_test

import (
	"testing"

	"github.com/grove-sh/cli/internal/platform"
)

// Whatever the platform, the answer has to be usable: it says what the
// situation is, and when grove cannot have the port it says what to do about
// it. Advice that only exists on one operating system is how the caller ends
// up printing nothing useful on the others.
func TestPrivilegedPortsAlwaysExplainsItself(t *testing.T) {
	access := platform.PrivilegedPorts()

	if access.Detail == "" {
		t.Error("Detail is empty")
	}
	if !access.Allowed && access.Advice == "" {
		t.Error("no advice, and grove cannot bind the port")
	}
}
