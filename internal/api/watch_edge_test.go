package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"kraken/internal/store"
)

// watch_edge_test.go — the EDGE dimension of 50-contract.md's slice A:
// what the two watchlist routes REFUSE, and what the frozen wire shape must
// NOT grow. Invariants and happy paths belong to the sibling blind file;
// nothing here duplicates them.
//
// Every package-level identifier is prefixed `edge` per the contract's
// "Blind test files" section — this package's test namespace is shared.

// --- helpers ---------------------------------------------------------------

func edgeJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return b
}

func edgePostVerdict(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	watchlistHandler(rec, httptest.NewRequest(http.MethodPost, "/api/watchlist", bytes.NewReader(edgeJSON(t, body))))
	return rec
}

func edgePostItem(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	watchItemHandler(rec, httptest.NewRequest(http.MethodPost, "/api/watchlist/item", bytes.NewReader(edgeJSON(t, body))))
	return rec
}

func edgeGetWatchlist(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	watchlistHandler(rec, httptest.NewRequest(http.MethodGet, "/api/watchlist"+query, nil))
	return rec
}

// edgeErrText reads the {"error": ...} body every refusal on this surface
// serves, so a case pins the REASON and not just the status code.
func edgeErrText(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		return rec.Body.String()
	}
	return e.Error
}

// edgeSeedWatched plants one live graph edge carrying observation and watches
// it — the new backing for what used to be a flat watchlist row (S3.5).
func edgeSeedWatched(t *testing.T, st *store.Store, observation string, w store.WatchItem) int64 {
	t.Helper()
	src, err := st.UpsertNode(store.GraphNode{Name: "dream pass"})
	if err != nil {
		t.Fatalf("upsert src: %v", err)
	}
	dst, err := st.UpsertNode(store.GraphNode{Name: "kraken"})
	if err != nil {
		t.Fatalf("upsert dst: %v", err)
	}
	id, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: src, DstID: dst, Predicate: "observed", Fact: observation, Strength: 5,
	})
	if err != nil {
		t.Fatalf("insert edge: %v", err)
	}
	if err := st.WatchEdge(id, w); err != nil {
		t.Fatalf("watch edge: %v", err)
	}
	return id
}

func edgeAddItem(t *testing.T, item map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return edgePostItem(t, map[string]any{"op": "add", "item": item})
}

// edgeAddResp decodes op add's response — {ok, id, folded} per A3.1/A3.4.
type edgeAddResp struct {
	OK     bool  `json:"ok"`
	ID     int64 `json:"id"`
	Folded bool  `json:"folded"`
}

func edgeDecodeAdd(t *testing.T, rec *httptest.ResponseRecorder) edgeAddResp {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("add: status %d, body %s", rec.Code, rec.Body.String())
	}
	var r edgeAddResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode add response %s: %v", rec.Body.String(), err)
	}
	return r
}

// --- A2: carl's verdict route ---------------------------------------------

