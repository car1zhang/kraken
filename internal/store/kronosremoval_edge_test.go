package store

import (
	"errors"
	"path/filepath"
	"testing"
)

// kronosremoval_edge_test.go — the EDGE dimension of the v31 migration in
// slice G of artifacts/50-contract.md (kronos removal): `orch_inbox` of a
// shape nobody planned for, a v30 database that never had any kronos state at
// all, and re-opening a store that is already at v31.
//
// Written blind from the contract. The contract's own test note says the step
// "must tolerate a table of any shape (it only drops)" — these cases hold it
// to exactly that. Package-level identifiers carry the `edge` prefix.

var edgeKronosRemovalKeys = []string{
	"kronos_agent",
	"kronos_ctx",
	"kronos_ctx_warned",
	"limit_hit:kronos",
	"fable_fallback:kronos",
}

// edgeKronosRemovalNeighbours are kv rows that merely LOOK adjacent to the
// removed ones — the surviving agent's own rows and a numeric job's hit row.
// A migration that deleted by a loose pattern would take them too.
var edgeKronosRemovalNeighbours = map[string]string{
	"limit_hit:iris":      `{"at":1,"error":"rate_limit"}`,
	"fable_fallback:iris": `{"pid":7}`,
	"limit_hit:42":        `{"at":2,"error":"rate_limit"}`,
}

func edgeKronosRemovalOpen(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return s
}

func edgeKronosRemovalVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

func edgeKronosRemovalHasTable(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("sqlite_master lookup %q: %v", name, err)
	}
	return n > 0
}

func edgeKronosRemovalExec(t *testing.T, s *Store, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
}

func edgeKronosRemovalAssertAfter(t *testing.T, s *Store) {
	t.Helper()
	// The current version rather than a literal 31: the knowledge graph's
	// removal appended v32 after this step landed.
	if v := edgeKronosRemovalVersion(t, s); v != schemaVersion {
		t.Fatalf("user_version = %d, want the current %d", v, schemaVersion)
	}
	if schemaVersion < 31 {
		t.Fatalf("schemaVersion = %d, want ≥ 31", schemaVersion)
	}
	if edgeKronosRemovalHasTable(t, s, "orch_inbox") {
		t.Errorf("orch_inbox still in sqlite_master after migration")
	}
	for _, k := range edgeKronosRemovalKeys {
		if v, err := s.GetKV(k); !errors.Is(err, ErrNotFound) {
			t.Errorf("kv %q survived the migration (value %q, err %v)", k, v, err)
		}
	}
	for k, want := range edgeKronosRemovalNeighbours {
		if v, err := s.GetKV(k); err != nil || v != want {
			t.Errorf("neighbour kv %q = %q (err %v), want %q unchanged", k, v, err, want)
		}
	}
	for _, tbl := range []string{"slack_inbox", "slack_marks"} {
		if !edgeKronosRemovalHasTable(t, s, tbl) {
			t.Errorf("table %s is gone after migration; iris' tables must survive", tbl)
		}
	}
}

// edgeKronosRemovalV30 builds a v30-looking file at path: a fresh store,
// `setup` run against it, the neighbour rows seeded, user_version set to 30.
func edgeKronosRemovalV30(t *testing.T, setup func(s *Store)) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "kraken.db")
	s := edgeKronosRemovalOpen(t, path)
	setup(s)
	for k, v := range edgeKronosRemovalNeighbours {
		if err := s.SetKV(k, v); err != nil {
			t.Fatalf("SetKV %q: %v", k, err)
		}
	}
	edgeKronosRemovalExec(t, s, `PRAGMA user_version = 30`)
	if err := s.Close(); err != nil {
		t.Fatalf("close v30 fixture: %v", err)
	}
	return path
}

