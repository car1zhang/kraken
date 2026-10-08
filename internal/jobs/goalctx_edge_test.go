package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// goalctx_edge_test.go — the EDGE dimension of 50-contract.md's Slice S2:
// goalContext's error paths and its byte budget, and the recall channel's
// suppressors. Written blind from the contract text, with no sight of
// internal/jobs/goalctx.go or of the pending diff to recall.go.
//
// The sibling blind author owns the rendering invariants (nesting under a
// parent, the pull-verb line, done goals excluded, one renderer for two call
// sites). What lives here is the half that keeps a spawn and a conversation
// safe: the "" answers, the cap, and the three states in which the goals block
// must NOT be delivered.
//
// Package-level identifiers are prefixed `edgeGoal`: this package's test
// namespace already carries an `edge`/`edgeRecall*` family and a second blind
// author is writing beside this file in the same round.

// edgeGoalEnv opens a scratch store and wires it as the active one, under a
// scratch HOME so nothing on the path under test reads the real ~/.kraken.
func edgeGoalEnv(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })
	return st
}

// edgeGoalJob creates a store-backed job and returns it through the ordinary
// load path, so whatever a Job carries about its linked goal is populated the
// way a real spawn populates it.
func edgeGoalJob(t *testing.T, st *store.Store, slug, session string) *Job {
	t.Helper()
	if _, err := st.CreateJob(&store.Job{Slug: slug, Repo: "monolith", Session: session}); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	j := JobBySlug(LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not found after create", slug)
	}
	return j
}

func edgeGoalAdd(t *testing.T, st *store.Store, g store.GoalAdd) *store.Goal {
	t.Helper()
	out, err := st.AddGoal(g, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("AddGoal %+v: %v", g, err)
	}
	return out
}

// edgeGoalPeriods is the pair every fixture below writes into, so a case is
// never about the calendar by accident.
func edgeGoalPeriods() (week, day string) { return store.GoalPeriods(time.Now()) }

// edgeGoalMarker is the literal the contract requires inside the marker that
// distinguishes the job's own goal from the rest of the list.
const edgeGoalMarker = "this job's goal"

// ---------------------------------------------------------------------------
// goalContext: the "" answers
// ---------------------------------------------------------------------------

// TestEdgeGoalContextEmptyWithoutStore is the spawn path's hard rule, restated:
// a cell started outside `kraken serve` (the viewer, `k` itself) has no active
// store, and goalContext must answer "" rather than opening its own handle —
// store.Open runs migrations, and a spawn is not the place to migrate a
// database.
func TestEdgeGoalContextEmptyWithoutStore(t *testing.T) {
	UseStore(nil)
	t.Cleanup(func() { UseStore(nil) })
	if got := goalContext(&Job{ID: 1, Slug: "no-store"}); got != "" {
		t.Errorf("goalContext with no active store = %q, want \"\"", got)
	}
}

// TestEdgeGoalContextEmptyWithNoGoals is the byte-for-byte promise: a machine
// where carl has never written a goal must stage exactly the brief it staged
// before this feature existed.
func TestEdgeGoalContextEmptyWithNoGoals(t *testing.T) {
	st := edgeGoalEnv(t)
	j := edgeGoalJob(t, st, "no-goals", "")
	if got := goalContext(j); got != "" {
		t.Errorf("goalContext with an empty goals table = %q, want \"\"", got)
	}
}

// TestEdgeGoalContextEmptyForOtherPeriodsOnly pins the same "" for the state
// that is NOT an empty table: goals exist, but none of them are current and the
// job links none. There is nothing relevant to say, so nothing is said.
func TestEdgeGoalContextEmptyForOtherPeriodsOnly(t *testing.T) {
	st := edgeGoalEnv(t)
	j := edgeGoalJob(t, st, "stale-goals", "")
	edgeGoalAdd(t, st, store.GoalAdd{Scope: "week", Period: "2026-08-03", Text: "last week's theme"})
	edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: "2026-08-04", Text: "last tuesday"})
	if got := goalContext(j); got != "" {
		t.Errorf("goalContext with only past-period goals = %q, want \"\"", got)
	}
}