// TestEdgeWatchVerdictRefusesEveryAgentOwnedKey pins A2's expanded key list.
// The four NEW ones (src, dst, predicate, strength) are the point: they only
// exist on the add payload, and a caller that put them on the verdict route
// must be told which route owns them rather than have them silently dropped.
func TestEdgeWatchVerdictRefusesEveryAgentOwnedKey(t *testing.T) {
	st := useTestStore(t)
	id := edgeSeedWatched(t, st, "zeus re-reads the same file", store.WatchItem{Class: "waste"})

	// Spelled out rather than ranged over agentOwnedWatchKeys: iterating the
	// implementation's own list would pass vacuously if a key were left out
	// of it, which is exactly the regression A2 exists to prevent.
	keys := []string{
		"observation", "class", "state", "sightings", "first_seen", "last_seen",
		"evidence", "suggested_fix", "pr_url",
		"src", "dst", "predicate", "strength",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			rec := edgePostVerdict(t, map[string]any{"id": id, "verdict": "fine", key: "whatever"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
			// A2 (amended at the join): "unchanged" governs the message text,
			// which points at the other surface by its CLI verb rather than by
			// path — so no assertion may require the literal
			// "/api/watchlist/item". Either spelling of "the other surface"
			// satisfies the obligation.
			got := edgeErrText(t, rec)
			if !strings.Contains(got, "kraken watchlist") && !strings.Contains(got, "/api/watchlist/item") {
				t.Errorf("error = %q, want it to point the caller at the other surface", got)
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Verdict != "" || got.Observation != "zeus re-reads the same file" {
		t.Fatalf("a refused request still wrote something: %+v", got)
	}
}

// TestEdgeWatchVerdictRefusals covers A2's remaining boundaries: an
// out-of-enum verdict, a missing id, an id nobody watches, and — the new
// one — an overlay whose EDGE has been invalidated, which A2 spells out as a
// 404 ("absent overlay or dead edge").
func TestEdgeWatchVerdictRefusals(t *testing.T) {
	st := useTestStore(t)
	live := edgeSeedWatched(t, st, "artifacts are rewrapped by hand", store.WatchItem{Class: "correction"})
	dead := edgeSeedWatched(t, st, "the board reorders on every poll", store.WatchItem{Class: "waste"})
	if err := st.InvalidateEdge(dead, 1785448586573); err != nil {
		t.Fatalf("InvalidateEdge: %v", err)
	}

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"bad verdict", map[string]any{"id": live, "verdict": "maybe"}, http.StatusBadRequest},
		{"missing id", map[string]any{"verdict": "fine"}, http.StatusBadRequest},
		{"zero id", map[string]any{"id": 0, "verdict": "fine"}, http.StatusBadRequest},
		{"unknown id", map[string]any{"id": live + 9999, "verdict": "fine"}, http.StatusNotFound},
		{"invalidated edge", map[string]any{"id": dead, "verdict": "fine"}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := edgePostVerdict(t, c.body)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d, body %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// --- A3: the agent's write ------------------------------------------------

// TestEdgeWatchItemRefusesCarlOwnedKeys pins A3's "as today" clause at BOTH
// levels and on BOTH ops that carry an item. An agent that echoed a row it
// just read back from GET /api/watchlist is the likeliest offender, and add
// is as reachable a path for that as update.
func TestEdgeWatchItemRefusesCarlOwnedKeys(t *testing.T) {
	st := useTestStore(t)
	id := edgeSeedWatched(t, st, "the same lint command is rediscovered", store.WatchItem{Class: "repetition"})

	for _, key := range []string{"verdict", "comment", "commented_at"} {
		t.Run("root/"+key, func(t *testing.T) {
			rec := edgePostItem(t, map[string]any{
				"op": "update", key: "x",
				"item": map[string]any{"id": id, "class": "repetition"},
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
		t.Run("item/update/"+key, func(t *testing.T) {
			rec := edgePostItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, "class": "repetition", key: "x",
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
		t.Run("item/add/"+key, func(t *testing.T) {
			rec := edgeAddItem(t, map[string]any{
				"observation": "a brand new observation", "class": "waste",
				"src": "zeus", "dst": "kraken", "strength": 5, key: "x",
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("a refused request still wrote carl's columns: %+v", got)
	}
}

// TestEdgeWatchItemAddRefusals sweeps every 400 A3.1 enumerates. The three
// strength cases are the ones worth spelling out: an ABSENT strength is an
// unstated confidence, not a default — the same rule /api/graph/add applies —
// so absent, 0 and 11 must all be refused.
func TestEdgeWatchItemAddRefusals(t *testing.T) {
	st := useTestStore(t)

	base := func() map[string]any {
		return map[string]any{
			"observation": "zeus re-reads the same file", "class": "waste",
			"src": "zeus", "dst": "job spec", "strength": 5,
		}
	}
	without := func(keys ...string) map[string]any {
		m := base()
		for _, k := range keys {
			delete(m, k)
		}
		return m
	}
	with := func(k string, v any) map[string]any {
		m := base()
		m[k] = v
		return m
	}

	cases := []struct {
		name string
		item map[string]any
	}{
		{"missing observation", without("observation")},
		{"blank observation", with("observation", "   ")},
		{"observation empty after normalization", with("observation", " \t\n ")},
		{"missing class", without("class")},
		{"bad class", with("class", "vibes")},
		{"blank class", with("class", "")},
		{"missing src", without("src")},
		{"missing dst", without("dst")},
		{"absent strength", without("strength")},
		{"strength 0", with("strength", 0)},
		{"strength 11", with("strength", 11)},
		{"strength negative", with("strength", -1)},
		{"bad state", with("state", "maybe")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeAddItem(t, c.item)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}

	list, err := st.ListWatchItems("")
	if err != nil {
		t.Fatalf("ListWatchItems: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("a refused add still wrote %d watch item(s): %+v", len(list), list)
	}
}

// TestEdgeWatchItemUpdateRefusesImmutableFactKeys pins A3.2's new refusal:
// the fact sentence and its endpoints are the EDGE, and edges are
// insert-only (W5) — so an update that mentions one is told to remove and
// re-add rather than being served a partial write.
func TestEdgeWatchItemUpdateRefusesImmutableFactKeys(t *testing.T) {
	st := useTestStore(t)
	id := edgeSeedWatched(t, st, "zeus narrates before it plans", store.WatchItem{
		Class: "prompting", State: "watching", Sightings: 3, SuggestedFix: "original fix",
	})

	// Each value is WELL-TYPED for its key: A3.2 refuses the key's PRESENCE,
	// and it is silent on decode-gate ordering, so a string in the typed
	// `strength` field would let the decoder answer first and never exercise
	// the immutability refusal this test exists for.
	for _, kv := range []struct {
		key   string
		value any
	}{
		{"observation", "whatever"},
		{"src", "whatever"},
		{"dst", "whatever"},
		{"predicate", "whatever"},
		{"strength", 5},
	} {
		key, value := kv.key, kv.value
		t.Run(key, func(t *testing.T) {
			rec := edgePostItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, "suggested_fix": "rewritten", key: value,
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
			if got := edgeErrText(t, rec); !strings.Contains(got, "remove and re-add") {
				t.Errorf("error = %q, want it to say remove and re-add to reword", got)
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Observation != "zeus narrates before it plans" || got.SuggestedFix != "original fix" {
		t.Fatalf("a refused update still wrote: %+v", got)
	}
}

// TestEdgeWatchItemUpdateIDBoundaries pins A3.2's id handling: absent is a
// 400, unknown is a 404, and an edge that exists but is unwatched is a 404
// too — the overlay row is what the verb addresses.
func TestEdgeWatchItemUpdateIDBoundaries(t *testing.T) {
	st := useTestStore(t)
	src, err := st.UpsertNode(store.GraphNode{Name: "zeus"})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := st.UpsertNode(store.GraphNode{Name: "kraken"})
	if err != nil {
		t.Fatal(err)
	}
	unwatched, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: src, DstID: dst, Predicate: "observed", Fact: "a fact nobody watches", Strength: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	// An overlay whose EDGE has been invalidated: A2 defines an unknown watch
	// item as "absent overlay OR dead edge", and A3.2's 404 uses that same
	// definition (settled in review round 1). Every read already hides this
	// row, so a 200 here would tell the agent a write landed somewhere it can
	// never be read back from.
	dead := edgeSeedWatched(t, st, "a fact the graph has since unlearned", store.WatchItem{Class: "waste"})
	if err := st.InvalidateEdge(dead, 1785448586573); err != nil {
		t.Fatalf("InvalidateEdge: %v", err)
	}

	cases := []struct {
		name string
		item map[string]any
		want int
	}{
		{"missing id", map[string]any{"class": "waste"}, http.StatusBadRequest},
		{"zero id", map[string]any{"id": 0, "class": "waste"}, http.StatusBadRequest},
		{"unknown id", map[string]any{"id": 424242, "class": "waste"}, http.StatusNotFound},
		{"live but unwatched edge", map[string]any{"id": unwatched, "class": "waste"}, http.StatusNotFound},
		{"invalidated edge", map[string]any{"id": dead, "class": "waste"}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := edgePostItem(t, map[string]any{"op": "update", "item": c.item})
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d, body %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestEdgeWatchItemRemoveUnwatchesAndKeepsTheFact pins A3.3 end to end: the
// 200 body says what actually happened (the overlay went, the fact stayed),
// a repeat is a 404, and a missing id is a 400.
func TestEdgeWatchItemRemoveUnwatchesAndKeepsTheFact(t *testing.T) {
	st := useTestStore(t)
	const fact = "the daemon logs the same tick twice"
	id := edgeSeedWatched(t, st, fact, store.WatchItem{Class: "waste"})

	rec := edgePostItem(t, map[string]any{"op": "remove", "id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if resp["ok"] != true || resp["unwatched"] != true {
		t.Fatalf("remove response = %v, want {\"ok\":true,\"unwatched\":true} (A3.3)", resp)
	}
	if _, err := st.GetWatchItem(id); err == nil {
		t.Fatal("the overlay row survived a remove")
	}
	// The fact itself is still a live graph edge — that is the whole reason
	// the response says "unwatched" rather than "deleted".
	e, err := st.FindLiveEdgeByFact(fact)
	if err != nil {
		t.Fatalf("FindLiveEdgeByFact: %v", err)
	}
	if e == nil || e.ID != id {
		t.Fatalf("remove took the fact with it: %+v", e)
	}

	if rec := edgePostItem(t, map[string]any{"op": "remove", "id": id}); rec.Code != http.StatusNotFound {
		t.Fatalf("repeat remove: status %d, want 404", rec.Code)
	}
	if rec := edgePostItem(t, map[string]any{"op": "remove"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("remove with no id: status %d, want 400", rec.Code)
	}
	if rec := edgePostItem(t, map[string]any{"op": "remove", "id": 999999}); rec.Code != http.StatusNotFound {
		t.Fatalf("remove of an unknown id: status %d, want 404", rec.Code)
	}
}

// TestEdgeWatchItemAddFoldIgnoresEndpointFields pins A3.1 step 2's last
// sentence: on a fold the observation already HAS endpoints, so src / dst /
// predicate / strength are ignored — they must not rewrite the edge (W5
// forbids it) and must not mint stray nodes. The fields the fold DOES apply
// (class/state/suggested_fix/pr_url) still land.
func TestEdgeWatchItemAddFoldIgnoresEndpointFields(t *testing.T) {
	st := useTestStore(t)

	first := edgeDecodeAdd(t, edgeAddItem(t, map[string]any{
		"observation": "zeus re-reads the same file", "class": "waste",
		"src": "zeus", "dst": "job spec", "strength": 7,
	}))
	if first.Folded {
		t.Fatalf("the first add reported folded=true: %+v", first)
	}

	second := edgeDecodeAdd(t, edgeAddItem(t, map[string]any{
		// Same observation modulo case and internal whitespace.
		"observation": "  ZEUS   re-reads\tthe same\nfile ", "class": "repetition",
		"src": "carl", "dst": "the moon", "predicate": "detests", "strength": 2,
		"suggested_fix": "cache it",
	}))
	if !second.Folded || second.ID != first.ID {
		t.Fatalf("second add = %+v, want a fold onto edge %d", second, first.ID)
	}

	edges, err := st.GetEdges([]int64{first.ID})
	if err != nil || len(edges) != 1 {
		t.Fatalf("GetEdges: %+v %v", edges, err)
	}
	if edges[0].Predicate != "concerns" {
		t.Errorf("predicate = %q, want the first add's default \"concerns\" — a fold must not reword the edge",
			edges[0].Predicate)
	}
	if edges[0].Strength != 7 {
		t.Errorf("strength = %d, want 7 — a fold ignores the incoming strength", edges[0].Strength)
	}
	for _, name := range []string{"carl", "the moon"} {
		n, err := st.ResolveNode(name)
		if err != nil {
			t.Fatal(err)
		}
		if n != nil {
			t.Errorf("the fold created node %q; A3.1 step 2 ignores src/dst entirely", name)
		}
	}

	got, err := st.GetWatchItem(first.ID)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Sightings != 2 {
		t.Errorf("sightings = %d, want 2 — a fold bumps the sighting", got.Sightings)
	}
	if got.SuggestedFix != "cache it" || got.Class != "repetition" {
		t.Errorf("the fold dropped the fields it DOES apply: %+v", got)
	}
}

// --- A1: the frozen GET shape ---------------------------------------------

// TestEdgeWatchlistGetStateFilterBoundaries pins A1's "as today" clauses at
// their edges: an unrecognized state is a 400 (a typo'd filter and an empty
// watchlist are different answers), an empty state means all, and an empty
// result serves [] rather than null.
func TestEdgeWatchlistGetStateFilterBoundaries(t *testing.T) {
	st := useTestStore(t)

	rec := edgeGetWatchlist(t, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"watchlist":[]`) {
		t.Fatalf("empty body = %s, want an empty ARRAY, never null", rec.Body.String())
	}

	edgeSeedWatched(t, st, "one", store.WatchItem{Class: "waste", State: "watching", LastSeen: 10})
	edgeSeedWatched(t, st, "two", store.WatchItem{Class: "waste", State: "shipped", LastSeen: 20})

	if got := len(edgeWatchlistItems(t, edgeGetWatchlist(t, ""))); got != 2 {
		t.Errorf("unfiltered list = %d items, want 2 (an empty state means all)", got)
	}
	if got := len(edgeWatchlistItems(t, edgeGetWatchlist(t, "?state="))); got != 2 {
		t.Errorf("?state= list = %d items, want 2 — a blank filter is not a refusal", got)
	}
	if got := len(edgeWatchlistItems(t, edgeGetWatchlist(t, "?state=declined"))); got != 0 {
		t.Errorf("?state=declined = %d items, want 0 — a valid-but-unmatched state is empty, not an error", got)
	}
	// A2 (amended at the join), per "as today": the state query param is
	// TrimSpace'd BEFORE the enum check, so a value that trims to a valid
	// state is accepted and filters normally — it is not a refusal. (The
	// store layer is stricter; S3.1 governs there, pinned separately in
	// TestEdgeListWatchItemsStateFilterBoundaries.)
	trimmed := edgeWatchlistItems(t, edgeGetWatchlist(t, "?state=watching%20"))
	if len(trimmed) != 1 || trimmed[0]["observation"] != "one" {
		t.Errorf("?state=watching%%20 = %+v, want the single watching item — the param is TrimSpace'd", trimmed)
	}
	for _, bad := range []string{"pending", "Watching", "all"} {
		if rec := edgeGetWatchlist(t, "?state="+bad); rec.Code != http.StatusBadRequest {
			t.Errorf("?state=%s: status %d, want 400", bad, rec.Code)
		}
	}
}

func edgeWatchlistItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Watchlist []map[string]any `json:"watchlist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return resp.Watchlist
}

// TestEdgeWatchlistGetGrowsNoKeys is A1's hard boundary, decoded as a raw map
// so a NEW key cannot hide behind a typed struct: the graph gave every item
// src/dst/predicate, and putting any of them on this wire would be an
// apiContract bump this job does not make (W3). The omitempty keys must also
// stay ABSENT when empty rather than serialising as "".
func TestEdgeWatchlistGetGrowsNoKeys(t *testing.T) {
	st := useTestStore(t)
	edgeSeedWatched(t, st, "zeus re-reads the same file", store.WatchItem{
		Class: "waste", State: "watching", Sightings: 2, FirstSeen: 10, LastSeen: 20, UpdatedAt: 30,
	})

	items := edgeWatchlistItems(t, edgeGetWatchlist(t, ""))
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1", items)
	}
	item := items[0]

	always := []string{"id", "observation", "class", "state", "sightings", "first_seen", "last_seen", "updated_at"}
	for _, k := range always {
		if _, ok := item[k]; !ok {
			t.Errorf("key %q missing from the item; A1 freezes it to today's shape", k)
		}
	}
	// Empty on this fixture, and all omitempty per A1.
	for _, k := range []string{"evidence", "suggested_fix", "pr_url", "verdict", "comment", "commented_at"} {
		if v, ok := item[k]; ok {
			t.Errorf("key %q present with %v; A1 marks it omitempty", k, v)
		}
	}
	// The refusal this test exists for.
	for _, k := range []string{"src", "dst", "predicate", "edge_id", "strength", "occurrences", "episode_id"} {
		if v, ok := item[k]; ok {
			t.Errorf("item grew key %q = %v — A1 admits NO new keys on this wire", k, v)
		}
	}
	allowed := map[string]bool{}
	for _, k := range append(always, "evidence", "suggested_fix", "pr_url", "verdict", "comment", "commented_at") {
		allowed[k] = true
	}
	var extra []string
	for k := range item {
		if !allowed[k] {
			extra = append(extra, k)
		}
	}
	sort.Strings(extra)
	if len(extra) != 0 {
		t.Errorf("unexpected keys %v on the frozen watchlist item shape", extra)
	}
}

// --- decode + method discipline, preserved "as today" ---------------------

// TestEdgeWatchlistDecodeAndMethodRefusals pins the guards that must survive
// the reshaping: bad JSON is a 400 on both routes, an unknown op is a 400, a
// missing item is a 400, and the reads/writes stay on their own verbs.
func TestEdgeWatchlistDecodeAndMethodRefusals(t *testing.T) {
	useTestStore(t)

	for _, path := range []string{"/api/watchlist", "/api/watchlist/item"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte("not json")))
		if path == "/api/watchlist" {
			watchlistHandler(rec, req)
		} else {
			watchItemHandler(rec, req)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s with a malformed body: status %d, want 400", path, rec.Code)
		}
	}

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"unknown op", map[string]any{"op": "bump", "id": 1}},
		{"missing op", map[string]any{"id": 1}},
		{"add with no item", map[string]any{"op": "add"}},
		{"update with no item", map[string]any{"op": "update"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if rec := edgePostItem(t, c.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}

	rec := httptest.NewRecorder()
	watchlistHandler(rec, httptest.NewRequest(http.MethodDelete, "/api/watchlist", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE /api/watchlist: status %d, want 405", rec.Code)
	}
	rec = httptest.NewRecorder()
	watchItemHandler(rec, httptest.NewRequest(http.MethodGet, "/api/watchlist/item", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/watchlist/item: status %d, want 405 (reads go through /api/watchlist)", rec.Code)
	}
}
