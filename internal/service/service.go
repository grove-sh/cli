// Package service keeps grove's daemon running across logins and reboots, using
// whatever the operating system provides. Each platform supplies its own
// implementation; this file holds what they share.
package service

import (
	"os"
	"path/filepath"
)

// State is what grove can tell about the daemon's service registration.
type State struct {
	// Supported means this platform has a manager grove knows how to use.
	Supported bool

	// Reason says why not, when Supported is false.
	Reason string

	Installed bool
	Enabled   bool
	Active    bool

	// Lingering means the service survives logout. Only systemd distinguishes
	// this; elsewhere it is false and uninteresting.
	Lingering bool

	// Path is where the unit or plist belongs, whether or not it is there.
	Path string
}

// Dir is where the service definition belongs. GROVE_SERVICE_DIR redirects it,
// which also puts this package in unmanaged mode: it will write a definition
// for you to read and will not touch the real service manager. Tests rely on
// that, so running them cannot register anything on the machine.
func Dir() string {
	if dir := os.Getenv("GROVE_SERVICE_DIR"); dir != "" {
		return dir
	}
	return defaultDir()
}

func Path() string {
	dir := Dir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, FileName)
}

// Managed reports whether this package may talk to the real service manager.
func Managed() bool {
	return os.Getenv("GROVE_SERVICE_DIR") == ""
}

func installed() bool {
	path := Path()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

func write(contents string) (string, error) {
	path := Path()
	if path == "" {
		return "", os.ErrNotExist
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
