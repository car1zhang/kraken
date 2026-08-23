package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// goalctx_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S2: goalContext (the spawn-time goals block) and the
// goals widening of Recall (the mid-conversation refresh).
//
// Written from the contract text alone. internal/jobs/goalctx.go and the
// pending diffs to recall.go / cell.go / crew.go were never read; the harness
// (storeForGraph, withFacts, edge) is pre-existing package state at the base
// commit, as is recall_contract_test.go's `contractRecall*` namespace — which
// this file deliberately does not touch.
//
// Dimension: LOGIC — the stated content obligations and the conditions under
// which a block is emitted. Hostile inputs, panics and the refusal arms beyond
// the two "" cases that ARE the spawn-path guarantee belong to athena-edge.
//
// Every package-level identifier declared here is prefixed `contractGoalCtx`.

// contractGoalCtxSpec is the job spec every fixture below carries. It shares
// no vocabulary with any goal text here, so a goals assertion can never be
// satisfied by the graph block leaking in.
const contractGoalCtxSpec = "rewrite the tmux pane discovery loop"

// contractGoalCtxEnv isolates the embedder credential and the home dir, so
// nothing on the path under test reaches the network or a real ~/.kraken.
func contractGoalCtxEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
}

// contractGoalCtxStore opens a scratch store, wires it as the active one, and
// stubs the graph seam empty — so every block this file observes is the GOALS
// block and nothing else.
func contractGoalCtxStore(t *testing.T) *store.Store {
	t.Helper()
	contractGoalCtxEnv(t)
	st := storeForGraph(t)
	withFacts(t, nil)
	return st
}

// contractGoalCtxPeriods is the current (week, day) pair, which is what
// goalContext reads for.
func contractGoalCtxPeriods(t *testing.T) (string, string) {
	t.Helper()
	return store.GoalPeriods(time.Now())
}

// contractGoalCtxAdd seeds one goal.
func contractGoalCtxAdd(t *testing.T, st *store.Store, scope, period, text string, parent int64) *store.Goal {
	t.Helper()
	g, err := st.AddGoal(store.GoalAdd{Scope: scope, Period: period, Text: text, ParentID: parent}, 1_700_000_000_000)
	if err != nil {
		t.Fatalf("AddGoal(%s/%s %q): %v", scope, period, text, err)
	}
	return g
}

// contractGoalCtxJob creates a job row and returns the loaded jobs.Job — via
// the package's own load path rather than a hand-built struct, so whatever
// field carries the goal link is populated the way a real spawn sees it.
func contractGoalCtxJob(t *testing.T, st *store.Store, slug, session string) *Job {
	t.Helper()
	row := store.Job{Slug: slug, Repo: "kraken", State: "working", Session: session, Body: contractGoalCtxSpec}
	if _, err := st.CreateJob(&row); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	j := JobBySlug(LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not found after create", slug)
	}
	return j
}

// contractGoalCtxLineWith returns the single block line containing needle.
func contractGoalCtxLineWith(t *testing.T, block, needle string) string {
	t.Helper()
	var hits []string
	for _, line := range strings.Split(block, "\n") {
		if strings.Contains(line, needle) {
			hits = append(hits, line)
		}
	}
	if len(hits) != 1 {
		t.Fatalf("want exactly one line containing %q, got %d:\n%s", needle, len(hits), block)
	}
	return hits[0]
}

// ---------------------------------------------------------------------------
// goalContext — the "" answers
// ---------------------------------------------------------------------------

// TestContractGoalCtxEmptyWithoutAStore is rule 3 of the three graphContext
// rules the contract restates for this function: it never opens its own
// handle, so with no ActiveStore the answer is "" — a cell started outside
// `kraken serve` must get a byte-identical brief, not a second migrating
// writer against kraken.db.
func TestContractGoalCtxEmptyWithoutAStore(t *testing.T) {
	contractGoalCtxEnv(t)
	UseStore(nil)
	if got := goalContext(&Job{ID: 1, Slug: "no-store", Body: contractGoalCtxSpec}); got != "" {
		t.Errorf("goalContext with no active store = %q, want \"\"", got)
	}
}

// TestContractGoalCtxEmptyWithNoGoals is the common answer and the important
// one: before carl has typed a goal, every spawn must produce the brief it
// would have produced before this feature existed.
func TestContractGoalCtxEmptyWithNoGoals(t *testing.T) {
	st := contractGoalCtxStore(t)
	j := contractGoalCtxJob(t, st, "no-goals", "")
	if got := goalContext(j); got != "" {
		t.Errorf("goalContext with an empty goals table = %q, want \"\"", got)
	}
}

