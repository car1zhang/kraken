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

// watch_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md's slice A (watchlist moves into the knowledge graph),
// written from the contract alone with no sight of internal/api/watchlist.go
// or internal/store/watch.go. Every assertion cites the contract section it
// traces to.
//
// Dimension: LOGIC — the frozen wire shape, the happy paths of the three
// routes, the fold→promote→create order, and the ownership split end to end.
// Boundaries, malformed bodies and refusal wording belong to athena-edge's
// watch_edge_test.go.
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

// contractItemResp is the /api/watchlist/item response envelope A3.1/A3.3
// freeze: {ok, id, folded} for add, {ok, unwatched} for remove.
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
// FROZEN key set (A1) can be asserted directly — a typed struct would silently
// swallow an extra key, which is precisely the failure A1+W3 forbid.
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
		t.Fatalf("body %s has no \"watchlist\" key (A1)", rec.Body.String())
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

// contractSeedWatched puts a live edge plus its watch overlay in the store
// directly, through the frozen S3 verbs — the fixture for every GET/verdict
// test below.
func contractSeedWatched(t *testing.T, st *store.Store, src, dst, predicate, fact string, w store.WatchItem) int64 {
	t.Helper()
	id := contractSeedEdge(t, st, src, dst, predicate, fact, 5)
	if err := st.WatchEdge(id, w); err != nil {
		t.Fatalf("WatchEdge: %v", err)
	}
	return id
}

func contractSeedEdge(t *testing.T, st *store.Store, src, dst, predicate, fact string, strength int64) int64 {
	t.Helper()
	a, err := st.UpsertNode(store.GraphNode{Name: src, Type: "component"})
	if err != nil {
		t.Fatalf("upsert %q: %v", src, err)
	}
	b, err := st.UpsertNode(store.GraphNode{Name: dst, Type: "component"})
	if err != nil {
		t.Fatalf("upsert %q: %v", dst, err)
	}
	id, added, err := st.InsertEdge(store.GraphEdge{
		SrcID: a, DstID: b, Predicate: predicate, Fact: fact, Strength: strength, ValidFrom: 1,
	})
	if err != nil || !added {
		t.Fatalf("insert edge %q: added=%v err=%v", fact, added, err)
	}
	return id
}

func contractLiveEdgeCount(t *testing.T, st *store.Store) int {
	t.Helper()
	edges, err := st.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range edges {
		if e.ExpiredAt == 0 {
			n++
		}
	}
	return n
}

// contractAlwaysKeys / contractOmitemptyKeys are A1's frozen key list, split
// by whether the key is omitempty. src/dst/predicate are deliberately absent
// from BOTH: the graph routes already expose linkage, and adding a key here
// would be an apiContract bump this job does not make (A1, W3).
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
// A1 — GET /api/watchlist, shape frozen byte-key-identical to today's
// ---------------------------------------------------------------------------

