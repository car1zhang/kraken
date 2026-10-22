package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// mindpass_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 639, the mind pass), slice B: the operator
// reader (`OperatorProse`), its context, `AroundRecord` and
// `OperatorDenylist`. Written from the frozen contract alone; internal/core's
// working tree was never read. The harness (mineHome, mineWrite, mineISO,
// mineJSON, mineAssistant, mineToolUse, mineText) is the committed
// mine_test.go's, which the contract names as reusable.
//
// Dimension: LOGIC — the stated invariants and happy paths. Refusals and
// boundaries are athena-edge's. Every package-level identifier here is
// prefixed `mpc`.

const (
	mpcCellProject  = "-Users-carl--kraken-cells-demo-12-wt-kraken"
	mpcPlainProject = "-Users-carl-Developer-kraken"
	mpcLogin        = "Carl Zhang"
)

// mpcWindow anchors a test window half an hour back: `since` an hour ago,
// records at base+n seconds, so every fixture file (written now) has an
// mtime inside the window.
func mpcWindow() (since int64, at func(n int64) int64) {
	now := time.Now().UnixMilli()
	base := now - 30*60_000
	return now - 60*60_000, func(n int64) int64 { return base + n*1000 }
}

// mpcPrompt is the contract's typed-prompt shape: `{"type":"user",
// "message":{"role":"user","content":…},"uuid","timestamp",
// "origin":{"kind":"human"},"promptSource":"typed","sessionId":<stem>}`.
// extra overrides keys; a nil value deletes one.
func mpcPrompt(t *testing.T, uuid, sess string, ts int64, content any, extra map[string]any) string {
	t.Helper()
	m := map[string]any{
		"type":         "user",
		"message":      map[string]any{"role": "user", "content": content},
		"uuid":         uuid,
		"timestamp":    mineISO(ts),
		"origin":       map[string]any{"kind": "human"},
		"promptSource": "typed",
		"sessionId":    sess,
	}
	for k, v := range extra {
		if v == nil {
			delete(m, k)
		} else {
			m[k] = v
		}
	}
	return mineJSON(t, m)
}

func mpcAsst(t *testing.T, ts int64, text string) string {
	t.Helper()
	return mineAssistant(t, ts, "claude-opus-5", mineText(text))
}

func mpcRun(t *testing.T, opts OperatorOptions) *OperatorResult {
	t.Helper()
	res, err := OperatorProse(opts)
	if err != nil {
		t.Fatalf("OperatorProse(%+v): %v", opts, err)
	}
	if res == nil {
		t.Fatal("OperatorProse returned a nil result with no error")
	}
	return res
}

func mpcTexts(res *OperatorResult) []string {
	out := []string{}
	for _, r := range res.Records {
		out = append(out, r.Text)
	}
	return out
}

func mpcByUUID(t *testing.T, res *OperatorResult, uuid string) OperatorRecord {
	t.Helper()
	for _, r := range res.Records {
		if r.UUID == uuid {
			return r
		}
	}
	t.Fatalf("no record %q among %v", uuid, mpcTexts(res))
	return OperatorRecord{}
}

// mpcSections splits `ReduceTranscript(path, 0)` into its emitted sections:
// each is `label\n<text>\n\n`, and a `[tool]` emission is the label line
// alone. Fixtures keep every text on one line so a blank line only ever
// separates sections.
func mpcSections(t *testing.T, path string) []ContextBlock {
	t.Helper()
	out, err := ReduceTranscript(path, 0)
	if err != nil {
		t.Fatalf("ReduceTranscript(%s, 0): %v", path, err)
	}
	var blocks []ContextBlock
	for _, chunk := range strings.Split(string(out), "\n\n") {
		if strings.TrimSpace(chunk) == "" {
			continue
		}
		label, text, _ := strings.Cut(chunk, "\n")
		blocks = append(blocks, ContextBlock{Label: label, Text: text})
	}
	return blocks
}

func mpcIndexOf(t *testing.T, secs []ContextBlock, label, text string) int {
	t.Helper()
	for i, b := range secs {
		if b.Label == label && b.Text == text {
			return i
		}
	}
	t.Fatalf("no section %s %q in %+v", label, text, secs)
	return -1
}

