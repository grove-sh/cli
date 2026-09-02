package daemon

import (
	"os"
	"path/filepath"
	"runtime"
)

// DefaultSocket picks a short path under a directory only this user can write.
// A unix socket path caps out near 104 bytes on macOS, so GROVE_SOCKET is the
// escape hatch for a deep home directory.
func DefaultSocket() string {
	if s := os.Getenv("GROVE_SOCKET"); s != "" {
		return s
	}
	if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
		return filepath.Join(dir, "grove", "control.sock")
	}
	return filepath.Join(StateDir(), "control.sock")
}

func StateDir() string {
	if dir := os.Getenv("GROVE_STATE_DIR"); dir != "" {
		return dir
	}
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "grove")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "grove")
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(home, "Library", "Application Support", "grove")
	}
	return filepath.Join(home, ".local", "state", "grove")
}
