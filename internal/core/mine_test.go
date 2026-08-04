package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixture helpers ---

// mineHome points util.Home() at a temp dir and returns ~/.claude/projects.
// Every test here builds its own transcripts under it; the real ~/.claude is
// never touched.
func mineHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	projects := filepath.Join(home, ".claude", "projects")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir projects: %v", err)
	}
	return projects
}

// mineWrite writes lines (already-serialized jsonl records, joined with
// "\n") to projects/<project>/<rel> and returns the path.
func mineWrite(t *testing.T, projects, project, rel string, lines ...string) string {
	t.Helper()
	path := filepath.Join(projects, project, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func mineISO(ms int64) string { return time.UnixMilli(ms).UTC().Format(time.RFC3339Nano) }

func mineJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return string(b)
}

func mineAssistant(t *testing.T, ts int64, model string, parts ...any) string {
	t.Helper()
	return mineJSON(t, map[string]any{
		"type": "assistant", "timestamp": mineISO(ts),
		"message": map[string]any{"model": model, "content": parts},
	})
}

func mineUser(t *testing.T, ts int64, content any) string {
	t.Helper()
	return mineJSON(t, map[string]any{
		"type": "user", "timestamp": mineISO(ts),
		"message": map[string]any{"role": "user", "content": content},
	})
}

func mineToolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}
}

func mineToolResult(id string, isErr bool, content string) map[string]any {
	return map[string]any{"type": "tool_result", "tool_use_id": id, "is_error": isErr, "content": content}
}

func mineText(s string) map[string]any { return map[string]any{"type": "text", "text": s} }

func mineFindDigest(t *testing.T, res *MineResult, suffix string) SessionDigest {
	t.Helper()
	for _, d := range res.Digests {
		if strings.HasSuffix(d.Path, suffix) {
			return d
		}
	}
	t.Fatalf("no digest for %s (have %d)", suffix, len(res.Digests))
	return SessionDigest{}
}

// --- tests ---

// TestMineTranscriptsDigestSignals covers every digest signal at once on a
// single realistic transcript: tool counts, is_error attributed to the tool
// that actually failed, interrupts, <synthetic> API give-ups, turn durations,
// and repeated identical calls.
func TestMineTranscriptsDigestSignals(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(6*time.Hour/time.Millisecond)
	at := func(minsAgo int64) int64 { return now - minsAgo*60_000 }

	lines := []string{
		// bookkeeping records: no timestamp, no signal, must not blow up
		`{"type":"mode","mode":"normal"}`,
		`{"type":"agent-name","agentName":"zeus-demo"}`,
		mineUser(t, at(120), "run the tests"),
		mineAssistant(t, at(119), "claude-opus-5",
			map[string]any{"type": "thinking", "thinking": "let me look"},
			mineToolUse("t1", "Bash", map[string]any{"command": "go test ./..."}),
		),
		mineUser(t, at(118), []any{mineToolResult("t1", false, "ok")}),
		// same command again — one repeated-call signature, count 2
		mineAssistant(t, at(117), "claude-opus-5", mineToolUse("t2", "Bash", map[string]any{"command": "go test ./..."})),
		mineUser(t, at(116), []any{mineToolResult("t2", false, "ok")}),
		mineAssistant(t, at(115), "claude-opus-5", mineToolUse("t3", "Read", map[string]any{"file_path": "/tmp/gone.go"})),
		mineUser(t, at(114), []any{mineToolResult("t3", true, "file not found")}),
		// a result whose tool_use never appeared: real failure, lost attribution
		mineUser(t, at(113), []any{mineToolResult("orphan", true, "boom")}),
		mineUser(t, at(112), "[Request interrupted by user for tool use]"),
		mineUser(t, at(111), []any{mineText("[Request interrupted by user]")}),
		mineAssistant(t, at(110), "<synthetic>", mineText("API Error: overloaded")),
		// output ceiling hit mid-thinking: no error, no interrupt, no synthetic
		// record — only stop_reason says the message was cut. TWO records, ONE
		// message.id: stop_reason is stamped on every record of a group, so
		// this is the fixture shape a per-record count fails on (it would
		// report 2) — the single-record form passes identically under the bug
		// and the fix, which is what let the per-record version ship green.
		mineJSON(t, map[string]any{"type": "assistant", "timestamp": mineISO(at(110)),
			"message": map[string]any{"id": "msg-ceiling", "model": "claude-opus-5", "stop_reason": "max_tokens",
				"content": []any{map[string]any{"type": "thinking", "thinking": "solving layout"}}}}),
		mineJSON(t, map[string]any{"type": "assistant", "timestamp": mineISO(at(110)),
			"message": map[string]any{"id": "msg-ceiling", "model": "claude-opus-5", "stop_reason": "max_tokens",
				"content": []any{mineText("partial answer")}}}),
		mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": 158600, "timestamp": mineISO(at(109))}),
		mineJSON(t, map[string]any{"type": "system", "subtype": "compact_boundary", "timestamp": mineISO(at(108))}),
	}
	mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess-a.jsonl", lines...)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if res.Since != since {
		t.Errorf("Since = %d, want %d", res.Since, since)
	}
	if res.Scanned != 1 {
		t.Fatalf("Scanned = %d, want 1", res.Scanned)
	}
	d := mineFindDigest(t, res, "sess-a.jsonl")

	if d.SessionID != "sess-a" {
		t.Errorf("SessionID = %q", d.SessionID)
	}
	if d.ProjectDir != "-Users-carl-Developer-monolith" {
		t.Errorf("ProjectDir = %q", d.ProjectDir)
	}
	if d.Subagent {
		t.Error("root transcript marked Subagent")
	}
	if d.ToolCounts["Bash"] != 2 || d.ToolCounts["Read"] != 1 {
		t.Errorf("ToolCounts = %v, want Bash:2 Read:1", d.ToolCounts)
	}
	if d.ErrorCounts["Read"] != 1 {
		t.Errorf("ErrorCounts[Read] = %d, want 1 (is_error attributed to the failing tool)", d.ErrorCounts["Read"])
	}
	if d.ErrorCounts["Bash"] != 0 {
		t.Errorf("ErrorCounts[Bash] = %d, want 0 (its results were clean)", d.ErrorCounts["Bash"])
	}
	if d.ErrorCounts["unknown"] != 1 {
		t.Errorf("ErrorCounts[unknown] = %d, want 1 (orphan result)", d.ErrorCounts["unknown"])
	}
	if d.Interrupts != 2 {
		t.Errorf("Interrupts = %d, want 2 (string and parts form)", d.Interrupts)
	}
	if d.SyntheticErrors != 1 {
		t.Errorf("SyntheticErrors = %d, want 1", d.SyntheticErrors)
	}
	if d.MaxTokensStops != 1 {
		t.Errorf("MaxTokensStops = %d, want 1 (two records, one message.id — one ceiling stop)", d.MaxTokensStops)
	}
	if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 158600 {
		t.Errorf("TurnDurations = %v, want [158600]", d.TurnDurations)
	}
	if got := d.RepeatedCalls["Bash(go test ./...)"]; got != 2 {
		t.Errorf("RepeatedCalls[Bash(go test ./...)] = %d, want 2 (have %v)", got, d.RepeatedCalls)
	}
	if len(d.RepeatedCalls) != 1 {
		t.Errorf("RepeatedCalls = %v, want only the >1 entry", d.RepeatedCalls)
	}
	if d.Lines != 15 {
		// the 15 timestamped records; the two undated bookkeeping lines
		// stay eligible for signals but are not part of the dated window
		t.Errorf("Lines = %d, want 15 (timestamped records only)", d.Lines)
	}
	if d.FirstTS != at(120) || d.LastTS != at(108) {
		t.Errorf("window = [%d,%d], want [%d,%d]", d.FirstTS, d.LastTS, at(120), at(108))
	}
}

