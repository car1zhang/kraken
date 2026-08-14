package store

import (
	"errors"
	"testing"
	"time"
)

// goals_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's store half: GoalPeriods, the validators' accepting
// arm, and the five CRUD methods (ListGoals/AddGoal/UpdateGoal/DeleteGoal/
// SetJobGoal) plus Job.GoalID and the jobFieldColumns exclusion.
//
// Written from the contract text alone. internal/store/goals.go, the v23
// migration and the goal_id additions to jobs.go were never read — only
// PRE-EXISTING repo state at the base commit (store_test.go's openTest,
// questions_test.go's shape, jobs.go's SetJobLoad reasoning).
//
// Dimension: LOGIC — the stated invariants and their happy paths. Refusals
// (bad scope, malformed period, a non-Monday week period, empty text, a
// parent on a week goal, ErrNotFound arms) belong to athena-edge and are not
// probed here; the one refusal-shaped obligation kept below is DeleteGoal's
// "absent id is a NO-OP", because it is a success contract, not a rejection.
//
// Migration byte-identity is already pinned by schema_test.go's
// TestMigratedSchemaMatchesFreshSchema and is deliberately not duplicated.
//
// Every package-level identifier declared here is prefixed `contractGoal` —
// athena-edge shares this package's test namespace.

// contractGoalNow is the fixed write clock (unix millis) every AddGoal/
// UpdateGoal call below stamps with, so a timestamp assertion is exact
// arithmetic rather than a race against the wall clock.
const contractGoalNow = int64(1_700_000_000_000)

// contractGoalLater is a strictly later write clock, for update stamping.
const contractGoalLater = contractGoalNow + 60_000

// contractGoalPeriods returns the current local (week, day) pair — the same
// pair the API's GET serves and every fixture below writes into, so a test
// that seeds "this week" agrees with whatever GoalPeriods says today.
func contractGoalPeriods(t *testing.T) (string, string) {
	t.Helper()
	week, day := GoalPeriods(time.Now())
	return week, day
}

// contractGoalExpectedMonday is the contract's week rule written out
// independently: walk back from d until the weekday IS Monday, which for a
// Monday is zero steps. Used as the oracle GoalPeriods is held against.
func contractGoalExpectedMonday(d time.Time) time.Time {
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, -1)
	}
	return d
}

// contractGoalAdd adds one goal and fails the test if the store refuses it —
// the fixture verb for every happy-path case below.
func contractGoalAdd(t *testing.T, s *Store, scope, period, text string, parent int64) *Goal {
	t.Helper()
	g, err := s.AddGoal(GoalAdd{Scope: scope, Period: period, Text: text, ParentID: parent}, contractGoalNow)
	if err != nil {
		t.Fatalf("AddGoal(%s/%s %q parent=%d): %v", scope, period, text, parent, err)
	}
	if g == nil {
		t.Fatalf("AddGoal(%s/%s %q) returned a nil goal with a nil error", scope, period, text)
	}
	return g
}

func contractGoalTexts(gs []Goal) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Text)
	}
	return out
}

// ---------------------------------------------------------------------------
// GoalPeriods
// ---------------------------------------------------------------------------

// TestContractGoalPeriodsDayIsTheLocalCalendarDate pins the `day` half
// literally: it is now's LOCAL 2006-01-02, not UTC's and not a week-derived
// value. Everything downstream (the API's current-period GET, goalContext's
// "today" list) keys on this string, so a UTC slip would silently show
// yesterday's goals for several hours a day.
func TestContractGoalPeriodsDayIsTheLocalCalendarDate(t *testing.T) {
	for _, now := range []time.Time{
		time.Date(2026, 8, 13, 0, 0, 0, 0, time.Local),
		time.Date(2026, 8, 13, 23, 59, 59, 0, time.Local),
		time.Date(2026, 1, 1, 12, 0, 0, 0, time.Local),
	} {
		_, day := GoalPeriods(now)
		if want := now.Format("2006-01-02"); day != want {
			t.Errorf("GoalPeriods(%s) day = %q, want the local calendar date %q", now, day, want)
		}
	}
}

