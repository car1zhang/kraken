package jobs

// Contract slice 3 (standing-agent reminder line): pins the closing
// `## Your pane text` section appended to irisBrief() — its four literals and
// its placement after the brief's previously-last section.

import (
	"strings"
	"testing"
)

var contractPaneTextLiterals = []string{
	"## Your pane text",
	"planning reminder",
	"First privately list what you need next",
	"never echo it, answer it, or narrate it",
}

func contractAssertPaneTextAfter(t *testing.T, name, brief, prevHeading string) {
	t.Helper()
	for _, want := range contractPaneTextLiterals {
		if !strings.Contains(brief, want) {
			t.Errorf("%s lacks %q", name, want)
		}
	}
	prev := strings.Index(brief, prevHeading)
	pane := strings.Index(brief, "## Your pane text")
	if prev < 0 {
		t.Fatalf("%s lost its %q heading", name, prevHeading)
	}
	if pane < 0 {
		return // already reported above
	}
	if pane <= prev {
		t.Errorf("%s: `## Your pane text` (offset %d) is not after %q (offset %d)", name, pane, prevHeading, prev)
	}
	// Appended at the END: no further `## ` heading follows it.
	if tail := brief[pane+len("## Your pane text"):]; strings.Contains(tail, "\n## ") {
		t.Errorf("%s: another `## ` heading follows `## Your pane text`; it must be the closing section", name)
	}
}

func TestContractStandingReminderIris(t *testing.T) {
	contractAssertPaneTextAfter(t, "irisBrief()", irisBrief(), "## Credentials in messages")
}
