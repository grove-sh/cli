//go:build !linux && !darwin

package platform

import "runtime"

func PrivilegedPorts() PortAccess {
	return PortAccess{
		Detail: "grove does not know how " + runtime.GOOS + " grants low ports",
		Advice: "Run the daemon on a high port: grove restart --listen 127.0.0.1:8443",
	}
}

func WSL() bool { return false }

// PrepareRedirect has nothing to stage here, since lowering the port floor is
// a sysctl rather than a pair of files. The static advice stands.
func PrepareRedirect(string) (string, error) { return "", nil }

// DefaultListen is where the daemon serves. Nothing here knows better, so ask for 443 and report what happens.
func DefaultListen() string { return "127.0.0.1:443" }
