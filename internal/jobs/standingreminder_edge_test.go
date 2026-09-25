package jobs

// Edge tests for contract slice 3, "standing-agent reminder line": the
// `## Your pane text` section appended at the END of kronosBrief() (after
// `## When in doubt, escalate`) and of irisBrief() (after `## Credentials in
// messages`), plus kronos' unchanged 16000-byte size cap.

import (
	"strings"
	"testing"
)

const edgeReminderHeading = "## Your pane text"

var edgeReminderLiterals = []string{
	"planning reminder",
	"First privately list what you need next",
	"never echo it, answer it, or narrate it",
}

// edgeReminderCheck asserts the section's shape in one brief: the heading
// appears exactly once, at a line start, strictly after `after`; it is the
// LAST `## ` heading (appended at the end); and the three body literals sit
// inside it, not somewhere earlier in the brief.
func edgeReminderCheck(t *testing.T, name, brief, after string) {
	t.Helper()
	if n := strings.Count(brief, edgeReminderHeading); n != 1 {
		t.Fatalf("%s: %q appears %d times, want exactly 1", name, edgeReminderHeading, n)
	}
	h := strings.Index(brief, "\n"+edgeReminderHeading)
	if h < 0 {
		t.Fatalf("%s: %q is not at the start of a line", name, edgeReminderHeading)
	}
	h++
	a := strings.Index(brief, after)
	if a < 0 {
		t.Fatalf("%s: lost the preceding heading %q", name, after)
	}
	if h <= a {
		t.Errorf("%s: %q at %d does not come after %q at %d", name, edgeReminderHeading, h, after, a)
	}
	if next := strings.Index(brief[h+len(edgeReminderHeading):], "\n## "); next >= 0 {
		t.Errorf("%s: a heading follows %q — the section must be appended at the END: %q",
			name, edgeReminderHeading, brief[h+len(edgeReminderHeading)+next:])
	}
	section := brief[h:]
	for _, lit := range edgeReminderLiterals {
		if !strings.Contains(section, lit) {
			t.Errorf("%s: literal %q is not inside the %q section", name, lit, edgeReminderHeading)
		}
	}
	if strings.TrimSpace(section) == edgeReminderHeading {
		t.Errorf("%s: %q section has no body", name, edgeReminderHeading)
	}
}

// TestEdgeKronosReminderSectionIsLastAfterEscalate pins kronos' section shape.
func TestEdgeKronosReminderSectionIsLastAfterEscalate(t *testing.T) {
	edgeKronosEnv(t)
	edgeReminderCheck(t, "kronosBrief", kronosBrief(), "## When in doubt, escalate")
}

// TestEdgeIrisReminderSectionIsLastAfterCredentials pins iris' section shape.
// irisBrief reads no environment, but HOME is sandboxed anyway so a regression
// that makes it read one can never touch the real ~/.kraken.
func TestEdgeIrisReminderSectionIsLastAfterCredentials(t *testing.T) {
	useTestStore(t)
	edgeReminderCheck(t, "irisBrief", irisBrief(), "## Credentials in messages")
}

// TestEdgeReminderSectionIdenticalLiteralsInBoth: the two briefs carry the
// same four literals verbatim — case and punctuation included — so a reword
// in one standing agent cannot drift from the other unnoticed.
func TestEdgeReminderSectionIdenticalLiteralsInBoth(t *testing.T) {
	edgeKronosEnv(t)
	briefs := map[string]string{"kronosBrief": kronosBrief(), "irisBrief": irisBrief()}
	for name, b := range briefs {
		for _, lit := range append([]string{edgeReminderHeading}, edgeReminderLiterals...) {
			if !strings.Contains(b, lit) {
				t.Errorf("%s lacks verbatim %q", name, lit)
			}
		}
	}
}

// TestEdgeKronosReminderBriefUnderSizeCap: appending the section must keep
// kronos' brief within the 16000-byte cap.
func TestEdgeKronosReminderBriefUnderSizeCap(t *testing.T) {
	edgeKronosEnv(t)
	if n := len(kronosBrief()); n > 16000 {
		t.Errorf("len(kronosBrief()) = %d, want <= 16000", n)
	}
}

// TestEdgeIrisReminderKeepsPreexistingObligations: the pre-existing iris
// obligations hold after the append — the three obligation clauses the
// existing iris test pins, and no instruction to queue or start a job.
func TestEdgeIrisReminderKeepsPreexistingObligations(t *testing.T) {
	useTestStore(t)
	brief := irisBrief()
	for _, want := range []string{"you never start", "kraken slack ack", "## Source"} {
		if !strings.Contains(brief, want) {
			t.Errorf("irisBrief() lost pre-existing obligation %q", want)
		}
	}
	for _, bad := range []string{"kraken cell submit", "/api/queue"} {
		if strings.Contains(brief, bad) {
			t.Errorf("irisBrief() now names %q — iris never authorizes queueing", bad)
		}
	}
}