// TestEdgeGoalContextNilJobIsEmpty covers the argument every "never fails a
// spawn" function is eventually handed; a panic here would take the whole
// cell start down.
func TestEdgeGoalContextNilJobIsEmpty(t *testing.T) {
	st := edgeGoalEnv(t)
	_, curDay := edgeGoalPeriods()
	edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "a current goal"})
	if got := goalContext(nil); got != "" {
		t.Errorf("goalContext(nil) = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// goalContext: the byte budget
// ---------------------------------------------------------------------------

// TestEdgeGoalContextRespectsItsByteCapOnWholeGoalBoundaries pins both halves
// of the budget. The cap itself keeps the staged brief bounded; the BOUNDARY
// rule is the one that matters more, because a goal cut mid-line reads as a
// complete instruction that says something carl never wrote.
func TestEdgeGoalContextRespectsItsByteCapOnWholeGoalBoundaries(t *testing.T) {
	st := edgeGoalEnv(t)
	j := edgeGoalJob(t, st, "over-cap", "")
	curWeek, curDay := edgeGoalPeriods()

	// Twenty goals of ~70 bytes each is ~1400 bytes of text alone, twice the
	// 700-byte cap, so truncation is guaranteed. Each carries a unique opening
	// tag and a unique closing tag: a rendered goal must show BOTH, and a goal
	// showing only its opening tag is exactly the mid-line cut this test exists
	// to catch. The texts stay short enough (~70 bytes) that no plausible
	// per-goal cap could be what trimmed them.
	texts := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		text := fmt.Sprintf("openzz%02d ", i) + strings.Repeat("q", 50) + fmt.Sprintf(" closezz%02d", i)
		texts = append(texts, text)
		scope, period := "day", curDay
		if i%5 == 0 {
			scope, period = "week", curWeek
		}
		edgeGoalAdd(t, st, store.GoalAdd{Scope: scope, Period: period, Text: text})
	}

	block := goalContext(j)
	if block == "" {
		t.Fatal("goalContext produced nothing for twenty current, undone goals")
	}
	if len(block) > goalContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(block), goalContextBytes, block)
	}
	shown := 0
	for i, text := range texts {
		open := fmt.Sprintf("openzz%02d ", i)
		if !strings.Contains(block, open) {
			continue
		}
		shown++
		if !strings.Contains(block, text) {
			t.Errorf("goal %d was cut mid-line — the block carries %q but not the whole text %q:\n%s",
				i, open, text, block)
		}
	}
	if shown == 0 {
		t.Errorf("the cap dropped every goal, leaving a header with nothing under it:\n%s", block)
	}
	if shown == len(texts) {
		t.Errorf("all %d goals fit inside a %d-byte cap — the fixture is not exercising truncation",
			len(texts), goalContextBytes)
	}
}

// TestEdgeGoalContextByteCapIsTheStatedValue pins the constant itself, because
// it is a number two other things are written against: the staged-brief ceiling
// test's sum, and this file's fixture sizing.
func TestEdgeGoalContextByteCapIsTheStatedValue(t *testing.T) {
	if goalContextBytes != 700 {
		t.Errorf("goalContextBytes = %d, want 700 — the staged-brief ceiling is extended by exactly this number",
			goalContextBytes)
	}
}

// ---------------------------------------------------------------------------
// goalContext: the linked goal's two exceptions
// ---------------------------------------------------------------------------

// TestEdgeGoalContextLinkedGoalFromAPastPeriodIsStillMarked covers the first
// exception to "current periods only". A job linked to last week's goal is a
// job still working on last week's goal; dropping it because the calendar
// turned over would silently withdraw the one line that says what this cell is
// FOR.
func TestEdgeGoalContextLinkedGoalFromAPastPeriodIsStillMarked(t *testing.T) {
	st := edgeGoalEnv(t)
	j := edgeGoalJob(t, st, "past-linked", "")
	past := edgeGoalAdd(t, st, store.GoalAdd{
		Scope: "week", Period: "2026-08-03", Text: "carry the migration over from last week"})
	if err := st.AddJobGoal(j.ID, past.ID); err != nil {
		t.Fatalf("AddJobGoal: %v", err)
	}
	// Reload through the ordinary path, exactly as a spawn does.
	j = JobBySlug(LoadJobs(), "past-linked")

	block := goalContext(j)
	if block == "" {
		t.Fatal("goalContext dropped the job's linked goal because its period is not current")
	}
	if !strings.Contains(block, past.Text) {
		t.Errorf("block does not carry the linked goal's text %q:\n%s", past.Text, block)
	}
	if !strings.Contains(block, edgeGoalMarker) {
		t.Errorf("block does not mark the job's own goal (want a marker containing %q):\n%s",
			edgeGoalMarker, block)
	}
}

