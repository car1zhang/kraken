package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Draft is one job's in-progress message text, keyed by job slug (durable
// across board reloads — the thing the file store never had a good home
// for).
type Draft struct {
	JobSlug   string
	Text      string
	UpdatedAt int64 // unix millis
}

// GetDraft fetches jobSlug's draft, or ErrNotFound if none is saved.
func (s *Store) GetDraft(jobSlug string) (*Draft, error) {
	var d Draft
	err := s.db.QueryRow(
		`SELECT job_slug, COALESCE(text,''), COALESCE(updated_at,0) FROM drafts WHERE job_slug = ?`,
		jobSlug,
	).Scan(&d.JobSlug, &d.Text, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get draft for %s: %w", jobSlug, err)
	}
	return &d, nil
}

// SetDraft upserts jobSlug's draft text, stamping updated_at to now.
func (s *Store) SetDraft(jobSlug, text string) error {
	if _, err := s.db.Exec(
		`INSERT INTO drafts (job_slug, text, updated_at) VALUES (?, ?, ?)
			ON CONFLICT(job_slug) DO UPDATE SET text = excluded.text, updated_at = excluded.updated_at`,
		jobSlug, text, time.Now().UnixMilli(),
	); err != nil {
		return fmt.Errorf("set draft for %s: %w", jobSlug, err)
	}
	return nil
}

// DeleteDraft removes jobSlug's draft, if any (a no-op, not an error, when
// none exists — mirrors a sent/discarded draft's cleanup).
func (s *Store) DeleteDraft(jobSlug string) error {
	if _, err := s.db.Exec(`DELETE FROM drafts WHERE job_slug = ?`, jobSlug); err != nil {
		return fmt.Errorf("delete draft for %s: %w", jobSlug, err)
	}
	return nil
}
