// Package service installs grove's daemon as a systemd user unit, so it comes
// back after a reboot without anyone keeping a terminal open.
package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
)

const UnitName = "grove.service"

// Available reports whether this machine booted with systemd. WSL without
// systemd enabled, and macOS, land here.
func Available() bool {
	if !Managed() {
		return true
	}
	info, err := os.Stat("/run/systemd/system")
	return err == nil && info.IsDir()
}

// UnitDir is where the unit belongs. GROVE_SERVICE_DIR redirects it, which
// also puts this package in unmanaged mode: it will write a unit for you to
// look at and will not touch the real service manager. Tests rely on that, so
// running them cannot enable anything on the machine.
func UnitDir() string {
	if dir := os.Getenv("GROVE_SERVICE_DIR"); dir != "" {
		return dir
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user")
}

func UnitPath() string {
	dir := UnitDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, UnitName)
}

// Managed reports whether this package may talk to the real service manager.
func Managed() bool {
	return os.Getenv("GROVE_SERVICE_DIR") == ""
}

// Unit is a user unit rather than a system one, because the leases, the
// worktrees, and the state directory all belong to one person. That is also
// why it cannot grant CAP_NET_BIND_SERVICE the way a system unit would: an
// unprivileged user manager has no capability to hand out, which is what makes
// the unprivileged port sysctl load bearing rather than convenient.
func Unit(executable, listen string) string {
	return fmt.Sprintf(`[Unit]
Description=grove local HTTPS proxy and lease registry
Documentation=https://github.com/grove-sh/cli

[Service]
# grove reports READY=1 once both listeners are up, so a start blocks until it
# can actually serve rather than returning when the process spawns.
Type=notify
# Without a start timeout a daemon that cannot bind its port leaves systemd
# waiting for READY=1 for its full default, which is a minute and a half.
TimeoutStartSec=10s
ExecStart=%s daemon --listen %s
Restart=on-failure
RestartSec=1s

[Install]
WantedBy=default.target
`, executable, listen)
}

// Write installs the unit and tells the user manager to re-read it. Rewriting
// on every install keeps ExecStart pointing at the current binary.
func Write(executable, listen string) (string, error) {
	path := UnitPath()
	if path == "" {
		return "", errors.New("service: no config directory to write a unit into")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(Unit(executable, listen)), 0o644); err != nil {
		return "", err
	}
	if !Managed() {
		return path, nil
	}
	if err := systemctl("daemon-reload"); err != nil {
		return "", err
	}
	return path, nil
}

// Enable makes the unit start at login without starting it now, which is the
// right thing when the port it wants is not available yet.
func Enable() error {
	if !Managed() {
		return nil
	}
	return systemctl("enable", UnitName)
}

func Start() error {
	if !Managed() {
		return nil
	}
	return systemctl("start", UnitName)
}

func Restart() error {
	if !Managed() {
		return nil
	}
	return systemctl("restart", UnitName)
}

type State struct {
	Installed bool
	Enabled   bool
	Active    bool
}

func Status() State {
	state := State{}
	if path := UnitPath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			state.Installed = true
		}
	}
	if !Managed() {
		return state
	}
	state.Enabled = systemctl("is-enabled", "--quiet", UnitName) == nil
	state.Active = systemctl("is-active", "--quiet", UnitName) == nil
	return state
}

// Lingering reports whether the user manager survives logout. Without it the
// daemon dies with the last session, so it is not there when you open a
// terminal after a reboot.
func Lingering() bool {
	if !Managed() {
		return false
	}
	current, err := user.Current()
	if err != nil {
		return false
	}
	out, err := exec.Command("loginctl", "show-user", current.Username, "--property=Linger").Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(out)) == "Linger=yes"
}

func EnableLingering() error {
	if !Managed() {
		return nil
	}
	current, err := user.Current()
	if err != nil {
		return err
	}
	return run("loginctl", "enable-linger", current.Username)
}

func systemctl(args ...string) error {
	return run("systemctl", append([]string{"--user"}, args...)...)
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		trimmed := strings.TrimSpace(string(output))
		if trimmed == "" {
			return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
		}
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
	}
	return nil
}
