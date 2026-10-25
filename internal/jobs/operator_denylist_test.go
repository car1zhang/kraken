package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
)

// TestOperatorReaderDropsWholeKrakenPrompts pins the whole-record drops
// against the REAL texts: the mind-thread capture seed and follow-up
// (the retired mindSeed/mindCaptureMessage, as literals) and the reactivation rebase note sent with
// no message (reactivatePrompt). Each is typed into a pane as a human-origin
// prompt, so only the reader's text rung keeps it out of the operator's
// words — and a denylist CUT would not do, since it would keep the
// operator's name or the branch sentence as a record. Driven end to end
// through core.OperatorProse over a fixture transcript under a temp HOME.
func TestOperatorReaderDropsWholeKrakenPrompts(t *testing.T) {
	t.Setenv("KRAKEN_OPERATOR", "Carl Zhang")
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".claude", "projects", "-Users-x--kraken-cells-demo-1-wt-kraken")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// The retired mind thread's seed and follow-up (job 674 removed mindSeed
	// and mindCaptureMessage), as they rendered for this operator: transcripts
	// written before the retirement still carry them, so the reader must still
	// drop them whole.
	const seed = "Carl Zhang highlighted this passage and sent it to you to become an item in their mental model:\n\n<<<passage\nDocs are updated and the runbook is written.\npassage>>>\n\nsource: {\"kind\":\"\"}\n\nThe item you build with them:\n- title: ≤ 120 runes, stated as a CLAIM (what is true), never a topic\n- scope: `universal` (true everywhere Carl Zhang works), or `repo` with `repo` (the checkout name) and `dir` (repo-relative, \"\" for the whole repo) — where the knowledge applies\n- kind: `decision`, `reading` or `note`\n- body: ≤ 8,000 runes — what Carl Zhang now knows, in their terms\n- quote: the passage above, verbatim; source: the source JSON above, verbatim\nFill those fields in SILENTLY. What Carl Zhang reads is your paraphrase of what they now understand — at most four sentences, in their terms, no field list, no mechanism explanation, no quote-back — and at most one question; they confirm, deny, or dig deeper, one point per turn. When they say finalize, run `kraken mm commit` with the item as JSON on stdin — {\"title\",\"scope\",\"repo\",\"dir\",\"kind\",\"body\",\"quote\",\"source\"} — and tell them the id in one line.\n"
	const followUp = "Carl Zhang highlighted another passage for their mental model:\n\n<<<passage\nDocs are updated and the runbook is written.\npassage>>>\n\nsource: {\"kind\":\"\"}\n\nSame item contract as before: propose a title, scope, kind and body for it (or fold it into the item you are building, if it belongs there), and commit with `kraken mm commit` only when Carl Zhang says finalize.\n"
	rebase, _ := reactivatePrompt("reorient", "", rebaseNoteText("kraken/demo-1", "origin/main"))
	texts := []string{
		seed,
		followUp,
		"\n\n<pasted_content id=\"ab12\">\n" + followUp + "\n</pasted_content id=\"ab12\">",
		rebase,
		"the operator's own control line",
		// The operator saying "highlighted" in their own words is kept: the
		// drop keys on kraken's full sentences, not the bare phrase.
		"I highlighted this passage in the doc because it is wrong",
	}
	now := time.Now()
	var lines []string
	for i, text := range texts {
		b, err := json.Marshal(map[string]any{
			"type": "user", "uuid": "u" + string(rune('a'+i)),
			"timestamp":    now.Add(time.Duration(i) * time.Second).UTC().Format(time.RFC3339Nano),
			"origin":       map[string]string{"kind": "human"},
			"promptSource": "typed",
			"message":      map[string]any{"role": "user", "content": text},
		})
		if err != nil {
			t.Fatal(err)
		}
		lines = append(lines, string(b))
	}
	if err := os.WriteFile(filepath.Join(dir, "sess.jsonl"), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := core.OperatorProse(core.OperatorOptions{Since: now.Add(-time.Minute).UnixMilli(), Login: Operator()})
	if err != nil {
		t.Fatalf("OperatorProse: %v", err)
	}
	var got []string
	for _, r := range res.Records {
		got = append(got, r.Text)
	}
	want := []string{"the operator's own control line", "I highlighted this passage in the doc because it is wrong"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("records = %q, want only the two operator lines %q", got, want)
	}
}

// TestOperatorDenylistCoversKrakenSends pins core.OperatorDenylist against
// every text this package types into a pane. The mind pass attributes a
// human-origin prompt to the operator unless its text matches the denylist —
// the transcript carries no flag that tells the two apart — so a new or
// reworded send that no entry opens would be quoted back to the operator as
// something they said. Texts built inline (the stall nudges, the rename
// notice) are restated here from their call sites.
func TestOperatorDenylistCoversKrakenSends(t *testing.T) {
	sends := map[string]string{
		"pr-watch prefix":      prWatchPrefix(),
		"merge bump":           mergeBumpMessage("https://github.com/o/r/pull/1"),
		"colour send":          "/color " + "red",
		"auto-handoff send":    "/handoff",
		"fable fallback":       fableFallbackPrompt(),
		"fable /model switch":  "/model " + core.FableFallbackModel,
		"limit rescue resume":  limitRescueResumePrompt,
		"limit reset resume":   limitResetResumePrompt,
		"stall nudge":          "you stalled (API error or an interrupted turn) — pick up exactly where you left off and continue the job",
		"rename notice":        "kraken: this job was renamed a → b; old CLI names keep resolving, artifact paths unchanged",
		"slack bump (one)":     slackBumpMessage(1),
		"slack bump (many)":    slackBumpMessage(3),
		"linear bump":          linearUpdateText("ENG-12", "https://linear.app/x/issue/ENG-12", true, nil, nil),
		"linear bump (no url)": linearUpdateText("A1B-7", "", false, nil, nil),
	}
	for name, text := range sends {
		covered := false
		for _, p := range core.OperatorDenylist {
			if strings.HasPrefix(text, p) {
				covered = true
				break
			}
		}
		// A pattern covers a send only when it matches at offset 0 — the
		// same "opens with" claim the literal check makes.
		for _, re := range core.OperatorDenylistRE {
			if loc := re.FindStringIndex(text); loc != nil && loc[0] == 0 {
				covered = true
			}
		}
		if !covered {
			t.Errorf("%s: %q opens with no core.OperatorDenylist entry", name, text)
		}
	}
}
