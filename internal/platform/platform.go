// Package platform keeps build tags and GOOS checks out of the rest of grove.
package platform

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

// Not the address browsers ask for: on macOS 443 arrives through a pf redirect,
// so the daemon binds the port that redirect targets.
