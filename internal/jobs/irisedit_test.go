package jobs

import (
	"strings"
	"testing"
)

// irisedit_test.go pins what iris' brief says about EDITING a draft. The
// server's half — that the text `kraken job raw` prints is accepted back —
// is pinned in internal/api's jobdraft_roundtrip_test.go.

// iriseditBrief returns the brief under a throwaway HOME. irisBrief reads no
// path today; the sandbox only keeps a future one off the real ~/.kraken.
func iriseditBrief(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return irisBrief()
}

// TestIrisBriefEditStartsFromRawText: the edit rule is present, names both
// halves of the failure it exists for, and lives in the section about what
// iris may trust instead of its own memory.
func TestIrisBriefEditStartsFromRawText(t *testing.T) {
	brief := iriseditBrief(t)
	const rule = "**An edit starts from the draft's own text, never from your memory of it.**"
	if n := strings.Count(brief, rule); n != 1 {
		t.Fatalf("the edit rule appears %d times, want exactly 1", n)
	}
	for _, want := range []string{
		"prints JSON: the job's whole text as `content` beside its `mtime`",
		"change only what the message changes",
		"Never rebuild a draft from what you remember writing",
		"never read `raw` for the mtime alone",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q", want)
		}
	}

	at := strings.Index(brief, rule)
	memory := strings.Index(brief, "\n## Your memory is the rows, not this conversation\n")
	next := strings.Index(brief, "\n## What you know about a job, and what you never add\n")
	if memory < 0 || next < 0 {
		t.Fatalf("section anchors missing: memory=%d next=%d", memory, next)
	}
	if at < memory || at > next {
		t.Errorf("the edit rule sits at %d, outside the memory section [%d, %d]", at, memory, next)
	}
}

// TestIrisBriefEditRuleDoesNotFightTheKeysRule: raw's text carries kraken's
// own keys, so a brief that told iris the server REFUSES them would make the
// edit rule impossible to follow. The keys sentence says what the server
// does with each kind instead.
func TestIrisBriefEditRuleDoesNotFightTheKeysRule(t *testing.T) {
	brief := iriseditBrief(t)
	if strings.Contains(brief, "the server refuses a draft that sets them") {
		t.Errorf("the brief still says the server refuses kraken's own keys")
	}
	if strings.Contains(brief, "or any other key yourself") {
		t.Errorf("the brief files every unnamed key under kraken's — base and agent are authored and written")
	}
	for _, want := range []string{
		"the server refuses a `state:` outright and ignores kraken's other keys",
		"kraken's own keys ride along as printed, since the server ignores them",
		// base and agent are NOT kraken's: the route writes both, so the
		// brief must not file them under the keys it calls inert
		"`base` and `agent` are authored keys too, and the server WRITES both",
		"on an edit keep each line exactly as `raw` printed it",
		// the retired-key refusal is a different rule and must survive
		"`ready` and `prereqs` are RETIRED keys: the server refuses a draft carrying either",
	} {
		if !strings.Contains(brief, want) {
			t.Errorf("the brief lacks %q", want)
		}
	}
}
