package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// watch_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md (watchlist moves into the knowledge graph). Written from the
// contract alone, with no sight of internal/store/schema.go's v16 parts or
// internal/store/watch.go. Every assertion cites the contract section it
// traces to; nothing here asserts a requirement the contract does not state.
//
// Dimension: LOGIC — stated invariants, happy paths, core behavioural
// obligations. Boundaries, refusals and error paths belong to athena-edge's
// watch_edge_test.go and are deliberately NOT duplicated here.
//
// Every package-level identifier is prefixed `contract` (a sibling author
// shares this package namespace).

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractOneLine is the contract's normalization rule, spelled out verbatim
// in S2.2: `oneLine(s) = strings.Join(strings.Fields(s), " ")`. Reimplemented
// here rather than imported so these tests pin the RULE, not whatever helper
// the implementation happens to name.
func contractOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// contractEpisodeHash is S2.3's hash rule: hex(sha256(kind + "\x00" + ref +
// "\x00" + content)) — the addEpisode hash rule the migration reuses so a
// re-run finds rather than duplicates.
func contractEpisodeHash(kind, ref, content string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + ref + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

// contractFlatWatchRow is one row of the retiring flat `watchlist` table
// (schemaV9's shape), the migration's input.
type contractFlatWatchRow struct {
	Observation  string
	Class        string
	State        string
	Sightings    int64
	FirstSeen    int64
	LastSeen     int64
	Evidence     string
	SuggestedFix string
	PRURL        string
	UpdatedAt    int64
	Verdict      string
	Comment      string
	CommentedAt  string
}

// contractEpisodeContent renders S2.3's episode content format exactly:
//
//	watchlist observation (<class>, <state>, seen <sightings>x): <observation>
//	[\nsuggested fix: <suggested_fix>]
//	[\ncarl's verdict: <verdict>[ — <comment>]]
func contractEpisodeContent(r contractFlatWatchRow) string {
	s := fmt.Sprintf("watchlist observation (%s, %s, seen %dx): %s",
		r.Class, r.State, r.Sightings, r.Observation)
	if r.SuggestedFix != "" {
		s += "\nsuggested fix: " + r.SuggestedFix
	}
	if r.Verdict != "" {
		s += "\ncarl's verdict: " + r.Verdict
		if r.Comment != "" {
			s += " — " + r.Comment
		}
	}
	return s
}

// contractOverlay mirrors the twelve kg_watch columns S1 freezes, read back
// straight from SQL so the migration assertions do not lean on the S3 verbs.
type contractOverlay struct {
	EdgeID       int64
	Class        string
	State        string
	Sightings    int64
	FirstSeen    int64
	LastSeen     int64
	Evidence     string
	SuggestedFix string
	PRURL        string
	UpdatedAt    int64
	Verdict      string
	Comment      string
	CommentedAt  string
}

// contractOpenV15 builds a genuine v15-shaped database by applying
// migrations[1..15] exactly the way migrate() does (one transaction, SQL then
// hook, user_version stamped last) and hands back the still-open handle plus
// the path. Seed the flat watchlist / kg_* fixtures through the handle, then
// hand both to contractMigrate.
func contractOpenV15(t *testing.T) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "kraken.db")
	dsn := fmt.Sprintf(
		"file:%s?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate", path)
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open v15 db: %v", err)
	}
	db.SetMaxOpenConns(1)
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin v15 build: %v", err)
	}
	for v := 1; v <= 15; v++ {
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
	if _, err := tx.Exec(`PRAGMA user_version = 15`); err != nil {
		tx.Rollback()
		t.Fatalf("stamp user_version 15: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v15 build: %v", err)
	}
	return db, path
}