func mpcSameBlocks(t *testing.T, what string, got, want []ContextBlock) {
	t.Helper()
	if got == nil {
		t.Errorf("%s is nil — the contract says both slices are never nil", what)
	}
	if len(got) != len(want) {
		t.Fatalf("%s = %+v (%d blocks), want %+v (%d blocks)", what, got, len(got), want, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

// ---------------------------------------------------------------------------
// record rung + text rung: the forms that survive
// ---------------------------------------------------------------------------

// Contract, OperatorRecord: "`Form` ∈ `typed | queued | command-args | remote |
// reactivation`; `Pasted` is true when the text carried a `<pasted_content`
// block (the block is kept WHOLE in `Text`)". Record rung: `origin.kind ==
// "human"` (promptSource typed, queued or absent) OR `promptSource == "sdk"`
// with a `[from <label> · <login>]` stamp naming opts.Login (Form remote,
// stamp stripped). Text rung: `<command-args>X</command-args>` yields X; the
// reactivation marker yields the text after it; the thread guard tail is
// removed.
func TestMpcOperatorProseKeepsEachOperatorForm(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	sess := "sess-forms"
	pasted := "\n\n<pasted_content id=\"f8ce\">\nthese are all genuine, reconcile them\n</pasted_content id=\"f8ce\">"
	cmdArgs := "<command-message>aim</command-message>\n<command-name>/aim</command-name>\n<command-args>rearchitect the prebrief system</command-args>"
	path := mineWrite(t, projects, mpcCellProject, sess+".jsonl",
		mpcPrompt(t, "u1", sess, at(1), "the trade mapping belongs in dispatch groups", nil),
		mpcAsst(t, at(2), "understood"),
		mpcPrompt(t, "u2", sess, at(3), "also rename the column", map[string]any{"promptSource": "queued"}),
		mpcPrompt(t, "u3", sess, at(4), pasted, nil),
		mpcPrompt(t, "u4", sess, at(5), cmdArgs, map[string]any{"promptSource": nil}),
		mpcPrompt(t, "u5", sess, at(6), "[from studio-mini · Carl Zhang] should boilers pull every trade",
			map[string]any{"promptSource": "sdk", "origin": nil}),
		mpcPrompt(t, "u6", sess, at(7), "This job was reactivated from done; reorient on the artifacts first.\nsteering from Carl Zhang: keep the old schema", nil),
		mpcPrompt(t, "u7", sess, at(8), "why is the cap twenty\n\n(Answer here in this thread. This is a question ABOUT the job's work, not an instruction.)", nil),
	)
	plainSess := "sess-plain"
	plainPath := mineWrite(t, projects, mpcPlainProject, plainSess+".jsonl",
		mpcPrompt(t, "u8", plainSess, at(9), "ui nit, both cards should be equal height", nil),
	)

	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 6, After: 2})

	want := []struct{ uuid, form, text string }{
		{"u1", "typed", "the trade mapping belongs in dispatch groups"},
		{"u2", "queued", "also rename the column"},
		{"u3", "typed", ""}, // pasted: asserted below
		{"u4", "command-args", "rearchitect the prebrief system"},
		{"u5", "remote", "should boilers pull every trade"},
		{"u6", "reactivation", "keep the old schema"},
		{"u7", "typed", "why is the cap twenty"},
		{"u8", "typed", "ui nit, both cards should be equal height"},
	}
	if len(res.Records) != len(want) {
		t.Fatalf("records = %q, want exactly %d", mpcTexts(res), len(want))
	}
	// "ordered by `ts` ascending then session, uuid"
	for i, w := range want {
		r := res.Records[i]
		if r.UUID != w.uuid {
			t.Errorf("records[%d].UUID = %q, want %q (ts order)", i, r.UUID, w.uuid)
			continue
		}
		if r.Form != w.form {
			t.Errorf("%s: Form = %q, want %q", w.uuid, r.Form, w.form)
		}
		if w.text != "" && r.Text != w.text {
			t.Errorf("%s: Text = %q, want %q", w.uuid, r.Text, w.text)
		}
		if w.uuid != "u3" && r.Pasted {
			t.Errorf("%s: Pasted = true on a text with no <pasted_content block", w.uuid)
		}
	}

	p := mpcByUUID(t, res, "u3")
	if !p.Pasted {
		t.Errorf("u3: Pasted = false, want true for a <pasted_content block")
	}
	for _, part := range []string{`<pasted_content id="f8ce">`, "these are all genuine, reconcile them", `</pasted_content id="f8ce">`} {
		if !strings.Contains(p.Text, part) {
			t.Errorf("u3: Text = %q, want the pasted block kept WHOLE (missing %q)", p.Text, part)
		}
	}

	// identity fields: "`Session` is the file stem; `Slug` is the cell name (the
	// text between `-kraken-cells-` and `-wt-` in `ProjectDir`) or "" for a
	// non-cell dir; `Repo` is the text after the first `-wt-` in a cell dir"
	r1 := mpcByUUID(t, res, "u1")
	if r1.Session != sess || r1.TS != at(1) || r1.TranscriptPath != path || r1.ProjectDir != mpcCellProject {
		t.Errorf("u1 identity = session %q ts %d path %q dir %q, want %q %d %q %q",
			r1.Session, r1.TS, r1.TranscriptPath, r1.ProjectDir, sess, at(1), path, mpcCellProject)
	}
	if r1.Slug != "demo-12" || r1.Repo != "kraken" {
		t.Errorf("u1 slug/repo = %q/%q, want demo-12/kraken", r1.Slug, r1.Repo)
	}
	r8 := mpcByUUID(t, res, "u8")
	if r8.Slug != "" || r8.Repo != "" || r8.Session != plainSess || r8.TranscriptPath != plainPath || r8.ProjectDir != mpcPlainProject {
		t.Errorf("u8 (non-cell dir) = %+v, want empty slug/repo, session %q", r8, plainSess)
	}

	// "`Scanned` = root transcript files opened"
	if res.Scanned != 2 || res.Skipped != 0 || res.Truncated != 0 {
		t.Errorf("scanned/skipped/truncated = %d/%d/%d, want 2/0/0", res.Scanned, res.Skipped, res.Truncated)
	}
	if res.Since != since || res.Until != 0 {
		t.Errorf("result since/until = %d/%d, want %d/0 echoed", res.Since, res.Until, since)
	}
}

// "`Login` compared exactly after `strings.TrimSpace` on both sides".
func TestMpcOperatorProseTrimsTheLoginBeforeComparing(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, mpcPlainProject, "s.jsonl",
		mpcPrompt(t, "r1", "s", at(1), "[from Carl's Macbook ·  Carl Zhang ] pull from all trades first",
			map[string]any{"promptSource": "sdk", "origin": nil}),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: "  Carl Zhang\t"})
	if len(res.Records) != 1 {
		t.Fatalf("records = %q, want the one remote record", mpcTexts(res))
	}
	if r := res.Records[0]; r.Form != "remote" || r.Text != "pull from all trades first" {
		t.Errorf("record = form %q text %q, want remote / the stamp stripped", r.Form, r.Text)
	}
}