// TestContractGoalPeriodsWeekIsTheLocalMonday walks a full fortnight, one day
// at a time, and holds GoalPeriods' week against the contract's rule written
// out independently: a Monday maps to ITSELF, every other weekday (Sunday
// included) to the most recent PAST Monday. The Sunday arm is the one that
// separates this from Go's own ISO-ish habits — a Sunday-starts-the-week
// implementation returns the Monday six days AHEAD, which passes every
// mid-week case and fails exactly one day in seven.
func TestContractGoalPeriodsWeekIsTheLocalMonday(t *testing.T) {
	start := time.Date(2026, 8, 1, 9, 30, 0, 0, time.Local)
	for i := 0; i < 14; i++ {
		now := start.AddDate(0, 0, i)
		week, _ := GoalPeriods(now)
		want := contractGoalExpectedMonday(now).Format("2006-01-02")
		if week != want {
			t.Errorf("GoalPeriods(%s, a %s) week = %q, want %q", now.Format("2006-01-02"), now.Weekday(), week, want)
			continue
		}
		parsed, err := time.ParseInLocation("2006-01-02", week, time.Local)
		if err != nil {
			t.Errorf("week %q does not parse as 2006-01-02: %v", week, err)
			continue
		}
		if parsed.Weekday() != time.Monday {
			t.Errorf("week %q for %s is a %s, want Monday", week, now.Weekday(), parsed.Weekday())
		}
	}
}

// TestContractGoalPeriodsMondayMapsToItself states the fixed point on its own,
// because it is the half a "walk back to the previous Monday" loop written
// with a do-while gets wrong: a Monday becomes the Monday SEVEN DAYS EARLIER,
// which is a whole week of goals that stop being current on Mondays.
func TestContractGoalPeriodsMondayMapsToItself(t *testing.T) {
	monday := contractGoalExpectedMonday(time.Date(2026, 8, 13, 14, 0, 0, 0, time.Local))
	if monday.Weekday() != time.Monday {
		t.Fatalf("fixture is a %s, not a Monday", monday.Weekday())
	}
	week, day := GoalPeriods(monday)
	if want := monday.Format("2006-01-02"); week != want {
		t.Errorf("GoalPeriods(a Monday) week = %q, want the same day %q", week, want)
	}
	if week != day {
		t.Errorf("on a Monday week (%q) and day (%q) must be the same date", week, day)
	}
}

// TestContractGoalPeriodsSundayMapsSixDaysBack is the Sunday arm as
// arithmetic rather than as a weekday name: the most recent past Monday is
// exactly six days behind a Sunday.
func TestContractGoalPeriodsSundayMapsSixDaysBack(t *testing.T) {
	sunday := contractGoalExpectedMonday(time.Date(2026, 8, 13, 0, 0, 0, 0, time.Local)).AddDate(0, 0, 6)
	if sunday.Weekday() != time.Sunday {
		t.Fatalf("fixture is a %s, not a Sunday", sunday.Weekday())
	}
	week, _ := GoalPeriods(sunday)
	if want := sunday.AddDate(0, 0, -6).Format("2006-01-02"); week != want {
		t.Errorf("GoalPeriods(a Sunday) week = %q, want %q (six days back, not the Monday ahead)", week, want)
	}
}

// ---------------------------------------------------------------------------
// validators — the accepting arm
// ---------------------------------------------------------------------------

// TestContractGoalValidatorsAcceptTheLegalShapes pins what the validators must
// let THROUGH. Their refusals are athena-edge's; a validator that refused
// everything would pass an edge suite alone and break the whole feature.
func TestContractGoalValidatorsAcceptTheLegalShapes(t *testing.T) {
	for _, scope := range []string{"week", "day"} {
		if err := validateGoalScope(scope); err != nil {
			t.Errorf("validateGoalScope(%q) = %v, want nil", scope, err)
		}
	}
	week, day := contractGoalPeriods(t)
	if err := validateGoalPeriod("week", week); err != nil {
		t.Errorf("validateGoalPeriod(\"week\", %q) = %v, want nil — that is this week's Monday", week, err)
	}
	if err := validateGoalPeriod("day", day); err != nil {
		t.Errorf("validateGoalPeriod(\"day\", %q) = %v, want nil", day, err)
	}
	// A day period is NOT constrained to Mondays — only week periods are.
	if err := validateGoalPeriod("day", "2026-08-15"); err != nil {
		t.Errorf("validateGoalPeriod(\"day\", a Saturday) = %v, want nil", err)
	}
}

// ---------------------------------------------------------------------------
// AddGoal
// ---------------------------------------------------------------------------

