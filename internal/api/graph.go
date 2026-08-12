package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// graph.go — the knowledge graph's HTTP surface: the viewer's read, the
// inspector's per-node read, the two dereference reads behind `k graph get`,
// the retrieval read, and the extraction agent's write.
//
// Every route here is a NEW PATH rather than a key added to, retyped in, or
// removed from an existing shape, so none of them bumps `apiContract`. That is the bump
// policy stated in ios/API.md's header and pinned by
// TestAPIContractUnchanged in contract_test.go, both read this session: a
// client that predates these routes never calls them and decodes nothing
// differently because they exist.
//
// The DTOs below are this package's own, per the house rule that the api
// layer owns its wire shapes and store types never serialize directly
// (store/graph.go carries no json tags at all — checked, not assumed). That
// separation is doing real work here rather than being ceremony: a
// store.GraphNode/GraphEdge carries an `Embedding []byte`, and marshaling
// one directly would put a base64 kilobyte per row on a payload the viewer
// polls.
//
// Split of writes vs reads under --mirror: /api/graph/add and
// /api/graph/episode-done are mirror-gated at registration (register.go),
// the same "a mirror board is read-only, full stop" policy questions,
// feedback, watchlist and job/load follow. The six GET reads plus
// POST /api/graph/search stay open — a read is a read, and search is a read
// despite its verb (it is a POST only because a query string is user text
// that has no business in a URL).

// --- viewer payload: GET /api/graph ---

// webGraphNode is one entity for the viewer. x/y are the SERVER-computed
// layout position (store.Layout()); a node with no cached position yet
// serves 0,0 and the client places it — spelled out in ios/API.md so a
// cluster at the origin reads as "layout hasn't run for these" rather than
// as data.
//
// Embedding is deliberately absent: it is a kilobyte of float32 per row that
// no client can do anything with.
type webGraphNode struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	X           float64  `json:"x"`
	Y           float64  `json:"y"`
	Aliases     []string `json:"aliases,omitempty"`
}

// webGraphEdge is one fact for the viewer. Both time axes ride: valid_until
// is WORLD time (0 = still true) and expired_at is SYSTEM time (0 = live),
// which is what lets the viewer strike an invalidated fact through rather
// than hide it. Neither is omitempty — 0 is a real, meaningful answer on
// both, and an absent key would be indistinguishable from it.
type webGraphEdge struct {
	ID          int64  `json:"id"`
	Src         int64  `json:"src"`
	Dst         int64  `json:"dst"`
	Predicate   string `json:"predicate"`
	Fact        string `json:"fact"`
	Strength    int64  `json:"strength"`
	Occurrences int64  `json:"occurrences"`
	ValidUntil  int64  `json:"valid_until"`
	ExpiredAt   int64  `json:"expired_at"`
	Episode     int64  `json:"episode"`
}

// webGraphStats is the growth curve both clients and `k graph stats` read.
//
// Its three multi-word keys are camelCase where the rest of this package is
// snake_case. That is not an accident and not a house-style lapse: these
// exact key spellings are the shape the graph viewer slice is being built
// against in parallel, so changing them here to match the surrounding file
// would break the client rather than tidy anything. Documented in
// ios/API.md so the inconsistency is a recorded decision rather than a
// surprise to the next reader.
type webGraphStats struct {
	Nodes             int64 `json:"nodes"`
	Edges             int64 `json:"edges"`
	LiveEdges         int64 `json:"live_edges"`
	Episodes          int64 `json:"episodes"`
	Pending           int64 `json:"pending"`
	MissingEmbeddings int64 `json:"missing_embeddings"`
}

// webGraphEpisode is one pending provenance unit handed to the extraction
// agent. `content` is the whole payload of this route — the reduced text the
// agent reads — so it rides inline; a URL per episode would turn one drain
// of the queue into N round trips, the same reasoning dream.go's report body
// follows.
//
// `hash` and `state` are deliberately absent: hash is the store's internal
// dedupe key and state is 'pending' for every row this route can serve.
type webGraphEpisode struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Ref     string `json:"ref"`
	Content string `json:"content"`
	TS      int64  `json:"ts"`
}

// webGraphFact is one retrieved fact from POST /api/graph/search. Endpoints
// are NAMES here, not ids: the caller of a search is a human reading a CLI
// or an agent reading facts, and neither can resolve an integer.
//
// There is no `score` key. core.GraphSearch's result carries a score map,
// but ORDER already expresses the ranking, and publishing a number whose
// scale is an implementation detail of the fusion would invite clients to
// threshold on it — the same argument webJobLoad makes for not putting the
// derived roi on the wire.
//
// `weak` is the one exception to that, and it is not a score: it is the
// BOOLEAN the spawn path's gate reaches — core.RelevantEdge's verdict, waived
// for facts touching a node the query named (see the anchor note at the split
// below) — published
// because the two readers of a search want opposite things from it. An agent
// wants to know which facts to actually rely on; a human canonicalizing an
// entity name wants to see the near-misses too. Dropping the weak ones would
// serve the first and break the second, so every fact still rides and each
// one says which it is. Not omitempty: `false` is the answer that matters
// most, and an absent key would be indistinguishable from an older server.
type webGraphFact struct {
	ID          int64  `json:"id"`
	Src         string `json:"src"`
	Dst         string `json:"dst"`
	Predicate   string `json:"predicate"`
	Fact        string `json:"fact"`
	Strength    int64  `json:"strength"`
	Occurrences int64  `json:"occurrences"`
	ValidUntil  int64  `json:"valid_until"`
	Weak        bool   `json:"weak"`
}

// graphStore is the one place every handler here reaches for the store, so
// "no store wired" is one sentence rather than six. A nil store is a 500
// with a reason, never a nil-pointer panic in a handler.
func graphStore(w http.ResponseWriter) (*store.Store, bool) {
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return nil, false
	}
	return st, true
}

func webGraphEdgeFrom(e store.GraphEdge) webGraphEdge {
	return webGraphEdge{
		ID: e.ID, Src: e.SrcID, Dst: e.DstID,
		Predicate: e.Predicate, Fact: e.Fact,
		Strength: e.Strength, Occurrences: e.Occurrences,
		ValidUntil: e.ValidUntil, ExpiredAt: e.ExpiredAt, Episode: e.EpisodeID,
	}
}

