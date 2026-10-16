package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// handoff_edge_test.go — the EDGE dimension of slice A of
// artifacts/50-contract.md (the handoff skill build), package main half:
// runHandoffHook's silent arms and its two-doc ordering, ctxHookFlags' --slug
// boundaries, runCtxHook's slugged no-POST and fallback arms, and
// userStatusLineCommand's "" arms. Happy paths and stated invariants belong to
// the sibling blind file.
//
// Written blind: no byte of cmd/kraken/cmd_handoffhook.go or of the slice-A
// edits to cmd_ctx.go was read while authoring this (cmd_ctx.go was read at
// HEAD only).
//
// Every package-level identifier here is prefixed `edge`.

// --- helpers ---------------------------------------------------------------

// edgeHandoffNow is the fixed clock every runHandoffHook test passes.
var edgeHandoffNow = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

// edgeHandoffDocDir returns a fresh dir holding one 80-handoff.md written
// "now", so a negative test is non-vacuous: a doc IS present, and the silence
// has to come from the arm under test.
func edgeHandoffDocDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	edgeWriteAged(t, dir, "80-handoff.md", 0)
	return dir
}

// edgeWriteAged writes name into dir with mtime = edgeHandoffNow - age.
func edgeWriteAged(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("# handoff\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	mt := edgeHandoffNow.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

// edgeRunHandoff runs the hook and returns its stdout.
func edgeRunHandoff(t *testing.T, stdin, dir string) string {
	t.Helper()
	var out bytes.Buffer
	runHandoffHook(strings.NewReader(stdin), &out, dir, edgeHandoffNow)
	return out.String()
}

// edgeHandoffContext decodes the hook's one JSON object and returns its
// additionalContext, failing on any other shape (contract: exactly one JSON
// object followed by a newline).
func edgeHandoffContext(t *testing.T, out string) string {
	t.Helper()
	if !strings.HasSuffix(out, "\n") {
		t.Fatalf("hook output does not end in a newline: %q", out)
	}
	dec := json.NewDecoder(strings.NewReader(out))
	var got struct {
		HookSpecificOutput struct {
			HookEventName     string `json:"hookEventName"`
			AdditionalContext string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("hook output is not a JSON object: %v\n%q", err, out)
	}
	var extra json.RawMessage
	if err := dec.Decode(&extra); err == nil {
		t.Fatalf("hook output carries more than one JSON value: %q", out)
	}
	if got.HookSpecificOutput.HookEventName != "SessionStart" {
		t.Fatalf("hookEventName = %q, want SessionStart", got.HookSpecificOutput.HookEventName)
	}
	return got.HookSpecificOutput.AdditionalContext
}

const edgeCompactPayload = `{"session_id":"s-1","source":"compact"}`

// --- runHandoffHook: the silent arms --------------------------------------

func TestEdgeHandoffHookSilentOnBadStdin(t *testing.T) {
	cases := map[string]string{
		"empty":          "",
		"whitespace":     "   \n",
		"not json":       "this is not json",
		"truncated json": `{"session_id":"s-1","source":"comp`,
		"json array":     `[{"session_id":"s-1","source":"compact"}]`,
		"json string":    `"compact"`,
		"json null":      `null`,
		"json number":    `42`,
	}
	for name, stdin := range cases {
		t.Run(name, func(t *testing.T) {
			dir := edgeHandoffDocDir(t)
			if out := edgeRunHandoff(t, stdin, dir); out != "" {
				t.Fatalf("stdin %q wrote %q, want nothing (contract: not a JSON object → nothing)", stdin, out)
			}
		})
	}
}

func TestEdgeHandoffHookSilentOnNonCompactSource(t *testing.T) {
	for _, src := range []string{"resume", "startup", "clear", "", "COMPACT"} {
		t.Run("source="+src, func(t *testing.T) {
			dir := edgeHandoffDocDir(t)
			payload := `{"session_id":"s-1","source":` + string(edgeMustJSON(t, src)) + `}`
			if out := edgeRunHandoff(t, payload, dir); out != "" {
				t.Fatalf("source %q wrote %q, want nothing (contract: source present and not \"compact\" → nothing)", src, out)
			}
		})
	}
}

// A source ABSENT from the payload is treated as compact.
func TestEdgeHandoffHookAbsentSourceIsCompact(t *testing.T) {
	dir := edgeHandoffDocDir(t)
	out := edgeRunHandoff(t, `{"session_id":"s-1"}`, dir)
	if out == "" {
		t.Fatalf("absent source wrote nothing; contract treats an absent source as compact")
	}
	ctx := edgeHandoffContext(t, out)
	if !strings.Contains(ctx, filepath.Join(dir, "80-handoff.md")) {
		t.Fatalf("context does not name the doc by absolute path:\n%s", ctx)
	}
}

// edgeRulesOnly asserts a compaction with no doc to list still injects the
// standing rules — and ONLY them: no listing header, no doc path, no closing
// sentence (the closing belongs to the listing). The pre-rules contract was
// "no docs → nothing"; the rules exist for exactly the no-doc compaction (an
// idle auto-compaction has no handoff doc), so silence is no longer right.
func edgeRulesOnly(t *testing.T, out, what string) {
	t.Helper()
	if out == "" {
		t.Fatalf("%s wrote nothing, want the standing rules", what)
	}
	ctx := edgeHandoffContext(t, out)
	if ctx != handoffStandingRules {
		t.Fatalf("%s context = %q, want exactly the standing rules", what, ctx)
	}
}

func TestEdgeHandoffHookRulesOnlyOnMissingDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	edgeRulesOnly(t, edgeRunHandoff(t, edgeCompactPayload, dir), "missing dir")
}

func TestEdgeHandoffHookRulesOnlyOnEmptyDirArg(t *testing.T) {
	// cmdHandoffHook resolves the dir from --artifacts; an absent flag leaves
	// it "", which names no existing directory.
	edgeRulesOnly(t, edgeRunHandoff(t, edgeCompactPayload, ""), "empty artifactsDir")
}

func TestEdgeHandoffHookRulesOnlyOnEmptyDir(t *testing.T) {
	edgeRulesOnly(t, edgeRunHandoff(t, edgeCompactPayload, t.TempDir()), "empty dir")
}

func TestEdgeHandoffHookRulesOnlyOnUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"00-overview.md",
		"50-contract.md",
		"80-handoff.txt",
		"80-handoff.md.bak",
		"79-handoff.md",
		"x80-handoff.md",
		"handoff.md",
	} {
		edgeWriteAged(t, dir, name, time.Minute)
	}
	edgeRulesOnly(t, edgeRunHandoff(t, edgeCompactPayload, dir), "dir with no 80-handoff*.md")
}

