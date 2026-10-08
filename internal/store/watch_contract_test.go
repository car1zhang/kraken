package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

// watch_contract_test.go — the LOGIC dimension of the watchlist's v29 shape:
// the standalone watch_items table, the verbs that write it, and the migration
// that carried the v16 graph overlay into it.
//
// It replaces the v16-era file of the same name. That file was written blind
// against a design in which an observation WAS a knowledge-graph edge and this
// table was a thin overlay riding it; v29 retires that design wholesale (the
// graph is now a compilation of carl's mental model, and an agent-observed
// fact inside it would be exactly what the cutover exists to remove). Its
// assertions about promote-vs-create, episode reuse by hash and endpoint-node
// fallback are not merely restated here — they no longer have an observable
// consequence, because v29 empties the graph those assertions read. What
// survived the port is everything still observable: the migration's end state,
// the fold, the ownership split, and the ordering.
//
// Boundaries, refusals and degenerate inputs belong to watch_edge_test.go and
// are deliberately NOT duplicated here.
//
// Every package-level identifier is prefixed `contractWatch` — this package's
// test namespace is shared with a dozen sibling files.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractWatchV28 rolls a fresh store back to the v28 shape and hands it back
// ready to be seeded: kg_watch is present, kg_episodes carries its legacy
// five-kind CHECK, and none of v29's three tables exists yet. Every migration
// assertion below starts here, because the only honest fixture for "a database
// upgrading into v29" is one that actually looks like v28.
func contractWatchV28(t *testing.T) *Store {
	t.Helper()
	s := openTest(t)
	downgradeFreshDB(t, s, 28)
	return s
}

