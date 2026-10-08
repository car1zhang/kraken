package store

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
)

// jobgoals_edge_test.go — the EDGE dimension of 50-contract.md's Slice S1
// STORE obligations for the many-to-many job↔goal link: every refusal, every
// no-op, every boundary of the `job_goals` table and the three verbs over it,
// plus the two directions of the v27 migration.
//
// Written blind from the frozen contract. internal/store/{schema,jobs,goals,
// jobgoals}*.go were never read in their working-tree state; the harness
// (openTest, CreateJob, ListJobs, downgradeFreshDB, schemaVersion) is
// pre-existing package state read at the base commit.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy paths — append order, GoalIDs filled by every loader, the batch
// reader's position ordering, the migration's backfill of a real goal_id.
// What lives here is the other half: the silent no-ops, the two distinct
// refusals (a missing GOAL versus a missing JOB), the watermark columns that
// must not move, the rows that must NOT exist, and the map key that must be
// ABSENT rather than present-and-empty.
//
// Package-level identifiers are prefixed `edgeJG` — this package's test
// namespace already carries an `edgeGoal*` family from the prior round and a
// second blind author is writing beside this file right now.

// edgeJGNow is a fixed unix-millis stamp for goal writes whose timestamp is
// not what the case is about.
const edgeJGNow int64 = 1_760_000_000_000

// edgeJGPeriods is the pair a fixture writes into so no case is accidentally
// about the calendar.
func edgeJGPeriods() (week, day string) { return GoalPeriods(time.Now()) }

// edgeJGGoal seeds one day goal in the current period and returns it.
func edgeJGGoal(t *testing.T, s *Store, text string) *Goal {
	t.Helper()
	_, day := edgeJGPeriods()
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: day, Text: text}, edgeJGNow)
	if err != nil {
		t.Fatalf("seed goal %q: %v", text, err)
	}
	return g
}

// edgeJGJob creates one active job and returns its id.
//
// The empty string is the DRAFT state: the jobs row's CHECK admits only the
// empty string plus working, blocked and done, and "draft" is DERIVED for a
// job that has not been started rather than stored as a literal.
func edgeJGJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken", State: ""})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return id
}

// edgeJGRow is one raw job_goals row. The contract FREEZES this table's
// columns, so reading them directly is reading the requirement rather than
// the implementation — and it is the only way to observe `position`, which no
// accessor in the frozen signature list returns.
type edgeJGRow struct {
	GoalID   int64
	Position int64
}

func edgeJGRawRows(t *testing.T, s *Store, jobID int64) []edgeJGRow {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT goal_id, position FROM job_goals WHERE job_id = ? ORDER BY position, goal_id`, jobID)
	if err != nil {
		t.Fatalf("read job_goals for job %d: %v", jobID, err)
	}
	defer rows.Close()
	var out []edgeJGRow
	for rows.Next() {
		var r edgeJGRow
		if err := rows.Scan(&r.GoalID, &r.Position); err != nil {
			t.Fatalf("scan job_goals: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate job_goals: %v", err)
	}
	return out
}

func edgeJGRawCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM job_goals`).Scan(&n); err != nil {
		t.Fatalf("count job_goals: %v", err)
	}
	return n
}

// edgeJGIDs reads one job's link ids back through the frozen accessor.
func edgeJGIDs(t *testing.T, s *Store, jobID int64) []int64 {
	t.Helper()
	j, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", jobID, err)
	}
	return j.GoalIDs
}

func edgeJGSameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// edgeJGAssertDescriptive pins the contract's "descriptive error" clause at
// the only level a test can: a non-empty, single-line, human-readable sentence
// — it becomes a 400 body verbatim on the route above.
func edgeJGAssertDescriptive(t *testing.T, what string, err error) {
	t.Helper()
	msg := err.Error()
	if strings.TrimSpace(msg) == "" {
		t.Errorf("%s: error message is empty — it becomes a 400 body verbatim", what)
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("%s: error message spans lines (%q) — a 400 body is one sentence", what, msg)
	}
}

// edgeJGWatermarks snapshots the two columns every link write must leave
// alone: `rev` (the editor's optimistic lock) and `updated_at` (the card's
// rendered AGE).
func edgeJGWatermarks(t *testing.T, s *Store, jobID int64) (rev, updatedAt int64) {
	t.Helper()
	j, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", jobID, err)
	}
	return j.Rev, j.UpdatedAt
}

// ---------------------------------------------------------------------------
// AddJobGoal: the no-op and the two refusals
// ---------------------------------------------------------------------------