// TestContractAddGoalReturnsTheCreatedRow pins AddGoal's return contract: the
// created row INCLUDING its assigned id, with both timestamps stamped from the
// caller's clock. The API's add op has no follow-up read, so a nil-or-partial
// return is a goal the client cannot address until the next poll.
func TestContractAddGoalReturnsTheCreatedRow(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)

	g := contractGoalAdd(t, s, "week", week, "ship goal tracking", 0)
	if g.ID == 0 {
		t.Error("AddGoal returned a row with no id; the caller cannot address the goal it just created")
	}
	if g.Scope != "week" || g.Period != week || g.Text != "ship goal tracking" {
		t.Errorf("returned row = %+v, want the posted scope/period/text", *g)
	}
	if g.Done {
		t.Error("a freshly added goal is not done")
	}
	if g.ParentID != 0 {
		t.Errorf("ParentID = %d, want 0 for a goal added with no parent", g.ParentID)
	}
	if g.CreatedAt != contractGoalNow || g.UpdatedAt != contractGoalNow {
		t.Errorf("CreatedAt/UpdatedAt = %d/%d, want both = now (%d)", g.CreatedAt, g.UpdatedAt, contractGoalNow)
	}

	// and the row that comes back out of the store is the same row
	got, err := s.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	if len(got) != 1 || got[0].ID != g.ID || got[0].Text != g.Text {
		t.Fatalf("ListGoals = %+v, want exactly the row AddGoal returned (%+v)", got, *g)
	}
}

// TestContractAddGoalTrimsText pins the trim as an observable: "  ship it \n"
// and "ship it" are the same goal text, and the trimmed form is what is
// STORED — not merely what is validated.
func TestContractAddGoalTrimsText(t *testing.T) {
	s := openTest(t)
	_, day := contractGoalPeriods(t)

	g := contractGoalAdd(t, s, "day", day, "  \t ship it \n ", 0)
	if g.Text != "ship it" {
		t.Errorf("returned Text = %q, want the trimmed %q", g.Text, "ship it")
	}
	got, err := s.ListGoals("day", day)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", got, err)
	}
	if got[0].Text != "ship it" {
		t.Errorf("stored Text = %q, want the trimmed %q — the trim must reach the row, not just the check", got[0].Text, "ship it")
	}
}

// TestContractAddGoalAssignsPositionsInInsertionOrder is the ordering contract
// at its source: position is "the next position within (scope, period)", and
// insertion order is what listing preserves. Three goals added in one order
// must read back in that order, with strictly increasing positions.
func TestContractAddGoalAssignsPositionsInInsertionOrder(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)

	first := contractGoalAdd(t, s, "week", week, "first", 0)
	second := contractGoalAdd(t, s, "week", week, "second", 0)
	third := contractGoalAdd(t, s, "week", week, "third", 0)

	if !(first.Position < second.Position && second.Position < third.Position) {
		t.Errorf("positions %d/%d/%d are not strictly increasing with insertion order",
			first.Position, second.Position, third.Position)
	}
	got, err := s.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	want := []string{"first", "second", "third"}
	names := contractGoalTexts(got)
	if len(names) != len(want) {
		t.Fatalf("ListGoals = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("ListGoals order = %v, want insertion order %v", names, want)
		}
	}
}

// TestContractAddGoalPositionsAreScopedToScopeAndPeriod: "next position WITHIN
// (scope, period)". A single global counter would still list correctly today
// and would make the two lanes' positions drift apart forever, which the panel
// renders as an unexplained gap the moment anything reorders on position.
func TestContractAddGoalPositionsAreScopedToScopeAndPeriod(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)

	weekFirst := contractGoalAdd(t, s, "week", week, "week one", 0)
	dayFirst := contractGoalAdd(t, s, "day", day, "day one", 0)
	if weekFirst.Position != dayFirst.Position {
		t.Errorf("first week goal position %d != first day goal position %d — the counter is not per (scope, period)",
			weekFirst.Position, dayFirst.Position)
	}

	otherDay := "2026-01-05" // a Monday, legal for either scope
	otherFirst := contractGoalAdd(t, s, "day", otherDay, "other day one", 0)
	if otherFirst.Position != dayFirst.Position {
		t.Errorf("first goal of a fresh period got position %d, want %d — position restarts per period",
			otherFirst.Position, dayFirst.Position)
	}
}

// TestContractAddGoalDayGoalUnderAWeekParent is the nesting happy path: a DAY
// goal may carry a parent_id, and only when it points at an existing
// WEEK-scope row. The refusals around it are athena-edge's; what is pinned
// here is that the legal combination is accepted and PERSISTED.
func TestContractAddGoalDayGoalUnderAWeekParent(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)

	parent := contractGoalAdd(t, s, "week", week, "ship the panel", 0)
	child := contractGoalAdd(t, s, "day", day, "wire the add input", parent.ID)
	if child.ParentID != parent.ID {
		t.Errorf("returned ParentID = %d, want the week goal's id %d", child.ParentID, parent.ID)
	}
	got, err := s.ListGoals("day", day)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", got, err)
	}
	if got[0].ParentID != parent.ID {
		t.Errorf("stored ParentID = %d, want %d", got[0].ParentID, parent.ID)
	}
}

