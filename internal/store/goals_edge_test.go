package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// goals_edge_test.go — the EDGE dimension of 50-contract.md's Slice S1 store
// obligations: every refusal and every boundary the goals row family states,
// written blind from the contract text with no sight of internal/store/goals.go.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy paths — ordering by position, insertion order, the "deleting a week
// goal leaves its day children with a cleared parent" rule. What lives here is
// the other half: what the store must REFUSE, and the two calendar edges
// GoalPeriods has to get right (a Monday input, a Sunday input) because every
// week goal in the database is keyed by that answer.
//
// Package-level identifiers are prefixed `edgeGoal` — this package's test
// namespace is shared with a second blind author writing in the same round,
// and the bare `edge` prefix is already taken elsewhere in the tree.

// edgeGoalNow is a fixed unix-millis stamp for writes whose timestamp is not
// what the case is about. A constant rather than time.Now() so a failure
// message never depends on when the suite ran.
const edgeGoalNow int64 = 1_760_000_000_000

// The calendar fixtures. 2026-08-13 is a Thursday (the base commit's own merge
// date), which pins the whole family: 08-10 is that week's Monday, 08-16 its
// Sunday, and 08-03 the Monday before it.
const (
	edgeGoalMonday     = "2026-08-10"
	edgeGoalWednesday  = "2026-08-12"
	edgeGoalPastMonday = "2026-08-03"
)

// edgeGoalCurrent returns the periods a write must use to land in "now", so a
// case about something OTHER than the calendar never accidentally trips the
// calendar rules.
func edgeGoalCurrent() (week, day string) { return GoalPeriods(time.Now()) }

// ---------------------------------------------------------------------------
// GoalPeriods: the Monday/Sunday boundary
// ---------------------------------------------------------------------------

// TestEdgeGoalPeriodsWeekBoundaries pins the two ends of the mapping the
// contract states: a Monday maps to ITSELF (not to the Monday before it), and
// every other weekday — Sunday explicitly included, which is where an
// ISO-vs-US week convention silently disagrees — maps to the most recent PAST
// Monday. Sunday is the one that matters: under a Sunday-starts-the-week
// convention it would map forward to the NEXT day, putting a Sunday evening's
// goals in a different week from Saturday's.
func TestEdgeGoalPeriodsWeekBoundaries(t *testing.T) {
	for _, c := range []struct {
		name     string
		at       time.Time
		wantWeek string
		wantDay  string
	}{
		{"monday midnight maps to itself",
			time.Date(2026, 8, 10, 0, 0, 0, 0, time.Local), edgeGoalMonday, edgeGoalMonday},
		{"monday late maps to itself",
			time.Date(2026, 8, 10, 23, 59, 59, 0, time.Local), edgeGoalMonday, edgeGoalMonday},
		{"thursday maps back to monday",
			time.Date(2026, 8, 13, 12, 0, 0, 0, time.Local), edgeGoalMonday, "2026-08-13"},
		{"sunday maps BACK to the monday of the week just lived, not forward",
			time.Date(2026, 8, 16, 12, 0, 0, 0, time.Local), edgeGoalMonday, "2026-08-16"},
		{"the sunday before a monday belongs to the PREVIOUS week",
			time.Date(2026, 8, 9, 23, 59, 59, 0, time.Local), edgeGoalPastMonday, "2026-08-09"},
	} {
		t.Run(c.name, func(t *testing.T) {
			week, day := GoalPeriods(c.at)
			if week != c.wantWeek {
				t.Errorf("GoalPeriods(%s) week = %q, want %q", c.at.Format(time.RFC3339), week, c.wantWeek)
			}
			if day != c.wantDay {
				t.Errorf("GoalPeriods(%s) day = %q, want %q", c.at.Format(time.RFC3339), day, c.wantDay)
			}
		})
	}
}

