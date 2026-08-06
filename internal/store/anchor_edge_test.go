package store

import (
	"fmt"
	"reflect"
	"testing"
)

// anchor_edge_test.go — the EDGE dimension of 50-contract.md Slice 1:
// (*Store).AnchorCandidates' boundaries and refusals. Written blind from the
// contract text; internal/store/graph.go was not read.
//
// The row this read produces is the ONLY input the degree guard ever sees, so
// every way of miscounting a degree is a way of silently letting a hub anchor
// (or of silently muting a person). Half the cases below are therefore about
// which edges count: expired ones must not, world-closed-but-live ones must,
// and a self-loop must count once rather than twice.
//
// Every package-level identifier is prefixed `edgeAnchor` — a sibling blind
// author shares this package's test binary, and an unprefixed helper collides
// and takes the whole package down rather than one file.

// --- fixtures --------------------------------------------------------------

// edgeAnchorNode upserts one node and returns its id.
func edgeAnchorNode(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.UpsertNode(GraphNode{Name: name, Type: "concept"})
	if err != nil {
		t.Fatalf("upsert node %q: %v", name, err)
	}
	return id
}

// edgeAnchorEdge inserts one live edge between two existing nodes.
func edgeAnchorEdge(t *testing.T, s *Store, src, dst int64, pred, fact string) int64 {
	t.Helper()
	id, added, err := s.InsertEdge(GraphEdge{
		SrcID: src, DstID: dst, Predicate: pred, Fact: fact, Strength: 5, Occurrences: 1,
	})
	if err != nil || !added {
		t.Fatalf("insert edge %q: id=%d added=%v err=%v", fact, id, added, err)
	}
	return id
}

// edgeAnchorByID indexes the candidate rows so a case can ask about one node
// without depending on its position in the slice.
func edgeAnchorByID(t *testing.T, s *Store) map[int64]AnchorCandidate {
	t.Helper()
	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	out := make(map[int64]AnchorCandidate, len(cands))
	for _, c := range cands {
		if _, dup := out[c.NodeID]; dup {
			t.Fatalf("node %d appears twice in the candidate rows — the alias join fanned the row out", c.NodeID)
		}
		out[c.NodeID] = c
	}
	return out
}

// --- degree: which edges count --------------------------------------------

// TestEdgeAnchorCandidatesSelfLoopCountsOnce pins the one arithmetic the
// contract calls out by name. A self-loop touches the node at BOTH endpoints,
// so the obvious `COUNT(*)` over `src_id = ? OR dst_id = ?` written as two
// summed counts scores it 2 — and a node two self-references away from the
// guard would then be muted for a reason nobody could see in the graph.
func TestEdgeAnchorCandidatesSelfLoopCountsOnce(t *testing.T) {
	s := openGraphStore(t)
	solo := edgeAnchorNode(t, s, "solo")
	pairA := edgeAnchorNode(t, s, "paira")
	pairB := edgeAnchorNode(t, s, "pairb")

	edgeAnchorEdge(t, s, solo, solo, "refers-to", "solo refers to itself")
	// Two distinct rows between the SAME pair (different predicates) are two
	// edges, not one: the count is over rows, and only the self-loop folds.
	edgeAnchorEdge(t, s, pairA, pairB, "uses", "paira uses pairb")
	edgeAnchorEdge(t, s, pairA, pairB, "owns", "paira owns pairb")

	cands := edgeAnchorByID(t, s)
	if got := cands[solo].Degree; got != 1 {
		t.Errorf("self-loop node degree = %d, want 1 — an edge touching a node at both endpoints is ONE live edge", got)
	}
	if got := cands[pairA].Degree; got != 2 {
		t.Errorf("paira degree = %d, want 2 — two rows between the same pair are two edges", got)
	}
	if got := cands[pairB].Degree; got != 2 {
		t.Errorf("pairb degree = %d, want 2 — degree counts edges at EITHER endpoint", got)
	}
}

// TestEdgeAnchorCandidatesIgnoreExpiredEdges pins the `expired_at = 0` half.
// An invalidated edge SURVIVES as a row (the graph is bi-temporal), so a
// degree that counted history would grow monotonically and would eventually
// push every long-lived person over the guard — the feature decaying to
// nothing with no error anywhere.
func TestEdgeAnchorCandidatesIgnoreExpiredEdges(t *testing.T) {
	s := openGraphStore(t)
	n := edgeAnchorNode(t, s, "personnode")
	other := edgeAnchorNode(t, s, "othernode")
	third := edgeAnchorNode(t, s, "thirdnode")

	live := edgeAnchorEdge(t, s, n, other, "knows", "personnode knows othernode")
	dead := edgeAnchorEdge(t, s, n, third, "knew", "personnode knew thirdnode")

	if got := edgeAnchorByID(t, s)[n].Degree; got != 2 {
		t.Fatalf("degree before invalidation = %d, want 2 — the fixture is wrong", got)
	}
	if err := s.InvalidateEdge(dead, 1_700_000_000_000); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	if got := edgeAnchorByID(t, s)[n].Degree; got != 1 {
		t.Errorf("degree after invalidating one of two edges = %d, want 1 — an unlearned fact is not a live edge", got)
	}

	// Invalidate the last one too: the node stays in the answer at degree 0
	// rather than disappearing with its edges.
	if err := s.InvalidateEdge(live, 1_700_000_000_000); err != nil {
		t.Fatalf("invalidate: %v", err)
	}
	cands := edgeAnchorByID(t, s)
	c, ok := cands[n]
	if !ok {
		t.Fatalf("a node whose every edge was invalidated vanished from the candidate rows (rows: %v)", cands)
	}
	if c.Degree != 0 {
		t.Errorf("degree with no live edges = %d, want 0", c.Degree)
	}
}

