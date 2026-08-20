package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// watch_contract_test.go — the LOGIC dimension of the watchlist routes: the
// frozen wire shape, the happy paths of the three routes, the fold, and the
// ownership split end to end. Boundaries, malformed bodies and refusal wording
// belong to watch_edge_test.go.
//
// These began as blind tests for the round that moved a watch item INTO the
// knowledge graph. Q9 moved it back OUT — a watch item is a `watch_items` row
// again — so the edge, episode and node assertions are gone. What survives is
// what was never about the backing: the wire keys never changed in either
// direction, which is the reason neither move bumped apiContract, and the
// column ownership held across both.
//
// Every package-level identifier is prefixed `contract`.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

func contractOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func contractPostVerdict(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	watchlistHandler(rec, httptest.NewRequest(http.MethodPost, "/api/watchlist", bytes.NewReader(mustJSON(t, body))))
	return rec
}

func contractPostItem(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	watchItemHandler(rec, httptest.NewRequest(http.MethodPost, "/api/watchlist/item", bytes.NewReader(mustJSON(t, body))))
	return rec
}

func contractGetWatchlist(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	watchlistHandler(rec, httptest.NewRequest(http.MethodGet, "/api/watchlist"+query, nil))
	return rec
}

// contractItemResp is the /api/watchlist/item response envelope: {ok, id,
// folded} for add and update, {ok, unwatched} for remove.
type contractItemResp struct {
	OK        bool  `json:"ok"`
	ID        int64 `json:"id"`
	Folded    bool  `json:"folded"`
	Unwatched bool  `json:"unwatched"`
}

func contractDecodeItemResp(t *testing.T, rec *httptest.ResponseRecorder) contractItemResp {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	var r contractItemResp
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatalf("decode item response %s: %v", rec.Body.String(), err)
	}
	if !r.OK {
		t.Fatalf("response = %s, want ok:true", rec.Body.String())
	}
	return r
}

// contractListRaw decodes GET /api/watchlist as raw key/value maps so the
// FROZEN key set can be asserted directly — a typed struct would silently
// swallow an extra key, which is precisely the failure the freeze forbids.
func contractListRaw(t *testing.T, query string) []map[string]json.RawMessage {
	t.Helper()
	rec := contractGetWatchlist(t, query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status %d, body %s", query, rec.Code, rec.Body.String())
	}
	var resp map[string][]map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode watchlist %s: %v", rec.Body.String(), err)
	}
	items, ok := resp["watchlist"]
	if !ok {
		t.Fatalf("body %s has no \"watchlist\" key", rec.Body.String())
	}
	return items
}

func contractKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// contractSeedWatched puts one live watch row in the store directly, through
// the frozen store verb — the fixture for every GET/verdict test below.
func contractSeedWatched(t *testing.T, st *store.Store, src, dst, predicate, observation string, w store.WatchItem) int64 {
	t.Helper()
	w.Observation = observation
	w.Src, w.Dst, w.Predicate = src, dst, predicate
	id, _, err := st.AddWatchItem(w)
	if err != nil {
		t.Fatalf("AddWatchItem %q: %v", observation, err)
	}
	return id
}

// contractAlwaysKeys / contractOmitemptyKeys are the frozen key list, split by
// whether the key is omitempty. src/dst/predicate are deliberately absent from
// BOTH: nothing on either client renders them, and adding a key here would be
// an apiContract bump neither move makes.
var contractAlwaysKeys = []string{
	"class", "id", "last_seen", "observation", "sightings", "state", "first_seen", "updated_at",
}

var contractOmitemptyKeys = []string{
	"comment", "commented_at", "evidence", "pr_url", "suggested_fix", "verdict",
}

