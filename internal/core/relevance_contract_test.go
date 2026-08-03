package core

import (
	"math"
	"sort"
	"strings"
	"testing"

	"kraken/internal/store"
)

// relevance_contract_test.go — the CORRECTNESS pins for slice S1a of
// artifacts/50-contract.md: the shared relevance judgment that the spawn path
// and the search route must now both read from one place.
//
// Every assertion below is transcribed from a contract ROW, not from an
// implementation. The three constants are pinned by value because they ARE
// the contract — "the one cosine relevance floor" is only one floor if both
// callers read the same number, and a test that computed the expected floor
// from the constant would pass no matter what the constant became.
//
// Boundary behaviour (an exact-0.72 dot, a negative overlap, a zero vector)
// is deliberately NOT here — that dimension belongs to the edge-path author.
// What is here is the selection rule: WHICH of the two judgments runs, and
// that each one answers the way the contract says on unambiguous input.

// kgcontractSortedTerms renders a ContentTerms result as a stable, printable
// list so a mismatch names the words rather than a map address.
func kgcontractSortedTerms(m map[string]bool) []string {
	out := []string{}
	for k, v := range m {
		if v {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func kgcontractJoin(ss []string) string { return strings.Join(ss, ",") }

// kgcontractUnit builds a 3-dimensional vector whose first component is x and
// whose second carries the rest of the unit norm, so its dot product with
// (1,0,0) is exactly x and normalizing it is a no-op. This is how a cosine of
// a chosen magnitude is expressed without depending on how RelevantEdge
// normalizes.
func kgcontractUnit(x float32) []float32 {
	y := float32(math.Sqrt(float64(1 - x*x)))
	return []float32{x, y, 0}
}

var kgcontractQueryAxis = []float32{1, 0, 0}

// TestKgcontractRelevanceConstantsAreTheContractValues pins the three numbers
// by literal value. S1a names each one; the point of hoisting them into core
// was that two call sites stop carrying their own copy, and a drifting value
// here silently re-splits them.
func TestKgcontractRelevanceConstantsAreTheContractValues(t *testing.T) {
	if GraphCosineFloor != 0.72 {
		t.Errorf("GraphCosineFloor = %v, want 0.72 — the one floor shared by the spawn path and the search route", GraphCosineFloor)
	}
	if GraphMinTermOverlap != 2 {
		t.Errorf("GraphMinTermOverlap = %v, want 2 distinct shared content words", GraphMinTermOverlap)
	}
	if GraphTermMinRunes != 4 {
		t.Errorf("GraphTermMinRunes = %v, want 4 — the shortest word ContentTerms counts", GraphTermMinRunes)
	}
}

// TestKgcontractContentTermsExampleTable is the contract's own worked example
// plus the rules it demonstrates. The headline row is executed verbatim from
// S1a: "Kraken's graph-search v2" → {kraken, graph, search}. Both exclusions
// in it are load-bearing — the possessive "s" and the "v2" are dropped for the
// SAME reason (under 4 runes), which is what makes the split rule "any
// non-letter/non-digit rune" rather than "whitespace".
func TestKgcontractContentTermsExampleTable(t *testing.T) {
	for _, c := range []struct {
		name, in string
		want     []string
	}{
		{
			// The contract's executed example, character for character.
			"contract example",
			"Kraken's graph-search v2",
			[]string{"graph", "kraken", "search"},
		},
		{"empty string", "", nil},
		{"only short words", "a to the v2 of", nil},
		{
			// Distinctness: a word repeated in the input is ONE term. The map
			// is a set, and TermOverlap's "distinct" promise rests on it.
			"repeats collapse",
			"kraken kraken KRAKEN Kraken",
			[]string{"kraken"},
		},
		{
			// Case folds down, never up: the whole gate compares lowercased
			// words on both sides.
			"uppercase folds",
			"KRAKEN DAEMON",
			[]string{"daemon", "kraken"},
		},
		{
			// Digits are content, not separators — that is precisely why "v2"
			// above is excluded by LENGTH and not by containing a digit.
			"digits are word characters",
			"zeus1 spawns 2026 cells",
			[]string{"2026", "cells", "spawns", "zeus1"},
		},
		{
			// Punctuation of every shape splits: slashes, dots, underscores,
			// parens. A ref like `carl/graph-retrieval-fixes-170` is the kind
			// of string this corpus is full of.
			"punctuation splits",
			// "carl" is exactly GraphTermMinRunes runes and so IS a term; "170"
			// (3) and "md" (2) are not. The inclusive boundary is the point.
			"carl/graph-retrieval_fixes(170).md",
			[]string{"carl", "fixes", "graph", "retrieval"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := kgcontractSortedTerms(ContentTerms(c.in))
			if kgcontractJoin(got) != kgcontractJoin(c.want) {
				t.Errorf("ContentTerms(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

// TestKgcontractTermOverlapCountsDistinctSharedWords pins the counter's three
// stated properties: DISTINCT words of the fact, zero against an empty term
// set, and a repeat in the fact counting once. The third is the one a naive
// implementation gets wrong — counting occurrences rather than words lets a
// single word repeated twice clear GraphMinTermOverlap on its own, which
// would call a one-word coincidence a strong match.
func TestKgcontractTermOverlapCountsDistinctSharedWords(t *testing.T) {
	terms := ContentTerms("kraken daemon spawns zeus cells")

	for _, c := range []struct {
		name, fact string
		want       int
	}{
		{"two distinct shared words", "kraken daemon writes the ledger", 2},
		{"one shared word", "zeus rides the bicycle downhill", 1},
		{"no shared words", "bicycle repair needs patience", 0},
		{
			// The repeat rule, stated in S1a: "a word repeated in `fact`
			// counts once".
			"a repeated word counts once",
			"kraken kraken kraken kraken",
			1,
		},
		{
			// Short words in the FACT are not content words either, so they
			// cannot contribute — the ≥4-rune gate applies to both sides.
			"short words in the fact do not count",
			"a to of kraken",
			1,
		},
		{"empty fact", "", 0},
		{"all five", "kraken daemon spawns zeus cells", 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := TermOverlap(c.fact, terms); got != c.want {
				t.Errorf("TermOverlap(%q, <query terms>) = %d, want %d", c.fact, got, c.want)
			}
		})
	}

	// An empty term set is 0 for every fact — stated explicitly, and the state
	// the gate is in for a query made entirely of short words.
	for _, empty := range []map[string]bool{nil, {}, ContentTerms("a to of")} {
		if got := TermOverlap("kraken daemon spawns zeus cells", empty); got != 0 {
			t.Errorf("TermOverlap against an empty term set = %d, want 0", got)
		}
	}
}

// TestKgcontractRelevantEdgePrefersCosineWhenItCan is the SELECTION half of
// S1a's last row: with a usable query vector and a decodable embedding of
// equal dimension, the verdict is the cosine's alone and "the lexical fallback
// is NOT consulted".
//
// Both cases are rigged so the two judgments DISAGREE. That is the only way to
// observe which one ran: a fact with zero lexical overlap that clears the
// floor must be relevant, and a fact with plenty of overlap that misses the
// floor must not be. An implementation that consulted both and OR'd them would
// pass a test where they agree and fail both of these.
func TestKgcontractRelevantEdgePrefersCosineWhenItCan(t *testing.T) {
	terms := ContentTerms("kraken daemon spawns zeus cells")

	// Clears the floor (0.75 ≥ 0.72), shares NOT ONE content word.
	above := store.GraphEdge{
		Fact:      "porcupines migrate through basalt quarries",
		Embedding: EncodeVector(kgcontractUnit(0.75)),
	}
	if TermOverlap(above.Fact, terms) != 0 {
		t.Fatalf("fixture is wrong: the above-floor fact must have zero lexical overlap")
	}
	if !RelevantEdge(above, kgcontractQueryAxis, terms) {
		t.Error("a fact whose cosine clears the floor was judged irrelevant — the vector verdict must stand on its own")
	}

	// Misses the floor (0.69 < 0.72) while sharing four content words.
	below := store.GraphEdge{
		Fact:      "kraken daemon spawns zeus",
		Embedding: EncodeVector(kgcontractUnit(0.69)),
	}
	if TermOverlap(below.Fact, terms) < GraphMinTermOverlap {
		t.Fatalf("fixture is wrong: the below-floor fact must clear the lexical gate")
	}
	if RelevantEdge(below, kgcontractQueryAxis, terms) {
		t.Error("a fact below the cosine floor was rescued by the lexical fallback — with a usable vector the fallback must not be consulted")
	}

	// The floor sits strictly between these two, which is what "the one cosine
	// relevance floor" means operationally: not 0, not 1, and the same number
	// the constant names.
	if GraphCosineFloor <= 0.69 || GraphCosineFloor > 0.75 {
		t.Errorf("GraphCosineFloor = %v, but the two cases above bracket it in (0.69, 0.75]", GraphCosineFloor)
	}
}

// TestKgcontractRelevantEdgeFallsBackToTermOverlap is the other half: each of
// the four stated conditions that make the vector verdict unavailable —
// no query vector, no stored embedding, an undecodable blob, a dimension
// mismatch — must land on `TermOverlap >= GraphMinTermOverlap`.
//
// Each condition is checked BOTH ways. A test that only asserted the false
// side would pass against an implementation that hard-coded "unusable vector
// means irrelevant", which is the exact regression this row exists to prevent:
// a corpus with no API key has no vectors at all, so that implementation would
// mark every fact in the graph weak.
func TestKgcontractRelevantEdgeFallsBackToTermOverlap(t *testing.T) {
	terms := ContentTerms("kraken daemon spawns zeus cells")

	// Two facts on opposite sides of the ≥2 gate, with no embedding of their
	// own — the embedding is supplied per-case below.
	const strongFact = "kraken daemon writes the ledger" // overlap 2
	const weakFact = "zeus rides the bicycle downhill"   // overlap 1

	if TermOverlap(strongFact, terms) != GraphMinTermOverlap {
		t.Fatalf("fixture is wrong: strongFact overlap = %d, want exactly %d", TermOverlap(strongFact, terms), GraphMinTermOverlap)
	}
	if TermOverlap(weakFact, terms) != GraphMinTermOverlap-1 {
		t.Fatalf("fixture is wrong: weakFact overlap = %d, want %d", TermOverlap(weakFact, terms), GraphMinTermOverlap-1)
	}

	for _, c := range []struct {
		name     string
		queryVec []float32
		emb      []byte
	}{
		{
			// No embedder configured anywhere in the process: the ordinary
			// state of a corpus with no OPENAI_API_KEY, and the state the
			// search route's tests run in.
			"nil query vector",
			nil,
			EncodeVector(kgcontractUnit(0.95)),
		},
		{
			// A vector exists for the query but this row was never embedded —
			// embedding is asynchronous, so every freshly written edge is here
			// for a while.
			"edge never embedded",
			kgcontractQueryAxis,
			nil,
		},
		{"edge embedded with an empty blob", kgcontractQueryAxis, []byte{}},
		{
			// A blob whose length is not a multiple of 4 cannot decode.
			// DecodeVector calls that corrupt; RelevantEdge must call it
			// "no vector verdict available" and judge lexically anyway.
			"undecodable blob",
			kgcontractQueryAxis,
			[]byte{1, 2, 3},
		},
		{
			// Written by a different embedder, or a different dims setting.
			// Four floats against a three-float query.
			"dimension mismatch",
			kgcontractQueryAxis,
			EncodeVector([]float32{1, 0, 0, 0}),
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if !RelevantEdge(store.GraphEdge{Fact: strongFact, Embedding: c.emb}, c.queryVec, terms) {
				t.Errorf("a fact sharing %d distinct content words was judged irrelevant — the lexical fallback must decide here", GraphMinTermOverlap)
			}
			if RelevantEdge(store.GraphEdge{Fact: weakFact, Embedding: c.emb}, c.queryVec, terms) {
				t.Errorf("a fact sharing only %d content word was judged relevant", GraphMinTermOverlap-1)
			}
		})
	}
}

// TestKgcontractRelevantEdgeAnswersRatherThanFailing pins "never panics, never
// errors" on the inputs a caller genuinely reaches it with. The signature has
// no error to return, so the only failure modes available to it are a panic
// and a wrong answer; this covers the first.
func TestKgcontractRelevantEdgeAnswersRatherThanFailing(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("RelevantEdge panicked: %v", r)
		}
	}()

	terms := ContentTerms("kraken daemon spawns zeus cells")
	for _, c := range []struct {
		name      string
		e         store.GraphEdge
		q         []float32
		tm        map[string]bool
		wantFalse bool // asserted only where the contract fixes the answer
	}{
		{"zero edge, nothing else", store.GraphEdge{}, nil, nil, true},
		{"zero edge with terms", store.GraphEdge{}, nil, terms, true},
		{"blank fact, empty terms", store.GraphEdge{Fact: "   "}, nil, map[string]bool{}, true},
		{"empty query vector slice", store.GraphEdge{Fact: "kraken daemon"}, []float32{}, terms, false},
		{"garbage blob, nil terms", store.GraphEdge{Fact: "x", Embedding: []byte{7}}, kgcontractQueryAxis, nil, true},
	} {
		// The assertion that matters here is that the call RETURNS at all; the
		// value is checked wherever the contract fixes it (a fact with no
		// content words overlaps nothing, so the fallback answers false).
		got := RelevantEdge(c.e, c.q, c.tm)
		if c.wantFalse && got {
			t.Errorf("%s: RelevantEdge = true, want false — there are no shared content words to find", c.name)
		}
	}
}