// TestEdgeAnchorCandidatesCountWorldClosedButLiveEdges is the other side of
// that line, and it is the one an implementation is likeliest to get wrong by
// being helpful: "we used X until March" is a LIVE edge (expired_at 0) with a
// closed world-time window. It is still a real connection in the graph, so it
// still counts toward how well-connected the node is. Filtering on valid_until
// here would make a node's degree drift down as the world moves on.
func TestEdgeAnchorCandidatesCountWorldClosedButLiveEdges(t *testing.T) {
	s := openGraphStore(t)
	n := edgeAnchorNode(t, s, "personnode")
	other := edgeAnchorNode(t, s, "othernode")

	id, added, err := s.InsertEdge(GraphEdge{
		SrcID: n, DstID: other, Predicate: "used",
		Fact: "personnode used othernode until March", Strength: 6, Occurrences: 1,
		ValidFrom: 1_700_000_000_000, ValidUntil: 1_753_900_000_000,
	})
	if err != nil || !added {
		t.Fatalf("insert world-closed edge: id=%d added=%v err=%v", id, added, err)
	}

	// Assert the fixture rather than trusting it: this case is meaningless if
	// the row did not land world-closed AND live.
	rows, err := s.GetEdges([]int64{id})
	if err != nil || len(rows) != 1 {
		t.Fatalf("read back seeded edge: %v %v", rows, err)
	}
	if rows[0].ValidUntil == 0 || rows[0].ExpiredAt != 0 {
		t.Fatalf("fixture is not world-closed-but-live: valid_until=%d expired_at=%d",
			rows[0].ValidUntil, rows[0].ExpiredAt)
	}

	if got := edgeAnchorByID(t, s)[n].Degree; got != 1 {
		t.Errorf("degree = %d, want 1 — a closed WORLD-time window is not an unlearned edge", got)
	}
}

// --- coverage, ordering, aliases -------------------------------------------

// TestEdgeAnchorCandidatesCoverEveryNodeInIDOrder pins the two structural
// promises together: one row per node in kg_nodes (the isolated node included)
// and ascending node id. Order is what makes AnchorNodes' answer reproducible
// across runs, and the isolated node is what an INNER JOIN onto kg_edges
// silently drops — the exact shape of a freshly-extracted person whose facts
// have not landed yet.
func TestEdgeAnchorCandidatesCoverEveryNodeInIDOrder(t *testing.T) {
	s := openGraphStore(t)
	var want []int64
	for _, name := range []string{"firstnode", "secondnode", "thirdnode", "lonelynode"} {
		want = append(want, edgeAnchorNode(t, s, name))
	}
	edgeAnchorEdge(t, s, want[0], want[1], "uses", "firstnode uses secondnode")
	edgeAnchorEdge(t, s, want[1], want[2], "uses", "secondnode uses thirdnode")

	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	if len(cands) != len(want) {
		t.Fatalf("got %d candidate rows for %d nodes: %+v", len(cands), len(want), cands)
	}
	for i, c := range cands {
		if c.NodeID != want[i] {
			t.Errorf("row %d is node %d, want %d — rows must be ordered by node id ascending (%+v)", i, c.NodeID, want[i], cands)
		}
		if i > 0 && cands[i-1].NodeID >= c.NodeID {
			t.Errorf("node ids not strictly ascending: %d then %d", cands[i-1].NodeID, c.NodeID)
		}
	}
	byID := edgeAnchorByID(t, s)
	if c := byID[want[3]]; c.Degree != 0 || len(c.Aliases) != 0 {
		t.Errorf("the isolated node came back as %+v, want degree 0 and no aliases", c)
	}
	if got := byID[want[0]].Name; got != "firstnode" {
		t.Errorf("canonical name = %q, want %q — the resolver matches on this string", got, "firstnode")
	}
}