// TestEdgeGoalPeriodsWeekIsAlwaysAMonday closes the loop between the two
// validators and the generator: every week string GoalPeriods can produce must
// be one validateGoalPeriod accepts for scope "week". A generator that emitted
// a Sunday would make the API's own default period unwritable.
func TestEdgeGoalPeriodsWeekIsAlwaysAMonday(t *testing.T) {
	base := time.Date(2026, 8, 10, 9, 0, 0, 0, time.Local)
	for i := 0; i < 14; i++ {
		at := base.AddDate(0, 0, i)
		week, _ := GoalPeriods(at)
		if err := validateGoalPeriod("week", week); err != nil {
			t.Errorf("GoalPeriods(%s) produced week %q that validateGoalPeriod refuses: %v",
				at.Format("Mon 2006-01-02"), week, err)
		}
	}
}

// ---------------------------------------------------------------------------
// the validators
// ---------------------------------------------------------------------------

func TestEdgeGoalValidateScopeRefusesAnythingButWeekAndDay(t *testing.T) {
	for _, ok := range []string{"week", "day"} {
		if err := validateGoalScope(ok); err != nil {
			t.Errorf("validateGoalScope(%q) = %v, want nil", ok, err)
		}
	}
	// Case matters, plurals are not aliases, and "" is the shape an absent
	// JSON key decodes to — the most likely bad value to actually arrive.
	for _, bad := range []string{"", " ", "Week", "WEEK", "days", "Day", "month", "today", "week ", "weekly"} {
		err := validateGoalScope(bad)
		if err == nil {
			t.Errorf("validateGoalScope(%q) = nil, want a refusal", bad)
			continue
		}
		edgeGoalAssertDescriptive(t, "validateGoalScope("+bad+")", err)
	}
}

func TestEdgeGoalValidatePeriodRefusesMalformedDates(t *testing.T) {
	// A day period only has to parse as 2006-01-02.
	if err := validateGoalPeriod("day", "2026-08-13"); err != nil {
		t.Errorf("validateGoalPeriod(day, 2026-08-13) = %v, want nil", err)
	}
	// Any weekday is a legal DAY period, including a Sunday.
	if err := validateGoalPeriod("day", "2026-08-16"); err != nil {
		t.Errorf("validateGoalPeriod(day, 2026-08-16) = %v, want nil — the Monday rule is week-only", err)
	}
	for _, bad := range []string{
		"",                     // absent key
		"   ",                  // whitespace only
		"2026-8-13",            // unpadded — not the stated layout
		"08-13-2026",           // US order
		"2026/08/13",           // wrong separator
		"2026-13-01",           // month out of range
		"2026-02-30",           // day out of range for the month
		"2026-08-13T00:00:00Z", // an RFC3339 stamp, not a calendar date
		"2026-08-13 ",          // trailing space
		"tomorrow",
	} {
		for _, scope := range []string{"day", "week"} {
			err := validateGoalPeriod(scope, bad)
			if err == nil {
				t.Errorf("validateGoalPeriod(%s, %q) = nil, want a refusal", scope, bad)
				continue
			}
			edgeGoalAssertDescriptive(t, "validateGoalPeriod("+scope+", "+bad+")", err)
		}
	}
}

// TestEdgeGoalValidatePeriodRefusesNonMondayWeek is the rule that keeps one
// week from having seven spellings. Without it "this week" is whatever date
// the client happened to send, and two clients disagreeing produce two
// disjoint week lists that each look empty to the other.
func TestEdgeGoalValidatePeriodRefusesNonMondayWeek(t *testing.T) {
	if err := validateGoalPeriod("week", edgeGoalMonday); err != nil {
		t.Fatalf("validateGoalPeriod(week, %s) = %v, want nil — that date IS a Monday", edgeGoalMonday, err)
	}
	// Every other day of the same week, including both neighbours of the
	// Monday: the day before (Sunday) is the off-by-one a "round down to
	// Sunday" implementation produces, the day after (Tuesday) the one a
	// missing check lets through.
	for _, notMonday := range []string{
		"2026-08-09", // Sunday before
		"2026-08-11", // Tuesday
		edgeGoalWednesday,
		"2026-08-13", // Thursday
		"2026-08-14", // Friday
		"2026-08-15", // Saturday
		"2026-08-16", // Sunday after
	} {
		err := validateGoalPeriod("week", notMonday)
		if err == nil {
			t.Errorf("validateGoalPeriod(week, %q) = nil — a week period that is not a Monday must be refused", notMonday)
			continue
		}
		edgeGoalAssertDescriptive(t, "validateGoalPeriod(week, "+notMonday+")", err)
	}
}

