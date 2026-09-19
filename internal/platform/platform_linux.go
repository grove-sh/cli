package platform

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/grove-sh/cli/internal/shell"
)

const (
	unprivilegedPortStart = "/proc/sys/net/ipv4/ip_unprivileged_port_start"

	// Named for grove so uninstall can tell its file from one the machine's
	// owner wrote, which is the only kind it should remove.
	sysctlFile = "/etc/sysctl.d/60-grove.conf"
	sysctlKey  = "net.ipv4.ip_unprivileged_port_start"
)

const sysctlAdvice = `Lowering the unprivileged port floor is one sysctl, survives every reinstall,
and leaves the daemon running as you rather than as root. Run grove install: it
writes the line and offers to install it.`

func PrivilegedPorts() PortAccess {
	floor, err := os.ReadFile(unprivilegedPortStart)
	if err != nil {
		return PortAccess{Detail: "cannot tell: " + err.Error(), Advice: sysctlAdvice}
	}

	value := strings.TrimSpace(string(floor))
	start, err := strconv.Atoi(value)
	if err != nil {
		return PortAccess{Detail: "cannot read the unprivileged port floor", Advice: sysctlAdvice}
	}
	switch {
	case start <= 80:
		return PortAccess{Allowed: true, Detail: "the unprivileged port floor is " + value + ", so grove can bind 443 and 80"}
	case start <= 443:
		return PortAccess{
			Allowed: true,
			Detail:  "the unprivileged port floor is " + value + ", so grove can bind 443, but not 80 for the http redirect",
			Advice:  sysctlAdvice,
		}
	}
	return PortAccess{Detail: "the unprivileged port floor is " + value + ", so 443 needs privileges grove does not have", Advice: sysctlAdvice}
}

// Under Windows the browser, the trust store, and the hosts file all live on
// the other side.
func WSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(release)), "microsoft")
}

// A floor already at 80 leaves nothing to do; one at 443 still costs the http
// redirect, so it is offered the same file.
func PreparePorts(dir string) (Plan, error) {
	access := PrivilegedPorts()
	if access.Allowed && access.Advice == "" {
		return Plan{}, nil
	}
	return lowerFloor(dir)
}

// Staged as a file and copied rather than written through tee, so the reader
// can open what root is about to be handed, and so the step is one command
// with no pipe in it.
func lowerFloor(dir string) (Plan, error) {
	staged := filepath.Join(dir, "sysctl", filepath.Base(sysctlFile))
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return Plan{}, err
	}
	if err := os.WriteFile(staged, []byte(sysctlKey+"=80\n"), 0o644); err != nil {
		return Plan{}, err
	}
	return Plan{
		Summary: "the floor is above 80, so grove cannot bind 443 or 80 as you",
		Intro: "Lowering the unprivileged port floor lets grove bind 443 and 80 as you, so the\n" +
			"daemon never runs as root. The file is written; installing it is one privileged\n" +
			"step:",
		Steps: []shell.Step{
			{"cp", staged, sysctlFile},
			{"sysctl", "-p", sysctlFile},
		},
		Outro: "80 rather than 443 so grove can also answer plain http and send it to https.\n" +
			"The file is one line, and the machine reads it again at every boot.",
	}, nil
}

// Only grove's own file is taken back. A floor lowered some other way is the
// machine's setting, and lowering one intercepts nothing, so there is nothing
// else to undo.
func RemovePorts(string) (Plan, error) {
	if _, err := os.Stat(sysctlFile); err != nil {
		return Plan{}, nil
	}
	return Plan{
		Summary: sysctlFile + " still lowers the port floor",
		Intro: "The sysctl grove installed is still lowering the port floor. Putting it back is\n" +
			"one privileged step:",
		Steps: []shell.Step{
			{"rm", sysctlFile},
			{"sysctl", "-w", sysctlKey + "=1024"},
		},
		// Removing the file changes nothing until a reboot, and reloading
		// what remains does not raise a value nothing sets any more.
		Outro: "The second line matters: without it the running floor stays lowered until a\n" +
			"reboot. 1024 is the kernel's default. If another file under /etc/sysctl.d sets\n" +
			"a floor of its own, sudo sysctl --system puts that one back.",
	}, nil
}

// The sysctl lowers the floor, so grove binds 443 itself.
func DefaultListen() string { return Address + ":443" }

// Best effort: 80 is below the floor the advised sysctl lowers to 443, so this
// usually fails and nothing breaks.
func DefaultHTTPListen() string { return Address + ":80" }