// ---------------------------------------------------------------------------
// ListGoals
// ---------------------------------------------------------------------------

// TestContractListGoalsEmptyPeriodIsEmptyNotAnError pins the answer every
// caller depends on before carl has typed a single goal: no rows is an empty
// result with a NIL error. The API serves `[]` off the back of this, and
// goalContext returns "" off the back of it; an error here would turn a fresh
// install into a 500 and a spawn-path degradation.
func TestContractListGoalsEmptyPeriodIsEmptyNotAnError(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)

	for _, c := range []struct{ scope, period string }{{"week", week}, {"day", day}} {
		got, err := s.ListGoals(c.scope, c.period)
		if err != nil {
			t.Errorf("ListGoals(%s, %s) errored on an empty period: %v", c.scope, c.period, err)
		}
		if len(got) != 0 {
			t.Errorf("ListGoals(%s, %s) = %+v, want empty", c.scope, c.period, got)
		}
	}
}

// TestContractListGoalsIsScopedToItsScopeAndPeriod: the listing is the panel's
// only filter, so a row from the other lane or another week leaking in is a
// goal carl sees under the wrong heading.
func TestContractListGoalsIsScopedToItsScopeAndPeriod(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)

	contractGoalAdd(t, s, "week", week, "this week", 0)
	contractGoalAdd(t, s, "day", day, "today", 0)
	contractGoalAdd(t, s, "week", "2026-01-05", "an old week", 0)
	contractGoalAdd(t, s, "day", "2026-01-07", "an old day", 0)

	gotWeek, err := s.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals week: %v", err)
	}
	if names := contractGoalTexts(gotWeek); len(names) != 1 || names[0] != "this week" {
		t.Errorf("ListGoals(week, current) = %v, want exactly [this week]", names)
	}
	gotDay, err := s.ListGoals("day", day)
	if err != nil {
		t.Fatalf("ListGoals day: %v", err)
	}
	if names := contractGoalTexts(gotDay); len(names) != 1 || names[0] != "today" {
		t.Errorf("ListGoals(day, current) = %v, want exactly [today]", names)
	}
}

// TestContractListGoalsOrdersByPositionThenID pins the SECOND ordering key.
// AddGoal never produces a tie, so the tie-break is only reachable by writing
// equal positions directly — which is exactly the state a future reorder verb
// (or a hand-edited row) produces, and the state where an unstable order makes
// the panel shuffle between two polls with nothing having changed.
func TestContractListGoalsOrdersByPositionThenID(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)

	for _, text := range []string{"alpha", "bravo", "charlie"} {
		if _, err := s.db.Exec(
			`INSERT INTO goals (scope, period, text, done, position, created_at, updated_at)
				VALUES ('week', ?, ?, 0, 5, ?, ?)`,
			week, text, contractGoalNow, contractGoalNow,
		); err != nil {
			t.Fatalf("seed %q at an equal position: %v", text, err)
		}
	}
	got, err := s.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListGoals = %+v, want 3 rows", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Fatalf("rows sharing a position came back as %v (ids %d,%d,%d), want ascending id",
				contractGoalTexts(got), got[0].ID, got[1].ID, got[2].ID)
		}
	}
}

// ---------------------------------------------------------------------------
// GetGoal (contract amendment 2026-08-13)
// ---------------------------------------------------------------------------

