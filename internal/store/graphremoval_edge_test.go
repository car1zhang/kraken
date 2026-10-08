package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// graphremoval_edge_test.go — the EDGE dimension of 50-contract.md slice A
// (migration v32, the knowledge-graph removal). Written blind from the contract
// text: schema.go, mm.go and watch.go were not read. The happy path (a full
// v31 fixture migrating clean) is athena-logic's; what lives here is the
// boundaries — a fixture missing some kg objects, a fixture with none, kv keys
// that merely LOOK like the deleted ones, a third Open, and a legacy mm row
// whose stored episode_id is non-zero.
//
// Every package-level identifier is prefixed `edgeGR` so it collides neither
// with athena-logic's `contract…` helpers nor with the existing `edge…`/`edgeMM…`
// families in this package.

// edgeGROpen opens (or re-opens) the store at path under the contract's test
// environment: HOME isolated, KRAKEN_DB_PATH and OPENAI_API_KEY unset.
func edgeGROpen(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	return s
}

func edgeGREnv(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv("OPENAI_API_KEY", "")
	return filepath.Join(t.TempDir(), "kraken.db")
}

func edgeGRExec(t *testing.T, s *Store, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.DB().Exec(q); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
}

func edgeGRVersion(t *testing.T, s *Store) int {
	t.Helper()
	var v int
	if err := s.DB().QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return v
}