// graphHandler serves GET /api/graph — the whole viewer payload in one
// response: every node with its cached position, every edge (live AND
// invalidated, since the viewer draws history struck through), and the
// stats block.
//
// One response rather than paged reads because the client is a canvas that
// needs the whole topology to lay out hit-testing and culling, and because a
// paged graph read has no natural page boundary — an edge's endpoints can
// live on any page.
func graphHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	nodes, err := st.NodesLite()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	edges, err := st.EdgesLite()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	pos, err := st.Layout()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	stats, err := st.GetGraphStats()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Aliases ARE joined here, in one bulk query (never per node). The
	// viewer only draws canonical names, but this route is also the nightly
	// graph ingest's ONE inventory pull, and an alias-blind inventory made
	// its canonicalization re-derive the same same-referent merges night
	// after night — registered aliases resolved correctly server-side while
	// the lead's match table kept printing their names as NEW.
	aliases, err := st.AllNodeAliases()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Positions are joined in memory from one Layout() sweep rather than a
	// per-node query: this route already reads every node, and the layout
	// table has at most one row per node.
	xy := make(map[int64]store.LayoutPos, len(pos))
	for _, p := range pos {
		xy[p.NodeID] = p
	}

	outNodes := make([]webGraphNode, 0, len(nodes))
	for _, n := range nodes {
		p := xy[n.ID] // absent = zero value = 0,0, which is the documented contract
		outNodes = append(outNodes, webGraphNode{
			ID: n.ID, Name: n.Name, Type: n.Type, Description: n.Description,
			X: p.X, Y: p.Y, Aliases: aliases[n.ID],
		})
	}
	outEdges := make([]webGraphEdge, 0, len(edges))
	for _, e := range edges {
		outEdges = append(outEdges, webGraphEdgeFrom(e))
	}

	webJSON(w, http.StatusOK, map[string]any{
		"nodes": outNodes,
		"edges": outEdges,
		"stats": webGraphStatsFrom(stats),
	})
}

func webGraphStatsFrom(s store.GraphStats) webGraphStats {
	return webGraphStats{
		Nodes: s.Nodes, Edges: s.Edges, LiveEdges: s.LiveEdges,
		Episodes: s.Episodes, Pending: s.PendingEpisodes,
		MissingEmbeddings: s.MissingEmbeddings,
	}
}

// graphStatsHandler serves GET /api/graph/stats — the same stats block
// /api/graph carries, on its own.
//
// Its own route rather than "just read /api/graph's stats field" because the
// one caller that wants stats ALONE is `k graph stats` running inside a
// cell, and making it pull every node and every edge to print six integers
// would be the most expensive read in this package for the cheapest answer.
// The shape is built by the same webGraphStatsFrom both routes call, so the
// two can't drift.
func graphStatsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	stats, err := st.GetGraphStats()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"stats": webGraphStatsFrom(stats)})
}

// graphNodeHandler serves GET /api/graph/node?id=N — the inspector's read:
// one node, its aliases, and every edge touching it, LIVE AND INVALIDATED
// both.
//
// The edge list is filtered out of AllEdges() rather than read through
// NeighborEdges, and that is a correctness choice before it is a convenience
// one: NeighborEdges is documented as returning only LIVE edges, and the
// inspector's whole point is to show a fact that stopped being true beside
// the one that replaced it. The cost is the same full-table read
// GET /api/graph already performs on every poll.
func graphNodeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("id"))
	if raw == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id " + strconv.Quote(raw) + " — want a positive integer node id"})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	nodes, err := st.GetNodes([]int64{id})
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// GetNodes SKIPS ids with no row rather than erroring, so an empty
	// result is how "no such node" arrives here — a 404, not a 200 with a
	// null node, because the caller named one specific node.
	if len(nodes) == 0 {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no node with id " + strconv.FormatInt(id, 10)})
		return
	}
	n := nodes[0]
	aliases, err := st.NodeAliases(id)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	all, err := st.EdgesLite()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	pos, err := st.Layout()
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	var x, y float64
	for _, p := range pos {
		if p.NodeID == id {
			x, y = p.X, p.Y
			break
		}
	}
	edges := make([]webGraphEdge, 0)
	for _, e := range all {
		if e.SrcID == id || e.DstID == id {
			edges = append(edges, webGraphEdgeFrom(e))
		}
	}
	webJSON(w, http.StatusOK, map[string]any{
		"node": webGraphNode{
			ID: n.ID, Name: n.Name, Type: n.Type, Description: n.Description,
			X: x, Y: y, Aliases: aliases,
		},
		"edges": edges,
	})
}

// --- dereference: GET /api/graph/fact, GET /api/graph/episode ---
//
// The two reads behind `k graph get`. An injected memory line carries a
// HANDLE — `[F<edge id> E<episode id>, seen Nx]` — precisely so the summary in
// the block can be short; these routes are what turn that handle back into the
// whole thing. Neither touches kg_recall: dereferencing a fact the agent was
// already handed is not a delivery, so it must not spend that fact's
// once-per-session budget.
//
// Both are GETs with an id in the query string rather than POSTs with a body,
// because unlike /api/graph/search there is no arbitrary user text here — an
// integer id has every business in a URL.

// graphFactSiblingCap bounds the sibling list PER ENDPOINT. The caller is an
// agent that followed a handle to read one fact in full; ten neighbours per
// side is enough to see what else kraken believes about those two entities
// without turning a targeted dereference into the hub dump a `carl` or
// `kraken` endpoint would otherwise produce.
const graphFactSiblingCap = 10

// webGraphFactFull is one edge in FULL — every column that means anything to a
// reader, which is what separates it from webGraphFact (the search result,
// which drops both system-time stamps and valid_from because a ranked list is
// scanned rather than studied) and from webGraphEdge (the viewer's shape,
// whose endpoints are ids because the canvas draws lines between nodes it
// already has).
//
// `fact` is the stored sentence BYTE-EXACT: untruncated, never clause-cut. The
// whole point of following a handle is to get past the injected summary's
// 180-rune cut, so a route that truncated again would answer a question nobody
// asked.
//
// Endpoints are NAMES, as on webGraphFact and for the same reason: the reader
// is a human or an agent, neither of which can resolve an integer. A missing
// node row degrades to `#<id>` via graphEndpointName rather than dropping or
// failing the fact — the fact IS the answer here even more than it is on
// search.
//
// No key is omitempty. 0 is a real answer on every stamp — `valid_until` 0 is
// "still true", `expired_at` 0 is "live", `created_at` 0 would be a row
// written before the column existed — and an absent key would be
// indistinguishable from it.
type webGraphFactFull struct {
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
	Episode     int64  `json:"episode"`
}