// TestEdgeJGAddExistingLinkIsAPositionPreservingNoOp is the idempotence clause
// stated with its teeth in: re-adding is not merely "does not error", it must
// not MOVE the link. A re-add that deleted-and-reappended would silently
// reorder a job's chips (and its lead groups in zeus' brief) every time two
// surfaces raced to link the same goal — the exact property the per-link ops
// exist to buy over a full replace.
func TestEdgeJGAddExistingLinkIsAPositionPreservingNoOp(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-readd")
	a := edgeJGGoal(t, s, "alpha")
	b := edgeJGGoal(t, s, "bravo")
	c := edgeJGGoal(t, s, "charlie")

	for _, g := range []*Goal{a, b, c} {
		if err := s.AddJobGoal(jobID, g.ID); err != nil {
			t.Fatalf("AddJobGoal(%d): %v", g.ID, err)
		}
	}
	before := edgeJGRawRows(t, s, jobID)
	if len(before) != 3 {
		t.Fatalf("seed produced %d link rows, want 3: %+v", len(before), before)
	}

	// Re-add the FIRST link (the one an append-on-conflict would move to the
	// end) and then the LAST, twice each.
	for _, g := range []*Goal{a, a, c, c} {
		if err := s.AddJobGoal(jobID, g.ID); err != nil {
			t.Fatalf("re-AddJobGoal(%d) = %v, want nil (a silent no-op)", g.ID, err)
		}
	}

	after := edgeJGRawRows(t, s, jobID)
	if len(after) != 3 {
		t.Fatalf("re-adding produced %d link rows, want the same 3: %+v", len(after), after)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Fatalf("row %d moved: %+v → %+v (whole set %+v → %+v)",
				i, before[i], after[i], before, after)
		}
	}
	if got := edgeJGIDs(t, s, jobID); !edgeJGSameIDs(got, []int64{a.ID, b.ID, c.ID}) {
		t.Errorf("GoalIDs = %v after re-adds, want %v", got, []int64{a.ID, b.ID, c.ID})
	}
}

// TestEdgeJGAddMissingGoalIsADescriptiveErrorNamingTheID pins the refusal that
// the route turns into a 400 the picker renders inline. The id has to be IN
// the sentence: the only actionable fact about "a goal that does not exist" is
// which id was asked for, and a bare FOREIGN KEY constraint failed carries
// neither the id nor a reader.
func TestEdgeJGAddMissingGoalIsADescriptiveErrorNamingTheID(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-missing-goal")
	real := edgeJGGoal(t, s, "a real goal")
	if err := s.AddJobGoal(jobID, real.ID); err != nil {
		t.Fatalf("seed link: %v", err)
	}

	for _, goalID := range []int64{987654, -1, real.ID + 9999} {
		err := s.AddJobGoal(jobID, goalID)
		if err == nil {
			t.Errorf("AddJobGoal(job, goal %d) = nil — a link to a goal that does not exist must be refused", goalID)
			continue
		}
		edgeJGAssertDescriptive(t, fmt.Sprintf("AddJobGoal missing goal %d", goalID), err)
		if !strings.Contains(err.Error(), strconv.FormatInt(goalID, 10)) {
			t.Errorf("AddJobGoal(job, goal %d) error %q does not name the goal id", goalID, err)
		}
	}

	// A refused add must not have written a row on its way out, and must not
	// have disturbed the link the job already had.
	if got := edgeJGIDs(t, s, jobID); !edgeJGSameIDs(got, []int64{real.ID}) {
		t.Errorf("GoalIDs = %v after refused adds, want just %v", got, []int64{real.ID})
	}
	if n := edgeJGRawCount(t, s); n != 1 {
		t.Errorf("job_goals holds %d rows after refused adds, want the 1 real link", n)
	}
}

// TestEdgeJGAddZeroIsAMissingGoalRefusalNeverAClear pins the amended row: 0 is
// the WIRE's clear-all and nothing else. At this layer it is an id matching no
// goal, and the difference is a whole job's labels — a store that read 0 as
// "clear" would turn one drifted client's `{"goal_id":0,"op":"add"}` into a
// silent wipe of every link the job has, with a 200 answering it.
//
// It is asserted as NOT `ErrNotFound` as well, because the two refusals are
// what the route turns into a 400 and a 404: the JOB here exists, so an
// ErrNotFound would send a bad request down the dead-job path.
func TestEdgeJGAddZeroIsAMissingGoalRefusalNeverAClear(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-add-zero")
	a := edgeJGGoal(t, s, "alpha")
	b := edgeJGGoal(t, s, "bravo")
	for _, g := range []*Goal{a, b} {
		if err := s.AddJobGoal(jobID, g.ID); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	err := s.AddJobGoal(jobID, 0)
	if err == nil {
		t.Fatal("AddJobGoal(job, 0) = nil — 0 is a goal id matching nothing, never a clear")
	}
	edgeJGAssertDescriptive(t, "AddJobGoal(job, 0)", err)
	if errors.Is(err, ErrNotFound) {
		t.Errorf("AddJobGoal(job, 0) = %v (ErrNotFound) — the JOB exists; the refusal is about the goal", err)
	}
	if got := edgeJGIDs(t, s, jobID); !edgeJGSameIDs(got, []int64{a.ID, b.ID}) {
		t.Errorf("GoalIDs = %v after AddJobGoal(job, 0), want %v — it must not have cleared anything",
			got, []int64{a.ID, b.ID})
	}

	// the same refusal against a job that has no links, where a "clear"
	// reading would be indistinguishable from success
	bare := edgeJGJob(t, s, "jg-add-zero-bare")
	if err := s.AddJobGoal(bare, 0); err == nil {
		t.Error("AddJobGoal(a link-less job, 0) = nil, want the same missing-goal refusal")
	}
}

// TestEdgeJGAddMissingJobIsErrNotFound is the OTHER refusal, and the split
// between the two is load-bearing rather than stylistic: the route answers 404
// for a job that does not exist and 400 for a goal that does not, so a store
// that collapsed them would make an editor picker holding a just-deleted goal
// indistinguishable from a dead job.
func TestEdgeJGAddMissingJobIsErrNotFound(t *testing.T) {
	s := openTest(t)
	g := edgeJGGoal(t, s, "a goal")
	for _, jobID := range []int64{0, -1, 987654} {
		err := s.AddJobGoal(jobID, g.ID)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("AddJobGoal(job %d, goal) = %v, want ErrNotFound", jobID, err)
		}
	}
	if n := edgeJGRawCount(t, s); n != 0 {
		t.Errorf("job_goals holds %d rows after adds against missing jobs, want 0", n)
	}
}