// TestMineTranscriptsReplayedRecordsNotCounted: a forked side thread's
// transcript is seeded with its parent's records wholesale — session_id
// (snake case) survives the fork and is the only usable marker. The token
// counters were ownership-gated from the start; the MECHANICAL counters were
// not, so a resume replay doubled every tool count, error, interrupt and
// duration while its spend stayed correct (one real cell read 26 errors
// where 16 exist). Every signal must ignore a record another session made.
func TestMineTranscriptsReplayedRecordsNotCounted(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(6*time.Hour/time.Millisecond)
	at := func(minsAgo int64) int64 { return now - minsAgo*60_000 }

	withSession := func(line, sid string) string {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("reparse fixture: %v", err)
		}
		m["session_id"] = sid
		return mineJSON(t, m)
	}

	lines := []string{
		// replayed from the parent: every signal type, none may count. The
		// turn_duration record and one interrupt deliberately carry NO
		// session_id — the REAL shapes (0 of ~1,700 system records in the
		// corpus carry one, and ~13% of interrupt records drop it) — so they
		// are gated only by lastSID inheritance from the foreign records
		// around them; a fixture that stamped session_id on a system record
		// certified a record shape the producer never emits.
		withSession(mineAssistant(t, at(120), "claude-opus-5",
			mineToolUse("p1", "Bash", map[string]any{"command": "go vet ./..."})), "parent-session"),
		withSession(mineUser(t, at(119), []any{mineToolResult("p1", true, "boom")}), "parent-session"),
		withSession(mineUser(t, at(118), "[Request interrupted by user]"), "parent-session"),
		mineUser(t, at(118), "[Request interrupted by user]"), // no session_id: inherits parent
		withSession(mineAssistant(t, at(117), "<synthetic>", mineText("API Error")), "parent-session"),
		mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration",
			"durationMs": 99_000, "timestamp": mineISO(at(116))}), // no session_id: inherits parent
		withSession(mineJSON(t, map[string]any{"type": "assistant", "timestamp": mineISO(at(115)),
			"message": map[string]any{"id": "msg-p", "model": "claude-opus-5", "stop_reason": "max_tokens",
				"content": []any{map[string]any{"type": "thinking", "thinking": "x"}}}}), "parent-session"),
		// the fork's own records: all counted — including a turn_duration
		// with no session_id, owned by inheritance from the fork's records
		withSession(mineAssistant(t, at(60), "claude-opus-5",
			mineToolUse("f1", "Read", map[string]any{"file_path": "/tmp/a.go"})), "fork-session"),
		withSession(mineUser(t, at(59), []any{mineToolResult("f1", true, "gone")}), "fork-session"),
		mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration",
			"durationMs": 1_000, "timestamp": mineISO(at(58))}), // no session_id: inherits fork
	}
	mineWrite(t, projects, "-Users-carl-Developer-monolith", "fork-session.jsonl", lines...)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "fork-session.jsonl")

	if d.ToolCounts["Bash"] != 0 || d.ToolCounts["Read"] != 1 {
		t.Errorf("ToolCounts = %v, want only the fork's own Read:1", d.ToolCounts)
	}
	if d.ErrorCounts["Bash"] != 0 || d.ErrorCounts["Read"] != 1 {
		t.Errorf("ErrorCounts = %v, want only the fork's own Read:1", d.ErrorCounts)
	}
	if d.Interrupts != 0 {
		t.Errorf("Interrupts = %d, want 0 (both are the parent's, one via lastSID inheritance)", d.Interrupts)
	}
	if d.SyntheticErrors != 0 {
		t.Errorf("SyntheticErrors = %d, want 0 (replayed)", d.SyntheticErrors)
	}
	if d.MaxTokensStops != 0 {
		t.Errorf("MaxTokensStops = %d, want 0 (replayed)", d.MaxTokensStops)
	}
	if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 1_000 {
		t.Errorf("TurnDurations = %v, want [1000] — the fork's own; the parent's 99000 carries no session_id and is owned by inheritance", d.TurnDurations)
	}
}

