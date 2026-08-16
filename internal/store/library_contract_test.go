package store

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// library_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md's Slice A store half (the artifact library: schema v24's
// `saved_artifacts` table and the four frozen accessors).
//
// Written from the frozen contract alone. internal/store/saved.go and
// internal/store/schema.go's v24 parts were never read by any route — no
// Read, no grep, no git show. Every assertion below traces to a stated
// obligation; where the contract is silent this file asserts nothing and the
// silence is disclosed in the round report instead.
//
// Dimension: CONTRACT — stated invariants and happy paths. Boundary shapes
// (oversize blobs, zero/negative ids, concurrent writers, unicode names) are
// athena-edge's ground in library_edge_test.go. The two ErrNotFound arms kept
// below are here because the contract states them as obligations OF the
// accessors ("`store.ErrNotFound` on miss", "ErrNotFound when no row was
// deleted; nil when one was") rather than as edge behaviour.
//
// Every package-level identifier declared here is prefixed `contractLibrary`:
// athena-edge shares this package's test namespace, and watch_contract_test.go
// already owns the bare `contract…` names in it.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractLibraryStore opens a fresh store in the test's own temp dir — the
// package's existing openTest idiom, restated locally so nothing in this file
// depends on another author's helper surviving the round. Nothing here may
// ever open ~/.kraken (the contract's isolation obligation for test authors).
func contractLibraryStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// contractLibraryJob creates a real jobs row and returns its id. saved_artifacts
// carries NO foreign key, so a job row is not strictly required for a save —
// but every non-survival test below uses one anyway, so that the survival test
// is measuring the ABSENCE of a cascade rather than the absence of a parent.
func contractLibraryJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug, Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob(%q): %v", slug, err)
	}
	return id
}

// contractLibrarySave calls the frozen accessor and fails the test on error —
// the happy path is the subject of nearly every assertion here, so its error
// arm is noise at each call site.
func contractLibrarySave(t *testing.T, s *Store, jobID int64, slug, name string, snapshot []byte, savedAt int64) int64 {
	t.Helper()
	id, err := s.SaveArtifact(jobID, slug, name, snapshot, savedAt)
	if err != nil {
		t.Fatalf("SaveArtifact(%d, %q, %q, %d bytes, %d): %v", jobID, slug, name, len(snapshot), savedAt, err)
	}
	if id <= 0 {
		t.Fatalf("SaveArtifact returned row id %d, want a real row id (the contract returns the row id either way)", id)
	}
	return id
}

// contractLibraryScalar reads a single integer out of the store's own handle —
// used only where the obligation is about the TABLE (its shape, its survival)
// rather than about an accessor's return value.
func contractLibraryScalar(t *testing.T, s *Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("scalar (%s): %v", query, err)
	}
	return n
}

