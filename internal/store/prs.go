package store

import (
	"fmt"
	"time"
)

// PR is one PR shipped from a job's cell branch — mirrors jobs.JobPR.
type PR struct {
	URL           string
	State         string
	LastCommentAt string
	BumpedSHA     string
	ConflictState string
}

// SetPRWatermark stamps watchPRs' per-URL comment/CI/conflict watermarks.
// A no-op when the URL isn't recorded for jobID (it was dropped between the
// caller's load and this write — nothing left to stamp). Does NOT bump
// jobs.rev: watermarks are daemon bookkeeping invisible to the editor, and
// bumping would 409 an open editor for a change it cannot see. UpsertPR
// never touches these columns, so a state refresh preserves watermarks.
func (s *Store) SetPRWatermark(jobID int64, url, lastCommentAt, bumpedSHA, conflictState string) error {
	_, err := s.db.Exec(
		`UPDATE prs SET last_comment_at = ?, bumped_sha = ?, conflict_state = ? WHERE job_id = ? AND url = ?`,
		lastCommentAt, bumpedSHA, conflictState, jobID, url,
	)
	if err != nil {
		return fmt.Errorf("set pr watermark %s for job %d: %w", url, jobID, err)
	}
	return nil
}

// UpsertPR records url against job_id if it isn't already there (INSERT OR
// IGNORE — a job can accumulate multiple PRs across reactivation runs, and
// re-attaching the same PR must not error), then makes sure its state is
// current. Two statements rather than one ON CONFLICT clause, so a
// pre-existing row's state always ends up exactly what's passed in.
//
// Also bumps the owning job's rev in the same transaction (L2), but ONLY
// when this call actually changed something — a new row inserted, or an
// existing row's state genuinely changed value (the UPDATE's WHERE guards
// on state != ? so a no-op re-write of the same state doesn't count as a
// change). This matters because a caller re-asserting a PR it already
// recorded must not churn rev/updated_at, while a save started against a
// rev that predates a
// genuine PR attach/state-change must still see it as stale rather than
// silently clobbering it — the editor's optimistic lock (UpdateJobFields's
// rev check).
func (s *Store) UpsertPR(jobID int64, url, state string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("upsert pr %s for job %d: %w", url, jobID, err)
	}
	defer tx.Rollback()
	if err := upsertPRTx(tx, jobID, url, state); err != nil {
		return err
	}
	return tx.Commit()
}

// upsertPRTx is UpsertPR's body, written against execer so SaveJobEdit
// (L10) can compose it inside a caller-owned transaction alongside the
// field update.
func upsertPRTx(x execer, jobID int64, url, state string) error {
	insRes, err := x.Exec(
		`INSERT OR IGNORE INTO prs (job_id, url, state) VALUES (?, ?, ?)`,
		jobID, url, state,
	)
	if err != nil {
		return fmt.Errorf("upsert pr %s for job %d: %w", url, jobID, err)
	}
	inserted, err := insRes.RowsAffected()
	if err != nil {
		return fmt.Errorf("upsert pr %s for job %d: %w", url, jobID, err)
	}
	updRes, err := x.Exec(
		`UPDATE prs SET state = ? WHERE job_id = ? AND url = ? AND state != ?`,
		state, jobID, url, state,
	)
	if err != nil {
		return fmt.Errorf("upsert pr %s for job %d: %w", url, jobID, err)
	}
	updated, err := updRes.RowsAffected()
	if err != nil {
		return fmt.Errorf("upsert pr %s for job %d: %w", url, jobID, err)
	}
	if inserted > 0 || updated > 0 {
		if err := bumpJobRev(x, jobID); err != nil {
			return fmt.Errorf("upsert pr %s for job %d: %w", url, jobID, err)
		}
	}
	return nil
}

// bumpJobRev advances jobs.rev (+ updated_at) for jobID — shared by
// upsertPRTx and removePRTx so a PR mutation is visible to the editor's
// optimistic-lock rev check exactly like any other job field change.
func bumpJobRev(x execer, jobID int64) error {
	_, err := x.Exec(
		`UPDATE jobs SET rev = rev + 1, updated_at = ? WHERE id = ?`,
		time.Now().UnixMilli(), jobID,
	)
	return err
}

// ListPRs returns job_id's PRs in insertion order.
func (s *Store) ListPRs(jobID int64) ([]PR, error) {
	return listPRsTx(s.db, jobID)
}

// listPRsTx is ListPRs' body, written against execer so SaveJobEdit (L10)
// can read the CURRENT PR set from inside its own transaction — reading
// outside the transaction and writing the diff inside it would leave a
// window for a concurrent PR attach (jobs/prhook.go's postPRAttach, a
// completely separate transaction) to land in between, silently undone by
// the save's own remove-anything-not-named pass.
func listPRsTx(x execer, jobID int64) ([]PR, error) {
	rows, err := x.Query(
		`SELECT url, state, COALESCE(last_comment_at, ''), COALESCE(bumped_sha, ''), COALESCE(conflict_state, '')
		 FROM prs WHERE job_id = ? ORDER BY rowid`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list prs for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []PR
	for rows.Next() {
		var p PR
		if err := rows.Scan(&p.URL, &p.State, &p.LastCommentAt, &p.BumpedSHA, &p.ConflictState); err != nil {
			return nil, fmt.Errorf("list prs for job %d: %w", jobID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RemovePR deletes one PR row, bumping the owning job's rev in the same
// transaction — same rationale as UpsertPR's bumpJobRev call: a PR removal
// is a job mutation the editor's optimistic lock must see. Only bumps when
// a row was actually deleted (same "no-op must not churn rev" contract as
// UpsertPR): removing a URL that was never attached is a no-op, not a
// mutation.
func (s *Store) RemovePR(jobID int64, url string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("remove pr %s for job %d: %w", url, jobID, err)
	}
	defer tx.Rollback()
	if err := removePRTx(tx, jobID, url); err != nil {
		return err
	}
	return tx.Commit()
}

// removePRTx is RemovePR's body, written against execer so SaveJobEdit
// (L10) can compose it inside a caller-owned transaction.
func removePRTx(x execer, jobID int64, url string) error {
	res, err := x.Exec(`DELETE FROM prs WHERE job_id = ? AND url = ?`, jobID, url)
	if err != nil {
		return fmt.Errorf("remove pr %s for job %d: %w", url, jobID, err)
	}
	deleted, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove pr %s for job %d: %w", url, jobID, err)
	}
	if deleted > 0 {
		if err := bumpJobRev(x, jobID); err != nil {
			return fmt.Errorf("remove pr %s for job %d: %w", url, jobID, err)
		}
	}
	return nil
}
