package store

import (
	"database/sql"
	"fmt"
	"time"
)

// eventColumns COALESCEs every plain-scanned column defensively — belt and
// suspenders alongside the NOT NULL DEFAULT columns in schema.go, so a NULL
// that sneaks in degrades to a blank field rather than erroring ListEvents.
const eventColumns = `id, COALESCE(ts,0), COALESCE(src,''), COALESCE(level,''),
	COALESCE(event,''), COALESCE(msg,''), COALESCE(job_slug,'')`

// Event is one logged occurrence — the events table's replacement for
// kraken's JSONL log lines. JobSlug is optional (empty means not tied to a
// job, e.g. a daemon-start line).
type Event struct {
	ID      int64
	TS      int64
	Src     string
	Level   string
	Event   string
	Msg     string
	JobSlug string
}

// maxEvents caps the events table at its newest N rows (jobs/store.go's
// UseStore doc comment): unlike the JSONL files (size-rotated,
// oldest simply falls off ListLogFiles' pagination), nothing else bounds
// this table's growth once util.LogEvent's sink feeds every call into it.
// A var, not a const, so tests can shrink it rather than insert 50,000+ rows
// to exercise pruning.
var maxEvents = 50000

// pruneCheckEvery amortizes the prune's extra query+delete across inserts:
// only 1 in every pruneCheckEvery appends pays for it, gated on the
// assigned row id (monotonic even across deletes) rather than a separate
// counter. Also a var for the same test-shrinking reason as maxEvents.
var pruneCheckEvery int64 = 1000

// AppendEvent inserts one event row, stamping TS to now if the caller left
// it zero, and returns the assigned id. job_slug is NOT NULL DEFAULT
// (empty string) in the schema, so an unset JobSlug is stored as an empty
// string rather than NULL — same meaning ("not tied to a job"), no
// sentinel NULL required on the way in. Amortized pruning (see
// pruneCheckEvery) keeps the table bounded at maxEvents rows without
// costing every single insert a second statement.
func (s *Store) AppendEvent(e Event) (int64, error) {
	if e.TS == 0 {
		e.TS = time.Now().UnixMilli()
	}
	res, err := s.db.Exec(
		`INSERT INTO events (ts, src, level, event, msg, job_slug) VALUES (?, ?, ?, ?, ?, ?)`,
		e.TS, e.Src, e.Level, e.Event, e.Msg, e.JobSlug,
	)
	if err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("append event: %w", err)
	}
	if pruneCheckEvery > 0 && id%pruneCheckEvery == 0 {
		if err := s.pruneEvents(); err != nil {
			return id, fmt.Errorf("append event: prune: %w", err)
		}
	}
	return id, nil
}

// pruneEvents deletes every event row older than the newest maxEvents,
// keyed on id (monotonic insertion order) rather than ts (wall-clock,
// caller-suppliable, not guaranteed monotonic). A cheap single statement —
// no per-row scan — called from inside AppendEvent (see pruneCheckEvery) so
// no separate scheduler goroutine is needed.
func (s *Store) pruneEvents() error {
	_, err := s.db.Exec(
		`DELETE FROM events WHERE id <= (SELECT COALESCE(MAX(id), 0) FROM events) - ?`,
		maxEvents,
	)
	if err != nil {
		return fmt.Errorf("prune events: %w", err)
	}
	return nil
}

// ListEvents returns up to limit events, newest (highest id) first,
// paginated by beforeID: pass 0 for the first page, then the last returned
// id to fetch the next page — the viewer's file-by-file walk-backwards
// pattern, now id-by-id.
func (s *Store) ListEvents(limit int, beforeID int64) ([]Event, error) {
	if limit <= 0 {
		limit = 100
	}
	var rows *sql.Rows
	var err error
	if beforeID > 0 {
		rows, err = s.db.Query(
			`SELECT `+eventColumns+` FROM events WHERE id < ? ORDER BY id DESC LIMIT ?`,
			beforeID, limit,
		)
	} else {
		rows, err = s.db.Query(
			`SELECT `+eventColumns+` FROM events ORDER BY id DESC LIMIT ?`,
			limit,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.TS, &e.Src, &e.Level, &e.Event, &e.Msg, &e.JobSlug); err != nil {
			return nil, fmt.Errorf("list events: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
