package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/grove-sh/cli/internal/daemon"
)

// Waits for the daemon to answer. Its output goes to a log file, since nothing
// is watching its stderr once it outlives this process.
func spawnDaemon(opts daemonOptions) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(daemon.StateDir(), 0o700); err != nil {
		return err
	}

	logPath := filepath.Join(daemon.StateDir(), "daemon.log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()

	// Where this run's output begins. The log is appended to across starts, so
	// reporting the tail of it would mix today's failure with last week's.
	from, err := logFile.Seek(0, io.SeekEnd)
	if err != nil {
		from = 0
	}

	child := exec.Command(self, append([]string{"daemon"}, opts.args()...)...)
	child.Stdout, child.Stderr = logFile, logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	// Reaped rather than left a zombie, and the same wait says the daemon gave
	// up: without it, one that cannot start costs the whole timeout in silence.
	exited := make(chan struct{})
	go func() {
		child.Wait()
		close(exited)
	}()

	if err := waitForSocket(opts.socket, exited, 15*time.Second); err != nil {
		// The daemon's own words, since this process only knows nothing answered.
		if said := linesSince(logPath, from); said != "" {
			return errors.New(said)
		}
		return err
	}
	return nil
}

func waitForSocket(path string, exited <-chan struct{}, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return nil
		}
		select {
		case <-exited:
			return errors.New("grove stopped before it could serve")
		case <-time.After(25 * time.Millisecond):
		}
	}
	return fmt.Errorf("the daemon did not answer at %s", path)
}

func waitForSocketGone(path string, within time.Duration) {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err != nil {
			return
		}
		conn.Close()
		time.Sleep(25 * time.Millisecond)
	}
}

// From the offset this start began at, not the whole appended log.
func linesSince(path string, from int64) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	if _, err := file.Seek(from, io.SeekStart); err != nil {
		return ""
	}
	var said []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		// The daemon prefixes its own lines, and so does whatever prints this.
		said = append(said, strings.TrimPrefix(scanner.Text(), "grove: "))
	}
	return strings.TrimSpace(strings.Join(said, "\n"))
}