// contractMigrate closes the v15 handle and re-Opens the same file, which runs
// migrations[16] (S1) and its migrateV16Watchlist hook (S2) for real.
func contractMigrate(t *testing.T, db *sql.DB, path string) *Store {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("close v15 db: %v", err)
	}
	s, err := Open(path)
	if err != nil {
		t.Fatalf("migrate a v15 database forward: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func contractInsertFlatRow(t *testing.T, db *sql.DB, r contractFlatWatchRow) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO watchlist
		(observation, class, state, sightings, first_seen, last_seen, evidence,
		 suggested_fix, pr_url, updated_at, verdict, comment, commented_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Observation, r.Class, r.State, r.Sightings, r.FirstSeen, r.LastSeen, r.Evidence,
		r.SuggestedFix, r.PRURL, r.UpdatedAt, r.Verdict, r.Comment, r.CommentedAt)
	if err != nil {
		t.Fatalf("seed watchlist row: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func contractInsertRawNode(t *testing.T, db *sql.DB, name, typ, desc string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO kg_nodes (name, type, description, embedding, created_at, updated_at)
		VALUES (?, ?, ?, x'', 1, 1)`, name, typ, desc)
	if err != nil {
		t.Fatalf("seed node %q: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func contractInsertRawEdge(t *testing.T, db *sql.DB, srcID, dstID int64, predicate, fact string, episodeID, expiredAt int64) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO kg_edges
		(src_id, dst_id, predicate, fact, episode_id, strength, occurrences, valid_from, valid_until, embedding, created_at, expired_at)
		VALUES (?, ?, ?, ?, ?, 5, 1, 0, 0, x'', 1, ?)`,
		srcID, dstID, predicate, fact, episodeID, expiredAt)
	if err != nil {
		t.Fatalf("seed edge %q: %v", fact, err)
	}
	id, _ := res.LastInsertId()
	return id
}

// contractReadOverlay reads one kg_watch row by edge id. Absent = fatal.
func contractReadOverlay(t *testing.T, s *Store, edgeID int64) contractOverlay {
	t.Helper()
	var o contractOverlay
	err := s.db.QueryRow(`SELECT edge_id, class, state, sightings, first_seen, last_seen,
		evidence, suggested_fix, pr_url, updated_at, verdict, comment, commented_at
		FROM kg_watch WHERE edge_id = ?`, edgeID).
		Scan(&o.EdgeID, &o.Class, &o.State, &o.Sightings, &o.FirstSeen, &o.LastSeen,
			&o.Evidence, &o.SuggestedFix, &o.PRURL, &o.UpdatedAt, &o.Verdict, &o.Comment, &o.CommentedAt)
	if err != nil {
		t.Fatalf("read kg_watch overlay for edge %d: %v", edgeID, err)
	}
	return o
}

func contractCount(t *testing.T, s *Store, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count (%s): %v", query, err)
	}
	return n
}

// contractSeedWatchedEdge inserts a live edge between two named nodes and puts
// a watch overlay on it via the frozen S3 verb.
func contractSeedWatchedEdge(t *testing.T, s *Store, src, dst, predicate, fact string, w WatchItem) int64 {
	t.Helper()
	id := contractSeedLiveEdge(t, s, src, dst, predicate, fact, 5)
	if err := s.WatchEdge(id, w); err != nil {
		t.Fatalf("WatchEdge(%d): %v", id, err)
	}
	return id
}

func contractSeedLiveEdge(t *testing.T, s *Store, src, dst, predicate, fact string, strength int64) int64 {
	t.Helper()
	a, err := s.UpsertNode(GraphNode{Name: src, Type: "component"})
	if err != nil {
		t.Fatalf("upsert src %q: %v", src, err)
	}
	b, err := s.UpsertNode(GraphNode{Name: dst, Type: "component"})
	if err != nil {
		t.Fatalf("upsert dst %q: %v", dst, err)
	}
	id, added, err := s.InsertEdge(GraphEdge{
		SrcID: a, DstID: b, Predicate: predicate, Fact: fact, Strength: strength, ValidFrom: 1,
	})
	if err != nil || !added {
		t.Fatalf("insert edge %q: added=%v err=%v", fact, added, err)
	}
	return id
}

func contractOpenFreshStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// S1 / S2 — schema v16 and the migration hook
// ---------------------------------------------------------------------------

// TestContractSchemaV16Lands pins S1's version bump and S2.7's two teardown
// obligations: the flat table is DROPPED and its ingest cursor deleted, while
// other sources' cursors are untouched.
func TestContractSchemaV16Lands(t *testing.T) {
	db, path := contractOpenV15(t)
	if _, err := db.Exec(`INSERT INTO kg_ingest_cursor (source, cursor, updated_at) VALUES ('rows:watchlist', 8, 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kg_ingest_cursor (source, cursor, updated_at) VALUES ('rows:feedback', 4, 1)`); err != nil {
		t.Fatal(err)
	}
	s := contractMigrate(t, db, path)

	// S1: schemaVersion = 17. (Was 16 when this contract was frozen; the Slack
	// intake bot's slack_inbox/slack_marks migration bumped it — an
	// append-only step that leaves every assertion below this one intact.)
	if schemaVersion != 17 {
		t.Fatalf("schemaVersion = %d, want 17 (S1)", schemaVersion)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}

	// S1: kg_watch exists and carries the frozen column set.
	if n := contractCount(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='kg_watch'`); n != 1 {
		t.Fatal("kg_watch table absent after v16 (S1)")
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM pragma_table_info('kg_watch')`); n != 13 {
		t.Fatalf("kg_watch has %d columns, want 13 (edge_id + the twelve overlay fields, S1)", n)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='kg_watch_state_last_seen'`); n != 1 {
		t.Fatal("kg_watch_state_last_seen index absent (S1)")
	}

	// S2.7: the flat table is gone and its cursor with it; other sources stay.
	if n := contractCount(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='watchlist'`); n != 0 {
		t.Fatal("watchlist table survived v16 — S2.7 requires DROP TABLE (clean cutover)")
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_ingest_cursor WHERE source = 'rows:watchlist'`); n != 0 {
		t.Fatal("rows:watchlist ingest cursor survived — S2.7 deletes it")
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_ingest_cursor WHERE source = 'rows:feedback'`); n != 1 {
		t.Fatal("v16 deleted an unrelated ingest cursor — S2.7 names only rows:watchlist")
	}
}

// TestContractMigrateV16CarriesEveryState pins S2.1: EVERY flat row migrates,
// shipped and declined included — those are the suppression signals the dream
// loop still reads. Each lands its own overlay carrying its own state.
func TestContractMigrateV16CarriesEveryState(t *testing.T) {
	db, path := contractOpenV15(t)
	for _, st := range []string{"watching", "shipped", "declined"} {
		contractInsertFlatRow(t, db, contractFlatWatchRow{
			Observation: "the " + st + " observation", Class: "waste", State: st,
			Sightings: 1, FirstSeen: 100, LastSeen: 200, UpdatedAt: 200,
		})
	}
	s := contractMigrate(t, db, path)

	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch`); n != 3 {
		t.Fatalf("kg_watch has %d rows, want 3 — S2.1 migrates rows in EVERY state", n)
	}
	for _, st := range []string{"watching", "shipped", "declined"} {
		n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch w JOIN kg_edges e ON e.id = w.edge_id
			WHERE w.state = ? AND e.fact = ? AND e.expired_at = 0`, st, "the "+st+" observation")
		if n != 1 {
			t.Fatalf("state %q: %d live watched edges, want 1 (S2.1/S2.4)", st, n)
		}
	}
}

