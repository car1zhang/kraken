package jobs

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// graphctx_test.go — the injection's guarantees, in the order they matter.
//
// The single most important one is that graphContext returns "" whenever the
// graph has nothing to say: every cell spawn goes through this function, and a
// job with no match must get the brief it would have got before the knowledge
// graph existed, byte for byte.
//
// Retrieval itself is stubbed through the graphFacts seam (graphctx.go) rather
// than seeded into a real store, because what is under test here is the GATE,
// the BUDGET and the RENDERING — three decisions this package owns — not
// core.GraphSearch's ranking, which has its own tests.

// withFacts swaps the retrieval seam for one test and restores it, so a failure
// in one case cannot leak a stub into the next.
func withFacts(t *testing.T, edges []store.GraphEdge) {
	t.Helper()
	prev := graphFacts
	graphFacts = func(*store.Store, []float32, string) []store.GraphEdge { return edges }
	t.Cleanup(func() { graphFacts = prev })
}

// storeForGraph opens a scratch store and wires it as the active one, which is
// what graphContext reads its kv cache through.
func storeForGraph(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })
	return st
}

// edge builds a fact whose sentence shares `terms` with a spec, with no
// embedding — so it is judged by the lexical gate.
func edge(id int64, fact string, occ int64) store.GraphEdge {
	return store.GraphEdge{ID: id, EpisodeID: id * 10, Fact: fact, Occurrences: occ}
}

func TestGraphContextEmptyWhenNothingRetrieved(t *testing.T) {
	storeForGraph(t)
	withFacts(t, nil)
	if got := graphContext(&Job{ID: 1, Body: "make the daemon pass faster"}); got != "" {
		t.Errorf("graphContext with an empty graph = %q, want \"\" — a job with no match must get a byte-identical brief", got)
	}
}

func TestGraphContextEmptyWithoutStore(t *testing.T) {
	// No UseStore: the file-mode entrypoints (the viewer, `k` itself) spawn
	// nothing, but a cell started outside `kraken serve` must not fail and must
	// not open its own handle — store.Open runs migrations.
	UseStore(nil)
	withFacts(t, []store.GraphEdge{edge(1, "the daemon pass is throttled to one minute", 3)})
	if got := graphContext(&Job{ID: 1, Body: "make the daemon pass faster"}); got != "" {
		t.Errorf("graphContext with no active store = %q, want \"\"", got)
	}
}

func TestGraphContextEmptyForEmptySpec(t *testing.T) {
	storeForGraph(t)
	withFacts(t, []store.GraphEdge{edge(1, "kraken serve is the single writer of kraken.db", 9)})
	for _, body := range []string{"", "   \n\t  "} {
		if got := graphContext(&Job{ID: 1, Body: body}); got != "" {
			t.Errorf("graphContext with spec %q = %q, want \"\" — there is nothing to retrieve against", body, got)
		}
	}
	if got := graphContext(nil); got != "" {
		t.Errorf("graphContext(nil) = %q, want \"\"", got)
	}
}

// TestGraphContextLexicalGate is the gate's whole point: retrieval returning a
// fact is not the same as the fact being relevant. FTS5 matches an OR of terms
// (store.SearchEdgesFTS), so the best of a bad candidate set still comes back
// ranked first.
func TestGraphContextLexicalGate(t *testing.T) {
	storeForGraph(t)
	spec := "speed up the throttled daemon pass that sums transcripts"
	withFacts(t, []store.GraphEdge{
		edge(1, "ios TestFlight uploads burn a CFBundleVersion permanently", 4),
	})
	if got := graphContext(&Job{ID: 1, Body: spec}); got != "" {
		t.Errorf("a fact sharing no content words with the spec was emitted:\n%s", got)
	}
	withFacts(t, []store.GraphEdge{
		edge(2, "the throttled daemon pass stamps its watermark before any early bail", 5),
	})
	got := graphContext(&Job{ID: 1, Body: spec})
	if got == "" {
		t.Fatal("a fact sharing several content words with the spec was rejected")
	}
	if !strings.Contains(got, "[E20, seen 5x]") {
		t.Errorf("block does not carry the episode id and sighting count:\n%s", got)
	}
	if !strings.HasPrefix(got, graphContextHeader) {
		t.Errorf("block does not open with the hedged header:\n%s", got)
	}
}