func contractSorted(ss ...[]string) []string {
	var out []string
	for _, s := range ss {
		out = append(out, s...)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// GET /api/watchlist — shape frozen byte-key-identical across both moves
// ---------------------------------------------------------------------------

// TestContractWatchlistGetKeySetIsFrozen is the guarantee in test form: the web
// DREAM view and the iOS app run UNCHANGED against a re-backed table only if
// this exact key set survives. A response carrying src/dst/predicate — or any
// other new key — is a FAILURE, not an improvement.
func TestContractWatchlistGetKeySetIsFrozen(t *testing.T) {
	st := useTestStore(t)
	full := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "zeus re-reads the same spec",
		store.WatchItem{
			Class: "waste", State: "watching",
			Evidence:     `{"n":3}`,
			SuggestedFix: "cache it", PRURL: "https://example/pr/1",
		})
	if err := st.SetWatchVerdict(full, "fix", "agreed"); err != nil {
		t.Fatal(err)
	}

	items := contractListRaw(t, "")
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	want := contractSorted(contractAlwaysKeys, contractOmitemptyKeys)
	if got := contractKeys(items[0]); !contractEqualStrings(got, want) {
		t.Fatalf("item keys = %v\nwant exactly %v (the shape is frozen; no new keys, and these routes never bump apiContract)", got, want)
	}
	for _, banned := range []string{"src", "dst", "predicate", "strength", "edge_id", "expired_at"} {
		if _, ok := items[0][banned]; ok {
			t.Fatalf("item carries %q — the wire keeps the row's linkage and lifecycle OFF it", banned)
		}
	}

	// `id` is the watch-row id and `observation` is the row's own sentence.
	var gotID int64
	if err := json.Unmarshal(items[0]["id"], &gotID); err != nil {
		t.Fatal(err)
	}
	if gotID != full {
		t.Fatalf("id = %d, want the watch row id %d", gotID, full)
	}
	var gotObs string
	if err := json.Unmarshal(items[0]["observation"], &gotObs); err != nil {
		t.Fatal(err)
	}
	if gotObs != "zeus re-reads the same spec" {
		t.Fatalf("observation = %q, want the row's own sentence", gotObs)
	}

	// The six omitempty keys drop out of a bare item; the other eight never do.
	bare := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "a bare observation",
		store.WatchItem{Class: "prompting", State: "watching"})
	items = contractListRaw(t, "")
	var bareItem map[string]json.RawMessage
	for _, it := range items {
		var id int64
		if err := json.Unmarshal(it["id"], &id); err != nil {
			t.Fatal(err)
		}
		if id == bare {
			bareItem = it
		}
	}
	if bareItem == nil {
		t.Fatal("the bare item is missing from the list")
	}
	if got := contractKeys(bareItem); !contractEqualStrings(got, contractSorted(contractAlwaysKeys)) {
		t.Fatalf("bare item keys = %v\nwant exactly %v (the omitempty set)", got, contractSorted(contractAlwaysKeys))
	}
}

// contractEqualStrings is a tiny helper kept local to this file's assertions.
func contractEqualStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestContractWatchlistGetFiltersAndNeverNull pins the two carried-over
// conventions: `?state=` filters, an empty list serves [] rather than null,
// and an unrecognized state is a 400 naming the enum rather than a silent [].
func TestContractWatchlistGetFiltersAndNeverNull(t *testing.T) {
	st := useTestStore(t)

	if got := contractGetWatchlist(t, "").Body.String(); !strings.Contains(got, `"watchlist":[]`) {
		t.Fatalf("empty body = %s, want an empty ARRAY, never null", got)
	}

	contractSeedWatched(t, st, "zeus", "kraken", "concerns", "still watching this",
		store.WatchItem{Class: "waste", State: "watching"})
	shipped := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "already shipped this",
		store.WatchItem{Class: "waste", State: "shipped"})

	items := contractListRaw(t, "?state=shipped")
	if len(items) != 1 {
		t.Fatalf("state=shipped returned %d items, want 1", len(items))
	}
	var id int64
	if err := json.Unmarshal(items[0]["id"], &id); err != nil {
		t.Fatal(err)
	}
	if id != shipped {
		t.Fatalf("filtered id = %d, want %d", id, shipped)
	}
	if all := contractListRaw(t, ""); len(all) != 2 {
		t.Fatalf("unfiltered list has %d items, want 2 (empty state means all)", len(all))
	}
	if rec := contractGetWatchlist(t, "?state=pending"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad state: status %d, want 400", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// POST /api/watchlist — carl's verdict
// ---------------------------------------------------------------------------

// TestContractWatchVerdictRoundTrip pins the happy path: commented_at is
// stamped server-side as RFC3339, the documented undo (both fields empty)
// reopens the item, and carl's route moves nothing on the agent's side of the
// split.
//
// The agent-side columns are compared against a READ-BACK baseline rather than
// against literals: the store owns what an insert stamps sightings and the two
// timestamps to, and what this test is responsible for is that the verdict
// route leaves whatever they were exactly as they were.
func TestContractWatchVerdictRoundTrip(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "an observation awaiting judgement",
		store.WatchItem{Class: "correction", State: "watching"})
	before, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}

	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fix", "comment": "  yes, do this  "}); rec.Code != http.StatusOK {
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
		t.Fatalf("commented_at = %q, want a server-stamped RFC3339: %v", got.CommentedAt, err)
	}
	// The ownership split: carl's route wrote nothing on the agent's side.
	if got.Sightings != before.Sightings || got.Class != before.Class ||
		got.FirstSeen != before.FirstSeen || got.LastSeen != before.LastSeen {
		t.Fatalf("the verdict route moved an agent-owned column: %+v, was %+v", got, before)
	}

	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": ""}); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d, body %s", rec.Code, rec.Body.String())
	}
	got, err = st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "" || got.Comment != "" {
		t.Fatalf("the undo did not reopen the item: %+v", got)
	}

	// An unknown watch item is a 404 — including a row that has been CLOSED,
	// which every read already hides.
	closed := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "an observation nobody tracks",
		store.WatchItem{Class: "waste", State: "watching"})
	if err := st.ExpireWatchItem(closed, 99); err != nil {
		t.Fatal(err)
	}
	if rec := contractPostVerdict(t, map[string]any{"id": closed, "verdict": "fine"}); rec.Code != http.StatusNotFound {
		t.Fatalf("verdict on a closed row: status %d, want 404", rec.Code)
	}
}

