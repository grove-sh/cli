package platform

import (
	"fmt"
	"os"
	"strings"

	"github.com/grove-sh/cli/internal/redirect"
)

// macOS has no unprivileged port floor to lower, and a LaunchAgent cannot bind
// a low port any more than a systemd user unit can. What works is pf: the
// daemon binds a port it is allowed to bind, and 443 is sent there. Confirmed
// on macOS 26, 15, and 15 on Intel, hostname and all: grove-sh/cli#2.
func PrivilegedPorts() PortAccess {
	conf, err := os.ReadFile(redirect.ConfPath)
	if err != nil {
		return PortAccess{Detail: "cannot read " + redirect.ConfPath + ": " + err.Error()}
	}
	if !redirect.Configured(string(conf)) {
		return PortAccess{Detail: "443 needs root here, and nothing redirects it yet"}
	}
	if _, err := os.Stat(redirect.AnchorPath); err != nil {
		return PortAccess{Detail: redirect.ConfPath + " loads grove's anchor, but " + redirect.AnchorPath + " is not there"}
	}
	return PortAccess{
		Allowed: true,
		Detail:  fmt.Sprintf("pf sends 443 to %d, so grove serves it without root", redirect.Port),
	}
}

// PrepareRedirect writes the two files the privileged step installs, so that
// step is a copy rather than a shell incantation, and so the pf.conf being
// installed can be read before it is.
//
// Grove writes them and stops there. Editing how a machine filters packets is
// not something to do behind someone's back, and it is the same stance the
// Linux sysctl gets.
func PrepareRedirect(dir string) (string, error) {
	current, err := os.ReadFile(redirect.ConfPath)
	if err != nil {
		return "", err
	}
	merged, changed, err := redirect.Conf(string(current))
	if err != nil {
		return "", err
	}
	if !changed {
		return "", nil
	}

	anchor, conf, err := redirect.Stage(dir, merged)
	if err != nil {
		return "", err
	}

	return strings.Join([]string{
		fmt.Sprintf("The daemon listens on %d, and pf can send 443 there. Both files are", redirect.Port),
		"written; installing them is one privileged step:",
		"",
		"  sudo cp " + anchor + " " + redirect.AnchorPath,
		"  sudo cp " + conf + " " + redirect.ConfPath,
		"  sudo pfctl -f " + redirect.ConfPath + " -E",
		"",
		redirect.ConfPath + " is the machine's, so grove copied yours and added two lines",
		"rather than writing its own. Worth reading before you install it:",
		"",
		"  diff " + redirect.ConfPath + " " + conf,
	}, "\n"), nil
}

func WSL() bool { return false }

// DefaultListen is the port pf sends 443 to, since binding 443 itself is what
// macOS will not allow. Without the redirect installed nothing reaches it,
// which is what PrivilegedPorts reports.
func DefaultListen() string { return fmt.Sprintf("127.0.0.1:%d", redirect.Port) }
