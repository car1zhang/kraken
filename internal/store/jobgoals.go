package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// jobgoals.go — schema v27's job_goals join table: which goals a job serves.
// One row per link, ordered by `position` within a job, and nothing else; the
// goals themselves live in goals.go and the job row in jobs.go.
//
// It replaces v23's single jobs.goal_id column, and the three verbs below are
// what that column's one setter (SetJobGoal, deleted with it) split into: a
// per-goal ADD, a per-goal REMOVE, and a CLEAR. A full-replace verb was the
// other candidate and is deliberately absent — it is the exact shape that
// reintroduces the stale-client wipe the one-field route exists to avoid, since
// a client that has not seen a link it does not know about would send the set
// without it and silently unlink it.
//
// What every verb here inherits from SetJobGoal, unchanged and load-bearing:
// NONE of them bumps `rev` or stamps `updated_at`. rev is the editor's
// optimistic lock (UpdateJobFields), and the picker posts DIRECTLY from an open
// editor form, so a bump would 409 that form's next save over a field it
// neither shows nor sent; updated_at is what the board renders as each card's
// AGE, so a bump would reset how long a job has been waiting every time carl
// labelled it. Both are the same reasoning SetJobLoad's doc comment spells out.

// requireJob is the ErrNotFound arm every verb below opens with: a job id that
// matches no row means the caller was addressing something that does not exist,
// which is SetJobLoad's contract and was SetJobGoal's.
//
// It has to be an explicit probe rather than a RowsAffected check, because two
// of the three verbs legitimately affect zero rows on a job that DOES exist
// (removing an absent link, clearing a job with none) — so "nothing changed" and
// "no such job" are only distinguishable by asking.
func requireJob(x execer, jobID int64) error {
	var id int64
	err := x.QueryRow(`SELECT id FROM jobs WHERE id = ?`, jobID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// AddJobGoal links goalID to jobID, appended at the end of that job's list.
//
// Adding a link the job already has is a SILENT NO-OP that leaves its position
// exactly where it was, which is `INSERT OR IGNORE`'s own behaviour against the
// composite primary key rather than a check in Go. Two things follow from that
// and both are wanted: a double-clicked picker row cannot reorder a job's chips,
// and the PRIMARY link (the wire's `goal` is `goals[0]`) cannot be changed by
// re-adding a goal that is already further down the list.
//
// A missing GOAL is its own descriptive error naming the id rather than a
// foreign-key violation, so the API layer's 400 says which id was wrong instead
// of quoting SQLite — and checking it here rather than leaning on the FK alone
// keeps the failure the same shape whether or not the caller's connection has
// foreign_keys on.
//
// Both probes and the insert share ONE transaction, which is what makes the
// paragraph above true rather than merely usual: as separate statements, a goal
// deleted in the gap would sail past the probe and come back from the write as a
// raw "FOREIGN KEY constraint failed" — the exact message the probe exists to
// prevent anyone ever seeing. The window is narrow (kraken serve is the single
// writer) but it is not empty: this call and a panel delete are two different
// HTTP requests. The MAX(position) read is inside it for the second half of the
// same reason AddGoal's is: two concurrent adds must not both read the same
// maximum and land on the same slot.
func (s *Store) AddJobGoal(jobID, goalID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("add goal %d to job %d: %w", goalID, jobID, err)
	}
	defer tx.Rollback()

	if err := requireJob(tx, jobID); err != nil {
		return fmt.Errorf("add goal %d to job %d: %w", goalID, jobID, err)
	}
	var exists int64
	err = tx.QueryRow(`SELECT id FROM goals WHERE id = ?`, goalID).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("add goal %d to job %d: goal %d does not exist", goalID, jobID, goalID)
	}
	if err != nil {
		return fmt.Errorf("add goal %d to job %d: %w", goalID, jobID, err)
	}
	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO job_goals (job_id, goal_id, position)
			SELECT ?, ?, COALESCE(MAX(position), 0) + 1 FROM job_goals WHERE job_id = ?`,
		jobID, goalID, jobID,
	); err != nil {
		return fmt.Errorf("add goal %d to job %d: %w", goalID, jobID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("add goal %d to job %d: %w", goalID, jobID, err)
	}
	return nil
}

// RemoveJobGoal unlinks one goal from one job. Removing a link that is not
// there is a no-op rather than an error — DeleteGoal's contract, and for the
// same reason: the caller's intent ("this job should not serve that goal") is
// satisfied either way, and a double-clicked picker row is not a failure. A
// missing JOB is still ErrNotFound: that caller was addressing nothing.
//
// The surviving links keep the positions they had — no renumber. Positions are
// only ever compared, never counted, so a gap means nothing; renumbering would
// mean rewriting rows this call was not asked to touch, and getting it wrong
// would silently move which link is the PRIMARY.
func (s *Store) RemoveJobGoal(jobID, goalID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("remove goal %d from job %d: %w", goalID, jobID, err)
	}
	defer tx.Rollback()

	if err := requireJob(tx, jobID); err != nil {
		return fmt.Errorf("remove goal %d from job %d: %w", goalID, jobID, err)
	}
	if _, err := tx.Exec(`DELETE FROM job_goals WHERE job_id = ? AND goal_id = ?`, jobID, goalID); err != nil {
		return fmt.Errorf("remove goal %d from job %d: %w", goalID, jobID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("remove goal %d from job %d: %w", goalID, jobID, err)
	}
	return nil
}

// ClearJobGoals unlinks every goal from one job — the picker's `—` row, and the
// one shape the pre-v27 wire could already express (goal_id 0), which is why it
// survives as its own verb rather than as N RemoveJobGoal calls a client would
// have to issue against a list it may not have seen whole.
//
// A job with no links is a no-op, for RemoveJobGoal's reason; a missing job is
// ErrNotFound, for the same reason again.
func (s *Store) ClearJobGoals(jobID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("clear goals for job %d: %w", jobID, err)
	}
	defer tx.Rollback()

	if err := requireJob(tx, jobID); err != nil {
		return fmt.Errorf("clear goals for job %d: %w", jobID, err)
	}
	if _, err := tx.Exec(`DELETE FROM job_goals WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("clear goals for job %d: %w", jobID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("clear goals for job %d: %w", jobID, err)
	}
	return nil
}