// TestEdgeGoalContextLinkedDoneGoalIsStillShown covers the second exception.
// Done goals are excluded from the list — except this one: a cell whose goal
// was ticked off mid-run must still be told what it is working on, or the brief
// contradicts the board.
func TestEdgeGoalContextLinkedDoneGoalIsStillShown(t *testing.T) {
	st := edgeGoalEnv(t)
	j := edgeGoalJob(t, st, "done-linked", "")
	_, curDay := edgeGoalPeriods()
	g := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "land the schema migration"})
	done := true
	if err := st.UpdateGoal(g.ID, store.GoalUpdate{Done: &done}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("UpdateGoal done: %v", err)
	}
	if err := st.AddJobGoal(j.ID, g.ID); err != nil {
		t.Fatalf("AddJobGoal: %v", err)
	}
	j = JobBySlug(LoadJobs(), "done-linked")

	block := goalContext(j)
	if block == "" {
		t.Fatal("goalContext dropped the job's linked goal because it is done")
	}
	if !strings.Contains(block, g.Text) {
		t.Errorf("block does not carry the done linked goal's text %q:\n%s", g.Text, block)
	}
	if !strings.Contains(block, edgeGoalMarker) {
		t.Errorf("block does not mark the job's own goal:\n%s", block)
	}
}

// ---------------------------------------------------------------------------
// Recall: the three suppressors
// ---------------------------------------------------------------------------

// edgeGoalRecallReq builds the payload a UserPromptSubmit hook sends for a
// resolvable job. The prompt is deliberately CONSTANT across a test's firings:
// the contract requires the goals decision to be made BEFORE the same-query
// short-circuit, so a changed goal set must be delivered even when the text of
// the prompt never changes — and a test that varied the prompt would prove
// nothing about that ordering.
func edgeGoalRecallReq(slug, session string) RecallRequest {
	return RecallRequest{
		SessionID: session,
		Event:     "UserPromptSubmit",
		Prompt:    "keep going on the thing we were doing",
		Slug:      slug,
	}
}

// TestEdgeGoalRecallNothingOnPostToolUse: the goals block is a refresh for the
// HUMAN's turn. PostToolUse fires per tool call — several times per assistant
// message — and a goals block arriving there would be the noise the whole
// once-per-change design exists to avoid. Everything else about this firing is
// valid, so the event name is the only thing that can suppress it.
func TestEdgeGoalRecallNothingOnPostToolUse(t *testing.T) {
	st := edgeGoalEnv(t)
	edgeGoalJob(t, st, "recall-posttooluse", "sess-posttooluse")
	_, curDay := edgeGoalPeriods()
	g := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "a very current goal"})

	req := edgeGoalRecallReq("recall-posttooluse", "sess-posttooluse")
	req.Event = "PostToolUse"
	if got := Recall(context.Background(), st, req); got != "" {
		t.Errorf("PostToolUse firing carried a goals block (goal %q):\n%s", g.Text, got)
	}
}

// TestEdgeGoalRecallSuppressedWhenTheSetIsUnchanged is the second half of the
// same restraint, and the one the contract spells out in the most detail: the
// block is delivered when the goal set CHANGES, and an unchanged set on a later
// prompt gets nothing — even though nothing else about the firing differs.
func TestEdgeGoalRecallSuppressedWhenTheSetIsUnchanged(t *testing.T) {
	st := edgeGoalEnv(t)
	edgeGoalJob(t, st, "recall-unchanged", "sess-unchanged")
	_, curDay := edgeGoalPeriods()
	g := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "write the goals panel"})

	req := edgeGoalRecallReq("recall-unchanged", "sess-unchanged")
	first := Recall(context.Background(), st, req)
	if !strings.Contains(first, g.Text) {
		t.Fatalf("first UserPromptSubmit carried no goals block:\n%q", first)
	}
	for i := 0; i < 3; i++ {
		if got := Recall(context.Background(), st, req); got != "" {
			t.Fatalf("firing %d re-sent an unchanged goals block:\n%s", i+2, got)
		}
	}
}

