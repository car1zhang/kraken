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

// watch_edge_test.go — the EDGE dimension of 50-contract.md's slice S:
// boundaries, error paths, refusals and degenerate inputs on the watch
// overlay (S3.*) and on schema v16's migration hook (S2.*). Happy paths and
// invariants live in watch_contract_test.go; nothing here duplicates them.
//
// Every package-level identifier is prefixed `edge` per the contract's
// "Blind test files" section — this package's test namespace is shared with
// a sibling blind author.

// --- helpers ---------------------------------------------------------------

// edgeOneLine is the contract's normalization rule, spelled out in S2.2 and
// reused by S3.3/S3.4: strings.Join(strings.Fields(s), " "). Written out
// locally rather than calling the implementation's helper so a test can
// never agree with a normalizer that drifted.
func edgeOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func edgeStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// edgeLiveEdge inserts one live edge carrying fact between two bare nodes and
// returns its id — the "existing LIVE edge" every S3 verb is defined against.
func edgeLiveEdge(t *testing.T, s *Store, fact string) int64 {
	t.Helper()
	src, err := s.UpsertNode(GraphNode{Name: "edge src"})
	if err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	dst, err := s.UpsertNode(GraphNode{Name: "edge dst"})
	if err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	id, added, err := s.InsertEdge(GraphEdge{SrcID: src, DstID: dst, Predicate: "observed", Fact: fact, Strength: 5})
	if err != nil {
		t.Fatalf("insert edge %q: %v", fact, err)
	}
	if !added {
		t.Fatalf("insert edge %q collapsed onto an existing row (%d) — fixture facts must be distinct", fact, id)
	}
	return id
}

// edgeV15Store builds a database at schema v15 by applying migrations[1..15]
// directly, exactly as the contract's "Blind test files" section prescribes,
// and hands back a Store pointed at it. Deliberately independent of
// schema_test.go's downgradeFreshDB (slice S owns that helper and its v16
// reversal): a migration test must not be able to pass because the helper it
// leans on was updated in the same breath as the migration.
func edgeV15Store(t *testing.T) *Store {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "kraken.db")
	db, err := sql.Open("sqlite3",
		"file:"+dbPath+"?_foreign_keys=on&_journal_mode=WAL&_busy_timeout=5000&_txlock=immediate")
	if err != nil {
		t.Fatalf("open v15 db: %v", err)
	}
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	t.Cleanup(func() { s.Close() })

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
		t.Fatalf("set user_version 15: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit v15 build: %v", err)
	}
	return s
}

