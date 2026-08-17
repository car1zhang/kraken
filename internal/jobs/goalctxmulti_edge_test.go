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

// goalctxmulti_edge_test.go — the EDGE dimension of 50-contract.md's Slice S2:
// what the multi-link goals block does at its BOUNDARIES — zero links, links
// that resolve to nothing, more lead groups than the byte cap can hold — and
// what the recall channel does when a LINK changes rather than a goal.
//
// Written blind from the frozen contract. internal/jobs/{goalctx,recall,
// kronos}.go were never read in their working-tree state; the harness idioms
// (UseStore, JobBySlug/LoadJobs, the graphFacts seam, RecallRequest) are
// pre-existing package state read at the base commit.
//
// The sibling blind author (athena-logic) owns the stated rendering
// invariants: the marker on every linked line, a linked DONE goal still
// rendering, lead groups ordered by link position ahead of the week groups.
// What lives here is the other half — the "" answers, the cap's behaviour once
// there are N leads competing for 700 bytes, and the three link-shaped changes
// that must re-arm the recall block exactly once each.
//
// Package-level identifiers are prefixed `edgeMG` — this package's test
// namespace already carries `edgeGoal*` and `edgeRecall*` families and a
// second blind author is writing beside this file right now.

// edgeMGMarker is the contract's literal, written out here rather than read
// off a constant: the requirement is the TEXT ` ← this job's goal`, and a test
// that referenced the implementation's constant would agree with a rewording
// of it by construction.
const edgeMGMarker = " ← this job's goal"

// edgeMGEnv opens a scratch store, wires it as the active one, and puts the
// embedder out of reach.
//
// The HOME override is not cosmetic: core.NewEmbedder falls back to
// ~/.kraken/openai.env, which EXISTS on the machine this suite runs on, so
// without it every Recall case below would make a live OpenAI call on the path
// under test.
func edgeMGEnv(t *testing.T) *store.Store {
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

// edgeMGNoFacts silences the knowledge graph for one test, so whatever a
// Recall case observes is the GOALS block and nothing else.
func edgeMGNoFacts(t *testing.T) {
	t.Helper()
	prev := graphFacts
	graphFacts = func(*store.Store, []float32, string) ([]store.GraphEdge, map[int64]bool) { return nil, nil }
	t.Cleanup(func() { graphFacts = prev })
}

func edgeMGJob(t *testing.T, st *store.Store, slug, session string) *Job {
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

func edgeMGAdd(t *testing.T, st *store.Store, g store.GoalAdd) *store.Goal {
	t.Helper()
	out, err := st.AddGoal(g, time.Now().UnixMilli())
	if err != nil {
		t.Fatalf("AddGoal %+v: %v", g, err)
	}
	return out
}

func edgeMGPeriods() (week, day string) { return store.GoalPeriods(time.Now()) }

// edgeMGPastMonday is a week period that is NOT the current one, for the
// off-period linked goals that LEAD the block. A fixed Monday: which one does
// not matter, only that it is never today's.
const edgeMGPastMonday = "2020-01-06"

func edgeMGLink(t *testing.T, st *store.Store, jobID, goalID int64) {
	t.Helper()
	if err := st.AddJobGoal(jobID, goalID); err != nil {
		t.Fatalf("AddJobGoal(%d, %d): %v", jobID, goalID, err)
	}
}

// ---------------------------------------------------------------------------
// jobGoalIDs: the nil answers, and the staleness it exists to defeat
// ---------------------------------------------------------------------------

// TestEdgeMGJobGoalIDsIsNilWhenNoJobResolves: every failure is nil, which is
// the same answer an unlinked job gives — the renderer downstream has one
// no-links path, not two.
func TestEdgeMGJobGoalIDsIsNilWhenNoJobResolves(t *testing.T) {
	st := edgeMGEnv(t)
	// a control that DOES resolve, so a blanket nil would fail here
	j := edgeMGJob(t, st, "mg-resolves", "")
	g := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: edgeMGDay(), Text: "a goal"})
	edgeMGLink(t, st, j.ID, g.ID)
	if got := jobGoalIDs(st, JobBySlug(LoadJobs(), "mg-resolves")); len(got) != 1 || got[0] != g.ID {
		t.Fatalf("control: jobGoalIDs = %v, want [%d]", got, g.ID)
	}

	for _, c := range []struct {
		name string
		job  *Job
	}{
		{"a job with neither an id nor a slug", &Job{}},
		{"a job whose slug names nothing", &Job{Slug: "no-such-job-anywhere"}},
		{"a job whose id names nothing", &Job{ID: 987654}},
	} {
		if got := jobGoalIDs(st, c.job); len(got) != 0 {
			t.Errorf("%s: jobGoalIDs = %v, want nil", c.name, got)
		}
	}
}

