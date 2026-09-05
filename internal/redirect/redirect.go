// Package redirect builds the pf rules that let macOS reach grove on 443.
//
// A process running as you cannot bind a port below 1024 there, and macOS has
// no floor to lower the way Linux does. So the daemon binds a port it is
// allowed to bind and pf sends 443 to it, which is how puma-dev has run without
// root for years. Confirmed on macOS 26, 15, and 15 on Intel: grove-sh/cli#2.
//
// Nothing here is macOS specific to build or test. It is text.
package redirect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Port is where the daemon listens on macOS, and where 443 is sent. It sits
	// outside the 20000-20999 lease range on purpose: a redirect target handed
	// out as someone's port would be a very confusing afternoon.
	Port = 10443

	// AnchorName is what the rules are called inside pf.
	AnchorName = "grove"

	// AnchorPath and ConfPath are where macOS keeps them.
	AnchorPath = "/etc/pf.anchors/grove"
	ConfPath   = "/etc/pf.conf"

	// PlistLabel and PlistPath name the job that puts the rules back after a
	// reboot. macOS loads /etc/pf.conf at boot but leaves pf switched off, so
	// without this the redirect is gone every morning.
	PlistLabel = "sh.grove.pf"
	PlistPath  = "/Library/LaunchDaemons/sh.grove.pf.plist"
)

// appleRDR is where Apple's own translation anchor sits in a stock pf.conf.
const appleRDR = `rdr-anchor "com.apple/*"`

var (
	reference = fmt.Sprintf("rdr-anchor %q", AnchorName)
	loadLine  = fmt.Sprintf("load anchor %q from %q", AnchorName, AnchorPath)

	// ErrUnknownConf says this pf.conf is not the shape macOS ships, so grove
	// has no idea where its rule would belong. Guessing at a firewall's rule
	// order is not a thing to do on someone's machine.
	ErrUnknownConf = errors.New("redirect: no rdr-anchor to add grove's beside")
)

// Anchor is the rule itself: everything arriving on loopback for 443 goes to
// the port the daemon could bind.
func Anchor(port int) string {
	return fmt.Sprintf("rdr pass on lo0 inet proto tcp from any to any port = 443 -> 127.0.0.1 port %d\n", port)
}

// Conf returns the machine's own pf.conf with two lines added, and reports
// whether anything changed.
//
// pf.conf is order sensitive, translation before filtering, so the reference
// goes beside Apple's rather than at the end, where it would be a parse error
// rather than a subtle bug. Apple's rules are carried through untouched: a
// local development tool has no business rewriting how a machine filters
// packets.
func Conf(existing string) (string, bool, error) {
	if Configured(existing) {
		return existing, false, nil
	}

	lines := strings.Split(existing, "\n")
	placed := make([]string, 0, len(lines)+2)
	found := false
	for _, line := range lines {
		placed = append(placed, line)
		if !found && strings.TrimSpace(line) == appleRDR {
			placed = append(placed, reference)
			found = true
		}
	}
	if !found {
		return "", false, ErrUnknownConf
	}

	out := strings.Join(placed, "\n")
	if !strings.HasSuffix(out, "\n") {
		out += "\n"
	}
	return out + loadLine + "\n", true, nil
}

// Configured reports whether a pf.conf already refers to grove's anchor. An
// anchor nothing refers to loads without complaint and does nothing at all,
// which is the failure worth being able to name.
func Configured(conf string) bool {
	for _, line := range strings.Split(conf, "\n") {
		if strings.TrimSpace(line) == reference {
			return true
		}
	}
	return false
}

// Plist is the launchd job that reloads and enables pf at boot.
//
// pfctl -E rather than -e: it enables pf and tolerates pf already being on,
// where -e fails and would leave a failed job in the log every boot for no
// reason. Loading this job runs it, so installing it also applies the rules.
func Plist() string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>/sbin/pfctl</string>
		<string>-E</string>
		<string>-f</string>
		<string>%s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, PlistLabel, ConfPath)
}

// Staged is where grove put the files the privileged step installs.
type Staged struct {
	Anchor string
	Conf   string
	Plist  string
}

// Stage writes all three into a directory grove owns, so the privileged step is
// a copy of readable files rather than a shell incantation nobody can check
// before running it. Root has to read them, so they are not private.
func Stage(dir, conf string) (Staged, error) {
	dir = filepath.Join(dir, "pf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return Staged{}, err
	}
	staged := Staged{
		Anchor: filepath.Join(dir, "anchor"),
		Conf:   filepath.Join(dir, "pf.conf"),
		Plist:  filepath.Join(dir, PlistLabel+".plist"),
	}
	for path, body := range map[string]string{
		staged.Anchor: Anchor(Port),
		staged.Conf:   conf,
		staged.Plist:  Plist(),
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return Staged{}, err
		}
	}
	return staged, nil
}
