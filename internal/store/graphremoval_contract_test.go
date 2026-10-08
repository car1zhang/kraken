package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// graphremoval_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice A (store): migration v32, the knowledge-graph
// removal. Written from the frozen contract alone; internal/store/*.go (non-test)
// was never read. Dimension: LOGIC — the migration lands, the graph objects and
// kv rows go, every row carl owns survives byte-identical, a re-open is a no-op.
// Boundaries and refusals are athena-edge's.
//
// Every package-level identifier here is prefixed `contract`.

// contractGRKgTables are the seven graph tables the contract's fixture names,
// plus the plain stand-in for the FTS virtual table.
var contractGRKgTables = []string{
	"kg_edges", "kg_nodes", "kg_node_aliases", "kg_episodes",
	"kg_ingest_cursor", "kg_layout", "kg_recall", "kg_edges_fts",
}

// contractGRDeletedKV are the kv keys v32 must delete.
var contractGRDeletedKV = []string{"kg.jobvec.1", "kg.jobvechash.1", "graph_ingest_last"}

// contractGRIsolate is the Test environment obligation: isolate HOME and unset
// OPENAI_API_KEY and KRAKEN_DB_PATH before opening a store.
func contractGRIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"OPENAI_API_KEY", "KRAKEN_DB_PATH"} {
		t.Setenv(k, "") // registers restore
		os.Unsetenv(k)
	}
}

// contractGRDump renders every row of table in rowid order as one string, so a
// before/after comparison is byte-for-byte over every column.
func contractGRDump(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	rows, err := db.Query(`SELECT * FROM ` + table + ` ORDER BY rowid`)
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns %s: %v", table, err)
	}
	var b strings.Builder
	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		for i, v := range vals {
			if bs, ok := v.([]byte); ok {
				v = string(bs)
			}
			fmt.Fprintf(&b, "%s=%#v;", cols[i], v)
		}
		b.WriteString("\n")
		n++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate %s: %v", table, err)
	}
	return fmt.Sprintf("%d rows\n%s", n, b.String())
}