// contractLibraryMetaByID finds one meta row in a list, or fails.
func contractLibraryMetaByID(t *testing.T, metas []SavedArtifactMeta, id int64) SavedArtifactMeta {
	t.Helper()
	for _, m := range metas {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("row %d absent from ListSavedArtifacts: %+v", id, metas)
	return SavedArtifactMeta{}
}

// contractLibraryIDs projects a meta list to its ids, for order assertions.
func contractLibraryIDs(metas []SavedArtifactMeta) []int64 {
	out := make([]int64, 0, len(metas))
	for _, m := range metas {
		out = append(out, m.ID)
	}
	return out
}

// contractLibraryOpenV23 builds a genuine v23-shaped database by replaying
// migrations[1..23] exactly as migrate() does, so the v24 assertions below run
// against a database that REACHED v24 by migration rather than by fresh
// creation. This is what pins the contract's "Never edit released schemaV23 in
// place": a v24 table folded into the v23 statement would leave a real v23
// database (every machine that has already run this binary) without the table
// forever, and a fresh-Open test cannot see that.
func contractLibraryOpenV23(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kraken.db")
	dsn := fmt.Sprintf(
		"file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open v23 db: %v", err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin v23 build: %v", err)
	}
	for v := 1; v <= 23; v++ {
		step, ok := migrations[v]
		if !ok {
			tx.Rollback()
			t.Fatalf("no migration registered for version %d", v)
		}
		if _, err := tx.Exec(step.sql); err != nil {
			tx.Rollback()
			t.Fatalf("apply migration %d: %v", v, err)
		}
		if step.hook != nil {
			if err := step.hook(tx); err != nil {
				tx.Rollback()
				t.Fatalf("apply migration %d hook: %v", v, err)
			}
		}
	}
	if _, err := tx.Exec(`PRAGMA user_version = 23`); err != nil {
		tx.Rollback()
		t.Fatalf("stamp user_version 23: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v23 build: %v", err)
	}
	return db, path
}

// ---------------------------------------------------------------------------
// schema v24 — the table's frozen shape
// ---------------------------------------------------------------------------

// TestContractLibrarySchemaV24TableShape pins every column-level clause the
// contract freezes for `saved_artifacts`, and the one that carries the whole
// feature: NO foreign key and NO cascade on job_id (the job_tokens survival
// rule). A schema with an FK would pass every accessor test in this file and
// still lose the library the moment a job is hard-deleted.
func TestContractLibrarySchemaV24TableShape(t *testing.T) {
	s := contractLibraryStore(t)

	// The version is a FLOOR, not a pin: the contract requires v24's effects to
	// be present, not the ladder frozen — and an exact pin has broken on the
	// two migrations after every previous contract that tried one.
	if schemaVersion < 24 {
		t.Fatalf("schemaVersion = %d, want >= 24 (the contract adds saved_artifacts as v24)", schemaVersion)
	}
	if n := contractLibraryScalar(t, s,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='saved_artifacts'`); n != 1 {
		t.Fatal("saved_artifacts table absent — schema v24 creates it")
	}

	// Exactly the six frozen columns, no more.
	type colInfo struct {
		name     string
		notNull  int64
		dflt     sql.NullString
		pk       int64
		declared string
	}
	rows, err := s.db.Query(`SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info('saved_artifacts')`)
	if err != nil {
		t.Fatalf("pragma_table_info: %v", err)
	}
	defer rows.Close()
	cols := map[string]colInfo{}
	var order []string
	for rows.Next() {
		var c colInfo
		if err := rows.Scan(&c.name, &c.declared, &c.notNull, &c.dflt, &c.pk); err != nil {
			t.Fatal(err)
		}
		cols[c.name] = c
		order = append(order, c.name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := []string{"id", "job_id", "slug", "name", "snapshot", "saved_at"}
	if len(order) != len(want) {
		t.Fatalf("saved_artifacts columns = %v, want exactly %v", order, want)
	}
	for _, name := range want {
		if _, ok := cols[name]; !ok {
			t.Fatalf("saved_artifacts is missing the %q column (have %v)", name, order)
		}
	}
	if cols["id"].pk != 1 {
		t.Fatalf("id pk flag = %d, want 1 (INTEGER PRIMARY KEY AUTOINCREMENT)", cols["id"].pk)
	}
	for _, name := range []string{"job_id", "slug", "name", "snapshot", "saved_at"} {
		if cols[name].notNull != 1 {
			t.Fatalf("column %q is nullable — the contract declares it NOT NULL", name)
		}
	}
	if !cols["saved_at"].dflt.Valid || cols["saved_at"].dflt.String != "0" {
		t.Fatalf("saved_at default = %v, want 0 (the contract's DEFAULT 0)", cols["saved_at"].dflt)
	}

	// The load-bearing clause: no foreign key at all on this table.
	if n := contractLibraryScalar(t, s,
		`SELECT COUNT(*) FROM pragma_foreign_key_list('saved_artifacts')`); n != 0 {
		t.Fatalf("saved_artifacts declares %d foreign key(s) — the contract requires NO foreign key and NO cascade (the job_tokens survival rule)", n)
	}

	// AUTOINCREMENT, observable through sqlite's own bookkeeping table: it is
	// what guarantees a deleted row's id is never handed to a later save, and
	// the library's urls are id-addressed.
	jobID := contractLibraryJob(t, s, "schema-shape")
	contractLibrarySave(t, s, jobID, "schema-shape", "a.md", []byte("a"), 10)
	if n := contractLibraryScalar(t, s,
		`SELECT COUNT(*) FROM sqlite_sequence WHERE name='saved_artifacts'`); n != 1 {
		t.Fatal("no sqlite_sequence row for saved_artifacts — the contract declares id INTEGER PK AUTOINCREMENT")
	}

	// UNIQUE(job_id, name), asserted against the TABLE rather than through the
	// upsert accessor (which is separately pinned below): the constraint is
	// what makes the upsert expressible at all.
	_, err = s.db.Exec(`INSERT INTO saved_artifacts (job_id, slug, name, snapshot, saved_at) VALUES (?, ?, ?, ?, ?)`,
		jobID, "schema-shape", "a.md", []byte("b"), 20)
	if err == nil {
		t.Fatal("a raw duplicate (job_id, name) insert succeeded — the contract declares UNIQUE(job_id, name)")
	}
	// A different name under the same job, and the same name under a different
	// job, are both legal: the constraint is on the PAIR.
	if _, err := s.db.Exec(`INSERT INTO saved_artifacts (job_id, slug, name, snapshot, saved_at) VALUES (?, ?, ?, ?, ?)`,
		jobID, "schema-shape", "b.md", []byte("b"), 20); err != nil {
		t.Fatalf("a second NAME under the same job was refused: %v — UNIQUE is on (job_id, name)", err)
	}
	other := contractLibraryJob(t, s, "schema-shape-2")
	if _, err := s.db.Exec(`INSERT INTO saved_artifacts (job_id, slug, name, snapshot, saved_at) VALUES (?, ?, ?, ?, ?)`,
		other, "schema-shape-2", "a.md", []byte("c"), 30); err != nil {
		t.Fatalf("the same NAME under a different job was refused: %v — UNIQUE is on (job_id, name)", err)
	}
}

// TestContractLibraryV23MigratesForward pins that saved_artifacts arrives as a
// NEW migration step rather than as an edit to the released v23 statement: a
// database built at v23 and reopened must reach schemaVersion and carry the
// table, with its pre-existing rows intact.
func TestContractLibraryV23MigratesForward(t *testing.T) {
	db, path := contractLibraryOpenV23(t)
	if err := db.Close(); err != nil {
		t.Fatalf("close v23 db: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate a v23 database forward: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if v := contractLibraryScalar(t, s, `PRAGMA user_version`); v != int64(schemaVersion) {
		t.Fatalf("user_version = %d, want %d after migrating v23 forward", v, schemaVersion)
	}
	if n := contractLibraryScalar(t, s,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='saved_artifacts'`); n != 1 {
		t.Fatal("a real v23 database did not gain saved_artifacts — v24 must be its own migration step, never an edit to the released schemaV23")
	}
	// The new table is usable straight away on a migrated database.
	jobID := contractLibraryJob(t, s, "post-migration")
	id := contractLibrarySave(t, s, jobID, "post-migration", "00-overview.md", []byte("hello"), 42)
	got, err := s.GetSavedArtifact(id)
	if err != nil || got == nil || !bytes.Equal(got.Snapshot, []byte("hello")) {
		t.Fatalf("GetSavedArtifact after migrating forward = %+v, %v", got, err)
	}
}

// ---------------------------------------------------------------------------
// SaveArtifact — the upsert
// ---------------------------------------------------------------------------

// TestContractLibrarySaveArtifactUpsertKeepsRowID pins the accessor's whole
// stated contract: upsert by (job_id, name); a re-save with the same pair
// REPLACES snapshot/slug/saved_at and RETURNS THE SAME row id; the row id is
// returned either way. Row identity is what the library's url is built from,
// so a re-save that minted a new id would silently orphan every reference to
// the old one.
func TestContractLibrarySaveArtifactUpsertKeepsRowID(t *testing.T) {
	s := contractLibraryStore(t)
	jobID := contractLibraryJob(t, s, "upsert")

	first := contractLibrarySave(t, s, jobID, "upsert", "50-contract.md", []byte("version one"), 1000)

	got, err := s.GetSavedArtifact(first)
	if err != nil {
		t.Fatalf("GetSavedArtifact(%d): %v", first, err)
	}
	want := &SavedArtifact{
		ID: first, JobID: jobID, Slug: "upsert", Name: "50-contract.md",
		Snapshot: []byte("version one"), SavedAt: 1000,
	}
	if got == nil || got.ID != want.ID || got.JobID != want.JobID || got.Slug != want.Slug ||
		got.Name != want.Name || got.SavedAt != want.SavedAt || !bytes.Equal(got.Snapshot, want.Snapshot) {
		t.Fatalf("saved row = %+v, want %+v (every field round-trips)", got, want)
	}

	// The re-save: same (job_id, name), everything else different.
	second, err := s.SaveArtifact(jobID, "upsert-renamed", "50-contract.md", []byte("version two"), 2000)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if second != first {
		t.Fatalf("re-save returned row id %d, want the same row %d — the contract upserts by (job_id, name) and keeps the id", second, first)
	}
	if n := contractLibraryScalar(t, s, `SELECT COUNT(*) FROM saved_artifacts`); n != 1 {
		t.Fatalf("%d rows after a re-save, want 1 — a re-save never duplicates", n)
	}
	got, err = s.GetSavedArtifact(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Snapshot, []byte("version two")) {
		t.Fatalf("snapshot = %q, want the re-saved bytes — a re-save REPLACES the snapshot", got.Snapshot)
	}
	if got.SavedAt != 2000 {
		t.Fatalf("saved_at = %d, want 2000 — a re-save replaces saved_at", got.SavedAt)
	}
	if got.Slug != "upsert-renamed" {
		t.Fatalf("slug = %q, want the re-saved display slug — a re-save replaces slug", got.Slug)
	}

	// A different NAME under the same job is a different row.
	otherName := contractLibrarySave(t, s, jobID, "upsert", "90-build-report.md", []byte("x"), 3000)
	if otherName == first {
		t.Fatalf("a second name under the same job reused row %d — the key is (job_id, name)", first)
	}
	// The same NAME under a different job is a different row.
	otherJob := contractLibraryJob(t, s, "upsert-2")
	crossJob := contractLibrarySave(t, s, otherJob, "upsert-2", "50-contract.md", []byte("y"), 4000)
	if crossJob == first || crossJob == otherName {
		t.Fatalf("the same name under a different job reused an existing row (%d) — the key is (job_id, name)", crossJob)
	}
	if n := contractLibraryScalar(t, s, `SELECT COUNT(*) FROM saved_artifacts`); n != 3 {
		t.Fatalf("%d rows, want 3", n)
	}
}

// TestContractLibrarySaveArtifactKeepsNestedNamesVerbatim pins that the store
// spelling of a name — nested and slash-separated, as the wire's `url` carries
// it "with name exactly as stored (slashes intact)" — survives a round trip
// unmangled, and that two nested names under one job are two rows.
func TestContractLibrarySaveArtifactKeepsNestedNamesVerbatim(t *testing.T) {
	s := contractLibraryStore(t)
	jobID := contractLibraryJob(t, s, "nested")

	a := contractLibrarySave(t, s, jobID, "nested", "icons/v1.png", []byte("one"), 10)
	b := contractLibrarySave(t, s, jobID, "nested", "icons/deep/v2.png", []byte("two"), 20)
	if a == b {
		t.Fatal("two distinct nested names collapsed onto one row")
	}
	for id, name := range map[int64]string{a: "icons/v1.png", b: "icons/deep/v2.png"} {
		got, err := s.GetSavedArtifact(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Name != name {
			t.Fatalf("name = %q, want %q verbatim (slashes intact)", got.Name, name)
		}
	}
}

// ---------------------------------------------------------------------------
// ListSavedArtifacts — order, meta-only
// ---------------------------------------------------------------------------

// TestContractLibraryListOrderIsSavedAtDescThenIDDesc pins the one ordering
// both clients render in served order and never re-sort: saved_at DESC, ties
// broken by id DESC (newest save first, and among simultaneous saves the most
// recently written row first).
func TestContractLibraryListOrderIsSavedAtDescThenIDDesc(t *testing.T) {
	s := contractLibraryStore(t)
	jobID := contractLibraryJob(t, s, "ordering")

	oldest := contractLibrarySave(t, s, jobID, "ordering", "oldest.md", []byte("o"), 100)
	tieLow := contractLibrarySave(t, s, jobID, "ordering", "tie-low.md", []byte("a"), 500)
	tieHigh := contractLibrarySave(t, s, jobID, "ordering", "tie-high.md", []byte("b"), 500)
	newest := contractLibrarySave(t, s, jobID, "ordering", "newest.md", []byte("n"), 900)
	if tieHigh <= tieLow {
		t.Fatalf("fixture: expected ascending row ids for the tie pair, got %d then %d", tieLow, tieHigh)
	}

	metas, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatalf("ListSavedArtifacts: %v", err)
	}
	got := contractLibraryIDs(metas)
	want := []int64{newest, tieHigh, tieLow, oldest}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list order = %v, want %v (saved_at DESC, ties id DESC)", got, want)
	}
}

