package store

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"time"
)

// Upload is one per-job image upload — mirrors the img-NNN.<ext> files
// jobs/uploads.go writes today, now blob-canonical.
type Upload struct {
	ID        int64
	JobID     int64
	Name      string
	Mime      string
	Bytes     []byte
	CreatedAt int64 // unix millis
}

var uploadNameRe = regexp.MustCompile(`^img-(\d+)\.`)

// uploadExtRe bounds what an upload extension may look like — a shape
// check, not an allowlist, same idiom as jobs.modelTokenRe.
var uploadExtRe = regexp.MustCompile(`^[a-z0-9]{1,8}$`)

// SaveUpload stores data as the next sequential img-NNN.<ext> name for
// job_id, computed in-DB from existing upload names the same way
// jobs.nextUploadName scans the uploads dir today. ext must match
// uploadExtRe (letters/digits only) or this returns ErrInvalidExt.
func (s *Store) SaveUpload(jobID int64, ext, mime string, data []byte) (Upload, error) {
	if !uploadExtRe.MatchString(ext) {
		return Upload{}, fmt.Errorf("save upload for job %d: %w: %q", jobID, ErrInvalidExt, ext)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return Upload{}, fmt.Errorf("save upload for job %d: %w", jobID, err)
	}
	defer tx.Rollback()

	rows, err := tx.Query(`SELECT name FROM uploads WHERE job_id = ?`, jobID)
	if err != nil {
		return Upload{}, fmt.Errorf("save upload for job %d: %w", jobID, err)
	}
	highest := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return Upload{}, fmt.Errorf("save upload for job %d: %w", jobID, err)
		}
		if m := uploadNameRe.FindStringSubmatch(name); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil && n > highest {
				highest = n
			}
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return Upload{}, fmt.Errorf("save upload for job %d: %w", jobID, err)
	}
	rows.Close()

	u := Upload{
		JobID:     jobID,
		Name:      fmt.Sprintf("img-%03d.%s", highest+1, ext),
		Mime:      mime,
		Bytes:     data,
		CreatedAt: time.Now().UnixMilli(),
	}
	res, err := tx.Exec(
		`INSERT INTO uploads (job_id, name, mime, bytes, created_at) VALUES (?, ?, ?, ?, ?)`,
		u.JobID, u.Name, u.Mime, u.Bytes, u.CreatedAt,
	)
	if err != nil {
		return Upload{}, fmt.Errorf("save upload for job %d: %w", jobID, err)
	}
	if u.ID, err = res.LastInsertId(); err != nil {
		return Upload{}, fmt.Errorf("save upload for job %d: %w", jobID, err)
	}
	if err := tx.Commit(); err != nil {
		return Upload{}, fmt.Errorf("save upload for job %d: %w", jobID, err)
	}
	return u, nil
}

// GetUpload fetches one upload by job id + name.
func (s *Store) GetUpload(jobID int64, name string) (*Upload, error) {
	var u Upload
	err := s.db.QueryRow(
		`SELECT id, job_id, name, COALESCE(mime,''), bytes, COALESCE(created_at,0) FROM uploads WHERE job_id = ? AND name = ?`,
		jobID, name,
	).Scan(&u.ID, &u.JobID, &u.Name, &u.Mime, &u.Bytes, &u.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get upload %s for job %d: %w", name, jobID, err)
	}
	return &u, nil
}

// ListUploads returns job_id's uploads ordered by name (img-001, img-002, ...).
func (s *Store) ListUploads(jobID int64) ([]Upload, error) {
	rows, err := s.db.Query(
		`SELECT id, job_id, name, COALESCE(mime,''), bytes, COALESCE(created_at,0) FROM uploads WHERE job_id = ? ORDER BY name`,
		jobID,
	)
	if err != nil {
		return nil, fmt.Errorf("list uploads for job %d: %w", jobID, err)
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		var u Upload
		if err := rows.Scan(&u.ID, &u.JobID, &u.Name, &u.Mime, &u.Bytes, &u.CreatedAt); err != nil {
			return nil, fmt.Errorf("list uploads for job %d: %w", jobID, err)
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpsertUploadNamed writes one upload row under an exact, caller-supplied
// name — unlike SaveUpload, which always computes the next sequential
// img-NNN name from what's already there. The importer needs this because
// it's replaying uploads that already have their on-disk img-NNN.<ext> name
// and must preserve it exactly rather than renumber. Keyed on (job_id,
// name): a second call with the same pair updates mime/bytes/created_at in
// place rather than erroring or duplicating, so re-running the importer
// against an unchanged uploads dir is a no-op.
func (s *Store) UpsertUploadNamed(jobID int64, name, mime string, data []byte, createdAt int64) error {
	if _, err := s.db.Exec(
		`INSERT INTO uploads (job_id, name, mime, bytes, created_at) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(job_id, name) DO UPDATE SET mime = excluded.mime, bytes = excluded.bytes, created_at = excluded.created_at`,
		jobID, name, mime, data, createdAt,
	); err != nil {
		return fmt.Errorf("upsert upload %s for job %d: %w", name, jobID, err)
	}
	return nil
}

// DeleteUploadsForJob removes every upload row for job_id — the archive-time
// cleanup jobs.ArchiveJob's os.RemoveAll(UploadsDir(...)) performs today.
func (s *Store) DeleteUploadsForJob(jobID int64) error {
	if _, err := s.db.Exec(`DELETE FROM uploads WHERE job_id = ?`, jobID); err != nil {
		return fmt.Errorf("delete uploads for job %d: %w", jobID, err)
	}
	return nil
}
