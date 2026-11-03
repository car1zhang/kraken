package core

// redactgoogle_contract_test.go — athena-logic's BLIND contract tests for
// slice C of dream-2026-10-30: the transcript redactor covers Google API key
// shapes (redactSecrets). Authored from artifacts/50-contract.md alone. All
// key-shaped values are invented.
//
// LOGIC dimension. Every package-level identifier is prefixed
// `contractRedactGoogle`.

import (
	"strings"
	"testing"
)

// 35 invented characters from [0-9A-Za-z_-]
const contractRedactGoogleAIzaTail = "Zq3_fake-Key0000inventedXYZ12345abc"

// 32 invented characters from [A-Za-z0-9_-]
const contractRedactGoogleAQTail = "Ab9_invented-oauth-tok0000000ZZZ"

func TestContractRedactGoogleShapes(t *testing.T) {
	if len(contractRedactGoogleAIzaTail) != 35 {
		t.Fatalf("fixture: AIza tail is %d chars, want 35", len(contractRedactGoogleAIzaTail))
	}
	if len(contractRedactGoogleAQTail) < 30 {
		t.Fatalf("fixture: AQ. tail is %d chars, want >= 30", len(contractRedactGoogleAQTail))
	}
	aiza := "AIza" + contractRedactGoogleAIzaTail
	aq := "AQ." + contractRedactGoogleAQTail
	aq30 := "AQ." + strings.Repeat("k", 30)
	cases := []struct{ in, want string }{
		// new shapes, redacted to first 5 runes + …[redacted]
		{"key " + aiza + " end", "key AIzaZ…[redacted] end"},
		{aiza, "AIzaZ…[redacted]"},
		{"tok " + aq + " end", "tok AQ.Ab…[redacted] end"},
		{aq30, "AQ.kk…[redacted]"},
		// word boundary: prefixed lookalikes untouched
		{"FAQ." + strings.Repeat("k", 30), "FAQ." + strings.Repeat("k", 30)},
		{"xAIza" + contractRedactGoogleAIzaTail, "xAIza" + contractRedactGoogleAIzaTail},
		// prose untouched
		{"the AIza prefix marks a browser key", "the AIza prefix marks a browser key"},
		{"see FAQ. for details", "see FAQ. for details"},
		// existing shapes keep their answers
		{"sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaa done", "sk-an…[redacted] done"},
		{"key AKIAIOSFODNN7EXAMPLE end", "key AKIAI…[redacted] end"},
		{"use sk-learn and deriveState()", "use sk-learn and deriveState()"},
		{"plain prose about a session token and a cookie", "plain prose about a session token and a cookie"},
	}
	for _, c := range cases {
		if got := redactSecrets(c.in); got != c.want {
			t.Errorf("redactSecrets(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