// edgeGoalAssertDescriptive pins the contract's "errors are descriptive
// sentences (they become API 400 bodies verbatim)" clause at the only level a
// test can: a non-empty, single-line, human-readable message rather than a
// bare sentinel or an empty string a 400 body would render as {}.
func edgeGoalAssertDescriptive(t *testing.T, what string, err error) {
	t.Helper()
	msg := err.Error()
	if strings.TrimSpace(msg) == "" {
		t.Errorf("%s: error message is empty — it becomes a 400 body verbatim", what)
	}
	if strings.Contains(msg, "\n") {
		t.Errorf("%s: error message spans lines (%q) — a 400 body is one sentence", what, msg)
	}
}

// ---------------------------------------------------------------------------
// AddGoal: refusals
// ---------------------------------------------------------------------------

func TestEdgeGoalAddRefusesInvalidScopeAndPeriod(t *testing.T) {
	s := openTest(t)
	_, curDay := edgeGoalCurrent()

	for _, c := range []struct {
		name string
		in   GoalAdd
	}{
		{"unknown scope", GoalAdd{Scope: "month", Period: curDay, Text: "ship it"}},
		{"empty scope", GoalAdd{Scope: "", Period: curDay, Text: "ship it"}},
		{"capitalised scope", GoalAdd{Scope: "Week", Period: edgeGoalMonday, Text: "ship it"}},
		{"malformed period", GoalAdd{Scope: "day", Period: "13-08-2026", Text: "ship it"}},
		{"empty period", GoalAdd{Scope: "day", Period: "", Text: "ship it"}},
		{"week period that is not a monday", GoalAdd{Scope: "week", Period: edgeGoalWednesday, Text: "ship it"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			g, err := s.AddGoal(c.in, edgeGoalNow)
			if err == nil {
				t.Fatalf("AddGoal(%+v) = %+v, nil — want a refusal", c.in, g)
			}
			edgeGoalAssertDescriptive(t, "AddGoal "+c.name, err)
			if g != nil {
				t.Errorf("a refused AddGoal returned a row: %+v", g)
			}
		})
	}
	edgeGoalAssertNothingWritten(t, s)
}

// TestEdgeGoalAddRefusesEmptyTextAfterTrim covers both halves of the text rule:
// the trim happens (so a padded value is stored bare) and the emptiness check
// runs AFTER it (so a value that is only whitespace never becomes a blank row
// on the board).
func TestEdgeGoalAddRefusesEmptyTextAfterTrim(t *testing.T) {
	s := openTest(t)
	_, curDay := edgeGoalCurrent()

	for _, blank := range []string{"", " ", "   ", "\t", "\n", " \t\n ", "\r\n"} {
		g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: blank}, edgeGoalNow)
		if err == nil {
			t.Errorf("AddGoal with text %q = %+v, nil — want a refusal", blank, g)
			continue
		}
		edgeGoalAssertDescriptive(t, "AddGoal blank text", err)
	}
	edgeGoalAssertNothingWritten(t, s)

	// The positive half of the same rule: padding is stripped, not refused.
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "  finish the migration \t"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("AddGoal with padded text: %v", err)
	}
	if g.Text != "finish the migration" {
		t.Errorf("stored text = %q, want the trimmed %q", g.Text, "finish the migration")
	}
}

