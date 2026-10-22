package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// mindpass_edge_test.go — the EDGE dimension of 50-contract.md slice B (the
// operator reader `OperatorProse` and the widener `AroundRecord`). Written
// blind from the frozen contract: operator.go was never read. Boundaries,
// refusals and drops only; the stated happy paths are athena-logic's.
//
// Fixtures reuse mine_test.go's committed helpers (mineHome, mineWrite,
// mineJSON, mineISO, mineAssistant, mineText, mineToolResult) and the record
// shapes the contract's "Record shapes a test may rely on" paragraph freezes.
// Every package-level identifier is prefixed `mpe`.

const mpeProject = "-Users-carl-Developer-kraken"

const mpeLogin = "carl@probook.ai"

// mpeUser builds one user record of the contract's measured shape. extra keys
// are merged over the base (origin, promptSource, isMeta, session_id, …).
func mpeUser(t *testing.T, ts int64, uuid, stem string, content any, extra map[string]any) string {
	t.Helper()
	rec := map[string]any{
		"type":      "user",
		"timestamp": mineISO(ts),
		"uuid":      uuid,
		"sessionId": stem,
		"message":   map[string]any{"role": "user", "content": content},
	}
	for k, v := range extra {
		rec[k] = v
	}
	return mineJSON(t, rec)
}

// mpeTyped is a typed operator prompt: origin human, promptSource typed.
func mpeTyped(t *testing.T, ts int64, uuid, stem, text string) string {
	t.Helper()
	return mpeUser(t, ts, uuid, stem, text, map[string]any{
		"origin": map[string]any{"kind": "human"}, "promptSource": "typed",
	})
}

func mpeAsst(t *testing.T, ts int64, text string) string {
	t.Helper()
	return mineAssistant(t, ts, "claude-opus-5", mineText(text))
}

func mpeProse(t *testing.T, opts OperatorOptions) *OperatorResult {
	t.Helper()
	res, err := OperatorProse(opts)
	if err != nil {
		t.Fatalf("OperatorProse(%+v): %v", opts, err)
	}
	if res == nil {
		t.Fatalf("OperatorProse(%+v) returned a nil result with no error", opts)
	}
	return res
}

func mpeTexts(res *OperatorResult) []string {
	out := make([]string, 0, len(res.Records))
	for _, r := range res.Records {
		out = append(out, r.Text)
	}
	return out
}

// mpeOnly asserts the result holds exactly one record whose text is want.
func mpeOnly(t *testing.T, res *OperatorResult, want string) {
	t.Helper()
	got := mpeTexts(res)
	if len(got) != 1 || got[0] != want {
		t.Fatalf("records = %q, want exactly [%q]", got, want)
	}
}

func mpeWindow() (now, since int64) {
	now = time.Now().UnixMilli()
	return now, now - int64(time.Hour/time.Millisecond)
}

// ---------------------------------------------------------------------------
// OperatorProse — refusals and empty worlds
// ---------------------------------------------------------------------------

// "`Since` required (`<= 0` is an error from `OperatorProse`)".
func TestMPEOperatorProseRefusesNonPositiveSince(t *testing.T) {
	mineHome(t)
	for _, since := range []int64{0, -1, -1_770_000_000_000} {
		if _, err := OperatorProse(OperatorOptions{Since: since, Login: mpeLogin}); err == nil {
			t.Errorf("OperatorProse(Since=%d) = nil error, want a refusal", since)
		}
	}
}