// webGraphSiblingFact is a neighbour of the dereferenced fact: `webGraphFact`
// minus `weak` — and, like `webGraphFact`, without the system-time stamps,
// since siblings come from `NeighborEdges` and are live-only.
//
// `weak` is absent because there is no QUERY on this route — a relevance
// verdict needs something to be relevant TO, and publishing a `false` here
// would assert one that was never computed. `expired_at`/`created_at` are
// absent because siblings come from store.NeighborEdges, which is live-only by
// contract, so `expired_at` would be 0 on every element — a column with one
// value is not information.
type webGraphSiblingFact struct {
	ID          int64  `json:"id"`
	Src         string `json:"src"`
	Dst         string `json:"dst"`
	Predicate   string `json:"predicate"`
	Fact        string `json:"fact"`
	Strength    int64  `json:"strength"`
	Occurrences int64  `json:"occurrences"`
	ValidUntil  int64  `json:"valid_until"`
}

// webGraphEpisodeRef is the fact bundle's provenance POINTER: which episode
// this fact came from, and nothing else.
//
// `content` is deliberately NOT here, unlike webGraphEpisode. An episode is a
// reduced transcript span — kilobytes of it — and inlining that into every
// fact dereference would drown the one sentence the caller asked for in the
// material it was extracted from. A caller that wants the provenance text asks
// for it by name, on the episode route, which is exactly what the `id` here is
// for.
type webGraphEpisodeRef struct {
	ID   int64  `json:"id"`
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
}

func webGraphFactFullFrom(e store.GraphEdge, nodes map[int64]store.GraphNode) webGraphFactFull {
	return webGraphFactFull{
		ID:        e.ID,
		Src:       graphEndpointName(nodes, e.SrcID),
		Dst:       graphEndpointName(nodes, e.DstID),
		Predicate: e.Predicate, Fact: e.Fact,
		Strength: e.Strength, Occurrences: e.Occurrences,
		ValidFrom: e.ValidFrom, ValidUntil: e.ValidUntil,
		CreatedAt: e.CreatedAt, ExpiredAt: e.ExpiredAt,
		Episode: e.EpisodeID,
	}
}

func webGraphSiblingFactFrom(e store.GraphEdge, nodes map[int64]store.GraphNode) webGraphSiblingFact {
	return webGraphSiblingFact{
		ID:        e.ID,
		Src:       graphEndpointName(nodes, e.SrcID),
		Dst:       graphEndpointName(nodes, e.DstID),
		Predicate: e.Predicate, Fact: e.Fact,
		Strength: e.Strength, Occurrences: e.Occurrences,
		ValidUntil: e.ValidUntil,
	}
}

// graphNodeMap reads the named nodes in ONE bulk call and keys them by id, so
// every endpoint on a response is glossed by one query rather than one per
// edge. Ids with no row are simply absent from the map, which is what
// graphEndpointName's `#<id>` fallback is for.
func graphNodeMap(st *store.Store, ids []int64) (map[int64]store.GraphNode, error) {
	rows, err := st.GetNodes(ids)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]store.GraphNode, len(rows))
	for _, n := range rows {
		out[n.ID] = n
	}
	return out, nil
}

// graphEndpointNode renders one endpoint of the dereferenced fact as the
// viewer's webGraphNode. x/y stay 0 — the "not positioned" contract
// POST /api/graph/search already documents — because a fact bundle is a thing
// to read, not a canvas to draw, and joining the layout table here would be a
// query for an answer nothing uses.
//
// A missing node row is not a 404 and not a dropped key: the caller named a
// FACT, and the fact exists. It degrades to the id plus the `#<id>` gloss, and
// every other field serializes as its zero value.
func graphEndpointNode(nodes map[int64]store.GraphNode, id int64, aliases []string) webGraphNode {
	n := nodes[id] // absent = zero value, which is exactly the degraded shape
	return webGraphNode{
		ID: id, Name: graphEndpointName(nodes, id),
		Type: n.Type, Description: n.Description,
		Aliases: aliases,
	}
}

// graphParseID is the id-parse both dereference routes share, in
// graphNodeHandler's exact shape: a missing id and an unparseable one are
// different sentences, and `what` names the kind of row the caller was after so
// a mistyped edge id does not read as a node error.
func graphParseID(w http.ResponseWriter, r *http.Request, what string) (int64, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get("id"))
	if raw == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return 0, false
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id " + strconv.Quote(raw) + " — want a positive integer " + what + " id"})
		return 0, false
	}
	return id, true
}