// A dir argument that is a regular FILE is a dir that does not exist.
func TestEdgeHandoffHookRulesOnlyWhenDirIsAFile(t *testing.T) {
	f := edgeWriteAged(t, t.TempDir(), "80-handoff.md", 0)
	edgeRulesOnly(t, edgeRunHandoff(t, edgeCompactPayload, f), "a file passed as artifactsDir")
}

// --- runHandoffHook: two docs ----------------------------------------------

// U7: older 80-handoff.md, newer 80-handoff-hephaestus.md → both, newest
// first, each with its whole-minute age relative to now.
func TestEdgeHandoffHookTwoDocsNewestFirst(t *testing.T) {
	dir := t.TempDir()
	older := edgeWriteAged(t, dir, "80-handoff.md", 12*time.Minute)
	newer := edgeWriteAged(t, dir, "80-handoff-hephaestus.md", 0)
	edgeWriteAged(t, dir, "00-overview.md", 0) // unrelated, must not appear

	ctx := edgeHandoffContext(t, edgeRunHandoff(t, edgeCompactPayload, dir))

	iNew := strings.Index(ctx, newer)
	iOld := strings.Index(ctx, older)
	if iNew < 0 || iOld < 0 {
		t.Fatalf("context must name both docs by absolute path (%s, %s):\n%s", newer, older, ctx)
	}
	if iNew > iOld {
		t.Fatalf("newer doc listed after older doc; contract: newest mtime first:\n%s", ctx)
	}
	between := ctx[iNew:iOld]
	if !strings.Contains(between, "written 0m ago") {
		t.Fatalf("newer doc's age `written 0m ago` not between it and the older doc:\n%s", ctx)
	}
	after := ctx[iOld:]
	if !strings.Contains(after, "written 12m ago") {
		t.Fatalf("older doc's age `written 12m ago` not after it:\n%s", ctx)
	}
	if strings.Contains(ctx, filepath.Join(dir, "00-overview.md")) {
		t.Fatalf("context names an unrelated artifact:\n%s", ctx)
	}
	const closing = "Read the one that is yours before anything else; a doc older than the work you remember is history."
	if !strings.HasSuffix(ctx, closing) {
		t.Fatalf("context does not end with the contract sentence:\n%s", ctx)
	}
}

