package config

import (
	"errors"
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

// Port is zero when nothing has allocated it. Host and URL are empty for a port
// entry, which has no hostname.
type Binding struct {
	Port int
	Host string
	URL  string

	// The entry's extra hostnames, keyed by the label that named them. Each
	// carries only a Host and URL: the port is the entry's own.
	Aliases map[string]Binding
}

type Values struct {
	Context Context
	Routes  map[string]Binding
	Ports   map[string]Binding
}

// [env] first, then the always-active entries, then the entry this command
// binds, so the most specific wins. A variable that cannot be resolved is an
// error: running a command with half its environment is worse than not running.
func (c *Config) Environment(active *Entry, values Values) (map[string]string, error) {
	out, skipped := c.environment(active, values)
	if len(skipped) > 0 {
		return nil, errors.New(skipped[0].Reason)
	}
	return out, nil
}

// Skipped names something that does not exist yet, usually an unleased port.
type Skipped struct {
	Name   string
	Reason string

	// Empty unless this was waiting on an entry: a reference to one that does
	// not exist is a mistake rather than a wait.
	Ref string
}

// UnboundError is a state that ends the moment something holds the entry,
// rather than a mistake in the file. Callers group by Ref, since one unheld
// entry is usually several unset variables.
type UnboundError struct {
	Ref   string
	Field string
}

func (e *UnboundError) Error() string {
	return e.Ref + " has no " + e.Field + " yet; it is only allocated while something binds it"
}

// Printing an environment is not running a command, so a port nobody holds
// costs one variable rather than the whole answer.
func (c *Config) EnvironmentSkipping(active *Entry, values Values) (map[string]string, []Skipped) {
	return c.environment(active, values)
}

func (c *Config) environment(active *Entry, values Values) (map[string]string, []Skipped) {
	out := make(map[string]string)
	var skipped []Skipped

	sources := []struct {
		self *Entry
		env  map[string]string
	}{{nil, c.Env}}
	for _, entry := range c.Detached() {
		sources = append(sources, struct {
			self *Entry
			env  map[string]string
		}{entry, entry.Env})
	}
	if active != nil && !active.Detached {
		sources = append(sources, struct {
			self *Entry
			env  map[string]string
		}{active, active.Env})
	}

	for _, source := range sources {
		skipped = append(skipped, c.resolveInto(out, source.self, source.env, values)...)
	}
	return out, skipped
}

func (c *Config) resolveInto(out map[string]string, self *Entry, env map[string]string, values Values) []Skipped {
	names := make([]string, 0, len(env))
	for name := range env {
		names = append(names, name)
	}
	slices.Sort(names)

	var skipped []Skipped
	for _, name := range names {
		resolved, err := resolve(env[name], self, values)
		if err != nil {
			miss := Skipped{Name: name, Reason: fmt.Sprintf("%s: %v", where(self, name), err)}
			var unbound *UnboundError
			if errors.As(err, &unbound) {
				miss.Ref = unbound.Ref
			}
			skipped = append(skipped, miss)
			continue
		}
		out[name] = resolved
	}
	return skipped
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
			return "", fmt.Errorf("{%s} names the entry being resolved, and [env] has none; use {<name>.%s}", path, parts[0])
		}
		return field(bindingOf(self, values), parts[0], self.Ref(), self.Kind == KindRoute)

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
	}

	// Which section an entry lives in is not part of the reference, or moving
	// one between [routes] and [ports] would break every line naming it. Three
	// parts name one of the entry's aliases, which only a route can have.
	if len(parts) != 2 && len(parts) != 3 {
		return "", fmt.Errorf("unknown token {%s}; a reference is {<name>.port}, .url or .host", path)
	}
	if binding, ok := values.Routes[parts[0]]; ok {
		if len(parts) == 3 {
			return aliasField(binding, parts[0], parts[1], parts[2])
		}
		return field(binding, parts[1], "routes."+parts[0], true)
	}
	if binding, ok := values.Ports[parts[0]]; ok {
		if len(parts) == 3 {
			return "", fmt.Errorf("{%s} reads as an alias of ports.%s, and only routes have hostnames to alias", path, parts[0])
		}
		return field(binding, parts[1], "ports."+parts[0], false)
	}
	return "", fmt.Errorf("{%s} names no entry called %q", path, parts[0])
}

// An alias is another hostname on the entry's own port, so it answers .host and
// .url and nothing else: {web.admin.port} would be {web.port} spelled longer,
// and saying so beats handing back the same number twice.
func aliasField(binding Binding, name, alias, wanted string) (string, error) {
	aliased, ok := binding.Aliases[alias]
	if !ok {
		return "", fmt.Errorf("{%s.%s.%s} names no alias %q on routes.%s%s", name, alias, wanted, alias, name, declares(binding.Aliases))
	}
	switch wanted {
	case "host":
		return aliased.Host, nil
	case "url":
		return aliased.URL, nil
	case "port":
		return "", fmt.Errorf("{%s.%s.port} is {%s.port}: an alias is another hostname on the same port", name, alias, name)
	}
	return "", fmt.Errorf("unknown field %q on alias %q of routes.%s", wanted, alias, name)
}

// A label is lowercased and hyphenated on the way in, so the alias a template
// has to name can differ from the one the file spelled. Listing what is there
// covers that surprise and a plain misspelling in one sentence.
func declares(aliases map[string]Binding) string {
	if len(aliases) == 0 {
		return ", which declares none"
	}
	names := make([]string, 0, len(aliases))
	for name := range aliases {
		names = append(names, name)
	}
	slices.Sort(names)
	return "; it has " + strings.Join(names, ", ")
}

// An empty URL means two things needing different messages: a port can never
// have one, a route has none until something leases it.
func field(binding Binding, name, ref string, routed bool) (string, error) {
	switch name {
	case "port":
		if binding.Port == 0 {
			return "", &UnboundError{Ref: ref, Field: "port"}
		}
		return strconv.Itoa(binding.Port), nil
	case "url":
		if binding.URL == "" {
			if !routed {
				return "", fmt.Errorf("%s has no URL; only routes get a hostname", ref)
			}
			return "", &UnboundError{Ref: ref, Field: "URL"}
		}
		return binding.URL, nil
	case "host":
		if binding.Host == "" {
			if !routed {
				return "", fmt.Errorf("%s has no hostname; only routes get one", ref)
			}
			return "", &UnboundError{Ref: ref, Field: "hostname"}
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

// A typo in a variable grove never sets is the bug this tool exists to prevent,
// so it fails at load rather than at use.
func checkTemplates(cfg *Config) error {
	values := Values{Routes: map[string]Binding{}, Ports: map[string]Binding{}}
	for name, route := range cfg.Routes {
		binding := Binding{Port: 1, Host: "h", URL: "u"}
		for _, alias := range route.Aliases {
			if binding.Aliases == nil {
				binding.Aliases = map[string]Binding{}
			}
			binding.Aliases[alias] = Binding{Host: "h", URL: "u"}
		}
		values.Routes[name] = binding
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