// ---------------------------------------------------------------------------
// record rung + text rung: the drops
// ---------------------------------------------------------------------------

// Record rung: `isMeta`, `isCompactSummary`, `isSidechain`, a record carrying
// `agentName`, a tool_result list, `origin.kind` other than human, a timestamp
// outside the window, an `sdk` stamp naming another login or a ` · cell <slug>`
// stamp — all yield nothing. Text rung drops: the eight literal openers, a
// teammate envelope, a bare `<command-message>`. Denylist: a match at offset 0
// drops the record. Exactly the one operator prompt survives.
func TestMpcOperatorProseDropsEveryNonOperatorShape(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	s := "sess-drops"
	sdk := map[string]any{"promptSource": "sdk", "origin": nil}
	n := int64(0)
	next := func() int64 { n++; return at(n) }
	lines := []string{
		mpcPrompt(t, "keep", s, next(), "KEEP-ME the only operator sentence", nil),
		// record rung
		mpcPrompt(t, "d-tr", s, next(), []any{map[string]any{"tool_use_id": "t1", "type": "tool_result", "content": "TOOL-RESULT"}},
			map[string]any{"origin": nil, "promptSource": nil, "toolUseResult": map[string]any{}}),
		mpcPrompt(t, "d-meta", s, next(), "Base directory for this skill: /x/.claude/skills/aim", map[string]any{"isMeta": true}),
		mpcPrompt(t, "d-img", s, next(), "[Image: original 1200x4481, displayed at 536x2000.]", map[string]any{"isMeta": true}),
		mpcPrompt(t, "d-compact", s, next(), "Summary of the earlier conversation", map[string]any{"isCompactSummary": true}),
		mpcPrompt(t, "d-side", s, next(), "SIDECHAIN-BRIEF", map[string]any{"isSidechain": true}),
		mpcPrompt(t, "d-task", s, next(), "TASK-NOTE finished", map[string]any{"origin": map[string]any{"kind": "task-notification"}, "promptSource": "system"}),
		mpcPrompt(t, "d-other", s, next(), "[from studio-mini · jess@probook.ai] what does the tracker do", sdk),
		mpcPrompt(t, "d-cell", s, next(), "[from studio-mini · cell other-3] ping from a cell", sdk),
		mpcPrompt(t, "d-before", s, since-10*60_000, "OUT-OF-WINDOW-OLD", nil),
		// text rung — the literal openers
		mpcPrompt(t, "x1", s, next(), "<task-notification>agent done</task-notification>", nil),
		mpcPrompt(t, "x2", s, next(), "<system-reminder>harness text</system-reminder>", nil),
		mpcPrompt(t, "x3", s, next(), "<local-command-stdout>ok</local-command-stdout>", nil),
		mpcPrompt(t, "x4", s, next(), "<bash-input>ls</bash-input>", nil),
		mpcPrompt(t, "x5", s, next(), "[Request interrupted by user]", nil),
		mpcPrompt(t, "x6", s, next(), "This session is being continued from a previous conversation that ran out of context.", nil),
		mpcPrompt(t, "x7", s, next(), "<teammate-message teammate_id=\"team-lead\">You are athena-style</teammate-message>", nil),
		mpcPrompt(t, "x8", s, next(), "<command-message>do</command-message>\n<command-name>/do</command-name>", nil),
		// the denylist at offset 0
		mpcPrompt(t, "k1", s, next(), "[kraken pr-watch — automated notice, not a reply from Carl Zhang] https://github.com/o/r/pull/5 has MERGED", nil),
		mpcPrompt(t, "k2", s, next(), "/color blue", nil),
		mpcPrompt(t, "k3", s, next(), "/handoff", nil),
		mpcPrompt(t, "k4", s, next(), "/compact Keep one sentence: the handoff doc is 80-handoff.md", nil),
		mpcPrompt(t, "k5", s, next(), "/model claude-opus-5-5", nil),
		mpcPrompt(t, "k6", s, next(), "kraken: this account's Fable usage ran out — continue on Opus", nil),
		mpcPrompt(t, "k7", s, next(), "you stalled (zeus hit an API error) — pick up exactly where you left off", nil),
		mpcPrompt(t, "k8", s, next(), "slack: 1 new message — run: kraken slack inbox", nil),
		mpcPrompt(t, "k9", s, next(), "orch: 2 new items — run: kraken orch inbox", nil),
	}
	mineWrite(t, projects, mpcPlainProject, s+".jsonl", lines...)

	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	if got := mpcTexts(res); len(got) != 1 || got[0] != "KEEP-ME the only operator sentence" {
		t.Fatalf("records = %q, want exactly the one operator prompt", got)
	}
	if res.Records[0].Form != "typed" {
		t.Errorf("Form = %q, want typed", res.Records[0].Form)
	}
}

