package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// graphfact_contract_test.go — the LOGIC pins for slice S2b of
// artifacts/50-contract.md: GET /api/graph/fact and GET /api/graph/episode,
// the two dereference reads that turn an injected [F# E#] handle back into
// what it stands for.
//
// The wire shapes are asserted through LOCAL types and raw key sets rather
// than through the handlers' own DTOs. That is deliberate: every key here is a
// promise to a reader in another process (`k graph get`, and a human reading
// its output), and a test that decoded into webGraphFactFull would keep
// passing through a rename that broke every one of those readers.
//
// The last test in the file is the cross-slice round-trip pin: a handle the
// RENDERER produced, resolved by the API to the same edge with the byte-exact
// full sentence. It is the one assertion that ties S1 and S2 together, and it
// runs against a real store with no embedder reachable, so it is BM25-only and
// never makes a paid call.
//
// Refusals — 405, missing/malformed id, unknown id, no store — are the sibling
// edge suite's.

// --- shared plumbing ---

// fxcGetGraph drives one of the read handlers directly, which is the right
// altitude for a route whose whole job is a store read plus a shape.
func fxcGetGraph(t *testing.T, h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// fxcDecodeObject decodes one JSON object into its raw members, which is what
// lets a case assert the exact key SET as well as the values.
func fxcDecodeObject(t *testing.T, what string, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s is not a JSON object: %v (raw %s)", what, err, raw)
	}
	return m
}

// fxcAssertKeys pins an object's key set EXACTLY — a missing key and an extra
// key are both failures, since "exactly these keys" is how the contract states
// every one of these shapes.
func fxcAssertKeys(t *testing.T, what string, obj map[string]json.RawMessage, want ...string) {
	t.Helper()
	var got []string
	for k := range obj {
		got = append(got, k)
	}
	// `want` aliases the caller's slice when it was passed with `...`, so it is
	// copied before sorting — a helper that reorders a package-level fixture as
	// a side effect is a helper that makes the next case depend on this one.
	wantCopy := append([]string{}, want...)
	sort.Strings(got)
	sort.Strings(wantCopy)
	if strings.Join(got, ",") != strings.Join(wantCopy, ",") {
		t.Errorf("%s keys = [%s], want exactly [%s]", what, strings.Join(got, ","), strings.Join(wantCopy, ","))
	}
}

func fxcString(t *testing.T, what string, obj map[string]json.RawMessage, key string) string {
	t.Helper()
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("%s has no %q key", what, key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("%s.%s is not a string: %v (raw %s)", what, key, err, raw)
	}
	return s
}

func fxcNumber(t *testing.T, what string, obj map[string]json.RawMessage, key string) int64 {
	t.Helper()
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("%s has no %q key", what, key)
	}
	var n int64
	if err := json.Unmarshal(raw, &n); err != nil {
		t.Fatalf("%s.%s is not a number: %v (raw %s)", what, key, err, raw)
	}
	return n
}

// The three frozen key sets, quoted from the contract.
var (
	fxcFactFullKeys = []string{"id", "src", "dst", "predicate", "fact", "strength",
		"occurrences", "valid_from", "valid_until", "created_at", "expired_at", "episode"}
	fxcSiblingKeys = []string{"id", "src", "dst", "predicate", "fact", "strength",
		"occurrences", "valid_until"}
	fxcNodeKeys = []string{"id", "name", "type", "description", "x", "y"}
)

// fxcGraphStore opens a store wired the way `kraken serve` wires one, with no
// embedder reachable: HOME points at a throwaway dir (so ~/.kraken/openai.env
// cannot be found) and OPENAI_API_KEY is cleared, which makes
// core.NewEmbedder return ErrNoEmbedder and every retrieval BM25-only. No test
// in this file may make a network call.
func fxcGraphStore(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	return st
}

func fxcNode(t *testing.T, st *store.Store, name, kind, desc string) int64 {
	t.Helper()
	id, err := st.UpsertNode(store.GraphNode{Name: name, Type: kind, Description: desc})
	if err != nil {
		t.Fatalf("upsert node %q: %v", name, err)
	}
	return id
}

