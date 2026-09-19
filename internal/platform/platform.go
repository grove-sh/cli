// Package platform keeps build tags and GOOS checks out of the rest of grove.
package platform

import "github.com/grove-sh/cli/internal/shell"

// Not 127.0.0.1, so that whatever else wants that address on 443 can have it.
// Only meaningful paired with defaultDomain in cmd/grove, whose wildcard points
// here: change either alone and hostnames resolve somewhere nothing serves.
const Address = "127.0.0.4"

// Whether a process running as you can bind below 1024, which is what serving
// hostnames with no port in them requires.
type PortAccess struct {
	Allowed bool

	Detail string

	// Empty when nothing is left to change. Can be set alongside Allowed: a
	// floor that clears 443 but not 80 is a yes with the redirect out of reach.
	Advice string
}

// Plan is the privileged step a platform needs, as prose to read and commands
// to run. The commands are values rather than text so that what grove offers
// to run is exactly what it printed. An empty Plan means nothing is left to do.
type Plan struct {
	// One line for a status table: what is still in place, or missing.
	Summary string

	// Before the steps: what they do and why.
	Intro string

	Steps []shell.Step

	// After the steps: what to know before saying yes.
	Outro string
}

func (p Plan) Empty() bool { return len(p.Steps) == 0 }
