//go:build !linux && !darwin

package platform

import "runtime"

func PrivilegedPorts() PortAccess {
	return PortAccess{
		Detail: "grove does not know how " + runtime.GOOS + " grants low ports",
		Advice: "Run the daemon on a high port: grove restart --listen " + Address + ":8443",
	}
}

func WSL() bool { return false }

// Nothing to stage on a platform grove knows nothing about.
func PrepareRedirect(string) (string, error) { return "", nil }

// RemoveRedirect has nothing to take back, since nothing was staged.
func RemoveRedirect(string) (string, error) { return "", nil }

// Nothing here knows better, so ask for 443 and report what happens.
func DefaultListen() string { return Address + ":443" }

// Best effort, like 443: nothing breaks when it fails.
func DefaultHTTPListen() string { return Address + ":80" }
