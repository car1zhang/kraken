package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// recallanchor_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, Slice 3: the `graphFacts`/`searchFacts` seam's new
// shape, `keepRelevantFacts`' anchor exemption, and the threading of anchors
// through `graphContext` and `Recall`.
//
// internal/jobs/graphctx.go and internal/jobs/recall.go were NOT read. Every
// assertion is transcribed from a slice-3 row: an edge touching an anchor is
// kept WITHOUT consulting RelevantEdge, while every other judgment — world
// time, duplicate wording, the skip set, the fact cap — still applies to it in
// the existing order; nil anchors behaves as empty; and an anchored delivery is
// marked in kg_recall like any other.
//
// Dimension: LOGIC. Package-level identifiers are prefixed `contractAnchor`;
// two sibling authors already share this package's test namespace.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractAnchorEnv makes "no embedder" true rather than assumed, so the query
// vector is nil and core.RelevantEdge is on its LEXICAL arm — the arm whose
// verdict the anchor exemption has to override.
func contractAnchorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
}

// contractAnchorSeam records what the seam was called with and answers with a
// fixed (edges, anchors) pair — the seam's new two-value shape.
type contractAnchorSeam struct {
	calls   int
	queries []string
}

func contractAnchorWithFacts(t *testing.T, edges []store.GraphEdge, anchors map[int64]bool) *contractAnchorSeam {
	t.Helper()
	rec := &contractAnchorSeam{}
	prev := graphFacts
	graphFacts = func(_ *store.Store, _ []float32, query string) ([]store.GraphEdge, map[int64]bool) {
		rec.calls++
		rec.queries = append(rec.queries, query)
		return edges, anchors
	}
	t.Cleanup(func() { graphFacts = prev })
	return rec
}

// The query every fixture below is judged against, and a fact sharing NOT ONE
// content word with it. That gap is the whole point: this fact is irrelevant by
// every judgment core.RelevantEdge can make, so the only thing that can keep it
// is the anchor exemption.
const contractAnchorQuery = "the throttled daemon pass reads transcripts past a byte watermark"

const contractAnchorIrrelevantFact = "ios TestFlight uploads burn a CFBundleVersion permanently"

const (
	contractAnchorSrcNode = int64(100)
	contractAnchorDstNode = int64(200)
	contractAnchorFarNode = int64(300)
)

// contractAnchorEdge builds an irrelevant fact hanging between the two fixture
// nodes, with a distinct sentence per id so the duplicate-wording collapse does
// not silently do the fact cap's job.
func contractAnchorEdge(id int64) store.GraphEdge {
	return store.GraphEdge{
		ID: id, EpisodeID: id * 10, SrcID: contractAnchorSrcNode, DstID: contractAnchorDstNode,
		Fact: contractAnchorIrrelevantFact + " " + strings.Repeat("x", int(id)), Occurrences: 1,
	}
}

func contractAnchorTerms() map[string]bool { return core.ContentTerms(contractAnchorQuery) }

func contractAnchorKeptIDs(kept []store.GraphEdge) []int64 {
	out := make([]int64, 0, len(kept))
	for _, e := range kept {
		out = append(out, e.ID)
	}
	return out
}

// ---------------------------------------------------------------------------
// keepRelevantFacts — the exemption
// ---------------------------------------------------------------------------

// TestContractAnchorKeepBypassesRelevantEdge is the exemption itself. The
// fixture asserts its own premise first: core.RelevantEdge REJECTS this fact, so
// a kept verdict cannot be the gate agreeing by accident.
//
// Both endpoints are checked, since the contract says "SrcID or DstID", and a
// third case pins the negative — the same fact with the anchor set empty must
// still be dropped, which is what makes this an exemption rather than an
// open gate.
func TestContractAnchorKeepBypassesRelevantEdge(t *testing.T) {
	terms := contractAnchorTerms()
	e := contractAnchorEdge(1)
	if core.RelevantEdge(e, nil, terms) {
		t.Fatalf("fixture is wrong: core.RelevantEdge accepts %q, so the exemption cannot be observed", e.Fact)
	}

	for _, c := range []struct {
		name    string
		anchors map[int64]bool
		want    int
	}{
		{"src endpoint anchors", map[int64]bool{contractAnchorSrcNode: true}, 1},
		{"dst endpoint anchors", map[int64]bool{contractAnchorDstNode: true}, 1},
		{"both endpoints anchor", map[int64]bool{contractAnchorSrcNode: true, contractAnchorDstNode: true}, 1},
		{"an unrelated node anchors", map[int64]bool{contractAnchorFarNode: true}, 0},
		{"no anchors", map[int64]bool{}, 0},
		{"nil anchors", nil, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			kept := keepRelevantFacts([]store.GraphEdge{e}, nil, terms, nil, c.anchors)
			if len(kept) != c.want {
				t.Errorf("kept %v, want %d fact(s) — the edge is irrelevant by every RelevantEdge judgment, "+
					"so only an anchored endpoint can keep it", contractAnchorKeptIDs(kept), c.want)
			}
		})
	}
}