// TestContractGetGoalReturnsTheWholeRowByID pins the by-id read field for
// field. This is the ONLY frozen reader that can reach a goal outside the
// current periods, which is exactly what the two callers that forced the
// amendment need: the API's `?slug=` job_goal (served "any period, done or
// not") and goalContext's linked-goal marker. A GetGoal that quietly filtered
// by period, or by done, would make both of those unimplementable while every
// current-period test in this file still passed.
func TestContractGetGoalReturnsTheWholeRowByID(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)

	parent := contractGoalAdd(t, s, "week", week, "the weekly", 0)
	child := contractGoalAdd(t, s, "day", day, "the nested daily", parent.ID)

	got, err := s.GetGoal(child.ID)
	if err != nil {
		t.Fatalf("GetGoal(%d): %v", child.ID, err)
	}
	if got == nil {
		t.Fatal("GetGoal returned a nil goal with a nil error")
	}
	if got.ID != child.ID || got.Scope != "day" || got.Period != day || got.Text != "the nested daily" {
		t.Errorf("GetGoal = %+v, want the row AddGoal created (%+v)", *got, *child)
	}
	if got.ParentID != parent.ID {
		t.Errorf("ParentID = %d, want %d", got.ParentID, parent.ID)
	}
	if got.Done {
		t.Error("Done = true for a goal that was never marked done")
	}
	if got.Position != child.Position {
		t.Errorf("Position = %d, want %d", got.Position, child.Position)
	}
	if got.CreatedAt != contractGoalNow || got.UpdatedAt != contractGoalNow {
		t.Errorf("CreatedAt/UpdatedAt = %d/%d, want both %d", got.CreatedAt, got.UpdatedAt, contractGoalNow)
	}
}

// TestContractGetGoalReadsAcrossPeriodsAndDoneState is the amendment's own
// reason, stated as an assertion: a DONE goal from a PAST period — the exact
// row a long-running job stays linked to — must still come back.
func TestContractGetGoalReadsAcrossPeriodsAndDoneState(t *testing.T) {
	s := openTest(t)
	stale := contractGoalAdd(t, s, "week", "2026-01-05", "an old, finished goal", 0)
	done := true
	if err := s.UpdateGoal(stale.ID, GoalUpdate{Done: &done}, contractGoalLater); err != nil {
		t.Fatalf("mark done: %v", err)
	}

	// it is genuinely outside every current listing
	week, _ := contractGoalPeriods(t)
	if rows, err := s.ListGoals("week", week); err != nil || len(rows) != 0 {
		t.Fatalf("fixture leaked into the current week (rows %+v, err %v)", rows, err)
	}

	got, err := s.GetGoal(stale.ID)
	if err != nil {
		t.Fatalf("GetGoal on a done, past-period goal: %v", err)
	}
	if got.Text != "an old, finished goal" || !got.Done || got.Period != "2026-01-05" {
		t.Errorf("GetGoal = %+v, want the done past-period row unfiltered", *got)
	}
}

// TestContractGetGoalReflectsLaterWrites: it is a live read, not a snapshot —
// the API's job_goal is served from it after arbitrary edits.
func TestContractGetGoalReflectsLaterWrites(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "before", 0)

	text, done := "after", true
	if err := s.UpdateGoal(g.ID, GoalUpdate{Text: &text, Done: &done}, contractGoalLater); err != nil {
		t.Fatalf("UpdateGoal: %v", err)
	}
	got, err := s.GetGoal(g.ID)
	if err != nil {
		t.Fatalf("GetGoal: %v", err)
	}
	if got.Text != "after" || !got.Done || got.UpdatedAt != contractGoalLater {
		t.Errorf("GetGoal = %+v, want the updated text/done/updated_at", *got)
	}
}

// TestContractGetGoalAbsentIDIsErrNotFound is the other half of the amended
// row's two-clause obligation. It is a SENTINEL identity, not just "an error":
// the API's job_goal path has to tell "this job links a goal that has since
// been deleted" (omit the key) apart from "the read failed" (a 500).
func TestContractGetGoalAbsentIDIsErrNotFound(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "present", 0)

	_, err := s.GetGoal(g.ID + 9999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("GetGoal on an absent id = %v, want ErrNotFound", err)
	}
	if err := s.DeleteGoal(g.ID); err != nil {
		t.Fatalf("DeleteGoal: %v", err)
	}
	if _, err := s.GetGoal(g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetGoal after the row was deleted = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// UpdateGoal
// ---------------------------------------------------------------------------

// TestContractUpdateGoalNilFieldsAreUnchanged is the whole of GoalUpdate's
// pointer contract: the panel's done-toggle sends ONLY done, and the
// click-to-edit sends ONLY text. A non-pointer struct written straight into
// the row would blank the other field on every toggle.
func TestContractUpdateGoalNilFieldsAreUnchanged(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)
	parent := contractGoalAdd(t, s, "week", week, "parent", 0)
	g := contractGoalAdd(t, s, "day", day, "original text", parent.ID)

	done := true
	if err := s.UpdateGoal(g.ID, GoalUpdate{Done: &done}, contractGoalLater); err != nil {
		t.Fatalf("UpdateGoal(done only): %v", err)
	}
	got, err := s.ListGoals("day", day)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", got, err)
	}
	if !got[0].Done {
		t.Error("Done was not set")
	}
	if got[0].Text != "original text" {
		t.Errorf("Text = %q after a done-only update, want it unchanged", got[0].Text)
	}
	if got[0].ParentID != parent.ID {
		t.Errorf("ParentID = %d after a done-only update, want it unchanged (%d)", got[0].ParentID, parent.ID)
	}

	text := "  edited text  "
	if err := s.UpdateGoal(g.ID, GoalUpdate{Text: &text}, contractGoalLater); err != nil {
		t.Fatalf("UpdateGoal(text only): %v", err)
	}
	got, err = s.ListGoals("day", day)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", got, err)
	}
	if got[0].Text != "edited text" {
		t.Errorf("Text = %q, want the trimmed %q", got[0].Text, "edited text")
	}
	if !got[0].Done {
		t.Error("Done was cleared by a text-only update; a nil field is unchanged")
	}
}