// "an absent [projects dir] is an empty result"; "`Records` never nil (an
// empty slice marshals as `[]`)".
func TestMPEOperatorProseAbsentProjectsDirIsEmptyNotError(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.claude at all
	_, since := mpeWindow()
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	if res.Records == nil {
		t.Fatal("Records is nil for an absent projects dir, want an empty non-nil slice")
	}
	if len(res.Records) != 0 || res.Scanned != 0 {
		t.Fatalf("absent projects dir: %d records, scanned %d, want 0/0", len(res.Records), res.Scanned)
	}
	b, err := json.Marshal(res)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"records":[]`) {
		t.Fatalf("empty result marshals as %s, want \"records\":[]", b)
	}
}

// "an unreadable projects dir is an error".
func TestMPEOperatorProseUnreadableProjectsDirIsError(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: mode bits do not deny access")
	}
	projects := mineHome(t)
	_, since := mpeWindow()
	if err := os.Chmod(projects, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(projects, 0o755) })
	if _, err := OperatorProse(OperatorOptions{Since: since, Login: mpeLogin}); err == nil {
		t.Fatal("OperatorProse over an unreadable projects dir = nil error, want an error")
	}
}

// ---------------------------------------------------------------------------
// record rung — every non-candidate shape yields nothing
// ---------------------------------------------------------------------------

// Each case writes the dropped shape plus one control typed prompt into a
// fresh world, so a reader that emits nothing at all cannot pass vacuously.
// Obligations: "a record is a CANDIDATE only when `type == "user"`, `isMeta`,
// `isCompactSummary` and `isSidechain` are all unset/false, … its content is a
// string or a list whose `text` blocks are taken (a `tool_result` list yields
// nothing), its timestamp is inside the window, and EITHER `origin.kind ==
// "human"` (promptSource `typed`, `queued` or absent) OR `promptSource ==
// "sdk"` and the text opens with a `[from <label> · <login>]` stamp whose
// login equals `opts.Login` …; an `sdk` record whose stamp names a different
// login, or a ` · cell <slug>` stamp, yields nothing".
func TestMPERecordRungDropsNonCandidates(t *testing.T) {
	human := map[string]any{"kind": "human"}
	cases := []struct {
		name  string
		login string
		rec   func(t *testing.T, ts int64, stem string) string
	}{
		{"tool_result list", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, []any{mineToolResult("t1", false, "DROPPED-TOOL-RESULT")},
				map[string]any{"origin": human, "promptSource": "typed", "toolUseResult": map[string]any{}})
		}},
		{"isMeta", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "Base directory for this skill: DROPPED",
				map[string]any{"origin": human, "promptSource": "typed", "isMeta": true})
		}},
		{"isCompactSummary", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "DROPPED compaction summary words",
				map[string]any{"origin": human, "promptSource": "typed", "isCompactSummary": true})
		}},
		{"isSidechain", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "DROPPED sidechain brief",
				map[string]any{"origin": human, "promptSource": "typed", "isSidechain": true})
		}},
		{"task-notification origin", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "DROPPED plain words",
				map[string]any{"origin": map[string]any{"kind": "task-notification"}})
		}},
		{"human origin, promptSource system", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "DROPPED system-sourced words",
				map[string]any{"origin": human, "promptSource": "system"})
		}},
		{"no origin at all", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "DROPPED originless words", nil)
		}},
		{"sdk with no stamp", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "DROPPED unstamped sdk words",
				map[string]any{"promptSource": "sdk"})
		}},
		{"sdk stamp naming another login", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "[from studio-mini · jess@probook.ai] DROPPED someone else's question",
				map[string]any{"promptSource": "sdk"})
		}},
		// The login is set to the very text a naive `· <login>` parse would
		// read out of a cell stamp, so only the cell rule can drop it.
		{"sdk cell stamp", "cell some-job", func(t *testing.T, ts int64, stem string) string {
			return mpeUser(t, ts, "u-drop", stem, "[from studio-mini · cell some-job] DROPPED cell-to-cell send",
				map[string]any{"promptSource": "sdk"})
		}},
		{"assistant record", mpeLogin, func(t *testing.T, ts int64, stem string) string {
			return mpeAsst(t, ts, "DROPPED assistant words")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			projects := mineHome(t)
			now, since := mpeWindow()
			mineWrite(t, projects, mpeProject, "sess-a.jsonl",
				c.rec(t, now-3000, "sess-a"),
				mpeTyped(t, now-2000, "u-keep", "sess-a", "CONTROL typed prompt"),
			)
			res := mpeProse(t, OperatorOptions{Since: since, Login: c.login})
			mpeOnly(t, res, "CONTROL typed prompt")
		})
	}
}

// "its timestamp is inside the window"; convention: "window edges are unix
// millis and INCLUSIVE on both sides (`since <= t <= until`)".
func TestMPERecordRungWindowEdges(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	until := now - 10_000
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, since-1, "u-before", "sess-a", "ONE-MS-BEFORE-SINCE"),
		mpeTyped(t, since, "u-at-since", "sess-a", "EXACTLY-AT-SINCE"),
		mpeTyped(t, until, "u-at-until", "sess-a", "EXACTLY-AT-UNTIL"),
		mpeTyped(t, until+1, "u-after", "sess-a", "ONE-MS-AFTER-UNTIL"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Until: until, Login: mpeLogin})
	got := strings.Join(mpeTexts(res), "|")
	if got != "EXACTLY-AT-SINCE|EXACTLY-AT-UNTIL" {
		t.Fatalf("records = %q, want exactly the two edge records", got)
	}
}

