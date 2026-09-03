//go:build !linux && !darwin

package service

import (
	"errors"
	"os"
	"path/filepath"
)

const FileName = "grove.service"

func defaultDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "grove")
}

var errNotWritten = errors.New("service: no service manager grove knows about")

// Nothing here knows how to keep the daemon running, so that is the operator's
// job.
func Supported() (bool, string) {
	return false, "grove knows no service manager here, so start the daemon yourself with grove restart"
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