// TestContractUpdateGoalStampsUpdatedAtAndLeavesCreatedAt: "any successful
// update sets UpdatedAt = now". created_at is the row's birth and must not
// move — it is the only thing that can order two goals added in the same
// position lane if positions are ever rewritten.
func TestContractUpdateGoalStampsUpdatedAtAndLeavesCreatedAt(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "stamped", 0)

	text := "restamped"
	if err := s.UpdateGoal(g.ID, GoalUpdate{Text: &text}, contractGoalLater); err != nil {
		t.Fatalf("UpdateGoal: %v", err)
	}
	got, err := s.ListGoals("week", week)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", got, err)
	}
	if got[0].UpdatedAt != contractGoalLater {
		t.Errorf("UpdatedAt = %d, want the update's now (%d)", got[0].UpdatedAt, contractGoalLater)
	}
	if got[0].CreatedAt != contractGoalNow {
		t.Errorf("CreatedAt = %d, want the ADD's now (%d) — an update never re-births the row", got[0].CreatedAt, contractGoalNow)
	}
}

// TestContractUpdateGoalSetsAndClearsTheParent covers GoalUpdate.ParentID's
// two legal moves: point a day goal at a week goal, and — the one the plain
// pointer type makes possible at all — point it at 0 to CLEAR the parent. A
// nil-means-clear design would have made "unassign this day goal" unreachable.
func TestContractUpdateGoalSetsAndClearsTheParent(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)
	parent := contractGoalAdd(t, s, "week", week, "the weekly", 0)
	g := contractGoalAdd(t, s, "day", day, "unassigned at first", 0)

	set := parent.ID
	if err := s.UpdateGoal(g.ID, GoalUpdate{ParentID: &set}, contractGoalLater); err != nil {
		t.Fatalf("UpdateGoal(set parent): %v", err)
	}
	got, err := s.ListGoals("day", day)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", got, err)
	}
	if got[0].ParentID != parent.ID {
		t.Fatalf("ParentID = %d after setting it, want %d", got[0].ParentID, parent.ID)
	}

	clear := int64(0)
	if err := s.UpdateGoal(g.ID, GoalUpdate{ParentID: &clear}, contractGoalLater); err != nil {
		t.Fatalf("UpdateGoal(clear parent): %v", err)
	}
	got, err = s.ListGoals("day", day)
	if err != nil || len(got) != 1 {
		t.Fatalf("ListGoals = %+v, err %v", got, err)
	}
	if got[0].ParentID != 0 {
		t.Errorf("ParentID = %d after a ptr-to-0 update, want 0 (cleared)", got[0].ParentID)
	}
}

// ---------------------------------------------------------------------------
// DeleteGoal
// ---------------------------------------------------------------------------

// TestContractDeleteGoalAbsentIDIsANoOp: "absent id is a no-op, not an error"
// — same contract DeleteQuestion has, and the reason the API's remove op still
// answers 200 for an id a stale panel is retrying.
func TestContractDeleteGoalAbsentIDIsANoOp(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "present", 0)

	if err := s.DeleteGoal(g.ID + 9999); err != nil {
		t.Errorf("DeleteGoal on an absent id = %v, want nil (no-op)", err)
	}
	if err := s.DeleteGoal(g.ID); err != nil {
		t.Fatalf("DeleteGoal on a present id: %v", err)
	}
	// deleting the same id twice is the same no-op
	if err := s.DeleteGoal(g.ID); err != nil {
		t.Errorf("second DeleteGoal of the same id = %v, want nil (no-op)", err)
	}
	got, err := s.ListGoals("week", week)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("ListGoals = %+v after the delete, want empty", got)
	}
}