// ---------------------------------------------------------------------------
// RemoveJobGoal: the no-op, the refusal, and the positions that must NOT move
// ---------------------------------------------------------------------------

// TestEdgeJGRemoveAbsentLinkIsASilentNoOp covers both shapes of "absent": a
// goal that exists but was never linked, and one this job used to link and no
// longer does. Both are a caller whose intent — "this job is not filed under
// that goal" — is already satisfied.
func TestEdgeJGRemoveAbsentLinkIsASilentNoOp(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-remove-absent")
	kept := edgeJGGoal(t, s, "kept")
	never := edgeJGGoal(t, s, "never linked")
	gone := edgeJGGoal(t, s, "linked then removed")

	for _, g := range []*Goal{kept, gone} {
		if err := s.AddJobGoal(jobID, g.ID); err != nil {
			t.Fatalf("seed link %d: %v", g.ID, err)
		}
	}
	if err := s.RemoveJobGoal(jobID, gone.ID); err != nil {
		t.Fatalf("first RemoveJobGoal: %v", err)
	}

	for _, c := range []struct {
		name   string
		goalID int64
	}{
		{"a goal that was never linked", never.ID},
		{"a link already removed", gone.ID},
		{"a goal id matching no goal at all", 987654},
		{"a negative goal id", -1},
	} {
		if err := s.RemoveJobGoal(jobID, c.goalID); err != nil {
			t.Errorf("RemoveJobGoal(%s, goal %d) = %v, want nil", c.name, c.goalID, err)
		}
	}
	if got := edgeJGIDs(t, s, jobID); !edgeJGSameIDs(got, []int64{kept.ID}) {
		t.Errorf("GoalIDs = %v after the no-op removes, want just %v", got, []int64{kept.ID})
	}
}

// TestEdgeJGRemoveMissingJobIsErrNotFound: a job id matching no row means the
// caller was addressing something that does not exist, which is a different
// answer from "that link was already gone".
func TestEdgeJGRemoveMissingJobIsErrNotFound(t *testing.T) {
	s := openTest(t)
	g := edgeJGGoal(t, s, "a goal")
	for _, jobID := range []int64{0, -1, 987654} {
		if err := s.RemoveJobGoal(jobID, g.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("RemoveJobGoal(job %d) = %v, want ErrNotFound", jobID, err)
		}
	}
}

// TestEdgeJGRemoveLeavesSurvivingPositionsAlone is the contract's explicit "no
// renumber" clause, and it is only observable in the `position` column: the id
// ORDER after a middle removal is the same whether the survivors were
// renumbered or not, so an accessor-only test could never see the difference.
// It matters because a renumber makes the next AddJobGoal's MAX(position)+1
// land on a slot a concurrent writer may already be holding.
func TestEdgeJGRemoveLeavesSurvivingPositionsAlone(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-no-renumber")
	a := edgeJGGoal(t, s, "alpha")
	b := edgeJGGoal(t, s, "bravo")
	c := edgeJGGoal(t, s, "charlie")
	for _, g := range []*Goal{a, b, c} {
		if err := s.AddJobGoal(jobID, g.ID); err != nil {
			t.Fatalf("seed link %d: %v", g.ID, err)
		}
	}
	before := map[int64]int64{}
	for _, r := range edgeJGRawRows(t, s, jobID) {
		before[r.GoalID] = r.Position
	}
	if len(before) != 3 {
		t.Fatalf("seed produced %d rows, want 3", len(before))
	}

	if err := s.RemoveJobGoal(jobID, b.ID); err != nil {
		t.Fatalf("RemoveJobGoal(middle): %v", err)
	}

	after := edgeJGRawRows(t, s, jobID)
	if len(after) != 2 {
		t.Fatalf("after removing the middle link there are %d rows, want 2: %+v", len(after), after)
	}
	for _, r := range after {
		if want := before[r.GoalID]; r.Position != want {
			t.Errorf("goal %d moved from position %d to %d — surviving links keep their positions",
				r.GoalID, want, r.Position)
		}
	}
	if got := edgeJGIDs(t, s, jobID); !edgeJGSameIDs(got, []int64{a.ID, c.ID}) {
		t.Errorf("GoalIDs = %v, want %v", got, []int64{a.ID, c.ID})
	}
}

