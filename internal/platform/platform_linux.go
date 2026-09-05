package platform

import (
	"os"
	"strconv"
	"strings"
)

const unprivilegedPortStart = "/proc/sys/net/ipv4/ip_unprivileged_port_start"

const sysctlAdvice = `Lowering the unprivileged port floor is one line, survives every reinstall, and
leaves the daemon running as you rather than as root:

  echo 'net.ipv4.ip_unprivileged_port_start=443' | sudo tee /etc/sysctl.d/60-grove.conf
  sudo sysctl --system`

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
	if start <= 443 {
		return PortAccess{Allowed: true, Detail: "the unprivileged port floor is " + value + ", so grove can bind 443"}
	}
	return PortAccess{Detail: "the unprivileged port floor is " + value + ", so 443 needs privileges grove does not have", Advice: sysctlAdvice}
}

// WSL reports whether this is a Linux kernel running under Windows, where the
// browser, the trust store, and the hosts file all live on the other side.
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

// PrepareRedirect has nothing to stage here, since lowering the port floor is
// a sysctl rather than a pair of files. The static advice stands.
func PrepareRedirect(string) (string, error) { return "", nil }

// DefaultListen is where the daemon serves. The sysctl lowers the floor, so grove binds 443 itself.
func DefaultListen() string { return "127.0.0.1:443" }