// TestContractWatchlistGetKeySetIsFrozen is the W1 guarantee in test form: the
// web DREAM view and the iOS app run UNCHANGED against the new backing only if
// this exact key set survives. A response carrying src/dst/predicate — or any
// other new key — is a FAILURE (A1, W3), not an improvement.
func TestContractWatchlistGetKeySetIsFrozen(t *testing.T) {
	st := useTestStore(t)
	full := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "zeus re-reads the same spec",
		store.WatchItem{
			Class: "waste", State: "watching", Sightings: 3,
			FirstSeen: 1000, LastSeen: 2000, Evidence: `{"n":3}`,
			SuggestedFix: "cache it", PRURL: "https://example/pr/1", UpdatedAt: 2000,
		})
	if err := st.SetWatchVerdict(full, "fix", "agreed", "2026-01-02T03:04:05Z"); err != nil {
		t.Fatal(err)
	}

	items := contractListRaw(t, "")
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	want := contractSorted(contractAlwaysKeys, contractOmitemptyKeys)
	if got := contractKeys(items[0]); !contractEqualStrings(got, want) {
		t.Fatalf("item keys = %v\nwant exactly %v (A1: the shape is frozen; no new keys, W3: these routes never bump apiContract)", got, want)
	}
	for _, banned := range []string{"src", "dst", "predicate", "strength", "edge_id"} {
		if _, ok := items[0][banned]; ok {
			t.Fatalf("item carries %q — A1 keeps graph linkage OFF this wire", banned)
		}
	}

	// A1: `id` is now the EDGE id and `observation` is the edge's fact sentence.
	var gotID int64
	if err := json.Unmarshal(items[0]["id"], &gotID); err != nil {
		t.Fatal(err)
	}
	if gotID != full {
		t.Fatalf("id = %d, want the edge id %d (A1)", gotID, full)
	}
	var gotObs string
	if err := json.Unmarshal(items[0]["observation"], &gotObs); err != nil {
		t.Fatal(err)
	}
	if gotObs != "zeus re-reads the same spec" {
		t.Fatalf("observation = %q, want the edge's fact sentence (A1)", gotObs)
	}

	// A1: the six omitempty keys drop out of a bare item; the other eight never do.
	bare := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "a bare observation",
		store.WatchItem{Class: "prompting", State: "watching", Sightings: 1, FirstSeen: 9000, LastSeen: 9000, UpdatedAt: 9000})
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
		t.Fatalf("bare item keys = %v\nwant exactly %v (A1's omitempty set)", got, contractSorted(contractAlwaysKeys))
	}
}

// contractEqualStrings is a tiny helper kept unexported-and-local to this file's
// assertions; named without the prefix would risk colliding, so it stays
// contract-scoped via its only callers above.
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

// TestContractWatchlistGetFiltersAndNeverNull pins A1's two carried-over
// conventions: `?state=` filters, an empty list serves [] rather than null,
// and an unrecognized state is a 400 naming the enum rather than a silent [].
func TestContractWatchlistGetFiltersAndNeverNull(t *testing.T) {
	st := useTestStore(t)

	if got := contractGetWatchlist(t, "").Body.String(); !strings.Contains(got, `"watchlist":[]`) {
		t.Fatalf("empty body = %s, want an empty ARRAY, never null (A1)", got)
	}

	contractSeedWatched(t, st, "zeus", "kraken", "concerns", "still watching this",
		store.WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 1, LastSeen: 10, UpdatedAt: 10})
	shipped := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "already shipped this",
		store.WatchItem{Class: "waste", State: "shipped", Sightings: 1, FirstSeen: 1, LastSeen: 20, UpdatedAt: 20})

	items := contractListRaw(t, "?state=shipped")
	if len(items) != 1 {
		t.Fatalf("state=shipped returned %d items, want 1 (A1)", len(items))
	}
	var id int64
	if err := json.Unmarshal(items[0]["id"], &id); err != nil {
		t.Fatal(err)
	}
	if id != shipped {
		t.Fatalf("filtered id = %d, want %d (A1)", id, shipped)
	}
	if all := contractListRaw(t, ""); len(all) != 2 {
		t.Fatalf("unfiltered list has %d items, want 2 (A1: empty state means all)", len(all))
	}
	if rec := contractGetWatchlist(t, "?state=pending"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad state: status %d, want 400 (A1)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// A2 — POST /api/watchlist, carl's verdict, unchanged
// ---------------------------------------------------------------------------

// TestContractWatchVerdictRoundTrip pins A2's happy path over the new backing:
// id is the edge id, commented_at is stamped server-side as RFC3339, and the
// documented undo (both fields empty) clears all three columns.
func TestContractWatchVerdictRoundTrip(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "a fact awaiting judgement",
		store.WatchItem{Class: "correction", State: "watching", Sightings: 2, FirstSeen: 1, LastSeen: 5, UpdatedAt: 5})

	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fix", "comment": "  yes, do this  "}); rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s (A2)", rec.Code, rec.Body.String())
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "fix" || got.Comment != "yes, do this" {
		t.Fatalf("verdict/comment = %q/%q (A2)", got.Verdict, got.Comment)
	}
	if _, err := time.Parse(time.RFC3339, got.CommentedAt); err != nil {
		t.Fatalf("commented_at = %q, want a server-stamped RFC3339 (A2): %v", got.CommentedAt, err)
	}
	// W2: carl's route wrote nothing on the agent's side of the split.
	if got.Sightings != 2 || got.Class != "correction" || got.FirstSeen != 1 || got.LastSeen != 5 {
		t.Fatalf("the verdict route moved an agent-owned column: %+v (W2)", got)
	}

	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": ""}); rec.Code != http.StatusOK {
		t.Fatalf("clear: status %d, body %s (A2)", rec.Code, rec.Body.String())
	}
	got, err = st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("the undo did not clear the trio: %+v (A2)", got)
	}

	// A2: an unknown watch item is a 404 — including a LIVE edge that nobody
	// watches (an absent overlay is an absent watch item).
	unwatched := contractSeedEdge(t, st, "zeus", "kraken", "mentions", "a fact nobody watches", 5)
	if rec := contractPostVerdict(t, map[string]any{"id": unwatched, "verdict": "fine"}); rec.Code != http.StatusNotFound {
		t.Fatalf("verdict on an unwatched edge: status %d, want 404 (A2)", rec.Code)
	}
}

