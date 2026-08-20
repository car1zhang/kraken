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

// watch_edge_test.go — the BOUNDARY dimension of the watchlist routes: what
// the two of them REFUSE, and what the frozen wire shape must not grow.
// Invariants and happy paths belong to watchlist_test.go; nothing here
// duplicates them.
//
// The file keeps its name and its `edge`-prefixed identifiers from the round
// that moved a watch item INTO the knowledge graph. Q9 moved it back out — a
// watch item is a `watch_items` row again — so the edge-specific assertions
// are gone and what survives is the part that was never about edges: the
// ownership split, the refusal set, and the wire keys, all of which held
// across both moves and are the reason the shape never needed an apiContract
// bump in either direction.

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

// edgeSeedWatched plants one live watch row carrying observation.
func edgeSeedWatched(t *testing.T, st *store.Store, observation string, w store.WatchItem) int64 {
	t.Helper()
	w.Observation = observation
	if w.Src == "" {
		w.Src = "dream pass"
	}
	if w.Dst == "" {
		w.Dst = "kraken"
	}
	if w.Predicate == "" {
		w.Predicate = "observed"
	}
	id, _, err := st.AddWatchItem(w)
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
	}
	return id
}

func edgeAddItem(t *testing.T, item map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return edgePostItem(t, map[string]any{"op": "add", "item": item})
}

// edgeAddResp decodes op add's response — {ok, id, folded}.
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

// --- carl's verdict route --------------------------------------------------