// TestEdgeGoalRecallReemitsExactlyOnceAfterAChange is the ordering assertion.
// The prompt text never changes across all four firings here, so a goals
// decision made AFTER recall.go's same-query short-circuit would return "" for
// the third one and the refresh would never arrive — which is precisely the bug
// the "decided BEFORE the short-circuit" clause exists to prevent.
func TestEdgeGoalRecallReemitsExactlyOnceAfterAChange(t *testing.T) {
	st := edgeGoalEnv(t)
	edgeGoalJob(t, st, "recall-changed", "sess-changed")
	_, curDay := edgeGoalPeriods()
	first := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "the original goal"})

	req := edgeGoalRecallReq("recall-changed", "sess-changed")
	if got := Recall(context.Background(), st, req); !strings.Contains(got, first.Text) {
		t.Fatalf("first firing carried no goals block:\n%q", got)
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("second firing re-sent an unchanged block:\n%s", got)
	}

	second := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "a goal carl just added"})
	third := Recall(context.Background(), st, req)
	if !strings.Contains(third, second.Text) {
		t.Fatalf("a CHANGED goal set was not delivered on the next prompt — the goals decision "+
			"is happening after the same-query short-circuit:\n%q", third)
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("the refreshed block was sent a second time:\n%s", got)
	}

	// A change of a different kind — a done toggle rather than an insert — is
	// still a change, and rearms delivery exactly once.
	done := true
	if err := st.UpdateGoal(first.ID, store.GoalUpdate{Done: &done}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("UpdateGoal done: %v", err)
	}
	if got := Recall(context.Background(), st, req); got == "" {
		t.Fatal("ticking a goal done did not rearm the goals block")
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("the post-toggle block was sent a second time:\n%s", got)
	}
}

// TestEdgeGoalRecallNothingWhenNoJobResolves covers the payload that names
// nothing this serve knows: an empty slug (an old settings.json that predates
// the argument) and a session id belonging to no job. There is no job, so
// there is no linked goal and no right answer about whose goals these are —
// the contract's gate is job resolution, and this is that gate's closed side.
func TestEdgeGoalRecallNothingWhenNoJobResolves(t *testing.T) {
	st := edgeGoalEnv(t)
	edgeGoalJob(t, st, "recall-resolvable", "sess-resolvable")
	_, curDay := edgeGoalPeriods()
	edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "a goal nobody unresolved should see"})

	for _, c := range []struct{ name, slug, session string }{
		{"no slug and an unknown session", "", "sess-belongs-to-nothing"},
		{"an unknown slug and an unknown session", "no-such-job", "sess-also-unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := Recall(context.Background(), st, edgeGoalRecallReq(c.slug, c.session))
			if got != "" {
				t.Errorf("an unresolvable firing received a goals block:\n%s", got)
			}
		})
	}
}

// TestEdgeGoalRecallResolvesBySessionWhenTheSlugIsEmpty is the closed gate's
// open side, and the reason the case above is about resolution rather than
// about the slug: a hook from an install that predates the slug argument still
// resolves through jobs.session, so an empty slug alone must not suppress
// anything.
func TestEdgeGoalRecallResolvesBySessionWhenTheSlugIsEmpty(t *testing.T) {
	st := edgeGoalEnv(t)
	edgeGoalJob(t, st, "recall-by-session", "sess-matches-a-job")
	_, curDay := edgeGoalPeriods()
	g := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "resolve me by session id"})

	req := edgeGoalRecallReq("", "sess-matches-a-job")
	if got := Recall(context.Background(), st, req); !strings.Contains(got, g.Text) {
		t.Errorf("a firing with no slug but a matching jobs.session got no goals block:\n%q", got)
	}
}

// ---------------------------------------------------------------------------
// round 3: three additions, each closing a gap this file's own round-1 report
// disclosed as untested. Same blind rules — every assertion below is still
// derived from the contract text plus the graphctx precedent it names.
// ---------------------------------------------------------------------------

