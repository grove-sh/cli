package trust_test

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/ca"
	"github.com/grove-sh/cli/internal/trust"
)

// After a real install the OS trust store rewrites the system bundle, so grove
// keeps no copy. A freshly generated root is not in there.
func TestSystemBundleDoesNotCarryAnUninstalledRoot(t *testing.T) {
	if trust.SystemBundle() == "" {
		t.Skip("this system keeps its roots outside a bundle file")
	}
	root, err := ca.OpenOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if trust.SystemBundleTrusts(root.Certificate()) {
		t.Error("an uninstalled root reported as present in the system bundle")
	}
}

func TestEnvCoversRuntimesThatIgnoreTheOSStore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, trust.RootFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "DENO_CERT"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}

	env := trust.Env(dir)

	if want := "NODE_EXTRA_CA_CERTS=" + filepath.Join(dir, trust.RootFile); !slices.Contains(env, want) {
		t.Errorf("missing %q from %v", want, env)
	}
	if system := trust.SystemBundle(); system != "" {
		for _, name := range []string{"REQUESTS_CA_BUNDLE", "DENO_CERT"} {
			if want := name + "=" + system; !slices.Contains(env, want) {
				t.Errorf("missing %q from %v", want, env)
			}
		}
	}
	// Anything reading the OS trust store already works, so grove stays out of
	// its way.
	for _, entry := range env {
		if strings.HasPrefix(entry, "SSL_CERT_FILE=") || strings.HasPrefix(entry, "CURL_CA_BUNDLE=") {
			t.Errorf("set %q, but the OS store already covers those", entry)
		}
	}
}

func TestEnvLeavesAnExistingValueAlone(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, trust.RootFile), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REQUESTS_CA_BUNDLE", "/corporate/roots.pem")

	env := trust.Env(dir)

	for _, entry := range env {
		if strings.HasPrefix(entry, "REQUESTS_CA_BUNDLE=") {
			t.Errorf("overwrote a deliberate REQUESTS_CA_BUNDLE with %q", entry)
		}
	}
}

func TestEnvOmitsWhatIsNotOnDisk(t *testing.T) {
	env := trust.Env(t.TempDir())

	if len(env) != 0 {
		t.Errorf("env = %v, want nothing without an installed CA", env)
	}
}

func TestFreshRootIsNotTrustedYet(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no system pool check on windows")
	}
	root, err := ca.OpenOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	if trust.Trusted(root.Certificate()) {
		t.Error("a freshly generated root reported as already trusted")
	}
}