// Never panics on any of the arms (a panic would fail the turn it fired on).
func TestEdgeHandoffHookNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("runHandoffHook panicked: %v", r)
		}
	}()
	var out bytes.Buffer
	runHandoffHook(edgeErrReader{}, &out, edgeHandoffDocDir(t), edgeHandoffNow)
	if out.Len() != 0 {
		t.Fatalf("unreadable stdin wrote %q, want nothing", out.String())
	}
	runHandoffHook(strings.NewReader(`{"source":{"nested":true}}`), &out, edgeHandoffDocDir(t), edgeHandoffNow)
	runHandoffHook(strings.NewReader(`{"session_id":7,"source":"compact"}`), &out, edgeHandoffDocDir(t), time.Time{})
}

// edgeErrReader fails every read — contract: unreadable stdin → nothing.
type edgeErrReader struct{}

func (edgeErrReader) Read([]byte) (int, error) { return 0, os.ErrClosed }

// --- ctxHookFlags: --slug --------------------------------------------------

func TestEdgeCtxHookFlagsSlugAsLastWordIsEmpty(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "")
	t.Setenv("KRAKEN_DB_PATH", "")
	if got := ctxHookFlags([]string{"--slug"}); got != "" {
		t.Fatalf("ctxHookFlags([--slug]) = %q, want \"\"", got)
	}
	if got := ctxHookFlags([]string{"--serve-addr", "127.0.0.1:9", "--slug"}); got != "" {
		t.Fatalf("trailing --slug = %q, want \"\"", got)
	}
}

func TestEdgeCtxHookFlagsSlugAbsentIsEmpty(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "")
	t.Setenv("KRAKEN_DB_PATH", "")
	if got := ctxHookFlags(nil); got != "" {
		t.Fatalf("ctxHookFlags(nil) = %q, want \"\"", got)
	}
	if got := ctxHookFlags([]string{"bare", "--db-path", "/tmp/x.db"}); got != "" {
		t.Fatalf("no --slug = %q, want \"\"", got)
	}
}

func TestEdgeCtxHookFlagsUnknownWordsIgnored(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "")
	t.Setenv("KRAKEN_DB_PATH", "")
	got := ctxHookFlags([]string{"junk", "--unknown", "x", "--slug", "my-job", "--other", "--serve-addr", "127.0.0.1:7"})
	if got != "my-job" {
		t.Fatalf("ctxHookFlags = %q, want my-job with unknown words ignored", got)
	}
	if v := os.Getenv("KRAKEN_SERVE_ADDR"); v != "127.0.0.1:7" {
		t.Fatalf("KRAKEN_SERVE_ADDR = %q, want the flag still honoured beside --slug", v)
	}
}

// --- runCtxHook with a slug ------------------------------------------------

// edgeCtxServer counts POSTs and records their paths.
type edgeCtxServer struct {
	hits  atomic.Int64
	paths chan string
}

func edgeStartCtxServer(t *testing.T) *edgeCtxServer {
	t.Helper()
	s := &edgeCtxServer{paths: make(chan string, 16)}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		select {
		case s.paths <- r.URL.Path:
		default:
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	return s
}

func edgeRunCtx(stdin, slug, userLine string) string {
	var out bytes.Buffer
	runCtxHook(strings.NewReader(stdin), &out, slug, userLine)
	return out.String()
}

func TestEdgeCtxHookSlugNullPctPrintsDashAndDoesNotPost(t *testing.T) {
	srv := edgeStartCtxServer(t)
	stdin := `{"session_id":"s-1","context_window":{"used_percentage":null,"context_window_size":200000}}`
	out := edgeRunCtx(stdin, "edge-job", "")
	if strings.TrimRight(out, "\n") != "edge-job —" {
		t.Fatalf("line = %q, want %q", out, "edge-job —")
	}
	if n := srv.hits.Load(); n != 0 {
		t.Fatalf("null percentage POSTed %d times, want 0", n)
	}
}

func TestEdgeCtxHookSlugBlankSessionDoesNotPost(t *testing.T) {
	srv := edgeStartCtxServer(t)
	stdin := `{"session_id":"","context_window":{"used_percentage":40,"context_window_size":200000}}`
	_ = edgeRunCtx(stdin, "edge-job", "printf carl-line")
	if n := srv.hits.Load(); n != 0 {
		t.Fatalf("blank session POSTed %d times, want 0", n)
	}
}

// With a slug the report goes to /api/cell/context and nowhere else.
func TestEdgeCtxHookSlugPostsOnlyCellRoute(t *testing.T) {
	srv := edgeStartCtxServer(t)
	stdin := `{"session_id":"s-1","context_window":{"used_percentage":40,"context_window_size":200000}}`
	_ = edgeRunCtx(stdin, "edge-job", "printf carl-line")
	if n := srv.hits.Load(); n != 1 {
		t.Fatalf("POST count = %d, want 1", n)
	}
	if p := <-srv.paths; p != "/api/cell/context" {
		t.Fatalf("slugged report POSTed to %q, want /api/cell/context", p)
	}
}

// The line is printed whatever happened to the POST — here, nothing listens.
func TestEdgeCtxHookSlugPrintsWhenServeIsDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)
	stdin := `{"session_id":"s-1","context_window":{"used_percentage":40,"context_window_size":200000}}`
	out := edgeRunCtx(stdin, "edge-job", "printf carl-line")
	if strings.TrimRight(out, "\n") != "carl-line" {
		t.Fatalf("line = %q, want carl's line printed despite the dead serve", out)
	}
}

