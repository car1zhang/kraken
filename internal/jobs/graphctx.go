package jobs

import (
	"encoding/base64"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// graphctx.go — the system-prompt injection: what a new cell's zeus is told
// the graph already knows about its job.
//
// This is the highest-blast-radius function in the knowledge graph, because
// it runs on the SPAWN path: every cell start goes through it, and whatever
// it returns is appended to the brief zeus obeys. Three rules follow from
// that, and each one is why a piece of this file looks the way it does.
//
//  1. It NEVER calls a network. The job's spec vector is read from the kv
//     cache graphPass keeps warm (refreshJobVectors); a cache miss degrades
//     to BM25-only rather than embedding the spec here. A spawn that waited
//     on api.openai.com would be a spawn that fails when the network does.
//
//  2. It NEVER fails a spawn. Every error path — no store, an unreadable
//     vector, a search that errors — returns "". A job with no match and a
//     job whose graph is sick get a BYTE-IDENTICAL brief, which is also what
//     makes the whole feature safely revertible.
//
//  3. It never opens its own database handle. store.Open runs MIGRATIONS, and
//     this repo's discipline is a single writer (`kraken serve`); a spawn path
//     opening a second handle would migrate the database from whatever process
//     happens to be spawning a cell. So: no ActiveStore, no block.

// graphContextFacts caps how many facts the block may carry (Q5). Five is a
// list zeus reads; twenty is a document it skims.
const graphContextFacts = 5

// graphContextBytes caps the whole block including its header (Q5). The brief
// has a 12,000-byte ceiling of its own (TestZeusBriefStaysLean) and every byte
// here is a byte of attention spent before the job's own spec.
const graphContextBytes = 600

// graphFactRunes one-lines and caps a single fact sentence. A fact longer
// than this is a fact that was written as a paragraph, and the block is a
// list.
const graphFactRunes = 180

// graphCosineFloor is this package's name for core.GraphCosineFloor — an
// alias, not a second number. The gate itself (the cosine leg, the lexical
// fallback and the two thresholds behind them) moved to
// core/relevance.go once POST /api/graph/search needed to reach the same
// verdict: the spawn path DROPS a fact that fails and the search route MARKS
// it, and two consequences of one judgment must not become two judgments.
//
// Kept as a local name because this package's own regression pin
// (TestGraphContextCosineGate) reports the floor it is testing against, and
// because "the number the spawn path gates on" is a fact about this file
// worth being able to read here.
const graphCosineFloor = core.GraphCosineFloor

// graphSearchCandidates / graphSearchExpand / graphSearchLimit are the
// retrieval budget handed to core.GraphSearch. The limit is larger than
// graphContextFacts on purpose: the gate below rejects facts, so retrieving
// exactly five would mean any rejection shortened the block rather than
// letting the next candidate take the slot.
const (
	graphSearchCandidates = 64
	graphSearchExpand     = 32
	graphSearchLimit      = 20
)

// graphFacts is the seam onto core's read path: the ONE place in this package
// that knows core.GraphResult's shape. Held as a var so a test can drive
// graphContext's gate, budget and rendering against a hand-built fact list
// without a seeded store — and so that a change to GraphResult is a change to
// one function rather than to the injection.
var graphFacts = searchFacts

// graphVectorLegMaxEdges bounds when the SPAWN path is willing to pay for the
// vector leg, and it exists because of a cost that is invisible from this side
// of the call.
//
// core.GraphSearch's vector leg (core/graphsearch.go, vectorLeg) has no slab
// cache — by design, since that file is specced as pure functions — so a
// non-nil queryVec makes it read EVERY edge row including its embedding blob
// and decode each one into a fresh []float32, on every call. At 1024 dims
// that is ~4 KB of blob plus ~4 KB of decoded vector per live edge: ~80 MB
// transient at 10,000 edges, ~400 MB at the spec's projected 50,000. That is
// fine for `k graph search`, which is a user-initiated round trip, and it is
// not fine on a path the daemon runs inside its own tick every time a cell
// starts.
//
// So above this many live edges the spawn path passes a nil vector and lets
// BM25 carry retrieval. The degradation is small and bounded, because the
// COSINE GATE DOES NOT DEGRADE WITH IT: core.RelevantEdge re-derives the
// cosine itself over the ≤20 returned edges (twenty decodes, not fifty
// thousand), so what changes is only which candidates were nominated, never
// how strictly they are judged. What is lost is a fact worded so differently
// that BM25 never surfaced it; what is kept is the guarantee that a spawn is
// bounded no matter how large the corpus grows.
//
// 10,000 is where the transient allocation crosses ~80 MB. If the graph
// outgrows this and vector-only recall turns out to matter at spawn time, the
// fix is a daemon-owned slab cache — this package is the right owner, since
// it is the one with process state — not a bigger number here.
const graphVectorLegMaxEdges = 10_000

// searchFacts runs the retrieval and flattens it to the edges the block
// renders. Errors collapse to nil: see rule 2 above.
func searchFacts(st *store.Store, vec []float32, query string) []store.GraphEdge {
	if len(vec) > 0 {
		// One extra COUNT against a table the spawn is about to search
		// anyway, next to a `git worktree add`. An unreadable stats query
		// drops the vector leg rather than the search: BM25 alone still
		// answers, and this is the spawn path.
		stats, err := st.GetGraphStats()
		if err != nil || stats.LiveEdges > graphVectorLegMaxEdges {
			vec = nil
		}
	}
	res, err := core.GraphSearch(st, vec, query, core.GraphSearchOpts{
		CandidateLimit: graphSearchCandidates,
		ExpandCap:      graphSearchExpand,
		// Floor 0: unfloored on purpose — see graphCosineFloor.
		Floor: 0,
		Limit: graphSearchLimit,
	})
	if err != nil {
		return nil
	}
	return res.Edges
}

// graphContext returns the block appended to zeus' brief for job j, or "" when
// the graph has nothing that clears the relevance gate.
//
// "" is the important answer and the common one. A job about something the
// graph has never seen must get the brief it would have got before this
// feature existed, byte for byte.
func graphContext(j *Job) string {
	if j == nil {
		return ""
	}
	spec := strings.TrimSpace(j.Body)
	if spec == "" {
		return ""
	}
	st := ActiveStore()
	if st == nil {
		return ""
	}
	vec := cachedSpecVector(st, j.ID)
	edges := graphFacts(st, vec, spec)
	if len(edges) == 0 {
		return ""
	}
	terms := core.ContentTerms(spec)
	now := time.Now().UnixMilli()
	var kept []store.GraphEdge
	seen := map[string]bool{}
	for _, e := range edges {
		if len(kept) >= graphContextFacts {
			break
		}
		// A fact whose world-time window has CLOSED is not current knowledge,
		// however system-time live the row is — the API deliberately accepts
		// "we used X until March" as one live edge with both bounds, and
		// retrieval serves it. Injecting it as a bare claim under a sighting
		// count would present the graph's own known-ended beliefs as settled
		// fact (review finding; the exact risk 52-testing-plan.md ranks
		// first). A FUTURE valid_until stays in: "true until March" is
		// currently true.
		if e.ValidUntil != 0 && e.ValidUntil <= now {
			continue
		}
		fact := core.TruncateOneLine(e.Fact, graphFactRunes)
		if fact == "" || seen[strings.ToLower(fact)] {
			continue
		}
		if !core.RelevantEdge(e, vec, terms) {
			continue
		}
		seen[strings.ToLower(fact)] = true
		kept = append(kept, e)
	}
	return renderGraphBlock(kept)
}

// cachedSpecVector reads the job's cached spec embedding. Every failure —
// no cached row, a corrupt base64 payload, a blob whose length is not a
// multiple of 4 — degrades to nil, which is the BM25-only path rather than an
// error: the graph having no vector for this spec is not a reason to withhold
// a lexical match.
func cachedSpecVector(st *store.Store, jobID int64) []float32 {
	if jobID == 0 {
		return nil
	}
	raw, err := st.GetKV(graphJobVecKey(jobID))
	if err != nil || raw == "" {
		return nil
	}
	blob, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil
	}
	vec, err := core.DecodeVector(blob)
	if err != nil || len(vec) == 0 {
		return nil
	}
	return vec
}