// ---------------------------------------------------------------------------
// ClearJobGoals
// ---------------------------------------------------------------------------

func TestEdgeJGClearMissingJobIsErrNotFound(t *testing.T) {
	s := openTest(t)
	for _, jobID := range []int64{0, -1, 987654} {
		if err := s.ClearJobGoals(jobID); !errors.Is(err, ErrNotFound) {
			t.Errorf("ClearJobGoals(job %d) = %v, want ErrNotFound", jobID, err)
		}
	}
}

// TestEdgeJGClearAtZeroLinksIsANoOp: clearing what is already clear is the
// state the `—` row lands a board in on a double click, and it must not be an
// error the picker has to render.
func TestEdgeJGClearAtZeroLinksIsANoOp(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-clear-empty")

	if err := s.ClearJobGoals(jobID); err != nil {
		t.Fatalf("ClearJobGoals on a job with no links = %v, want nil", err)
	}
	// And after a real clear, the second one is the same no-op.
	g := edgeJGGoal(t, s, "briefly linked")
	if err := s.AddJobGoal(jobID, g.ID); err != nil {
		t.Fatalf("AddJobGoal: %v", err)
	}
	if err := s.ClearJobGoals(jobID); err != nil {
		t.Fatalf("first ClearJobGoals: %v", err)
	}
	if err := s.ClearJobGoals(jobID); err != nil {
		t.Fatalf("second ClearJobGoals = %v, want nil", err)
	}
	if got := edgeJGIDs(t, s, jobID); len(got) != 0 {
		t.Errorf("GoalIDs = %v after the clear, want empty", got)
	}
	if n := edgeJGRawCount(t, s); n != 0 {
		t.Errorf("job_goals holds %d rows after the clear, want 0", n)
	}
}