// TestEdgeGoalAddParentRules covers every way a parent_id can be wrong. The
// nesting is one level deep by construction — a week goal may not have one at
// all, and a day goal's parent must be a week goal that exists — so all three
// refusals here are what keeps the tree from becoming a graph.
func TestEdgeGoalAddParentRules(t *testing.T) {
	s := openTest(t)
	curWeek, curDay := edgeGoalCurrent()

	week, err := s.AddGoal(GoalAdd{Scope: "week", Period: curWeek, Text: "the week's theme"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed week goal: %v", err)
	}
	day, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "today's first"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed day goal: %v", err)
	}

	t.Run("a week goal may not carry a parent", func(t *testing.T) {
		g, err := s.AddGoal(GoalAdd{Scope: "week", Period: curWeek, Text: "nested week", ParentID: week.ID}, edgeGoalNow)
		if err == nil {
			t.Fatalf("AddGoal week-with-parent = %+v, nil — only day goals may nest", g)
		}
		edgeGoalAssertDescriptive(t, "AddGoal week-with-parent", err)
	})

	t.Run("a day goal may not nest under another day goal", func(t *testing.T) {
		g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "nested day", ParentID: day.ID}, edgeGoalNow)
		if err == nil {
			t.Fatalf("AddGoal day-under-day = %+v, nil — a parent must be week-scope", g)
		}
		edgeGoalAssertDescriptive(t, "AddGoal day-under-day", err)
	})

	t.Run("a parent that does not exist is refused", func(t *testing.T) {
		g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "orphan", ParentID: 987654}, edgeGoalNow)
		if err == nil {
			t.Fatalf("AddGoal with a missing parent = %+v, nil", g)
		}
		edgeGoalAssertDescriptive(t, "AddGoal missing parent", err)
	})

	// Exactly the two seeds must survive: none of the three refusals above may
	// have written a row on its way to the error.
	if got := edgeGoalCount(t, s, "week", curWeek) + edgeGoalCount(t, s, "day", curDay); got != 2 {
		t.Fatalf("refused parent writes left %d rows, want the 2 seeds", got)
	}

	// And the one legal shape still works, so the rules above are refusals
	// rather than a blanket ban on nesting.
	nested, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "nested properly", ParentID: week.ID}, edgeGoalNow)
	if err != nil {
		t.Fatalf("a day goal under a week goal must be accepted: %v", err)
	}
	if nested.ParentID != week.ID {
		t.Errorf("ParentID = %d, want %d", nested.ParentID, week.ID)
	}
}

// ---------------------------------------------------------------------------
// UpdateGoal: refusals
// ---------------------------------------------------------------------------

