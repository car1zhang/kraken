package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// handoffdialect_edge_test.go — the EDGE dimension of the "third standing
// rule" slice (artifacts/50-contract.md: the shell-dialect brief line added to
// handoffStandingRules). Pins boundaries: every no-doc shape emits exactly the
// new constant, a non-compact source emits nothing, the constant is the exact
// prefix ahead of a one- and two-doc listing, the obligation substrings are
// case-sensitive and their backtick forms literal, the third rule sits after
// the spawn-root rule, and the two doc files count three rules.
//
// Written blind: no byte of cmd/kraken/cmd_handoffhook.go was read.
//
// Every package-level identifier here is prefixed `dialectEdge`.

// --- helpers ---------------------------------------------------------------

var dialectEdgeNow = time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)

const dialectEdgeCompact = `{"session_id":"s-dialect","source":"compact"}`

const dialectEdgeListingHeader = "\n\nHandoff docs in this cell's artifacts dir, newest first:\n"

// The O4 obligation substrings, byte for byte as the contract freezes them.
var dialectEdgeThirdRule = []string{
	"every brief you write carries the shell-dialect line verbatim",
	"zsh",
	"quote any word starting with `=`",
	"quote every glob",
	"`timeout N`",
	"never name a variable `path`",
}

func dialectEdgeRun(t *testing.T, stdin, dir string) string {
	t.Helper()
	var out bytes.Buffer
	runHandoffHook(strings.NewReader(stdin), &out, dir, dialectEdgeNow)
	return out.String()
}

// dialectEdgeContext decodes the hook's single JSON object and returns its
// additionalContext; any other shape fails the test.
func dialectEdgeContext(t *testing.T, out string) string {
	t.Helper()
	if out == "" {
		t.Fatalf("hook wrote nothing on a compaction")
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

func dialectEdgeWriteAged(t *testing.T, dir, name string, age time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("# handoff\n"), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	mt := dialectEdgeNow.Add(-age)
	if err := os.Chtimes(p, mt, mt); err != nil {
		t.Fatalf("chtimes %s: %v", p, err)
	}
	return p
}

// dialectEdgeExactRules asserts the hook output is exactly the new constant.
func dialectEdgeExactRules(t *testing.T, out, what string) {
	t.Helper()
	ctx := dialectEdgeContext(t, out)
	if ctx != handoffStandingRules {
		t.Fatalf("%s: additionalContext = %q\nwant exactly handoffStandingRules = %q", what, ctx, handoffStandingRules)
	}
	// The output carries the NEW text, not merely whatever the constant is.
	if !strings.Contains(ctx, "three hold") || !strings.Contains(ctx, dialectEdgeThirdRule[0]) {
		t.Fatalf("%s: context lacks the three-rule text:\n%s", what, ctx)
	}
}

// dialectEdgeCaseFlip returns s with the case of its first letter flipped, or
// "" when s has no ASCII letter to flip.
func dialectEdgeCaseFlip(s string) string {
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			return s[:i] + string(c-'a'+'A') + s[i+1:]
		case c >= 'A' && c <= 'Z':
			return s[:i] + string(c-'A'+'a') + s[i+1:]
		}
	}
	return ""
}

// --- no-doc shapes: exactly the new constant --------------------------------

func TestEdgeHandoffDialectNoDocShapesEmitExactlyTheConstant(t *testing.T) {
	cases := map[string]func(t *testing.T) string{
		"empty dir":     func(t *testing.T) string { return t.TempDir() },
		"missing dir":   func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
		"empty dir arg": func(t *testing.T) string { return "" },
		"dir is a file": func(t *testing.T) string {
			return dialectEdgeWriteAged(t, t.TempDir(), "80-handoff.md", 0)
		},
		"only unrelated files": func(t *testing.T) string {
			d := t.TempDir()
			for _, n := range []string{"00-overview.md", "50-contract.md", "79-handoff.md", "80-handoff.txt"} {
				dialectEdgeWriteAged(t, d, n, time.Minute)
			}
			return d
		},
	}
	for name, mk := range cases {
		t.Run(name, func(t *testing.T) {
			dialectEdgeExactRules(t, dialectEdgeRun(t, dialectEdgeCompact, mk(t)), name)
		})
	}
}

func TestEdgeHandoffDialectAbsentSourceNoDocEmitsTheConstant(t *testing.T) {
	dialectEdgeExactRules(t, dialectEdgeRun(t, `{"session_id":"s-dialect"}`, t.TempDir()), "absent source")
}

func TestEdgeHandoffDialectNonCompactSourceEmitsNothing(t *testing.T) {
	for _, src := range []string{"startup", "resume", "clear", "Compact"} {
		t.Run(src, func(t *testing.T) {
			for _, dir := range []string{t.TempDir(), dialectEdgeDocDir(t)} {
				payload := `{"session_id":"s-dialect","source":"` + src + `"}`
				if out := dialectEdgeRun(t, payload, dir); out != "" {
					t.Fatalf("source %q wrote %q, want nothing", src, out)
				}
			}
		})
	}
}

func dialectEdgeDocDir(t *testing.T) string {
	t.Helper()
	d := t.TempDir()
	dialectEdgeWriteAged(t, d, "80-handoff.md", 0)
	return d
}

// --- docs present: rules are the exact prefix --------------------------------

func TestEdgeHandoffDialectOneDocExactShape(t *testing.T) {
	dir := t.TempDir()
	doc := dialectEdgeWriteAged(t, dir, "80-handoff.md", 3*time.Minute)
	ctx := dialectEdgeContext(t, dialectEdgeRun(t, dialectEdgeCompact, dir))
	want := handoffStandingRules + dialectEdgeListingHeader +
		"- " + doc + " (written 3m ago)\n" + handoffClosing
	if ctx != want {
		t.Fatalf("one-doc context mismatch\n got: %q\nwant: %q", ctx, want)
	}
	if !strings.HasPrefix(ctx, handoffStandingRules) {
		t.Fatalf("rules are not the byte prefix:\n%s", ctx)
	}
}

