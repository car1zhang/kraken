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
// A watch item is a row in its own `watch_items` table again (Q9 moved it back
// out of the graph), so every id here is a watch-row id and seeding one is a
// single store call. The wire shape is unchanged across BOTH moves, which is
// what these tests keep asserting.

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

// seedWatchItem writes one live watch row and returns its id — the id every
// route in this file takes. The endpoints default to a stable pair so a test
// seeding several observations does not have to invent names it never asserts
// on.
func seedWatchItem(t *testing.T, st *store.Store, observation string, w store.WatchItem) int64 {
	t.Helper()
	w.Observation = observation
	if w.Src == "" {
		w.Src = "dream pass"
	}
	if w.Dst == "" {
		w.Dst = "kraken"
	}
	if w.Predicate == "" {
		w.Predicate = "concerns"
	}
	id, _, err := st.AddWatchItem(w)
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

// TestWatchlistGetOmitsExpiredItems pins what `remove` now means: the row is
// CLOSED rather than deleted, so it leaves every list while staying readable
// by id. A reader sees exactly what it saw before — the item is gone — and
// nothing carl judged is ever actually destroyed.
func TestWatchlistGetOmitsExpiredItems(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "an observation that stops mattering", store.WatchItem{Class: "waste"})

	if err := st.ExpireWatchItem(id, 5); err != nil {
		t.Fatal(err)
	}
	rec := getWatchlist(t, "")
	if got := rec.Body.String(); !bytes.Contains([]byte(got), []byte(`"watchlist":[]`)) {
		t.Fatalf("body = %s, want the expired item gone", got)
	}
	if _, err := st.GetWatchItem(id); err != nil {
		t.Fatalf("GetWatchItem after expiry: %v — an expired row must stay readable by id", err)
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
	// The stamp is the STORE's now — SetWatchVerdict takes no timestamp, so a
	// caller cannot choose one. What this asserts is that the value that lands
	// is a real server-side RFC3339 instant rather than whatever a client sent.
	if _, err := time.Parse(time.RFC3339, got.CommentedAt); err != nil {
		t.Fatalf("commented_at = %q, want server-stamped RFC3339: %v", got.CommentedAt, err)
	}
}

// TestWatchVerdictEmptyClears pins store.SetWatchVerdict's documented undo
// reaching the wire: an empty verdict AND comment reopens the item.
//
// commented_at is deliberately not asserted here. The handler no longer passes
// it — the store owns the stamp — so what it does on a clear is store/watch.go's
// contract to state and store/watch_test.go's to pin, and asserting it from
// this side would be this file claiming a fact it cannot see.
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
	if got.Verdict != "" || got.Comment != "" {
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
// verdict route models none of the agent-owned columns — nor the observation's
// own endpoints — and a request that mentions one is refused rather than
// having it quietly dropped. A caller that believed it edited the observation
// would never find out otherwise.
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

// watchAddItem is the minimal well-formed add payload: an observation and its
// class. src/dst ride along because a real caller names them, but they are no
// longer required — the row has no edge to hang between.
func watchAddItem(observation string, extra map[string]any) map[string]any {
	item := map[string]any{
		"observation": observation, "class": "waste",
		"src": "dream pass", "dst": "kraken",
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
		t.Fatalf("resp = %+v, want ok plus the new row id, unfolded", resp)
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
}

// TestWatchItemAddWithoutEndpoints pins the requirement that went away with
// the edge: src/dst were mandatory only because a new fact could not be
// inserted without two endpoints to hang it between, and a `watch_items` row
// has no such need. Refusing an add for want of them would be enforcing a
// constraint that no longer exists anywhere below this line.
func TestWatchItemAddWithoutEndpoints(t *testing.T) {
	useTestStore(t)

	rec := postWatchItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "the same lint command is rediscovered every run", "class": "repetition",
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
}

// TestWatchItemAddFolds pins the behaviour that replaced the skill's old
// find-then-bump dance: re-stating an observation already tracked (differing
// only in case and wrapping) increments the sighting count on the EXISTING
// item rather than adding a near-duplicate row.
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
		"hermes agents\n  re-read the   contract",
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

func TestWatchItemAddValidation(t *testing.T) {
	useTestStore(t)

	cases := []struct {
		name string
		body map[string]any
	}{
		{"empty observation", map[string]any{"op": "add", "item": watchAddItem("   ", nil)}},
		{"bad class", map[string]any{"op": "add", "item": watchAddItem("x", map[string]any{"class": "vibes"})}},
		{"missing class", map[string]any{"op": "add", "item": map[string]any{
			"observation": "x", "src": "dream pass", "dst": "kraken",
		}}},
		{"bad state", map[string]any{"op": "add", "item": watchAddItem("x", map[string]any{"state": "maybe"})}},
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
// first_seen/last_seen/sightings — the facts the row exists to record. The old
// GET-then-backfill dance is gone; the COALESCE(NULLIF(...)) columns do it in
// one statement.
//
// The three values are read back from the seeded row rather than asserted
// against literals, because the store owns what an insert stamps them to —
// this test is about the update PRESERVING whatever they were, which is the
// property the handler is responsible for.
func TestWatchItemUpdatePreservesHistory(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "a", store.WatchItem{
		Class: "repetition", State: "watching",
	})
	before, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}

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
	if got.Sightings != before.Sightings || got.FirstSeen != before.FirstSeen || got.LastSeen != before.LastSeen {
		t.Fatalf("history clobbered by a partial update: %+v, was sightings %d first %d last %d",
			got, before.Sightings, before.FirstSeen, before.LastSeen)
	}
	if got.State != "watching" || got.Class != "repetition" {
		t.Fatalf("class/state clobbered: %q/%q", got.Class, got.State)
	}
	if got.SuggestedFix != "do X" {
		t.Fatalf("update did not land: %+v", got)
	}
}

// TestWatchItemUpdateRefusesTheObservation: the sentence and its endpoints are
// the row's identity — the fold matches on the sentence byte for byte — so
// those keys are refused rather than accepted-and-ignored, which would tell a
// caller its reword landed while silently splitting one tracked pattern in two.
func TestWatchItemUpdateRefusesTheObservation(t *testing.T) {
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
		t.Fatalf("observation = %q, want it untouched", got.Observation)
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

// TestWatchItemUpdateOnExpiredItemIs404 pins the other half of "unknown watch
// item": a CLOSED row is not a watch item any more. It is still readable by
// id, so without the handler's liveness probe the store's UPDATE would match
// it and answer 200 — a write into something no reader can see, which is the
// worst shape a 200 can have.
func TestWatchItemUpdateOnExpiredItemIs404(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	id := seedWatchItem(t, st, "an observation that stops mattering", store.WatchItem{Class: "waste"})
	if err := st.ExpireWatchItem(id, 5); err != nil {
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

// TestWatchItemRemoveCloses pins the meaning of `remove`: the row is closed,
// not deleted. It leaves every list, it stays readable by id, and closing it
// AGAIN is a success rather than a 404 — the caller asked for it to be gone
// and it is gone. An id naming no row at all is still a 404, because that is
// a different statement.
func TestWatchItemRemoveCloses(t *testing.T) {
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
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatalf("GetWatchItem after remove: %v — no watch row is ever deleted", err)
	}
	if got.ExpiredAt == 0 {
		t.Fatalf("item = %+v, want expired_at stamped", got)
	}

	if rec := postWatchItem(t, map[string]any{"op": "remove", "id": id}); rec.Code != http.StatusOK {
		t.Fatalf("repeat remove: status %d, want 200 (closing a closed row is idempotent)", rec.Code)
	}
	if rec := postWatchItem(t, map[string]any{"op": "remove", "id": id + 999}); rec.Code != http.StatusNotFound {
		t.Fatalf("remove of an unknown id: status %d, want 404", rec.Code)
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
