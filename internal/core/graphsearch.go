package core

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"kraken/internal/store"
)

// graphsearch.go — the knowledge graph's read path: BM25 + vector candidates,
// fused by reciprocal rank, reranked by graph signals, widened by one hop,
// serialised as flat triples.
//
// The load-bearing property is what is NOT here: no network, no model call.
// This is what lets graphContext run inside a normal cell spawn and lets a
// cell's `k graph search` answer in one round trip. Everything expensive
// (embedding the corpus, extracting facts) already happened in the daemon or
// in an extraction cell; retrieval is arithmetic over rows.
//
// The one input that may be absent is the QUERY VECTOR: embedding is async
// everywhere (see embed.go), so a corpus with no API key configured has no
// vectors at all. A nil queryVec is therefore not an error — it degrades the
// search to its BM25 leg alone, which is exactly the eval harness's first arm.

// rrfK is reciprocal rank fusion's smoothing constant. 60 is the value the
// original RRF paper used and the one the spec fixes, and the number matters
// less than the fact that fusion reads RANKS rather than scores: BM25 returns
// a negative log-odds-ish number and cosine returns [-1,1], so any fusion that
// touched the magnitudes would be dominated by whichever leg happened to have
// the wider scale. Ranks are commensurable by construction.
const rrfK = 60

// expandSeedEdges is how many of the top ranked edges donate their endpoints
// to the 1-hop expansion. Three rather than one: the top edge alone makes the
// expansion a property of a single BM25 hit, and rather than all of them
// because NeighborEdges' cost is linear in the seed set while the ExpandCap
// cuts the result back to a dozen nodes regardless.
const expandSeedEdges = 3

// hopBoost is the multiplier an edge gets for touching a node the top BM25
// edge touches — hop distance 0 from the query's strongest lexical anchor.
// Everything further away keeps its fused score unchanged.
const hopBoost = 1.5

// occBoostScale scales the log-occurrence multiplier. See fuseScore.
const occBoostScale = 0.25

// nameBoost is the multiplier an edge gets for touching a node the QUERY NAMED
// — an anchor. Larger than hopBoost because the two signals are not the same
// strength of evidence: hop distance says "the top lexical hit also touched
// this node", which is structure, while an anchor says the query spelled the
// entity out. It is still a multiplier rather than an additive term, so the
// rule the rerank has always obeyed holds — a fact no leg retrieved cannot be
// boosted into the answer, and with the name leg above, an anchored node's
// edges ARE retrieved rather than conjured.
const nameBoost = 2.0

// GraphSearchOpts bounds a search. Every field's zero value means "use the
// default" rather than "zero", so the empty struct is the ordinary call.
type GraphSearchOpts struct {
	CandidateLimit int     // per-leg candidate pool; 0 means 50
	ExpandCap      int     // 1-hop expansion NODE cap; 0 means 12, negative disables. Expansion facts ride ABOVE Limit.
	Floor          float32 // min score to keep a RANKED fact; 0 means no floor (never applied to expansion)
	Limit          int     // max RANKED facts; 0 means 20. The result ceiling is Limit + expansion.
	// VectorLegMaxEdges skips the vector leg entirely above this many live
	// edges; 0 means 10,000. The leg reads EVERY edge row including its
	// embedding blob (measured during review: 177 ms and 458 MB of alloc at
	// 50k edges × 1024 dims, on the process's only DB connection), so past
	// the default the search stays BM25-only rather than stalling every
	// other reader in the process. Belongs here rather than in each caller:
	// the jobs spawn path carried this gate and the API path did not, which
	// is exactly the drift a shared bound exists to prevent.
	VectorLegMaxEdges int
}

// Default caps. They are constants rather than tunables because every caller
// in the binary wants the same shape: a block small enough to paste into a
// system prompt.
const (
	defaultCandLimit         = 50
	defaultExpandCap         = 12
	defaultFactLimit         = 20
	defaultVectorLegMaxEdges = 10_000
)

