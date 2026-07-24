package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Feedback is one doc-feedback entry: a frozen artifact snapshot plus the
// user's plaintext note — mirrors what jobs/feedback.go wrote under
// ~/Documents/pluto/doc-feedback/ before rows became the only store.
// JobID (schema v5) is the row's immutable owning-job id and is now the KEY
// every read (ListFeedback/ListFeedbackMeta) and ownership check
// (api.lookupOwnedFeedback) scopes on, exactly like ArchiveJob/UnarchiveJob
// already rekey JobSlug by matching on JobID — job_slug alone can't
// disambiguate ownership once a freed slug is reused by a new job (see
// ArchiveJob's doc comment): both generations carry the identical job_slug
// string for as long as the new one stays active. JobSlug remains a stored
// display/provenance field (rekeyed alongside archive/unarchive so it always
// reads correctly), never a lookup key. 0 means no resolvable owning job (a
// pre-v5 row the migration couldn't match, or a fossil left behind by a
// hard-deleted job — see DeleteJob); such a row can never match a job_id-
// scoped read (real job ids are AUTOINCREMENT from 1), only the unscoped
// full sweep. Session is zeus' Claude session id at submit time (j.Session,
// stamped by the API layer, not this package); Addressed/AddressedAt are
// zeus-owned — only AddressFeedback ever writes them — and hold zeus' own
// free-text account of how it addressed the note plus a server-stamped
// RFC3339 timestamp, not a boolean.
type Feedback struct {
	ID          int64
	JobID       int64
	JobSlug     string
	Artifact    string
	Snapshot    []byte
	SnapshotExt string
	Note        string
	TS          int64 // unix millis
	Session     string
	Addressed   string
	AddressedAt string
}

// feedbackColumns COALESCEs every plain-scanned column defensively, same
// belt-and-suspenders reasoning as jobColumns/eventColumns: the schema
// already declares these NOT NULL with an empty-string default, but a NULL
// that sneaks in anyway (schema drift, a hand-edited row, an old importer
// run predating this migration) must degrade to a blank field rather than
// error the whole query. job_id is genuinely nullable (see Feedback's doc
// comment), scanned via sql.NullInt64 below rather than COALESCEd to 0 in
// SQL, so it's left bare here like archived_at/archived_name in jobColumns.
const feedbackColumns = `id, job_id, COALESCE(job_slug,''), COALESCE(artifact,''), snapshot,
	COALESCE(snapshot_ext,''), COALESCE(note,''), COALESCE(ts,0),
	COALESCE(session,''), COALESCE(addressed,''), COALESCE(addressed_at,'')`

func scanFeedback(row interface{ Scan(...any) error }) (*Feedback, error) {
	var f Feedback
	var jobID sql.NullInt64
	if err := row.Scan(&f.ID, &jobID, &f.JobSlug, &f.Artifact, &f.Snapshot, &f.SnapshotExt,
		&f.Note, &f.TS, &f.Session, &f.Addressed, &f.AddressedAt); err != nil {
		return nil, err
	}
	f.JobID = jobID.Int64
	return &f, nil
}

