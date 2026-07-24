package store

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestQuestionsRoundTrip(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "questions-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if _, err := s.GetQuestions(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetQuestions on empty: want ErrNotFound, got %v", err)
	}

	if err := s.UpsertQuestions(id, []byte("- q: one\n"), 100); err != nil {
		t.Fatalf("UpsertQuestions 1: %v", err)
	}
	q, err := s.GetQuestions(id)
	if err != nil {
		t.Fatalf("GetQuestions 1: %v", err)
	}
	if !bytes.Equal(q.Bytes, []byte("- q: one\n")) || q.Mtime != 100 || q.JobID != id {
		t.Fatalf("unexpected row: %+v", q)
	}

	// overwrite
	if err := s.UpsertQuestions(id, []byte("- q: two\n"), 200); err != nil {
		t.Fatalf("UpsertQuestions 2: %v", err)
	}
	q, err = s.GetQuestions(id)
	if err != nil {
		t.Fatalf("GetQuestions 2: %v", err)
	}
	if !bytes.Equal(q.Bytes, []byte("- q: two\n")) || q.Mtime != 200 {
		t.Fatalf("row not overwritten: %+v", q)
	}

	if err := s.DeleteQuestions(id); err != nil {
		t.Fatalf("DeleteQuestions: %v", err)
	}
	if _, err := s.GetQuestions(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetQuestions after delete: want ErrNotFound, got %v", err)
	}

	// DeleteQuestions on a job with no row is a no-op, not an error
	if err := s.DeleteQuestions(id); err != nil {
		t.Fatalf("DeleteQuestions on missing row: %v", err)
	}
}

// TestQuestionsEmptyBytesRoundTrip pins the contract called out in
// UpsertQuestions/GetQuestions' doc comments: a present-but-empty
// questions.yaml (zeus created the file but hasn't written any questions
// yet) upserts as a real row with zero-length Bytes, NOT as ErrNotFound.
// Callers (the sync layer, the API) must treat empty Bytes the same as "no
// questions" without confusing it for "never synced" — that distinction
// only matters at the row-existence level, which this test pins.
func TestQuestionsEmptyBytesRoundTrip(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "questions-empty"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	if err := s.UpsertQuestions(id, []byte{}, 42); err != nil {
		t.Fatalf("UpsertQuestions empty: %v", err)
	}
	q, err := s.GetQuestions(id)
	if err != nil {
		t.Fatalf("GetQuestions after empty upsert: want a row, got err: %v", err)
	}
	if len(q.Bytes) != 0 {
		t.Fatalf("want zero-length Bytes, got %q", q.Bytes)
	}
	if q.Mtime != 42 {
		t.Fatalf("want Mtime 42, got %d", q.Mtime)
	}
}

// TestSchemaV1ToV2MigratesQuestionsTable covers finding #1: a real
// kraken.db can exist at PRAGMA user_version = 1 (schemaV1's tables only,
// no questions table) from before the doc-questions feature shipped.
// Reopening such a database must apply migrations[2] and create the
// questions table rather than silently leaving it missing (migrate() only
// applies versions above the database's CURRENT version, so an in-place
// edit to schemaV1 — the bug this test guards against — would never run
// against an existing v1 database).
func TestSchemaV1ToV2MigratesQuestionsTable(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the
	// real re-Open below, rather than only ever calling s.migrate() in
	// process.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Roll this fresh (already-migrated-to-v3) database back to a
	// v1-shaped one: drop the questions table schemaV2 created, re-add the
	// jobs.type column migrations[3] (Q14) drops, and reset user_version to
	// 1, faithfully reproducing what a real pre-questions, pre-Q14
	// kraken.db looks like on disk.
	if _, err := s.db.Exec(`DROP TABLE questions`); err != nil {
		t.Fatalf("drop questions: %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN type TEXT NOT NULL DEFAULT 'build'`); err != nil {
		t.Fatalf("re-add type column: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v1: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// the questions table must exist and be usable now.
	id, err := s.CreateJob(&Job{Slug: "post-migrate"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertQuestions(id, []byte("- q: one\n"), 1); err != nil {
		t.Fatalf("UpsertQuestions after migration: %v", err)
	}
	q, err := s.GetQuestions(id)
	if err != nil {
		t.Fatalf("GetQuestions after migration: %v", err)
	}
	if !bytes.Equal(q.Bytes, []byte("- q: one\n")) {
		t.Fatalf("unexpected row after migration: %+v", q)
	}

	// Fidelity check (finding #7): the assertions above all ran an
	// in-process s.migrate() call directly — they don't prove a genuinely
	// fresh process opening a v1-shaped kraken.db off disk (the real-world
	// scenario) gets the same treatment through the actual Open() entry
	// point. Roll the same on-disk file back to v1-shaped again, close it,
	// and re-Open it for real.
	if _, err := s.db.Exec(`DROP TABLE questions`); err != nil {
		t.Fatalf("drop questions (2nd downgrade): %v", err)
	}
	if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN type TEXT NOT NULL DEFAULT 'build'`); err != nil {
		t.Fatalf("re-add type column (2nd downgrade): %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("reset user_version (2nd downgrade): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v1-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	id2, err := s2.CreateJob(&Job{Slug: "post-reopen"})
	if err != nil {
		t.Fatalf("CreateJob after re-Open: %v", err)
	}
	if err := s2.UpsertQuestions(id2, []byte("- q: two\n"), 2); err != nil {
		t.Fatalf("UpsertQuestions after re-Open: %v", err)
	}
	q2, err := s2.GetQuestions(id2)
	if err != nil {
		t.Fatalf("GetQuestions after re-Open: %v", err)
	}
	if !bytes.Equal(q2.Bytes, []byte("- q: two\n")) {
		t.Fatalf("unexpected row after re-Open migration: %+v", q2)
	}
}