func TestEdgeHandoffDialectTwoDocsExactShape(t *testing.T) {
	dir := t.TempDir()
	older := dialectEdgeWriteAged(t, dir, "80-handoff.md", 40*time.Minute)
	newer := dialectEdgeWriteAged(t, dir, "80-handoff-hephaestus.md", time.Minute)
	ctx := dialectEdgeContext(t, dialectEdgeRun(t, dialectEdgeCompact, dir))
	want := handoffStandingRules + dialectEdgeListingHeader +
		"- " + newer + " (written 1m ago)\n" +
		"- " + older + " (written 40m ago)\n" +
		handoffClosing
	if ctx != want {
		t.Fatalf("two-doc context mismatch\n got: %q\nwant: %q", ctx, want)
	}
	// The third rule lives in the prefix, never in the listing tail.
	tail := ctx[len(handoffStandingRules):]
	if strings.Contains(tail, dialectEdgeThirdRule[0]) {
		t.Fatalf("third rule repeated after the prefix:\n%s", tail)
	}
}

// --- newline shape ------------------------------------------------------------

func TestEdgeHandoffDialectConstantIsOneLineButListingIsNot(t *testing.T) {
	if handoffStandingRules == "" {
		t.Fatalf("handoffStandingRules is empty")
	}
	if strings.Contains(handoffStandingRules, "\n") {
		t.Fatalf("handoffStandingRules carries a newline: %q", handoffStandingRules)
	}
	if strings.Contains(handoffStandingRules, "\r") {
		t.Fatalf("handoffStandingRules carries a carriage return: %q", handoffStandingRules)
	}
	ctx := dialectEdgeContext(t, dialectEdgeRun(t, dialectEdgeCompact, dialectEdgeDocDir(t)))
	if !strings.Contains(ctx, "\n") {
		t.Fatalf("context with a doc has no newline: %q", ctx)
	}
	if i := strings.Index(ctx, "\n"); i != len(handoffStandingRules) {
		t.Fatalf("first newline at %d, want %d (right after the one-line rules)", i, len(handoffStandingRules))
	}
}

// --- case sensitivity and literal backticks ------------------------------------

// Every obligation substring is matched with its exact case: the constant must
// carry the exact form, and the test's own matcher must reject a case variant.
func TestEdgeHandoffDialectSubstringsAreCaseSensitive(t *testing.T) {
	subs := append([]string{"three hold", "Edit or Write tool", "worktree root"}, dialectEdgeThirdRule...)
	for _, s := range subs {
		if !strings.Contains(handoffStandingRules, s) {
			t.Errorf("handoffStandingRules lacks exact-case %q", s)
		}
		flip := dialectEdgeCaseFlip(s)
		if flip == "" || flip == s {
			continue
		}
		// A case variant is a different string: matching it would not satisfy
		// the obligation, so prove the matcher distinguishes them.
		if strings.Contains(s, flip) {
			t.Errorf("matcher treats %q and %q as equal", s, flip)
		}
	}
	// "two hold" is absent in its exact form (the contract's O2).
	if strings.Contains(handoffStandingRules, "two hold") {
		t.Errorf("handoffStandingRules still says %q", "two hold")
	}
}

// The backtick forms are literal: the prose must carry the backticks
// themselves, not just the words around them.
func TestEdgeHandoffDialectBacktickFormsAreLiteral(t *testing.T) {
	for _, s := range []string{"`=`", "`timeout N`", "`path`"} {
		if !strings.Contains(handoffStandingRules, s) {
			t.Errorf("handoffStandingRules lacks the literal backtick form %q", s)
		}
	}
	if !strings.Contains(handoffStandingRules, "timeout N") {
		t.Errorf("`timeout N` must be the two words with one space")
	}
	if strings.Contains(handoffStandingRules, "quote any word starting with =") {
		t.Errorf("the `=` form appears without its backticks")
	}
}

// --- ordering -----------------------------------------------------------------

func TestEdgeHandoffDialectThirdRuleFollowsSpawnRoot(t *testing.T) {
	iRoot := strings.Index(handoffStandingRules, "worktree root")
	iBrief := strings.Index(handoffStandingRules, "every brief you write")
	if iRoot < 0 || iBrief < 0 {
		t.Fatalf("missing anchors: worktree root at %d, every brief you write at %d", iRoot, iBrief)
	}
	if iBrief <= iRoot {
		t.Fatalf("third rule (at %d) does not follow the spawn-root rule (at %d):\n%s", iBrief, iRoot, handoffStandingRules)
	}
	// Observed through the hook, too: the order survives into the output.
	ctx := dialectEdgeContext(t, dialectEdgeRun(t, dialectEdgeCompact, t.TempDir()))
	if strings.Index(ctx, "every brief you write") <= strings.Index(ctx, "worktree root") {
		t.Fatalf("hook output reorders the rules:\n%s", ctx)
	}
}

// --- doc files ------------------------------------------------------------------

func TestEdgeHandoffDialectDocFilesCountThreeRules(t *testing.T) {
	for _, p := range []string{"../../CLAUDE.md", "../../.claude/skills/handoff/SKILL.md"} {
		t.Run(p, func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			s := string(b)
			if strings.Contains(s, "two standing rules") {
				t.Errorf("%s still says %q", p, "two standing rules")
			}
			if !strings.Contains(s, "three standing rules") {
				t.Errorf("%s does not say %q", p, "three standing rules")
			}
		})
	}
}
