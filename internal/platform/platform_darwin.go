package platform

import (
	"fmt"
	"os"
	"strings"

	"github.com/grove-sh/cli/internal/redirect"
)

// Only reads the machine. What the answer means lives in redirect.Access, where
// a Linux machine's tests can reach it.
func PrivilegedPorts() PortAccess {
	state, err := inspect()
	if err != nil {
		return PortAccess{Detail: err.Error(), Advice: redirect.Advice}
	}
	allowed, detail, advice := redirect.Access(state)
	return PortAccess{Allowed: allowed, Detail: detail, Advice: advice}
}

// An anchor nothing refers to loads cleanly and does nothing, so the reference
// counts as much as the file it names.
func inspect() (redirect.State, error) {
	conf, err := os.ReadFile(redirect.ConfPath)
	if err != nil {
		return redirect.State{}, fmt.Errorf("cannot read %s: %w", redirect.ConfPath, err)
	}
	exists := func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	}
	return redirect.State{
		Referenced: redirect.Configured(string(conf)),
		Anchor:     exists(redirect.AnchorPath),
		Boot:       exists(redirect.PlistPath),
	}, nil
}

// Writes the files and stops there: editing how a machine filters packets is
// not a thing to do behind someone's back, and the Linux sysctl gets the same.
func PrepareRedirect(dir string) (string, error) {
	state, err := inspect()
	if err != nil {
		return "", err
	}
	if ready, _, _ := redirect.Access(state); ready {
		return "", nil
	}

	current, err := os.ReadFile(redirect.ConfPath)
	if err != nil {
		return "", err
	}
	// Already referenced is fine: the merge is idempotent.
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

// Where pf sends 443, since macOS will not allow binding it. Nothing reaches
// here until the redirect is installed, which PrivilegedPorts reports on.
func DefaultListen() string { return fmt.Sprintf("127.0.0.1:%d", redirect.Port) }

func WSL() bool { return false }

// The same arrangement for 80, out of the same anchor.
func DefaultHTTPListen() string { return fmt.Sprintf("127.0.0.1:%d", redirect.HTTPPort) }
