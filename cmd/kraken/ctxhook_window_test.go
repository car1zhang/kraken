package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ctxhook_window_test.go — the ctx-hook's restatement of the statusline's
// used_percentage as a share of the window Claude Code will actually
// auto-compact at (autoCompactWindow in the user's settings.json), rather
// than of the model's full window. Measured 2026-10-20: every cell on a
// machine whose settings set autoCompactWindow below the model window
// compacted while the hook reported ~46%, so the daemon's 75% handoff
// threshold was unreachable and the daemon never typed /handoff.
//
// Lead-authored SIGHTED regression pins, not contract tests. Package-level
// helpers are prefixed `window`; the test server and the POST assertion come
// from handoff_contract_test.go (logicServe, logicAssertCellPost), so a
// rename there breaks these too.

func windowWriteUserSettings(t *testing.T, home, content string) {
	t.Helper()
	// The environment variable outranks the file, so every file-reading
	// test runs with it unset whatever the machine's own environment says.
	t.Setenv(autoCompactWindowEnv, "")
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
		t.Fatalf("write settings: %v", err)
	}
}

// The per-model window wins over the top-level one for that model; any
// other model takes the top-level one.
func TestCtxHookAutoCompactWindowPerModelBeatsTopLevel(t *testing.T) {
	home := t.TempDir()
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000,"modelSettings":{"claude-opus-5-5":{"autoCompactWindow":400000},"claude-fable-5-1":{"effort":"high"}}}`)
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 400000 {
		t.Errorf("per-model window = %d, want 400000", got)
	}
	if got := autoCompactWindow(home, "claude-fable-5-1"); got != 500000 {
		t.Errorf("model without its own window = %d, want the top-level 500000", got)
	}
	if got := autoCompactWindow(home, ""); got != 500000 {
		t.Errorf("unknown model = %d, want the top-level 500000", got)
	}
}

// No home, no file, an unparseable file, or a file without the key all read
// as 0 — "no such setting" — and never as an error: the hook never fails.
func TestCtxHookAutoCompactWindowAbsentIsZero(t *testing.T) {
	if got := autoCompactWindow("", "claude-opus-5-5"); got != 0 {
		t.Errorf("empty home = %d, want 0", got)
	}
	home := t.TempDir()
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 0 {
		t.Errorf("missing file = %d, want 0", got)
	}
	windowWriteUserSettings(t, home, `{not json`)
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 0 {
		t.Errorf("unparseable file = %d, want 0", got)
	}
	windowWriteUserSettings(t, home, `{"model":"opus","statusLine":{"command":"x"}}`)
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 0 {
		t.Errorf("file without the key = %d, want 0", got)
	}
	windowWriteUserSettings(t, home, `{"autoCompactWindow":-5,"modelSettings":{"claude-opus-5-5":{"autoCompactWindow":0}}}`)
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 0 {
		t.Errorf("non-positive values = %d, want 0", got)
	}
}

// effectiveFill: scales when the auto-compact window is smaller than the
// model window, caps at 100, and otherwise returns its inputs unchanged.
func TestCtxHookEffectiveFill(t *testing.T) {
	cases := []struct {
		name       string
		pct        float64
		size, auto int64
		wantPct    float64
		wantWindow int64
	}{
		{"scaled: 42.5% of 1M is 85% of 500K", 42.5, 1000000, 500000, 85, 500000},
		{"capped at 100 past the compaction point", 60, 1000000, 500000, 100, 500000},
		{"no auto-compact window: unchanged", 42.5, 1000000, 0, 42.5, 1000000},
		{"auto-compact window no smaller than size: unchanged", 42.5, 1000000, 1000000, 42.5, 1000000},
		{"auto-compact window larger than size: unchanged", 42.5, 200000, 500000, 42.5, 200000},
		{"no size: unchanged", 42.5, 0, 500000, 42.5, 0},
		{"zero fill stays zero", 0, 1000000, 500000, 0, 500000},
	}
	for _, c := range cases {
		gotPct, gotWindow := effectiveFill(c.pct, c.size, c.auto)
		if gotPct != c.wantPct || gotWindow != c.wantWindow {
			t.Errorf("%s: effectiveFill(%v, %d, %d) = (%v, %d), want (%v, %d)", c.name, c.pct, c.size, c.auto, gotPct, gotWindow, c.wantPct, c.wantWindow)
		}
	}
}

// End to end: with a settings file naming a 500K window for the session's
// model, a 42.5%-of-1M statusline reports 85% of 500K to serve and prints it.
func TestCtxHookReportsFillOfTheAutoCompactWindow(t *testing.T) {
	rec := logicServe(t)
	home := t.TempDir()
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000}`)
	payload := `{"session_id":"sess-1","model":{"id":"claude-fable-5-1","display_name":"Fable"},"context_window":{"used_percentage":42.5,"context_window_size":1000000}}`
	var out bytes.Buffer
	runCtxHook(strings.NewReader(payload), &out, "my-job", "", home)

	posts := rec.all()
	if len(posts) != 1 {
		t.Fatalf("posts = %+v, want exactly one", posts)
	}
	if got := posts[0].body["used_percentage"]; got != float64(85) {
		t.Errorf("body used_percentage = %v, want 85", got)
	}
	if got := posts[0].body["context_window_size"]; got != float64(500000) {
		t.Errorf("body context_window_size = %v, want 500000", got)
	}
	if got, want := out.String(), "my-job 85.0%\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// The same payload with no settings file at that home reports the harness'