// "`Until` 0 = no ceiling".
func TestMPERecordRungUntilZeroIsNoCeiling(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, now+int64(time.Hour/time.Millisecond), "u-future", "sess-a", "AN-HOUR-AHEAD"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Until: 0, Login: mpeLogin})
	mpeOnly(t, res, "AN-HOUR-AHEAD")
}

// "skipping files whose mtime is older than `Since`" — even when the records
// inside claim timestamps in the window.
func TestMPEOperatorProseSkipsFilesOlderThanSince(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	stale := mineWrite(t, projects, mpeProject, "stale.jsonl",
		mpeTyped(t, now-3000, "u-stale", "stale", "IN-WINDOW-RECORD-IN-STALE-FILE"),
	)
	old := time.UnixMilli(since).Add(-time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}
	mineWrite(t, projects, mpeProject, "fresh.jsonl",
		mpeTyped(t, now-2000, "u-fresh", "fresh", "CONTROL typed prompt"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	mpeOnly(t, res, "CONTROL typed prompt")
}

// ---------------------------------------------------------------------------
// text rung — whole-record drops
// ---------------------------------------------------------------------------

// "the WHOLE record is dropped when its (stamp-stripped, trimmed) text begins
// with any of: `<task-notification>`, `<system-reminder>`, `<local-command-`,
// `<bash-`, `[Request interrupted`, `This session is being continued`, a
// `<teammate-message` envelope …, or a bare `<command-message>`/
// `<command-name>` block with no `<command-args>`".
func TestMPETextRungDropsMachineOpenings(t *testing.T) {
	for name, text := range map[string]string{
		"task-notification":     "<task-notification>\n<task-id>abc</task-id>\n<status>completed</status>\n</task-notification>",
		"system-reminder":       "<system-reminder>\nDROPPED reminder\n</system-reminder>",
		"local-command":         "<local-command-stdout>DROPPED output</local-command-stdout>",
		"bash":                  "<bash-input>ls -la</bash-input>",
		"request interrupted":   "[Request interrupted by user for tool use]",
		"session continued":     "This session is being continued from a previous conversation that ran out of context.",
		"teammate envelope":     "<teammate-message teammate_id=\"athena-logic\">DROPPED teammate report</teammate-message>",
		"bare command-message":  "<command-message>do</command-message>\n<command-name>/do</command-name>",
		"bare command-name":     "<command-name>/do</command-name>",
		"leading whitespace":    "  \n\t<system-reminder>DROPPED after trim</system-reminder>",
		"stamped then reminder": "[from studio-mini · " + mpeLogin + "] <system-reminder>DROPPED after stamp strip</system-reminder>",
	} {
		t.Run(name, func(t *testing.T) {
			projects := mineHome(t)
			now, since := mpeWindow()
			var rec string
			if strings.HasPrefix(text, "[from ") {
				rec = mpeUser(t, now-3000, "u-drop", "sess-a", text, map[string]any{"promptSource": "sdk"})
			} else {
				rec = mpeTyped(t, now-3000, "u-drop", "sess-a", text)
			}
			mineWrite(t, projects, mpeProject, "sess-a.jsonl",
				rec,
				mpeTyped(t, now-2000, "u-keep", "sess-a", "CONTROL typed prompt"),
			)
			res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
			mpeOnly(t, res, "CONTROL typed prompt")
		})
	}
}

// The denylist's contents as the contract enumerates them: "the machine
// prefixes …: `[kraken pr-watch`, `/color `, `/handoff`, `/compact`, `/model
// claude-`, `kraken: `, `you stalled`, `slack: `, `orch: `, `steering from `
// is NOT in it".
func TestMPEOperatorDenylistMembership(t *testing.T) {
	have := map[string]bool{}
	for _, p := range OperatorDenylist {
		have[p] = true
		if p == "" {
			t.Error("OperatorDenylist holds an empty prefix, which would match every text at offset 0")
		}
	}
	for _, want := range []string{"[kraken pr-watch", "/color ", "/handoff", "/compact", "/model claude-", "kraken: ", "you stalled", "slack: ", "orch: "} {
		if !have[want] {
			t.Errorf("OperatorDenylist lacks %q", want)
		}
	}
	for p := range have {
		if strings.HasPrefix(p, "steering from") {
			t.Errorf("OperatorDenylist holds %q — `steering from ` is NOT in it (reactivation keeps the operator half)", p)
		}
	}
}

// "a text whose match is at offset 0, or whose kept part is empty, drops the
// record" — for every entry of the exported list.
func TestMPEDenylistAtOffsetZeroDropsTheRecord(t *testing.T) {
	for _, prefix := range OperatorDenylist {
		if prefix == "" {
			continue
		}
		t.Run(prefix, func(t *testing.T) {
			projects := mineHome(t)
			now, since := mpeWindow()
			mineWrite(t, projects, mpeProject, "sess-a.jsonl",
				mpeTyped(t, now-3000, "u-drop", "sess-a", prefix+"DROPPED machine tail"),
				mpeTyped(t, now-2000, "u-keep", "sess-a", "CONTROL typed prompt"),
			)
			res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
			mpeOnly(t, res, "CONTROL typed prompt")
		})
	}
}

// "whose kept part is empty, drops the record": whitespace before the match
// trims to nothing.
func TestMPEDenylistWhitespaceOnlyKeptPartDrops(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, now-3000, "u-drop", "sess-a", "   \n /handoff"),
		mpeTyped(t, now-2000, "u-keep", "sess-a", "CONTROL typed prompt"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	mpeOnly(t, res, "CONTROL typed prompt")
}

// "the text is CUT at the EARLIEST match and the part before it kept
// (trimmed)" — two different prefixes, the earlier one in the text wins
// regardless of their order in the list.
func TestMPEDenylistCutsAtTheEarliestOfTwoMatches(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, now-3000, "u-glued", "sess-a", "keep this half  you stalled (idle) — pick up /compact Keep one sentence"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	mpeOnly(t, res, "keep this half")
}

// ---------------------------------------------------------------------------
// identity rung
// ---------------------------------------------------------------------------

// "a record whose snake-case `session_id` is non-empty and differs from the
// file stem is a fork replay and yields nothing".
func TestMPEIdentityRungDropsForkReplay(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-fork.jsonl",
		mpeUser(t, now-3000, "u-parent", "sess-fork", "DROPPED replayed parent prompt", map[string]any{
			"origin": map[string]any{"kind": "human"}, "promptSource": "typed", "session_id": "sess-parent",
		}),
		mpeTyped(t, now-2000, "u-keep", "sess-fork", "CONTROL typed prompt"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	mpeOnly(t, res, "CONTROL typed prompt")
}

// "one uuid is emitted once across ALL files (first seen wins, files visited
// in sorted path order)" — the sorted-first file's copy is the one kept.
func TestMPEIdentityRungOneUUIDOnceSortedFirstWins(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	first := mineWrite(t, projects, mpeProject, "aaa.jsonl",
		mpeTyped(t, now-3000, "u-shared", "aaa", "THE SHARED PROMPT"),
	)
	mineWrite(t, projects, mpeProject, "zzz.jsonl",
		mpeTyped(t, now-2000, "u-shared", "zzz", "THE SHARED PROMPT"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	if len(res.Records) != 1 {
		t.Fatalf("records = %q, want the shared uuid emitted exactly once", mpeTexts(res))
	}
	if r := res.Records[0]; r.TranscriptPath != first || r.Session != "aaa" {
		t.Fatalf("kept record from %q (session %q), want the sorted-first file %q", r.TranscriptPath, r.Session, first)
	}
}

// "in a file whose head `agentName` begins with `zeus-`, the FIRST candidate
// record of the file (the staged job prompt) yields nothing" — the first
// CANDIDATE, so a non-candidate record ahead of it does not use the slot up.
// The head name rides a bookkeeping `agent-name` record (mine_test's shape)
// so the user records themselves carry no agentName.
func TestMPEIdentityRungZeusFirstCandidateDropped(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, "-Users-carl--kraken-cells-demo-7-wt-kraken", "sess-z.jsonl",
		`{"type":"agent-name","agentName":"zeus-demo"}`,
		mpeUser(t, now-5000, "u-tr", "sess-z", []any{mineToolResult("t0", false, "noise")}, nil),
		mpeTyped(t, now-4000, "u-spec", "sess-z", "THE STAGED JOB PROMPT"),
		mpeAsst(t, now-3000, "on it"),
		mpeTyped(t, now-2000, "u-carl", "sess-z", "SECOND CANDIDATE KEPT"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	mpeOnly(t, res, "SECOND CANDIDATE KEPT")
}

// "`Skipped` = root files excluded as teammate transcripts (… a non-lead
// `agentName`)" — the file yields nothing and counts once.
func TestMPETeammateRootIsSkipped(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-team.jsonl",
		mpeUser(t, now-3000, "u-team", "sess-team", "DROPPED teammate brief", map[string]any{
			"origin": map[string]any{"kind": "human"}, "promptSource": "typed",
			"agentName": "athena-logic", "teamName": "mental-model-pass",
		}),
	)
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, now-2000, "u-keep", "sess-a", "CONTROL typed prompt"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	mpeOnly(t, res, "CONTROL typed prompt")
	if res.Skipped != 1 {
		t.Fatalf("Skipped = %d, want 1 (the teammate root)", res.Skipped)
	}
}

// "walks every `~/.claude/projects/*/*.jsonl` ROOT file (never
// `<session>/subagents/`)"; "`Scanned` = root transcript files opened".
func TestMPESidecarIsNeverRead(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, now-3000, "u-keep", "sess-a", "CONTROL typed prompt"),
	)
	mineWrite(t, projects, mpeProject, filepath.Join("sess-a", "subagents", "agent-a1.jsonl"),
		mpeTyped(t, now-2000, "u-side", "agent-a1", "DROPPED sidecar prompt"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	mpeOnly(t, res, "CONTROL typed prompt")
	if res.Scanned != 1 {
		t.Fatalf("Scanned = %d, want 1 — a sidecar is not a root transcript file", res.Scanned)
	}
}

// "`Truncated` = files whose scan ended early (a line over `mineScanBufMax` …)
// — their records read so far are KEPT".
func TestMPETruncatedFileKeepsRecordsReadBeforeTheCut(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	over := strings.Repeat("x", mineScanBufMax+(1<<20))
	mineWrite(t, projects, mpeProject, "sess-big.jsonl",
		mpeTyped(t, now-4000, "u-before", "sess-big", "READ BEFORE THE CUT"),
		mpeUser(t, now-3000, "u-huge", "sess-big", []any{mineToolResult("t1", false, over)}, nil),
		mpeTyped(t, now-2000, "u-after", "sess-big", "UNREACHABLE AFTER THE CUT"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin})
	if res.Truncated != 1 {
		t.Fatalf("Truncated = %d, want 1", res.Truncated)
	}
	mpeOnly(t, res, "READ BEFORE THE CUT")
}

// ---------------------------------------------------------------------------
// context
// ---------------------------------------------------------------------------

func mpeFind(t *testing.T, res *OperatorResult, uuid string) OperatorRecord {
	t.Helper()
	for _, r := range res.Records {
		if r.UUID == uuid {
			return r
		}
	}
	t.Fatalf("no record with uuid %q in %q", uuid, mpeTexts(res))
	return OperatorRecord{}
}

// "a record at the file's start has an empty `Before`"; "both slices never
// nil"; and context never reaches into another file — the sibling file's
// later sections are not this record's Before.
func TestMPEContextAtFileStartIsEmptyAndNeverCrossesFiles(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "aaa.jsonl",
		mpeAsst(t, now-9000, "OTHER-FILE-ASSISTANT-WORDS"),
		mpeTyped(t, now-8000, "u-other", "aaa", "other file prompt"),
		mpeAsst(t, now-7000, "OTHER-FILE-TRAILING-WORDS"),
	)
	mineWrite(t, projects, mpeProject, "bbb.jsonl",
		mpeTyped(t, now-3000, "u-start", "bbb", "FIRST LINE OF ITS FILE"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin, Before: 6, After: 2})
	r := mpeFind(t, res, "u-start")
	if r.Context.Before == nil || r.Context.After == nil {
		t.Fatalf("context slices nil: %+v", r.Context)
	}
	if len(r.Context.Before) != 0 {
		t.Fatalf("Before = %+v, want empty for a record at its file's start", r.Context.Before)
	}
	if len(r.Context.After) != 0 {
		t.Fatalf("After = %+v, want empty for a file's only section — the sibling file is another session", r.Context.After)
	}
}

// "`Before`/`After` … negative values read as 0".
func TestMPEContextNegativeRadiiReadAsZero(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeAsst(t, now-5000, "PRECEDING"),
		mpeTyped(t, now-4000, "u-mid", "sess-a", "MIDDLE PROMPT"),
		mpeAsst(t, now-3000, "FOLLOWING"),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin, Before: -3, After: -1})
	r := mpeFind(t, res, "u-mid")
	if r.Context.Before == nil || r.Context.After == nil {
		t.Fatalf("context slices nil: %+v", r.Context)
	}
	if len(r.Context.Before) != 0 || len(r.Context.After) != 0 {
		t.Fatalf("context = %+v, want both sides empty for negative radii", r.Context)
	}
}

// "each block's `Text` is capped at 1,500 runes with the suffix `… [N more
// characters elided]`" — counted in RUNES (a multi-byte rune fixture), and a
// block of exactly 1,500 runes is left alone.
func TestMPEContextBlockCapIs1500RunesWithExactMarker(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, now-5000, "u-cap", "sess-a", "THE CAPPED PROMPT"),
		mpeAsst(t, now-4000, strings.Repeat("é", 2000)),
		mpeTyped(t, now-3000, "u-exact", "sess-a", "THE EXACT PROMPT"),
		mpeAsst(t, now-2000, strings.Repeat("ü", 1500)),
	)
	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin, Before: 0, After: 1})

	capped := mpeFind(t, res, "u-cap")
	if len(capped.Context.After) != 1 {
		t.Fatalf("After = %+v, want one block", capped.Context.After)
	}
	if want := strings.Repeat("é", 1500) + "… [500 more characters elided]"; capped.Context.After[0].Text != want {
		t.Fatalf("capped block text = %.80q… (%d runes), want 1500 runes + the exact marker", capped.Context.After[0].Text, len([]rune(capped.Context.After[0].Text)))
	}

	exact := mpeFind(t, res, "u-exact")
	if len(exact.Context.After) != 1 || exact.Context.After[0].Text != strings.Repeat("ü", 1500) {
		t.Fatalf("a 1500-rune block was altered: %+v", exact.Context.After)
	}
}