// GraphResult is one search's answer. Edges is the ranked fact list, best
// first. Nodes carries every node either endpoint of any returned edge names,
// so a caller can render `(src, predicate, dst)` without a second query.
// Scores is keyed by EDGE id and holds the number the ordering actually used
// — the fused rank score after the rerank multipliers, not the raw fusion, so
// that a caller comparing two entries against each other (or against Floor)
// is comparing the same quantity the sort did.
// Anchors carries the node ids the query NAMED (see AnchorNodes), and it is on
// the result rather than kept inside the search because the two consumers of a
// search both have a judgment to make about them: the injection paths pass an
// anchored fact without consulting RelevantEdge, and the search route declines
// to mark it weak. Empty is the common answer and means "this query named
// nothing", which is the case that must behave exactly as it did before
// anchors existed.
type GraphResult struct {
	Edges   []store.GraphEdge         // ranked, best first
	Nodes   map[int64]store.GraphNode // every node referenced by Edges
	Scores  map[int64]float32         // edge id -> fused score
	Anchors map[int64]bool            // node ids the query named; never nil
	// Ranked is how many leading entries of Edges are RANKED facts — the
	// three legs' fused result after Floor and Limit, so at most Limit —
	// and everything after them is the 1-hop expansion. A consumer
	// that judges relevance after the fact (the search route's weak gate)
	// needs the seam, because an expansion is only ever as good as the
	// facts it widened: widen a coincidence and the answer is the
	// coincidence's neighbourhood, whatever was asked.
	Ranked int
}

