package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Artifact is one blob-canonical artifact file, path relative to the job's
// artifacts dir (subdirs preserved in path).
type Artifact struct {
	ID    int64
	JobID int64
	Path  string
	Bytes []byte
	Mtime int64 // caller-supplied unix millis; this package never stamps it
}

// ArtifactFile is the input shape for SyncSet: what the block-event walker
// found on disk for one file that needs its bytes upserted (a new or
// changed file — see SyncArtifacts' unchanged param for the other case).
type ArtifactFile struct {
	Path  string
	Bytes []byte
	Mtime int64 // caller-supplied unix millis
}

// ArtifactMeta is one artifact's identity plus size/mtime, deliberately
// without its bytes — LENGTH(bytes) stands in for a stored size so a caller
// deciding what's unchanged (ListArtifactMeta's purpose) never pays to read
// a blob column it's about to skip anyway.
type ArtifactMeta struct {
	Path  string
	Mtime int64
	Size  int64
}

// ListArtifactMeta returns job_id's artifacts' path/mtime/size, ordered by
// path, without their bytes. The block-event sync walker (jobs/sync.go)
// calls this first to compare against what it finds on disk, so it can
// build SyncArtifacts' unchanged set without ever reading a blob column for
// a file it isn't going to touch.
func (s *Store) ListArtifactMeta(jobID int64) ([]ArtifactMeta, error) {
	rows, err := s.db.Query(
		`SELECT path, COALESCE(mtime,0), LENGTH(bytes) FROM artifacts WHERE job_id = ? ORDER BY path`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list artifact meta for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []ArtifactMeta
	for rows.Next() {
		var m ArtifactMeta
		if err := rows.Scan(&m.Path, &m.Mtime, &m.Size); err != nil {
			return nil, fmt.Errorf("list artifact meta for job %d: %w", jobID, err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// SyncArtifacts makes job_id's artifact rows an exact mirror of the union of
// files and unchanged: every entry in files is upserted (updating
// bytes/mtime — a new or changed file), every path named in unchanged is
// left completely untouched (the caller has already verified, via
// ListArtifactMeta, that its stored (path, mtime, size) still matches disk,
// so re-writing identical bytes would just be wasted I/O), and any existing
// row whose path is in NEITHER set is deleted. One transaction, so a crash
// mid-sync leaves the last-good mirror rather than a torn one (Q11's
// true-mirror semantic).
//
// unchanged is a plain parameter (S17 — was previously variadic, "0 or 1
// map", purely to keep every 2-arg call site that had nothing unchanged
// compiling without touching them; nil is exactly as cheap to pass
// explicitly and a plain parameter is the honest signature for what's
// really a required argument). A caller with nothing unchanged this pass
// — the importer's one-shot bulk load, in particular — just passes nil.
//
// files is assumed de-duplicated by path already, and disjoint from
// unchanged: a path the caller lists in both, or twice within files, is a
// caller bug (the walker is expected to visit each file once and classify
// it as exactly one or the other) — SyncArtifacts doesn't re-check, and the
// last occurrence in files simply wins the upsert.
func (s *Store) SyncArtifacts(jobID int64, files []ArtifactFile, unchanged map[string]bool) error {
	un := unchanged

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("sync artifacts for job %d: %w", jobID, err)
	}
	defer tx.Rollback()

	keep := make([]string, 0, len(files)+len(un))
	for _, f := range files {
		if _, err := tx.Exec(
			`INSERT INTO artifacts (job_id, path, bytes, mtime) VALUES (?, ?, ?, ?)
				ON CONFLICT(job_id, path) DO UPDATE SET bytes = excluded.bytes, mtime = excluded.mtime`,
			jobID, f.Path, f.Bytes, f.Mtime,
		); err != nil {
			return fmt.Errorf("sync artifacts for job %d: %w", jobID, err)
		}
		keep = append(keep, f.Path)
	}
	for p := range un {
		keep = append(keep, p)
	}

	rows, err := tx.Query(`SELECT path FROM artifacts WHERE job_id = ?`, jobID)
	if err != nil {
		return fmt.Errorf("sync artifacts for job %d: %w", jobID, err)
	}
	keepSet := make(map[string]bool, len(keep))
	for _, p := range keep {
		keepSet[p] = true
	}
	var stale []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return fmt.Errorf("sync artifacts for job %d: %w", jobID, err)
		}
		if !keepSet[p] {
			stale = append(stale, p)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("sync artifacts for job %d: %w", jobID, err)
	}
	rows.Close()

	for _, p := range stale {
		if _, err := tx.Exec(`DELETE FROM artifacts WHERE job_id = ? AND path = ?`, jobID, p); err != nil {
			return fmt.Errorf("sync artifacts for job %d: %w", jobID, err)
		}
	}
	return tx.Commit()
}

// GetArtifact fetches one artifact by job id + path.
func (s *Store) GetArtifact(jobID int64, path string) (*Artifact, error) {
	var a Artifact
	err := s.db.QueryRow(
		`SELECT id, job_id, path, bytes, COALESCE(mtime,0) FROM artifacts WHERE job_id = ? AND path = ?`,
		jobID, path,
	).Scan(&a.ID, &a.JobID, &a.Path, &a.Bytes, &a.Mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get artifact %s for job %d: %w", path, jobID, err)
	}
	return &a, nil
}

// ListArtifacts returns job_id's artifacts ordered by path.
func (s *Store) ListArtifacts(jobID int64) ([]Artifact, error) {
	rows, err := s.db.Query(
		`SELECT id, job_id, path, bytes, COALESCE(mtime,0) FROM artifacts WHERE job_id = ? ORDER BY path`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list artifacts for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []Artifact
	for rows.Next() {
		var a Artifact
		if err := rows.Scan(&a.ID, &a.JobID, &a.Path, &a.Bytes, &a.Mtime); err != nil {
			return nil, fmt.Errorf("list artifacts for job %d: %w", jobID, err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}