// TestContractDeleteGoalLeavesChildrenWithTheParentCleared is carl's nesting
// decision as a data guarantee: a weekly goal's day children are NEVER
// cascade-deleted. Deleting the week goal drops them to unassigned — the row
// survives with ParentID 0 — because a day goal is a real commitment carl
// wrote down, not a decoration on the weekly one.
func TestContractDeleteGoalLeavesChildrenWithTheParentCleared(t *testing.T) {
	s := openTest(t)
	week, day := contractGoalPeriods(t)
	parent := contractGoalAdd(t, s, "week", week, "the weekly", 0)
	childA := contractGoalAdd(t, s, "day", day, "child a", parent.ID)
	childB := contractGoalAdd(t, s, "day", day, "child b", parent.ID)
	loose := contractGoalAdd(t, s, "day", day, "already loose", 0)

	if err := s.DeleteGoal(parent.ID); err != nil {
		t.Fatalf("DeleteGoal(parent): %v", err)
	}
	got, err := s.ListGoals("day", day)
	if err != nil {
		t.Fatalf("ListGoals: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("ListGoals = %v, want all 3 day goals to survive the parent's delete", contractGoalTexts(got))
	}
	byID := map[int64]Goal{}
	for _, g := range got {
		byID[g.ID] = g
	}
	for _, id := range []int64{childA.ID, childB.ID, loose.ID} {
		g, ok := byID[id]
		if !ok {
			t.Fatalf("day goal %d was cascade-deleted with its parent", id)
		}
		if g.ParentID != 0 {
			t.Errorf("day goal %q still points at the deleted parent (ParentID %d), want 0", g.Text, g.ParentID)
		}
	}
	if left, err := s.ListGoals("week", week); err != nil || len(left) != 0 {
		t.Errorf("ListGoals(week) = %+v (err %v), want the deleted week goal gone", left, err)
	}
}

// ---------------------------------------------------------------------------
// SetJobGoal + Job.GoalID
// ---------------------------------------------------------------------------

// contractGoalJob creates a job whose rev and updated_at are DELIBERATELY not
// "now": SetJobGoal's whole obligation is that it moves neither, and a row
// stamped at the current millisecond cannot tell a preserved updated_at from a
// freshly rewritten one.
func contractGoalJob(t *testing.T, s *Store, slug string) *Job {
	t.Helper()
	j := Job{Slug: slug, Repo: "kraken", State: "working", Rev: 5, CreatedAt: 1_600_000_000_000, UpdatedAt: 1_600_000_000_000}
	id, err := s.CreateJob(&j)
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", id, err)
	}
	return got
}

// TestContractSetJobGoalLinksWithoutTouchingRevOrUpdatedAt is the colour-route
// property restated for goals, and the reason this write does not go through
// UpdateJobFields: rev is the editor's optimistic lock (a bump 409s an open
// form over a field the form never showed), and updated_at is what the board
// renders as a card's AGE (a bump resets how long the job has been waiting).
func TestContractSetJobGoalLinksWithoutTouchingRevOrUpdatedAt(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "the linked goal", 0)
	before := contractGoalJob(t, s, "linked-job")

	if err := s.SetJobGoal(before.ID, g.ID); err != nil {
		t.Fatalf("SetJobGoal: %v", err)
	}
	after, err := s.GetJobByID(before.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if after.GoalID != g.ID {
		t.Errorf("GoalID = %d after the link, want %d", after.GoalID, g.ID)
	}
	if after.Rev != before.Rev {
		t.Errorf("rev moved %d → %d; a goal link is outside the rev gate", before.Rev, after.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("updated_at moved %d → %d; a goal link must not reset the card's age", before.UpdatedAt, after.UpdatedAt)
	}
}

// TestContractSetJobGoalZeroClearsTheLink pins the unlink verb: 0 is not "no
// change", it is the editor picker's `—` row.
func TestContractSetJobGoalZeroClearsTheLink(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "linked then not", 0)
	j := contractGoalJob(t, s, "unlink-job")

	if err := s.SetJobGoal(j.ID, g.ID); err != nil {
		t.Fatalf("SetJobGoal(link): %v", err)
	}
	if err := s.SetJobGoal(j.ID, 0); err != nil {
		t.Fatalf("SetJobGoal(clear): %v", err)
	}
	after, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if after.GoalID != 0 {
		t.Errorf("GoalID = %d after clearing, want 0", after.GoalID)
	}
	if after.Rev != j.Rev || after.UpdatedAt != j.UpdatedAt {
		t.Errorf("clearing moved rev/updated_at (%d/%d → %d/%d); the clear is the same write as the set",
			j.Rev, j.UpdatedAt, after.Rev, after.UpdatedAt)
	}
}