// TestContractAnchorKeepNilAnchorsBehavesAsEmpty pins the stated equivalence
// literally, over a MIXED batch (one relevant fact, one not) so the comparison
// has something to differ about. `nil` is what every pre-existing caller of the
// old four-argument function effectively passes.
func TestContractAnchorKeepNilAnchorsBehavesAsEmpty(t *testing.T) {
	terms := contractAnchorTerms()
	relevant := edge(7, "the throttled daemon pass stamps its watermark before any early bail", 3)
	edges := []store.GraphEdge{contractAnchorEdge(1), relevant, contractAnchorEdge(2)}

	withNil := keepRelevantFacts(edges, nil, terms, nil, nil)
	withEmpty := keepRelevantFacts(edges, nil, terms, nil, map[int64]bool{})
	gotNil, gotEmpty := contractAnchorKeptIDs(withNil), contractAnchorKeptIDs(withEmpty)
	if len(gotNil) != len(gotEmpty) {
		t.Fatalf("nil anchors kept %v, empty anchors kept %v", gotNil, gotEmpty)
	}
	for i := range gotNil {
		if gotNil[i] != gotEmpty[i] {
			t.Fatalf("nil anchors kept %v, empty anchors kept %v — the two spellings must be the same call", gotNil, gotEmpty)
		}
	}
	// And with no anchors the kept list is the pre-change one: exactly the fact
	// core.RelevantEdge accepts, in the order it arrived.
	if len(gotNil) != 1 || gotNil[0] != relevant.ID {
		t.Errorf("kept %v, want just the relevant fact %d — with empty anchors nothing else may survive",
			gotNil, relevant.ID)
	}
}

// TestContractAnchorKeepStillAppliesEveryOtherJudgment is the exemption's
// boundary as the contract draws it: anchored facts bypass RelevantEdge and
// NOTHING else. Each case anchors the edge and then trips one of the other
// judgments; each must still drop it.
func TestContractAnchorKeepStillAppliesEveryOtherJudgment(t *testing.T) {
	terms := contractAnchorTerms()
	anchors := map[int64]bool{contractAnchorSrcNode: true}
	now := time.Now().UnixMilli()

	t.Run("closed world-time window", func(t *testing.T) {
		e := contractAnchorEdge(1)
		e.ValidUntil = now - 60_000
		if kept := keepRelevantFacts([]store.GraphEdge{e}, nil, terms, nil, anchors); len(kept) != 0 {
			t.Errorf("an anchored fact whose world-time window has closed was kept %v — "+
				"an anchor makes a fact RELEVANT, not currently true", contractAnchorKeptIDs(kept))
		}
		// The other side of that boundary: a future valid_until is a fact that
		// currently holds, and the anchor must not be the reason it survives —
		// but it must survive.
		future := contractAnchorEdge(1)
		future.ValidUntil = now + 3_600_000
		if kept := keepRelevantFacts([]store.GraphEdge{future}, nil, terms, nil, anchors); len(kept) != 1 {
			t.Errorf("an anchored fact valid until a future instant was dropped: kept %v",
				contractAnchorKeptIDs(kept))
		}
	})

	t.Run("session skip set", func(t *testing.T) {
		e := contractAnchorEdge(1)
		skip := map[int64]bool{e.ID: true}
		if kept := keepRelevantFacts([]store.GraphEdge{e}, nil, terms, skip, anchors); len(kept) != 0 {
			t.Errorf("an anchored fact already delivered this session was kept %v — the skip set still binds",
				contractAnchorKeptIDs(kept))
		}
	})

	t.Run("duplicate wording", func(t *testing.T) {
		a := contractAnchorEdge(1)
		b := contractAnchorEdge(2)
		b.Fact = a.Fact // the same sentence arriving from two traversals
		kept := keepRelevantFacts([]store.GraphEdge{a, b}, nil, terms, nil, anchors)
		if len(kept) != 1 {
			t.Errorf("two anchored facts with identical wording kept %v, want one — "+
				"the duplicate collapse still applies", contractAnchorKeptIDs(kept))
		}
	})

	t.Run("fact cap", func(t *testing.T) {
		var edges []store.GraphEdge
		for i := int64(1); i <= 12; i++ {
			edges = append(edges, contractAnchorEdge(i))
		}
		kept := keepRelevantFacts(edges, nil, terms, nil, anchors)
		if len(kept) != graphContextFacts {
			t.Errorf("twelve anchored facts kept %d, want the shared cap of %d — an anchor is not a licence "+
				"to spend the whole budget", len(kept), graphContextFacts)
		}
	})
}

