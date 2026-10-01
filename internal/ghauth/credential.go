package ghauth

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"time"
)

func ParseCredentialInput(r io.Reader) (map[string]string, error) {
	attrs := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		attrs[k] = v
	}
	return attrs, sc.Err()
}

func RepoFromCredential(attrs map[string]string) (string, error) {
	if attrs["protocol"] != "https" {
		return "", fmt.Errorf("only https remotes are supported (got %q)", attrs["protocol"])
	}
	if !strings.EqualFold(attrs["host"], "github.com") {
		return "", fmt.Errorf("only github.com is supported (got %q)", attrs["host"])
	}
	parts := strings.Split(strings.Trim(attrs["path"], "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("git did not send the repository path; credential.useHttpPath must be true")
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), nil
}

func FormatCredential(token string, expiresAt time.Time) string {
	var b strings.Builder
	b.WriteString("username=x-access-token\n")
	b.WriteString("password=" + token + "\n")
	if !expiresAt.IsZero() {
		fmt.Fprintf(&b, "password_expiry_utc=%d\n", expiresAt.Unix())
	}
	return b.String()
}
