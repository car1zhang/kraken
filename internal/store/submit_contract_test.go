package store

import (
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
)

// submit_contract_test.go — the LOGIC dimension of "eliminate the queued
// status" in the store, authored blind from artifacts/50-contract.md.
//
// Every package-level identifier is prefixed `contract`; a sibling edge author
// shares this package's test binary.
//
// What this file pins:
//   - schemaVersion == 20.
//   - migration 20 against a genuine v19-shaped database: the retired job
//     columns are gone, every queued board_order row is gone, and the rows that
//     had nothing to do with either survive untouched.

// contractTableColumns returns the column names of one table as a set, read
// through PRAGMA table_info so the assertion speaks about the database's actual
// shape rather than about a Go struct's fields.
func contractTableColumns(t *testing.T, s *Store, table string) map[string]bool {
	t.Helper()
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		t.Fatalf("table_info(%s): %v", table, err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info(%s) row: %v", table, err)
		}
		cols[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info(%s) rows: %v", table, err)
	}
	if len(cols) == 0 {
		t.Fatalf("table_info(%s) returned no columns — no such table?", table)
	}
	return cols
}

func submitContractUserVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// TestSubmitContractSchemaVersionIsTwenty: the migration that drops the retired
// job columns and prunes the queued board order is version 20 — pinned as
// migrations[20] still being that step, plus the version having reached it.
// It opened as a schemaVersion == 20 equality, which the very next appended
// migration (v21, the rename feature's former-slug table, joined at the
// rebase) broke while breaking nothing about the queued retirement —
// recall_contract_test.go documents retiring the same fragility the same way.
func TestSubmitContractSchemaVersionIsTwenty(t *testing.T) {
	if migrations[20].sql != schemaV20 {
		t.Fatalf("migrations[20] is not schemaV20 — the queued retirement must arrive at v20")
	}
	if schemaVersion < 20 {
		t.Fatalf("schemaVersion = %d, want at least 20", schemaVersion)
	}
}

// TestSubmitContractMigration20FromV19: the real-world path — a kraken.db
// sitting at user_version 19 on this machine, opened by a binary carrying
// migration 20.
//
// The fixture is built with downgradeFreshDB (schema_test.go), whose documented
// job is to reproduce the FULL shape a genuine database at that version had:
// asking it for a v19 shape is what forces migration 20's reversal to be
// written there too, and the ready-column assertion below fails loudly rather
// than vacuously if it was not.
//
// Three things are asserted after the migration, and each has a distinct
// failure mode: the columns being gone is the schema change itself; the queued
// board_order rows being gone is the data half (nothing else ever prunes them —
// there is no CHECK constraint and no client will save that column again); and
// the surviving rows being untouched is what separates a migration from a wipe.
func TestSubmitContractMigration20FromV19(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	// two jobs whose rows must survive, and whose ids the board order points at
	queuedID, err := s.CreateJob(&Job{Slug: "was-queued", Repo: "kraken", Base: "main", Body: "spec one\n"})
	if err != nil {
		t.Fatalf("CreateJob was-queued: %v", err)
	}
	workingID, err := s.CreateJob(&Job{Slug: "still-working", Repo: "monolith", Base: "main", Body: "spec two\n", State: "working"})
	if err != nil {
		t.Fatalf("CreateJob still-working: %v", err)
	}

	// one board_order row per column: the queued one the migration deletes, the
	// working one it must leave completely alone.
	if err := s.SetBoardColumn("queued", []int64{queuedID}); err != nil {
		t.Fatalf("SetBoardColumn queued: %v", err)
	}
	if err := s.SetBoardColumn("working", []int64{workingID}); err != nil {
		t.Fatalf("SetBoardColumn working: %v", err)
	}

	downgradeFreshDB(t, s, 19)

	// fixture faithfulness: a v19 database HAS the columns migration 20 drops.
	// Without this the test below could pass against a fixture that never had
	// them, proving nothing.
	if cols := contractTableColumns(t, s, "jobs"); !cols["ready"] {
		t.Fatalf("the v19 fixture has no jobs.ready column — downgradeFreshDB needs a `target < 20` arm restoring what migration 20 removes")
	}
	if got := submitContractUserVersion(t, s); got != 19 {
		t.Fatalf("fixture user_version = %d, want 19", got)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close before re-Open: %v", err)
	}

	// the real entry point: a fresh process opening a v19-shaped file.
	s2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("re-Open a v19-shaped db (migration 20 must apply cleanly): %v", err)
	}
	defer s2.Close()

	if got := submitContractUserVersion(t, s2); got != schemaVersion {
		t.Fatalf("user_version after migration = %d, want %d", got, schemaVersion)
	}

	cols := contractTableColumns(t, s2, "jobs")
	for _, retired := range []string{"ready", "prereqs"} {
		if cols[retired] {
			t.Errorf("jobs.%s survived migration 20", retired)
		}
	}
	// the columns the change does NOT touch: a migration that rebuilt the table
	// and lost one of these would be invisible to the two checks above.
	for _, kept := range []string{"id", "slug", "repo", "base", "body", "state", "rev"} {
		if !cols[kept] {
			t.Errorf("jobs.%s is missing after migration 20 — the migration removed more than it should", kept)
		}
	}

	// the data half: the queued column's rows are gone, every other column's
	// rows are exactly as they were.
	order, err := s2.GetBoardOrder()
	if err != nil {
		t.Fatalf("GetBoardOrder: %v", err)
	}
	if got, ok := order["queued"]; ok {
		t.Errorf(`board_order still holds a "queued" column: %v`, got)
	}
	if !reflect.DeepEqual(order["working"], []int64{workingID}) {
		t.Errorf("board_order[working] = %v, want %v — non-queued rows must survive untouched", order["working"], []int64{workingID})
	}

	// the jobs themselves: both rows intact, ids preserved (the board order
	// above points at them), and the formerly-queued one now reads as a plain
	// unstarted draft row.
	list, err := s2.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListJobs returned %d rows, want the 2 seeded", len(list))
	}
	wasQueued, err := s2.GetJobBySlug("was-queued")
	if err != nil {
		t.Fatalf("the formerly-queued job's row did not survive migration 20: %v", err)
	}
	if wasQueued.ID != queuedID || wasQueued.Repo != "kraken" || wasQueued.Body != "spec one\n" {
		t.Errorf("was-queued row mangled: %+v", wasQueued)
	}
	if wasQueued.State != "" {
		t.Errorf("was-queued state = %q, want empty — a formerly-queued job reads as draft, and carl re-submits it", wasQueued.State)
	}
	working, err := s2.GetJobBySlug("still-working")
	if err != nil {
		t.Fatalf("the working job's row did not survive migration 20: %v", err)
	}
	if working.ID != workingID || working.State != "working" || working.Body != "spec two\n" {
		t.Errorf("still-working row mangled: %+v", working)
	}
}

// TestSubmitContractFreshOpenHasNoRetiredJobColumns is the other direction: a
// database created today must never carry the retired columns at all — a
// migration that dropped them while the base schema string still declared them
// would leave a fresh db and a migrated one disagreeing, which only a shape
// comparison catches.
func TestSubmitContractFreshOpenHasNoRetiredJobColumns(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := openTest(t)
	cols := contractTableColumns(t, s, "jobs")
	for _, retired := range []string{"ready", "prereqs"} {
		if cols[retired] {
			t.Errorf("a freshly created database still declares jobs.%s", retired)
		}
	}
}