// TestContractAnchorKeepMixesAnchoredAndOrdinaryFacts pins that the exemption
// ADDS to the kept list rather than replacing it: with anchors present, the
// facts the ordinary gate accepts are still there, and the order the edges
// arrived in is preserved. A rewrite that partitioned the batch would reorder
// it, and order is what the fact cap then truncates on.
func TestContractAnchorKeepMixesAnchoredAndOrdinaryFacts(t *testing.T) {
	terms := contractAnchorTerms()
	relevant := edge(7, "the throttled daemon pass stamps its watermark before any early bail", 3)
	anchored := contractAnchorEdge(1)
	edges := []store.GraphEdge{anchored, relevant}

	kept := keepRelevantFacts(edges, nil, terms, nil, map[int64]bool{contractAnchorSrcNode: true})
	got := contractAnchorKeptIDs(kept)
	if len(got) != 2 {
		t.Fatalf("kept %v, want both the anchored fact %d and the relevant fact %d", got, anchored.ID, relevant.ID)
	}
	if got[0] != anchored.ID || got[1] != relevant.ID {
		t.Errorf("kept %v, want them in arrival order [%d %d]", got, anchored.ID, relevant.ID)
	}
}

// ---------------------------------------------------------------------------
// graphContext / Recall — the threading
// ---------------------------------------------------------------------------

// TestContractAnchorGraphContextThreadsAnchors pins that the spawn path passes
// the seam's second return value into the gate rather than dropping it. The
// same fact with no anchors produces "" — the pre-change answer — and with its
// endpoint anchored it produces the block.
func TestContractAnchorGraphContextThreadsAnchors(t *testing.T) {
	contractAnchorEnv(t)
	storeForGraph(t)
	e := contractAnchorEdge(1)

	contractAnchorWithFacts(t, []store.GraphEdge{e}, nil)
	if got := graphContext(&Job{ID: 1, Body: contractAnchorQuery}); got != "" {
		t.Fatalf("an irrelevant, unanchored fact was injected — the fixture cannot show the threading:\n%s", got)
	}

	contractAnchorWithFacts(t, []store.GraphEdge{e}, map[int64]bool{contractAnchorSrcNode: true})
	got := graphContext(&Job{ID: 1, Body: contractAnchorQuery})
	if got == "" {
		t.Fatal("an anchored fact was not injected on the spawn path — graphContext must thread the seam's anchors into the gate")
	}
	if !strings.Contains(got, "[E10,") {
		t.Errorf("the block does not carry the anchored fact's episode id:\n%s", got)
	}
	if !strings.HasPrefix(got, graphContextHeader) {
		t.Errorf("the block does not open with the shared header:\n%s", got)
	}
}

// TestContractAnchorRecallInjectsAnchoredFacts is 52-testing-plan.md's recall
// integration row: a firing whose query anchors a node injects that node's
// fact even though nothing about the sentence matches. Asserted against the
// same fact with no anchors, so "" and "the block" are the two answers the
// SAME fixture gives depending only on the anchor set.
func TestContractAnchorRecallInjectsAnchoredFacts(t *testing.T) {
	contractAnchorEnv(t)
	st := storeForGraph(t)
	e := contractAnchorEdge(1)

	contractAnchorWithFacts(t, []store.GraphEdge{e}, nil)
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-none", Prompt: contractAnchorQuery}); got != "" {
		t.Fatalf("an irrelevant, unanchored fact was injected:\n%s", got)
	}

	rec := contractAnchorWithFacts(t, []store.GraphEdge{e}, map[int64]bool{contractAnchorDstNode: true})
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-anchor", Prompt: contractAnchorQuery})
	if got == "" {
		t.Fatal("an anchored fact was not injected — Recall must thread the seam's anchors the same way graphContext does")
	}
	if !strings.Contains(got, "[E10,") {
		t.Errorf("the block does not carry the anchored fact's episode id:\n%s", got)
	}
	if rec.calls != 1 {
		t.Errorf("retrieval ran %d times, want 1", rec.calls)
	}
	if rec.queries[0] != contractAnchorQuery {
		t.Errorf("retrieval query = %q, want the prompt %q", rec.queries[0], contractAnchorQuery)
	}
}

