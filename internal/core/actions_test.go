package core

import "testing"

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
	if ZeusModel != "claude-fable-5" {
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
