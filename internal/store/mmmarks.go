package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// mmmarks.go — schema v39's mm_marks (schema.go): passages the operator marked
// as read with the board's ✓ read button, waiting for a mind run to read them.
//
// A mark is pending (consumed_run 0) until a run carries it in its bundle,
// and then consumed exactly once: ConsumeMMMarks only stamps pending rows, so
// a second run that somehow held the same id cannot claim it. A consumed
// mark is history and cannot be removed; a pending one can.

// MMMark is one mm_marks row. JobID 0 is a mark with no job (a library
// snapshot, a Model-pane row). ConsumedRun 0 means pending.
type MMMark struct {
	ID, JobID                          int64
	Slug, Text, SourceJSON             string
	CreatedAt, ConsumedRun, ConsumedAt int64
}

// ErrMMMarkConsumed refuses RemoveMMMark on a mark a run already read.
var ErrMMMarkConsumed = errors.New("mm mark is already consumed by a mind run")

// mmMarkFoldMS is the fold window: the same passage marked twice within a
// minute (a double press, two boards) is one mark.
const mmMarkFoldMS = 60_000

const mmMarkColumns = `SELECT id, job_id, slug, text, source_json, created_at, consumed_run, consumed_at FROM mm_marks`

func scanMMMark(row interface{ Scan(...any) error }) (MMMark, error) {
	var m MMMark
	err := row.Scan(&m.ID, &m.JobID, &m.Slug, &m.Text, &m.SourceJSON, &m.CreatedAt, &m.ConsumedRun, &m.ConsumedAt)
	return m, err
}

// AddMMMark stores a pending mark and returns its id, or — when a pending
// mark of the same job with byte-equal text and source was created within a
// minute of m.CreatedAt — returns that mark's id with folded true and writes
// nothing. Empty text is refused.
func (s *Store) AddMMMark(m MMMark) (int64, bool, error) {
	if strings.TrimSpace(m.Text) == "" {
		return 0, false, errors.New("mm mark text is empty")
	}
	var existing int64
	err := s.db.QueryRow(
		`SELECT id FROM mm_marks WHERE job_id = ? AND text = ? AND source_json = ? AND consumed_run = 0
			AND created_at >= ? AND created_at <= ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		m.JobID, m.Text, m.SourceJSON, m.CreatedAt-mmMarkFoldMS, m.CreatedAt+mmMarkFoldMS,
	).Scan(&existing)
	if err == nil {
		return existing, true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, false, fmt.Errorf("add mm mark: %w", err)
	}
	res, err := s.db.Exec(
		`INSERT INTO mm_marks (job_id, slug, text, source_json, created_at) VALUES (?, ?, ?, ?, ?)`,
		m.JobID, m.Slug, m.Text, m.SourceJSON, m.CreatedAt,
	)
	if err != nil {
		return 0, false, fmt.Errorf("add mm mark: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("add mm mark: %w", err)
	}
	return id, false, nil
}

// ListMMMarks returns marks newest first (created_at, then id). jobID 0 lists
// every mark, job-less ones included; pendingOnly keeps unconsumed marks.
func (s *Store) ListMMMarks(jobID int64, pendingOnly bool) ([]MMMark, error) {
	query := mmMarkColumns
	var where []string
	var args []any
	if jobID != 0 {
		where = append(where, `job_id = ?`)
		args = append(args, jobID)
	}
	if pendingOnly {
		where = append(where, `consumed_run = 0`)
	}
	if len(where) > 0 {
		query += ` WHERE ` + strings.Join(where, ` AND `)
	}
	query += ` ORDER BY created_at DESC, id DESC`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list mm marks: %w", err)
	}
	defer rows.Close()
	out := []MMMark{}
	for rows.Next() {
		m, err := scanMMMark(rows)
		if err != nil {
			return nil, fmt.Errorf("list mm marks: %w", err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list mm marks: %w", err)
	}
	return out, nil
}

// ConsumeMMMarks stamps runID and at on every listed mark that is still
// pending. A mark already consumed keeps its first run; an unknown id is
// ignored.
func (s *Store) ConsumeMMMarks(ids []int64, runID, at int64) error {
	if len(ids) == 0 {
		return nil
	}
	ph := make([]string, len(ids))
	args := []any{runID, at}
	for i, id := range ids {
		ph[i] = "?"
		args = append(args, id)
	}
	if _, err := s.db.Exec(
		`UPDATE mm_marks SET consumed_run = ?, consumed_at = ?
			WHERE consumed_run = 0 AND id IN (`+strings.Join(ph, ",")+`)`, args...,
	); err != nil {
		return fmt.Errorf("consume mm marks: %w", err)
	}
	return nil
}

// RemoveMMMark deletes a pending mark. ErrMMMarkConsumed when a run already
// read it; ErrNotFound when the id names nothing.
func (s *Store) RemoveMMMark(id int64) error {
	res, err := s.db.Exec(`DELETE FROM mm_marks WHERE id = ? AND consumed_run = 0`, id)
	if err != nil {
		return fmt.Errorf("remove mm mark %d: %w", id, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("remove mm mark %d: %w", id, err)
	}
	if n > 0 {
		return nil
	}
	var consumed int64
	err = s.db.QueryRow(`SELECT consumed_run FROM mm_marks WHERE id = ?`, id).Scan(&consumed)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("remove mm mark %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("remove mm mark %d: %w", id, err)
	}
	return fmt.Errorf("remove mm mark %d: %w", id, ErrMMMarkConsumed)
}
