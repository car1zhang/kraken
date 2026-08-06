package store

import (
	"strings"
	"testing"
)

// anchor_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, Slice 1: `store.AnchorCandidate` and
// `(*Store).AnchorCandidates`.
//
// internal/store/graph.go was NOT read. Every assertion is transcribed from the
// slice-1 row: one row per node ordered by id, LIVE-edge degree at either
// endpoint with a self-loop counting once, a world-closed but live edge still
// counting, aliases joined and sorted ascending, and a read that writes
// nothing.
//
// Dimension: LOGIC. Package-level identifiers are prefixed `contractAnchor`.

// contractAnchorByID indexes the candidate rows so an assertion can name the
// node it is about instead of a slice position.
func contractAnchorByID(cands []AnchorCandidate) map[int64]AnchorCandidate {
	out := map[int64]AnchorCandidate{}
	for _, c := range cands {
		out[c.NodeID] = c
	}
	return out
}

// contractAnchorNode upserts one node and fails loudly, since every fixture
// below is meaningless without its ids.
func contractAnchorNode(t *testing.T, s *Store, name string) int64 {
	t.Helper()
	id, err := s.UpsertNode(GraphNode{Name: name, Type: "concept"})
	if err != nil {
		t.Fatalf("upsert node %q: %v", name, err)
	}
	return id
}

// contractAnchorEdge inserts one live edge between two nodes.
func contractAnchorEdge(t *testing.T, s *Store, src, dst int64, pred, fact string) int64 {
	t.Helper()
	id, added, err := s.InsertEdge(GraphEdge{SrcID: src, DstID: dst, Predicate: pred, Fact: fact, Strength: 5})
	if err != nil || !added {
		t.Fatalf("insert edge %q: id=%d added=%v err=%v", fact, id, added, err)
	}
	return id
}

// TestContractAnchorCandidatesRowPerNodeAscending pins the row's first two
// clauses: EVERY node in kg_nodes appears, and the rows ascend by node id.
// Ascending id is what makes the resolver's answer — and therefore the whole
// search's — reproducible for a fixed corpus, so it is a property of the read
// rather than of any caller's sort.
func TestContractAnchorCandidatesRowPerNodeAscending(t *testing.T) {
	s := openGraphStore(t)
	want := []string{"zephyrine", "cofounder", "harbourside"}
	ids := make([]int64, 0, len(want))
	for _, n := range want {
		ids = append(ids, contractAnchorNode(t, s, n))
	}

	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	if len(cands) != len(want) {
		t.Fatalf("got %d candidate rows for %d nodes: %+v", len(cands), len(want), cands)
	}
	for i := 1; i < len(cands); i++ {
		if cands[i-1].NodeID >= cands[i].NodeID {
			t.Fatalf("rows are not ascending by node id: %d then %d", cands[i-1].NodeID, cands[i].NodeID)
		}
	}
	byID := contractAnchorByID(cands)
	for i, n := range want {
		got, ok := byID[ids[i]]
		if !ok {
			t.Fatalf("node %q (%d) has no candidate row", n, ids[i])
		}
		if !strings.EqualFold(got.Name, n) {
			t.Errorf("node %d: Name = %q, want the canonical %q", ids[i], got.Name, n)
		}
	}
}

