package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvReportsWhatIsLeased(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(tempRepo(t, "app1"))
	if code, _, stderr := exercise(t, "sync", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	code, stdout, stderr := exercise(t, "env", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	if !strings.Contains(stdout, "export DB_PORT=") {
		t.Errorf("stdout does not carry the detached port:\n%s", stdout)
	}
	// DATABASE_URL names that port, so it resolves without anything running.
	if !strings.Contains(stdout, "export DATABASE_URL=") {
		t.Errorf("stdout does not carry the composed URL:\n%s", stdout)
	}
}

// Reporting is not allocating, so a variable naming a port nobody holds is
// left out, and said out loud rather than quietly dropped.
func TestEnvSkipsWhatIsNotLeasedAndSaysSo(t *testing.T) {
	t.Chdir(tempRepo(t, "app1"))

	code, stdout, stderr := exercise(t, "env", "--socket", "/nonexistent/grove.sock")

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "DB_PORT") {
		t.Errorf("reported a port nothing holds:\n%s", stdout)
	}
	if !strings.Contains(stderr, "DB_PORT is not set") {
		t.Errorf("stderr does not name what was left out:\n%s", stderr)
	}
}

func TestEnvShellFormatSurvivesEval(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	writeConfig(t, repo, `
[env]
QUOTED = "it's fine"
PLAIN = "simple"
`)
	t.Chdir(repo)

	_, stdout, _ := exercise(t, "env", "--socket", socket)

	// Single quotes take everything literally, so only a quote in the value
	// needs escaping.
	if !strings.Contains(stdout, `export QUOTED='it'\''s fine'`) {
		t.Errorf("a quote in the value was not escaped:\n%s", stdout)
	}
	if !strings.Contains(stdout, "export PLAIN='simple'") {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestEnvJSON(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	writeConfig(t, repo, "[env]\nONE = \"1\"\n")
	t.Chdir(repo)

	_, stdout, _ := exercise(t, "env", "--socket", socket, "--format", "json")

	var got map[string]string
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if got["ONE"] != "1" {
		t.Errorf("got %v", got)
	}
}

func TestEnvRejectsAnUnknownFormat(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(tempRepo(t, "app1"))

	code, _, stderr := exercise(t, "env", "--socket", socket, "--format", "yaml")

	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "shell, dotenv, or json") {
		t.Errorf("stderr does not name the choices: %q", stderr)
	}
}