// contractWatchOverlay seeds one live edge carrying `fact` between two named
// nodes, plus the kg_watch overlay row v29 has to carry across. It returns the
// edge id so a test can expire the edge and check that the overlay is left
// behind.
func contractWatchOverlay(t *testing.T, s *Store, src, dst, pred, fact string, o WatchItem) int64 {
	t.Helper()
	// Raw SQL: the graph accessors went with v32, and this fixture runs on a
	// v28-shaped database where the kg_* tables exist again.
	node := func(name string) int64 {
		t.Helper()
		if _, err := s.db.Exec(`INSERT INTO kg_nodes (name) VALUES (?) ON CONFLICT DO NOTHING`, name); err != nil {
			t.Fatalf("seed node %q: %v", name, err)
		}
		var id int64
		if err := s.db.QueryRow(`SELECT id FROM kg_nodes WHERE LOWER(name) = LOWER(?)`, name).Scan(&id); err != nil {
			t.Fatalf("read node %q: %v", name, err)
		}
		return id
	}
	srcID, dstID := node(src), node(dst)
	res, err := s.db.Exec(`INSERT INTO kg_edges (src_id, dst_id, predicate, fact, strength) VALUES (?, ?, ?, ?, 5)`,
		srcID, dstID, pred, fact)
	if err != nil {
		t.Fatalf("seed edge %q: %v", fact, err)
	}
	edgeID, _ := res.LastInsertId()
	if _, err := s.db.Exec(`INSERT INTO kg_watch (edge_id, class, state, sightings, first_seen,
		last_seen, evidence, suggested_fix, pr_url, updated_at, verdict, comment, commented_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		edgeID, o.Class, o.State, o.Sightings, o.FirstSeen, o.LastSeen, o.Evidence,
		o.SuggestedFix, o.PRURL, o.UpdatedAt, o.Verdict, o.Comment, o.CommentedAt); err != nil {
		t.Fatalf("seed kg_watch overlay: %v", err)
	}
	return edgeID
}

// contractWatchByObservation indexes a listing so an assertion can name the row
// it means rather than depending on the order two unrelated rows came back in.
func contractWatchByObservation(t *testing.T, s *Store, state string) map[string]WatchItem {
	t.Helper()
	list, err := s.ListWatchItems(state)
	if err != nil {
		t.Fatalf("ListWatchItems(%q): %v", state, err)
	}
	out := map[string]WatchItem{}
	for _, w := range list {
		out[w.Observation] = w
	}
	return out
}

func contractWatchTableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("probe table %q: %v", name, err)
	}
	return true
}

func contractWatchCount(t *testing.T, s *Store, table string) int {
	t.Helper()
	var n int
	// The table name is this test's own constant, never input.
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// ---------------------------------------------------------------------------
// the migration
// ---------------------------------------------------------------------------

// TestContractWatchV29Lands is the shape assertion for the cutover: after a
// v28 database migrates, the three new tables exist, the overlay table is
// gone, and (since the walk continues to v32) every kg_* table is gone too.
//
// The kg_* half is the one worth having a test for: a walk that left the
// graph tables standing would pass every functional test in this package,
// since nothing else reads them any more.
func TestContractWatchV29Lands(t *testing.T) {
	s := contractWatchV28(t)

	// Seed the graph so the wipe has something to prove it removed.
	contractWatchOverlay(t, s, "kraken", "carl", "observed", "an observation", WatchItem{
		Class: "waste", State: "watching", Sightings: 3, FirstSeen: 100, LastSeen: 200,
	})
	if _, err := s.db.Exec(`INSERT INTO kg_episodes (kind, ref, content, hash, ts, state, created_at)
		VALUES ('transcript', 'x', 'y', 'hash-1', 1, 'pending', 1)`); err != nil {
		t.Fatalf("seed episode: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO kg_recall (session_id, edge_id, ts) VALUES ('sess', 1, 1)`); err != nil {
		t.Fatalf("seed recall: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO kg_layout (node_id, x, y, rev) VALUES (1, 1.0, 2.0, 3)`); err != nil {
		t.Fatalf("seed layout: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO kg_ingest_cursor (source, cursor, updated_at) VALUES ('rows:feedback', 9, 9)`); err != nil {
		t.Fatalf("seed ingest cursor: %v", err)
	}
	if _, err := s.db.Exec(`INSERT INTO kg_node_aliases (node_id, alias) VALUES (1, 'k')`); err != nil {
		t.Fatalf("seed alias: %v", err)
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate into v29: %v", err)
	}

	for _, name := range []string{"mm_items", "mm_code_anchors", "watch_items"} {
		if !contractWatchTableExists(t, s, name) {
			t.Errorf("%s missing after v29", name)
		}
	}
	if contractWatchTableExists(t, s, "kg_watch") {
		t.Errorf("kg_watch survived v29")
	}
	// migrate() walks on past v29 to v32, which drops every kg_* table — so
	// what v29 emptied is absent rather than empty on the far side.
	for _, table := range []string{"kg_edges", "kg_episodes", "kg_nodes", "kg_node_aliases",
		"kg_recall", "kg_layout", "kg_ingest_cursor", "kg_edges_fts"} {
		if contractWatchTableExists(t, s, table) {
			t.Errorf("%s still exists after the walk to v%d", table, schemaVersion)
		}
	}
}

// TestContractWatchCarriesOverlayColumnsAcross pins the carry-across itself:
// one live overlay becomes one watch_items row holding the edge's sentence and
// endpoints plus EVERY overlay column, carl's judgement trio included.
//
// The trio is the half that would be invisible if it broke. Losing sightings
// would show up as a wrong number on the board; losing a verdict looks exactly
// like an observation carl has not judged yet, so the dream loop re-proposes
// something he already ruled on and nothing anywhere reports a fault.
func TestContractWatchCarriesOverlayColumnsAcross(t *testing.T) {
	s := contractWatchV28(t)
	contractWatchOverlay(t, s, "hephaestus", "contract", "cites", "hephaestus cites the failing assertion", WatchItem{
		Class: "correction", State: "watching", Sightings: 4, FirstSeen: 1000, LastSeen: 5000,
		Evidence: `{"n":4}`, SuggestedFix: "cite the obligation", PRURL: "https://example/pr/1",
		UpdatedAt: 5001, Verdict: "fix", Comment: "yes, do this", CommentedAt: "2026-08-01T00:00:00Z",
	})

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate into v29: %v", err)
	}

	list, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want exactly one carried row, got %d: %+v", len(list), list)
	}
	got := list[0]
	if got.Observation != "hephaestus cites the failing assertion" {
		t.Errorf("observation = %q, want the edge's fact", got.Observation)
	}
	if got.Src != "hephaestus" || got.Dst != "contract" || got.Predicate != "cites" {
		t.Errorf("endpoints = (%q, %q, %q), want the edge's", got.Src, got.Dst, got.Predicate)
	}
	if got.Class != "correction" || got.State != "watching" || got.Sightings != 4 {
		t.Errorf("overlay columns lost: %+v", got)
	}
	if got.FirstSeen != 1000 || got.LastSeen != 5000 || got.UpdatedAt != 5001 {
		t.Errorf("timestamps lost: first=%d last=%d updated=%d", got.FirstSeen, got.LastSeen, got.UpdatedAt)
	}
	if got.Evidence != `{"n":4}` || got.SuggestedFix != "cite the obligation" || got.PRURL != "https://example/pr/1" {
		t.Errorf("agent text columns lost: %+v", got)
	}
	if got.Verdict != "fix" || got.Comment != "yes, do this" || got.CommentedAt != "2026-08-01T00:00:00Z" {
		t.Errorf("carl's trio lost: verdict=%q comment=%q at=%q", got.Verdict, got.Comment, got.CommentedAt)
	}
	if got.ExpiredAt != 0 {
		t.Errorf("carried row must be live, expired_at = %d", got.ExpiredAt)
	}
	if got.CreatedAt != 1000 {
		t.Errorf("created_at = %d, want first_seen (when the watch began)", got.CreatedAt)
	}
}

