package store

import "fmt"

// Prereq is one job-slug prerequisite, optionally gated (blocks queueing
// until satisfied) — mirrors jobs.Prereq.
type Prereq struct {
	Slug  string
	Gated bool
}

// ReplacePrereqs atomically replaces job_id's whole prereq list: delete
// then re-insert, so the caller never has to diff old vs new.
func (s *Store) ReplacePrereqs(jobID int64, prereqs []Prereq) error {
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("replace prereqs for job %d: %w", jobID, err)
	}
	defer tx.Rollback()
	if err := replacePrereqsTx(tx, jobID, prereqs); err != nil {
		return err
	}
	return tx.Commit()
}

// replacePrereqsTx is ReplacePrereqs' body, written against execer so
// SaveJobEdit (L10) can compose it inside a caller-owned transaction.
func replacePrereqsTx(x execer, jobID int64, prereqs []Prereq) error {
	if _, err := x.Exec(`DELETE FROM prereqs WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("replace prereqs for job %d: %w", jobID, err)
	}
	for _, p := range prereqs {
		if _, err := x.Exec(
			`INSERT INTO prereqs (job_id, prereq_slug, gated) VALUES (?, ?, ?)`,
			jobID, p.Slug, p.Gated,
		); err != nil {
			return fmt.Errorf("replace prereqs for job %d: %w", jobID, err)
		}
	}
	return nil
}

// ListPrereqs returns job_id's prereqs in insertion order.
func (s *Store) ListPrereqs(jobID int64) ([]Prereq, error) {
	return listPrereqsTx(s.db, jobID)
}

// listPrereqsTx is ListPrereqs' body, written against execer so
// LoadJobsWithChildren (L12) can read it inside a caller-owned read
// transaction alongside the jobs and PRs queries — one consistent
// snapshot instead of separate calls that could each interleave with a
// concurrent write.
func listPrereqsTx(x execer, jobID int64) ([]Prereq, error) {
	rows, err := x.Query(
		`SELECT prereq_slug, gated FROM prereqs WHERE job_id = ? ORDER BY rowid`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list prereqs for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []Prereq
	for rows.Next() {
		var p Prereq
		if err := rows.Scan(&p.Slug, &p.Gated); err != nil {
			return nil, fmt.Errorf("list prereqs for job %d: %w", jobID, err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