// edgeMGDay is today's local day period, a one-liner used by the fixtures.
func edgeMGDay() string { _, day := edgeMGPeriods(); return day }

// TestEdgeMGJobGoalIDsRereadsTheRowSoAnUnlinkIsSeen is the staleness rule in
// its REMOVAL direction, which is the one that misleads rather than merely
// omits: the link write bumps no watermark, so a Job in hand can hold a link
// carl has since cleared, and a renderer that trusted it would mark a goal as
// "this job's goal" after the board says it is not.
func TestEdgeMGJobGoalIDsRereadsTheRowSoAnUnlinkIsSeen(t *testing.T) {
	st := edgeMGEnv(t)
	j := edgeMGJob(t, st, "mg-stale", "")
	a := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: edgeMGDay(), Text: "alpha"})
	b := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: edgeMGDay(), Text: "bravo"})
	edgeMGLink(t, st, j.ID, a.ID)
	edgeMGLink(t, st, j.ID, b.ID)

	// The Job value is captured HERE and never reloaded — exactly the shape a
	// spawn path holds while carl edits the board.
	stale := JobBySlug(LoadJobs(), "mg-stale")

	if err := st.RemoveJobGoal(j.ID, a.ID); err != nil {
		t.Fatalf("RemoveJobGoal: %v", err)
	}
	if got := jobGoalIDs(st, stale); len(got) != 1 || got[0] != b.ID {
		t.Errorf("jobGoalIDs from a stale Job = %v, want [%d] — the row is re-read, not trusted", got, b.ID)
	}
	if err := st.ClearJobGoals(j.ID); err != nil {
		t.Fatalf("ClearJobGoals: %v", err)
	}
	if got := jobGoalIDs(st, stale); len(got) != 0 {
		t.Errorf("jobGoalIDs after a clear = %v, want nil", got)
	}
}

// ---------------------------------------------------------------------------
// renderGoalsBlock: the "" answers
// ---------------------------------------------------------------------------

// TestEdgeMGRenderIsEmptyWithNothingToRender pins the three shapes of "no
// groups", and the nil/empty-slice pair specifically: a caller that built an
// empty slice and a caller that built nil are the same caller, and any
// difference between them is a brief that changes byte-for-byte on nothing.
func TestEdgeMGRenderIsEmptyWithNothingToRender(t *testing.T) {
	st := edgeMGEnv(t)
	if got := renderGoalsBlock(st, nil); got != "" {
		t.Errorf("renderGoalsBlock(nil links, empty table) = %q, want \"\"", got)
	}
	if got := renderGoalsBlock(st, []int64{}); got != "" {
		t.Errorf("renderGoalsBlock(empty links, empty table) = %q, want \"\"", got)
	}
	// The same equality once there ARE goals: nil and [] must render the
	// identical block, since neither says anything about a link.
	edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: edgeMGDay(), Text: "an unlinked current goal"})
	nilBlock := renderGoalsBlock(st, nil)
	emptyBlock := renderGoalsBlock(st, []int64{})
	if nilBlock == "" {
		t.Fatal("renderGoalsBlock rendered nothing for a current, undone goal")
	}
	if nilBlock != emptyBlock {
		t.Errorf("nil links rendered\n%q\nbut an empty slice rendered\n%q", nilBlock, emptyBlock)
	}
	if strings.Contains(nilBlock, edgeMGMarker) {
		t.Errorf("a job with NO links got a marked line:\n%s", nilBlock)
	}
}

