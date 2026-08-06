package core

import (
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	"kraken/internal/store"
)

// anchor_edge_test.go — the EDGE dimension of 50-contract.md Slice 2:
// core.AnchorNodes' boundaries and refusals, and the parts of the extended
// core.GraphSearch that are about a LIMIT rather than about a happy path.
// Written blind: neither relevance.go nor graphsearch.go was read.
//
// The load-bearing refusal here is the degree guard. Anchoring is a bypass of
// the relevance gate further down the pipeline, so a candidate that anchors
// when it should not does not merely rank badly — it injects a hub's facts
// into a conversation that never asked for them. Half the cases below are
// therefore about the ways a match can be claimed that the contract forbids:
// a name whose tokens all fall under the rune gate, a multi-token name missing
// one token from the query, and — the quiet one — a BLANK name, whose empty
// token set makes "every token is present" vacuously true.
//
// Every package-level identifier is prefixed `edgeAnchor`: this package's test
// namespace already carries a `kgedge*` family and an unprefixed helper
// collides with the sibling blind author's file.

// --- fixtures --------------------------------------------------------------

// edgeAnchorOpenStore opens a scratch store for the GraphSearch cases.
func edgeAnchorOpenStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// edgeAnchorCand builds one candidate row. AnchorNodes is pure over its
// inputs, so every matching case can be written without a store at all —
// which is what lets the degree boundary be walked value by value.
func edgeAnchorCand(id int64, name string, degree int, aliases ...string) store.AnchorCandidate {
	return store.AnchorCandidate{NodeID: id, Name: name, Aliases: aliases, Degree: degree}
}

// edgeAnchorHub seeds one node named "hubnode" with `leaves` live edges to
// `leaves` distinct leaves, and returns the hub's id.
//
// The fixture's whole point is that NOTHING retrieves these edges except the
// name leg: no fact sentence contains "hubnode", the leaf names are single
// tokens that appear in no query below, and no embedding is written, so the
// vector leg is empty. Whatever comes back for the query "hubnode" arrived
// because the node anchored.
func edgeAnchorHub(t *testing.T, s *store.Store, leaves int) int64 {
	t.Helper()
	hub, err := s.UpsertNode(store.GraphNode{Name: "hubnode", Type: "component"})
	if err != nil {
		t.Fatalf("upsert hub: %v", err)
	}
	for i := 0; i < leaves; i++ {
		leaf, err := s.UpsertNode(store.GraphNode{Name: fmt.Sprintf("leafnode%02d", i)})
		if err != nil {
			t.Fatalf("upsert leaf %d: %v", i, err)
		}
		if _, added, err := s.InsertEdge(store.GraphEdge{
			SrcID: hub, DstID: leaf, Predicate: "touches",
			Fact: fmt.Sprintf("quiet wiring number %02d hums", i), Strength: 5, Occurrences: 1,
		}); err != nil || !added {
			t.Fatalf("seed edge %d: added=%v err=%v", i, added, err)
		}
	}
	return hub
}

