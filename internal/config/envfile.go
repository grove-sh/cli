package config

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// EnvFiles reads the project's env_files in order, later files winning. A file
// that is not there is skipped: .env is usually gitignored, so its absence is
// normal rather than a mistake.
func (c *Config) LoadEnvFiles() (map[string]string, error) {
	out := make(map[string]string)
	for _, name := range c.EnvFiles {
		values, err := readEnvFile(filepath.Join(c.Dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, err
		}
		for key, value := range values {
			out[key] = value
		}
	}
	return out, nil
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