// TestEdgeMGLinkIDsThatResolveToNothingForgeNoHeader is the failure mode with
// no alarm on it: link ids that name deleted goals produce no lead groups, and
// with no current-period goals beside them there is nothing left. A header
// alone announces that carl has goals and then declines to say what they are —
// worse than saying nothing, because zeus cannot tell the difference between
// "no goals" and "goals kraken failed to read".
func TestEdgeMGLinkIDsThatResolveToNothingForgeNoHeader(t *testing.T) {
	st := edgeMGEnv(t)
	if got := renderGoalsBlock(st, []int64{987654, 987655}); got != "" {
		t.Errorf("renderGoalsBlock with link ids matching no goal = %q, want \"\"", got)
	}
	// The same through the spawn seam, with the ids REAL until carl deletes
	// them — the way production reaches this state.
	j := edgeMGJob(t, st, "mg-dangling", "")
	g := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: edgeMGDay(), Text: "doomed"})
	edgeMGLink(t, st, j.ID, g.ID)
	if err := st.DeleteGoal(g.ID); err != nil {
		t.Fatalf("DeleteGoal: %v", err)
	}
	if got := goalContext(JobBySlug(LoadJobs(), "mg-dangling")); got != "" {
		t.Errorf("goalContext after the only linked goal was deleted = %q, want \"\"", got)
	}
}

// TestEdgeMGNoStoreIsEmpty: renderGoalsBlock's own nil-store arm, and
// goalContext's. A cell started outside `kraken serve` has no active store,
// and the block must be "" rather than an opened second handle — store.Open
// runs MIGRATIONS, and a spawn is not the place to migrate a database.
func TestEdgeMGNoStoreIsEmpty(t *testing.T) {
	UseStore(nil)
	t.Cleanup(func() { UseStore(nil) })
	if got := renderGoalsBlock(nil, []int64{1, 2, 3}); got != "" {
		t.Errorf("renderGoalsBlock(nil store) = %q, want \"\"", got)
	}
	if got := goalContext(&Job{ID: 1, Slug: "mg-no-store"}); got != "" {
		t.Errorf("goalContext with no active store = %q, want \"\"", got)
	}
	if got := goalContext(nil); got != "" {
		t.Errorf("goalContext(nil) = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// the byte cap, once there are N lead groups
// ---------------------------------------------------------------------------

// TestEdgeMGCapBoundsNLeadGroupsAndFirstLinksWin is the clause the whole
// many-to-many change leans on for its safety: N off-period linked goals each
// LEAD as their own group, and the ONLY thing bounding N is the 700-byte cap.
// Twelve equal-length leads is roughly twice that, so truncation is guaranteed
// — and with the lines all the same size, "a group that does not fit is
// skipped and the next is tried" degenerates to a strict prefix, which is what
// "first links win" means and what makes it testable at all.
//
// Every goal carries a unique opening AND closing tag: a rendered goal must
// show both, and one showing only its opening tag is the mid-line cut that
// would put a sentence carl never wrote into zeus' system prompt.
func TestEdgeMGCapBoundsNLeadGroupsAndFirstLinksWin(t *testing.T) {
	st := edgeMGEnv(t)
	j := edgeMGJob(t, st, "mg-cap", "")

	const n = 12
	texts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		text := fmt.Sprintf("openzz%02d ", i) + strings.Repeat("q", 60) + fmt.Sprintf(" closezz%02d", i)
		texts = append(texts, text)
		// A PAST week period, so each of these is off-period and therefore a
		// LEAD group rather than an ordinary week line.
		g := edgeMGAdd(t, st, store.GoalAdd{Scope: "week", Period: edgeMGPastMonday, Text: text})
		edgeMGLink(t, st, j.ID, g.ID)
	}

	block := goalContext(JobBySlug(LoadJobs(), "mg-cap"))
	if block == "" {
		t.Fatal("goalContext produced nothing for twelve linked goals")
	}
	if len(block) > goalContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(block), goalContextBytes, block)
	}

	var shownIdx []int
	for i, text := range texts {
		open := fmt.Sprintf("openzz%02d ", i)
		if !strings.Contains(block, open) {
			continue
		}
		shownIdx = append(shownIdx, i)
		if !strings.Contains(block, text) {
			t.Errorf("linked goal %d was cut mid-line — the block carries %q but not the whole text:\n%s",
				i, open, block)
		}
	}
	if len(shownIdx) == 0 {
		t.Fatalf("the cap dropped every linked goal, leaving a header with nothing under it:\n%s", block)
	}
	if len(shownIdx) == n {
		t.Fatalf("all %d linked goals fit inside a %d-byte cap — the fixture is not exercising truncation",
			n, goalContextBytes)
	}
	// FIRST LINKS WIN: what survived is a prefix of the link order, not an
	// arbitrary subset. With equal-length lines, any other outcome means the
	// leads were reordered or the cap picked by something other than position.
	for want, got := range shownIdx {
		if got != want {
			t.Fatalf("the surviving leads are %v — with equal-length lines the survivors must be the FIRST links, i.e. a prefix",
				shownIdx)
		}
	}
	// and they appear in that order in the text
	prev := -1
	for _, i := range shownIdx {
		at := strings.Index(block, fmt.Sprintf("openzz%02d ", i))
		if at <= prev {
			t.Fatalf("lead %d appears at byte %d, out of link-position order:\n%s", i, at, block)
		}
		prev = at
	}
	// every one of them is a LINKED goal, so every rendered line carries the
	// marker — the cap must not be trimming the marker off to make a line fit
	if got, want := strings.Count(block, edgeMGMarker), len(shownIdx); got != want {
		t.Errorf("block carries %d markers for %d rendered linked goals:\n%s", got, want, block)
	}
}

