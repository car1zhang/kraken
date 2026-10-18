package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// handoffdialect_contract_test.go — athena-logic's BLIND contract tests for
// the handoff hook's third standing rule (50-contract.md, obligations O1–O7):
// the shell-dialect brief line joins the edit-route and spawn-root rules in
// handoffStandingRules, the hook emits it, and the two doc lines that describe
// the hook count three rules.
//
// Dimension: LOGIC — the stated obligations, one answer per assertion.
// Every package-level identifier is prefixed `dialectLogic`.

// dialectLogicHookOut is the frozen SessionStart output shape.
type dialectLogicHookOut struct {
	HookSpecificOutput struct {
		HookEventName     string `json:"hookEventName"`
		AdditionalContext string `json:"additionalContext"`
	} `json:"hookSpecificOutput"`
}

// dialectLogicRun runs the hook on a compaction payload against dir and
// returns the decoded single JSON object it printed.
func dialectLogicRun(t *testing.T, payload, dir string) dialectLogicHookOut {
	t.Helper()
	var buf bytes.Buffer
	runHandoffHook(strings.NewReader(payload), &buf, dir, time.Now())
	raw := buf.String()
	dec := json.NewDecoder(strings.NewReader(raw))
	var out dialectLogicHookOut
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("hook stdout %q is not a JSON object: %v", raw, err)
	}
	if _, err := dec.Token(); err != io.EOF {
		t.Fatalf("hook stdout %q carries more than one JSON value", raw)
	}
	return out
}

// dialectLogicThirdRule lists O4's exact substrings, each required at least once.
var dialectLogicThirdRule = []string{
	"every brief you write carries the shell-dialect line verbatim",
	"zsh",
	"quote any word starting with `=`",
	"quote every glob",
	"`timeout N`",
	"never name a variable `path`",
}

// dialectLogicAssertRules applies O2–O5 to text (the constant itself, or the
// hook's additionalContext), naming where the text came from on failure.
func dialectLogicAssertRules(t *testing.T, text, where string) {
	t.Helper()
	if !strings.Contains(text, "three hold") {
		t.Errorf("%s: missing %q (O2)", where, "three hold")
	}
	if strings.Contains(text, "two hold") {
		t.Errorf("%s: still contains %q (O2)", where, "two hold")
	}
	for _, s := range []string{"Edit or Write tool", "worktree root"} {
		if !strings.Contains(text, s) {
			t.Errorf("%s: existing rule %q dropped (O3)", where, s)
		}
	}
	for _, s := range dialectLogicThirdRule {
		if !strings.Contains(text, s) {
			t.Errorf("%s: third rule missing %q (O4)", where, s)
		}
	}
	root := strings.Index(text, "worktree root")
	brief := strings.Index(text, "every brief you write")
	if root < 0 || brief < 0 || brief <= root {
		t.Errorf("%s: shell-dialect rule (index %d) must follow the spawn-root rule (index %d) (O5)", where, brief, root)
	}
}

// O1: one line, non-empty.
func TestLogicHandoffDialectConstantIsOneNonEmptyLine(t *testing.T) {
	if handoffStandingRules == "" {
		t.Fatal("handoffStandingRules is empty (O1)")
	}
	if strings.Contains(handoffStandingRules, "\n") {
		t.Fatalf("handoffStandingRules contains a newline (O1): %q", handoffStandingRules)
	}
}

// O2: the count sentence says three.
func TestLogicHandoffDialectCountsThreeRules(t *testing.T) {
	if !strings.Contains(handoffStandingRules, "three hold") {
		t.Errorf("handoffStandingRules lacks %q: %q", "three hold", handoffStandingRules)
	}
	if strings.Contains(handoffStandingRules, "two hold") {
		t.Errorf("handoffStandingRules still says %q: %q", "two hold", handoffStandingRules)
	}
}

// O3: the edit-route and spawn-root rules survive.
func TestLogicHandoffDialectKeepsTheTwoExistingRules(t *testing.T) {
	for _, s := range []string{"Edit or Write tool", "worktree root"} {
		if !strings.Contains(handoffStandingRules, s) {
			t.Errorf("handoffStandingRules lost %q: %q", s, handoffStandingRules)
		}
	}
}

// O4: every substring of the shell-dialect rule is present.
func TestLogicHandoffDialectCarriesTheShellDialectRule(t *testing.T) {
	for _, s := range dialectLogicThirdRule {
		if !strings.Contains(handoffStandingRules, s) {
			t.Errorf("handoffStandingRules lacks %q: %q", s, handoffStandingRules)
		}
	}
}

