package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/store"
)

// graphfact_edge_test.go — the two dereference reads' refusals and
// degradations (50-contract.md, "S2b — API"), authored blind. The bundle's
// happy shape (every key present, siblings ordered and capped) belongs to the
// sibling contract suite and to contract_test.go; what lives here is what the
// routes REFUSE (bad ids, wrong verb, unknown row) and what they must serve
// ANYWAY (an invalidated fact, a fact whose endpoint node row has gone).
//
// useTestStore (storemode_test.go) is CALLED, never redeclared.

// fxeGetGraph drives a handler directly with a GET, the idiom graph_test.go
// uses for the node/pending reads.
func fxeGetGraph(h http.HandlerFunc, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// fxeErrBody pulls the {"error": "..."} sentence out of a refusal. Declared
// rather than reusing graph_test.go's bodyError so this file stands alone.
func fxeErrBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("response is not an {\"error\":...} object: %v (body %s)", err, rec.Body.String())
	}
	return e.Error
}

type fxeNode struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases"`
}

// fxeFullFact decodes the keys webGraphFactFull is frozen to. `episode` is
// deliberately absent from this struct: no assertion here reads it, and
// declaring a type for a key this file does not test would couple the file to
// a shape it has no business pinning (contract_test.go owns that).
type fxeFullFact struct {
	ID          int64  `json:"id"`
	Src         string `json:"src"`
	Dst         string `json:"dst"`
	Predicate   string `json:"predicate"`
	Fact        string `json:"fact"`
	Strength    int64  `json:"strength"`
	Occurrences int64  `json:"occurrences"`
	ValidFrom   int64  `json:"valid_from"`
	ValidUntil  int64  `json:"valid_until"`
	CreatedAt   int64  `json:"created_at"`
	ExpiredAt   int64  `json:"expired_at"`
}

type fxeSiblings struct {
	Src json.RawMessage `json:"src"`
	Dst json.RawMessage `json:"dst"`
}

type fxeFactBundle struct {
	Fact     fxeFullFact     `json:"fact"`
	Src      fxeNode         `json:"src"`
	Dst      fxeNode         `json:"dst"`
	Siblings fxeSiblings     `json:"siblings"`
	Episode  json.RawMessage `json:"episode"`
}

type fxeEpisodeRow struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Content string `json:"content"`
	TS      int64  `json:"ts"`
}

type fxeEpisodeBundle struct {
	Episode *fxeEpisodeRow  `json:"episode"`
	Edges   json.RawMessage `json:"edges"`
}

// fxeSeedEdge plants two nodes and one edge between them, returning the ids.
func fxeSeedEdge(t *testing.T, st *store.Store, srcName, dstName, fact string) (edgeID, srcID, dstID int64) {
	t.Helper()
	srcID, err := st.UpsertNode(store.GraphNode{Name: srcName, Type: "component", Description: "the source"})
	if err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	dstID, err = st.UpsertNode(store.GraphNode{Name: dstName, Type: "component", Description: "the destination"})
	if err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	edgeID, _, err = st.InsertEdge(store.GraphEdge{
		SrcID: srcID, DstID: dstID, Predicate: "relates-to", Fact: fact, Strength: 7, Occurrences: 3,
	})
	if err != nil {
		t.Fatalf("insert edge: %v", err)
	}
	return edgeID, srcID, dstID
}