// own numbers unchanged — the pre-change behaviour, kept for a machine that
// never set autoCompactWindow.
func TestCtxHookWithoutTheSettingReportsTheHarnessNumber(t *testing.T) {
	rec := logicServe(t)
	home := t.TempDir()
	payload := `{"session_id":"sess-1","model":{"id":"claude-fable-5-1"},"context_window":{"used_percentage":42.5,"context_window_size":1000000}}`
	var out bytes.Buffer
	runCtxHook(strings.NewReader(payload), &out, "my-job", "", home)

	logicAssertCellPost(t, rec)
	if got, want := out.String(), "my-job 42.5%\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// A value Claude Code accepts but an int64 field would not — the string
// "auto" that /autocompact writes, a float literal — costs only its own
// entry: a sibling model's "auto" leaves the top level readable, the model's
// own "auto" is its tuned window (0, not the top level), and 500000.0 is
// 500000.
func TestCtxHookAutoCompactWindowAutoAndFloatValues(t *testing.T) {
	home := t.TempDir()
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000,"modelSettings":{"claude-opus-5-5":{"autoCompactWindow":"auto"}}}`)
	if got := autoCompactWindow(home, "claude-fable-5-1"); got != 500000 {
		t.Errorf("sibling model's \"auto\": fable = %d, want the top-level 500000", got)
	}
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 0 {
		t.Errorf("own \"auto\": opus = %d, want 0 (Claude Code's tuned window, not the top level)", got)
	}
	windowWriteUserSettings(t, home, `{"autoCompactWindow":"auto","modelSettings":{"claude-opus-5-5":{"autoCompactWindow":400000}}}`)
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 400000 {
		t.Errorf("top-level \"auto\" beside a per-model number: opus = %d, want 400000", got)
	}
	if got := autoCompactWindow(home, "claude-fable-5-1"); got != 0 {
		t.Errorf("top-level \"auto\": fable = %d, want 0", got)
	}
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000.0,"modelSettings":{"claude-opus-5-5":{"autoCompactWindow":4e5}}}`)
	if got := autoCompactWindow(home, "claude-fable-5-1"); got != 500000 {
		t.Errorf("float literal = %d, want 500000", got)
	}
	if got := autoCompactWindow(home, "claude-opus-5-5"); got != 400000 {
		t.Errorf("exponent literal = %d, want 400000", got)
	}
	// A value that is neither a whole number nor "auto" — a quoted number, a
	// fraction, a non-object entry — is skipped, and the lookup goes on.
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000,"modelSettings":{"claude-opus-5-5":{"autoCompactWindow":"400000"},"claude-fable-5-1":"oops","claude-haiku-5":{"autoCompactWindow":400000.5}}}`)
	for _, model := range []string{"claude-opus-5-5", "claude-fable-5-1", "claude-haiku-5"} {
		if got := autoCompactWindow(home, model); got != 500000 {
			t.Errorf("unreadable per-model value for %s = %d, want the top-level 500000", model, got)
		}
	}
}

// CLAUDE_CODE_AUTO_COMPACT_WINDOW outranks the file, as it does for Claude
// Code itself; a value that is not a token count is the tuned window (0),
// and an empty one reads as unset.
func TestCtxHookAutoCompactWindowEnvOutranksTheFile(t *testing.T) {
	home := t.TempDir()
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000}`)
	t.Setenv(autoCompactWindowEnv, "300000")
	if got := autoCompactWindow(home, "claude-fable-5-1"); got != 300000 {
		t.Errorf("env set = %d, want 300000", got)
	}
	t.Setenv(autoCompactWindowEnv, "auto")
	if got := autoCompactWindow(home, "claude-fable-5-1"); got != 0 {
		t.Errorf("env \"auto\" = %d, want 0 rather than the file's 500000", got)
	}
	t.Setenv(autoCompactWindowEnv, " ")
	if got := autoCompactWindow(home, "claude-fable-5-1"); got != 500000 {
		t.Errorf("blank env = %d, want the file's 500000", got)
	}
}