// TestContractLibraryListIsMetaOnlyWithSize pins the meta form: Size is
// LENGTH(snapshot) for every row, and the meta type carries no bytes at all —
// the list is the board's hot read and the blob column is deliberately outside
// it.
func TestContractLibraryListIsMetaOnlyWithSize(t *testing.T) {
	s := contractLibraryStore(t)
	jobID := contractLibraryJob(t, s, "meta")

	// The type itself: no []byte field anywhere on SavedArtifactMeta. A blob
	// field would be a promise that the list reads the column.
	metaType := reflect.TypeOf(SavedArtifactMeta{})
	for i := 0; i < metaType.NumField(); i++ {
		f := metaType.Field(i)
		if f.Type.Kind() == reflect.Slice && f.Type.Elem().Kind() == reflect.Uint8 {
			t.Fatalf("SavedArtifactMeta.%s is a byte slice — the meta form never carries the snapshot (LENGTH only)", f.Name)
		}
	}

	big := bytes.Repeat([]byte("x"), 4096)
	// A multi-byte name/body: Size is LENGTH(snapshot) over BYTES, which is what
	// a blob length is.
	utf8Body := []byte("héllo — ünïcode")
	empty := contractLibrarySave(t, s, jobID, "meta", "empty.md", []byte{}, 10)
	small := contractLibrarySave(t, s, jobID, "meta", "small.md", []byte("hello"), 20)
	multi := contractLibrarySave(t, s, jobID, "meta", "utf8.md", utf8Body, 30)
	large := contractLibrarySave(t, s, jobID, "meta", "large.html", big, 40)

	metas, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	for id, wantSize := range map[int64]int64{
		empty: 0,
		small: int64(len("hello")),
		multi: int64(len(utf8Body)),
		large: int64(len(big)),
	} {
		m := contractLibraryMetaByID(t, metas, id)
		if m.Size != wantSize {
			t.Fatalf("row %d Size = %d, want LENGTH(snapshot) = %d", id, m.Size, wantSize)
		}
	}

	// The identity fields the clients render still ride the meta form.
	m := contractLibraryMetaByID(t, metas, small)
	if m.JobID != jobID || m.Slug != "meta" || m.Name != "small.md" || m.SavedAt != 20 {
		t.Fatalf("meta row = %+v, want job %d / slug meta / name small.md / saved_at 20", m, jobID)
	}
}

