package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Questions is one job's doc-questions file (questions.yaml), stored
// blob-canonical like an artifact: whole-file bytes plus a caller-supplied
// mtime, one row per job.
type Questions struct {
	JobID int64
	Bytes []byte
	Mtime int64 // caller-supplied unix millis; this package never stamps it
}

// UpsertQuestions writes jobID's questions.yaml bytes, replacing any prior
// row (one row per job, like drafts — not append-only like artifacts/
// events). data is the whole file's bytes as found on disk; mtime is
// caller-supplied unix millis, matching artifacts' convention of never
// having this package stamp its own clock.
//
// A present-but-empty file (data == []byte{}) is a valid, meaningful row —
// "questions.yaml exists but zeus hasn't written any questions yet" — not
// treated any differently from a non-empty one here. GetQuestions round-trips
// it as Bytes with length 0, distinct from ErrNotFound (no row at all);
// callers must treat empty Bytes the same as "no questions" without
// confusing it for "never synced."
func (s *Store) UpsertQuestions(jobID int64, data []byte, mtime int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO questions (job_id, bytes, mtime) VALUES (?, ?, ?)
			ON CONFLICT(job_id) DO UPDATE SET bytes = excluded.bytes, mtime = excluded.mtime`,
		jobID, data, mtime,
	); err != nil {
		return fmt.Errorf("upsert questions for job %d: %w", jobID, err)
	}
	return nil
}

// GetQuestions fetches jobID's stored questions.yaml, or ErrNotFound when no
// row exists yet — a job that has never had a questions.yaml is a normal,
// expected state, matching the package's other single-row lookups
// (GetDraft, GetArtifact). A row with zero-length Bytes is NOT ErrNotFound:
// it means questions.yaml existed on disk but was empty, and callers must
// treat that the same as "no questions" rather than treating it as missing.
func (s *Store) GetQuestions(jobID int64) (*Questions, error) {
	var q Questions
	err := s.db.QueryRow(
		`SELECT job_id, bytes, COALESCE(mtime,0) FROM questions WHERE job_id = ?`,
		jobID,
	).Scan(&q.JobID, &q.Bytes, &q.Mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get questions for job %d: %w", jobID, err)
	}
	return &q, nil
}

// DeleteQuestions removes jobID's questions row, if any (a no-op, not an
// error, when none exists — mirrors the file having been deleted or never
// having existed).
func (s *Store) DeleteQuestions(jobID int64) error {
	if _, err := s.db.Exec(`DELETE FROM questions WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("delete questions for job %d: %w", jobID, err)
	}
	return nil
}
