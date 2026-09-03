package config_test

import (
	"strings"
	"testing"

	"github.com/grove-sh/cli/internal/config"
)

func values() config.Values {
	return config.Values{
		Context: config.Context{Slug: "myapp-feat1", Project: "myapp", Variant: "feat1"},
		Routes: map[string]config.Binding{
			"web":    {Port: 20101, Host: "myapp-feat1.grov.site", URL: "https://myapp-feat1.grov.site"},
			"admin":  {Port: 20102, Host: "myapp-feat1-admin.grov.site", URL: "https://myapp-feat1-admin.grov.site"},
			"studio": {Port: 20103, Host: "myapp-feat1-studio.grov.site", URL: "https://myapp-feat1-studio.grov.site"},
		},
		Ports: map[string]config.Binding{"db": {Port: 20104}},
	}
}

func TestEnvironmentForADevServer(t *testing.T) {
	cfg := load(t, myappConfig)
	active := cfg.Routes["web"]

	env, err := cfg.Environment(active, values())
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"PORT":                 "20101",
		"NEXT_PUBLIC_SITE_URL": "https://myapp-feat1.grov.site",
		"SUPABASE_STUDIO_PORT": "20103",
		"SUPABASE_DB_PORT":     "20104",
		"SUPABASE_PROJECT_ID":  "myapp-feat1",
		"POSTGRES_URL":         "postgresql://postgres:postgres@127.0.0.1:20104/postgres",
	}
	for name, value := range want {
		if env[name] != value {
			t.Errorf("%s = %q, want %q", name, env[name], value)
		}
	}
	if _, set := env["VITE_SITE_URL"]; set {
		t.Error("the other app's variables leaked in")
	}
}

// Nothing is bound, so only the always-active entries and [env] apply.
func TestEnvironmentWithNothingSelected(t *testing.T) {
	cfg := load(t, myappConfig)

	env, err := cfg.Environment(nil, values())
	if err != nil {
		t.Fatal(err)
	}

	if _, set := env["PORT"]; set {
		t.Error("PORT was set for a command that binds nothing")
	}
	if env["SUPABASE_DB_PORT"] != "20104" {
		t.Errorf("detached ports did not apply: %v", env)
	}
	if env["POSTGRES_URL"] == "" {
		t.Error("POSTGRES_URL needs no lease, since it names a detached port")
	}
}

func TestActiveEntryOverridesTheProjectDefault(t *testing.T) {
	cfg := load(t, `
[env]
PORT = "3000"

[routes.web]
env = { PORT = "{port}" }
`)

	env, err := cfg.Environment(cfg.Routes["web"], config.Values{
		Routes: map[string]config.Binding{"web": {Port: 20200, Host: "h", URL: "u"}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if env["PORT"] != "20200" {
		t.Errorf("PORT = %q, want the bound port to win", env["PORT"])
	}
}

func TestUnallocatedPortIsAnError(t *testing.T) {
	cfg := load(t, `
[routes.web]
env = { PORT = "{port}" }
`)

	_, err := cfg.Environment(cfg.Routes["web"], config.Values{Routes: map[string]config.Binding{}})

	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "routes.web") || !strings.Contains(err.Error(), "PORT") {
		t.Errorf("error names neither the entry nor the variable: %v", err)
	}
}

func TestSelfTokenInProjectEnv(t *testing.T) {
	err := loadErr(t, `
[env]
SITE = "{url}"
`)
	if !strings.Contains(err.Error(), "routes.<name>") {
		t.Errorf("error does not suggest the cross reference form: %v", err)
	}
}

func TestPortsHaveNoHostname(t *testing.T) {
	err := loadErr(t, `
[ports.db]
env = { URL = "{url}" }
`)
	if !strings.Contains(err.Error(), "hostname") {
		t.Errorf("error does not explain it: %v", err)
	}
}

func TestUnknownTokensFailAtLoad(t *testing.T) {
	for _, body := range []string{
		"[env]\nA = \"{prot}\"\n",
		"[env]\nA = \"{context.branch}\"\n",
		"[env]\nA = \"{ports.nope}\"\n",
		"[env]\nA = \"{routes.nope.url}\"\n",
		"[env]\nA = \"{routes.web}\"\n\n[routes.web]\n",
	} {
		t.Run(strings.TrimSpace(body), func(t *testing.T) {
			if err := loadErr(t, body); err == nil {
				t.Error("accepted")
			}
		})
	}
}

// An env value is often JSON or a shell snippet, so a brace that cannot be a
// token stays exactly as written.
func TestBracesThatAreNotTokensAreLiteral(t *testing.T) {
	cfg := load(t, `
[env]
FEATURES = '{"beta":true}'
SHELL_DEFAULT = "${PORT:-3000}"
`)

	env, err := cfg.Environment(nil, values())
	if err != nil {
		t.Fatal(err)
	}

	if env["FEATURES"] != `{"beta":true}` {
		t.Errorf("FEATURES = %q", env["FEATURES"])
	}
	if env["SHELL_DEFAULT"] != "${PORT:-3000}" {
		t.Errorf("SHELL_DEFAULT = %q", env["SHELL_DEFAULT"])
	}
}

func TestCrossRouteReference(t *testing.T) {
	cfg := load(t, `
[routes.web]
env = { PORT = "{port}" }

[routes.api]
env = { PORT = "{port}", WEB_ORIGIN = "{routes.web.url}", WEB_HOST = "{routes.web.host}" }
`)

	env, err := cfg.Environment(cfg.Routes["api"], values2())
	if err != nil {
		t.Fatal(err)
	}

	if env["WEB_ORIGIN"] != "https://web.grov.site" || env["WEB_HOST"] != "web.grov.site" {
		t.Errorf("cross reference resolved to %q and %q", env["WEB_ORIGIN"], env["WEB_HOST"])
	}
}

func values2() config.Values {
	return config.Values{Routes: map[string]config.Binding{
		"web": {Port: 1, Host: "web.grov.site", URL: "https://web.grov.site"},
		"api": {Port: 2, Host: "api.grov.site", URL: "https://api.grov.site"},
	}}
}