// TestGraphContextSingleSharedWordIsNotEnough pins graphMinTermOverlap. One
// shared word is a coincidence in a corpus where every job mentions kraken.
func TestGraphContextSingleSharedWordIsNotEnough(t *testing.T) {
	storeForGraph(t)
	withFacts(t, []store.GraphEdge{edge(1, "kraken signs its binary with a real identity", 2)})
	if got := graphContext(&Job{ID: 1, Body: "kraken should render diagrams differently"}); got != "" {
		t.Errorf("a fact sharing one word was emitted:\n%s", got)
	}
}

// TestGraphContextCosineGate covers the vector leg: with a cached spec vector
// and an embedded fact, the decision is a cosine against graphCosineFloor and
// the lexical overlap is bypassed entirely — which is the case that lets a
// fact worded completely differently still land.
func TestGraphContextCosineGate(t *testing.T) {
	st := storeForGraph(t)
	spec := "improve retrieval quality"
	// A three-dimensional unit vector stands in for the real 1024; the gate
	// only ever computes a dot product, so the dimension is irrelevant and a
	// hand-checkable vector is worth more than a realistic one.
	cacheSpecVector(t, st, 7, []float32{1, 0, 0})

	near := store.GraphEdge{ID: 1, EpisodeID: 11, Fact: "totally unrelated wording", Occurrences: 2,
		Embedding: core.EncodeVector([]float32{0.9, 0.435889894, 0})} // cos = 0.90
	far := store.GraphEdge{ID: 2, EpisodeID: 22, Fact: "also unrelated wording", Occurrences: 2,
		Embedding: core.EncodeVector([]float32{0.5, 0.866025404, 0})} // cos = 0.50

	withFacts(t, []store.GraphEdge{near})
	if got := graphContext(&Job{ID: 7, Body: spec}); !strings.Contains(got, "[E11") {
		t.Errorf("a fact at cosine 0.90 (floor %v) was rejected: %q", graphCosineFloor, got)
	}
	withFacts(t, []store.GraphEdge{far})
	if got := graphContext(&Job{ID: 7, Body: spec}); got != "" {
		t.Errorf("a fact at cosine 0.50 (floor %v) was emitted:\n%s", graphCosineFloor, got)
	}
}

// cacheSpecVector writes the kv row graphPass' refreshJobVectors would have
// written, which is the only way a vector reaches the spawn path.
func cacheSpecVector(t *testing.T, st *store.Store, jobID int64, v []float32) {
	t.Helper()
	if err := st.SetKV(graphJobVecKey(jobID), base64.StdEncoding.EncodeToString(core.EncodeVector(v))); err != nil {
		t.Fatalf("cache spec vector: %v", err)
	}
}

// TestGraphContextCorruptVectorFallsBackToLexical pins the degradation: a
// cache row that will not decode must not withhold a fact the lexical gate
// would have accepted, and must never panic on the spawn path.
func TestGraphContextCorruptVectorFallsBackToLexical(t *testing.T) {
	st := storeForGraph(t)
	if err := st.SetKV(graphJobVecKey(3), "not base64 at all!!"); err != nil {
		t.Fatalf("set kv: %v", err)
	}
	spec := "the throttled daemon pass over transcripts"
	withFacts(t, []store.GraphEdge{edge(1, "the throttled daemon pass reads transcripts past a byte watermark", 2)})
	if got := graphContext(&Job{ID: 3, Body: spec}); got == "" {
		t.Error("a corrupt cached vector suppressed a fact the lexical gate accepts")
	}
}

// TestGraphContextBudget pins both halves of Q5's budget and the truncation
// rule: at most graphContextFacts lines, at most graphContextBytes bytes, and
// a cut that only ever falls between facts.
func TestGraphContextBudget(t *testing.T) {
	storeForGraph(t)
	spec := "throttled daemon pass reads transcripts past a byte watermark cursor"
	var edges []store.GraphEdge
	for i := int64(1); i <= 12; i++ {
		edges = append(edges, edge(i,
			"the throttled daemon pass reads transcripts past a byte watermark cursor and never rewinds", i))
	}
	// Distinct sentences: identical ones are deduped, which would test the
	// dedupe rather than the budget.
	for i := range edges {
		edges[i].Fact += " (" + strings.Repeat("x", i+1) + ")"
	}
	withFacts(t, edges)
	got := graphContext(&Job{ID: 1, Body: spec})
	if got == "" {
		t.Fatal("budget test retrieved nothing — the gate rejected facts it should accept")
	}
	if len(got) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(got), graphContextBytes, got)
	}
	lines := strings.Count(got, "\n- ")
	if lines > graphContextFacts {
		t.Errorf("block carries %d facts, over the %d cap", lines, graphContextFacts)
	}
	// Every emitted line is whole: it starts with "- " and ends with the
	// bracketed provenance. A mid-sentence cut is the one way this block could
	// actively mislead, since a half-fact with an episode id after it reads as
	// a complete claim.
	body := strings.TrimPrefix(got, graphContextHeader)
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if !strings.HasPrefix(line, "- ") || !strings.HasSuffix(line, "x]") {
			t.Errorf("truncated mid-fact: %q", line)
		}
	}
}