// A model id carrying a `[1m]` or dated suffix finds the canonical key, the
// exact key wins when both are present, and a payload with no model id takes
// the top level.
func TestCtxHookAutoCompactWindowMatchesModelSpellings(t *testing.T) {
	home := t.TempDir()
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000,"modelSettings":{"claude-opus-5-5":{"autoCompactWindow":400000},"claude-opus-5-5[1m]":{"autoCompactWindow":450000}}}`)
	cases := map[string]int64{
		"claude-opus-5-5":              400000,
		"claude-opus-5-5[1m]":          450000,
		"claude-opus-5-5-20260801":     400000,
		"claude-opus-5-5-20260801[1m]": 400000,
		"claude-fable-5-1[1m]":         500000,
		"":                             500000,
	}
	for model, want := range cases {
		if got := autoCompactWindow(home, model); got != want {
			t.Errorf("model %q = %d, want %d", model, got, want)
		}
	}
	windowWriteUserSettings(t, home, `{"modelSettings":{"claude-opus-5-5":{"autoCompactWindow":400000}}}`)
	if got := autoCompactWindow(home, ""); got != 0 {
		t.Errorf("no model id and no top level = %d, want 0", got)
	}
}

// With the operator's own statusline wrapped, the scaled number goes to serve
// and the untouched stdin bytes go to the user's command, so a user script
// that prints the harness' percentage prints the harness' percentage.
func TestCtxHookWrappedUserLineGetsRawStdinWhileServeGetsTheScaledFill(t *testing.T) {
	rec := logicServe(t)
	home := t.TempDir()
	windowWriteUserSettings(t, home, `{"autoCompactWindow":500000}`)
	payload := `{"session_id":"sess-1","model":{"id":"claude-fable-5-1"},"context_window":{"used_percentage":42.5,"context_window_size":1000000}}`
	var out bytes.Buffer
	runCtxHook(strings.NewReader(payload), &out, "my-job", "cat", home)

	posts := rec.all()
	if len(posts) != 1 || posts[0].body["used_percentage"] != float64(85) {
		t.Fatalf("posts = %+v, want one carrying used_percentage 85", posts)
	}
	if got := out.String(); !strings.Contains(got, `"used_percentage":42.5`) {
		t.Errorf("user line got %q, want the raw stdin with used_percentage 42.5", got)
	}
}
