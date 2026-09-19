package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/platform"
	"github.com/grove-sh/cli/internal/shell"
)

// Runs the step as written, so a test can watch the runner without sudo.
func bare(step shell.Step) *exec.Cmd { return exec.Command(step[0], step[1:]...) }

func plan(steps ...shell.Step) platform.Plan {
	return platform.Plan{Intro: "Two lines:", Steps: steps, Outro: "Read them first."}
}

// A buffer is not a terminal, which is also every test's situation: the plan
// has to come out in full and nothing may run.
func TestOfferOnlyPrintsWhenNothingCanAnswer(t *testing.T) {
	var out bytes.Buffer
	e := elevation{in: strings.NewReader("y\n"), out: &out, err: &out, elevate: bare}

	applied, err := e.offer(plan(shell.Step{"false"}))
	if err != nil || applied {
		t.Fatalf("applied = %v, err = %v", applied, err)
	}

	for _, want := range []string{"Two lines:", "\n  sudo false\n", "Read them first.", "--yes"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestOfferRunsWithYes(t *testing.T) {
	var out bytes.Buffer
	e := elevation{yes: true, in: strings.NewReader(""), out: &out, err: &out, elevate: bare}

	applied, err := e.offer(plan(shell.Step{"true"}, shell.Step{"echo", "ran"}))
	if err != nil || !applied {
		t.Fatalf("applied = %v, err = %v", applied, err)
	}
	if !strings.Contains(out.String(), "ran\n") {
		t.Errorf("the second step's output is missing:\n%s", out.String())
	}
}

// The steps are ordered so every prefix leaves the machine loadable, and the
// reader finishing by hand needs the rest, not the start.
func TestApplyStopsAtTheFirstFailureAndSaysWhatIsLeft(t *testing.T) {
	var out bytes.Buffer
	e := elevation{yes: true, in: strings.NewReader(""), out: &out, err: &out, elevate: bare}

	_, err := e.offer(plan(
		shell.Step{"true"},
		shell.Step{"sh", "-c", "exit 3"},
		shell.Step{"echo", "never"},
		shell.Step{"echo", "reached"},
	))
	if err == nil {
		t.Fatal("no error from a failing step")
	}

	said := err.Error()
	if !strings.HasPrefix(said, "sudo sh -c 'exit 3' failed: exit status 3") {
		t.Errorf("error does not name the step and its status: %q", said)
	}
	if !strings.Contains(said, "Still to run, in this order:\n\n  sudo echo never\n  sudo echo reached") {
		t.Errorf("error does not list the remainder in order: %q", said)
	}
	// Once, in the printed plan. A second time would be the step's own echo.
	if n := strings.Count(out.String(), "never"); n != 1 {
		t.Errorf("a step after the failure ran:\n%s", out.String())
	}
}

func TestOfferSaysSoWhenThereIsNoSudo(t *testing.T) {
	var out bytes.Buffer
	e := elevation{yes: true, in: strings.NewReader(""), out: &out, err: &out, elevate: nil}

	applied, err := e.offer(plan(shell.Step{"true"}))
	if err != nil || applied {
		t.Fatalf("applied = %v, err = %v", applied, err)
	}
	if !strings.Contains(out.String(), "sudo is not on PATH") {
		t.Errorf("output does not explain why nothing ran:\n%s", out.String())
	}
}

// Anything but a yes is a no, including the enter key alone.
func TestConsentDefaultsToNo(t *testing.T) {
	for answer, want := range map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true,
		"\n": false, "n\n": false, "no\n": false, "": false, "yeah\n": false,
	} {
		var out bytes.Buffer
		e := elevation{in: strings.NewReader(answer), out: &out}
		if got := e.consent(styles(&out)); got != want {
			t.Errorf("consent(%q) = %v, want %v", answer, got, want)
		}
		if !strings.Contains(out.String(), "[y/N]") {
			t.Errorf("the prompt does not show the default:\n%s", out.String())
		}
	}
}

func TestOfferHasNothingToSayForAnEmptyPlan(t *testing.T) {
	var out bytes.Buffer
	e := elevation{yes: true, out: &out, err: &out, elevate: bare}

	applied, err := e.offer(platform.Plan{})
	if err != nil || applied || out.Len() != 0 {
		t.Errorf("applied = %v, err = %v, output = %q", applied, err, out.String())
	}
}
