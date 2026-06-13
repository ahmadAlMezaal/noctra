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

func TestRunManual_CopiesEnvTemplate(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, ".env.example"), "FOO=bar\n")

	in := bufio.NewScanner(strings.NewReader(""))
	if err := runManual(dir, in); err != nil {
		t.Fatalf("runManual: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		t.Errorf(".env should have been created: %v", err)
	}
	// Repos are no longer scaffolded from a template — they're declared via the
	// Linear project Repo: directive (repos.json is an optional hand-written
	// fallback). runManual must not create one.
	if _, err := os.Stat(filepath.Join(dir, "repos.json")); err == nil {
		t.Error("repos.json should not be created by runManual")
	}
}

func TestAskInt_EOFPreservesExisting(t *testing.T) {
	// Empty input → fallback used → EOF before second prompt.
	// Existing value "7" should win over factory default 3.
	w := newWizardWithInput("")
	got := w.askInt("Max concurrent", "7", 3, 1)
	if got != 7 {
		t.Errorf("askInt EOF with existing=7 returned %d, want 7", got)
	}
}

func TestAskInt_EOFNoExistingUsesFallback(t *testing.T) {
	w := newWizardWithInput("")
	got := w.askInt("Max concurrent", "", 3, 1)
	if got != 3 {
		t.Errorf("askInt EOF with no existing returned %d, want 3", got)
	}
}

func TestChooseGeminiMode(t *testing.T) {
	w := newWizardWithInput("2\n")
	if got := w.chooseGeminiMode("api"); got != "cli" {
		t.Errorf("chooseGeminiMode returned %q, want cli", got)
	}
}

func TestChooseGeminiModeEOFPreservesCLI(t *testing.T) {
	w := newWizardWithInput("")
	if got := w.chooseGeminiMode("cli"); got != "cli" {
		t.Errorf("chooseGeminiMode EOF returned %q, want cli", got)
	}
}

func TestWriteEnvFileIncludesGeminiMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := writeEnvFile(path, envValues{
		linearKey:    "lin_xyz",
		team:         "ENG",
		agentBackend: "claude",
		triggerMode:  "state",
		trigger:      "Next",
		review:       "In Review",
		mainBranch:   "main",
		concurrency:  "3",
		dispatches:   "10",
		retries:      "3",
		timeoutMin:   "45",
		geminiMode:   "cli",
		tgEnabled:    "false",
		tgVerbose:    "false",
		autoIterate:  "false",
		maxIter:      "3",
		prPoll:       "120",
	}); err != nil {
		t.Fatalf("writeEnvFile: %v", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, `GEMINI_MODE="cli"`) {
		t.Errorf("generated .env missing GEMINI_MODE:\n%s", text)
	}
	if !strings.Contains(text, "run 'gemini' once") {
		t.Errorf("generated .env missing CLI login hint:\n%s", text)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