// GraphSearch runs the whole read path against st.
//
// The pipeline, in order:
//
//  1. BM25 leg — SearchEdgesFTS over live edges' fact sentences.
//  2. Vector leg — only when queryVec is non-nil. Live, embedded edges are
//     decoded into a candidate slab and scanned by TopK.
//  3. Name leg — only when the query NAMES a low-degree node (AnchorNodes
//     over st.AnchorCandidates). Those nodes' live edges enter as a third
//     ranked list. Absent anchors this leg is empty and every step below runs
//     over exactly the two lists it always did.
//  4. Fusion — RRF(k=60) on the RANK lists: score(id) = Σ 1/(60+rank),
//     rank 1-based within each leg. Never on the raw scores; see rrfK.
//  5. Rerank — the fused score is multiplied by graph signals: occurrence (a
//     fact independently restated by many episodes is a fact about kraken
//     rather than about one conversation), hop distance from the top BM25
//     edge's endpoints, and whether the edge touches an anchor. The exact
//     formula is in fuseScore.
//  6. Expansion — one hop out of the top edges' endpoints, capped at
//     ExpandCap distinct NEW nodes, appended below every ranked fact.
//  7. Floor and Limit.
//
// An empty query with a nil vector is not an error: both legs are empty and
// the result is empty. Invalidated edges never appear — the store's own
// queries filter on expired_at, so this file never re-states that rule.
func GraphSearch(st *store.Store, queryVec []float32, query string, o GraphSearchOpts) (GraphResult, error) {
	res := GraphResult{
		Nodes:   map[int64]store.GraphNode{},
		Scores:  map[int64]float32{},
		Anchors: map[int64]bool{},
	}
	if o.CandidateLimit <= 0 {
		o.CandidateLimit = defaultCandLimit
	}
	if o.ExpandCap == 0 {
		o.ExpandCap = defaultExpandCap
	}
	if o.ExpandCap < 0 {
		o.ExpandCap = 0 // negative = expansion off; 0 keeps the zero-value-is-default rule
	}
	if o.Limit <= 0 {
		o.Limit = defaultFactLimit
	}
	if o.VectorLegMaxEdges <= 0 {
		o.VectorLegMaxEdges = defaultVectorLegMaxEdges
	}
	if st == nil {
		return res, fmt.Errorf("graph search: nil store")
	}

	// (a) BM25 leg. An empty query is not an error — SearchEdgesFTS returns
	// nothing for a query with no terms, and a vector-only search is a real
	// call shape.
	bm25, err := st.SearchEdgesFTS(query, o.CandidateLimit)
	if err != nil {
		return res, fmt.Errorf("graph search bm25: %w", err)
	}

	// (b) Vector leg, gated by VectorLegMaxEdges — see the field's comment.
	var vec []Scored
	if queryVec != nil {
		vec, err = vectorLeg(st, queryVec, o.CandidateLimit, o.VectorLegMaxEdges)
		if err != nil {
			return res, err
		}
	}

	// (b2) Anchor resolution. A FAILED READ IS NOT A FAILED SEARCH: anchors are
	// an additional way to be found, so a store that cannot answer the
	// candidate query degrades to the two-leg search this file has always run
	// rather than to an error the injection paths would collapse to nothing.
	//
	// Guarded on the TERMS rather than resolved unconditionally, because
	// AnchorCandidates is two whole-table sweeps and a query with no content
	// terms cannot anchor anything — AnchorNodes' own first branch returns
	// empty for it. The empty-query vector-only shape the (a) comment above
	// names as a real call, and any query whose every token is under
	// GraphTermMinRunes, therefore pay nothing for a feature they cannot use,
	// on a path that runs inside every cell spawn and every prompt-submit hook
	// firing. It also makes obligation (d) literal rather than merely
	// equivalent: a query that cannot anchor executes the pre-change pipeline.
	if terms := ContentTerms(query); len(terms) > 0 {
		if cands, cerr := st.AnchorCandidates(); cerr == nil {
			res.Anchors = AnchorNodes(terms, cands)
		}
	}

	// (b3) Name leg — the third rank list. It exists for the case both other
	// legs are structurally unable to serve: a query naming an entity by an
	// alias ("dilly") against facts that spell the canonical name
	// ("Christopher DiLollo"), where BM25 has no shared token and the cosine
	// between a three-word question and a biography sentence sits under any
	// usable floor.
	//
	// Only edges NeighborEdges actually returns for the anchored nodes go in,
	// so the rule the rerank obeys holds here too: structure cannot nominate an
	// edge that was never retrieved, it can only retrieve the edges an anchored
	// node literally has. Truncated at CandidateLimit like the other two legs —
	// a leg of unbounded length would out-vote them by sheer count.
	var nameLeg []int64
	if len(res.Anchors) > 0 {
		anchorIDs := make([]int64, 0, len(res.Anchors))
		for id := range res.Anchors {
			anchorIDs = append(anchorIDs, id)
		}
		// Sorted for the same reason the fused id list is: map order is not
		// deterministic, and a fixed graph plus a fixed query must produce the
		// same answer run to run.
		sort.Slice(anchorIDs, func(i, j int) bool { return anchorIDs[i] < anchorIDs[j] })
		neighbors, err := st.NeighborEdges(anchorIDs)
		if err != nil {
			return res, fmt.Errorf("graph search name leg: %w", err)
		}
		for _, e := range neighbors {
			if len(nameLeg) >= o.CandidateLimit {
				break
			}
			nameLeg = append(nameLeg, e.ID)
		}
	}

	if len(bm25) == 0 && len(vec) == 0 && len(nameLeg) == 0 {
		return res, nil
	}

	// (c) RRF on ranks. Three lists now, summed by edge id exactly as two were
	// — an edge appearing in more than one leg accumulates each leg's
	// contribution, which is the whole of what fusion means here.
	fused := map[int64]float64{}
	for i, id := range bm25 {
		fused[id] += 1 / float64(rrfK+i+1)
	}
	for i, s := range vec {
		fused[s.ID] += 1 / float64(rrfK+i+1)
	}
	for i, id := range nameLeg {
		fused[id] += 1 / float64(rrfK+i+1)
	}

	ids := make([]int64, 0, len(fused))
	for id := range fused {
		ids = append(ids, id)
	}
	// Sorted before the fetch so the whole pipeline is deterministic for a
	// fixed graph: map iteration order is not, and a tie in the final score
	// would otherwise resolve differently run to run.
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	edges, err := st.GetEdges(ids)
	if err != nil {
		return res, fmt.Errorf("graph search edges: %w", err)
	}

	// (d) Rerank. Seed nodes are the endpoints of the best BM25 hit; with no
	// BM25 leg at all there is no lexical anchor and the hop multiplier is 1
	// for every edge.
	seeds := map[int64]bool{}
	if len(bm25) > 0 {
		for _, e := range edges {
			if e.ID == bm25[0] {
				seeds[e.SrcID] = true
				seeds[e.DstID] = true
				break
			}
		}
	}
	scored := make([]store.GraphEdge, 0, len(edges))
	for _, e := range edges {
		s := fuseScore(fused[e.ID], e, seeds, res.Anchors)
		if o.Floor > 0 && float32(s) < o.Floor {
			continue
		}
		res.Scores[e.ID] = float32(s)
		scored = append(scored, e)
	}
	sort.SliceStable(scored, func(i, j int) bool {
		a, b := scored[i], scored[j]
		sa, sb := res.Scores[a.ID], res.Scores[b.ID]
		if sa != sb {
			return sa > sb
		}
		return a.ID < b.ID
	})

	// (f, applied early) Limit bounds the RANKED list — before expansion, not
	// after. Appending expansion below the ranked facts and THEN truncating
	// meant any query with ≥Limit surviving candidates dropped 100% of its
	// expansion: with the defaults (Limit 20, CandidateLimit 50) that was the
	// normal case, and expansion was dead code for every production caller,
	// its only test passing on a one-edge fixture (review finding). The true
	// result ceiling is therefore Limit ranked facts PLUS whatever ExpandCap
	// admits — both documented on the fields.
	if len(scored) > o.Limit {
		for _, e := range scored[o.Limit:] {
			delete(res.Scores, e.ID)
		}
		scored = scored[:o.Limit]
	}

	// (e) 1-hop expansion, from the endpoints of the surviving top edges.
	// The Floor is deliberately NOT applied to these: an expansion fact was
	// never a candidate, so it has no fused score to compare, and it is
	// scored below every ranked fact by construction — applying the floor
	// would make Floor and ExpandCap mutually exclusive rather than
	// independent.
	expanded, err := expand(st, scored, res.Scores, o.ExpandCap)
	if err != nil {
		return res, err
	}
	res.Edges = append(scored, expanded...)
	res.Ranked = len(scored)
	out := res.Edges

	// Every node either endpoint of a returned edge names, so the caller can
	// render a triple without going back to the store.
	nodeIDs := make([]int64, 0, 2*len(out))
	seen := map[int64]bool{}
	for _, e := range out {
		for _, id := range [2]int64{e.SrcID, e.DstID} {
			if !seen[id] {
				seen[id] = true
				nodeIDs = append(nodeIDs, id)
			}
		}
	}
	nodes, err := st.GetNodes(nodeIDs)
	if err != nil {
		return res, fmt.Errorf("graph search nodes: %w", err)
	}
	for _, n := range nodes {
		res.Nodes[n.ID] = n
	}
	return res, nil
}

