package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"kraken/internal/store"
)

// graphsearch_edge_test.go — the EDGE dimension of POST /api/graph/search after
// Fixes A and B: the limit clamp at its boundaries, the two degenerate
// groupings (no weak facts at all, no strong facts at all), the zero-count that
// must survive serialization, and the corrupt-blob path that must not turn a
// search into an error.
//
// Every identifier is prefixed `kgedge` — a sibling author shares this
// package's test binary and an unprefixed helper collides.
//
// NO EMBEDDER IS AVAILABLE IN A TEST (core.embedURL is a compile-time constant
// pointing at api.openai.com, so there is no seam to redirect), which means
// every case here runs the LEXICAL fallback: a fact is strong iff it shares at
// least two distinct >=4-rune words with the query. The cosine branch is
// unreachable from this package without a network and is pinned in
// internal/core/relevance_edge_test.go instead.

// kgedgeQuery is the one query every fixture below is built around. Its four
// content words are kraken / spawns / cells / nightly.
const kgedgeQuery = "kraken spawns cells nightly"

// kgedgeStrongFact shares four content words with kgedgeQuery — comfortably
// over the >=2 gate — and is the only fact in any fixture the BM25 leg matches.
const kgedgeStrongFact = "kraken spawns cells nightly inside every hub"