// TestContractWatchVerdictRefusesAgentOwnedKeys pins the key-rejection half:
// every agent-owned key, the observation's endpoints included, is refused on
// carl's route rather than being silently dropped.
func TestContractWatchVerdictRefusesAgentOwnedKeys(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "the original observation",
		store.WatchItem{Class: "waste", State: "watching"})

	for _, key := range []string{"src", "dst", "predicate"} {
		found := false
		for _, k := range agentOwnedWatchKeys {
			if k == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("agentOwnedWatchKeys is missing %q — the endpoints belong to the agent's route", key)
		}
	}
	for _, key := range agentOwnedWatchKeys {
		t.Run(key, func(t *testing.T) {
			rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fine", key: "whatever"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400, body %s", key, rec.Code, rec.Body.String())
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "" || got.Observation != "the original observation" {
		t.Fatalf("a refused request still wrote something: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// op add — fold or create
// ---------------------------------------------------------------------------

// TestContractWatchItemAddCreates pins the create path end to end: the
// observation is normalized to one line, the row carries the endpoints and the
// optional workflow fields the caller sent, the defaults land, carl's trio
// stays empty, and the DREAM view sees it on the frozen wire under the same id
// the response returned.
func TestContractWatchItemAddCreates(t *testing.T) {
	st := useTestStore(t)

	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation":   "zeus   re-reads\nthe same spec twice",
		"class":         "waste",
		"src":           "zeus",
		"dst":           "job specs",
		"predicate":     "re-reads",
		"evidence":      `{"n":3}`,
		"suggested_fix": "cache the spec",
		"pr_url":        "https://example/pr/2",
	}}))
	if resp.ID == 0 || resp.Folded {
		t.Fatalf("add response = %+v, want a new row id with folded:false", resp)
	}

	got, err := st.GetWatchItem(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	wantObs := contractOneLine("zeus   re-reads\nthe same spec twice")
	if got.Observation != wantObs {
		t.Fatalf("observation = %q, want oneLine(observation) = %q", got.Observation, wantObs)
	}
	if got.Src != "zeus" || got.Dst != "job specs" || got.Predicate != "re-reads" {
		t.Fatalf("endpoints = %q/%q/%q, want the given ones", got.Src, got.Dst, got.Predicate)
	}
	if got.Class != "waste" || got.State != "watching" || got.Sightings != 1 {
		t.Fatalf("defaults = %+v, want class waste / state watching / sightings 1", got)
	}
	if got.Evidence != `{"n":3}` || got.SuggestedFix != "cache the spec" || got.PRURL != "https://example/pr/2" {
		t.Fatalf("optional fields did not land: %+v", got)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("an agent add wrote carl's trio: %+v", got)
	}

	// The DREAM view sees it on the frozen wire, keyed by the same id.
	items := contractListRaw(t, "")
	if len(items) != 1 {
		t.Fatalf("GET returned %d items, want 1", len(items))
	}
	var listedID int64
	if err := json.Unmarshal(items[0]["id"], &listedID); err != nil {
		t.Fatal(err)
	}
	if listedID != resp.ID {
		t.Fatalf("listed id = %d, want the row id %d", listedID, resp.ID)
	}
}

// TestContractWatchItemAddDefaultsPredicate pins the one optional endpoint
// field: an omitted predicate defaults to `concerns` rather than refusing.
func TestContractWatchItemAddDefaultsPredicate(t *testing.T) {
	st := useTestStore(t)
	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "the daemon polls too often", "class": "waste",
		"src": "the daemon", "dst": "kraken",
	}}))
	got, err := st.GetWatchItem(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Predicate != "concerns" {
		t.Fatalf("predicate = %q, want the default `concerns`", got.Predicate)
	}
}

