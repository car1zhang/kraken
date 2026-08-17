package store

import (
	"errors"
	"testing"
)

// background_test.go — the board_bg singleton (schema v26, background.go):
// set replaces, get/updated-at agree, delete restores the no-background
// default and is idempotent.

func TestBoardBackgroundUnsetIsNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.GetBoardBackground(); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBoardBackground on a fresh db: err = %v, want ErrNotFound", err)
	}
	if _, err := s.BoardBackgroundUpdatedAt(); !errors.Is(err, ErrNotFound) {
		t.Errorf("BoardBackgroundUpdatedAt on a fresh db: err = %v, want ErrNotFound", err)
	}
}

func TestBoardBackgroundSetGetReplace(t *testing.T) {
	s := openTest(t)

	at1, err := s.SetBoardBackground("image/png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("SetBoardBackground: %v", err)
	}
	if at1 == 0 {
		t.Error("SetBoardBackground returned updated_at 0")
	}
	bg, err := s.GetBoardBackground()
	if err != nil {
		t.Fatalf("GetBoardBackground: %v", err)
	}
	if bg.Mime != "image/png" || string(bg.Bytes) != "png-bytes" || bg.UpdatedAt != at1 {
		t.Errorf("got %q/%q/%d, want image/png / png-bytes / %d", bg.Mime, bg.Bytes, bg.UpdatedAt, at1)
	}
	if at, err := s.BoardBackgroundUpdatedAt(); err != nil || at != at1 {
		t.Errorf("BoardBackgroundUpdatedAt = %d, %v, want %d, nil", at, err, at1)
	}

	// A second set REPLACES the singleton rather than adding a row or
	// erroring — and its stamp is STRICTLY greater than the replaced row's,
	// even inside the same millisecond: the stamp is the served image's only
	// cache key (see SetBoardBackground's comment), so a reused stamp is a
	// reused URL a browser may have pinned for a year.
	at2, err := s.SetBoardBackground("image/jpeg", []byte("jpg-bytes"))
	if err != nil {
		t.Fatalf("SetBoardBackground (replace): %v", err)
	}
	if at2 <= at1 {
		t.Errorf("replace stamp %d is not strictly greater than %d", at2, at1)
	}
	bg, err = s.GetBoardBackground()
	if err != nil {
		t.Fatalf("GetBoardBackground after replace: %v", err)
	}
	if bg.Mime != "image/jpeg" || string(bg.Bytes) != "jpg-bytes" || bg.UpdatedAt != at2 {
		t.Errorf("after replace got %q/%q/%d, want image/jpeg / jpg-bytes / %d", bg.Mime, bg.Bytes, bg.UpdatedAt, at2)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM board_bg`).Scan(&n); err != nil || n != 1 {
		t.Errorf("board_bg rows = %d (%v), want exactly 1 — the table is a singleton", n, err)
	}
}

func TestBoardBackgroundDeleteIsIdempotent(t *testing.T) {
	s := openTest(t)
	if err := s.DeleteBoardBackground(); err != nil {
		t.Fatalf("DeleteBoardBackground with nothing set: %v", err)
	}
	if _, err := s.SetBoardBackground("image/png", []byte("x")); err != nil {
		t.Fatalf("SetBoardBackground: %v", err)
	}
	if err := s.DeleteBoardBackground(); err != nil {
		t.Fatalf("DeleteBoardBackground: %v", err)
	}
	if _, err := s.GetBoardBackground(); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetBoardBackground after delete: err = %v, want ErrNotFound", err)
	}
}