// vectorLeg decodes every live, embedded edge into a candidate slab and runs
// TopK over it. Brute force, per vector.go's Q1 decision — the seam to an ANN
// index is TopK alone.
//
// A blob that will not decode is SKIPPED rather than fatal, matching TopK's
// treatment of a dimension mismatch and for the same reason: a search verb
// that errors out entirely because one row was written by a different
// embedder is worse than one that answers from the rows that are intact.
// Nothing here can fail short and silently in the SumTokensDelta sense — a
// skipped candidate loses recall for one fact, it does not corrupt a stored
// counter.
func vectorLeg(st *store.Store, queryVec []float32, limit, maxEdges int) ([]Scored, error) {
	// The cheap count first: one indexed aggregate against reading every
	// blob. Above the gate the leg reports empty and the caller's search is
	// BM25-only — degradation, not an error, same posture as a missing key.
	stats, err := st.GetGraphStats()
	if err != nil {
		return nil, fmt.Errorf("graph search vector leg: %w", err)
	}
	if stats.LiveEdges > int64(maxEdges) {
		return nil, nil
	}
	all, err := st.AllEdges()
	if err != nil {
		return nil, fmt.Errorf("graph search vector leg: %w", err)
	}
	cand := make(map[int64][]float32, len(all))
	for _, e := range all {
		if e.ExpiredAt != 0 || len(e.Embedding) == 0 {
			continue
		}
		v, err := DecodeVector(e.Embedding)
		if err != nil {
			continue
		}
		cand[e.ID] = v
	}
	return TopK(queryVec, cand, limit), nil
}

// fuseScore applies the rerank multipliers to one edge's fused rank score.
// The exact formula, which is the whole of the rerank:
//
//	score = rrf * (1 + 0.25*ln(1+occurrences)) * (1.5 if the edge touches a
//	        seed node, else 1) * (2.0 if the edge touches an anchor node, else 1)
//
// All three are MULTIPLIERS on the fused score rather than additive terms, so a
// fact that no leg retrieved cannot be boosted into the answer by graph
// structure alone — structure reorders candidates, it does not nominate them.
// Occurrence is log-scaled because it is a count with no ceiling: a fact seen
// in 100 episodes is more trustworthy than one seen in 10, but not ten times
// more. The seed multiplier is a flat step rather than a decay because
// expansion is one hop: every edge here is either at hop 0 from the seed or
// unclassified, and there is no hop 2 to distinguish. ("Seed" rather than
// "anchor" deliberately — in this function `anchors` now means the nodes the
// QUERY NAMED, which is a different signal from the top BM25 hit's endpoints.) The anchor multiplier
// composes with both rather than replacing them — an edge can be lexically
// central AND named, and that is the strongest evidence available here.
func fuseScore(rrf float64, e store.GraphEdge, seeds, anchors map[int64]bool) float64 {
	occ := e.Occurrences
	if occ < 0 {
		occ = 0
	}
	s := rrf * (1 + occBoostScale*math.Log1p(float64(occ)))
	if seeds[e.SrcID] || seeds[e.DstID] {
		s *= hopBoost
	}
	if anchors[e.SrcID] || anchors[e.DstID] {
		s *= nameBoost
	}
	return s
}

