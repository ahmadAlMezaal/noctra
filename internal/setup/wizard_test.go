package setup

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMask(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "(unset)"},
		{"abc", "***"},
		{"abcdefgh", "********"},
		{"lin_api_test", "lin_…test"},
		{"lin_api_long_key_abc12345", "lin_…2345"},
	}
	for _, c := range cases {
		if got := mask(c.in); got != c.want {
			t.Errorf("mask(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMaskOrNone(t *testing.T) {
	if got := maskOrNone(""); got != "(disabled)" {
		t.Errorf("maskOrNone empty = %q", got)
	}
	if got := maskOrNone("lin_api_test"); got != "lin_…test" {
		t.Errorf("maskOrNone non-empty = %q", got)
	}
}

func TestYes(t *testing.T) {
	cases := map[string]bool{
		"y": true, "Y": true, "yes": true, "Yes": true, " YES ": true,
		"": false, "n": false, "no": false, "anything else": false,
	}
	for in, want := range cases {
		if got := yes(in); got != want {
			t.Errorf("yes(%q) = %v, want %v", in, got, want)
		}
	}
}

// newWizardWithInput builds a wizard reading from a string — useful for
// driving the prompt helpers from tests.
func newWizardWithInput(s string) *wizard {
	return &wizard{in: bufio.NewScanner(strings.NewReader(s))}
}

func TestAskEx_EnterKeepsExisting(t *testing.T) {
	w := newWizardWithInput("\n") // user just hits Enter
	got := w.askEx("API key", askOpts{existing: "lin_secret_abcdef"})
	if got != "lin_secret_abcdef" {
		t.Errorf("got %q, want existing value", got)
	}
}

func TestAskEx_FallbackWhenNoExisting(t *testing.T) {
	w := newWizardWithInput("\n")
	got := w.askEx("Trigger state", askOpts{fallback: "Next"})
	if got != "Next" {
		t.Errorf("got %q, want %q", got, "Next")
	}
}

func TestAskEx_InputReplacesExisting(t *testing.T) {
	w := newWizardWithInput("lin_brand_new_value\n")
	got := w.askEx("API key", askOpts{existing: "lin_old"})
	if got != "lin_brand_new_value" {
		t.Errorf("got %q, want %q", got, "lin_brand_new_value")
	}
}

func TestAskEx_RequiredTerminatesOnEOF(t *testing.T) {
	w := newWizardWithInput("\n\n") // two empty lines, then EOF
	got := w.askEx("API key", askOpts{required: true})
	if !w.eof {
		t.Fatal("expected EOF flag to be set after stdin runs out")
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestAskEx_RequiredAcceptsNonEmpty(t *testing.T) {
	w := newWizardWithInput("\nvalue\n") // empty first, then a value
	got := w.askEx("API key", askOpts{required: true})
	if got != "value" {
		t.Errorf("got %q, want %q", got, "value")
	}
}

func TestRunManual_CopiesTemplates(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, ".env.example"), "FOO=bar\n")
	writeTestFile(t, filepath.Join(dir, "repos.example.json"), "{\"repos\": {}}\n")

	// stdin is unused when no overwrite prompt fires (no existing .env/repos.json)
	stashedStdin := os.Stdin
	defer func() { os.Stdin = stashedStdin }()
	r, _, _ := os.Pipe()
	os.Stdin = r
	r.Close()

	if err := runManual(dir); err != nil {
		t.Fatalf("runManual: %v", err)
	}

	for _, name := range []string{".env", "repos.json"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s should have been created: %v", name, err)
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
