package store

import (
	"fmt"
	"time"
)

// Feedback is one doc-feedback entry: a frozen artifact snapshot plus the
// user's plaintext note — mirrors what jobs/feedback.go writes under
// ~/Documents/pluto/doc-feedback/ today.
type Feedback struct {
	ID          int64
	JobSlug     string
	Artifact    string
	Snapshot    []byte
	SnapshotExt string
	Note        string
	TS          int64 // unix millis
}

// AddFeedback inserts one feedback entry, stamping TS to now if the caller
// left it zero, and returns the assigned id.
func (s *Store) AddFeedback(f Feedback) (int64, error) {
	if f.TS == 0 {
		f.TS = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`INSERT INTO feedback (job_slug, artifact, snapshot, snapshot_ext, note, ts)
			VALUES (?, ?, ?, ?, ?, ?)`,
		f.JobSlug, f.Artifact, f.Snapshot, f.SnapshotExt, f.Note, f.TS,
	)
	if err != nil {
		return 0, fmt.Errorf("add feedback for %s: %w", f.JobSlug, err)
	}
	return res.LastInsertId()
}

// ListFeedback returns feedback entries for jobSlug, oldest first; an empty
// jobSlug returns every entry (the /dream nightly sweep's use case).
func (s *Store) ListFeedback(jobSlug string) ([]Feedback, error) {
	query := `SELECT id, COALESCE(job_slug,''), COALESCE(artifact,''), snapshot,
		COALESCE(snapshot_ext,''), COALESCE(note,''), COALESCE(ts,0) FROM feedback`
	args := []any{}
	if jobSlug != "" {
		query += ` WHERE job_slug = ?`
		args = append(args, jobSlug)
	}
	query += ` ORDER BY ts`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list feedback for %q: %w", jobSlug, err)
	}
	defer rows.Close()
	var out []Feedback
	for rows.Next() {
		var f Feedback
		if err := rows.Scan(&f.ID, &f.JobSlug, &f.Artifact, &f.Snapshot, &f.SnapshotExt, &f.Note, &f.TS); err != nil {
			return nil, fmt.Errorf("list feedback for %q: %w", jobSlug, err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