// expand walks one hop out of the top ranked edges' endpoints and returns the
// edges worth appending, in the store's own (occurrences DESC, id) order.
//
// The cap counts DISTINCT NEW NODES — nodes no ranked edge already named —
// not edges, because the cap exists to bound how much unfamiliar vocabulary
// the block asks its reader to absorb. An edge that only connects two
// already-named nodes therefore costs nothing against the cap and is always
// kept: it is new information about a concept the reader already has.
//
// Scores decrease strictly along the appended run and start below every
// ranked fact, so a caller that sorts by score alone reproduces this order.
// A cyclic graph terminates here for the structural reason that this is one
// hop and not a traversal: NeighborEdges is called exactly once.
func expand(st *store.Store, ranked []store.GraphEdge, scores map[int64]float32, capNodes int) ([]store.GraphEdge, error) {
	if len(ranked) == 0 || capNodes <= 0 {
		return nil, nil
	}
	known := map[int64]bool{}    // nodes already named by a ranked edge
	haveEdge := map[int64]bool{} // edges already in the answer
	for _, e := range ranked {
		known[e.SrcID] = true
		known[e.DstID] = true
		haveEdge[e.ID] = true
	}
	// Seeds come from the top edges only, deduped in first-seen order so the
	// query passed to NeighborEdges is deterministic.
	var seedIDs []int64
	seedSeen := map[int64]bool{}
	for i, e := range ranked {
		if i >= expandSeedEdges {
			break
		}
		for _, id := range [2]int64{e.SrcID, e.DstID} {
			if !seedSeen[id] {
				seedSeen[id] = true
				seedIDs = append(seedIDs, id)
			}
		}
	}

	neighbors, err := st.NeighborEdges(seedIDs)
	if err != nil {
		return nil, fmt.Errorf("graph search expand: %w", err)
	}

	// The base is strictly below the lowest ranked score so the appended run
	// can never outrank a fact a retrieval leg actually returned.
	base := float64(scores[ranked[len(ranked)-1].ID]) / 2
	if base <= 0 {
		base = math.SmallestNonzeroFloat32
	}

	var out []store.GraphEdge
	newNodes := 0
	for _, e := range neighbors {
		if haveEdge[e.ID] {
			continue
		}
		add := 0
		if !known[e.SrcID] {
			add++
		}
		if !known[e.DstID] && e.DstID != e.SrcID {
			add++
		}
		if newNodes+add > capNodes {
			continue // a cheaper edge later in the list may still fit
		}
		known[e.SrcID] = true
		known[e.DstID] = true
		newNodes += add
		haveEdge[e.ID] = true
		out = append(out, e)
		scores[e.ID] = float32(base / float64(len(out)+1))
	}
	return out, nil
}

// Triples serialises a result as unordered linearised triples, one fact per
// line, in the ranked order Edges already carries:
//
//	(src, predicate, dst) :: fact sentence
//
// "Unordered" is about the SET, not the lines: nothing downstream may read
// meaning into a fact's position, which is why there is no numbering and no
// score in the output. The `::` separator is a deliberate two-character
// sequence that does not occur in ordinary prose, so a reader (human or
// model) can split a line back into its triple and its sentence even when the
// sentence itself contains commas and parentheses.
//
// A node id with no row in Nodes renders as `#<id>` rather than blank: a fact
// whose endpoint could not be named is still a fact, and an empty name would
// silently read as a triple about nothing.
func Triples(r GraphResult) string {
	var b strings.Builder
	for _, e := range r.Edges {
		b.WriteString("(")
		b.WriteString(nodeName(r, e.SrcID))
		b.WriteString(", ")
		b.WriteString(e.Predicate)
		b.WriteString(", ")
		b.WriteString(nodeName(r, e.DstID))
		b.WriteString(")")
		if f := strings.TrimSpace(e.Fact); f != "" {
			b.WriteString(" :: ")
			b.WriteString(f)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func nodeName(r GraphResult, id int64) string {
	if n, ok := r.Nodes[id]; ok && n.Name != "" {
		return n.Name
	}
	return "#" + strconv.FormatInt(id, 10)
}
