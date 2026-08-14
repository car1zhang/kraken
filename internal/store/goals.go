package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// goals.go — schema v23's goals table: carl's weekly and daily goals, the
// one layer of nesting between them (a day goal may point at the week goal
// it serves), and nothing else. Every row is carl's — unlike questions.go's
// split ownership, there is no agent-owned column here, so no write below
// has to preserve anything across another writer's update.
//
// Two things are deliberately NOT in this file. Job linkage lives on the
// jobs row (SetJobGoal, jobs.go) because the link is a property of the job,
// not of the goal — deleting a goal unlinks it through the schema's ON
// DELETE SET NULL rather than through code here. And the "which week is it"
// question is answered by GoalPeriods below, one place, so the panel, the
// API's current-period read and the injected brief block can never disagree
// about where Monday is.

// goalDateLayout is the period format for BOTH scopes: a day goal's period
// is its own local calendar date, a week goal's is that week's local Monday.
// The same layout the dream loop keys its days by — this feature's day
// boundary is carl's local calendar, never UTC.
const goalDateLayout = "2006-01-02"

// Goal is one goals row. Tagless, like every other store row type: the wire
// shape is the API layer's own DTO (internal/api/goals.go), a deliberate,
// independently evolvable contract rather than whatever this struct happens
// to carry.
type Goal struct {
	ID     int64
	Scope  string // "week" | "day"
	Period string // local calendar date; a week goal's is its Monday
	Text   string
	Done   bool
	// Position orders goals within one (Scope, Period) list. Assigned by
	// AddGoal as the next slot, so insertion order is what a listing shows.
	Position int64
	// ParentID is the week goal a DAY goal serves, 0 for none — which is
	// both the never-assigned state and what a day goal degrades to when its
	// week goal is deleted (schemaV23's ON DELETE SET NULL). A week goal
	// never carries one; AddGoal/UpdateGoal refuse it.
	ParentID  int64
	CreatedAt int64 // unix millis
	UpdatedAt int64 // unix millis
}

// GoalAdd is AddGoal's input: the four fields a caller supplies. Position,
// the timestamps and the id are all assigned by the write, so they are
// absent here rather than present-and-ignored — a caller cannot set a
// position that the next add would contradict.
type GoalAdd struct {
	Scope    string
	Period   string
	Text     string
	ParentID int64
}

// GoalUpdate is UpdateGoal's partial: a nil field is UNCHANGED, which is
// what makes a done-checkbox toggle and a text edit the same call without
// either one clobbering the other. ParentID is a pointer for the reason
// jobload's Unblock/Scope are: with a plain int64 "absent" and "explicitly
// 0" are the same value, and 0 is the CLEAR — so every update that only
// meant to rename a goal would silently unnest it.
type GoalUpdate struct {
	Text     *string
	Done     *bool
	ParentID *int64
}

// GoalPeriods returns the two period keys now falls in: the local Monday of
// now's week, and now's own local calendar date. The ONE place either is
// derived, so the panel, the API's current-period read and the brief-time
// injection can never disagree about which week it is.
//
// Weeks start Monday (carl's call): a Monday maps to itself and every other
// weekday — SUNDAY INCLUDED — maps back to the most recent past Monday.
// That is what the (weekday + 6) % 7 shift buys over Go's own Weekday
// numbering, where Sunday is 0 and a naive subtraction would push it FORWARD
// into the week that has not started yet.
//
// AddDate is date arithmetic on the calendar fields, not a duration
// subtraction, so a DST boundary inside the week cannot shift the answer by
// a day the way now.Add(-n * 24 * time.Hour) can.
func GoalPeriods(now time.Time) (week, day string) {
	back := (int(now.Weekday()) + 6) % 7
	return now.AddDate(0, 0, -back).Format(goalDateLayout), now.Format(goalDateLayout)
}

// validateGoalScope/validateGoalPeriod return a descriptive, API-surfaceable
// error rather than a bare bool — questions.go's validateQID convention —
// because these sentences become 400 bodies verbatim, read by carl in a
// panel and by zeus on stderr.
func validateGoalScope(scope string) error {
	if scope != "week" && scope != "day" {
		return fmt.Errorf("bad goal scope %q: want week or day", scope)
	}
	return nil
}

// validateGoalPeriod checks the period against its scope: parseable as a
// local calendar date, and — for a week goal — an actual MONDAY, since a
// week goal's period IS its week's Monday (GoalPeriods) and a row keyed to
// any other weekday would be invisible to every reader that asks for the
// current week.
//
// It re-checks the scope rather than assuming a validated one: it takes the
// scope as an argument, so a caller reaching it with garbage would otherwise
// get "your period is fine" for a row that can never be listed.
func validateGoalPeriod(scope, period string) error {
	if err := validateGoalScope(scope); err != nil {
		return err
	}
	t, err := time.ParseInLocation(goalDateLayout, period, time.Local)
	if err != nil {
		return fmt.Errorf("bad goal period %q: want a local calendar date like 2026-08-13", period)
	}
	if scope == "week" && t.Weekday() != time.Monday {
		return fmt.Errorf("bad week period %q: a week goal's period is its week's Monday, and that date is a %s", period, t.Weekday())
	}
	return nil
}

