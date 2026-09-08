// Package redirect builds the pf rules that let macOS reach grove on 443.
//
// A process running as you cannot bind a port below 1024 there, and unlike
// Linux there is no floor to lower, so the daemon binds a high port and pf
// sends 443 to it. Nothing here is macOS specific to build or test: it is text.
package redirect

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Both sit outside the 20000-20999 lease range, so a redirect target is
	// never also handed out as someone's port.
	Port     = 10443
	HTTPPort = 10080

	AnchorName = "grove"
	AnchorPath = "/etc/pf.anchors/grove"
	ConfPath   = "/etc/pf.conf"

	// macOS loads /etc/pf.conf at boot but leaves pf switched off, so without
	// this job the redirect is gone every morning.
	PlistLabel = "sh.grove.pf"
	PlistPath  = "/Library/LaunchDaemons/sh.grove.pf.plist"
)

// Apple's own translation anchor, as it appears in a stock pf.conf.
const appleRDR = `rdr-anchor "com.apple/*"`

var (
	reference = fmt.Sprintf("rdr-anchor %q", AnchorName)
	loadLine  = fmt.Sprintf("load anchor %q from %q", AnchorName, AnchorPath)

	// ErrUnknownConf: guessing where a rule belongs in a firewall's order is
	// not a thing to do on someone's machine.
	ErrUnknownConf = errors.New("redirect: no rdr-anchor to add grove's beside")
)

// Scoped to one address, because an unscoped rule takes every loopback
// connection on 443 whatever else is bound there, and outlives the daemon.
func Anchor(addr string, https, http int) string {
	rule := func(from, to int) string {
		return fmt.Sprintf("rdr pass on lo0 inet proto tcp from any to %s port = %d -> %s port %d\n", addr, from, addr, to)
	}
	return rule(443, https) + rule(80, http)
}

// Conf adds two lines. pf.conf is order sensitive, translation before
// filtering, so the reference goes beside Apple's rather than at the end, where
// it would be a parse error. Apple's own rules are carried through untouched.
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

// Without is the inverse of Conf: the machine's pf.conf with grove's two lines
// taken out, and whether either was there to take. Apple's rules and anyone
// else's are carried through, which is why this removes named lines rather
// than restoring a copy of what pf.conf looked like before.
func Without(existing string) (string, bool) {
	kept := make([]string, 0, len(existing))
	removed := false
	for _, line := range strings.Split(existing, "\n") {
		switch strings.TrimSpace(line) {
		case reference, loadLine:
			removed = true
		default:
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), removed
}

// Configured matters because an anchor nothing refers to loads without
// complaint and does nothing at all.
func Configured(conf string) bool {
	for _, line := range strings.Split(conf, "\n") {
		if strings.TrimSpace(line) == reference {
			return true
		}
	}
	return false
}

// Plist restores the alias as well as the rules, since macOS configures neither
// the address nor pf at boot, and without the address nothing resolves at all.
//
// Sequenced with ; so pf still loads when the alias is already there, and
// pfctl -E rather than -e so it tolerates pf already being on. Loading the job
// runs it, so installing it applies both.
func Plist(addr string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>%s</string>
	<key>ProgramArguments</key>
	<array>
		<string>/bin/sh</string>
		<string>-c</string>
		<string>/sbin/ifconfig lo0 alias %s netmask 0xffffffff; /sbin/pfctl -E -f %s</string>
	</array>
	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`, PlistLabel, addr, ConfPath)
}

// Staged is where grove wrote the files the privileged step installs.
type Staged struct {
	Anchor string
	Conf   string
	Plist  string
}

// Stage leaves readable files rather than a shell incantation nobody can check
// first. Root has to read them, so they are not private.
func Stage(dir, addr, conf string) (Staged, error) {
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
		staged.Anchor: Anchor(addr, Port, HTTPPort),
		staged.Conf:   conf,
		staged.Plist:  Plist(addr),
	} {
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return Staged{}, err
		}
	}
	return staged, nil
}

// State: Referenced is pf.conf pointing at the anchor, Boot is the job that
// reloads it after a reboot.
type State struct {
	Referenced bool
	Anchor     bool
	Boot       bool
}

// Advice is kept short because grove install prints the whole procedure.
const Advice = `Grove can send 443 to a port it is allowed to bind, which is what macOS leaves
open to you. Run grove install: it writes the pf rule, a copy of this machine's
pf.conf with two lines added, and the launchd job that puts them back after a
reboot, then prints the one privileged step that installs the three.`

// Access is pure, so every platform's tests cover it: a report that says no
// without saying what would make it yes is the failure worth guarding against.
func Access(state State) (allowed bool, detail, advice string) {
	var missing []string
	if !state.Referenced {
		missing = append(missing, "nothing redirects it yet")
	}
	if !state.Anchor {
		missing = append(missing, AnchorPath+" is not there")
	}
	if !state.Boot {
		missing = append(missing, "nothing puts the rules back after a reboot")
	}
	if len(missing) == 0 {
		return true, fmt.Sprintf("pf sends 443 to %d, so grove serves it without root", Port), ""
	}
	return false, "443 needs root here, and " + strings.Join(missing, ", "), Advice
}