// TestEdgeAnchorCandidatesAliasesAreSortedAndOptional covers both alias
// obligations at once. The sort is stated in the contract and the aliasless
// node is where "nil or empty, callers must treat alike" is decided: a caller
// ranging over a nil slice is fine, one indexing [0] is not, and only one of
// those two is visible from here.
func TestEdgeAnchorCandidatesAliasesAreSortedAndOptional(t *testing.T) {
	s := openGraphStore(t)
	named := edgeAnchorNode(t, s, "lewis carroll")
	bare := edgeAnchorNode(t, s, "barenode")

	// Added in deliberately reverse order: an implementation that returned
	// insertion order would pass a test whose fixture was already sorted.
	for _, a := range []string{"zeta handle", "dodgson", "charles"} {
		if err := s.AddAlias(named, a); err != nil {
			t.Fatalf("add alias %q: %v", a, err)
		}
	}

	byID := edgeAnchorByID(t, s)
	got := byID[named].Aliases
	want := []string{"charles", "dodgson", "zeta handle"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("aliases = %v, want %v (ascending)", got, want)
	}
	if n := len(byID[bare].Aliases); n != 0 {
		t.Errorf("a node with no alias rows carries %d aliases: %v", n, byID[bare].Aliases)
	}
	// The aliasless row must be safe to range over whichever spelling of
	// "empty" the implementation chose.
	for range byID[bare].Aliases {
		t.Error("ranged over an alias on a node that has none")
	}
}

// TestEdgeAnchorCandidatesOnAnEmptyGraph: a fresh install has no nodes at all,
// and every cell spawn calls this read. Nothing is an ordinary answer here, not
// an error — an error would put GraphSearch on its degrade path forever.
func TestEdgeAnchorCandidatesOnAnEmptyGraph(t *testing.T) {
	s := openGraphStore(t)
	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("an empty graph errored: %v", err)
	}
	if len(cands) != 0 {
		t.Errorf("an empty graph returned %d candidate rows: %+v", len(cands), cands)
	}
}

// TestEdgeAnchorCandidatesIsReadOnly pins the contract's last clause. This read
// runs on every search, so a stray write here would put a mutation on the
// hottest path in the process — and `kraken serve` is the db's single writer,
// which is exactly the invariant a helpful "cache the degree" column would
// break.
func TestEdgeAnchorCandidatesIsReadOnly(t *testing.T) {
	s := openGraphStore(t)
	a := edgeAnchorNode(t, s, "firstnode")
	b := edgeAnchorNode(t, s, "secondnode")
	edgeAnchorEdge(t, s, a, b, "uses", "firstnode uses secondnode")
	if err := s.AddAlias(a, "the first one"); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	before, err := s.GetGraphStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := s.AnchorCandidates(); err != nil {
			t.Fatalf("AnchorCandidates run %d: %v", i, err)
		}
	}
	after, err := s.GetGraphStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Errorf("the graph changed across three reads:\nbefore %+v\nafter  %+v", before, after)
	}

	// And the answer itself is stable across repeats — a read that returned a
	// different order on the second call would make GraphSearch's determinism
	// obligation unsatisfiable from below.
	first, err := s.AnchorCandidates()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		again, err := s.AnchorCandidates()
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(first, again) {
			t.Fatalf("run %d differs:\n%+v\nvs\n%+v", i, again, first)
		}
	}
}

// TestEdgeAnchorCandidatesOnAClosedStoreDoesNotInventRows is the failure the
// core layer is required to degrade around. What matters here is that the
// failure is REPORTED rather than returned as an empty graph: "no candidates"
// and "the read failed" are the same value to a caller that ignores the error,
// and only one of them means the corpus really has no nodes.
func TestEdgeAnchorCandidatesOnAClosedStoreDoesNotInventRows(t *testing.T) {
	s := openGraphStore(t)
	edgeAnchorNode(t, s, "personnode")
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	cands, err := s.AnchorCandidates() // must not panic
	if err == nil {
		t.Errorf("a closed store answered with %d rows and no error", len(cands))
	}
	if len(cands) != 0 {
		t.Errorf("a failed read returned %d rows: %+v", len(cands), cands)
	}
}

// TestEdgeAnchorCandidatesScaleToTheWholeGraph is the shape the live corpus
// has (~300 nodes): every node present, degrees correct, one read. A per-node
// query would still pass every case above and would turn one read into 300.
func TestEdgeAnchorCandidatesScaleToTheWholeGraph(t *testing.T) {
	s := openGraphStore(t)
	hub := edgeAnchorNode(t, s, "hubnode")
	const leaves = 40
	var leafIDs []int64
	for i := 0; i < leaves; i++ {
		leaf := edgeAnchorNode(t, s, fmt.Sprintf("leafnode%02d", i))
		leafIDs = append(leafIDs, leaf)
		edgeAnchorEdge(t, s, hub, leaf, "touches", fmt.Sprintf("hubnode touches leafnode%02d quietly", i))
	}
	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	if len(cands) != leaves+1 {
		t.Fatalf("got %d rows, want %d", len(cands), leaves+1)
	}
	byID := edgeAnchorByID(t, s)
	if got := byID[hub].Degree; got != leaves {
		t.Errorf("hub degree = %d, want %d", got, leaves)
	}
	for _, id := range leafIDs {
		if got := byID[id].Degree; got != 1 {
			t.Errorf("leaf %d degree = %d, want 1", id, got)
		}
	}
}