func fxcEdge(t *testing.T, st *store.Store, e store.GraphEdge) int64 {
	t.Helper()
	id, added, err := st.InsertEdge(e)
	if err != nil || !added {
		t.Fatalf("insert edge %q: added=%v err=%v", e.Fact, added, err)
	}
	return id
}

func fxcEpisode(t *testing.T, st *store.Store, ep store.Episode) int64 {
	t.Helper()
	id, _, err := st.AddEpisode(ep)
	if err != nil {
		t.Fatalf("add episode %q: %v", ep.Ref, err)
	}
	return id
}

// --- GET /api/graph/fact ---

// fxcTheFact is deliberately awkward: a multi-byte em dash, curly quotes and
// internal punctuation. "Byte-exact" is a claim that survives exactly this
// kind of sentence or it is not a claim at all.
const fxcTheFact = "kraken serve is the single writer of kraken.db — every other process, " +
	"including a cell's own “kraken” binary, reaches it over HTTP."

// TestFxcGraphFactReturnsTheStoredSentenceByteExact is the headline of S2:
// "`fact` is the stored sentence BYTE-EXACT — untruncated, never clause-cut."
//
// The whole feature exists because the injected line is a SUMMARY; a
// dereference that returned a second summary would answer the question the
// caller already had an answer to.
//
// It also pins the bundle's key set and both endpoint glosses, since the
// contract states the top-level object as "exactly these keys" and the
// endpoints as names rather than ids.
func TestFxcGraphFactReturnsTheStoredSentenceByteExact(t *testing.T) {
	st := fxcGraphStore(t)

	epID := fxcEpisode(t, st, store.Episode{
		Kind: "transcript", Ref: "sess-42.jsonl",
		Content: "the provenance text that must never inline into a fact bundle",
		Hash:    "fxc-fact-ep-1", TS: 1700000000000,
	})
	srcID := fxcNode(t, st, "kraken", "system", "the CLI and daemon that manages cells")
	if err := st.AddAlias(srcID, "k"); err != nil {
		t.Fatalf("add alias: %v", err)
	}
	dstID := fxcNode(t, st, "zeus", "agent", "a cell's only working agent")

	edgeID := fxcEdge(t, st, store.GraphEdge{
		SrcID: srcID, DstID: dstID, Predicate: "owns", Fact: fxcTheFact,
		EpisodeID: epID, Strength: 7, Occurrences: 3, ValidFrom: 1700000001000,
	})

	rec := fxcGetGraph(t, graphFactHandler, fmt.Sprintf("/api/graph/fact?id=%d", edgeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/fact?id=%d = %d, body %s", edgeID, rec.Code, rec.Body.String())
	}

	top := fxcDecodeObject(t, "response", rec.Body.Bytes())
	fxcAssertKeys(t, "response", top, "fact", "src", "dst", "episode", "siblings")

	// --- the fact itself ---
	fact := fxcDecodeObject(t, "fact", top["fact"])
	fxcAssertKeys(t, "fact", fact, fxcFactFullKeys...)

	if got := fxcString(t, "fact", fact, "fact"); got != fxcTheFact {
		t.Errorf("fact.fact = %q,\nwant the stored sentence byte-exact %q", got, fxcTheFact)
	}
	if got := fxcNumber(t, "fact", fact, "id"); got != edgeID {
		t.Errorf("fact.id = %d, want %d", got, edgeID)
	}
	// src/dst are node NAMES here, not ids — the fact is meant to be readable
	// on its own.
	if got := fxcString(t, "fact", fact, "src"); got != "kraken" {
		t.Errorf("fact.src = %q, want the node NAME %q", got, "kraken")
	}
	if got := fxcString(t, "fact", fact, "dst"); got != "zeus" {
		t.Errorf("fact.dst = %q, want the node NAME %q", got, "zeus")
	}
	if got := fxcString(t, "fact", fact, "predicate"); got != "owns" {
		t.Errorf("fact.predicate = %q, want %q", got, "owns")
	}
	if got := fxcNumber(t, "fact", fact, "strength"); got != 7 {
		t.Errorf("fact.strength = %d, want 7", got)
	}
	if got := fxcNumber(t, "fact", fact, "occurrences"); got != 3 {
		t.Errorf("fact.occurrences = %d, want 3", got)
	}
	if got := fxcNumber(t, "fact", fact, "valid_from"); got != 1700000001000 {
		t.Errorf("fact.valid_from = %d, want 1700000001000", got)
	}
	// A live fact: both "still true" stamps are the real answer 0, and the
	// contract forbids omitempty precisely so 0 is served rather than dropped.
	if got := fxcNumber(t, "fact", fact, "valid_until"); got != 0 {
		t.Errorf("fact.valid_until = %d, want 0 on a live fact", got)
	}
	if got := fxcNumber(t, "fact", fact, "expired_at"); got != 0 {
		t.Errorf("fact.expired_at = %d, want 0 on a live fact", got)
	}
	// The edge's own episode handle — the id, matching the E# the renderer
	// prints. The bundle's separate `episode` object is what carries kind/ref.
	if got := fxcNumber(t, "fact", fact, "episode"); got != epID {
		t.Errorf("fact.episode = %d, want the edge's episode id %d", got, epID)
	}

	// --- the endpoints ---
	src := fxcDecodeObject(t, "src", top["src"])
	fxcAssertKeys(t, "src", src, append(append([]string{}, fxcNodeKeys...), "aliases")...)
	if got := fxcNumber(t, "src", src, "id"); got != srcID {
		t.Errorf("src.id = %d, want %d", got, srcID)
	}
	if got := fxcString(t, "src", src, "name"); got != "kraken" {
		t.Errorf("src.name = %q, want %q", got, "kraken")
	}
	if got := fxcString(t, "src", src, "type"); got != "system" {
		t.Errorf("src.type = %q, want %q", got, "system")
	}
	if got := fxcString(t, "src", src, "description"); got != "the CLI and daemon that manages cells" {
		t.Errorf("src.description = %q, want the stored gloss", got)
	}
	var aliases []string
	if err := json.Unmarshal(src["aliases"], &aliases); err != nil {
		t.Fatalf("src.aliases is not a string list: %v (raw %s)", err, src["aliases"])
	}
	if len(aliases) != 1 || aliases[0] != "k" {
		t.Errorf("src.aliases = %v, want [k] — populated via NodeAliases", aliases)
	}
	// x/y are served as the "not positioned" 0s the search route already uses.
	if got := fxcNumber(t, "src", src, "x"); got != 0 {
		t.Errorf("src.x = %d, want 0 (not positioned)", got)
	}

	dst := fxcDecodeObject(t, "dst", top["dst"])
	// A node with no aliases omits the key entirely — the one omitempty field
	// in the DTO.
	fxcAssertKeys(t, "dst", dst, fxcNodeKeys...)
	if got := fxcString(t, "dst", dst, "name"); got != "zeus" {
		t.Errorf("dst.name = %q, want %q", got, "zeus")
	}
	if got := fxcString(t, "dst", dst, "type"); got != "agent" {
		t.Errorf("dst.type = %q, want %q", got, "agent")
	}

	// --- the episode stub ---
	ep := fxcDecodeObject(t, "episode", top["episode"])
	fxcAssertKeys(t, "episode", ep, "id", "kind", "ref")
	if got := fxcNumber(t, "episode", ep, "id"); got != epID {
		t.Errorf("episode.id = %d, want %d", got, epID)
	}
	if got := fxcString(t, "episode", ep, "kind"); got != "transcript" {
		t.Errorf("episode.kind = %q, want %q", got, "transcript")
	}
	if got := fxcString(t, "episode", ep, "ref"); got != "sess-42.jsonl" {
		t.Errorf("episode.ref = %q, want %q", got, "sess-42.jsonl")
	}
	// The locked decision, asserted as an ABSENCE: episode content never
	// inlines into a fact bundle. Present-key assertions cannot catch this
	// one — the content would simply ride along and nothing would fail.
	if _, ok := ep["content"]; ok {
		t.Errorf("the fact bundle inlined the episode's content: %s", top["episode"])
	}
	if strings.Contains(rec.Body.String(), "the provenance text that must never inline") {
		t.Errorf("the episode's content text reached the fact bundle:\n%s", rec.Body.String())
	}

	// --- siblings: present, empty, and serialized as [] rather than null ---
	sib := fxcDecodeObject(t, "siblings", top["siblings"])
	fxcAssertKeys(t, "siblings", sib, "src", "dst")
	for _, key := range []string{"src", "dst"} {
		if string(sib[key]) == "null" {
			t.Errorf("siblings.%s serialized as null, want [] — an empty list is a list", key)
		}
		var list []json.RawMessage
		if err := json.Unmarshal(sib[key], &list); err != nil {
			t.Fatalf("siblings.%s is not an array: %v (raw %s)", key, err, sib[key])
		}
		if len(list) != 0 {
			t.Errorf("siblings.%s = %d facts, want 0 — the only edge in the graph is the dereferenced one", key, len(list))
		}
	}
}

// TestFxcGraphFactSiblingsHonorTheCapPerEndpoint pins two rules at once: the
// sibling lists are capped at graphFactSiblingCap PER ENDPOINT, and the
// dereferenced edge is excluded from both.
//
// Excluding the fact itself is what makes the list mean "what else touches
// these two entities". Without it the first sibling is always the fact the
// caller just asked about, which is one of the ten slots spent saying nothing.
func TestFxcGraphFactSiblingsHonorTheCapPerEndpoint(t *testing.T) {
	st := fxcGraphStore(t)

	srcID := fxcNode(t, st, "hubnode", "component", "the shared endpoint")
	dstID := fxcNode(t, st, "targetnode", "component", "the other endpoint")
	edgeID := fxcEdge(t, st, store.GraphEdge{
		SrcID: srcID, DstID: dstID, Predicate: "calibrates",
		Fact: "the hubnode calibrates the targetnode once per tick", Strength: 6, Occurrences: 9,
	})

	// Twenty more live edges on the src endpoint: comfortably past the cap, so
	// a cap of ten is distinguishable from "however many there were".
	for i := 0; i < 20; i++ {
		leaf := fxcNode(t, st, fmt.Sprintf("srcleaf%02d", i), "component", "")
		fxcEdge(t, st, store.GraphEdge{
			SrcID: srcID, DstID: leaf, Predicate: "touches",
			Fact: fmt.Sprintf("the hubnode touches srcleaf%02d during the pass", i), Strength: 5, Occurrences: 1,
		})
	}
	// Two on the dst endpoint: under the cap, so the cap is not what shapes it.
	for i := 0; i < 2; i++ {
		leaf := fxcNode(t, st, fmt.Sprintf("dstleaf%02d", i), "component", "")
		fxcEdge(t, st, store.GraphEdge{
			SrcID: dstID, DstID: leaf, Predicate: "feeds",
			Fact: fmt.Sprintf("the targetnode feeds dstleaf%02d afterwards", i), Strength: 5, Occurrences: 1,
		})
	}

	rec := fxcGetGraph(t, graphFactHandler, fmt.Sprintf("/api/graph/fact?id=%d", edgeID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/fact?id=%d = %d, body %s", edgeID, rec.Code, rec.Body.String())
	}

	top := fxcDecodeObject(t, "response", rec.Body.Bytes())
	sib := fxcDecodeObject(t, "siblings", top["siblings"])

	for _, c := range []struct {
		key  string
		want int
	}{
		{"src", graphFactSiblingCap},
		{"dst", 2},
	} {
		var list []json.RawMessage
		if err := json.Unmarshal(sib[c.key], &list); err != nil {
			t.Fatalf("siblings.%s is not an array: %v", c.key, err)
		}
		if len(list) != c.want {
			t.Errorf("siblings.%s = %d facts, want %d", c.key, len(list), c.want)
		}
		for i, raw := range list {
			obj := fxcDecodeObject(t, fmt.Sprintf("siblings.%s[%d]", c.key, i), raw)
			fxcAssertKeys(t, fmt.Sprintf("siblings.%s[%d]", c.key, i), obj, fxcSiblingKeys...)
			if got := fxcNumber(t, "sibling", obj, "id"); got == edgeID {
				t.Errorf("siblings.%s carries the dereferenced edge %d itself", c.key, edgeID)
			}
			// Names, not ids, exactly as on the fact itself.
			if got := fxcString(t, "sibling", obj, "src"); got == "" {
				t.Errorf("siblings.%s[%d].src is empty, want a node name", c.key, i)
			}
		}
	}
	if graphFactSiblingCap != 10 {
		t.Errorf("graphFactSiblingCap = %d, want the contract's 10", graphFactSiblingCap)
	}
}

// --- GET /api/graph/episode ---

// TestFxcGraphEpisodeInlinesContentAndCarriesItsEdges is the episode route's
// whole purpose, and the mirror image of the rule above: content DOES inline
// here, because pulling the provenance text is what the caller asked for.
//
// The edges list is "every edge EdgesForEpisode returns (live and
// invalidated, ascending id)". The invalidated one is not an edge case here —
// a dead fact's handle may be cited in an old brief, and an episode that
// silently dropped the facts it retracted would be a provenance record that
// hides its own corrections.
func TestFxcGraphEpisodeInlinesContentAndCarriesItsEdges(t *testing.T) {
	st := fxcGraphStore(t)

	const content = "the transcript excerpt this episode was extracted from"
	epID := fxcEpisode(t, st, store.Episode{
		Kind: "transcript", Ref: "sess-77.jsonl", Content: content,
		Hash: "fxc-ep-77", TS: 1700000123000,
	})
	otherEp := fxcEpisode(t, st, store.Episode{
		Kind: "feedback", Ref: "note-9", Content: "a different provenance unit",
		Hash: "fxc-ep-other", TS: 1700000124000,
	})

	a := fxcNode(t, st, "alpha", "component", "first")
	b := fxcNode(t, st, "beta", "component", "second")
	c := fxcNode(t, st, "gamma", "component", "third")

	live := fxcEdge(t, st, store.GraphEdge{SrcID: a, DstID: b, Predicate: "calls",
		Fact: "alpha calls beta on every tick", EpisodeID: epID, Strength: 6, Occurrences: 2})
	dead := fxcEdge(t, st, store.GraphEdge{SrcID: b, DstID: c, Predicate: "blocks",
		Fact: "beta blocks gamma until the lock clears", EpisodeID: epID, Strength: 4, Occurrences: 1})
	foreign := fxcEdge(t, st, store.GraphEdge{SrcID: a, DstID: c, Predicate: "ignores",
		Fact: "alpha ignores gamma entirely", EpisodeID: otherEp, Strength: 3, Occurrences: 1})

	const deadUntil = 1700000500000
	if err := st.InvalidateEdge(dead, deadUntil); err != nil {
		t.Fatalf("invalidate edge: %v", err)
	}

	rec := fxcGetGraph(t, graphEpisodeHandler, fmt.Sprintf("/api/graph/episode?id=%d", epID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/graph/episode?id=%d = %d, body %s", epID, rec.Code, rec.Body.String())
	}

	top := fxcDecodeObject(t, "response", rec.Body.Bytes())
	fxcAssertKeys(t, "response", top, "episode", "edges")

	ep := fxcDecodeObject(t, "episode", top["episode"])
	fxcAssertKeys(t, "episode", ep, "id", "kind", "ref", "content", "ts")
	if got := fxcNumber(t, "episode", ep, "id"); got != epID {
		t.Errorf("episode.id = %d, want %d", got, epID)
	}
	if got := fxcString(t, "episode", ep, "kind"); got != "transcript" {
		t.Errorf("episode.kind = %q, want %q", got, "transcript")
	}
	if got := fxcString(t, "episode", ep, "ref"); got != "sess-77.jsonl" {
		t.Errorf("episode.ref = %q, want %q", got, "sess-77.jsonl")
	}
	if got := fxcString(t, "episode", ep, "content"); got != content {
		t.Errorf("episode.content = %q, want the stored text %q — this route's purpose is the provenance text", got, content)
	}
	if got := fxcNumber(t, "episode", ep, "ts"); got != 1700000123000 {
		t.Errorf("episode.ts = %d, want 1700000123000", got)
	}

	var edges []json.RawMessage
	if err := json.Unmarshal(top["edges"], &edges); err != nil {
		t.Fatalf("edges is not an array: %v (raw %s)", err, top["edges"])
	}
	if len(edges) != 2 {
		t.Fatalf("edges = %d, want exactly the episode's two (live and invalidated): %s", len(edges), top["edges"])
	}

	first := fxcDecodeObject(t, "edges[0]", edges[0])
	second := fxcDecodeObject(t, "edges[1]", edges[1])
	fxcAssertKeys(t, "edges[0]", first, fxcFactFullKeys...)
	fxcAssertKeys(t, "edges[1]", second, fxcFactFullKeys...)

	// Ascending id, which is EdgesForEpisode's stated ordering.
	if got := fxcNumber(t, "edges[0]", first, "id"); got != live {
		t.Errorf("edges[0].id = %d, want the lower id %d — the order is ascending id", got, live)
	}
	if got := fxcNumber(t, "edges[1]", second, "id"); got != dead {
		t.Errorf("edges[1].id = %d, want %d", got, dead)
	}
	// The invalidated one comes back WITH its closed windows visible.
	if got := fxcNumber(t, "edges[1]", second, "valid_until"); got != deadUntil {
		t.Errorf("the invalidated edge's valid_until = %d, want %d", got, deadUntil)
	}
	if got := fxcNumber(t, "edges[1]", second, "expired_at"); got == 0 {
		t.Errorf("the invalidated edge's expired_at = 0, want the system-time stamp InvalidateEdge wrote")
	}
	if got := fxcNumber(t, "edges[0]", first, "expired_at"); got != 0 {
		t.Errorf("the live edge's expired_at = %d, want 0", got)
	}
	// Names resolved, not ids.
	if got := fxcString(t, "edges[0]", first, "src"); got != "alpha" {
		t.Errorf("edges[0].src = %q, want the node NAME %q", got, "alpha")
	}
	// And nothing from another episode leaked in.
	if strings.Contains(rec.Body.String(), "alpha ignores gamma entirely") {
		t.Errorf("an edge from episode %d (id %d) appeared under episode %d:\n%s",
			otherEp, foreign, epID, rec.Body.String())
	}
}

// --- the cross-slice round-trip pin ---

// fxcHandleRE is the frozen line format read backwards: the handle exactly as
// a reader of the block would find it. Anchoring on the whole bracket rather
// than on a bare F<digits> is what makes this test fail when the LINE format
// drifts, which is the drift it exists to catch.
var fxcHandleRE = regexp.MustCompile(`\[F(\d+) E(\d+), seen \d+x\]`)

// fxcRoundTripFact is over graphFactRunes (180) on purpose: the block must
// SUMMARIZE it and the API must not. A fact short enough to survive the cutter
// intact would let a dereference that returned the block's own text pass.
const fxcRoundTripFact = "tokensPass runs inside kraken serve's daemon tick, remembers how many " +
	"bytes of each transcript it already summed, and is skipped under mirror mode so that the " +
	"observation never quietly becomes a write to somebody else's database."

// TestFxcRecallHandleRoundTripsToTheSameFact is the cross-slice obligation:
// "A renderer-produced line contains a handle the API resolves to the SAME
// edge; one test ties the two so they cannot drift."
//
// Everything here goes through exported seams — store.Open via useTestStore,
// jobs.UseStore, UpsertNode/InsertEdge, jobs.Recall, then the handler — so it
// measures the two halves as their real callers meet them rather than as this
// package's internals.
//
// Environment isolation is enforced rather than assumed (fxcGraphStore): HOME
// is a temp dir and OPENAI_API_KEY is cleared, so core.NewEmbedder returns
// ErrNoEmbedder, retrieval degrades to BM25-only, and no paid call is
// reachable from this test.
func TestFxcRecallHandleRoundTripsToTheSameFact(t *testing.T) {
	st := fxcGraphStore(t)

	epID := fxcEpisode(t, st, store.Episode{
		Kind: "transcript", Ref: "sess-roundtrip.jsonl",
		Content: "where the tokensPass fact came from", Hash: "fxc-roundtrip-ep", TS: 1700000200000,
	})
	src := fxcNode(t, st, "tokensPass", "component", "the daemon pass that keeps each job's token row current")
	dst := fxcNode(t, st, "kraken serve", "component", "the single process that owns the tick and the API")
	edgeID := fxcEdge(t, st, store.GraphEdge{
		SrcID: src, DstID: dst, Predicate: "runs-inside", Fact: fxcRoundTripFact,
		EpisodeID: epID, Strength: 8, Occurrences: 2,
	})

	// The query shares several ≥4-rune content words with the fact sentence,
	// which is what the relevance gate's lexical arm needs when no embedder is
	// reachable.
	const query = "how does tokensPass relate to kraken serve, its daemon tick and transcript bytes"

	block := jobs.Recall(context.Background(), st, jobs.RecallRequest{
		SessionID: "fxc-roundtrip-session",
		Event:     "UserPromptSubmit",
		Prompt:    query,
	})
	if block == "" {
		t.Fatal("jobs.Recall produced no block for a query sharing several content words with the only fact in the graph — " +
			"the round trip cannot be measured without a rendered line")
	}

	m := fxcHandleRE.FindStringSubmatch(block)
	if m == nil {
		t.Fatalf("the rendered block carries no [F<id> E<id>, seen Nx] handle:\n%s", block)
	}
	factID, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatalf("unparseable F handle %q: %v", m[1], err)
	}
	episodeHandle, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		t.Fatalf("unparseable E handle %q: %v", m[2], err)
	}
	if factID != edgeID {
		t.Errorf("the block cited F%d, but the only edge in the graph is %d", factID, edgeID)
	}
	if episodeHandle != epID {
		t.Errorf("the block cited E%d, but the edge's episode is %d", episodeHandle, epID)
	}
	// The premise this fixture rests on: the block SUMMARIZED the fact, so the
	// dereference is answering a question the line could not.
	if !strings.Contains(block, "…") {
		t.Errorf("the block did not truncate a %d-rune fact, so this case no longer proves the API returns MORE than the line:\n%s",
			len([]rune(fxcRoundTripFact)), block)
	}

	// Now the other half: the handle, resolved.
	rec := fxcGetGraph(t, graphFactHandler, fmt.Sprintf("/api/graph/fact?id=%d", factID))
	if rec.Code != http.StatusOK {
		t.Fatalf("dereferencing the block's own handle F%d = %d, body %s", factID, rec.Code, rec.Body.String())
	}
	top := fxcDecodeObject(t, "response", rec.Body.Bytes())
	fact := fxcDecodeObject(t, "fact", top["fact"])

	if got := fxcNumber(t, "fact", fact, "id"); got != factID {
		t.Errorf("F%d dereferenced to edge %d", factID, got)
	}
	if got := fxcString(t, "fact", fact, "fact"); got != fxcRoundTripFact {
		t.Errorf("F%d dereferenced to\n%q\nwant the stored sentence byte-exact\n%q", factID, got, fxcRoundTripFact)
	}
}
