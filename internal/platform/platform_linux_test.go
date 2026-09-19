package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The floor on the machine running this is whatever it is, so the plan is
// tested below the check that decides whether to offer it.
func TestLowerFloorStagesTheFileItInstalls(t *testing.T) {
	dir := t.TempDir()

	plan, err := lowerFloor(dir)
	if err != nil {
		t.Fatal(err)
	}

	staged := filepath.Join(dir, "sysctl", "60-grove.conf")
	body, err := os.ReadFile(staged)
	if err != nil {
		t.Fatalf("nothing staged: %v", err)
	}
	if string(body) != "net.ipv4.ip_unprivileged_port_start=80\n" {
		t.Errorf("staged %q", body)
	}

	if len(plan.Steps) != 2 {
		t.Fatalf("%d steps, want 2", len(plan.Steps))
	}
	if got := plan.Steps[0].String(); got != "cp "+staged+" "+sysctlFile {
		t.Errorf("first step = %q", got)
	}
	// Reloads the one file rather than every sysctl on the machine, so the
	// output is one line and nothing else is touched.
	if got := plan.Steps[1].String(); got != "sysctl -p "+sysctlFile {
		t.Errorf("second step = %q", got)
	}
	if plan.Summary == "" || plan.Intro == "" || !strings.Contains(plan.Outro, "80 rather than 443") {
		t.Errorf("plan is missing its prose: %+v", plan)
	}
}
