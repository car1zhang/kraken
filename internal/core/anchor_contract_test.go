package core

import (
	"sort"
	"testing"

	"kraken/internal/store"
)

// anchor_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, Slice 2: `core.AnchorMaxDegree`,
// `core.AnchorNodes`, `core.GraphResult.Anchors` and the three ways
// `core.GraphSearch` changes (name leg, boost, zero-anchor no-op).
//
// Written from the contract table alone. internal/core/relevance.go and
// internal/core/graphsearch.go were NOT read; every assertion below is
// transcribed from a contract ROW, and every symbol it names is one the
// contract freezes.
//
// Dimension: LOGIC — the stated invariants and the happy paths. Boundaries,
// hostile inputs and error paths are athena-edge's ground; where an invariant
// IS stated as an absolute ("result is never nil on ANY input", "at exactly 30:
// anchors") it is pinned here because the happy path's meaning depends on it.
//
// Package-level identifiers are prefixed `contractAnchor` — a sibling author
// shares this package's test namespace.

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// contractAnchorCand builds one candidate row. Variadic aliases so the common
// no-alias case reads as a name and a degree, which is what most rows are.
func contractAnchorCand(id int64, name string, degree int, aliases ...string) store.AnchorCandidate {
	return store.AnchorCandidate{NodeID: id, Name: name, Aliases: aliases, Degree: degree}
}