// AddFeedback inserts one feedback entry, stamping TS to now if the caller
// left it zero, and returns the assigned id. Addressed/AddressedAt are
// deliberately not caller-settable here — they're written later, only via
// AddressFeedback — so a fresh entry always starts unaddressed. JobID left
// zero inserts NULL (job ids are AUTOINCREMENT-assigned starting at 1, so 0
// never collides with a real row) — matches "no resolvable owning job", the
// same state a pre-v5/orphaned row already carries.
func (s *Store) AddFeedback(f Feedback) (int64, error) {
	if f.TS == 0 {
		f.TS = time.Now().UnixMilli()
	}
	var jobID any
	if f.JobID != 0 {
		jobID = f.JobID
	}
	res, err := s.db.Exec(
		`INSERT INTO feedback (job_id, job_slug, artifact, snapshot, snapshot_ext, note, ts, session)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		jobID, f.JobSlug, f.Artifact, f.Snapshot, f.SnapshotExt, f.Note, f.TS, f.Session,
	)
	if err != nil {
		return 0, fmt.Errorf("add feedback for %s: %w", f.JobSlug, err)
	}
	return res.LastInsertId()
}

// GetFeedback looks up one feedback entry by id, or ErrNotFound when no row
// matches.
func (s *Store) GetFeedback(id int64) (*Feedback, error) {
	row := s.db.QueryRow(`SELECT `+feedbackColumns+` FROM feedback WHERE id = ?`, id)
	f, err := scanFeedback(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get feedback %d: %w", id, err)
	}
	return f, nil
}

// ListFeedback returns feedback entries owned by jobID, oldest first; jobID
// <= 0 returns every entry regardless of ownership (the /dream nightly
// sweep's use case, and carl's own board sweep for a full read) — job ids
// are AUTOINCREMENT-assigned starting at 1, so 0 (or any non-positive value)
// never collides with a real row, same convention AddFeedback's JobID
// handling already relies on. Scoped on job_id, NOT job_slug (see Feedback's
// doc comment): job_slug is a mutable display string that a freed slug's
// next job can reuse verbatim, so a job_slug-keyed read can't tell two
// generations of the same slug apart the moment both exist — job_id is each
// row's immutable owner and is collision-immune regardless of any job_slug
// coincidence. A NULL job_id (a pre-v5 fossil, or a row orphaned by
// DeleteJob's ON DELETE SET NULL) can never equal a positive jobID, so such
// a row never matches a scoped call — it only ever surfaces via the jobID
// <= 0 full sweep.
func (s *Store) ListFeedback(jobID int64) ([]Feedback, error) {
	query := `SELECT ` + feedbackColumns + ` FROM feedback`
	args := []any{}
	if jobID > 0 {
		query += ` WHERE job_id = ?`
		args = append(args, jobID)
	}
	query += ` ORDER BY ts`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list feedback for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, fmt.Errorf("list feedback for job %d: %w", jobID, err)
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// ListFeedbackMeta is ListFeedback minus the snapshot blob: the board
// payload only needs (id, artifact, note, ts, session, addressed,
// addressed_at), and the board polls it per job on a tick — dragging every
// row's frozen snapshot bytes through each poll would be pure waste. The
// returned entries' Snapshot is always nil; callers needing bytes use
// GetFeedback/ListFeedback. Same job_id-scoping and ordering as ListFeedback
// (jobID <= 0 for the full sweep; a NULL job_id row never matches a scoped
// call — see ListFeedback's doc comment).
func (s *Store) ListFeedbackMeta(jobID int64) ([]Feedback, error) {
	query := `SELECT id, job_id, COALESCE(job_slug,''), COALESCE(artifact,''), NULL,
		COALESCE(snapshot_ext,''), COALESCE(note,''), COALESCE(ts,0),
		COALESCE(session,''), COALESCE(addressed,''), COALESCE(addressed_at,'') FROM feedback`
	args := []any{}
	if jobID > 0 {
		query += ` WHERE job_id = ?`
		args = append(args, jobID)
	}
	query += ` ORDER BY ts`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list feedback meta for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, fmt.Errorf("list feedback meta for job %d: %w", jobID, err)
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// UpdateFeedbackNote edits an existing entry's note and re-freezes its
// snapshot (Q4: an edit captures the artifact as CURRENTLY served, not the
// original submit-time bytes) — session/addressed/addressed_at/ts are left
// untouched. Errors with ErrNotFound when id doesn't exist.
func (s *Store) UpdateFeedbackNote(id int64, note string, snapshot []byte, snapshotExt string) error {
	res, err := s.db.Exec(
		`UPDATE feedback SET note = ?, snapshot = ?, snapshot_ext = ? WHERE id = ?`,
		note, snapshot, snapshotExt, id,
	)
	if err != nil {
		return fmt.Errorf("update feedback %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update feedback %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("update feedback %d: %w", id, ErrNotFound)
	}
	return nil
}

// AddressFeedback updates the zeus-owned addressed/addressed_at columns
// only (zeus' `kraken feedback addressed` verb) — note/snapshot/session/ts
// are left untouched. Errors with ErrNotFound when id doesn't exist.
func (s *Store) AddressFeedback(id int64, addressed, addressedAt string) error {
	res, err := s.db.Exec(
		`UPDATE feedback SET addressed = ?, addressed_at = ? WHERE id = ?`,
		addressed, addressedAt, id,
	)
	if err != nil {
		return fmt.Errorf("address feedback %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("address feedback %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("address feedback %d: %w", id, ErrNotFound)
	}
	return nil
}

// DeleteFeedback permanently removes a feedback entry (Q9: carl can delete
// from the board). Errors with ErrNotFound when id doesn't exist.
func (s *Store) DeleteFeedback(id int64) error {
	res, err := s.db.Exec(`DELETE FROM feedback WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete feedback %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete feedback %d: %w", id, err)
	}
	if n == 0 {
		return fmt.Errorf("delete feedback %d: %w", id, ErrNotFound)
	}
	return nil
}