// "`Until` 0 = no ceiling" and the window is inclusive: a record past a set
// ceiling yields nothing, a record exactly ON either edge is kept.
func TestMpcOperatorProseWindowIsInclusive(t *testing.T) {
	projects := mineHome(t)
	_, at := mpcWindow()
	since, until := at(10), at(20)
	mineWrite(t, projects, mpcPlainProject, "w.jsonl",
		mpcPrompt(t, "w0", "w", at(5), "BEFORE-THE-FLOOR", nil),
		mpcPrompt(t, "w1", "w", since, "ON-THE-FLOOR", nil),
		mpcPrompt(t, "w2", "w", until, "ON-THE-CEILING", nil),
		mpcPrompt(t, "w3", "w", at(25), "PAST-THE-CEILING", nil),
	)
	// the file's mtime is now, past `until`: the mtime skip is "older than
	// Since", so it must still be read.
	res := mpcRun(t, OperatorOptions{Since: since, Until: until, Login: mpcLogin})
	if got := mpcTexts(res); len(got) != 2 || got[0] != "ON-THE-FLOOR" || got[1] != "ON-THE-CEILING" {
		t.Fatalf("records = %q, want exactly the two on-edge prompts", got)
	}
	if res.Until != until {
		t.Errorf("Until = %d, want %d echoed", res.Until, until)
	}
}

// Text rung, the denylist: "the text is CUT at the earliest match and the
// part before it kept (trimmed)" — the glued pr-watch case.
func TestMpcOperatorProseKeepsTheOperatorHalfOfAGluedBump(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, mpcPlainProject, "g.jsonl",
		mpcPrompt(t, "g1", "g", at(1), "apply the same changes to the og kraken repo.[kraken pr-watch — automated notice, not a reply from Carl Zhang] https://github.com/o/r/pull/5 has MERGED", nil),
		mpcPrompt(t, "g2", "g", at(2), "ship it after the tests pass\nyou stalled (api error) — pick up exactly where you left off", nil),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	got := mpcTexts(res)
	want := []string{"apply the same changes to the og kraken repo.", "ship it after the tests pass"}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("records = %q, want %q", got, want)
	}
}

// `var OperatorDenylist = []string{…}` holds "the machine prefixes … `[kraken
// pr-watch`, `/color `, `/handoff`, `/compact`, `/model claude-`, `kraken: `,
// `you stalled`, `slack: `, `orch: `", and `steering from ` is NOT in it.
func TestMpcOperatorDenylistHoldsTheListedPrefixes(t *testing.T) {
	have := map[string]bool{}
	for _, p := range OperatorDenylist {
		have[p] = true
	}
	for _, want := range []string{"[kraken pr-watch", "/color ", "/handoff", "/compact", "/model claude-", "kraken: ", "you stalled", "slack: ", "orch: "} {
		if !have[want] {
			t.Errorf("OperatorDenylist lacks %q: %q", want, OperatorDenylist)
		}
	}
	for _, p := range OperatorDenylist {
		if strings.HasPrefix(p, "steering from") {
			t.Errorf("OperatorDenylist carries %q — the reactivation marker is not a machine prefix", p)
		}
	}
}

// ---------------------------------------------------------------------------
// identity rung, teammates, sidecars
// ---------------------------------------------------------------------------

// `Skipped` = "root files excluded as teammate transcripts (… a non-lead
// `agentName`)"; OperatorProse "walks every `~/.claude/projects/*/*.jsonl`
// ROOT file (never `<session>/subagents/`)".
func TestMpcOperatorProseSkipsTeammateRootsAndNeverReadsSidecars(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, mpcCellProject, "lead.jsonl",
		mpcPrompt(t, "l1", "lead", at(1), "LEAD-PROMPT from the operator", nil),
	)
	mineWrite(t, projects, mpcCellProject, "mate.jsonl",
		mpcPrompt(t, "m1", "mate", at(2), "TEAMMATE-BRIEF you are athena-logic",
			map[string]any{"agentName": "athena-logic", "teamName": "session-1"}),
	)
	mineWrite(t, projects, mpcCellProject, filepath.Join("lead", "subagents", "agent-a1.jsonl"),
		mpcPrompt(t, "s1", "agent-a1", at(3), "SIDECAR-BRIEF from zeus", nil),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	if got := mpcTexts(res); len(got) != 1 || got[0] != "LEAD-PROMPT from the operator" {
		t.Fatalf("records = %q, want only the lead's prompt", got)
	}
	if res.Skipped != 1 {
		t.Errorf("Skipped = %d, want 1 (the teammate root)", res.Skipped)
	}
}