// Obligation: "missing id → 400 {"error":"missing id"}; non-integer or <= 0 →
// 400 with the `bad id … — want a positive integer …` shape" (Id parsing
// follows graphNodeHandler exactly). A malformed id must never fall through
// to a lookup of some other row.
func TestFxeGraphDereferenceRefusesMalformedIDs(t *testing.T) {
	useTestStore(t)

	for _, route := range []struct {
		name string
		h    http.HandlerFunc
		path string
	}{
		{"fact", graphFactHandler, "/api/graph/fact"},
		{"episode", graphEpisodeHandler, "/api/graph/episode"},
	} {
		t.Run(route.name, func(t *testing.T) {
			// The missing-id case carries its own sentence.
			rec := fxeGetGraph(route.h, route.path)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("no id at all: status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := fxeErrBody(t, rec); got != "missing id" {
				t.Errorf("no id at all: error = %q, want %q", got, "missing id")
			}

			for _, q := range []string{
				"?id=",                     // present but empty
				"?id=abc",                  // not a number
				"?id=0",                    // ids are positive
				"?id=-3",                   // ... and never negative
				"?id=12.5",                 // a float is not an integer id
				"?id=1e3",                  // nor scientific notation
				"?id=%20",                  // whitespace only
				"?id=1,2",                  // a list is not an id
				"?id=99999999999999999999", // int64 overflow
			} {
				rec := fxeGetGraph(route.h, route.path+q)
				if rec.Code != http.StatusBadRequest {
					t.Errorf("%s: status = %d, want 400 (body %s)", q, rec.Code, rec.Body.String())
					continue
				}
				if got := fxeErrBody(t, rec); got == "" {
					t.Errorf("%s: refusal carried no error sentence", q)
				}
			}
		})
	}
}

// Obligation: "Unknown edge id → 404 {"error":"no fact with id N"}" and
// "Unknown episode id → 404 {"error":"no episode with id N"}". A 200 with a
// null body would let `k graph get` print "nothing" for a handle that is
// merely mistyped.
func TestFxeGraphDereferenceRefusesUnknownRowsWithA404(t *testing.T) {
	useTestStore(t)

	rec := fxeGetGraph(graphFactHandler, "/api/graph/fact?id=999")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown edge: status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if got := fxeErrBody(t, rec); got != "no fact with id 999" {
		t.Errorf("unknown edge: error = %q, want %q", got, "no fact with id 999")
	}

	rec = fxeGetGraph(graphEpisodeHandler, "/api/graph/episode?id=4242")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown episode: status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if got := fxeErrBody(t, rec); got != "no episode with id 4242" {
		t.Errorf("unknown episode: error = %q, want %q", got, "no episode with id 4242")
	}
}

// Obligation: "Both are GET-only (405 {"error":"GET only"} otherwise, house
// pattern)."
func TestFxeGraphDereferenceRefusesNonGET(t *testing.T) {
	useTestStore(t)

	for _, route := range []struct {
		name string
		h    http.HandlerFunc
		path string
	}{
		{"fact", graphFactHandler, "/api/graph/fact?id=1"},
		{"episode", graphEpisodeHandler, "/api/graph/episode?id=1"},
	} {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			t.Run(route.name+"/"+method, func(t *testing.T) {
				rec := httptest.NewRecorder()
				route.h(rec, httptest.NewRequest(method, route.path, nil))
				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("status = %d, want 405 (body %s)", rec.Code, rec.Body.String())
				}
				if got := fxeErrBody(t, rec); got != "GET only" {
					t.Errorf("error = %q, want %q", got, "GET only")
				}
			})
		}
	}
}

