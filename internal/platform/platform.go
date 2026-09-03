// Package platform answers the questions whose answers differ per operating
// system, so the rest of grove can stay free of build tags and GOOS checks.
package platform

// PortAccess describes whether a process running as you can bind a port below
// 1024, which is what serving hostnames without a port in them requires.
type PortAccess struct {
	Allowed bool

	// Detail is one line on how things stand, whatever the answer.
	Detail string

	// Advice is how to change it, and is empty when nothing needs changing.
	Advice string
}
