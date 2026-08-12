package store

import (
	"testing"
)

// recall_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice A's store half (the kg_recall table and its two
// accessors). Written from the contract text alone, with no sight of
// internal/store/recall.go or schema.go's v19 parts.
//
// Dimension: LOGIC — the stated obligations, the happy paths, the invariants.
// Boundaries and hostile inputs are athena-edge's ground; where an obligation
// IS an input rule (the contract states MarkRecalled's empty-input behavior
// as part of its contract line) it is pinned here too, and overlap is fine.
//
// Every package-level identifier is prefixed `contract` — a sibling author
// shares this package namespace.
//
// The migration itself has no test of its own by design: the contract's own
// note says so, and it is true — openTest() migrates a fresh database all the
// way to schemaVersion, so every test below that touches kg_recall through
// the accessors, and contractRecallRows' direct SELECT of the three named
// columns, IS the assertion that v19 created the table in the stated shape.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractRecallRows reads the raw kg_recall rows for a session, by the exact
// column names the contract's DDL states. Going under the accessors is
// deliberate and is the only way two of the obligations are observable at
// all: RecalledEdgeIDs returns a SET, so it can neither report a duplicated
// row nor report a row's ts, and "never duplicates a row or updates its ts"
// is a claim about both.
func contractRecallRows(t *testing.T, s *Store, sessionID string) map[int64]int64 {
	t.Helper()
	rows, err := s.db.Query(`SELECT edge_id, ts FROM kg_recall WHERE session_id = ? ORDER BY edge_id`, sessionID)
	if err != nil {
		t.Fatalf("select kg_recall (session %q): %v", sessionID, err)
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var edgeID, ts int64
		if err := rows.Scan(&edgeID, &ts); err != nil {
			t.Fatalf("scan kg_recall row: %v", err)
		}
		if _, dup := out[edgeID]; dup {
			t.Fatalf("kg_recall holds TWO rows for session %q edge %d — the PRIMARY KEY (session_id, edge_id) is not in force", sessionID, edgeID)
		}
		out[edgeID] = ts
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate kg_recall: %v", err)
	}
	return out
}