// TestEdgeJGClearTouchesOnlyTheNamedJob: the clear is scoped by job_id, and a
// DELETE that forgot its WHERE would unfile every job on the board with one
// click of the `—` row.
func TestEdgeJGClearTouchesOnlyTheNamedJob(t *testing.T) {
	s := openTest(t)
	mine := edgeJGJob(t, s, "jg-clear-mine")
	theirs := edgeJGJob(t, s, "jg-clear-theirs")
	a := edgeJGGoal(t, s, "alpha")
	b := edgeJGGoal(t, s, "bravo")
	for _, l := range []struct {
		job  int64
		goal int64
	}{{mine, a.ID}, {mine, b.ID}, {theirs, a.ID}, {theirs, b.ID}} {
		if err := s.AddJobGoal(l.job, l.goal); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	if err := s.ClearJobGoals(mine); err != nil {
		t.Fatalf("ClearJobGoals: %v", err)
	}
	if got := edgeJGIDs(t, s, mine); len(got) != 0 {
		t.Errorf("cleared job still carries %v", got)
	}
	if got := edgeJGIDs(t, s, theirs); !edgeJGSameIDs(got, []int64{a.ID, b.ID}) {
		t.Errorf("the OTHER job's links = %v, want %v — a clear is scoped to one job",
			got, []int64{a.ID, b.ID})
	}
}

// ---------------------------------------------------------------------------
// the watermarks none of the three verbs may move
// ---------------------------------------------------------------------------

// TestEdgeJGNoVerbBumpsRevOrUpdatedAt carries the prior round's SetJobGoal
// obligation forward onto all three replacements at once. `updated_at` is what
// every card renders as its AGE — the one column that says which job has sat
// longest — and `rev` is the job editor's optimistic lock, posted from an OPEN
// form. Bumping either would reset every labelled card's age and 409 that
// form's next save over a number it neither shows nor sent.
func TestEdgeJGNoVerbBumpsRevOrUpdatedAt(t *testing.T) {
	s := openTest(t)
	// rev/updated_at deliberately in the past, so a rewrite to "now" is loud.
	row := Job{Slug: "jg-watermarks", Repo: "kraken", State: "working", Rev: 5,
		CreatedAt: 1_600_000_000_000, UpdatedAt: 1_600_000_000_000}
	jobID, err := s.CreateJob(&row)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	a := edgeJGGoal(t, s, "alpha")
	b := edgeJGGoal(t, s, "bravo")
	wantRev, wantAt := edgeJGWatermarks(t, s, jobID)

	for _, step := range []struct {
		name string
		run  func() error
	}{
		{"add", func() error { return s.AddJobGoal(jobID, a.ID) }},
		{"add a second", func() error { return s.AddJobGoal(jobID, b.ID) }},
		{"add an existing link (the no-op)", func() error { return s.AddJobGoal(jobID, a.ID) }},
		{"remove", func() error { return s.RemoveJobGoal(jobID, a.ID) }},
		{"remove an absent link (the no-op)", func() error { return s.RemoveJobGoal(jobID, a.ID) }},
		{"clear", func() error { return s.ClearJobGoals(jobID) }},
		{"clear at zero links (the no-op)", func() error { return s.ClearJobGoals(jobID) }},
	} {
		if err := step.run(); err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		rev, at := edgeJGWatermarks(t, s, jobID)
		if rev != wantRev {
			t.Errorf("%s moved rev %d → %d", step.name, wantRev, rev)
		}
		if at != wantAt {
			t.Errorf("%s moved updated_at %d → %d", step.name, wantAt, at)
		}
	}
}

// ---------------------------------------------------------------------------
// ListJobGoalLinks: the ABSENT key
// ---------------------------------------------------------------------------

// TestEdgeJGListJobGoalLinksOmitsUnlinkedJobs is the map's stated shape, and
// the distinction is not cosmetic: the board build indexes chips by job id, so
// a present-with-empty entry is a job that reads as "answered: no goals" from a
// reader that only checks key presence, while an ABSENT key is the one
// spelling of "no links" the contract allows.
func TestEdgeJGListJobGoalLinksOmitsUnlinkedJobs(t *testing.T) {
	s := openTest(t)

	// the empty database first: no jobs, no links, no keys
	links, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks on an empty db: %v", err)
	}
	if len(links) != 0 {
		t.Fatalf("ListJobGoalLinks on an empty db = %v, want no entries", links)
	}

	linked := edgeJGJob(t, s, "jg-linked")
	bare := edgeJGJob(t, s, "jg-never-linked")
	cleared := edgeJGJob(t, s, "jg-cleared")
	unlinked := edgeJGJob(t, s, "jg-unlinked-again")
	g := edgeJGGoal(t, s, "the goal")

	if err := s.AddJobGoal(linked, g.ID); err != nil {
		t.Fatalf("link: %v", err)
	}
	if err := s.AddJobGoal(cleared, g.ID); err != nil {
		t.Fatalf("link to clear: %v", err)
	}
	if err := s.ClearJobGoals(cleared); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := s.AddJobGoal(unlinked, g.ID); err != nil {
		t.Fatalf("link to remove: %v", err)
	}
	if err := s.RemoveJobGoal(unlinked, g.ID); err != nil {
		t.Fatalf("remove: %v", err)
	}

	links, err = s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks: %v", err)
	}
	if got, ok := links[linked]; !ok || !edgeJGSameIDs(got, []int64{g.ID}) {
		t.Errorf("links[%d] = %v (present %v), want %v", linked, got, ok, []int64{g.ID})
	}
	for _, c := range []struct {
		name  string
		jobID int64
	}{
		{"a job that never had a link", bare},
		{"a job whose links were CLEARED", cleared},
		{"a job whose only link was REMOVED", unlinked},
	} {
		if got, ok := links[c.jobID]; ok {
			t.Errorf("%s (id %d) is present in the map as %v — unlinked jobs are ABSENT, never present-with-empty",
				c.name, c.jobID, got)
		}
	}
	if len(links) != 1 {
		t.Errorf("map holds %d entries (%v), want exactly the 1 linked job", len(links), links)
	}
}

// ---------------------------------------------------------------------------
// what removes a link without anybody calling a link verb
// ---------------------------------------------------------------------------

