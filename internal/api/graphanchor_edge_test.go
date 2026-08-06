package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// graphanchor_edge_test.go — the EDGE dimension of 50-contract.md Slice 4:
// POST /api/graph/search's anchored-fact marking at its boundaries. Written
// blind; internal/api/graph.go was not read.
//
// NO EMBEDDER IS REACHABLE FROM A TEST (core.embedURL is a compile-time
// constant and useTestStore sandboxes HOME), so every case here runs the
// LEXICAL arm: a fact is strong iff it shares at least two distinct >=4-rune
// words with the query. That is what makes the anchored cases legible — an
// anchored fact below the term gate is a fact that WOULD be weak, and its
// coming back strong is the whole slice.
//
// The refusal half matters as much as the promotion half: `k graph search` is
// how a human reads the graph, and a hub promoted to strong on a name match
// would put thirty unrelated sentences above the divider. The degree guard is
// therefore walked here too, end to end through the route.
//
// Every package-level identifier is prefixed `edgeAnchor` — this package's
// test binary already carries a `kgedge*` and a `kgcontract*` family, and a
// sibling blind author is writing beside this file.

// --- fixtures --------------------------------------------------------------

// edgeAnchorNoEmbedder puts the route on its BM25-plus-name-leg path
// deterministically; an exported key in the developer's shell would otherwise
// send these cases at the network.
func edgeAnchorNoEmbedder(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "")
}

