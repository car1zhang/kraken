package core

import (
	"strings"
	"unicode"

	"kraken/internal/store"
)

// relevance.go — the one judgment of whether a retrieved fact is ABOUT the
// text that retrieved it. Two callers need it and they must not disagree: the
// spawn-path injection (internal/jobs' graphContext, which DROPS what fails)
// and POST /api/graph/search (which MARKS what fails). Same verdict, two
// consequences.
//
// It lives in core rather than in either caller because RANKING IS NOT
// RELEVANCE, and that gap is what both callers are compensating for.
// GraphSearch orders candidates against each other, so the best of a bad set
// still comes back first and scores like any other top hit; the functions here
// ask the separate, absolute question of whether the winner is any good. A
// second copy of that question would be a second answer to it.
//
// Note what is deliberately absent: no stemming, no stopword list, no
// language model. This is a coincidence filter, and every failure it guards
// against is answered by word length and distinctness alone.

// GraphCosineFloor is the vector leg's relevance gate, and it is the ONLY
// place the spec's 0.72 (Q5) is applied — deliberately not as
// GraphSearchOpts.Floor.
//
// The reason is that the two numbers are not the same kind of number. Q5's
// 0.72 is a COSINE: a similarity between two unit vectors, on [-1, 1], where
// 0.72 means "these two sentences are about the same thing". What GraphSearch
// ranks by is a FUSED RRF score — a sum of 1/(k+rank) terms, which for k=60
// lives around 0.016 for a top-ranked fact and has no scale in common with a
// cosine at all. Handing 0.72 to a fused score would reject every fact ever
// retrieved, silently, and the symptom would be the feature simply never
// appearing to work. So the search runs unfloored (or floored only on rank
// depth) and the gate is applied here, against the leg the number actually
// describes.
const GraphCosineFloor = 0.72

// GraphMinTermOverlap is the BM25-only fallback gate: how many distinct
// content words a fact must share with the query text when no vector
// comparison is possible (no embedder configured, no cached vector, or a fact
// embedded under a different model's dimensions).
//
// Two rather than one, because one shared word is not evidence: FTS5 matches
// on an OR of terms (store.SearchEdgesFTS), so a fact that merely contains
// "kraken" ranks against a query that mentions kraken once. Two distinct
// content words is the cheapest thing that is not a coincidence, and being
// wrong in this direction costs a real fact, while being wrong in the other
// costs the reader's trust in every fact after it.
const GraphMinTermOverlap = 2

// GraphTermMinRunes is the shortest word the overlap gate counts. Short
// tokens are overwhelmingly grammar; requiring four runes removes the need
// for a stopword list this package would then have to maintain.
const GraphTermMinRunes = 4

// ContentTerms reduces text to the set of distinct lowercase words long
// enough to carry meaning. Deliberately not a stemmer and not a stopword
// list: this is a coincidence filter, and the failure it guards against
// (one shared common word) is answered by length alone.
//
// Splitting on every non-letter/non-digit rune means punctuation, hyphens and
// possessives are separators rather than part of a word, so `Kraken's
// graph-search v2` yields {kraken, graph, search} — "s" and "v2" are under
// GraphTermMinRunes and drop out.
func ContentTerms(s string) map[string]bool {
	out := map[string]bool{}
	for _, f := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len([]rune(f)) >= GraphTermMinRunes {
			out[f] = true
		}
	}
	return out
}

// TermOverlap counts how many DISTINCT content words a fact shares with
// terms. Distinct, so a fact repeating one word five times does not clear a
// gate meant to measure breadth of agreement.
func TermOverlap(fact string, terms map[string]bool) int {
	if len(terms) == 0 {
		return 0
	}
	hit := map[string]bool{}
	for t := range ContentTerms(fact) {
		if terms[t] {
			hit[t] = true
		}
	}
	return len(hit)
}

