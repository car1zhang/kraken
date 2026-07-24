package store

import (
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestSchemaV2ToV3DropsTypeColumn covers Q14: a real kraken.db can exist at
// PRAGMA user_version = 2 (schemaV1+schemaV2's tables, including the jobs.type
// column with its CHECK(type IN ('build','info')) constraint) from before the
// job-level type concept was deleted. Reopening such a database must apply
// migrations[3] and drop the column — go-sqlite3 bundles a modern SQLite with
// native ALTER TABLE ... DROP COLUMN support, so this is a single statement,
// not a rebuild-the-table dance — rather than silently leaving it in place.
func TestSchemaV2ToV3DropsTypeColumn(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring TestSchemaV1ToV2MigratesQuestionsTable.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Roll this fresh (already-migrated-to-v3) database back to a v2-shaped
	// one: re-add the type column schemaV1 originally created (with a seeded
	// row using it) and reset user_version to 2, faithfully reproducing what
	// a real pre-Q14 kraken.db looks like on disk.
	if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN type TEXT NOT NULL DEFAULT 'build'`); err != nil {
		t.Fatalf("re-add type column: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO jobs (slug, type, repo) VALUES ('v2-shaped', 'info', 'kraken')`); err != nil {
		t.Fatalf("seed v2-shaped row: %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatalf("reset user_version: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v2: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	if version != 3 {
		t.Fatalf("user_version = %d, want 3", version)
	}

	// the type column must be gone.
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "type" {
			t.Fatal("jobs.type column survived migration to v3")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}

	// the seeded row must survive intact, minus the dropped column.
	j, err := s.GetJobBySlug("v2-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.Repo != "kraken" {
		t.Fatalf("row mangled by migration: %+v", j)
	}

	// Fidelity check: the assertions above all ran an in-process s.migrate()
	// call directly — roll the same on-disk file back to v2-shaped again,
	// close it, and re-Open it for real, mirroring
	// TestSchemaV1ToV2MigratesQuestionsTable's second-downgrade check.
	if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN type TEXT NOT NULL DEFAULT 'build'`); err != nil {
		t.Fatalf("re-add type column (2nd downgrade): %v", err)
	}
	if _, err := s.db.Exec(`PRAGMA user_version = 2`); err != nil {
		t.Fatalf("reset user_version (2nd downgrade): %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v2-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	if _, err := s2.GetJobBySlug("v2-shaped"); err != nil {
		t.Fatalf("row missing after re-Open: %v", err)
	}
}

// TestFreshOpenEndsAtCurrentSchemaVersion pins the fresh-database path
// (never seen v1 or v2 on disk) also lands at the current schemaVersion —
// distinct from TestOpenMigrateIdempotent, which round-trips an
// already-current database, and TestSchemaV2ToV3DropsTypeColumn /
// TestSchemaV1ToV2MigratesQuestionsTable, which upgrade an older one.
func TestFreshOpenEndsAtCurrentSchemaVersion(t *testing.T) {
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("fresh open user_version = %d, want %d", version, schemaVersion)
	}
	// the type column must never exist on a freshly created database either
	// (migrations[1..3] all apply inside the same transaction).
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "type" {
			t.Fatal("fresh database has a jobs.type column")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
}

// TestOpenRefusesNewerSchema covers athena-edge-g's G-1 finding: migrate()'s
// loop only ever walks forward (version+1 up to schemaVersion), so a binary
// whose schemaVersion is older than the on-disk user_version silently
// no-ops and returns success — the db then fails deep inside the first
// query that touches a column/table only a later migration added or
// dropped, instead of erroring clearly at Open(). Simulate "an older
// binary opens a db written by a newer one" by bumping user_version above
// schemaVersion via a raw PRAGMA, closing, and re-Opening.
func TestOpenRefusesNewerSchema(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	newerV := schemaVersion + 1
	if _, err := s.db.Exec(`PRAGMA user_version = ` + strconv.Itoa(newerV)); err != nil {
		t.Fatalf("bump user_version: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("Open a db from a newer schema version: want error, got nil")
	}
	wantV := strconv.Itoa(newerV)
	if !strings.Contains(err.Error(), "schema v"+wantV) || !strings.Contains(err.Error(), "upgrade the binary") {
		t.Fatalf("Open error = %q, want it to name v%s and say to upgrade the binary", err.Error(), wantV)
	}
}