// edgeFlatRow is one pre-v16 `watchlist` row, field for field (schemaV9).
type edgeFlatRow struct {
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

func edgeInsertFlat(t *testing.T, s *Store, r edgeFlatRow) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO watchlist
		(observation, class, state, sightings, first_seen, last_seen, evidence, suggested_fix, pr_url,
		 updated_at, verdict, comment, commented_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Observation, r.Class, r.State, r.Sightings, r.FirstSeen, r.LastSeen, r.Evidence, r.SuggestedFix,
		r.PRURL, r.UpdatedAt, r.Verdict, r.Comment, r.CommentedAt)
	if err != nil {
		t.Fatalf("seed flat watchlist row: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

// edgeEpisodeContent renders S2.3's exact episode format.
func edgeEpisodeContent(r edgeFlatRow, observation string) string {
	c := fmt.Sprintf("watchlist observation (%s, %s, seen %dx): %s", r.Class, r.State, r.Sightings, observation)
	if r.SuggestedFix != "" {
		c += "\nsuggested fix: " + r.SuggestedFix
	}
	if r.Verdict != "" {
		c += "\ncarl's verdict: " + r.Verdict
		if r.Comment != "" {
			c += " — " + r.Comment
		}
	}
	return c
}

// edgeEpisodeHash is S2.3's hash rule: hex(sha256(kind \x00 ref \x00 content)).
func edgeEpisodeHash(kind, ref, content string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + ref + "\x00" + content))
	return hex.EncodeToString(sum[:])
}

func edgeMigrateTo16(t *testing.T, s *Store) {
	t.Helper()
	if err := s.migrate(); err != nil {
		t.Fatalf("migrate to v16: %v", err)
	}
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
}

func edgeWatchRowCount(t *testing.T, s *Store, edgeID int64) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_watch WHERE edge_id = ?`, edgeID).Scan(&n); err != nil {
		t.Fatalf("count kg_watch rows for edge %d: %v", edgeID, err)
	}
	return n
}

// --- S3: refusals and not-found paths -------------------------------------

// TestEdgeWatchVerbsErrNotFoundWithoutOverlay pins the ErrNotFound leg of
// every verb that operates on an EXISTING overlay (S3.2, S3.6, S3.7, S3.8,
// S3.9): a live edge that was never watched, and an edge id that never
// existed at all, both miss.
func TestEdgeWatchVerbsErrNotFoundWithoutOverlay(t *testing.T) {
	s := edgeStore(t)
	live := edgeLiveEdge(t, s, "zeus re-reads the same file every cell")
	const ghost = int64(999999)

	for _, id := range []int64{live, ghost} {
		if _, err := s.GetWatchItem(id); !errors.Is(err, ErrNotFound) { // S3.2
			t.Errorf("GetWatchItem(%d) err = %v, want ErrNotFound", id, err)
		}
		if err := s.UpdateWatchItem(id, WatchItem{SuggestedFix: "x"}); !errors.Is(err, ErrNotFound) { // S3.6
			t.Errorf("UpdateWatchItem(%d) err = %v, want ErrNotFound", id, err)
		}
		if err := s.BumpWatchSighting(id, 100, "e"); !errors.Is(err, ErrNotFound) { // S3.7
			t.Errorf("BumpWatchSighting(%d) err = %v, want ErrNotFound", id, err)
		}
		if err := s.SetWatchVerdict(id, "fine", "", ""); !errors.Is(err, ErrNotFound) { // S3.8
			t.Errorf("SetWatchVerdict(%d) err = %v, want ErrNotFound", id, err)
		}
		if err := s.UnwatchEdge(id); !errors.Is(err, ErrNotFound) { // S3.9
			t.Errorf("UnwatchEdge(%d) err = %v, want ErrNotFound", id, err)
		}
	}
}

// TestEdgeInvalidatedEdgeVanishesFromWatchReads is S3.1's construction rule
// at its sharpest: invalidating a watched fact removes it from the DREAM
// view, so every READ must stop seeing it (S3.1, S3.2, S3.3) — while the
// overlay row itself is still there, which is why UnwatchEdge (defined
// purely on the overlay row, S3.9) still succeeds.
func TestEdgeInvalidatedEdgeVanishesFromWatchReads(t *testing.T) {
	s := edgeStore(t)
	dead := edgeLiveEdge(t, s, "artifacts are re-read instead of cached")
	alive := edgeLiveEdge(t, s, "zeus narrates before it plans")
	for _, id := range []int64{dead, alive} {
		if err := s.WatchEdge(id, WatchItem{Class: "waste", LastSeen: 500}); err != nil {
			t.Fatalf("WatchEdge(%d): %v", id, err)
		}
	}
	if err := s.InvalidateEdge(dead, 1785448586573); err != nil {
		t.Fatalf("InvalidateEdge: %v", err)
	}

	list, err := s.ListWatchItems("") // S3.1
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(list) != 1 || list[0].EdgeID != alive {
		t.Fatalf("list = %+v, want only the live edge %d — an overlay on an invalidated edge is not listed", list, alive)
	}
	if _, err := s.GetWatchItem(dead); !errors.Is(err, ErrNotFound) { // S3.2
		t.Errorf("GetWatchItem(dead) err = %v, want ErrNotFound", err)
	}
	if _, err := s.FindWatchItem("artifacts are re-read instead of cached"); !errors.Is(err, ErrNotFound) { // S3.3
		t.Errorf("FindWatchItem(dead fact) err = %v, want ErrNotFound", err)
	}
	if e, err := s.FindLiveEdgeByFact("artifacts are re-read instead of cached"); err != nil || e != nil { // S3.4
		t.Errorf("FindLiveEdgeByFact(dead fact) = %+v, %v; want (nil, nil) — the edge is not live", e, err)
	}
	// The overlay row survives the invalidation, so the verb defined on that
	// row alone still has something to delete (S3.9).
	if edgeWatchRowCount(t, s, dead) != 1 {
		t.Fatal("invalidating the edge deleted the overlay row; S3.9 says only UnwatchEdge does that")
	}
	if err := s.UnwatchEdge(dead); err != nil {
		t.Errorf("UnwatchEdge on an invalidated edge's overlay = %v, want success (S3.9 keys on the overlay row)", err)
	}
}

// TestEdgeUnwatchDeletesOverlayOnlyAndRefusesRepeat pins S3.9's two halves:
// the edge and its episode survive untouched, and a repeat delete is
// ErrNotFound rather than a silent success.
func TestEdgeUnwatchDeletesOverlayOnlyAndRefusesRepeat(t *testing.T) {
	s := edgeStore(t)
	epID, _, err := s.AddEpisode(Episode{Kind: "watchlist", Ref: "watch:api", Content: "c", Hash: "edge-h1", TS: 42})
	if err != nil {
		t.Fatalf("AddEpisode: %v", err)
	}
	id := edgeLiveEdge(t, s, "the dream pass re-reads yesterday's transcripts")
	if _, err := s.db.Exec(`UPDATE kg_edges SET episode_id = ? WHERE id = ?`, epID, id); err != nil {
		t.Fatalf("attach episode: %v", err)
	}
	if err := s.WatchEdge(id, WatchItem{Class: "repetition"}); err != nil {
		t.Fatalf("WatchEdge: %v", err)
	}

	if err := s.UnwatchEdge(id); err != nil {
		t.Fatalf("UnwatchEdge: %v", err)
	}
	if n := edgeWatchRowCount(t, s, id); n != 0 {
		t.Fatalf("overlay rows after unwatch = %d, want 0", n)
	}
	edges, err := s.GetEdges([]int64{id})
	if err != nil || len(edges) != 1 {
		t.Fatalf("the edge must SURVIVE an unwatch: %+v %v", edges, err)
	}
	if edges[0].ExpiredAt != 0 || edges[0].EpisodeID != epID {
		t.Fatalf("unwatch touched the edge: %+v", edges[0])
	}
	ep, err := s.GetEpisode(epID)
	if err != nil || ep == nil {
		t.Fatalf("the episode must SURVIVE an unwatch: %+v %v", ep, err)
	}
	if err := s.UnwatchEdge(id); !errors.Is(err, ErrNotFound) { // S3.9, "including repeat delete"
		t.Fatalf("repeat UnwatchEdge err = %v, want ErrNotFound", err)
	}
}

// TestEdgeWatchEdgeRefusals covers S3.5's four refusal paths: an id with no
// edge, an id whose edge is invalidated (not LIVE), a second watch of an
// already-watched edge, and a populated-but-invalid state. Each must leave
// the overlay table exactly as it found it.
func TestEdgeWatchEdgeRefusals(t *testing.T) {
	s := edgeStore(t)

	if err := s.WatchEdge(999999, WatchItem{Class: "waste"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("WatchEdge(no such edge) err = %v, want ErrNotFound", err)
	}

	expired := edgeLiveEdge(t, s, "kraken polls the same log twice")
	if err := s.InvalidateEdge(expired, 1700000000000); err != nil {
		t.Fatalf("InvalidateEdge: %v", err)
	}
	if err := s.WatchEdge(expired, WatchItem{Class: "waste"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("WatchEdge(invalidated edge) err = %v, want ErrNotFound — the overlay rides a LIVE edge", err)
	}
	if n := edgeWatchRowCount(t, s, expired); n != 0 {
		t.Errorf("a refused WatchEdge still wrote %d overlay row(s)", n)
	}

	bad := edgeLiveEdge(t, s, "zeus asks a question it already answered")
	if err := s.WatchEdge(bad, WatchItem{Class: "prompting", State: "maybe"}); err == nil {
		t.Error("WatchEdge accepted state \"maybe\"; S3.5 validates a non-empty state")
	}
	if n := edgeWatchRowCount(t, s, bad); n != 0 {
		t.Errorf("a state-refused WatchEdge still wrote %d overlay row(s)", n)
	}

	dup := edgeLiveEdge(t, s, "the board reorders itself on every poll")
	if err := s.WatchEdge(dup, WatchItem{Class: "waste", Sightings: 4, Evidence: "first"}); err != nil {
		t.Fatalf("first WatchEdge: %v", err)
	}
	if err := s.WatchEdge(dup, WatchItem{Class: "feedback", Sightings: 99, Evidence: "second"}); err == nil {
		t.Error("WatchEdge on an already-watched edge succeeded; S3.5 requires an error, not a silent overwrite")
	}
	got, err := s.GetWatchItem(dup)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Class != "waste" || got.Sightings != 4 || got.Evidence != "first" {
		t.Fatalf("the refused second watch overwrote the first: %+v", got)
	}
}

// TestEdgeWatchEdgeZeroValueDefaults pins S3.5's stamping rules on the
// degenerate input — a wholly zero WatchItem — plus the carl trio's absence:
// "a fresh overlay starts unjudged", even when the caller supplies one.
func TestEdgeWatchEdgeZeroValueDefaults(t *testing.T) {
	s := edgeStore(t)
	id := edgeLiveEdge(t, s, "artifact prose is rewrapped by hand each time")

	before := time.Now().UnixMilli()
	if err := s.WatchEdge(id, WatchItem{Verdict: "fix", Comment: "carl said so", CommentedAt: "2026-08-01T00:00:00Z"}); err != nil {
		t.Fatalf("WatchEdge: %v", err)
	}
	after := time.Now().UnixMilli()

	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Sightings != 1 {
		t.Errorf("sightings = %d, want the default 1", got.Sightings)
	}
	if got.State != "watching" {
		t.Errorf("state = %q, want the default \"watching\"", got.State)
	}
	if got.LastSeen < before || got.LastSeen > after {
		t.Errorf("last_seen = %d, want now in [%d,%d]", got.LastSeen, before, after)
	}
	if got.FirstSeen != got.LastSeen {
		t.Errorf("first_seen = %d, last_seen = %d; a zero first_seen defaults to last_seen", got.FirstSeen, got.LastSeen)
	}
	if got.UpdatedAt < before || got.UpdatedAt > after {
		t.Errorf("updated_at = %d, want now in [%d,%d]", got.UpdatedAt, before, after)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("WatchEdge wrote the carl trio: %+v — S3.5 says a fresh overlay starts unjudged", got)
	}
}

// TestEdgeListWatchItemsStateFilterBoundaries pins S3.1's empty-vs-invalid
// distinction: "" is every row (not a refusal), an unrecognized state is an
// error (not an empty list), and a valid state that matches nothing is an
// empty list (not an error). The tie in the ordering key is checked too —
// last_seen DESC, edge_id DESC.
func TestEdgeListWatchItemsStateFilterBoundaries(t *testing.T) {
	s := edgeStore(t)
	a := edgeLiveEdge(t, s, "one")
	b := edgeLiveEdge(t, s, "two")
	if err := s.WatchEdge(a, WatchItem{Class: "waste", State: "watching", LastSeen: 700}); err != nil {
		t.Fatal(err)
	}
	if err := s.WatchEdge(b, WatchItem{Class: "waste", State: "watching", LastSeen: 700}); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListWatchItems("")
	if err != nil || len(all) != 2 {
		t.Fatalf("ListWatchItems(\"\") = %+v, %v; want both rows and no error", all, err)
	}
	// Same last_seen: the second key breaks the tie, greatest edge id first.
	if all[0].EdgeID != b || all[1].EdgeID != a {
		t.Errorf("tie order = [%d %d], want [%d %d] (last_seen DESC, edge_id DESC)", all[0].EdgeID, all[1].EdgeID, b, a)
	}

	empty, err := s.ListWatchItems("declined")
	if err != nil {
		t.Errorf("ListWatchItems(\"declined\") errored on a valid-but-unmatched state: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("ListWatchItems(\"declined\") = %+v, want empty", empty)
	}

	for _, bad := range []string{"pending", "Watching", "  "} {
		got, err := s.ListWatchItems(bad)
		if err == nil {
			t.Errorf("ListWatchItems(%q) = %+v, want an error naming the enum", bad, got)
		}
		if len(got) != 0 {
			t.Errorf("ListWatchItems(%q) returned %d rows alongside its refusal", bad, len(got))
		}
	}
}

// TestEdgeUpdateWatchItemBoundaries pins S3.6's refusals and its degenerate
// input: an entirely zero WatchItem changes nothing but still stamps
// updated_at, and a populated-but-invalid state is refused without writing.
func TestEdgeUpdateWatchItemBoundaries(t *testing.T) {
	s := edgeStore(t)
	id := edgeLiveEdge(t, s, "the same lint command is rediscovered every cell")
	if err := s.WatchEdge(id, WatchItem{
		Class: "repetition", State: "watching", Sightings: 6,
		FirstSeen: 1000, LastSeen: 2000, Evidence: "e", SuggestedFix: "f", PRURL: "u",
	}); err != nil {
		t.Fatalf("WatchEdge: %v", err)
	}
	if err := s.SetWatchVerdict(id, "fix", "agreed", "2026-07-31T12:00:00Z"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}

	// Invalid state: refused, and nothing else in the row moves.
	if err := s.UpdateWatchItem(id, WatchItem{State: "maybe", SuggestedFix: "rewritten"}); err == nil {
		t.Error("UpdateWatchItem accepted state \"maybe\"; S3.6 validates a populated state")
	}
	if got, _ := s.GetWatchItem(id); got.SuggestedFix != "f" {
		t.Fatalf("a state-refused update still wrote suggested_fix = %q", got.SuggestedFix)
	}

	// Wholly zero item: every field left alone, updated_at stamped now.
	before := time.Now().UnixMilli()
	if err := s.UpdateWatchItem(id, WatchItem{}); err != nil {
		t.Fatalf("UpdateWatchItem(zero): %v", err)
	}
	after := time.Now().UnixMilli()
	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Class != "repetition" || got.State != "watching" || got.Sightings != 6 ||
		got.FirstSeen != 1000 || got.LastSeen != 2000 ||
		got.Evidence != "e" || got.SuggestedFix != "f" || got.PRURL != "u" {
		t.Fatalf("a zero-valued update clobbered fields: %+v", got)
	}
	if got.UpdatedAt < before || got.UpdatedAt > after {
		t.Errorf("updated_at = %d, want now in [%d,%d] — S3.6 always stamps it", got.UpdatedAt, before, after)
	}
	// The carl trio is out of this verb's reach even when handed one.
	if err := s.UpdateWatchItem(id, WatchItem{Verdict: "fine", Comment: "no", CommentedAt: "2000-01-01T00:00:00Z"}); err != nil {
		t.Fatalf("UpdateWatchItem(carl fields): %v", err)
	}
	got, _ = s.GetWatchItem(id)
	if got.Verdict != "fix" || got.Comment != "agreed" || got.CommentedAt != "2026-07-31T12:00:00Z" {
		t.Fatalf("UpdateWatchItem touched the carl trio: %+v", got)
	}
}

// TestEdgeBumpWatchSightingBoundaries pins S3.7's degenerate inputs: a zero
// lastSeen means now (for BOTH last_seen and updated_at), an empty evidence
// string replaces wholesale rather than being treated as "leave alone", and
// first_seen plus the carl trio are untouched.
func TestEdgeBumpWatchSightingBoundaries(t *testing.T) {
	s := edgeStore(t)
	id := edgeLiveEdge(t, s, "the daemon logs the same tick twice")
	if err := s.WatchEdge(id, WatchItem{
		Class: "waste", Sightings: 3, FirstSeen: 1000, LastSeen: 2000, Evidence: "old evidence",
	}); err != nil {
		t.Fatalf("WatchEdge: %v", err)
	}
	if err := s.SetWatchVerdict(id, "fine", "leave it", "2026-07-01T00:00:00Z"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}

	before := time.Now().UnixMilli()
	if err := s.BumpWatchSighting(id, 0, ""); err != nil {
		t.Fatalf("BumpWatchSighting: %v", err)
	}
	after := time.Now().UnixMilli()

	got, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Sightings != 4 {
		t.Errorf("sightings = %d, want 4 (SQL-side +1 from 3)", got.Sightings)
	}
	if got.LastSeen < before || got.LastSeen > after {
		t.Errorf("last_seen = %d, want now in [%d,%d] when lastSeen is 0", got.LastSeen, before, after)
	}
	if got.UpdatedAt != got.LastSeen {
		t.Errorf("updated_at = %d, last_seen = %d; S3.7 sets both to the same value", got.UpdatedAt, got.LastSeen)
	}
	if got.Evidence != "" {
		t.Errorf("evidence = %q, want \"\" — S3.7 replaces evidence wholesale", got.Evidence)
	}
	if got.FirstSeen != 1000 {
		t.Errorf("first_seen = %d, want 1000 untouched", got.FirstSeen)
	}
	if got.Verdict != "fine" || got.Comment != "leave it" || got.CommentedAt != "2026-07-01T00:00:00Z" {
		t.Fatalf("a sighting bump disturbed the carl trio: %+v", got)
	}

	// Two bumps are +2, not "set to 1" — the increment is SQL-side.
	if err := s.BumpWatchSighting(id, 9000, "fresh"); err != nil {
		t.Fatalf("second BumpWatchSighting: %v", err)
	}
	got, _ = s.GetWatchItem(id)
	if got.Sightings != 5 || got.LastSeen != 9000 || got.UpdatedAt != 9000 || got.Evidence != "fresh" {
		t.Fatalf("second bump = %+v, want sightings 5 / last_seen 9000 / evidence \"fresh\"", got)
	}
}

// TestEdgeSetWatchVerdictBoundaries pins S3.8's refusal and its two
// degenerate inputs: an out-of-enum verdict is refused without writing, and
// empty values CLEAR (the documented undo) rather than being ignored. The
// verb also leaves updated_at alone.
func TestEdgeSetWatchVerdictBoundaries(t *testing.T) {
	s := edgeStore(t)
	id := edgeLiveEdge(t, s, "the spawn path reads the job spec twice")
	if err := s.WatchEdge(id, WatchItem{Class: "waste", UpdatedAt: 4242}); err != nil {
		t.Fatalf("WatchEdge: %v", err)
	}

	if err := s.SetWatchVerdict(id, "maybe", "", ""); err == nil {
		t.Error("SetWatchVerdict accepted verdict \"maybe\"; S3.8 validates against \"\"|fine|fix")
	}
	if got, _ := s.GetWatchItem(id); got.Verdict != "" {
		t.Fatalf("a refused verdict still landed: %+v", got)
	}

	if err := s.SetWatchVerdict(id, "fix", "do it", "2026-07-30T09:00:00Z"); err != nil {
		t.Fatalf("SetWatchVerdict(fix): %v", err)
	}
	got, _ := s.GetWatchItem(id)
	if got.UpdatedAt != 4242 {
		t.Errorf("updated_at = %d, want 4242 — S3.8 does not bump it", got.UpdatedAt)
	}

	// The documented undo: all three empty writes all three empty.
	if err := s.SetWatchVerdict(id, "", "", ""); err != nil {
		t.Fatalf("SetWatchVerdict(clear): %v", err)
	}
	got, _ = s.GetWatchItem(id)
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("empty values did not clear: %+v", got)
	}
	if got.UpdatedAt != 4242 {
		t.Errorf("updated_at = %d after the clear, want 4242", got.UpdatedAt)
	}
}

// TestEdgeFindWatchItemNormalizationAndMisses pins S3.3's normalization edges
// (leading/trailing whitespace, internal runs, newlines, tabs, case folding),
// its winner rule (greatest last_seen, tie greatest edge_id — the OPPOSITE of
// S3.4's), its watched-only scope, and its ErrNotFound miss shape.
func TestEdgeFindWatchItemNormalizationAndMisses(t *testing.T) {
	s := edgeStore(t)

	edgeLiveEdge(t, s, "a live edge nobody is watching")
	if _, err := s.FindWatchItem("a live edge nobody is watching"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindWatchItem over an UNWATCHED live edge err = %v, want ErrNotFound — S3.3 scopes to watched edges", err)
	}
	if _, err := s.FindWatchItem("nothing has ever said this"); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindWatchItem(miss) err = %v, want ErrNotFound", err)
	}
	if _, err := s.FindWatchItem("   \t\n  "); !errors.Is(err, ErrNotFound) {
		t.Errorf("FindWatchItem(whitespace-only) err = %v, want ErrNotFound", err)
	}

	// Three facts that normalize to the same key, differing only in case and
	// internal whitespace.
	older := edgeLiveEdge(t, s, "Zeus  re-reads   the same file")
	newer := edgeLiveEdge(t, s, "zeus re-reads the same file")
	tieLo := edgeLiveEdge(t, s, "ZEUS RE-READS THE SAME FILE")
	if err := s.WatchEdge(older, WatchItem{Class: "waste", LastSeen: 100}); err != nil {
		t.Fatal(err)
	}
	if err := s.WatchEdge(newer, WatchItem{Class: "waste", LastSeen: 900}); err != nil {
		t.Fatal(err)
	}
	if err := s.WatchEdge(tieLo, WatchItem{Class: "waste", LastSeen: 900}); err != nil {
		t.Fatal(err)
	}

	// tieLo has the same last_seen as newer and a GREATER edge id, so it wins
	// the tie (S3.3: greatest last_seen, tie greatest edge_id).
	for _, q := range []string{
		"zeus re-reads the same file",
		"  zeus re-reads the same file  ",
		"ZEUS\tre-reads\n\n the   same    file",
		"\nZeus re-reads the same file\t",
	} {
		got, err := s.FindWatchItem(q)
		if err != nil {
			t.Fatalf("FindWatchItem(%q): %v", q, err)
		}
		if got.EdgeID != tieLo {
			t.Errorf("FindWatchItem(%q) = edge %d, want %d (greatest last_seen, tie greatest edge id)", q, got.EdgeID, tieLo)
		}
	}
	// Sanity: the query really did need normalizing to match at all.
	if edgeOneLine("ZEUS\tre-reads\n\n the   same    file") != "ZEUS re-reads the same file" {
		t.Fatal("the fixture's oneLine expectation is wrong")
	}
}

// TestEdgeFindLiveEdgeByFactMissShapeAndWinner pins the ways S3.4 differs
// from S3.3 — it is the "create vs promote" probe, so its miss is (nil, nil)
// not ErrNotFound, it sees UNWATCHED edges, and among several matches the
// LOWEST edge id wins.
func TestEdgeFindLiveEdgeByFactMissShapeAndWinner(t *testing.T) {
	s := edgeStore(t)

	got, err := s.FindLiveEdgeByFact("no edge says this")
	if err != nil || got != nil {
		t.Fatalf("FindLiveEdgeByFact(miss) = %+v, %v; want (nil, nil) — S3.4's miss is not an error", got, err)
	}
	if got, err := s.FindLiveEdgeByFact("   "); err != nil || got != nil {
		t.Fatalf("FindLiveEdgeByFact(blank) = %+v, %v; want (nil, nil)", got, err)
	}

	lowest := edgeLiveEdge(t, s, "Artifacts Wrap At 95 Columns")
	middle := edgeLiveEdge(t, s, "artifacts wrap at   95 columns")
	highest := edgeLiveEdge(t, s, "ARTIFACTS WRAP AT 95 COLUMNS")
	// Only the highest is watched — S3.4 must still prefer the lowest id.
	if err := s.WatchEdge(highest, WatchItem{Class: "correction", LastSeen: 9999}); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{
		"artifacts wrap at 95 columns",
		"  ARTIFACTS\twrap\nat  95   columns ",
	} {
		e, err := s.FindLiveEdgeByFact(q)
		if err != nil {
			t.Fatalf("FindLiveEdgeByFact(%q): %v", q, err)
		}
		if e == nil {
			t.Fatalf("FindLiveEdgeByFact(%q) = nil, want the lowest-id match %d", q, lowest)
		}
		if e.ID != lowest {
			t.Errorf("FindLiveEdgeByFact(%q) = edge %d, want %d (lowest id wins)", q, e.ID, lowest)
		}
	}

	// Invalidate every candidate: a probe over a dead fact must report "no
	// live edge" and NOT resurrect one.
	for _, id := range []int64{lowest, middle, highest} {
		if err := s.InvalidateEdge(id, 1); err != nil {
			t.Fatalf("InvalidateEdge(%d): %v", id, err)
		}
	}
	if e, err := s.FindLiveEdgeByFact("artifacts wrap at 95 columns"); err != nil || e != nil {
		t.Fatalf("FindLiveEdgeByFact over invalidated edges = %+v, %v; want (nil, nil)", e, err)
	}
}

// --- S2: migration edges ---------------------------------------------------

// TestEdgeMigrateV16SkipsEmptyObservation pins S2.2's only skip case: a flat
// row whose observation is empty AFTER oneLine normalization is skipped
// ENTIRELY — no episode, no fallback edge, no overlay — while its neighbours
// migrate normally.
func TestEdgeMigrateV16SkipsEmptyObservation(t *testing.T) {
	s := edgeV15Store(t)
	blank := edgeInsertFlat(t, s, edgeFlatRow{
		Observation: " \t\n  ", Class: "waste", State: "watching", Sightings: 2, FirstSeen: 1, LastSeen: 2,
	})
	real := edgeInsertFlat(t, s, edgeFlatRow{
		Observation: "zeus re-reads the same file", Class: "waste", State: "watching",
		Sightings: 1, FirstSeen: 10, LastSeen: 20,
	})
	edgeMigrateTo16(t, s)

	var episodes int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_episodes WHERE kind = 'watchlist'`).Scan(&episodes); err != nil {
		t.Fatal(err)
	}
	if episodes != 1 {
		t.Errorf("watchlist episodes = %d, want 1 — the blank row (%d) must mint nothing", episodes, blank)
	}
	var edges, overlays int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_edges`).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_watch`).Scan(&overlays); err != nil {
		t.Fatal(err)
	}
	if edges != 1 || overlays != 1 {
		t.Fatalf("edges = %d, overlays = %d; want exactly one of each (row %d migrated, row %d skipped)", edges, overlays, real, blank)
	}
	var ref string
	if err := s.db.QueryRow(`SELECT ref FROM kg_episodes WHERE kind = 'watchlist'`).Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if ref != fmt.Sprintf("watchlist:%d", real) {
		t.Errorf("episode ref = %q, want the surviving row's ref", ref)
	}
}