// graphSiblings returns the LIVE edges touching one endpoint, minus the edge
// being dereferenced, capped at graphFactSiblingCap and keeping
// store.NeighborEdges' `occurrences DESC, id` order — so the cap keeps the
// most-corroborated neighbours rather than an arbitrary ten.
//
// The dereferenced edge is excluded because it is already the response's
// `fact`; leaving it in would make the bundle assert the same row twice and
// spend one of the ten slots saying nothing.
func graphSiblings(st *store.Store, nodeID, exclude int64) ([]store.GraphEdge, error) {
	rows, err := st.NeighborEdges([]int64{nodeID})
	if err != nil {
		return nil, err
	}
	out := make([]store.GraphEdge, 0, graphFactSiblingCap)
	for _, e := range rows {
		if e.ID == exclude {
			continue
		}
		if len(out) >= graphFactSiblingCap {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

// graphFactHandler serves GET /api/graph/fact?id=N — one fact in full, its two
// endpoint entities, the episode it came from, and what else those entities
// are known for.
//
// An INVALIDATED edge still dereferences, and that is the point rather than an
// oversight: a handle cited in a brief written last month may name a fact
// kraken has since unlearned, and answering 404 would leave the reader unable
// to tell "kraken never said this" from "kraken stopped believing it". The
// bundle carries `expired_at`, so the answer says which.
func graphFactHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	id, ok := graphParseID(w, r, "edge")
	if !ok {
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	// GetEdges SKIPS ids with no row rather than erroring, so an empty result
	// is how "no such fact" arrives here — a 404, not a 200 with a null fact,
	// because the caller named one specific edge.
	edges, err := st.GetEdges([]int64{id})
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(edges) == 0 {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no fact with id " + strconv.FormatInt(id, 10)})
		return
	}
	e := edges[0]

	srcSibs, err := graphSiblings(st, e.SrcID, e.ID)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// A SELF-LOOP fact has one endpoint, so it has one sibling list; it rides
	// under both keys rather than being computed twice, which is both the
	// cheaper answer and the honest one (the two lists could not differ).
	dstSibs := srcSibs
	if e.DstID != e.SrcID {
		dstSibs, err = graphSiblings(st, e.DstID, e.ID)
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	// One node read for every name this response renders — the two endpoints
	// plus both sibling lists' endpoints — rather than one per fact.
	ids := []int64{e.SrcID, e.DstID}
	for _, s := range srcSibs {
		ids = append(ids, s.SrcID, s.DstID)
	}
	for _, s := range dstSibs {
		ids = append(ids, s.SrcID, s.DstID)
	}
	nodes, err := graphNodeMap(st, ids)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	srcAliases, err := st.NodeAliases(e.SrcID)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	dstAliases := srcAliases
	if e.DstID != e.SrcID {
		dstAliases, err = st.NodeAliases(e.DstID)
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	// An edge whose episode row is gone — or an episode id of 0, which no
	// episode ever has — serves `null` rather than an empty object: "kraken
	// cannot say where this came from" and "it came from episode 0" are
	// different claims.
	var epRef *webGraphEpisodeRef
	ep, err := st.GetEpisode(e.EpisodeID)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if ep != nil {
		epRef = &webGraphEpisodeRef{ID: ep.ID, Kind: ep.Kind, Ref: ep.Ref}
	}

	srcFacts := make([]webGraphSiblingFact, 0, len(srcSibs))
	for _, s := range srcSibs {
		srcFacts = append(srcFacts, webGraphSiblingFactFrom(s, nodes))
	}
	dstFacts := make([]webGraphSiblingFact, 0, len(dstSibs))
	for _, s := range dstSibs {
		dstFacts = append(dstFacts, webGraphSiblingFactFrom(s, nodes))
	}

	webJSON(w, http.StatusOK, map[string]any{
		"fact":    webGraphFactFullFrom(e, nodes),
		"src":     graphEndpointNode(nodes, e.SrcID, srcAliases),
		"dst":     graphEndpointNode(nodes, e.DstID, dstAliases),
		"episode": epRef,
		"siblings": map[string]any{
			"src": srcFacts,
			"dst": dstFacts,
		},
	})
}

// graphEpisodeHandler serves GET /api/graph/episode?id=N — one provenance unit
// with its text, and every fact extracted from it.
//
// `content` DOES inline here, unlike in the fact bundle's episode pointer:
// pulling the provenance text is this route's entire purpose, and the caller
// asked for it by id rather than receiving it as a side effect.
//
// The edge list is live AND invalidated (store.EdgesForEpisode does not filter
// expired_at), because the question is what this episode produced, not what
// survives of it.
func graphEpisodeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	id, ok := graphParseID(w, r, "episode")
	if !ok {
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	ep, err := st.GetEpisode(id)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if ep == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no episode with id " + strconv.FormatInt(id, 10)})
		return
	}
	edges, err := st.EdgesForEpisode(id)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	ids := make([]int64, 0, len(edges)*2)
	for _, e := range edges {
		ids = append(ids, e.SrcID, e.DstID)
	}
	nodes, err := graphNodeMap(st, ids)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// An episode that yielded no facts serves `[]`, never null: it is a span
	// that HAS been read and produced nothing, which is an ordinary answer and
	// a different one from a client that failed to decode a list.
	facts := make([]webGraphFactFull, 0, len(edges))
	for _, e := range edges {
		facts = append(facts, webGraphFactFullFrom(e, nodes))
	}
	webJSON(w, http.StatusOK, map[string]any{
		"episode": webGraphEpisode{ID: ep.ID, Kind: ep.Kind, Ref: ep.Ref, Content: ep.Content, TS: ep.TS},
		"edges":   facts,
	})
}

// --- retrieval: POST /api/graph/search ---

// graphSearchReq is POST /api/graph/search's body. Limit is a plain int, not
// a pointer: absent and 0 mean the same thing here (use the default), which
// is the opposite of jobload's unblock/scope — there, 0 was a DISTINCT
// meaningful request (the clear), so the two cases had to stay separable.
// Nothing on this route treats 0 as an instruction, so nothing is lost.
type graphSearchReq struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

const (
	// graphSearchDefaultLimit / graphSearchMaxLimit bound the fact list. The
	// cap is a bound on the RESPONSE, not a rejection: a caller asking for
	// more gets the cap, since a search that refuses to answer because the
	// number was too big is worse than one that answers with plenty.
	graphSearchDefaultLimit = 20
	graphSearchMaxLimit     = 200

	// graphSearchCandidates / graphSearchExpandCap are the retrieval
	// pipeline's internal widths, fixed here rather than exposed on the wire:
	// they are properties of how well the fusion works, not of what a caller
	// wants, and a client tuning them would be tuning something it cannot
	// observe.
	//
	// graphSearchExpandCap is a CEILING on the value handed to
	// core.GraphSearch, which is min(50, limit). Read what it bounds
	// carefully: it is a NEW-NODE budget (core.GraphSearchOpts calls it the
	// "1-hop expansion NODE cap"), so it limits how far OUT of the ranked
	// result the expansion may reach, not how many facts come back. An edge
	// whose endpoints are BOTH already named by a ranked fact costs zero nodes
	// and rides for free at any cap — so ten parallel edges between one
	// already-known pair arrive whether the cap is 50 or 1.
	//
	// Scaling it with the limit is therefore about reach, and expansion facts
	// ride ABOVE Limit: a flat 50 let `limit: 1` walk fifty distinct nodes away
	// from a single ranked fact, so the caller that asked for one fact got a
	// neighbourhood, and every fact in it was one it had no reason to trust.
	// Tying the reach to what the caller asked for keeps a small ask small on
	// the fixtures where expansion is node-bound; it is not a bound on the
	// response size, and nothing here should be read as promising one.
	graphSearchCandidates = 100
	graphSearchExpandCap  = 50

	// graphSearchFloor is a rank-DEPTH cutoff, not a relevance gate, and the
	// difference matters enough to state: RRF scores are a function of rank
	// alone (Σ 1/(60+rank) — the fusion never touches BM25 or cosine
	// magnitudes), so the top hit of even a completely off-topic query
	// scores ~0.019 and clears any floor low enough to admit ordinary
	// results. What 0.01 actually does is truncate the fused list around
	// rank 65, bounding the response. An off-topic query therefore returns
	// its least-bad matches rather than nothing — the caller reading this
	// response weighs facts by their content, not by their presence. The real
	// relevance gate is core.RelevantEdge below — a true cosine when a query
	// vector and a stored one are both available, and a shared-content-word
	// count when either is missing (no embedder, no blob, wrong dimension).
	// Here it LABELS rather than filters, since a search that answered
	// "nothing" would strand the one caller (the graph skill's canonicalization
	// step) that needs to see near-misses.
	graphSearchFloor = 0.01

	// graphWeakDivider separates the strong run of `triples` from the weak one.
	// It is a plain line rather than a key because `triples` is TEXT that an
	// agent pastes into its context: whatever marks the boundary has to survive
	// being read as prose, and a bare sentence does while a sigil or a per-line
	// suffix does not. The trailing newline is part of it — every other line in
	// this blob carries its own.
	graphWeakDivider = "weakly related:\n"
)

// graphSearchHandler serves POST /api/graph/search: embed the query, fuse
// vector and BM25 candidates, expand one hop, and return both the structured
// facts and the triple text an agent pastes into its context.
//
// It is NOT mirror-gated. A search writes nothing — it is a POST purely
// because the query is arbitrary user text (this corpus is full of `%3`,
// `carl/<slug>-<id>` and quotes), which has no business being URL-encoded
// into a query string and logged by every intermediary.
//
// A missing embedder degrades SILENTLY to BM25-only. core.NewEmbedder's own
// contract says ErrNoEmbedder is "feature not configured" and never a
// failure to report, so a fresh install with no OPENAI_API_KEY gets keyword
// search rather than a 500 — and every OTHER embedder error is treated the
// same way here, deliberately: a down API or a rate limit must not take the
// search offline when BM25 can still answer.
func graphSearchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req graphSearchReq
	if !decodeGraphBody(w, r, &req, "this route takes only query and limit") {
		return
	}
	query := strings.TrimSpace(req.Query)
	if query == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing query"})
		return
	}
	limit := req.Limit
	if limit <= 0 {
		limit = graphSearchDefaultLimit
	}
	if limit > graphSearchMaxLimit {
		limit = graphSearchMaxLimit
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}

	// A nil query vector is core.GraphSearch's documented BM25-only mode, so
	// every embedder failure lands on the same nil rather than on a branch
	// per error kind.
	var vec []float32
	if emb, err := core.NewEmbedder(); err == nil {
		if vecs, err := emb.Embed([]string{query}); err == nil && len(vecs) == 1 {
			vec = vecs[0]
		}
	}

	// Expansion scales with what the caller asked for; see graphSearchExpandCap.
	expandCap := graphSearchExpandCap
	if limit < expandCap {
		expandCap = limit
	}

	res, err := core.GraphSearch(st, vec, query, core.GraphSearchOpts{
		CandidateLimit: graphSearchCandidates,
		ExpandCap:      expandCap,
		Floor:          graphSearchFloor,
		Limit:          limit,
	})
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// The expansion arm of the pipeline hands back edges with NO embedding:
	// store.NeighborEdges selects a literal x'' for that column on purpose,
	// because a hub node with 10k edges would otherwise hydrate ~40 MB of blobs
	// so core could keep twelve of them. Every earlier consumer read only
	// names, facts and counts, so the elision was invisible — this gate is the
	// first one that reads the blob, and left alone it would silently ask a
	// DIFFERENT QUESTION of expansion facts than of ranked ones: cosine for the
	// ranked list, a two-shared-words coincidence for everything expanded in.
	// That is exactly how an unrelated neighbour gets called strong and
	// suppresses the "nothing strongly matched" headline this feature exists to
	// print. One gate, one notion of relevance — so the dropped blobs are read
	// back here, in one call, for the edges that lack them and nothing else.
	//
	// Only when there is a query vector to compare them against. With `vec` nil
	// — a keyless install, or an embedder that just failed — RelevantEdge
	// short-circuits to its lexical arm without ever reading a blob, so the
	// fetch could not change a single verdict; skipping it means the install
	// that has no vectors at all also never pays for the query, and never meets
	// the failure path below.
	//
	// The honest bound on the work: `missing` is at most the WHOLE returned
	// result. Ranked edges are not guaranteed to carry vectors either —
	// embedding is asynchronous (store.GraphStats counts the backlog as
	// MissingEmbeddings), so a freshly extracted fact can rank before it is
	// embedded — and the expansion run is capped in NODES, not edges. So the
	// ceiling is the ranked list (≤ the clamped limit, ≤ 200) plus however many
	// facts the expansion appended, in ONE GetEdges call; that call chunks its
	// IN-list at 500 ids, so an ordinary result is a single query.
	var hydrated map[int64][]byte
	if len(vec) > 0 {
		var missing []int64
		for _, e := range res.Edges {
			if len(e.Embedding) == 0 {
				missing = append(missing, e.ID)
			}
		}
		if len(missing) > 0 {
			// A 500 rather than a shrug, matching every other store error on
			// this route: the same handle answered GraphSearch a line ago, so a
			// failure now is a real fault, and the alternative is serving
			// confidently mislabeled facts — the one outcome `weak` exists to
			// prevent.
			rows, err := st.GetEdges(missing)
			if err != nil {
				webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
			hydrated = make(map[int64][]byte, len(rows))
			for _, e := range rows {
				hydrated[e.ID] = e.Embedding
			}
		}
	}

	// The same judgment the spawn path applies, on the same query vector the
	// legs above ran with — so a fact called weak here is a fact graphContext
	// would have refused to inject. With no embedder `vec` is nil and
	// core.RelevantEdge falls to its lexical gate, which is the same
	// degradation the retrieval itself already took.
	//
	// One MANDATED divergence — the asymmetry is in the INPUTS, not the
	// judgment: graphContext does NOT re-hydrate, so an expansion fact this
	// route cosine-judges the spawn path still judges lexically. The verdicts
	// can then disagree either way: the spawn path may inject a fact this
	// route calls weak (a two-word coincidence), or drop one this route calls
	// strong (a paraphrase sharing too few words). That asymmetry is
	// deliberate and pinned — the spawn path's gate
	// behavior is frozen byte-identical by the jobs regression suite, and it is
	// a path that must never grow a query it can fail on. Do not "restore
	// consistency" by teaching graphctx.go to hydrate; that breaks the pin.
	// The shared thing is the JUDGMENT (core.RelevantEdge), not the inputs each
	// caller can afford to feed it.
	terms := core.ContentTerms(query)
	facts := make([]webGraphFact, 0, len(res.Edges))
	// Split for `triples` only. `facts` keeps PIPELINE order — ranked facts
	// then expansion — because that order is the ranking, and a client reading
	// the structured list has the `weak` flag to group by itself.
	strong := make([]store.GraphEdge, 0, len(res.Edges))
	var weak []store.GraphEdge
	for _, e := range res.Edges {
		// Judged on the edge's REAL vector: the hydrated copy where the
		// pipeline elided one, the edge as returned otherwise. A row that has
		// never been embedded hydrates to an empty blob and falls to the
		// lexical gate — which is the correct verdict for it rather than a
		// fallback, since there is no vector to disagree with.
		//
		// Judged ONCE per edge, too: RelevantEdge decodes the blob, and the
		// verdict feeds both `facts` and the `triples` split below.
		judged := e
		if blob, ok := hydrated[e.ID]; ok {
			judged.Embedding = blob
		}
		// A fact touching a node the QUERY NAMED is never weak, whatever the
		// cosine says. `weak` means "retrieval ranked this but it is not about
		// what you asked", and for an anchored fact that claim is false by
		// construction: the search resolved the anchor from the query's own
		// terms against the node's name or alias. RelevantEdge cannot see that
		// — it compares the fact SENTENCE — so a biography of an entity the
		// caller spelled out would come back labeled as a coincidence, under
		// the "nothing strongly matched" headline. This is the same exemption
		// the injection paths take (jobs' keepRelevantFacts), reached through
		// the same res.Anchors, so the two consequences of one judgment stay
		// one judgment. Non-anchored facts are judged exactly as before.
		isWeak := !res.Anchors[e.SrcID] && !res.Anchors[e.DstID] &&
			!core.RelevantEdge(judged, vec, terms)
		if isWeak {
			weak = append(weak, e)
		} else {
			strong = append(strong, e)
		}
		// An endpoint missing from the result's node map degrades to a
		// rendered id rather than dropping the fact: the fact IS the answer,
		// and losing it because one gloss didn't come back would be the worse
		// failure.
		facts = append(facts, webGraphFact{
			ID:  e.ID,
			Src: graphEndpointName(res.Nodes, e.SrcID),
			Dst: graphEndpointName(res.Nodes, e.DstID),

			Predicate: e.Predicate, Fact: e.Fact,
			Strength: e.Strength, Occurrences: e.Occurrences,
			ValidUntil: e.ValidUntil,
			Weak:       isWeak,
		})
	}
	// The entities touched by those facts ride alongside, sorted by id for a
	// deterministic response. This is not decoration: .claude/skills/graph's
	// canonicalization step uses `k graph search "<name>"` to decide whether
	// an incoming name is something already known — and that judgement is
	// about ENTITIES. Facts alone answer it only indirectly, and the one
	// call the skill makes there is a merge VETO, which needs to see the
	// candidate's name, type and gloss to refuse on.
	ids := make([]int64, 0, len(res.Nodes))
	for id := range res.Nodes {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	nodes := make([]webGraphNode, 0, len(ids))
	for _, id := range ids {
		n := res.Nodes[id]
		// No x/y lookup: a search result is a list to read, not a canvas to
		// draw, and joining the layout table here would be a second query on
		// a path that has no use for the answer. They serve as 0,0, which is
		// the same "not positioned" contract GET /api/graph documents.
		nodes = append(nodes, webGraphNode{
			ID: n.ID, Name: n.Name, Type: n.Type, Description: n.Description,
		})
	}

	// `triples` is regrouped: everything that cleared the gate, then the
	// divider, then everything that did not. Both runs keep their pipeline
	// order, so the ranking survives inside each. Rendered through core.Triples
	// over two sub-results sharing res.Nodes rather than by formatting lines
	// here — the `(src, predicate, dst) :: fact` spelling stays owned by the one
	// function that has always owned it, and this route cannot drift from it.
	//
	// Grouped rather than annotated per line because the reader is a model
	// filling its context: a run of facts under a heading is skippable in one
	// glance, whereas a suffix on every line has to be parsed on every line.
	triples := core.Triples(core.GraphResult{Nodes: res.Nodes, Edges: strong})
	if len(weak) > 0 {
		triples += graphWeakDivider + core.Triples(core.GraphResult{Nodes: res.Nodes, Edges: weak})
	}

	webJSON(w, http.StatusOK, map[string]any{
		"nodes": nodes,
		"facts": facts,
		// `strong` is the count a caller checks before trusting anything: zero
		// means the graph answered, but with nothing that is actually about the
		// query. Derivable from `facts` — but the CLI renders `triples` and
		// never decodes `facts` (see cmd_graph.go), so making it re-derive this
		// would mean modeling the whole fact schema to count a boolean.
		"strong":  len(strong),
		"triples": triples,
	})
}

// graphEndpointName renders one edge endpoint for a human or an agent,
// falling back to "#<id>" when the node is absent from the result set.
func graphEndpointName(nodes map[int64]store.GraphNode, id int64) string {
	if n, ok := nodes[id]; ok && n.Name != "" {
		return n.Name
	}
	return "#" + strconv.FormatInt(id, 10)
}

// --- the extraction agent's write: POST /api/graph/add ---

// graphAddReq is one extraction turn's whole output: the entities it saw,
// the facts it read, and the facts it now believes stopped being true.
//
// Three lists in ONE request rather than three routes because they are one
// atomic claim about one episode: an edge names endpoints the same body
// creates, and an invalidation is only meaningful beside the edge that
// replaced it. Splitting them would make ordering the caller's problem.
type graphAddReq struct {
	EpisodeID  int64                  `json:"episode_id"`
	Nodes      []graphAddNode         `json:"nodes"`
	Edges      []graphAddEdge         `json:"edges"`
	Invalidate []graphInvalidateEntry `json:"invalidate"`
}

type graphAddNode struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Aliases     []string `json:"aliases"`
}

// graphAddEdge names its endpoints by NAME, never by id. The extraction
// agent reads prose and writes prose; it has no way to know an integer, and
// asking it to would mean a resolve round trip per endpoint before it could
// state a single fact. A name that resolves to nothing creates a bare node —
// the fact is the thing worth keeping, and an entity mentioned only as an
// endpoint is still an entity.
//
// Both world-time bounds are accepted on the way in. `valid_from` is the
// usual one; `valid_until` is here because a fact can be extracted ALREADY
// KNOWN to have ended — an episode saying "we used X until March" states one
// edge with both bounds, not a live edge plus an invalidation of something
// that was never live. Leaving it undeclared would have made every payload
// carrying it a 400 under DisallowUnknownFields, which is exactly the shape
// .claude/skills/graph/SKILL.md documents to the extraction agent.
//
// Note the asymmetry with `invalidate`, which is deliberate: a `valid_until`
// HERE is part of the original claim and leaves `expired_at` at 0 (the row is
// live — kraken still believes it, the world just moved on), whereas
// InvalidateEdge stamps `expired_at` because kraken UNLEARNED something it
// had asserted. World time and system time are not interchangeable.
type graphAddEdge struct {
	Src        string `json:"src"`
	Dst        string `json:"dst"`
	Predicate  string `json:"predicate"`
	Fact       string `json:"fact"`
	Strength   int64  `json:"strength"`
	ValidFrom  int64  `json:"valid_from"`
	ValidUntil int64  `json:"valid_until"`
}

type graphInvalidateEntry struct {
	EdgeID     int64 `json:"edge_id"`
	ValidUntil int64 `json:"valid_until"`
}

// graphAddNodeResult / graphAddEdgeResult report what each item became. The
// id is the point — an agent that just created an entity needs to be able to
// say so — and `added` distinguishes a new row from a merge into an existing
// one, which is the only way a caller can tell that its "new" entity was
// actually an alias of something already known.
type graphAddNodeResult struct {
	Name  string `json:"name"`
	ID    int64  `json:"id"`
	Added bool   `json:"added"`
}

type graphAddEdgeResult struct {
	Src   string `json:"src"`
	Dst   string `json:"dst"`
	ID    int64  `json:"id"`
	Added bool   `json:"added"`
}

// graphStrengthMin / graphStrengthMax bound an edge's extractor confidence.
// An out-of-range value — INCLUDING an absent one, which decodes to 0 — is a
// 400 rather than a coerced default, for jobload.go's reason: the number is
// a claim about how sure the extractor was, and inventing one on its behalf
// records a confidence nobody asserted.
const (
	graphStrengthMin = 1
	graphStrengthMax = 10
)

// decodeGraphBody is the shared decode discipline for this file's POSTs,
// lifted from jobload.go: a 1 MB cap answered with 413 rather than read into
// memory, DisallowUnknownFields so a key matching NO field is a refusal
// instead of a request that quietly means something else, and the unknown-
// field case told apart from ordinary malformed JSON by the only means
// encoding/json offers — a substring of its prose. The offending key is
// never echoed: it comes from the body, so quoting it would let a caller
// choose the size of the response.
//
// `takes` is the tail of the unknown-field refusal, naming this route's
// actual field set so the caller is told what IS accepted rather than only
// that something wasn't.
func decodeGraphBody(w http.ResponseWriter, r *http.Request, into any, takes string) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(into); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return false
		}
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — " + takes,
			})
			return false
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return false
	}
	return true
}

