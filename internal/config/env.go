// Package config loads Noctra's runtime configuration from .env and the environment into a validated Config.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// LoadEnvFile reads a KEY=VALUE .env file (# comments and blanks skipped, optional surrounding quotes stripped); a missing file yields an empty map, not an error.
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

// PatchEnvFile atomically upserts updates into path's .env: matched keys replaced in-place, new keys appended, comments/blanks/order preserved (mode 0600). The single shared .env writer.
func PatchEnvFile(path string, updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}

	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = splitLines(string(data))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	seen := make(map[string]bool, len(updates))
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		eq := strings.IndexByte(trimmed, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(trimmed[:eq])
		if val, ok := updates[key]; ok {
			lines[i] = key + `="` + val + `"`
			seen[key] = true
		}
	}

	// append unmatched keys, sorted for deterministic output
	var newKeys []string
	for key := range updates {
		if !seen[key] {
			newKeys = append(newKeys, key)
		}
	}
	sort.Strings(newKeys)
	for _, key := range newKeys {
		lines = append(lines, key+`="`+updates[key]+`"`)
	}

	content := strings.Join(lines, "\n")
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return atomicWriteFile(path, []byte(content), 0o600)
}

// atomicWriteFile writes via a temp file + rename to avoid partial writes on crash.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".env.tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}

// splitLines splits s into lines, trimming one trailing newline to avoid a phantom empty element.
func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
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