// TestMineInputSummaryStripsShellBoilerplate: a command opening with
// `VAR=<cell scratchpad path>; ` or `cd <worktree> && ` used to spend the
// whole 100-rune signature window on the prefix, so 19 DISTINCT analysis
// commands sharing one scratchpad assignment digested as one call repeated
// 19 times — a loop-shaped false positive. The signature must land on the
// command; a command that is ONLY boilerplate keeps its original text.
func TestMineInputSummaryStripsShellBoilerplate(t *testing.T) {
	long := "/private/tmp/claude-501/-Users-carl--kraken-cells-some-cell-142-wt-monolith/0123456789abcdef-0123-4567-89ab-cdef01234567/scratchpad"
	sig := func(cmd string) string {
		return callSignature("Bash", json.RawMessage(mineJSON(t, map[string]any{"command": cmd})))
	}
	a := sig("S=" + long + "; python3 $S/rs_reachable.py 2>&1 | tail -50")
	b := sig("S=" + long + "; sed -n '1,45p' $S/fetch_matrices.py")
	if a == b {
		t.Errorf("two distinct commands behind one boilerplate prefix share a signature: %q", a)
	}
	if !strings.Contains(a, "python3") || !strings.Contains(b, "sed -n") {
		t.Errorf("signatures did not reach the command: %q / %q", a, b)
	}
	if got := sig("cd /Users/carl/dev/wt-monolith && go test ./..."); !strings.Contains(got, "go test") {
		t.Errorf("cd prefix not stripped: %q", got)
	}
	if got := sig("FOO=1; cd /tmp; BAR=2; rg pattern"); !strings.Contains(got, "rg pattern") {
		t.Errorf("mixed assignment/cd run not stripped: %q", got)
	}
	if got := sig("cd /Users/carl/dev/wt-monolith"); got != "Bash(cd /Users/carl/dev/wt-monolith)" {
		t.Errorf("boilerplate-only command lost its signature: %q", got)
	}
	// The strip is bounded by WHITESPACE inside values/paths: an env-prefix
	// form is one command and must survive whole, or the isolated-HOME
	// smoke-test shape would collapse to the signature of whatever follows
	// the first `;` — a first draft did exactly that to 591 real commands.
	if got := sig("HOME=/tmp/x go test -tags sqlite_fts5 ./internal/core ; echo done"); !strings.Contains(got, "HOME=/tmp/x go test") {
		t.Errorf("env-prefix command was mis-stripped: %q", got)
	}
	// Newline-terminated boilerplate is the dominant real multi-line shape;
	// the strip must stop at ITS OWN line, never eat through a later `;`.
	if got := sig("cd /Users/carl/repo\npython3 script.py; echo done"); !strings.Contains(got, "python3 script.py") {
		t.Errorf("newline-terminated cd not stripped to the command: %q", got)
	}
	a2 := sig("S=/tmp/scratch\npython3 $S/a.py; python3 $S/b.py")
	b2 := sig("S=/tmp/scratch\npython3 $S/OTHER.py; python3 $S/b.py")
	if a2 == b2 {
		t.Errorf("two distinct multi-line commands share a signature: %q", a2)
	}
	// The empty-after-strip fallback needs an input the regex fully consumes.
	if got := sig("cd /tmp;"); got != "Bash(cd /tmp;)" {
		t.Errorf("fully-stripped command lost its signature: %q", got)
	}
}

// TestCallIdentityKeepsDistinctLongInputsDistinct: RepeatedCalls groups on
// callIdentity, not the 100-rune display signature. Grouping on the display
// window collapsed DISTINCT calls whenever their inputs shared a 100-rune
// prefix — python heredocs sharing an import preamble plus one long
// absolute path, and Read calls on staged transcripts whose 218-char paths
// share a 105-char scratchpad prefix — so the digest reported clusters of
// 9-11 in sessions with zero literal repeats, and three independent readers
// each re-measured the false positive before the grouping was widened.
func TestCallIdentityKeepsDistinctLongInputsDistinct(t *testing.T) {
	prefix := "/private/tmp/claude-501/-Users-carl--kraken-cells-some-cell-142-wt-kraken/01234567-89ab-cdef-0123-456789abcdef/scratchpad/scout-one/"
	readA := json.RawMessage(mineJSON(t, map[string]any{"file_path": prefix + "agent-aaaaaaaaaaaaaaaaa.reduced.txt"}))
	readB := json.RawMessage(mineJSON(t, map[string]any{"file_path": prefix + "agent-bbbbbbbbbbbbbbbbb.reduced.txt"}))
	if callSignature("Read", readA) != callSignature("Read", readB) {
		t.Fatalf("test premise broken: the two paths no longer collide in the display window")
	}
	if callIdentity("Read", readA) == callIdentity("Read", readB) {
		t.Errorf("distinct paths behind a shared >100-rune prefix share an identity: %q", callIdentity("Read", readA))
	}

	heredoc := "python3 - <<'PYEOF'\nimport json\np = \"/Users/carl/.claude/projects/-Users-carl--kraken-cells-some-cell-142-wt-kraken/"
	cmdA := json.RawMessage(mineJSON(t, map[string]any{"command": heredoc + "a.jsonl\"\nprint(1)\nPYEOF"}))
	cmdB := json.RawMessage(mineJSON(t, map[string]any{"command": heredoc + "b.jsonl\"\nprint(2)\nPYEOF"}))
	if callIdentity("Bash", cmdA) == callIdentity("Bash", cmdB) {
		t.Errorf("distinct heredocs behind a shared preamble share an identity")
	}

	// An identical re-run is still one identity — the signal the counter
	// exists for — including when only ignorable boilerplate differs. The
	// re-run is constructed independently so this cannot degenerate into
	// x != x on one value.
	cmdA2 := json.RawMessage(mineJSON(t, map[string]any{"command": heredoc + "a.jsonl\"\nprint(1)\nPYEOF"}))
	if callIdentity("Bash", cmdA) != callIdentity("Bash", cmdA2) {
		t.Errorf("identical commands do not share an identity")
	}
	again := json.RawMessage(mineJSON(t, map[string]any{"command": "cd /Users/carl/other && " + heredoc + "a.jsonl\"\nprint(1)\nPYEOF"}))
	if callIdentity("Bash", cmdA) != callIdentity("Bash", again) {
		t.Errorf("boilerplate-only difference split one identity in two")
	}
}