// TestEdgeJGDeletingAGoalRemovesExactlyItsLinks is the cascade's blast radius,
// asserted in both directions on ONE job: the deleted goal's link is gone and
// the job's OTHER links are untouched. That is what makes "a job linked to a
// deleted goal keeps reading cleanly with one fewer goal" true rather than
// merely intended.
func TestEdgeJGDeletingAGoalRemovesExactlyItsLinks(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-goal-deleted")
	other := edgeJGJob(t, s, "jg-goal-deleted-neighbour")
	doomed := edgeJGGoal(t, s, "about to be deleted")
	survivor := edgeJGGoal(t, s, "still here")

	for _, l := range []struct{ job, goal int64 }{
		{jobID, doomed.ID}, {jobID, survivor.ID}, {other, doomed.ID},
	} {
		if err := s.AddJobGoal(l.job, l.goal); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	if err := s.DeleteGoal(doomed.ID); err != nil {
		t.Fatalf("DeleteGoal on a LINKED goal must not be blocked by the link: %v", err)
	}

	// every read path still answers, with one fewer goal
	byID, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID after deleting a linked goal: %v", err)
	}
	if !edgeJGSameIDs(byID.GoalIDs, []int64{survivor.ID}) {
		t.Errorf("GetJobByID GoalIDs = %v, want %v", byID.GoalIDs, []int64{survivor.ID})
	}
	bySlug, err := s.GetJobBySlug("jg-goal-deleted")
	if err != nil {
		t.Fatalf("GetJobBySlug after deleting a linked goal: %v", err)
	}
	if !edgeJGSameIDs(bySlug.GoalIDs, []int64{survivor.ID}) {
		t.Errorf("GetJobBySlug GoalIDs = %v, want %v", bySlug.GoalIDs, []int64{survivor.ID})
	}
	all, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs after deleting a linked goal: %v", err)
	}
	for _, j := range all {
		if j.ID == jobID && !edgeJGSameIDs(j.GoalIDs, []int64{survivor.ID}) {
			t.Errorf("ListJobs GoalIDs = %v, want %v", j.GoalIDs, []int64{survivor.ID})
		}
	}

	// the neighbour, whose ONLY link was the deleted goal, leaves the map
	// entirely rather than appearing with an empty list
	links, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks: %v", err)
	}
	if got, ok := links[other]; ok {
		t.Errorf("the job whose only link was the deleted goal is present as %v; it must be ABSENT", got)
	}
	if n := edgeJGRawCount(t, s); n != 1 {
		t.Errorf("job_goals holds %d rows after the goal delete, want the 1 surviving link", n)
	}
}

// TestEdgeJGDeletingAJobRemovesItsLinkRows is the other cascade direction.
// Without it the table accumulates rows keyed on a job id that will be handed
// out again by nobody but still shows up in the batch reader's map forever.
func TestEdgeJGDeletingAJobRemovesItsLinkRows(t *testing.T) {
	s := openTest(t)
	doomed := edgeJGJob(t, s, "jg-job-deleted")
	kept := edgeJGJob(t, s, "jg-job-kept")
	g := edgeJGGoal(t, s, "shared goal")
	for _, id := range []int64{doomed, kept} {
		if err := s.AddJobGoal(id, g.ID); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	if err := s.DeleteJob(doomed); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if rows := edgeJGRawRows(t, s, doomed); len(rows) != 0 {
		t.Errorf("job_goals still holds %+v for a hard-deleted job", rows)
	}
	links, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks: %v", err)
	}
	if got, ok := links[doomed]; ok {
		t.Errorf("a hard-deleted job is still in the link map as %v", got)
	}
	if got, ok := links[kept]; !ok || !edgeJGSameIDs(got, []int64{g.ID}) {
		t.Errorf("the surviving job's links = %v (present %v), want %v", got, ok, []int64{g.ID})
	}
}

// TestEdgeJGArchiveAndUnarchiveChangeNoLink: archiving is a move between board
// columns, not a statement about what the job was for, and the wire serves
// `goals` for archived cards precisely so the filter can cover the archive
// drawer. A link dropped on archival would empty that drawer silently.
func TestEdgeJGArchiveAndUnarchiveChangeNoLink(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-archived")
	a := edgeJGGoal(t, s, "alpha")
	b := edgeJGGoal(t, s, "bravo")
	for _, g := range []*Goal{a, b} {
		if err := s.AddJobGoal(jobID, g.ID); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}
	before := edgeJGRawRows(t, s, jobID)

	if _, err := s.ArchiveJob(jobID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if got := edgeJGIDs(t, s, jobID); !edgeJGSameIDs(got, []int64{a.ID, b.ID}) {
		t.Errorf("GoalIDs = %v after archiving, want %v", got, []int64{a.ID, b.ID})
	}
	links, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks while archived: %v", err)
	}
	if got, ok := links[jobID]; !ok || !edgeJGSameIDs(got, []int64{a.ID, b.ID}) {
		t.Errorf("an ARCHIVED job's links = %v (present %v), want %v", got, ok, []int64{a.ID, b.ID})
	}

	if err := s.UnarchiveJob(jobID); err != nil {
		t.Fatalf("UnarchiveJob: %v", err)
	}
	after := edgeJGRawRows(t, s, jobID)
	if len(after) != len(before) {
		t.Fatalf("link rows %+v → %+v across the archive round trip", before, after)
	}
	for i := range before {
		if after[i] != before[i] {
			t.Errorf("row %d changed across the archive round trip: %+v → %+v", i, before[i], after[i])
		}
	}
}