// TestContractLibraryListEmptyTableIsNotAnError pins the empty case: no rows
// is an empty list and a nil error, never a miss.
func TestContractLibraryListEmptyTableIsNotAnError(t *testing.T) {
	s := contractLibraryStore(t)
	metas, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatalf("ListSavedArtifacts on an empty table: %v, want nil error", err)
	}
	if len(metas) != 0 {
		t.Fatalf("ListSavedArtifacts on an empty table = %+v, want no rows", metas)
	}
}

// ---------------------------------------------------------------------------
// GetSavedArtifact / DeleteSavedArtifact — the ErrNotFound arms
// ---------------------------------------------------------------------------

// TestContractLibraryGetSavedArtifactMissIsErrNotFound pins the stated miss
// behaviour: `store.ErrNotFound`, so the bytes route can answer 404 rather
// than 500 for a row that is simply gone.
func TestContractLibraryGetSavedArtifactMissIsErrNotFound(t *testing.T) {
	s := contractLibraryStore(t)
	jobID := contractLibraryJob(t, s, "get-miss")
	live := contractLibrarySave(t, s, jobID, "get-miss", "a.md", []byte("a"), 10)

	got, err := s.GetSavedArtifact(live + 1000)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSavedArtifact(unknown id) = %+v, %v; want ErrNotFound", got, err)
	}
	// ...and the live row is still readable, so the miss is about the id.
	if got, err := s.GetSavedArtifact(live); err != nil || got == nil || got.ID != live {
		t.Fatalf("GetSavedArtifact(%d) = %+v, %v", live, got, err)
	}
}