// TestContractWatchCarriesEveryState carries shipped and declined rows too.
//
// Those are SUPPRESSION signals: the dream loop reads them to learn an
// observation has been ruled on and must not be re-proposed. Dropping them
// would make every declined observation come back as fresh the first night
// after the migration — a regression that looks like the loop working.
func TestContractWatchCarriesEveryState(t *testing.T) {
	s := contractWatchV28(t)
	for i, state := range []string{"watching", "shipped", "declined"} {
		contractWatchOverlay(t, s, "src-"+state, "dst-"+state, "observed", "observation "+state, WatchItem{
			Class: "repetition", State: state, Sightings: int64(i + 1),
			FirstSeen: int64(100 * (i + 1)), LastSeen: int64(200 * (i + 1)),
		})
	}

	if err := s.migrate(); err != nil {
		t.Fatalf("migrate into v29: %v", err)
	}

	byObs := contractWatchByObservation(t, s, "")
	if len(byObs) != 3 {
		t.Fatalf("want all three states carried, got %d: %+v", len(byObs), byObs)
	}
	for _, state := range []string{"watching", "shipped", "declined"} {
		got, ok := byObs["observation "+state]
		if !ok {
			t.Errorf("state %q was not carried across", state)
			continue
		}
		if got.State != state {
			t.Errorf("observation %q landed in state %q", state, got.State)
		}
	}
}

// ---------------------------------------------------------------------------
// the verbs
// ---------------------------------------------------------------------------

// TestContractAddWatchItemCreatesThenFolds is the fold, which is the whole
// reason this is one verb instead of a find and an insert.
//
// `sightings` is the one signal separating a one-off from a recurring pattern,
// and so the entire basis for deciding what gets fixed versus merely watched.
// Without the fold nothing errors — the count just sits at 1 across N rows
// that are all the same observation, and the feature quietly stops working.
func TestContractAddWatchItemCreatesThenFolds(t *testing.T) {
	s := openTest(t)

	id, folded, err := s.AddWatchItem(WatchItem{
		Observation: "athena rewrites the contract from the code",
		Src:         "athena", Dst: "contract", Predicate: "rewrites",
		Class: "correction", Evidence: `{"first":true}`,
	})
	if err != nil {
		t.Fatalf("AddWatchItem (create): %v", err)
	}
	if folded {
		t.Fatalf("the first add cannot be a fold")
	}

	first, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if first.Sightings != 1 {
		t.Errorf("a fresh row starts at 1 sighting, got %d", first.Sightings)
	}
	if first.State != "watching" {
		t.Errorf("a blank state defaults to watching, got %q", first.State)
	}
	if first.FirstSeen == 0 || first.FirstSeen != first.LastSeen {
		t.Errorf("a first sighting is its own last: first=%d last=%d", first.FirstSeen, first.LastSeen)
	}

	sameID, folded, err := s.AddWatchItem(WatchItem{
		Observation: "athena rewrites the contract from the code",
		Evidence:    `{"second":true}`, SuggestedFix: "cite the obligation",
	})
	if err != nil {
		t.Fatalf("AddWatchItem (fold): %v", err)
	}
	if !folded {
		t.Fatalf("a byte-identical observation must fold, not create")
	}
	if sameID != id {
		t.Fatalf("fold returned id %d, want the existing %d", sameID, id)
	}

	second, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem after fold: %v", err)
	}
	if second.Sightings != 2 {
		t.Errorf("sightings = %d after one fold, want 2", second.Sightings)
	}
	if second.FirstSeen != first.FirstSeen {
		t.Errorf("first_seen moved on a fold: %d -> %d", first.FirstSeen, second.FirstSeen)
	}
	if second.LastSeen < first.LastSeen {
		t.Errorf("last_seen went backwards on a fold: %d -> %d", first.LastSeen, second.LastSeen)
	}
	if second.Evidence != `{"second":true}` {
		t.Errorf("evidence = %q, want the fresh sample", second.Evidence)
	}
	if second.SuggestedFix != "cite the obligation" {
		t.Errorf("suggested_fix = %q, want the newly supplied one", second.SuggestedFix)
	}
	// The fold restated neither class nor endpoints, so both stay as created.
	if second.Class != "correction" {
		t.Errorf("class = %q, want the created value left alone by a partial fold", second.Class)
	}
	if second.Src != "athena" || second.Dst != "contract" {
		t.Errorf("endpoints changed on a fold: (%q, %q)", second.Src, second.Dst)
	}

	if n := contractWatchCount(t, s, "watch_items"); n != 1 {
		t.Errorf("two adds of one observation left %d rows, want 1", n)
	}
}

