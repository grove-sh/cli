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