// TestContractLibraryDeleteRemovesExactlyOneRow pins both delete arms: nil
// when one row was deleted, ErrNotFound when none was (never a silent
// no-op), and exactly one row leaves the table.
func TestContractLibraryDeleteRemovesExactlyOneRow(t *testing.T) {
	s := contractLibraryStore(t)
	jobID := contractLibraryJob(t, s, "delete")
	a := contractLibrarySave(t, s, jobID, "delete", "a.md", []byte("a"), 10)
	b := contractLibrarySave(t, s, jobID, "delete", "b.md", []byte("b"), 20)
	otherJob := contractLibraryJob(t, s, "delete-2")
	c := contractLibrarySave(t, s, otherJob, "delete-2", "a.md", []byte("c"), 30)

	if err := s.DeleteSavedArtifact(a); err != nil {
		t.Fatalf("DeleteSavedArtifact(%d) = %v, want nil when a row was deleted", a, err)
	}
	if _, err := s.GetSavedArtifact(a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("the deleted row is still readable: %v", err)
	}
	metas, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Fatalf("%d rows survive, want 2 — delete removes exactly one row", len(metas))
	}
	contractLibraryMetaByID(t, metas, b)
	contractLibraryMetaByID(t, metas, c)

	// The repeat: nothing left to remove is ErrNotFound, not success.
	if err := s.DeleteSavedArtifact(a); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat DeleteSavedArtifact(%d) = %v, want ErrNotFound (never a silent no-op)", a, err)
	}
	if err := s.DeleteSavedArtifact(c + 1000); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteSavedArtifact(unknown id) = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// survival — the promise no other table makes
