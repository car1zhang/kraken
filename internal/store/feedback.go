package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Feedback is one doc-feedback entry: a frozen artifact snapshot plus the
// user's plaintext note. Rows are the only feedback store.
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
// owned by the NIGHTLY DREAM PASS — only AddressFeedback ever writes them —
// and hold that pass' free-text account of the durable fix it shipped for the
// note plus a server-stamped RFC3339 timestamp, not a boolean. An ordinary
// cell's zeus reads its feedback and acts on it but never writes this column
// (internal/jobs/crew.go's brief states the prohibition), so an empty
// Addressed means "no durable fix has shipped for this note" rather than
// "nobody handled it" — the two came apart deliberately, and every reader that
// renders a count off this column has to say the first thing, not the second.
// Rows predating that split are the exception and cannot be migrated: most
// carry an OWNING CELL's first-person account written under the retired rule
// (on this machine, 5 of the 7 non-empty ones), and a wipe would take the
// genuine durable-fix closures with them. So the column reads honestly going
// forward and approximately backwards, which is the trade a wipe would invert.
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

// FeedbackSince returns every feedback row created at or after sinceMS,
// across ALL jobs, oldest first. Snapshot blobs are excluded (like
// ListFeedbackMeta) — the dream loop's nightly sweep only needs metadata, and
// dragging every frozen artifact snapshot in the window through a sweep that
// never reads them would be pure waste.
//
// "Arrived in the window" means CREATED in the window, not touched in it: ts
// is immutable after insert — UpdateFeedbackNote deliberately rewrites
// note/snapshot and leaves ts alone — so editing an old note does not make it
// look new to a later sweep, and a row is swept exactly once by consecutive
// non-overlapping windows.
//
// The read is UNSCOPED, so a NULL-job_id fossil (a pre-v5 row the migration
// couldn't match, or a row orphaned by DeleteJob's ON DELETE SET NULL) still
// surfaces here — the same rows ListFeedback(0) returns and a job_id-scoped
// call never can. That is deliberate: the note is worth mining whether or not
// its job still exists. The comparison is `>= sinceMS`, so a row stamped
// exactly at the floor is IN the window (a caller sweeping consecutive
// windows should advance its floor past the last one it consumed).
//
// schemaV8's feedback_ts index is what makes this a seek rather than a full
// scan: schemaV1's only feedback index leads with job_slug, which an unscoped
// ts predicate cannot use. The predicate is a BARE `ts >= ?` rather than the
// COALESCE(ts,0) this file's other reads use in their SELECT lists, and that
// asymmetry is deliberate: ts is NOT NULL DEFAULT 0 so a NULL isn't reachable,
// and wrapping the indexed column in a function would make the index
// unusable — turning the sweep back into the full table scan feedback_ts was
// added to avoid. Defensive COALESCE belongs in the projection, never in the
// predicate that has to hit an index.
func (s *Store) FeedbackSince(sinceMS int64) ([]Feedback, error) {
	rows, err := s.db.Query(
		`SELECT id, job_id, COALESCE(job_slug,''), COALESCE(artifact,''), NULL,
			COALESCE(snapshot_ext,''), COALESCE(note,''), COALESCE(ts,0),
			COALESCE(session,''), COALESCE(addressed,''), COALESCE(addressed_at,'')
			FROM feedback WHERE ts >= ? ORDER BY ts, id`,
		sinceMS,
	)
	if err != nil {
		return nil, fmt.Errorf("list feedback since %d: %w", sinceMS, err)
	}
	defer rows.Close()
	var out []Feedback
	for rows.Next() {
		f, err := scanFeedback(rows)
		if err != nil {
			return nil, fmt.Errorf("list feedback since %d: %w", sinceMS, err)
		}
		out = append(out, *f)
	}
	return out, rows.Err()
}

// CountFeedbackSince is FeedbackSince's cheap sibling: how many rows the
// window holds, without materializing any of them. Same window semantics
// (created-in-window, unscoped, floor inclusive) — it is the count of exactly
// the rows FeedbackSince would return, so a caller can decide whether a sweep
// is worth running at all before paying for one.
func (s *Store) CountFeedbackSince(sinceMS int64) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM feedback WHERE ts >= ?`, sinceMS).Scan(&n); err != nil {
		return 0, fmt.Errorf("count feedback since %d: %w", sinceMS, err)
	}
	return n, nil
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

// AddressFeedback updates the addressed/addressed_at columns only (the
// `kraken feedback addressed` verb, which ONLY the nightly dream pass runs —
// see Feedback's doc comment) — note/snapshot/session/ts are left untouched.
// Errors with ErrNotFound when id doesn't exist.
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