// TestContractWatchVerdictRefusesAgentOwnedKeys pins A2's key-rejection half
// AND its one change: agentOwnedWatchKeys gains the add payload's new keys
// (src, dst, predicate, strength), so an agent that posts an endpoint to
// carl's route is told to use the other one rather than having it dropped.
func TestContractWatchVerdictRefusesAgentOwnedKeys(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "the original fact",
		store.WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 1, LastSeen: 2, UpdatedAt: 2})

	for _, key := range []string{"src", "dst", "predicate", "strength"} {
		found := false
		for _, k := range agentOwnedWatchKeys {
			if k == key {
				found = true
			}
		}
		if !found {
			t.Fatalf("agentOwnedWatchKeys is missing %q — A2 says it gains the add payload's new keys", key)
		}
	}
	for _, key := range agentOwnedWatchKeys {
		t.Run(key, func(t *testing.T) {
			rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fine", key: "whatever"})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s: status %d, want 400 (A2/W2), body %s", key, rec.Code, rec.Body.String())
			}
		})
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "" || got.Observation != "the original fact" {
		t.Fatalf("a refused request still wrote something: %+v (A2)", got)
	}
}

// ---------------------------------------------------------------------------
// A3.1 — op add: fold, then promote, then create
// ---------------------------------------------------------------------------