// Obligation: "An INVALIDATED edge still dereferences (its expired_at visible
// in the bundle) — a dead fact's handle may be cited in an old brief." The
// stamp is what lets the reader see the fact is retired rather than trusting
// it as current.
func TestFxeGraphFactServesAnInvalidatedEdgeWithItsStamps(t *testing.T) {
	st := useTestStore(t)

	const fact = "kraken serve — the single writer — owns kraken.db, and it never yields the lock to anyone at all, ever."
	id, _, _ := fxeSeedEdge(t, st, "kraken serve", "kraken.db", fact)
	const stamp = int64(1769040000000)
	if err := st.InvalidateEdge(id, stamp); err != nil {
		t.Fatalf("invalidate: %v", err)
	}

	rec := fxeGetGraph(graphFactHandler, fmt.Sprintf("/api/graph/fact?id=%d", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — an invalidated fact must still dereference (body %s)", rec.Code, rec.Body.String())
	}
	var got fxeFactBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Fact.ID != id {
		t.Errorf("fact.id = %d, want %d", got.Fact.ID, id)
	}
	if got.Fact.ExpiredAt == 0 {
		t.Errorf("fact.expired_at = 0 on an invalidated edge — the caller cannot tell a retired fact from a live one")
	}
	if got.Fact.ValidUntil != stamp {
		t.Errorf("fact.valid_until = %d, want the world-time instant passed to InvalidateEdge (%d)", got.Fact.ValidUntil, stamp)
	}
	// Retired is not degraded: the sentence is still served byte-exact.
	if got.Fact.Fact != fact {
		t.Errorf("fact.fact = %q, want the stored sentence byte-exact", got.Fact.Fact)
	}
}

// Obligation: "A missing node row degrades to {"id": <id>, "name": "#<id>"} —
// never a 404 (the caller named a fact, and the fact exists)", and
// webGraphFactFull's src/dst use "the existing graphEndpointName fallback
// #<id> when the node row is missing ... a missing gloss never drops or 500s
// it". kg_edges carries no foreign key to kg_nodes, so this state is
// reachable, not hypothetical.
func TestFxeGraphFactDegradesAMissingEndpointNodeToAHashName(t *testing.T) {
	st := useTestStore(t)

	const missingSrc, missingDst = int64(90001), int64(90002)
	id, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: missingSrc, DstID: missingDst, Predicate: "relates-to",
		Fact: "an orphaned fact whose endpoint rows are not there", Strength: 4, Occurrences: 1,
	})
	if err != nil {
		t.Fatalf("insert edge: %v", err)
	}

	rec := fxeGetGraph(graphFactHandler, fmt.Sprintf("/api/graph/fact?id=%d", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a missing gloss must never drop the fact (body %s)", rec.Code, rec.Body.String())
	}
	var got fxeFactBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Fact.Src != fmt.Sprintf("#%d", missingSrc) {
		t.Errorf("fact.src = %q, want %q", got.Fact.Src, fmt.Sprintf("#%d", missingSrc))
	}
	if got.Fact.Dst != fmt.Sprintf("#%d", missingDst) {
		t.Errorf("fact.dst = %q, want %q", got.Fact.Dst, fmt.Sprintf("#%d", missingDst))
	}
	if got.Src.ID != missingSrc || got.Src.Name != fmt.Sprintf("#%d", missingSrc) {
		t.Errorf("src node = %+v, want {id:%d name:#%d}", got.Src, missingSrc, missingSrc)
	}
	if got.Dst.ID != missingDst || got.Dst.Name != fmt.Sprintf("#%d", missingDst) {
		t.Errorf("dst node = %+v, want {id:%d name:#%d}", got.Dst, missingDst, missingDst)
	}
}

