package service

import (
	"strings"
	"testing"
)

func TestUnitFile(t *testing.T) {
	out := unitFile("/home/u/.local/bin/nightshift", "/home/u/.local/bin:/usr/bin")

	for _, want := range []string{
		"ExecStart=/home/u/.local/bin/nightshift run",
		"Environment=PATH=/home/u/.local/bin:/usr/bin",
		"Description=Nightshift — autonomous Linear→PR agent",
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unit file missing %q\n---\n%s", want, out)
		}
	}
}