// TestContractListWatchItemsOrderAndLiveFilter pins the only listing this
// table has: LIVE rows, most recently observed first, optionally filtered to
// one state.
//
// Order is behaviour rather than presentation here — the DREAM view renders
// this list top-down, so "which observation does carl see first" is decided
// entirely by this ORDER BY.
func TestContractListWatchItemsOrderAndLiveFilter(t *testing.T) {
	s := openTest(t)

	oldest := seedWatchItem(t, s, "oldest")
	middle := seedWatchItem(t, s, "middle")
	newest := seedWatchItem(t, s, "newest")
	retired := seedWatchItem(t, s, "retired")

	// last_seen is what the order reads, and AddWatchItem stamps `now` — so
	// the fixture sets it explicitly rather than relying on three adds landing
	// in three distinguishable milliseconds.
	for id, last := range map[int64]int64{oldest: 100, middle: 200, newest: 300, retired: 400} {
		if err := s.UpdateWatchItem(id, WatchItem{LastSeen: last}); err != nil {
			t.Fatalf("UpdateWatchItem last_seen: %v", err)
		}
	}
	if err := s.UpdateWatchItem(middle, WatchItem{State: "shipped"}); err != nil {
		t.Fatalf("UpdateWatchItem state: %v", err)
	}
	if err := s.ExpireWatchItem(retired, 900); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}

	all, err := s.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	var got []string
	for _, w := range all {
		got = append(got, w.Observation)
	}
	if strings.Join(got, ",") != "newest,middle,oldest" {
		t.Errorf("order = %v, want newest-last_seen first with the expired row absent", got)
	}

	shipped, err := s.ListWatchItems("shipped")
	if err != nil {
		t.Fatalf(`ListWatchItems("shipped"): %v`, err)
	}
	if len(shipped) != 1 || shipped[0].Observation != "middle" {
		t.Errorf("state filter returned %+v, want just the shipped row", shipped)
	}
}

// TestContractUpdateWatchItemIsPartialAndPreservesTrio holds the file's most
// important behaviour from the agent's side: an update writes only the fields
// the caller populated, and can never touch carl's three columns.
//
// Both halves protect against the same silent failure. The caller is an LLM
// restating what it currently believes — under verbatim-write semantics that
// stores state=” and sightings=0, which removes the row from the one query
// the board renders while every read of the raw table still looks fine; and a
// restatement carrying the carl-owned columns would erase a comment he had
// just written, with the row still looking fine afterwards.
func TestContractUpdateWatchItemIsPartialAndPreservesTrio(t *testing.T) {
	s := openTest(t)
	id := seedWatchItem(t, s, "zeus arbitrates against the test rather than the contract")

	if err := s.UpdateWatchItem(id, WatchItem{
		State: "shipped", Sightings: 7, Evidence: `{"n":7}`, SuggestedFix: "cite the obligation",
	}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}
	if err := s.SetWatchVerdict(id, "fine", "leave it"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}

	// A later agent write restating ONLY pr_url must leave everything else —
	// its own earlier columns and carl's judgement — exactly as it found them.
	before, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if err := s.UpdateWatchItem(id, WatchItem{PRURL: "https://example/pr/9"}); err != nil {
		t.Fatalf("UpdateWatchItem (partial): %v", err)
	}
	after, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem after partial: %v", err)
	}

	if after.PRURL != "https://example/pr/9" {
		t.Errorf("pr_url = %q, want the newly supplied one", after.PRURL)
	}
	if after.State != "shipped" || after.Sightings != 7 {
		t.Errorf("a partial update zeroed agent columns: state=%q sightings=%d", after.State, after.Sightings)
	}
	if after.Evidence != before.Evidence || after.SuggestedFix != before.SuggestedFix {
		t.Errorf("a partial update blanked text columns: %+v", after)
	}
	if after.Verdict != "fine" || after.Comment != "leave it" || after.CommentedAt != before.CommentedAt {
		t.Errorf("an agent write touched carl's trio: verdict=%q comment=%q at=%q",
			after.Verdict, after.Comment, after.CommentedAt)
	}
	if after.UpdatedAt == 0 {
		t.Errorf("updated_at is the one column always written, got 0")
	}
	if after.Observation != before.Observation {
		t.Errorf("the observation is not reachable from UpdateWatchItem, but it moved: %q", after.Observation)
	}
}

