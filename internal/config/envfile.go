package config

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// OverrideName is the tier above everything grove resolves, read from the
// project directory. It is not a grove.toml key: Next, Vite, CRA and Rails
// dotenv all already treat .env.local as the personal override, and their
// gitignore templates already carry it. A key would feed into OverrideFiles.
const OverrideName = ".env.local"

// Grove fills these from the lease it holds, so an override file setting one
// would have grove name a route or a port it does not serve. The same four
// names layer sets in cmd/grove/exec.go.
var ReservedNames = []string{"GROVE_CONTEXT", "GROVE_PORT", "GROVE_HOST", "GROVE_URL"}

// Later files win. A missing one is skipped: .env is usually gitignored, so its
// absence is normal rather than a mistake.
func (c *Config) LoadEnvFiles() (map[string]string, error) {
	out := make(map[string]string)
	for _, name := range c.EnvFiles {
		values, err := c.readOptional(name)
		if err != nil {
			return nil, err
		}
		maps.Copy(out, values)
	}
	return out, nil
}

// OverrideFiles names the files whose values beat grove's own. A project that
// lists .env.local in env_files has already said where it goes, so it stays in
// that lower tier: an upgrade must not change what a file already in use means.
func (c *Config) OverrideFiles() []string {
	if slices.Contains(c.EnvFiles, OverrideName) {
		return nil
	}
	return []string{OverrideName}
}

// LoadOverrideEnvFiles reads the tier nothing else beats.
func (c *Config) LoadOverrideEnvFiles() (map[string]string, error) {
	out := make(map[string]string)
	for _, name := range c.OverrideFiles() {
		values, err := c.readOptional(name)
		if err != nil {
			return nil, err
		}
		// Refused here rather than wherever a name is read, because this is
		// the only tier that could get one of them believed: an env_file
		// setting GROVE_PORT is already beaten by the lease.
		for _, reserved := range ReservedNames {
			if _, forged := values[reserved]; forged {
				return nil, fmt.Errorf("%s: sets %s, which grove fills from the lease it holds; a value here has grove report a route nothing serves. Drop the line, or use a name of your own", name, reserved)
			}
		}
		maps.Copy(out, values)
	}
	return out, nil
}

func (c *Config) readOptional(name string) (map[string]string, error) {
	values, err := readEnvFile(filepath.Join(c.Dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return values, err
}

func readEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	out := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		out[strings.TrimSpace(key)] = unquote(strings.TrimSpace(value))
	}
	return out, scanner.Err()
}

func unquote(value string) string {
	if len(value) < 2 {
		return value
	}
	switch {
	case strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`):
		if unquoted, err := strconv.Unquote(value); err == nil {
			return unquoted
		}
		return value[1 : len(value)-1]
	case strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'"):
		return value[1 : len(value)-1]
	}
	// An unquoted value ends at a trailing comment.
	if comment := strings.Index(value, " #"); comment >= 0 {
		return strings.TrimSpace(value[:comment])
	}
	return value
}
