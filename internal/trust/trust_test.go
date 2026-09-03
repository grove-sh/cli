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

// systemBundle stands in for the operating system's roots. Pointing at one
// that does not carry grove's root is the macOS shape: the file exists, and
// the keychain install never writes to it.
func systemBundle(t *testing.T, contents string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "system-roots.pem")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GROVE_SYSTEM_BUNDLE", path)
}

func TestMergedBundleCarriesBothSetsOfRoots(t *testing.T) {
	systemBundle(t, "-----BEGIN CERTIFICATE-----\nsystemroot\n-----END CERTIFICATE-----\n")
	dir := t.TempDir()
	root, err := ca.OpenOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}

	path, err := trust.WriteBundle(dir, root.RootPEM())
	if err != nil {
		t.Fatal(err)
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(written), "systemroot") {
		t.Error("the merged bundle dropped the system roots, which would break every other TLS call")
	}
	if !strings.Contains(string(written), string(root.RootPEM())) {
		t.Error("the merged bundle is missing grove's root")
	}
}

func TestBundlePrefersGrovesCopyWhenThereIsOne(t *testing.T) {
	systemBundle(t, "system\n")
	dir := t.TempDir()
	root, err := ca.OpenOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}

	if path, merged := trust.Bundle(dir); merged || path != trust.SystemBundle() {
		t.Errorf("with no copy, Bundle = %q, merged = %v", path, merged)
	}

	if _, err := trust.WriteBundle(dir, root.RootPEM()); err != nil {
		t.Fatal(err)
	}

	path, merged := trust.Bundle(dir)
	if !merged || path != filepath.Join(dir, trust.BundleFile) {
		t.Errorf("with a copy, Bundle = %q, merged = %v", path, merged)
	}
}

// Once the system's own file carries the root, the copy is the only thing that
// can go stale, so it goes away.
func TestRemoveBundleLeavesOneSourceOfTruth(t *testing.T) {
	systemBundle(t, "system\n")
	dir := t.TempDir()
	root, err := ca.OpenOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trust.WriteBundle(dir, root.RootPEM()); err != nil {
		t.Fatal(err)
	}

	if err := trust.RemoveBundle(dir); err != nil {
		t.Fatal(err)
	}
	if _, merged := trust.Bundle(dir); merged {
		t.Error("Bundle still reports a copy")
	}
	// Removing one that is not there is how install behaves on Linux every
	// time, so it cannot be an error.
	if err := trust.RemoveBundle(dir); err != nil {
		t.Errorf("removing a bundle that is gone: %v", err)
	}
}

func TestEnvPointsRuntimesAtTheMergedCopy(t *testing.T) {
	systemBundle(t, "system\n")
	dir := t.TempDir()
	root, err := ca.OpenOrCreate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := trust.WriteBundle(dir, root.RootPEM()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NODE_EXTRA_CA_CERTS", "REQUESTS_CA_BUNDLE", "DENO_CERT"} {
		t.Setenv(name, "")
		os.Unsetenv(name)
	}

	env := trust.Env(dir)

	merged := filepath.Join(dir, trust.BundleFile)
	for _, want := range []string{"REQUESTS_CA_BUNDLE=" + merged, "DENO_CERT=" + merged} {
		if !slices.Contains(env, want) {
			t.Errorf("missing %q from %v", want, env)
		}
	}
	// Node adds to its roots rather than replacing them, so it takes the root
	// on its own.
	if want := "NODE_EXTRA_CA_CERTS=" + filepath.Join(dir, trust.RootFile); !slices.Contains(env, want) {
		t.Errorf("missing %q from %v", want, env)
	}
}
