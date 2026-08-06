package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// graphanchor_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, Slice 4: `graphSearchHandler`'s anchor marking.
//
// internal/api/graph.go was NOT read. The obligation is one sentence: a fact
// touching an anchor node is never marked weak — `facts[].weak` is false and it
// joins the strong run of `triples` — regardless of cosine or term-overlap
// verdicts, while non-anchored facts are judged exactly as today and an
// anchor-free query's response is unchanged.
//
// Dimension: LOGIC. Package-level identifiers are prefixed `contractAnchor`.
//
// No embedder is reachable from a test (useTestStore points HOME at a throwaway
// dir and every case clears OPENAI_API_KEY), so the route's query vector is nil
// and core.RelevantEdge's LEXICAL arm is what the marking would otherwise use.

// contractAnchorFact mirrors the WIRE, not the handler's struct: `weak` is a
// wire promise, and a test decoding into webGraphFact would keep passing if the
// field were renamed on the way out.
type contractAnchorFact struct {
	ID        int64  `json:"id"`
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	Predicate string `json:"predicate"`
	Fact      string `json:"fact"`
	Weak      bool   `json:"weak"`
}

type contractAnchorResp struct {
	Nodes []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"nodes"`
	Facts   []contractAnchorFact `json:"facts"`
	Triples string               `json:"triples"`
	// A pointer so "the route stopped sending it" is distinguishable from
	// "the route sent zero".
	Strong *int `json:"strong"`
}

const contractAnchorDivider = "weakly related:"

func contractAnchorSearch(t *testing.T, body string) contractAnchorResp {
	t.Helper()
	rec := postGraph(graphSearchHandler, "/api/graph/search", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/graph/search %s: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	var resp contractAnchorResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search response: %v (body %s)", err, rec.Body.String())
	}
	return resp
}

// contractAnchorGraph opens a store wired into the jobs package with no
// embedder configured — the same wiring `kraken serve` does.
func contractAnchorGraph(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("OPENAI_API_KEY", "")
	return st
}

// contractAnchorSeedEdge writes one fact, creating its endpoint nodes on
// demand, and returns the edge id.
func contractAnchorSeedEdge(t *testing.T, st *store.Store, nodes map[string]int64, src, pred, dst, fact string) int64 {
	t.Helper()
	node := func(name string) int64 {
		if id, ok := nodes[name]; ok {
			return id
		}
		id, err := st.UpsertNode(store.GraphNode{Name: name, Type: "concept"})
		if err != nil {
			t.Fatalf("upsert node %q: %v", name, err)
		}
		nodes[name] = id
		return id
	}
	id, added, err := st.InsertEdge(store.GraphEdge{
		SrcID: node(src), DstID: node(dst), Predicate: pred, Fact: fact, Strength: 5, Occurrences: 1,
	})
	if err != nil || !added {
		t.Fatalf("seed edge %q: added=%v err=%v", fact, added, err)
	}
	return id
}

func contractAnchorLines(triples string) []string {
	s := strings.TrimSuffix(triples, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func contractAnchorTripleLine(f contractAnchorFact) string {
	return fmt.Sprintf("(%s, %s, %s) :: %s", f.Src, f.Predicate, f.Dst, f.Fact)
}

func contractAnchorFactBySentence(t *testing.T, resp contractAnchorResp, fact string) contractAnchorFact {
	t.Helper()
	for _, f := range resp.Facts {
		if f.Fact == fact {
			return f
		}
	}
	t.Fatalf("fact %q is absent from the response (%d facts):\n%s", fact, len(resp.Facts), resp.Triples)
	return contractAnchorFact{}
}

// The two fixture sentences the anchored cases below turn on.
//
//   - contractAnchorNamedFact shares NOT ONE content word with the query and
//     carries no embedding, so core.RelevantEdge rejects it outright. Its src
//     node is the one the query names. Everything about its marking therefore
//     comes from the anchor.
//   - contractAnchorLooseFact shares exactly one content word — below
//     core.GraphMinTermOverlap — and touches no anchor, so it is the control
//     that must STAY weak in the same response.
const (
	contractAnchorNamedFact = "the cofounder signed the charter in autumn"
	contractAnchorLooseFact = "quasar rides alone downhill"
	contractAnchorNamedQ    = "zephyrine quasar"
)

// TestContractAnchorSearchMarksAnchoredFactsStrong is the slice's whole
// obligation, asserted on a fact the ordinary judgment REJECTS. The fixture
// checks that premise first: without the anchor there is no reason on earth for
// this fact to be strong, so a false `weak` can only be the anchor exemption.
func TestContractAnchorSearchMarksAnchoredFactsStrong(t *testing.T) {
	st := contractAnchorGraph(t)
	nodes := map[string]int64{}
	contractAnchorSeedEdge(t, st, nodes, "zephyrine", "served-as", "cofounder", contractAnchorNamedFact)
	contractAnchorSeedEdge(t, st, nodes, "quasarnode", "notes", "otherleaf", contractAnchorLooseFact)

	terms := core.ContentTerms(contractAnchorNamedQ)
	if core.RelevantEdge(store.GraphEdge{Fact: contractAnchorNamedFact}, nil, terms) {
		t.Fatalf("fixture is wrong: the anchored fact already clears core.RelevantEdge, so its strong marking proves nothing")
	}
	if core.RelevantEdge(store.GraphEdge{Fact: contractAnchorLooseFact}, nil, terms) {
		t.Fatalf("fixture is wrong: the control fact clears core.RelevantEdge and cannot show the weak marking")
	}

	body, err := json.Marshal(map[string]any{"query": contractAnchorNamedQ, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := contractAnchorSearch(t, string(body))

	named := contractAnchorFactBySentence(t, resp, contractAnchorNamedFact)
	if named.Weak {
		t.Errorf("the fact hanging off the node the query NAMES is marked weak — "+
			"`k graph search zephyrine` must lead with it, not bury it under %q", contractAnchorDivider)
	}
	// The control: judged exactly as today, which for one shared content word
	// means weak.
	loose := contractAnchorFactBySentence(t, resp, contractAnchorLooseFact)
	if !loose.Weak {
		t.Errorf("a fact touching no anchor and sharing %d content word was marked strong — "+
			"non-anchored facts are judged exactly as today", core.TermOverlap(contractAnchorLooseFact, terms))
	}
	if resp.Strong == nil {
		t.Fatal(`the response carries no "strong" key`)
	}
	if *resp.Strong != 1 {
		t.Errorf("strong = %d, want 1 — the anchored fact and nothing else", *resp.Strong)
	}
}

// TestContractAnchorSearchAnchoredFactJoinsTheStrongRun pins the other half of
// the sentence: the rendered block groups it above the divider, in the strong
// run, with the per-fact line format untouched. The CLI does no grouping of its
// own, so a fact marked strong on the wire but rendered below the divider would
// still read as "weakly related" in a cell's context window.
func TestContractAnchorSearchAnchoredFactJoinsTheStrongRun(t *testing.T) {
	st := contractAnchorGraph(t)
	nodes := map[string]int64{}
	contractAnchorSeedEdge(t, st, nodes, "zephyrine", "served-as", "cofounder", contractAnchorNamedFact)
	contractAnchorSeedEdge(t, st, nodes, "quasarnode", "notes", "otherleaf", contractAnchorLooseFact)

	body, err := json.Marshal(map[string]any{"query": contractAnchorNamedQ, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := contractAnchorSearch(t, string(body))
	lines := contractAnchorLines(resp.Triples)

	dividerAt := -1
	for i, ln := range lines {
		if ln == contractAnchorDivider {
			dividerAt = i
			break
		}
	}
	if dividerAt < 0 {
		t.Fatalf("no %q line, but the answer carries a weak fact:\n%s", contractAnchorDivider, resp.Triples)
	}
	named := contractAnchorFactBySentence(t, resp, contractAnchorNamedFact)
	want := contractAnchorTripleLine(named)
	found := -1
	for i, ln := range lines {
		if ln == want {
			found = i
			break
		}
	}
	if found < 0 {
		t.Fatalf("the anchored fact's line %q is absent from the block:\n%s", want, resp.Triples)
	}
	if found > dividerAt {
		t.Errorf("the anchored fact renders BELOW the %q divider (line %d of %d):\n%s",
			contractAnchorDivider, found, len(lines), resp.Triples)
	}
	// The weak control is still on the other side, so the divider still means
	// what it meant.
	loose := contractAnchorTripleLine(contractAnchorFactBySentence(t, resp, contractAnchorLooseFact))
	for i, ln := range lines {
		if ln == loose && i < dividerAt {
			t.Errorf("the non-anchored weak fact renders above the divider:\n%s", resp.Triples)
		}
	}
}

// TestContractAnchorSearchAnchorFreeResponseIsUnchanged is the slice's
// conditionality clause, and it is the one that makes the whole feature safe to
// ship: for a query naming no node, every fact's marking must agree with
// core.RelevantEdge's own verdict — the pre-change rule, applied by the
// pre-change judgment — and the response must still carry its four keys.
//
// Comparing against core.RelevantEdge rather than against a hard-coded list is
// deliberate: it asserts that NOTHING new participates in the marking when the
// anchor set is empty, which is exactly what "byte-identical to the pre-change
// handler" means observably.
func TestContractAnchorSearchAnchorFreeResponseIsUnchanged(t *testing.T) {
	st := contractAnchorGraph(t)
	nodes := map[string]int64{}

	// Node names are of the srcnodeNN shape, so no query term can name one.
	const query = "kraken daemon spawns zeus cells"
	facts := []string{
		"kraken daemon writes the ledger",    // overlap 2 → strong
		"zeus rides the bicycle downhill",    // overlap 1 → weak
		"zeus opens cells quickly",           // overlap 2 → strong
		"cells of the honeycomb hold nectar", // overlap 1 → weak
	}
	for i, f := range facts {
		contractAnchorSeedEdge(t, st, nodes,
			fmt.Sprintf("srcnode%02d", i), "states", fmt.Sprintf("dstnode%02d", i), f)
	}

	// The anchor-free premise, asserted rather than assumed: not one node name
	// is covered by the query's terms.
	cands, err := st.AnchorCandidates()
	if err != nil {
		t.Fatalf("AnchorCandidates: %v", err)
	}
	if got := core.AnchorNodes(core.ContentTerms(query), cands); len(got) != 0 {
		t.Fatalf("the fixture anchors %v for %q; this case must anchor nothing", got, query)
	}

	body, err := json.Marshal(map[string]any{"query": query, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := contractAnchorSearch(t, string(body))
	if len(resp.Facts) != len(facts) {
		t.Fatalf("got %d facts, want all %d — marking must never drop one:\n%s", len(resp.Facts), len(facts), resp.Triples)
	}

	terms := core.ContentTerms(query)
	strong := 0
	for _, f := range resp.Facts {
		wantWeak := !core.RelevantEdge(store.GraphEdge{Fact: f.Fact}, nil, terms)
		if f.Weak != wantWeak {
			t.Errorf("fact %q: weak = %v, want %v — with no anchor the verdict is core.RelevantEdge's alone "+
				"(it shares %d distinct content words)", f.Fact, f.Weak, wantWeak, core.TermOverlap(f.Fact, terms))
		}
		if !f.Weak {
			strong++
		}
	}
	if resp.Strong == nil {
		t.Fatal(`the response carries no "strong" key on an anchor-free query`)
	}
	if *resp.Strong != strong {
		t.Errorf("strong = %d, want %d (the number of facts with weak == false)", *resp.Strong, strong)
	}
	if *resp.Strong != 2 {
		t.Errorf("strong = %d, want 2 on this fixture", *resp.Strong)
	}
	// The response shape itself: nodes still served for name resolution, still
	// ascending by id, endpoints still rendered as names.
	if len(resp.Nodes) == 0 {
		t.Fatal("no nodes returned — canonicalization resolves names against these")
	}
	for i := 1; i < len(resp.Nodes); i++ {
		if resp.Nodes[i-1].ID >= resp.Nodes[i].ID {
			t.Errorf("nodes not sorted by ascending id: %d then %d", resp.Nodes[i-1].ID, resp.Nodes[i].ID)
		}
	}
	for _, f := range resp.Facts {
		if f.Src == "" || strings.HasPrefix(f.Src, "#") || f.Dst == "" {
			t.Errorf("fact %d endpoints = (%q, %q), want resolved node names", f.ID, f.Src, f.Dst)
		}
	}
	// And the request shape is unchanged: `anchors` is not an input.
	if rec := postGraph(graphSearchHandler, "/api/graph/search", `{"query":"x","anchors":[1]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown request field: status %d, want 400 — this slice adds no request key", rec.Code)
	}
}