// "when the capped blocks of one record total more than 8,000 runes, blocks
// are dropped farthest-from-the-record first until they fit, and the dropped
// count is stated as one trailing block `{label: "…", text: "[K blocks
// elided]"}` on the side it was dropped from". Eight capped blocks of ~1,530
// runes each: five fit (≈7,650), six do not (≈9,180), so the three farthest go.
func TestMPEContextRecordBudgetDropsFarthestFirst(t *testing.T) {
	projects := mineHome(t)
	now, since := mpeWindow()
	lines := []string{mpeTyped(t, now-20_000, "u-budget", "sess-a", "THE BUDGETED PROMPT")}
	for i := 1; i <= 8; i++ {
		tag := "B" + string(rune('0'+i)) + "|"
		lines = append(lines, mpeAsst(t, now-20_000+int64(i)*1000, tag+strings.Repeat("x", 2000-len(tag))))
	}
	mineWrite(t, projects, mpeProject, "sess-a.jsonl", lines...)

	res := mpeProse(t, OperatorOptions{Since: since, Login: mpeLogin, Before: 0, After: 8})
	r := mpeFind(t, res, "u-budget")
	after := r.Context.After
	if len(after) != 6 {
		t.Fatalf("After has %d blocks, want 5 kept + 1 trailing elision block: %+v", len(after), after)
	}
	for i := 0; i < 5; i++ {
		tag := "B" + string(rune('0'+i+1)) + "|"
		if !strings.HasPrefix(after[i].Text, tag) {
			t.Errorf("After[%d] = %.12q…, want the %d-th nearest block (%s)", i, after[i].Text, i+1, tag)
		}
	}
	if want := (ContextBlock{Label: "…", Text: "[3 blocks elided]"}); after[5] != want {
		t.Fatalf("trailing block = %+v, want %+v", after[5], want)
	}
	if len(r.Context.Before) != 0 {
		t.Fatalf("Before = %+v, want empty (radius 0)", r.Context.Before)
	}
}