// TestContractWatchItemAddFoldsRepeat pins the fold: re-adding the same
// wording is a repeat SIGHTING — the server-side fold that replaced the dream
// loop's old Find→Bump dance — and the endpoints of the existing row are left
// alone, since the observation already has them.
func TestContractWatchItemAddFoldsRepeat(t *testing.T) {
	st := useTestStore(t)
	first := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "zeus re-reads the same spec", "class": "waste",
		"src": "zeus", "dst": "job specs", "predicate": "re-reads",
		"evidence": "sighting one",
	}}))

	second := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "zeus   re-reads\tthe same spec", "class": "repetition",
		"src": "ignored src", "dst": "ignored dst", "predicate": "ignored",
		"evidence": "sighting two", "suggested_fix": "cache it", "state": "shipped",
	}}))
	if !second.Folded {
		t.Fatalf("second add = %+v, want folded:true", second)
	}
	if second.ID != first.ID {
		t.Fatalf("folded id = %d, want the existing row %d", second.ID, first.ID)
	}
	if all := contractListRaw(t, ""); len(all) != 1 {
		t.Fatalf("%d items, want 1 — a fold adds no row", len(all))
	}

	got, err := st.GetWatchItem(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Sightings != 2 {
		t.Fatalf("sightings = %d, want 2", got.Sightings)
	}
	if got.Evidence != "sighting two" {
		t.Fatalf("evidence = %q, want it replaced wholesale", got.Evidence)
	}
	if got.SuggestedFix != "cache it" || got.Class != "repetition" || got.State != "shipped" {
		t.Fatalf("row = %+v, want suggested_fix/class/state applied on a fold", got)
	}
	if got.Src != "zeus" || got.Dst != "job specs" || got.Predicate != "re-reads" {
		t.Fatalf("a fold rewrote the endpoints: %q/%q/%q", got.Src, got.Dst, got.Predicate)
	}
}

// ---------------------------------------------------------------------------
// update and remove
// ---------------------------------------------------------------------------

// TestContractWatchItemUpdateIsPartial: a partial update must leave the
// counters and carl's judgement alone by virtue of the store verb being
// partial. The baselines are read back rather than asserted as literals, for
// the reason the verdict round-trip reads them back.
func TestContractWatchItemUpdateIsPartial(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "an observation carl judged",
		store.WatchItem{Class: "repetition", State: "watching"})
	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fix", "comment": "agreed"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}
	before, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}

	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": id, "suggested_fix": "do X",
	}}))
	if resp.ID != id {
		t.Fatalf("update response id = %d, want %d", resp.ID, id)
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.SuggestedFix != "do X" {
		t.Fatalf("the update did not land: %+v", got)
	}
	if got.Sightings != before.Sightings || got.FirstSeen != before.FirstSeen ||
		got.LastSeen != before.LastSeen || got.Class != "repetition" || got.State != "watching" {
		t.Fatalf("a partial update zeroed untouched columns: %+v, was %+v (zero = leave alone)", got, before)
	}
	if got.Verdict != "fix" || got.Comment != "agreed" || got.CommentedAt == "" {
		t.Fatalf("the agent's update erased carl's judgement: %+v", got)
	}

	// The observation and its endpoints are immutable on this route — refused
	// rather than silently dropped.
	for _, key := range []string{"observation", "src", "dst", "predicate"} {
		t.Run(key, func(t *testing.T) {
			rec := contractPostItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, key: "x",
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s in an update item: status %d, want 400, body %s", key, rec.Code, rec.Body.String())
			}
		})
	}
	after, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Observation != "an observation carl judged" {
		t.Fatalf("a refused update still changed the observation: %q", after.Observation)
	}
}