// An orch_inbox nobody planned for: extra columns, NOT NULL constraints, an
// index of its own, and rows in it. The step only drops — it must not care.
func TestKronosRemovalEdgeMigrationDropsOddlyShapedOrchInbox(t *testing.T) {
	path := edgeKronosRemovalV30(t, func(s *Store) {
		edgeKronosRemovalExec(t, s,
			`CREATE TABLE orch_inbox (
				id        INTEGER PRIMARY KEY AUTOINCREMENT,
				kind      TEXT NOT NULL,
				slug      TEXT NOT NULL DEFAULT '',
				body      TEXT NOT NULL DEFAULT '',
				delivered INTEGER NOT NULL DEFAULT 0,
				acked     INTEGER NOT NULL DEFAULT 0,
				extra_one BLOB,
				extra_two REAL DEFAULT 1.5
			)`,
			`CREATE INDEX orch_inbox_edge_idx ON orch_inbox(delivered, acked)`,
			`INSERT INTO orch_inbox (kind, slug, body) VALUES ('cell-gone', 'a', 'x'), ('pr-merged', 'b', 'y')`,
		)
		for _, k := range edgeKronosRemovalKeys {
			if err := s.SetKV(k, `{"edge":true}`); err != nil {
				t.Fatalf("SetKV %q: %v", k, err)
			}
		}
	})
	s := edgeKronosRemovalOpen(t, path)
	defer s.Close()
	edgeKronosRemovalAssertAfter(t, s)
	if edgeKronosRemovalHasTable(t, s, "orch_inbox_edge_idx") {
		t.Errorf("orch_inbox's own index survived the table drop")
	}
}

// A v30 database that never had kronos state at all: no orch_inbox, none of
// the five keys. The step must succeed anyway and still land on 31.
func TestKronosRemovalEdgeMigrationWithNothingToRemove(t *testing.T) {
	path := edgeKronosRemovalV30(t, func(s *Store) {
		// A fresh store at the new schema must already lack orch_inbox; drop
		// defensively so the fixture is the shape the test names regardless.
		edgeKronosRemovalExec(t, s, `DROP TABLE IF EXISTS orch_inbox`)
		for _, k := range edgeKronosRemovalKeys {
			if _, err := s.GetKV(k); !errors.Is(err, ErrNotFound) {
				t.Fatalf("fresh store already carries kv %q (err %v)", k, err)
			}
		}
	})
	s := edgeKronosRemovalOpen(t, path)
	defer s.Close()
	edgeKronosRemovalAssertAfter(t, s)
}

// Only SOME of the keys present, and the table present but empty: each
// removal must be independent of the others.
func TestKronosRemovalEdgeMigrationPartialState(t *testing.T) {
	path := edgeKronosRemovalV30(t, func(s *Store) {
		edgeKronosRemovalExec(t, s, `CREATE TABLE orch_inbox (id INTEGER PRIMARY KEY)`)
		for _, k := range []string{"kronos_ctx_warned", "fable_fallback:kronos"} {
			if err := s.SetKV(k, "1"); err != nil {
				t.Fatalf("SetKV %q: %v", k, err)
			}
		}
	})
	s := edgeKronosRemovalOpen(t, path)
	defer s.Close()
	edgeKronosRemovalAssertAfter(t, s)
}

// Re-opening a store that is ALREADY at v31 runs no migration step: rows
// written after v31 survive a close/open, whatever their key. Contract
// silence: the contract states the v30→v31 step, not that it never re-runs;
// the strict answer asserted is "a versioned step runs once".
func TestKronosRemovalEdgeReopenAtV31IsNoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "kraken.db")
	s := edgeKronosRemovalOpen(t, path)
	if v := edgeKronosRemovalVersion(t, s); v != schemaVersion {
		t.Fatalf("fresh open user_version = %d, want the current %d", v, schemaVersion)
	}
	if edgeKronosRemovalHasTable(t, s, "orch_inbox") {
		t.Fatalf("fresh store holds orch_inbox")
	}
	if err := s.SetKV("limit_hit:iris", "kept"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetKV("kronos_agent", "written-after-v31"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s2 := edgeKronosRemovalOpen(t, path)
	defer s2.Close()
	if v := edgeKronosRemovalVersion(t, s2); v != schemaVersion {
		t.Fatalf("re-open user_version = %d, want the current %d", v, schemaVersion)
	}
	if v, err := s2.GetKV("limit_hit:iris"); err != nil || v != "kept" {
		t.Errorf("limit_hit:iris after re-open = %q (err %v), want kept", v, err)
	}
	if v, err := s2.GetKV("kronos_agent"); err != nil || v != "written-after-v31" {
		t.Errorf("a row written at v31 changed on re-open (= %q, err %v): the v31 step re-ran", v, err)
	}
	for _, tbl := range []string{"slack_inbox", "slack_marks"} {
		if !edgeKronosRemovalHasTable(t, s2, tbl) {
			t.Errorf("table %s missing after re-open", tbl)
		}
	}
}