// contractAnchorIDs renders an anchor set as a sorted, printable list so a
// mismatch names the nodes rather than a map address.
func contractAnchorIDs(m map[int64]bool) []int64 {
	out := []int64{}
	for id, ok := range m {
		if ok {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func contractAnchorEqual(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// contractAnchorSeed writes a fixture in order and returns the edge ids in
// FIXTURE order plus the node ids by name. seedGraph keys its edge map by the
// fact sentence, which cannot express a fixture whose two facts are the same
// sentence — and that fixture is what the boost test needs.
func contractAnchorSeed(t *testing.T, s *store.Store, fx []fixtureEdge) ([]int64, map[string]int64) {
	t.Helper()
	nodes := map[string]int64{}
	node := func(name string) int64 {
		if id, ok := nodes[name]; ok {
			return id
		}
		id, err := s.UpsertNode(store.GraphNode{Name: name, Type: "concept"})
		if err != nil {
			t.Fatalf("upsert node %q: %v", name, err)
		}
		nodes[name] = id
		return id
	}
	ids := make([]int64, 0, len(fx))
	for _, e := range fx {
		occ := e.occ
		if occ <= 0 {
			occ = 1
		}
		id, added, err := s.InsertEdge(store.GraphEdge{
			SrcID: node(e.src), DstID: node(e.dst), Predicate: e.pred,
			Fact: e.fact, Strength: 5, Occurrences: occ,
		})
		if err != nil || !added {
			t.Fatalf("seed edge %q: id=%d added=%v err=%v", e.fact, id, added, err)
		}
		if e.vec != nil {
			if err := s.SetEmbedding("edge", id, EncodeVector(e.vec)); err != nil {
				t.Fatalf("embed edge %q: %v", e.fact, err)
			}
		}
		ids = append(ids, id)
	}
	return ids, nodes
}

// ---------------------------------------------------------------------------
// AnchorMaxDegree / nameBoost — the two tunables the contract names by value
// ---------------------------------------------------------------------------

// TestContractAnchorMaxDegreeIsThirty pins the guard by LITERAL value. Q1 is
// settled at 30 and the number IS the contract: a test that computed the
// boundary from the constant would pass no matter what the constant became,
// and the whole point of the guard is that a hub with hundreds of edges never
// drags its neighbourhood into every answer.
func TestContractAnchorMaxDegreeIsThirty(t *testing.T) {
	if AnchorMaxDegree != 30 {
		t.Errorf("AnchorMaxDegree = %v, want 30 — the settled Q1 degree guard", AnchorMaxDegree)
	}
}

// TestContractAnchorNameBoostIsTwo pins the rerank multiplier the contract
// names in slice 2 row (c). Same reasoning as above: the number is the
// contract, not an implementation detail, because it is what decides whether an
// anchored fact can climb past a lexically stronger one.
func TestContractAnchorNameBoostIsTwo(t *testing.T) {
	if nameBoost != 2.0 {
		t.Errorf("nameBoost = %v, want 2.0", nameBoost)
	}
}

// ---------------------------------------------------------------------------
// AnchorNodes — the matching rule
// ---------------------------------------------------------------------------

// TestContractAnchorNodesMatchingRule is the resolver's stated rule, case by
// case, over one shared candidate list. Each row is one clause of the contract
// sentence "a candidate anchors iff its Degree <= AnchorMaxDegree AND at least
// one of {canonical Name, any alias} matches", where matching is
// all-tokens-present under ContentTerms.
//
// The candidates are shared across rows deliberately: every query is asked of
// the WHOLE list, so a rule that over-matches shows up as an extra id rather
// than as a silent pass.
func TestContractAnchorNodesMatchingRule(t *testing.T) {
	const (
		lewis    = int64(1) // single-token name
		hamilton = int64(2) // two-token name
		markdown = int64(3) // matched only through an alias
		ada      = int64(4) // every token under GraphTermMinRunes
		lovelace = int64(5) // one short token, one long one
	)
	cands := []store.AnchorCandidate{
		contractAnchorCand(lewis, "lewis", 3),
		contractAnchorCand(hamilton, "lewis hamilton", 4),
		contractAnchorCand(markdown, "markdown artifacts", 2, "artifact prose"),
		contractAnchorCand(ada, "ada", 1),
		contractAnchorCand(lovelace, "ada lovelace", 1),
	}

	for _, c := range []struct {
		name  string
		query string
		want  []int64
	}{
		{
			// The headline case: a query carrying a node's single-token name
			// anchors that node. "who is" contributes nothing — both words are
			// under GraphTermMinRunes — which is exactly why the feature works
			// on the question shape it was built for.
			"single-token name",
			"who is lewis",
			[]int64{lewis},
		},
		{
			// A multi-word name needs EVERY one of its ≥4-rune tokens present.
			// Here both are, so the two-token node joins the single-token one:
			// "lewis" is still contained in the query.
			"multi-token name, all tokens present",
			"who is lewis hamilton",
			[]int64{lewis, hamilton},
		},
		{
			// Order-insensitive: the query is a bag of terms on both sides.
			"multi-token name, reversed order",
			"hamilton lewis notes",
			[]int64{lewis, hamilton},
		},
		{
			// The negative half of the all-tokens rule, and the one a naive
			// any-token implementation gets wrong: half a name is not a match.
			// (`lewis` still anchors — its whole name IS present.)
			"multi-token name, one token missing",
			"hamilton drove yesterday",
			nil,
		},
		{
			// An alias anchors its node when the canonical name appears
			// nowhere in the query — the lewis-shape this feature exists for.
			"alias matches when the canonical name does not",
			"artifact prose rules",
			[]int64{markdown},
		},
		{
			// Case folds on both sides, via ContentTerms' own lowering.
			"case-insensitive",
			"WHO IS LEWIS HAMILTON",
			[]int64{lewis, hamilton},
		},
		{
			// A name whose every token is under GraphTermMinRunes can never
			// anchor: ContentTerms("ada") is empty, and the rule requires it to
			// be non-empty. The node named "ada lovelace" DOES anchor on the
			// same query, because its short token drops out of the NAME side
			// exactly as it drops out of the query side.
			"short tokens drop out of the name side too",
			"ada lovelace papers",
			[]int64{lovelace},
		},
		{
			// And the short-name node stays unanchored even when its exact
			// name is the whole query.
			"a name of only short tokens never anchors",
			"ada",
			nil,
		},
		{
			"a query matching nothing anchors nothing",
			"porcupines migrate through basalt quarries",
			nil,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := contractAnchorIDs(AnchorNodes(ContentTerms(c.query), cands))
			want := c.want
			if want == nil {
				want = []int64{}
			}
			if !contractAnchorEqual(got, want) {
				t.Errorf("AnchorNodes(ContentTerms(%q)) = %v, want %v", c.query, got, want)
			}
		})
	}
}

// TestContractAnchorNodesDegreeGuard pins Q1's settled boundary from both
// sides on an OTHERWISE PERFECT match, which is the only way to observe the
// guard at all: each candidate below is named exactly by the query, so the only
// thing that can exclude one is its degree.
//
// The boundary is inclusive — "at exactly 30: anchors" — and it is checked at
// 29, 30 and 31 rather than only at 30, because an off-by-one lands on 29 or 31
// and a single-point check cannot see which side it fell.
func TestContractAnchorNodesDegreeGuard(t *testing.T) {
	terms := ContentTerms("who is lewis")
	for _, c := range []struct {
		degree int
		want   bool
	}{
		{0, true}, // a node with no edges at all is the ideal anchor
		{1, true},
		{AnchorMaxDegree - 1, true},
		{AnchorMaxDegree, true},      // inclusive: "at exactly 30: anchors"
		{AnchorMaxDegree + 1, false}, // "never anchors, even on exact name match"
		{500, false},
	} {
		got := AnchorNodes(terms, []store.AnchorCandidate{contractAnchorCand(7, "lewis", c.degree)})
		if got[7] != c.want {
			t.Errorf("degree %d: anchored = %v, want %v (guard is %d, inclusive)",
				c.degree, got[7], c.want, AnchorMaxDegree)
		}
	}
}

// TestContractAnchorNodesEmptyInputsAreEmptySets pins the two stated empties
// and the never-nil promise. Callers index the result without checking it, so
// a nil map returned on some input path is a panic waiting for the first query
// made entirely of short words.
func TestContractAnchorNodesEmptyInputsAreEmptySets(t *testing.T) {
	cands := []store.AnchorCandidate{contractAnchorCand(1, "lewis", 2)}
	for _, c := range []struct {
		name  string
		terms map[string]bool
		cands []store.AnchorCandidate
	}{
		{"empty terms", map[string]bool{}, cands},
		{"nil terms", nil, cands},
		{"terms of only short words", ContentTerms("who is a to of"), cands},
		{"empty candidates", ContentTerms("who is lewis"), []store.AnchorCandidate{}},
		{"nil candidates", ContentTerms("who is lewis"), nil},
		{"nothing at all", nil, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := AnchorNodes(c.terms, c.cands)
			if got == nil {
				t.Fatal("AnchorNodes returned nil — the result is never nil on any input")
			}
			if len(contractAnchorIDs(got)) != 0 {
				t.Errorf("AnchorNodes = %v, want an empty set", contractAnchorIDs(got))
			}
		})
	}
}

// TestContractAnchorNodesTreatsAnEmptyAliasListAlike pins the row's stated
// "empty alias list is nil or empty, callers must treat alike": the two
// spellings of "this node has no aliases" must produce the same verdict.
func TestContractAnchorNodesTreatsAnEmptyAliasListAlike(t *testing.T) {
	terms := ContentTerms("who is lewis")
	nilAliases := store.AnchorCandidate{NodeID: 1, Name: "lewis", Aliases: nil, Degree: 2}
	emptyAliases := store.AnchorCandidate{NodeID: 1, Name: "lewis", Aliases: []string{}, Degree: 2}
	if !AnchorNodes(terms, []store.AnchorCandidate{nilAliases})[1] {
		t.Error("a candidate with nil Aliases did not anchor on its canonical name")
	}
	if !AnchorNodes(terms, []store.AnchorCandidate{emptyAliases})[1] {
		t.Error("a candidate with an empty Aliases slice did not anchor on its canonical name")
	}
}

// ---------------------------------------------------------------------------
// GraphSearch — Anchors, the name leg, the boost
// ---------------------------------------------------------------------------

// contractAnchorLewisFixture is the shape 52-testing-plan.md names: one
// low-degree person node whose facts do NOT repeat its name, plus an unrelated
// fact that the query's other terms retrieve lexically.
//
// The first fact is the load-bearing one. Its sentence contains neither
// "zephyrine" nor any query term, and no embedder is configured in a test, so
// NEITHER existing leg can nominate it. If it comes back at all, it came back
// through the name leg.
func contractAnchorLewisFixture() []fixtureEdge {
	return []fixtureEdge{
		{src: "zephyrine", dst: "cofounder", pred: "served-as",
			fact: "the cofounder signed the charter in autumn"},
		{src: "othernode", dst: "morenode", pred: "records",
			fact: "quasar drift alignment holds"},
		{src: "cofounder", dst: "faraway", pred: "mentions",
			fact: "an unrelated distant claim about nothing"},
	}
}

// TestContractAnchorGraphSearchNameLegNominates is slice 2 row (b): with a
// non-empty anchor set, the anchored node's edges join fusion as a third rank
// list. Expansion is switched OFF (ExpandCap -1) so the only routes into the
// answer are the three retrieval legs — otherwise a fact reached by 1-hop
// expansion would be indistinguishable from one nominated by name.
func TestContractAnchorGraphSearchNameLegNominates(t *testing.T) {
	s := openSearchStore(t)
	ids, nodes := contractAnchorSeed(t, s, contractAnchorLewisFixture())

	r, err := GraphSearch(s, nil, "zephyrine", GraphSearchOpts{ExpandCap: -1, Limit: 10})
	if err != nil {
		t.Fatalf("anchored search errored: %v", err)
	}
	got := edgeIDs(r)
	if len(got) != 1 || got[0] != ids[0] {
		t.Fatalf("search for the alias-free name returned %v, want just the anchored node's fact %d — "+
			"its sentence carries no query term and there is no embedder, so the name leg is the only route in",
			got, ids[0])
	}
	if !contractAnchorEqual(contractAnchorIDs(r.Anchors), []int64{nodes["zephyrine"]}) {
		t.Errorf("Anchors = %v, want exactly the one resolved node %d", contractAnchorIDs(r.Anchors), nodes["zephyrine"])
	}
	// Structure still cannot nominate: the third fixture edge hangs off
	// "cofounder", an ENDPOINT of the anchored edge but not itself an anchor,
	// so NeighborEdges(anchorIDs) never returns it and it must not appear.
	for _, e := range r.Edges {
		if e.ID == ids[2] {
			t.Error("an edge one hop past the anchor was nominated — only edges NeighborEdges returns for ANCHOR nodes join the name leg")
		}
	}
}

// TestContractAnchorGraphSearchZeroAnchorsDoesNotNominate is the same fixture
// from the other side, and it is the zero-anchor invariant's operative half at
// this layer: a query that names nothing must leave the anchored-only fact
// exactly where it was — out of the answer — and must report an empty (but
// non-nil) anchor set.
func TestContractAnchorGraphSearchZeroAnchorsDoesNotNominate(t *testing.T) {
	s := openSearchStore(t)
	ids, _ := contractAnchorSeed(t, s, contractAnchorLewisFixture())

	r, err := GraphSearch(s, nil, "quasar drift", GraphSearchOpts{ExpandCap: -1, Limit: 10})
	if err != nil {
		t.Fatalf("anchor-free search errored: %v", err)
	}
	if r.Anchors == nil {
		t.Fatal("Anchors is nil on a successful search — callers index it without checking")
	}
	if n := len(contractAnchorIDs(r.Anchors)); n != 0 {
		t.Errorf("Anchors = %v on a query naming no node, want empty", contractAnchorIDs(r.Anchors))
	}
	got := edgeIDs(r)
	if len(got) != 1 || got[0] != ids[1] {
		t.Fatalf("anchor-free search returned %v, want just the lexically-matching fact %d — "+
			"with no anchor the name leg must not run at all", got, ids[1])
	}
}

// TestContractAnchorGraphSearchAnchorsIsNonNilOnEveryReturn pins the field's
// stated "non-nil on every successful GraphSearch return" across the three
// shapes a caller reaches: an empty query, a query matching nothing, and an
// ordinary lexical hit.
func TestContractAnchorGraphSearchAnchorsIsNonNilOnEveryReturn(t *testing.T) {
	s := openSearchStore(t)
	contractAnchorSeed(t, s, contractAnchorLewisFixture())

	for _, q := range []string{"   ", "", "somethingnobodywrote", "quasar drift", "zephyrine"} {
		r, err := GraphSearch(s, nil, q, GraphSearchOpts{ExpandCap: -1, Limit: 10})
		if err != nil {
			t.Fatalf("GraphSearch(%q) errored: %v", q, err)
		}
		if r.Anchors == nil {
			t.Errorf("GraphSearch(%q): Anchors is nil", q)
		}
		if r.Nodes == nil || r.Scores == nil {
			t.Errorf("GraphSearch(%q): the pre-existing maps must stay non-nil too", q)
		}
	}
}

// TestContractAnchorGraphSearchBoostFlipsTheOrder is row (c) stated as the
// thing it is for: an anchored edge outranks a non-anchored one it would
// otherwise lose to.
//
// The fixture is two edges on disjoint node pairs whose sentences are the same
// four words but for a final one that appears in no query, so nothing but rank
// position separates them lexically. The baseline run establishes which one the
// un-anchored pipeline puts second — that is the LOSER, and it is the loser
// whose node name the second query then carries. Reading the baseline rather
// than assuming it is what makes this a test of the boost rather than of FTS5's
// tie-breaking.
func TestContractAnchorGraphSearchBoostFlipsTheOrder(t *testing.T) {
	s := openSearchStore(t)
	ids, nodes := contractAnchorSeed(t, s, []fixtureEdge{
		{src: "aurorine", dst: "targetone", pred: "states", fact: "quasar drift alignment holds"},
		{src: "bunkerine", dst: "targettwo", pred: "states", fact: "quasar drift alignment stands"},
	})
	srcName := map[int64]string{ids[0]: "aurorine", ids[1]: "bunkerine"}

	base, err := GraphSearch(s, nil, "quasar drift alignment", GraphSearchOpts{ExpandCap: -1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(base.Edges) != 2 {
		t.Fatalf("baseline returned %v, want both facts — the fixture cannot show a flip otherwise", edgeIDs(base))
	}
	if len(contractAnchorIDs(base.Anchors)) != 0 {
		t.Fatalf("the baseline query anchored %v; it names no node and must anchor nothing", contractAnchorIDs(base.Anchors))
	}
	winner, loser := base.Edges[0], base.Edges[1]
	if base.Scores[winner.ID] < base.Scores[loser.ID] {
		t.Fatalf("baseline order disagrees with its own scores: %v", base.Scores)
	}

	// The loser's own node name, which appears in no fact sentence — so the
	// lexical leg sees the same corpus and the same relative ranks.
	q := "quasar drift alignment " + srcName[loser.ID]
	boosted, err := GraphSearch(s, nil, q, GraphSearchOpts{ExpandCap: -1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if !contractAnchorEqual(contractAnchorIDs(boosted.Anchors), []int64{nodes[srcName[loser.ID]]}) {
		t.Fatalf("Anchors = %v, want exactly node %q (%d)",
			contractAnchorIDs(boosted.Anchors), srcName[loser.ID], nodes[srcName[loser.ID]])
	}
	if len(boosted.Edges) != 2 {
		t.Fatalf("boosted search returned %v, want both facts still", edgeIDs(boosted))
	}
	if boosted.Edges[0].ID != loser.ID {
		t.Errorf("edge %d anchors the query and still ranks below edge %d (scores %v) — "+
			"an anchored edge must outrank the equal-standing fact it lost to",
			loser.ID, winner.ID, boosted.Scores)
	}
	if boosted.Scores[loser.ID] <= boosted.Scores[winner.ID] {
		t.Errorf("anchored edge %d scored %v against %v for the unanchored edge %d — the boost did not apply",
			loser.ID, boosted.Scores[loser.ID], boosted.Scores[winner.ID], winner.ID)
	}
	// The boost is the ANCHOR's, not the query's: the fact that does not touch
	// the anchor node keeps the score it had before the extra term.
	if boosted.Scores[winner.ID] != base.Scores[winner.ID] {
		t.Errorf("the unanchored edge %d moved from %v to %v — a term that matches no fact and anchors "+
			"another node must not rescore it", winner.ID, base.Scores[winner.ID], boosted.Scores[winner.ID])
	}
}

// TestContractAnchorGraphSearchFloorAppliesToAnchoredEdges is row (f): the
// Floor opt is the RANKING's, and the gate exemption belongs to the jobs and
// api layers. A floor above anything RRF can produce must empty the answer even
// when every candidate is anchored — otherwise `Floor` would silently stop
// meaning what its two existing callers pass it for.
func TestContractAnchorGraphSearchFloorAppliesToAnchoredEdges(t *testing.T) {
	s := openSearchStore(t)
	contractAnchorSeed(t, s, contractAnchorLewisFixture())

	// Sanity: the same query with no floor DOES return the anchored fact, so
	// the emptiness below is the floor's doing and not the fixture's.
	open, err := GraphSearch(s, nil, "zephyrine", GraphSearchOpts{ExpandCap: -1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(open.Edges) == 0 {
		t.Fatal("the anchored fact does not retrieve without a floor; the floor case below would be vacuous")
	}

	high, err := GraphSearch(s, nil, "zephyrine", GraphSearchOpts{ExpandCap: -1, Limit: 10, Floor: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(high.Edges) != 0 {
		t.Errorf("Floor 1 kept %d anchored edge(s) %v — the floor applies to anchored fused scores exactly as to others",
			len(high.Edges), edgeIDs(high))
	}
	if len(high.Scores) != 0 {
		t.Errorf("Floor 1 left %d score(s) behind", len(high.Scores))
	}
	if high.Anchors == nil {
		t.Error("Anchors is nil on a floored-to-empty result")
	}
}

// TestContractAnchorGraphSearchIsDeterministic is row (e). The name leg adds a
// third rank list and a set-valued input, both of which are the classic way to
// lose a stable order to map iteration; the existing determinism test cannot
// see it because its query anchors nothing.
func TestContractAnchorGraphSearchIsDeterministic(t *testing.T) {
	s := openSearchStore(t)
	contractAnchorSeed(t, s, []fixtureEdge{
		{src: "zephyrine", dst: "cofounder", pred: "served-as", fact: "the cofounder signed the charter in autumn"},
		{src: "zephyrine", dst: "harbourside", pred: "lived-at", fact: "she kept a house by the water", occ: 4},
		{src: "zephyrine", dst: "quasarline", pred: "wrote", fact: "quasar drift alignment holds"},
		{src: "othernode", dst: "morenode", pred: "records", fact: "quasar drift alignment also wobbles"},
	})

	var firstTriples string
	var firstAnchors []int64
	for i := 0; i < 8; i++ {
		r, err := GraphSearch(s, nil, "quasar drift zephyrine", GraphSearchOpts{ExpandCap: 5, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		gotTriples, gotAnchors := Triples(r), contractAnchorIDs(r.Anchors)
		if i == 0 {
			firstTriples, firstAnchors = gotTriples, gotAnchors
			if gotTriples == "" {
				t.Fatal("the anchored query returned nothing; determinism over an empty answer proves nothing")
			}
			if len(gotAnchors) == 0 {
				t.Fatal("the query named a seeded node and anchored nothing")
			}
			continue
		}
		if gotTriples != firstTriples {
			t.Fatalf("run %d differed:\n%s\nvs\n%s", i, gotTriples, firstTriples)
		}
		if !contractAnchorEqual(gotAnchors, firstAnchors) {
			t.Fatalf("run %d resolved anchors %v, first run resolved %v", i, gotAnchors, firstAnchors)
		}
	}
}

// TestContractAnchorGraphSearchAnchorsAreExactlyTheResolvedNodes ties the
// field to the resolver rather than to whatever the search happened to touch:
// the ids in GraphResult.Anchors must be exactly AnchorNodes' answer over the
// store's own candidate rows for the same query. A search that reported its
// RETRIEVED node ids there would pass every other test in this file.
func TestContractAnchorGraphSearchAnchorsAreExactlyTheResolvedNodes(t *testing.T) {
	s := openSearchStore(t)
	contractAnchorSeed(t, s, contractAnchorLewisFixture())

	const q = "zephyrine quasar drift"
	cands, err := s.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	want := contractAnchorIDs(AnchorNodes(ContentTerms(q), cands))
	if len(want) == 0 {
		t.Fatal("the fixture resolves no anchors for this query; the comparison would be vacuous")
	}

	r, err := GraphSearch(s, nil, q, GraphSearchOpts{ExpandCap: -1, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := contractAnchorIDs(r.Anchors); !contractAnchorEqual(got, want) {
		t.Errorf("GraphResult.Anchors = %v, want AnchorNodes' own answer %v", got, want)
	}
}
