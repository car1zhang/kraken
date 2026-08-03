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

// graphsearch_contract_test.go — the CORRECTNESS pins for slice S1c of
// artifacts/50-contract.md: POST /api/graph/search after Fixes A and B.
//
// No embedder key is reachable from a test (useTestStore points HOME at a
// throwaway dir and every case clears OPENAI_API_KEY), so the route's query
// vector is nil throughout and core.RelevantEdge's LEXICAL fallback is what
// decides weak vs strong here. Every fixture below is therefore built out of
// shared ≥4-rune words: two or more with the query is strong, fewer is weak.
// That is deliberate — it is the arm of the gate a real deployment falls back
// to whenever embedding lags, and it is the only arm reachable without a
// network call.

// kgcontractSearchFact mirrors the wire, not the handler's struct. Decoding
// through a local type is the point: `weak` is a WIRE promise, and a test that
// decoded into webGraphFact would keep passing if the field were renamed on
// the way out.
type kgcontractSearchFact struct {
	ID        int64  `json:"id"`
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	Predicate string `json:"predicate"`
	Fact      string `json:"fact"`
	Weak      bool   `json:"weak"`
}

type kgcontractSearchResp struct {
	Nodes   []json.RawMessage      `json:"nodes"`
	Facts   []kgcontractSearchFact `json:"facts"`
	Triples string                 `json:"triples"`
	// A pointer so "the route stopped sending it" is distinguishable from
	// "the route sent zero" — the same distinction the CLI has to make.
	Strong *int `json:"strong"`
}

// kgcontractSearch posts one search and decodes it, failing the test on any
// non-200. Every case in this file goes through the real handler and a real
// seeded store: the arithmetic under test is about how many rows come back,
// which no stub can answer.
func kgcontractSearch(t *testing.T, body string) kgcontractSearchResp {
	t.Helper()
	rec := postGraph(graphSearchHandler, "/api/graph/search", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/graph/search %s: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	var resp kgcontractSearchResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode search response: %v (body %s)", err, rec.Body.String())
	}
	return resp
}

// kgcontractGraph opens a store wired into the jobs package (the same wiring
// `kraken serve` does) with no embedder configured.
func kgcontractGraph(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("OPENAI_API_KEY", "") // BM25-only: no network in a test
	return st
}

