package jobs

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// goalctxmulti_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S2: jobGoalIDs, renderGoalsBlock's set marking, and
// the recall block's re-arm on a link change.
//
// Written from the contract text alone. The working-tree versions of
// internal/jobs/{goalctx,recall,kronos}.go were never read; every piece of
// context here is PRE-EXISTING package state at the base commit (HEAD =
// 66eb71a) — graphctx_test.go's storeForGraph/withFacts, the prior round's
// goalctx_contract_test.go and goalrecall_pin_test.go, and the three
// package-level constants goalContextHeader / goalContextMarker /
// goalContextBytes — unexported, and reachable here only because this file is
// in the same package — all of which the contract states are UNCHANGED.
//
// Dimension: LOGIC — the stated content obligations and the conditions under
// which a block is emitted. The failure arms ("every failure → nil", "every
// failure → \"\""), hostile goal text and the cap's inside-a-group break are
// athena-edge's ground; the two "" cases kept below are SUCCESS contracts (the
// spawn path's byte-identical-brief guarantee), not refusals.
//
// Every package-level identifier declared here is prefixed `contractGM` —
// athena-edge shares this package's test namespace, as do the pre-existing
// `contractGoalCtx*` and `contractRecall*` families.

const contractGMNow = int64(1_700_000_000_000)

// contractGMStore opens a scratch store, wires it active, and stubs the graph
// seam EMPTY — so every block observed here is the goals block and nothing
// else.
func contractGMStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	st := storeForGraph(t)
	withFacts(t, nil)
	return st
}

// contractGMPeriods is the current (week, day) pair renderGoalsBlock reads for.
func contractGMPeriods(t *testing.T) (string, string) {
	t.Helper()
	return store.GoalPeriods(time.Now())
}

// contractGMGoal seeds one goal in an explicit period.
func contractGMGoal(t *testing.T, st *store.Store, scope, period, text string) *store.Goal {
	t.Helper()
	g, err := st.AddGoal(store.GoalAdd{Scope: scope, Period: period, Text: text}, contractGMNow)
	if err != nil {
		t.Fatalf("AddGoal(%s/%s %q): %v", scope, period, text, err)
	}
	return g
}

// contractGMWeekGoal seeds one CURRENT-week goal.
func contractGMWeekGoal(t *testing.T, st *store.Store, text string) *store.Goal {
	t.Helper()
	week, _ := contractGMPeriods(t)
	return contractGMGoal(t, st, "week", week, text)
}

// contractGMJob creates a job row and returns the loaded jobs.Job — through
// the package's own load path rather than a hand-built struct, so whatever
// field carries the links is populated the way a real spawn sees it.
func contractGMJob(t *testing.T, st *store.Store, slug string) *Job {
	t.Helper()
	row := store.Job{Slug: slug, Repo: "kraken", State: "working", Body: "rewrite the tmux pane discovery loop"}
	if _, err := st.CreateJob(&row); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	j := JobBySlug(LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not found after create", slug)
	}
	return j
}

func contractGMLink(t *testing.T, st *store.Store, jobID, goalID int64) {
	t.Helper()
	if err := st.AddJobGoal(jobID, goalID); err != nil {
		t.Fatalf("AddJobGoal(job %d, goal %d): %v", jobID, goalID, err)
	}
}

