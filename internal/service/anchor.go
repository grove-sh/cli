package service

import (
	"io"
	"os"
	"path/filepath"
)

// Anchor copies an executable into a directory grove owns and returns the path
// of the copy, which is what a unit should name.
//
// A unit outlives whatever the binary was installed from. A package manager
// puts the version in the path and moves it on the next upgrade, and a
// node_modules tree is deleted outright by ordinary clean scripts, either of
// which would leave the service pointing at a file that is no longer there.
// The copy is grove's own, so nothing a package manager does can strand it.
//
// It lands by rename, so a daemon already running from the old copy keeps the
// inode it started with, and a failure part way through leaves the previous
// one in place.
func Anchor(executable, dir string) (string, error) {
	target := filepath.Join(dir, "grove")
	if sameFile(executable, target) {
		return target, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	source, err := os.Open(executable)
	if err != nil {
		return "", err
	}
	defer source.Close()

	tmp, err := os.CreateTemp(dir, ".grove-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())

	if _, err := io.Copy(tmp, source); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return "", err
	}
	return target, nil
}

// sameFile reports whether the copy would be its own source, which is what
// re-running install from the anchored binary does.
func sameFile(a, b string) bool {
	left, err := os.Stat(a)
	if err != nil {
		return false
	}
	right, err := os.Stat(b)
	if err != nil {
		return false
	}
	return os.SameFile(left, right)
}
