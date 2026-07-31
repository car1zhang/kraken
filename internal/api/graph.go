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
// inspector's per-node read, the retrieval read, and the extraction agent's
// write.
//
// Every route here is a NEW PATH rather than a key added to, retyped in, or
// removed from an existing shape, so `apiContract` stays 5. That is the bump
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
// feedback, watchlist and job/load follow. The four reads stay open — a read
// is a read, and POST /api/graph/search is a read despite its verb (it is a
// POST only because a query string is user text that has no business in a
// URL).

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
type webGraphFact struct {
	ID          int64  `json:"id"`
	Src         string `json:"src"`
	Dst         string `json:"dst"`
	Predicate   string `json:"predicate"`
	Fact        string `json:"fact"`
	Strength    int64  `json:"strength"`
	Occurrences int64  `json:"occurrences"`
	ValidUntil  int64  `json:"valid_until"`
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

	// Positions are joined in memory from one Layout() sweep rather than a
	// per-node query: this route already reads every node, and the layout
	// table has at most one row per node.
	xy := make(map[int64]store.LayoutPos, len(pos))
	for _, p := range pos {
		xy[p.NodeID] = p
	}

	// Aliases are NOT joined here. The viewer draws canonical names; aliases
	// are an inspector-level detail, and fetching them would be one query per
	// node on the hottest read this subsystem has.
	outNodes := make([]webGraphNode, 0, len(nodes))
	for _, n := range nodes {
		p := xy[n.ID] // absent = zero value = 0,0, which is the documented contract
		outNodes = append(outNodes, webGraphNode{
			ID: n.ID, Name: n.Name, Type: n.Type, Description: n.Description,
			X: p.X, Y: p.Y,
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
	// response weighs facts by their content, not by their presence. The
	// spawn-path injection (jobs' graphContext) has the real relevance gate,
	// applied against a true cosine.
	graphSearchFloor = 0.01
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

	res, err := core.GraphSearch(st, vec, query, core.GraphSearchOpts{
		CandidateLimit: graphSearchCandidates,
		ExpandCap:      graphSearchExpandCap,
		Floor:          graphSearchFloor,
		Limit:          limit,
	})
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	facts := make([]webGraphFact, 0, len(res.Edges))
	for _, e := range res.Edges {
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

	webJSON(w, http.StatusOK, map[string]any{
		"nodes":   nodes,
		"facts":   facts,
		"triples": core.Triples(res),
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
