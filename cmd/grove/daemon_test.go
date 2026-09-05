package main

import (
	"path/filepath"
	"testing"
)

// A redirected service directory is how grove stays away from the real service
// manager, and the whole reason a test can run this at all. Deferring to it
// there would restart nothing, since an unmanaged systemctl call is a no-op,
// and then wait fifteen seconds for a socket that is not coming.
func TestRestartIgnoresTheServiceWhenItIsNotManaged(t *testing.T) {
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(t.TempDir(), "units"))

	if serviceOwnsDaemon(defaultDaemonOptions()) {
		t.Error("grove would restart a service manager it has been told to stay away from")
	}
}

// Options the unit does not describe are a request for a different daemon, and
// restarting the unit would quietly serve the old ones instead.
func TestRestartSpawnsWhenAskedForSomethingTheUnitIsNot(t *testing.T) {
	t.Setenv("GROVE_SERVICE_DIR", filepath.Join(t.TempDir(), "units"))

	elsewhere := func(change func(*daemonOptions)) daemonOptions {
		opts := defaultDaemonOptions()
		change(&opts)
		return opts
	}
	for _, opts := range []daemonOptions{
		elsewhere(func(o *daemonOptions) { o.listen = "127.0.0.1:9999" }),
		elsewhere(func(o *daemonOptions) { o.socket = "/tmp/elsewhere.sock" }),
		elsewhere(func(o *daemonOptions) { o.domain = "example.test" }),
		elsewhere(func(o *daemonOptions) { o.caDir = "/tmp/elsewhere" }),
	} {
		if serviceOwnsDaemon(opts) {
			t.Errorf("%+v: restarting the unit would ignore what was asked for", opts)
		}
	}
}