// normalizeGraphAdd collapses internal whitespace — newlines included — in
// every stored text field, in place. The write path is where this invariant
// belongs (review finding): core.Triples' output is one fact per line and its
// `(src, predicate, dst)` tuple is split on punctuation, so a fact carrying a
// newline (a model quoting a code block is plausible extractor output) or a
// name carrying `)` breaks every line-oriented reader downstream —
// normalising here fixes the CLI, the skill's canonicalization step and the
// injection block at once. Fields+Join rather than a targeted newline strip:
// the readers assume single-spaced one-line text, not merely newline-free.
func normalizeGraphAdd(req *graphAddReq) {
	oneLine := func(s string) string { return strings.Join(strings.Fields(s), " ") }
	for i := range req.Nodes {
		req.Nodes[i].Name = oneLine(req.Nodes[i].Name)
		req.Nodes[i].Description = oneLine(req.Nodes[i].Description)
		for j := range req.Nodes[i].Aliases {
			req.Nodes[i].Aliases[j] = oneLine(req.Nodes[i].Aliases[j])
		}
	}
	for i := range req.Edges {
		req.Edges[i].Src = oneLine(req.Edges[i].Src)
		req.Edges[i].Dst = oneLine(req.Edges[i].Dst)
		req.Edges[i].Predicate = oneLine(req.Edges[i].Predicate)
		req.Edges[i].Fact = oneLine(req.Edges[i].Fact)
	}
}