// TestContractGoalCtxEmptyWhenEveryCurrentGoalIsDone: done goals are excluded,
// and a block header over an empty list would claim carl has goals and then
// name none.
func TestContractGoalCtxEmptyWhenEveryCurrentGoalIsDone(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)
	done := true
	for _, seed := range []struct{ scope, period, text string }{
		{"week", week, "finished weekly thing"},
		{"day", day, "finished daily thing"},
	} {
		g := contractGoalCtxAdd(t, st, seed.scope, seed.period, seed.text, 0)
		if err := st.UpdateGoal(g.ID, store.GoalUpdate{Done: &done}, 1_700_000_001_000); err != nil {
			t.Fatalf("mark done: %v", err)
		}
	}
	j := contractGoalCtxJob(t, st, "all-done", "")
	if got := goalContext(j); got != "" {
		t.Errorf("goalContext with only DONE goals = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// goalContext — the block's content
// ---------------------------------------------------------------------------

// TestContractGoalCtxCarriesEveryUndoneCurrentGoal is the block's central
// claim: both lanes, every undone row, for the CURRENT periods only. A stale
// period leaking in tells a fresh cell to work toward something carl closed
// out last week.
func TestContractGoalCtxCarriesEveryUndoneCurrentGoal(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)

	contractGoalCtxAdd(t, st, "week", week, "weekly alpha", 0)
	contractGoalCtxAdd(t, st, "week", week, "weekly bravo", 0)
	contractGoalCtxAdd(t, st, "day", day, "daily charlie", 0)
	contractGoalCtxAdd(t, st, "week", "2026-01-05", "stale weekly delta", 0)
	contractGoalCtxAdd(t, st, "day", "2026-01-07", "stale daily echo", 0)

	j := contractGoalCtxJob(t, st, "has-goals", "")
	block := goalContext(j)
	if block == "" {
		t.Fatal("goalContext returned \"\" with three undone current goals")
	}
	for _, want := range []string{"weekly alpha", "weekly bravo", "daily charlie"} {
		if !strings.Contains(block, want) {
			t.Errorf("block omits the undone current goal %q:\n%s", want, block)
		}
	}
	for _, unwanted := range []string{"stale weekly delta", "stale daily echo"} {
		if strings.Contains(block, unwanted) {
			t.Errorf("block carries the non-current goal %q:\n%s", unwanted, block)
		}
	}
}

// TestContractGoalCtxExcludesDoneGoals — a done goal is history; the block is
// what the cell should be working toward.
func TestContractGoalCtxExcludesDoneGoals(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)

	contractGoalCtxAdd(t, st, "week", week, "still open weekly", 0)
	finished := contractGoalCtxAdd(t, st, "day", day, "already finished daily", 0)
	done := true
	if err := st.UpdateGoal(finished.ID, store.GoalUpdate{Done: &done}, 1_700_000_001_000); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	block := goalContext(contractGoalCtxJob(t, st, "mixed-done", ""))
	if !strings.Contains(block, "still open weekly") {
		t.Fatalf("block omits the undone goal:\n%s", block)
	}
	if strings.Contains(block, "already finished daily") {
		t.Errorf("block carries a DONE goal:\n%s", block)
	}
}

// TestContractGoalCtxNestsADayGoalUnderItsParent pins the nesting the whole
// parent_id column exists for: a day goal with a parent renders UNDER that
// parent's line, visibly indented, so the cell can see which weekly commitment
// today's work serves. An orphaned day goal stays at the top level.
func TestContractGoalCtxNestsADayGoalUnderItsParent(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)

	parent := contractGoalCtxAdd(t, st, "week", week, "weekly parent goal", 0)
	contractGoalCtxAdd(t, st, "day", day, "nested child goal", parent.ID)
	contractGoalCtxAdd(t, st, "day", day, "loose today goal", 0)

	block := goalContext(contractGoalCtxJob(t, st, "nested", ""))
	parentLine := contractGoalCtxLineWith(t, block, "weekly parent goal")
	childLine := contractGoalCtxLineWith(t, block, "nested child goal")
	looseLine := contractGoalCtxLineWith(t, block, "loose today goal")

	if strings.Index(block, "nested child goal") < strings.Index(block, "weekly parent goal") {
		t.Errorf("the child renders BEFORE its parent:\n%s", block)
	}
	if !strings.HasPrefix(childLine, " ") {
		t.Errorf("the nested day goal's line %q is not indented under its parent — nesting must be visible:\n%s", childLine, block)
	}
	if strings.HasPrefix(parentLine, " ") {
		t.Errorf("the weekly parent's line %q is itself indented; it is a top-level row:\n%s", parentLine, block)
	}
	if strings.HasPrefix(looseLine, " ") {
		t.Errorf("an UNPARENTED day goal's line %q is indented like a nested one:\n%s", looseLine, block)
	}
}