// graphContextHeader opens the block. It is hedged on purpose: the graph is
// extracted by a model from transcripts, so a fact in it is evidence rather
// than instruction, and zeus has to be able to weigh it against what it finds
// in the repo. The leading newline separates it from whatever the brief's own
// last line was.
const graphContextHeader = "\nKnown from past work (may be stale — verify before relying):\n"

// renderGraphBlock formats the kept facts, honouring the byte cap on FACT
// BOUNDARIES only — a half-sentence with an episode id after it reads as a
// complete claim, which is the one way this block could actively mislead.
//
// Returns "" when nothing fits, including the case where the header plus the
// first fact already exceeds the cap: a header with no facts under it claims
// the graph knows something and then says nothing.
func renderGraphBlock(edges []store.GraphEdge) string {
	if len(edges) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(graphContextHeader)
	n := 0
	for _, e := range edges {
		line := factLine(e)
		if b.Len()+len(line) > graphContextBytes {
			// Skip THIS fact and try the next — never break: graphFactRunes
			// caps runes while this cap counts bytes, so one 180-rune fact in
			// em-dash-heavy prose is 540+ bytes, and breaking on it zeroed
			// the whole block while four shorter facts ranked below it fit
			// fine (review finding). Note the two caps are therefore not
			// independent: ~2 full-length ASCII facts fill 600 bytes, so
			// graphContextFacts=5 is a ceiling only shorter facts reach.
			continue
		}
		b.WriteString(line)
		n++
	}
	if n == 0 {
		return ""
	}
	return b.String()
}

// factLine renders one fact. The episode id and the sighting count are what
// make the claim weighable: a fact seen once in one transcript and a fact
// seen in eleven independent episodes are different kinds of evidence, and
// the id is where zeus goes to check.
func factLine(e store.GraphEdge) string {
	fact := core.TruncateOneLine(e.Fact, graphFactRunes)
	// A fact sentence written with its own terminator must not gain a second
	// one; one written without gets the one the block's format promises.
	fact = strings.TrimRight(fact, " .")
	occ := e.Occurrences
	if occ < 1 {
		occ = 1 // a stored edge was seen at least once, whatever the column says
	}
	return "- " + fact + ". [E" + strconv.FormatInt(e.EpisodeID, 10) +
		", seen " + strconv.FormatInt(occ, 10) + "x]\n"
}