// edgeGRKGObjects lists every sqlite_master object (table, index, trigger,
// view) whose name starts with `kg_`. FTS5 shadow tables (kg_edges_fts_data,
// …) are tables too, so they are covered by the same scan.
func edgeGRKGObjects(t *testing.T, s *Store) []string {
	t.Helper()
	rows, err := s.DB().Query(`SELECT type || ':' || name FROM sqlite_master WHERE substr(name, 1, 3) = 'kg_'`)
	if err != nil {
		t.Fatalf("scan sqlite_master: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatalf("scan row: %v", err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	sort.Strings(out)
	return out
}

func edgeGRKVPresent(t *testing.T, s *Store, key string) bool {
	t.Helper()
	_, err := s.GetKV(key)
	if err == nil {
		return true
	}
	if errors.Is(err, ErrNotFound) {
		return false
	}
	t.Fatalf("GetKV(%q): %v", key, err)
	return false
}

// edgeGRDowngrade stamps user_version = 31 and closes, so the next Open runs
// migration 32 alone (the contract's fixture recipe).
func edgeGRDowngrade(t *testing.T, s *Store) {
	t.Helper()
	edgeGRExec(t, s, `PRAGMA user_version = 31`)
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// "schemaV32 uses DROP TRIGGER IF EXISTS and DROP TABLE IF EXISTS throughout
// (an obligation of slice A)" — a v31 database carrying only SOME of the kg
// objects (a partially-created or hand-pruned database) must still migrate,
// and every kg object it DID carry must be gone.
func TestGraphRemovalEdgeMigrationToleratesAPartialKGFixture(t *testing.T) {
	path := edgeGREnv(t)
	s := edgeGROpen(t, path)
	edgeGRExec(t, s,
		`CREATE TABLE kg_edges (id INTEGER PRIMARY KEY, fact TEXT)`,
		`CREATE TABLE kg_recall (session_id TEXT, edge_id INTEGER)`,
		// one of the three triggers only — the other two are absent
		`CREATE TRIGGER kg_edges_ai AFTER INSERT ON kg_edges BEGIN SELECT 1; END`,
		`INSERT INTO kg_edges (fact) VALUES ('a fact that must not survive')`,
		`INSERT INTO kg_recall VALUES ('sess', 1)`,
	)
	edgeGRDowngrade(t, s)

	s2 := edgeGROpen(t, path)
	defer s2.Close()
	if v := edgeGRVersion(t, s2); v != 32 {
		t.Fatalf("user_version after migrating a partial fixture = %d, want 32", v)
	}
	if left := edgeGRKGObjects(t, s2); len(left) != 0 {
		t.Fatalf("kg objects survived migration 32: %v", left)
	}
}

// The other partial shape: the FTS stand-in and a trigger-free kg_nodes with NO
// kg_edges at all, so every `DROP TRIGGER kg_edges_*` targets a table that
// does not exist either.
func TestGraphRemovalEdgeMigrationToleratesFTSWithoutEdges(t *testing.T) {
	path := edgeGREnv(t)
	s := edgeGROpen(t, path)
	edgeGRExec(t, s,
		`CREATE TABLE kg_edges_fts (fact TEXT)`,
		`CREATE TABLE kg_nodes (id INTEGER PRIMARY KEY, name TEXT)`,
		`CREATE TABLE kg_layout (node_id INTEGER, x REAL, y REAL)`,
	)
	edgeGRDowngrade(t, s)

	s2 := edgeGROpen(t, path)
	defer s2.Close()
	if v := edgeGRVersion(t, s2); v != 32 {
		t.Fatalf("user_version = %d, want 32", v)
	}
	if left := edgeGRKGObjects(t, s2); len(left) != 0 {
		t.Fatalf("kg objects survived migration 32: %v", left)
	}
}

// IF EXISTS taken to its limit: a v31 database with no kg object at all.
func TestGraphRemovalEdgeMigrationToleratesNoKGObjects(t *testing.T) {
	path := edgeGREnv(t)
	s := edgeGROpen(t, path)
	if left := edgeGRKGObjects(t, s); len(left) != 0 {
		// "opening a fresh database lands at 32 in one pass" + "After migration
		// no table whose name starts with kg_ exists" — a fresh store is itself
		// post-migration, FTS5 shadow tables included.
		t.Fatalf("a fresh store carries kg objects: %v", left)
	}
	edgeGRDowngrade(t, s)

	s2 := edgeGROpen(t, path)
	defer s2.Close()
	if v := edgeGRVersion(t, s2); v != 32 {
		t.Fatalf("user_version = %d, want 32", v)
	}
	if left := edgeGRKGObjects(t, s2); len(left) != 0 {
		t.Fatalf("kg objects present after migrating an empty fixture: %v", left)
	}
}

// "kv rows whose key starts with `kg.jobvec` or equals `graph_ingest_last` are
// deleted" — and NOTHING else. The near-misses below pin both halves of that
// predicate: a prefix match must be a real prefix (not a substring, not a
// different `kg.` key), and the equality must be an equality (not a prefix, and
// not a LIKE whose `_` wildcard would also match `graphXingestXlast`).
func TestGraphRemovalEdgeMigrationKVDeletionIsExact(t *testing.T) {
	path := edgeGREnv(t)
	s := edgeGROpen(t, path)

	gone := []string{"kg.jobvec", "kg.jobvec.1", "kg.jobvechash.1", "kg.jobvec.999.extra", "graph_ingest_last"}
	kept := []string{
		"kg.other",             // a kg. key that is not a jobvec key
		"kg.jobve",             // one rune short of the prefix
		"x.kg.jobvec.1",        // contains, but does not START with, the prefix
		"graph_ingest_last.1",  // starts with, but does not EQUAL, graph_ingest_last
		"graph_ingest_last_at", // same
		"graphXingestXlast",    // what LIKE 'graph_ingest_last' would also match
		"Xgraph_ingest_last",   // a suffix match is not an equality either
		"dream_last_run",       // an unrelated key
	}
	for _, k := range append(append([]string{}, gone...), kept...) {
		if err := s.SetKV(k, `{"v":"`+k+`"}`); err != nil {
			t.Fatalf("SetKV(%q): %v", k, err)
		}
	}
	edgeGRDowngrade(t, s)

	s2 := edgeGROpen(t, path)
	defer s2.Close()
	for _, k := range gone {
		if edgeGRKVPresent(t, s2, k) {
			t.Errorf("kv %q survived migration 32, want deleted", k)
		}
	}
	for _, k := range kept {
		v, err := s2.GetKV(k)
		if err != nil {
			t.Errorf("kv %q was deleted by migration 32 (err %v), want it untouched", k, err)
			continue
		}
		if want := `{"v":"` + k + `"}`; v != want {
			t.Errorf("kv %q = %q after migration, want %q", k, v, want)
		}
	}
}

// "running `Open` twice on the same file is a no-op the second time (version
// stays 32, no error)" — taken one step further: after the migrating Open, a
// second AND a third Open each leave the version, the kept kv row and the mm
// row exactly as they were, and recreate no kg object.
func TestGraphRemovalEdgeThirdOpenIsANoOp(t *testing.T) {
	path := edgeGREnv(t)
	s := edgeGROpen(t, path)
	edgeGRExec(t, s,
		`CREATE TABLE kg_episodes (id INTEGER PRIMARY KEY, content TEXT)`,
		`INSERT INTO kg_episodes (content) VALUES ('ep')`,
	)
	if err := s.SetKV("kg.other", "keep"); err != nil {
		t.Fatalf("SetKV: %v", err)
	}
	mmID, err := s.AddMMItem(MMItem{Kind: "note", Source: "manual", Content: "edge third open", CreatedAt: 1_770_000_000_000})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	before, err := s.GetMMItem(mmID)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	edgeGRDowngrade(t, s)

	for i := 2; i <= 4; i++ {
		si := edgeGROpen(t, path)
		if v := edgeGRVersion(t, si); v != 32 {
			si.Close()
			t.Fatalf("Open #%d: user_version = %d, want 32", i, v)
		}
		if left := edgeGRKGObjects(t, si); len(left) != 0 {
			si.Close()
			t.Fatalf("Open #%d: kg objects present: %v", i, left)
		}
		if v, err := si.GetKV("kg.other"); err != nil || v != "keep" {
			si.Close()
			t.Fatalf("Open #%d: kv kg.other = %q, %v; want \"keep\"", i, v, err)
		}
		after, err := si.GetMMItem(mmID)
		if err != nil {
			si.Close()
			t.Fatalf("Open #%d: GetMMItem: %v", i, err)
		}
		if after.Content != before.Content || after.Kind != before.Kind || after.CreatedAt != before.CreatedAt || after.EpisodeID != before.EpisodeID {
			si.Close()
			t.Fatalf("Open #%d: mm row changed: before %+v after %+v", i, before, after)
		}
		if err := si.Close(); err != nil {
			t.Fatalf("Open #%d: close: %v", i, err)
		}
	}
}

// "`store.MMItem.EpisodeID` field kept (`int64`) — Still scanned from
// `mm_items.episode_id`; nothing writes it any more. `ListMMItems`/`GetMMItem`
// return existing rows with their stored value, new rows with 0." The legacy
// row is one minted under v29/v30, whose episode_id was stamped by the
// now-removed SetMMItemEpisode — so the fixture stamps it by raw SQL, and the
// migration must not zero it.
func TestGraphRemovalEdgeLegacyEpisodeIDSurvivesMigrationAndScan(t *testing.T) {
	path := edgeGREnv(t)
	s := edgeGROpen(t, path)
	legacy, err := s.AddMMItem(MMItem{Kind: "note", Source: "manual", Content: "legacy row", CreatedAt: 1_770_000_000_000})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	edgeGRExec(t, s, `CREATE TABLE kg_episodes (id INTEGER PRIMARY KEY, content TEXT)`)
	if _, err := s.DB().Exec(`UPDATE mm_items SET episode_id = ? WHERE id = ?`, int64(4242), legacy); err != nil {
		t.Fatalf("stamp legacy episode_id: %v", err)
	}
	edgeGRDowngrade(t, s)

	s2 := edgeGROpen(t, path)
	defer s2.Close()
	got, err := s2.GetMMItem(legacy)
	if err != nil {
		t.Fatalf("GetMMItem(legacy): %v", err)
	}
	if got.EpisodeID != 4242 {
		t.Errorf("GetMMItem(legacy).EpisodeID = %d, want the stored 4242", got.EpisodeID)
	}

	fresh, err := s2.AddMMItem(MMItem{Kind: "note", Source: "manual", Content: "post-removal row", CreatedAt: 1_770_000_000_001})
	if err != nil {
		t.Fatalf("AddMMItem(fresh): %v", err)
	}
	gotFresh, err := s2.GetMMItem(fresh)
	if err != nil {
		t.Fatalf("GetMMItem(fresh): %v", err)
	}
	if gotFresh.EpisodeID != 0 {
		t.Errorf("a row created after the removal has EpisodeID %d, want 0 (nothing writes it any more)", gotFresh.EpisodeID)
	}

	all, err := s2.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems(true): %v", err)
	}
	seen := map[int64]int64{}
	for _, it := range all {
		seen[it.ID] = it.EpisodeID
	}
	if ep, ok := seen[legacy]; !ok || ep != 4242 {
		t.Errorf("ListMMItems legacy row episode_id = %d (present %v), want 4242", ep, ok)
	}
	if ep, ok := seen[fresh]; !ok || ep != 0 {
		t.Errorf("ListMMItems fresh row episode_id = %d (present %v), want 0", ep, ok)
	}
	var raw sql.NullInt64
	if err := s2.DB().QueryRow(`SELECT episode_id FROM mm_items WHERE id = ?`, legacy).Scan(&raw); err != nil {
		t.Fatalf("raw episode_id read: %v", err)
	}
	if !raw.Valid || raw.Int64 != 4242 {
		t.Errorf("stored mm_items.episode_id = %+v after migration, want 4242", raw)
	}
}

// A kg-named object the migration's list could plausibly miss: an INDEX on a
// kg table. "After migration no table whose name starts with `kg_` exists" is
// stated for tables; an index dies with its table, so this pins only that the
// table drop is real (not a rename to a kg_ name kept around).
func TestGraphRemovalEdgeMigrationDropsIndexedKGTables(t *testing.T) {
	path := edgeGREnv(t)
	s := edgeGROpen(t, path)
	edgeGRExec(t, s,
		`CREATE TABLE kg_node_aliases (node_id INTEGER, alias TEXT)`,
		`CREATE INDEX kg_node_aliases_alias ON kg_node_aliases(alias)`,
		`CREATE TABLE kg_ingest_cursor (path TEXT PRIMARY KEY, off INTEGER)`,
	)
	edgeGRDowngrade(t, s)

	s2 := edgeGROpen(t, path)
	defer s2.Close()
	for _, obj := range edgeGRKGObjects(t, s2) {
		if strings.HasPrefix(obj, "table:") {
			t.Errorf("kg table survived migration 32: %s", obj)
		}
	}
}
