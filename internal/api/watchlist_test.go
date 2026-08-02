package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// watchlist_test.go — coverage for watchlist.go's two POST surfaces and the
// one GET, with the ownership split (carl's verdict vs the agent's
// observation) as the thing most of these tests are actually pinning: the
// store enforces it column by column, and these assert the HTTP layer refuses
// a wrong-side write loudly instead of silently dropping it.
//
// A watch item is now a LIVE GRAPH EDGE plus a kg_watch overlay, so every id
// here is an edge id and seeding one means writing a real edge. The wire shape
// is unchanged, which is what these tests keep asserting.

func postWatchVerdict(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/watchlist", bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	watchlistHandler(rec, req)
	return rec
}

func postWatchItem(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/watchlist/item", bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	watchItemHandler(rec, req)
	return rec
}

func getWatchlist(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/watchlist"+query, nil)
	rec := httptest.NewRecorder()
	watchlistHandler(rec, req)
	return rec
}

// seedWatchEdge writes one live edge stating `observation` and returns its id.
// The endpoints are the same pair the migration's fallback uses, so a test
// that seeds several observations gets several edges between two stable nodes
// rather than a node explosion.
func seedWatchEdge(t *testing.T, st *store.Store, observation string) int64 {
	t.Helper()
	src, err := st.UpsertNode(store.GraphNode{Name: "dream pass"})
	if err != nil {
		t.Fatalf("UpsertNode src: %v", err)
	}
	dst, err := st.UpsertNode(store.GraphNode{Name: "kraken"})
	if err != nil {
		t.Fatalf("UpsertNode dst: %v", err)
	}
	id, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: src, DstID: dst, Predicate: "concerns", Fact: observation,
		Strength: 5, ValidFrom: 1,
	})
	if err != nil {
		t.Fatalf("InsertEdge: %v", err)
	}
	return id
}

// seedWatchItem writes the edge AND its overlay, returning the edge id — the
// id every route in this file takes.
func seedWatchItem(t *testing.T, st *store.Store, observation string, w store.WatchItem) int64 {
	t.Helper()
	id := seedWatchEdge(t, st, observation)
	if err := st.WatchEdge(id, w); err != nil {
		t.Fatalf("WatchEdge: %v", err)
	}
	return id
}

// --- GET ---