// ---------------------------------------------------------------------------
// AroundRecord
// ---------------------------------------------------------------------------

func mpeAroundFixture(t *testing.T) string {
	t.Helper()
	projects := mineHome(t)
	now, _ := mpeWindow()
	body := strings.Repeat("♦", 4000) + strings.Repeat("♣", 1000)
	return mineWrite(t, projects, mpeProject, "sess-a.jsonl",
		mpeTyped(t, now-5000, "u-hit", "sess-a", "THE AROUND PROMPT"),
		mineAssistant(t, now-4000, "claude-opus-5", mineToolUse("t1", "Read", map[string]any{"file_path": "/x.txt"})),
		mpeUser(t, now-3000, "u-tr", "sess-a", []any{mineToolResult("t1", false, body)}, map[string]any{"toolUseResult": map[string]any{}}),
		mpeAsst(t, now-2000, "AFTER THE RESULT"),
	)
}

// "`found` false (nil blocks, nil error) when no record carries `uuid`".
func TestMPEAroundRecordUnknownUUID(t *testing.T) {
	p := mpeAroundFixture(t)
	blocks, _, found, err := AroundRecord(p, "u-nowhere", 5, false)
	if err != nil || found || blocks != nil {
		t.Fatalf("AroundRecord(unknown) = (%v, found %v, err %v), want (nil, false, nil)", blocks, found, err)
	}
	blocks, _, found, err = AroundRecord(p, "u-nowhere", 5, true)
	if err != nil || found || blocks != nil {
		t.Fatalf("AroundRecord(unknown, raw) = (%v, found %v, err %v), want (nil, false, nil)", blocks, found, err)
	}
}