// contractRecallCount is the whole-table row count, for the "no write"
// obligations: a refusal that wrote a row somewhere else is still a write.
func contractRecallCount(t *testing.T, s *Store) int64 {
	t.Helper()
	var n int64
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_recall`).Scan(&n); err != nil {
		t.Fatalf("count kg_recall: %v", err)
	}
	return n
}

// ---------------------------------------------------------------------------
// schema v19
// ---------------------------------------------------------------------------

// TestContractRecallSchemaVersionAtLeast19 pins the contract's stated
// version as a FLOOR. Recall's own slice appended exactly one migration and
// reached 19; the exact-equality pin it originally carried was a claim about
// that slice which every LATER schema change would falsify while saying
// nothing about recall — v20 (the queued-status retirement) is the first such
// change. What still matters here is that kg_recall's migration landed, which
// is what >= 19 says, plus the fresh-open agreement below.
func TestContractRecallSchemaVersionAtLeast19(t *testing.T) {
	if schemaVersion < 19 {
		t.Fatalf("schemaVersion = %d, want >= 19 (contract: Slice A appends schemaV19)", schemaVersion)
	}
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("a freshly opened store is at user_version %d, want %d", version, schemaVersion)
	}
}

// TestContractRecallTableExistsInTheStatedShape is the migration test the
// contract's note describes: a fresh store already carries kg_recall with the
// three named columns, and its accessors work against it with no setup.
func TestContractRecallTableExistsInTheStatedShape(t *testing.T) {
	s := openTest(t)
	if got := contractRecallCount(t, s); got != 0 {
		t.Fatalf("a fresh store has %d kg_recall rows, want 0", got)
	}
	if got := contractRecallRows(t, s, "nobody"); len(got) != 0 {
		t.Fatalf("kg_recall rows for an unknown session = %v, want none", got)
	}
	ids, err := s.RecalledEdgeIDs("nobody")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs on a fresh store: %v", err)
	}
	if ids == nil {
		t.Fatal("RecalledEdgeIDs returned a NIL map for a session with no rows; the contract requires a non-nil empty map")
	}
}

// ---------------------------------------------------------------------------
// RecalledEdgeIDs
// ---------------------------------------------------------------------------

// TestContractRecalledEdgeIDsNonNilEmptyMap pins both of the contract's
// "nothing was ever marked" cases — an unknown session and the empty session
// id — as the SAME answer: a non-nil empty map and a nil error, never an
// error. jobs.Recall fails closed on a read error, so an error here would
// silently disable injection rather than report anything.
func TestContractRecalledEdgeIDsNonNilEmptyMap(t *testing.T) {
	s := openTest(t)
	for _, sid := range []string{"never-seen-session", ""} {
		ids, err := s.RecalledEdgeIDs(sid)
		if err != nil {
			t.Errorf("RecalledEdgeIDs(%q) error = %v, want nil (nothing marked is not an error)", sid, err)
			continue
		}
		if ids == nil {
			t.Errorf("RecalledEdgeIDs(%q) = nil map, want a NON-NIL empty map", sid)
			continue
		}
		if len(ids) != 0 {
			t.Errorf("RecalledEdgeIDs(%q) = %v, want empty", sid, ids)
		}
	}
}

// TestContractRecalledEdgeIDsReturnsWhatWasMarked is the accessor pair's
// central happy path: what MarkRecalled wrote is what RecalledEdgeIDs reports,
// as a set with every id present and true.
func TestContractRecalledEdgeIDsReturnsWhatWasMarked(t *testing.T) {
	s := openTest(t)
	want := []int64{4, 8, 15, 16, 23, 42}
	if err := s.MarkRecalled("sess-a", want, 1_700_000_000_000); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	ids, err := s.RecalledEdgeIDs("sess-a")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(ids) != len(want) {
		t.Fatalf("RecalledEdgeIDs = %v, want %d ids %v", ids, len(want), want)
	}
	for _, id := range want {
		if !ids[id] {
			t.Errorf("edge %d was marked but is missing from the recalled set %v", id, ids)
		}
	}
}

// TestContractRecalledEdgeIDsIsPerSession pins the dedup KEY. "At most one
// delivery of a given edge id per session_id" is the contract's definition of
// "retrieved in the conversation"; a set that leaked across sessions would
// mean a fact delivered in one conversation is withheld from every later one.
func TestContractRecalledEdgeIDsIsPerSession(t *testing.T) {
	s := openTest(t)
	if err := s.MarkRecalled("sess-a", []int64{1, 2}, 100); err != nil {
		t.Fatalf("MarkRecalled a: %v", err)
	}
	if err := s.MarkRecalled("sess-b", []int64{2, 3}, 200); err != nil {
		t.Fatalf("MarkRecalled b: %v", err)
	}
	a, err := s.RecalledEdgeIDs("sess-a")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs a: %v", err)
	}
	b, err := s.RecalledEdgeIDs("sess-b")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs b: %v", err)
	}
	if !a[1] || !a[2] || a[3] {
		t.Errorf("sess-a = %v, want {1,2} exactly — edge 3 belongs to another session", a)
	}
	if !b[2] || !b[3] || b[1] {
		t.Errorf("sess-b = %v, want {2,3} exactly — edge 1 belongs to another session", b)
	}
}

// ---------------------------------------------------------------------------
// MarkRecalled
// ---------------------------------------------------------------------------

// TestContractMarkRecalledWritesOneRowPerIDWithTheGivenTS pins the write's
// stated shape: one row per id, all carrying the ts the caller passed (never
// a clock read of the store's own).
func TestContractMarkRecalledWritesOneRowPerIDWithTheGivenTS(t *testing.T) {
	s := openTest(t)
	const ts = int64(1_699_999_999_999)
	if err := s.MarkRecalled("sess", []int64{7, 9}, ts); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	rows := contractRecallRows(t, s, "sess")
	if len(rows) != 2 {
		t.Fatalf("kg_recall holds %d rows for the session, want 2: %v", len(rows), rows)
	}
	for _, id := range []int64{7, 9} {
		got, ok := rows[id]
		if !ok {
			t.Errorf("no kg_recall row for edge %d", id)
			continue
		}
		if got != ts {
			t.Errorf("edge %d row ts = %d, want the caller's %d", id, got, ts)
		}
	}
}

// TestContractMarkRecalledIsInsertOrIgnore is the contract's re-mark rule in
// all three of its halves at once: re-marking is not an error, it does not
// duplicate the row, and it does not update the row's ts. The third is the one
// with teeth — an updating write would make the row say when the edge was last
// CONSIDERED rather than when it was delivered.
func TestContractMarkRecalledIsInsertOrIgnore(t *testing.T) {
	s := openTest(t)
	const first = int64(1000)
	const second = int64(2000)
	if err := s.MarkRecalled("sess", []int64{5}, first); err != nil {
		t.Fatalf("first MarkRecalled: %v", err)
	}
	if err := s.MarkRecalled("sess", []int64{5}, second); err != nil {
		t.Fatalf("re-marking an already-present id must not be an error, got: %v", err)
	}
	rows := contractRecallRows(t, s, "sess")
	if len(rows) != 1 {
		t.Fatalf("kg_recall holds %d rows after a re-mark, want 1: %v", len(rows), rows)
	}
	if rows[5] != first {
		t.Errorf("edge 5 row ts = %d after re-marking with %d, want the original %d — an already-present id is silently SKIPPED, not updated", rows[5], second, first)
	}
	ids, err := s.RecalledEdgeIDs("sess")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(ids) != 1 || !ids[5] {
		t.Errorf("RecalledEdgeIDs after a re-mark = %v, want exactly {5}", ids)
	}
}

// TestContractMarkRecalledMixedBatchLandsWhole is the observable half of the
// atomicity obligation, and it is the half that actually bites in production:
// jobs.Recall marks the whole kept set in one call, and that set routinely
// mixes ids this session has never seen with ids a concurrent path already
// recorded. Every NEW id must land even though a present one is skipped —
// an implementation that bailed on the first conflict would silently stop
// deduplicating everything after it.
//
// True partial-write invisibility (a failure part-way through the batch) is
// not reachable from the frozen symbols: nothing in the signature can be made
// to fail mid-loop. Recorded in the report rather than faked here.
func TestContractMarkRecalledMixedBatchLandsWhole(t *testing.T) {
	s := openTest(t)
	if err := s.MarkRecalled("sess", []int64{2}, 100); err != nil {
		t.Fatalf("seed MarkRecalled: %v", err)
	}
	if err := s.MarkRecalled("sess", []int64{1, 2, 3, 4}, 200); err != nil {
		t.Fatalf("mixed MarkRecalled: %v", err)
	}
	rows := contractRecallRows(t, s, "sess")
	if len(rows) != 4 {
		t.Fatalf("kg_recall holds %d rows, want 4 (1,2,3,4): %v", len(rows), rows)
	}
	if rows[2] != 100 {
		t.Errorf("edge 2 row ts = %d, want the original 100 — the already-present id is skipped, not rewritten", rows[2])
	}
	for _, id := range []int64{1, 3, 4} {
		if rows[id] != 200 {
			t.Errorf("edge %d row ts = %d, want 200 — a new id in a batch containing a conflict must still land", id, rows[id])
		}
	}
}

// TestContractMarkRecalledRepeatedIDsWithinOneBatch pins the same rule inside
// a single call: the kept set jobs.Recall passes is built from a retrieval
// result, and a duplicate there must be absorbed rather than crash the write.
func TestContractMarkRecalledRepeatedIDsWithinOneBatch(t *testing.T) {
	s := openTest(t)
	if err := s.MarkRecalled("sess", []int64{6, 6, 7, 6}, 300); err != nil {
		t.Fatalf("MarkRecalled with a repeated id: %v", err)
	}
	rows := contractRecallRows(t, s, "sess")
	if len(rows) != 2 || rows[6] != 300 || rows[7] != 300 {
		t.Errorf("kg_recall rows = %v, want exactly {6:300, 7:300}", rows)
	}
}

// TestContractMarkRecalledEmptyIDsIsANoOp pins "empty/nil edgeIDs → nil, no
// write". jobs.Recall never calls it with an empty set today, but the answer
// has to be a quiet nil rather than an error: an empty delivery is not a
// failed one.
func TestContractMarkRecalledEmptyIDsIsANoOp(t *testing.T) {
	s := openTest(t)
	if err := s.MarkRecalled("sess", nil, 400); err != nil {
		t.Errorf("MarkRecalled with nil ids = %v, want nil", err)
	}
	if err := s.MarkRecalled("sess", []int64{}, 400); err != nil {
		t.Errorf("MarkRecalled with an empty slice = %v, want nil", err)
	}
	if n := contractRecallCount(t, s); n != 0 {
		t.Errorf("kg_recall holds %d rows after two empty marks, want 0", n)
	}
}

// TestContractMarkRecalledRefusesABlankSession pins the refusal AND its "no
// write" half. A blank session id is not a dedup key, so a row under it would
// be a delivery record nothing could ever match against.
func TestContractMarkRecalledRefusesABlankSession(t *testing.T) {
	s := openTest(t)
	for _, sid := range []string{"", "   ", "\t\n "} {
		if err := s.MarkRecalled(sid, []int64{1}, 500); err == nil {
			t.Errorf("MarkRecalled(%q, …) = nil error, want a refusal", sid)
		}
	}
	if n := contractRecallCount(t, s); n != 0 {
		t.Errorf("kg_recall holds %d rows after refused marks, want 0 — a refusal must write nothing", n)
	}
}

// TestContractRecallSurvivesAnInvalidatedEdge pins the no-FK/no-cascade
// stance from the only side a test can see it: an edge id with no kg_edges row
// behind it at all is marked and read back without complaint. A foreign key
// would have refused the write; a cascade would have taken the row away with
// the edge.
func TestContractRecallSurvivesAnInvalidatedEdge(t *testing.T) {
	s := openTest(t)
	const ghost = int64(987654321) // no such edge has ever existed
	if err := s.MarkRecalled("sess", []int64{ghost}, 600); err != nil {
		t.Fatalf("MarkRecalled for an edge id with no kg_edges row: %v (no FK is required)", err)
	}
	ids, err := s.RecalledEdgeIDs("sess")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if !ids[ghost] {
		t.Errorf("recalled set %v lost the row for edge %d — the recall row is meant to outlive its edge harmlessly", ids, ghost)
	}
}
