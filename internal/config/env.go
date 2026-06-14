// Package config loads Noctra's runtime configuration from .env and the
// process environment, and exposes a validated Config struct.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// PatchEnvFile atomically upserts the given key-value pairs into the .env
// file at path. Existing lines (comments, blanks, ordering) are preserved;
// only keys present in updates are replaced in-place. Keys in updates that
// don't appear in the file are appended at the end. The result is written
// atomically (temp file + rename) with mode 0600.
//
// Both `noctra config set` and the setup wizard share this writer so
// there's exactly one comment-preserving, atomic .env writer.
func PatchEnvFile(path string, updates map[string]string) error {
	if len(updates) == 0 {
		return nil
	}

	// Read existing lines (if the file exists).
	var lines []string
	if data, err := os.ReadFile(path); err == nil {
		lines = splitLines(string(data))
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", path, err)
	}

	// Track which keys we've already updated in-place.
	seen := make(map[string]bool, len(updates))

	// Walk existing lines; replace values for matched keys.
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

	// Append keys that weren't found in the existing file, sorted
	// alphabetically for deterministic output.
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
	// Ensure trailing newline.
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}

	return atomicWriteFile(path, []byte(content), 0o600)
}

// atomicWriteFile writes data to a temp file alongside path, then renames it
// into place. This avoids partial writes on crash / power loss.
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

// splitLines splits s into lines, preserving empty trailing lines (unlike
// strings.Split which would add a phantom empty element after a trailing \n).
func splitLines(s string) []string {
	// Remove a single trailing newline to avoid a phantom empty line.
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
