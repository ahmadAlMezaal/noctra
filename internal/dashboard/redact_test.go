package dashboard

import (
	"strings"
	"testing"
)

func TestRedactor_Literals(t *testing.T) {
	r := NewRedactor([]string{"lin_api_abc123456789012345678901234567890"})
	input := "using key lin_api_abc123456789012345678901234567890 here"
	got := r.Redact(input)
	if strings.Contains(got, "lin_api_abc123456789012345678901234567890") {
		t.Errorf("literal not redacted: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Errorf("expected [REDACTED] in output: %s", got)
	}
}

func TestRedactor_ShortLiteralsIgnored(t *testing.T) {
	r := NewRedactor([]string{"short", ""})
	input := "the word short should remain"
	got := r.Redact(input)
	if !strings.Contains(got, "short") {
		t.Errorf("short literal should not be redacted: %s", got)
	}
}

func TestRedactor_GitHubPAT(t *testing.T) {
	r := NewRedactor(nil)
	input := "token is ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"
	got := r.Redact(input)
	if strings.Contains(got, "ghp_") {
		t.Errorf("GitHub PAT not redacted: %s", got)
	}
}

func TestRedactor_GitHubOAuth(t *testing.T) {
	r := NewRedactor(nil)
	input := "oauth gho_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl"
	got := r.Redact(input)
	if strings.Contains(got, "gho_") {
		t.Errorf("GitHub OAuth token not redacted: %s", got)
	}
}

func TestRedactor_AnthropicKey(t *testing.T) {
	r := NewRedactor(nil)
	input := "key=sk-ant-abcdefghijklmnopqrstuvwxyz"
	got := r.Redact(input)
	if strings.Contains(got, "sk-ant-") {
		t.Errorf("Anthropic key not redacted: %s", got)
	}
}

func TestRedactor_OpenAIKey(t *testing.T) {
	r := NewRedactor(nil)
	input := "OPENAI_API_KEY=sk-abcdefghijklmnopqrstuvwxyz"
	got := r.Redact(input)
	if strings.Contains(got, "sk-abcdefghijklmnopqrstuvwxyz") {
		t.Errorf("OpenAI key not redacted: %s", got)
	}
}

func TestRedactor_BearerToken(t *testing.T) {
	r := NewRedactor(nil)
	input := "Authorization: Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.long.token"
	got := r.Redact(input)
	if strings.Contains(got, "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9") {
		t.Errorf("Bearer token not redacted: %s", got)
	}
}

func TestRedactor_GenericAPIKeyEnvVar(t *testing.T) {
	r := NewRedactor(nil)
	input := `api_key=ABCDEF1234567890abcdef1234`
	got := r.Redact(input)
	if strings.Contains(got, "ABCDEF1234567890abcdef1234") {
		t.Errorf("generic api_key not redacted: %s", got)
	}
}

func TestRedactor_SlackToken(t *testing.T) {
	r := NewRedactor(nil)
	input := "SLACK_TOKEN=xoxb-1234-5678-abcdefgh"
	got := r.Redact(input)
	if strings.Contains(got, "xoxb-") {
		t.Errorf("Slack token not redacted: %s", got)
	}
}

func TestRedactor_GoogleAPIKey(t *testing.T) {
	r := NewRedactor(nil)
	input := "key AIzaSyBabcdefghijklmnopqrstuvwxyz012345 here"
	got := r.Redact(input)
	if strings.Contains(got, "AIzaSy") {
		t.Errorf("Google API key not redacted: %s", got)
	}
}

func TestRedactor_Nil(t *testing.T) {
	var r *Redactor
	got := r.Redact("no-op: ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl")
	if !strings.Contains(got, "ghp_") {
		t.Errorf("nil redactor should be a no-op: %s", got)
	}
}

func TestRedactor_NoFalsePositives(t *testing.T) {
	r := NewRedactor(nil)
	input := "normal log line: processing ENG-42, status ok, 3 retries"
	got := r.Redact(input)
	if got != input {
		t.Errorf("false positive: input changed from %q to %q", input, got)
	}
}

func TestRedactor_LinearAPIKey(t *testing.T) {
	r := NewRedactor(nil)
	input := "LINEAR_API_KEY=lin_api_abcdefghijklmnopqrstuvwxyz123456"
	got := r.Redact(input)
	if strings.Contains(got, "lin_api_") {
		t.Errorf("Linear API key not redacted: %s", got)
	}
}

func TestRedactor_MultiplePatternsInOneLine(t *testing.T) {
	r := NewRedactor([]string{"my-secret-config-value-12345678"})
	input := "token ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijkl and value my-secret-config-value-12345678"
	got := r.Redact(input)
	if strings.Contains(got, "ghp_") {
		t.Errorf("GitHub PAT not redacted: %s", got)
	}
	if strings.Contains(got, "my-secret-config-value-12345678") {
		t.Errorf("literal not redacted: %s", got)
	}
}