// TestContractWatchItemAddCreates pins A3.1 step 4 end to end: the fact
// sentence becomes a real graph edge between real nodes, with its own done
// episode, and the overlay rides it — the whole point of the move.
func TestContractWatchItemAddCreates(t *testing.T) {
	st := useTestStore(t)

	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation":   "zeus   re-reads\nthe same spec twice",
		"class":         "waste",
		"src":           "zeus",
		"dst":           "job specs",
		"predicate":     "re-reads",
		"strength":      7,
		"evidence":      `{"n":3}`,
		"suggested_fix": "cache the spec",
		"pr_url":        "https://example/pr/2",
	}}))
	if resp.ID == 0 || resp.Folded {
		t.Fatalf("add response = %+v, want a new edge id with folded:false (A3.1.4)", resp)
	}

	wantFact := contractOneLine("zeus   re-reads\nthe same spec twice")
	edge, err := st.FindLiveEdgeByFact(wantFact)
	if err != nil || edge == nil {
		t.Fatalf("no live edge for the added observation: %+v %v (A3.1.4)", edge, err)
	}
	if edge.ID != resp.ID {
		t.Fatalf("response id %d != the created edge %d (A3.1.4)", resp.ID, edge.ID)
	}
	if edge.Fact != wantFact {
		t.Fatalf("fact = %q, want oneLine(observation) = %q (A3.1 step 1)", edge.Fact, wantFact)
	}
	if edge.Predicate != "re-reads" {
		t.Fatalf("predicate = %q, want the given one (A3.1.4)", edge.Predicate)
	}
	if edge.Strength != 7 {
		t.Fatalf("strength = %d, want the given 7 (A3.1.4)", edge.Strength)
	}
	if edge.ValidFrom == 0 {
		t.Fatal("valid_from = 0, want now (A3.1.4)")
	}

	// The endpoints are real nodes, upserted by name.
	nodes, err := st.AllNodes()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, n := range nodes {
		names[strings.ToLower(n.Name)] = true
	}
	if !names["zeus"] || !names["job specs"] {
		t.Fatalf("nodes = %v, want zeus and job specs upserted by name (A3.1.4)", names)
	}

	// Its own episode: kind watchlist, ref watch:api, marked done immediately.
	if edge.EpisodeID == 0 {
		t.Fatal("the created edge has no episode (A3.1.4)")
	}
	ep, err := st.GetEpisode(edge.EpisodeID)
	if err != nil || ep == nil {
		t.Fatalf("episode %d missing: %v (A3.1.4)", edge.EpisodeID, err)
	}
	if ep.Kind != "watchlist" || ep.Ref != "watch:api" || ep.State != "done" {
		t.Fatalf("episode = %+v, want kind watchlist / ref watch:api / state done (A3.1.4)", ep)
	}

	// The overlay: the item's fields, S3.5's defaults, and NO judgement.
	got, err := st.GetWatchItem(resp.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Class != "waste" || got.State != "watching" || got.Sightings != 1 {
		t.Fatalf("overlay defaults = %+v, want class waste / state watching / sightings 1 (A3.1.4, S3.5)", got)
	}
	if got.Evidence != `{"n":3}` || got.SuggestedFix != "cache the spec" || got.PRURL != "https://example/pr/2" {
		t.Fatalf("overlay optional fields did not land: %+v (A3.1.4)", got)
	}
	if got.Verdict != "" || got.Comment != "" || got.CommentedAt != "" {
		t.Fatalf("an agent add wrote carl's trio: %+v (W2)", got)
	}

	// W1: the DREAM view sees it on the frozen wire, keyed by the edge id.
	items := contractListRaw(t, "")
	if len(items) != 1 {
		t.Fatalf("GET returned %d items, want 1 (W1)", len(items))
	}
	var listedID int64
	if err := json.Unmarshal(items[0]["id"], &listedID); err != nil {
		t.Fatal(err)
	}
	if listedID != resp.ID {
		t.Fatalf("listed id = %d, want the edge id %d (A1/W1)", listedID, resp.ID)
	}
}

// TestContractWatchItemAddDefaultsPredicate pins A3.1's one optional endpoint
// field: an omitted predicate defaults to `concerns` rather than refusing.
func TestContractWatchItemAddDefaultsPredicate(t *testing.T) {
	st := useTestStore(t)
	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "the daemon polls too often", "class": "waste",
		"src": "the daemon", "dst": "kraken", "strength": 4,
	}}))
	edge, err := st.FindLiveEdgeByFact("the daemon polls too often")
	if err != nil || edge == nil {
		t.Fatalf("no edge: %+v %v", edge, err)
	}
	if edge.ID != resp.ID {
		t.Fatalf("id %d != edge %d", resp.ID, edge.ID)
	}
	if edge.Predicate != "concerns" {
		t.Fatalf("predicate = %q, want the default `concerns` (A3.1)", edge.Predicate)
	}
}

