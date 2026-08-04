package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// linear.go — the linear_issues table (schema v18): one row per Linear issue
// kraken has ever processed, which is what makes the intake pass idempotent.
//
// The row is the DEDUPE record and the DELIVERY watermark in one:
//
//   - issue_id is Linear's issue UUID, which survives a rename, a move between
//     teams and an identifier change. The human key (ENG-142) is carried
//     alongside for reading, never for lookup.
//   - job_id is the job created FROM the issue, or 0 with a skip_reason
//     saying why no job was created (a slug already taken, say). Both shapes
//     are terminal: an issue with a row is never composed into a job again,
//     whatever became of the job.
//   - last_event_at is the newest Linear activity already APPLIED to that job
//     — delivered as a bump into a live cell, or appended to the body of a job
//     that has not started. It is what makes the poll cursor's deliberate
//     overlap margin free: an event at or before the stamp is ignored, and the
//     stamp only advances after a confirmed delivery (the watchPRs watermark
//     discipline).
//
// No FK and no cascade, the job_tokens precedent exactly: the row must outlive
// the job it created. An archived or hard-deleted job whose row went with it
// would make the next poll re-create the issue as a brand-new job, which is
// the one failure this table exists to prevent — carl deleting a job is him
// saying he is done with it, not asking for it back.

// LinearIssueRow is one row of linear_issues. Times are unix millis, matching
// every other timestamp in this package.
//
// Deliberately tag-free like every other type here — nothing in this row goes
// on the wire, and the API layer owns its own DTOs regardless.
type LinearIssueRow struct {
	IssueID    string
	Identifier string
	// JobID == 0 means no job was created; SkipReason says why. A row can
	// never move from 0 to a real id: PutLinearIssue is insert-only, so the
	// decision recorded here is the decision, forever.
	JobID      int64
	SkipReason string
	CreatedAt  int64
	// LastEventAt is the newest Linear updatedAt already delivered for this
	// issue. Zero on a row whose job was never created.
	LastEventAt int64
}

// linearIssueColumns COALESCEs every scanned column for jobTokensColumns'
// belt-and-suspenders reason: the schema declares all of these NOT NULL with a
// same-typed default, but a NULL that sneaks in anyway (schema drift, a
// hand-edited row) must degrade to a zero field rather than error the query.
const linearIssueColumns = `issue_id, COALESCE(identifier,''), COALESCE(job_id,0),
	COALESCE(skip_reason,''), COALESCE(created_at,0), COALESCE(last_event_at,0)`

func scanLinearIssue(row interface{ Scan(...any) error }) (*LinearIssueRow, error) {
	var r LinearIssueRow
	if err := row.Scan(&r.IssueID, &r.Identifier, &r.JobID, &r.SkipReason,
		&r.CreatedAt, &r.LastEventAt); err != nil {
		return nil, err
	}
	return &r, nil
}

// GetLinearIssue returns one issue's row, or ErrNotFound when kraken has never
// processed it — the same pointer-returning, sql.ErrNoRows → ErrNotFound shape
// as every other single-row getter in this package.
//
// ErrNotFound is the MEANINGFUL answer here rather than a mere absence: it is
// the pass's "this issue is new", and the whole create path hangs off it.
func (s *Store) GetLinearIssue(issueID string) (*LinearIssueRow, error) {
	row := s.db.QueryRow(`SELECT `+linearIssueColumns+` FROM linear_issues WHERE issue_id = ?`, issueID)
	r, err := scanLinearIssue(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get linear issue %s: %w", issueID, err)
	}
	return r, nil
}

// PutLinearIssue records an issue as processed. INSERT ONLY — a duplicate
// issue_id is an error, never an upsert.
//
// That is the load-bearing half of the verb. An upsert here would let a bug in
// the pass silently overwrite the job_id of an issue that already produced a
// job, orphaning that job from its source and freeing the issue to be created
// all over again; a UNIQUE violation instead surfaces the double-processing as
// the error it is. The one mutation this table does allow is
// SetLinearIssueEvent, and it never touches identity.
func (s *Store) PutLinearIssue(r *LinearIssueRow) error {
	if r == nil {
		return fmt.Errorf("put linear issue: nil row")
	}
	if _, err := s.db.Exec(
		`INSERT INTO linear_issues (issue_id, identifier, job_id, skip_reason, created_at, last_event_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
		r.IssueID, r.Identifier, r.JobID, r.SkipReason, r.CreatedAt, r.LastEventAt,
	); err != nil {
		return fmt.Errorf("put linear issue %s: %w", r.IssueID, err)
	}
	return nil
}

// SetLinearIssueEvent advances one issue's delivery watermark, monotonically.
//
// Monotonic in SQL rather than read-modify-write in Go for BumpJobTokens'
// reason: kraken serve answers HTTP while the poll runs, and a read-then-write
// pair around this column could move it BACKWARDS if two writes interleaved —
// which would re-deliver events carl has already seen. max() also makes "at or
// below the stored value" a no-op that returns nil, so a replayed overlap
// margin costs one statement and changes nothing.
//
// A missing row is an ERROR, the SetJobLoad posture: the caller stamped
// something it never recorded, which is a bug in the pass rather than a state
// this table can be in. The distinction survives the max() because SQLite
// counts a row MATCHED by an UPDATE as affected whether or not its value
// changed, so zero rows affected means exactly "no such issue".
//
// It touches last_event_at and nothing else — no other column of this table
// and, unlike most of what the pass does, nothing on jobs at all.
func (s *Store) SetLinearIssueEvent(issueID string, lastEventAt int64) error {
	res, err := s.db.Exec(
		`UPDATE linear_issues SET last_event_at = max(last_event_at, ?) WHERE issue_id = ?`,
		lastEventAt, issueID,
	)
	if err != nil {
		return fmt.Errorf("set linear issue event %s: %w", issueID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("set linear issue event %s: %w", issueID, err)
	}
	if n == 0 {
		return fmt.Errorf("set linear issue event %s: %w", issueID, ErrNotFound)
	}
	return nil
}

// ListLinearIssues returns every processed issue's row.
//
// Ordering is by issue_id purely so the result is deterministic; nothing
// renders in this order, and the contract promises none.
func (s *Store) ListLinearIssues() ([]*LinearIssueRow, error) {
	rows, err := s.db.Query(`SELECT ` + linearIssueColumns + ` FROM linear_issues ORDER BY issue_id`)
	if err != nil {
		return nil, fmt.Errorf("list linear issues: %w", err)
	}
	defer rows.Close()
	var out []*LinearIssueRow
	for rows.Next() {
		r, err := scanLinearIssue(rows)
		if err != nil {
			return nil, fmt.Errorf("list linear issues: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