// Identity rung: "a record whose snake-case `session_id` is non-empty and
// differs from the file stem is a fork replay and yields nothing; among the
// rest, one uuid is emitted once across ALL files (first seen wins, files
// visited in sorted path order)".
func TestMpcOperatorProseEmitsEachRecordOnce(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, mpcPlainProject, "sess-p.jsonl",
		mpcPrompt(t, "u-a", "sess-p", at(1), "PARENT-PROMPT", map[string]any{"session_id": "sess-p"}),
	)
	mineWrite(t, projects, mpcPlainProject, "sess-f.jsonl",
		// the replay: the parent's uuid and snake session_id, camel rewritten
		mpcPrompt(t, "u-a", "sess-f", at(1), "PARENT-PROMPT", map[string]any{"session_id": "sess-p"}),
		mpcPrompt(t, "u-b", "sess-f", at(2), "FORK-OWN-PROMPT", map[string]any{"session_id": "sess-f"}),
	)
	// one uuid in two files with no session_id at all: first in sorted path
	// order wins
	mineWrite(t, projects, mpcPlainProject, "sess-q.jsonl",
		mpcPrompt(t, "u-c", "sess-q", at(3), "DUPLICATED-PROMPT", nil),
	)
	mineWrite(t, projects, mpcPlainProject, "sess-r.jsonl",
		mpcPrompt(t, "u-c", "sess-r", at(3), "DUPLICATED-PROMPT", nil),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	got := mpcTexts(res)
	want := []string{"PARENT-PROMPT", "FORK-OWN-PROMPT", "DUPLICATED-PROMPT"}
	if len(got) != len(want) {
		t.Fatalf("records = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("records[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if r := mpcByUUID(t, res, "u-a"); r.Session != "sess-p" {
		t.Errorf("u-a session = %q, want sess-p (the replay is dropped)", r.Session)
	}
	if r := mpcByUUID(t, res, "u-c"); r.Session != "sess-q" {
		t.Errorf("u-c session = %q, want sess-q (first in sorted path order)", r.Session)
	}
}

// Identity rung: "in a file whose head `agentName` begins with `zeus-`, the
// FIRST candidate record of the file (the staged job prompt) yields nothing".
// The head name rides a bookkeeping record (the `agent-name` line mine_test's
// own fixture uses), so the user records themselves carry no agentName and
// the record rung's agentName drop is not what removes the prompt.
func TestMpcOperatorProseDropsTheZeusFirstPrompt(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, mpcCellProject, "zeus.jsonl",
		`{"type":"agent-name","agentName":"zeus-demo-12"}`,
		mpcPrompt(t, "z1", "zeus", at(1), "STAGED-JOB-SPEC build the thing", nil),
		mpcAsst(t, at(2), "starting"),
		mpcPrompt(t, "z2", "zeus", at(3), "OPERATOR-STEER use the smaller cap", nil),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	if got := mpcTexts(res); len(got) != 1 || got[0] != "OPERATOR-STEER use the smaller cap" {
		t.Fatalf("records = %q, want only the steer after the staged prompt", got)
	}
	if res.Skipped != 0 {
		t.Errorf("Skipped = %d, want 0 — zeus is a lead, not a teammate", res.Skipped)
	}
}

// `Truncated` = "files whose scan ended early (a line over `mineScanBufMax` or
// a read error) — their records read so far are KEPT".
func TestMpcOperatorProseKeepsWhatATruncatedScanRead(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	huge := `{"type":"assistant","message":{"content":[{"type":"text","text":"` + strings.Repeat("x", mineScanBufMax+1024) + `"}]}}`
	mineWrite(t, projects, mpcPlainProject, "long.jsonl",
		mpcPrompt(t, "t1", "long", at(1), "BEFORE-THE-LONG-LINE", nil),
		huge,
		mpcPrompt(t, "t2", "long", at(3), "AFTER-THE-LONG-LINE", nil),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	if res.Truncated != 1 {
		t.Errorf("Truncated = %d, want 1", res.Truncated)
	}
	if got := mpcTexts(res); len(got) < 1 || got[0] != "BEFORE-THE-LONG-LINE" {
		t.Fatalf("records = %q, want the record read before the over-long line kept", got)
	}
}

// ---------------------------------------------------------------------------
// redaction
// ---------------------------------------------------------------------------

// "`redactSecrets` is applied to `Text` and to every context block's `Text`".
func TestMpcOperatorProseRedactsTextAndContext(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	secret := "ghp_ABCDEFGHIJKLMNOPQRSTUVWX12"
	mineWrite(t, projects, mpcPlainProject, "r.jsonl",
		mpcPrompt(t, "r0", "r", at(1), "first question", nil),
		mpcAsst(t, at(2), "the old token was "+secret),
		mpcPrompt(t, "r1", "r", at(3), "rotate "+secret+" tonight", nil),
		mpcAsst(t, at(4), "rotating "+secret),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 6, After: 2})
	r := mpcByUUID(t, res, "r1")
	if strings.Contains(r.Text, secret) || !strings.Contains(r.Text, "…[redacted]") {
		t.Errorf("Text = %q, want the token redacted", r.Text)
	}
	for _, b := range append(append([]ContextBlock{}, r.Context.Before...), r.Context.After...) {
		if strings.Contains(b.Text, secret) {
			t.Errorf("context block %+v leaks the token", b)
		}
	}
	if len(r.Context.Before) == 0 || len(r.Context.After) == 0 {
		t.Fatalf("context = %+v, want blocks on both sides", r.Context)
	}
}

// ---------------------------------------------------------------------------
// context
// ---------------------------------------------------------------------------

// Context: "the file is reduced exactly as `ReduceTranscript(path, 0)` reduces
// it … `Before` is the up-to-`Before` sections immediately preceding it
// (oldest first) and `After` the up-to-`After` sections following it, neither
// containing the record's own section; the denylist is NOT applied to context
// (a kraken bump the agent reacted to stays) … a record at the file's start has
// an empty `Before`".
func TestMpcOperatorProseContextIsTheReducersSections(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	s := "ctx"
	path := mineWrite(t, projects, mpcCellProject, s+".jsonl",
		mpcPrompt(t, "c0", s, at(1), "FIRST-PROMPT look at the tracker", nil),
		mpcAsst(t, at(2), "A1 reading the tracker"),
		mineAssistant(t, at(3), "claude-opus-5", mineToolUse("t1", "Bash", map[string]any{"command": "go test ./..."})),
		mpcAsst(t, at(4), "A2 tests pass"),
		mpcPrompt(t, "c-bump", s, at(5), "[kraken pr-watch — automated notice, not a reply from Carl Zhang] https://github.com/o/r/pull/9 got a new comment", nil),
		mpcPrompt(t, "c1", s, at(6), "SECOND-PROMPT fix the comment it raised", nil),
		mpcAsst(t, at(7), "A3 on it"),
		mpcAsst(t, at(8), "A4 pushed"),
		mpcAsst(t, at(9), "A5 done"),
	)
	secs := mpcSections(t, path)
	i0 := mpcIndexOf(t, secs, "[user]", "FIRST-PROMPT look at the tracker")
	i1 := mpcIndexOf(t, secs, "[user]", "SECOND-PROMPT fix the comment it raised")
	mpcIndexOf(t, secs, "[tool] Bash(go test ./...)", "") // the fixture's own sanity

	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 6, After: 2})
	if got := mpcTexts(res); len(got) != 2 {
		t.Fatalf("records = %q, want the two operator prompts (the bump is denylisted)", got)
	}

	first := mpcByUUID(t, res, "c0")
	mpcSameBlocks(t, "FIRST.Before", first.Context.Before, []ContextBlock{})
	mpcSameBlocks(t, "FIRST.After", first.Context.After, secs[i0+1:i0+3])

	second := mpcByUUID(t, res, "c1")
	lo := i1 - 6
	if lo < 0 {
		lo = 0
	}
	mpcSameBlocks(t, "SECOND.Before", second.Context.Before, secs[lo:i1])
	mpcSameBlocks(t, "SECOND.After", second.Context.After, secs[i1+1:i1+3])

	var sawBump bool
	for _, b := range second.Context.Before {
		if strings.HasPrefix(b.Text, "[kraken pr-watch") {
			sawBump = true
		}
	}
	if !sawBump {
		t.Errorf("SECOND.Before = %+v, want the pr-watch bump kept as context", second.Context.Before)
	}
}

// "each block's `Text` is capped at 1,500 runes with the suffix `… [N more
// characters elided]`".
func TestMpcOperatorProseCapsEachContextBlock(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	long := strings.Repeat("a", 2000)
	mineWrite(t, projects, mpcPlainProject, "cap.jsonl",
		mpcAsst(t, at(1), long),
		mpcPrompt(t, "k1", "cap", at(2), "PROMPT-AFTER-LONG-BLOCK", nil),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 6, After: 2})
	r := mpcByUUID(t, res, "k1")
	if len(r.Context.Before) != 1 {
		t.Fatalf("Before = %+v, want the one assistant block", r.Context.Before)
	}
	b := r.Context.Before[0]
	if b.Label != "[assistant]" {
		t.Errorf("label = %q, want [assistant]", b.Label)
	}
	if !strings.HasPrefix(b.Text, strings.Repeat("a", 1500)) || strings.HasPrefix(b.Text, strings.Repeat("a", 1501)) {
		t.Errorf("block text does not keep exactly the first 1500 runes: %q", mpcTail(b.Text))
	}
	if !strings.HasSuffix(b.Text, "… [500 more characters elided]") {
		t.Errorf("block text = …%q, want the elision suffix naming 500", mpcTail(b.Text))
	}
}

