package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/grove-sh/cli/internal/redirect"
	"github.com/grove-sh/cli/internal/shell"
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
	staged, err := redirect.Stage(dir, Address, merged)
	if err != nil {
		return "", err
	}

	// Quoted, because this is pasted into a shell and the default state
	// directory on macOS is ~/Library/Application Support/grove.
	return strings.Join([]string{
		fmt.Sprintf("The daemon listens on %d, and pf can send 443 there. The three files are", redirect.Port),
		"written; installing them is one privileged step:",
		"",
		"  sudo cp " + shell.Quote(staged.Anchor) + " " + redirect.AnchorPath,
		"  sudo cp " + shell.Quote(staged.Conf) + " " + redirect.ConfPath,
		"  sudo cp " + shell.Quote(staged.Plist) + " " + redirect.PlistPath,
		"  sudo launchctl bootstrap system " + redirect.PlistPath,
		"",
		"That last line loads the job, which runs it, so the redirect starts",
		"working now and again after every reboot.",
		"",
		redirect.ConfPath + " is the machine's own, so grove copied yours and added two",
		"lines rather than writing its own. Worth reading before you install it:",
		"",
		"  diff " + redirect.ConfPath + " " + shell.Quote(staged.Conf),
	}, "\n"), nil
}

// RemoveRedirect stages a pf.conf without grove's lines and prints the step
// that installs it, the same way PrepareRedirect does for putting them in.
//
// Uninstalling only the trust store used to leave the redirect and its boot job
// behind, so a machine with no grove on it still sent every loopback
// connection on 443 to a port nothing was listening to.
func RemoveRedirect(dir string) (string, error) {
	state, err := inspect()
	if err != nil {
		return "", err
	}
	if !state.Referenced && !state.Anchor && !state.Boot {
		return "", nil
	}

	current, err := os.ReadFile(redirect.ConfPath)
	if err != nil {
		return "", err
	}
	cleaned, _ := redirect.Without(string(current))
	staged := filepath.Join(dir, "pf", "pf.conf.clean")
	if err := os.MkdirAll(filepath.Dir(staged), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(staged, []byte(cleaned), 0o644); err != nil {
		return "", err
	}

	// The reference goes before the file it names, which is install's order
	// reversed: pf.conf must never mention an anchor that is not there, or
	// pfctl refuses the whole ruleset and the machine loses its own rules too.
	// Someone pasting five lines does not stop at the first failure, so every
	// prefix of this list has to leave pf loadable.
	return strings.Join([]string{
		"The redirect and the job that puts it back are still installed. Removing",
		"them is one privileged step:",
		"",
		"  sudo cp " + shell.Quote(staged) + " " + redirect.ConfPath,
		"  sudo pfctl -f " + redirect.ConfPath,
		"  sudo pfctl -a " + redirect.AnchorName + " -F nat",
		"  sudo launchctl bootout system " + redirect.PlistPath,
		"  sudo rm " + redirect.PlistPath + " " + redirect.AnchorPath,
		"  sudo ifconfig lo0 -alias " + Address,
		"",
		"In that order: the first two take grove out of the machine's own rules,",
		"the third clears what is still loaded, then the files go, and the last",
		"takes " + Address + " back off the loopback interface. pf itself is left",
		"enabled, since it may have been on before grove and other rules may want it.",
		"",
		"pfctl warns that -f could flush rules the system added at startup. It says",
		"that every time and it is not a failure; a real problem names a file and a",
		"line. To read the file without loading it, add -n.",
		"",
		redirect.ConfPath + " is the machine's own, so grove took its two lines out of",
		"the copy above rather than restoring one it remembered. Worth reading first:",
		"",
		"  diff " + redirect.ConfPath + " " + shell.Quote(staged),
	}, "\n"), nil
}

// Where pf sends 443, since macOS will not allow binding it. Nothing reaches
// here until the redirect is installed, which PrivilegedPorts reports on.
func DefaultListen() string { return fmt.Sprintf("%s:%d", Address, redirect.Port) }

func WSL() bool { return false }

// The same arrangement for 80, out of the same anchor.
func DefaultHTTPListen() string { return fmt.Sprintf("%s:%d", Address, redirect.HTTPPort) }
