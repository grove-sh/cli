package shell_test

import (
	"testing"

	"github.com/grove-sh/cli/internal/shell"
)

// The printed line is what a reader pastes, so the shell has to hand back the
// arguments grove would have run directly.
func TestStepPrintsWhatItWouldRun(t *testing.T) {
	step := shell.Step{"cp", "/Users/x/Library/Application Support/grove/pf/anchor", "/etc/pf.anchors/grove"}

	got := argsOf(t, step.String())

	if len(got) != len(step) {
		t.Fatalf("the shell saw %d words, want %d: %q", len(got), len(step), got)
	}
	for i := range step {
		if got[i] != step[i] {
			t.Errorf("word %d: shell saw %q, want %q", i, got[i], step[i])
		}
	}
}

// Quoting every word would be safe and unreadable: the lines are printed for a
// person first.
func TestStepLeavesPlainWordsAlone(t *testing.T) {
	step := shell.Step{"sysctl", "-w", "net.ipv4.ip_unprivileged_port_start=1024"}
	if got := step.String(); got != "sysctl -w net.ipv4.ip_unprivileged_port_start=1024" {
		t.Errorf("String() = %q", got)
	}
}

func TestStepQuotesWhatAShellWouldExpand(t *testing.T) {
	for _, word := range []string{"$HOME", "a b", "it's", "*", "", "~"} {
		got := argsOf(t, shell.Step{"printf", word}.String())
		if len(got) != 2 || got[1] != word {
			t.Errorf("%q came back as %q", word, got)
		}
	}
}