// kgcontractEdge writes one fact, creating its endpoint nodes on demand.
func kgcontractEdge(t *testing.T, st *store.Store, nodes map[string]int64, src, pred, dst, fact string) int64 {
	t.Helper()
	node := func(name string) int64 {
		if id, ok := nodes[name]; ok {
			return id
		}
		id, err := st.UpsertNode(store.GraphNode{Name: name, Type: "concept"})
		if err != nil {
			t.Fatal(err)
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

// kgcontractLines splits a triples blob into its lines, dropping the trailing
// empty one core.Triples' final newline produces.
func kgcontractLines(triples string) []string {
	s := strings.TrimSuffix(triples, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// kgcontractTripleLine is the per-fact line format S1c says is UNCHANGED.
func kgcontractTripleLine(f kgcontractSearchFact) string {
	return fmt.Sprintf("(%s, %s, %s) :: %s", f.Src, f.Predicate, f.Dst, f.Fact)
}

const kgcontractWeakDivider = "weakly related:"

// TestKgcontractSearchExpansionCapTracksTheLimit is Fix A, on the star fixture
// S1c names: one edge whose fact carries the query term, plus thirty edges
// fanning off one shared endpoint to thirty distinct leaves that match
// nothing.
//
// The fan is what makes the cap observable. Only ONE edge is ever ranked
// (nothing else matches the query at all), so every additional fact in the
// response arrived through the 1-hop expansion, and the expansion's node cap
// is the only thing bounding it. With the old fixed cap of 50, `limit: 1`
// returned the ranked fact plus all thirty leaves — a caller who asked for one
// fact got thirty-one. That is the eval's exact complaint.
func TestKgcontractSearchExpansionCapTracksTheLimit(t *testing.T) {
	st := kgcontractGraph(t)
	nodes := map[string]int64{}

	// The one lexical hit. "zephyrite" appears nowhere else in the fixture.
	kgcontractEdge(t, st, nodes, "hubnode", "calibrates", "targetnode",
		"zephyrite calibration lives inside the hubnode")
	// Thirty fan edges from the shared endpoint to thirty distinct leaves.
	// Their sentences share no word with the query, so they can only enter the
	// answer through expansion.
	for i := 0; i < 30; i++ {
		kgcontractEdge(t, st, nodes, "hubnode", "touches", fmt.Sprintf("leafnode%02d", i),
			fmt.Sprintf("quiet unrelated wiring number %02d", i))
	}

	narrow := kgcontractSearch(t, `{"query":"zephyrite","limit":1}`)
	if len(narrow.Facts) > 2 {
		t.Errorf("limit 1 returned %d facts, want at most 2 — the expansion cap must be min(50, limit), not a fixed 50", len(narrow.Facts))
	}
	if len(narrow.Facts) == 0 {
		t.Fatal("limit 1 returned no facts at all — the ranked hit must survive")
	}

	wide := kgcontractSearch(t, `{"query":"zephyrite","limit":20}`)
	if len(wide.Facts) < 15 {
		t.Errorf("limit 20 returned %d facts, want at least 15 — a larger ask must still get a wide neighbourhood", len(wide.Facts))
	}
	// The proportionality itself, which is the whole of Fix A: asking for more
	// gets more. The eval reproduced the bug as "limit 1 and limit 10 return
	// the same thing".
	if len(wide.Facts) <= len(narrow.Facts) {
		t.Errorf("limit 1 returned %d facts and limit 20 returned %d — the response size must track the ask",
			len(narrow.Facts), len(wide.Facts))
	}
}

// TestKgcontractSearchRankedListSemanticsUnchanged is Fix A's other half: the
// cap change must not have narrowed the RANKED list. On a corpus with more
// matching edges than the limit, `limit: 20` still returns at least its twenty
// ranked facts — the cap bounds expansion, never retrieval.
func TestKgcontractSearchRankedListSemanticsUnchanged(t *testing.T) {
	st := kgcontractGraph(t)
	nodes := map[string]int64{}

	// Thirty independently-matching facts on thirty disjoint node pairs, so
	// nothing here depends on expansion at all.
	for i := 0; i < 30; i++ {
		kgcontractEdge(t, st, nodes,
			fmt.Sprintf("alphanode%02d", i), "documents", fmt.Sprintf("betanode%02d", i),
			fmt.Sprintf("widget assembly step %02d is recorded", i))
	}

	resp := kgcontractSearch(t, `{"query":"widget assembly","limit":20}`)
	if len(resp.Facts) < 20 {
		t.Errorf("limit 20 over 30 matching edges returned %d facts, want at least the 20 ranked ones", len(resp.Facts))
	}
	// And the default is still 20 ranked facts, not the new cap: an absent
	// limit is unchanged by this slice.
	def := kgcontractSearch(t, `{"query":"widget assembly"}`)
	if len(def.Facts) < 20 {
		t.Errorf("an absent limit returned %d facts, want the unchanged default of 20 ranked facts", len(def.Facts))
	}
}

// kgcontractMixedFixture seeds two facts that clear the lexical gate against
// kgcontractMixedQuery and two that do not, on eight distinct nodes so no
// expansion fact can join the answer and muddy the counts.
const kgcontractMixedQuery = "kraken daemon spawns zeus cells"

const (
	kgcontractStrongA = "kraken daemon writes the ledger"    // shares kraken, daemon
	kgcontractStrongB = "zeus opens cells quickly"           // shares zeus, cells
	kgcontractWeakA   = "zeus rides the bicycle downhill"    // shares zeus only
	kgcontractWeakB   = "cells of the honeycomb hold nectar" // shares cells only
)

func kgcontractMixedFixture(t *testing.T, st *store.Store, facts []string) {
	t.Helper()
	nodes := map[string]int64{}
	for i, f := range facts {
		kgcontractEdge(t, st, nodes,
			fmt.Sprintf("srcnode%02d", i), "states", fmt.Sprintf("dstnode%02d", i), f)
	}
	// The fixture is only meaningful if the shared judgment agrees with the
	// labels above; assert that here rather than discovering it as a confusing
	// failure in the route's own assertions.
	terms := core.ContentTerms(kgcontractMixedQuery)
	for _, f := range facts {
		want := f == kgcontractStrongA || f == kgcontractStrongB
		got := core.RelevantEdge(store.GraphEdge{Fact: f}, nil, terms)
		if got != want {
			t.Fatalf("fixture is wrong: core.RelevantEdge(%q) = %v, want %v", f, got, want)
		}
	}
}

// TestKgcontractSearchMarksWeakFactsWithoutDroppingOrReordering is the heart
// of Fix B. Four facts, two of them below the relevance bar: all four must come
// back, in the pipeline's own order, each carrying an honest `weak` flag, and
// the response must count the strong ones.
//
// "Never drops" is asserted by COUNT and "never reorders" by comparing the
// response against core.GraphSearch's own edge order for the same query — the
// pipeline S1c says is untouched. Comparing against the pipeline rather than
// against a hard-coded list keeps this a test of the marking step: if ranking
// ever changes for an unrelated reason, this still asserts exactly what it
// means to assert, which is that MARKING did not move anything.
func TestKgcontractSearchMarksWeakFactsWithoutDroppingOrReordering(t *testing.T) {
	st := kgcontractGraph(t)
	kgcontractMixedFixture(t, st, []string{
		kgcontractStrongA, kgcontractWeakA, kgcontractStrongB, kgcontractWeakB,
	})

	body, err := json.Marshal(map[string]any{"query": kgcontractMixedQuery, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := kgcontractSearch(t, string(body))

	if len(resp.Facts) != 4 {
		t.Fatalf("got %d facts, want all 4 — marking must never drop a fact:\n%s", len(resp.Facts), resp.Triples)
	}

	// Every fact's flag, by sentence.
	wantWeak := map[string]bool{
		kgcontractStrongA: false,
		kgcontractStrongB: false,
		kgcontractWeakA:   true,
		kgcontractWeakB:   true,
	}
	strongCount := 0
	for _, f := range resp.Facts {
		want, known := wantWeak[f.Fact]
		if !known {
			t.Errorf("unexpected fact in the response: %q", f.Fact)
			continue
		}
		if f.Weak != want {
			t.Errorf("fact %q: weak = %v, want %v (it shares %d distinct content words with the query)",
				f.Fact, f.Weak, want, core.TermOverlap(f.Fact, core.ContentTerms(kgcontractMixedQuery)))
		}
		if !f.Weak {
			strongCount++
		}
	}

	// The new top-level count. Absent is a different failure from wrong, so
	// the pointer is checked before the value.
	if resp.Strong == nil {
		t.Fatal(`the response carries no "strong" key — the CLI cannot tell an all-weak answer from an ordinary one without it`)
	}
	if *resp.Strong != strongCount {
		t.Errorf("strong = %d, want %d (the number of facts with weak == false)", *resp.Strong, strongCount)
	}
	if *resp.Strong != 2 {
		t.Errorf("strong = %d, want 2 on this fixture", *resp.Strong)
	}

	// Order: the response's fact ids must appear in the same relative order
	// core.GraphSearch produced them in, with the same opts the route says it
	// passes. A subsequence check rather than equality, so this asserts
	// ORDERING alone and does not accidentally re-pin the pipeline's contents.
	res, err := core.GraphSearch(st, nil, kgcontractMixedQuery, core.GraphSearchOpts{
		ExpandCap: min(50, 20),
		Limit:     20,
	})
	if err != nil {
		t.Fatal(err)
	}
	pipeline := make([]int64, 0, len(res.Edges))
	for _, e := range res.Edges {
		pipeline = append(pipeline, e.ID)
	}
	at := 0
	for _, f := range resp.Facts {
		for at < len(pipeline) && pipeline[at] != f.ID {
			at++
		}
		if at == len(pipeline) {
			t.Fatalf("fact %d (%q) is out of pipeline order — marking must not reorder facts.\nresponse: %v\npipeline: %v",
				f.ID, f.Fact, resp.Facts, pipeline)
		}
		at++
	}
}

// TestKgcontractSearchTriplesRegroupsAroundTheDivider pins the rendered block:
// strong lines first in their own relative order, then a line that is EXACTLY
// `weakly related:`, then the weak lines in theirs. The divider's exact text
// is the contract because S1d leans on it — the CLI prints it verbatim and
// does no grouping of its own, so any drift here silently changes what a cell
// reads in its context window.
func TestKgcontractSearchTriplesRegroupsAroundTheDivider(t *testing.T) {
	st := kgcontractGraph(t)
	kgcontractMixedFixture(t, st, []string{
		kgcontractStrongA, kgcontractWeakA, kgcontractStrongB, kgcontractWeakB,
	})

	body, err := json.Marshal(map[string]any{"query": kgcontractMixedQuery, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := kgcontractSearch(t, string(body))
	lines := kgcontractLines(resp.Triples)

	// Exactly one divider, and it is that string with nothing around it.
	dividerAt := -1
	for i, ln := range lines {
		if ln != kgcontractWeakDivider {
			continue
		}
		if dividerAt >= 0 {
			t.Fatalf("two %q dividers in one block:\n%s", kgcontractWeakDivider, resp.Triples)
		}
		dividerAt = i
	}
	if dividerAt < 0 {
		t.Fatalf("no %q line, but the answer has weak facts:\n%s", kgcontractWeakDivider, resp.Triples)
	}

	// The expected line lists, taken from the facts array's own order — which
	// the test above has already pinned to the pipeline's.
	var wantStrong, wantWeak []string
	for _, f := range resp.Facts {
		if f.Weak {
			wantWeak = append(wantWeak, kgcontractTripleLine(f))
		} else {
			wantStrong = append(wantStrong, kgcontractTripleLine(f))
		}
	}
	if len(lines) != len(wantStrong)+1+len(wantWeak) {
		t.Fatalf("triples has %d lines, want %d strong + 1 divider + %d weak:\n%s",
			len(lines), len(wantStrong), len(wantWeak), resp.Triples)
	}
	gotStrong := lines[:dividerAt]
	gotWeak := lines[dividerAt+1:]
	if strings.Join(gotStrong, "\n") != strings.Join(wantStrong, "\n") {
		t.Errorf("lines above the divider =\n%s\nwant\n%s", strings.Join(gotStrong, "\n"), strings.Join(wantStrong, "\n"))
	}
	if strings.Join(gotWeak, "\n") != strings.Join(wantWeak, "\n") {
		t.Errorf("lines below the divider =\n%s\nwant\n%s", strings.Join(gotWeak, "\n"), strings.Join(wantWeak, "\n"))
	}
}

// TestKgcontractSearchAllStrongHasNoDivider: the divider appears IFF there is
// at least one weak fact. An answer where everything cleared the bar reads
// exactly as it did before this slice — no empty section, no trailing label.
func TestKgcontractSearchAllStrongHasNoDivider(t *testing.T) {
	st := kgcontractGraph(t)
	kgcontractMixedFixture(t, st, []string{kgcontractStrongA, kgcontractStrongB})

	body, err := json.Marshal(map[string]any{"query": kgcontractMixedQuery, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := kgcontractSearch(t, string(body))

	if len(resp.Facts) != 2 {
		t.Fatalf("got %d facts, want 2:\n%s", len(resp.Facts), resp.Triples)
	}
	for _, f := range resp.Facts {
		if f.Weak {
			t.Errorf("fact %q was marked weak, but it shares 2 content words with the query", f.Fact)
		}
	}
	if resp.Strong == nil || *resp.Strong != 2 {
		t.Errorf("strong = %v, want 2", resp.Strong)
	}
	if strings.Contains(resp.Triples, kgcontractWeakDivider) {
		t.Errorf("an all-strong answer carries a %q divider:\n%s", kgcontractWeakDivider, resp.Triples)
	}
	// Every line is a fact line, in the facts array's order.
	lines := kgcontractLines(resp.Triples)
	if len(lines) != len(resp.Facts) {
		t.Fatalf("triples has %d lines for %d facts:\n%s", len(lines), len(resp.Facts), resp.Triples)
	}
	for i, f := range resp.Facts {
		if lines[i] != kgcontractTripleLine(f) {
			t.Errorf("line %d = %q, want %q — the per-fact format is unchanged", i, lines[i], kgcontractTripleLine(f))
		}
	}
}

// TestKgcontractSearchAllWeakReportsZeroStrong: the case the eval's "python
// 3.14 migration" probe lands in. Facts still come back — they are the best
// the corpus has — but `strong` is 0 and the divider leads, which is what lets
// the CLI say so before printing any of them.
func TestKgcontractSearchAllWeakReportsZeroStrong(t *testing.T) {
	st := kgcontractGraph(t)
	kgcontractMixedFixture(t, st, []string{kgcontractWeakA, kgcontractWeakB})

	body, err := json.Marshal(map[string]any{"query": kgcontractMixedQuery, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	resp := kgcontractSearch(t, string(body))

	if len(resp.Facts) != 2 {
		t.Fatalf("got %d facts, want both weak facts kept — marking never drops:\n%s", len(resp.Facts), resp.Triples)
	}
	for _, f := range resp.Facts {
		if !f.Weak {
			t.Errorf("fact %q was marked strong, but it shares only 1 content word with the query", f.Fact)
		}
	}
	if resp.Strong == nil {
		t.Fatal(`no "strong" key on an all-weak answer — 0 must be reported, not omitted`)
	}
	if *resp.Strong != 0 {
		t.Errorf("strong = %d, want 0", *resp.Strong)
	}
	lines := kgcontractLines(resp.Triples)
	if len(lines) == 0 || lines[0] != kgcontractWeakDivider {
		t.Fatalf("first triples line = %q, want the %q divider ahead of every weak fact:\n%s",
			func() string {
				if len(lines) == 0 {
					return ""
				}
				return lines[0]
			}(), kgcontractWeakDivider, resp.Triples)
	}
}

// TestKgcontractSearchEverythingElseIsUnchanged re-pins the parts of the route
// S1c says this slice does not touch, since a rewrite of the response
// assembly is exactly where they would be lost: nodes are still served for
// name resolution, still sorted by ascending id, and endpoints still render as
// names rather than ids.
func TestKgcontractSearchEverythingElseIsUnchanged(t *testing.T) {
	st := kgcontractGraph(t)
	kgcontractMixedFixture(t, st, []string{kgcontractStrongA, kgcontractWeakA})

	body, err := json.Marshal(map[string]any{"query": kgcontractMixedQuery, "limit": 20})
	if err != nil {
		t.Fatal(err)
	}
	rec := postGraph(graphSearchHandler, "/api/graph/search", string(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var raw struct {
		Nodes []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
		} `json:"nodes"`
		Facts []kgcontractSearchFact `json:"facts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Nodes) == 0 {
		t.Fatal("no nodes returned — canonicalization resolves names against these")
	}
	for i := 1; i < len(raw.Nodes); i++ {
		if raw.Nodes[i-1].ID >= raw.Nodes[i].ID {
			t.Errorf("nodes not sorted by ascending id: %d then %d", raw.Nodes[i-1].ID, raw.Nodes[i].ID)
		}
	}
	for _, f := range raw.Facts {
		if f.Src == "" || strings.HasPrefix(f.Src, "#") || f.Dst == "" {
			t.Errorf("fact %d endpoints = (%q, %q), want resolved node names", f.ID, f.Src, f.Dst)
		}
	}
	// The refusals are unchanged too — a blank query is still a 400 rather
	// than "every fact in the corpus, all of them weak".
	if rec := postGraph(graphSearchHandler, "/api/graph/search", `{"query":"   "}`); rec.Code != http.StatusBadRequest {
		t.Errorf("blank query: status %d, want 400", rec.Code)
	}
	if rec := postGraph(graphSearchHandler, "/api/graph/search", `{"query":"x","strong":1}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown request field: status %d, want 400 — `strong` is a RESPONSE key only", rec.Code)
	}
}