// Obligation: "Empty lists serve as [], not null (make: zero-len slices)." A
// null here is not cosmetic — every client of this bundle has to branch on it
// separately, and the CLI prints the JSON straight through to an agent.
func TestFxeGraphFactSiblingListsAreEmptyArraysForALeafFact(t *testing.T) {
	st := useTestStore(t)

	id, _, _ := fxeSeedEdge(t, st, "kraken", "zeus", "kraken spawns zeus into the cell's first pane")

	rec := fxeGetGraph(graphFactHandler, fmt.Sprintf("/api/graph/fact?id=%d", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got fxeFactBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	// The dereferenced edge itself is excluded from both lists, so the ONLY
	// edge in the corpus leaves both empty.
	if s := string(got.Siblings.Src); s != "[]" {
		t.Errorf("siblings.src = %s, want [] — an empty list, present and not null", s)
	}
	if s := string(got.Siblings.Dst); s != "[]" {
		t.Errorf("siblings.dst = %s, want [] — an empty list, present and not null", s)
	}
	// And the whole key is present rather than omitted.
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["siblings"]; !ok {
		t.Errorf("the response has no `siblings` key at all: %s", rec.Body.String())
	}
}

// Obligation: "`episode`: ... `null` when the edge's episode row is absent."
// An edge with episode_id 0 (a hand-planted row) has no provenance to serve,
// and the caller must be told that rather than shown a zero-valued object.
func TestFxeGraphFactServesANullEpisodeWhenTheRowIsAbsent(t *testing.T) {
	st := useTestStore(t)

	id, _, _ := fxeSeedEdge(t, st, "kraken", "zeus", "a fact with no provenance episode behind it")

	rec := fxeGetGraph(graphFactHandler, fmt.Sprintf("/api/graph/fact?id=%d", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	ep, ok := raw["episode"]
	if !ok {
		t.Fatalf("the response has no `episode` key at all: %s", rec.Body.String())
	}
	if string(ep) != "null" {
		t.Errorf("episode = %s, want null — the edge cites no episode row", ep)
	}
}

// Obligation: "`edges`: [webGraphFactFull...] ... [] when the episode yielded
// no edges", with EdgesForEpisode's own "Empty result for an unknown episode
// is (nil, nil), not an error" one step below it. An episode swept but never
// drained is exactly this state.
func TestFxeGraphEpisodeWithNoEdgesServesAnEmptyArray(t *testing.T) {
	st := useTestStore(t)

	epID, _, err := st.AddEpisode(store.Episode{
		Kind: "job", Ref: "some-job", Content: "a spec nobody has drained yet", Hash: "fxe-empty-episode", TS: 1,
	})
	if err != nil {
		t.Fatalf("add episode: %v", err)
	}

	rec := fxeGetGraph(graphEpisodeHandler, fmt.Sprintf("/api/graph/episode?id=%d", epID))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var got fxeEpisodeBundle
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if got.Episode == nil || got.Episode.ID != epID {
		t.Fatalf("episode = %+v, want the seeded row (id %d)", got.Episode, epID)
	}
	if s := string(got.Edges); s != "[]" {
		t.Errorf("edges = %s, want [] — an episode that yielded nothing is not an error and not null", s)
	}
}

// Obligation: "Both routes are READS: registered UNGATED under --mirror
// (beside /api/graph/search etc.)." Driven through Register so the assertion
// covers the WIRING rather than the handler — a route gated in register.go
// would still pass a handler-level probe.
func TestFxeGraphDereferenceStaysUngatedInMirrorMode(t *testing.T) {
	st := useTestStore(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	id, _, _ := fxeSeedEdge(t, st, "kraken", "zeus", "kraken spawns zeus into the cell's first pane")
	epID, _, err := st.AddEpisode(store.Episode{
		Kind: "job", Ref: "some-job", Content: "provenance", Hash: "fxe-mirror-episode", TS: 1,
	})
	if err != nil {
		t.Fatalf("add episode: %v", err)
	}

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range []string{
		fmt.Sprintf("/api/graph/fact?id=%d", id),
		fmt.Sprintf("/api/graph/episode?id=%d", epID),
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s: 403 under mirror mode — a dereference is a READ and must stay ungated", path)
			continue
		}
		if strings.Contains(rec.Body.String(), "disabled in mirror mode") {
			t.Errorf("%s: body carries the mirror refusal: %s", path, rec.Body.String())
		}
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200 — the route must be registered and reachable (body %s)", path, rec.Code, rec.Body.String())
		}
	}
}

// Obligation: "apiContract stays 5 (additive routes never bump it)." Two new
// keys and two new routes are additive by construction; a bump here would
// make every installed iOS build refuse the board.
func TestFxeGraphDereferenceDoesNotBumpTheContract(t *testing.T) {
	if apiContract != 5 {
		t.Errorf("apiContract = %d, want 5 — the dereference routes are additive", apiContract)
	}
	if graphFactSiblingCap != 10 {
		t.Errorf("graphFactSiblingCap = %d, want 10", graphFactSiblingCap)
	}
}
