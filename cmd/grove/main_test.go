package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	for _, arg := range []string{"-v", "-version"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{arg}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s: exit code = %d, want 0", arg, code)
		}
		if got := stdout.String(); !strings.HasPrefix(got, "grove ") {
			t.Errorf("%s: stdout = %q, want a \"grove \" prefix", arg, got)
		}
		if stderr.Len() != 0 {
			t.Errorf("%s: stderr = %q, want empty", arg, stderr.String())
		}
	}
}

func TestRunUnknownFlag(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-nope"}, &stdout, &stderr); code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "not defined") {
		t.Errorf("stderr = %q, want it to explain the bad flag", stderr.String())
	}
}