// TestContractAnchorRecallMarksAnchoredDeliveries pins the row's last sentence:
// "a fact injected via the anchor path is not re-injected on a later firing".
// The second firing uses a DIFFERENT query so the short-circuit cannot be what
// produces the empty answer — only a kg_recall row can.
func TestContractAnchorRecallMarksAnchoredDeliveries(t *testing.T) {
	contractAnchorEnv(t)
	st := storeForGraph(t)
	e := contractAnchorEdge(1)
	anchors := map[int64]bool{contractAnchorSrcNode: true}
	contractAnchorWithFacts(t, []store.GraphEdge{e}, anchors)

	first := Recall(context.Background(), st, RecallRequest{SessionID: "sess-mark", Prompt: contractAnchorQuery})
	if first == "" {
		t.Fatal("the first firing injected nothing")
	}
	ids, err := st.RecalledEdgeIDs("sess-mark")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if !ids[e.ID] {
		t.Errorf("edge %d was injected through the anchor path but not recorded in kg_recall (set %v) — "+
			"an anchored delivery is marked like any other", e.ID, ids)
	}

	second := Recall(context.Background(), st, RecallRequest{SessionID: "sess-mark",
		Prompt: "a completely different question about the daemon pass and its transcripts"})
	if second != "" {
		t.Errorf("a later firing re-injected the anchored fact:\n%s", second)
	}
}

// TestContractAnchorRecallHonoursTheSkipSetOnAnchoredFacts is the same rule
// from the read side, and it is the one that keeps a long conversation moving:
// an anchored fact already delivered leaves the pool and the NEXT anchored fact
// takes its slot, rather than the block going empty.
func TestContractAnchorRecallHonoursTheSkipSetOnAnchoredFacts(t *testing.T) {
	contractAnchorEnv(t)
	st := storeForGraph(t)
	contractAnchorWithFacts(t,
		[]store.GraphEdge{contractAnchorEdge(1), contractAnchorEdge(2)},
		map[int64]bool{contractAnchorSrcNode: true})

	if err := st.MarkRecalled("sess-skip", []int64{1}, 1); err != nil {
		t.Fatalf("seed MarkRecalled: %v", err)
	}
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-skip", Prompt: contractAnchorQuery})
	if got == "" {
		t.Fatal("Recall injected nothing though the second anchored fact had never been delivered")
	}
	if strings.Contains(got, "[E10,") {
		t.Errorf("an already-delivered anchored fact was injected again:\n%s", got)
	}
	if !strings.Contains(got, "[E20,") {
		t.Errorf("the undelivered anchored fact is missing from the block:\n%s", got)
	}
}

// TestContractAnchorSearchFactsReturnsEdgesAndAnchors exercises the seam every
// other test in this file stubs, against a REAL store. Without it the whole
// file would pass against a searchFacts that returned a permanently nil anchor
// set: the stub agrees with itself by construction.
//
// The seeded fact's sentence carries neither the node's name nor any other
// query term, and no embedder is configured, so both the edge and the anchor id
// can only have come from the name path.
func TestContractAnchorSearchFactsReturnsEdgesAndAnchors(t *testing.T) {
	contractAnchorEnv(t)
	st := storeForGraph(t)
	src, err := st.UpsertNode(store.GraphNode{Name: "zephyrine", Type: "person"})
	if err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	dst, err := st.UpsertNode(store.GraphNode{Name: "cofounder", Type: "role"})
	if err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	if _, added, err := st.InsertEdge(store.GraphEdge{SrcID: src, DstID: dst, Predicate: "served-as",
		Fact: "the cofounder signed the charter in autumn", EpisodeID: 3, Strength: 8}); err != nil || !added {
		t.Fatalf("insert edge: added=%v err=%v", added, err)
	}

	edges, anchors := searchFacts(st, nil, "who is zephyrine")
	if anchors == nil {
		t.Fatal("searchFacts returned a nil anchor set for a query naming a seeded low-degree node")
	}
	if !anchors[src] {
		t.Errorf("anchors = %v, want the named node %d", anchors, src)
	}
	if len(edges) == 0 {
		t.Fatal("the anchored node's fact was not retrieved — its sentence carries no query term, " +
			"so the name leg is the only route in and it did not run")
	}
	found := false
	for _, e := range edges {
		if e.EpisodeID == 3 {
			found = true
		}
	}
	if !found {
		t.Errorf("the seeded fact is absent from %d retrieved edge(s)", len(edges))
	}

	// And end to end through the real search, with no stub anywhere: the
	// anchored fact reaches the injected block.
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-e2e", Prompt: "who is zephyrine"})
	if got == "" {
		t.Error("a \"who is <name>\" firing against a seeded store injected nothing")
	}
	if !strings.Contains(got, "[E3,") {
		t.Errorf("the injected block does not carry the seeded fact:\n%s", got)
	}
}
