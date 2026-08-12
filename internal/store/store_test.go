package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db file not created: %v", err)
	}
}

func TestOpenMigrateIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.CreateJob(&Job{Slug: "a"}); err != nil {
		t.Fatalf("seed CreateJob: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open (re-migrate): %v", err)
	}
	defer s2.Close()

	var version int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	j, err := s2.GetJobBySlug("a")
	if err != nil {
		t.Fatalf("data survived reopen: %v", err)
	}
	if j.Slug != "a" {
		t.Fatalf("job mangled across reopen: %+v", j)
	}
}

func TestPragmasApplied(t *testing.T) {
	s := openTest(t)
	var journalMode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if journalMode != "wal" {
		t.Errorf("journal_mode = %q, want wal", journalMode)
	}
	var fk int
	if err := s.db.QueryRow(`PRAGMA foreign_keys`).Scan(&fk); err != nil {
		t.Fatalf("read foreign_keys: %v", err)
	}
	if fk != 1 {
		t.Errorf("foreign_keys = %d, want 1", fk)
	}
	var busyTimeout int
	if err := s.db.QueryRow(`PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busyTimeout != 5000 {
		t.Errorf("busy_timeout = %d, want 5000", busyTimeout)
	}
}

func TestForeignKeyCascade(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "cascade"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertPR(id, "https://example.com/pr/1", "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if err := s.DeleteJob(id); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 0 {
		t.Errorf("prs not cascaded: %+v", prs)
	}
}

func TestBackupProducesOpenableDB(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateJob(&Job{Slug: "backup-me", Repo: "kraken"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	dest := filepath.Join(t.TempDir(), "backups", "kraken.db")
	if err := s.Backup(dest); err != nil {
		t.Fatalf("Backup: %v", err)
	}

	s2, err := Open(dest)
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer s2.Close()
	j, err := s2.GetJobBySlug("backup-me")
	if err != nil {
		t.Fatalf("backup missing seeded row: %v", err)
	}
	if j.Repo != "kraken" {
		t.Errorf("backup row mangled: %+v", j)
	}
}

// TestMigrateV1Forward covers the forward migration a real machine takes: a
// database created at schema v1 (no questions table, jobs.type still
// present, no prs.conflict_state column) must replay migrations 2..N on the
// next Open and serve all three PR watermarks.
func TestMigrateV1Forward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate v1 forward): %v", err)
	}
	defer s.Close()
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	id, err := s.CreateJob(&Job{Slug: "migrated"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	url := "https://github.com/example/kraken/pull/6"
	if err := s.UpsertPR(id, url, "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	if err := s.SetPRWatermark(id, url, "", "", "clean"); err != nil {
		t.Fatalf("SetPRWatermark on migrated column: %v", err)
	}
	prs, err := s.ListPRs(id)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(prs) != 1 || prs[0].ConflictState != "clean" {
		t.Fatalf("conflict_state after migration: got %+v", prs)
	}
}