// TestEdgeJGFormSaveCannotTouchTheLinks replaces the prior round's
// `goal_id is not in jobFieldColumns` pin with the property that pin existed to
// buy, now that the column is gone: a full form or raw save — which replaces
// every field it knows about — must leave a job's links exactly where they
// were. A client that predates the feature sends no links and must not be able
// to clear them by omission.
func TestEdgeJGFormSaveCannotTouchTheLinks(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-form-save")
	a := edgeJGGoal(t, s, "alpha")
	b := edgeJGGoal(t, s, "bravo")
	for _, g := range []*Goal{a, b} {
		if err := s.AddJobGoal(jobID, g.ID); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}
	before, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	if _, err := s.UpdateJobFields(jobID, before.Rev, map[string]any{
		"repo": "monolith", "base": "main", "body": "rewritten spec", "state": "working",
	}); err != nil {
		t.Fatalf("UpdateJobFields: %v", err)
	}

	if got := edgeJGIDs(t, s, jobID); !edgeJGSameIDs(got, []int64{a.ID, b.ID}) {
		t.Errorf("GoalIDs = %v after a full field save, want %v", got, []int64{a.ID, b.ID})
	}
	// and the allowlist itself carries no link-shaped key, so no caller can
	// smuggle one in through the generated statement
	for _, key := range []string{"goal_id", "goal_ids", "goals", "job_goals"} {
		if jobFieldColumns[key] {
			t.Errorf("jobFieldColumns carries %q — a full form/raw save can now reach a job's goal links", key)
		}
	}
}

// TestEdgeJGUnlinkedJobHasEmptyGoalIDsOnEveryLoader: the "empty/nil when
// unlinked" clause, checked on all three loaders because ONE attach helper with
// N call sites is exactly the FormerSlugs hazard the contract names — a loader
// that forgot the attach serves an unlinked job that is in fact linked, and
// nothing errors.
func TestEdgeJGUnlinkedJobHasEmptyGoalIDsOnEveryLoader(t *testing.T) {
	s := openTest(t)
	jobID := edgeJGJob(t, s, "jg-never-linked-loaders")
	// A second, LINKED job exists so a loader that returns the wrong job's
	// ids (a join gone wrong) shows up as a non-empty answer here.
	other := edgeJGJob(t, s, "jg-linked-neighbour")
	g := edgeJGGoal(t, s, "someone else's goal")
	if err := s.AddJobGoal(other, g.ID); err != nil {
		t.Fatalf("seed neighbour link: %v", err)
	}

	byID, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if len(byID.GoalIDs) != 0 {
		t.Errorf("GetJobByID GoalIDs = %v for an unlinked job, want empty", byID.GoalIDs)
	}
	bySlug, err := s.GetJobBySlug("jg-never-linked-loaders")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if len(bySlug.GoalIDs) != 0 {
		t.Errorf("GetJobBySlug GoalIDs = %v for an unlinked job, want empty", bySlug.GoalIDs)
	}
	all, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	for _, j := range all {
		if j.ID == jobID && len(j.GoalIDs) != 0 {
			t.Errorf("ListJobs GoalIDs = %v for an unlinked job, want empty", j.GoalIDs)
		}
		if j.ID == other && !edgeJGSameIDs(j.GoalIDs, []int64{g.ID}) {
			t.Errorf("the control job's ListJobs GoalIDs = %v, want %v — the fixture just broke", j.GoalIDs, []int64{g.ID})
		}
	}
}

// ---------------------------------------------------------------------------
// the v27 migration, both directions
// ---------------------------------------------------------------------------

