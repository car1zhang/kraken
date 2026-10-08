package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// kronosremoval_contract_test.go — athena-logic's BLIND contract tests for
// the kronos removal's schema half (artifacts/50-contract.md slice G, rows
// `store.schemaVersion` and "v31 migration"). Written from the contract text
// alone; no non-test file in this package was read. Dimension: LOGIC — a
// fresh store lands at 31 without orch_inbox and with the Slack tables, and a
// v30 database migrates forward dropping exactly the kronos state.

// contractKronosRemovalKronosKeys are the five kv rows v31 deletes (Q5).
var contractKronosRemovalKronosKeys = []string{
	"kronos_agent",
	"kronos_ctx",
	"kronos_ctx_warned",
	"limit_hit:kronos",
	"fable_fallback:kronos",
}

func contractKronosRemovalUserVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("PRAGMA user_version: %v", err)
	}
	return v
}

func contractKronosRemovalHasTable(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var got string
	err := s.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?`, name).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("sqlite_master lookup %q: %v", name, err)
	}
	return true
}

// contractKronosRemovalHasAnyMasterRow: "sqlite_master holds no row named
// orch_inbox" — any type (table, index, trigger), not just tables.
func contractKronosRemovalHasAnyMasterRow(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var n int
	if err := s.DB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, name).Scan(&n); err != nil {
		t.Fatalf("sqlite_master count %q: %v", name, err)
	}
	return n > 0
}

// Contract: `const schemaVersion = 31`.
func TestKronosRemovalContractSchemaVersionIs31(t *testing.T) {
	// At least 31 rather than exactly: the knowledge graph's removal appended
	// v32 after this step landed, and a later step never un-applies v31.
	if schemaVersion < 31 {
		t.Fatalf("schemaVersion = %d, want ≥ 31", schemaVersion)
	}
}

// Contract: a freshly opened store reports user_version 31, sqlite_master
// holds no row named orch_inbox, and slack_inbox / slack_marks still exist.
func TestKronosRemovalContractFreshStoreShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	if v := contractKronosRemovalUserVersion(t, s); v != schemaVersion {
		t.Errorf("user_version = %d, want the current %d", v, schemaVersion)
	}
	if contractKronosRemovalHasAnyMasterRow(t, s, "orch_inbox") {
		t.Error("a fresh store still has an orch_inbox row in sqlite_master")
	}
	for _, keep := range []string{"slack_inbox", "slack_marks"} {
		if !contractKronosRemovalHasTable(t, s, keep) {
			t.Errorf("fresh store is missing %s — iris' inbox must survive", keep)
		}
	}
}

// Contract: on a database at user_version 30 holding an orch_inbox table and
// the five kronos kv rows plus an unrelated one, Open succeeds, the table is
// gone, the five keys are gone, the unrelated key survives with its value,
// and user_version is 31. Fixture built exactly as the contract's test note
// prescribes.
func TestKronosRemovalContractV30MigratesToV31(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "kraken.db")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.DB().Exec(`CREATE TABLE orch_inbox (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatalf("create orch_inbox fixture: %v", err)
	}
	if _, err := s1.DB().Exec(`INSERT INTO orch_inbox (id) VALUES (1), (2)`); err != nil {
		t.Fatalf("seed orch_inbox: %v", err)
	}
	for _, k := range contractKronosRemovalKronosKeys {
		if err := s1.SetKV(k, `{"seed":"`+k+`"}`); err != nil {
			t.Fatalf("seed SetKV %q: %v", k, err)
		}
	}
	const keepKey, keepVal = "limit_hit:iris", `{"at":1769040000000,"error":"rate_limit"}`
	if err := s1.SetKV(keepKey, keepVal); err != nil {
		t.Fatalf("seed SetKV %q: %v", keepKey, err)
	}
	if _, err := s1.DB().Exec(`PRAGMA user_version = 30`); err != nil {
		t.Fatalf("set user_version 30: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("Open of a v30 database failed: %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	if v := contractKronosRemovalUserVersion(t, s2); v != schemaVersion {
		t.Errorf("user_version after migration = %d, want the current %d", v, schemaVersion)
	}
	if contractKronosRemovalHasAnyMasterRow(t, s2, "orch_inbox") {
		t.Error("orch_inbox survived the v31 migration")
	}
	for _, k := range contractKronosRemovalKronosKeys {
		v, err := s2.GetKV(k)
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("kv %q after migration = %q, %v; want ErrNotFound (deleted by v31)", k, v, err)
		}
	}
	got, err := s2.GetKV(keepKey)
	if err != nil {
		t.Fatalf("unrelated kv %q lost by the migration: %v", keepKey, err)
	}
	if got != keepVal {
		t.Errorf("unrelated kv %q = %q, want %q (byte-identical)", keepKey, got, keepVal)
	}
	for _, keep := range []string{"slack_inbox", "slack_marks"} {
		if !contractKronosRemovalHasTable(t, s2, keep) {
			t.Errorf("%s missing after the v31 migration", keep)
		}
	}
}