// TestEdgeMigrateV16FoldsCaseWhitespaceCollision is the collision pair the
// contract's test-file section calls for: two flat rows whose observations
// differ only in case and internal whitespace land on ONE pre-seeded live
// edge (S2.4) and fold per S2.5 — greater last_seen wins every field, except
// sightings which sums and first_seen which takes the min. It also pins the
// episode_id backfill's guard: an edge that already HAS an episode keeps it.
func TestEdgeMigrateV16FoldsCaseWhitespaceCollision(t *testing.T) {
	s := edgeV15Store(t)
	// A pre-existing episode on the edge — S2.4 only backfills when episode_id
	// is 0, so this one must survive.
	res, err := s.db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('transcript', 'pre', 'pre', 'edge-pre-hash', 5, 'done', 5)`)
	if err != nil {
		t.Fatal(err)
	}
	preEp, _ := res.LastInsertId()
	src := edgeSeedNode(t, s, "dream pass", "component", "seeded")
	dst := edgeSeedNode(t, s, "kraken", "component", "seeded")
	edgeID := edgeSeedEdge(t, s, src, dst, "zeus re-reads the same file", preEp)

	edgeInsertFlat(t, s, edgeFlatRow{
		Observation: "Zeus   re-reads\tthe same  file", Class: "waste", State: "watching",
		Sightings: 2, FirstSeen: 10, LastSeen: 100, Evidence: "old", SuggestedFix: "old fix",
		PRURL: "old-url", UpdatedAt: 111, Verdict: "fine", Comment: "old comment", CommentedAt: "2026-01-01T00:00:00Z",
	})
	edgeInsertFlat(t, s, edgeFlatRow{
		Observation: "zeus re-reads the same file", Class: "repetition", State: "shipped",
		Sightings: 3, FirstSeen: 50, LastSeen: 200, Evidence: "new", SuggestedFix: "new fix",
		PRURL: "new-url", UpdatedAt: 222, Verdict: "fix", Comment: "new comment", CommentedAt: "2026-02-02T00:00:00Z",
	})
	edgeMigrateTo16(t, s)

	var edges int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_edges`).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if edges != 1 {
		t.Fatalf("edges = %d, want 1 — a case/whitespace collision pair lands on ONE edge (S2.4)", edges)
	}
	var overlays int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_watch`).Scan(&overlays); err != nil {
		t.Fatal(err)
	}
	if overlays != 1 {
		t.Fatalf("overlay rows = %d, want 1 — kg_watch is keyed by edge_id, so the pair must FOLD (S2.5)", overlays)
	}

	got, err := s.GetWatchItem(edgeID)
	if err != nil {
		t.Fatalf("GetWatchItem(%d): %v", edgeID, err)
	}
	if got.Sightings != 5 {
		t.Errorf("sightings = %d, want 5 (2+3, summed)", got.Sightings)
	}
	if got.FirstSeen != 10 {
		t.Errorf("first_seen = %d, want 10 (min of 10 and 50)", got.FirstSeen)
	}
	if got.LastSeen != 200 || got.Class != "repetition" || got.State != "shipped" ||
		got.Evidence != "new" || got.SuggestedFix != "new fix" || got.PRURL != "new-url" || got.UpdatedAt != 222 {
		t.Errorf("greater-last_seen row did not win every other field: %+v", got)
	}
	if got.Verdict != "fix" || got.Comment != "new comment" || got.CommentedAt != "2026-02-02T00:00:00Z" {
		t.Errorf("carl trio = %q/%q/%q, want the greater-last_seen row's, verbatim (S2.5)", got.Verdict, got.Comment, got.CommentedAt)
	}

	var episodeID int64
	if err := s.db.QueryRow(`SELECT episode_id FROM kg_edges WHERE id = ?`, edgeID).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	if episodeID != preEp {
		t.Errorf("episode_id = %d, want the pre-existing %d — S2.4 backfills ONLY when it is 0", episodeID, preEp)
	}
}

// TestEdgeMigrateV16LowestEdgeIDWinsAmongLiveMatches pins S2.4's tiebreak:
// when several LIVE edges match one observation, the lowest id is the watch
// edge and the others gain no overlay. Invalidated matches are not candidates
// at all.
func TestEdgeMigrateV16LowestEdgeIDWinsAmongLiveMatches(t *testing.T) {
	s := edgeV15Store(t)
	src := edgeSeedNode(t, s, "dream pass", "component", "")
	dst := edgeSeedNode(t, s, "kraken", "component", "")
	// An invalidated edge inserted FIRST, so a naive "lowest id" that forgot
	// the expired_at filter would pick it.
	deadID := edgeSeedEdge(t, s, src, dst, "artifacts are rewrapped by hand", 0)
	if _, err := s.db.Exec(`UPDATE kg_edges SET expired_at = 7, valid_until = 7 WHERE id = ?`, deadID); err != nil {
		t.Fatal(err)
	}
	lowID := edgeSeedEdge(t, s, src, dst, "Artifacts Are Rewrapped By Hand", 0)
	highID := edgeSeedEdge(t, s, src, dst, "artifacts are   rewrapped by hand", 0)

	edgeInsertFlat(t, s, edgeFlatRow{
		Observation: "artifacts are rewrapped by hand", Class: "correction", State: "watching",
		Sightings: 4, FirstSeen: 11, LastSeen: 22,
	})
	edgeMigrateTo16(t, s)

	if n := edgeWatchRowCount(t, s, lowID); n != 1 {
		t.Errorf("overlay rows on the lowest live match (%d) = %d, want 1", lowID, n)
	}
	for _, id := range []int64{deadID, highID} {
		if n := edgeWatchRowCount(t, s, id); n != 0 {
			t.Errorf("edge %d gained %d overlay row(s); only the lowest live match wins (S2.4)", id, n)
		}
	}
	var edges int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_edges`).Scan(&edges); err != nil {
		t.Fatal(err)
	}
	if edges != 3 {
		t.Errorf("edges = %d, want the 3 seeded — a match must not also mint a fallback edge", edges)
	}
}