// userStatusLine failure arms fall back to the default line. A null
// percentage keeps the default line's spelling unambiguous (`<slug> —`) and
// sends no POST, so nothing here depends on a server.
func TestEdgeCtxHookUserLineFallbacks(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "127.0.0.1:1")
	stdin := `{"session_id":"s-1","context_window":{"used_percentage":null}}`
	cases := map[string]string{
		"exits non-zero with output":    "echo partial; exit 3",
		"exits non-zero without output": "exit 7",
		"prints nothing":                "true",
		"command not found":             "/definitely/not/a/binary-edge",
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			out := edgeRunCtx(stdin, "edge-job", cmd)
			if strings.TrimRight(out, "\n") != "edge-job —" {
				t.Fatalf("userStatusLine %q → %q, want the default line %q", cmd, out, "edge-job —")
			}
		})
	}
}

// A userStatusLine that outlives ctxHookTimeout is cut off at that bound and
// the default line printed instead.
func TestEdgeCtxHookUserLineTimeoutFallsBack(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "127.0.0.1:1")
	stdin := `{"session_id":"s-1","context_window":{"used_percentage":null}}`
	start := time.Now()
	out := edgeRunCtx(stdin, "edge-job", "sleep 5; echo too-late")
	elapsed := time.Since(start)
	if strings.TrimRight(out, "\n") != "edge-job —" {
		t.Fatalf("slow userStatusLine → %q, want the default line", out)
	}
	if elapsed > ctxHookTimeout+1500*time.Millisecond {
		t.Fatalf("runCtxHook took %v, want it bounded by ctxHookTimeout (%v)", elapsed, ctxHookTimeout)
	}
}

// Exactly one line is printed, even when the user's command prints several
// or none — contract: "prints ONE line".
func TestEdgeCtxHookPrintsExactlyOneLineOnFallback(t *testing.T) {
	t.Setenv("KRAKEN_SERVE_ADDR", "127.0.0.1:1")
	stdin := `{"session_id":"s-1","context_window":{"used_percentage":null}}`
	out := edgeRunCtx(stdin, "edge-job", "exit 1")
	if strings.Count(out, "\n") > 1 || strings.TrimSpace(out) == "" {
		t.Fatalf("output = %q, want exactly one line", out)
	}
}

// --- userStatusLineCommand -------------------------------------------------

func edgeWriteUserSettings(t *testing.T, body string) string {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

func TestEdgeUserStatusLineCommandMissingFile(t *testing.T) {
	if got := userStatusLineCommand(t.TempDir()); got != "" {
		t.Fatalf("missing settings.json → %q, want \"\"", got)
	}
	if got := userStatusLineCommand(filepath.Join(t.TempDir(), "nope")); got != "" {
		t.Fatalf("missing home → %q, want \"\"", got)
	}
}

func TestEdgeUserStatusLineCommandEmptyArms(t *testing.T) {
	cases := map[string]string{
		"unparseable":          `{"statusLine": {"command": "x"`,
		"empty file":           ``,
		"json array":           `[]`,
		"no statusLine key":    `{"model":"opus"}`,
		"statusLine string":    `{"statusLine":"printf hi"}`,
		"statusLine array":     `{"statusLine":["printf hi"]}`,
		"statusLine null":      `{"statusLine":null}`,
		"command not a string": `{"statusLine":{"type":"command","command":42}}`,
		"no command key":       `{"statusLine":{"type":"command"}}`,
		"wraps ctx-hook":       `{"statusLine":{"type":"command","command":"/Users/x/.local/bin/kraken ctx-hook --slug s"}}`,
		"ctx-hook mid-string":  `{"statusLine":{"type":"command","command":"sh -c 'kraken ctx-hook' | cat"}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			home := edgeWriteUserSettings(t, body)
			if got := userStatusLineCommand(home); got != "" {
				t.Fatalf("%s → %q, want \"\"", name, got)
			}
		})
	}
}

func edgeMustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