// TestContractGoalCtxMarksTheJobsLinkedGoal is what makes the block about THIS
// job rather than about carl's week in general: the linked goal carries a
// literal marker containing "this job's goal", and no other line does.
func TestContractGoalCtxMarksTheJobsLinkedGoal(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)

	linked := contractGoalCtxAdd(t, st, "week", week, "the goal this job serves", 0)
	contractGoalCtxAdd(t, st, "week", week, "an unrelated weekly", 0)
	contractGoalCtxAdd(t, st, "day", day, "an unrelated daily", 0)

	j := contractGoalCtxJob(t, st, "marked", "")
	if err := st.AddJobGoal(j.ID, linked.ID); err != nil {
		t.Fatalf("AddJobGoal: %v", err)
	}
	j = JobBySlug(LoadJobs(), "marked")

	block := goalContext(j)
	if block == "" {
		t.Fatal("goalContext returned \"\"")
	}
	if n := strings.Count(block, "this job's goal"); n != 1 {
		t.Fatalf("the marker \"this job's goal\" appears %d times, want exactly 1:\n%s", n, block)
	}
	marked := contractGoalCtxLineWith(t, block, "this job's goal")
	if !strings.Contains(marked, "the goal this job serves") {
		t.Errorf("the marker is not on the LINKED goal's line (%q):\n%s", marked, block)
	}
}

// TestContractGoalCtxIncludesTheLinkedGoalEvenWhenDoneOrStale is the marker
// rule's harder half: the linked goal is included "even when done or from a
// non-current period". Both exclusions that govern every other row are waived
// for this one, because a job pointing at a goal and the brief not naming it
// is worse than a slightly stale line.
func TestContractGoalCtxIncludesTheLinkedGoalEvenWhenDoneOrStale(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)

	contractGoalCtxAdd(t, st, "week", week, "an ordinary current weekly", 0)
	stale := contractGoalCtxAdd(t, st, "week", "2026-01-05", "a done goal from a past week", 0)
	done := true
	if err := st.UpdateGoal(stale.ID, store.GoalUpdate{Done: &done}, 1_700_000_001_000); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	j := contractGoalCtxJob(t, st, "stale-link", "")
	if err := st.AddJobGoal(j.ID, stale.ID); err != nil {
		t.Fatalf("AddJobGoal: %v", err)
	}
	j = JobBySlug(LoadJobs(), "stale-link")

	block := goalContext(j)
	if !strings.Contains(block, "a done goal from a past week") {
		t.Errorf("block omits the job's LINKED goal because it is done and stale:\n%s", block)
	}
	marked := contractGoalCtxLineWith(t, block, "this job's goal")
	if !strings.Contains(marked, "a done goal from a past week") {
		t.Errorf("the marker is not on the linked goal's line (%q):\n%s", marked, block)
	}
}

// TestContractGoalCtxNamesThePullVerb: the block is a snapshot taken at spawn,
// so it has to say where the live list lives — the same job graphContext's
// header does for facts.
func TestContractGoalCtxNamesThePullVerb(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	contractGoalCtxAdd(t, st, "week", week, "a goal worth re-reading", 0)

	block := goalContext(contractGoalCtxJob(t, st, "verb", ""))
	if !strings.Contains(block, "kraken goals get") {
		t.Errorf("block does not name the pull verb `kraken goals get`:\n%s", block)
	}
}

// ---------------------------------------------------------------------------
// goalContext — the budget
// ---------------------------------------------------------------------------

// TestContractGoalCtxBytesConstIs700 pins the cap's VALUE, because it is a
// number two other tests depend on arithmetically (the staged-brief ceiling
// here, and graphctx_test.go's, extended "by exactly goalContextBytes").
func TestContractGoalCtxBytesConstIs700(t *testing.T) {
	if goalContextBytes != 700 {
		t.Errorf("goalContextBytes = %d, want 700", goalContextBytes)
	}
}