// TestEdgeMigrateV16ReusesExistingEpisodeByHash pins S2.3's find-or-create:
// an existing kg_episodes row carrying the hash the migration would compute
// is REUSED, not duplicated — and S2.6 then flips it to done.
func TestEdgeMigrateV16ReusesExistingEpisodeByHash(t *testing.T) {
	s := edgeV15Store(t)
	row := edgeFlatRow{
		// Already one-line, so S2.3's "<observation>" is unambiguous here
		// whichever form the renderer uses.
		Observation: "zeus re-reads the same file", Class: "waste", State: "watching",
		Sightings: 3, FirstSeen: 10, LastSeen: 4242, SuggestedFix: "cache it",
		Verdict: "fix", Comment: "yes, do this",
	}
	flatID := edgeInsertFlat(t, s, row)
	ref := fmt.Sprintf("watchlist:%d", flatID)
	content := edgeEpisodeContent(row, row.Observation)
	hash := edgeEpisodeHash("watchlist", ref, content)

	res, err := s.db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('watchlist', ?, ?, ?, ?, 'pending', 1)`, ref, content, hash, row.LastSeen)
	if err != nil {
		t.Fatalf("pre-seed episode: %v", err)
	}
	preID, _ := res.LastInsertId()

	edgeMigrateTo16(t, s)

	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_episodes WHERE hash = ?`, hash).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("episodes with the computed hash = %d, want 1 — find-or-create is by hash (S2.3)", n)
	}
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_episodes WHERE kind = 'watchlist'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("watchlist episodes = %d, want 1 — the pre-seeded row must be reused, not duplicated", total)
	}
	ep, err := s.GetEpisode(preID)
	if err != nil || ep == nil {
		t.Fatalf("the pre-seeded episode vanished: %+v %v", ep, err)
	}
	if ep.State != "done" {
		t.Errorf("episode state = %q, want \"done\" (S2.6)", ep.State)
	}
	// The fallback edge (no live edge matched) points at the reused episode.
	var episodeID int64
	if err := s.db.QueryRow(`SELECT episode_id FROM kg_edges`).Scan(&episodeID); err != nil {
		t.Fatal(err)
	}
	if episodeID != preID {
		t.Errorf("edge episode_id = %d, want the reused episode %d", episodeID, preID)
	}
	// The carl trio migrates verbatim alongside it (S2.5).
	item, err := s.FindWatchItem(row.Observation)
	if err != nil {
		t.Fatalf("FindWatchItem after migration: %v", err)
	}
	if item.Verdict != "fix" || item.Comment != "yes, do this" {
		t.Errorf("carl trio = %q/%q, want fix/\"yes, do this\" verbatim", item.Verdict, item.Comment)
	}
}

