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

func seedWatchItem(t *testing.T, st *store.Store, w store.WatchItem) int64 {
	t.Helper()
	id, err := st.AddWatchItem(w)
	if err != nil {
		t.Fatalf("AddWatchItem: %v", err)
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
	seedWatchItem(t, st, store.WatchItem{Observation: "a", Class: "waste", State: "watching"})
	seedWatchItem(t, st, store.WatchItem{Observation: "b", Class: "waste", State: "shipped"})

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
		t.Fatalf("filtered list = %+v, want only the shipped row", resp.Watchlist)
	}

	all := getWatchlist(t, "")
	var allResp struct {
		Watchlist []webWatchItem `json:"watchlist"`
	}
	if err := json.Unmarshal(all.Body.Bytes(), &allResp); err != nil {
		t.Fatal(err)
	}
	if len(allResp.Watchlist) != 2 {
		t.Fatalf("unfiltered list = %+v, want both rows (empty state means all)", allResp.Watchlist)
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
	id := seedWatchItem(t, st, store.WatchItem{Observation: "a", Class: "correction"})

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
// reaching the wire: an empty verdict AND comment reopens the row, wiping
// commented_at with it rather than leaving a timestamp for a judgement that
// no longer exists.
func TestWatchVerdictEmptyClears(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, store.WatchItem{Observation: "a", Class: "waste"})
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
		t.Fatalf("row not cleared: %+v", got)
	}
}

func TestWatchVerdictValidation(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, store.WatchItem{Observation: "a", Class: "waste"})

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
// verdict route models none of the agent-owned columns, and a request that
// mentions one is refused rather than having it quietly dropped — a caller
// that believed it edited the observation would never find out otherwise.
func TestWatchVerdictRefusesAgentOwnedKeys(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, store.WatchItem{Observation: "original", Class: "waste"})

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

func TestWatchItemAddReturnsID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()

	rec := postWatchItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "zeus keeps re-reading the same file", "class": "waste",
		"evidence": `{"n":3}`, "suggested_fix": "cache it",
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK bool  `json:"ok"`
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.ID == 0 {
		t.Fatalf("resp = %+v, want ok plus the new row id", resp)
	}
	got, err := st.GetWatchItem(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Observation != "zeus keeps re-reading the same file" || got.Class != "waste" {
		t.Fatalf("row = %+v", got)
	}
	if got.State != "watching" || got.Sightings != 1 {
		t.Fatalf("defaults not applied: state %q sightings %d", got.State, got.Sightings)
	}
}

func TestWatchItemAddValidation(t *testing.T) {
	useTestStore(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty observation", map[string]any{"op": "add", "item": map[string]any{"observation": "   ", "class": "waste"}}},
		{"bad class", map[string]any{"op": "add", "item": map[string]any{"observation": "x", "class": "vibes"}}},
		{"missing class", map[string]any{"op": "add", "item": map[string]any{"observation": "x"}}},
		{"bad state", map[string]any{"op": "add", "item": map[string]any{"observation": "x", "class": "waste", "state": "maybe"}}},
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

// TestWatchItemUpdatePreservesHistory pins the reason watchItemUpsert re-reads
// the row before writing: UpdateWatchItem is a full-row restatement, so a
// partial update (the normal case for an agent that only wants to revise its
// suggested fix) must not zero first_seen/last_seen/sightings — the two facts
// the table exists to record.
func TestWatchItemUpdatePreservesHistory(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, store.WatchItem{
		Observation: "a", Class: "repetition", State: "watching",
		Sightings: 7, FirstSeen: 1000, LastSeen: 2000,
	})

	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": id, "observation": "a, restated", "class": "repetition", "suggested_fix": "do X",
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
	if got.State != "watching" {
		t.Fatalf("state clobbered: %q", got.State)
	}
	if got.Observation != "a, restated" || got.SuggestedFix != "do X" {
		t.Fatalf("update did not land: %+v", got)
	}
}

// TestWatchItemUpdatePreservesCarlsVerdict is the end-to-end version of
// store.UpdateWatchItem's single most important behaviour: an agent's
// restatement of an observation carl has already judged must leave that
// judgement completely alone.
func TestWatchItemUpdatePreservesCarlsVerdict(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, store.WatchItem{Observation: "a", Class: "prompting"})
	if rec := postWatchVerdict(t, map[string]any{"id": id, "verdict": "fix", "comment": "agreed"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}

	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": id, "observation": "a, seen again", "class": "prompting",
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
// that just read a whole row back from GET /api/watchlist is exactly the
// caller likeliest to echo verdict/comment straight back in.
func TestWatchItemRefusesCarlOwnedKeys(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, store.WatchItem{Observation: "a", Class: "feedback"})

	for _, key := range carlOwnedWatchKeys {
		t.Run("root/"+key, func(t *testing.T) {
			rec := postWatchItem(t, map[string]any{
				"op": "update", key: "x",
				"item": map[string]any{"id": id, "observation": "a", "class": "feedback"},
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
		t.Run("item/"+key, func(t *testing.T) {
			rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, "observation": "a", "class": "feedback", key: "x",
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

func TestWatchItemUpdateUnknownIDIs404(t *testing.T) {
	useTestStore(t)
	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": 4242, "observation": "a", "class": "waste",
	}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestWatchItemUpdateMissingIDIs400(t *testing.T) {
	useTestStore(t)
	rec := postWatchItem(t, map[string]any{"op": "update", "item": map[string]any{
		"observation": "a", "class": "waste",
	}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestWatchItemRemove(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, store.WatchItem{Observation: "a", Class: "waste"})

	if rec := postWatchItem(t, map[string]any{"op": "remove", "id": id}); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if _, err := st.GetWatchItem(id); err == nil {
		t.Fatal("row still present after remove")
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