func TestEdgeGoalUpdateMissingIDIsErrNotFound(t *testing.T) {
	s := openTest(t)
	text := "does not matter"
	for _, id := range []int64{0, -1, 424242} {
		err := s.UpdateGoal(id, GoalUpdate{Text: &text}, edgeGoalNow)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("UpdateGoal(%d) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestEdgeGoalUpdateRefusesEmptyText(t *testing.T) {
	s := openTest(t)
	_, curDay := edgeGoalCurrent()
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "original"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	for _, blank := range []string{"", " ", "   ", "\t\n"} {
		b := blank
		err := s.UpdateGoal(g.ID, GoalUpdate{Text: &b}, edgeGoalNow+1)
		if err == nil {
			t.Errorf("UpdateGoal with text %q = nil, want a refusal", blank)
			continue
		}
		edgeGoalAssertDescriptive(t, "UpdateGoal blank text", err)
	}
	// The refused updates must not have blanked the row on their way out.
	if got := edgeGoalOne(t, s, "day", curDay, g.ID); got.Text != "original" {
		t.Errorf("text = %q after refused updates, want %q", got.Text, "original")
	}

	// The positive half: a padded value is trimmed, not refused.
	padded := "  rewritten  "
	if err := s.UpdateGoal(g.ID, GoalUpdate{Text: &padded}, edgeGoalNow+2); err != nil {
		t.Fatalf("UpdateGoal with padded text: %v", err)
	}
	if got := edgeGoalOne(t, s, "day", curDay, g.ID); got.Text != "rewritten" {
		t.Errorf("text = %q, want the trimmed %q", got.Text, "rewritten")
	}
}

// TestEdgeGoalUpdateParentRules restates AddGoal's three parent refusals for
// the update path — the contract says they are the same rules, and an update
// path that skipped them would be the way every illegal shape actually gets
// into the table.
func TestEdgeGoalUpdateParentRules(t *testing.T) {
	s := openTest(t)
	curWeek, curDay := edgeGoalCurrent()

	week, err := s.AddGoal(GoalAdd{Scope: "week", Period: curWeek, Text: "week theme"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed week: %v", err)
	}
	otherWeek, err := s.AddGoal(GoalAdd{Scope: "week", Period: curWeek, Text: "second theme"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed second week: %v", err)
	}
	day, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "today"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed day: %v", err)
	}

	t.Run("a week goal cannot be given a parent by update", func(t *testing.T) {
		p := otherWeek.ID
		if err := s.UpdateGoal(week.ID, GoalUpdate{ParentID: &p}, edgeGoalNow+1); err == nil {
			t.Fatal("UpdateGoal gave a week goal a parent — only day goals may nest")
		} else {
			edgeGoalAssertDescriptive(t, "UpdateGoal week-gets-parent", err)
		}
	})

	t.Run("a day goal cannot be re-parented onto a day goal", func(t *testing.T) {
		sibling, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "sibling"}, edgeGoalNow)
		if err != nil {
			t.Fatalf("seed sibling: %v", err)
		}
		p := sibling.ID
		if err := s.UpdateGoal(day.ID, GoalUpdate{ParentID: &p}, edgeGoalNow+1); err == nil {
			t.Fatal("UpdateGoal parented a day goal under a day goal")
		} else {
			edgeGoalAssertDescriptive(t, "UpdateGoal day-under-day", err)
		}
	})

	t.Run("a parent that does not exist is refused", func(t *testing.T) {
		p := int64(987654)
		if err := s.UpdateGoal(day.ID, GoalUpdate{ParentID: &p}, edgeGoalNow+1); err == nil {
			t.Fatal("UpdateGoal accepted a parent id matching no row")
		} else {
			edgeGoalAssertDescriptive(t, "UpdateGoal missing parent", err)
		}
	})

	t.Run("ptr-to-0 clears the parent", func(t *testing.T) {
		set := week.ID
		if err := s.UpdateGoal(day.ID, GoalUpdate{ParentID: &set}, edgeGoalNow+2); err != nil {
			t.Fatalf("set parent: %v", err)
		}
		if got := edgeGoalOne(t, s, "day", curDay, day.ID); got.ParentID != week.ID {
			t.Fatalf("ParentID = %d after set, want %d", got.ParentID, week.ID)
		}
		zero := int64(0)
		if err := s.UpdateGoal(day.ID, GoalUpdate{ParentID: &zero}, edgeGoalNow+3); err != nil {
			t.Fatalf("clear parent with ptr-to-0: %v", err)
		}
		if got := edgeGoalOne(t, s, "day", curDay, day.ID); got.ParentID != 0 {
			t.Fatalf("ParentID = %d after ptr-to-0, want 0 (no parent)", got.ParentID)
		}
	})

	t.Run("a nil ParentID leaves the parent untouched", func(t *testing.T) {
		set := week.ID
		if err := s.UpdateGoal(day.ID, GoalUpdate{ParentID: &set}, edgeGoalNow+4); err != nil {
			t.Fatalf("set parent: %v", err)
		}
		newText := "text only"
		if err := s.UpdateGoal(day.ID, GoalUpdate{Text: &newText}, edgeGoalNow+5); err != nil {
			t.Fatalf("text-only update: %v", err)
		}
		got := edgeGoalOne(t, s, "day", curDay, day.ID)
		if got.ParentID != week.ID {
			t.Errorf("a nil ParentID cleared the parent: %d, want %d", got.ParentID, week.ID)
		}
		if got.Text != "text only" {
			t.Errorf("text = %q, want %q", got.Text, "text only")
		}
	})
}

// ---------------------------------------------------------------------------
// DeleteGoal / ListGoals boundaries
// ---------------------------------------------------------------------------

// TestEdgeGoalDeleteAbsentIDIsNoOp matches DeleteQuestion's stance: the caller
// wanted the row gone, and it is gone. A board that double-clicks delete must
// not see an error toast for succeeding twice.
func TestEdgeGoalDeleteAbsentIDIsNoOp(t *testing.T) {
	s := openTest(t)
	for _, id := range []int64{0, -1, 987654} {
		if err := s.DeleteGoal(id); err != nil {
			t.Errorf("DeleteGoal(%d) = %v, want nil (absent id is a no-op)", id, err)
		}
	}
	// And deleting the same real row twice is the same no-op the second time.
	_, curDay := edgeGoalCurrent()
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "transient"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := s.DeleteGoal(g.ID); err != nil {
		t.Fatalf("first DeleteGoal: %v", err)
	}
	if err := s.DeleteGoal(g.ID); err != nil {
		t.Fatalf("second DeleteGoal on the same id: %v, want nil", err)
	}
}

func TestEdgeGoalListRefusesInvalidScopeAndPeriod(t *testing.T) {
	s := openTest(t)
	for _, c := range []struct{ scope, period string }{
		{"month", edgeGoalMonday},
		{"", edgeGoalMonday},
		{"Day", "2026-08-13"},
		{"day", "13-08-2026"},
		{"day", ""},
		{"week", edgeGoalWednesday}, // a listing key that is not a Monday can never match a row
	} {
		got, err := s.ListGoals(c.scope, c.period)
		if err == nil {
			t.Errorf("ListGoals(%q, %q) = %+v, nil — want a refusal", c.scope, c.period, got)
			continue
		}
		edgeGoalAssertDescriptive(t, "ListGoals", err)
	}
}

// TestEdgeGoalListEmptyPeriodIsEmptyNotError pins the difference between "no
// goals" and "something went wrong": a period nobody has written to is the
// ordinary state of every future week, and the panel renders it as an empty
// list rather than an error.
func TestEdgeGoalListEmptyPeriodIsEmptyNotError(t *testing.T) {
	s := openTest(t)
	for _, c := range []struct{ scope, period string }{
		{"week", edgeGoalMonday},
		{"day", "2026-08-13"},
		{"week", edgeGoalPastMonday},
	} {
		got, err := s.ListGoals(c.scope, c.period)
		if err != nil {
			t.Errorf("ListGoals(%q, %q) = %v, want nil error", c.scope, c.period, err)
		}
		if len(got) != 0 {
			t.Errorf("ListGoals(%q, %q) = %+v, want empty", c.scope, c.period, got)
		}
	}
}

// ---------------------------------------------------------------------------
// GetGoal (contract amendment 2026-08-13): the by-id read's closed side
// ---------------------------------------------------------------------------

// TestEdgeGoalGetMissingIDIsErrNotFound is the amendment's stated refusal. The
// sentinel matters more here than for the other by-id methods: GetGoal's two
// callers are the API's `?slug=` job_goal key and goalContext's linked-goal
// marker, and BOTH of them meet an absent goal in the ordinary course of
// business (a job whose linked goal carl deleted, an ON DELETE SET NULL that
// has not been observed yet). A caller can only degrade that to "no goal
// rendered" if the refusal is distinguishable from a real failure — so a bare
// error, or a sql.ErrNoRows that never got wrapped, would push both surfaces
// into their error paths for a state that is not an error.
func TestEdgeGoalGetMissingIDIsErrNotFound(t *testing.T) {
	s := openTest(t)
	for _, id := range []int64{0, -1, 987654} {
		g, err := s.GetGoal(id)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("GetGoal(%d) error = %v, want ErrNotFound", id, err)
		}
		if g != nil {
			t.Errorf("GetGoal(%d) returned a row alongside its error: %+v", id, g)
		}
	}
}