// TestEdgeMigrateV16MarksEveryWatchlistEpisodeDone pins S2.6 on the rows the
// loop never touched: a PENDING watchlist episode with no relation to any
// flat row is still flipped to done, while a pending episode of another kind
// is left alone.
func TestEdgeMigrateV16MarksEveryWatchlistEpisodeDone(t *testing.T) {
	s := edgeV15Store(t)
	if _, err := s.db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('watchlist', 'watchlist:9999', 'orphan', 'edge-orphan-hash', 1, 'pending', 1),
		       ('feedback',  'feedback:1',     'other',  'edge-other-hash',  1, 'pending', 1)`); err != nil {
		t.Fatalf("pre-seed episodes: %v", err)
	}
	edgeMigrateTo16(t, s)

	var watchState, feedbackState string
	if err := s.db.QueryRow(`SELECT state FROM kg_episodes WHERE hash = 'edge-orphan-hash'`).Scan(&watchState); err != nil {
		t.Fatal(err)
	}
	if watchState != "done" {
		t.Errorf("pending watchlist episode state = %q, want \"done\" (S2.6)", watchState)
	}
	if err := s.db.QueryRow(`SELECT state FROM kg_episodes WHERE hash = 'edge-other-hash'`).Scan(&feedbackState); err != nil {
		t.Fatal(err)
	}
	if feedbackState != "pending" {
		t.Errorf("pending FEEDBACK episode state = %q, want \"pending\" — S2.6 scopes to kind 'watchlist'", feedbackState)
	}
}

// TestEdgeMigrateV16FallbackEdgeReusesEndpointNodes pins S2.4's no-match
// branch at its riskiest point: `kraken` resolves case-insensitively against
// an existing node, so the migration must never mint a second one and must
// never overwrite the existing gloss ("description only written if the node
// is created new").
func TestEdgeMigrateV16FallbackEdgeReusesEndpointNodes(t *testing.T) {
	s := edgeV15Store(t)
	edgeSeedNode(t, s, "Kraken", "system", "the gloss carl already wrote")

	row := edgeFlatRow{
		Observation: "  the same lint command is   rediscovered every cell  ",
		Class:       "repetition", State: "watching", Sightings: 6, FirstSeen: 1000, LastSeen: 2000,
	}
	edgeInsertFlat(t, s, row)
	edgeMigrateTo16(t, s)

	var krakens int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_nodes WHERE LOWER(name) = 'kraken'`).Scan(&krakens); err != nil {
		t.Fatal(err)
	}
	if krakens != 1 {
		t.Fatalf("nodes named kraken = %d, want 1 — S2.4 resolves case-insensitively before creating", krakens)
	}
	var name, typ, desc string
	if err := s.db.QueryRow(`SELECT name, type, description FROM kg_nodes WHERE LOWER(name) = 'kraken'`).
		Scan(&name, &typ, &desc); err != nil {
		t.Fatal(err)
	}
	if desc != "the gloss carl already wrote" || typ != "system" || name != "Kraken" {
		t.Errorf("the reused kraken node was rewritten: name=%q type=%q desc=%q", name, typ, desc)
	}

	var (
		predicate string
		fact      string
		strength  int64
		occ       int64
		validFrom int64
		expiredAt int64
		episodeID int64
	)
	if err := s.db.QueryRow(`SELECT predicate, fact, strength, occurrences, valid_from, expired_at, episode_id
		FROM kg_edges`).Scan(&predicate, &fact, &strength, &occ, &validFrom, &expiredAt, &episodeID); err != nil {
		t.Fatalf("read the fallback edge: %v", err)
	}
	if predicate != "observed" {
		t.Errorf("predicate = %q, want \"observed\"", predicate)
	}
	if want := edgeOneLine(row.Observation); fact != want {
		t.Errorf("fact = %q, want the oneLine'd observation %q (S2.2/S2.4)", fact, want)
	}
	// occurrences is 1, NOT the row's 6 sightings (S2.4, amended in review
	// round 1): occurrences counts distinct EPISODES independently stating the
	// fact, and a flat row is one episode however many times the dream loop
	// sighted it. Seeding it from sightings would hand every migrated fact an
	// unearned rerank advantage in NeighborEdges' `ORDER BY occurrences DESC`.
	if strength != 5 || occ != 1 || validFrom != 1000 || expiredAt != 0 {
		t.Errorf("fallback edge = strength %d, occurrences %d, valid_from %d, expired_at %d; want 5 / 1 (NOT the 6 sightings) / 1000 (first_seen) / 0",
			strength, occ, validFrom, expiredAt)
	}
	// ...and the recurrence count the row DID carry lives on the overlay
	// instead, which is the whole point of keeping the two counters apart
	// (S3.10).
	item, err := s.FindWatchItem(row.Observation)
	if err != nil {
		t.Fatalf("FindWatchItem after migration: %v", err)
	}
	if item.Sightings != 6 {
		t.Errorf("overlay sightings = %d, want the row's 6 — the recurrence count moves to kg_watch, not to the edge", item.Sightings)
	}
	if episodeID == 0 {
		t.Error("fallback edge episode_id = 0, want the S2.3 episode")
	}
	// The fact reached FTS through the existing kg_edges triggers.
	ids, err := s.SearchEdgesFTS("rediscovered", 10)
	if err != nil {
		t.Fatalf("SearchEdgesFTS: %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("FTS hits = %v, want the migrated fact to be searchable", ids)
	}
}