// validateGraphAdd is the whole gate, and it runs BEFORE any write. That
// ordering is the reason it is a separate function: these three lists are
// written through separate store calls with no enclosing transaction, so a
// validation failure discovered halfway would leave a half-applied episode
// with no way to roll it back. Everything refusable is refused first, and
// what remains can only fail on a real store error.
func validateGraphAdd(req graphAddReq) error {
	if len(req.Nodes) == 0 && len(req.Edges) == 0 && len(req.Invalidate) == 0 {
		return errors.New("nothing to add: send at least one of nodes, edges or invalidate")
	}
	// Normalization happens in normalizeGraphAdd BEFORE this gate runs, so
	// every check below sees the text as it will be stored.
	_ = req
	for i, n := range req.Nodes {
		if strings.TrimSpace(n.Name) == "" {
			return fmt.Errorf("nodes[%d]: blank name — a node's name is its identity", i)
		}
		for j, a := range n.Aliases {
			if strings.TrimSpace(a) == "" {
				return fmt.Errorf("nodes[%d].aliases[%d]: blank alias", i, j)
			}
		}
	}
	for i, e := range req.Edges {
		switch {
		case strings.TrimSpace(e.Src) == "":
			return fmt.Errorf("edges[%d]: blank src", i)
		case strings.TrimSpace(e.Dst) == "":
			return fmt.Errorf("edges[%d]: blank dst", i)
		case strings.TrimSpace(e.Predicate) == "":
			return fmt.Errorf("edges[%d]: blank predicate", i)
		case strings.TrimSpace(e.Fact) == "":
			// The fact sentence IS the edge's name and the only text the FTS
			// index and the embedder ever see, so an edge without one is
			// unsearchable by both halves of retrieval.
			return fmt.Errorf("edges[%d]: blank fact — the fact sentence is what retrieval searches, an edge without one is unfindable", i)
		case e.Strength < graphStrengthMin || e.Strength > graphStrengthMax:
			return fmt.Errorf("edges[%d]: strength %d out of range — want %d..%d, and send it explicitly rather than omitting it (an absent strength is not a default, it is an unstated confidence)",
				i, e.Strength, graphStrengthMin, graphStrengthMax)
		}
	}
	for i, iv := range req.Invalidate {
		if iv.EdgeID <= 0 {
			return fmt.Errorf("invalidate[%d]: missing edge_id", i)
		}
		// 0 on valid_until means "still true" everywhere else in this schema
		// (store/graph.go's GraphEdge documents it), so accepting it here
		// would write a row claiming both that kraken unlearned the fact and
		// that the fact never stopped holding.
		if iv.ValidUntil <= 0 {
			return fmt.Errorf("invalidate[%d]: missing valid_until — 0 means \"still true\" on this column, which is the opposite of invalidating; send the unix-millis instant the fact stopped holding", i)
		}
	}
	return nil
}

