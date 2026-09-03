package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/grove-sh/cli/internal/daemon"
)

// spawnDaemon starts a daemon that outlives this process and waits for it to
// answer. Its output goes to a log file, since nothing is watching its stderr.
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

	child := exec.Command(self, append([]string{"daemon"}, opts.args()...)...)
	child.Stdout, child.Stderr = logFile, logFile
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		return err
	}
	// Nothing waits on it, so let the kernel reap it rather than leaving a
	// zombie behind when this process outlives the start.
	go child.Wait()

	if err := waitForSocket(opts.socket, 15*time.Second); err != nil {
		return fmt.Errorf("%w\n%s", err, lastLines(logPath, 5))
	}
	return nil
}

func waitForSocket(path string, within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		conn, err := net.Dial("unix", path)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(25 * time.Millisecond)
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

func lastLines(path string, n int) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer file.Close()

	var kept []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		kept = append(kept, scanner.Text())
		if len(kept) > n {
			kept = kept[1:]
		}
	}
	return strings.Join(kept, "\n")
}
