package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// stall_login_test.go — the dead-credential arm of classifyStall: the two
// /login banners classify as Kind "usage-limit" so the limit rescue's
// rebind-and-respawn covers a cell whose bound account cannot log in, which
// no nudge and no reset wait can fix. Written against LastStall, the
// exported reader, like the sibling stall test files.
//
// Every package-level identifier here is prefixed `loginStall`, and the
// fixtures are this file's own rather than another test file's —
// stall_edge_test.go's stance, for its reason: this file must compile on
// its own if its neighbours move.

// loginStallTranscript writes lines to a fresh temp jsonl and returns its
// path.
func loginStallTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "login-stall.jsonl")
	content := strings.Join(lines, "\n")
	if len(lines) > 0 {
		content += "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loginStallSynthetic renders one synthetic assistant record — the record
// shape Claude Code stamps `<synthetic>` on, the only shape this arm reads.
func loginStallSynthetic(text string) string {
	return `{"type":"assistant","message":{"model":"<synthetic>","content":[{"type":"text","text":"` + text + `"}]}}`
}

// loginStallRealModel renders an ordinary (non-synthetic) assistant record.
func loginStallRealModel(text string) string {
	return `{"type":"assistant","message":{"model":"claude-fable-5","content":[{"type":"text","text":"` + text + `"}]}}`
}

// TestLoginStallBannersClassifyAsUsageLimit pins the trigger set: both
// banner shapes Claude Code renders for a dead credential, verbatim, with
// no "api error" prefix — the fixtures carry none because the real banners
// carry none, the exact trap that shipped the usage-limit arm unreachable
// once already.
func TestLoginStallBannersClassifyAsUsageLimit(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		{"login expired", "Login expired · Please run /login"},
		{"not logged in", "Not logged in · Please run /login"},
		{"mixed case", "LOGIN EXPIRED · Please run /login"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LastStall(loginStallTranscript(t, loginStallSynthetic(c.text)))
			if got.Kind != "usage-limit" {
				t.Fatalf("Kind = %q, want %q (got %+v)", got.Kind, "usage-limit", got)
			}
			if got.Transient {
				t.Errorf("Transient = true, want false — a dead credential is not a retryable blip")
			}
		})
	}
}

// TestLoginStallTextCarriesTheBanner pins the honesty half: the Stall's
// Text is the banner itself, because the recorded hit's details — and so
// the notification carl reads — are built from it, and the "usage-limit"
// Kind alone would misname what happened.
func TestLoginStallTextCarriesTheBanner(t *testing.T) {
	const banner = "Login expired · Please run /login"
	got := LastStall(loginStallTranscript(t, loginStallSynthetic(banner)))
	if got.Text != banner {
		t.Fatalf("Text = %q, want the banner verbatim %q", got.Text, banner)
	}
}

// TestLoginStallRequiresASyntheticRecord pins the guard that makes the
// substring markers safe: a real model merely saying "not logged in" — a
// perfectly ordinary sentence in a debugging turn — classifies as nothing.
func TestLoginStallRequiresASyntheticRecord(t *testing.T) {
	for _, text := range []string{
		"Login expired · Please run /login",
		"the curl failed because the test user was not logged in",
	} {
		got := LastStall(loginStallTranscript(t, loginStallRealModel(text)))
		if got.Kind != "" {
			t.Errorf("%q on a real model: Kind = %q, want none", text, got.Kind)
		}
	}
}
