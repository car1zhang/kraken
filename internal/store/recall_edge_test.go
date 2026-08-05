package store

import (
	"path/filepath"
	"testing"
)

// recall_edge_test.go — the EDGE dimension of 50-contract.md's Slice A store
// half: the refusals, the no-ops and the degenerate sessions of `kg_recall`,
// `MarkRecalled` and `RecalledEdgeIDs`. Happy paths and the ordinary
// mark-then-read invariant belong to the sibling blind author; nothing here
// duplicates them beyond what a refusal has to read back to prove it wrote
// nothing.
//
// Every package-level identifier is prefixed `edgeRecall` — this package's
// test namespace already carries an `edgeStore`/`edgeLiveEdge` family from
// watch_edge_test.go and a second blind author writing beside this file.
//
// Row counts are read with raw SQL against the table the contract names
// rather than through the accessors under test: an accessor that agreed with
// its own bug would report "nothing was written" for a write that happened.

func edgeRecallStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// edgeRecallRowCount counts every row in kg_recall, across all sessions —
// the only way to say "no write happened" that a refusal cannot fake.
func edgeRecallRowCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM kg_recall`).Scan(&n); err != nil {
		t.Fatalf("count kg_recall: %v", err)
	}
	return n
}

// edgeRecallTS reads the stored ts for one (session, edge) pair, reporting
// ok=false when no such row exists.
func edgeRecallTS(t *testing.T, s *Store, sessionID string, edgeID int64) (int64, bool) {
	t.Helper()
	var ts int64
	err := s.db.QueryRow(`SELECT ts FROM kg_recall WHERE session_id = ? AND edge_id = ?`,
		sessionID, edgeID).Scan(&ts)
	if err != nil {
		return 0, false
	}
	return ts, true
}

// --- MarkRecalled refusals -------------------------------------------------

// TestEdgeRecallMarkRefusesBlankSession pins the contract's first refusal: a
// session id that is empty or nothing but whitespace has no dedup key, so a
// row keyed on it could never be found again. It must be an error AND it must
// leave the table empty — a refusal that half-wrote would be worse than one
// that silently accepted, since the rows would be unreachable forever.
func TestEdgeRecallMarkRefusesBlankSession(t *testing.T) {
	s := edgeRecallStore(t)
	for _, sess := range []string{"", " ", "\t", "\n", "  \t\r\n  "} {
		err := s.MarkRecalled(sess, []int64{1, 2, 3}, 1_700_000_000_000)
		if err == nil {
			t.Errorf("MarkRecalled(%q, 3 ids) = nil, want an error — a blank session id is not a dedup key", sess)
		}
		if n := edgeRecallRowCount(t, s); n != 0 {
			t.Fatalf("MarkRecalled(%q) wrote %d rows despite refusing", sess, n)
		}
	}
}

// TestEdgeRecallMarkEmptyIDsIsASilentNoop pins the second refusal, which is
// deliberately NOT an error: "nothing was relevant this turn" is the common
// outcome of a recall, and a caller must not have to special-case it.
func TestEdgeRecallMarkEmptyIDsIsASilentNoop(t *testing.T) {
	s := edgeRecallStore(t)
	for name, ids := range map[string][]int64{"nil": nil, "empty": {}} {
		if err := s.MarkRecalled("sess-noop", ids, 1_700_000_000_000); err != nil {
			t.Errorf("MarkRecalled with %s ids = %v, want nil — an empty delivery is normal", name, err)
		}
		if n := edgeRecallRowCount(t, s); n != 0 {
			t.Fatalf("MarkRecalled with %s ids wrote %d rows", name, n)
		}
	}
	// And the session stays unknown afterwards: a no-op must not mint a
	// phantom presence for the session.
	got, err := s.RecalledEdgeIDs("sess-noop")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs after a no-op mark: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("RecalledEdgeIDs after a no-op mark = %v, want empty", got)
	}
}

// TestEdgeRecallMarkBlankSessionBeatsEmptyIDs settles the one ordering the
// contract leaves implicit: both refusals can apply to the same call. The
// blank-session rule is the one that says "error"; it must not be shadowed by
// the empty-ids rule's nil.
func TestEdgeRecallMarkBlankSessionBeatsEmptyIDs(t *testing.T) {
	s := edgeRecallStore(t)
	if err := s.MarkRecalled("   ", nil, 1); err == nil {
		t.Error("MarkRecalled(blank session, nil ids) = nil, want the blank-session error")
	}
	if n := edgeRecallRowCount(t, s); n != 0 {
		t.Fatalf("wrote %d rows", n)
	}
}

// --- re-marking ------------------------------------------------------------

// TestEdgeRecallReMarkKeepsOriginalTS is the INSERT OR IGNORE contract stated
// as its two observable consequences: re-marking is not an error, and the
// original ts survives. The ts is when the memory was actually delivered; an
// UPSERT that refreshed it would make every re-run of a hook look like a
// fresh delivery.
func TestEdgeRecallReMarkKeepsOriginalTS(t *testing.T) {
	s := edgeRecallStore(t)
	const sess = "sess-remark"
	const first, second = int64(1_700_000_000_000), int64(1_800_000_000_000)

	if err := s.MarkRecalled(sess, []int64{11, 22}, first); err != nil {
		t.Fatalf("first MarkRecalled: %v", err)
	}
	if err := s.MarkRecalled(sess, []int64{11, 22, 33}, second); err != nil {
		t.Fatalf("re-MarkRecalled: %v", err)
	}

	if n := edgeRecallRowCount(t, s); n != 3 {
		t.Fatalf("kg_recall holds %d rows after re-marking 2 of 3 ids, want 3 — a re-mark duplicated a row", n)
	}
	for _, id := range []int64{11, 22} {
		ts, ok := edgeRecallTS(t, s, sess, id)
		if !ok {
			t.Fatalf("edge %d lost its row on re-mark", id)
		}
		if ts != first {
			t.Errorf("edge %d ts = %d after re-mark, want the original %d", id, ts, first)
		}
	}
	ts, ok := edgeRecallTS(t, s, sess, 33)
	if !ok {
		t.Fatal("the genuinely new edge 33 was not inserted alongside the ignored ones")
	}
	if ts != second {
		t.Errorf("edge 33 ts = %d, want the second call's %d", ts, second)
	}
}

// TestEdgeRecallMarkToleratesDuplicateIDsInOneCall covers the shape a caller
// most easily produces by accident: retrieval returning the same edge twice
// through two traversals. One call, ids repeated — the batch must land as a
// set, not fail on its own primary key.
func TestEdgeRecallMarkToleratesDuplicateIDsInOneCall(t *testing.T) {
	s := edgeRecallStore(t)
	if err := s.MarkRecalled("sess-dupes", []int64{5, 5, 6, 5}, 42); err != nil {
		t.Fatalf("MarkRecalled with repeated ids = %v, want nil", err)
	}
	if n := edgeRecallRowCount(t, s); n != 2 {
		t.Errorf("kg_recall holds %d rows for ids {5,5,6,5}, want 2", n)
	}
}

// TestEdgeRecallMarkIsAllOrNothing pins the atomicity clause from the only
// side a caller can observe it: whatever a single call does, the set it
// promised is entirely present afterwards. A batch that committed its ids one
// statement at a time would still pass this on a healthy database — which is
// why it is paired with the count, so a batch that dropped a tail id shows up
// here rather than silently under-recording a delivery.
func TestEdgeRecallMarkIsAllOrNothing(t *testing.T) {
	s := edgeRecallStore(t)
	const sess = "sess-atomic"
	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if err := s.MarkRecalled(sess, ids, 7); err != nil {
		t.Fatalf("MarkRecalled: %v", err)
	}
	got, err := s.RecalledEdgeIDs(sess)
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("RecalledEdgeIDs = %d ids, want all %d — a partial mark is observable", len(got), len(ids))
	}
	for _, id := range ids {
		if !got[id] {
			t.Errorf("edge %d missing from the marked set", id)
		}
	}
	if n := edgeRecallRowCount(t, s); n != len(ids) {
		t.Errorf("kg_recall holds %d rows, want %d", n, len(ids))
	}
}

// --- RecalledEdgeIDs on nothing -------------------------------------------

// TestEdgeRecalledEdgeIDsOnUnknownSession pins the shape the caller relies on
// most: a session nobody has ever marked returns a NON-NIL empty map and a
// nil error. Non-nil matters because the caller indexes it directly — a nil
// map read is fine in Go, but a nil returned alongside a nil error is
// indistinguishable from "the read failed and was swallowed", and the caller
// fails CLOSED on a read error.
func TestEdgeRecalledEdgeIDsOnUnknownSession(t *testing.T) {
	s := edgeRecallStore(t)
	for _, sess := range []string{"never-seen", "", "   ", "\t\n"} {
		got, err := s.RecalledEdgeIDs(sess)
		if err != nil {
			t.Errorf("RecalledEdgeIDs(%q) error = %v, want nil — nothing was ever marked for it, which is not a failure", sess, err)
		}
		if got == nil {
			t.Errorf("RecalledEdgeIDs(%q) = nil map, want a non-nil empty map", sess)
			continue
		}
		if len(got) != 0 {
			t.Errorf("RecalledEdgeIDs(%q) = %v, want empty", sess, got)
		}
	}
}

// TestEdgeRecalledEdgeIDsIsSessionScoped is the dedup key's whole point: one
// session's deliveries must never suppress another's. "At most once per
// session_id" is the contract; a query that forgot its WHERE would read as a
// global mute and the feature would appear to work exactly once ever.
func TestEdgeRecalledEdgeIDsIsSessionScoped(t *testing.T) {
	s := edgeRecallStore(t)
	if err := s.MarkRecalled("sess-a", []int64{100, 200}, 1); err != nil {
		t.Fatalf("mark a: %v", err)
	}
	got, err := s.RecalledEdgeIDs("sess-b")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs(sess-b): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("sess-b sees %v — session A's deliveries leaked across the dedup key", got)
	}
	// Marking the SAME edge for a second session is a fresh row, not a
	// conflict: the primary key is the pair.
	if err := s.MarkRecalled("sess-b", []int64{100}, 2); err != nil {
		t.Fatalf("mark b: %v", err)
	}
	if n := edgeRecallRowCount(t, s); n != 3 {
		t.Errorf("kg_recall holds %d rows for {a:100,a:200,b:100}, want 3", n)
	}
	if ts, ok := edgeRecallTS(t, s, "sess-a", 100); !ok || ts != 1 {
		t.Errorf("session A's row for edge 100 = (%d,%v), want (1,true) — session B's mark overwrote it", ts, ok)
	}
}

// TestEdgeRecallSurvivesAnInvalidatedEdge pins the no-FK stance: kg_recall
// carries no foreign key to kg_edges and no cascade, so a recall row for an
// edge that has since been invalidated (or never existed at all) is harmless
// rather than a constraint failure. Recording a delivery must not depend on
// the delivered fact still being current — the point of the row is that the
// session already SAW it.
func TestEdgeRecallSurvivesAnInvalidatedEdge(t *testing.T) {
	s := edgeRecallStore(t)
	src, err := s.UpsertNode(GraphNode{Name: "edgeRecall src"})
	if err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	dst, err := s.UpsertNode(GraphNode{Name: "edgeRecall dst"})
	if err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	id, _, err := s.InsertEdge(GraphEdge{SrcID: src, DstID: dst, Predicate: "observed",
		Fact: "an edge that is about to be invalidated.", Strength: 5})
	if err != nil {
		t.Fatalf("insert edge: %v", err)
	}
	if err := s.MarkRecalled("sess-invalidated", []int64{id, id + 9999}, 3); err != nil {
		t.Fatalf("MarkRecalled over a live edge and a nonexistent one: %v", err)
	}
	if err := s.InvalidateEdge(id, 4); err != nil {
		t.Fatalf("InvalidateEdge: %v", err)
	}
	got, err := s.RecalledEdgeIDs("sess-invalidated")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs after invalidation: %v", err)
	}
	if !got[id] || !got[id+9999] {
		t.Errorf("recall rows = %v, want both %d and %d to survive — no FK, no cascade", got, id, id+9999)
	}
}
