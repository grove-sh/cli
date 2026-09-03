package platform

// macOS has no equivalent of the unprivileged port floor, and a LaunchAgent
// cannot bind a low port any more than a systemd user unit can. The two real
// answers are a pf redirect, as puma-dev does, or launchd socket activation
// where root binds the port and hands the descriptor over. Neither is written.
func PrivilegedPorts() PortAccess {
	return PortAccess{
		Detail: "macOS has no unprivileged port floor to lower, so 443 needs root",
		Advice: "Until grove installs a pf redirect or a root LaunchDaemon, run the daemon on a high port: grove restart --listen 127.0.0.1:8443",
	}
}

func WSL() bool { return false }
