package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/store"
)

// graph_test.go — behavior, not wire shape. The shapes are pinned in
// contract_test.go beside ios/API.md's transcription; this file covers what
// the handlers REFUSE and why, since every refusal here exists to stop a
// write that would have been silently wrong.

// postGraph is the one-line request helper every case below uses. Body is
// raw bytes rather than a value so a case can send deliberately malformed
// JSON — which is the point of half of them.
func postGraph(h http.HandlerFunc, path string, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body))))
	return rec
}

func bodyError(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("response is not an {\"error\":...} object: %v (body %s)", err, rec.Body.String())
	}
	return e.Error
}

// TestGraphAddRefusesMalformedBodies covers the decode gate and every
// validation branch. Each case is a body that would have written something
// WRONG rather than nothing — a fabricated confidence, a fact with no
// searchable sentence, an invalidation that claims the fact never stopped
// holding — so each must be a 400 that names the offending item.
func TestGraphAddRefusesMalformedBodies(t *testing.T) {
	useTestStore(t)

	cases := []struct {
		name string
		body string
		want string // a substring of the refusal, so the reason is pinned, not just the status
	}{
		{"not json", `{`, "bad json"},
		{
			// The DisallowUnknownFields case: a key matching no field is a
			// refusal, not a request that quietly means something else.
			"unknown top-level field",
			`{"episode_id":1,"nodez":[]}`,
			"unrecognized field",
		},
		{"empty request", `{}`, "nothing to add"},
		{"blank node name", `{"nodes":[{"name":"  "}]}`, "nodes[0]"},
		{"blank alias", `{"nodes":[{"name":"a","aliases":["ok",""]}]}`, "nodes[0].aliases[1]"},
		{"blank src", `{"edges":[{"src":"","dst":"b","predicate":"p","fact":"f","strength":5}]}`, "edges[0]: blank src"},
		{"blank dst", `{"edges":[{"src":"a","dst":"","predicate":"p","fact":"f","strength":5}]}`, "edges[0]: blank dst"},
		{"blank predicate", `{"edges":[{"src":"a","dst":"b","predicate":"","fact":"f","strength":5}]}`, "blank predicate"},
		{"blank fact", `{"edges":[{"src":"a","dst":"b","predicate":"p","fact":"","strength":5}]}`, "blank fact"},
		{
			// An ABSENT strength decodes to 0, which is out of range — the
			// same refusal an explicit 0 gets, deliberately: an omitted
			// confidence is an unstated one, not a default.
			"absent strength",
			`{"edges":[{"src":"a","dst":"b","predicate":"p","fact":"f"}]}`,
			"strength 0 out of range",
		},
		{"strength too high", `{"edges":[{"src":"a","dst":"b","predicate":"p","fact":"f","strength":11}]}`, "strength 11 out of range"},
		{"invalidate without edge_id", `{"invalidate":[{"valid_until":1}]}`, "invalidate[0]: missing edge_id"},
		{
			// 0 means "still true" on valid_until everywhere else in the
			// schema, so accepting it would write a row asserting both that
			// kraken unlearned the fact and that it never stopped holding.
			"invalidate without valid_until",
			`{"invalidate":[{"edge_id":3}]}`,
			"missing valid_until",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := postGraph(graphAddHandler, "/api/graph/add", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := bodyError(t, rec); !strings.Contains(got, tc.want) {
				t.Errorf("error = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

// TestGraphAddValidatesBeforeWriting is the load-bearing one: the three
// lists are written through separate store calls with no enclosing
// transaction, so a body whose LAST item is invalid must write NOTHING. If
// validation ever moves inline with the writes, this fails with a node
// already in the table.
func TestGraphAddValidatesBeforeWriting(t *testing.T) {
	st := useTestStore(t)

	body := `{"nodes":[{"name":"kraken","type":"system"}],
	          "edges":[{"src":"kraken","dst":"zeus","predicate":"spawns","fact":"f","strength":5}],
	          "invalidate":[{"edge_id":99,"valid_until":0}]}`
	rec := postGraph(graphAddHandler, "/api/graph/add", body)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
	nodes, err := st.AllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 0 {
		t.Fatalf("a refused add wrote %d node(s) — validation must run before any write", len(nodes))
	}
	edges, err := st.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("a refused add wrote %d edge(s)", len(edges))
	}
}

// TestGraphAddCreatesBareEndpointNodes pins the documented resolve-by-name
// rule: an edge endpoint nobody declared as a node still becomes one, so the
// FACT is kept rather than dropped over a missing entity declaration.
func TestGraphAddCreatesBareEndpointNodes(t *testing.T) {
	st := useTestStore(t)

	body := `{"edges":[{"src":"kraken","dst":"zeus","predicate":"spawns","fact":"kraken spawns zeus","strength":7}]}`
	rec := postGraph(graphAddHandler, "/api/graph/add", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	nodes, err := st.AllNodes()
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2 bare endpoint nodes", len(nodes))
	}
}

// TestGraphAddReportsMergeVsCreate covers the `added` flag, which is the only
// way a caller learns its "new" entity was already known under an alias or
// that its fact was a repeat sighting rather than a new edge.
func TestGraphAddReportsMergeVsCreate(t *testing.T) {
	useTestStore(t)

	first := `{"nodes":[{"name":"kraken","description":"the CLI","aliases":["k"]}],
	           "edges":[{"src":"kraken","dst":"zeus","predicate":"spawns","fact":"kraken spawns zeus","strength":7}]}`
	rec := postGraph(graphAddHandler, "/api/graph/add", first)
	if rec.Code != http.StatusOK {
		t.Fatalf("first add: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nodes []graphAddNodeResult `json:"nodes"`
		Edges []graphAddEdgeResult `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Nodes) != 1 || !resp.Nodes[0].Added {
		t.Fatalf("first add: nodes = %+v, want one with added=true", resp.Nodes)
	}
	if len(resp.Edges) != 1 || !resp.Edges[0].Added {
		t.Fatalf("first add: edges = %+v, want one with added=true", resp.Edges)
	}

	// The SAME entity named by its alias, and the same fact again. Both must
	// come back added=false, and the node id must be the one from the first
	// add — that is what "merged" means here.
	second := `{"nodes":[{"name":"k"}],
	            "edges":[{"src":"kraken","dst":"zeus","predicate":"spawns","fact":"kraken spawns zeus","strength":9}]}`
	rec = postGraph(graphAddHandler, "/api/graph/add", second)
	if rec.Code != http.StatusOK {
		t.Fatalf("second add: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp2 struct {
		Nodes []graphAddNodeResult `json:"nodes"`
		Edges []graphAddEdgeResult `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp2); err != nil {
		t.Fatal(err)
	}
	if len(resp2.Nodes) != 1 || resp2.Nodes[0].Added {
		t.Fatalf("second add: nodes = %+v, want one with added=false (alias merge)", resp2.Nodes)
	}
	if resp2.Nodes[0].ID != resp.Nodes[0].ID {
		t.Errorf("alias resolved to node %d, want %d", resp2.Nodes[0].ID, resp.Nodes[0].ID)
	}
	if len(resp2.Edges) != 1 || resp2.Edges[0].Added {
		t.Fatalf("second add: edges = %+v, want one with added=false (repeat sighting)", resp2.Edges)
	}
}

// TestGraphAddAcceptsTheSkillPayload is a contract test against
// .claude/skills/graph/SKILL.md rather than against this package: that file
// is checked in and EMBEDDED into every cell worktree, so the extraction
// agent sends what it says and cannot be told otherwise at runtime. Combined
// with DisallowUnknownFields, a field the agent will send that this route
// does not declare is a 400 on EVERY write — which is exactly how
// `valid_until` on an edge (as opposed to on an invalidation) was caught.
//
// Two payloads, because the contract has two sources and they are not the
// same document:
//
//   - "skill literal" is copied verbatim from SKILL.md's "### 4. Write"
//     block as it stands today. Its edges carry NO world-time fields. This
//     is the drift guard on that file: if the block changes, this stops
//     matching it and the two must be reconciled in the same commit.
//   - "with world time" is that same payload plus `valid_from`/`valid_until`
//     on the edge — the fields hephaestus-jobs named as part of the add
//     contract even though the skill's example omits them. This is the
//     regression guard for the 400 above, and it is the case the skill's
//     example does NOT cover, which is precisely why it needs its own
//     assertion rather than being folded into the literal.
//
// Keeping them apart is the point. Editing the literal to include the
// world-time fields would have made this test pass while silently ceasing to
// mirror any real document.
func TestGraphAddAcceptsTheSkillPayload(t *testing.T) {
	// skillLiteral is SKILL.md's "### 4. Write" example with only its
	// `invalidate` edge_id substituted, since that has to name a row that
	// actually exists for the third list to exercise its real path.
	skillLiteral := func(victim int64) string {
		return `{
		  "nodes": [
		    {"name": "tokensPass", "type": "component",
		     "description": "The daemon pass that keeps each job's job_tokens row current.",
		     "aliases": ["token pass"]}
		  ],
		  "edges": [
		    {"src": "tokensPass", "dst": "kraken serve", "predicate": "runs-inside",
		     "fact": "tokensPass runs inside kraken serve's daemon tick and is skipped under --mirror.",
		     "strength": 8}
		  ],
		  "invalidate": [
		    {"edge_id": ` + strconv.FormatInt(victim, 10) + `, "valid_until": 1753900000000}
		  ]
		}`
	}
	// withWorldTime adds the two fields the skill's example omits.
	withWorldTime := func(victim int64) string {
		return `{
		  "nodes": [
		    {"name": "tokensPass", "type": "component",
		     "description": "The daemon pass that keeps each job's job_tokens row current.",
		     "aliases": ["token pass"]}
		  ],
		  "edges": [
		    {"src": "tokensPass", "dst": "kraken serve", "predicate": "runs-inside",
		     "fact": "tokensPass runs inside kraken serve's daemon tick and is skipped under --mirror.",
		     "strength": 8, "valid_from": 0, "valid_until": 0}
		  ],
		  "invalidate": [
		    {"edge_id": ` + strconv.FormatInt(victim, 10) + `, "valid_until": 1753900000000}
		  ]
		}`
	}

	for _, tc := range []struct {
		name string
		body func(int64) string
	}{
		{"skill literal", skillLiteral},
		{"with world time", withWorldTime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A store per case: the payloads overlap, and a shared one would
			// make the second case a repeat-sighting rather than the fresh
			// write it is meant to be.
			st := useTestStore(t)
			src, err := st.UpsertNode(store.GraphNode{Name: "old-src"})
			if err != nil {
				t.Fatal(err)
			}
			dst, err := st.UpsertNode(store.GraphNode{Name: "old-dst"})
			if err != nil {
				t.Fatal(err)
			}
			victim, _, err := st.InsertEdge(store.GraphEdge{
				SrcID: src, DstID: dst, Predicate: "was", Fact: "a fact that stopped holding", Strength: 5,
			})
			if err != nil {
				t.Fatal(err)
			}
			rec := postGraph(graphAddHandler, "/api/graph/add", tc.body(victim))
			if rec.Code != http.StatusOK {
				t.Fatalf("the /graph add contract (%s) was refused: status %d, body %s",
					tc.name, rec.Code, rec.Body.String())
			}
		})
	}
}

// TestGraphAddEdgeValidUntilIsWorldTimeOnly pins the asymmetry between an
// edge's own `valid_until` and an `invalidate` entry's. Both write world
// time; only invalidation stamps SYSTEM time. An episode saying "we used X
// until March" states one edge that kraken still believes (expired_at 0) —
// it is not a fact kraken unlearned, and conflating the two would make the
// viewer strike through history it never retracted.
func TestGraphAddEdgeValidUntilIsWorldTimeOnly(t *testing.T) {
	st := useTestStore(t)

	body := `{"edges":[{"src":"a","dst":"b","predicate":"used","fact":"a used b until March",
	           "strength":6,"valid_from":1700000000000,"valid_until":1753900000000}]}`
	rec := postGraph(graphAddHandler, "/api/graph/add", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	edges, err := st.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want 1", len(edges))
	}
	e := edges[0]
	if e.ValidFrom != 1700000000000 || e.ValidUntil != 1753900000000 {
		t.Errorf("world time = [%d, %d], want [1700000000000, 1753900000000]", e.ValidFrom, e.ValidUntil)
	}
	if e.ExpiredAt != 0 {
		t.Errorf("expired_at = %d, want 0 — a closed WORLD-time window is not an unlearned fact", e.ExpiredAt)
	}
}

// TestGraphAddRepeatBumpsOccurrencesAndTakesMaxStrength pins the first of the
// two promises .claude/skills/graph/SKILL.md makes to the extraction agent:
// a repeated (src, dst, predicate) on a LIVE edge is a corroborating sighting,
// not a duplicate row. The skill leans on this hard — it is what `graphContext`
// renders as `seen Nx`, and it is why the skill tells the agent to state a
// fact it has seen again rather than skip it as already present.
//
// The behaviour is store.InsertEdge's, but it is pinned HERE, through the
// route, because the promise is made to a caller that only ever reaches it
// through this route.
func TestGraphAddRepeatBumpsOccurrencesAndTakesMaxStrength(t *testing.T) {
	st := useTestStore(t)

	first := `{"edges":[{"src":"a","dst":"b","predicate":"p","fact":"a p b","strength":4}]}`
	if rec := postGraph(graphAddHandler, "/api/graph/add", first); rec.Code != http.StatusOK {
		t.Fatalf("first add: status %d, body %s", rec.Code, rec.Body.String())
	}
	// A second sighting, more confident. Then a third, LESS confident: max,
	// not last-wins, so a weak restatement cannot erode a strong one.
	second := `{"edges":[{"src":"a","dst":"b","predicate":"p","fact":"a p b","strength":9}]}`
	if rec := postGraph(graphAddHandler, "/api/graph/add", second); rec.Code != http.StatusOK {
		t.Fatalf("second add: status %d, body %s", rec.Code, rec.Body.String())
	}
	third := `{"edges":[{"src":"a","dst":"b","predicate":"p","fact":"a p b","strength":2}]}`
	if rec := postGraph(graphAddHandler, "/api/graph/add", third); rec.Code != http.StatusOK {
		t.Fatalf("third add: status %d, body %s", rec.Code, rec.Body.String())
	}

	edges, err := st.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edge rows, want 1 — a repeat must fold in, not duplicate", len(edges))
	}
	if edges[0].Occurrences != 3 {
		t.Errorf("occurrences = %d, want 3 — this is what graphContext renders as `seen Nx`", edges[0].Occurrences)
	}
	if edges[0].Strength != 9 {
		t.Errorf("strength = %d, want the max (9), not the last (2)", edges[0].Strength)
	}
}

// TestGraphInvalidateClosesWindowAndDeletesNothing pins the second promise:
// invalidation closes the validity window and the row survives. Point-in-time
// queries are the entire reason this graph is bi-temporal, so a delete here
// would silently discard the history the design exists to keep.
func TestGraphInvalidateClosesWindowAndDeletesNothing(t *testing.T) {
	st := useTestStore(t)

	add := `{"edges":[{"src":"a","dst":"b","predicate":"p","fact":"a p b","strength":5}]}`
	rec := postGraph(graphAddHandler, "/api/graph/add", add)
	if rec.Code != http.StatusOK {
		t.Fatalf("add: status %d, body %s", rec.Code, rec.Body.String())
	}
	var addResp struct {
		Edges []graphAddEdgeResult `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &addResp); err != nil {
		t.Fatal(err)
	}
	id := addResp.Edges[0].ID

	inv := `{"invalidate":[{"edge_id":` + strconv.FormatInt(id, 10) + `,"valid_until":1753900000000}]}`
	rec = postGraph(graphAddHandler, "/api/graph/add", inv)
	if rec.Code != http.StatusOK {
		t.Fatalf("invalidate: status %d, body %s", rec.Code, rec.Body.String())
	}
	var invResp struct {
		Invalidated []int64 `json:"invalidated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &invResp); err != nil {
		t.Fatal(err)
	}
	if len(invResp.Invalidated) != 1 || invResp.Invalidated[0] != id {
		t.Errorf("invalidated = %v, want [%d]", invResp.Invalidated, id)
	}

	edges, err := st.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("got %d edges, want the invalidated row to SURVIVE", len(edges))
	}
	if edges[0].ValidUntil != 1753900000000 {
		t.Errorf("valid_until = %d, want the world-time instant sent", edges[0].ValidUntil)
	}
	if edges[0].ExpiredAt == 0 {
		t.Error("expired_at = 0 — invalidation must stamp system time")
	}

	// Invalidating the same edge twice is a SUCCESS — store.InvalidateEdge
	// treats an already-expired edge as a no-op, which is what makes this
	// handler's "idempotent under a retry" claim true: an agent re-running a
	// partially-applied batch must not 500 on work that already landed. The
	// first invalidation's window survives the retry untouched.
	rec = postGraph(graphAddHandler, "/api/graph/add", inv)
	if rec.Code != http.StatusOK {
		t.Errorf("re-invalidate: status %d, want 200 (idempotent retry), body %s", rec.Code, rec.Body.String())
	}
	edges, err = st.AllEdges()
	if err != nil {
		t.Fatal(err)
	}
	if edges[0].ValidUntil != 1753900000000 {
		t.Errorf("retry rewrote valid_until to %d", edges[0].ValidUntil)
	}
	// An id with NO row is still refused — a typo must not be acked as done.
	badInv := `{"invalidate":[{"edge_id":99999,"valid_until":1753900000000}]}`
	rec = postGraph(graphAddHandler, "/api/graph/add", badInv)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("invalidate of a nonexistent edge: status %d, want 500", rec.Code)
	}
}

// TestGraphSearchReturnsNodesForResolution pins that a search answers with
// ENTITIES as well as facts. The skill's canonicalization step uses
// `k graph search "<name>"` to decide whether an incoming name is something
// already known; facts alone answer that only indirectly.
func TestGraphSearchReturnsNodesForResolution(t *testing.T) {
	useTestStore(t)
	t.Setenv("OPENAI_API_KEY", "") // BM25-only path — no network in a test

	add := `{"nodes":[{"name":"tokensPass","type":"component","description":"the daemon pass"}],
	         "edges":[{"src":"tokensPass","dst":"kraken serve","predicate":"runs-inside",
	                   "fact":"tokensPass runs inside kraken serve's daemon tick","strength":8}]}`
	if rec := postGraph(graphAddHandler, "/api/graph/add", add); rec.Code != http.StatusOK {
		t.Fatalf("add: status %d, body %s", rec.Code, rec.Body.String())
	}

	rec := postGraph(graphSearchHandler, "/api/graph/search", `{"query":"tokensPass daemon tick"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("search: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nodes   []webGraphNode `json:"nodes"`
		Facts   []webGraphFact `json:"facts"`
		Triples string         `json:"triples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Facts) == 0 {
		t.Fatal("no facts matched a query lifted from the stored fact sentence")
	}
	if len(resp.Nodes) == 0 {
		t.Fatal("no nodes returned — canonicalization cannot resolve a name against facts alone")
	}
	// Node ids ascend, which is what makes the response deterministic for a
	// fixed corpus.
	for i := 1; i < len(resp.Nodes); i++ {
		if resp.Nodes[i-1].ID >= resp.Nodes[i].ID {
			t.Errorf("nodes not sorted by id: %d then %d", resp.Nodes[i-1].ID, resp.Nodes[i].ID)
		}
	}
	// Endpoints render as NAMES, never ids — the whole reason webGraphFact
	// differs from webGraphEdge.
	if resp.Facts[0].Src == "" || strings.HasPrefix(resp.Facts[0].Src, "#") {
		t.Errorf("fact src = %q, want a resolved node name", resp.Facts[0].Src)
	}
}

// TestGraphNodeRefusals covers the inspector read's argument gate. A missing
// node is a 404 rather than a 200 with a null node: the caller named one
// specific node, so "there isn't one" is the answer.
func TestGraphNodeRefusals(t *testing.T) {
	useTestStore(t)

	for _, tc := range []struct {
		query string
		want  int
	}{
		{"", http.StatusBadRequest},
		{"?id=", http.StatusBadRequest},
		{"?id=abc", http.StatusBadRequest},
		{"?id=0", http.StatusBadRequest},
		{"?id=-3", http.StatusBadRequest},
		{"?id=999", http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		graphNodeHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/node"+tc.query, nil))
		if rec.Code != tc.want {
			t.Errorf("id%q: status = %d, want %d (body %s)", tc.query, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// TestGraphNodeShowsInvalidatedEdges is why the inspector reads AllEdges and
// not NeighborEdges: NeighborEdges is live-only, and the whole point of the
// panel is to show a retired fact beside the one that replaced it.
func TestGraphNodeShowsInvalidatedEdges(t *testing.T) {
	st := useTestStore(t)

	src, err := st.UpsertNode(store.GraphNode{Name: "kraken"})
	if err != nil {
		t.Fatal(err)
	}
	dst, err := st.UpsertNode(store.GraphNode{Name: "zeus"})
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := st.InsertEdge(store.GraphEdge{
		SrcID: src, DstID: dst, Predicate: "spawns", Fact: "kraken spawns zeus", Strength: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InvalidateEdge(id, 1769040000000); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	graphNodeHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/node?id=1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Edges []webGraphEdge `json:"edges"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Edges) != 1 {
		t.Fatalf("got %d edges, want the invalidated one", len(resp.Edges))
	}
	if resp.Edges[0].ExpiredAt == 0 {
		t.Errorf("expired_at = 0 on an invalidated edge — the viewer cannot strike it through")
	}
	if resp.Edges[0].ValidUntil != 1769040000000 {
		t.Errorf("valid_until = %d, want the world-time instant passed to InvalidateEdge", resp.Edges[0].ValidUntil)
	}
}

// TestGraphEpisodeDoneRefusesUnknownID pins the 404. store.MarkEpisodeDone's
// UPDATE matches nothing and reports no error for an unknown id, so without
// the handler's lookup a typo would be acknowledged as done while the real
// episode sat pending forever.
func TestGraphEpisodeDoneRefusesUnknownID(t *testing.T) {
	useTestStore(t)

	rec := postGraph(graphEpisodeDoneHandler, "/api/graph/episode-done", `{"id":404}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	rec = postGraph(graphEpisodeDoneHandler, "/api/graph/episode-done", `{"id":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("id 0: status = %d, want 400", rec.Code)
	}
	rec = postGraph(graphEpisodeDoneHandler, "/api/graph/episode-done", `{"episode":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: status = %d, want 400", rec.Code)
	}
	if got := bodyError(t, rec); !strings.Contains(got, "unrecognized field") {
		t.Errorf("error = %q, want the unknown-field refusal", got)
	}
}

// TestGraphSearchRefusals covers the query gate. A blank query is a 400
// rather than "every fact in the corpus".
func TestGraphSearchRefusals(t *testing.T) {
	useTestStore(t)

	for _, tc := range []struct {
		name, body string
	}{
		{"blank query", `{"query":"   "}`},
		{"no query", `{"limit":5}`},
		{"unknown field", `{"query":"x","depth":2}`},
		{"not json", `nope`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := postGraph(graphSearchHandler, "/api/graph/search", tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestGraphPendingLimit covers the limit gate: a bad value is a 400 (a typo
// must not silently fetch the default), an over-large one is CAPPED rather
// than refused, since a caller asking for plenty should get plenty.
func TestGraphPendingLimit(t *testing.T) {
	st := useTestStore(t)
	for i := 0; i < 3; i++ {
		if _, _, err := st.AddEpisode(store.Episode{
			Kind: "job", Ref: "r", Content: "c", Hash: string(rune('a' + i)), TS: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, bad := range []string{"?limit=0", "?limit=-1", "?limit=abc"} {
		rec := httptest.NewRecorder()
		graphPendingHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/pending"+bad, nil))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", bad, rec.Code)
		}
	}

	rec := httptest.NewRecorder()
	graphPendingHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/pending?limit=100000", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("oversized limit: status = %d, want it capped and served (body %s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Episodes []webGraphEpisode `json:"episodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Episodes) != 3 {
		t.Errorf("got %d episodes, want all 3", len(resp.Episodes))
	}

	rec = httptest.NewRecorder()
	graphPendingHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph/pending?limit=2", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Episodes) != 2 {
		t.Errorf("limit=2 returned %d episodes", len(resp.Episodes))
	}
}

// TestGraphMethodGuards: every route answers 405 to the wrong verb rather
// than falling through to a handler that would read an absent body.
func TestGraphMethodGuards(t *testing.T) {
	useTestStore(t)

	gets := []http.HandlerFunc{graphHandler, graphStatsHandler, graphNodeHandler, graphPendingHandler}
	for i, h := range gets {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, "/x", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET handler %d: POST status = %d, want 405", i, rec.Code)
		}
	}
	posts := []http.HandlerFunc{graphSearchHandler, graphAddHandler, graphEpisodeDoneHandler}
	for i, h := range posts {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST handler %d: GET status = %d, want 405", i, rec.Code)
		}
	}
}

// TestGraphNodePositionsJoinFromLayout pins the documented x/y contract: a
// node with a cached position serves it, one without serves 0,0 — the state
// every node is in until the layout pass has run, which the client places
// itself rather than reading as data.
func TestGraphNodePositionsJoinFromLayout(t *testing.T) {
	st := useTestStore(t)

	positioned, err := st.UpsertNode(store.GraphNode{Name: "kraken"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertNode(store.GraphNode{Name: "zeus"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetLayout([]store.LayoutPos{{NodeID: positioned, X: -12.5, Y: 88.25, Rev: 1}}); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	graphHandler(rec, httptest.NewRequest(http.MethodGet, "/api/graph", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Nodes []webGraphNode `json:"nodes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(resp.Nodes))
	}
	byID := map[int64]webGraphNode{}
	for _, n := range resp.Nodes {
		byID[n.ID] = n
	}
	if got := byID[positioned]; got.X != -12.5 || got.Y != 88.25 {
		t.Errorf("positioned node = (%v, %v), want (-12.5, 88.25)", got.X, got.Y)
	}
	for id, n := range byID {
		if id == positioned {
			continue
		}
		if n.X != 0 || n.Y != 0 {
			t.Errorf("unpositioned node = (%v, %v), want the documented 0,0", n.X, n.Y)
		}
	}
}