// TestDigestRepeatedCallsGroupsOnIdentity pins the fix at the DIGEST level,
// not just on the helper: the counting line must key on callIdentity, and a
// revert to callSignature there keeps every unit test above green while the
// digest quietly resumes reporting false clusters (a one-token mutation the
// review round demonstrated). Two distinct Reads sharing a >100-rune prefix
// must produce NO RepeatedCalls entry while a literal Bash re-run still
// counts — the value assertion, not just helper agreement.
func TestDigestRepeatedCallsGroupsOnIdentity(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)
	at := func(minsAgo int64) int64 { return now - minsAgo*60_000 }

	prefix := "/private/tmp/claude-501/-Users-carl--kraken-cells-some-cell-142-wt-kraken/01234567-89ab-cdef-0123-456789abcdef/scratchpad/scout-one/"
	lines := []string{
		mineUser(t, at(50), "read the staged views"),
		mineAssistant(t, at(49), "claude-opus-5", mineToolUse("r1", "Read", map[string]any{"file_path": prefix + "agent-aaaaaaaaaaaaaaaaa.reduced.txt"})),
		mineUser(t, at(48), []any{mineToolResult("r1", false, "ok")}),
		mineAssistant(t, at(47), "claude-opus-5", mineToolUse("r2", "Read", map[string]any{"file_path": prefix + "agent-bbbbbbbbbbbbbbbbb.reduced.txt"})),
		mineUser(t, at(46), []any{mineToolResult("r2", false, "ok")}),
		mineAssistant(t, at(45), "claude-opus-5", mineToolUse("b1", "Bash", map[string]any{"command": "go test ./..."})),
		mineUser(t, at(44), []any{mineToolResult("b1", false, "ok")}),
		mineAssistant(t, at(43), "claude-opus-5", mineToolUse("b2", "Bash", map[string]any{"command": "go test ./..."})),
		mineUser(t, at(42), []any{mineToolResult("b2", false, "ok")}),
	}
	mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess-ident.jsonl", lines...)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if len(res.Digests) != 1 {
		t.Fatalf("digests = %d, want 1", len(res.Digests))
	}
	d := mineFindDigest(t, res, "sess-ident.jsonl")
	if got := d.RepeatedCalls["Bash(go test ./...)"]; got != 2 {
		t.Errorf("RepeatedCalls[Bash(go test ./...)] = %d, want 2 (have %v)", got, d.RepeatedCalls)
	}
	if len(d.RepeatedCalls) != 1 {
		t.Errorf("RepeatedCalls = %v, want ONLY the literal Bash repeat — a Read entry here means the digest is grouping on the display window again", d.RepeatedCalls)
	}
}

// TestMineTranscriptsExcludesOldRecords: file mtime selects the candidate,
// the per-record timestamp bounds the window. A session whose turn ran for
// 92 minutes has a fresh mtime and stale records; the stale ones must not
// count.
func TestMineTranscriptsExcludesOldRecords(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)
	old := since - int64(48*time.Hour/time.Millisecond)

	mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineAssistant(t, old, "claude-opus-5", mineToolUse("t0", "Grep", map[string]any{"pattern": "TODO"})),
		mineUser(t, old, []any{mineToolResult("t0", true, "nope")}),
		mineUser(t, old, "[Request interrupted by user]"),
		mineAssistant(t, old, "<synthetic>", mineText("API Error")),
		mineJSON(t, map[string]any{"type": "assistant", "timestamp": mineISO(old),
			"message": map[string]any{"id": "msg-old", "model": "claude-opus-5", "stop_reason": "max_tokens",
				"content": []any{mineText("cut off")}}}),
		mineJSON(t, map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": 999, "timestamp": mineISO(old)}),
		mineAssistant(t, now-1000, "claude-opus-5", mineToolUse("t1", "Bash", map[string]any{"command": "make build"})),
	)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	d := mineFindDigest(t, res, "sess.jsonl")
	if d.ToolCounts["Grep"] != 0 {
		t.Errorf("out-of-window tool counted: %v", d.ToolCounts)
	}
	if d.ToolCounts["Bash"] != 1 {
		t.Errorf("in-window tool missing: %v", d.ToolCounts)
	}
	if len(d.ErrorCounts) != 0 || d.Interrupts != 0 || d.SyntheticErrors != 0 ||
		d.MaxTokensStops != 0 || len(d.TurnDurations) != 0 {
		t.Errorf("out-of-window signals leaked: errs=%v int=%d syn=%d max=%d durs=%v",
			d.ErrorCounts, d.Interrupts, d.SyntheticErrors, d.MaxTokensStops, d.TurnDurations)
	}
	if d.Lines != 1 {
		t.Errorf("Lines = %d, want 1 (only the in-window record)", d.Lines)
	}
	if d.FirstTS != now-1000 || d.LastTS != now-1000 {
		t.Errorf("window = [%d,%d], want both %d", d.FirstTS, d.LastTS, now-1000)
	}
}