// TestContractGoalCtxTruncatesOnWholeGoalBoundaries is the cap plus the rule
// that makes it safe: a block cut mid-line would present half a goal as a
// whole one, which is the one way this block could actively mislead a cell.
// So for every goal, the block either carries its FULL text or no part of it.
func TestContractGoalCtxTruncatesOnWholeGoalBoundaries(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)

	var texts []string
	for i := 0; i < 20; i++ {
		text := fmt.Sprintf("goal-%02d-%s", i, strings.Repeat("w", 60))
		texts = append(texts, text)
		scope, period := "week", week
		if i%2 == 1 {
			scope, period = "day", day
		}
		contractGoalCtxAdd(t, st, scope, period, text, 0)
	}

	block := goalContext(contractGoalCtxJob(t, st, "overflow", ""))
	if block == "" {
		t.Fatal("goalContext returned \"\" with twenty undone goals; the cap skips rows, it does not zero the block")
	}
	if len(block) > goalContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap", len(block), goalContextBytes)
	}
	for _, text := range texts {
		// the id prefix is unique per goal, so a partial line is detectable as
		// "the prefix is here but the whole text is not"
		prefix := text[:len("goal-00-")+8]
		if strings.Contains(block, prefix) && !strings.Contains(block, text) {
			t.Errorf("goal %q was cut mid-line; the block must truncate on whole-goal boundaries:\n%s", text, block)
		}
	}
}

// TestContractGoalCtxKeepsTheStagedBriefUnderItsCeiling is the integration
// half: cell.go stages zeusBrief + goalContext + graphContext, and the
// ceiling is zeusBriefCeiling (crew_test.go's shared const — a ratchet there
// moves this sum with it, with no number here to go stale) plus each
// injection's declared cap.
func TestContractGoalCtxKeepsTheStagedBriefUnderItsCeiling(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)
	for i := 0; i < 20; i++ {
		scope, period := "week", week
		if i%2 == 1 {
			scope, period = "day", day
		}
		contractGoalCtxAdd(t, st, scope, period, fmt.Sprintf("ceiling goal %02d %s", i, strings.Repeat("z", 50)), 0)
	}
	j := contractGoalCtxJob(t, st, "ceiling", "")

	const ceiling = zeusBriefCeiling + graphContextBytes + goalContextBytes
	if n := len(zeusBrief(j) + goalContext(j) + graphContext(j)); n > ceiling {
		t.Errorf("staged brief is %d bytes, over the %d-byte combined ceiling", n, ceiling)
	}
}

// ---------------------------------------------------------------------------
// Recall — the goals block
// ---------------------------------------------------------------------------

// TestContractRecallGoalsBlockOnFirstUserPrompt is the widening's happy path,
// asserted as an IDENTITY against goalContext rather than against a
// hand-written string: "one renderer, two call sites" is the obligation, and
// only comparing the two can fail while both are individually plausible.
func TestContractRecallGoalsBlockOnFirstUserPrompt(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, day := contractGoalCtxPeriods(t)
	parent := contractGoalCtxAdd(t, st, "week", week, "weekly for recall", 0)
	contractGoalCtxAdd(t, st, "day", day, "daily for recall", parent.ID)
	j := contractGoalCtxJob(t, st, "recall-goals", "")

	want := goalContext(j)
	if want == "" {
		t.Fatal("the spawn block is empty for these fixtures; the comparison would be vacuous")
	}
	got := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-goals-1", Event: "UserPromptSubmit", Slug: "recall-goals",
		Prompt: "what should I pick up next",
	})
	if got != want {
		t.Errorf("Recall context differs from the spawn goals block:\n got: %q\nwant: %q", got, want)
	}
}

// TestContractRecallGoalsSuppressedWhenTheSetIsUnchanged is the change-gate:
// the block is unsolicited text in someone else's conversation, so an
// unchanged goal set on a later firing injects NOTHING. The second firing uses
// a DIFFERENT prompt on purpose — otherwise the pre-existing same-query
// short-circuit would make the assertion vacuous.
func TestContractRecallGoalsSuppressedWhenTheSetIsUnchanged(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	contractGoalCtxAdd(t, st, "week", week, "unchanged weekly", 0)
	contractGoalCtxJob(t, st, "recall-unchanged", "")

	first := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-unchanged", Event: "UserPromptSubmit", Slug: "recall-unchanged", Prompt: "first prompt"})
	if first == "" {
		t.Fatal("the first UserPromptSubmit injected nothing")
	}
	second := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-unchanged", Event: "UserPromptSubmit", Slug: "recall-unchanged", Prompt: "a second, different prompt"})
	if second != "" {
		t.Errorf("an unchanged goal set injected again:\n%q", second)
	}
}