// TestGraphContextDeterministic pins the property the spec asks for: the same
// graph and the same spec produce the same bytes. A system prompt that varies
// between two spawns of the same job is unreproducible by construction.
func TestGraphContextDeterministic(t *testing.T) {
	storeForGraph(t)
	spec := "the throttled daemon pass over transcripts and cursors"
	edges := []store.GraphEdge{
		edge(1, "the throttled daemon pass reads transcripts past a cursor", 3),
		edge(2, "the daemon pass stamps its throttle before any early bail", 2),
		edge(3, "transcripts are append-only so cursors only move forward", 7),
	}
	withFacts(t, edges)
	first := graphContext(&Job{ID: 1, Body: spec})
	if first == "" {
		t.Fatal("retrieved nothing")
	}
	for i := 0; i < 5; i++ {
		if got := graphContext(&Job{ID: 1, Body: spec}); got != first {
			t.Fatalf("run %d differs:\n%q\nvs\n%q", i, got, first)
		}
	}
}

// TestGraphContextDedupesIdenticalFacts guards the case 1-hop expansion makes
// easy to hit: the same sentence arriving twice from two different traversals.
func TestGraphContextDedupesIdenticalFacts(t *testing.T) {
	storeForGraph(t)
	spec := "the throttled daemon pass over transcripts"
	fact := "the throttled daemon pass reads transcripts past a cursor"
	withFacts(t, []store.GraphEdge{edge(1, fact, 3), edge(2, fact, 1)})
	got := graphContext(&Job{ID: 1, Body: spec})
	if n := strings.Count(got, "- "); n != 1 {
		t.Errorf("identical facts were not deduped (%d lines):\n%s", n, got)
	}
}

// TestGraphContextKeepsTheBriefUnderItsCeiling is component 6's stated
// integration check: zeusBrief + graphContext is what cell.go actually stages,
// and TestZeusBriefStaysLean only measures the first half.
func TestGraphContextKeepsTheBriefUnderItsCeiling(t *testing.T) {
	storeForGraph(t)
	spec := "throttled daemon pass reads transcripts past a byte watermark cursor"
	var edges []store.GraphEdge
	for i := int64(1); i <= 20; i++ {
		edges = append(edges, edge(i,
			"the throttled daemon pass reads transcripts past a byte watermark cursor "+strings.Repeat("y", int(i)), i))
	}
	withFacts(t, edges)
	j := &Job{ID: 1, Slug: "lean", Body: spec}
	// 12,000 is TestZeusBriefStaysLean's ceiling for the brief alone; the
	// injection's own cap is what keeps the sum bounded, so state the sum
	// explicitly rather than trusting two independent numbers to stay in step.
	const ceiling = 12_000 + graphContextBytes
	if n := len(zeusBrief(j) + graphContext(j)); n > ceiling {
		t.Errorf("staged brief is %d bytes, over the %d-byte combined ceiling", n, ceiling)
	}
}

// TestGraphContextSurvivesASickGraph is the spawn-path guarantee stated as a
// test: a retrieval that blows up returns "" rather than propagating. A cell
// must never fail to start because the knowledge graph is broken.
func TestGraphContextSurvivesASickGraph(t *testing.T) {
	storeForGraph(t)
	prev := graphFacts
	graphFacts = func(*store.Store, []float32, string) []store.GraphEdge {
		// searchFacts' own contract: every error collapses to nil.
		return nil
	}
	t.Cleanup(func() { graphFacts = prev })
	if got := graphContext(&Job{ID: 1, Body: "anything at all"}); got != "" {
		t.Errorf("a failed search produced %q, want \"\"", got)
	}
}