// TestEdgeJGMigrationV26ToV27LeavesNoRowForANullGoalID is the backfill's
// negative half. `INSERT … SELECT id, goal_id FROM jobs` without the `WHERE
// goal_id IS NOT NULL` guard writes a link row whose goal_id is NULL for every
// job carl never labelled — the overwhelming majority — and those rows are
// invisible to a FK check that never fires on NULL, so nothing complains and
// every unlabelled job comes back from the batch reader present-with-a-null.
func TestEdgeJGMigrationV26ToV27LeavesNoRowForANullGoalID(t *testing.T) {
	s := openTest(t)
	// An EXACT pin rather than a floor, deliberately: this fixture rolls a
	// fresh store back to v26 and lets migrate() walk it forward through every
	// later step, so whoever appends the next migration has to come here and
	// confirm downgradeFreshDB reverses their step before this passes again.
	// Bumped to 28 with the account-binding columns, then to 29 with the
	// mental-model cutover, then to 30 with jobs.agent (downgradeFreshDB's v30
	// arm drops that column; v29's arm drops the three mm tables, puts kg_watch
	// back, and rebuilds kg_episodes with its pre-v29 CHECK), then to 31 with
	// the orchestrator's retirement (downgradeFreshDB's v31 arm recreates
	// orch_inbox, which its v24 arm then drops again for this v26 target),
	// then to 32 with the knowledge graph's removal (v32's arm recreates the
	// kg_* tables), then to 33 with the mental-model rework (v33's arm,
	// undoV33, reverses the mm_items rebuild, threads.role and mm_flags).
	if schemaVersion != 33 {
		t.Fatalf("schemaVersion = %d, want 33 — this fixture seeds a v26 shape and walks it forward to the current version", schemaVersion)
	}
	labelled := edgeJGJob(t, s, "jg-mig-labelled")
	unlabelled := edgeJGJob(t, s, "jg-mig-unlabelled")
	second := edgeJGJob(t, s, "jg-mig-second")
	g := edgeJGGoal(t, s, "the pre-migration goal")
	other := edgeJGGoal(t, s, "another pre-migration goal")

	downgradeFreshDB(t, s, 26)

	// a v26-shaped database: jobs.goal_id exists again, two rows carry one,
	// one row carries NULL.
	if _, err := s.db.Exec(`UPDATE jobs SET goal_id = ? WHERE id = ?`, g.ID, labelled); err != nil {
		t.Fatalf("seed jobs.goal_id: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE jobs SET goal_id = ? WHERE id = ?`, other.ID, second); err != nil {
		t.Fatalf("seed second jobs.goal_id: %v", err)
	}
	if _, err := s.db.Exec(`UPDATE jobs SET goal_id = NULL WHERE id = ?`, unlabelled); err != nil {
		t.Fatalf("seed NULL goal_id: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v26: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after migrate, want %d", version, schemaVersion)
	}

	// jobs.goal_id is gone as a COLUMN — a select naming it must fail, which
	// is a stronger claim than "it reads as 0".
	if err := s.db.QueryRow(`SELECT goal_id FROM jobs WHERE id = ?`, labelled).Scan(new(int)); err == nil {
		t.Error("jobs.goal_id is still selectable after v27")
	}

	// exactly two link rows, both at position 0, and NOT ONE for the NULL
	if n := edgeJGRawCount(t, s); n != 2 {
		t.Fatalf("job_goals holds %d rows after the backfill, want exactly 2 (a NULL goal_id yields no row)", n)
	}
	for _, c := range []struct {
		jobID  int64
		goalID int64
	}{{labelled, g.ID}, {second, other.ID}} {
		rows := edgeJGRawRows(t, s, c.jobID)
		if len(rows) != 1 {
			t.Fatalf("job %d has %d link rows after the backfill, want 1: %+v", c.jobID, len(rows), rows)
		}
		if rows[0].GoalID != c.goalID {
			t.Errorf("job %d backfilled goal %d, want %d", c.jobID, rows[0].GoalID, c.goalID)
		}
		if rows[0].Position != 0 {
			t.Errorf("job %d backfilled at position %d, want 0", c.jobID, rows[0].Position)
		}
	}
	if rows := edgeJGRawRows(t, s, unlabelled); len(rows) != 0 {
		t.Errorf("a NULL goal_id produced link rows %+v; it must produce none", rows)
	}
	links, err := s.ListJobGoalLinks()
	if err != nil {
		t.Fatalf("ListJobGoalLinks after the migration: %v", err)
	}
	if got, ok := links[unlabelled]; ok {
		t.Errorf("the never-labelled job is present in the link map as %v after the migration", got)
	}
}

// TestEdgeJGDowngradePast27RestoresTheLowestPositionLink pins the companion
// half of the migration — the one the contract file list would otherwise leave
// off, and the one that silently breaks every OTHER schema test the moment
// something downgrades past 27: `downgradeFreshDB` must reproduce a real v26
// SHAPE, which means the column back, populated from each job's LOWEST-position
// link (the primary — the head of the array the wire serves), and the table
// gone.
func TestEdgeJGDowngradePast27RestoresTheLowestPositionLink(t *testing.T) {
	s := openTest(t)
	multi := edgeJGJob(t, s, "jg-down-multi")
	single := edgeJGJob(t, s, "jg-down-single")
	none := edgeJGJob(t, s, "jg-down-none")
	first := edgeJGGoal(t, s, "the primary")
	secondGoal := edgeJGGoal(t, s, "the secondary")
	thirdGoal := edgeJGGoal(t, s, "the tertiary")

	for _, l := range []struct{ job, goal int64 }{
		{multi, first.ID}, {multi, secondGoal.ID}, {multi, thirdGoal.ID},
		{single, secondGoal.ID},
	} {
		if err := s.AddJobGoal(l.job, l.goal); err != nil {
			t.Fatalf("seed link: %v", err)
		}
	}

	downgradeFreshDB(t, s, 26)

	// the table is gone
	if _, err := s.db.Exec(`SELECT 1 FROM job_goals`); err == nil {
		t.Error("job_goals survived the downgrade past v27")
	}
	// the column is back, carrying each job's lowest-position link
	for _, c := range []struct {
		name  string
		jobID int64
		want  int64 // 0 means the column must be NULL
	}{
		{"a job with three links keeps the head of the array", multi, first.ID},
		{"a job with one link keeps it", single, secondGoal.ID},
		{"a job with no links comes back NULL", none, 0},
	} {
		var got int64
		err := s.db.QueryRow(`SELECT COALESCE(goal_id, 0) FROM jobs WHERE id = ?`, c.jobID).Scan(&got)
		if err != nil {
			t.Fatalf("%s: read jobs.goal_id: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: jobs.goal_id = %d, want %d", c.name, got, c.want)
		}
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != 26 {
		t.Errorf("user_version = %d after downgrading to 26, want 26", version)
	}
}