// TestContractMigrateV16MatchBackfillsEpisode pins S2.3 (the episode's exact
// content/hash/kind/ref/ts/state), S2.4's MATCH branch (case- and
// whitespace-insensitive lookup over live edges, and the sanctioned one-time
// episode_id backfill), and S2.5's verbatim twelve-field copy.
func TestContractMigrateV16MatchBackfillsEpisode(t *testing.T) {
	db, path := contractOpenV15(t)
	src := contractInsertRawNode(t, db, "zeus", "component", "the orchestrator")
	dst := contractInsertRawNode(t, db, "transcripts", "artifact", "")
	// LOWER(fact) equals LOWER(oneLine(observation)) — differing case and
	// internal whitespace is exactly the shape S2.4 must still match.
	edgeID := contractInsertRawEdge(t, db, src, dst, "re-reads", "Zeus Re-Reads The Same Transcript", 0, 0)

	// Single-spaced here, differing from the seeded edge's fact only in CASE, so
	// this test isolates the case-insensitive half of the S2.4 match while
	// asserting the episode CONTENT byte for byte. (Amended S2.3 now states the
	// content carries the RAW observation; the whitespace-messy side of that is
	// pinned by TestContractMigrateV16FallbackEdge, and whitespace-insensitive
	// MATCHING by the collision test below.)
	row := contractFlatWatchRow{
		Observation: "zeus re-reads the same transcript",
		Class:       "waste", State: "watching",
		Sightings: 3, FirstSeen: 1000, LastSeen: 2000,
		Evidence: `{"n":3}`, SuggestedFix: "cache it", PRURL: "https://example/pr/1",
		UpdatedAt: 2500, Verdict: "fix", Comment: "agreed", CommentedAt: "2026-01-02T03:04:05Z",
	}
	oldID := contractInsertFlatRow(t, db, row)
	s := contractMigrate(t, db, path)

	// S2.4 match: no new edge was minted; the pre-existing one is the watch edge.
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_edges`); n != 1 {
		t.Fatalf("kg_edges has %d rows, want 1 — S2.4's match branch must REUSE the live edge, not add a fallback", n)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch WHERE edge_id = ?`, edgeID); n != 1 {
		t.Fatalf("no overlay on the matched edge %d (S2.4/S2.5)", edgeID)
	}

	// S2.3: exactly one watchlist episode, content/hash/kind/ref/ts/state frozen.
	wantContent := contractEpisodeContent(row)
	wantHash := contractEpisodeHash("watchlist", fmt.Sprintf("watchlist:%d", oldID), wantContent)
	var epID, epTS int64
	var epKind, epRef, epContent, epHash, epState string
	err := s.db.QueryRow(`SELECT id, kind, ref, content, hash, ts, state FROM kg_episodes`).
		Scan(&epID, &epKind, &epRef, &epContent, &epHash, &epTS, &epState)
	if err != nil {
		t.Fatalf("read the migrated episode (S2.3): %v", err)
	}
	if epKind != "watchlist" {
		t.Fatalf("episode kind = %q, want watchlist (S2.3)", epKind)
	}
	if epRef != fmt.Sprintf("watchlist:%d", oldID) {
		t.Fatalf("episode ref = %q, want watchlist:%d (S2.3)", epRef, oldID)
	}
	if epContent != wantContent {
		t.Fatalf("episode content =\n%q\nwant (the retiring sweepWatchlist format, S2.3)\n%q", epContent, wantContent)
	}
	if epHash != wantHash {
		t.Fatalf("episode hash = %q, want hex(sha256(kind\\x00ref\\x00content)) = %q (S2.3)", epHash, wantHash)
	}
	if epTS != row.LastSeen {
		t.Fatalf("episode ts = %d, want the row's last_seen %d (S2.3)", epTS, row.LastSeen)
	}
	if epState != "done" {
		t.Fatalf("episode state = %q, want done (S2.3/S2.6)", epState)
	}

	// S2.4 match: episode_id was 0, so it is backfilled to the S2.3 episode.
	var gotEpisode int64
	if err := s.db.QueryRow(`SELECT episode_id FROM kg_edges WHERE id = ?`, edgeID).Scan(&gotEpisode); err != nil {
		t.Fatal(err)
	}
	if gotEpisode != epID {
		t.Fatalf("edge episode_id = %d, want the migration's episode %d (S2.4 backfill)", gotEpisode, epID)
	}

	// S2.5: all twelve overlay fields copied verbatim, carl's trio included —
	// the migration is the one path that carries an existing judgement across.
	got := contractReadOverlay(t, s, edgeID)
	want := contractOverlay{
		EdgeID: edgeID, Class: row.Class, State: row.State, Sightings: row.Sightings,
		FirstSeen: row.FirstSeen, LastSeen: row.LastSeen, Evidence: row.Evidence,
		SuggestedFix: row.SuggestedFix, PRURL: row.PRURL, UpdatedAt: row.UpdatedAt,
		Verdict: row.Verdict, Comment: row.Comment, CommentedAt: row.CommentedAt,
	}
	if got != want {
		t.Fatalf("overlay = %+v\nwant a verbatim twelve-field copy (S2.5) %+v", got, want)
	}

	// W5 + A1: the fact sentence itself is never rewritten by the migration.
	var fact string
	if err := s.db.QueryRow(`SELECT fact FROM kg_edges WHERE id = ?`, edgeID).Scan(&fact); err != nil {
		t.Fatal(err)
	}
	if fact != "Zeus Re-Reads The Same Transcript" {
		t.Fatalf("fact = %q — no slice may UPDATE kg_edges.fact (W5)", fact)
	}
}

// TestContractMigrateV16LowestMatchingEdgeWins pins S2.4's tie-break: when
// several live edges normalize to the same fact, the LOWEST edge id is the
// watch edge — deterministic, so a re-run lands the overlay in the same place.
func TestContractMigrateV16LowestMatchingEdgeWins(t *testing.T) {
	db, path := contractOpenV15(t)
	a := contractInsertRawNode(t, db, "dream pass", "component", "")
	b := contractInsertRawNode(t, db, "kraken", "component", "")
	low := contractInsertRawEdge(t, db, a, b, "observed", "the daemon polls hourly", 0, 0)
	high := contractInsertRawEdge(t, db, a, b, "notes", "The Daemon   Polls  HOURLY", 0, 0)
	if high <= low {
		t.Fatalf("fixture: expected the second edge to take a higher id (%d, %d)", low, high)
	}
	contractInsertFlatRow(t, db, contractFlatWatchRow{
		Observation: "The daemon polls hourly", Class: "repetition", State: "watching",
		Sightings: 1, FirstSeen: 5, LastSeen: 9, UpdatedAt: 9,
	})
	s := contractMigrate(t, db, path)

	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch`); n != 1 {
		t.Fatalf("kg_watch has %d rows, want 1", n)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch WHERE edge_id = ?`, low); n != 1 {
		t.Fatalf("overlay did not land on the lowest matching edge %d — S2.4's tie-break", low)
	}
}

