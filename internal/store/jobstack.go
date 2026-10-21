package store

import (
	"fmt"
)

// jobstack.go — schema v35's job_stacks table: a cell's manual-test URLs
// (its "stack"), one row per (job, url), registered by the cell through
// `kraken stack add` and rendered by the board as linked chips beside the
// PR chips. The daemon's probe pass keeps `status` honest for loopback URLs.
//
// What every verb here inherits from SetJobLoad and the job_goals verbs,
// unchanged and load-bearing: NONE of them bumps `rev` or stamps
// `updated_at`. rev is the editor's optimistic lock and updated_at is what
// the board renders as each card's AGE; a cell rewrites this list at its
// manual-test gate, which is exactly when the age should keep counting and
// an open editor should keep saving.

// StackLink is one row. Tag-free like every type in this package: the API
// layer owns the wire shape (internal/api/jobstack.go's webStackLink).
type StackLink struct {
	JobID    int64
	URL      string
	Label    string
	Status   string
	At       int64
	ProbedAt int64
}

// StackStatusUnknown is the status a row is born with and keeps while the
// daemon has never dialled it — every non-loopback URL stays here forever.
// The up/down spellings are the probe's (internal/jobs/stackprobe.go); the
// store does not validate the value, it is a string the daemon owns.
const StackStatusUnknown = "unknown"

const stackColumns = `job_id, url, label, status, at, probed_at`

// UpsertStack registers url against jobID, or re-labels it when the row
// already exists. A re-add keeps the row's status and registration time —
// the cell saying the same URL twice is not a new stack, and resetting the
// probe's verdict would flash a dead port green for a tick.
//
// requireJob first: an unknown job is ErrNotFound rather than a row that
// nothing will ever render (jobgoals.go's rule).
func (s *Store) UpsertStack(jobID int64, url, label string, at int64) error {
	if err := requireJob(s.db, jobID); err != nil {
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO job_stacks (job_id, url, label, status, at) VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(job_id, url) DO UPDATE SET label = excluded.label`,
		jobID, url, label, StackStatusUnknown, at,
	)
	if err != nil {
		return fmt.Errorf("upsert stack %s for job %d: %w", url, jobID, err)
	}
	return nil
}

// RemoveStack deletes the row whose url OR label equals token (url wins when
// a label happens to equal another row's url — the match is tried in that
// order and only the first hit is deleted). Returns ErrNotFound when the job
// does not exist AND when nothing matched: the caller asked to remove one
// specific thing, and "it was not there" is an answer worth stating, not a
// silent success.
func (s *Store) RemoveStack(jobID int64, token string) error {
	if err := requireJob(s.db, jobID); err != nil {
		return err
	}
	for _, col := range []string{"url", "label"} {
		res, err := s.db.Exec(
			`DELETE FROM job_stacks WHERE job_id = ? AND `+col+` = ?`,
			jobID, token,
		)
		if err != nil {
			return fmt.Errorf("remove stack %q for job %d: %w", token, jobID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			return nil
		}
	}
	return ErrNotFound
}

// ClearStacks deletes every row of jobID. Clearing an empty list is a
// success — the cell is stating a fact about its stack, and "none" was
// already true. ErrNotFound only for a job that does not exist.
func (s *Store) ClearStacks(jobID int64) error {
	if err := requireJob(s.db, jobID); err != nil {
		return err
	}
	return s.DeleteStacksForJob(jobID)
}

// DeleteStacksForJob is the teardown's verb: a bare delete by job id with no
// existence check, because teardown runs on a job row that may already be in
// any state and a stale link is a smaller problem than a teardown that
// reports failure after succeeding. ClearStacks wraps it for the cell-side
// verb, which does want the existence check.
func (s *Store) DeleteStacksForJob(jobID int64) error {
	if _, err := s.db.Exec(`DELETE FROM job_stacks WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("delete stacks for job %d: %w", jobID, err)
	}
	return nil
}

// ListStacks returns jobID's rows in registration order (the rowid order an
// INSERT gives them; an upsert keeps the original rowid, so re-labelling a
// link does not move its chip). An empty list for an unknown job rather than
// ErrNotFound: the board calls this for every card and a missing job is not
// an error at render time.
func (s *Store) ListStacks(jobID int64) ([]StackLink, error) {
	rows, err := s.db.Query(
		`SELECT `+stackColumns+` FROM job_stacks WHERE job_id = ? ORDER BY rowid`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list stacks for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []StackLink
	for rows.Next() {
		var l StackLink
		if err := rows.Scan(&l.JobID, &l.URL, &l.Label, &l.Status, &l.At, &l.ProbedAt); err != nil {
			return nil, fmt.Errorf("list stacks for job %d: %w", jobID, err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// SetStackStatus stamps the probe's verdict on one row. A no-op (nil) when
// the row is gone — the cell cleared it between the probe's read and this
// write, and there is nothing left to describe. Never touches label or at.
func (s *Store) SetStackStatus(jobID int64, url, status string, probedAt int64) error {
	_, err := s.db.Exec(
		`UPDATE job_stacks SET status = ?, probed_at = ? WHERE job_id = ? AND url = ?`,
		status, probedAt, jobID, url,
	)
	if err != nil {
		return fmt.Errorf("set stack status %s for job %d: %w", url, jobID, err)
	}
	return nil
}