// graphAddHandler serves POST /api/graph/add.
//
// Failure posture: there is no transaction spanning the three lists, so a
// store error partway through leaves earlier items written. That is reported
// rather than hidden — the 500 carries the per-item results accumulated so
// far, so a caller can retry the remainder instead of re-sending writes that
// already landed (and every store call here is idempotent under a retry
// anyway: UpsertNode merges, AddAlias is ON CONFLICT DO NOTHING, InsertEdge
// treats a repeat as a sighting).
func graphAddHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req graphAddReq
	if !decodeGraphBody(w, r, &req,
		"this route takes only episode_id, nodes, edges and invalidate") {
		return
	}
	normalizeGraphAdd(&req)
	if err := validateGraphAdd(req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}

	nodeResults := make([]graphAddNodeResult, 0, len(req.Nodes))
	edgeResults := make([]graphAddEdgeResult, 0, len(req.Edges))
	invalidated := make([]int64, 0, len(req.Invalidate))

	fail := func(err error) {
		webJSON(w, http.StatusInternalServerError, map[string]any{
			"error":       err.Error(),
			"nodes":       nodeResults,
			"edges":       edgeResults,
			"invalidated": invalidated,
		})
	}

	// resolved memoizes name -> id within this request. Two edges naming the
	// same endpoint are the normal case, and without this each would pay a
	// ResolveNode round trip.
	resolved := map[string]int64{}
	nodeID := func(name string) (int64, error) {
		key := strings.ToLower(strings.TrimSpace(name))
		if id, ok := resolved[key]; ok {
			return id, nil
		}
		id, err := st.UpsertNode(store.GraphNode{Name: name})
		if err != nil {
			return 0, err
		}
		resolved[key] = id
		return id, nil
	}

	for _, n := range req.Nodes {
		// A node is "added" when nothing already resolved its name or one of
		// its aliases — asked BEFORE the upsert, since afterwards the two
		// cases are indistinguishable.
		existing, err := st.ResolveNode(n.Name)
		if err != nil {
			fail(err)
			return
		}
		id, err := st.UpsertNode(store.GraphNode{
			Name: n.Name, Type: n.Type, Description: n.Description,
		})
		if err != nil {
			fail(err)
			return
		}
		for _, a := range n.Aliases {
			if err := st.AddAlias(id, a); err != nil {
				fail(err)
				return
			}
		}
		resolved[strings.ToLower(strings.TrimSpace(n.Name))] = id
		nodeResults = append(nodeResults, graphAddNodeResult{Name: n.Name, ID: id, Added: existing == nil})
	}

	for _, e := range req.Edges {
		src, err := nodeID(e.Src)
		if err != nil {
			fail(err)
			return
		}
		dst, err := nodeID(e.Dst)
		if err != nil {
			fail(err)
			return
		}
		id, added, err := st.InsertEdge(store.GraphEdge{
			SrcID: src, DstID: dst, Predicate: e.Predicate, Fact: e.Fact,
			EpisodeID: req.EpisodeID, Strength: e.Strength,
			ValidFrom: e.ValidFrom, ValidUntil: e.ValidUntil,
		})
		if err != nil {
			fail(err)
			return
		}
		edgeResults = append(edgeResults, graphAddEdgeResult{Src: e.Src, Dst: e.Dst, ID: id, Added: added})
	}

	for _, iv := range req.Invalidate {
		if err := st.InvalidateEdge(iv.EdgeID, iv.ValidUntil); err != nil {
			fail(err)
			return
		}
		invalidated = append(invalidated, iv.EdgeID)
	}

	webJSON(w, http.StatusOK, map[string]any{
		"nodes":       nodeResults,
		"edges":       edgeResults,
		"invalidated": invalidated,
	})
}