// jobGoalLinkOrder is the ONE ordering rule every read below shares: job by
// job, each job's goals in position order, with the goal id as the tiebreak so
// two rows that somehow share a position still come back in a fixed order
// rather than in whatever order SQLite chose today (ListGoals' rule). Written
// once and concatenated rather than repeated per query, because a second
// spelling of it is how a scoped read and a whole-table read start disagreeing
// about which link is a job's PRIMARY.
const jobGoalLinkOrder = ` ORDER BY job_id, position, goal_id`

// jobGoalLinkQuery reads every link in the table; jobGoalLinkOneQuery reads one
// job's. Two statements rather than one because their callers have opposite
// shapes, not because they answer differently — see attachJobGoals.
const jobGoalLinkQuery = `SELECT job_id, goal_id FROM job_goals` + jobGoalLinkOrder

const jobGoalLinkOneQuery = `SELECT job_id, goal_id FROM job_goals WHERE job_id = ?` + jobGoalLinkOrder

// ListJobGoalLinks returns every linked job's goal ids, in position order.
//
// A job with no links is ABSENT from the map rather than present with an empty
// slice: absence is the map's own spelling of "serves no goal", and a
// present-but-empty entry would be a second spelling of it that a caller could
// tell apart from the first without meaning to.
//
// One unparameterised query for the whole table, which is what lets a board
// build resolve every card's chips without a per-job read — the same shape
// (and the same reasoning) as attachFormerSlugs.
func (s *Store) ListJobGoalLinks() (map[int64][]int64, error) {
	rows, err := s.db.Query(jobGoalLinkQuery)
	if err != nil {
		return nil, fmt.Errorf("list job goal links: %w", err)
	}
	defer rows.Close()
	out := map[int64][]int64{}
	for rows.Next() {
		var jobID, goalID int64
		if err := rows.Scan(&jobID, &goalID); err != nil {
			return nil, fmt.Errorf("list job goal links: %w", err)
		}
		out[jobID] = append(out[jobID], goalID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list job goal links: %w", err)
	}
	return out, nil
}

// attachJobGoals fills GoalIDs on every row in js — ONE query whatever the
// caller's size, and the query it picks is the caller's shape rather than a
// preference.
//
// A MULTI-row load takes the whole table unparameterised: attachFormerSlugs'
// shape, for attachFormerSlugs' reason — an N-query loop issues a round trip
// per job on a pool capped at a single connection, and an IN-list has to be
// rebuilt and re-planned on every board load, which is the hottest read in the
// process. One unparameterised statement is the only shape constant in the
// number of jobs.
//
// A SINGLE-row load takes the `WHERE job_id = ?` form instead, and that is not
// symmetry for its own sake: GetJobBySlug/GetJobByID/GetJobByFormerSlug are the
// hot per-JOB readers — the daemon's per-cell passes, every recall firing, every
// slug-scoped route — and each of them was paying a full scan of every link in
// the database to learn about one job. The scoped read walks the primary key's
// own index instead. Both share jobGoalLinkOrder, so the two can only differ in
// how many rows they return, never in which link comes first.
//
// It runs against the caller's execer so a load inside a read transaction
// (loadJobsWithChildrenTx) sees the same snapshot as the job rows themselves.
func attachJobGoals(x execer, js []*Job) error {
	if len(js) == 0 {
		return nil
	}
	byID := make(map[int64]*Job, len(js))
	for _, j := range js {
		byID[j.ID] = j
	}
	query, args := jobGoalLinkQuery, []any(nil)
	if len(js) == 1 {
		query, args = jobGoalLinkOneQuery, []any{js[0].ID}
	}
	rows, err := x.Query(query, args...)
	if err != nil {
		return fmt.Errorf("attach job goals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var jobID, goalID int64
		if err := rows.Scan(&jobID, &goalID); err != nil {
			return fmt.Errorf("attach job goals: %w", err)
		}
		if j := byID[jobID]; j != nil {
			j.GoalIDs = append(j.GoalIDs, goalID)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("attach job goals: %w", err)
	}
	return nil
}
