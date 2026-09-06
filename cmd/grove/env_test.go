package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvReportsWhatIsLeased(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(tempRepo(t, "app1"))
	if code, _, stderr := exercise(t, "hold", "--socket", socket); code != 0 {
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
	if !strings.Contains(stderr, "waiting on ports nothing holds yet") {
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
	if !strings.Contains(stderr, "shell or json") {
		t.Errorf("stderr does not name the choices: %q", stderr)
	}
}

// A hostname is static per context, so a command that binds nothing can still
// name a route's URL. Only ports need an allocation. Without this, a build at
// the repository root cannot know its own public URL.
func TestRouteURLsResolveWithoutALease(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	writeConfig(t, repo, `
[routes.web]
dir = "apps/web"
label = ""
env = { PORT = "{port}" }

[env]
SITE_URL = "{web.url}"
SITE_HOST = "{web.host}"
`)
	t.Chdir(repo)

	code, stdout, stderr := exercise(t, "env", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	if !strings.Contains(stdout, "export SITE_URL='https://app1."+defaultDomain+"'") {
		t.Errorf("no URL for an unbound route:\n%s", stdout)
	}
	if !strings.Contains(stdout, "export SITE_HOST='app1."+defaultDomain+"'") {
		t.Errorf("no hostname for an unbound route:\n%s", stdout)
	}
	// The port still needs a lease, and nothing is holding one.
	if strings.Contains(stdout, "export PORT=") {
		t.Errorf("a port appeared without an allocation:\n%s", stdout)
	}
}

// Grove's own variables describe a lease, so with nothing holding one they are
// absent. Saying which, and why, is what stops grove env and grove exec looking
// like they disagree for no reason.
func TestEnvNamesItsOwnVariablesWhenNothingIsBound(t *testing.T) {
	t.Chdir(tempRepo(t, "app1"))

	code, stdout, stderr := exercise(t, "env", "--socket", "/nonexistent/grove.sock")

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	for _, name := range []string{"GROVE_PORT", "GROVE_HOST", "GROVE_URL"} {
		if !strings.Contains(stderr, name) {
			t.Errorf("stderr does not account for %s:\n%s", name, stderr)
		}
		if strings.Contains(stdout, name+"=") {
			t.Errorf("%s was exported with nothing holding a lease:\n%s", name, stdout)
		}
	}
	// One line for the entry they all wait on, not one per variable.
	if got := strings.Count(stderr, "only allocated while"); got != 0 {
		t.Errorf("the reason is repeated %d times:\n%s", got, stderr)
	}
	// The context exists whether or not anything is bound, so it is not a miss.
	if !strings.Contains(stdout, "export GROVE_CONTEXT=") {
		t.Errorf("GROVE_CONTEXT is missing:\n%s", stdout)
	}
	if strings.Contains(stderr, "GROVE_CONTEXT") {
		t.Errorf("GROVE_CONTEXT was reported as missing:\n%s", stderr)
	}
}

// With a lease held there is nothing to account for, and repeating the
// explanation would be noise on the ordinary path.
func TestEnvIsQuietAboutItsOwnVariablesWhenBound(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	writeConfig(t, repo, "[routes.web]\ndetached = true\nenv.PORT = \"{port}\"\n")
	t.Chdir(repo)
	if code, _, stderr := exercise(t, "hold", "--socket", socket); code != 0 {
		t.Fatal(stderr)
	}

	_, stdout, stderr := exercise(t, "env", "--socket", socket, "-s", "web")

	if !strings.Contains(stdout, "export GROVE_PORT=") {
		t.Errorf("GROVE_PORT is missing with a lease held:\n%s", stdout)
	}
	if strings.Contains(stderr, "GROVE_PORT") {
		t.Errorf("stderr explains an absence that is not one:\n%s", stderr)
	}
}

// A format that cannot quote hands a tool a broken file the moment a value has
// a space in it, and grove replaces the dotenv workflow rather than feeding it.
func TestEnvNoLongerWritesDotenv(t *testing.T) {
	t.Chdir(tempRepo(t, "app1"))

	code, _, stderr := exercise(t, "env", "--socket", "/nonexistent/grove.sock", "--format", "dotenv")

	if code != 2 {
		t.Errorf("exit = %d, want a usage error", code)
	}
	if !strings.Contains(stderr, "shell or json") {
		t.Errorf("stderr does not name the formats that remain: %s", stderr)
	}
}

// A captured or piped stream gets plain text, since these commands are eval'd
// and read by scripts, and an escape sequence there is corruption.
func TestStylesLeaveAnythingButATerminalAlone(t *testing.T) {
	name, detail := styles(&bytes.Buffer{})

	if got := name("GROVE_PORT"); got != "GROVE_PORT" {
		t.Errorf("name = %q, want it untouched", got)
	}
	if got := detail("because"); got != "because" {
		t.Errorf("detail = %q, want it untouched", got)
	}
}

// An entry that exists has a name whether or not it holds a port, and calling
// a reference to it a typo sends someone looking for a mistake in their file.
func TestAnUnheldPortIsNotReportedAsMissing(t *testing.T) {
	repo := tempRepo(t, "app1")
	writeConfig(t, repo, "[env]\nURL = \"postgres://localhost:{db.port}/app\"\n\n[ports.db]\ndetached = true\n")
	t.Chdir(repo)

	_, _, stderr := exercise(t, "env", "--socket", "/nonexistent/grove.sock")

	if strings.Contains(stderr, "names no entry") {
		t.Errorf("an entry that is right there in the file was called missing:\n%s", stderr)
	}
	if !strings.Contains(stderr, "ports.db") {
		t.Errorf("stderr does not name the entry being waited on:\n%s", stderr)
	}
}