// TestContractRecallGoalsReEmittedAfterAChange is the other half: editing a
// goal under a live cell is exactly the moment the refresh is for.
func TestContractRecallGoalsReEmittedAfterAChange(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	g := contractGoalCtxAdd(t, st, "week", week, "the original wording", 0)
	j := contractGoalCtxJob(t, st, "recall-changed", "")

	if first := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-changed", Event: "UserPromptSubmit", Slug: "recall-changed", Prompt: "p1"}); first == "" {
		t.Fatal("the first UserPromptSubmit injected nothing")
	}

	text := "the edited wording"
	if err := st.UpdateGoal(g.ID, store.GoalUpdate{Text: &text}, 1_700_000_002_000); err != nil {
		t.Fatalf("UpdateGoal: %v", err)
	}
	second := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-changed", Event: "UserPromptSubmit", Slug: "recall-changed", Prompt: "p2"})
	if second == "" {
		t.Fatal("a CHANGED goal set injected nothing")
	}
	if !strings.Contains(second, "the edited wording") {
		t.Errorf("the re-emitted block does not carry the edit:\n%s", second)
	}
	if want := goalContext(JobBySlug(LoadJobs(), "recall-changed")); second != want {
		t.Errorf("re-emitted block differs from the spawn block:\n got: %q\nwant: %q", second, want)
	}
	_ = j
}

// TestContractRecallGoalsDeliveredEvenWhenTheQueryIsUnchanged pins the
// ORDERING clause literally: goals are decided BEFORE the same-query
// short-circuit. Carl edits a goal and hits enter on the same text he sent
// last turn — the refresh must still arrive, or the one moment the feature
// exists for is the one moment it is swallowed.
func TestContractRecallGoalsDeliveredEvenWhenTheQueryIsUnchanged(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	contractGoalCtxAdd(t, st, "week", week, "first goal", 0)
	contractGoalCtxJob(t, st, "recall-samequery", "")

	const prompt = "the very same prompt, twice"
	if first := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-samequery", Event: "UserPromptSubmit", Slug: "recall-samequery", Prompt: prompt}); first == "" {
		t.Fatal("the first UserPromptSubmit injected nothing")
	}
	contractGoalCtxAdd(t, st, "week", week, "a brand new goal", 0)

	second := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-samequery", Event: "UserPromptSubmit", Slug: "recall-samequery", Prompt: prompt})
	if second == "" {
		t.Fatal("a changed goal set was swallowed by the same-query short-circuit")
	}
	if !strings.Contains(second, "a brand new goal") {
		t.Errorf("the block does not carry the new goal:\n%s", second)
	}
}

// TestContractRecallGoalsOnlyOnUserPromptSubmit: PostToolUse fires per tool
// call, and goals on every one of those would be the noise the once-per-
// session rule exists to prevent everywhere else.
func TestContractRecallGoalsOnlyOnUserPromptSubmit(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	contractGoalCtxAdd(t, st, "week", week, "a goal nobody should see here", 0)
	contractGoalCtxJob(t, st, "recall-event", "")

	for _, event := range []string{"PostToolUse", "Stop", ""} {
		got := Recall(context.Background(), st, RecallRequest{
			SessionID: "sess-event-" + event, Event: event, Slug: "recall-event",
			Prompt: "a prompt on a non-UserPromptSubmit firing"})
		if got != "" {
			t.Errorf("event %q injected a goals block:\n%s", event, got)
		}
	}
}

// TestContractRecallGoalsResolveTheJobBySessionID is the fallback resolver:
// an old settings.json (installed before the slug argument existed) sends no
// slug, so the job is found by matching jobs.session against the payload's
// session id.
func TestContractRecallGoalsResolveTheJobBySessionID(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	contractGoalCtxAdd(t, st, "week", week, "found by session", 0)
	contractGoalCtxJob(t, st, "recall-by-session", "sess-by-session")

	got := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-by-session", Event: "UserPromptSubmit", Prompt: "no slug in this payload"})
	if got == "" {
		t.Fatal("a slugless payload whose session id matches a job injected nothing")
	}
	if !strings.Contains(got, "found by session") {
		t.Errorf("block does not carry the goal:\n%s", got)
	}
	if want := goalContext(JobBySlug(LoadJobs(), "recall-by-session")); got != want {
		t.Errorf("session-resolved block differs from the spawn block:\n got: %q\nwant: %q", got, want)
	}
}