// AnchorMaxDegree is the name anchor's degree guard: a node with more than
// this many live edges never anchors, however exactly the query names it.
//
// The guard is what keeps anchoring from becoming a second, worse ranking. A
// hub — `carl`, `kraken` — is named by a large fraction of the corpus, so a
// query mentioning it would promote hundreds of edges past the gate, and the
// facts that actually answered the question would be one line in a flood.
// Those nodes are also exactly the ones the ORDINARY legs already serve: their
// facts are numerous, well-worded and lexically reachable. The anchor exists
// for the opposite case — a node with three facts nobody's query happens to
// share four-rune words with.
//
// 30 rather than a percentile of the degree distribution because the number
// must be stable as the graph grows: a percentile silently re-admits hubs when
// the corpus doubles, and a fixed count is the thing a reader can check a node
// against by hand.
const AnchorMaxDegree = 30

// AnchorNodes resolves which of cands the query's terms NAME. Pure: no store,
// no clock, so both the search path and a test reach the same verdict from the
// same two inputs.
//
// A candidate anchors when its degree is within the guard AND at least one of
// its strings — the canonical name or any alias — is fully covered by terms:
// every content token of that string is present in the query's term set.
// Coverage rather than overlap, because a partial match is a coincidence at
// this leverage: "lewis" naming the `lewis` node is the feature, while "the
// probook dispatch service" half-matching `dispatch` would bypass the gate on
// the strength of one word.
//
// Both sides run through ContentTerms, which is what makes the match
// case-insensitive and punctuation-insensitive without a second normalizer
// here — and what makes a name whose every token is under GraphTermMinRunes
// (a two-letter alias) unable to anchor at all. That is the honest outcome:
// the query side dropped those tokens too, so there is nothing left to have
// matched on.
//
// The result is never nil, so a caller can index it without a guard.
func AnchorNodes(terms map[string]bool, cands []store.AnchorCandidate) map[int64]bool {
	out := map[int64]bool{}
	if len(terms) == 0 {
		return out
	}
	for _, c := range cands {
		if c.Degree > AnchorMaxDegree {
			continue
		}
		if nameCovered(c.Name, terms) {
			out[c.NodeID] = true
			continue
		}
		for _, a := range c.Aliases {
			if nameCovered(a, terms) {
				out[c.NodeID] = true
				break
			}
		}
	}
	return out
}

// nameCovered reports whether every content token of name appears in terms. An
// empty token set (a name that is all punctuation, or all words under
// GraphTermMinRunes) is NOT covered — vacuous truth here would anchor every
// such node on every query.
func nameCovered(name string, terms map[string]bool) bool {
	nt := ContentTerms(name)
	if len(nt) == 0 {
		return false
	}
	for t := range nt {
		if !terms[t] {
			return false
		}
	}
	return true
}

// RelevantEdge is the gate. A fact clears it only by independently passing one
// of two thresholds — not merely because retrieval ranked it, which it would
// do for the best of a bad set.
//
// Per-fact rather than "judge the top one and take the rest": the result is
// read as a list of claims, and a reader given four good facts and one
// unrelated one learns to distrust all five.
//
// It never errors and never panics, because both callers are on paths where a
// judgment is always required: the spawn path must not fail a spawn over a
// corrupt blob, and the search route must be able to label every fact it is
// about to return.
func RelevantEdge(e store.GraphEdge, queryVec []float32, terms map[string]bool) bool {
	if len(queryVec) > 0 && len(e.Embedding) > 0 {
		if cand, err := DecodeVector(e.Embedding); err == nil && len(cand) == len(queryVec) {
			// Both sides are unit-norm on write (Normalize), so the dot
			// product IS the cosine and GraphCosineFloor applies directly.
			if score, err := Dot(queryVec, cand); err == nil {
				return score >= GraphCosineFloor
			}
		}
		// A dimension mismatch means this row was embedded under a different
		// model or dims setting and graphPass has not re-embedded it yet.
		// Fall through to the lexical gate rather than dropping the fact: the
		// vector is stale, the sentence is not.
	}
	return TermOverlap(e.Fact, terms) >= GraphMinTermOverlap
}