// contractGMLineWith returns the single block line containing needle.
func contractGMLineWith(t *testing.T, block, needle string) string {
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

func contractGMSameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// jobGoalIDs
// ---------------------------------------------------------------------------

// TestContractGMJobGoalIDsReReadsTheRowInPositionOrder is jobGoalIDs' whole
// reason for re-reading rather than trusting the Job in hand: the link write
// bumps neither `rev` nor `updated_at`, so a jobs.Job loaded at any point
// before the link — which is every spawn path, all of which load the job first
// — can be arbitrarily stale and nothing about it says so.
//
// The fixture loads the job BEFORE the links are made and passes that stale
// value in, which is exactly the shape the real callers have.
func TestContractGMJobGoalIDsReReadsTheRowInPositionOrder(t *testing.T) {
	st := contractGMStore(t)
	stale := contractGMJob(t, st, "stale-in-hand")
	second := contractGMWeekGoal(t, st, "linked second in the table")
	first := contractGMWeekGoal(t, st, "linked first in the table")

	contractGMLink(t, st, stale.ID, second.ID)
	contractGMLink(t, st, stale.ID, first.ID)

	want := []int64{second.ID, first.ID}
	if got := jobGoalIDs(st, stale); !contractGMSameIDs(got, want) {
		t.Errorf("jobGoalIDs on a Job loaded before the links = %v, want %v", got, want)
	}
}

// TestContractGMJobGoalIDsIsEmptyForAnUnlinkedJob — the common answer, and the
// one every brief written before carl files anything depends on.
func TestContractGMJobGoalIDsIsEmptyForAnUnlinkedJob(t *testing.T) {
	st := contractGMStore(t)
	contractGMWeekGoal(t, st, "a goal nobody linked")
	j := contractGMJob(t, st, "unlinked")
	if got := jobGoalIDs(st, j); len(got) != 0 {
		t.Errorf("jobGoalIDs for an unlinked job = %v, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// renderGoalsBlock — set marking
// ---------------------------------------------------------------------------

// TestContractGMMarkerLandsOnEveryLinkedLineAndNoOther is the change's
// central sentence, and both halves fail differently. Marking only the FIRST
// member silently demotes every other goal the cell was pointed at to
// background context; marking a line that is not linked tells zeus the cell is
// for something carl never filed it under.
func TestContractGMMarkerLandsOnEveryLinkedLineAndNoOther(t *testing.T) {
	st := contractGMStore(t)
	a := contractGMWeekGoal(t, st, "alpha goal")
	b := contractGMWeekGoal(t, st, "bravo goal")
	c := contractGMWeekGoal(t, st, "charlie goal")

	block := renderGoalsBlock(st, []int64{a.ID, c.ID})
	if block == "" {
		t.Fatal("renderGoalsBlock returned \"\" with three current goals seeded")
	}
	if n := strings.Count(block, goalContextMarker); n != 2 {
		t.Fatalf("block carries %d markers, want exactly 2 (one per linked goal):\n%s", n, block)
	}
	if line := contractGMLineWith(t, block, a.Text); !strings.Contains(line, goalContextMarker) {
		t.Errorf("the first linked goal's line is unmarked: %q", line)
	}
	if line := contractGMLineWith(t, block, c.Text); !strings.Contains(line, goalContextMarker) {
		t.Errorf("the second linked goal's line is unmarked: %q", line)
	}
	if line := contractGMLineWith(t, block, b.Text); strings.Contains(line, goalContextMarker) {
		t.Errorf("an UNLINKED goal's line carries the marker: %q", line)
	}
}

// TestContractGMLinkedDoneGoalStillRenders is the done-filter carve-out
// becoming set membership. A done goal is normally dropped — it steers
// nothing — but the one the cell was deliberately pointed at is the line that
// says what the cell is FOR, and dropping it because carl ticked it off would
// leave a brief that never names its own subject. The unlinked done goal in
// the same fixture is the control: the carve-out is membership, not "done
// goals now render".
func TestContractGMLinkedDoneGoalStillRenders(t *testing.T) {
	st := contractGMStore(t)
	linkedDone := contractGMWeekGoal(t, st, "the finished goal this cell serves")
	looseDone := contractGMWeekGoal(t, st, "a finished goal nobody linked")
	open := contractGMWeekGoal(t, st, "an open goal")
	done := true
	for _, g := range []*store.Goal{linkedDone, looseDone} {
		if err := st.UpdateGoal(g.ID, store.GoalUpdate{Done: &done}, contractGMNow+1); err != nil {
			t.Fatalf("mark %q done: %v", g.Text, err)
		}
	}

	block := renderGoalsBlock(st, []int64{linkedDone.ID})
	if !strings.Contains(block, linkedDone.Text) {
		t.Errorf("a linked DONE goal was dropped from the block:\n%s", block)
	}
	if line := contractGMLineWith(t, block, linkedDone.Text); !strings.Contains(line, goalContextMarker) {
		t.Errorf("the linked done goal's line is unmarked: %q", line)
	}
	if strings.Contains(block, looseDone.Text) {
		t.Errorf("an UNLINKED done goal rendered; the carve-out is membership, not a done-goals-now-render change:\n%s", block)
	}
	if !strings.Contains(block, open.Text) {
		t.Errorf("the ordinary open goal is missing:\n%s", block)
	}
}

// TestContractGMTwoLinkedDoneGoalsBothRender is the same carve-out at N: the
// old scalar code could only ever carry ONE done goal, and an implementation
// that kept a single "the linked one" exception would drop the second.
func TestContractGMTwoLinkedDoneGoalsBothRender(t *testing.T) {
	st := contractGMStore(t)
	first := contractGMWeekGoal(t, st, "first finished goal")
	second := contractGMWeekGoal(t, st, "second finished goal")
	open := contractGMWeekGoal(t, st, "still open")
	done := true
	for _, g := range []*store.Goal{first, second} {
		if err := st.UpdateGoal(g.ID, store.GoalUpdate{Done: &done}, contractGMNow+1); err != nil {
			t.Fatalf("mark %q done: %v", g.Text, err)
		}
	}

	block := renderGoalsBlock(st, []int64{first.ID, second.ID})
	for _, g := range []*store.Goal{first, second, open} {
		if !strings.Contains(block, g.Text) {
			t.Errorf("%q is missing from the block:\n%s", g.Text, block)
		}
	}
	if n := strings.Count(block, goalContextMarker); n != 2 {
		t.Errorf("block carries %d markers, want 2:\n%s", n, block)
	}
}

// TestContractGMOffPeriodLinksLeadInLinkOrder pins the two clauses that make a
// multi-link block readable: goals the current lists do not cover LEAD as their
// own groups, and they lead in LINK-POSITION order rather than id order or
// whatever order the by-id reads happened to complete in. Leading is what keeps
// them furthest from the byte cap; the order is carl's own statement of which
// goal this job mostly serves.
func TestContractGMOffPeriodLinksLeadInLinkOrder(t *testing.T) {
	st := contractGMStore(t)
	// two goals from a period that has rolled — a Monday, so either scope is
	// legal for it
	older := contractGMGoal(t, st, "week", "2026-01-05", "the older off-period goal")
	newer := contractGMGoal(t, st, "week", "2026-01-12", "the newer off-period goal")
	current := contractGMWeekGoal(t, st, "this week's own goal")

	// linked NEWER first, so link order and id order disagree
	block := renderGoalsBlock(st, []int64{newer.ID, older.ID})

	iNewer := strings.Index(block, newer.Text)
	iOlder := strings.Index(block, older.Text)
	iCurrent := strings.Index(block, current.Text)
	if iNewer < 0 || iOlder < 0 || iCurrent < 0 {
		t.Fatalf("block is missing one of the three goals (newer=%d older=%d current=%d):\n%s",
			iNewer, iOlder, iCurrent, block)
	}
	if !(iNewer < iOlder) {
		t.Errorf("off-period leads are in the wrong order (newer at %d, older at %d); want LINK order:\n%s",
			iNewer, iOlder, block)
	}
	if !(iOlder < iCurrent) {
		t.Errorf("an off-period lead (%d) came after the current-week group (%d); leads go first:\n%s",
			iOlder, iCurrent, block)
	}
	if n := strings.Count(block, goalContextMarker); n != 2 {
		t.Errorf("block carries %d markers, want 2:\n%s", n, block)
	}
}

// TestContractGMOneLinkRendersExactlyAsBefore is the contract's
// "byte-identical to today" clause written out as bytes. The whole feature is
// meant to be invisible to a job that links one goal, and the assertion is
// built from the frozen constants rather than from a golden string, so it
// still means what it says if the header is ever reworded.
func TestContractGMOneLinkRendersExactlyAsBefore(t *testing.T) {
	st := contractGMStore(t)
	g := contractGMWeekGoal(t, st, "the one goal this job serves")
	j := contractGMJob(t, st, "one-link")
	contractGMLink(t, st, j.ID, g.ID)

	want := goalContextHeader + "- [week] " + g.Text + goalContextMarker + "\n"
	if got := goalContext(j); got != want {
		t.Errorf("goalContext at one link =\n%q\nwant\n%q", got, want)
	}
	if got := renderGoalsBlock(st, []int64{g.ID}); got != want {
		t.Errorf("renderGoalsBlock at one link =\n%q\nwant\n%q", got, want)
	}
}

// TestContractGMNoGoalsIsEmpty is the spawn path's guarantee and the common
// case: before carl has typed a goal, every cell must get the brief it would
// have got before this feature existed, byte for byte. Asserted at both call
// shapes — nil links and an empty slice — because the scalar signature had one
// spelling of "none" and the slice has two.
func TestContractGMNoGoalsIsEmpty(t *testing.T) {
	st := contractGMStore(t)
	j := contractGMJob(t, st, "no-goals")

	if got := renderGoalsBlock(st, nil); got != "" {
		t.Errorf("renderGoalsBlock(nil) on an empty goals table = %q, want \"\"", got)
	}
	if got := renderGoalsBlock(st, []int64{}); got != "" {
		t.Errorf("renderGoalsBlock([]) on an empty goals table = %q, want \"\"", got)
	}
	if got := goalContext(j); got != "" {
		t.Errorf("goalContext on an empty goals table = %q, want \"\"", got)
	}
}

// TestContractGMLeadGroupsAreBoundedByTheByteCap: the cap is UNCHANGED, and it
// is the only thing bounding N lead groups — a job may link any number of goals
// and the block is a fixed slice of zeus' attention. First links win, which is
// what makes the truncation legible rather than arbitrary.
//
// Every lead here is the same length, so "which fit" is monotone and the
// assertion cannot pass by accident on a short goal sneaking in behind a
// skipped long one.
func TestContractGMLeadGroupsAreBoundedByTheByteCap(t *testing.T) {
	st := contractGMStore(t)
	const n = 20
	texts := make([]string, n)
	ids := make([]int64, n)
	for i := 0; i < n; i++ {
		texts[i] = fmt.Sprintf("lead goal %02d %s", i, strings.Repeat("x", 40))
		ids[i] = contractGMGoal(t, st, "week", "2026-01-05", texts[i]).ID
	}

	block := renderGoalsBlock(st, ids)
	if block == "" {
		t.Fatal("renderGoalsBlock returned \"\" with 20 linked goals")
	}
	if len(block) > goalContextBytes {
		t.Errorf("block is %d bytes, over the %d cap:\n%s", len(block), goalContextBytes, block)
	}
	if !strings.Contains(block, texts[0]) {
		t.Errorf("the FIRST linked goal did not make it into the block:\n%s", block)
	}
	if strings.Contains(block, texts[n-1]) {
		t.Errorf("the last of 20 linked goals fits inside a %d-byte cap; the fixture proves nothing:\n%s",
			goalContextBytes, block)
	}
	// The cap stops BETWEEN goals, never inside one: a half-written goal reads
	// as a complete and wrong instruction. Every goal line the block carries
	// must therefore hold one of the texts WHOLE.
	whole := map[string]bool{}
	for _, txt := range texts {
		whole[txt] = true
	}
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		if !strings.HasPrefix(line, "- [") {
			continue // the header
		}
		found := false
		for txt := range whole {
			if strings.Contains(line, txt) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("a goal line carries no goal WHOLE — the cap cut inside one: %q", line)
		}
	}
}

// TestContractGMGoalContextMarksEveryLinkEndToEnd is the spawn path's own
// assertion — goalContext takes a *Job and nothing else, so a jobGoalIDs that
// returned only the first id would leave every other chip unmarked in the
// brief while renderGoalsBlock's own tests above all still passed.
func TestContractGMGoalContextMarksEveryLinkEndToEnd(t *testing.T) {
	st := contractGMStore(t)
	a := contractGMWeekGoal(t, st, "alpha goal")
	b := contractGMWeekGoal(t, st, "bravo goal")
	unlinked := contractGMWeekGoal(t, st, "charlie goal")
	j := contractGMJob(t, st, "two-links")
	contractGMLink(t, st, j.ID, a.ID)
	contractGMLink(t, st, j.ID, b.ID)

	block := goalContext(j)
	if n := strings.Count(block, goalContextMarker); n != 2 {
		t.Fatalf("goalContext carries %d markers, want 2:\n%s", n, block)
	}
	if line := contractGMLineWith(t, block, a.Text); !strings.Contains(line, goalContextMarker) {
		t.Errorf("%q is unmarked: %q", a.Text, line)
	}
	if line := contractGMLineWith(t, block, b.Text); !strings.Contains(line, goalContextMarker) {
		t.Errorf("%q is unmarked: %q", b.Text, line)
	}
	if line := contractGMLineWith(t, block, unlinked.Text); strings.Contains(line, goalContextMarker) {
		t.Errorf("the unlinked goal is marked: %q", line)
	}
}

// ---------------------------------------------------------------------------
// recall — re-arm on a link change
// ---------------------------------------------------------------------------

// TestContractGMRecallReArmsOnceOnEveryLinkChange is the fingerprint clause
// stated as the sequence a real session actually produces. The mechanism is
// unchanged — the suppressor hashes the RENDERED block — so what is under test
// is that a link add, a link remove and a clear each MOVE that rendering, and
// that each one re-arms EXACTLY once: a change that did not re-arm leaves zeus
// steering by a goal set carl has already edited, and one that re-armed on
// every firing would put the same unchanged block in front of it on every
// prompt for the rest of the session.
//
// Every prompt below is distinct, so the graph half's same-query short-circuit
// is not what is being measured, and the graph seam is stubbed empty so the
// only thing that can appear is the goals block.
func TestContractGMRecallReArmsOnceOnEveryLinkChange(t *testing.T) {
	st := contractGMStore(t)
	const slug = "recall-links"
	j := contractGMJob(t, st, slug)
	first := contractGMWeekGoal(t, st, "first tracked goal")
	second := contractGMWeekGoal(t, st, "second tracked goal")

	req := RecallRequest{SessionID: "sess-" + slug, Event: "UserPromptSubmit", Slug: slug}
	firings := 0
	fire := func() string {
		t.Helper()
		firings++
		req.Prompt = fmt.Sprintf("prompt %d", firings)
		return Recall(context.Background(), st, req)
	}

	// baseline: an unlinked job still sees carl's goals, unmarked
	base := fire()
	if !strings.Contains(base, first.Text) || !strings.Contains(base, second.Text) {
		t.Fatalf("the first firing did not carry both goals:\n%q", base)
	}
	if strings.Contains(base, goalContextMarker) {
		t.Fatalf("an unlinked job's block carries the linked marker:\n%q", base)
	}
	if got := fire(); got != "" {
		t.Fatalf("an unchanged goal state was re-delivered:\n%q", got)
	}

	step := func(what string, mutate func(), wantMarkers int) {
		t.Helper()
		mutate()
		got := fire()
		if got == "" {
			t.Fatalf("%s did not re-arm the goals block", what)
		}
		if marks := strings.Count(got, goalContextMarker); marks != wantMarkers {
			t.Fatalf("after %s the block carries %d markers, want %d:\n%q", what, marks, wantMarkers, got)
		}
		if again := fire(); again != "" {
			t.Fatalf("%s re-armed more than once — the next, unchanged firing delivered:\n%q", what, again)
		}
	}

	step("a link add", func() { contractGMLink(t, st, j.ID, first.ID) }, 1)
	step("a second link add", func() { contractGMLink(t, st, j.ID, second.ID) }, 2)
	step("a link remove", func() {
		if err := st.RemoveJobGoal(j.ID, second.ID); err != nil {
			t.Fatalf("RemoveJobGoal: %v", err)
		}
	}, 1)
	step("a clear", func() {
		if err := st.ClearJobGoals(j.ID); err != nil {
			t.Fatalf("ClearJobGoals: %v", err)
		}
	}, 0)
}