// --- the extraction queue: GET /api/graph/pending, POST /api/graph/episode-done ---

const (
	graphPendingDefaultLimit = 20
	// graphPendingMaxLimit bounds the response for the same reason the search
	// cap does — an episode's `content` is a reduced transcript span, so a
	// large limit is a large body, not just a long list.
	graphPendingMaxLimit = 200
)

// graphPendingHandler serves GET /api/graph/pending?limit= — the extraction
// agent's queue read, oldest first (store.PendingEpisodes' own ordering, so
// the backlog drains in ingest order).
func graphPendingHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	limit := graphPendingDefaultLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad limit " + strconv.Quote(raw) + " — want a positive integer"})
			return
		}
		limit = n
	}
	if limit > graphPendingMaxLimit {
		limit = graphPendingMaxLimit
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	eps, err := st.PendingEpisodes(limit)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]webGraphEpisode, 0, len(eps))
	for _, e := range eps {
		out = append(out, webGraphEpisode{ID: e.ID, Kind: e.Kind, Ref: e.Ref, Content: e.Content, TS: e.TS})
	}
	webJSON(w, http.StatusOK, map[string]any{"episodes": out})
}

// graphEpisodeDoneReq is POST /api/graph/episode-done's body: the id of the
// episode whose extraction turn finished.
type graphEpisodeDoneReq struct {
	ID int64 `json:"id"`
}

// graphEpisodeDoneHandler flips one episode out of the pending queue. Its
// own route rather than a field of /api/graph/add because an episode can
// legitimately yield NOTHING — a span with no facts in it is still an
// episode that has been read, and folding "done" into the write would leave
// it pending forever, re-extracted on every pass.
func graphEpisodeDoneHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req graphEpisodeDoneReq
	if !decodeGraphBody(w, r, &req, "this route takes only id") {
		return
	}
	if req.ID <= 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	st, ok := graphStore(w)
	if !ok {
		return
	}
	// An unknown id is a 404 rather than a silent 200: MarkEpisodeDone's
	// UPDATE matches nothing and reports no error, so without this lookup a
	// typo'd id would be acknowledged as done and the real episode would sit
	// pending forever.
	ep, err := st.GetEpisode(req.ID)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if ep == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no episode with id " + strconv.FormatInt(req.ID, 10)})
		return
	}
	if err := st.MarkEpisodeDone(req.ID); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