// goalColumns is every column scanGoal reads, COALESCEd for the plain (non-
// Null*) Go fields — the same belt-and-suspenders reasoning as jobColumns:
// the schema declares them NOT NULL DEFAULT already, but a NULL that sneaks
// in anyway must degrade to a blank field rather than error the whole query.
// parent_id is genuinely nullable (NULL is the normal "unassigned" state)
// and is scanned via sql.NullInt64 below, so it is left bare.
const goalColumns = `id, scope, period, COALESCE(text,''), COALESCE(done,0),
	COALESCE(position,0), parent_id, COALESCE(created_at,0), COALESCE(updated_at,0)`

func scanGoal(row interface{ Scan(...any) error }) (*Goal, error) {
	var g Goal
	var parentID sql.NullInt64
	if err := row.Scan(&g.ID, &g.Scope, &g.Period, &g.Text, &g.Done,
		&g.Position, &parentID, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, err
	}
	g.ParentID = parentID.Int64
	return &g, nil
}

// ListGoals returns one (scope, period) list in display order: position
// first, id as the tiebreak so two rows that somehow share a position still
// come back oldest-first rather than in whatever order SQLite chose today.
// An empty list is not an error — a week nobody has written goals for yet is
// the normal state of every week before carl gets to it.
func (s *Store) ListGoals(scope, period string) ([]Goal, error) {
	if err := validateGoalPeriod(scope, period); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(
		`SELECT `+goalColumns+` FROM goals WHERE scope = ? AND period = ? ORDER BY position, id`,
		scope, period,
	)
	if err != nil {
		return nil, fmt.Errorf("list %s goals for %s: %w", scope, period, err)
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, fmt.Errorf("list %s goals for %s: %w", scope, period, err)
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// GetGoal reads one goal by id, ErrNotFound when it is gone. The by-id read
// exists because a LINKED goal is not necessarily a current-period one: a job
// keeps serving the goal carl picked for it after the week rolls over, and
// both the injected brief block and the editor's picker have to be able to
// name it. Every other read in this file is a list.
func (s *Store) GetGoal(id int64) (*Goal, error) {
	row := s.db.QueryRow(`SELECT `+goalColumns+` FROM goals WHERE id = ?`, id)
	g, err := scanGoal(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get goal %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get goal %d: %w", id, err)
	}
	return g, nil
}

// validateGoalParent enforces the two halves of the nesting rule that SQL
// cannot state (a CHECK cannot read another row and a FK cannot read this
// one): the child must be DAY-scope, and the parent must EXIST and be
// WEEK-scope. One implementation, called by both AddGoal and UpdateGoal, so
// those three refusals only have to be gotten right once — and it runs against
// the caller's transaction so the parent it verified cannot be deleted between
// the check and the insert.
//
// What it deliberately does NOT check is the PERIOD: a day goal may name a
// week goal from any week, this one or a past one. That is tolerated rather
// than refused in v1, and it is not silently wrong — every reader groups a day
// goal under a parent it can see in the CURRENT week's list (the panel's
// groupGoals, the injected brief block), so a cross-period parent renders as
// UNASSIGNED, exactly like a day goal with no parent at all. The row keeps the
// id; the display degrades. Refusing it would need this function to take the
// child's period as a fourth argument and would make "carry yesterday's goal
// forward" a refusal instead of a shrug.
//
// Depth is capped at one by the "parent must be week-scoped" arm alone: a
// week goal can never carry a parent, so no chain longer than day→week can
// be built, and a goal cannot be its own parent (its scope would have to be
// both day and week at once).
func validateGoalParent(x execer, scope string, parentID int64) error {
	if scope != "day" {
		return fmt.Errorf("only a day goal can sit under a week goal; this one is %s-scope", scope)
	}
	var parentScope string
	err := x.QueryRow(`SELECT scope FROM goals WHERE id = ?`, parentID).Scan(&parentScope)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("parent goal %d does not exist", parentID)
	}
	if err != nil {
		return fmt.Errorf("look up parent goal %d: %w", parentID, err)
	}
	if parentScope != "week" {
		return fmt.Errorf("goal %d is a %s goal and cannot be a parent — only a week goal can", parentID, parentScope)
	}
	return nil
}

// trimGoalText is the one text rule, shared by AddGoal and UpdateGoal: the
// stored text is trimmed, and a goal that is only whitespace is refused
// rather than stored blank. A blank goal is not a smaller goal — it is a row
// carl cannot tell apart from the add-input he abandoned.
func trimGoalText(text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", errors.New("goal text is empty")
	}
	return trimmed, nil
}