// TestMineTranscriptsSubagentAndCategory: sidecars under
// <session>/subagents/ are included and marked, and Category keys off the
// kraken-cells marker in the munged project dir name.
func TestMineTranscriptsSubagentAndCategory(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	cellDir := "-Users-carl--kraken-cells-demo-job-wt-kraken"
	mineWrite(t, projects, cellDir, "zeus.jsonl",
		mineAssistant(t, now-1000, "claude-opus-5", mineToolUse("t1", "Bash", map[string]any{"command": "ls"})))
	mineWrite(t, projects, cellDir, filepath.Join("zeus", "subagents", "agent-abc123.jsonl"),
		mineAssistant(t, now-900, "claude-opus-5", mineToolUse("t2", "Read", map[string]any{"file_path": "/x.go"})))
	mineWrite(t, projects, "-Users-carl-Developer-monolith", "other.jsonl",
		mineAssistant(t, now-800, "claude-opus-5", mineToolUse("t3", "Glob", map[string]any{"pattern": "*.go"})))

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if res.Scanned != 3 {
		t.Fatalf("Scanned = %d, want 3 (root + sidecar + other)", res.Scanned)
	}

	zeus := mineFindDigest(t, res, filepath.Join(cellDir, "zeus.jsonl"))
	if zeus.Subagent || zeus.Category != "kraken" {
		t.Errorf("zeus digest: Subagent=%v Category=%q, want false/kraken", zeus.Subagent, zeus.Category)
	}
	side := mineFindDigest(t, res, "agent-abc123.jsonl")
	if !side.Subagent {
		t.Error("sidecar not marked Subagent")
	}
	if side.Category != "kraken" {
		t.Errorf("sidecar Category = %q, want kraken", side.Category)
	}
	if side.SessionID != "agent-abc123" {
		t.Errorf("sidecar SessionID = %q", side.SessionID)
	}
	if side.ToolCounts["Read"] != 1 {
		t.Errorf("sidecar tool calls dropped: %v", side.ToolCounts)
	}
	other := mineFindDigest(t, res, "other.jsonl")
	if other.Category != "other" {
		t.Errorf("non-cell Category = %q, want other", other.Category)
	}
}

// TestMineTranscriptsSkipsStaleFiles: a file whose mtime predates the window
// is a Skipped candidate, never opened.
func TestMineTranscriptsSkipsStaleFiles(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	stale := mineWrite(t, projects, "-Users-carl-Developer-old", "stale.jsonl",
		mineAssistant(t, now-1000, "claude-opus-5", mineToolUse("t1", "Bash", map[string]any{"command": "ls"})))
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	mineWrite(t, projects, "-Users-carl-Developer-new", "fresh.jsonl",
		mineAssistant(t, now-1000, "claude-opus-5", mineToolUse("t1", "Bash", map[string]any{"command": "ls"})))

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if res.Scanned != 1 || res.Skipped != 1 {
		t.Fatalf("Scanned=%d Skipped=%d, want 1/1", res.Scanned, res.Skipped)
	}
	if !strings.HasSuffix(res.Digests[0].Path, "fresh.jsonl") {
		t.Errorf("scanned the wrong file: %s", res.Digests[0].Path)
	}
}

// TestMineTranscriptsMalformedAndTruncated: a garbage line mid-file and a
// truncated final line (the normal shape of a transcript being appended to
// right now) cost only themselves.
func TestMineTranscriptsMalformedAndTruncated(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := filepath.Join(projects, "-Users-carl-Developer-monolith", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := strings.Join([]string{
		mineAssistant(t, now-3000, "claude-opus-5", mineToolUse("t1", "Bash", map[string]any{"command": "ls"})),
		`{"type":"assistant", NOT JSON AT ALL`,
		"",
		mineAssistant(t, now-2000, "claude-opus-5", mineToolUse("t2", "Read", map[string]any{"file_path": "/x.go"})),
		`{"type":"assistant","message":{"content":[{"type":"tool_u`, // truncated final line, no newline
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if res.Scanned != 1 {
		t.Fatalf("Scanned = %d, want 1 (a malformed line must not fail the file)", res.Scanned)
	}
	d := res.Digests[0]
	if d.ToolCounts["Bash"] != 1 || d.ToolCounts["Read"] != 1 {
		t.Errorf("ToolCounts = %v, want Bash:1 Read:1 (records either side of the garbage)", d.ToolCounts)
	}
}

// TestMineTranscriptsHugeLine: an 8 MB single line is real (a tool result
// inlined whole) and is exactly why the scanner buffer is raised. With
// bufio's 64 KB default the scan would end here and every later record would
// silently vanish.
func TestMineTranscriptsHugeLine(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	huge := strings.Repeat("x", 8<<20)
	mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineAssistant(t, now-3000, "claude-opus-5", mineToolUse("t1", "Read", map[string]any{"file_path": "/big.txt"})),
		mineUser(t, now-2900, []any{mineToolResult("t1", false, huge)}),
		mineAssistant(t, now-2000, "claude-opus-5", mineToolUse("t2", "Bash", map[string]any{"command": "wc -l /big.txt"})),
	)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if res.Scanned != 1 {
		t.Fatalf("Scanned = %d, want 1", res.Scanned)
	}
	d := res.Digests[0]
	if d.ToolCounts["Bash"] != 1 {
		t.Errorf("record after the 8MB line lost: %v", d.ToolCounts)
	}
	if d.Lines != 3 {
		t.Errorf("Lines = %d, want 3", d.Lines)
	}
	if d.Truncated {
		t.Error("Truncated set for a line UNDER the ceiling")
	}
	if res.Truncated != 0 {
		t.Errorf("MineResult.Truncated = %d, want 0", res.Truncated)
	}
}

// TestMineTranscriptsOverCeilingIsReported is the failure the whole
// Truncated flag exists for: a line ABOVE mineScanBufMax (the 8 MB case
// above is only half the ceiling, so it cannot catch this) ends the scan,
// and everything after it is invisible. A partial digest that looked
// complete would report a handful of tool calls and zero errors for a busy
// session, and the night would read clean. So it must be reported, and
// ReduceTranscript must fail closed rather than hand back a fragment.
func TestMineTranscriptsOverCeilingIsReported(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	over := strings.Repeat("x", mineScanBufMax+(1<<20)) // one line past the ceiling
	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineAssistant(t, now-3000, "claude-opus-5", mineToolUse("t1", "Read", map[string]any{"file_path": "/big.txt"})),
		mineUser(t, now-2900, []any{mineToolResult("t1", false, over)}),
		// everything past the oversized line is unreachable
		mineAssistant(t, now-2000, "claude-opus-5", mineToolUse("t2", "Bash", map[string]any{"command": "wc -l /big.txt"})),
	)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if res.Scanned != 1 {
		t.Fatalf("Scanned = %d, want 1", res.Scanned)
	}
	d := res.Digests[0]
	if !d.Truncated {
		t.Error("digest built from a partial scan is not marked Truncated")
	}
	if res.Truncated != 1 {
		t.Errorf("MineResult.Truncated = %d, want 1", res.Truncated)
	}
	// the premise: the scan really did stop, so this is a floor not a total
	if d.ToolCounts["Bash"] != 0 {
		t.Errorf("expected the post-ceiling record to be unreachable, got %v", d.ToolCounts)
	}

	if _, err := ReduceTranscript(path, since); err == nil {
		t.Error("ReduceTranscript returned a fragment with no error")
	}
}

// TestMineInputSummaryMatchesSummarizeInput pins the narrow-struct decode to
// summarizeInput's behaviour: same key priority, same truncation. The digest
// avoids materialising a tool input (a Write carries its whole file body in
// `content`), and that optimisation must not quietly change what a signature
// reads as.
func TestMineInputSummaryMatchesSummarizeInput(t *testing.T) {
	inputs := []map[string]any{
		{"command": "go test ./..."},
		{"file_path": "/x/y.go", "content": strings.Repeat("z", 1<<20)}, // the case the struct exists for
		{"pattern": "TODO", "path": "/repo"},                            // `path` outranks `pattern` in both
		{"description": "do a thing"},
		{"prompt": strings.Repeat("long ", 200)}, // exercises truncation
		{"url": "https://example.com"},
		{"skill": "dream"},
		{"command": 42, "file_path": "/fallback.go"}, // non-string value must not abort the decode
		{"unknown_key": "nothing matches"},           // no recognised key: raw-JSON fallback
		{},
	}
	for _, in := range inputs {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		want := summarizeInput(in)
		got := mineInputSummary(raw)
		if got != want {
			t.Errorf("mineInputSummary(%v) = %q, summarizeInput = %q", in, got, want)
		}
	}
}

// TestMineTranscriptsNoProjectsDir: the one failure that is an error rather
// than a Skipped — the walk itself is impossible.
func TestMineTranscriptsNoProjectsDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := MineTranscripts(0); err == nil {
		t.Error("want error when ~/.claude/projects is absent")
	}
}

