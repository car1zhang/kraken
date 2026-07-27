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
	if len(d.TurnDurations) != 1 || d.TurnDurations[0] != 158600 {
		t.Errorf("TurnDurations = %v, want [158600]", d.TurnDurations)
	}
	if got := d.RepeatedCalls["Bash(go test ./...)"]; got != 2 {
		t.Errorf("RepeatedCalls[Bash(go test ./...)] = %d, want 2 (have %v)", got, d.RepeatedCalls)
	}
	if len(d.RepeatedCalls) != 1 {
		t.Errorf("RepeatedCalls = %v, want only the >1 entry", d.RepeatedCalls)
	}
	if d.Lines != 13 {
		// the 13 timestamped records; the two undated bookkeeping lines
		// stay eligible for signals but are not part of the dated window
		t.Errorf("Lines = %d, want 13 (timestamped records only)", d.Lines)
	}
	if d.FirstTS != at(120) || d.LastTS != at(108) {
		t.Errorf("window = [%d,%d], want [%d,%d]", d.FirstTS, d.LastTS, at(120), at(108))
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
	if len(d.ErrorCounts) != 0 || d.Interrupts != 0 || d.SyntheticErrors != 0 || len(d.TurnDurations) != 0 {
		t.Errorf("out-of-window signals leaked: errs=%v int=%d syn=%d durs=%v",
			d.ErrorCounts, d.Interrupts, d.SyntheticErrors, d.TurnDurations)
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