// edgeAnchorSearchResp is a LOCAL decode of the wire rather than a reuse of the
// handler's own structs: `weak` and `strong` are wire promises, and decoding
// through the server's types would make a renamed json tag invisible.
type edgeAnchorSearchResp struct {
	Nodes []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"nodes"`
	Facts []struct {
		ID        int64  `json:"id"`
		Src       string `json:"src"`
		Dst       string `json:"dst"`
		Predicate string `json:"predicate"`
		Fact      string `json:"fact"`
		Weak      bool   `json:"weak"`
	} `json:"facts"`
	Triples string `json:"triples"`
	Strong  *int   `json:"strong"`
}

const edgeAnchorDivider = "weakly related:"

func edgeAnchorSearch(t *testing.T, body string) edgeAnchorSearchResp {
	t.Helper()
	rec := postGraph(graphSearchHandler, "/api/graph/search", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("search %s: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	var resp edgeAnchorSearchResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return resp
}

// edgeAnchorNode upserts one node and returns its id.
func edgeAnchorNode(t *testing.T, st *store.Store, name string) int64 {
	t.Helper()
	id, err := st.UpsertNode(store.GraphNode{Name: name, Type: "concept"})
	if err != nil {
		t.Fatalf("upsert %q: %v", name, err)
	}
	return id
}

// edgeAnchorEdge inserts one live edge between two existing nodes.
func edgeAnchorEdge(t *testing.T, st *store.Store, src, dst int64, pred, fact string) int64 {
	t.Helper()
	id, added, err := st.InsertEdge(store.GraphEdge{
		SrcID: src, DstID: dst, Predicate: pred, Fact: fact, Strength: 6, Occurrences: 1,
	})
	if err != nil || !added {
		t.Fatalf("insert edge %q: added=%v err=%v", fact, added, err)
	}
	return id
}

func edgeAnchorLines(triples string) []string {
	var out []string
	for _, ln := range strings.Split(triples, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

func edgeAnchorWeakCount(r edgeAnchorSearchResp) int {
	n := 0
	for _, f := range r.Facts {
		if f.Weak {
			n++
		}
	}
	return n
}

// --- promotion --------------------------------------------------------------

// TestEdgeAnchorSearchPromotesAnAnchoredFactBelowTheTermGate is this slice's
// whole point, built so that only the anchor can explain the answer. The
// anchored node is named by the query and its ONE fact shares no content word
// with it at all — so the fact is both retrieved (name leg) and judged weak by
// every existing rule, and its coming back strong is the marking change and
// nothing else. A second, unanchored fact shares exactly one content word:
// retrieved by BM25, still weak, and the divider that separates the two proves
// the promotion is per-fact rather than a blanket "everything is strong now".
func TestEdgeAnchorSearchPromotesAnAnchoredFactBelowTheTermGate(t *testing.T) {
	st := useTestStore(t)
	edgeAnchorNoEmbedder(t)

	const query = "zephyrite calibration"
	anchored := edgeAnchorNode(t, st, "zephyrite")
	target := edgeAnchorNode(t, st, "targetnode")
	anchoredID := edgeAnchorEdge(t, st, anchored, target, "touches", "quiet wiring number one hums")

	// A fact ABOUT zephyrite that lives on other nodes: one shared content
	// word, so weak, and its endpoints anchor nothing.
	elsewhereA := edgeAnchorNode(t, st, "harbournode")
	elsewhereB := edgeAnchorNode(t, st, "tidenode")
	weakID := edgeAnchorEdge(t, st, elsewhereA, elsewhereB, "mentions", "zephyrite drifts alone")

	// The fixture's premise, asserted rather than assumed: the shared judgment
	// calls BOTH of these weak. Anything strong in the answer came from the
	// anchor path.
	terms := core.ContentTerms(query)
	for _, f := range []string{"quiet wiring number one hums", "zephyrite drifts alone"} {
		if core.RelevantEdge(store.GraphEdge{Fact: f}, nil, terms) {
			t.Fatalf("fixture is wrong: %q already clears the term gate", f)
		}
	}

	resp := edgeAnchorSearch(t, `{"query":"`+query+`","limit":20}`)
	if len(resp.Facts) != 2 {
		t.Fatalf("got %d facts, want both — the anchored one via the name leg and the other via BM25:\n%s",
			len(resp.Facts), resp.Triples)
	}
	byID := map[int64]bool{}
	for _, f := range resp.Facts {
		byID[f.ID] = f.Weak
	}
	if weak, ok := byID[anchoredID]; !ok {
		t.Fatalf("the anchored fact %d is missing from the answer entirely: %+v", anchoredID, resp.Facts)
	} else if weak {
		t.Errorf("the anchored fact came back weak — a fact touching an anchor node is never marked weak, whatever the term gate says")
	}
	if weak, ok := byID[weakID]; !ok {
		t.Errorf("the unanchored fact %d was dropped: %+v", weakID, resp.Facts)
	} else if !weak {
		t.Errorf("an unanchored fact sharing one content word came back strong — non-anchored facts are judged exactly as today")
	}
	if resp.Strong == nil {
		t.Fatal(`the response carries no "strong" key`)
	}
	if *resp.Strong != 1 {
		t.Errorf("strong = %d, want 1 (the anchored fact alone)", *resp.Strong)
	}

	// The anchored fact joins the STRONG run of triples: above the divider,
	// which is what makes `k graph search lewis` lead with lewis's facts.
	lines := edgeAnchorLines(resp.Triples)
	divider := -1
	for i, ln := range lines {
		if ln == edgeAnchorDivider {
			if divider >= 0 {
				t.Fatalf("two dividers in one block:\n%s", resp.Triples)
			}
			divider = i
		}
	}
	if divider < 0 {
		t.Fatalf("no %q line, but one fact is weak:\n%s", edgeAnchorDivider, resp.Triples)
	}
	if divider != 1 {
		t.Errorf("divider is at line %d, want 1 (immediately after the single strong line):\n%s", divider, resp.Triples)
	}
	if !strings.Contains(lines[0], "quiet wiring number one hums") {
		t.Errorf("the line above the divider is %q, want the anchored fact's", lines[0])
	}
}

// TestEdgeAnchorSearchAliasOnlyMatchPromotes covers the arm a canonical-name
// implementation forgets. `k graph search dodgson` must lead with the node's
// facts even though its stored name is "lewis carroll" — an alias is the name
// a human actually types, which is the entire reason kg_node_aliases exists.
func TestEdgeAnchorSearchAliasOnlyMatchPromotes(t *testing.T) {
	st := useTestStore(t)
	edgeAnchorNoEmbedder(t)

	person := edgeAnchorNode(t, st, "lewis carroll")
	if err := st.AddAlias(person, "dodgson"); err != nil {
		t.Fatalf("add alias: %v", err)
	}
	other := edgeAnchorNode(t, st, "targetnode")
	id := edgeAnchorEdge(t, st, person, other, "founded", "quiet wiring number one hums")

	resp := edgeAnchorSearch(t, `{"query":"dodgson","limit":20}`)
	if len(resp.Facts) != 1 {
		t.Fatalf("got %d facts, want the alias-anchored one:\n%s", len(resp.Facts), resp.Triples)
	}
	if resp.Facts[0].ID != id {
		t.Fatalf("got fact %d, want %d", resp.Facts[0].ID, id)
	}
	if resp.Facts[0].Weak {
		t.Error("a fact anchored through an ALIAS was marked weak — its sentence contains no query term, which is the case aliases exist for")
	}
	if resp.Strong == nil || *resp.Strong != 1 {
		t.Errorf("strong = %v, want 1", resp.Strong)
	}
	if strings.Contains(resp.Triples, edgeAnchorDivider) {
		t.Errorf("an all-strong answer carries the divider:\n%s", resp.Triples)
	}
}

// --- the guard, through the route ------------------------------------------

// edgeAnchorHub seeds a node named "hubnode" with `leaves` live edges whose
// sentences each share exactly ONE content word with edgeAnchorHubQuery. One
// word is deliberate: BM25 retrieves every leaf fact either way, so the answer
// is the same SET of facts at 30 edges and at 31 and the only thing that moves
// is the marking. That isolates the guard from retrieval completely.
func edgeAnchorHub(t *testing.T, st *store.Store, leaves int) {
	t.Helper()
	hub := edgeAnchorNode(t, st, "hubnode")
	for i := 0; i < leaves; i++ {
		leaf := edgeAnchorNode(t, st, fmt.Sprintf("leafnode%02d", i))
		edgeAnchorEdge(t, st, hub, leaf, "touches", fmt.Sprintf("hubnode wiring number %02d hums", i))
	}
}

const edgeAnchorHubQuery = "hubnode calibration"

// TestEdgeAnchorSearchDegreeGuardDecidesTheMarking walks the guard from both
// sides on the same fixture shape. At exactly AnchorMaxDegree the hub anchors
// and every fact touching it leads the answer; one edge later it does not, and
// the identical facts fall back to the term gate, which calls all of them weak.
// A guard written as `<` or as `>` instead of `<=` moves exactly one of these
// two rows and nothing else in the system notices.
func TestEdgeAnchorSearchDegreeGuardDecidesTheMarking(t *testing.T) {
	t.Run("at the guard", func(t *testing.T) {
		st := useTestStore(t)
		edgeAnchorNoEmbedder(t)
		edgeAnchorHub(t, st, core.AnchorMaxDegree)

		resp := edgeAnchorSearch(t, `{"query":"`+edgeAnchorHubQuery+`","limit":50}`)
		if len(resp.Facts) == 0 {
			t.Fatal("no facts returned; every leaf fact shares the term \"hubnode\" and must be retrieved")
		}
		if n := edgeAnchorWeakCount(resp); n != 0 {
			t.Errorf("%d of %d facts are weak at exactly AnchorMaxDegree (%d) — every one touches the anchor:\n%s",
				n, len(resp.Facts), core.AnchorMaxDegree, resp.Triples)
		}
		if resp.Strong == nil || *resp.Strong != len(resp.Facts) {
			t.Errorf("strong = %v for %d facts, want all of them", resp.Strong, len(resp.Facts))
		}
		if strings.Contains(resp.Triples, edgeAnchorDivider) {
			t.Errorf("an all-strong answer carries the divider:\n%s", resp.Triples)
		}
	})

	t.Run("one over the guard", func(t *testing.T) {
		st := useTestStore(t)
		edgeAnchorNoEmbedder(t)
		edgeAnchorHub(t, st, core.AnchorMaxDegree+1)

		resp := edgeAnchorSearch(t, `{"query":"`+edgeAnchorHubQuery+`","limit":50}`)
		if len(resp.Facts) == 0 {
			t.Fatal("no facts returned; retrieval must be unchanged by the guard")
		}
		if n := edgeAnchorWeakCount(resp); n != len(resp.Facts) {
			t.Errorf("%d of %d facts came back strong one edge over the guard — a hub must never anchor, and each of these shares only one content word with the query:\n%s",
				len(resp.Facts)-n, len(resp.Facts), resp.Triples)
		}
		if resp.Strong == nil || *resp.Strong != 0 {
			t.Errorf("strong = %v, want 0", resp.Strong)
		}
		lines := edgeAnchorLines(resp.Triples)
		first := ""
		if len(lines) > 0 {
			first = lines[0]
		}
		if first != edgeAnchorDivider {
			t.Errorf("first triples line = %q, want the %q divider ahead of every weak fact:\n%s",
				first, edgeAnchorDivider, resp.Triples)
		}
	})
}

// --- the anchor-free response ----------------------------------------------

// TestEdgeAnchorSearchAnchorFreeQueryIsUnchanged is the invariant the whole
// change is conditional on, asserted the two ways a blind test can reach it:
// every fact's flag still equals the shared judgment's verdict, and the
// response carries no key it did not carry before. Byte-identity to the
// pre-change handler cannot be observed from inside one binary; these two
// together are what it decomposes into.
//
// No node in the fixture is named by the query — the names are single tokens
// that appear nowhere in it — so the anchor set is provably empty.
func TestEdgeAnchorSearchAnchorFreeQueryIsUnchanged(t *testing.T) {
	st := useTestStore(t)
	edgeAnchorNoEmbedder(t)

	const query = "widget assembly ledger"
	for i, fact := range []string{
		"widget assembly ledger is recorded", // three shared words: strong
		"widget alone in the corner",         // one shared word: weak
		"assembly ledger stays open",         // two shared words: strong
	} {
		src := edgeAnchorNode(t, st, fmt.Sprintf("srcnode%02d", i))
		dst := edgeAnchorNode(t, st, fmt.Sprintf("dstnode%02d", i))
		edgeAnchorEdge(t, st, src, dst, "states", fact)
	}

	body := `{"query":"` + query + `","limit":20}`
	resp := edgeAnchorSearch(t, body)
	if len(resp.Facts) != 3 {
		t.Fatalf("got %d facts, want all 3:\n%s", len(resp.Facts), resp.Triples)
	}
	terms := core.ContentTerms(query)
	strong := 0
	for _, f := range resp.Facts {
		want := !core.RelevantEdge(store.GraphEdge{Fact: f.Fact}, nil, terms)
		if f.Weak != want {
			t.Errorf("fact %q: weak = %v, want %v — with no anchor the verdict is exactly core.RelevantEdge's",
				f.Fact, f.Weak, want)
		}
		if !f.Weak {
			strong++
		}
	}
	if resp.Strong == nil || *resp.Strong != strong {
		t.Errorf("strong = %v, want %d", resp.Strong, strong)
	}
	if strong != 2 {
		t.Errorf("%d strong facts on this fixture, want 2", strong)
	}

	// The response's top-level shape is unchanged: no anchor ids, no new
	// counter, nothing a client would have to learn to ignore.
	rec := postGraph(graphSearchHandler, "/api/graph/search", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatal(err)
	}
	known := map[string]bool{"nodes": true, "facts": true, "triples": true, "strong": true}
	got := make([]string, 0, len(keys))
	for k := range keys {
		got = append(got, k)
	}
	sort.Strings(got)
	for _, k := range got {
		if !known[k] {
			t.Errorf("response carries a new top-level key %q (all keys: %v) — the response shape is unchanged by this slice", k, got)
		}
	}
	for k := range known {
		if _, ok := keys[k]; !ok {
			t.Errorf("response lost the %q key (all keys: %v)", k, got)
		}
	}
	// Per-fact keys too: an `anchored` flag added beside `weak` is a wire change.
	var envelope struct {
		Facts []map[string]json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for i, f := range envelope.Facts {
		if _, ok := f["weak"]; !ok {
			t.Fatalf("facts[%d] has no \"weak\" key", i)
		}
		if _, ok := f["anchor"]; ok {
			t.Errorf("facts[%d] carries a new \"anchor\" key — the wire shape is unchanged", i)
		}
		if _, ok := f["anchored"]; ok {
			t.Errorf("facts[%d] carries a new \"anchored\" key — the wire shape is unchanged", i)
		}
	}
}

// TestEdgeAnchorSearchRefusalsAreUnchanged: the new marking happens in the same
// serialization step the error path skips, so a refusal must stay a bare
// {"error":...}. A 400 that grew a `strong` count would read to the CLI as a
// legitimate empty answer.
func TestEdgeAnchorSearchRefusalsAreUnchanged(t *testing.T) {
	st := useTestStore(t)
	edgeAnchorNoEmbedder(t)
	person := edgeAnchorNode(t, st, "zephyrite")
	other := edgeAnchorNode(t, st, "targetnode")
	edgeAnchorEdge(t, st, person, other, "touches", "quiet wiring number one hums")

	for _, body := range []string{
		`{"query":"   "}`,
		`{"limit":5}`,
		`{"query":"zephyrite","anchors":true}`,
	} {
		rec := postGraph(graphSearchHandler, "/api/graph/search", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"strong"`) || strings.Contains(rec.Body.String(), `"facts"`) {
			t.Errorf("%s: the refusal carries answer keys: %s", body, rec.Body.String())
		}
	}
}

// TestEdgeAnchorSearchNoMatchAndNoAnchorIsStillEmpty: a query that names no
// node and matches no sentence is the third degenerate grouping, and the name
// leg must not have turned it into "every fact hanging off anything". Zero
// facts, empty triples, and a non-nil zero `strong`.
func TestEdgeAnchorSearchNoMatchAndNoAnchorIsStillEmpty(t *testing.T) {
	st := useTestStore(t)
	edgeAnchorNoEmbedder(t)
	a := edgeAnchorNode(t, st, "zephyrite")
	b := edgeAnchorNode(t, st, "targetnode")
	edgeAnchorEdge(t, st, a, b, "touches", "quiet wiring number one hums")

	resp := edgeAnchorSearch(t, `{"query":"xylophone marzipan quokka"}`)
	if len(resp.Facts) != 0 {
		t.Fatalf("got %d facts for a query naming no node and sharing no term: %+v", len(resp.Facts), resp.Facts)
	}
	if strings.TrimSpace(resp.Triples) != "" {
		t.Errorf("triples = %q, want empty", resp.Triples)
	}
	if resp.Strong == nil || *resp.Strong != 0 {
		t.Errorf("strong = %v, want a non-nil 0", resp.Strong)
	}
}