// contractGRKgObjects lists every sqlite_master object whose name starts with
// `kg_` (tables, triggers, indexes alike), sorted.
func contractGRKgObjects(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT type || ':' || name FROM sqlite_master WHERE name LIKE 'kg\_%' ESCAPE '\'`)
	if err != nil {
		t.Fatalf("sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan sqlite_master: %v", err)
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func contractGRSHA(x string) string {
	sum := sha256.Sum256([]byte(x))
	return hex.EncodeToString(sum[:])
}

func contractGRVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	return v
}

// contractGRSurvivorTables are the tables whose rows must survive v32
// byte-identical.
var contractGRSurvivorTables = []string{"mm_items", "mm_code_anchors", "watch_items", "threads", "jobs"}

// contractGRSeedV31 builds the contract's pre-migration fixture: a fresh store
// rolled back to user_version 31 carrying every kg object, the three doomed kv
// rows plus one unrelated one, and one row in each survivor table. It closes
// the store and returns the path plus the survivor dumps taken just before the
// close.
func contractGRSeedV31(t *testing.T) (string, map[string]string) {
	t.Helper()
	contractGRIsolate(t)
	path := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("fresh Open: %v", err)
	}
	db := s.DB()

	// Survivor rows, through the store's own accessors.
	jobID, err := s.CreateJob(&Job{Slug: "graphremoval-survivor", Body: "keep me"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", CreatedAt: 1000}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	itemID, err := s.AddMMItem(MMItem{
		Kind: "reading", Source: "code", SourceRef: "kraken/internal/jobs/mm.go:10-12",
		Content: "func f() {\n\treturn\n}", CreatedAt: 10,
	})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	if err := s.AddMMCodeAnchor(MMCodeAnchor{
		ItemID: itemID, Repo: "kraken", Path: "internal/jobs/mm.go",
		StartLine: 10, EndLine: 12,
		AnchorSHA:  "1111111111111111111111111111111111111111",
		AnchorKind: "origin-main",
		Text:       "func f() {\n\treturn\n}",
		TextSHA:    contractGRSHA("func f() {\n\treturn\n}"),
		Lang:       "go", CheckedAt: 1_700_000_000_000,
		CheckedSHA: "1111111111111111111111111111111111111111",
	}); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	if _, _, err := s.AddWatchItem(WatchItem{
		Observation: "the graph removal keeps the watch store",
		Class:       "correction", Evidence: `{"fixture":true}`,
	}); err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	// An episode id stamped before v32 (the graph era's only writer is gone, so
	// raw SQL stands in for it): "existing rows return their stored value".
	if _, err := db.Exec(`UPDATE mm_items SET episode_id = 4242 WHERE id = ?`, itemID); err != nil {
		t.Fatalf("stamp episode_id: %v", err)
	}

	// The kg objects. v32 itself drops them on a fresh open, so on a fresh
	// store none of these names exists yet; IF NOT EXISTS keeps the fixture
	// honest either way.
	for _, tbl := range contractGRKgTables {
		if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS ` + tbl + ` (id INTEGER PRIMARY KEY, v TEXT)`); err != nil {
			t.Fatalf("create %s: %v", tbl, err)
		}
		if _, err := db.Exec(`INSERT INTO `+tbl+` (v) VALUES (?)`, "row-in-"+tbl); err != nil {
			t.Fatalf("seed %s: %v", tbl, err)
		}
	}
	for _, trg := range []struct{ name, when string }{
		{"kg_edges_ai", "AFTER INSERT"},
		{"kg_edges_ad", "AFTER DELETE"},
		{"kg_edges_au", "AFTER UPDATE"},
	} {
		q := `CREATE TRIGGER IF NOT EXISTS ` + trg.name + ` ` + trg.when +
			` ON kg_edges BEGIN SELECT 1; END`
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("create trigger %s: %v", trg.name, err)
		}
	}
	for _, k := range contractGRDeletedKV {
		if err := s.SetKV(k, "doomed"); err != nil {
			t.Fatalf("SetKV %s: %v", k, err)
		}
	}
	if err := s.SetKV("graphremoval_unrelated", "survive-me"); err != nil {
		t.Fatalf("SetKV unrelated: %v", err)
	}
	if got := contractGRKgObjects(t, db); len(got) != len(contractGRKgTables)+3 {
		t.Fatalf("fixture kg objects = %v, want %d", got, len(contractGRKgTables)+3)
	}

	before := map[string]string{}
	for _, tbl := range contractGRSurvivorTables {
		before[tbl] = contractGRDump(t, db, tbl)
		if strings.HasPrefix(before[tbl], "0 rows") {
			t.Fatalf("fixture seeded no %s row", tbl)
		}
	}
	if _, err := db.Exec(`PRAGMA user_version = 31`); err != nil {
		t.Fatalf("set user_version 31: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path, before
}

// "`const schemaVersion = 32`" — "opening a fresh database lands at 32 in one pass."
// PR 2 ('Carried from PR 1'): the exact version pin is now schemaVersion-relative.
func TestGraphRemovalSchemaVersionIs32(t *testing.T) {
	if schemaVersion < 32 {
		t.Fatalf("schemaVersion = %d, want >= 32", schemaVersion)
	}
	contractGRIsolate(t)
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if v := contractGRVersion(t, s.DB()); v != schemaVersion {
		t.Fatalf("fresh user_version = %d, want %d", v, schemaVersion)
	}
}

// "After migration no table whose name starts with `kg_` exists (`sqlite_master`)"
// — the fresh-database half: a fresh database reaches the same shape.
func TestGraphRemovalFreshDatabaseHasNoKgObjects(t *testing.T) {
	contractGRIsolate(t)
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	if got := contractGRKgObjects(t, s.DB()); len(got) != 0 {
		t.Fatalf("fresh database carries kg objects %v, want none", got)
	}
}

// "Opening a database at v31 migrates to 32" / "A v31 database carrying rows in
// every kg table ... migrates without error" / "no table whose name starts with
// `kg_` exists, including `kg_edges_fts` and its three triggers `kg_edges_ai/ad/au`".
func TestGraphRemovalV31MigratesAndDropsEveryKgObject(t *testing.T) {
	path, _ := contractGRSeedV31(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open at v31: %v", err)
	}
	defer s.Close()
	db := s.DB()
	if v := contractGRVersion(t, db); v != schemaVersion {
		t.Fatalf("user_version after migrating v31 = %d, want %d", v, schemaVersion)
	}
	if got := contractGRKgObjects(t, db); len(got) != 0 {
		t.Fatalf("kg objects survived v32: %v", got)
	}
	for _, trg := range []string{"kg_edges_ai", "kg_edges_ad", "kg_edges_au"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='trigger' AND name=?`, trg).Scan(&n); err != nil {
			t.Fatalf("trigger probe %s: %v", trg, err)
		}
		if n != 0 {
			t.Errorf("trigger %s survived v32", trg)
		}
	}
}

