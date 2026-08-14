package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// goalrecall_pin_test.go — the regression pin for a defect this slice shipped
// and then fixed, kept because the fix is invisible from every other angle.
//
// The suppressor that keeps mid-run goals quiet is a per-session fingerprint of
// the last goals block the session was shown. The first cut recorded that
// fingerprint only when a block was actually delivered — which is correct for
// every state except the empty one, and the empty one is reachable: carl clears
// the board, or ticks the last goal done. Nothing was recorded then, so the
// session's remembered state stayed the OLD block's; putting those goals back
// rendered something matching it, and the suppressor swallowed the delivery.
// Not for a turn — forever, until the next EDIT changed the text again.
//
// The pin drives exactly that sequence. It discriminates: against the pre-fix
// logic (record only when the rendered block is non-empty) the third Recall
// returns "" and this test fails; against the fix (record the fingerprint of
// the current rendered state, empty included) it returns the block.
func TestGoalsRecallReArmsAfterDeleteThenRestore(t *testing.T) {
	st := storeForGraph(t)
	// no graph facts: this test is about the goals half, and an empty graph is
	// what isolates it — Recall's graph block stays "" throughout.
	withFacts(t, nil)

	const slug = "goal-pin"
	const text = "ship goal tracking"
	if _, err := st.CreateJob(&store.Job{Slug: slug, State: "working"}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	week, _ := store.GoalPeriods(time.Now())
	add := func() {
		t.Helper()
		if _, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: text}, time.Now().UnixMilli()); err != nil {
			t.Fatalf("add goal: %v", err)
		}
	}
	add()

	// One session, three prompts. The prompts differ so the graph half's
	// same-query short-circuit is not what is being measured.
	req := RecallRequest{SessionID: "sess-goal-pin", Event: "UserPromptSubmit", Slug: slug}
	fire := func(prompt string) string {
		req.Prompt = prompt
		return Recall(context.Background(), st, req)
	}

	if got := fire("first prompt"); !strings.Contains(got, text) {
		t.Fatalf("first firing did not carry the goal:\n%q", got)
	}
	// Unchanged goals stay quiet — the suppressor's ordinary job, asserted here
	// so a fix that simply stopped suppressing would not pass this test.
	if got := fire("second prompt"); got != "" {
		t.Fatalf("an unchanged goal set was re-delivered:\n%q", got)
	}

	goals, err := st.ListGoals("week", week)
	if err != nil || len(goals) != 1 {
		t.Fatalf("list goals = %v, %v; want exactly one", goals, err)
	}
	if err := st.DeleteGoal(goals[0].ID); err != nil {
		t.Fatalf("delete goal: %v", err)
	}
	// Nothing to inject once the board is empty — but the session's remembered
	// state has to move all the same, which is what the next firing proves.
	if got := fire("third prompt"); got != "" {
		t.Fatalf("an empty goal set produced a block:\n%q", got)
	}

	add()
	if got := fire("fourth prompt"); !strings.Contains(got, text) {
		t.Fatalf("a restored goal set was suppressed — the session's last recorded "+
			"state never moved when the goals were cleared:\n%q", got)
	}
}

// TestGoalsRecallDoesNotRecordStateOnANonGoalsFiring is the other half of the
// same fingerprint rule, and it fails against the obvious over-correction.
//
// "Record the current state on every firing" would record the EMPTY state on
// every PostToolUse call, since those never carry goals — and then the next
// UserPromptSubmit would see a difference and re-deliver an unchanged block,
// on every prompt that happened to follow a tool call. A firing that was never
// going to carry goals must leave the recorded state alone.
func TestGoalsRecallDoesNotRecordStateOnANonGoalsFiring(t *testing.T) {
	st := storeForGraph(t)
	withFacts(t, nil)

	const slug = "goal-pin-tool"
	if _, err := st.CreateJob(&store.Job{Slug: slug, State: "working"}); err != nil {
		t.Fatalf("create job: %v", err)
	}
	week, _ := store.GoalPeriods(time.Now())
	if _, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "keep the board honest"}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("add goal: %v", err)
	}

	req := RecallRequest{SessionID: "sess-goal-pin-tool", Event: "UserPromptSubmit", Slug: slug, Prompt: "first prompt"}
	if got := Recall(context.Background(), st, req); !strings.Contains(got, "keep the board honest") {
		t.Fatalf("first firing did not carry the goal:\n%q", got)
	}
	// a tool call in between, which carries no goals and must record nothing
	tool := req
	tool.Event = "PostToolUse"
	tool.Prompt = "a tool ran"
	if got := Recall(context.Background(), st, tool); got != "" {
		t.Fatalf("a PostToolUse firing carried goals:\n%q", got)
	}
	req.Prompt = "second prompt"
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("an unchanged goal set was re-delivered after a tool call:\n%q", got)
	}
}