// TestContractWatchItemAddPromotesExistingEdge pins A3.1 step 3: an
// observation whose wording already exists as an UNWATCHED live fact promotes
// that fact — no second near-duplicate edge, and the existing edge is not
// rewritten (W5).
func TestContractWatchItemAddPromotesExistingEdge(t *testing.T) {
	st := useTestStore(t)
	existing := contractSeedEdge(t, st, "artifacts", "95 columns", "wrap-at", "artifacts wrap at 95 columns", 9)

	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		// Same sentence, different case and spacing — the same normalized fact.
		"observation": "ARTIFACTS   wrap at 95 columns", "class": "correction",
		"src": "somewhere else", "dst": "somewhere else again", "predicate": "ignored", "strength": 2,
		"suggested_fix": "say so in the skill",
	}}))
	if resp.ID != existing {
		t.Fatalf("add returned id %d, want the promoted edge %d (A3.1.3)", resp.ID, existing)
	}
	if resp.Folded {
		t.Fatalf("promote reported folded:true — A3.1.3 responds folded:false (a fold is a repeat SIGHTING)")
	}
	if n := contractLiveEdgeCount(t, st); n != 1 {
		t.Fatalf("%d live edges, want 1 — a promote must not mint a near-duplicate (A3.1.3)", n)
	}
	edges, err := st.GetEdges([]int64{existing})
	if err != nil || len(edges) != 1 {
		t.Fatal(err)
	}
	if edges[0].Predicate != "wrap-at" || edges[0].Strength != 9 || edges[0].Fact != "artifacts wrap at 95 columns" {
		t.Fatalf("the promote rewrote the existing edge: %+v (W5: edges are insert-only)", edges[0])
	}
	got, err := st.GetWatchItem(existing)
	if err != nil {
		t.Fatal(err)
	}
	if got.Class != "correction" || got.SuggestedFix != "say so in the skill" || got.Sightings != 1 || got.State != "watching" {
		t.Fatalf("the promoted overlay = %+v, want the item's fields plus S3.5's defaults (A3.1.3)", got)
	}
}

// TestContractWatchItemAddFoldsRepeat pins A3.1 step 2: re-adding the same
// wording is a repeat SIGHTING — the server-side fold that replaces the dream
// loop's old Find→Bump dance. src/dst/predicate/strength are IGNORED on a fold.
func TestContractWatchItemAddFoldsRepeat(t *testing.T) {
	st := useTestStore(t)
	first := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "zeus re-reads the same spec", "class": "waste",
		"src": "zeus", "dst": "job specs", "predicate": "re-reads", "strength": 6,
		"evidence": "sighting one",
	}}))
	nodesBefore, err := st.AllNodes()
	if err != nil {
		t.Fatal(err)
	}

	second := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "ZEUS   re-reads\tthe same SPEC", "class": "repetition",
		"src": "ignored src", "dst": "ignored dst", "predicate": "ignored", "strength": 10,
		"evidence": "sighting two", "suggested_fix": "cache it", "state": "shipped",
	}}))
	if !second.Folded {
		t.Fatalf("second add = %+v, want folded:true (A3.1.2)", second)
	}
	if second.ID != first.ID {
		t.Fatalf("folded id = %d, want the existing edge %d (A3.1.2)", second.ID, first.ID)
	}
	if n := contractLiveEdgeCount(t, st); n != 1 {
		t.Fatalf("%d live edges, want 1 — a fold adds no edge (A3.1.2)", n)
	}

	got, err := st.GetWatchItem(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	// BumpWatchSighting: +1 and evidence replaced wholesale (S3.7).
	if got.Sightings != 2 {
		t.Fatalf("sightings = %d, want 2 (A3.1.2 → S3.7)", got.Sightings)
	}
	if got.Evidence != "sighting two" {
		t.Fatalf("evidence = %q, want it replaced wholesale (S3.7)", got.Evidence)
	}
	// ...then the carried extras applied via UpdateWatchItem.
	if got.SuggestedFix != "cache it" || got.Class != "repetition" || got.State != "shipped" {
		t.Fatalf("overlay = %+v, want suggested_fix/class/state applied on a fold (A3.1.2)", got)
	}
	// src/dst/predicate/strength IGNORED: no new nodes, edge untouched.
	nodesAfter, err := st.AllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodesAfter) != len(nodesBefore) {
		t.Fatalf("a fold created %d node(s) — A3.1.2 IGNORES src/dst on a fold", len(nodesAfter)-len(nodesBefore))
	}
	edges, err := st.GetEdges([]int64{first.ID})
	if err != nil || len(edges) != 1 {
		t.Fatal(err)
	}
	if edges[0].Predicate != "re-reads" || edges[0].Strength != 6 {
		t.Fatalf("a fold rewrote the edge: %+v — A3.1.2 ignores predicate/strength (W5)", edges[0])
	}
}