// edgeAnchorSorted flattens an anchor set into a comparable, ordered list.
func edgeAnchorSorted(m map[int64]bool) []int64 {
	out := make([]int64, 0, len(m))
	for id, on := range m {
		if on {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// edgeAnchorEdgeIDs flattens a result's edge ids in order.
func edgeAnchorEdgeIDs(r GraphResult) []int64 {
	out := make([]int64, 0, len(r.Edges))
	for _, e := range r.Edges {
		out = append(out, e.ID)
	}
	return out
}

// --- AnchorNodes: the degree guard -----------------------------------------

// TestEdgeAnchorNodesDegreeGuardBoundary walks the guard value by value across
// the line, on an EXACT single-token name match — so the only thing deciding
// each row is the degree. The contract settles Q1 at 30 and states that 30
// itself anchors, which is the half an implementation written as `< max`
// silently gets wrong: one node, the most connected person in the graph minus
// one edge, quietly stops answering.
func TestEdgeAnchorNodesDegreeGuardBoundary(t *testing.T) {
	if AnchorMaxDegree != 30 {
		t.Fatalf("AnchorMaxDegree = %d, want 30 — Q1 is settled and the guard is the one tunable this slice adds", AnchorMaxDegree)
	}
	terms := ContentTerms("what do we know about lewis")
	for _, tc := range []struct {
		degree int
		want   bool
	}{
		{0, true},
		{1, true},
		{AnchorMaxDegree - 1, true},
		{AnchorMaxDegree, true},
		{AnchorMaxDegree + 1, false},
		{1000, false},
	} {
		got := AnchorNodes(terms, []store.AnchorCandidate{edgeAnchorCand(7, "lewis", tc.degree)})
		if got[7] != tc.want {
			t.Errorf("degree %d: anchored = %v, want %v (guard is %d, and %d itself must anchor)",
				tc.degree, got[7], tc.want, AnchorMaxDegree, AnchorMaxDegree)
		}
		if n := len(edgeAnchorSorted(got)); (n > 0) != tc.want {
			t.Errorf("degree %d: %d anchors, want %v", tc.degree, n, tc.want)
		}
	}
}

// TestEdgeAnchorNodesGuardBeatsAnAliasMatchToo: the guard is a property of the
// CANDIDATE, not of which string matched. An implementation that checked the
// degree on the canonical-name arm and forgot it on the alias arm passes every
// name-side case and lets the busiest node in the graph anchor on a nickname.
func TestEdgeAnchorNodesGuardBeatsAnAliasMatchToo(t *testing.T) {
	terms := ContentTerms("ask dodgson about the manuscript")
	under := store.AnchorCandidate{NodeID: 1, Name: "lewis carroll",
		Aliases: []string{"charles", "dodgson"}, Degree: AnchorMaxDegree}
	over := store.AnchorCandidate{NodeID: 2, Name: "another person",
		Aliases: []string{"dodgson"}, Degree: AnchorMaxDegree + 1}

	got := AnchorNodes(terms, []store.AnchorCandidate{under, over})
	if !got[1] {
		t.Errorf("a candidate at exactly the guard did not anchor on its alias: %v", got)
	}
	if got[2] {
		t.Errorf("a candidate one over the guard anchored on an alias: %v", got)
	}
	// One matching alias out of several is enough; "charles" being absent from
	// the query must not veto "dodgson" being present.
	if len(edgeAnchorSorted(got)) != 1 {
		t.Errorf("anchors = %v, want exactly the under-guard node", edgeAnchorSorted(got))
	}
}

// --- AnchorNodes: what can never match -------------------------------------

// TestEdgeAnchorNodesNameOfOnlyShortTokensNeverAnchors pins Q5's accepted
// limitation as a REFUSAL rather than as a gap. Every one of these names is
// spelled out verbatim in the query, so the only reason none anchors is that
// ContentTerms drops its every token at the 4-rune floor — which leaves the
// name side with an empty token set, and an empty set must never be treated as
// "all its tokens are present".
func TestEdgeAnchorNodesNameOfOnlyShortTokensNeverAnchors(t *testing.T) {
	terms := ContentTerms("the cj bot worked overtime with ktm and va")
	if len(terms) == 0 {
		t.Fatal("fixture query has no content terms at all; the case below would be the empty-terms case instead")
	}
	cands := []store.AnchorCandidate{
		edgeAnchorCand(1, "cj", 0),
		edgeAnchorCand(2, "the bot", 0),
		edgeAnchorCand(3, "a b c", 0),
		edgeAnchorCand(4, "ktm", 0, "va"),
	}
	if got := AnchorNodes(terms, cands); len(edgeAnchorSorted(got)) != 0 {
		t.Errorf("anchors = %v, want none — a name whose every token is under GraphTermMinRunes (%d) can never anchor",
			edgeAnchorSorted(got), GraphTermMinRunes)
	}
	// The rune floor is counted in RUNES on the name side too: three CJK
	// characters are 9 bytes and 3 runes, so the node named with them can never
	// anchor while its four-rune neighbour can.
	cjk := []store.AnchorCandidate{edgeAnchorCand(5, "知知知", 0), edgeAnchorCand(6, "知知知知", 0)}
	got := AnchorNodes(ContentTerms("知知知 知知知知"), cjk)
	if got[5] {
		t.Error("a 3-rune (9-byte) name anchored — the name side is counting bytes, not runes")
	}
	if !got[6] {
		t.Errorf("a 4-rune name did not anchor: %v", got)
	}
}

// TestEdgeAnchorNodesUnnamedCandidatesNeverAnchor is the vacuous-truth trap,
// and it is the single most dangerous edge in this slice: "every element of
// ContentTerms(name) is present in terms" is TRUE of every query when the name
// contributes no elements. A row with a blank name is not hypothetical —
// nothing in the schema forbids one, and the answer for it is "match nothing",
// not "match everything". Run against an empty term set as well, where the
// bug would anchor the whole graph on a query with no content words.
func TestEdgeAnchorNodesUnnamedCandidatesNeverAnchor(t *testing.T) {
	cands := []store.AnchorCandidate{
		{NodeID: 1, Name: "", Degree: 0},
		{NodeID: 2, Name: "   \t\n ", Degree: 0},
		{NodeID: 3, Name: "-,.;:!?()[]", Degree: 0},
		{NodeID: 4, Name: "carroll", Aliases: []string{"", "   ", "-"}, Degree: 0},
	}
	for name, terms := range map[string]map[string]bool{
		"nil terms":        nil,
		"empty terms":      {},
		"unrelated query":  ContentTerms("something else entirely"),
		"query with words": ContentTerms("alice through the looking glass"),
	} {
		if got := AnchorNodes(terms, cands); len(edgeAnchorSorted(got)) != 0 {
			t.Errorf("%s: anchors = %v, want none — an empty name/alias token set matches NOTHING, not everything",
				name, edgeAnchorSorted(got))
		}
	}
	// The same candidate DOES anchor once its real name is in the query, which
	// is what proves the rows above were rejected for their blank strings and
	// not because the fixture could never anchor at all.
	if got := AnchorNodes(ContentTerms("carroll wrote alice"), cands); !got[4] {
		t.Errorf("the candidate with a real name and blank aliases did not anchor on its name: %v", got)
	}
}

// TestEdgeAnchorNodesMultiTokenNameNeedsEveryToken pins "all tokens present"
// from the side that costs a match. One token short is the common case in
// practice — a query says "lewis" and the node is "lewis carroll" — and the
// contract's answer is that it does not anchor. Order-insensitivity is checked
// beside it because an implementation reaching for substring containment would
// pass the missing-token row and fail this one.
func TestEdgeAnchorNodesMultiTokenNameNeedsEveryToken(t *testing.T) {
	cand := []store.AnchorCandidate{edgeAnchorCand(5, "lewis carroll", 2)}

	for _, q := range []string{
		"what did lewis say",         // first token only
		"carroll wrote it",           // second token only
		"lewis and charles disagree", // one token plus a decoy
	} {
		if got := AnchorNodes(ContentTerms(q), cand); got[5] {
			t.Errorf("query %q anchored a two-token name with one token missing", q)
		}
	}
	for _, q := range []string{
		"lewis carroll wrote alice",
		"carroll and lewis both signed",     // reversed
		"CARROLL, then LEWIS, in that file", // reversed and shouted
	} {
		if got := AnchorNodes(ContentTerms(q), cand); !got[5] {
			t.Errorf("query %q did not anchor — every token is present and order must not matter", q)
		}
	}

	// A multi-token ALIAS is held to the same rule.
	aliased := []store.AnchorCandidate{{NodeID: 6, Name: "someone else",
		Aliases: []string{"charles dodgson"}, Degree: 1}}
	if got := AnchorNodes(ContentTerms("ask dodgson about it"), aliased); got[6] {
		t.Error("a two-token alias anchored on one of its tokens")
	}
	if got := AnchorNodes(ContentTerms("ask charles dodgson about it"), aliased); !got[6] {
		t.Error("a two-token alias did not anchor when both tokens are present")
	}
}

// TestEdgeAnchorNodesShortTokensDropFromTheNameSideToo is the mirror of the
// case above and the reason the contract spells the rule out twice. A name
// carrying a sub-4-rune word ("de", "von", "the") can still anchor, because
// that word is not in the query's term set either — it was dropped from BOTH
// sides. An implementation that tokenized the name without the rune gate would
// demand a token the query can never supply, and every such person would go
// permanently unanchorable.
func TestEdgeAnchorNodesShortTokensDropFromTheNameSideToo(t *testing.T) {
	for _, name := range []string{"de lewis", "lewis the third", "v2 lewis"} {
		cand := []store.AnchorCandidate{edgeAnchorCand(9, name, 1)}
		if got := AnchorNodes(ContentTerms("lewis third alone"), cand); !got[9] {
			t.Errorf("name %q did not anchor on a query carrying its every >=%d-rune token",
				name, GraphTermMinRunes)
		}
	}
}

// TestEdgeAnchorNodesEmptyInputsGiveAnEmptyNonNilSet pins the two empty
// arguments and the never-nil promise together. Callers index this map without
// checking, exactly as they do GraphResult.Nodes/Scores, so a nil return is a
// nil-map read away from being fine and a write away from a panic.
func TestEdgeAnchorNodesEmptyInputsGiveAnEmptyNonNilSet(t *testing.T) {
	cands := []store.AnchorCandidate{
		edgeAnchorCand(1, "lewis", 0),
		edgeAnchorCand(2, "hubnode", 0, "the hub"),
	}
	for name, got := range map[string]map[int64]bool{
		"nil terms":     AnchorNodes(nil, cands),
		"empty terms":   AnchorNodes(map[string]bool{}, cands),
		"nil cands":     AnchorNodes(ContentTerms("who is lewis"), nil),
		"empty cands":   AnchorNodes(ContentTerms("who is lewis"), []store.AnchorCandidate{}),
		"both empty":    AnchorNodes(nil, nil),
		"no match":      AnchorNodes(ContentTerms("xylophone marzipan quokka"), cands),
		"terms of dust": AnchorNodes(ContentTerms("a b c to me"), cands),
	} {
		if got == nil {
			t.Errorf("%s: AnchorNodes returned a nil map — the result is never nil on any input", name)
			continue
		}
		if len(got) != 0 {
			t.Errorf("%s: anchors = %v, want an empty set", name, got)
		}
	}
}

// TestEdgeAnchorNodesIsPureAndRepeatable pins the "Pure." in the contract as
// something observable. The candidate rows come straight off a store read that
// other code shares, and the term set is the query's — a resolver that sorted
// the aliases in place, or that deleted matched terms as it consumed them,
// would corrupt whatever the caller does next with no symptom here at all.
func TestEdgeAnchorNodesIsPureAndRepeatable(t *testing.T) {
	terms := ContentTerms("what did lewis carroll write")
	termsCopy := map[string]bool{}
	for k, v := range terms {
		termsCopy[k] = v
	}
	cands := []store.AnchorCandidate{
		{NodeID: 1, Name: "lewis carroll", Aliases: []string{"zeta", "dodgson", "charles"}, Degree: 3},
		{NodeID: 2, Name: "hubnode", Aliases: nil, Degree: AnchorMaxDegree + 5},
	}
	candsCopy := make([]store.AnchorCandidate, len(cands))
	for i, c := range cands {
		candsCopy[i] = c
		candsCopy[i].Aliases = append([]string(nil), c.Aliases...)
	}

	first := AnchorNodes(terms, cands)
	if !reflect.DeepEqual(terms, termsCopy) {
		t.Errorf("the term set was mutated: %v, want %v", terms, termsCopy)
	}
	for i := range cands {
		if !reflect.DeepEqual(cands[i], candsCopy[i]) {
			t.Errorf("candidate %d was mutated: %+v, want %+v", i, cands[i], candsCopy[i])
		}
	}
	for i := 0; i < 4; i++ {
		if again := AnchorNodes(terms, cands); !reflect.DeepEqual(again, first) {
			t.Fatalf("run %d differs: %v vs %v", i, again, first)
		}
	}
	// Duplicate rows for one node collapse rather than double-counting.
	dupes := []store.AnchorCandidate{edgeAnchorCand(1, "lewis", 1), edgeAnchorCand(1, "lewis", 1)}
	if got := AnchorNodes(ContentTerms("about lewis"), dupes); len(got) != 1 {
		t.Errorf("two rows for one node produced %v, want a single entry", got)
	}
}

// --- GraphSearch: the anchor-shaped boundaries -----------------------------

// TestEdgeAnchorGraphSearchAnchorsAlwaysPresent pins the never-nil half of
// GraphResult.Anchors on the three degenerate answers the pipeline actually
// produces: the empty query every cell spawn sends, the query that matched
// nothing, and the query that DID retrieve but anchored nothing. Every node in
// this fixture is named with a sub-4-rune token, so the anchor set is provably
// empty for structural reasons rather than by accident.
func TestEdgeAnchorGraphSearchAnchorsAlwaysPresent(t *testing.T) {
	s := edgeAnchorOpenStore(t)
	a, err := s.UpsertNode(store.GraphNode{Name: "a"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.UpsertNode(store.GraphNode{Name: "b"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.InsertEdge(store.GraphEdge{SrcID: a, DstID: b, Predicate: "uses",
		Fact: "alpha beta gamma", Strength: 5, Occurrences: 1}); err != nil {
		t.Fatal(err)
	}

	for _, q := range []string{"   ", "", "somethingnobodywrote", "alpha beta"} {
		r, err := GraphSearch(s, nil, q, GraphSearchOpts{ExpandCap: -1, Limit: 10})
		if err != nil {
			t.Fatalf("query %q errored: %v", q, err)
		}
		if r.Anchors == nil {
			t.Errorf("query %q: Anchors is nil — callers index it without checking, exactly as they do Nodes and Scores", q)
			continue
		}
		if len(r.Anchors) != 0 {
			t.Errorf("query %q anchored %v, but every node in the fixture is named with a sub-%d-rune token",
				q, edgeAnchorSorted(r.Anchors), GraphTermMinRunes)
		}
	}
}

// TestEdgeAnchorGraphSearchDegreeGuardAtTheBoundary is the guard observed end
// to end, which is the only place its two halves can be seen doing opposite
// things on the same fixture. Nothing in either graph retrieves "hubnode" —
// no fact sentence contains it, no embedding exists — so the ONLY route into
// the answer is the name leg, and the answer flips from "all of the hub's
// facts" to "nothing at all" on one extra edge.
func TestEdgeAnchorGraphSearchDegreeGuardAtTheBoundary(t *testing.T) {
	at := edgeAnchorOpenStore(t)
	hubAt := edgeAnchorHub(t, at, AnchorMaxDegree)
	rAt, err := GraphSearch(at, nil, "hubnode", GraphSearchOpts{ExpandCap: -1, Limit: 100})
	if err != nil {
		t.Fatalf("search at the guard: %v", err)
	}
	if !rAt.Anchors[hubAt] {
		t.Fatalf("a node with exactly AnchorMaxDegree (%d) live edges did not anchor: %v",
			AnchorMaxDegree, edgeAnchorSorted(rAt.Anchors))
	}
	if len(edgeAnchorSorted(rAt.Anchors)) != 1 {
		t.Errorf("anchors = %v, want only the hub", edgeAnchorSorted(rAt.Anchors))
	}
	if len(rAt.Edges) == 0 {
		t.Fatal("the anchored node nominated no edges — nothing else in this fixture can retrieve them")
	}
	if len(rAt.Edges) > AnchorMaxDegree {
		t.Errorf("got %d edges for a node with %d live edges", len(rAt.Edges), AnchorMaxDegree)
	}
	for _, e := range rAt.Edges {
		if e.SrcID != hubAt && e.DstID != hubAt {
			t.Errorf("edge %d touches neither endpoint of the anchor — the name leg nominated a stranger", e.ID)
		}
		if _, ok := rAt.Scores[e.ID]; !ok {
			t.Errorf("edge %d came back with no score", e.ID)
		}
	}

	over := edgeAnchorOpenStore(t)
	hubOver := edgeAnchorHub(t, over, AnchorMaxDegree+1)
	rOver, err := GraphSearch(over, nil, "hubnode", GraphSearchOpts{ExpandCap: -1, Limit: 100})
	if err != nil {
		t.Fatalf("search one over the guard: %v", err)
	}
	if rOver.Anchors == nil {
		t.Fatal("Anchors is nil on a zero-anchor answer")
	}
	if rOver.Anchors[hubOver] || len(rOver.Anchors) != 0 {
		t.Errorf("a node with %d live edges anchored: %v", AnchorMaxDegree+1, edgeAnchorSorted(rOver.Anchors))
	}
	if len(rOver.Edges) != 0 {
		t.Errorf("a query naming only an over-guard hub returned %d edges %v — with no anchor there is no leg that retrieves them",
			len(rOver.Edges), edgeAnchorEdgeIDs(rOver))
	}
}

// TestEdgeAnchorGraphSearchFloorStillBindsAnchoredEdges pins obligation (f),
// which is the boundary between this slice's two exemptions. The keep-gate
// exemption belongs to the jobs and api layers; RANKING has no exemption at
// all, so a Floor above every producible score empties the answer whether the
// edges were anchored or not. An implementation that "helpfully" spared
// anchored edges from the Floor would make GraphSearchOpts.Floor mean
// different things to different callers.
func TestEdgeAnchorGraphSearchFloorStillBindsAnchoredEdges(t *testing.T) {
	s := edgeAnchorOpenStore(t)
	hub := edgeAnchorHub(t, s, 3)

	// Sanity: without the floor these edges DO come back, so the emptiness
	// below is the floor's doing and not the fixture's.
	open, err := GraphSearch(s, nil, "hubnode", GraphSearchOpts{ExpandCap: -1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(open.Edges) == 0 {
		t.Fatal("the anchored node nominated nothing even without a floor; the case below would be vacuous")
	}

	// 1 is far above anything RRF can produce: the best single-leg contribution
	// is 1/(rrfK+1), and the name boost is a factor of two.
	high, err := GraphSearch(s, nil, "hubnode", GraphSearchOpts{ExpandCap: -1, Limit: 50, Floor: 1})
	if err != nil {
		t.Fatalf("floored search errored: %v", err)
	}
	if len(high.Edges) != 0 {
		t.Errorf("Floor 1 kept %d anchored edges %v — the gate exemption is the jobs/api layer's, not the ranking's",
			len(high.Edges), edgeAnchorEdgeIDs(high))
	}
	if len(high.Scores) != 0 {
		t.Errorf("Floor 1 left %d scores behind", len(high.Scores))
	}
	// The ANCHOR set is not a ranked list and the floor must not empty it: the
	// jobs layer reads it to exempt facts it is about to render, and an empty
	// set there would silently disable the whole feature downstream.
	if !high.Anchors[hub] {
		t.Errorf("Floor 1 emptied the anchor set too: %v", edgeAnchorSorted(high.Anchors))
	}
}

// TestEdgeAnchorNameLegNominatesOnlyTheAnchorsOwnEdges pins the sentence the
// contract puts in capitals: structure still cannot nominate un-retrieved
// edges. An anchored node opens the door for the edges NeighborEdges returns
// for IT, and not for the graph behind them — otherwise anchoring one person
// would walk their whole neighbourhood into the answer, which is the failure
// mode the degree guard exists to bound in the first place.
//
// Expansion is switched OFF (ExpandCap -1), since expansion is a separate,
// already-tested mechanism that would legitimately bring the two-hop edge in
// and would make this assertion untrue for the wrong reason.
func TestEdgeAnchorNameLegNominatesOnlyTheAnchorsOwnEdges(t *testing.T) {
	s := edgeAnchorOpenStore(t)
	anchor, err := s.UpsertNode(store.GraphNode{Name: "zephyrite"})
	if err != nil {
		t.Fatal(err)
	}
	mid, err := s.UpsertNode(store.GraphNode{Name: "middlenode"})
	if err != nil {
		t.Fatal(err)
	}
	far, err := s.UpsertNode(store.GraphNode{Name: "farawaynode"})
	if err != nil {
		t.Fatal(err)
	}
	near, _, err := s.InsertEdge(store.GraphEdge{SrcID: anchor, DstID: mid, Predicate: "touches",
		Fact: "quiet wiring number one hums", Strength: 5, Occurrences: 1})
	if err != nil {
		t.Fatal(err)
	}
	beyond, _, err := s.InsertEdge(store.GraphEdge{SrcID: mid, DstID: far, Predicate: "touches",
		Fact: "quiet wiring number two hums", Strength: 5, Occurrences: 1})
	if err != nil {
		t.Fatal(err)
	}

	r, err := GraphSearch(s, nil, "zephyrite", GraphSearchOpts{ExpandCap: -1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Anchors[anchor] {
		t.Fatalf("the single-token name did not anchor its own node: %v", edgeAnchorSorted(r.Anchors))
	}
	ids := edgeAnchorEdgeIDs(r)
	var sawNear, sawBeyond bool
	for _, id := range ids {
		switch id {
		case near:
			sawNear = true
		case beyond:
			sawBeyond = true
		}
	}
	if !sawNear {
		t.Errorf("the anchor's own edge is missing from %v — its sentence shares no word with the query, so the name leg is its only route in", ids)
	}
	if sawBeyond {
		t.Errorf("a two-hop edge (%d) was nominated by the name leg: %v — only edges NeighborEdges returns for the ANCHOR may join", beyond, ids)
	}
	if _, scored := r.Scores[beyond]; scored {
		t.Errorf("an edge retrieved by no leg acquired a score: %v", r.Scores)
	}
}

// TestEdgeAnchorGraphSearchStaysDeterministicWithAnchors: obligation (e), on
// the path that adds a third rank list and a map-keyed anchor set — two new
// places for map iteration order to leak into the answer. A search whose order
// varied between two identical calls would make every downstream cache and
// every reproducible spawn a lie.
func TestEdgeAnchorGraphSearchStaysDeterministicWithAnchors(t *testing.T) {
	s := edgeAnchorOpenStore(t)
	hub := edgeAnchorHub(t, s, 6)
	if _, err := s.UpsertNode(store.GraphNode{Name: "sidenode"}); err != nil {
		t.Fatal(err)
	}

	var firstTriples string
	var firstIDs, firstAnchors []int64
	for i := 0; i < 8; i++ {
		r, err := GraphSearch(s, nil, "hubnode", GraphSearchOpts{ExpandCap: 5, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		ids, anchors, triples := edgeAnchorEdgeIDs(r), edgeAnchorSorted(r.Anchors), Triples(r)
		if i == 0 {
			firstIDs, firstAnchors, firstTriples = ids, anchors, triples
			if !r.Anchors[hub] {
				t.Fatalf("the hub did not anchor: %v", anchors)
			}
			continue
		}
		if !reflect.DeepEqual(ids, firstIDs) {
			t.Fatalf("run %d reordered the edges: %v vs %v", i, ids, firstIDs)
		}
		if !reflect.DeepEqual(anchors, firstAnchors) {
			t.Fatalf("run %d changed the anchor set: %v vs %v", i, anchors, firstAnchors)
		}
		if triples != firstTriples {
			t.Fatalf("run %d rendered differently:\n%s\nvs\n%s", i, triples, firstTriples)
		}
	}
}

// TestEdgeAnchorGraphSearchInvalidatedAnchorEdgesStayOut: anchoring is a
// nomination, never a resurrection. An invalidated edge is history the graph
// deliberately keeps, and the name leg reads NeighborEdges — which is live-only
// — so an anchored node's retired facts must not reappear just because someone
// said its name.
func TestEdgeAnchorGraphSearchInvalidatedAnchorEdgesStayOut(t *testing.T) {
	s := edgeAnchorOpenStore(t)
	anchor, err := s.UpsertNode(store.GraphNode{Name: "zephyrite"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.UpsertNode(store.GraphNode{Name: "middlenode"})
	if err != nil {
		t.Fatal(err)
	}
	retired, _, err := s.InsertEdge(store.GraphEdge{SrcID: anchor, DstID: other, Predicate: "used",
		Fact: "quiet wiring number one hums", Strength: 5, Occurrences: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InvalidateEdge(retired, 1_700_000_000_000); err != nil {
		t.Fatal(err)
	}

	r, err := GraphSearch(s, nil, "zephyrite", GraphSearchOpts{ExpandCap: -1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range r.Edges {
		if e.ID == retired {
			t.Fatal("an invalidated edge came back through the name leg")
		}
	}
	// The node still anchors — its degree is 0 now, which is well under the
	// guard. Anchoring a node with nothing live to say is an empty answer, not
	// an error.
	if !r.Anchors[anchor] {
		t.Errorf("a node whose only edge was invalidated stopped anchoring: %v", edgeAnchorSorted(r.Anchors))
	}
	if len(r.Edges) != 0 {
		t.Errorf("got %d edges %v, want none", len(r.Edges), edgeAnchorEdgeIDs(r))
	}
}