// TestContractJobGoalIDIsPopulatedByEveryReadPath: "populated (0 when NULL) by
// EVERY job read path". A GoalID that only appears on GetJobByID would show a
// chip on the editor and none on the board, which is exactly the class of bug
// jobColumns exists as one shared string to prevent.
func TestContractJobGoalIDIsPopulatedByEveryReadPath(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "read-path goal", 0)
	linked := contractGoalJob(t, s, "read-path-linked")
	unlinked := contractGoalJob(t, s, "read-path-unlinked")
	if err := s.SetJobGoal(linked.ID, g.ID); err != nil {
		t.Fatalf("SetJobGoal: %v", err)
	}

	bySlug, err := s.GetJobBySlug("read-path-linked")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if bySlug.GoalID != g.ID {
		t.Errorf("GetJobBySlug GoalID = %d, want %d", bySlug.GoalID, g.ID)
	}
	byID, err := s.GetJobByID(linked.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if byID.GoalID != g.ID {
		t.Errorf("GetJobByID GoalID = %d, want %d", byID.GoalID, g.ID)
	}

	all, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	seen := map[string]int64{}
	for _, j := range all {
		seen[j.Slug] = j.GoalID
	}
	if seen["read-path-linked"] != g.ID {
		t.Errorf("ListJobs GoalID for the linked job = %d, want %d", seen["read-path-linked"], g.ID)
	}
	if seen["read-path-unlinked"] != 0 {
		t.Errorf("ListJobs GoalID for the unlinked job = %d, want 0 (NULL reads as 0)", seen["read-path-unlinked"])
	}

	rows, _, err := s.LoadJobsWithChildren()
	if err != nil {
		t.Fatalf("LoadJobsWithChildren: %v", err)
	}
	found := false
	for _, j := range rows {
		if j.Slug == "read-path-linked" {
			found = true
			if j.GoalID != g.ID {
				t.Errorf("LoadJobsWithChildren GoalID = %d, want %d", j.GoalID, g.ID)
			}
		}
		if j.Slug == "read-path-unlinked" && j.GoalID != 0 {
			t.Errorf("LoadJobsWithChildren GoalID for the unlinked job = %d, want 0", j.GoalID)
		}
	}
	if !found {
		t.Fatal("LoadJobsWithChildren did not return the linked job at all")
	}
	_ = unlinked
}

// TestContractDeletingALinkedGoalLeavesTheJobReadable is the ON DELETE SET
// NULL guarantee from the job's side: a deleted goal degrades the job to "no
// goal", never to a read error and never to a dangling id the chip would try
// to render.
func TestContractDeletingALinkedGoalLeavesTheJobReadable(t *testing.T) {
	s := openTest(t)
	week, _ := contractGoalPeriods(t)
	g := contractGoalAdd(t, s, "week", week, "doomed goal", 0)
	j := contractGoalJob(t, s, "orphaned-link")
	if err := s.SetJobGoal(j.ID, g.ID); err != nil {
		t.Fatalf("SetJobGoal: %v", err)
	}

	if err := s.DeleteGoal(g.ID); err != nil {
		t.Fatalf("DeleteGoal: %v", err)
	}
	after, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID after deleting the linked goal: %v", err)
	}
	if after.GoalID != 0 {
		t.Errorf("GoalID = %d after the goal was deleted, want 0", after.GoalID)
	}
	if after.Slug != "orphaned-link" {
		t.Errorf("job scanned as %+v; a deleted goal must not disturb the rest of the row", *after)
	}
}

// TestContractGoalIDIsNotAJobFieldColumn pins the form-save-cannot-wipe-it
// property at its source. The editor's saveform is a FULL REPLACE over
// jobFieldColumns; goal_id being in that map would mean any client predating
// the picker silently unlinks the goal on the next save.
func TestContractGoalIDIsNotAJobFieldColumn(t *testing.T) {
	if jobFieldColumns["goal_id"] {
		t.Error("goal_id is in jobFieldColumns — a full form save could then wipe the link")
	}
	s := openTest(t)
	j := contractGoalJob(t, s, "fields-guard")
	if _, err := s.UpdateJobFields(j.ID, j.Rev, map[string]any{"goal_id": int64(1)}); err == nil {
		t.Error("UpdateJobFields accepted goal_id; the allowlist must refuse it")
	}
}
