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

// mindlayer_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (PR 2, the mental-model rework), slice A (store):
// schema v33, the new MMItem columns, ListMMItemsFiltered, ReviseMMItem, the
// mm_flags verbs and Thread.Role / GetThreadBySession.
//
// Written from the frozen contract alone; internal/store/*.go (non-test) was
// never read by any route. Dimension: LOGIC — stated invariants and happy
// paths. Refusals and boundaries are athena-edge's (`edgeML…`).
//
// Every package-level identifier here is prefixed `contractML`.

// contractMLIsolate is the Test environment obligation: isolate HOME and unset
// KRAKEN_DB_PATH, KRAKEN_THREAD_SESSION, KRAKEN_THREAD_ROLE before opening a
// store.
func contractMLIsolate(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"KRAKEN_DB_PATH", "KRAKEN_THREAD_SESSION", "KRAKEN_THREAD_ROLE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func contractMLStore(t *testing.T) *Store {
	t.Helper()
	contractMLIsolate(t)
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func contractMLVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		t.Fatalf("user_version: %v", err)
	}
	return v
}

func contractMLSHA(x string) string {
	sum := sha256.Sum256([]byte(x))
	return hex.EncodeToString(sum[:])
}

// contractMLColumns returns table's column name → default-value text.
func contractMLColumns(t *testing.T, db *sql.DB, table string) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT name, COALESCE(dflt_value, '') FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatalf("table_info %s: %v", table, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var n, d string
		if err := rows.Scan(&n, &d); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		out[n] = d
	}
	return out
}

// contractMLDumpCols renders the given columns of every row of table, one
// line per row, lines sorted — a byte-for-byte comparison over exactly those
// columns that does not depend on physical row order.
func contractMLDumpCols(t *testing.T, db *sql.DB, table string, cols []string) string {
	t.Helper()
	q := `SELECT ` + strings.Join(cols, ", ") + ` FROM ` + table
	rows, err := db.Query(q)
	if err != nil {
		t.Fatalf("dump %s: %v", table, err)
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatalf("scan %s: %v", table, err)
		}
		var b strings.Builder
		for i, v := range vals {
			if bs, ok := v.([]byte); ok {
				v = string(bs)
			}
			fmt.Fprintf(&b, "%s=%#v;", cols[i], v)
		}
		lines = append(lines, b.String())
	}
	sort.Strings(lines)
	return fmt.Sprintf("%d rows\n%s", len(lines), strings.Join(lines, "\n"))
}

func contractMLColNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The eight columns v33 adds to mm_items, with their stated defaults.
var contractMLNewItemCols = map[string]string{
	"title": "''", "scope": "'repo'", "repo": "''", "dir": "''", "quote": "''",
	"source_json": "''", "thread_session": "''", "revised_at": "0",
}

