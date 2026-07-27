package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// downgradeFreshDB rolls a freshly-opened Store (already migrated all the
// way to schemaVersion) back to look like a genuine on-disk database from
// an older user_version — not just "missing the one column this test cares
// about," but the FULL shape a real kraken.db at that version actually had,
// reversing every later migration's change in reverse order. This matters
// once schemaVersion grows past whatever a "vN-shaped" test originally
// targeted: migrate() always walks forward to the CURRENT schemaVersion, so
// a test simulating "a v2 database" must hand migrate() something that
// looks exactly like a real v2 database, or a later migration's SQL (e.g.
// v5's `ALTER TABLE jobs DROP COLUMN skip_doc`) fails against a shape that
// was never real — skip_doc, dropped only at v5, is still present at v1-v4,
// so rolling back to any of those must re-add it, not leave it gone.
func downgradeFreshDB(t *testing.T, s *Store, target int) {
	t.Helper()
	// Reverse v11: drop job_tokens' four zeus columns (added at v11, absent
	// through v10). The v11 migration's OTHER half — emptying both token
	// tables — is deliberately not reversed: it destroys rows rather than
	// shape, and a downgrade helper's job is to reproduce an older SHAPE. A
	// test that wants pre-migration rows seeds them after this call.
	//
	// Reversed FIRST: downgrades unwind newest-first, mirroring the order
	// migrate() applies them.
	if target < 11 {
		for _, stmt := range []string{
			`ALTER TABLE job_tokens DROP COLUMN zeus_in`,
			`ALTER TABLE job_tokens DROP COLUMN zeus_out`,
			`ALTER TABLE job_tokens DROP COLUMN zeus_cache_write`,
			`ALTER TABLE job_tokens DROP COLUMN zeus_cache_read`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v11) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v10: drop the two token tables (added at v10, absent through
	// v9). Both are brand-new tables with no indexes of their own, so
	// dropping them is the whole reversal.
	if target < 10 {
		for _, stmt := range []string{
			`DROP TABLE job_token_files`,
			`DROP TABLE job_tokens`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v10) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v9: drop the watchlist table (added at v9, absent through v8)
	// and the feedback(ts) index it shipped alongside. The watchlist's own
	// indexes go with the table — SQLite drops a table's indexes along with
	// it — but feedback_ts sits on a table that SURVIVES, so it has to be
	// dropped by name or the v9 migration's CREATE INDEX would collide on the
	// way back up.
	//
	if target < 9 {
		for _, stmt := range []string{
			`DROP TABLE watchlist`,
			`DROP INDEX feedback_ts`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v9) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v8: drop jobs.color (added at v8, absent through v7).
	if target < 8 {
		if _, err := s.db.Exec(`ALTER TABLE jobs DROP COLUMN color`); err != nil {
			t.Fatalf("downgrade to v%d (undo v8, drop jobs.color): %v", target, err)
		}
	}
	// Reverse v7: drop the threads table entirely (added at v7, absent
	// through v6). Its unique index goes with it — SQLite drops a table's
	// indexes along with the table.
	if target < 7 {
		if _, err := s.db.Exec(`DROP TABLE threads`); err != nil {
			t.Fatalf("downgrade to v%d (undo v7, drop threads): %v", target, err)
		}
	}
	// Reverse v6: drop feedback.job_id (added at v6, absent through v5).
	if target < 6 {
		if _, err := s.db.Exec(`ALTER TABLE feedback DROP COLUMN job_id`); err != nil {
			t.Fatalf("downgrade to v%d (undo v6, drop feedback.job_id): %v", target, err)
		}
	}
	// Reverse v5: re-add jobs.skip_doc (dropped at v5, present through v4),
	// drop feedback's v5 columns (added at v5, absent through v4), and swap
	// the relational questions table back for schemaV2's blob shape (the
	// shape every version through v4 used).
	if target < 5 {
		for _, stmt := range []string{
			`ALTER TABLE jobs ADD COLUMN skip_doc INTEGER NOT NULL DEFAULT 0`,
			`ALTER TABLE feedback DROP COLUMN session`,
			`ALTER TABLE feedback DROP COLUMN addressed`,
			`ALTER TABLE feedback DROP COLUMN addressed_at`,
			`DROP TABLE questions`,
			`CREATE TABLE questions (
				job_id INTEGER PRIMARY KEY NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
				bytes  BLOB,
				mtime  INTEGER NOT NULL DEFAULT 0
			)`,
		} {
			if _, err := s.db.Exec(stmt); err != nil {
				t.Fatalf("downgrade to v%d (undo v5) %q: %v", target, stmt, err)
			}
		}
	}
	// Reverse v4: drop prs.conflict_state (added at v4, absent through v3).
	if target < 4 {
		if _, err := s.db.Exec(`ALTER TABLE prs DROP COLUMN conflict_state`); err != nil {
			t.Fatalf("downgrade to v%d (undo v4, drop prs.conflict_state): %v", target, err)
		}
	}
	// Reverse v3: re-add jobs.type (dropped at v3, present through v2).
	if target < 3 {
		if _, err := s.db.Exec(`ALTER TABLE jobs ADD COLUMN type TEXT NOT NULL DEFAULT 'build'`); err != nil {
			t.Fatalf("downgrade to v%d (undo v3, re-add type): %v", target, err)
		}
	}
	// Reverse v2: drop the questions table entirely — v1 never had one
	// (must run after the v5-undo block above, which (re)creates it).
	if target < 2 {
		if _, err := s.db.Exec(`DROP TABLE questions`); err != nil {
			t.Fatalf("downgrade to v%d (undo v2, drop questions): %v", target, err)
		}
	}
	if _, err := s.db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, target)); err != nil {
		t.Fatalf("reset user_version to %d: %v", target, err)
	}
}

// TestSchemaMigratesForwardFromV1 covers finding #1: a real
// kraken.db can exist at PRAGMA user_version = 1 (schemaV1's tables only,
// no questions table, jobs.type and jobs.skip_doc both still present) from
// before the doc-questions feature shipped. Reopening such a database must
// walk forward through every migration up to the current schemaVersion —
// creating the questions table (v2), dropping jobs.type (v3), adding
// prs.conflict_state (v4), and dropping jobs.skip_doc plus restructuring
// questions into relational rows (v5) —
// rather than silently leaving any of it undone (migrate() only applies
// versions above the database's CURRENT version, so an in-place edit to an
// already-released schemaV<N> string — the bug this test guards against —
// would never run against an existing v1 database).
func TestSchemaMigratesForwardFromV1(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the
	// real re-Open below, rather than only ever calling s.migrate() in
	// process.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	downgradeFreshDB(t, s, 1)

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

	// the (now relational) questions table must exist and be usable.
	id, err := s.CreateJob(&Job{Slug: "post-migrate"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "Q1", Text: "one", Importance: "P0"}); err != nil {
		t.Fatalf("UpsertQuestion after migration: %v", err)
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions after migration: %v", err)
	}
	if len(qs) != 1 || qs[0].Text != "one" {
		t.Fatalf("unexpected questions after migration: %+v", qs)
	}

	// Fidelity check (finding #7): the assertions above all ran an
	// in-process s.migrate() call directly — they don't prove a genuinely
	// fresh process opening a v1-shaped kraken.db off disk (the real-world
	// scenario) gets the same treatment through the actual Open() entry
	// point. Roll the same on-disk file back to v1-shaped again, close it,
	// and re-Open it for real.
	downgradeFreshDB(t, s, 1)
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
	if err := s2.UpsertQuestion(id2, DocQuestion{QID: "Q1", Text: "two", Importance: "P1"}); err != nil {
		t.Fatalf("UpsertQuestion after re-Open: %v", err)
	}
	qs2, err := s2.ListQuestions(id2)
	if err != nil {
		t.Fatalf("ListQuestions after re-Open: %v", err)
	}
	if len(qs2) != 1 || qs2[0].Text != "two" {
		t.Fatalf("unexpected questions after re-Open migration: %+v", qs2)
	}
}

// TestSchemaMigratesForwardFromV2 covers Q14: a real kraken.db can exist at
// PRAGMA user_version = 2 (schemaV1+schemaV2's tables, including the jobs.type
// column with its CHECK(type IN ('build','info')) constraint) from before the
// job-level type concept was deleted. Reopening such a database must walk
// forward through every migration up to the current schemaVersion — dropping
// jobs.type (v3), adding prs.conflict_state (v4), then dropping
// jobs.skip_doc and restructuring questions (v5) —
// go-sqlite3 bundles a modern SQLite with native ALTER TABLE ... DROP
// COLUMN support (no rebuild-the-table dance needed), so each drop is a
// single statement.
func TestSchemaMigratesForwardFromV2(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring TestSchemaMigratesForwardFromV1.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	downgradeFreshDB(t, s, 2)
	if _, err := s.db.Exec(`INSERT INTO jobs (slug, type, repo) VALUES ('v2-shaped', 'info', 'kraken')`); err != nil {
		t.Fatalf("seed v2-shaped row: %v", err)
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
			t.Fatal("jobs.type column survived migration")
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
	// TestSchemaMigratesForwardFromV1's second-downgrade check.
	downgradeFreshDB(t, s, 2)
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
// (never seen an older version on disk) also lands at the current
// schemaVersion — distinct from TestOpenMigrateIdempotent, which
// round-trips an already-current database, and the "vN-shaped" tests above,
// which upgrade an older one.
func TestFreshOpenEndsAtCurrentSchemaVersion(t *testing.T) {
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("fresh open user_version = %d, want %d", version, schemaVersion)
	}
	// jobs.type/skip_doc must never exist on a freshly created database
	// either (migrations[1..6] all apply inside the same transaction).
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
		if name == "type" || name == "skip_doc" {
			t.Fatalf("fresh database has a jobs.%s column", name)
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

// TestSchemaV4ToV5MigratesQuestionsAndFeedback covers the v5 migration's
// full contract (54-feedback-contract.md): jobs.skip_doc drops, feedback
// gains session/addressed/addressed_at defaulted empty on pre-existing
// rows, and questions.yaml blob rows become relational question rows —
// preserving file order as position — while a malformed blob is SKIPPED
// with a logged event rather than aborting the whole migration (carl's
// clean-cutover ruling: one-time move, best effort on bad legacy data).
func TestSchemaV4ToV5MigratesQuestionsAndFeedback(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the
	// real re-Open below, same pattern as the tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	goodID, err := s.CreateJob(&Job{Slug: "good-job"})
	if err != nil {
		t.Fatalf("CreateJob good: %v", err)
	}
	badID, err := s.CreateJob(&Job{Slug: "bad-job"})
	if err != nil {
		t.Fatalf("CreateJob bad: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobSlug: "good-job", Artifact: "explain.html", Note: "pre-existing", TS: 5}); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	// Roll this fresh (already-migrated-to-v5) database back to a
	// v4-shaped one: re-add jobs.skip_doc, drop feedback's v5 columns, and
	// swap the relational questions table back out for schemaV2's blob
	// shape, faithfully reproducing what a real pre-v5 kraken.db looks
	// like on disk. downgradeFreshDB seeds neither jobs nor the blob rows
	// (goodID/badID/the feedback row above were created before the
	// downgrade, so they already exist and survive it) — the legacy
	// question blobs still need seeding by hand below.
	downgradeFreshDB(t, s, 4)

	goodBlob := []byte("questions:\n" +
		"  - id: Q1\n" +
		"    text: is this right?\n" +
		"    importance: P1\n" +
		"    answer: yes\n" +
		"    answered-at: 2026-01-01T00:00:00Z\n" +
		"  - id: Q2\n" +
		"    text: second one\n" +
		"    importance: P0\n")
	if _, err := s.db.Exec(`INSERT INTO questions (job_id, bytes, mtime) VALUES (?, ?, ?)`, goodID, goodBlob, 100); err != nil {
		t.Fatalf("seed good blob: %v", err)
	}
	// Missing "importance" — the original strict parser would reject this
	// item, so the migration must skip the whole blob rather than guess.
	badBlob := []byte("questions:\n  - id: Q1\n    text: broken\n")
	if _, err := s.db.Exec(`INSERT INTO questions (job_id, bytes, mtime) VALUES (?, ?, ?)`, badID, badBlob, 200); err != nil {
		t.Fatalf("seed bad blob: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v3: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// jobs.skip_doc must be gone.
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("table_info(jobs): %v", err)
	}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan table_info row: %v", err)
		}
		if name == "skip_doc" {
			rows.Close()
			t.Fatal("jobs.skip_doc survived migration to v5")
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(jobs) rows: %v", err)
	}
	rows.Close()

	// feedback's new columns exist and default empty on the pre-existing row.
	list, err := s.ListFeedback(goodID)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("feedback row lost across migration: %+v", list)
	}
	if list[0].Session != "" || list[0].Addressed != "" || list[0].AddressedAt != "" {
		t.Fatalf("pre-existing feedback row should default new columns empty: %+v", list[0])
	}
	if list[0].Note != "pre-existing" {
		t.Fatalf("feedback row mangled by migration: %+v", list[0])
	}

	// good-job's blob parsed into two rows, in file order, answer/answered_at
	// carried over from the blob.
	qs, err := s.ListQuestions(goodID)
	if err != nil {
		t.Fatalf("ListQuestions good: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("want 2 migrated questions, got %+v", qs)
	}
	if qs[0].QID != "Q1" || qs[0].Text != "is this right?" || qs[0].Importance != "P1" ||
		qs[0].Answer != "yes" || qs[0].AnsweredAt != "2026-01-01T00:00:00Z" || qs[0].Position != 0 {
		t.Fatalf("Q1 migrated wrong: %+v", qs[0])
	}
	if qs[1].QID != "Q2" || qs[1].Text != "second one" || qs[1].Importance != "P0" || qs[1].Position != 1 {
		t.Fatalf("Q2 migrated wrong: %+v", qs[1])
	}

	// bad-job's blob was unparseable and must be SKIPPED, not fatal to the
	// whole migration — no rows for it, and a logged event naming it.
	badQs, err := s.ListQuestions(badID)
	if err != nil {
		t.Fatalf("ListQuestions bad: %v", err)
	}
	if len(badQs) != 0 {
		t.Fatalf("malformed blob should not have produced any rows: %+v", badQs)
	}
	events, err := s.ListEvents(50, 0)
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	found := false
	for _, e := range events {
		if e.Event == "questions-migration-skip" && e.JobSlug == "bad-job" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no logged event for the skipped malformed blob; events = %+v", events)
	}

	// questions_blob must be gone — its job is done.
	var blobTableExists int
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='questions_blob'`).Scan(&blobTableExists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("questions_blob table survived migration: err=%v", err)
	}

	// Fidelity check: the assertions above all ran an in-process
	// s.migrate() call directly — roll the same on-disk file back to
	// v4-shaped again, close it, and re-Open it for real, mirroring the
	// other migration tests' second-downgrade check.
	downgradeFreshDB(t, s, 4)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v4-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade recreated a blob-shaped, EMPTY questions table (no
	// re-seeded bytes for goodID this time) — re-Open's migration should
	// leave goodID with zero questions rather than erroring, which is
	// enough to prove the relational table survives another real Open()
	// round-trip.
	qs2, err := s2.ListQuestions(goodID)
	if err != nil {
		t.Fatalf("ListQuestions after re-Open: %v", err)
	}
	if len(qs2) != 0 {
		t.Fatalf("expected no questions after the 2nd (unseeded) downgrade+re-Open: %+v", qs2)
	}
}

// TestSchemaV5ToV6BackfillsFeedbackJobID covers the v6 migration (B2):
// existing feedback rows, seeded straight through the pre-v6 shape (no
// job_id column), must have job_id backfilled by matching job_slug against
// an active job's slug first, then an archived job's archived_name — and a
// row matching neither (an orphaned fossil, e.g. its job was later
// hard-deleted) must be left with job_id 0/NULL rather than failing the
// whole migration, same best-effort ruling as v5's malformed-blob handling.
func TestSchemaV5ToV6BackfillsFeedbackJobID(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	activeID, err := s.CreateJob(&Job{Slug: "active-job"})
	if err != nil {
		t.Fatalf("CreateJob active: %v", err)
	}
	archivedID, err := s.CreateJob(&Job{Slug: "archived-job"})
	if err != nil {
		t.Fatalf("CreateJob to-archive: %v", err)
	}
	archivedName, err := s.ArchiveJob(archivedID)
	if err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	downgradeFreshDB(t, s, 5)

	// Seed three feedback rows straight through the raw v5 shape (no job_id
	// column yet): one matching an active job's slug, one matching an
	// archived job's archived_name, and one matching neither (an orphan —
	// e.g. its job was later hard-deleted, or the row simply predates
	// anything this migration can resolve).
	for _, r := range []struct{ slug, note string }{
		{"active-job", "for the active job"},
		{archivedName, "for the archived job"},
		{"ghost-job", "orphaned, no matching job"},
	} {
		if _, err := s.db.Exec(
			`INSERT INTO feedback (job_slug, artifact, note, ts) VALUES (?, ?, ?, ?)`,
			r.slug, "a.md", r.note, 100,
		); err != nil {
			t.Fatalf("seed feedback %q: %v", r.slug, err)
		}
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v5: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	rows, err := s.ListFeedback(0)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	byNote := map[string]Feedback{}
	for _, f := range rows {
		byNote[f.Note] = f
	}
	if got, ok := byNote["for the active job"]; !ok || got.JobID != activeID {
		t.Fatalf("active job feedback job_id = %+v, want JobID %d", got, activeID)
	}
	if got, ok := byNote["for the archived job"]; !ok || got.JobID != archivedID {
		t.Fatalf("archived job feedback job_id = %+v, want JobID %d", got, archivedID)
	}
	if got, ok := byNote["orphaned, no matching job"]; !ok || got.JobID != 0 {
		t.Fatalf("orphaned feedback job_id = %+v, want JobID 0 (unmatched, left NULL)", got)
	}
}

// TestSchemaV6ToV7AddsThreads covers the v7 migration: a real kraken.db can
// exist at PRAGMA user_version = 6 (no threads table at all) from before
// side threads shipped. Reopening such a database must create the table —
// usable immediately, unique index and FK cascade included — while leaving
// every pre-existing row untouched, since v7 is a pure add with no data to
// move forward (migrations[7] carries no hook).
func TestSchemaV6ToV7AddsThreads(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring the migration tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	jobID, err := s.CreateJob(&Job{Slug: "v6-shaped", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobID: jobID, JobSlug: "v6-shaped", Note: "pre-existing", TS: 5}); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	downgradeFreshDB(t, s, 6)

	// the table really is gone at the v6 shape — otherwise this test would
	// pass without the migration doing anything.
	var exists int
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='threads'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("threads table still present at the v6 shape: err=%v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v6: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// the new table exists and is usable end to end.
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Title: "post-migrate", Pane: "%1"}); err != nil {
		t.Fatalf("AddThread after migration: %v", err)
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads after migration: %v", err)
	}
	if len(list) != 1 || list[0].Title != "post-migrate" {
		t.Fatalf("unexpected threads after migration: %+v", list)
	}
	// ...including the unique index the migration's DDL carries.
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1"}); err == nil {
		t.Fatal("duplicate tid accepted after migration: threads_job_tid index missing")
	}

	// pre-existing rows survive untouched.
	j, err := s.GetJobBySlug("v6-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.Repo != "kraken" {
		t.Fatalf("job row mangled by migration: %+v", j)
	}
	fb, err := s.ListFeedback(jobID)
	if err != nil {
		t.Fatalf("ListFeedback after migration: %v", err)
	}
	if len(fb) != 1 || fb[0].Note != "pre-existing" {
		t.Fatalf("feedback row mangled by migration: %+v", fb)
	}

	// Fidelity check: the assertions above ran an in-process s.migrate()
	// call directly — roll the same on-disk file back to v6-shaped again,
	// close it, and re-Open it for real, mirroring the other migration
	// tests' second-downgrade check.
	downgradeFreshDB(t, s, 6)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v6-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade dropped the (seeded) threads table, so the re-created
	// one starts empty — enough to prove the table survives a real Open()
	// round-trip and numbering restarts cleanly on it.
	next, err := s2.NextThreadID(jobID)
	if err != nil {
		t.Fatalf("NextThreadID after re-Open: %v", err)
	}
	if next != "t1" {
		t.Fatalf("NextThreadID on the re-created table = %q, want t1", next)
	}
}

// TestSchemaV8ToV9AddsWatchlistAndFeedbackTSIndex covers the v9 migration: a
// real kraken.db can exist at PRAGMA user_version = 8 (no watchlist table, no
// feedback(ts) index) from before the dream loop shipped. Reopening such a
// database must create both — usable immediately — while leaving every
// pre-existing row untouched, since v9 is a pure add with no data to move
// forward (migrations[9] carries no hook).
func TestSchemaV8ToV9AddsWatchlistAndFeedbackTSIndex(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring the migration tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	jobID, err := s.CreateJob(&Job{Slug: "v8-shaped", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.AddFeedback(Feedback{JobID: jobID, JobSlug: "v8-shaped", Note: "pre-existing", TS: 5}); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	downgradeFreshDB(t, s, 8)

	// both really are gone at the v8 shape — otherwise this test would pass
	// without the migration doing anything.
	var exists int
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='watchlist'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("watchlist table still present at the v8 shape: err=%v", err)
	}
	err = s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='index' AND name='feedback_ts'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("feedback_ts index still present at the v8 shape: err=%v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v7: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// the new table is usable end to end.
	wid, err := s.AddWatchItem(WatchItem{Observation: "post-migrate", Class: "waste"})
	if err != nil {
		t.Fatalf("AddWatchItem after migration: %v", err)
	}
	list, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems after migration: %v", err)
	}
	if len(list) != 1 || list[0].ID != wid || list[0].Observation != "post-migrate" {
		t.Fatalf("unexpected watchlist rows after migration: %+v", list)
	}

	// ...and the index the sweep read depends on exists by name.
	if err := s.db.QueryRow(
		`SELECT 1 FROM sqlite_master WHERE type='index' AND name='feedback_ts'`,
	).Scan(&exists); err != nil {
		t.Fatalf("feedback_ts index missing after migration: %v", err)
	}

	// pre-existing rows survive untouched, and the new sweep read sees them.
	j, err := s.GetJobBySlug("v8-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.Repo != "kraken" {
		t.Fatalf("job row mangled by migration: %+v", j)
	}
	swept, err := s.FeedbackSince(0)
	if err != nil {
		t.Fatalf("FeedbackSince after migration: %v", err)
	}
	if len(swept) != 1 || swept[0].Note != "pre-existing" {
		t.Fatalf("feedback row mangled by migration: %+v", swept)
	}

	// Fidelity check: the assertions above ran an in-process s.migrate() call
	// directly — roll the same on-disk file back to v8-shaped again, close it,
	// and re-Open it for real, mirroring the other migration tests'
	// second-downgrade check.
	downgradeFreshDB(t, s, 8)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v8-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade dropped the (seeded) watchlist table, so the
	// re-created one starts empty — enough to prove it survives a real Open()
	// round-trip.
	list2, err := s2.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems after re-Open: %v", err)
	}
	if len(list2) != 0 {
		t.Fatalf("expected an empty watchlist after the 2nd downgrade+re-Open: %+v", list2)
	}
}

// TestSchemaV10ToV11SplitsAndWipesTokens covers the v11 migration, whose two
// halves are checked separately because only one of them is the usual kind.
//
// The ordinary half: job_tokens gains four zeus columns, usable immediately.
//
// The half that is the whole point: BOTH token tables are EMPTIED. The
// counters accumulate forward from job_token_files' byte watermarks and
// nothing ever re-reads what is behind one, so a surviving pre-split row would
// hold a job's entire history with the four new columns at zero and report
// that job as 100% subagent forever, with no pass anywhere able to fix it.
// Wiping is what makes the daemon re-sum from byte zero with the split in
// place. A test that only asserted the columns exist would pass against
// exactly the bug this migration exists to avoid.
func TestSchemaV10ToV11SplitsAndWipesTokens(t *testing.T) {
	// Opened directly (not via openTest) so the path is in hand for the real
	// re-Open below, mirroring the migration tests above.
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	jobID, err := s.CreateJob(&Job{Slug: "v10-shaped", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	downgradeFreshDB(t, s, 10)

	// The pre-split rows a real v10 database carries: an all-time total with
	// no split, plus the watermarks that would stop it ever being re-summed.
	// Seeded AFTER the downgrade and with raw SQL, because BumpJobTokens now
	// writes columns the v10 shape does not have.
	if _, err := s.db.Exec(
		`INSERT INTO job_tokens (job_slug, in_tokens, out_tokens, cache_write, cache_read, sessions, updated_at)
			VALUES ('v10-shaped', 111, 222, 333, 444, 9, 1700000000000)`); err != nil {
		t.Fatalf("seed a v10-shaped token row: %v", err)
	}
	if _, err := s.db.Exec(
		`INSERT INTO job_token_files (job_slug, path, byte_offset) VALUES ('v10-shaped', '/p/a.jsonl', 4096)`); err != nil {
		t.Fatalf("seed a v10-shaped watermark: %v", err)
	}

	// the columns really are gone at the v10 shape — otherwise this test would
	// pass without the migration doing anything.
	var exists int
	err = s.db.QueryRow(`SELECT 1 FROM pragma_table_info('job_tokens') WHERE name = 'zeus_in'`).Scan(&exists)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("zeus_in still present at the v10 shape: err=%v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v10: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// BOTH tables are empty: the row AND the watermark that would have stopped
	// the re-sum.
	rows, err := s.ListJobTokens()
	if err != nil {
		t.Fatalf("ListJobTokens after migration: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("job_tokens = %+v, want it EMPTIED — an un-backfillable row is worse than no row", rows)
	}
	offs, err := s.AllJobTokenOffsets()
	if err != nil {
		t.Fatalf("AllJobTokenOffsets after migration: %v", err)
	}
	if len(offs) != 0 {
		t.Fatalf("job_token_files = %v, want it EMPTIED — a surviving watermark makes the re-sum a no-op", offs)
	}

	// ...and the re-sum a daemon pass would then do lands with the split.
	if err := s.BumpJobTokens("v10-shaped", JobTokens{
		In: 111, Out: 222, CacheWrite: 333, CacheRead: 444, Sessions: 9,
		ZeusIn: 40, ZeusOut: 80, ZeusCacheWrite: 100, ZeusCacheRead: 200,
	}); err != nil {
		t.Fatalf("BumpJobTokens after migration: %v", err)
	}
	got, err := s.GetJobTokens("v10-shaped")
	if err != nil {
		t.Fatalf("GetJobTokens after migration: %v", err)
	}
	if got.In != 111 || got.ZeusIn != 40 || got.ZeusOut != 80 ||
		got.ZeusCacheWrite != 100 || got.ZeusCacheRead != 200 {
		t.Fatalf("row after the re-sum = %+v, want the totals back with the zeus half filled in", got)
	}

	// Rows OUTSIDE the two token tables are untouched: this migration wipes
	// derived state only.
	j, err := s.GetJobBySlug("v10-shaped")
	if err != nil {
		t.Fatalf("GetJobBySlug after migration: %v", err)
	}
	if j.ID != jobID || j.Repo != "kraken" {
		t.Fatalf("job row mangled by migration: %+v", j)
	}

	// Fidelity check: the assertions above ran an in-process s.migrate() call
	// directly — roll the same on-disk file back to v10-shaped again, close
	// it, and re-Open it for real, mirroring the other migration tests'
	// second-downgrade check.
	downgradeFreshDB(t, s, 10)
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v10-shaped db: %v", err)
	}
	defer s2.Close()

	var version2 int
	if err := s2.db.QueryRow(`PRAGMA user_version`).Scan(&version2); err != nil {
		t.Fatalf("read user_version after re-Open: %v", err)
	}
	if version2 != schemaVersion {
		t.Fatalf("user_version after re-Open = %d, want %d", version2, schemaVersion)
	}
	// The 2nd downgrade left the (re-summed) row behind with its columns
	// dropped, and the real Open() wiped it again — which is the point: the
	// wipe is part of the migration, not of the test's setup.
	rows2, err := s2.ListJobTokens()
	if err != nil {
		t.Fatalf("ListJobTokens after re-Open: %v", err)
	}
	if len(rows2) != 0 {
		t.Fatalf("job_tokens after re-Open = %+v, want the wipe to have run through Open() too", rows2)
	}
}

// schemaDump returns every table/index/trigger definition in the database,
// sorted by name — the whole shape, not just the one table a given test
// happens to care about. Comparing two dumps is what makes "migrated forward"
// and "created fresh" a checkable claim rather than an assumption.
//
// sqlite_autoindex_* entries are skipped: SQLite invents those for implicit
// PRIMARY KEY/UNIQUE constraints, gives them a NULL sql, and numbers them per
// table in creation order — so an ALTER-heavy migration path can legitimately
// end up with the same constraints under different autoindex numbers.
func schemaDump(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(
		`SELECT type, name, COALESCE(sql,'') FROM sqlite_master
			WHERE name NOT LIKE 'sqlite_autoindex_%' ORDER BY type, name`)
	if err != nil {
		t.Fatalf("read sqlite_master: %v", err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var typ, name, ddl string
		if err := rows.Scan(&typ, &name, &ddl); err != nil {
			t.Fatalf("scan sqlite_master row: %v", err)
		}
		fmt.Fprintf(&b, "%s %s\n%s\n---\n", typ, name, strings.Join(strings.Fields(ddl), " "))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("sqlite_master rows: %v", err)
	}
	return b.String()
}

// TestMigratedSchemaMatchesFreshSchema is the invariant every one of the
// per-version migration tests above assumes but none of them actually checks:
// a database that walked the whole migration chain must end up with the SAME
// shape as one created from scratch today. A migration that adds a column
// with a subtly different declaration (missing NOT NULL, a different DEFAULT)
// or forgets an index leaves a database that passes every functional test and
// then behaves differently under load or under a NULL — this compares the
// full sqlite_master dump so that drift is caught structurally instead.
func TestMigratedSchemaMatchesFreshSchema(t *testing.T) {
	fresh := openTest(t)
	want := schemaDump(t, fresh)

	for _, from := range []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		dbPath := filepath.Join(t.TempDir(), "kraken.db")
		s, err := Open(dbPath)
		if err != nil {
			t.Fatalf("Open (from v%d): %v", from, err)
		}
		downgradeFreshDB(t, s, from)
		if err := s.Close(); err != nil {
			t.Fatalf("Close before re-Open (from v%d): %v", from, err)
		}
		s2, err := Open(dbPath)
		if err != nil {
			t.Fatalf("re-Open a v%d-shaped db: %v", from, err)
		}
		got := schemaDump(t, s2)
		s2.Close()
		if got != want {
			t.Errorf("schema after migrating from v%d differs from a fresh database:\n--- migrated ---\n%s\n--- fresh ---\n%s", from, got, want)
		}
	}
}

// TestSchemaMigrateTwiceIsNoOp pins migrate()'s idempotence directly at the
// current schemaVersion: v9's step is pure CREATE (no IF NOT EXISTS), so a
// second application would fail loudly on "table watchlist already exists" if
// the user_version guard ever stopped holding it back.
func TestSchemaMigrateTwiceIsNoOp(t *testing.T) {
	s := openTest(t)
	if _, err := s.AddWatchItem(WatchItem{Observation: "survivor"}); err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	if err := s.migrate(); err != nil {
		t.Fatalf("re-running migrate() on an already-current db: %v", err)
	}
	list, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(list) != 1 || list[0].Observation != "survivor" {
		t.Fatalf("a no-op migrate must not touch data: %+v", list)
	}
}

// TestSchemaV5ToV6AmbiguousSlugLeavesJobIDNull is the sibling case
// TestSchemaV5ToV6BackfillsFeedbackJobID doesn't reach: a pre-v6 row whose
// job_slug matches an active job's slug AND an archived job's archived_name
// at the same time (gen1 archived under its bare slug, freeing it; gen2
// created live under the same name). Nothing in the pre-v6 data says which
// generation the row belongs to, so it must be left NULL. Resolving it to
// the active job instead is destructive, not merely wrong: archiving that
// job later rekeys by job_id and would drag every such row off the older
// generation's card.
func TestSchemaV5ToV6AmbiguousSlugLeavesJobIDNull(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	gen1, err := s.CreateJob(&Job{Slug: "shared"})
	if err != nil {
		t.Fatalf("CreateJob gen1: %v", err)
	}
	archivedName, err := s.ArchiveJob(gen1)
	if err != nil {
		t.Fatalf("ArchiveJob gen1: %v", err)
	}
	if archivedName != "shared" {
		t.Fatalf("archived name = %q, want the bare slug (fixture assumption)", archivedName)
	}
	gen2, err := s.CreateJob(&Job{Slug: "shared"})
	if err != nil {
		t.Fatalf("CreateJob gen2 (reusing the freed slug): %v", err)
	}

	downgradeFreshDB(t, s, 5)

	if _, err := s.db.Exec(
		`INSERT INTO feedback (job_slug, artifact, note, ts) VALUES (?, ?, ?, ?)`,
		"shared", "a.md", "ambiguous across two generations", 100,
	); err != nil {
		t.Fatalf("seed ambiguous feedback: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v5: %v", err)
	}

	rows, err := s.ListFeedback(0)
	if err != nil {
		t.Fatalf("ListFeedback: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("feedback rows = %+v, want exactly the seeded one", rows)
	}
	if rows[0].JobID != 0 {
		t.Fatalf("ambiguous row job_id = %d, want 0/NULL (matched both job %d and job %d)",
			rows[0].JobID, gen2, gen1)
	}
	// and it must be invisible to BOTH generations' scoped reads, not
	// silently attached to one of them
	for _, id := range []int64{gen1, gen2} {
		scoped, err := s.ListFeedback(id)
		if err != nil {
			t.Fatalf("ListFeedback(%d): %v", id, err)
		}
		if len(scoped) != 0 {
			t.Fatalf("ambiguous row surfaced on job %d's scoped read: %+v", id, scoped)
		}
	}
}

// TestKeywordColumnsAreQuotedInDDL pins the house pattern F5 restored: a
// column whose name is a SQLite keyword is quoted in the DDL itself, not
// only at the Go use sites. Both cases in the schema today —
// board_order."column" (schemaV1) and threads."window" (schemaV7) — parse
// unquoted inside a CREATE TABLE, so the bare form looks fine right up until
// someone copies it into an expression, where it is a syntax error. Quoting
// it where the next schema author reads it is what stops that copy.
//
// The second half is the behavioral half: the quoted names must actually
// work in a real expression (WHERE / ORDER BY), which is where an unquoted
// declaration would have stopped being merely stylistic.
func TestKeywordColumnsAreQuotedInDDL(t *testing.T) {
	for _, c := range []struct{ ddl, col string }{
		{schemaV1, "column"},
		{schemaV7, "window"},
	} {
		quoted := `"` + c.col + `"`
		if !strings.Contains(c.ddl, quoted) {
			t.Fatalf("DDL declares %s unquoted; SQLite keyword columns must be written %s", c.col, quoted)
		}
	}

	s := openTest(t)
	jobID := seedThreadJob(t, s, "keyword-cols")
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", Pane: "%1", Window: "@4"}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	var got string
	if err := s.db.QueryRow(
		`SELECT "window" FROM threads WHERE job_id = ? AND "window" != '' ORDER BY "window"`, jobID,
	).Scan(&got); err != nil {
		t.Fatalf(`querying threads."window" in an expression: %v`, err)
	}
	if got != "@4" {
		t.Fatalf(`threads."window" = %q, want @4`, got)
	}
}

// TestSchemaV7ToV8AddsColor covers the v8 migration against a database that
// really is at v7: the column must appear with every EXISTING row left on
// the empty string — "no colour", which is exactly the behaviour those jobs had before the
// column existed. A migration that defaulted to some palette entry instead
// would silently recolour (and, via the daemon's reconcile, retype) every
// job on the board the first time the new binary ran.
func TestSchemaV7ToV8AddsColor(t *testing.T) {
	s := openTest(t)
	downgradeFreshDB(t, s, 7)

	if _, err := s.db.Exec(
		`INSERT INTO jobs (slug, state, rev, run) VALUES ('legacy', 'working', 1, 1)`,
	); err != nil {
		t.Fatalf("seed v7 row: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate from v7: %v", err)
	}

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d after migrate, want %d", version, schemaVersion)
	}

	var color string
	if err := s.db.QueryRow(`SELECT color FROM jobs WHERE slug = 'legacy'`).Scan(&color); err != nil {
		t.Fatalf("read migrated color: %v", err)
	}
	if color != "" {
		t.Fatalf("pre-existing row got color %q, want '' (no colour)", color)
	}

	// and the column is NOT NULL DEFAULT '', so an insert that never
	// mentions it still scans into a plain string field
	got, err := s.GetJobBySlug("legacy")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if got.Color != "" {
		t.Fatalf("scanned color = %q, want empty", got.Color)
	}
}
