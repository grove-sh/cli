package main

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
)

func startDaemon(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	if _, err := ca.OpenOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	server, err := daemon.New(daemon.Config{Domain: defaultDomain, CADir: dir})
	if err != nil {
		t.Fatal(err)
	}
	control, err := daemon.Listen(filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	https, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	served := make(chan error, 1)
	go func() { served <- server.Serve(control, https) }()
	t.Cleanup(func() {
		server.Shutdown()
		if err := <-served; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return control.Addr().String()
}

// A single app on the context's own hostname, plus a detached port standing in
// for a container stack.
const repoConfig = `
env_files = [".env"]

[routes.web]
dir = "."
label = ""
env = { PORT = "{port}", SITE_URL = "{url}" }

[ports.db]
detached = true
env = { DB_PORT = "{port}" }

[env]
DATABASE_URL = "postgres://127.0.0.1:{ports.db}/app"
`

func tempRepo(t *testing.T, name string) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "grove.toml"), []byte(repoConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestExecSetsTheContextEnvironment(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--",
		"sh", "-c", `printf '%s\n%s\n%s\n%s\n' "$PORT" "$GROVE_PORT" "$SITE_URL" "$GROVE_URL" > env.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "env.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if len(lines) != 4 {
		t.Fatalf("child saw %d values: %q", len(lines), written)
	}
	port, grovePort, siteURL, url := lines[0], lines[1], lines[2], lines[3]

	if port != grovePort {
		t.Errorf("PORT = %q but GROVE_PORT = %q", port, grovePort)
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		t.Fatalf("PORT = %q: %v", port, err)
	}
	if n < 20000 || n > 20999 {
		t.Errorf("PORT = %d, outside the default range", n)
	}
	if url != "https://app1."+defaultDomain {
		t.Errorf("GROVE_URL = %q", url)
	}
	if siteURL != url {
		t.Errorf("SITE_URL = %q, want the route's own URL %q", siteURL, url)
	}
}

func TestExecNeedsAConfig(t *testing.T) {
	socket := startDaemon(t)
	bare := t.TempDir()
	t.Chdir(bare)

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--", "true")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "grove.toml") {
		t.Errorf("stderr does not name the missing file: %q", stderr)
	}
}

// A command that binds nothing still needs the detached ports, since that is
// where a database URL comes from.
func TestExecInjectsDetachedPortsWithNothingBound(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	deep := filepath.Join(repo, "packages", "db")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(deep)

	code, _, stderr := exercise(t, "exec", "--socket", socket, "-s", "db", "--",
		"sh", "-c", `printf '%s\n%s\n' "$DB_PORT" "$DATABASE_URL" > out.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(deep, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if lines[0] == "" {
		t.Fatal("DB_PORT was not set")
	}
	if want := "postgres://127.0.0.1:" + lines[0] + "/app"; lines[1] != want {
		t.Errorf("DATABASE_URL = %q, want %q", lines[1], want)
	}
}

// A stale port hand copied into .env is the drift grove exists to remove, so
// grove's own values win over the file.
func TestGroveOverridesEnvFiles(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("PORT=3000\nAPI_KEY=secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--",
		"sh", "-c", `printf '%s\n%s\n' "$PORT" "$API_KEY" > out.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if lines[0] == "3000" {
		t.Error("the file's PORT won over the leased one")
	}
	if lines[1] != "secret" {
		t.Errorf("API_KEY = %q, want the file's value to survive", lines[1])
	}
}

// A failing child has already explained itself, so grove adds nothing.
func TestExecPropagatesTheExitCode(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(tempRepo(t, "app1"))

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--", "sh", "-c", "exit 7")

	if code != 7 {
		t.Errorf("exit = %d, want 7", code)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestExecReportsAMissingCommand(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(tempRepo(t, "app1"))

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--", "grove-no-such-binary")

	if code != 127 {
		t.Errorf("exit = %d, want 127", code)
	}
	if !strings.Contains(stderr, "grove-no-such-binary") {
		t.Errorf("stderr does not name the command: %q", stderr)
	}
}

func TestExecWithoutADaemon(t *testing.T) {
	t.Chdir(tempRepo(t, "app1"))

	code, _, stderr := exercise(t, "exec", "--socket", filepath.Join(t.TempDir(), "absent.sock"), "--", "true")

	if code != 1 {
		t.Errorf("exit = %d, want 1", code)
	}
	if !strings.Contains(stderr, "grove daemon") {
		t.Errorf("stderr does not say how to start one: %q", stderr)
	}
}

// The lease has to outlive the acquire call and expire with the child, which is
// only visible from outside the process holding it.
func TestLsSeesTheContextWhileExecRuns(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	ready := filepath.Join(repo, "ready")
	stop := filepath.Join(repo, "stop")
	finished := make(chan int, 1)
	go func() {
		code, _, _ := exercise(t, "exec", "--socket", socket, "--", "sh", "-c",
			"touch "+ready+"; while [ ! -f "+stop+" ]; do sleep 0.05; done")
		finished <- code
	}()

	waitForFile(t, ready)

	_, listed, _ := exercise(t, "ls", "--socket", socket)
	if !strings.Contains(listed, "app1."+defaultDomain) {
		t.Errorf("ls does not show the running context:\n%s", listed)
	}

	if err := os.WriteFile(stop, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := <-finished; code != 0 {
		t.Errorf("exec exited %d", code)
	}

	// The route goes with the command. The detached port does not, since
	// whatever binds it is still running.
	_, listed, _ = exercise(t, "ls", "--socket", socket)
	if strings.Contains(listed, "app1."+defaultDomain) {
		t.Errorf("the route outlived the command:\n%s", listed)
	}
	if !strings.Contains(listed, "app1:db") {
		t.Errorf("the detached port was released with the command:\n%s", listed)
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}