// TestContractWatchItemAddFoldBeatsPromote pins A3.1's ORDER: with both a
// watched and an unwatched live edge carrying the same normalized fact, the
// fold branch runs first. Order matters — promoting instead would leave the
// dream loop with two watch items for one observation.
func TestContractWatchItemAddFoldBeatsPromote(t *testing.T) {
	st := useTestStore(t)
	unwatched := contractSeedEdge(t, st, "zeus", "kraken", "mentions", "the same observation twice over", 5)
	watched := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "The Same Observation Twice Over",
		store.WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 1, LastSeen: 50, UpdatedAt: 50})
	if unwatched >= watched {
		t.Fatalf("fixture: expected the unwatched edge to take the lower id (%d, %d)", unwatched, watched)
	}

	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "the same observation twice over", "class": "waste",
		"src": "zeus", "dst": "kraken", "strength": 5,
	}}))
	if !resp.Folded || resp.ID != watched {
		t.Fatalf("add = %+v, want a fold onto the WATCHED edge %d (A3.1: fold before promote)", resp, watched)
	}
	if n := contractLiveEdgeCount(t, st); n != 2 {
		t.Fatalf("%d live edges, want the original 2 (A3.1.2)", n)
	}
	if _, err := st.GetWatchItem(unwatched); err == nil {
		t.Fatalf("the unwatched edge %d gained an overlay — the fold branch must win (A3.1)", unwatched)
	}
}

// ---------------------------------------------------------------------------
// A3.2 / A3.3 — update and remove
// ---------------------------------------------------------------------------

// TestContractWatchItemUpdateIsPartial pins A3.2: the old GET-then-backfill
// dance is gone, so a partial update must leave the counters and carl's
// judgement alone by virtue of the store verb being partial (S3.6).
func TestContractWatchItemUpdateIsPartial(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "an observation carl judged",
		store.WatchItem{Class: "repetition", State: "watching", Sightings: 7, FirstSeen: 1000, LastSeen: 2000, UpdatedAt: 2000})
	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fix", "comment": "agreed"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}

	resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "update", "item": map[string]any{
		"id": id, "suggested_fix": "do X",
	}}))
	if resp.ID != id {
		t.Fatalf("update response id = %d, want %d (A3.4)", resp.ID, id)
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.SuggestedFix != "do X" {
		t.Fatalf("the update did not land: %+v (A3.2)", got)
	}
	if got.Sightings != 7 || got.FirstSeen != 1000 || got.LastSeen != 2000 || got.Class != "repetition" || got.State != "watching" {
		t.Fatalf("a partial update zeroed untouched columns: %+v (A3.2: zero = leave alone)", got)
	}
	if got.Verdict != "fix" || got.Comment != "agreed" || got.CommentedAt == "" {
		t.Fatalf("the agent's update erased carl's judgement: %+v (W2)", got)
	}

	// A3.2: the fact is immutable on this route — the five edge-shaped keys are
	// refused rather than silently dropped.
	for _, key := range []string{"observation", "src", "dst", "predicate", "strength"} {
		t.Run(key, func(t *testing.T) {
			rec := contractPostItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, key: "x",
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s in an update item: status %d, want 400 (A3.2), body %s", key, rec.Code, rec.Body.String())
			}
		})
	}
	after, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Observation != "an observation carl judged" {
		t.Fatalf("a refused update still changed the fact: %q (A3.2, W5)", after.Observation)
	}
}