// TestReduceTranscript: user prose, tool calls and assistant text survive;
// thinking, tool results and attachments do not — and the result is
// materially smaller than the input.
func TestReduceTranscript(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)
	bulk := strings.Repeat("RESULT-NOISE ", 4000)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		`{"type":"mode","mode":"normal"}`,
		mineJSON(t, map[string]any{"type": "attachment", "timestamp": mineISO(now - 5000), "attachment": map[string]any{"content": bulk}}),
		mineUser(t, now-4000, "PLEASE-FIX-THE-FLAKY-TEST"),
		mineUser(t, now-3900, []any{mineText("<system-reminder>ignore me</system-reminder>")}),
		mineAssistant(t, now-3800, "claude-opus-5",
			map[string]any{"type": "thinking", "thinking": "SECRET-THINKING " + bulk},
			mineText("ASSISTANT-SAYS-THIS"),
			mineToolUse("t1", "Bash", map[string]any{"command": "go test ./internal/core/..."}),
		),
		mineUser(t, now-3700, []any{mineToolResult("t1", false, bulk)}),
		// older than since: excluded
		mineUser(t, since-60_000, "STALE-USER-TURN"),
	)

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)

	for _, want := range []string{
		"PLEASE-FIX-THE-FLAKY-TEST",
		"ASSISTANT-SAYS-THIS",
		"[tool] Bash(go test ./internal/core/...)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reduced output missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"SECRET-THINKING", "RESULT-NOISE", "system-reminder", "STALE-USER-TURN"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("reduced output leaked %q", unwanted)
		}
	}
	if i, j := strings.Index(got, "PLEASE-FIX"), strings.Index(got, "ASSISTANT-SAYS"); i < 0 || j < 0 || i > j {
		t.Errorf("turn order not preserved:\n%s", got)
	}
	if ratio := float64(len(out)) / float64(len(raw)); ratio > 0.1 {
		t.Errorf("reduction ratio %.3f, want a materially smaller output", ratio)
	}
	t.Logf("reduction: %d -> %d bytes (%.1f%%)", len(raw), len(out), 100*float64(len(out))/float64(len(raw)))
}