// "an open or read failure is an error".
func TestMPEAroundRecordMissingFileIsError(t *testing.T) {
	if _, _, _, err := AroundRecord(filepath.Join(t.TempDir(), "nope.jsonl"), "u-hit", 3, false); err == nil {
		t.Fatal("AroundRecord on a missing file = nil error, want an error")
	}
}

// "`radius` clamped at 0" — radius 0 and a negative radius both return the
// record's own section alone, at index 0.
func TestMPEAroundRecordRadiusZeroAndNegative(t *testing.T) {
	p := mpeAroundFixture(t)
	for _, radius := range []int{0, -4} {
		blocks, hit, found, err := AroundRecord(p, "u-hit", radius, false)
		if err != nil || !found {
			t.Fatalf("radius %d: found %v err %v", radius, found, err)
		}
		if len(blocks) != 1 || hit != 0 {
			t.Fatalf("radius %d: %d blocks, hit %d, want the record alone at 0: %+v", radius, len(blocks), hit, blocks)
		}
		if !strings.Contains(blocks[0].Text, "THE AROUND PROMPT") {
			t.Fatalf("radius %d: the lone block is %+v, want the record's own section", radius, blocks[0])
		}
	}
}

// "with `raw == true` … `Label` = `[<type>]` (… `[tool_result]` …) and `Text`
// = the record's text with a `tool_result` body truncated to 4,000 runes with
// the `… [N more characters elided]` suffix".
func TestMPEAroundRecordRawTruncatesToolResultAt4000Runes(t *testing.T) {
	p := mpeAroundFixture(t)
	blocks, _, found, err := AroundRecord(p, "u-hit", 2, true)
	if err != nil || !found {
		t.Fatalf("found %v err %v", found, err)
	}
	var tr *ContextBlock
	for i := range blocks {
		if blocks[i].Label == "[tool_result]" {
			tr = &blocks[i]
		}
	}
	if tr == nil {
		t.Fatalf("no [tool_result] block in raw mode: %+v", blocks)
	}
	if strings.Contains(tr.Text, "♣") {
		t.Fatal("raw tool_result block carries runes past the 4,000-rune cap")
	}
	if !strings.Contains(tr.Text, strings.Repeat("♦", 4000)) {
		t.Fatal("raw tool_result block lost runes inside the 4,000-rune cap")
	}
	if !strings.HasSuffix(tr.Text, "… [1000 more characters elided]") {
		t.Fatalf("raw tool_result block ends %q, want the exact elision suffix", tr.Text[max(0, len(tr.Text)-60):])
	}
}
