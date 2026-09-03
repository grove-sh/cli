package config

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A token is {name} or {group.name} or {routes.name.field}. The strict opening
// character keeps JSON and shell braces in an env value literal.
var token = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_.-]*)\}`)

type Context struct {
	Slug    string
	Project string
	Variant string
}

// A Binding is what an entry resolved to once its port was allocated. Port is
// zero when nothing has allocated it, and Host and URL are empty for a port
// entry, which has no hostname.
type Binding struct {
	Port int
	Host string
	URL  string
}

type Values struct {
	Context Context
	Routes  map[string]Binding
	Ports   map[string]Binding
}

// Environment resolves every template into the variables a command should run
// with: the project's own [env] first, then the entries that are always active,
// then whichever entry this command binds, so the most specific wins.
func (c *Config) Environment(active *Entry, values Values) (map[string]string, error) {
	out := make(map[string]string)

	if err := c.resolveInto(out, nil, c.Env, values); err != nil {
		return nil, err
	}
	for _, entry := range c.Detached() {
		if err := c.resolveInto(out, entry, entry.Env, values); err != nil {
			return nil, err
		}
	}
	if active != nil && !active.Detached {
		if err := c.resolveInto(out, active, active.Env, values); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (c *Config) resolveInto(out map[string]string, self *Entry, env map[string]string, values Values) error {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		resolved, err := resolve(env[name], self, values)
		if err != nil {
			return fmt.Errorf("%s: %w", where(self, name), err)
		}
		out[name] = resolved
	}
	return nil
}

func resolve(template string, self *Entry, values Values) (string, error) {
	var failure error
	out := token.ReplaceAllStringFunc(template, func(match string) string {
		value, err := lookup(match[1:len(match)-1], self, values)
		if err != nil && failure == nil {
			failure = err
		}
		return value
	})
	return out, failure
}

func lookup(path string, self *Entry, values Values) (string, error) {
	parts := strings.Split(path, ".")

	switch parts[0] {
	case "port", "url", "host":
		if len(parts) != 1 {
			return "", fmt.Errorf("unknown token {%s}", path)
		}
		if self == nil {
			return "", fmt.Errorf("{%s} names the entry being resolved, and [env] has none; use {routes.<name>.%s}", path, parts[0])
		}
		return field(bindingOf(self, values), parts[0], self.Ref())

	case "context":
		if len(parts) != 2 {
			return "", fmt.Errorf("unknown token {%s}", path)
		}
		switch parts[1] {
		case "slug":
			return values.Context.Slug, nil
		case "project":
			return values.Context.Project, nil
		case "variant":
			return values.Context.Variant, nil
		}
		return "", fmt.Errorf("unknown token {%s}", path)

	case "ports":
		if len(parts) != 2 {
			return "", fmt.Errorf("unknown token {%s}; a port has no fields, write {ports.%s}", path, parts[1])
		}
		binding, ok := values.Ports[parts[1]]
		if !ok {
			return "", fmt.Errorf("{%s} names no [ports.%s]", path, parts[1])
		}
		return field(binding, "port", "ports."+parts[1])

	case "routes":
		if len(parts) != 3 {
			return "", fmt.Errorf("unknown token {%s}; write {routes.<name>.port}, .url or .host", path)
		}
		binding, ok := values.Routes[parts[1]]
		if !ok {
			return "", fmt.Errorf("{%s} names no [routes.%s]", path, parts[1])
		}
		return field(binding, parts[2], "routes."+parts[1])
	}

	return "", fmt.Errorf("unknown token {%s}", path)
}

func field(binding Binding, name, ref string) (string, error) {
	switch name {
	case "port":
		if binding.Port == 0 {
			return "", fmt.Errorf("%s has no port yet; it is only allocated while something binds it", ref)
		}
		return strconv.Itoa(binding.Port), nil
	case "url":
		if binding.URL == "" {
			return "", fmt.Errorf("%s has no URL; only routes get a hostname", ref)
		}
		return binding.URL, nil
	case "host":
		if binding.Host == "" {
			return "", fmt.Errorf("%s has no hostname; only routes get one", ref)
		}
		return binding.Host, nil
	}
	return "", fmt.Errorf("unknown field %q on %s", name, ref)
}

func bindingOf(entry *Entry, values Values) Binding {
	if entry.Kind == KindRoute {
		return values.Routes[entry.Name]
	}
	return values.Ports[entry.Name]
}

// checkTemplates catches what can be known without running anything: bad token
// syntax, references to entries that do not exist, and hostname fields asked of
// a port. A typo in a variable grove never sets is the bug this tool exists to
// prevent, so it fails at load rather than at use.
func checkTemplates(cfg *Config) error {
	values := Values{Routes: map[string]Binding{}, Ports: map[string]Binding{}}
	for name := range cfg.Routes {
		values.Routes[name] = Binding{Port: 1, Host: "h", URL: "u"}
	}
	for name := range cfg.Ports {
		values.Ports[name] = Binding{Port: 1}
	}

	sources := []struct {
		self *Entry
		env  map[string]string
	}{{nil, cfg.Env}}
	for _, entry := range cfg.All() {
		sources = append(sources, struct {
			self *Entry
			env  map[string]string
		}{entry, entry.Env})
	}

	for _, source := range sources {
		for name, template := range source.env {
			if _, err := resolve(template, source.self, values); err != nil {
				return fmt.Errorf("%s: %w", where(source.self, name), err)
			}
		}
	}
	return nil
}

func where(self *Entry, name string) string {
	if self == nil {
		return "[env] " + name
	}
	return "[" + self.Ref() + "] " + name
}
