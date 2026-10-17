package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// handoff_contract_test.go — athena-logic's BLIND contract tests for the
// handoff build's slice A, package main half (50-contract.md § "Slice A"):
// runHandoffHook, ctxHookFlags' new --slug return, runCtxHook's cell mode and
// userStatusLineCommand.
//
// Dimension: LOGIC — the stated obligations and happy paths, one answer per
// assertion. Refusals, malformed input and failure fallbacks are athena-edge's.
//
// Every package-level identifier is prefixed `logic`: a sibling author shares
// this package's declaration space.

// logicHandoffSentence is the closing sentence the contract freezes verbatim.
const logicHandoffSentence = "Read the one that is yours before anything else; a doc older than the work you remember is history."

// logicNow is a fixed clock so every age is exact.
var logicNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// logicWriteDoc writes name into dir with an mtime `age` before logicNow and
// returns its absolute path.
func logicWriteDoc(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("# handoff\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	mt := logicNow.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

// logicHookOut is the frozen output shape.
type logicHookOut struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// logicRunHandoff runs the hook and returns raw stdout.
func logicRunHandoff(t *testing.T, payload, dir string) string {
	t.Helper()
	var out bytes.Buffer
	runHandoffHook(strings.NewReader(payload), &out, dir, logicNow)
	return out.String()
}

// logicDecodeHandoff asserts stdout is exactly ONE JSON object followed by a
// newline, with exactly the two frozen keys, and returns it.
func logicDecodeHandoff(t *testing.T, raw string) logicHookOut {
	t.Helper()
	if !strings.HasSuffix(raw, "\n") {
		t.Fatalf("stdout %q does not end with a newline", raw)
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	var generic map[string]map[string]any
	if err := dec.Decode(&generic); err != nil {
		t.Fatalf("stdout %q is not a JSON object of objects: %v", raw, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("stdout %q carries more than one JSON value", raw)
	}
	if len(generic) != 1 {
		t.Fatalf("top-level keys = %v, want exactly hookSpecificOutput", generic)
	}
	inner, ok := generic["hookSpecificOutput"]
	if !ok {
		t.Fatalf("top-level keys = %v, want hookSpecificOutput", generic)
	}
	if len(inner) != 2 {
		t.Fatalf("hookSpecificOutput keys = %v, want exactly hookEventName and additionalContext", inner)
	}
	var out logicHookOut
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return out
}

// U6: one doc, source compact → the frozen shape naming the doc's absolute
// path and its age in whole minutes, ending with the frozen sentence.
func TestLogicHandoffHookOneDoc(t *testing.T) {
	dir := t.TempDir()
	doc := logicWriteDoc(t, dir, "80-handoff.md", 12*time.Minute)

	out := logicDecodeHandoff(t, logicRunHandoff(t, `{"session_id":"sess-1","source":"compact"}`, dir))
	if got := out.HookSpecificOutput.HookEventName; got != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", got)
	}
	text := out.HookSpecificOutput.AdditionalContext
	if !strings.Contains(text, doc) {
		t.Errorf("additionalContext %q does not name the doc's absolute path %q", text, doc)
	}
	if !strings.Contains(text, "written 12m ago") {
		t.Errorf("additionalContext %q does not carry the age `written 12m ago`", text)
	}
	if !strings.HasSuffix(text, logicHandoffSentence) {
		t.Errorf("additionalContext %q does not end with the frozen sentence", text)
	}
}

// The standing rules open the context on every compaction and the doc listing
// follows them, so the rules are read first and the listing still ends in its
// frozen closing sentence.
func TestLogicHandoffHookRulesPrecedeTheListing(t *testing.T) {
	dir := t.TempDir()
	doc := logicWriteDoc(t, dir, "80-handoff.md", 3*time.Minute)

	out := logicDecodeHandoff(t, logicRunHandoff(t, `{"session_id":"sess-1","source":"compact"}`, dir))
	text := out.HookSpecificOutput.AdditionalContext
	if !strings.HasPrefix(text, handoffStandingRules) {
		t.Errorf("additionalContext %q does not open with the standing rules", text)
	}
	if strings.Index(text, handoffStandingRules) > strings.Index(text, doc) {
		t.Errorf("additionalContext %q lists the doc before the rules", text)
	}
	if !strings.HasSuffix(text, logicHandoffSentence) {
		t.Errorf("additionalContext %q does not end with the frozen sentence", text)
	}
}

// A compaction with no handoff doc — an idle auto-compaction's shape — still
// injects the rules, and nothing else: no listing header and no closing
// sentence, since neither has a doc to be about. The two rules are named
// because they are the ones a compaction summary drops: the Edit-tool route
// for repo files, and spawning teammates from the worktree root.
func TestLogicHandoffHookNoDocStillRestatesTheRules(t *testing.T) {
	out := logicDecodeHandoff(t, logicRunHandoff(t, `{"session_id":"sess-1","source":"compact"}`, t.TempDir()))
	if got := out.HookSpecificOutput.HookEventName; got != "SessionStart" {
		t.Errorf("hookEventName = %q, want SessionStart", got)
	}
	text := out.HookSpecificOutput.AdditionalContext
	if text != handoffStandingRules {
		t.Errorf("additionalContext = %q, want exactly the standing rules", text)
	}
	for _, want := range []string{"Edit or Write tool", "never through a Bash heredoc", "worktree root"} {
		if !strings.Contains(text, want) {
			t.Errorf("standing rules %q do not name %q", text, want)
		}
	}
	if strings.Contains(text, logicHandoffSentence) {
		t.Errorf("no-doc context %q carries the listing's closing sentence", text)
	}
}

// A doc written at `now` is `written 0m ago` (the contract's own example).
func TestLogicHandoffHookZeroMinuteAge(t *testing.T) {
	dir := t.TempDir()
	logicWriteDoc(t, dir, "80-handoff.md", 0)

	out := logicDecodeHandoff(t, logicRunHandoff(t, `{"session_id":"sess-1","source":"compact"}`, dir))
	if text := out.HookSpecificOutput.AdditionalContext; !strings.Contains(text, "written 0m ago") {
		t.Errorf("additionalContext %q does not carry `written 0m ago`", text)
	}
}

// U7: two docs → both named, newest mtime first, each with its own age; a
// file not matching 80-handoff*.md is not named.
func TestLogicHandoffHookTwoDocsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	older := logicWriteDoc(t, dir, "80-handoff.md", 30*time.Minute)
	newer := logicWriteDoc(t, dir, "80-handoff-hephaestus.md", 5*time.Minute)
	other := logicWriteDoc(t, dir, "00-overview.md", 1*time.Minute)

	out := logicDecodeHandoff(t, logicRunHandoff(t, `{"session_id":"sess-1","source":"compact"}`, dir))
	text := out.HookSpecificOutput.AdditionalContext
	iNew := strings.Index(text, newer)
	iOld := strings.Index(text, older)
	if iNew < 0 || iOld < 0 {
		t.Fatalf("additionalContext %q does not name both docs (%q, %q)", text, newer, older)
	}
	if iNew >= iOld {
		t.Errorf("newer doc at %d, older at %d — want newest mtime first", iNew, iOld)
	}
	for _, age := range []string{"written 5m ago", "written 30m ago"} {
		if !strings.Contains(text, age) {
			t.Errorf("additionalContext %q does not carry %q", text, age)
		}
	}
	if strings.Contains(text, other) {
		t.Errorf("additionalContext %q names %q, which does not match 80-handoff*.md", text, other)
	}
	if !strings.HasSuffix(text, logicHandoffSentence) {
		t.Errorf("additionalContext %q does not end with the frozen sentence", text)
	}
}

// A payload with NO source key is treated as compact: it writes.
func TestLogicHandoffHookAbsentSourceIsCompact(t *testing.T) {
	dir := t.TempDir()
	doc := logicWriteDoc(t, dir, "80-handoff.md", 3*time.Minute)

	out := logicDecodeHandoff(t, logicRunHandoff(t, `{"session_id":"sess-1"}`, dir))
	if text := out.HookSpecificOutput.AdditionalContext; !strings.Contains(text, doc) {
		t.Errorf("additionalContext %q does not name %q — an absent source must read as compact", text, doc)
	}
}

// ---------------------------------------------------------------------------
// ctxHookFlags
// ---------------------------------------------------------------------------

// ctxHookFlags returns the --slug value AND still sets the env vars.
func TestLogicCtxHookFlagsReturnsSlugAndSetsEnv(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "")
	t.Setenv("KRAKEN_DB_PATH", "")

	got := ctxHookFlags([]string{"--serve-addr", "127.0.0.1:9911", "--slug", "my-job", "--db-path", "/tmp/logic.db"})
	if got != "my-job" {
		t.Errorf("ctxHookFlags returned %q, want my-job", got)
	}
	if v := os.Getenv("KRAKEN_SERVE_ADDR"); v != "127.0.0.1:9911" {
		t.Errorf("KRAKEN_SERVE_ADDR = %q, want 127.0.0.1:9911", v)
	}
	if v := os.Getenv("KRAKEN_DB_PATH"); v != "/tmp/logic.db" {
		t.Errorf("KRAKEN_DB_PATH = %q, want /tmp/logic.db", v)
	}
}

// No --slug → "" (the report-nowhere form).
func TestLogicCtxHookFlagsNoSlugIsEmpty(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "")
	if got := ctxHookFlags([]string{"--serve-addr", "127.0.0.1:9912"}); got != "" {
		t.Errorf("ctxHookFlags without --slug returned %q, want \"\"", got)
	}
}