// ---------------------------------------------------------------------------

// TestContractLibrarySurvivesJobHardDelete is the contract's `survival` row and
// the one behaviour the whole feature exists for: after `DELETE FROM jobs WHERE
// id = <job_id>` the saved rows remain LISTABLE and READABLE, byte-identical.
// The delete is issued as raw SQL, exactly as the contract words it, so the
// assertion is about the TABLE's shape rather than about any accessor's
// politeness.
func TestContractLibrarySurvivesJobHardDelete(t *testing.T) {
	s := contractLibraryStore(t)
	doomed := contractLibraryJob(t, s, "doomed")
	survivor := contractLibraryJob(t, s, "survivor")

	// Bytes chosen to be exactly recoverable: a NUL, a high byte and multi-byte
	// UTF-8, so "byte-identical" means bytes rather than "string equal".
	htmlBytes := []byte("<h1>frozen\x00\xff — héllo</h1>")
	svgBytes := []byte("<svg><text>diagram</text></svg>")

	a := contractLibrarySave(t, s, doomed, "doomed", "00-overview.html", htmlBytes, 1000)
	b := contractLibrarySave(t, s, doomed, "doomed", "icons/x.svg", svgBytes, 2000)
	c := contractLibrarySave(t, s, survivor, "survivor", "00-overview.html", []byte("untouched"), 3000)

	if _, err := s.db.Exec(`DELETE FROM jobs WHERE id = ?`, doomed); err != nil {
		t.Fatalf("DELETE FROM jobs WHERE id = %d: %v", doomed, err)
	}
	if n := contractLibraryScalar(t, s, `SELECT COUNT(*) FROM jobs WHERE id = ?`, doomed); n != 0 {
		t.Fatalf("the job row survived the hard delete (%d) — fixture is not exercising the survival case", n)
	}

	// Still listable: all three rows, in the contract's order.
	metas, err := s.ListSavedArtifacts()
	if err != nil {
		t.Fatalf("ListSavedArtifacts after the hard delete: %v", err)
	}
	if got, want := contractLibraryIDs(metas), []int64{c, b, a}; !reflect.DeepEqual(got, want) {
		t.Fatalf("list after hard delete = %v, want %v — no row may be cascaded away", got, want)
	}

	// Still readable, byte-identical, with provenance intact: the row keeps
	// pointing at the id of a job that no longer exists, which is the whole
	// point of there being no foreign key.
	for id, want := range map[int64][]byte{a: htmlBytes, b: svgBytes} {
		got, err := s.GetSavedArtifact(id)
		if err != nil {
			t.Fatalf("GetSavedArtifact(%d) after the hard delete: %v", id, err)
		}
		if !bytes.Equal(got.Snapshot, want) {
			t.Fatalf("row %d snapshot = %q, want the frozen bytes %q", id, got.Snapshot, want)
		}
		if got.JobID != doomed {
			t.Fatalf("row %d job_id = %d, want the dead job's id %d preserved", id, got.JobID, doomed)
		}
	}
	// The display slug frozen at save time also survives — it is the row's own
	// copy, not a join.
	got, err := s.GetSavedArtifact(a)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slug != "doomed" || got.Name != "00-overview.html" || got.SavedAt != 1000 {
		t.Fatalf("row = %+v, want slug/name/saved_at frozen at save time", got)
	}

	// A save is still possible against the dead job id: nothing validates the
	// parent, so a re-save of a torn-down job's artifact still upserts its row.
	again, err := s.SaveArtifact(doomed, "doomed", "00-overview.html", []byte("re-saved"), 4000)
	if err != nil {
		t.Fatalf("re-save against a deleted job id: %v", err)
	}
	if again != a {
		t.Fatalf("re-save after the hard delete minted row %d, want the same row %d", again, a)
	}
}