// TestReduceTranscriptEmitsMidTurnSteers: a message typed while a turn runs
// never becomes a `user` record — it is stored as a `queue-operation`
// (enqueue) or an `attachment` carrying a `queued_command` — and the reducer
// used to drop both, so a scout reading the reduced view saw the agent pivot
// with no visible prompt. These are course corrections, the exact messages
// the dream loop's prompting-feedback output exists to collect. Both storage
// shapes emit as [user mid-turn]; a non-enqueue queue-operation and a
// non-queued_command attachment stay dropped, and the since filter applies.
// Two more rules, both measured: task-notifications queue through this same
// channel in bulk (92 in one real session) and are dropped by prefix; and
// one real message is routinely stored as BOTH records (516 of 935 distinct
// queued texts corpus-wide — the measurement the reducer doc comment
// carries), so a repeat of an already-emitted text collapses to one section
// even when a second message's records interleave between the pair — a
// seen-set, not adjacency, which the interleaved fixture below exercises.
func TestReduceTranscriptEmitsMidTurnSteers(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineUser(t, now-5000, "the opening ask"),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 4000), "operation": "enqueue", "content": "STEER-VIA-QUEUE-OP"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 3900), "operation": "dequeue", "content": "DEQUEUE-NOISE"}),
		mineJSON(t, map[string]any{"type": "attachment", "timestamp": mineISO(now - 3800), "attachment": map[string]any{"type": "queued_command", "prompt": "STEER-VIA-QUEUED-COMMAND", "origin": map[string]any{"kind": "human"}}}),
		mineJSON(t, map[string]any{"type": "attachment", "timestamp": mineISO(now - 3700), "attachment": map[string]any{"type": "file", "content": "ATTACHMENT-NOISE"}}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(since - 60_000), "operation": "enqueue", "content": "STALE-STEER"}),
		// one message, double-written: enqueue + queued_command, same text —
		// INTERLEAVED with a second message's enqueue, the observed real
		// ordering (enq1, enq2, qc1), which adjacency-dedupe missed
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 3000), "operation": "enqueue", "content": "DOUBLE-WRITTEN-STEER"}),
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 2950), "operation": "enqueue", "content": "SECOND-INTERLEAVED-STEER"}),
		mineJSON(t, map[string]any{"type": "attachment", "timestamp": mineISO(now - 3000), "attachment": map[string]any{"type": "queued_command", "prompt": "DOUBLE-WRITTEN-STEER", "origin": map[string]any{"kind": "human"}}}),
		// task-notifications queue through the same channel and stay dropped
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 2900), "operation": "enqueue", "content": "<task-notification>\n<task-id>a1</task-id>\nNOTIFICATION-NOISE"}),
		// so does the harness's stopped-agents notice — a user ACTION in the
		// harness's prose, which must not enter prompting feedback as carl's
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 2800), "operation": "enqueue", "content": "3 background agents were stopped by the user: \"AGENTS-STOPPED-NOISE\""}),
		// a queued teammate envelope routes to the teammate label, not [user]
		mineJSON(t, map[string]any{"type": "queue-operation", "timestamp": mineISO(now - 2700), "operation": "enqueue", "content": "<teammate-message teammate_id=\"athena-logic\">QUEUED-TEAMMATE-TEXT</teammate-message>"}),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)

	for _, want := range []string{
		"[user mid-turn]\nSTEER-VIA-QUEUE-OP",
		"[user mid-turn]\nSTEER-VIA-QUEUED-COMMAND",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("reduced output missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"DEQUEUE-NOISE", "ATTACHMENT-NOISE", "STALE-STEER", "NOTIFICATION-NOISE", "AGENTS-STOPPED-NOISE"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("reduced output leaked %q:\n%s", unwanted, got)
		}
	}
	if n := strings.Count(got, "DOUBLE-WRITTEN-STEER"); n != 1 {
		t.Errorf("double-written steer emitted %d times, want exactly 1:\n%s", n, got)
	}
	if !strings.Contains(got, "SECOND-INTERLEAVED-STEER") {
		t.Errorf("interleaved second steer missing:\n%s", got)
	}
	if !strings.Contains(got, "[teammate athena-logic mid-turn]\nQUEUED-TEAMMATE-TEXT") {
		t.Errorf("queued teammate envelope not routed to the teammate label:\n%s", got)
	}
	if strings.Contains(got, "[user mid-turn]\n<teammate-message") {
		t.Errorf("queued teammate envelope attributed to carl:\n%s", got)
	}
}

// TestReduceTranscriptSeparatesTeammateFromUser is the label that matters
// most: [user] means carl. A brief zeus sends hephaestus arrives inside a
// user record wrapped in a <teammate-message> envelope, and emitting it as
// [user] would quote kraken's own prompts back to carl as his. Both content
// shapes (plain string and text part) route the same way, and an idle-poll
// heartbeat collapses to one line rather than riding along as prose.
func TestReduceTranscriptSeparatesTeammateFromUser(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	brief := `<teammate-message teammate_id="zeus-demo">IMPLEMENT-THE-SLICE-AS-BRIEFED</teammate-message>`
	idle := `<teammate-message teammate_id="athena-logic">{"type":"idle_notification","idleReason":"waiting"}</teammate-message>`

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineUser(t, now-5000, "CARL-SAID-THIS"),
		mineUser(t, now-4000, brief),
		mineUser(t, now-3000, []any{mineText(idle)}),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)

	if !strings.Contains(got, "[user]\nCARL-SAID-THIS") {
		t.Errorf("carl's own turn lost or mislabelled:\n%s", got)
	}
	if !strings.Contains(got, "[teammate zeus-demo]") {
		t.Errorf("teammate brief not labelled by sender:\n%s", got)
	}
	if !strings.Contains(got, "[teammate athena-logic]") {
		t.Errorf("teammate text part not labelled by sender:\n%s", got)
	}
	// the actual regression: teammate traffic emitted as if carl wrote it
	if strings.Contains(got, "[user]\nIMPLEMENT-THE-SLICE-AS-BRIEFED") {
		t.Errorf("teammate brief attributed to carl:\n%s", got)
	}
	if strings.Contains(got, "teammate-message") {
		t.Errorf("raw envelope leaked into the reduced view:\n%s", got)
	}
	if !strings.Contains(got, "idle (waiting)") {
		t.Errorf("idle heartbeat not collapsed:\n%s", got)
	}
	if strings.Contains(got, "idle_notification") {
		t.Errorf("idle heartbeat emitted verbatim:\n%s", got)
	}
	if n := strings.Count(got, "[user]"); n != 1 {
		t.Errorf("[user] appears %d times, want 1 (carl's turn only):\n%s", n, got)
	}
}

// TestReduceTranscriptCapsHugeText: a text block is bounded by construction,
// not by luck. Nothing today is near the cap, but one oversized block must
// not be able to dominate a scout's payload — and when it is cut, the cut is
// stated rather than silent.
func TestReduceTranscriptCapsHugeText(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineUser(t, now-2000, strings.Repeat("A", mineTextCap+50_000)),
		mineAssistant(t, now-1000, "claude-opus-5", mineText("SHORT-TAIL")),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)
	if len(got) > mineTextCap+2000 {
		t.Errorf("emitted %d bytes for a capped block, want ~%d", len(got), mineTextCap)
	}
	if !strings.Contains(got, "more characters elided") {
		t.Errorf("cap applied silently:\n%.200s", got)
	}
	if !strings.Contains(got, "SHORT-TAIL") {
		t.Error("record after the capped block lost")
	}
}

