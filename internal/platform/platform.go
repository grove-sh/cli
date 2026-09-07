// Package platform keeps build tags and GOOS checks out of the rest of grove.
package platform

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