// contractMLRollBackToV32 turns a fresh (v33) database back into the v32
// shape the contract names as the migration's input: mm_flags gone,
// threads.role gone, the eight new mm_items columns gone, user_version 32. It
// is fixture-side surgery on a database this test owns. It returns false when
// sqlite refuses a DROP COLUMN (e.g. a table-level CHECK naming the column),
// in which case the caller falls back to the plain "user_version = 32 set
// back" fixture the lead's brief names.
func contractMLRollBackToV32(t *testing.T, db *sql.DB) bool {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE IF EXISTS mm_flags`); err != nil {
		t.Fatalf("drop mm_flags: %v", err)
	}
	// Indexes on the two reshaped tables that mention a v33 column would
	// block DROP COLUMN; drop them (v33 recreates whatever it needs).
	newCols := append(contractMLColNames(contractMLNewItemCols), "role")
	rows, err := db.Query(`SELECT name, COALESCE(sql,'') FROM sqlite_master WHERE type='index' AND tbl_name IN ('mm_items','threads')`)
	if err != nil {
		t.Fatalf("index probe: %v", err)
	}
	var doomed []string
	for rows.Next() {
		var n, q string
		if err := rows.Scan(&n, &q); err != nil {
			t.Fatalf("scan index: %v", err)
		}
		for _, c := range newCols {
			if q != "" && strings.Contains(q, c) {
				doomed = append(doomed, n)
				break
			}
		}
	}
	rows.Close()
	for _, n := range doomed {
		if _, err := db.Exec(`DROP INDEX IF EXISTS "` + n + `"`); err != nil {
			t.Fatalf("drop index %s: %v", n, err)
		}
	}
	for _, c := range contractMLColNames(contractMLNewItemCols) {
		if _, err := db.Exec(`ALTER TABLE mm_items DROP COLUMN ` + c); err != nil {
			t.Logf("fixture: cannot DROP mm_items.%s (%v) — falling back to user_version-only rollback", c, err)
			return false
		}
	}
	// amendment 13 added threads.turn_uuid beside role; the lead extended this
	// rollback to drop it too (sighted edit, fixture only).
	for _, c := range []string{"role", "turn_uuid"} {
		if _, err := db.Exec(`ALTER TABLE threads DROP COLUMN ` + c); err != nil {
			t.Logf("fixture: cannot DROP threads.%s (%v) — falling back to user_version-only rollback", c, err)
			return false
		}
	}
	return true
}

// contractMLSeedV32 builds a v32 database carrying a legacy mm_items row (with
// its code anchor and an episode id), a manual note, a job and a thread, then
// closes it. It returns the path and the pre-migration dumps of the old
// columns of mm_items/threads and all of mm_code_anchors.
func contractMLSeedV32(t *testing.T) (string, map[string]string, map[string][]string) {
	t.Helper()
	contractMLIsolate(t)
	path := filepath.Join(t.TempDir(), "kraken.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("fresh Open: %v", err)
	}
	db := s.DB()
	jobID, err := s.CreateJob(&Job{Slug: "mindlayer-survivor", Repo: "kraken", Body: "keep me"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", SessionID: "sess-legacy", State: "live", CreatedAt: 1000}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	codeID, err := s.AddMMItem(MMItem{
		Kind: "reading", Source: "code", JobID: jobID, Slug: "mindlayer-survivor",
		SourceRef: "kraken/internal/jobs/mm.go:10-12", Content: "func f() {\n\treturn\n}", CreatedAt: 10,
	})
	if err != nil {
		t.Fatalf("AddMMItem code: %v", err)
	}
	if err := s.AddMMCodeAnchor(MMCodeAnchor{
		ItemID: codeID, Repo: "kraken", Path: "internal/jobs/mm.go", StartLine: 10, EndLine: 12,
		AnchorSHA: "1111111111111111111111111111111111111111", AnchorKind: "origin-main",
		Text: "func f() {\n\treturn\n}", TextSHA: contractMLSHA("func f() {\n\treturn\n}"),
		Lang: "go", CheckedAt: 1_700_000_000_000, CheckedSHA: "1111111111111111111111111111111111111111",
	}); err != nil {
		t.Fatalf("AddMMCodeAnchor: %v", err)
	}
	if _, err := s.AddMMItem(MMItem{Kind: "note", Source: "manual", Content: "legacy note\nsecond line", CreatedAt: 20}); err != nil {
		t.Fatalf("AddMMItem note: %v", err)
	}
	if _, err := db.Exec(`UPDATE mm_items SET episode_id = 4242 WHERE id = ?`, codeID); err != nil {
		t.Fatalf("stamp episode_id: %v", err)
	}

	contractMLRollBackToV32(t, db)

	itemCols := contractMLColNames(contractMLColumns(t, db, "mm_items"))
	threadCols := contractMLColNames(contractMLColumns(t, db, "threads"))
	anchorCols := contractMLColNames(contractMLColumns(t, db, "mm_code_anchors"))
	cols := map[string][]string{"mm_items": itemCols, "threads": threadCols, "mm_code_anchors": anchorCols}
	before := map[string]string{}
	for tbl, c := range cols {
		before[tbl] = contractMLDumpCols(t, db, tbl, c)
	}
	if _, err := db.Exec(`PRAGMA user_version = 32`); err != nil {
		t.Fatalf("set user_version 32: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return path, before, cols
}

// "`const schemaVersion = 33`" — "a fresh database lands at 33". 34 since the
// fleet's migration was appended after it, 35 since job_stacks (schemaV35),
// 36 since the ask-thread columns (schemaV36), 37 since the nightly mind pass
// (schemaV37), 38 since the spawn links (jobSpawnsTable).
func TestMindLayerSchemaVersionIs33AndFreshLandsThere(t *testing.T) {
	if schemaVersion != 39 {
		t.Fatalf("schemaVersion = %d, want 39", schemaVersion)
	}
	s := contractMLStore(t)
	if v := contractMLVersion(t, s.DB()); v != 39 {
		t.Fatalf("fresh user_version = %d, want 39", v)
	}
}

// "`mm_items` gains `title TEXT NOT NULL DEFAULT ”`, `scope ... DEFAULT 'repo'`,
// `repo`, `dir`, `quote`, `source_json`, `thread_session` (DEFAULT ”),
// `revised_at INTEGER NOT NULL DEFAULT 0`" / "`threads` gains `role ... DEFAULT
// 'ask'`" / "New table `mm_flags(...)`, index `mm_flags_state(state)`."
func TestMindLayerFreshSchemaShape(t *testing.T) {
	s := contractMLStore(t)
	db := s.DB()
	items := contractMLColumns(t, db, "mm_items")
	for col, def := range contractMLNewItemCols {
		got, ok := items[col]
		if !ok {
			t.Errorf("mm_items lacks column %s", col)
			continue
		}
		if got != def {
			t.Errorf("mm_items.%s default = %s, want %s", col, got, def)
		}
	}
	threads := contractMLColumns(t, db, "threads")
	if got, ok := threads["role"]; !ok || got != "'ask'" {
		t.Errorf("threads.role default = %q (present %v), want 'ask'", got, ok)
	}
	flags := contractMLColumns(t, db, "mm_flags")
	for _, col := range []string{"id", "item_id", "against_id", "job_id", "slug", "session", "note", "state",
		"raised_by", "raise_note", "raised_at", "resolved_at", "resolution", "created_at"} {
		if _, ok := flags[col]; !ok {
			t.Errorf("mm_flags lacks column %s", col)
		}
	}
	if len(flags) != 14 {
		t.Errorf("mm_flags has %d columns %v, want the 14 the contract names", len(flags), contractMLColNames(flags))
	}
	if flags["state"] != "'pending'" {
		t.Errorf("mm_flags.state default = %s, want 'pending'", flags["state"])
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='mm_flags_state' AND tbl_name='mm_flags'`).Scan(&n); err != nil {
		t.Fatalf("index probe: %v", err)
	}
	if n != 1 {
		t.Errorf("index mm_flags_state missing")
	}
}

// "A v32 database migrates to 33 in one `Open`" / "every existing row survives
// byte-identical in its old columns, `mm_code_anchors` untouched" — plus the new
// columns read as their defaults on the legacy rows.
func TestMindLayerV32MigratesPreservingRows(t *testing.T) {
	path, before, cols := contractMLSeedV32(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open at v32: %v", err)
	}
	defer s.Close()
	db := s.DB()
	if v := contractMLVersion(t, db); v != 39 {
		t.Fatalf("user_version after migrating v32 = %d, want 39", v)
	}
	for tbl, c := range cols {
		if after := contractMLDumpCols(t, db, tbl, c); after != before[tbl] {
			t.Errorf("%s old columns changed across v33:\nbefore:\n%s\nafter:\n%s", tbl, before[tbl], after)
		}
	}
	items, err := s.ListMMItems(true)
	if err != nil {
		t.Fatalf("ListMMItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("legacy items after migration = %d, want 2", len(items))
	}
	for _, it := range items {
		if it.Title != "" || it.Scope != "repo" || it.Repo != "" || it.Dir != "" || it.Quote != "" ||
			it.SourceJSON != "" || it.ThreadSession != "" || it.RevisedAt != 0 {
			t.Errorf("legacy item %d new fields = %+v, want the v33 defaults (scope repo, rest empty/0)", it.ID, it)
		}
	}
	var ep int64
	if err := db.QueryRow(`SELECT COUNT(*) FROM mm_items WHERE episode_id = 4242`).Scan(&ep); err != nil || ep != 1 {
		t.Errorf("episode_id 4242 row count = %d (%v), want 1", ep, err)
	}
	var role string
	if err := db.QueryRow(`SELECT role FROM threads WHERE tid = 't1'`).Scan(&role); err != nil {
		t.Fatalf("threads.role: %v", err)
	}
	if role != "ask" {
		t.Errorf("legacy thread role = %q, want ask", role)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM mm_flags`).Scan(&n); err != nil {
		t.Errorf("mm_flags missing after migration: %v", err)
	}
}

// "a second `Open` is a no-op."
func TestMindLayerSecondOpenIsNoOp(t *testing.T) {
	path, _, cols := contractMLSeedV32(t)
	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	snap := map[string]string{}
	all := map[string][]string{}
	for tbl := range cols {
		all[tbl] = contractMLColNames(contractMLColumns(t, s.DB(), tbl))
		snap[tbl] = contractMLDumpCols(t, s.DB(), tbl, all[tbl])
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	if v := contractMLVersion(t, s2.DB()); v != 39 {
		t.Fatalf("user_version after second Open = %d, want 39", v)
	}
	for tbl, c := range all {
		if after := contractMLDumpCols(t, s2.DB(), tbl, c); after != snap[tbl] {
			t.Errorf("%s changed on the no-op second Open", tbl)
		}
	}
}

// "`AddMMItem` persists all of them; `GetMMItem`/`ListMMItems` return them;
// ... `Source == "capture"` is valid".
func TestMindLayerAddMMItemPersistsNewFields(t *testing.T) {
	s := contractMLStore(t)
	want := MMItem{
		Kind: "decision", Source: "capture", Slug: "j", Content: "the body\nline two", CreatedAt: 100,
		Title: "the daemon owns the render", Scope: "repo", Repo: "kraken", Dir: "internal/jobs",
		Quote: "highlighted words", SourceJSON: `{"kind":"artifact","name":"00-overview.md"}`,
		ThreadSession: "mind-sess", RevisedAt: 0,
	}
	id, err := s.AddMMItem(want)
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	got, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	check := func(label string, g MMItem) {
		if g.Title != want.Title || g.Scope != want.Scope || g.Repo != want.Repo || g.Dir != want.Dir ||
			g.Quote != want.Quote || g.SourceJSON != want.SourceJSON || g.ThreadSession != want.ThreadSession ||
			g.Source != "capture" || g.Content != want.Content {
			t.Errorf("%s = %+v, want the new fields of %+v", label, g, want)
		}
	}
	check("GetMMItem", got)
	list, err := s.ListMMItems(false)
	if err != nil {
		t.Fatalf("ListMMItems: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListMMItems = %d rows, want 1", len(list))
	}
	check("ListMMItems[0]", list[0])

	// universal scope round-trips too
	uid, err := s.AddMMItem(MMItem{Kind: "note", Source: "capture", Content: "u", CreatedAt: 101, Title: "u", Scope: "universal"})
	if err != nil {
		t.Fatalf("AddMMItem universal: %v", err)
	}
	if u, _ := s.GetMMItem(uid); u.Scope != "universal" {
		t.Errorf("universal item scope = %q", u.Scope)
	}
}

// "`Scope` defaults to `repo` when empty on insert".
func TestMindLayerAddMMItemDefaultsScopeToRepo(t *testing.T) {
	s := contractMLStore(t)
	id, err := s.AddMMItem(MMItem{Kind: "note", Source: "manual", Content: "x", CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	got, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if got.Scope != "repo" {
		t.Errorf("Scope = %q, want repo", got.Scope)
	}
}

func contractMLAdd(t *testing.T, s *Store, it MMItem) int64 {
	t.Helper()
	if it.Kind == "" {
		it.Kind = "note"
	}
	if it.Source == "" {
		it.Source = "capture"
	}
	id, err := s.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem(%+v): %v", it, err)
	}
	return id
}

func contractMLFiltered(t *testing.T, s *Store, f MMFilter) []int64 {
	t.Helper()
	got, err := s.ListMMItemsFiltered(f)
	if err != nil {
		t.Fatalf("ListMMItemsFiltered(%+v): %v", f, err)
	}
	out := make([]int64, 0, len(got))
	for _, it := range got {
		out = append(out, it.ID)
	}
	return out
}

func contractMLSameIDs(t *testing.T, label string, got, want []int64) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("%s = %v, want %v", label, got, want)
	}
}

// "`ListMMItemsFiltered`: Newest first; `Live` excludes invalidated rows; `Repo`
// is an exact match on `repo`; `Dir` matches `dir` exactly OR any ancestor
// directory of it (`Dir:"a/b"` matches items with dir `a/b`, `a`, and `”`);
// `Scope` exact; `Q` is a case-insensitive substring over `title`, `content` and
// `quote` (any one matching qualifies); empty fields are no filter".
func TestMindLayerListMMItemsFiltered(t *testing.T) {
	s := contractMLStore(t)
	root := contractMLAdd(t, s, MMItem{Title: "root rule", Content: "c0", Scope: "repo", Repo: "kraken", Dir: "", CreatedAt: 10})
	a := contractMLAdd(t, s, MMItem{Title: "a rule", Content: "c1", Scope: "repo", Repo: "kraken", Dir: "a", CreatedAt: 20})
	ab := contractMLAdd(t, s, MMItem{Title: "ab rule", Content: "Capacity planning", Scope: "repo", Repo: "kraken", Dir: "a/b", CreatedAt: 30})
	abc := contractMLAdd(t, s, MMItem{Title: "abc rule", Content: "c3", Scope: "repo", Repo: "kraken", Dir: "a/b/c", CreatedAt: 40})
	sib := contractMLAdd(t, s, MMItem{Title: "sibling", Content: "c4", Quote: "the CAPACITY quote", Scope: "repo", Repo: "kraken", Dir: "a/bc", CreatedAt: 50})
	other := contractMLAdd(t, s, MMItem{Title: "other repo capacity", Content: "c5", Scope: "repo", Repo: "kraken2", Dir: "a/b", CreatedAt: 60})
	uni := contractMLAdd(t, s, MMItem{Title: "universal truth", Content: "c6", Scope: "universal", CreatedAt: 70})
	dead := contractMLAdd(t, s, MMItem{Title: "dead capacity", Content: "c7", Scope: "repo", Repo: "kraken", Dir: "a/b", CreatedAt: 80})
	if err := s.InvalidateMMItem(dead, 90, "removed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}

	contractMLSameIDs(t, "empty filter", contractMLFiltered(t, s, MMFilter{}),
		[]int64{dead, uni, other, sib, abc, ab, a, root})
	contractMLSameIDs(t, "Live", contractMLFiltered(t, s, MMFilter{Live: true}),
		[]int64{uni, other, sib, abc, ab, a, root})
	contractMLSameIDs(t, "Repo kraken (live)", contractMLFiltered(t, s, MMFilter{Live: true, Repo: "kraken"}),
		[]int64{sib, abc, ab, a, root})
	contractMLSameIDs(t, "Repo kraken Dir a/b", contractMLFiltered(t, s, MMFilter{Live: true, Repo: "kraken", Dir: "a/b"}),
		[]int64{ab, a, root})
	contractMLSameIDs(t, "Scope universal", contractMLFiltered(t, s, MMFilter{Scope: "universal"}),
		[]int64{uni})
	contractMLSameIDs(t, "Scope repo live", contractMLFiltered(t, s, MMFilter{Live: true, Scope: "repo"}),
		[]int64{other, sib, abc, ab, a, root})
	// Q over title (other, dead), content (ab), quote (sib), case-insensitive.
	contractMLSameIDs(t, "Q capacity", contractMLFiltered(t, s, MMFilter{Q: "capacity"}),
		[]int64{dead, other, sib, ab})
	contractMLSameIDs(t, "Q CAPACITY live", contractMLFiltered(t, s, MMFilter{Live: true, Q: "CaPaCiTy"}),
		[]int64{other, sib, ab})
	contractMLSameIDs(t, "Q + Repo", contractMLFiltered(t, s, MMFilter{Live: true, Q: "capacity", Repo: "kraken"}),
		[]int64{sib, ab})
}

// "`ListMMItems(includeInvalidated)` keeps its signature and behaviour."
func TestMindLayerListMMItemsUnchanged(t *testing.T) {
	s := contractMLStore(t)
	a := contractMLAdd(t, s, MMItem{Title: "a", Content: "a", CreatedAt: 1})
	b := contractMLAdd(t, s, MMItem{Title: "b", Content: "b", CreatedAt: 2})
	if err := s.InvalidateMMItem(a, 3, "removed", ""); err != nil {
		t.Fatalf("InvalidateMMItem: %v", err)
	}
	all, err := s.ListMMItems(true)
	if err != nil || len(all) != 2 || all[0].ID != b {
		t.Errorf("ListMMItems(true) = %+v, %v; want both, newest first", all, err)
	}
	live, err := s.ListMMItems(false)
	if err != nil || len(live) != 1 || live[0].ID != b {
		t.Errorf("ListMMItems(false) = %+v, %v; want only %d", live, err, b)
	}
}

// "`ReviseMMItem`: Rewrites exactly those six columns and stamps `revised_at =
// r.At`; ... `content_sha` is re-stamped from the new content; `created_at`,
// `source_json`, `thread_session` are untouched."
func TestMindLayerReviseMMItemRewritesExactlyItsColumns(t *testing.T) {
	s := contractMLStore(t)
	orig := MMItem{
		Kind: "reading", Source: "capture", Slug: "j", Content: "old body", CreatedAt: 500,
		Title: "old title", Scope: "repo", Repo: "kraken", Dir: "internal/jobs", Quote: "old quote",
		SourceJSON: `{"kind":"manual"}`, ThreadSession: "mind-1",
	}
	id := contractMLAdd(t, s, orig)
	rev := MMRevision{Title: "new title", Content: "new body", Quote: "new quote", Scope: "universal", Repo: "", Dir: "", At: 777}
	if err := s.ReviseMMItem(id, rev); err != nil {
		t.Fatalf("ReviseMMItem: %v", err)
	}
	got, err := s.GetMMItem(id)
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if got.Title != rev.Title || got.Content != rev.Content || got.Quote != rev.Quote ||
		got.Scope != rev.Scope || got.Repo != rev.Repo || got.Dir != rev.Dir {
		t.Errorf("revised item = %+v, want the six columns of %+v", got, rev)
	}
	if got.RevisedAt != 777 {
		t.Errorf("RevisedAt = %d, want 777", got.RevisedAt)
	}
	if got.CreatedAt != 500 || got.SourceJSON != orig.SourceJSON || got.ThreadSession != orig.ThreadSession {
		t.Errorf("untouched columns moved: created_at %d source_json %q thread_session %q",
			got.CreatedAt, got.SourceJSON, got.ThreadSession)
	}
	if got.Kind != orig.Kind || got.Source != orig.Source || got.Slug != orig.Slug {
		t.Errorf("kind/source/slug moved: %q/%q/%q", got.Kind, got.Source, got.Slug)
	}
	var sha string
	if err := s.DB().QueryRow(`SELECT content_sha FROM mm_items WHERE id = ?`, id).Scan(&sha); err != nil {
		t.Fatalf("content_sha: %v", err)
	}
	if sha != contractMLSHA("new body") {
		t.Errorf("content_sha = %q, want sha256(new body) %q", sha, contractMLSHA("new body"))
	}
}

func contractMLGetFlag(t *testing.T, s *Store, id int64) MMFlag {
	t.Helper()
	f, err := s.GetMMFlag(id)
	if err != nil {
		t.Fatalf("GetMMFlag(%d): %v", id, err)
	}
	return f
}

// "`AddMMFlag`: Inserts with `State` as given (`pending` when empty)" /
// "`GetMMFlag` ... returns".
func TestMindLayerAddMMFlagRoundTrip(t *testing.T) {
	s := contractMLStore(t)
	want := MMFlag{ItemID: 7, AgainstID: 9, JobID: 3, Slug: "j", Session: "s-team", Note: "contradicts #9", CreatedAt: 1234}
	id, err := s.AddMMFlag(want)
	if err != nil {
		t.Fatalf("AddMMFlag: %v", err)
	}
	got := contractMLGetFlag(t, s, id)
	if got.ID != id || got.ItemID != 7 || got.AgainstID != 9 || got.JobID != 3 || got.Slug != "j" ||
		got.Session != "s-team" || got.Note != "contradicts #9" || got.CreatedAt != 1234 {
		t.Errorf("GetMMFlag = %+v, want %+v with id %d", got, want, id)
	}
	if got.State != "pending" {
		t.Errorf("State = %q, want pending (empty on insert)", got.State)
	}
	if got.Resolution != "" || got.ResolvedAt != 0 || got.RaisedAt != 0 || got.RaisedBy != "" {
		t.Errorf("fresh flag carries raise/resolve stamps: %+v", got)
	}

	rid, err := s.AddMMFlag(MMFlag{Note: "born raised", State: "raised", RaisedBy: "zeus-sess", RaisedAt: 55, CreatedAt: 1300})
	if err != nil {
		t.Fatalf("AddMMFlag raised: %v", err)
	}
	r := contractMLGetFlag(t, s, rid)
	if r.State != "raised" || r.RaisedBy != "zeus-sess" || r.RaisedAt != 55 {
		t.Errorf("raised-on-insert flag = %+v, want state raised by zeus-sess at 55", r)
	}
}

// "`ListMMFlags("")` returns every flag newest first; a state filters exactly".
func TestMindLayerListMMFlags(t *testing.T) {
	s := contractMLStore(t)
	p1, _ := s.AddMMFlag(MMFlag{Note: "p1", CreatedAt: 10})
	r1, _ := s.AddMMFlag(MMFlag{Note: "r1", State: "raised", CreatedAt: 20})
	d1, _ := s.AddMMFlag(MMFlag{Note: "d1", State: "dismissed", CreatedAt: 30})
	p2, _ := s.AddMMFlag(MMFlag{Note: "p2", CreatedAt: 40})
	ids := func(fs []MMFlag) []int64 {
		out := []int64{}
		for _, f := range fs {
			out = append(out, f.ID)
		}
		return out
	}
	all, err := s.ListMMFlags("")
	if err != nil {
		t.Fatalf("ListMMFlags: %v", err)
	}
	contractMLSameIDs(t, `ListMMFlags("")`, ids(all), []int64{p2, d1, r1, p1})
	pend, err := s.ListMMFlags("pending")
	if err != nil {
		t.Fatalf("ListMMFlags pending: %v", err)
	}
	contractMLSameIDs(t, "ListMMFlags(pending)", ids(pend), []int64{p2, p1})
	raised, _ := s.ListMMFlags("raised")
	contractMLSameIDs(t, "ListMMFlags(raised)", ids(raised), []int64{r1})
	dis, _ := s.ListMMFlags("dismissed")
	contractMLSameIDs(t, "ListMMFlags(dismissed)", ids(dis), []int64{d1})
}

// "`RaiseMMFlag`/`DismissMMFlag`: Each moves a `pending` flag to
// `raised`/`dismissed`, stamping `raised_by`, `raise_note`, `raised_at = at`" /
// "`ResolveMMFlag`: Only a `raised` flag with `resolution == ”` accepts it;
// `resolution` must be `drop` or `keep`; stamps `resolved_at`".
func TestMindLayerFlagStateTransitions(t *testing.T) {
	s := contractMLStore(t)
	a, _ := s.AddMMFlag(MMFlag{Note: "a", CreatedAt: 1})
	b, _ := s.AddMMFlag(MMFlag{Note: "b", CreatedAt: 2})
	c, _ := s.AddMMFlag(MMFlag{Note: "c", CreatedAt: 3})

	if err := s.RaiseMMFlag(a, 100, "zeus", "verified"); err != nil {
		t.Fatalf("RaiseMMFlag: %v", err)
	}
	fa := contractMLGetFlag(t, s, a)
	if fa.State != "raised" || fa.RaisedBy != "zeus" || fa.RaiseNote != "verified" || fa.RaisedAt != 100 {
		t.Errorf("raised flag = %+v", fa)
	}
	if fa.Resolution != "" || fa.ResolvedAt != 0 {
		t.Errorf("raise touched resolution: %+v", fa)
	}

	if err := s.DismissMMFlag(b, 200, "zeus", "not a contradiction"); err != nil {
		t.Fatalf("DismissMMFlag: %v", err)
	}
	fb := contractMLGetFlag(t, s, b)
	if fb.State != "dismissed" || fb.RaisedBy != "zeus" || fb.RaiseNote != "not a contradiction" || fb.RaisedAt != 200 {
		t.Errorf("dismissed flag = %+v", fb)
	}

	if err := s.ResolveMMFlag(a, 300, "drop"); err != nil {
		t.Fatalf("ResolveMMFlag drop: %v", err)
	}
	fa = contractMLGetFlag(t, s, a)
	if fa.Resolution != "drop" || fa.ResolvedAt != 300 || fa.State != "resolved" {
		t.Errorf("resolved flag = %+v, want state resolved, drop at 300 (Amendment 2)", fa)
	}

	if err := s.RaiseMMFlag(c, 400, "zeus", ""); err != nil {
		t.Fatalf("RaiseMMFlag c: %v", err)
	}
	if err := s.ResolveMMFlag(c, 500, "keep"); err != nil {
		t.Fatalf("ResolveMMFlag keep: %v", err)
	}
	fc := contractMLGetFlag(t, s, c)
	if fc.Resolution != "keep" || fc.ResolvedAt != 500 || fc.State != "resolved" {
		t.Errorf("kept flag = %+v, want state resolved, keep at 500 (Amendment 2)", fc)
	}
	// Amendment 2: "`ListMMFlags(\"raised\")` therefore returns only unresolved
	// raised flags".
	if raised, err := s.ListMMFlags("raised"); err != nil || len(raised) != 0 {
		t.Errorf("ListMMFlags(raised) after both resolves = %+v, %v; want none", raised, err)
	}

	// one-way: a resolved flag does not accept a second resolution, and
	// nothing changes.
	if err := s.ResolveMMFlag(a, 600, "keep"); err == nil {
		t.Errorf("a second ResolveMMFlag on a resolved flag succeeded")
	}
	if fa2 := contractMLGetFlag(t, s, a); fa2.Resolution != "drop" || fa2.ResolvedAt != 300 {
		t.Errorf("refused re-resolve changed the flag: %+v", fa2)
	}
}

// "`Thread.Role`: `AddThreadNext`/`AddThread` persist it (`ask` when empty);
// `GetThread`/`ListThreads` return it."
func TestMindLayerThreadRolePersists(t *testing.T) {
	s := contractMLStore(t)
	jobID, err := s.CreateJob(&Job{Slug: "roles"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", SessionID: "s1", State: "live", Role: "mind", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread mind: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t2", SessionID: "s2", State: "live", CreatedAt: 2}); err != nil {
		t.Fatalf("AddThread default: %v", err)
	}
	next, err := s.AddThreadNext(Thread{JobID: jobID, Title: "x", SessionID: "s3", State: "live", Role: "mind"})
	if err != nil {
		t.Fatalf("AddThreadNext: %v", err)
	}
	if next.Role != "mind" {
		t.Errorf("AddThreadNext returned Role %q, want mind", next.Role)
	}
	want := map[string]string{"t1": "mind", "t2": "ask", next.TID: "mind"}
	for tid, role := range want {
		g, err := s.GetThread(jobID, tid)
		if err != nil {
			t.Fatalf("GetThread %s: %v", tid, err)
		}
		if g.Role != role {
			t.Errorf("GetThread(%s).Role = %q, want %q", tid, g.Role, role)
		}
	}
	list, err := s.ListThreads(jobID)
	if err != nil {
		t.Fatalf("ListThreads: %v", err)
	}
	if len(list) != 3 {
		t.Fatalf("ListThreads = %d rows, want 3", len(list))
	}
	for _, th := range list {
		if th.Role != want[th.TID] {
			t.Errorf("ListThreads %s Role = %q, want %q", th.TID, th.Role, want[th.TID])
		}
	}
}

// "`GetThreadBySession`: Returns the thread row whose `session_id` matches, `nil,
// nil` when none (any state)."
func TestMindLayerGetThreadBySession(t *testing.T) {
	s := contractMLStore(t)
	jobID, err := s.CreateJob(&Job{Slug: "bysession"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t1", SessionID: "live-sess", State: "live", Role: "mind", CreatedAt: 1}); err != nil {
		t.Fatalf("AddThread: %v", err)
	}
	if err := s.AddThread(Thread{JobID: jobID, TID: "t2", SessionID: "closed-sess", State: "closed", Role: "ask", CreatedAt: 2}); err != nil {
		t.Fatalf("AddThread closed: %v", err)
	}
	got, err := s.GetThreadBySession("live-sess")
	if err != nil || got == nil {
		t.Fatalf("GetThreadBySession(live-sess) = %v, %v", got, err)
	}
	if got.JobID != jobID || got.TID != "t1" || got.Role != "mind" || got.State != "live" {
		t.Errorf("GetThreadBySession(live-sess) = %+v", *got)
	}
	closed, err := s.GetThreadBySession("closed-sess")
	if err != nil || closed == nil || closed.TID != "t2" {
		t.Errorf("GetThreadBySession(closed-sess) = %v, %v; want the closed row (any state)", closed, err)
	}
	none, err := s.GetThreadBySession("no-such-sess")
	if err != nil || none != nil {
		t.Errorf("GetThreadBySession(unknown) = %v, %v; want nil, nil", none, err)
	}
}