// TestMineTranscriptsSkipsContentFreeDigest: a file touched inside the window
// whose every record predates it has nothing to say. Counting it as Scanned
// would overstate coverage with an empty row.
func TestMineTranscriptsSkipsContentFreeDigest(t *testing.T) {
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)
	old := since - int64(48*time.Hour/time.Millisecond)

	mineWrite(t, projects, "-Users-carl-Developer-monolith", "stale-content.jsonl",
		mineUser(t, old, "ancient turn"),
		mineAssistant(t, old, "claude-opus-5", mineToolUse("t0", "Bash", map[string]any{"command": "ls"})),
	)
	mineWrite(t, projects, "-Users-carl-Developer-monolith", "real.jsonl",
		mineAssistant(t, now-1000, "claude-opus-5", mineToolUse("t1", "Bash", map[string]any{"command": "ls"})),
	)

	res, err := MineTranscripts(since)
	if err != nil {
		t.Fatalf("MineTranscripts: %v", err)
	}
	if res.Scanned != 1 || res.Skipped != 1 {
		t.Fatalf("Scanned=%d Skipped=%d, want 1/1", res.Scanned, res.Skipped)
	}
	if len(res.Digests) != 1 || !strings.HasSuffix(res.Digests[0].Path, "real.jsonl") {
		t.Errorf("content-free digest kept: %+v", res.Digests)
	}
}

func TestReduceTranscriptMissingFile(t *testing.T) {
	if _, err := ReduceTranscript(filepath.Join(t.TempDir(), "nope.jsonl"), 0); err == nil {
		t.Error("want error for a missing transcript")
	}
}

// TestReduceTranscriptRedactsSecrets: the reduced view is what every dream
// reader and the graph ingest copy OUT of a transcript, so a credential
// pasted into a pane once would be re-copied into a fresh transcript on
// every later sweep that touches the session. Emission redacts
// vendor-prefixed token shapes in prose and in tool-call summaries, keeping
// a short identifying prefix; prose that merely talks about tokens, and
// names that share a prefix with one (sk-learn), pass through untouched.
func TestReduceTranscriptRedactsSecrets(t *testing.T) {
	const slackTok = "xoxc-1111111111111-2222222222222-3333333333333-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const cookie = "xoxd-HYd6B4%2Fabcdefghijklmnop"
	projects := mineHome(t)
	now := time.Now().UnixMilli()
	since := now - int64(time.Hour/time.Millisecond)

	path := mineWrite(t, projects, "-Users-carl-Developer-monolith", "sess.jsonl",
		mineUser(t, now-5000, "here you go: "+slackTok),
		mineAssistant(t, now-4000, "claude-opus-5",
			mineText("adding the cookie line now; sk-learn stays, and the docs say ghp_ tokens rotate"),
			mineToolUse("t1", "Bash", map[string]any{"command": "printf 'SLACK_D_COOKIE=" + cookie + "' >> ~/.kraken/slack.env"}),
		),
	)

	out, err := ReduceTranscript(path, since)
	if err != nil {
		t.Fatalf("ReduceTranscript: %v", err)
	}
	got := string(out)
	for _, leaked := range []string{slackTok, cookie} {
		if strings.Contains(got, leaked) {
			t.Errorf("reduced output leaked a credential with prefix %q", leaked[:8])
		}
	}
	for _, want := range []string{"xoxc-…[redacted]", "xoxd-…[redacted]", "sk-learn", "ghp_ tokens"} {
		if !strings.Contains(got, want) {
			t.Errorf("reduced output missing %q:\n%s", want, got)
		}
	}
}

// TestRedactSecretsSurvivesSummaryTruncation: redaction must run before the
// call-summary truncation windows, not after — a credential sitting past
// rune 100 of a Bash command would otherwise be CUT to a fragment too short
// to match any shape, and the fragment would walk through the redactor into
// both callSignature and callIdentity output (the review probe that found
// it: `printf 'SLACK_D_COOKIE=xoxd-HYd` — truncated, unmatched, emitted).
func TestRedactSecretsSurvivesSummaryTruncation(t *testing.T) {
	pad := strings.Repeat("x", 90)
	input := json.RawMessage(`{"command":"echo ` + pad + ` && printf 'SLACK_D_COOKIE=xoxd-HYd6B4%2Fabcdefghijklmnopqrstuvwxyz' >> ~/.kraken/slack.env"}`)
	for _, got := range []string{callSignature("Bash", input), callIdentity("Bash", input)} {
		if strings.Contains(got, "xoxd-H") {
			t.Errorf("summary leaked a credential fragment: %q", got)
		}
	}
	if got := callIdentity("Bash", input); !strings.Contains(got, "xoxd-…[redacted]") {
		t.Errorf("identity summary missing redaction marker: %q", got)
	}
}

// TestRedactSecrets: the prefix rule must redact every vendor shape whole —
// including a compact-header JWT (`{"alg":"HS256"}` is 17 chars after eyJ,
// which a 20-char floor silently missed) — while leaving prose that merely
// talks about tokens, and prefix-sharing names like sk-learn, untouched.
func TestRedactSecrets(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sk-ant-api03-aaaaaaaaaaaaaaaaaaaaaaaa done", "sk-an…[redacted] done"},
		{"github_pat_11AAAAAAA0aaaaaaaaaaaaaa", "githu…[redacted]"},
		{"key AKIAIOSFODNN7EXAMPLE end", "key AKIAI…[redacted] end"},
		{"jwt eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0", "jwt eyJhb…[redacted]"},
		{"compact eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.sig", "compact eyJhb…[redacted]"},
		{"d=xoxd-9Aa1Bb2C/c3Dd4Ee+5Ff6Gg7Hh8Ii9Jj0Kk=", "d=xoxd-…[redacted]"},
		{"d=xoxd-AA+BBBBBBBBBBBBBBBBBBBBBBBBBB", "d=xoxd-…[redacted]"},
		{"plain prose about a session token and a cookie", "plain prose about a session token and a cookie"},
		{"use sk-learn and deriveState()", "use sk-learn and deriveState()"},
		{"branch sk-abcdefghijklmno stays", "branch sk-abcdefghijklmno stays"},
		{"key sk-abcdefghijklmnop goes", "key sk-ab…[redacted] goes"},
	}
	for _, c := range cases {
		if got := redactSecrets(c.in); got != c.want {
			t.Errorf("redactSecrets(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
