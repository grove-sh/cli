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
//
// Three pieces have to be in place. The rule, a reference to it from the
// machine's own pf.conf, since an anchor nothing refers to loads cleanly and
// does nothing, and a launchd job to put it all back after a reboot, since
// macOS loads pf.conf at boot but leaves pf switched off.
func PrivilegedPorts() PortAccess {
	missing, err := missingPieces()
	if err != nil {
		return PortAccess{Detail: err.Error()}
	}
	if len(missing) == 0 {
		return PortAccess{
			Allowed: true,
			Detail:  fmt.Sprintf("pf sends 443 to %d, so grove serves it without root", redirect.Port),
		}
	}
	return PortAccess{Detail: "443 needs root here, and " + strings.Join(missing, ", ")}
}

// missingPieces names what is not in place yet, in the words the report uses.
func missingPieces() ([]string, error) {
	conf, err := os.ReadFile(redirect.ConfPath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", redirect.ConfPath, err)
	}

	var missing []string
	if !redirect.Configured(string(conf)) {
		missing = append(missing, "nothing redirects it yet")
	}
	if _, err := os.Stat(redirect.AnchorPath); err != nil {
		missing = append(missing, redirect.AnchorPath+" is not there")
	}
	if _, err := os.Stat(redirect.PlistPath); err != nil {
		missing = append(missing, "nothing puts the rules back after a reboot")
	}
	return missing, nil
}

// PrepareRedirect writes the files the privileged step installs, so that step
// is a copy of things you can read first rather than a shell incantation.
//
// Grove writes them and stops there. Editing how a machine filters packets is
// not something to do behind someone's back, and it is the same stance the
// Linux sysctl gets.
func PrepareRedirect(dir string) (string, error) {
	missing, err := missingPieces()
	if err != nil {
		return "", err
	}
	if len(missing) == 0 {
		return "", nil
	}

	current, err := os.ReadFile(redirect.ConfPath)
	if err != nil {
		return "", err
	}
	// Already referenced is fine: the merge is idempotent, and the staged copy
	// then matches what is installed.
	merged, _, err := redirect.Conf(string(current))
	if err != nil {
		return "", err
	}
	staged, err := redirect.Stage(dir, merged)
	if err != nil {
		return "", err
	}

	return strings.Join([]string{
		fmt.Sprintf("The daemon listens on %d, and pf can send 443 there. The three files are", redirect.Port),
		"written; installing them is one privileged step:",
		"",
		"  sudo cp " + staged.Anchor + " " + redirect.AnchorPath,
		"  sudo cp " + staged.Conf + " " + redirect.ConfPath,
		"  sudo cp " + staged.Plist + " " + redirect.PlistPath,
		"  sudo launchctl bootstrap system " + redirect.PlistPath,
		"",
		"That last line loads the job, which runs it, so the redirect starts",
		"working now and again after every reboot.",
		"",
		redirect.ConfPath + " is the machine's own, so grove copied yours and added two",
		"lines rather than writing its own. Worth reading before you install it:",
		"",
		"  diff " + redirect.ConfPath + " " + staged.Conf,
	}, "\n"), nil
}

// DefaultListen is the port pf sends 443 to, since binding 443 itself is what
// macOS will not allow. Without the redirect installed nothing reaches it,
// which is what PrivilegedPorts reports.
func DefaultListen() string { return fmt.Sprintf("127.0.0.1:%d", redirect.Port) }

func WSL() bool { return false }