// TestContractWatchItemRemoveClosesTheRow: remove CLOSES the row. The response
// says so, the row leaves every read, and it survives by id — carl's verdict
// on it included, which is the whole reason nothing here is a delete.
//
// A later add of the same wording therefore creates a FRESH row rather than
// reviving the closed one: the fold matches live rows only, so re-adding is
// carl's pass noticing the thing again after having dropped it, which is a new
// sighting count rather than a continuation of the old one.
func TestContractWatchItemRemoveClosesTheRow(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "an observation that outlives its watch",
		store.WatchItem{Class: "waste", State: "watching"})
	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fine", "comment": "noted"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}

	rec := contractPostItem(t, map[string]any{"op": "remove", "id": id})
	resp := contractDecodeItemResp(t, rec)
	if !resp.Unwatched {
		t.Fatalf("remove response = %s, want {\"ok\":true,\"unwatched\":true}", rec.Body.String())
	}

	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatalf("the row did not survive a remove: %v — no watch row is ever deleted", err)
	}
	if got.ExpiredAt == 0 {
		t.Fatalf("row = %+v, want expired_at stamped", got)
	}
	if got.Verdict != "fine" || got.Comment != "noted" {
		t.Fatalf("closing the row took carl's judgement with it: %+v", got)
	}
	if items := contractListRaw(t, ""); len(items) != 0 {
		t.Fatalf("GET still lists %d item(s) after remove", len(items))
	}

	again := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "an observation that outlives its watch", "class": "waste",
		"src": "zeus", "dst": "kraken",
	}}))
	if again.Folded || again.ID == id {
		t.Fatalf("re-add = %+v, want a FRESH row — the fold matches live rows only", again)
	}
	if items := contractListRaw(t, ""); len(items) != 1 {
		t.Fatalf("%d items after the re-add, want just the fresh one", len(items))
	}
}

// ---------------------------------------------------------------------------
// the ownership split, end to end across both routes
// ---------------------------------------------------------------------------

// TestContractWatchOwnershipSplitSurvivesTheMove pins it whole: no
// agent-reachable path writes verdict/comment/commented_at, no carl-reachable
// path writes anything else, and both routes still refuse the other side's
// keys at every level they did before.
func TestContractWatchOwnershipSplitSurvivesTheMove(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "the contested observation",
		store.WatchItem{Class: "feedback", State: "watching"})
	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fine", "comment": "leave it"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}

	// The agent's route refuses carl's keys at BOTH levels.
	for _, key := range carlOwnedWatchKeys {
		t.Run("root/"+key, func(t *testing.T) {
			rec := contractPostItem(t, map[string]any{
				"op": "update", key: "x", "item": map[string]any{"id": id, "class": "feedback"},
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
		t.Run("item/"+key, func(t *testing.T) {
			rec := contractPostItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, "class": "feedback", key: "x",
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
		})
	}

	// A legal agent write (fold via add) still cannot move carl's trio.
	if resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "the   contested observation", "class": "waste",
		"src": "zeus", "dst": "kraken", "evidence": "again",
	}})); !resp.Folded || resp.ID != id {
		t.Fatalf("fold = %+v, want a fold onto %d", resp, id)
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "fine" || got.Comment != "leave it" || got.CommentedAt == "" {
		t.Fatalf("an agent add moved carl's trio: %+v", got)
	}
	if got.Sightings != 2 || got.Evidence != "again" || got.Class != "waste" {
		t.Fatalf("the fold did not apply the agent's own fields: %+v", got)
	}
}
