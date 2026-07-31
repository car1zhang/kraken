package core

import (
	"path/filepath"
	"testing"

	"kraken/internal/store"
)

func openSearchStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fixtureEdge is one fact to seed. Node names are created on demand.
type fixtureEdge struct {
	src, dst, pred, fact string
	occ                  int64
	vec                  []float32
}

// seedGraph writes the fixture and returns the edge id of each fact, keyed by
// the fact string, plus the node ids by name.
func seedGraph(t *testing.T, s *store.Store, fx []fixtureEdge) (map[string]int64, map[string]int64) {
	t.Helper()
	nodes := map[string]int64{}
	node := func(name string) int64 {
		if id, ok := nodes[name]; ok {
			return id
		}
		id, err := s.UpsertNode(store.GraphNode{Name: name, Type: "concept", Description: name + " gloss"})
		if err != nil {
			t.Fatal(err)
		}
		nodes[name] = id
		return id
	}
	edges := map[string]int64{}
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
				t.Fatal(err)
			}
		}
		edges[e.fact] = id
	}
	return edges, nodes
}

func edgeIDs(r GraphResult) []int64 {
	out := make([]int64, 0, len(r.Edges))
	for _, e := range r.Edges {
		out = append(out, e.ID)
	}
	return out
}

// TestGraphSearchRRFUsesRanksNotScores is the fusion's defining property.
// The same graph is searched twice with cosine candidates whose RANK ORDER is
// identical but whose MAGNITUDES differ by six orders of magnitude. A fusion
// that touched the raw scores would reorder (or at least rescore) the answer;
// RRF on ranks cannot see the difference at all.
func TestGraphSearchRRFUsesRanksNotScores(t *testing.T) {
	small := [][]float32{
		{1, 0, 0, 0},
		{0.9, 0.1, 0, 0},
		{0, 1, 0, 0},
		{0, 0, 1, 0},
	}
	run := func(scale float32) GraphResult {
		s := openSearchStore(t)
		fx := []fixtureEdge{
			{src: "a", dst: "b", pred: "uses", fact: "alpha alpha alpha", vec: scaleVec(small[0], scale)},
			{src: "b", dst: "c", pred: "uses", fact: "alpha beta", vec: scaleVec(small[1], scale)},
			{src: "c", dst: "d", pred: "uses", fact: "gamma delta", vec: scaleVec(small[2], scale)},
			{src: "d", dst: "e", pred: "uses", fact: "epsilon zeta", vec: scaleVec(small[3], scale)},
		}
		seedGraph(t, s, fx)
		q := []float32{0, 0.2, 1, 0} // ranks: gamma-delta, epsilon-zeta, alpha-beta, alpha^3
		r, err := GraphSearch(s, scaleVec(q, scale), "alpha", GraphSearchOpts{ExpandCap: 0, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	a := run(1)
	b := run(1e6)
	// Ids are assigned in the same order by both runs (same fixture, same
	// insertion order), so the two answers must be identical element for
	// element, score for score.
	ga, gb := edgeIDs(a), edgeIDs(b)
	if len(ga) == 0 {
		t.Fatal("no results — fixture did not retrieve")
	}
	if len(ga) != len(gb) {
		t.Fatalf("scaling the vectors changed the result length: %v vs %v", ga, gb)
	}
	for i := range ga {
		if ga[i] != gb[i] {
			t.Fatalf("scaling the vectors reordered the answer: %v vs %v — RRF must fuse RANKS, never scores", ga, gb)
		}
		if a.Scores[ga[i]] != b.Scores[gb[i]] {
			t.Fatalf("edge %d scored %v at scale 1 and %v at scale 1e6 — the fused score must not depend on magnitude",
				ga[i], a.Scores[ga[i]], b.Scores[gb[i]])
		}
	}
}

func scaleVec(v []float32, k float32) []float32 {
	out := make([]float32, len(v))
	for i := range v {
		out[i] = v[i] * k
	}
	return out
}

// TestGraphSearchEmptyIsEmptyNotError: no query terms and no vector means both
// legs are empty. That is an ordinary answer, not a failure — graphContext
// calls this on every cell spawn and must be able to get nothing back.
func TestGraphSearchEmptyIsEmptyNotError(t *testing.T) {
	s := openSearchStore(t)
	seedGraph(t, s, []fixtureEdge{{src: "a", dst: "b", pred: "uses", fact: "alpha beta"}})
	r, err := GraphSearch(s, nil, "   ", GraphSearchOpts{})
	if err != nil {
		t.Fatalf("empty query errored: %v", err)
	}
	if len(r.Edges) != 0 {
		t.Fatalf("empty query returned %d edges", len(r.Edges))
	}
	if r.Nodes == nil || r.Scores == nil {
		t.Fatal("maps must be non-nil even on an empty result — callers index them without checking")
	}
	if got := Triples(r); got != "" {
		t.Fatalf("Triples of an empty result = %q, want empty", got)
	}
}

// TestGraphSearchNoMatchIsEmpty: a query whose terms are in the corpus'
// vocabulary of nothing returns nothing rather than the whole graph.
func TestGraphSearchNoMatchIsEmpty(t *testing.T) {
	s := openSearchStore(t)
	seedGraph(t, s, []fixtureEdge{{src: "a", dst: "b", pred: "uses", fact: "alpha beta"}})
	r, err := GraphSearch(s, nil, "somethingnobodywrote", GraphSearchOpts{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Edges) != 0 {
		t.Fatalf("no-match query returned %v", edgeIDs(r))
	}
}

// hubFixture: one lexically-matching anchor edge plus seven facts hanging off
// the hub it names, each introducing exactly one new node. That shape is what
// makes the expansion cap countable.
func hubFixture() []fixtureEdge {
	fx := []fixtureEdge{{src: "seed", dst: "hub", pred: "reaches", fact: "alpha anchor fact"}}
	for _, n := range []string{"n2", "n3", "n4", "n5", "n6", "n7", "n8"} {
		fx = append(fx, fixtureEdge{src: "hub", dst: n, pred: "touches", fact: "gamma " + n})
	}
	return fx
}

func TestGraphSearchExpansionRespectsCapExactly(t *testing.T) {
	s := openSearchStore(t)
	seedGraph(t, s, hubFixture())

	r, err := GraphSearch(s, nil, "alpha", GraphSearchOpts{ExpandCap: 3, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	// One ranked fact (the only "alpha" match) plus exactly three expansion
	// edges: each brings one new node and the cap is three.
	if len(r.Edges) != 4 {
		t.Fatalf("cap 3: got %d edges %v, want 1 ranked + 3 expanded", len(r.Edges), edgeIDs(r))
	}
	newNodes := map[int64]bool{}
	anchor := r.Edges[0]
	for _, e := range r.Edges[1:] {
		for _, id := range []int64{e.SrcID, e.DstID} {
			if id != anchor.SrcID && id != anchor.DstID {
				newNodes[id] = true
			}
		}
	}
	if len(newNodes) != 3 {
		t.Fatalf("expansion introduced %d new nodes, cap was exactly 3", len(newNodes))
	}
	// Every expanded fact must score strictly below every ranked one.
	for _, e := range r.Edges[1:] {
		if r.Scores[e.ID] >= r.Scores[anchor.ID] {
			t.Fatalf("expanded edge %d scored %v, ranked anchor scored %v — expansion must sit below retrieval",
				e.ID, r.Scores[e.ID], r.Scores[anchor.ID])
		}
	}

	// A wider cap admits more of the same neighbourhood, which is what proves
	// the count above came from the cap and not from the fixture running out.
	wide, err := GraphSearch(s, nil, "alpha", GraphSearchOpts{ExpandCap: 12, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(wide.Edges) != 8 {
		t.Fatalf("cap 12: got %d edges, want all 8 facts", len(wide.Edges))
	}

	// Cap 0 means the default, not "no expansion"; cap is only ever a bound.
	def, err := GraphSearch(s, nil, "alpha", GraphSearchOpts{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Edges) != 8 {
		t.Fatalf("default cap: got %d edges, want all 8", len(def.Edges))
	}
}

// TestGraphSearchCyclicFixtureTerminates: a graph with a cycle must not send
// the expansion round it. It cannot, structurally — expansion is one call to
// NeighborEdges — and this pins that it stays one.
func TestGraphSearchCyclicFixtureTerminates(t *testing.T) {
	s := openSearchStore(t)
	seedGraph(t, s, []fixtureEdge{
		{src: "a", dst: "b", pred: "leads", fact: "cycle one"},
		{src: "b", dst: "c", pred: "leads", fact: "cycle two"},
		{src: "c", dst: "a", pred: "leads", fact: "cycle three"},
	})
	r, err := GraphSearch(s, nil, "cycle", GraphSearchOpts{ExpandCap: 50, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Edges) != 3 {
		t.Fatalf("cyclic fixture returned %d edges %v, want the 3 distinct facts", len(r.Edges), edgeIDs(r))
	}
	seen := map[int64]bool{}
	for _, e := range r.Edges {
		if seen[e.ID] {
			t.Fatalf("edge %d returned twice — the cycle was walked", e.ID)
		}
		seen[e.ID] = true
	}
	if len(r.Nodes) != 3 {
		t.Fatalf("Nodes = %d, want every endpoint of every returned edge (3)", len(r.Nodes))
	}
}

func TestGraphSearchFloorAndLimitHonored(t *testing.T) {
	s := openSearchStore(t)
	seedGraph(t, s, hubFixture())

	// A floor above anything RRF can produce (the best possible single-leg
	// score is 1/61) empties the answer, expansion included: expansion seeds
	// off the ranked facts, and there are none left.
	high, err := GraphSearch(s, nil, "alpha gamma", GraphSearchOpts{Floor: 1, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(high.Edges) != 0 {
		t.Fatalf("Floor 1 kept %d edges %v", len(high.Edges), edgeIDs(high))
	}
	if len(high.Scores) != 0 {
		t.Fatalf("Floor 1 left %d scores behind", len(high.Scores))
	}

	low, err := GraphSearch(s, nil, "alpha gamma", GraphSearchOpts{Floor: 1e-6, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(low.Edges) == 0 {
		t.Fatal("a floor below every score dropped everything")
	}
	// Every surviving score really is above the floor.
	for _, e := range low.Edges[:1] {
		if low.Scores[e.ID] < 1e-6 {
			t.Fatalf("edge %d survived Floor 1e-6 with score %v", e.ID, low.Scores[e.ID])
		}
	}

	// Limit bounds the RANKED list; expansion rides above it. With ExpandCap 0
	// (the none-requested spelling is a negative cap here, since 0 means
	// default) the total equals Limit exactly.
	lim, err := GraphSearch(s, nil, "gamma", GraphSearchOpts{ExpandCap: -1, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(lim.Edges) != 3 {
		t.Fatalf("Limit 3 with no expansion returned %d edges", len(lim.Edges))
	}
	if len(lim.Scores) != len(lim.Edges) {
		t.Fatalf("%d scores for %d edges — Scores must describe exactly what was returned", len(lim.Scores), len(lim.Edges))
	}
	for _, e := range lim.Edges {
		if _, ok := lim.Scores[e.ID]; !ok {
			t.Fatalf("edge %d returned with no score", e.ID)
		}
	}
}

// The review finding this pins: with the defaults (Limit < surviving
// candidates), expansion used to be appended after the ranked list and then
// truncated away — 100% dropped in the normal case, its only test passing on
// a one-edge fixture. Now Limit bounds the RANKED half and expansion facts
// must SURVIVE past it, scored strictly below every ranked fact.
func TestGraphSearchExpansionSurvivesLimit(t *testing.T) {
	s := openSearchStore(t)
	seedGraph(t, s, hubFixture())

	res, err := GraphSearch(s, nil, "gamma", GraphSearchOpts{ExpandCap: 12, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Edges) <= 2 {
		t.Fatalf("Limit 2 returned %d edges — expansion was truncated away again", len(res.Edges))
	}
	rankedFloor := res.Scores[res.Edges[1].ID]
	for _, e := range res.Edges[2:] {
		if res.Scores[e.ID] >= rankedFloor {
			t.Fatalf("expansion edge %d scored %v, at or above the ranked floor %v", e.ID, res.Scores[e.ID], rankedFloor)
		}
	}
}

// TestGraphSearchIsDeterministic: the same store and the same query must give
// the same bytes. Map iteration is the obvious way to lose this.
func TestGraphSearchIsDeterministic(t *testing.T) {
	s := openSearchStore(t)
	seedGraph(t, s, hubFixture())
	var first string
	for i := 0; i < 8; i++ {
		r, err := GraphSearch(s, nil, "alpha gamma", GraphSearchOpts{ExpandCap: 5, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		got := Triples(r)
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("run %d differed:\n%s\nvs\n%s", i, got, first)
		}
	}
}

// TestGraphSearchOccurrenceBoostOutranksTie: two facts retrieved by the same
// leg at adjacent ranks, where the lower-ranked one has been independently
// stated many times. The rerank is what lets the repeatedly-corroborated fact
// come first.
func TestGraphSearchOccurrenceBoostOutranksTie(t *testing.T) {
	s := openSearchStore(t)
	ids, _ := seedGraph(t, s, []fixtureEdge{
		{src: "a", dst: "b", pred: "uses", fact: "omega rare", occ: 1},
		{src: "c", dst: "d", pred: "uses", fact: "omega common", occ: 100},
	})
	r, err := GraphSearch(s, nil, "omega", GraphSearchOpts{ExpandCap: 0, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Edges) != 2 {
		t.Fatalf("want both omega facts, got %v", edgeIDs(r))
	}
	if r.Scores[ids["omega common"]] <= r.Scores[ids["omega rare"]] {
		t.Fatalf("occurrence boost did not apply: common=%v rare=%v",
			r.Scores[ids["omega common"]], r.Scores[ids["omega rare"]])
	}
}

// TestGraphSearchSkipsInvalidatedEdges: an invalidated fact is history, not an
// answer. The store's own queries enforce this; the test is here because the
// read path is where a regression would show.
func TestGraphSearchSkipsInvalidatedEdges(t *testing.T) {
	s := openSearchStore(t)
	ids, _ := seedGraph(t, s, []fixtureEdge{
		{src: "a", dst: "b", pred: "uses", fact: "sigma retired"},
		{src: "c", dst: "d", pred: "uses", fact: "sigma current"},
	})
	if err := s.InvalidateEdge(ids["sigma retired"], 123); err != nil {
		t.Fatal(err)
	}
	r, err := GraphSearch(s, nil, "sigma", GraphSearchOpts{ExpandCap: 5, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range r.Edges {
		if e.ID == ids["sigma retired"] {
			t.Fatal("an invalidated edge came back from search")
		}
	}
	if len(r.Edges) != 1 {
		t.Fatalf("got %d edges, want only the live sigma fact", len(r.Edges))
	}
}

// TestGraphSearchToleratesCorruptEmbedding: one unreadable blob costs recall
// on one fact and nothing else. A search verb that failed outright because a
// row was written by a different embedder would take retrieval offline for the
// whole corpus.
func TestGraphSearchToleratesCorruptEmbedding(t *testing.T) {
	s := openSearchStore(t)
	ids, _ := seedGraph(t, s, []fixtureEdge{
		{src: "a", dst: "b", pred: "uses", fact: "tau good", vec: []float32{1, 0, 0, 0}},
		{src: "c", dst: "d", pred: "uses", fact: "tau bad"},
	})
	// Three bytes: not a multiple of four, so DecodeVector refuses it.
	if err := s.SetEmbedding("edge", ids["tau bad"], []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	r, err := GraphSearch(s, []float32{1, 0, 0, 0}, "", GraphSearchOpts{ExpandCap: 0, Limit: 10})
	if err != nil {
		t.Fatalf("a corrupt embedding failed the whole search: %v", err)
	}
	if len(r.Edges) != 1 || r.Edges[0].ID != ids["tau good"] {
		t.Fatalf("got %v, want just the decodable fact %d", edgeIDs(r), ids["tau good"])
	}
}

func TestTriplesLineShape(t *testing.T) {
	r := GraphResult{
		Edges: []store.GraphEdge{
			{ID: 1, SrcID: 10, DstID: 11, Predicate: "stages", Fact: "kraken stages zeus' brief as a file."},
			{ID: 2, SrcID: 11, DstID: 12, Predicate: "read-by", Fact: ""},
			{ID: 3, SrcID: 10, DstID: 99, Predicate: "spawns", Fact: "kraken spawns a cell."},
		},
		Nodes: map[int64]store.GraphNode{
			10: {ID: 10, Name: "kraken"},
			11: {ID: 11, Name: "zeus-brief.md"},
			12: {ID: 12, Name: "the pane's shell"},
		},
	}
	want := "(kraken, stages, zeus-brief.md) :: kraken stages zeus' brief as a file.\n" +
		"(zeus-brief.md, read-by, the pane's shell)\n" +
		"(kraken, spawns, #99) :: kraken spawns a cell.\n"
	if got := Triples(r); got != want {
		t.Fatalf("Triples =\n%q\nwant\n%q", got, want)
	}
}

func TestGraphSearchNilStore(t *testing.T) {
	if _, err := GraphSearch(nil, nil, "alpha", GraphSearchOpts{}); err == nil {
		t.Fatal("a nil store must error rather than answer nothing — an empty answer would read as an empty graph")
	}
}