// --slug as the last word → "".
func TestLogicCtxHookFlagsTrailingSlugIsEmpty(t *testing.T) {
	if got := ctxHookFlags([]string{"--slug"}); got != "" {
		t.Errorf("ctxHookFlags with a trailing --slug returned %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// runCtxHook
// ---------------------------------------------------------------------------

type logicPost struct {
	path string
	body map[string]any
}

type logicRecorder struct {
	mu    sync.Mutex
	posts []logicPost
}

func (r *logicRecorder) all() []logicPost {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]logicPost(nil), r.posts...)
}

// logicServe points KRAKEN_SERVE_ADDR at a recording server answering 200.
func logicServe(t *testing.T) *logicRecorder {
	t.Helper()
	rec := &logicRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		rec.mu.Lock()
		rec.posts = append(rec.posts, logicPost{path: r.URL.Path, body: body})
		rec.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", srv.Listener.Addr().String())
	return rec
}

const logicStatusPayload = `{"session_id":"sess-1","context_window":{"used_percentage":42.5,"context_window_size":1000000}}`

// assertCellPost checks the one POST went to /api/cell/context with the
// frozen four keys.
func logicAssertCellPost(t *testing.T, rec *logicRecorder) {
	t.Helper()
	posts := rec.all()
	if len(posts) != 1 {
		t.Fatalf("posts = %+v, want exactly one", posts)
	}
	p := posts[0]
	if p.path != "/api/cell/context" {
		t.Errorf("POST path = %q, want /api/cell/context", p.path)
	}
	if p.body["slug"] != "my-job" {
		t.Errorf("body slug = %v, want my-job", p.body["slug"])
	}
	if p.body["session_id"] != "sess-1" {
		t.Errorf("body session_id = %v, want sess-1", p.body["session_id"])
	}
	if p.body["used_percentage"] != 42.5 {
		t.Errorf("body used_percentage = %v, want 42.5", p.body["used_percentage"])
	}
	if p.body["context_window_size"] != float64(1000000) {
		t.Errorf("body context_window_size = %v, want 1000000", p.body["context_window_size"])
	}
	if len(p.body) != 4 {
		t.Errorf("body = %v, want exactly slug, session_id, used_percentage, context_window_size", p.body)
	}
}

// Cell mode, no user statusline: POST to /api/cell/context and print the
// default line `<slug> NN%`. The percentage format is the same one the
// slug-less `ctx NN%` line uses (the contract writes both as `NN%`).
func TestLogicRunCtxHookCellModeDefaultLine(t *testing.T) {
	rec := logicServe(t)
	var out bytes.Buffer
	runCtxHook(strings.NewReader(logicStatusPayload), &out, "my-job", "")

	logicAssertCellPost(t, rec)
	if got, want := out.String(), "my-job 42.5%\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// Cell mode with a null percentage: no POST, the em-dash line.
func TestLogicRunCtxHookCellModeNullPct(t *testing.T) {
	rec := logicServe(t)
	var out bytes.Buffer
	runCtxHook(strings.NewReader(`{"session_id":"sess-1","context_window":{"used_percentage":null,"context_window_size":1000000}}`), &out, "my-job", "")

	if posts := rec.all(); len(posts) != 0 {
		t.Errorf("posts = %+v, want none for a null percentage", posts)
	}
	if got, want := out.String(), "my-job —\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// Cell mode with the user's statusline: still POSTs, and prints that
// command's stdout — run with the SAME stdin bytes (the command extracts the
// session id from its stdin, which proves the bytes reached it).
func TestLogicRunCtxHookCellModeWrapsUserStatusLine(t *testing.T) {
	rec := logicServe(t)
	userCmd := `sed -n 's/.*"session_id":"\([^"]*\)".*/mine sid=\1/p'`
	var out bytes.Buffer
	runCtxHook(strings.NewReader(logicStatusPayload), &out, "my-job", userCmd)

	logicAssertCellPost(t, rec)
	if got, want := out.String(), "mine sid=sess-1\n"; got != want {
		t.Errorf("stdout = %q, want the user's own line %q", got, want)
	}
}

// Slug empty: the slug-less form (the retired kronos orchestrator's
// statusline) reports NOWHERE and still prints a line, `ctx NN%`, so a
// settings file that still carries it renders rather than errors. Rewritten
// by the lead at the kronos-removal merge from a test that pinned the old
// /api/orch/context report; a sighted test, recorded as such in that job's
// build report.
func TestLogicRunCtxHookNoSlugReportsNowhere(t *testing.T) {
	rec := logicServe(t)
	var out bytes.Buffer
	runCtxHook(strings.NewReader(logicStatusPayload), &out, "", "")

	if posts := rec.all(); len(posts) != 0 {
		t.Fatalf("posts = %+v, want none without a slug", posts)
	}
	if got, want := out.String(), "ctx 42.5%\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// ---------------------------------------------------------------------------
// userStatusLineCommand
// ---------------------------------------------------------------------------

func logicWriteUserSettings(t *testing.T, home, content string) {
	t.Helper()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

func TestLogicUserStatusLineCommandReadsTheCommand(t *testing.T) {
	home := t.TempDir()
	logicWriteUserSettings(t, home, `{"model":"x","statusLine":{"type":"command","command":"~/bin/my-line --short"}}`)
	if got, want := userStatusLineCommand(home), "~/bin/my-line --short"; got != want {
		t.Errorf("userStatusLineCommand = %q, want %q", got, want)
	}
}

// Never wrap ourselves: a command containing ctx-hook reads as "".
func TestLogicUserStatusLineCommandSkipsCtxHook(t *testing.T) {
	home := t.TempDir()
	logicWriteUserSettings(t, home, `{"statusLine":{"type":"command","command":"/usr/local/bin/kraken ctx-hook --slug x"}}`)
	if got := userStatusLineCommand(home); got != "" {
		t.Errorf("userStatusLineCommand = %q, want \"\" for a ctx-hook command", got)
	}
}

func TestLogicUserStatusLineCommandNoKey(t *testing.T) {
	home := t.TempDir()
	logicWriteUserSettings(t, home, `{"model":"x"}`)
	if got := userStatusLineCommand(home); got != "" {
		t.Errorf("userStatusLineCommand = %q, want \"\" with no statusLine key", got)
	}
}