// kgedgeSearchResp is a LOCAL decode of the response rather than a reuse of the
// handler's own types: the point of these cases is the wire contract (does
// `strong` appear, is `weak` present on every fact), and decoding through the
// server's structs would make a renamed json tag invisible.
type kgedgeSearchResp struct {
	Nodes []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"nodes"`
	Facts []struct {
		ID   int64  `json:"id"`
		Src  string `json:"src"`
		Dst  string `json:"dst"`
		Fact string `json:"fact"`
		Weak bool   `json:"weak"`
	} `json:"facts"`
	Triples string `json:"triples"`
	Strong  *int   `json:"strong"`
}

func (r kgedgeSearchResp) weakCount() int {
	n := 0
	for _, f := range r.Facts {
		if f.Weak {
			n++
		}
	}
	return n
}

func (r kgedgeSearchResp) strongCount() int { return len(r.Facts) - r.weakCount() }

// kgedgeNoEmbedder puts the handler on its BM25-only leg deterministically: an
// exported key in the developer's shell would otherwise make these cases reach
// for the network, and the HOME sandbox useTestStore installs is what keeps
// ~/.kraken/openai.env out of it.
func kgedgeNoEmbedder(t *testing.T) {
	t.Helper()
	t.Setenv("OPENAI_API_KEY", "")
}

// kgedgeSeedEdge inserts one live edge, creating its endpoints by name.
func kgedgeSeedEdge(t *testing.T, st *store.Store, src, dst, predicate, fact string) int64 {
	t.Helper()
	s, err := st.UpsertNode(store.GraphNode{Name: src})
	if err != nil {
		t.Fatalf("upsert %q: %v", src, err)
	}
	d, err := st.UpsertNode(store.GraphNode{Name: dst})
	if err != nil {
		t.Fatalf("upsert %q: %v", dst, err)
	}
	id, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: s, DstID: d, Predicate: predicate, Fact: fact, Strength: 6,
	})
	if err != nil {
		t.Fatalf("insert edge %q: %v", fact, err)
	}
	return id
}

// kgedgeSeedStar builds the fixture the contract's cap row names: ONE edge the
// query matches lexically, plus `leaves` edges fanning out of that edge's
// shared endpoint to distinct leaf nodes. None of the leaf facts share a word
// with the query, so none is a BM25 candidate and every one of them can only
// arrive through the 1-hop expansion — which makes the total fact count a
// direct readout of the ExpandCap the route handed core.GraphSearch.
func kgedgeSeedStar(t *testing.T, st *store.Store, leaves int) {
	t.Helper()
	kgedgeSeedEdge(t, st, "kraken", "hub", "spawns", kgedgeStrongFact)
	for i := 0; i < leaves; i++ {
		kgedgeSeedEdge(t, st, "hub", fmt.Sprintf("leaf-%02d", i),
			"touches", fmt.Sprintf("hub touches leaf number %02d quietly", i))
	}
}

// kgedgeSearch posts one search and decodes it, failing the test on anything
// that is not a 200.
func kgedgeSearch(t *testing.T, body string) kgedgeSearchResp {
	t.Helper()
	rec := postGraph(graphSearchHandler, "/api/graph/search", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("search %s: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	var resp kgedgeSearchResp
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return resp
}

// kgedgeSearchKeys posts one search and returns its top-level keys undecoded,
// so a case can ask whether a key was PRESENT as opposed to what it decoded to.
func kgedgeSearchKeys(t *testing.T, body string) map[string]json.RawMessage {
	t.Helper()
	rec := postGraph(graphSearchHandler, "/api/graph/search", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("search %s: status %d, body %s", body, rec.Code, rec.Body.String())
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &keys); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return keys
}

// kgedgeTripleLines splits the triples block into non-empty lines.
func kgedgeTripleLines(triples string) []string {
	var out []string
	for _, ln := range strings.Split(triples, "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}

// kgedgeDivider is the exact line the regrouped triples block uses to separate
// the two runs. It is a full line, not a prefix: the CLI prints it verbatim
// alongside fact lines, so anything else here (an emoji, a blank line, a
// trailing space) reaches an agent's context as a fact-shaped sentence.
const kgedgeDivider = "weakly related:"

// TestKgedgeSearchLimitClampBoundaries walks the clamp at every stated edge.
// The observable is the TOTAL fact count on the star fixture, which is
// 1 ranked + min(ExpandCap, leaves) expansion — and ExpandCap is min(50, L)
// after clamping. Before Fix A the cap was a fixed 50 and every one of these
// rows returned the same oversized answer, which is the eval failure the fix
// exists for.
func TestKgedgeSearchLimitClampBoundaries(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	kgedgeSeedStar(t, st, 30)

	count := func(body string) int {
		return len(kgedgeSearch(t, body).Facts)
	}

	// limit 1: the smallest legal ask. ExpandCap = min(50, 1) = 1, so exactly
	// one leaf may ride along — the contract's own executed bound.
	atOne := count(`{"query":"` + kgedgeQuery + `"` + `,"limit":1}`)
	if atOne > 2 {
		t.Errorf("limit 1 returned %d facts, want at most 2 (1 ranked + 1 expansion)", atOne)
	}
	if atOne < 1 {
		t.Fatalf("limit 1 returned %d facts — the ranked hit itself was lost", atOne)
	}

	// An ABSENT limit and an explicit 0 are the same request: both mean the
	// default 20. A route that treated 0 as "zero" would answer an unlimited
	// query with nothing, and one that treated it as "no cap" would answer it
	// with everything.
	absent := count(`{"query":"` + kgedgeQuery + `"}`)
	zero := count(`{"query":"` + kgedgeQuery + `","limit":0}`)
	twenty := count(`{"query":"` + kgedgeQuery + `","limit":20}`)
	if absent != zero || absent != twenty {
		t.Errorf("fact counts: absent=%d, limit 0=%d, limit 20=%d — all three are the default 20",
			absent, zero, twenty)
	}
	if twenty < 15 {
		t.Errorf("limit 20 returned %d facts, want at least 15", twenty)
	}

	// Proportionality itself: the whole point of Fix A is that a small ask
	// produces a small answer.
	if atOne >= twenty {
		t.Errorf("limit 1 returned %d facts and limit 20 returned %d — the response is not proportional to the ask",
			atOne, twenty)
	}

	// The upper clamp. 201 is refused-by-clamping rather than refused
	// outright, so it must answer exactly as 200 does; and neither may exceed
	// the ranked ceiling (200) plus the expansion ceiling (50).
	twoHundred := count(`{"query":"` + kgedgeQuery + `","limit":200}`)
	overCap := count(`{"query":"` + kgedgeQuery + `","limit":201}`)
	if overCap != twoHundred {
		t.Errorf("limit 201 returned %d facts, limit 200 returned %d — 201 must clamp to 200", overCap, twoHundred)
	}
	if overCap > 250 {
		t.Errorf("limit 201 returned %d facts, over the clamped ceiling of 200 ranked + 50 expansion", overCap)
	}
	// Whatever the clamp does, a bigger ask never answers with less.
	if twoHundred < twenty {
		t.Errorf("limit 200 returned %d facts, fewer than limit 20's %d", twoHundred, twenty)
	}
}

// TestKgedgeSearchStrongIsPresentEvenWhenZero is the one that makes the CLI's
// headline possible. `strong: 0` is the interesting value and it is the exact
// value `omitempty` deletes; the CLI reads a MISSING key as "an older server
// that never sent it" and renders today's output, so a dropped zero is
// indistinguishable from an unfixed server and Fix B silently does nothing.
func TestKgedgeSearchStrongIsPresentEvenWhenZero(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	// One shared content word — matched by BM25, one short of the >=2 gate.
	// The node is "leviathan", not "kraken": a node whose NAME the query
	// covers anchors under the name-anchor feature and is promoted to strong
	// regardless of term overlap, which would defeat this fixture's point.
	// The fact SENTENCE keeps "kraken" so BM25 still retrieves it.
	kgedgeSeedEdge(t, st, "leviathan", "harbour", "sits-in", "kraken sits quietly in a harbour")

	keys := kgedgeSearchKeys(t, `{"query":"`+kgedgeQuery+`"}`)
	raw, ok := keys["strong"]
	if !ok {
		t.Fatalf("no \"strong\" key in the response (keys: %v) — the CLI cannot tell an all-weak result from an old server", kgedgeMapKeys(keys))
	}
	if strings.TrimSpace(string(raw)) != "0" {
		t.Fatalf("strong = %s, want 0 — no fact shares two content words with the query", raw)
	}

	resp := kgedgeSearch(t, `{"query":"`+kgedgeQuery+`"}`)
	if len(resp.Facts) == 0 {
		t.Fatal("no facts returned — the fixture's fact shares a term with the query and must still be RETRIEVED")
	}
	if resp.Strong == nil || *resp.Strong != 0 {
		t.Errorf("strong = %v, want a non-nil 0", resp.Strong)
	}
	if resp.strongCount() != 0 {
		t.Errorf("%d fact(s) came back weak=false, but strong reports 0 — the count and the flags disagree",
			resp.strongCount())
	}
}

func kgedgeMapKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestKgedgeSearchAllWeakGrouping: with NO strong fact the divider is the FIRST
// line of the block and every fact line follows it. A divider emitted after the
// facts, or omitted because "there is nothing to divide", puts weakly-related
// sentences into an agent's context with nothing marking them.
func TestKgedgeSearchAllWeakGrouping(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	// "leviathan", not "kraken": a query-covered node name would anchor and
	// promote both facts to strong (see the fixture note in
	// TestKgedgeSearchStrongIsPresentEvenWhenZero).
	kgedgeSeedEdge(t, st, "leviathan", "harbour", "sits-in", "kraken sits quietly in a harbour")
	kgedgeSeedEdge(t, st, "leviathan", "tide", "rides", "kraken rides an ordinary tide")

	resp := kgedgeSearch(t, `{"query":"`+kgedgeQuery+`"}`)
	if len(resp.Facts) < 2 {
		t.Fatalf("got %d facts, want both fixture edges (each shares the term \"kraken\")", len(resp.Facts))
	}
	if resp.weakCount() != len(resp.Facts) {
		t.Fatalf("%d of %d facts are weak, want all of them", resp.weakCount(), len(resp.Facts))
	}

	lines := kgedgeTripleLines(resp.Triples)
	if len(lines) != len(resp.Facts)+1 {
		t.Fatalf("triples has %d lines for %d facts, want facts+1 (the divider):\n%s",
			len(lines), len(resp.Facts), resp.Triples)
	}
	if lines[0] != kgedgeDivider {
		t.Errorf("first triples line = %q, want exactly %q", lines[0], kgedgeDivider)
	}
	if n := strings.Count(resp.Triples, kgedgeDivider); n != 1 {
		t.Errorf("divider appears %d times, want exactly 1", n)
	}
	for _, ln := range lines[1:] {
		if !strings.HasPrefix(ln, "(") {
			t.Errorf("line %q below the divider is not a `(src, predicate, dst) :: fact` line", ln)
		}
	}
}

// TestKgedgeSearchNoWeakFactsHasNoDivider is the other degenerate grouping: a
// result where everything cleared the gate carries NO divider at all. An
// unconditional divider would append a heading to every strong answer and teach
// its reader to ignore it.
func TestKgedgeSearchNoWeakFactsHasNoDivider(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	// Two facts, each sharing three content words, and no other edges — so
	// there is nothing for the expansion to bring in weakly.
	kgedgeSeedEdge(t, st, "kraken", "cells", "spawns", "kraken spawns cells nightly")
	kgedgeSeedEdge(t, st, "kraken", "cells", "schedules", "kraken spawns nightly cells on schedule")

	resp := kgedgeSearch(t, `{"query":"`+kgedgeQuery+`"}`)
	if len(resp.Facts) == 0 {
		t.Fatal("no facts returned for a query lifted from the stored sentences")
	}
	if resp.weakCount() != 0 {
		t.Fatalf("%d fact(s) marked weak, want none:\n%s", resp.weakCount(), resp.Triples)
	}
	if resp.Strong == nil || *resp.Strong != len(resp.Facts) {
		t.Errorf("strong = %v for %d facts, want all of them", resp.Strong, len(resp.Facts))
	}
	if strings.Contains(resp.Triples, kgedgeDivider) {
		t.Errorf("a result with no weak facts still carries the divider:\n%s", resp.Triples)
	}
	if got := len(kgedgeTripleLines(resp.Triples)); got != len(resp.Facts) {
		t.Errorf("triples has %d lines for %d facts, want one line each", got, len(resp.Facts))
	}
}

// TestKgedgeSearchMixedGroupingKeepsEveryFact covers the middle case at its
// edges: exactly one strong fact and many weak ones. The counts must add up
// (marking never DROPS a fact), the divider must appear exactly once, and every
// line above it must be a strong fact's.
func TestKgedgeSearchMixedGroupingKeepsEveryFact(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	kgedgeSeedStar(t, st, 12)

	resp := kgedgeSearch(t, `{"query":"`+kgedgeQuery+`","limit":20}`)
	if len(resp.Facts) < 5 {
		t.Fatalf("got %d facts, want the ranked hit plus its expansion", len(resp.Facts))
	}
	if resp.Strong == nil {
		t.Fatal("strong key absent")
	}
	if *resp.Strong != 1 {
		t.Errorf("strong = %d, want 1 (only the ranked fact shares two content words)", *resp.Strong)
	}
	if resp.strongCount()+resp.weakCount() != len(resp.Facts) {
		t.Errorf("strong %d + weak %d != %d facts", resp.strongCount(), resp.weakCount(), len(resp.Facts))
	}
	if resp.weakCount() == 0 {
		t.Fatal("no weak facts — the expansion facts share no content word with the query")
	}
	// The strong one is the ranked hit, and it is still FIRST: marking may not
	// reorder the facts array.
	if resp.Facts[0].Weak {
		t.Errorf("facts[0] (the ranked hit) came back weak: %q", resp.Facts[0].Fact)
	}

	lines := kgedgeTripleLines(resp.Triples)
	if len(lines) != len(resp.Facts)+1 {
		t.Fatalf("triples has %d lines for %d facts, want facts+1:\n%s", len(lines), len(resp.Facts), resp.Triples)
	}
	idx := -1
	for i, ln := range lines {
		if ln == kgedgeDivider {
			if idx >= 0 {
				t.Fatalf("divider appears twice, at %d and %d", idx, i)
			}
			idx = i
		}
	}
	if idx != *resp.Strong {
		t.Errorf("divider is at line %d, want %d (immediately after the %d strong line(s))",
			idx, *resp.Strong, *resp.Strong)
	}
}

// TestKgedgeSearchNoMatchEmitsNoDivider: a query that matched nothing is the
// third grouping. Zero facts means zero weak facts, so there is nothing to
// divide — and `strong` is still 0 rather than absent, which is what lets the
// CLI tell "we found nothing" from "we found nothing you asked for".
func TestKgedgeSearchNoMatchEmitsNoDivider(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	kgedgeSeedEdge(t, st, "kraken", "cells", "spawns", "kraken spawns cells nightly")

	resp := kgedgeSearch(t, `{"query":"xylophone marzipan quokka"}`)
	if len(resp.Facts) != 0 {
		t.Fatalf("got %d facts for a query sharing no term with the corpus", len(resp.Facts))
	}
	if strings.TrimSpace(resp.Triples) != "" {
		t.Errorf("triples = %q, want empty", resp.Triples)
	}
	if strings.Contains(resp.Triples, kgedgeDivider) {
		t.Error("an empty result carries the divider")
	}
	if resp.Strong == nil || *resp.Strong != 0 {
		t.Errorf("strong = %v, want a non-nil 0", resp.Strong)
	}
}

// TestKgedgeSearchEveryFactCarriesWeak: `weak` is a required key on every
// element, not one emitted only when true. A fact whose weak key is omitted
// decodes to false in every client, so an omitempty here silently promotes
// every weak fact back to strong on the wire.
func TestKgedgeSearchEveryFactCarriesWeak(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	kgedgeSeedStar(t, st, 6)

	rec := postGraph(graphSearchHandler, "/api/graph/search", `{"query":"`+kgedgeQuery+`","limit":20}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Facts []map[string]json.RawMessage `json:"facts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Facts) == 0 {
		t.Fatal("no facts returned")
	}
	for i, f := range envelope.Facts {
		raw, ok := f["weak"]
		if !ok {
			t.Fatalf("facts[%d] has no \"weak\" key — an omitted flag reads as strong in every client", i)
		}
		if s := strings.TrimSpace(string(raw)); s != "true" && s != "false" {
			t.Errorf("facts[%d].weak = %s, want a boolean", i, raw)
		}
	}
}

// TestKgedgeSearchSurvivesCorruptEmbeddings: rows written by a different
// embedder (or torn mid-write) are the normal state of a corpus mid-migration.
// The search must still answer 200 with its facts — judging degrades, it never
// fails the request. Without a key configured the vector leg is skipped
// entirely, so this is the guard against any NEW eager decode Fix B might add
// on the marking path.
func TestKgedgeSearchSurvivesCorruptEmbeddings(t *testing.T) {
	st := useTestStore(t)
	kgedgeNoEmbedder(t)
	// "leviathan"/"pods", not "kraken"/"cells": both original names are
	// covered by kgedgeQuery's terms and would anchor, promoting the weak
	// fact to strong (see the fixture note in
	// TestKgedgeSearchStrongIsPresentEvenWhenZero).
	strong := kgedgeSeedEdge(t, st, "leviathan", "pods", "spawns", "kraken spawns cells nightly")
	weak := kgedgeSeedEdge(t, st, "leviathan", "harbour", "sits-in", "kraken sits quietly in a harbour")

	// Length 3: not a multiple of 4, so DecodeVector calls it a corrupt row.
	if err := st.SetEmbedding("edge", strong, []byte{0x01, 0x02, 0x03}); err != nil {
		t.Fatal(err)
	}
	// A well-formed blob of the WRONG dimension — one float32 where the
	// configured model produces 1024.
	if err := st.SetEmbedding("edge", weak, []byte{0x00, 0x00, 0x80, 0x3f}); err != nil {
		t.Fatal(err)
	}

	resp := kgedgeSearch(t, `{"query":"`+kgedgeQuery+`"}`)
	if len(resp.Facts) != 2 {
		t.Fatalf("got %d facts, want both — a corrupt blob must cost a JUDGMENT, never a fact", len(resp.Facts))
	}
	if resp.Strong == nil {
		t.Fatal("strong key absent")
	}
	// The lexical fallback still judges: three shared words vs one.
	if *resp.Strong != 1 {
		t.Errorf("strong = %d, want 1 — the fallback judges when no vector is usable", *resp.Strong)
	}
}

// TestKgedgeSearchRefusalsAreUnchanged re-pins the gates Fix B must not have
// disturbed, since the new keys are added in the same serialization step the
// error path skips: a refusal must stay a bare {"error":...} rather than
// growing a `strong: 0` that reads as a legitimate empty answer.
func TestKgedgeSearchRefusalsAreUnchanged(t *testing.T) {
	useTestStore(t)
	kgedgeNoEmbedder(t)

	for _, body := range []string{`{"query":"   "}`, `{"limit":5}`, `{"query":"x","weak":true}`} {
		rec := postGraph(graphSearchHandler, "/api/graph/search", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"strong"`) {
			t.Errorf("%s: the refusal carries a strong count: %s", body, rec.Body.String())
		}
	}
}
