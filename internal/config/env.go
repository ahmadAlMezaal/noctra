// Package config loads Noctra's runtime configuration from .env and the
// process environment, and exposes a validated Config struct.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// LoadEnvFile reads a KEY=VALUE file (Noctra's .env format).
//
// Lines starting with # are treated as comments; blank lines are skipped.
// Values may optionally be wrapped in single or double quotes (which are
// stripped). A missing file is not an error — an empty map is returned, since
// configuration may also come from the process environment.
func LoadEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			return nil, fmt.Errorf("%s:%d: missing '=' in %q", path, lineNum, line)
		}

		key := strings.TrimSpace(line[:eq])
		val := unquote(strings.TrimSpace(line[eq+1:]))
		out[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
