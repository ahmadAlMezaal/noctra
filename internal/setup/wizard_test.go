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
	// Manual setup only copies the .env template; repos are declared via the
	// Linear project Repo: directive, so no registry file is created.
	if _, err := os.Stat(filepath.Join(dir, "registry.json")); err == nil {
		t.Error("manual setup should not create a registry file")
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

func TestMergeEnvFile_PreservesUnknownKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	// Simulate a .env with a hand-added key (LINEAR_OAUTH_TOKEN) the wizard
	// doesn't manage. This is the exact scenario that ENG-214 fixes.
	original := `# Generated by ./noctra setup
LINEAR_API_KEY="lin_old_key"
LINEAR_TEAM_KEY="ENG"
LINEAR_OAUTH_TOKEN="lin_oauth_secret_token"
AGENT_BACKEND="claude"
TRIGGER_MODE="state"
TRIGGER_STATE="Next"
IN_REVIEW_STATE="In Review"
CUSTOM_SETTING="keep_me"
`
	writeTestFile(t, path, original)

	// Run mergeEnvFile with updated wizard values.
	vals := envValues{
		linearKey:    "lin_new_key",
		team:         "ENG",
		agentBackend: "codex",
		triggerMode:  "state",
		trigger:      "Backlog",
		review:       "In Review",
		mainBranch:   "main",
		concurrency:  "5",
		dispatches:   "10",
		retries:      "3",
		timeoutMin:   "45",
		geminiMode:   "api",
		tgEnabled:    "false",
		tgVerbose:    "false",
		autoIterate:  "false",
		maxIter:      "3",
		prPoll:       "120",
	}
	if err := mergeEnvFile(path, vals); err != nil {
		t.Fatalf("mergeEnvFile: %v", err)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)

	// Wizard-managed keys should be updated.
	if !strings.Contains(text, `LINEAR_API_KEY="lin_new_key"`) {
		t.Errorf("LINEAR_API_KEY not updated:\n%s", text)
	}
	if !strings.Contains(text, `AGENT_BACKEND="codex"`) {
		t.Errorf("AGENT_BACKEND not updated:\n%s", text)
	}
	if !strings.Contains(text, `TRIGGER_STATE="Backlog"`) {
		t.Errorf("TRIGGER_STATE not updated:\n%s", text)
	}

	// Hand-added keys must be preserved — this is the critical check.
	if !strings.Contains(text, `LINEAR_OAUTH_TOKEN="lin_oauth_secret_token"`) {
		t.Errorf("LINEAR_OAUTH_TOKEN was lost during merge:\n%s", text)
	}
	if !strings.Contains(text, `CUSTOM_SETTING="keep_me"`) {
		t.Errorf("CUSTOM_SETTING was lost during merge:\n%s", text)
	}

	// Comments should be preserved.
	if !strings.Contains(text, "# Generated by") {
		t.Errorf("header comment was lost:\n%s", text)
	}
}

func TestEnvValues_ToMap(t *testing.T) {
	v := envValues{
		linearKey:    "lin_key",
		team:         "ENG",
		agentBackend: "claude",
		triggerMode:  "label",
		triggerLabel: "noctra",
		review:       "In Review",
		mainBranch:   "main",
		repoPath:     "/some/path",
		concurrency:  "3",
		dispatches:   "10",
		retries:      "3",
		timeoutMin:   "45",
		geminiMode:   "api",
		geminiKey:    "gk_abc",
		tgEnabled:    "true",
		tgToken:      "bot123",
		tgChat:       "chat456",
		tgVerbose:    "false",
		autoIterate:  "true",
		maxIter:      "5",
		prPoll:       "60",
		trusted:      "bot-a,bot-b",
	}

	m := v.toMap()

	// Check a few key entries.
	if m["LINEAR_API_KEY"] != "lin_key" {
		t.Errorf("LINEAR_API_KEY: %q", m["LINEAR_API_KEY"])
	}
	if m["TRIGGER_LABEL"] != "noctra" {
		t.Errorf("TRIGGER_LABEL: %q", m["TRIGGER_LABEL"])
	}
	// In label mode, TRIGGER_STATE should not be set.
	if _, ok := m["TRIGGER_STATE"]; ok {
		t.Error("TRIGGER_STATE should not be in map for label mode")
	}
	if m["REPO_PATH"] != "/some/path" {
		t.Errorf("REPO_PATH: %q", m["REPO_PATH"])
	}
}

func TestEnvValues_ToMap_StateModeNoRepoPath(t *testing.T) {
	v := envValues{
		triggerMode: "state",
		trigger:     "Next",
	}
	m := v.toMap()

	if _, ok := m["TRIGGER_LABEL"]; ok {
		t.Error("TRIGGER_LABEL should not be in map for state mode")
	}
	if m["TRIGGER_STATE"] != "Next" {
		t.Errorf("TRIGGER_STATE: %q", m["TRIGGER_STATE"])
	}
	// Empty repoPath should not appear.
	if _, ok := m["REPO_PATH"]; ok {
		t.Error("REPO_PATH should not be in map when empty")
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