// TestContractAnchorCandidatesDegreeCountsLiveEdgesOnly is the row's degree
// clause in full, on one fixture that exercises each of its four statements at
// once. Every count below is stated in the contract, and the four cases pull in
// different directions — an implementation that counted rows rather than live
// rows, or endpoints rather than nodes, fails exactly one of them.
func TestContractAnchorCandidatesDegreeCountsLiveEdgesOnly(t *testing.T) {
	s := openGraphStore(t)
	a := contractAnchorNode(t, s, "alphanode")
	b := contractAnchorNode(t, s, "betanode")
	c := contractAnchorNode(t, s, "gammanode")
	d := contractAnchorNode(t, s, "deltanode")
	lone := contractAnchorNode(t, s, "lonelynode")

	// 1. An ordinary live edge counts for BOTH endpoints.
	contractAnchorEdge(t, s, a, b, "touches", "alphanode touches betanode")

	// 2. An INVALIDATED edge counts for neither: the row survives (the graph is
	//    bi-temporal) but a retracted fact is not part of a node's live
	//    neighbourhood, and counting it would push a churny node over the guard
	//    on history alone.
	dead := contractAnchorEdge(t, s, a, c, "used", "alphanode used gammanode")
	if err := s.InvalidateEdge(dead, 1753900000000); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	// 3. A WORLD-closed but live edge (valid_until set, expired_at 0) still
	//    counts. "We used X until March" is a fact kraken still believes.
	if _, added, err := s.InsertEdge(GraphEdge{SrcID: b, DstID: d, Predicate: "used",
		Fact: "betanode used deltanode until March", Strength: 5, ValidUntil: 1753900000000}); err != nil || !added {
		t.Fatalf("insert world-closed edge: added=%v err=%v", added, err)
	}

	// 4. A self-loop counts ONCE, not twice — the node is one endpoint of it in
	//    the sense the guard cares about.
	contractAnchorEdge(t, s, d, d, "refers-to", "deltanode refers to itself")

	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	byID := contractAnchorByID(cands)
	for _, want := range []struct {
		id     int64
		name   string
		degree int
		why    string
	}{
		{a, "alphanode", 1, "one live edge; the invalidated one does not count"},
		{b, "betanode", 2, "one ordinary live edge plus one world-closed but LIVE edge"},
		{c, "gammanode", 0, "its only edge was invalidated"},
		{d, "deltanode", 2, "the world-closed edge plus a self-loop counted once"},
		{lone, "lonelynode", 0, "a node with no edges still appears, with degree 0"},
	} {
		got, ok := byID[want.id]
		if !ok {
			t.Errorf("%s (%d) has no candidate row at all", want.name, want.id)
			continue
		}
		if got.Degree != want.degree {
			t.Errorf("%s: Degree = %d, want %d — %s", want.name, got.Degree, want.degree, want.why)
		}
	}
}

// TestContractAnchorCandidatesAliasesJoinSortedToTheRightNode pins the alias
// clause: rows join from kg_node_aliases to their OWN node, ascending, and a
// node with no aliases carries an empty list rather than a phantom entry.
//
// The aliases are inserted in reverse order so "sorted ascending" is a real
// assertion rather than a restatement of insertion order.
func TestContractAnchorCandidatesAliasesJoinSortedToTheRightNode(t *testing.T) {
	s := openGraphStore(t)
	withAliases := contractAnchorNode(t, s, "markdown artifacts")
	bare := contractAnchorNode(t, s, "zephyrine")

	for _, a := range []string{"zulu prose", "mike prose", "alpha prose"} {
		if err := s.AddAlias(withAliases, a); err != nil {
			t.Fatalf("add alias %q: %v", a, err)
		}
	}

	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	byID := contractAnchorByID(cands)

	got := byID[withAliases].Aliases
	want := []string{"alpha prose", "mike prose", "zulu prose"}
	if len(got) != len(want) {
		t.Fatalf("node %d aliases = %v, want %v", withAliases, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("node %d aliases = %v, want them ascending: %v", withAliases, got, want)
		}
	}
	if n := len(byID[bare].Aliases); n != 0 {
		t.Errorf("a node with no alias rows carries %d alias(es) %v — aliases must join to their OWN node",
			n, byID[bare].Aliases)
	}
}

// TestContractAnchorCandidatesIsReadOnlyAndRepeatable pins "read-only: no
// write, no schema change" at the strongest point a test can reach it: the
// graph's own census is unchanged across the call, and two calls in a row
// answer identically. This read runs on EVERY search, so a write hiding in it
// would be a write on every query the daemon serves.
func TestContractAnchorCandidatesIsReadOnlyAndRepeatable(t *testing.T) {
	s := openGraphStore(t)
	a := contractAnchorNode(t, s, "alphanode")
	b := contractAnchorNode(t, s, "betanode")
	contractAnchorEdge(t, s, a, b, "touches", "alphanode touches betanode")
	if err := s.AddAlias(a, "the alpha"); err != nil {
		t.Fatalf("add alias: %v", err)
	}

	before, err := s.GetGraphStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	first, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	second, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates (second call): %v", err)
	}
	after, err := s.GetGraphStats()
	if err != nil {
		t.Fatalf("stats: %v", err)
	}

	if before != after {
		t.Errorf("the graph census changed across the read: %+v then %+v", before, after)
	}
	if len(first) != len(second) {
		t.Fatalf("two consecutive reads returned %d and %d rows", len(first), len(second))
	}
	for i := range first {
		if first[i].NodeID != second[i].NodeID || first[i].Name != second[i].Name || first[i].Degree != second[i].Degree {
			t.Fatalf("row %d differs between two reads: %+v vs %+v", i, first[i], second[i])
		}
		if len(first[i].Aliases) != len(second[i].Aliases) {
			t.Fatalf("row %d alias count differs between two reads: %v vs %v", i, first[i].Aliases, second[i].Aliases)
		}
	}
}