// TestContractSetWatchVerdictWritesExactlyTheTrio is UpdateWatchItem's mirror
// from carl's side: judging an observation can never roll back what the
// nightly pass has since refined, and updated_at — the AGENT's stamp — is
// deliberately not bumped by a judgement.
func TestContractSetWatchVerdictWritesExactlyTheTrio(t *testing.T) {
	s := openTest(t)
	id := seedWatchItem(t, s, "the fold caveat")
	if err := s.UpdateWatchItem(id, WatchItem{
		Class: "waste", State: "shipped", Sightings: 3, Evidence: "e", SuggestedFix: "f", PRURL: "p",
	}); err != nil {
		t.Fatalf("UpdateWatchItem: %v", err)
	}
	before, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}

	if err := s.SetWatchVerdict(id, "fix", "worth doing"); err != nil {
		t.Fatalf("SetWatchVerdict: %v", err)
	}
	after, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem after verdict: %v", err)
	}

	if after.Verdict != "fix" || after.Comment != "worth doing" {
		t.Errorf("verdict/comment not written: %+v", after)
	}
	if _, perr := time.Parse(time.RFC3339, after.CommentedAt); perr != nil {
		t.Errorf("commented_at = %q, want RFC3339 stamped by the store: %v", after.CommentedAt, perr)
	}
	if !strings.HasSuffix(after.CommentedAt, "Z") {
		t.Errorf("commented_at = %q, want UTC", after.CommentedAt)
	}
	if after.Class != before.Class || after.State != before.State || after.Sightings != before.Sightings ||
		after.Evidence != before.Evidence || after.SuggestedFix != before.SuggestedFix || after.PRURL != before.PRURL {
		t.Errorf("a verdict touched agent-owned columns:\nbefore %+v\nafter  %+v", before, after)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("updated_at tracks the agent's write, not carl's: %d -> %d", before.UpdatedAt, after.UpdatedAt)
	}
}

// TestContractExpireWatchItemClosesRatherThanDeletes is the v29 shape change
// on the remove path: taking an observation off the board is not a claim that
// it was never true, so the row survives, keeps its id, and stays readable.
//
// Both earlier designs deleted — v9 the row, v16 the overlay — and both lost
// the record. This is the assertion that would catch a "simplification" back
// to a DELETE, which every other test in this file would still pass.
func TestContractExpireWatchItemClosesRatherThanDeletes(t *testing.T) {
	s := openTest(t)
	id := seedWatchItem(t, s, "an observation carl retires")

	if err := s.ExpireWatchItem(id, 4242); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}

	if got := contractWatchByObservation(t, s, ""); len(got) != 0 {
		t.Errorf("an expired row is still listed: %+v", got)
	}
	w, err := s.GetWatchItem(id)
	if err != nil {
		t.Fatalf("an expired row must still read by id: %v", err)
	}
	if w.ExpiredAt != 4242 {
		t.Errorf("expired_at = %d, want the supplied stamp", w.ExpiredAt)
	}
	if w.Observation != "an observation carl retires" {
		t.Errorf("the closed row lost its observation: %+v", w)
	}
	if n := contractWatchCount(t, s, "watch_items"); n != 1 {
		t.Errorf("expiry deleted the row: %d rows remain", n)
	}
}
