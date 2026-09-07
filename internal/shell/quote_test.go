package shell_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/shell"
)

// The case grove hits on every Mac: the default state directory has a space in
// it, so a command built without quoting silently becomes one with more
// arguments than it should have.
func TestQuoteSurvivesAPathWithASpace(t *testing.T) {
	path := "/Users/x/Library/Application Support/grove/pf/anchor"

	got := argsOf(t, shell.Quote(path)+" /etc/pf.anchors/grove")

	want := []string{path, "/etc/pf.anchors/grove"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("the shell saw %q, want %q", got, want)
	}
}

// Everything a path can hold that a shell would otherwise act on.
func TestQuoteHandsTheShellTheValueItWasGiven(t *testing.T) {
	for _, value := range []string{
		"plain",
		"with a space",
		"it's quoted",
		"$HOME",
		"`whoami`",
		"a;rm -rf b",
		"tab\there",
		`back\slash`,
		"*",
		"~",
	} {
		if got := argsOf(t, shell.Quote(value)); len(got) != 1 || got[0] != value {
			t.Errorf("%q came back as %q", value, got)
		}
	}
}

// The only authority on whether quoting worked is a shell, so this asks one and
// reports the arguments it produced.
func argsOf(t *testing.T, args string) []string {
	t.Helper()
	out, err := exec.Command("sh", "-c", `printf '%s\n' `+args).Output()
	if err != nil {
		t.Fatalf("printf %s: %v", args, err)
	}
	return strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
}
