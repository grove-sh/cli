package service

import (
	"errors"
	"os"
	"path/filepath"
)

const FileName = "sh.grove.daemon.plist"

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "LaunchAgents")
}

var errNotWritten = errors.New("service: launchd support is not written yet")

// A LaunchAgent would keep the daemon running the way the systemd user unit
// does on Linux, and it runs into the same wall on port 443: an agent runs as
// you and cannot bind a low port. That is one decision, taken once, for both
// this file and platform.PrivilegedPorts.
func Supported() (bool, string) {
	return false, "grove does not write a launchd agent yet, so start the daemon yourself with grove restart"
}

func Install(executable, listen string) (string, error) { return "", errNotWritten }

func Enable() error { return errNotWritten }

func Start() error { return errNotWritten }

func Restart() error { return errNotWritten }

func Lingering() bool { return false }

func EnableLingering() error { return nil }

func Status() State {
	supported, reason := Supported()
	return State{Supported: supported, Reason: reason, Path: Path(), Installed: installed()}
}