// TestEdgeGoalContextRefusesAHeaderAloneForAnOversizeGoal covers the degenerate
// case the ordinary budget test cannot reach: ONE goal whose text alone will not
// fit under goalContextBytes. Truncation "on whole-goal boundaries" then has
// nothing whole to keep, and the only two honest answers are the empty block or
// the goal rendered whole — never a header announcing goals with nothing under
// it, which asserts that carl has goals while showing none of them.
//
// The strict reading asserted here is "": the block is dropped — a header
// with nothing under it is worse than no block at all. The second case below
// exists because a byte budget over a rune-counted renderer is exactly where a
// "can't happen" branch turns out to be reachable.
func TestEdgeGoalContextRefusesAHeaderAloneForAnOversizeGoal(t *testing.T) {
	for _, c := range []struct {
		name string
		text string
	}{
		// Plain ASCII, comfortably past the cap on bytes alone.
		{"ascii past the byte cap", strings.Repeat("w", goalContextBytes+200)},
		// Four-byte runes: only 250 characters, but 1000 bytes. A renderer that
		// caps a goal's text in RUNES while the block's budget is in BYTES lets
		// this one through its per-goal check and then blows the block budget.
		{"four-byte runes past the byte cap", strings.Repeat("\U0001D11E", 250)},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := edgeGoalEnv(t)
			j := edgeGoalJob(t, st, "oversize-goal", "")
			_, curDay := edgeGoalPeriods()
			edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: c.text})

			block := goalContext(j)
			if block == "" {
				return // the whole block was dropped, which is the answer
			}
			if len(block) > goalContextBytes {
				t.Fatalf("block is %d bytes, over the %d-byte cap — one goal too large to fit "+
					"was emitted anyway:\n%q", len(block), goalContextBytes, block)
			}
			t.Fatalf("a goal of %d bytes could not fit under the %d-byte cap, yet a %d-byte block "+
				"was still produced — a header with nothing under it announces goals while showing "+
				"none:\n%q", len(c.text), goalContextBytes, len(block), block)
		})
	}
}

// TestEdgeGoalRecallNothingWithoutASessionID isolates the session id as the only
// variable: the job resolves by slug, the goals are current and undone, and the
// event is UserPromptSubmit — so the payload clears every gate the contract
// names for the goals block except that it carries no session.
//
// Without a session there is no key for the per-session state the suppressor
// lives in, so "has this session already seen this block" has no answer. The
// strict reading is that an unanswerable dedup question is refused rather than
// guessed: a firing that cannot be deduplicated would re-send the same block on
// every prompt forever, which is rule 2 of this whole surface.
func TestEdgeGoalRecallNothingWithoutASessionID(t *testing.T) {
	st := edgeGoalEnv(t)
	edgeGoalJob(t, st, "recall-no-session", "")
	_, curDay := edgeGoalPeriods()
	g := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "a goal with no session to send it to"})

	for _, session := range []string{"", " ", "\t"} {
		req := edgeGoalRecallReq("recall-no-session", session)
		if got := Recall(context.Background(), st, req); got != "" {
			t.Errorf("a firing with session_id %q received a goals block (goal %q) — "+
				"there is no key to suppress the next one with:\n%s", session, g.Text, got)
		}
	}
}

// TestEdgeGoalRecallReemitsAfterATextEdit is the third kind of change, after the
// insert and the done-toggle the earlier cases cover. The contract's test is
// whether "the rendered goals block DIFFERS from the last goals block this
// session received" — a reworded goal renders differently, so it re-arms; and
// like every other change it must deliver exactly once, not on every prompt
// after it.
//
// The prompt text is constant across all four firings here, as it is in the
// insert case, so this also re-proves the ordering against a second kind of
// change: a decision made after the same-query short-circuit would never
// deliver the edit at all.
func TestEdgeGoalRecallReemitsAfterATextEdit(t *testing.T) {
	st := edgeGoalEnv(t)
	edgeGoalJob(t, st, "recall-textedit", "sess-textedit")
	_, curDay := edgeGoalPeriods()
	g := edgeGoalAdd(t, st, store.GoalAdd{Scope: "day", Period: curDay, Text: "the original wording"})

	req := edgeGoalRecallReq("recall-textedit", "sess-textedit")
	if got := Recall(context.Background(), st, req); !strings.Contains(got, "the original wording") {
		t.Fatalf("first firing carried no goals block:\n%q", got)
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("second firing re-sent an unchanged block:\n%s", got)
	}

	edited := "the rewritten wording"
	if err := st.UpdateGoal(g.ID, store.GoalUpdate{Text: &edited}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("UpdateGoal text: %v", err)
	}
	third := Recall(context.Background(), st, req)
	if !strings.Contains(third, edited) {
		t.Fatalf("a goal reworded between prompts was not delivered — a text edit is a change "+
			"to the rendered block:\n%q", third)
	}
	if strings.Contains(third, "the original wording") {
		t.Errorf("the delivered block still carries the pre-edit text:\n%s", third)
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("the post-edit block was sent a second time:\n%s", got)
	}
}
