package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"

	"github.com/grove-sh/cli/internal/platform"
)

const FileName = "grove.service"

func defaultDir() string {
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

func Supported() (bool, string) {
	if !Managed() {
		return true, ""
	}
	if info, err := os.Stat("/run/systemd/system"); err == nil && info.IsDir() {
		return true, ""
	}
	if platform.WSL() {
		return false, "this distro runs without systemd; add \"[boot]\\nsystemd=true\" to /etc/wsl.conf and run wsl --shutdown"
	}
	return false, "this machine did not boot with systemd"
}

// Unit is a user unit rather than a system one, because the leases, the
// worktrees, and the state directory all belong to one person. That is also
// why it cannot grant CAP_NET_BIND_SERVICE the way a system unit would: an
// unprivileged user manager has no capability to hand out, which is what makes
// the unprivileged port floor load bearing rather than convenient.
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

// Install writes the unit and has the user manager re-read it. Rewriting on
// every install keeps ExecStart pointing at the current binary.
func Install(executable, listen string) (string, error) {
	path, err := write(Unit(executable, listen))
	if err != nil {
		return "", err
	}
	if !Managed() {
		return path, nil
	}
	return path, systemctl("daemon-reload")
}

// Enable makes the unit start at login without starting it now, which is the
// right thing when the port it wants is not available yet.
func Enable() error { return systemctl("enable", FileName) }

func Start() error { return systemctl("start", FileName) }

func Restart() error { return systemctl("restart", FileName) }

func Status() State {
	supported, reason := Supported()
	state := State{Supported: supported, Reason: reason, Path: Path(), Installed: installed()}
	if !supported || !Managed() {
		return state
	}
	state.Enabled = systemctl("is-enabled", "--quiet", FileName) == nil
	state.Active = systemctl("is-active", "--quiet", FileName) == nil
	state.Lingering = Lingering()
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
	if !Managed() {
		return nil
	}
	return run("systemctl", append([]string{"--user"}, args...)...)
}

func run(name string, args ...string) error {
	output, err := exec.Command(name, args...).CombinedOutput()
	if err == nil {
		return nil
	}
	if trimmed := strings.TrimSpace(string(output)); trimmed != "" {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, trimmed)
	}
	return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
}