// TestEdgeGoalGetAfterDeleteIsErrNotFound is the same refusal reached the way
// production actually reaches it: the goal existed, a job pointed at it, and
// carl deleted it. The link is nulled by ON DELETE SET NULL, but any caller
// still holding the old id must get the sentinel rather than a stale row.
func TestEdgeGoalGetAfterDeleteIsErrNotFound(t *testing.T) {
	s := openTest(t)
	_, curDay := edgeGoalCurrent()
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "about to be deleted"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Positive control: the row is reachable by id while it exists, otherwise
	// the refusal below proves nothing.
	got, err := s.GetGoal(g.ID)
	if err != nil {
		t.Fatalf("GetGoal on a live goal: %v", err)
	}
	if got == nil || got.ID != g.ID || got.Text != "about to be deleted" {
		t.Fatalf("GetGoal returned %+v, want the seeded row", got)
	}
	if err := s.DeleteGoal(g.ID); err != nil {
		t.Fatalf("DeleteGoal: %v", err)
	}
	if got, err := s.GetGoal(g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetGoal after delete = %+v, %v — want ErrNotFound", got, err)
	}
}

// TestEdgeGoalGetReachesGoalsOutsideTheCurrentPeriods is the amendment's own
// justification, asserted: GetGoal is by ID and by ID ALONE. A read that
// filtered by the current periods — or by done — would answer ErrNotFound for
// exactly the two goals the amendment exists to reach (last week's linked goal,
// and a linked goal already ticked off), silently withdrawing the job_goal key
// and the spawn brief's marker.
func TestEdgeGoalGetReachesGoalsOutsideTheCurrentPeriods(t *testing.T) {
	s := openTest(t)
	past, err := s.AddGoal(GoalAdd{Scope: "week", Period: edgeGoalPastMonday, Text: "last week's theme"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed past-period goal: %v", err)
	}
	if got, err := s.GetGoal(past.ID); err != nil {
		t.Errorf("GetGoal on a goal outside the current periods: %v — the read is by id alone", err)
	} else if got == nil || got.Period != edgeGoalPastMonday {
		t.Errorf("GetGoal = %+v, want the past-period row", got)
	}

	_, curDay := edgeGoalCurrent()
	doneGoal, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "already ticked off"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed done goal: %v", err)
	}
	done := true
	if err := s.UpdateGoal(doneGoal.ID, GoalUpdate{Done: &done}, edgeGoalNow+1); err != nil {
		t.Fatalf("UpdateGoal done: %v", err)
	}
	if got, err := s.GetGoal(doneGoal.ID); err != nil {
		t.Errorf("GetGoal on a done goal: %v — done is not a reason to hide a row from a by-id read", err)
	} else if got == nil || !got.Done {
		t.Errorf("GetGoal = %+v, want the done row with Done true", got)
	}
}