// TestSearchFactsAgainstARealStore exercises the seam every other test in
// this file stubs. Without it the whole suite would pass against a
// core.GraphSearch that had been renamed, retyped, or had stopped returning
// anything — the stub agrees with itself by construction.
//
// It also covers the case searchFacts' size guard produces on a large graph:
// a nil query vector. Retrieval is then BM25-only, which must still answer.
func TestSearchFactsAgainstARealStore(t *testing.T) {
	st := storeForGraph(t)
	src, err := st.UpsertNode(store.GraphNode{Name: "tokensPass", Type: "component",
		Description: "The daemon pass that keeps each job's job_tokens row current."})
	if err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	dst, err := st.UpsertNode(store.GraphNode{Name: "kraken serve", Type: "component",
		Description: "The single process that owns the scheduler tick and the web API."})
	if err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	if _, _, err := st.InsertEdge(store.GraphEdge{SrcID: src, DstID: dst, Predicate: "runs-inside",
		Fact:      "tokensPass runs inside kraken serve's daemon tick and is skipped under mirror.",
		EpisodeID: 3, Strength: 8}); err != nil {
		t.Fatalf("insert edge: %v", err)
	}

	edges := searchFacts(st, nil, "how does tokensPass relate to kraken serve")
	if len(edges) == 0 {
		t.Fatal("BM25-only retrieval against a seeded graph returned nothing — " +
			"either the FTS index is not populated or the seam has drifted")
	}
	if edges[0].EpisodeID != 3 {
		t.Errorf("top edge = %+v, want the seeded one", edges[0])
	}
	// And the whole path end to end, with no stub anywhere.
	if got := graphContext(&Job{ID: 1, Body: "make tokensPass faster inside kraken serve"}); got == "" {
		t.Error("graphContext found nothing against a seeded graph with a matching spec")
	}
	// The negative half: a spec about something the graph has never heard of
	// must still produce "" through the REAL search, not just through a stub
	// that was handed an empty slice.
	if got := graphContext(&Job{ID: 1, Body: "redesign the iOS TestFlight upload screen"}); got != "" {
		t.Errorf("an unrelated spec produced a block through the real search:\n%s", got)
	}
}

// TestFactLineShape pins the rendered line, since the format is what tells
// zeus the block is evidence rather than instruction — an episode id it can
// check and a count it can weigh.
func TestFactLineShape(t *testing.T) {
	for _, c := range []struct {
		in   store.GraphEdge
		want string
	}{
		{store.GraphEdge{EpisodeID: 4, Fact: "a fact with its own period.", Occurrences: 3},
			"- a fact with its own period. [E4, seen 3x]\n"},
		{store.GraphEdge{EpisodeID: 5, Fact: "a fact without one", Occurrences: 1},
			"- a fact without one. [E5, seen 1x]\n"},
		// A stored edge was seen at least once whatever the column says; a
		// "seen 0x" line would read as a fact nobody ever observed.
		{store.GraphEdge{EpisodeID: 6, Fact: "a fact with a zeroed count", Occurrences: 0},
			"- a fact with a zeroed count. [E6, seen 1x]\n"},
		// Multi-line facts are one-lined: the block is a list, and a newline
		// inside a line breaks the format the reader is parsing by eye.
		{store.GraphEdge{EpisodeID: 7, Fact: "a fact\nsplit over lines", Occurrences: 2},
			"- a fact split over lines. [E7, seen 2x]\n"},
	} {
		if got := factLine(c.in); got != c.want {
			t.Errorf("factLine(%q) = %q, want %q", c.in.Fact, got, c.want)
		}
	}
}

// TestRenderGraphBlockRefusesAHeaderAlone pins the degenerate case: a header
// announcing that the graph knows something, with nothing under it, is worse
// than no block at all.
//
// Reaching it takes MULTI-BYTE text, and that is the point rather than an
// awkward fixture. factLine caps a fact at graphFactRunes RUNES while the
// budget is in BYTES, so an ASCII fact always fits (180 runes ≈ 210 bytes,
// well under 600) — but 180 CJK runes are 540 bytes, and with the header and
// the provenance suffix that clears the cap. A byte budget over a rune cap is
// exactly where a "can't happen" branch turns out to be reachable.
func TestRenderGraphBlockRefusesAHeaderAlone(t *testing.T) {
	long := store.GraphEdge{EpisodeID: 1, Occurrences: 1, Fact: strings.Repeat("知", graphFactRunes)}
	if got := renderGraphBlock([]store.GraphEdge{long}); got != "" {
		t.Errorf("a fact too long to fit produced a header-only block:\n%q", got)
	}
	if got := renderGraphBlock(nil); got != "" {
		t.Errorf("renderGraphBlock(nil) = %q, want \"\"", got)
	}
}
