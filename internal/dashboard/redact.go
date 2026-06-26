package dashboard

import (
	"regexp"
	"strings"
)

var secretPatterns = []*regexp.Regexp{
	// Generic API key / token patterns (hex, base64, alphanumeric 20+ chars)
	regexp.MustCompile(`(?i)(api[_-]?key|api[_-]?token|auth[_-]?token|secret[_-]?key|access[_-]?token|bearer)\s*[=:]\s*["']?([A-Za-z0-9/+=_-]{20,})["']?`),

	// GitHub tokens (classic PATs, fine-grained, OAuth, app tokens)
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`gho_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`ghs_[A-Za-z0-9]{36,}`),
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{22,}`),

	// Linear API keys
	regexp.MustCompile(`lin_api_[A-Za-z0-9]{30,}`),

	// Slack tokens and webhooks
	regexp.MustCompile(`xox[bpsar]-[A-Za-z0-9-]{10,}`),

	// Generic long hex secrets (32+ hex chars, e.g. SHA tokens)
	regexp.MustCompile(`(?i)(token|secret|key|password|credential)\s*[=:]\s*["']?([0-9a-f]{32,})["']?`),

	// Bearer tokens in headers
	regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9/+=_.-]{20,}`),

	// Anthropic API keys
	regexp.MustCompile(`sk-ant-[A-Za-z0-9-]{20,}`),

	// OpenAI API keys
	regexp.MustCompile(`sk-[A-Za-z0-9]{20,}`),

	// Gemini / Google API keys
	regexp.MustCompile(`AIza[A-Za-z0-9_-]{35}`),
}

type Redactor struct {
	literals []string
}

// NewRedactor builds a redactor from literal secret values (short strings < 8 chars are skipped).
func NewRedactor(literals []string) *Redactor {
	var filtered []string
	for _, v := range literals {
		if len(v) >= 8 {
			filtered = append(filtered, v)
		}
	}
	return &Redactor{literals: filtered}
}

func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	for _, lit := range r.literals {
		if strings.Contains(s, lit) {
			s = strings.ReplaceAll(s, lit, "[REDACTED]")
		}
	}
	for _, pat := range secretPatterns {
		s = pat.ReplaceAllStringFunc(s, func(match string) string {
			return "[REDACTED]"
		})
	}
	return s
}