// "when the capped blocks of one record total more than 8,000 runes, blocks are
// dropped farthest-from-the-record first until they fit, and the dropped count
// is stated as one trailing block `{label: "…", text: "[K blocks elided]"}` on
// the side it was dropped from".
func TestMpcOperatorProseDropsFarthestBlocksPastTheRecordTotal(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	lines := []string{}
	for i := 0; i < 6; i++ {
		lines = append(lines, mpcAsst(t, at(int64(i+1)), strings.Repeat(string(rune('a'+i)), 1400)))
	}
	lines = append(lines, mpcPrompt(t, "big", "big", at(10), "PROMPT-AFTER-SIX-BLOCKS", nil))
	mineWrite(t, projects, mpcPlainProject, "big.jsonl", lines...)

	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 6, After: 0})
	r := mpcByUUID(t, res, "big")
	// 6 × 1400 = 8400 > 8000; dropping the farthest one leaves 7000.
	if len(r.Context.Before) != 6 {
		t.Fatalf("Before = %d blocks, want 5 kept + 1 elision marker", len(r.Context.Before))
	}
	for i := 0; i < 5; i++ {
		want := strings.Repeat(string(rune('b'+i)), 1400)
		if got := r.Context.Before[i]; got.Label != "[assistant]" || got.Text != want {
			t.Errorf("Before[%d] = %s %.10q…, want the %c block (the farthest, a, dropped)", i, got.Label, got.Text, 'b'+i)
		}
	}
	if m := r.Context.Before[5]; m.Label != "…" || m.Text != "[1 blocks elided]" {
		t.Errorf("trailing marker = %+v, want {…, [1 blocks elided]}", m)
	}
	if len(r.Context.After) != 0 {
		t.Errorf("After = %+v, want empty with After 0", r.Context.After)
	}
	total := 0
	for _, b := range r.Context.Before[:5] {
		total += utf8.RuneCountInString(b.Text)
	}
	if total > 8000 {
		t.Errorf("kept context totals %d runes, want <= 8000", total)
	}
}