// TestWatchlistGetNeverNull pins B3's convention on this route: an empty
// watchlist must serve "[]", not "null" — a JSON null decodes to a nil array
// on every client and reads as a broken response rather than an empty one.
func TestWatchlistGetNeverNull(t *testing.T) {
	useTestStore(t)

	rec := getWatchlist(t, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !bytes.Contains([]byte(got), []byte(`"watchlist":[]`)) {
		t.Fatalf("body = %s, want an empty array", got)
	}
}

func TestWatchlistGetFiltersByState(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	seedWatchItem(t, st, "a", store.WatchItem{Class: "waste", State: "watching"})
	seedWatchItem(t, st, "b", store.WatchItem{Class: "waste", State: "shipped"})

	rec := getWatchlist(t, "?state=shipped")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Watchlist []webWatchItem `json:"watchlist"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Watchlist) != 1 || resp.Watchlist[0].Observation != "b" {
		t.Fatalf("filtered list = %+v, want only the shipped item", resp.Watchlist)
	}

	all := getWatchlist(t, "")
	var allResp struct {
		Watchlist []webWatchItem `json:"watchlist"`
	}
	if err := json.Unmarshal(all.Body.Bytes(), &allResp); err != nil {
		t.Fatal(err)
	}
	if len(allResp.Watchlist) != 2 {
		t.Fatalf("unfiltered list = %+v, want both items (empty state means all)", allResp.Watchlist)
	}
}

// TestWatchlistGetOmitsInvalidatedEdges pins the coupling the move buys: the
// overlay rides the edge, so the graph's revision pass striking a fact through
// is also the DREAM view dropping it — with no second bookkeeping step that
// could be forgotten.
func TestWatchlistGetOmitsInvalidatedEdges(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a fact that stops being true", store.WatchItem{Class: "waste"})

	if err := st.InvalidateEdge(id, 5); err != nil {
		t.Fatal(err)
	}
	rec := getWatchlist(t, "")
	if got := rec.Body.String(); !bytes.Contains([]byte(got), []byte(`"watchlist":[]`)) {
		t.Fatalf("body = %s, want the invalidated item gone", got)
	}
}

// TestWatchlistGetBadStateIs400 pins the "a typo'd filter and an empty
// watchlist are different answers" rule: an unrecognized state must not
// silently serve [].
func TestWatchlistGetBadStateIs400(t *testing.T) {
	useTestStore(t)
	rec := getWatchlist(t, "?state=pending")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestWatchlistMethodNotAllowed(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/watchlist", nil)
	rec := httptest.NewRecorder()
	watchlistHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/watchlist/item", nil)
	rec = httptest.NewRecorder()
	watchItemHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("item GET: status %d, want 405 (reads go through /api/watchlist)", rec.Code)
	}
}

// --- carl's verdict write ---

func TestWatchVerdictStampsCommentedAt(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{Class: "correction"})

	rec := postWatchVerdict(t, map[string]any{"id": id, "verdict": "fix", "comment": "  yes, do this  "})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "fix" || got.Comment != "yes, do this" {
		t.Fatalf("verdict/comment = %q/%q", got.Verdict, got.Comment)
	}
	if _, err := time.Parse(time.RFC3339, got.CommentedAt); err != nil {
		t.Fatalf("commented_at = %q, want server-stamped RFC3339: %v", got.CommentedAt, err)
	}
}

// TestWatchVerdictEmptyClears pins store.SetWatchVerdict's documented undo
// reaching the wire: an empty verdict AND comment reopens the item, wiping
// commented_at with it rather than leaving a timestamp for a judgement that
// no longer exists.
func TestWatchVerdictEmptyClears(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{Class: "waste"})
	if rec := postWatchVerdict(t, map[string]any{"id": id, "verdict": "fine"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}

	if rec := postWatchVerdict(t, map[string]any{"id": id, "verdict": ""}); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("item not cleared: %+v", got)
	}
}

func TestWatchVerdictValidation(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{Class: "waste"})

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"bad verdict", map[string]any{"id": id, "verdict": "maybe"}, http.StatusBadRequest},
		{"missing id", map[string]any{"verdict": "fine"}, http.StatusBadRequest},
		{"unknown id", map[string]any{"id": id + 999, "verdict": "fine"}, http.StatusNotFound},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postWatchVerdict(t, c.body)
			if rec.Code != c.want {
				t.Fatalf("status %d, want %d, body %s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
}

// TestWatchVerdictRefusesAgentOwnedKeys pins the split from carl's side: the
// verdict route models none of the agent-owned columns — nor any of the edge's
// own fields, which joined the list when `add` gained src/dst/predicate/
// strength — and a request that mentions one is refused rather than having it
// quietly dropped. A caller that believed it edited the observation would
// never find out otherwise.
func TestWatchVerdictRefusesAgentOwnedKeys(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "original", store.WatchItem{Class: "waste"})

	for _, key := range agentOwnedWatchKeys {
		t.Run(key, func(t *testing.T) {
			rec := postWatchVerdict(t, map[string]any{"id": id, "verdict": "fine", key: "whatever"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400, body %s", key, rec.Code, rec.Body.String())
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation != "original" || got.Verdict != "" {
		t.Fatalf("a refused request still wrote something: %+v", got)
	}
}

// --- the agent's observation write ---

// watchAddItem is the minimal well-formed add payload: an observation, its
// class, the endpoints the new fact runs between and the extractor's
// confidence in it.
func watchAddItem(observation string, extra map[string]any) map[string]any {
	item := map[string]any{
		"observation": observation, "class": "waste",
		"src": "dream pass", "dst": "kraken", "strength": 5,
	}
	for k, v := range extra {
		item[k] = v
	}
	return item
}

func TestWatchItemAddReturnsID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()

	rec := postWatchItem(t, map[string]any{"op": "add", "item": watchAddItem(
		"zeus keeps re-reading the same file",
		map[string]any{"evidence": `{"n":3}`, "suggested_fix": "cache it"},
	)})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool  `json:"ok"`
		ID     int64 `json:"id"`
		Folded bool  `json:"folded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ID == 0 || resp.Folded {
		t.Fatalf("resp = %+v, want ok plus the new edge id, unfolded", resp)
	}
	got, err := st.GetWatchItem(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation != "zeus keeps re-reading the same file" || got.Class != "waste" {
		t.Fatalf("item = %+v", got)
	}
	if got.State != "watching" || got.Sightings != 1 {
		t.Fatalf("defaults not applied: state %q sightings %d", got.State, got.Sightings)
	}
	// The id is an EDGE id, and the edge is a real, searchable fact — the whole
	// reason for the move.
	edge, err := st.FindLiveEdgeByFact("zeus keeps re-reading the same file")
	if err != nil {
		t.Fatal(err)
	}
	if edge == nil || edge.ID != resp.ID {
		t.Fatalf("edge = %+v, want the live fact this add minted", edge)
	}
}

// TestWatchItemAddFolds pins the behaviour that replaced the skill's old
// find-then-bump dance: re-stating an observation the graph already carries
// (differing only in case and wrapping) increments the sighting count on the
// EXISTING item rather than minting a near-duplicate fact.
func TestWatchItemAddFolds(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()

	first := postWatchItem(t, map[string]any{"op": "add", "item": watchAddItem("hermes agents re-read the contract", nil)})
	if first.Code != http.StatusOK {
		t.Fatalf("first add: status %d, body %s", first.Code, first.Body.String())
	}
	var one struct {
		ID     int64 `json:"id"`
		Folded bool  `json:"folded"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &one); err != nil {
		t.Fatal(err)
	}

	second := postWatchItem(t, map[string]any{"op": "add", "item": watchAddItem(
		"Hermes agents\n  re-read the   contract",
		map[string]any{"suggested_fix": "cache the contract"},
	)})
	if second.Code != http.StatusOK {
		t.Fatalf("second add: status %d, body %s", second.Code, second.Body.String())
	}
	var two struct {
		ID     int64 `json:"id"`
		Folded bool  `json:"folded"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &two); err != nil {
		t.Fatal(err)
	}
	if two.ID != one.ID || !two.Folded {
		t.Fatalf("second add = %+v, want a fold onto %d", two, one.ID)
	}
	got, err := st.GetWatchItem(one.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sightings != 2 {
		t.Fatalf("sightings = %d, want the fold to have bumped it", got.Sightings)
	}
	if got.SuggestedFix != "cache the contract" {
		t.Fatalf("suggested_fix = %q, want the fold to have applied it", got.SuggestedFix)
	}
}

// TestWatchItemAddPromotesExistingFact: a fact some transcript already
// contributed is ADOPTED, not duplicated — the dream loop noticing it is a
// reason to watch it, never a reason to write the same sentence twice.
func TestWatchItemAddPromotesExistingFact(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	edgeID := seedWatchEdge(t, st, "the nightly pass re-reads yesterday's transcripts")

	rec := postWatchItem(t, map[string]any{"op": "add", "item": watchAddItem(
		"the nightly pass re-reads yesterday's transcripts", nil,
	)})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		ID     int64 `json:"id"`
		Folded bool  `json:"folded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.ID != edgeID || resp.Folded {
		t.Fatalf("resp = %+v, want the existing edge %d promoted (not folded, not duplicated)", resp, edgeID)
	}
}

func TestWatchItemAddValidation(t *testing.T) {
	useTestStore(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty observation", map[string]any{"op": "add", "item": watchAddItem("   ", nil)}},
		{"bad class", map[string]any{"op": "add", "item": watchAddItem("x", map[string]any{"class": "vibes"})}},
		{"missing class", map[string]any{"op": "add", "item": map[string]any{
			"observation": "x", "src": "dream pass", "dst": "kraken", "strength": 5,
		}}},
		{"bad state", map[string]any{"op": "add", "item": watchAddItem("x", map[string]any{"state": "maybe"})}},
		{"missing src", map[string]any{"op": "add", "item": map[string]any{
			"observation": "x", "class": "waste", "dst": "kraken", "strength": 5,
		}}},
		{"missing dst", map[string]any{"op": "add", "item": map[string]any{
			"observation": "x", "class": "waste", "src": "dream pass", "strength": 5,
		}}},
		{"missing strength", map[string]any{"op": "add", "item": map[string]any{
			"observation": "x", "class": "waste", "src": "dream pass", "dst": "kraken",
		}}},
		{"strength out of range", map[string]any{"op": "add", "item": watchAddItem("x", map[string]any{"strength": 11})}},
		{"missing item", map[string]any{"op": "add"}},
		{"unknown op", map[string]any{"op": "bump", "id": 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := postWatchItem(t, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestWatchItemUpdatePreservesHistory pins what the store's partial update
// buys the handler: a request that restates only suggested_fix must not zero
// first_seen/last_seen/sightings — the two facts the overlay exists to record.
// The old GET-then-backfill dance is gone; the COALESCE(NULLIF(...)) columns
// do it in one statement.
func TestWatchItemUpdatePreservesHistory(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{
		Class: "repetition", State: "watching",
		Sightings: 7, FirstSeen: 1000, LastSeen: 2000,
	})

	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": id, "suggested_fix": "do X",
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sightings != 7 || got.FirstSeen != 1000 || got.LastSeen != 2000 {
		t.Fatalf("history clobbered by a partial update: sightings %d first %d last %d", got.Sightings, got.FirstSeen, got.LastSeen)
	}
	if got.State != "watching" || got.Class != "repetition" {
		t.Fatalf("class/state clobbered: %q/%q", got.Class, got.State)
	}
	if got.SuggestedFix != "do X" {
		t.Fatalf("update did not land: %+v", got)
	}
}

// TestWatchItemUpdateRefusesTheFact: the observation and its endpoints ARE the
// edge, and edges are insert-only — so those keys are refused rather than
// accepted-and-ignored, which would tell a caller its reword landed.
func TestWatchItemUpdateRefusesTheFact(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "original wording", store.WatchItem{Class: "waste"})

	for _, key := range immutableWatchKeys {
		t.Run(key, func(t *testing.T) {
			rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, key: "reworded",
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400, body %s", key, rec.Code, rec.Body.String())
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation != "original wording" {
		t.Fatalf("observation = %q, want the fact untouched", got.Observation)
	}
}

// TestWatchItemUpdatePreservesCarlsVerdict is the end-to-end version of
// store.UpdateWatchItem's single most important behaviour: an agent's
// restatement of an item carl has already judged must leave that judgement
// completely alone.
func TestWatchItemUpdatePreservesCarlsVerdict(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{Class: "prompting"})
	if rec := postWatchVerdict(t, map[string]any{"id": id, "verdict": "fix", "comment": "agreed"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}

	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": id, "class": "prompting", "state": "shipped",
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "fix" || got.Comment != "agreed" || got.CommentedAt == "" {
		t.Fatalf("agent write erased carl's judgement: %+v", got)
	}
}

// TestWatchItemRefusesCarlOwnedKeys covers both levels the check runs at: the
// request root and inside `item` — the latter matters most, since an agent
// that just read a whole item back from GET /api/watchlist is exactly the
// caller likeliest to echo verdict/comment straight back in.
func TestWatchItemRefusesCarlOwnedKeys(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{Class: "feedback"})

	for _, key := range carlOwnedWatchKeys {
		t.Run("root/"+key, func(t *testing.T) {
			rec := postWatchItem(t, map[string]any{
				"op": "update", key: "x",
				"item": map[string]any{"id": id, "class": "feedback"},
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
		t.Run("item/"+key, func(t *testing.T) {
			rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, "class": "feedback", key: "x",
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "" || got.Comment != "" {
		t.Fatalf("a refused request still wrote carl's columns: %+v", got)
	}
}

// TestWatchItemUpdateOnInvalidatedEdgeIs404 pins the other half of "unknown
// watch item": an overlay whose edge kraken has unlearned is not a watch item
// any more. Without the handler's liveness probe the store's UPDATE still
// matches the orphaned overlay row and answers 200 — a write into something no
// reader can see, which is the worst shape a 200 can have.
func TestWatchItemUpdateOnInvalidatedEdgeIs404(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a fact that stops being true", store.WatchItem{Class: "waste"})
	if err := st.InvalidateEdge(id, 5); err != nil {
		t.Fatal(err)
	}

	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": id, "suggested_fix": "do X",
	}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// TestWatchItemRejectsMixedCaseKeys: encoding/json matches struct fields
// case-insensitively, so a capitalized key POPULATES the field it names. Every
// guard on this surface therefore has to fold case too, or the one spelling
// that gets past it is also the one that gets silently discarded.
func TestWatchItemRejectsMixedCaseKeys(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "original wording", store.WatchItem{Class: "waste"})

	cases := []struct {
		name string
		body map[string]any
	}{
		{"immutable", map[string]any{"op": "update", "item": map[string]any{"id": id, "Observation": "reworded"}}},
		{"carl/item", map[string]any{"op": "update", "item": map[string]any{"id": id, "Verdict": "fine"}}},
		{"carl/root", map[string]any{"op": "update", "Comment": "x", "item": map[string]any{"id": id}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if rec := postWatchItem(t, c.body); rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}
	if rec := postWatchVerdict(t, map[string]any{"id": id, "verdict": "fine", "Suggested_Fix": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("verdict route: status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation != "original wording" || got.Verdict != "" {
		t.Fatalf("a refused request still wrote something: %+v", got)
	}
}

func TestWatchItemUpdateUnknownIDIs404(t *testing.T) {
	useTestStore(t)
	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": 4242, "class": "waste",
	}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestWatchItemUpdateMissingIDIs400(t *testing.T) {
	useTestStore(t)
	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"class": "waste",
	}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// TestWatchItemRemoveUnwatches pins the narrowed meaning of `remove`: the
// overlay goes, the FACT stays. Unlearning a fact is the graph's invalidate
// path, which is a much louder act than the dream loop losing interest.
func TestWatchItemRemoveUnwatches(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{Class: "waste"})

	rec := postWatchItem(t, map[string]any{"op": "remove", "id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK        bool `json:"ok"`
		Unwatched bool `json:"unwatched"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || !resp.Unwatched {
		t.Fatalf("resp = %+v, want the unwatched acknowledgement", resp)
	}
	if _, err := st.GetWatchItem(id); err == nil {
		t.Fatal("overlay still present after remove")
	}
	edge, err := st.FindLiveEdgeByFact("a")
	if err != nil {
		t.Fatal(err)
	}
	if edge == nil || edge.ID != id {
		t.Fatalf("edge = %+v, want the fact itself to have survived unwatching", edge)
	}

	if rec := postWatchItem(t, map[string]any{"op": "remove", "id": id}); rec.Code != http.StatusNotFound {
		t.Fatalf("repeat remove: status %d, want 404", rec.Code)
	}
	if rec := postWatchItem(t, map[string]any{"op": "remove"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("remove with no id: status %d, want 400", rec.Code)
	}
}

func TestWatchlistBadJSONIs400(t *testing.T) {
	useTestStore(t)
	for _, path := range []string{"/api/watchlist", "/api/watchlist/item"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte("not json")))
		rec := httptest.NewRecorder()
		if path == "/api/watchlist" {
			watchlistHandler(rec, req)
		} else {
			watchItemHandler(rec, req)
		}
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d, want 400", path, rec.Code)
		}
	}
}