// TestContractAnchorSearchAllAnchoredHasNoDivider is the shape the manual
// check in 52-testing-plan.md describes: `k graph search lewis` over a corpus
// where every returned fact hangs off the named node. Every fact is strong, so
// the block reads exactly as an all-strong answer always has — no divider, no
// empty section.
func TestContractAnchorSearchAllAnchoredHasNoDivider(t *testing.T) {
	st := contractAnchorGraph(t)
	nodes := map[string]int64{}
	contractAnchorSeedEdge(t, st, nodes, "zephyrine", "served-as", "cofounder", contractAnchorNamedFact)
	contractAnchorSeedEdge(t, st, nodes, "zephyrine", "lived-at", "harbourside", "she kept a house by the water")

	body, err := json.Marshal(map[string]any{"query": "who is zephyrine", "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := contractAnchorSearch(t, string(body))
	if len(resp.Facts) == 0 {
		t.Fatal("a query naming a seeded low-degree node returned no facts at all")
	}
	for _, f := range resp.Facts {
		if f.Weak {
			t.Errorf("fact %q touches the anchored node and was marked weak", f.Fact)
		}
	}
	if resp.Strong == nil || *resp.Strong != len(resp.Facts) {
		t.Errorf("strong = %v, want all %d facts", resp.Strong, len(resp.Facts))
	}
	if strings.Contains(resp.Triples, contractAnchorDivider) {
		t.Errorf("an all-anchored answer carries a %q divider:\n%s", contractAnchorDivider, resp.Triples)
	}
	lines := contractAnchorLines(resp.Triples)
	if len(lines) != len(resp.Facts) {
		t.Fatalf("triples has %d lines for %d facts:\n%s", len(lines), len(resp.Facts), resp.Triples)
	}
	for i, f := range resp.Facts {
		if lines[i] != contractAnchorTripleLine(f) {
			t.Errorf("line %d = %q, want %q — the per-fact format is unchanged", i, lines[i], contractAnchorTripleLine(f))
		}
	}
}