// TestEdgeMigrateV16DropsFlatTableAndCursor pins S2.7: the flat table is gone
// and ONLY the rows:watchlist ingest cursor is deleted.
func TestEdgeMigrateV16DropsFlatTableAndCursor(t *testing.T) {
	s := edgeV15Store(t)
	if _, err := s.db.Exec(`INSERT INTO kg_ingest_cursor (source, cursor, updated_at)
		VALUES ('rows:watchlist', 42, 1), ('rows:feedback', 7, 1)`); err != nil {
		t.Fatalf("pre-seed cursors: %v", err)
	}
	edgeInsertFlat(t, s, edgeFlatRow{Observation: "something worth keeping", Class: "waste", State: "watching", Sightings: 1})
	edgeMigrateTo16(t, s)

	var name string
	err := s.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='watchlist'`).Scan(&name)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("the flat watchlist table survived v16: name=%q err=%v", name, err)
	}
	cursors, err := s.AllIngestCursors()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cursors["rows:watchlist"]; ok {
		t.Error("the rows:watchlist ingest cursor survived v16 (S2.7)")
	}
	if cursors["rows:feedback"] != 7 {
		t.Errorf("cursors = %v, want the unrelated rows:feedback cursor untouched", cursors)
	}
}

// --- fixture helpers used by the migration tests --------------------------

func edgeSeedNode(t *testing.T, s *Store, name, typ, desc string) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO kg_nodes (name, type, description, embedding, created_at, updated_at)
		VALUES (?, ?, ?, x'', 1, 1)`, name, typ, desc)
	if err != nil {
		t.Fatalf("seed node %q: %v", name, err)
	}
	id, _ := res.LastInsertId()
	return id
}

func edgeSeedEdge(t *testing.T, s *Store, src, dst int64, fact string, episodeID int64) int64 {
	t.Helper()
	res, err := s.db.Exec(`INSERT INTO kg_edges
		(src_id, dst_id, predicate, fact, episode_id, strength, occurrences, valid_from, valid_until, embedding, created_at, expired_at)
		VALUES (?, ?, 'observed', ?, ?, 5, 1, 0, 0, x'', 1, 0)`, src, dst, fact, episodeID)
	if err != nil {
		t.Fatalf("seed edge %q: %v", fact, err)
	}
	id, _ := res.LastInsertId()
	return id
}