// O5: the third rule comes after the spawn-root rule.
func TestLogicHandoffDialectRuleFollowsSpawnRoot(t *testing.T) {
	root := strings.Index(handoffStandingRules, "worktree root")
	brief := strings.Index(handoffStandingRules, "every brief you write")
	if root < 0 {
		t.Fatalf("handoffStandingRules lacks %q", "worktree root")
	}
	if brief < 0 {
		t.Fatalf("handoffStandingRules lacks %q", "every brief you write")
	}
	if brief <= root {
		t.Errorf("index of %q (%d) must exceed index of %q (%d)", "every brief you write", brief, "worktree root", root)
	}
}

// O1–O5 together, as one consolidated check on the constant.
func TestLogicHandoffDialectConstantSatisfiesAllTextObligations(t *testing.T) {
	dialectLogicAssertRules(t, handoffStandingRules, "handoffStandingRules")
}

// O6: a compaction with no docs emits exactly the constant, so the new text is
// observable through the hook — both with source "compact" and source absent.
func TestLogicHandoffDialectHookEmitsTheNewRules(t *testing.T) {
	for _, payload := range []string{
		`{"session_id":"s-dialect","source":"compact"}`,
		`{"session_id":"s-dialect"}`,
	} {
		out := dialectLogicRun(t, payload, t.TempDir())
		if got := out.HookSpecificOutput.HookEventName; got != "SessionStart" {
			t.Errorf("payload %s: hookEventName = %q, want SessionStart", payload, got)
		}
		ctx := out.HookSpecificOutput.AdditionalContext
		if ctx != handoffStandingRules {
			t.Errorf("payload %s: additionalContext = %q, want exactly handoffStandingRules %q", payload, ctx, handoffStandingRules)
		}
		dialectLogicAssertRules(t, ctx, "additionalContext ("+payload+")")
	}
}

// O6 (with docs): the rules still open the context, so the third rule rides
// every compaction whether or not a handoff doc exists.
func TestLogicHandoffDialectRulesOpenContextWithDocs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/80-handoff.md", []byte("# handoff\n"), 0o644); err != nil {
		t.Fatalf("write doc: %v", err)
	}
	out := dialectLogicRun(t, `{"session_id":"s-dialect","source":"compact"}`, dir)
	ctx := out.HookSpecificOutput.AdditionalContext
	want := handoffStandingRules + "\n\nHandoff docs in this cell's artifacts dir, newest first:\n"
	if !strings.HasPrefix(ctx, want) {
		t.Fatalf("additionalContext %q does not open with the rules and the listing header", ctx)
	}
	if !strings.HasSuffix(ctx, handoffClosing) {
		t.Errorf("additionalContext %q does not end with handoffClosing", ctx)
	}
	dialectLogicAssertRules(t, ctx[:len(handoffStandingRules)], "additionalContext prefix")
}

// dialectLogicReadRepo reads a repo file relative to cmd/kraken.
func dialectLogicReadRepo(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// O7: CLAUDE.md's `.claude/settings.json` table row says three standing rules.
// Scoped to that row (the line the contract names) rather than the whole file.
func TestLogicHandoffDialectClaudeMDRowSaysThree(t *testing.T) {
	body := dialectLogicReadRepo(t, "../../CLAUDE.md")
	var row string
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, "|") && strings.Contains(line, "`<cell worktree>/.claude/settings.json`") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatal("CLAUDE.md has no `<cell worktree>/.claude/settings.json` table row")
	}
	if strings.Contains(row, "two standing rules") {
		t.Error("CLAUDE.md settings.json row still says \"two standing rules\"")
	}
	if !strings.Contains(row, "three standing rules") {
		t.Error("CLAUDE.md settings.json row lacks \"three standing rules\"")
	}
}

// O7: the handoff skill says three standing rules, never two.
func TestLogicHandoffDialectSkillSaysThree(t *testing.T) {
	body := dialectLogicReadRepo(t, "../../.claude/skills/handoff/SKILL.md")
	if strings.Contains(body, "two standing rules") {
		t.Error("handoff SKILL.md still says \"two standing rules\"")
	}
	if !strings.Contains(body, "three standing rules") {
		t.Error("handoff SKILL.md lacks \"three standing rules\"")
	}
}
