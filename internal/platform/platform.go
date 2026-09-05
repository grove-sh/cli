// Package platform answers the questions whose answers differ per operating
// system, so the rest of grove can stay free of build tags and GOOS checks.
package platform

// PortAccess describes whether a process running as you can bind a port below
// 1024, which is what serving hostnames without a port in them requires.
type PortAccess struct {
	Allowed bool

	// Detail is one line on how things stand, whatever the answer.
	Detail string

	// Advice is how to change it, and is empty when nothing is left to change.
	// It can be set alongside Allowed: a floor that clears 443 but not 80 is a
	// yes with the http redirect still out of reach.
	Advice string
}

// Loopback is the address the daemon serves on, which is not the same as the
// address browsers ask for. On macOS 443 arrives through a pf redirect, so the
// daemon binds the port that redirect targets.