// TestContractMigrateV16FallbackEdge pins S2.4's NO-MATCH branch field by
// field: the mechanical `dream pass --observed--> kraken` edge, the node
// resolution rule (case-insensitive reuse, never a second `kraken` node, the
// canned description written only when the node is created new), and the
// per-field edge shape (fact/episode/strength/occurrences/valid_from/live).
func TestContractMigrateV16FallbackEdge(t *testing.T) {
	db, path := contractOpenV15(t)
	// A `kraken` node already exists, spelled differently and already glossed.
	contractInsertRawNode(t, db, "Kraken", "system", "the gloss that was already stored")

	row := contractFlatWatchRow{
		Observation: "zeus   re-reads\nthe same spec twice",
		Class:       "waste", State: "watching",
		Sightings: 4, FirstSeen: 1111, LastSeen: 2222, UpdatedAt: 2222,
		SuggestedFix: "cache the spec",
	}
	oldID := contractInsertFlatRow(t, db, row)
	s := contractMigrate(t, db, path)

	wantFact := contractOneLine(row.Observation)
	var edgeID, srcID, dstID, episodeID, strength, occurrences, validFrom, expiredAt, createdAt int64
	var predicate, fact string
	err := s.db.QueryRow(`SELECT id, src_id, dst_id, predicate, fact, episode_id, strength,
		occurrences, valid_from, expired_at, created_at FROM kg_edges`).
		Scan(&edgeID, &srcID, &dstID, &predicate, &fact, &episodeID, &strength,
			&occurrences, &validFrom, &expiredAt, &createdAt)
	if err != nil {
		t.Fatalf("read the fallback edge (S2.4): %v", err)
	}
	if fact != wantFact {
		t.Fatalf("fallback fact = %q, want oneLine(observation) = %q (S2.4)", fact, wantFact)
	}
	if predicate != "observed" {
		t.Fatalf("fallback predicate = %q, want observed (S2.4)", predicate)
	}
	if strength != 5 {
		t.Fatalf("fallback strength = %d, want 5 (S2.4)", strength)
	}
	// AMENDED S2.4 (review round 1): occurrences = 1, NOT the row's sightings.
	// `occurrences` counts distinct EPISODES independently stating the fact, and
	// a flat row is ONE episode however many times the dream loop sighted it —
	// seeding it from sightings would hand every migrated fact an unearned
	// rerank advantage. The recurrence count lives in kg_watch.sightings alone
	// (S3.10), which the overlay assertion further down still pins verbatim.
	if occurrences != 1 {
		t.Fatalf("fallback occurrences = %d, want 1 (amended S2.4) — the row's sightings (%d) belong in kg_watch.sightings, not in the graph's corroboration counter (S3.10)",
			occurrences, row.Sightings)
	}
	if validFrom != row.FirstSeen {
		t.Fatalf("fallback valid_from = %d, want the row's first_seen %d (S2.4)", validFrom, row.FirstSeen)
	}
	if expiredAt != 0 {
		t.Fatalf("fallback edge is not live: expired_at = %d (S2.4)", expiredAt)
	}
	if createdAt == 0 {
		t.Fatal("fallback created_at = 0, want now (S2.4)")
	}

	// Episode wiring: the fallback edge points at the S2.3 episode for its row,
	// found BY HASH. AMENDED S2.3 (review round 1) now states explicitly that
	// the content carries the RAW stored observation — the oneLine rule applies
	// to the fact text and the S2.4 match only — so this row's deliberately
	// whitespace-messy observation makes the raw-vs-normalized distinction
	// load-bearing rather than ambiguous: a content built from `obs` instead of
	// `r.observation` hashes differently and breaks the find-or-create reuse
	// that keeps an already-swept row from getting two provenance episodes.
	wantHash := contractEpisodeHash("watchlist", fmt.Sprintf("watchlist:%d", oldID), contractEpisodeContent(row))
	var epID int64
	if err := s.db.QueryRow(`SELECT id FROM kg_episodes WHERE kind = 'watchlist' AND hash = ?`, wantHash).Scan(&epID); err != nil {
		t.Fatalf("no S2.3 episode for row %d at the RAW-observation hash (amended S2.3): %v", oldID, err)
	}
	if episodeID != epID {
		t.Fatalf("fallback episode_id = %d, want the S2.3 episode %d (S2.4)", episodeID, epID)
	}

	// Node resolution: src is the canned `dream pass` component, dst is the
	// PRE-EXISTING kraken node (resolved case-insensitively, gloss untouched
	// because the node was not created new).
	var srcName, srcType, srcDesc string
	if err := s.db.QueryRow(`SELECT name, type, description FROM kg_nodes WHERE id = ?`, srcID).
		Scan(&srcName, &srcType, &srcDesc); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(srcName, "dream pass") {
		t.Fatalf("fallback src node = %q, want `dream pass` (S2.4)", srcName)
	}
	if srcType != "component" {
		t.Fatalf("dream pass type = %q, want component (S2.4)", srcType)
	}
	if srcDesc != "The nightly corroboration pass that reads the previous day's feedback and transcripts." {
		t.Fatalf("dream pass description = %q, want the canned gloss (S2.4)", srcDesc)
	}
	var dstName, dstDesc string
	if err := s.db.QueryRow(`SELECT name, description FROM kg_nodes WHERE id = ?`, dstID).
		Scan(&dstName, &dstDesc); err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(dstName, "kraken") {
		t.Fatalf("fallback dst node = %q, want `kraken` (S2.4)", dstName)
	}
	if dstDesc != "the gloss that was already stored" {
		t.Fatalf("dst description = %q — S2.4 writes the canned kraken gloss only when the node is CREATED NEW", dstDesc)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_nodes WHERE LOWER(name) = 'kraken'`); n != 1 {
		t.Fatalf("%d kraken nodes — S2.4 resolves case-insensitively and never mints a second one", n)
	}

	// The overlay still copies all twelve fields (S2.5) on the fallback path.
	got := contractReadOverlay(t, s, edgeID)
	if got.Sightings != row.Sightings || got.FirstSeen != row.FirstSeen || got.LastSeen != row.LastSeen ||
		got.Class != row.Class || got.State != row.State || got.SuggestedFix != row.SuggestedFix {
		t.Fatalf("overlay = %+v, want a verbatim copy of %+v (S2.5)", got, row)
	}

	// The fact reached FTS through the existing kg_edges triggers (S2.4's
	// closing sentence): a migrated watch item is searchable with no extra
	// plumbing, which is the whole point of the move.
	ids, err := s.SearchEdgesFTS("spec twice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || ids[0] != edgeID {
		t.Fatalf("FTS = %v, want [%d] — the migrated fact must be searchable (S2.4)", ids, edgeID)
	}
}

// TestContractMigrateV16FoldsCaseWhitespaceCollision is the fixture the
// contract's "Blind test files" section MANDATES: two flat rows whose
// observations differ only in case and internal whitespace land on ONE edge
// and fold per S2.5 — greater last_seen wins every field, EXCEPT sightings
// (sum) and first_seen (min).
func TestContractMigrateV16FoldsCaseWhitespaceCollision(t *testing.T) {
	db, path := contractOpenV15(t)
	src := contractInsertRawNode(t, db, "zeus", "component", "")
	dst := contractInsertRawNode(t, db, "files", "artifact", "")
	edgeID := contractInsertRawEdge(t, db, src, dst, "re-reads", "Zeus re-reads the Same File", 0, 0)

	older := contractFlatWatchRow{
		Observation: "zeus  re-reads\tthe SAME file",
		Class:       "waste", State: "watching",
		Sightings: 3, FirstSeen: 500, LastSeen: 2000,
		Evidence: "older evidence", SuggestedFix: "older fix", PRURL: "",
		UpdatedAt: 2000,
	}
	newer := contractFlatWatchRow{
		Observation: "Zeus re-reads the same FILE",
		Class:       "repetition", State: "shipped",
		Sightings: 4, FirstSeen: 1500, LastSeen: 5000,
		Evidence: "newer evidence", SuggestedFix: "newer fix", PRURL: "https://example/pr/9",
		UpdatedAt: 5000, Verdict: "fine", Comment: "no action", CommentedAt: "2026-02-03T04:05:06Z",
	}
	oldA := contractInsertFlatRow(t, db, older)
	oldB := contractInsertFlatRow(t, db, newer)
	s := contractMigrate(t, db, path)

	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch`); n != 1 {
		t.Fatalf("kg_watch has %d rows, want 1 — the collision pair must FOLD onto one edge (S2.5)", n)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_edges`); n != 1 {
		t.Fatalf("kg_edges has %d rows, want 1 — both rows match the same live edge (S2.4)", n)
	}

	got := contractReadOverlay(t, s, edgeID)
	want := contractOverlay{
		EdgeID: edgeID,
		// Every field from the greater-last_seen row...
		Class: newer.Class, State: newer.State, LastSeen: newer.LastSeen,
		Evidence: newer.Evidence, SuggestedFix: newer.SuggestedFix, PRURL: newer.PRURL,
		UpdatedAt: newer.UpdatedAt, Verdict: newer.Verdict, Comment: newer.Comment,
		CommentedAt: newer.CommentedAt,
		// ...except these two.
		Sightings: older.Sightings + newer.Sightings,
		FirstSeen: older.FirstSeen,
	}
	if got != want {
		t.Fatalf("folded overlay = %+v\nwant %+v (S2.5: newer wins, sightings sum, first_seen min)", got, want)
	}

	// S2.3 is per-row: both rows still get their own provenance episode, both
	// done (S2.6), and the edge's backfilled episode_id is one of them.
	for _, oldID := range []int64{oldA, oldB} {
		if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_episodes WHERE ref = ?`,
			fmt.Sprintf("watchlist:%d", oldID)); n != 1 {
			t.Fatalf("row %d has no episode of its own — S2.3 runs per row", oldID)
		}
	}
	var episodeID int64
	if err := s.db.QueryRow(`SELECT episode_id FROM kg_edges WHERE id = ?`, edgeID).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	if episodeID == 0 {
		t.Fatal("edge episode_id still 0 after the fold — S2.4 backfills it")
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_episodes WHERE id = ? AND kind = 'watchlist'`, episodeID); n != 1 {
		t.Fatalf("edge episode_id %d is not one of the migration's watchlist episodes (S2.4)", episodeID)
	}
}

// TestContractMigrateV16MarksWatchlistEpisodesDone pins S2.6: after the loop,
// EVERY kind='watchlist' episode is done — including ones the retiring sweep
// had left pending — while other kinds' pending episodes are untouched.
func TestContractMigrateV16MarksWatchlistEpisodesDone(t *testing.T) {
	db, path := contractOpenV15(t)
	if _, err := db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('watchlist', 'watchlist:404', 'a sweep episode for a row that is gone', 'h-watch', 7, 'pending', 7)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('feedback', '12', 'unrelated pending work', 'h-feed', 7, 'pending', 7)`); err != nil {
		t.Fatal(err)
	}
	contractInsertFlatRow(t, db, contractFlatWatchRow{
		Observation: "a live observation", Class: "feedback", State: "watching",
		Sightings: 1, FirstSeen: 1, LastSeen: 2, UpdatedAt: 2,
	})
	s := contractMigrate(t, db, path)

	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_episodes WHERE kind = 'watchlist' AND state != 'done'`); n != 0 {
		t.Fatalf("%d watchlist episodes are still pending — S2.6 marks every one done", n)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_episodes WHERE kind = 'feedback' AND state = 'pending'`); n != 1 {
		t.Fatal("v16 drained a non-watchlist pending episode — S2.6 names kind='watchlist' only")
	}
}

