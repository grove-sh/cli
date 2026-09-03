package main

import (
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/daemon"
)

// socketDir is a short directory, because t.TempDir on macOS returns a
// /var/folders path long enough on its own to blow the unix socket path limit.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "grove")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

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
	control, err := daemon.Listen(filepath.Join(socketDir(t), "control.sock"))
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

// writeConfig replaces the repository's grove.toml, for a test that cares
// about something the shared fixture does not cover.
func writeConfig(t *testing.T, repo, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(repo, "grove.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

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
	// This suite runs on a build server too, where a missing daemon is
	// tolerated rather than fatal.
	t.Setenv("CI", "")

	code, _, stderr := exercise(t, "exec", "--autostart=false",
		"--socket", filepath.Join(socketDir(t), "absent.sock"), "--", "true")

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

	if _, listed, _ := exercise(t, "ls", "--socket", socket); !strings.Contains(listed, "running") {
		t.Errorf("ls does not show the route as running:\n%s", listed)
	}

	if err := os.WriteFile(stop, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if code := <-finished; code != 0 {
		t.Errorf("exec exited %d", code)
	}

	// The route's lease goes with the command, so it reads idle again. The
	// detached port does not, since whatever binds it is still running.
	_, listed, _ := exercise(t, "ls", "--socket", socket)
	if !strings.Contains(listed, "idle") {
		t.Errorf("the route outlived the command:\n%s", listed)
	}
	if !strings.Contains(listed, "detached") {
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

// A restarted daemon knows nothing, so the context has to be able to say it
// exists without running a command.
func TestSyncRestoresDetachedEntries(t *testing.T) {
	socket := startDaemon(t)
	t.Chdir(tempRepo(t, "app1"))

	code, stdout, stderr := exercise(t, "sync", "--socket", socket)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "app1:db") {
		t.Errorf("sync did not report the detached port:\n%s", stdout)
	}

	_, listed, _ := exercise(t, "ls", "--socket", socket)
	for _, line := range strings.Split(listed, "\n") {
		switch {
		case strings.HasPrefix(line, "db") && !strings.Contains(line, "detached"):
			t.Errorf("the daemon does not hold the detached port:\n%s", listed)
		// An attached route needs a running command to mean anything, so sync
		// must leave it alone.
		case strings.HasPrefix(line, "web") && !strings.Contains(line, "idle"):
			t.Errorf("sync restored an attached route:\n%s", listed)
		}
	}
}

func TestSyncSaysSoWhenThereIsNothingToDo(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	if err := os.WriteFile(filepath.Join(repo, "grove.toml"), []byte("[routes.web]\ndir = \".\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)

	code, stdout, _ := exercise(t, "sync", "--socket", socket)

	if code != 0 {
		t.Errorf("exit = %d, want 0", code)
	}
	if !strings.Contains(stdout, "no detached ports") {
		t.Errorf("stdout = %q", stdout)
	}
}

// Nothing is running, so exec has to bring a daemon up before it can lease a
// port, and clean up after itself is the caller's job, not grove's.
func TestExecStartsADaemonWhenNoneIsRunning(t *testing.T) {
	state := t.TempDir()
	socket := filepath.Join(socketDir(t), "control.sock")
	t.Setenv("GROVE_STATE_DIR", state)
	t.Setenv("GROVE_SOCKET", socket)
	t.Setenv("GROVE_LISTEN", "127.0.0.1:0")
	t.Setenv("GROVE_TEST_RUN_CLI", "1")
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(state, "units"))
	// Autostart is the non-CI path: with CI set, a missing daemon is stepped
	// around rather than started.
	t.Setenv("CI", "")
	if _, err := ca.OpenOrCreate(state); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if client, err := daemon.Dial(socket); err == nil {
			client.Stop()
			client.Close()
		}
	})

	repo := tempRepo(t, "app1")
	t.Chdir(repo)

	code, _, stderr := exercise(t, "exec", "--", "sh", "-c", "echo $PORT > port.txt")
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "port.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(written)) == "" {
		t.Error("the child got no port, so the daemon never came up")
	}
	if _, err := daemon.Dial(socket); err != nil {
		t.Errorf("the daemon did not stay up: %v", err)
	}
}

// An inline override is the most deliberate thing in the chain, so a
// checked-in .env must not clobber it. dotenv behaves this way too, and
// swapping one for the other should not change what `PORT=4000 pnpm dev` does.
func TestEnvFilesYieldToTheInvokingEnvironment(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("API_KEY=from-file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(repo)
	t.Setenv("API_KEY", "from-shell")

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--",
		"sh", "-c", `printf '%s\n%s\n' "$API_KEY" "$PORT" > out.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if lines[0] != "from-shell" {
		t.Errorf("API_KEY = %q, want the inline value to survive", lines[0])
	}
	// The leased port still wins, since the hostname routes to it.
	if lines[1] == "" || lines[1] == "3000" {
		t.Errorf("PORT = %q, want the leased one", lines[1])
	}
}

// And the inverse: grove's own values override an inline attempt to name the
// port, because the route already points at the one it leased.
func TestGroveOverridesAnInlinePort(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	t.Setenv("PORT", "4000")

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--",
		"sh", "-c", `echo "$PORT" > port.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "port.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(written)) == "4000" {
		t.Error("an inline PORT won, so the hostname would route to the wrong port")
	}
}

// A build service is the authority on its own environment: grove is not
// running there, and tests and migrations need the POSTGRES_URL that CI set.
// So with nothing to talk to, the command runs untouched rather than failing.
func TestUnderCIWithNoDaemonTheEnvironmentIsUntouched(t *testing.T) {
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	t.Setenv("CI", "true")
	t.Setenv("POSTGRES_URL", "postgres://ci-supplied/db")

	code, _, stderr := exercise(t, "exec", "--socket", filepath.Join(socketDir(t), "absent.sock"), "--",
		"sh", "-c", `printf '%s\n%s\n' "$POSTGRES_URL" "${DB_PORT:-unset}" > out.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(written)), "\n")
	if lines[0] != "postgres://ci-supplied/db" {
		t.Errorf("POSTGRES_URL = %q, want the value CI set", lines[0])
	}
	// Nothing at all from grove, rather than half an answer: an injected
	// {ports.db} would name a port that does not exist on a build server.
	if lines[1] != "unset" {
		t.Errorf("DB_PORT = %q, want nothing", lines[1])
	}
	if !strings.Contains(stderr, "as it stands") {
		t.Errorf("stderr does not say what happened: %q", stderr)
	}
}

// The trigger is CI plus no daemon, not CI alone: a daemon deliberately
// running on a build server gets used like any other.
func TestUnderCIWithADaemonGroveStillApplies(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	t.Setenv("CI", "true")

	code, _, stderr := exercise(t, "exec", "--socket", socket, "--",
		"sh", "-c", `echo "$DB_PORT" > out.txt`)
	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}

	written, err := os.ReadFile(filepath.Join(repo, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(written)) == "" {
		t.Error("grove did nothing, though a daemon was running")
	}
}

func TestIfAvailableToleratesAMissingDaemonAnywhere(t *testing.T) {
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	t.Setenv("CI", "")

	code, _, stderr := exercise(t, "exec", "--if-available",
		"--socket", filepath.Join(socketDir(t), "absent.sock"), "--", "true")

	if code != 0 {
		t.Fatalf("exit = %d: %s", code, stderr)
	}
}

// Asking twice means it. A child that hangs mid shutdown would otherwise hold
// its lease for as long as it stays alive, and since grove relays rather than
// exits, signalling grove could not break that either.
func TestSecondInterruptKillsAChildThatWillNotStop(t *testing.T) {
	socket := startDaemon(t)
	repo := tempRepo(t, "app1")
	t.Chdir(repo)
	t.Setenv("CI", "")

	// Keep the default action from killing the test process if a signal lands
	// before exec has registered its own handler.
	guard := make(chan os.Signal, 4)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	ready := filepath.Join(repo, "ready")
	finished := make(chan int, 1)
	go func() {
		code, _, _ := exercise(t, "exec", "--socket", socket, "--", "sh", "-c",
			"trap '' INT TERM; touch "+ready+"; while true; do sleep 0.05; done")
		finished <- code
	}()
	waitForFile(t, ready)

	self, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	self.Signal(syscall.SIGINT)
	time.Sleep(300 * time.Millisecond)
	self.Signal(syscall.SIGINT)

	select {
	case code := <-finished:
		// 128 plus SIGKILL, since the child had to be taken out.
		if code != 137 {
			t.Errorf("exit = %d, want 137", code)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("exec never returned, so the lease is still held")
	}

	_, listed, _ := exercise(t, "ls", "--socket", socket)
	for _, line := range strings.Split(listed, "\n") {
		if strings.HasPrefix(line, "web") && !strings.Contains(line, "idle") {
			t.Errorf("the lease survived the kill:\n%s", listed)
		}
	}
}