// ---------------------------------------------------------------------------
// SetJobGoal: refusals and the link's degradation
// ---------------------------------------------------------------------------

func TestEdgeGoalSetJobGoalMissingJobIsErrNotFound(t *testing.T) {
	s := openTest(t)
	_, curDay := edgeGoalCurrent()
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "a goal"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	for _, jobID := range []int64{0, -1, 987654} {
		if err := s.SetJobGoal(jobID, g.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("SetJobGoal(job %d) = %v, want ErrNotFound", jobID, err)
		}
	}
	// The clearing form addresses the same missing job and must refuse the
	// same way: "clear the link on a job that does not exist" is still a
	// caller addressing nothing.
	if err := s.SetJobGoal(987654, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetJobGoal(missing job, 0) = %v, want ErrNotFound", err)
	}
}

func TestEdgeGoalSetJobGoalMissingGoalIsRefused(t *testing.T) {
	s := openTest(t)
	jobID, err := s.CreateJob(&Job{Slug: "goal-link-missing"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	for _, goalID := range []int64{987654, -1} {
		err := s.SetJobGoal(jobID, goalID)
		if err == nil {
			t.Errorf("SetJobGoal(job, goal %d) = nil — a link to a goal that does not exist must be refused", goalID)
			continue
		}
		edgeGoalAssertDescriptive(t, "SetJobGoal missing goal", err)
	}
	j, err := s.GetJobBySlug("goal-link-missing")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if j.GoalID != 0 {
		t.Errorf("a refused SetJobGoal wrote GoalID = %d, want 0", j.GoalID)
	}
}

// TestEdgeGoalSetJobGoalZeroClears is the unlink path: 0 is the wire's "—",
// not an id, and it must always be accepted for a job that exists.
func TestEdgeGoalSetJobGoalZeroClears(t *testing.T) {
	s := openTest(t)
	jobID, err := s.CreateJob(&Job{Slug: "goal-link-clear"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	_, curDay := edgeGoalCurrent()
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "linked"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	if err := s.SetJobGoal(jobID, g.ID); err != nil {
		t.Fatalf("SetJobGoal link: %v", err)
	}
	if j, _ := s.GetJobBySlug("goal-link-clear"); j == nil || j.GoalID != g.ID {
		t.Fatalf("link did not take: %+v", j)
	}
	if err := s.SetJobGoal(jobID, 0); err != nil {
		t.Fatalf("SetJobGoal(0) = %v, want nil (0 clears)", err)
	}
	j, err := s.GetJobBySlug("goal-link-clear")
	if err != nil {
		t.Fatalf("GetJobBySlug after clear: %v", err)
	}
	if j.GoalID != 0 {
		t.Errorf("GoalID = %d after clearing, want 0", j.GoalID)
	}
	// Clearing an already-clear link is the same accepted write, not an error.
	if err := s.SetJobGoal(jobID, 0); err != nil {
		t.Errorf("second SetJobGoal(0) = %v, want nil", err)
	}
}

// TestEdgeGoalDeletingALinkedGoalLeavesTheJobReadable is the ON DELETE SET NULL
// promise stated as behaviour: deleting a goal a job points at degrades that
// job to "no goal", never to an unscannable row or a foreign-key error. It is
// the failure mode that would take the whole board down rather than one chip.
func TestEdgeGoalDeletingALinkedGoalLeavesTheJobReadable(t *testing.T) {
	s := openTest(t)
	jobID, err := s.CreateJob(&Job{Slug: "goal-link-delete"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	_, curDay := edgeGoalCurrent()
	g, err := s.AddGoal(GoalAdd{Scope: "day", Period: curDay, Text: "doomed"}, edgeGoalNow)
	if err != nil {
		t.Fatalf("seed goal: %v", err)
	}
	if err := s.SetJobGoal(jobID, g.ID); err != nil {
		t.Fatalf("SetJobGoal: %v", err)
	}
	if err := s.DeleteGoal(g.ID); err != nil {
		t.Fatalf("DeleteGoal on a linked goal: %v — the delete must not be blocked by the link", err)
	}

	// Every read path must still answer, with the link degraded to 0.
	bySlug, err := s.GetJobBySlug("goal-link-delete")
	if err != nil {
		t.Fatalf("GetJobBySlug after deleting the linked goal: %v", err)
	}
	if bySlug.GoalID != 0 {
		t.Errorf("GetJobBySlug GoalID = %d, want 0", bySlug.GoalID)
	}
	byID, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID after deleting the linked goal: %v", err)
	}
	if byID.GoalID != 0 {
		t.Errorf("GetJobByID GoalID = %d, want 0", byID.GoalID)
	}
	all, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs after deleting the linked goal: %v", err)
	}
	for _, j := range all {
		if j.ID == jobID && j.GoalID != 0 {
			t.Errorf("ListJobs GoalID = %d, want 0", j.GoalID)
		}
	}
}

// TestEdgeGoalIDIsNotAFormSaveColumn pins the contract's "goal_id is NOT added
// to jobFieldColumns" clause directly. It is the whole reason /api/job/goal
// exists as its own route: a full form save that could name goal_id would wipe
// the link from a client that predates the field.
func TestEdgeGoalIDIsNotAFormSaveColumn(t *testing.T) {
	if jobFieldColumns["goal_id"] {
		t.Fatal("goal_id is in jobFieldColumns — a full form/raw save can now wipe a job's goal link")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func edgeGoalCount(t *testing.T, s *Store, scope, period string) int {
	t.Helper()
	gs, err := s.ListGoals(scope, period)
	if err != nil {
		t.Fatalf("ListGoals(%q, %q): %v", scope, period, err)
	}
	return len(gs)
}

func edgeGoalOne(t *testing.T, s *Store, scope, period string, id int64) Goal {
	t.Helper()
	gs, err := s.ListGoals(scope, period)
	if err != nil {
		t.Fatalf("ListGoals(%q, %q): %v", scope, period, err)
	}
	for _, g := range gs {
		if g.ID == id {
			return g
		}
	}
	t.Fatalf("goal %d not present in %s/%s: %+v", id, scope, period, gs)
	return Goal{}
}

// edgeGoalAssertNothingWritten checks the periods a refused write in this file
// could plausibly have landed in. A refusal that still inserts is worse than
// one that errors loudly: the row is invisible to the validator that rejected
// it and visible to every listing.
func edgeGoalAssertNothingWritten(t *testing.T, s *Store) {
	t.Helper()
	curWeek, curDay := edgeGoalCurrent()
	for _, c := range []struct{ scope, period string }{
		{"week", curWeek}, {"day", curDay},
		{"week", edgeGoalMonday}, {"day", edgeGoalWednesday},
	} {
		gs, err := s.ListGoals(c.scope, c.period)
		if err != nil {
			t.Fatalf("ListGoals(%q, %q): %v", c.scope, c.period, err)
		}
		if len(gs) != 0 {
			t.Fatalf("a refused write landed a row in %s/%s: %+v", c.scope, c.period, gs)
		}
	}
}
