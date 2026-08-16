package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// saved.go — schema v24's saved_artifacts table: the artifact LIBRARY. One
// row is one artifact carl asked to keep, frozen as the bytes it held at
// save time and addressed thereafter by its own row id rather than by the
// job it came from.
//
// Two properties shape every accessor below, and both are about SURVIVAL.
// The row carries no foreign key onto jobs (schemaV24), so a library entry
// outlives teardown, archiving and the hard delete alike — which is why the
// slug is STORED here rather than joined at read time: after the job row is
// gone there is nothing left to join against, and a library that emptied
// itself when carl tidied up would be the opposite of a library. And the
// snapshot is a COPY, never a pointer at the live file, so an agent
// rewriting the artifact in place cannot change what carl saved. Both are
// exactly feedback.go's freeze, for exactly its reasons.
//
// The reads are split by whether they need the bytes — ListSavedArtifacts
// reads LENGTH(snapshot) and never the blob itself, GetSavedArtifact reads
// one whole row — the same reader split ListFeedbackMeta and GetFeedback
// draw, and for the same reason: the list is what a view renders on every
// poll and the bytes are what exactly one click asks for.

// SavedArtifact is one saved_artifacts row, bytes included.
//
// Tagless, like every other store row type: the wire shape is the API
// layer's own DTO (internal/api/library.go), a deliberate, independently
// evolvable contract rather than whatever this struct happens to carry.
type SavedArtifact struct {
	ID    int64
	JobID int64
	// Slug is the DISPLAY slug as it stood at save time — identity is JobID.
	Slug string
	// Name is the artifact's path within its job, slashes intact.
	Name     string
	Snapshot []byte
	SavedAt  int64 // unix millis
}

// SavedArtifactMeta is one row WITHOUT its bytes: what a listing renders.
// Size is LENGTH(snapshot), so a caller can still say how big the saved copy
// is without the list read dragging every blob in the table through memory.
type SavedArtifactMeta struct {
	ID      int64
	JobID   int64
	Slug    string
	Name    string
	SavedAt int64
	Size    int64
}

// SaveArtifact freezes snapshot under (jobID, name) and returns the row id.
//
// It is an UPSERT on that pair, and the id is what makes the distinction
// visible: a re-save REPLACES the bytes, the slug and the timestamp of the
// row already there and hands back the SAME id, so a library entry an
// artifact viewer is currently pointing at stays the entry it was pointing
// at. Two rows for one artifact would give the save toggle two states to be
// in at once.
//
// Three statements in one transaction rather than an ON CONFLICT clause, the
// shape prs.go's UpsertPR also takes — here for a reason of its own: SQLite's
// last_insert_rowid() is not updated by the UPDATE arm of an upsert, so the
// id an upsert reports for a replace is whatever the connection inserted
// LAST — a different row entirely, or none. Reading the existing id first is
// the only form that answers the question this signature asks.
//
// savedAt is passed in rather than read from the clock here for the reason
// AddGoal's now is: the API layer stamps one time per request, and a test
// can write a row at a chosen instant without moving the machine's clock.
func (s *Store) SaveArtifact(jobID int64, slug, name string, snapshot []byte, savedAt int64) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("save artifact %s for job %d: %w", name, jobID, err)
	}
	defer tx.Rollback()

	var id int64
	err = tx.QueryRow(`SELECT id FROM saved_artifacts WHERE job_id = ? AND name = ?`, jobID, name).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, ierr := tx.Exec(
			`INSERT INTO saved_artifacts (job_id, slug, name, snapshot, saved_at) VALUES (?, ?, ?, ?, ?)`,
			jobID, slug, name, snapshot, savedAt,
		)
		if ierr != nil {
			return 0, fmt.Errorf("save artifact %s for job %d: %w", name, jobID, ierr)
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, fmt.Errorf("save artifact %s for job %d: %w", name, jobID, err)
		}
	case err != nil:
		return 0, fmt.Errorf("save artifact %s for job %d: %w", name, jobID, err)
	default:
		// The slug is re-written along with the bytes: a job renamed since the
		// last save is displayed under the name it has now, not the one it had.
		if _, uerr := tx.Exec(
			`UPDATE saved_artifacts SET slug = ?, snapshot = ?, saved_at = ? WHERE id = ?`,
			slug, snapshot, savedAt, id,
		); uerr != nil {
			return 0, fmt.Errorf("save artifact %s for job %d: %w", name, jobID, uerr)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("save artifact %s for job %d: %w", name, jobID, err)
	}
	return id, nil
}

// ListSavedArtifacts returns the whole library, newest first — saved_at
// descending, id descending as the tiebreak so two rows saved in the same
// millisecond still come back in a stable, most-recent-first order rather
// than in whatever order SQLite chose today.
//
// An empty library is not an error: it is the state every database is in
// before carl saves his first artifact.
func (s *Store) ListSavedArtifacts() ([]SavedArtifactMeta, error) {
	rows, err := s.db.Query(`SELECT id, job_id, COALESCE(slug,''), COALESCE(name,''),
		COALESCE(saved_at,0), LENGTH(snapshot) FROM saved_artifacts
		ORDER BY saved_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list saved artifacts: %w", err)
	}
	defer rows.Close()
	out := []SavedArtifactMeta{}
	for rows.Next() {
		var m SavedArtifactMeta
		if err := rows.Scan(&m.ID, &m.JobID, &m.Slug, &m.Name, &m.SavedAt, &m.Size); err != nil {
			return nil, fmt.Errorf("list saved artifacts: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list saved artifacts: %w", err)
	}
	return out, nil
}

// GetSavedArtifact reads one row whole, bytes included — the read behind
// serving a saved copy. ErrNotFound when the id names nothing, so the caller
// can answer 404 rather than 500 for a link to an entry carl has since
// removed.
func (s *Store) GetSavedArtifact(id int64) (*SavedArtifact, error) {
	var a SavedArtifact
	err := s.db.QueryRow(`SELECT id, job_id, COALESCE(slug,''), COALESCE(name,''),
		snapshot, COALESCE(saved_at,0) FROM saved_artifacts WHERE id = ?`, id).
		Scan(&a.ID, &a.JobID, &a.Slug, &a.Name, &a.Snapshot, &a.SavedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get saved artifact %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get saved artifact %d: %w", id, err)
	}
	return &a, nil
}

// DeleteSavedArtifact removes one row, ErrNotFound when there was none to
// remove.
//
// The opposite of DeleteGoal's no-op-on-absent contract, deliberately: the
// remove affordance sits on a row the caller is LOOKING at, so "that row was
// already gone" is news — it means the library view is stale — rather than
// the same outcome by another route. The caller turns it into a 404 and the
// next poll re-renders the truth.
func (s *Store) DeleteSavedArtifact(id int64) error {
	res, err := s.db.Exec(`DELETE FROM saved_artifacts WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete saved artifact %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete saved artifact %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("delete saved artifact %d: %w", id, ErrNotFound)
	}
	return nil
}