// AddGoal appends one goal to its (scope, period) list and returns the
// stored row, id included — the caller (the panel's add-input) renders what
// was actually written rather than what it hoped it sent.
//
// now is passed in rather than read from the clock here for the reason
// SetJobLoad's At is: the API layer stamps one time per request, and a test
// can write a row at a chosen instant without moving the machine's clock.
//
// The position query and the insert share ONE transaction, so two concurrent
// adds cannot both read the same MAX(position) and land on the same slot.
func (s *Store) AddGoal(g GoalAdd, now int64) (*Goal, error) {
	if err := validateGoalPeriod(g.Scope, g.Period); err != nil {
		return nil, err
	}
	text, err := trimGoalText(g.Text)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("add %s goal: %w", g.Scope, err)
	}
	defer tx.Rollback()

	if g.ParentID != 0 {
		if err := validateGoalParent(tx, g.Scope, g.ParentID); err != nil {
			return nil, err
		}
	}
	var position int64
	if err := tx.QueryRow(
		`SELECT COALESCE(MAX(position), 0) + 1 FROM goals WHERE scope = ? AND period = ?`,
		g.Scope, g.Period,
	).Scan(&position); err != nil {
		return nil, fmt.Errorf("add %s goal: %w", g.Scope, err)
	}
	res, err := tx.Exec(
		`INSERT INTO goals (scope, period, text, done, position, parent_id, created_at, updated_at)
			VALUES (?, ?, ?, 0, ?, ?, ?, ?)`,
		g.Scope, g.Period, text, position, nullableGoalID(g.ParentID), now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("add %s goal: %w", g.Scope, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("add %s goal: %w", g.Scope, err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("add %s goal: %w", g.Scope, err)
	}
	return &Goal{
		ID: id, Scope: g.Scope, Period: g.Period, Text: text,
		Position: position, ParentID: g.ParentID, CreatedAt: now, UpdatedAt: now,
	}, nil
}

// nullableGoalID maps this package's 0-means-none convention onto SQL NULL,
// for both goals.parent_id and jobs.goal_id (SetJobGoal). Untyped nil rather
// than sql.NullInt64{}: database/sql binds either as NULL, and the nil form
// keeps the call sites reading as the one decision they make.
func nullableGoalID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// UpdateGoal applies a partial update. Every nil field is left exactly as it
// was, so the panel's three edits — rename, done toggle, re-nest — are three
// calls that cannot clobber each other's column.
//
// UpdatedAt is stamped on every successful call, including one that changes
// no other column: the timestamp records that carl touched the goal, and the
// only alternative (deciding whether the new values differ from the stored
// ones) would make an idempotent re-save silently different from a first one.
//
// A missing id is ErrNotFound rather than a silent no-op, unlike DeleteGoal:
// an update names a row the caller believes it is looking at, so a no-op
// would report success for an edit that reached nothing.
func (s *Store) UpdateGoal(id int64, upd GoalUpdate, now int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("update goal %d: %w", id, err)
	}
	defer tx.Rollback()

	// The row's own scope is read first because the parent rule is stated in
	// terms of it (only a day goal may carry a parent) and the caller does
	// not send it — the update names an id, not a whole goal.
	var scope string
	err = tx.QueryRow(`SELECT scope FROM goals WHERE id = ?`, id).Scan(&scope)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("update goal %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("update goal %d: %w", id, err)
	}

	setClause := ""
	var args []any
	if upd.Text != nil {
		text, err := trimGoalText(*upd.Text)
		if err != nil {
			return err
		}
		setClause += "text = ?, "
		args = append(args, text)
	}
	if upd.Done != nil {
		setClause += "done = ?, "
		args = append(args, *upd.Done)
	}
	if upd.ParentID != nil {
		// Pointer-to-0 is the explicit CLEAR (unnest this day goal), which is
		// why it skips the parent check entirely rather than failing it.
		if *upd.ParentID != 0 {
			if err := validateGoalParent(tx, scope, *upd.ParentID); err != nil {
				return err
			}
		}
		setClause += "parent_id = ?, "
		args = append(args, nullableGoalID(*upd.ParentID))
	}
	setClause += "updated_at = ?"
	args = append(args, now, id)

	if _, err := tx.Exec(`UPDATE goals SET `+setClause+` WHERE id = ?`, args...); err != nil {
		return fmt.Errorf("update goal %d: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("update goal %d: %w", id, err)
	}
	return nil
}

// DeleteGoal removes one goal. A no-op, not an error, when the id is already
// gone — DeleteQuestion's contract, and for the same reason: the caller's
// intent ("this goal should not exist") is satisfied either way, and a
// double-click on a delete affordance is not a failure.
//
// Two things survive the row and neither is handled here: a day goal that
// pointed at it keeps existing with its parent CLEARED, and a job that
// linked it keeps existing with its link cleared. Both fall out of
// schemaV23's ON DELETE SET NULL, which is why this method is one statement
// rather than a transaction that fixes up two other tables — and why the
// nesting degrades instead of cascading, which would delete a day's work
// along with the week goal it happened to sit under.
func (s *Store) DeleteGoal(id int64) error {
	if _, err := s.db.Exec(`DELETE FROM goals WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete goal %d: %w", id, err)
	}
	return nil
}