// "kv rows whose key starts with `kg.jobvec` or equals `graph_ingest_last` are
// deleted" — and nothing else in kv is.
func TestGraphRemovalV32DeletesGraphKVOnly(t *testing.T) {
	path, _ := contractGRSeedV31(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open at v31: %v", err)
	}
	defer s.Close()
	for _, k := range contractGRDeletedKV {
		if v, err := s.GetKV(k); err == nil {
			t.Errorf("kv %q survived v32 with value %q", k, v)
		}
	}
	if v, err := s.GetKV("graphremoval_unrelated"); err != nil || v != "survive-me" {
		t.Errorf("unrelated kv row = %q, err %v; want survive-me", v, err)
	}
}

// "every `mm_items`, `mm_code_anchors`, `watch_items`, `threads`, `jobs` row
// survives byte-identical".
func TestGraphRemovalV32PreservesSurvivorRowsByteIdentical(t *testing.T) {
	path, before := contractGRSeedV31(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open at v31: %v", err)
	}
	defer s.Close()
	for _, tbl := range contractGRSurvivorTables {
		if after := contractGRDump(t, s.DB(), tbl); after != before[tbl] {
			t.Errorf("%s changed across v32:\nbefore:\n%s\nafter:\n%s", tbl, before[tbl], after)
		}
	}
}

// "running `Open` twice on the same file is a no-op the second time (version
// stays 32, no error)".
func TestGraphRemovalSecondOpenIsNoOp(t *testing.T) {
	path, _ := contractGRSeedV31(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	snap := map[string]string{}
	for _, tbl := range contractGRSurvivorTables {
		snap[tbl] = contractGRDump(t, s.DB(), tbl)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	if v := contractGRVersion(t, s2.DB()); v != schemaVersion {
		t.Fatalf("user_version after second Open = %d, want %d", v, schemaVersion)
	}
	if got := contractGRKgObjects(t, s2.DB()); len(got) != 0 {
		t.Fatalf("kg objects after second Open: %v", got)
	}
	for _, tbl := range contractGRSurvivorTables {
		if after := contractGRDump(t, s2.DB(), tbl); after != snap[tbl] {
			t.Errorf("%s changed on the no-op second Open", tbl)
		}
	}
}

// "`store.MMItem.EpisodeID` field kept ... `ListMMItems`/`GetMMItem` return
// existing rows with their stored value, new rows with 0."
func TestGraphRemovalMMItemEpisodeIDStoredValueAndZero(t *testing.T) {
	path, _ := contractGRSeedV31(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open at v31: %v", err)
	}
	defer s.Close()
	var oldID int64
	if err := s.DB().QueryRow(`SELECT id FROM mm_items WHERE episode_id = 4242`).Scan(&oldID); err != nil {
		t.Fatalf("locate the pre-v32 item: %v", err)
	}
	old, err := s.GetMMItem(oldID)
	if err != nil {
		t.Fatalf("GetMMItem(old): %v", err)
	}
	if old.EpisodeID != 4242 {
		t.Errorf("pre-v32 item EpisodeID = %d, want stored 4242", old.EpisodeID)
	}
	newID, err := s.AddMMItem(MMItem{Kind: "note", Source: "manual", Content: "after removal", CreatedAt: 20})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	fresh, err := s.GetMMItem(newID)
	if err != nil {
		t.Fatalf("GetMMItem(new): %v", err)
	}
	if fresh.EpisodeID != 0 {
		t.Errorf("post-v32 item EpisodeID = %d, want 0 (nothing writes it)", fresh.EpisodeID)
	}
}
