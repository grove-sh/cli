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

// Writing an agent waits on the port question in platform.PrivilegedPorts,
// tracked in grove-sh/cli#2, since an agent runs as you and cannot bind 443.
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