// TestEdgeWatchVerdictRefusesEveryAgentOwnedKey pins the whole agent-owned key
// list. src/dst/predicate are the interesting ones: they exist only on the add
// payload, so a caller that put them on the verdict route must be told which
// route owns them rather than have them silently dropped.
//
// `strength` is deliberately NOT on this list any more: it was the edge's
// confidence, and the standalone table has no such column, so refusing it
// would be refusing a word that means nothing anywhere.
func TestEdgeWatchVerdictRefusesEveryAgentOwnedKey(t *testing.T) {
	st := useTestStore(t)
	id := edgeSeedWatched(t, st, "zeus re-reads the same file", store.WatchItem{Class: "waste"})

	// Spelled out rather than ranged over agentOwnedWatchKeys: iterating the
	// implementation's own list would pass vacuously if a key were left out
	// of it, which is exactly the regression this test exists to prevent.
	keys := []string{
		"observation", "class", "state", "sightings", "first_seen", "last_seen",
		"evidence", "suggested_fix", "pr_url",
		"src", "dst", "predicate",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			rec := edgePostVerdict(t, map[string]any{"id": id, "verdict": "fine", key: "whatever"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
			// The message points at the other surface by its CLI verb rather
			// than by path, so either spelling satisfies the obligation.
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

// TestEdgeWatchVerdictRefusals covers the remaining boundaries: an out-of-enum
// verdict, a missing id, an id naming no row, and a row that has been CLOSED —
// which is a 404 for the same reason a dead edge used to be, and it is the
// same rule wearing new clothes: every read hides the row, so judging it would
// write somewhere no reader can see.
func TestEdgeWatchVerdictRefusals(t *testing.T) {
	st := useTestStore(t)
	live := edgeSeedWatched(t, st, "artifacts are rewrapped by hand", store.WatchItem{Class: "correction"})
	dead := edgeSeedWatched(t, st, "the board reorders on every poll", store.WatchItem{Class: "waste"})
	if err := st.ExpireWatchItem(dead, 1785448586573); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
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
		{"expired item", map[string]any{"id": dead, "verdict": "fine"}, http.StatusNotFound},
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

// --- the agent's write ------------------------------------------------------

// TestEdgeWatchItemRefusesCarlOwnedKeys pins the carl-owned refusal at BOTH
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
				"src": "zeus", "dst": "kraken", key: "x",
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

// TestEdgeWatchItemAddRefusals sweeps every 400 an add can earn. The src/dst
// and strength cases are GONE rather than moved: endpoints were required only
// because a new fact could not be inserted without two of them, and strength
// was the edge's own column — a `watch_items` row needs neither, so refusing
// an add over either would enforce a constraint nothing below this line has.
func TestEdgeWatchItemAddRefusals(t *testing.T) {
	st := useTestStore(t)

	base := func() map[string]any {
		return map[string]any{
			"observation": "zeus re-reads the same file", "class": "waste",
			"src": "zeus", "dst": "job spec",
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

// TestEdgeWatchItemUpdateRefusesImmutableFactKeys: the observation and its
// endpoints are the row's IDENTITY — the fold matches on the sentence — so an
// update that mentions one is told to remove and re-add rather than being
// served a partial write that would silently split one tracked pattern in two.
func TestEdgeWatchItemUpdateRefusesImmutableFactKeys(t *testing.T) {
	st := useTestStore(t)
	id := edgeSeedWatched(t, st, "zeus narrates before it plans", store.WatchItem{
		Class: "prompting", State: "watching", SuggestedFix: "original fix",
	})

	for _, key := range []string{"observation", "src", "dst", "predicate"} {
		t.Run(key, func(t *testing.T) {
			rec := edgePostItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, "suggested_fix": "rewritten", key: "whatever",
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

// TestEdgeWatchItemUpdateIDBoundaries pins update's id handling: absent is a
// 400, unknown is a 404, and a CLOSED row is a 404 too — it is still readable
// by id, so without the liveness probe the store's UPDATE would match it and
// answer 200 into something no reader can see.
func TestEdgeWatchItemUpdateIDBoundaries(t *testing.T) {
	st := useTestStore(t)
	dead := edgeSeedWatched(t, st, "an observation nobody tracks any more", store.WatchItem{Class: "waste"})
	if err := st.ExpireWatchItem(dead, 1785448586573); err != nil {
		t.Fatalf("ExpireWatchItem: %v", err)
	}

	cases := []struct {
		name string
		item map[string]any
		want int
	}{
		{"missing id", map[string]any{"class": "waste"}, http.StatusBadRequest},
		{"zero id", map[string]any{"id": 0, "class": "waste"}, http.StatusBadRequest},
		{"unknown id", map[string]any{"id": 424242, "class": "waste"}, http.StatusNotFound},
		{"expired item", map[string]any{"id": dead, "class": "waste"}, http.StatusNotFound},
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

// TestEdgeWatchItemRemoveClosesTheRow pins remove end to end: the 200 body
// says what actually happened, the row leaves every list but survives by id,
// a repeat is a SUCCESS (closing a closed row is idempotent), a missing id is
// a 400 and an id naming nothing is a 404.
func TestEdgeWatchItemRemoveClosesTheRow(t *testing.T) {
	st := useTestStore(t)
	id := edgeSeedWatched(t, st, "the daemon logs the same tick twice", store.WatchItem{Class: "waste"})

	rec := edgePostItem(t, map[string]any{"op": "remove", "id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if resp["ok"] != true || resp["unwatched"] != true {
		t.Fatalf("remove response = %v, want {\"ok\":true,\"unwatched\":true}", resp)
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem after remove: %v — no watch row is ever deleted", err)
	}
	if got.ExpiredAt == 0 {
		t.Fatalf("row = %+v, want expired_at stamped", got)
	}
	if items := edgeWatchlistItems(t, edgeGetWatchlist(t, "")); len(items) != 0 {
		t.Fatalf("list = %+v, want the closed row gone from every read", items)
	}

	if rec := edgePostItem(t, map[string]any{"op": "remove", "id": id}); rec.Code != http.StatusOK {
		t.Fatalf("repeat remove: status %d, want 200 (closing a closed row is idempotent)", rec.Code)
	}
	if rec := edgePostItem(t, map[string]any{"op": "remove"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("remove with no id: status %d, want 400", rec.Code)
	}
	if rec := edgePostItem(t, map[string]any{"op": "remove", "id": 999999}); rec.Code != http.StatusNotFound {
		t.Fatalf("remove of an unknown id: status %d, want 404", rec.Code)
	}
}

// TestEdgeWatchItemAddFoldIgnoresEndpointFields: on a fold the observation is
// already recorded with its endpoints, so src/dst/predicate on the second add
// are ignored — they must not rewrite the row's identity. The fields the fold
// DOES apply (class/state/suggested_fix/pr_url) still land, and the sighting
// count still goes up, which is the whole reason a fold exists.
func TestEdgeWatchItemAddFoldIgnoresEndpointFields(t *testing.T) {
	st := useTestStore(t)

	first := edgeDecodeAdd(t, edgeAddItem(t, map[string]any{
		"observation": "zeus re-reads the same file", "class": "waste",
		"src": "zeus", "dst": "job spec",
	}))
	if first.Folded {
		t.Fatalf("the first add reported folded=true: %+v", first)
	}

	second := edgeDecodeAdd(t, edgeAddItem(t, map[string]any{
		// Same observation modulo case and internal whitespace.
		"observation": "  zeus   re-reads\tthe same\nfile ", "class": "repetition",
		"src": "carl", "dst": "the moon", "predicate": "detests",
		"suggested_fix": "cache it",
	}))
	if !second.Folded || second.ID != first.ID {
		t.Fatalf("second add = %+v, want a fold onto %d", second, first.ID)
	}

	got, err := st.GetWatchItem(first.ID)
	if err != nil {
		t.Fatalf("GetWatchItem: %v", err)
	}
	if got.Src != "zeus" || got.Dst != "job spec" || got.Predicate != "concerns" {
		t.Errorf("the fold rewrote the row's endpoints: src %q dst %q predicate %q",
			got.Src, got.Dst, got.Predicate)
	}
	if got.Sightings != 2 {
		t.Errorf("sightings = %d, want 2 — a fold bumps the sighting", got.Sightings)
	}
	if got.SuggestedFix != "cache it" || got.Class != "repetition" {
		t.Errorf("the fold dropped the fields it DOES apply: %+v", got)
	}
}

// --- the frozen GET shape ---------------------------------------------------

// TestEdgeWatchlistGetStateFilterBoundaries pins the state filter at its
// edges: an unrecognized state is a 400 (a typo'd filter and an empty
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

	edgeSeedWatched(t, st, "one", store.WatchItem{Class: "waste", State: "watching"})
	edgeSeedWatched(t, st, "two", store.WatchItem{Class: "waste", State: "shipped"})

	if got := len(edgeWatchlistItems(t, edgeGetWatchlist(t, ""))); got != 2 {
		t.Errorf("unfiltered list = %d items, want 2 (an empty state means all)", got)
	}
	if got := len(edgeWatchlistItems(t, edgeGetWatchlist(t, "?state="))); got != 2 {
		t.Errorf("?state= list = %d items, want 2 — a blank filter is not a refusal", got)
	}
	if got := len(edgeWatchlistItems(t, edgeGetWatchlist(t, "?state=declined"))); got != 0 {
		t.Errorf("?state=declined = %d items, want 0 — a valid-but-unmatched state is empty, not an error", got)
	}
	// The state query param is TrimSpace'd BEFORE the enum check, so a value
	// that trims to a valid state is accepted and filters normally — it is not
	// a refusal. (The store layer is stricter; that is pinned on its own side.)
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

// TestEdgeWatchlistGetGrowsNoKeys is the hard boundary, decoded as a raw map so
// a NEW key cannot hide behind a typed struct. The row has now moved twice and
// carries src/dst/predicate in the store both times; putting any of them on
// this wire would be an apiContract bump neither move makes. The omitempty
// keys must also stay ABSENT when empty rather than serialising as "".
func TestEdgeWatchlistGetGrowsNoKeys(t *testing.T) {
	st := useTestStore(t)
	edgeSeedWatched(t, st, "zeus re-reads the same file", store.WatchItem{
		Class: "waste", State: "watching",
	})

	items := edgeWatchlistItems(t, edgeGetWatchlist(t, ""))
	if len(items) != 1 {
		t.Fatalf("items = %+v, want 1", items)
	}
	item := items[0]

	always := []string{"id", "observation", "class", "state", "sightings", "first_seen", "last_seen", "updated_at"}
	for _, k := range always {
		if _, ok := item[k]; !ok {
			t.Errorf("key %q missing from the item; the shape is frozen", k)
		}
	}
	// Empty on this fixture, and all omitempty.
	for _, k := range []string{"evidence", "suggested_fix", "pr_url", "verdict", "comment", "commented_at"} {
		if v, ok := item[k]; ok {
			t.Errorf("key %q present with %v; it is omitempty", k, v)
		}
	}
	// The refusal this test exists for.
	for _, k := range []string{"src", "dst", "predicate", "edge_id", "strength", "occurrences", "episode_id", "expired_at"} {
		if v, ok := item[k]; ok {
			t.Errorf("item grew key %q = %v — this wire admits NO new keys", k, v)
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

// --- decode + method discipline --------------------------------------------

// TestEdgeWatchlistDecodeAndMethodRefusals pins the guards that must survive
// every reshaping: bad JSON is a 400 on both routes, an unknown op is a 400, a
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