// ---------------------------------------------------------------------------
// AroundRecord
// ---------------------------------------------------------------------------

// "with `raw == false` the blocks are the same sections `OperatorProse` builds
// context from, `radius` each side of the record's own section, `hit` the
// record's index in `blocks`"; "`radius` clamped at 0" and the record alone at
// 0; "`found` false (nil blocks, nil error) when no record carries `uuid`".
func TestMpcAroundRecordReducedMode(t *testing.T) {
	projects := mineHome(t)
	_, at := mpcWindow()
	s := "around"
	path := mineWrite(t, projects, mpcPlainProject, s+".jsonl",
		mpcPrompt(t, "a0", s, at(1), "OPENING-PROMPT", nil),
		mpcAsst(t, at(2), "B1 first reply"),
		mpcAsst(t, at(3), "B2 second reply"),
		mpcPrompt(t, "a1", s, at(4), "THE-ASKED-PROMPT", nil),
		mpcAsst(t, at(5), "B3 third reply"),
		mpcAsst(t, at(6), "B4 fourth reply"),
		mpcAsst(t, at(7), "B5 fifth reply"),
	)
	secs := mpcSections(t, path)
	hitAt := mpcIndexOf(t, secs, "[user]", "THE-ASKED-PROMPT")

	blocks, hit, found, err := AroundRecord(path, "a1", 2, false)
	if err != nil || !found {
		t.Fatalf("AroundRecord = found %v err %v, want found", found, err)
	}
	mpcSameBlocks(t, "radius 2", blocks, secs[hitAt-2:hitAt+3])
	if hit != 2 || blocks[hit].Text != "THE-ASKED-PROMPT" {
		t.Errorf("hit = %d, want 2 (the record's own section)", hit)
	}

	alone, hit0, found0, err := AroundRecord(path, "a1", 0, false)
	if err != nil || !found0 {
		t.Fatalf("radius 0: found %v err %v", found0, err)
	}
	mpcSameBlocks(t, "radius 0", alone, secs[hitAt:hitAt+1])
	if hit0 != 0 {
		t.Errorf("radius 0 hit = %d, want 0", hit0)
	}

	neg, hitN, foundN, err := AroundRecord(path, "a1", -3, false)
	if err != nil || !foundN || len(neg) != 1 || hitN != 0 {
		t.Errorf("radius -3 = %+v hit %d found %v err %v, want the record alone (clamped at 0)", neg, hitN, foundN, err)
	}

	none, _, foundX, err := AroundRecord(path, "no-such-uuid", 2, false)
	if err != nil || foundX || none != nil {
		t.Errorf("unknown uuid = blocks %+v found %v err %v, want nil, false, nil", none, foundX, err)
	}
}

