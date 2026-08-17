package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// background.go — the web board's one optional background image (schema
// v26's board_bg). A singleton row: SetBoardBackground replaces whatever is
// there, DeleteBoardBackground restores the plain-board default, and the
// board payload reads only the updated_at stamp (BoardBackgroundUpdatedAt)
// so the multi-megabyte blob never rides the 2s-cached board rebuild.

// BoardBackground is the stored image: its sniffed MIME type, the bytes as
// uploaded, and the upload time (unix millis — the wire's cache-buster).
type BoardBackground struct {
	Mime      string
	Bytes     []byte
	UpdatedAt int64
}

// SetBoardBackground stores data as THE board background, replacing any
// previous one, and returns the updated_at stamp it wrote. The stamp is
// STRICTLY greater than the replaced row's, not just time.Now(): the stamp
// is the served image's only cache key behind a year-long Cache-Control
// (?v=<stamp>, api/background.go), so a replace landing on the same
// millisecond — or a backwards clock step — would reuse a URL the browser
// has pinned, sticking the old image with no recovery short of another
// upload. The floor is the LIVE row's stamp, so the guarantee is
// per-generation: DeleteBoardBackground removes the row and the next upload
// starts from bare time.Now() again.
func (s *Store) SetBoardBackground(mime string, data []byte) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("set board background: %w", err)
	}
	defer tx.Rollback()
	var prev int64
	err = tx.QueryRow(`SELECT COALESCE(updated_at,0) FROM board_bg WHERE id = 1`).Scan(&prev)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("set board background: %w", err)
	}
	now := time.Now().UnixMilli()
	if now <= prev {
		now = prev + 1
	}
	if _, err := tx.Exec(
		`INSERT INTO board_bg (id, mime, updated_at, bytes) VALUES (1, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET mime = excluded.mime, updated_at = excluded.updated_at, bytes = excluded.bytes`,
		mime, now, data,
	); err != nil {
		return 0, fmt.Errorf("set board background: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("set board background: %w", err)
	}
	return now, nil
}

// GetBoardBackground fetches the background, or ErrNotFound when none is
// set.
func (s *Store) GetBoardBackground() (*BoardBackground, error) {
	var bg BoardBackground
	err := s.db.QueryRow(
		`SELECT COALESCE(mime,''), bytes, COALESCE(updated_at,0) FROM board_bg WHERE id = 1`,
	).Scan(&bg.Mime, &bg.Bytes, &bg.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get board background: %w", err)
	}
	return &bg, nil
}

// BoardBackgroundUpdatedAt is the board payload's read: the updated_at stamp
// alone, or ErrNotFound when none is set — never the blob, which would put
// megabytes inside a board rebuild that runs every couple of seconds.
func (s *Store) BoardBackgroundUpdatedAt() (int64, error) {
	var at int64
	err := s.db.QueryRow(`SELECT COALESCE(updated_at,0) FROM board_bg WHERE id = 1`).Scan(&at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("board background updated_at: %w", err)
	}
	return at, nil
}

// DeleteBoardBackground removes the background — the plain-board default.
// Idempotent: deleting when none is set is a no-op, not an error.
func (s *Store) DeleteBoardBackground() error {
	if _, err := s.db.Exec(`DELETE FROM board_bg WHERE id = 1`); err != nil {
		return fmt.Errorf("delete board background: %w", err)
	}
	return nil
}
