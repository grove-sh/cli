package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/grove-sh/cli/internal/daemon"
)

const (
	FileName = "sh.grove.daemon.plist"
	label    = "sh.grove.daemon"
)

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}

// launchd is always there, which is the one thing macOS makes easy here.
func Supported() (bool, string) { return true, "" }

// Agent is a LaunchAgent rather than a LaunchDaemon, for the same reason the
// Linux unit is a user unit: the leases, the worktrees, and the state directory
// all belong to one person, and a root daemon would resolve a different HOME.
//
// It is also why the agent cannot have 443. An agent runs as you, so it binds
// what you may bind, and pf is what carries 443 to the port it does bind. The
// LaunchDaemon that loads those rules is a separate, root owned job, installed
// by grove install rather than by this.
//
// KeepAlive with SuccessfulExit false is systemd's Restart=on-failure: come
// back after a crash, stay down after grove stop. There is no readiness
// protocol to match Type=notify, so launchd calls it started as soon as the
// process exists, and the caller is what waits for the socket.
func Agent(executable, listen string) string {
	log := filepath.Join(daemon.StateDir(), "daemon.log")
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>%s</string>
		<string>daemon</string>
		<string>--listen</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>KeepAlive</key>
	<dict>
		<key>SuccessfulExit</key>
		<false/>
	</dict>
	<key>StandardOutPath</key>
	<string>%s</string>
	<key>StandardErrorPath</key>
	<string>%s</string>
</dict>
</plist>
`, label, executable, listen, log, log)
}

// Install writes the agent. Rewriting on every install keeps it pointing at the
// current binary, and launchd is told to re-read it only when something is
// already loaded, since bootstrapping an agent also starts it.
func Install(executable, listen string) (string, error) {
	path, err := write(Agent(executable, listen))
	if err != nil {
		return "", err
	}
	if !Managed() || !loaded() {
		return path, nil
	}
	return path, Restart()
}

// Enable loads the agent so it starts at login. launchd conflates loading with
// starting, and RunAtLoad means bootstrapping runs it, so unlike systemd this
// cannot arm without starting. Callers that must not start yet skip it.
func Enable() error {
	if !Managed() {
		return nil
	}
	if loaded() {
		return nil
	}
	if err := launchctl("bootstrap", domainTarget(), Path()); err != nil {
		return err
	}
	// A disabled label stays disabled through a bootstrap, and being disabled
	// is sticky across reboots, so it is worth clearing rather than leaving a
	// service that is installed, loaded, and silently refused.
	return launchctl("enable", serviceTarget())
}

func Start() error {
	if !Managed() {
		return nil
	}
	if !loaded() {
		return Enable()
	}
	return launchctl("kickstart", serviceTarget())
}

// Restart replaces the running process. The -k is what makes it a restart
// rather than a start, since kickstart on a running job does nothing.
func Restart() error {
	if !Managed() {
		return nil
	}
	if !loaded() {
		return Enable()
	}
	return launchctl("kickstart", "-k", serviceTarget())
}

func Status() State {
	supported, reason := Supported()
	state := State{Supported: supported, Reason: reason, Path: Path(), Installed: installed()}
	if !supported || !Managed() {
		return state
	}
	state.Enabled = loaded()
	state.Active = running()
	state.Lingering = Lingering()
	return state
}

// Lingering has no meaning here. A LaunchAgent belongs to a login session by
// design, so there is nothing to enable and nothing to warn about.
func Lingering() bool { return true }

func EnableLingering() error { return nil }

// loaded reports whether launchd knows about the agent at all.
func loaded() bool {
	return exec.Command("launchctl", "print", serviceTarget()).Run() == nil
}

// running reports whether launchd has a process for it right now, which is not
// the same as knowing about it: a job that exited cleanly stays loaded.
func running() bool {
	out, err := exec.Command("launchctl", "print", serviceTarget()).Output()
	if err != nil {
		return false
	}
	return strings.Contains(string(out), "state = running")
}

func domainTarget() string { return "gui/" + strconv.Itoa(os.Getuid()) }

func serviceTarget() string { return domainTarget() + "/" + label }

func launchctl(args ...string) error {
	if !Managed() {
		return nil
	}
	return run("launchctl", args...)
}