// TestContractMigrateV16IsIdempotentByHash pins the find-or-create half of
// S2.3: an episode whose hash already exists is REUSED, not duplicated.
func TestContractMigrateV16IsIdempotentByHash(t *testing.T) {
	db, path := contractOpenV15(t)
	row := contractFlatWatchRow{
		Observation: "the same observation twice over", Class: "prompting", State: "watching",
		Sightings: 2, FirstSeen: 10, LastSeen: 20, UpdatedAt: 20,
	}
	oldID := contractInsertFlatRow(t, db, row)
	// Pre-plant the exact episode the migration would mint.
	content := contractEpisodeContent(row)
	hash := contractEpisodeHash("watchlist", fmt.Sprintf("watchlist:%d", oldID), content)
	res, err := db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('watchlist', ?, ?, ?, ?, 'pending', 1)`,
		fmt.Sprintf("watchlist:%d", oldID), content, hash, row.LastSeen)
	if err != nil {
		t.Fatal(err)
	}
	plantedID, _ := res.LastInsertId()

	s := contractMigrate(t, db, path)
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_episodes WHERE hash = ?`, hash); n != 1 {
		t.Fatalf("%d episodes carry the hash — S2.3 find-or-creates by the unique hash", n)
	}
	var episodeID int64
	if err := s.db.QueryRow(`SELECT episode_id FROM kg_edges`).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	if episodeID != plantedID {
		t.Fatalf("edge episode_id = %d, want the reused episode %d (S2.3)", episodeID, plantedID)
	}
}

// ---------------------------------------------------------------------------
// S3 — internal/store/watch.go's frozen verbs
// ---------------------------------------------------------------------------

