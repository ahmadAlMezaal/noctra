package agent

import "testing"

func TestParseUsage_CodexTokensUsed(t *testing.T) {
	output := "some output here\n\ntokens used: 167,195\n"
	u := ParseUsage(output)
	if u.TotalTokens != 167195 {
		t.Errorf("TotalTokens: got %d, want 167195", u.TotalTokens)
	}
}

func TestParseUsage_InputOutputTokens(t *testing.T) {
	output := "Input tokens: 50,000\nOutput tokens: 12,345\n"
	u := ParseUsage(output)
	if u.InputTokens != 50000 {
		t.Errorf("InputTokens: got %d, want 50000", u.InputTokens)
	}
	if u.OutputTokens != 12345 {
		t.Errorf("OutputTokens: got %d, want 12345", u.OutputTokens)
	}
	if u.TotalTokens != 62345 {
		t.Errorf("TotalTokens (sum): got %d, want 62345", u.TotalTokens)
	}
}

func TestParseUsage_ExplicitTotal(t *testing.T) {
	output := "Input tokens: 50,000\nOutput tokens: 12,345\nTotal tokens: 70,000\n"
	u := ParseUsage(output)
	if u.TotalTokens != 70000 {
		t.Errorf("TotalTokens (explicit): got %d, want 70000", u.TotalTokens)
	}
}

func TestParseUsage_Cost(t *testing.T) {
	output := "Total cost: $1.23\n"
	u := ParseUsage(output)
	if u.CostUSD != 1.23 {
		t.Errorf("CostUSD: got %f, want 1.23", u.CostUSD)
	}
}

func TestParseUsage_CostWithComma(t *testing.T) {
	output := "Cost: $1,234.56\n"
	u := ParseUsage(output)
	if u.CostUSD != 1234.56 {
		t.Errorf("CostUSD: got %f, want 1234.56", u.CostUSD)
	}
}

func TestParseUsage_NoUsageInfo(t *testing.T) {
	output := "Agent completed work on ENG-42.\nAll tests pass.\n"
	u := ParseUsage(output)
	if u.TotalTokens != 0 || u.InputTokens != 0 || u.OutputTokens != 0 || u.CostUSD != 0 {
		t.Errorf("expected zero usage for output without usage info, got %+v", u)
	}
}

func TestParseUsage_CaseInsensitive(t *testing.T) {
	output := "TOKENS USED: 42,000\nTOTAL COST: $5.00\n"
	u := ParseUsage(output)
	if u.TotalTokens != 42000 {
		t.Errorf("TotalTokens: got %d, want 42000", u.TotalTokens)
	}
	if u.CostUSD != 5.0 {
		t.Errorf("CostUSD: got %f, want 5.0", u.CostUSD)
	}
}

func TestParseCommaInt(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"167195", 167195},
		{"167,195", 167195},
		{"1,234,567", 1234567},
		{"0", 0},
		{"", 0},
		{"abc", 0},
	}
	for _, tt := range tests {
		got := parseCommaInt(tt.input)
		if got != tt.want {
			t.Errorf("parseCommaInt(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}