// TestEdgeMGSingleLinkBlockKeepsTodaysShape is the "renders byte-identically to
// today" clause at the level a blind author can hold it: the header opens the
// block exactly once, every line is a top-level or nested goal line, and
// exactly one line carries the marker. A one-link job is the ONLY shape that
// existed before this change, so anything new appearing here — a count, a
// section header, a second marker — is the regression.
func TestEdgeMGSingleLinkBlockKeepsTodaysShape(t *testing.T) {
	st := edgeMGEnv(t)
	j := edgeMGJob(t, st, "mg-one-link", "")
	week, day := edgeMGPeriods()
	parent := edgeMGAdd(t, st, store.GoalAdd{Scope: "week", Period: week, Text: "ship goal filtering"})
	edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: day, Text: "land the migration", ParentID: parent.ID})
	linked := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: day, Text: "write the picker"})
	edgeMGLink(t, st, j.ID, linked.ID)

	block := goalContext(JobBySlug(LoadJobs(), "mg-one-link"))
	if block == "" {
		t.Fatal("goalContext produced nothing for a one-link job with current goals")
	}
	if got := strings.Count(block, edgeMGMarker); got != 1 {
		t.Errorf("block carries %d markers, want exactly 1 for a one-link job:\n%s", got, block)
	}
	// Everything after the header line: the block opens with a newline and one
	// header sentence, and every line under it is a goal line and nothing else.
	var body []string
	seenHeader := false
	for _, line := range strings.Split(strings.TrimSuffix(block, "\n"), "\n") {
		if line == "" {
			continue
		}
		if !seenHeader {
			seenHeader = true
			if strings.HasPrefix(line, "- [") || strings.HasPrefix(line, "    ↳ ") {
				t.Fatalf("the block opens with a goal line rather than a header:\n%s", block)
			}
			continue
		}
		body = append(body, line)
	}
	if len(body) == 0 {
		t.Fatalf("the block is a header with nothing under it:\n%s", block)
	}
	for i, line := range body {
		if !strings.HasPrefix(line, "- [") && !strings.HasPrefix(line, "    ↳ ") {
			t.Errorf("goal line %d is neither top-level nor nested: %q\nwhole block:\n%s", i, line, block)
		}
		if strings.Contains(line, edgeMGMarker) && !strings.Contains(line, linked.Text) {
			t.Errorf("the marker landed on a line that is not the linked goal: %q", line)
		}
	}
}

// ---------------------------------------------------------------------------
// recall: a LINK change is a change
// ---------------------------------------------------------------------------

func edgeMGRecallReq(slug, session string) RecallRequest {
	return RecallRequest{
		SessionID: session,
		Event:     "UserPromptSubmit",
		Prompt:    "keep going on the thing we were doing",
		Slug:      slug,
	}
}