// "with `raw == true` every record inside the radius is rendered, one block per
// record, `Label` = `[<type>]` (`[user]`, `[assistant]`, `[tool_result]`,
// `[attachment]`…) and `Text` = the record's text with a `tool_result` body
// truncated to 4,000 runes with the `… [N more characters elided]` suffix".
func TestMpcAroundRecordRawModeRendersRecordsAndTruncatesToolResults(t *testing.T) {
	projects := mineHome(t)
	_, at := mpcWindow()
	s := "raw"
	body := strings.Repeat("r", 5000)
	path := mineWrite(t, projects, mpcPlainProject, s+".jsonl",
		mpcPrompt(t, "q0", s, at(1), "RAW-ASK read the log", nil),
		mineAssistant(t, at(2), "claude-opus-5", mineToolUse("t1", "Read", map[string]any{"file_path": "/tmp/log"})),
		mpcPrompt(t, "q2", s, at(3), []any{map[string]any{"tool_use_id": "t1", "type": "tool_result", "content": body}},
			map[string]any{"origin": nil, "promptSource": nil, "toolUseResult": map[string]any{}}),
		mpcAsst(t, at(4), "RAW-REPLY the log is clean"),
	)
	blocks, hit, found, err := AroundRecord(path, "q0", 2, true)
	if err != nil || !found {
		t.Fatalf("AroundRecord raw = found %v err %v", found, err)
	}
	if hit != 0 || len(blocks) != 3 {
		t.Fatalf("raw radius 2 at the first record = %d blocks hit %d, want 3 blocks (the record + 2 after) hit 0: %+v", len(blocks), hit, blocks)
	}
	if blocks[0].Label != "[user]" || !strings.Contains(blocks[0].Text, "RAW-ASK read the log") {
		t.Errorf("blocks[0] = %+v, want the asked [user] record", blocks[0])
	}
	if blocks[1].Label != "[assistant]" {
		t.Errorf("blocks[1].Label = %q, want [assistant]", blocks[1].Label)
	}
	tr := blocks[2]
	if tr.Label != "[tool_result]" {
		t.Errorf("blocks[2].Label = %q, want [tool_result]", tr.Label)
	}
	if !strings.HasPrefix(tr.Text, strings.Repeat("r", 4000)) || strings.HasPrefix(tr.Text, strings.Repeat("r", 4001)) {
		t.Errorf("tool_result text does not keep exactly 4000 runes of the body")
	}
	if !strings.HasSuffix(tr.Text, "… [1000 more characters elided]") {
		t.Errorf("tool_result text ends %q, want the elision suffix naming 1000", mpcTail(tr.Text))
	}
}

// mpcTail is the last 60 bytes of s (all of it when shorter), for messages.
func mpcTail(s string) string {
	if len(s) <= 60 {
		return s
	}
	return s[len(s)-60:]
}

// ---------------------------------------------------------------------------
// JSON shape
// ---------------------------------------------------------------------------

func mpcKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	keys := []string{}
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func mpcWantKeys(t *testing.T, what string, raw json.RawMessage, want ...string) {
	t.Helper()
	sort.Strings(want)
	got := mpcKeys(t, raw)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s keys = %v, want %v", what, got, want)
	}
}

// The result's tags as the contract writes them, and "`Records` never nil (an
// empty slice marshals as `[]`)"; "both slices never nil".
func TestMpcOperatorResultMarshalsTheContractKeys(t *testing.T) {
	projects := mineHome(t)
	since, at := mpcWindow()
	mineWrite(t, projects, mpcCellProject, "j.jsonl",
		mpcPrompt(t, "j1", "j", at(1), "ONLY-PROMPT", nil),
	)
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin, Before: 6, After: 2})
	raw, err := json.Marshal(res)
	if err != nil {
		t.Fatal(err)
	}
	mpcWantKeys(t, "OperatorResult", raw, "since", "until", "scanned", "skipped", "truncated", "records")
	var top struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(raw, &top); err != nil || len(top.Records) != 1 {
		t.Fatalf("records = %s (%v)", raw, err)
	}
	mpcWantKeys(t, "OperatorRecord", top.Records[0],
		"session", "uuid", "ts", "transcript_path", "project_dir", "slug", "repo", "form", "text", "pasted", "context")
	var rec struct {
		Context json.RawMessage `json:"context"`
	}
	_ = json.Unmarshal(top.Records[0], &rec)
	mpcWantKeys(t, "OperatorContext", rec.Context, "before", "after")
	if !strings.Contains(string(rec.Context), `"before":[]`) || !strings.Contains(string(rec.Context), `"after":[]`) {
		t.Errorf("context = %s, want both slices marshalled as []", rec.Context)
	}
	b, _ := json.Marshal(ContextBlock{Label: "[user]", Text: "x"})
	mpcWantKeys(t, "ContextBlock", b, "label", "text")

	// an empty projects dir: an empty result whose records marshal as []
	if err := os.RemoveAll(filepath.Join(projects, mpcCellProject)); err != nil {
		t.Fatal(err)
	}
	empty := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	eraw, _ := json.Marshal(empty)
	if !strings.Contains(string(eraw), `"records":[]`) {
		t.Errorf("empty result = %s, want \"records\":[]", eraw)
	}
}

// "an absent one is an empty result".
func TestMpcOperatorProseAbsentProjectsDirIsEmpty(t *testing.T) {
	projects := mineHome(t)
	if err := os.RemoveAll(projects); err != nil {
		t.Fatal(err)
	}
	since, _ := mpcWindow()
	res := mpcRun(t, OperatorOptions{Since: since, Login: mpcLogin})
	if res.Records == nil || len(res.Records) != 0 || res.Scanned != 0 {
		t.Errorf("absent projects dir = %+v, want an empty, non-nil result", res)
	}
}