// TestContractListWatchItemsOrderAndLiveFilter pins S3.1: the three-way join,
// the ordering (last_seen DESC, edge_id DESC), the state filter, and the
// construction that matters most — an overlay whose edge is INVALIDATED drops
// out of the DREAM view without anyone touching the overlay.
func TestContractListWatchItemsOrderAndLiveFilter(t *testing.T) {
	s := contractOpenFreshStore(t)
	a := contractSeedWatchedEdge(t, s, "zeus", "kraken", "concerns", "fact a",
		WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 100, LastSeen: 300})
	b := contractSeedWatchedEdge(t, s, "zeus", "kraken", "concerns", "fact b",
		WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 100, LastSeen: 300})
	c := contractSeedWatchedEdge(t, s, "zeus", "kraken", "concerns", "fact c",
		WatchItem{Class: "waste", State: "shipped", Sightings: 1, FirstSeen: 100, LastSeen: 200})
	if b <= a {
		t.Fatalf("fixture: expected ascending edge ids, got %d then %d", a, b)
	}

	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatal(err)
	}
	// S3.1: last_seen DESC, then edge_id DESC.
	if len(all) != 3 || all[0].EdgeID != b || all[1].EdgeID != a || all[2].EdgeID != c {
		t.Fatalf("order = %+v, want [%d %d %d] (last_seen DESC, edge_id DESC — S3.1)", all, b, a, c)
	}
	// S3.1: the join hydrates the endpoints and the fact sentence.
	if all[0].Observation != "fact b" {
		t.Fatalf("Observation = %q, want the edge's fact (S3, WatchItem.Observation)", all[0].Observation)
	}
	if all[0].Src != "zeus" || all[0].Dst != "kraken" || all[0].Predicate != "concerns" {
		t.Fatalf("endpoints = %q/%q/%q, want zeus/kraken/concerns joined from kg_nodes (S3.1)", all[0].Src, all[0].Dst, all[0].Predicate)
	}

	// S3.1: a non-empty state filters.
	shipped, err := s.ListWatchItems("shipped")
	if err != nil {
		t.Fatal(err)
	}
	if len(shipped) != 1 || shipped[0].EdgeID != c {
		t.Fatalf("state=shipped = %+v, want only edge %d (S3.1)", shipped, c)
	}

	// S3.1: invalidating the underlying fact removes it from the DREAM view by
	// construction — the overlay row is not touched.
	if err := s.InvalidateEdge(b, 1); err != nil {
		t.Fatal(err)
	}
	after, err := s.ListWatchItems("")
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 2 || after[0].EdgeID != a {
		t.Fatalf("after invalidation list = %+v, want the invalidated edge gone (S3.1)", after)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch WHERE edge_id = ?`, b); n != 1 {
		t.Fatal("invalidation deleted the overlay row — S3.1 hides it by JOIN, it does not delete")
	}
	// S3.2: the same rule on the single-row read.
	if _, err := s.GetWatchItem(b); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetWatchItem on a dead edge = %v, want ErrNotFound (S3.2)", err)
	}
	got, err := s.GetWatchItem(a)
	if err != nil || got == nil || got.EdgeID != a || got.Observation != "fact a" {
		t.Fatalf("GetWatchItem(%d) = %+v, %v (S3.2)", a, got, err)
	}
}

// TestContractFindWatchItemNormalizesAndPicksLatest pins S3.3: the lookup
// normalizes with oneLine + LOWER, searches WATCHED live edges only, and
// breaks ties on greatest last_seen.
func TestContractFindWatchItemNormalizesAndPicksLatest(t *testing.T) {
	s := contractOpenFreshStore(t)
	stale := contractSeedWatchedEdge(t, s, "zeus", "kraken", "concerns", "the daemon polls hourly",
		WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 1, LastSeen: 100})
	fresh := contractSeedWatchedEdge(t, s, "zeus", "kraken", "notes", "The Daemon Polls Hourly",
		WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 1, LastSeen: 900})
	// An UNWATCHED live edge with the same normalized fact must be invisible here.
	unwatched := contractSeedLiveEdge(t, s, "zeus", "kraken", "mentions", "THE DAEMON   polls  hourly", 5)

	got, err := s.FindWatchItem("  the   daemon\n polls HOURLY  ")
	if err != nil {
		t.Fatalf("FindWatchItem: %v (S3.3)", err)
	}
	if got == nil || got.EdgeID != fresh {
		t.Fatalf("FindWatchItem = %+v, want edge %d (greatest last_seen wins — S3.3)", got, fresh)
	}
	if got.EdgeID == unwatched {
		t.Fatal("FindWatchItem returned an UNWATCHED edge — S3.3 searches watched edges only")
	}

	// S3.4: the create-vs-promote probe sees ALL live edges, watched or not,
	// and the LOWEST id wins — the opposite tie-break, deliberately.
	edge, err := s.FindLiveEdgeByFact("the daemon polls hourly")
	if err != nil {
		t.Fatalf("FindLiveEdgeByFact: %v (S3.4)", err)
	}
	if edge == nil || edge.ID != stale {
		t.Fatalf("FindLiveEdgeByFact = %+v, want the lowest matching edge %d (S3.4)", edge, stale)
	}
	// S3.4: nothing matching is (nil, nil), not an error — it is the signal to
	// CREATE.
	miss, err := s.FindLiveEdgeByFact("a sentence nobody ever said")
	if err != nil {
		t.Fatalf("FindLiveEdgeByFact miss returned an error: %v — S3.4 says (nil, nil)", err)
	}
	if miss != nil {
		t.Fatalf("FindLiveEdgeByFact miss = %+v, want nil (S3.4)", miss)
	}
}

// TestContractFindLiveEdgeByFactSeesUnwatchedEdges pins the promote half of
// S3.4: an edge with NO overlay is exactly what this probe exists to find.
func TestContractFindLiveEdgeByFactSeesUnwatchedEdges(t *testing.T) {
	s := contractOpenFreshStore(t)
	id := contractSeedLiveEdge(t, s, "artifacts", "95 columns", "wrap-at", "artifacts wrap at 95 columns", 6)
	got, err := s.FindLiveEdgeByFact("ARTIFACTS   wrap\tat 95 columns")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ID != id {
		t.Fatalf("FindLiveEdgeByFact = %+v, want the unwatched edge %d (S3.4)", got, id)
	}
	// An invalidated edge is not a promotion target.
	if err := s.InvalidateEdge(id, 1); err != nil {
		t.Fatal(err)
	}
	dead, err := s.FindLiveEdgeByFact("artifacts wrap at 95 columns")
	if err != nil {
		t.Fatal(err)
	}
	if dead != nil {
		t.Fatalf("FindLiveEdgeByFact returned a dead edge %+v — S3.4 searches LIVE edges", dead)
	}
}

// TestContractWatchEdgeAppliesDefaults pins S3.5's default set and its closing
// rule: a fresh overlay starts UNJUDGED — WatchEdge never writes carl's trio.
func TestContractWatchEdgeAppliesDefaults(t *testing.T) {
	s := contractOpenFreshStore(t)
	id := contractSeedLiveEdge(t, s, "zeus", "kraken", "concerns", "a brand new observation", 5)
	before := time.Now().UnixMilli()
	if err := s.WatchEdge(id, WatchItem{Class: "waste"}); err != nil {
		t.Fatalf("WatchEdge: %v (S3.5)", err)
	}
	after := time.Now().UnixMilli()

	got := contractReadOverlay(t, s, id)
	if got.Sightings != 1 {
		t.Fatalf("sightings = %d, want the default 1 (S3.5)", got.Sightings)
	}
	if got.State != "watching" {
		t.Fatalf("state = %q, want the default watching (S3.5)", got.State)
	}
	if got.LastSeen < before || got.LastSeen > after {
		t.Fatalf("last_seen = %d, want now in [%d,%d] (S3.5)", got.LastSeen, before, after)
	}
	if got.FirstSeen != got.LastSeen {
		t.Fatalf("first_seen = %d, last_seen = %d — S3.5 defaults first_seen to last_seen", got.FirstSeen, got.LastSeen)
	}
	if got.UpdatedAt < before || got.UpdatedAt > after {
		t.Fatalf("updated_at = %d, want now (S3.5)", got.UpdatedAt)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("WatchEdge wrote carl's trio %+v — S3.5/W2 say a fresh overlay starts unjudged", got)
	}
	// Explicit values are honoured rather than defaulted over.
	id2 := contractSeedLiveEdge(t, s, "zeus", "kraken", "concerns", "an observation with history", 5)
	if err := s.WatchEdge(id2, WatchItem{
		Class: "repetition", State: "shipped", Sightings: 9,
		FirstSeen: 111, LastSeen: 222, Evidence: "e", SuggestedFix: "f", PRURL: "u", UpdatedAt: 333,
	}); err != nil {
		t.Fatal(err)
	}
	got2 := contractReadOverlay(t, s, id2)
	want2 := contractOverlay{
		EdgeID: id2, Class: "repetition", State: "shipped", Sightings: 9,
		FirstSeen: 111, LastSeen: 222, Evidence: "e", SuggestedFix: "f", PRURL: "u", UpdatedAt: 333,
	}
	if got2 != want2 {
		t.Fatalf("overlay = %+v, want the supplied values %+v (S3.5)", got2, want2)
	}
}

// TestContractUpdateWatchItemIsPartial pins S3.6: a zero-valued field is left
// alone, UpdatedAt is always stamped, and the carl trio is NEVER touched —
// the single most important behaviour of the agent's write path (W2).
func TestContractUpdateWatchItemIsPartial(t *testing.T) {
	s := contractOpenFreshStore(t)
	id := contractSeedWatchedEdge(t, s, "zeus", "kraken", "concerns", "an observation carl already judged",
		WatchItem{
			Class: "repetition", State: "watching", Sightings: 7,
			FirstSeen: 1000, LastSeen: 2000, Evidence: "old evidence", UpdatedAt: 2000,
		})
	if err := s.SetWatchVerdict(id, "fix", "agreed", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UnixMilli()
	if err := s.UpdateWatchItem(id, WatchItem{SuggestedFix: "do X"}); err != nil {
		t.Fatalf("UpdateWatchItem: %v (S3.6)", err)
	}
	after := time.Now().UnixMilli()

	got := contractReadOverlay(t, s, id)
	if got.SuggestedFix != "do X" {
		t.Fatalf("the populated field did not land: %+v (S3.6)", got)
	}
	// Zero-valued fields left alone — the counters the table exists to record.
	if got.Sightings != 7 || got.FirstSeen != 1000 || got.LastSeen != 2000 {
		t.Fatalf("a partial update zeroed history: sightings=%d first=%d last=%d (S3.6)", got.Sightings, got.FirstSeen, got.LastSeen)
	}
	if got.Class != "repetition" || got.State != "watching" || got.Evidence != "old evidence" {
		t.Fatalf("a partial update clobbered untouched columns: %+v (S3.6)", got)
	}
	// UpdatedAt always stamped (now when zero).
	if got.UpdatedAt < before || got.UpdatedAt > after {
		t.Fatalf("updated_at = %d, want now in [%d,%d] (S3.6)", got.UpdatedAt, before, after)
	}
	// W2/S3.6: carl's trio is untouched by any agent-reachable write.
	if got.Verdict != "fix" || got.Comment != "agreed" || got.CommentedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("the agent's update erased carl's judgement: %+v (S3.6/W2)", got)
	}
	// S3.6: the verb cannot change the edge — the fact sentence is immutable.
	var fact string
	if err := s.db.QueryRow(`SELECT fact FROM kg_edges WHERE id = ?`, id).Scan(&fact); err != nil {
		t.Fatal(err)
	}
	if fact != "an observation carl already judged" {
		t.Fatalf("fact = %q — UpdateWatchItem touches overlay columns only (S3.6, W5)", fact)
	}
	// An explicit UpdatedAt is honoured rather than overwritten with now.
	if err := s.UpdateWatchItem(id, WatchItem{Sightings: 12, UpdatedAt: 4242}); err != nil {
		t.Fatal(err)
	}
	got = contractReadOverlay(t, s, id)
	if got.Sightings != 12 || got.UpdatedAt != 4242 {
		t.Fatalf("explicit values ignored: sightings=%d updated_at=%d (S3.6)", got.Sightings, got.UpdatedAt)
	}
}

// TestContractBumpWatchSighting pins S3.7: SQL-side increment, last_seen and
// updated_at both set to the given moment, evidence replaced wholesale,
// first_seen and carl's trio untouched.
func TestContractBumpWatchSighting(t *testing.T) {
	s := contractOpenFreshStore(t)
	id := contractSeedWatchedEdge(t, s, "zeus", "kraken", "concerns", "a repeating observation",
		WatchItem{
			Class: "repetition", State: "watching", Sightings: 3,
			FirstSeen: 1000, LastSeen: 2000, Evidence: "first sighting", UpdatedAt: 2000,
		})
	if err := s.SetWatchVerdict(id, "fine", "noted", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	if err := s.BumpWatchSighting(id, 7777, "second sighting"); err != nil {
		t.Fatalf("BumpWatchSighting: %v (S3.7)", err)
	}
	got := contractReadOverlay(t, s, id)
	if got.Sightings != 4 {
		t.Fatalf("sightings = %d, want 3+1 (S3.7)", got.Sightings)
	}
	if got.LastSeen != 7777 || got.UpdatedAt != 7777 {
		t.Fatalf("last_seen=%d updated_at=%d, want both 7777 (S3.7)", got.LastSeen, got.UpdatedAt)
	}
	if got.Evidence != "second sighting" {
		t.Fatalf("evidence = %q, want it replaced wholesale (S3.7)", got.Evidence)
	}
	if got.FirstSeen != 1000 {
		t.Fatalf("first_seen = %d, want it untouched (S3.7)", got.FirstSeen)
	}
	if got.Verdict != "fine" || got.Comment != "noted" || got.CommentedAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("a bump moved carl's trio: %+v (S3.7/W2)", got)
	}

	// S3.7: lastSeen 0 means now, and the increment is still SQL-side.
	before := time.Now().UnixMilli()
	if err := s.BumpWatchSighting(id, 0, "third sighting"); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UnixMilli()
	got = contractReadOverlay(t, s, id)
	if got.Sightings != 5 {
		t.Fatalf("sightings = %d, want 5 (S3.7)", got.Sightings)
	}
	if got.LastSeen < before || got.LastSeen > after || got.UpdatedAt != got.LastSeen {
		t.Fatalf("lastSeen 0 did not stamp now: last=%d updated=%d (S3.7)", got.LastSeen, got.UpdatedAt)
	}
}

// TestContractSetWatchVerdictWritesExactlyTheTrio pins S3.8: unconditional
// write of the three carl-owned columns (empty values CLEAR — the documented
// undo), nothing else moves, and updated_at is deliberately not bumped.
func TestContractSetWatchVerdictWritesExactlyTheTrio(t *testing.T) {
	s := contractOpenFreshStore(t)
	id := contractSeedWatchedEdge(t, s, "zeus", "kraken", "concerns", "an observation awaiting judgement",
		WatchItem{
			Class: "waste", State: "watching", Sightings: 2,
			FirstSeen: 100, LastSeen: 200, Evidence: "e", SuggestedFix: "f", PRURL: "u", UpdatedAt: 200,
		})

	if err := s.SetWatchVerdict(id, "fix", "do it", "2026-03-04T05:06:07Z"); err != nil {
		t.Fatalf("SetWatchVerdict: %v (S3.8)", err)
	}
	got := contractReadOverlay(t, s, id)
	want := contractOverlay{
		EdgeID: id, Class: "waste", State: "watching", Sightings: 2,
		FirstSeen: 100, LastSeen: 200, Evidence: "e", SuggestedFix: "f", PRURL: "u", UpdatedAt: 200,
		Verdict: "fix", Comment: "do it", CommentedAt: "2026-03-04T05:06:07Z",
	}
	if got != want {
		t.Fatalf("overlay = %+v\nwant only the trio changed, updated_at NOT bumped (S3.8) %+v", got, want)
	}

	// The documented undo: empty values clear all three.
	if err := s.SetWatchVerdict(id, "", "", ""); err != nil {
		t.Fatalf("clear: %v (S3.8)", err)
	}
	got = contractReadOverlay(t, s, id)
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("empty values did not clear the trio: %+v (S3.8)", got)
	}
	if got.UpdatedAt != 200 || got.Sightings != 2 || got.Evidence != "e" {
		t.Fatalf("the clear touched an agent-owned column: %+v (S3.8/W2)", got)
	}
	// "fine" is the third legal verdict.
	if err := s.SetWatchVerdict(id, "fine", "", ""); err != nil {
		t.Fatalf("verdict fine rejected: %v (S3.8)", err)
	}
}

// TestContractUnwatchEdgeLeavesTheFactStanding pins S3.9: remove means
// UNWATCH. The edge and its episode survive untouched, still live, still
// searchable, still a promotion target.
func TestContractUnwatchEdgeLeavesTheFactStanding(t *testing.T) {
	s := contractOpenFreshStore(t)
	epID, _, err := s.AddEpisode(Episode{Kind: "watchlist", Ref: "watch:api", Content: "c", Hash: "h", TS: 1})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.UpsertNode(GraphNode{Name: "zeus", Type: "component"})
	b, _ := s.UpsertNode(GraphNode{Name: "kraken", Type: "component"})
	id, _, err := s.InsertEdge(GraphEdge{
		SrcID: a, DstID: b, Predicate: "concerns", Fact: "a fact that outlives its watch",
		EpisodeID: epID, Strength: 6, ValidFrom: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WatchEdge(id, WatchItem{Class: "waste"}); err != nil {
		t.Fatal(err)
	}

	if err := s.UnwatchEdge(id); err != nil {
		t.Fatalf("UnwatchEdge: %v (S3.9)", err)
	}
	if n := contractCount(t, s, `SELECT COUNT(*) FROM kg_watch WHERE edge_id = ?`, id); n != 0 {
		t.Fatal("overlay row survived UnwatchEdge (S3.9)")
	}
	edges, err := s.GetEdges([]int64{id})
	if err != nil || len(edges) != 1 {
		t.Fatalf("the edge did not survive the unwatch: %v %v (S3.9)", edges, err)
	}
	if edges[0].ExpiredAt != 0 || edges[0].EpisodeID != epID || edges[0].Fact != "a fact that outlives its watch" {
		t.Fatalf("the unwatch mutated the edge: %+v (S3.9)", edges[0])
	}
	if ep, err := s.GetEpisode(epID); err != nil || ep == nil {
		t.Fatalf("the episode did not survive the unwatch: %+v %v (S3.9)", ep, err)
	}
	// Still live, so it remains a promote target for a future add (S3.4).
	live, err := s.FindLiveEdgeByFact("a fact that outlives its watch")
	if err != nil || live == nil || live.ID != id {
		t.Fatalf("the unwatched fact is no longer a live edge: %+v %v (S3.9/S3.4)", live, err)
	}
	// Repeat delete has nothing left to remove.
	if err := s.UnwatchEdge(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("repeat UnwatchEdge = %v, want ErrNotFound (S3.9)", err)
	}
}

// TestContractSightingsAreIndependentOfOccurrences pins S3.10: the overlay's
// sightings counter (the dream loop's) and the edge's occurrences counter
// (episode corroboration) are deliberately not kept in sync by slice S.
func TestContractSightingsAreIndependentOfOccurrences(t *testing.T) {
	s := contractOpenFreshStore(t)
	a, _ := s.UpsertNode(GraphNode{Name: "zeus", Type: "component"})
	b, _ := s.UpsertNode(GraphNode{Name: "kraken", Type: "component"})
	id, _, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "concerns", Fact: "two counters, one row", Strength: 5})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.WatchEdge(id, WatchItem{Class: "waste", Sightings: 1}); err != nil {
		t.Fatal(err)
	}

	// Bumping the watch counter leaves the edge's corroboration count alone.
	if err := s.BumpWatchSighting(id, 500, "again"); err != nil {
		t.Fatal(err)
	}
	edges, err := s.GetEdges([]int64{id})
	if err != nil || len(edges) != 1 {
		t.Fatal(err)
	}
	if edges[0].Occurrences != 1 {
		t.Fatalf("occurrences = %d, want 1 — a watch sighting is not episode corroboration (S3.10)", edges[0].Occurrences)
	}

	// ...and a repeat InsertEdge (real corroboration) leaves sightings alone.
	if _, added, err := s.InsertEdge(GraphEdge{SrcID: a, DstID: b, Predicate: "concerns", Fact: "two counters, one row", Strength: 5}); err != nil || added {
		t.Fatalf("repeat insert: added=%v err=%v", added, err)
	}
	edges, _ = s.GetEdges([]int64{id})
	if edges[0].Occurrences != 2 {
		t.Fatalf("occurrences = %d, want 2 (S3.10)", edges[0].Occurrences)
	}
	got := contractReadOverlay(t, s, id)
	if got.Sightings != 2 {
		t.Fatalf("sightings = %d, want 2 — only the bump moved it (S3.10)", got.Sightings)
	}
}
