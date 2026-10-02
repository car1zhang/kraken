package core

import (
	"errors"
	"strings"
	"testing"
)

// pinned as an invariant, not a value: the point of -x/-y is to clear tmux's
// 80x24 default-size, which starves pane splits in a never-attached session.
func TestDetachedSessionSizeClearsTmuxDefault(t *testing.T) {
	if DetachedSessionCols <= 80 || DetachedSessionRows <= 24 {
		t.Fatalf("detached session %dx%d does not exceed tmux's 80x24 default",
			DetachedSessionCols, DetachedSessionRows)
	}
}

// Both models kraken spawns on are pinned ids, not tier aliases: an alias would
// silently follow whatever claude ships next, which is exactly the drift the
// constants exist to prevent. They are also pinned to be DIFFERENT — the split
// is the policy (zeus on the fast tier, everything else on the deep one), and a
// ZeusModel edited back to DefaultModel's value would erase it while every
// other test here still passed.
func TestSpawnModelsArePinnedIDs(t *testing.T) {
	if DefaultModel != "claude-opus-5" {
		t.Fatalf("DefaultModel = %q; changing kraken's default model is a deliberate edit — update this test with it", DefaultModel)
	}
	if ZeusModel != "claude-fable-5-1" {
		t.Fatalf("ZeusModel = %q; changing the model a cell's zeus runs on is a deliberate edit — update this test with it", ZeusModel)
	}
	if ZeusModel == DefaultModel {
		t.Fatal("ZeusModel == DefaultModel: zeus runs on its own tier, and collapsing the two silently retires the split")
	}
}

func TestModelArgs(t *testing.T) {
	got := ModelArgs("")
	if len(got) != 2 || got[0] != "--model" || got[1] != DefaultModel {
		t.Fatalf("no model must fall back to DefaultModel: %v", got)
	}
	// an explicit model (a job's model: field) wins over the default
	if got := ModelArgs("sonnet"); len(got) != 2 || got[1] != "sonnet" {
		t.Fatalf("explicit model dropped: %v", got)
	}
}

// Every spawn path routes through ModelArgs — a claude kraken starts never
// falls back to whatever model claude's own config picks.
func TestBuildSpawnArgvPinsModel(t *testing.T) {
	argv, err := BuildSpawnArgv("claude", "foo", "", "")
	if err != nil {
		t.Fatalf("BuildSpawnArgv: %v", err)
	}
	found := false
	for i, a := range argv {
		if a == "--model" && i+1 < len(argv) && argv[i+1] == DefaultModel {
			found = true
		}
	}
	if !found {
		t.Fatalf("spawned claude missing --model %s: %v", DefaultModel, argv)
	}
}

// the hint keys on the two TCC denial texts alone: any other osascript
// failure (syntax error, timeout, missing Ghostty) must pass through
// un-decorated.
func TestFocusErrHintOnlyOnTCCDenials(t *testing.T) {
	denials := []string{
		`kraken-focus-1.applescript:127:325: execution error: System Events got an error: osascript is not allowed assistive access. (-25211)`,
		`execution error: Not authorized to send Apple events to System Events. (-1743)`,
	}
	for _, out := range denials {
		if hint := focusErrHint(out); hint == "" {
			t.Fatalf("TCC denial %q got no hint", out)
		}
	}
	for _, out := range []string{"", "execution error: syntax error", "AppleEvent timed out. (-1712)"} {
		if hint := focusErrHint(out); hint != "" {
			t.Fatalf("non-denial output %q got hint %q", out, hint)
		}
	}
}

// the fallback's argv is pinned whole, as the shape measured working on
// 2026-08-06: -d must be present (it is what makes a burst of opens converge
// on one window), and the target carries the "=" exact-match prefix so a
// stale snapshot's session name can never prefix-match a live sibling's
// window out from under the user.
func TestGhosttyAttachArgs(t *testing.T) {
	got := strings.Join(ghosttyAttachArgs("cell-demo-2"), " ")
	want := "-na Ghostty.app --args -e tmux attach-session -d -t =cell-demo-2"
	if got != want {
		t.Fatalf("ghosttyAttachArgs = %q, want %q", got, want)
	}
}

// the never-kick-a-healthy-client rule hangs on RaiseSession's errors.Is
// check matching what focusGhosttyWindow's notfound branch returns — a %w
// quietly becoming %v would resume kicking healthy clients with every other
// test still green, so the linkage is pinned here.
func TestNoGhosttyWindowErrIsMatchable(t *testing.T) {
	err := noGhosttyWindowErr("cell-demo-2", "notfound")
	if !errors.Is(err, errNoGhosttyWindow) {
		t.Fatalf("noGhosttyWindowErr does not match errNoGhosttyWindow: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"cell-demo-2:"`) || !strings.Contains(msg, "set-titles") {
		t.Fatalf("message lost the session or the set-titles remedy: %q", msg)
	}
}

// The confirmation's text as a kronos pane showed it, captured live.
const modelSwitchDialog = `❯ orch: 3 new item(s) — run: kraken orch inbox
  ⎿  You've reached your Fable limit. Run /usage-credits to continue or switch models with /model.

✻ Cogitated for 2s · done 11:23 AM

▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔
   Switch model?
   Your next response will be slower and use more tokens

   This conversation is cached for the current model. Switching to Opus 5 means the full history gets re-read on your next message.

   ❯ 1. Yes, switch to Opus 5
     2. No, go back
`

func TestModelSwitchConfirmShown(t *testing.T) {
	if !modelSwitchConfirmShown(modelSwitchDialog) {
		t.Fatal("the live dialog with Yes highlighted was not recognised")
	}
	noHighlight := strings.Replace(strings.Replace(modelSwitchDialog, "❯ 1. Yes", "  1. Yes", 1), "  2. No", "❯ 2. No", 1)
	if modelSwitchConfirmShown(noHighlight) {
		t.Error("No highlighted: an Enter would decline, so it must not count")
	}
	permission := strings.Replace(modelSwitchDialog, "Switch model?", "Do you want to proceed?", 1)
	if modelSwitchConfirmShown(permission) {
		t.Error("a different question with a 'Yes, switch to' line counted")
	}
	scrolled := modelSwitchDialog + strings.Repeat("later output\n", modelSwitchConfirmLines)
	if modelSwitchConfirmShown(scrolled) {
		t.Error("a dialog scrolled above the last lines counted as open")
	}
	if modelSwitchConfirmShown("") {
		t.Error("empty pane counted")
	}
}
