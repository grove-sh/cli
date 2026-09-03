package daemon

import (
	"net"
	"os"
)

// Ready tells a service manager the proxy is actually listening, rather than
// merely spawned, so a start command can block until grove can serve. Nothing
// happens when the process was not started by one.
func Ready() {
	notify("READY=1")
}

func Stopping() {
	notify("STOPPING=1")
}

func notify(payload string) {
	socket := os.Getenv("NOTIFY_SOCKET")
	if socket == "" {
		return
	}
	// A leading @ means an abstract socket, which Go spells with a NUL.
	if socket[0] == '@' {
		socket = "\x00" + socket[1:]
	}

	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socket, Net: "unixgram"})
	if err != nil {
		return
	}
	defer conn.Close()
	conn.Write([]byte(payload))
}
