// Package platform answers the questions whose answers differ per operating
// system, so the rest of grove can stay free of build tags and GOOS checks.
package platform

// PortAccess describes whether a process running as you can bind a port below
// 1024, which is what serving hostnames without a port in them requires.
type PortAccess struct {
	// Allowed means grove can take the port as it stands.
	Allowed bool

	// Detail says what the situation is, in one line.
	Detail string

	// Advice says how to change it, and is empty when nothing needs changing.
	Advice string
}