// TestContractRecallGoalsEmissionNeverTouchesKgRecall keeps the two channels
// separate: kg_recall is the graph's once-per-session ledger, and a goals-only
// firing must leave it completely alone — otherwise a goal refresh would spend
// the session's fact budget and permanently deny zeus facts nobody read.
func TestContractRecallGoalsEmissionNeverTouchesKgRecall(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	contractGoalCtxAdd(t, st, "week", week, "a goal, not a fact", 0)
	contractGoalCtxJob(t, st, "recall-kg", "")

	const sid = "sess-kg-untouched"
	if got := Recall(context.Background(), st, RecallRequest{
		SessionID: sid, Event: "UserPromptSubmit", Slug: "recall-kg", Prompt: "anything"}); got == "" {
		t.Fatal("the goals block was not emitted; the assertion below would be vacuous")
	}
	marked, err := st.RecalledEdgeIDs(sid)
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("goals emission wrote %d kg_recall marks, want 0", len(marked))
	}
}

// TestContractRecallGoalsBlockPrecedesTheGraphBlock pins the concatenation
// order the contract states — goalsBlock + graphBlock — with both halves
// non-empty, which is the only arrangement in which the order is observable.
func TestContractRecallGoalsBlockPrecedesTheGraphBlock(t *testing.T) {
	contractGoalCtxEnv(t)
	st := storeForGraph(t)
	const query = "the throttled daemon pass reads transcripts past a byte watermark"
	withFacts(t, []store.GraphEdge{
		edge(1, "the throttled daemon pass stamps its watermark before any early bail", 3),
	})

	week, _ := store.GoalPeriods(time.Now())
	if _, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "a goal about nothing in particular"}, 1); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
	row := store.Job{Slug: "recall-order", Repo: "kraken", State: "working", Body: query}
	if _, err := st.CreateJob(&row); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	j := JobBySlug(LoadJobs(), "recall-order")
	if j == nil {
		t.Fatal("job not found after create")
	}

	goals := goalContext(j)
	graph := graphContext(j)
	if goals == "" || graph == "" {
		t.Fatalf("fixture is not both-non-empty (goals %d bytes, graph %d bytes)", len(goals), len(graph))
	}

	got := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-order", Event: "UserPromptSubmit", Slug: "recall-order", Prompt: query})
	if got != goals+graph {
		t.Errorf("Recall context = %q, want goalsBlock+graphBlock = %q", got, goals+graph)
	}
}

// TestContractRecallGoalsResolveTheJobByAFormerSlug closes round 2's gap #6.
// The contract resolves the job "by `req.Slug` against current slug or former
// slugs" — and the former-slug arm is not a curiosity, it is the ordinary
// state of a renamed live job: the recall hook command was written into that
// cell's settings.json with the slug the job had AT INSTALL TIME, and a rename
// does not rewrite it. Without this arm, renaming a job silently stops its
// goals refresh for the rest of the cell's life, with nothing failing.
func TestContractRecallGoalsResolveTheJobByAFormerSlug(t *testing.T) {
	st := contractGoalCtxStore(t)
	week, _ := contractGoalCtxPeriods(t)
	contractGoalCtxAdd(t, st, "week", week, "found by a former slug", 0)
	contractGoalCtxJob(t, st, "recall-old-name", "")

	row, err := st.GetJobBySlug("recall-old-name")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if _, err := st.RenameJob(row.ID, row.Rev, "recall-new-name"); err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	// the fixture is only meaningful if the old name has genuinely stopped
	// being a CURRENT slug — otherwise this passes through the arm above it
	if _, err := st.GetJobBySlug("recall-old-name"); err == nil {
		t.Fatal("the old slug still resolves as a current slug; the rename did not take")
	}

	got := Recall(context.Background(), st, RecallRequest{
		SessionID: "sess-former-slug", Event: "UserPromptSubmit", Slug: "recall-old-name",
		Prompt: "an installed hook still carrying the old name"})
	if got == "" {
		t.Fatal("a payload carrying a FORMER slug injected nothing; a renamed job's goals refresh must survive the rename")
	}
	if !strings.Contains(got, "found by a former slug") {
		t.Errorf("block does not carry the goal:\n%s", got)
	}
	if want := goalContext(JobBySlug(LoadJobs(), "recall-new-name")); got != want {
		t.Errorf("former-slug-resolved block differs from the spawn block:\n got: %q\nwant: %q", got, want)
	}
}