// TestContractWatchItemRemoveUnwatchesOnly pins A3.3: remove is an UNWATCH.
// The response says so, and the fact itself survives in the graph — still
// live, still searchable, still a promotion target for a later add.
func TestContractWatchItemRemoveUnwatchesOnly(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "a fact that outlives its watch",
		store.WatchItem{Class: "waste", State: "watching", Sightings: 1, FirstSeen: 1, LastSeen: 2, UpdatedAt: 2})

	rec := contractPostItem(t, map[string]any{"op": "remove", "id": id})
	resp := contractDecodeItemResp(t, rec)
	if !resp.Unwatched {
		t.Fatalf("remove response = %s, want {\"ok\":true,\"unwatched\":true} (A3.3)", rec.Body.String())
	}

	if _, err := st.GetWatchItem(id); err == nil {
		t.Fatal("the overlay survived a remove (A3.3)")
	}
	if items := contractListRaw(t, ""); len(items) != 0 {
		t.Fatalf("GET still lists %d item(s) after remove (A3.3)", len(items))
	}
	// The fact itself survives — that is what the response body claims.
	edge, err := st.FindLiveEdgeByFact("a fact that outlives its watch")
	if err != nil || edge == nil || edge.ID != id {
		t.Fatalf("the fact did not survive the unwatch: %+v %v (A3.3)", edge, err)
	}
	if n := contractLiveEdgeCount(t, st); n != 1 {
		t.Fatalf("%d live edges after remove, want 1 (A3.3)", n)
	}
	// ...and a later add promotes it back rather than creating a duplicate.
	again := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "A Fact That Outlives Its Watch", "class": "waste",
		"src": "zeus", "dst": "kraken", "strength": 5,
	}}))
	if again.ID != id || again.Folded {
		t.Fatalf("re-add = %+v, want a promote of edge %d (A3.1.3)", again, id)
	}
	if n := contractLiveEdgeCount(t, st); n != 1 {
		t.Fatalf("%d live edges after the re-add, want 1 (A3.1.3)", n)
	}
}

// ---------------------------------------------------------------------------
// W2 — the ownership split, end to end across both routes
// ---------------------------------------------------------------------------

// TestContractWatchOwnershipSplitSurvivesTheMove pins W2 whole: no
// agent-reachable path writes verdict/comment/commented_at, no carl-reachable
// path writes anything else, and both routes still refuse the other side's
// keys at every level they did before.
func TestContractWatchOwnershipSplitSurvivesTheMove(t *testing.T) {
	st := useTestStore(t)
	id := contractSeedWatched(t, st, "zeus", "kraken", "concerns", "the contested observation",
		store.WatchItem{Class: "feedback", State: "watching", Sightings: 4, FirstSeen: 10, LastSeen: 20, UpdatedAt: 20})
	if rec := contractPostVerdict(t, map[string]any{"id": id, "verdict": "fine", "comment": "leave it"}); rec.Code != http.StatusOK {
		t.Fatalf("seed verdict: status %d", rec.Code)
	}

	// The agent's route refuses carl's keys at BOTH levels (A3 preamble).
	for _, key := range carlOwnedWatchKeys {
		t.Run("root/"+key, func(t *testing.T) {
			rec := contractPostItem(t, map[string]any{
				"op": "update", key: "x", "item": map[string]any{"id": id, "class": "feedback"},
			})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (W2), body %s", rec.Code, rec.Body.String())
			}
		})
		t.Run("item/"+key, func(t *testing.T) {
			rec := contractPostItem(t, map[string]any{"op": "update", "item": map[string]any{
				"id": id, "class": "feedback", key: "x",
			}})
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (W2), body %s", rec.Code, rec.Body.String())
			}
		})
	}

	// A legal agent write (fold via add) still cannot move carl's trio.
	if resp := contractDecodeItemResp(t, contractPostItem(t, map[string]any{"op": "add", "item": map[string]any{
		"observation": "THE contested   observation", "class": "waste",
		"src": "zeus", "dst": "kraken", "strength": 5, "evidence": "again",
	}})); !resp.Folded || resp.ID != id {
		t.Fatalf("fold = %+v, want a fold onto %d", resp, id)
	}
	got, err := st.GetWatchItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verdict != "fine" || got.Comment != "leave it" || got.CommentedAt == "" {
		t.Fatalf("an agent add moved carl's trio: %+v (W2)", got)
	}
	if got.Sightings != 5 || got.Evidence != "again" || got.Class != "waste" {
		t.Fatalf("the fold did not apply the agent's own fields: %+v (A3.1.2)", got)
	}
}