// TestEdgeMGRecallReArmsOncePerLinkChange is the contract's link-side clause,
// and it is only meaningful because the PROMPT never changes across these
// firings: a goals decision made after recall.go's same-query short-circuit
// would return "" for every one of them, and the refresh a link change owes
// zeus would never arrive.
//
// All three link verbs are exercised, each followed by a firing that must be
// SUPPRESSED — "re-arms exactly once" is the half that a naive "always send
// when links are non-empty" would pass the first assertion of and fail here.
func TestEdgeMGRecallReArmsOncePerLinkChange(t *testing.T) {
	st := edgeMGEnv(t)
	edgeMGNoFacts(t)
	j := edgeMGJob(t, st, "mg-recall", "sess-mg-recall")
	_, day := edgeMGPeriods()
	a := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: day, Text: "the first goal"})
	b := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: day, Text: "the second goal"})
	req := edgeMGRecallReq("mg-recall", "sess-mg-recall")

	first := Recall(context.Background(), st, req)
	if !strings.Contains(first, a.Text) {
		t.Fatalf("the first UserPromptSubmit carried no goals block:\n%q", first)
	}
	if strings.Contains(first, edgeMGMarker) {
		t.Fatalf("an unlinked job's block carries the linked-goal marker:\n%s", first)
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("an unchanged set was re-sent:\n%s", got)
	}

	for _, step := range []struct {
		name string
		run  func() error
		want string // a substring the re-armed block must carry
	}{
		{"a link ADD", func() error { return st.AddJobGoal(j.ID, a.ID) }, edgeMGMarker},
		{"a SECOND link add", func() error { return st.AddJobGoal(j.ID, b.ID) }, edgeMGMarker},
		{"a link REMOVE", func() error { return st.RemoveJobGoal(j.ID, a.ID) }, edgeMGMarker},
		{"a CLEAR", func() error { return st.ClearJobGoals(j.ID) }, a.Text},
	} {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		got := Recall(context.Background(), st, req)
		if got == "" {
			t.Fatalf("%s did not re-arm the goals block", step.name)
		}
		if !strings.Contains(got, step.want) {
			t.Errorf("%s re-armed a block missing %q:\n%s", step.name, step.want, got)
		}
		if again := Recall(context.Background(), st, req); again != "" {
			t.Fatalf("%s: the re-armed block was sent a SECOND time:\n%s", step.name, again)
		}
	}
	// the clear really did land — the last block must carry no marker at all
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("a firing after the settled clear carried a block:\n%s", got)
	}
}

// TestEdgeMGRecallStaysSuppressedWhenTheLinkSetRoundTrips is the idempotence
// half. Two writes that cancel out are not a change, and a fingerprint over
// the WRITES rather than over the rendered state would re-deliver an identical
// block — the noise the once-per-change design exists to avoid, arriving on
// every board fiddle carl undoes.
func TestEdgeMGRecallStaysSuppressedWhenTheLinkSetRoundTrips(t *testing.T) {
	st := edgeMGEnv(t)
	edgeMGNoFacts(t)
	j := edgeMGJob(t, st, "mg-roundtrip", "sess-mg-roundtrip")
	_, day := edgeMGPeriods()
	g := edgeMGAdd(t, st, store.GoalAdd{Scope: "day", Period: day, Text: "a current goal"})
	edgeMGLink(t, st, j.ID, g.ID)
	req := edgeMGRecallReq("mg-roundtrip", "sess-mg-roundtrip")

	if got := Recall(context.Background(), st, req); got == "" {
		t.Fatal("the first firing carried no goals block")
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Fatalf("the first block was re-sent:\n%s", got)
	}

	// unlink and relink between two firings: the rendered state is identical
	if err := st.RemoveJobGoal(j.ID, g.ID); err != nil {
		t.Fatalf("RemoveJobGoal: %v", err)
	}
	if err := st.AddJobGoal(j.ID, g.ID); err != nil {
		t.Fatalf("AddJobGoal: %v", err)
	}
	if got := Recall(context.Background(), st, req); got != "" {
		t.Errorf("a link set that round-tripped to itself re-sent the block:\n%s", got)
	}
}
