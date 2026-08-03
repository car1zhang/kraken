package core

import (
	"math"
	"testing"

	"kraken/internal/store"
)

// relevance_edge_test.go — the EDGE dimension of internal/core/relevance.go:
// boundaries, undecodable input, degradations and the refusals that keep a
// verdict from being invented out of a corrupt row.
//
// Every identifier here is prefixed `kgedge` because a sibling author shares
// this package's test binary; an unprefixed helper collides and takes the whole
// package down rather than one file.
//
// The load-bearing property of RelevantEdge is that it NEVER errors and never
// panics: it is called once per returned fact inside a search route that has
// already succeeded, so any input it cannot make sense of must degrade to the
// lexical fallback (or to "weak") rather than fail the request. Half the cases
// below are corrupt-blob shapes for exactly that reason.

// kgedgeUnitPair builds a 2-dim unit vector whose dot product with the query
// vector [1,0] is exactly c. Unit-norm on BOTH sides means the verdict is the
// same whether RelevantEdge dots the vectors as-stored (they are normalized on
// write — see vector.go) or re-normalizes them first: the float32 rounding of
// the second component moves the norm by ~1e-7, which is three orders of
// magnitude under the margins these cases use.
func kgedgeUnitPair(c float64) []float32 {
	s := math.Sqrt(1 - c*c)
	return []float32{float32(c), float32(s)}
}

// kgedgeQueryVec is the query side of every cosine case: the first basis
// vector, so the dot product reads straight off the edge's first component.
func kgedgeQueryVec() []float32 { return []float32{1, 0} }

// kgedgeStrangerFact shares NO content word with kgedgeQueryTerms, so any case
// using it has the same verdict under the cosine branch and under the lexical
// fallback. That is deliberate for the corrupt-vector cases: they pin "never
// panics, verdict is deterministic" without also betting on which branch a
// NaN or a zero vector lands in.
const kgedgeStrangerFact = "wholly unrelated sentence about breakfast pastry"

// kgedgeQueryTerms is the term set the lexical fallback judges against:
// three words of the query, so a fact sharing any two of them sits exactly at
// GraphMinTermOverlap.
func kgedgeQueryTerms() map[string]bool {
	return map[string]bool{"kraken": true, "spawns": true, "nightly": true}
}

// TestKgedgeContentTermsBoundaries covers the rune-length gate and the
// splitting rule at their edges. The gate is stated in RUNES, and a byte-length
// implementation passes every ASCII case while silently dropping (or keeping)
// exactly the wrong multi-byte words — which is why the CJK pair is here.
func TestKgedgeContentTermsBoundaries(t *testing.T) {
	t.Run("empty string has no terms", func(t *testing.T) {
		if got := ContentTerms(""); len(got) != 0 {
			t.Errorf("ContentTerms(\"\") = %v, want empty", got)
		}
		if got := ContentTerms("   \t\n  "); len(got) != 0 {
			t.Errorf("ContentTerms(whitespace) = %v, want empty", got)
		}
		if got := ContentTerms("-,.;:!?()[]"); len(got) != 0 {
			t.Errorf("ContentTerms(punctuation only) = %v, want empty", got)
		}
	})

	t.Run("rune length boundary", func(t *testing.T) {
		// GraphTermMinRunes is the SHORTEST word kept, so the word one rune
		// under it is dropped and the word exactly at it is kept.
		short := "abc"
		exact := "abcd"
		got := ContentTerms(short + " " + exact)
		if got[short] {
			t.Errorf("a %d-rune word was kept; GraphTermMinRunes = %d", len([]rune(short)), GraphTermMinRunes)
		}
		if !got[exact] {
			t.Errorf("a word of exactly GraphTermMinRunes (%d) runes was dropped: %v", GraphTermMinRunes, got)
		}
	})

	t.Run("length is counted in runes not bytes", func(t *testing.T) {
		// "知知知" is 3 runes / 9 bytes: over the byte gate, under the rune
		// gate. "知知知知" is 4 runes / 12 bytes and must be kept.
		got := ContentTerms("知知知 知知知知")
		if got["知知知"] {
			t.Error("a 3-rune (9-byte) word was kept — the gate is counting bytes, not runes")
		}
		if !got["知知知知"] {
			t.Errorf("a 4-rune word was dropped: %v", got)
		}
		if len(got) != 1 {
			t.Errorf("ContentTerms = %v, want exactly the 4-rune word", got)
		}
	})

	t.Run("digits are word characters", func(t *testing.T) {
		// The split is on non-letter/non-DIGIT, so a version number is one
		// word rather than two fragments, and it passes the gate on its own
		// rune count: "v2" is 2 runes and dies, "2026" is 4 and lives.
		got := ContentTerms("v2 2026")
		if got["v2"] {
			t.Error("\"v2\" (2 runes) survived the length gate")
		}
		if !got["2026"] {
			t.Errorf("an all-digit 4-rune word was dropped: %v", got)
		}
	})

	t.Run("every non-alphanumeric rune splits", func(t *testing.T) {
		got := ContentTerms("graph-search/inner_join\tfused,ranks")
		for _, want := range []string{"graph", "search", "inner", "join", "fused", "ranks"} {
			if !got[want] {
				t.Errorf("%q missing from %v — a separator did not split", want, got)
			}
		}
		if got["graph-search"] || got["inner_join"] {
			t.Errorf("a separator was treated as a word character: %v", got)
		}
	})

	t.Run("terms are distinct and case folded", func(t *testing.T) {
		got := ContentTerms("Kraken KRAKEN kraken kRaKeN")
		if len(got) != 1 || !got["kraken"] {
			t.Errorf("ContentTerms = %v, want the single lowercase key \"kraken\"", got)
		}
		// Case folding must be per-RUNE, not per-byte: a byte-level lowercase
		// leaves a multi-byte capital alone and the two spellings become two
		// different terms.
		upper := ContentTerms("CAFÉS")
		lower := ContentTerms("cafés")
		if len(upper) != 1 || len(lower) != 1 {
			t.Fatalf("ContentTerms(CAFÉS) = %v, ContentTerms(cafés) = %v, want one term each", upper, lower)
		}
		for k := range upper {
			if !lower[k] {
				t.Errorf("case folding is byte-wise: %v and %v do not share a key", upper, lower)
			}
		}
	})
}

// TestKgedgeTermOverlapBoundaries covers the counting rule's edges: an empty
// term set, a repeated word, and a fact word that is itself under the length
// gate. Each one, counted wrong, moves a fact across the weak/strong line
// without any other symptom.
func TestKgedgeTermOverlapBoundaries(t *testing.T) {
	t.Run("empty and nil term sets count nothing", func(t *testing.T) {
		if got := TermOverlap("kraken spawns nightly", nil); got != 0 {
			t.Errorf("TermOverlap(_, nil) = %d, want 0", got)
		}
		if got := TermOverlap("kraken spawns nightly", map[string]bool{}); got != 0 {
			t.Errorf("TermOverlap(_, empty) = %d, want 0", got)
		}
	})

	t.Run("empty fact counts nothing", func(t *testing.T) {
		if got := TermOverlap("", kgedgeQueryTerms()); got != 0 {
			t.Errorf("TermOverlap(\"\", terms) = %d, want 0", got)
		}
		if got := TermOverlap("   ", kgedgeQueryTerms()); got != 0 {
			t.Errorf("TermOverlap(blank, terms) = %d, want 0", got)
		}
	})

	t.Run("a repeated word counts once", func(t *testing.T) {
		// Without the distinctness rule this fact scores 4 and clears
		// GraphMinTermOverlap on the strength of one word said four times.
		if got := TermOverlap("kraken kraken kraken kraken", kgedgeQueryTerms()); got != 1 {
			t.Errorf("TermOverlap = %d, want 1 — a repeated word is one shared word", got)
		}
		if got := TermOverlap("Kraken kraken KRAKEN spawns", kgedgeQueryTerms()); got != 2 {
			t.Errorf("TermOverlap = %d, want 2 (kraken once, spawns once)", got)
		}
	})

	t.Run("fact words under the rune gate are not content words", func(t *testing.T) {
		// The term set is arbitrary input — a caller could hold a short key.
		// The FACT side still applies GraphTermMinRunes, so a 3-rune word
		// matches nothing.
		terms := map[string]bool{"api": true, "key": true}
		if got := TermOverlap("api key api key", terms); got != 0 {
			t.Errorf("TermOverlap = %d, want 0 — words under GraphTermMinRunes are not content words", got)
		}
	})

	t.Run("exactly GraphMinTermOverlap is the strong side of the line", func(t *testing.T) {
		terms := kgedgeQueryTerms()
		one := TermOverlap("kraken hums politely", terms)
		two := TermOverlap("kraken spawns politely", terms)
		if one != 1 {
			t.Fatalf("one-word overlap = %d, want 1", one)
		}
		if two != GraphMinTermOverlap {
			t.Fatalf("two-word overlap = %d, want GraphMinTermOverlap (%d)", two, GraphMinTermOverlap)
		}
		if RelevantEdge(store.GraphEdge{Fact: "kraken hums politely"}, nil, terms) {
			t.Error("an overlap of 1 was judged strong; the gate is >= GraphMinTermOverlap")
		}
		if !RelevantEdge(store.GraphEdge{Fact: "kraken spawns politely"}, nil, terms) {
			t.Error("an overlap of exactly GraphMinTermOverlap was judged weak")
		}
	})
}

// TestKgedgeRelevantEdgeCosineBoundary pins the floor comparison itself, from
// both sides with a margin far wider than float32 rounding. Both facts share
// nothing lexically, so the fallback cannot mask a wrong comparison.
func TestKgedgeRelevantEdgeCosineBoundary(t *testing.T) {
	q := kgedgeQueryVec()
	terms := kgedgeQueryTerms()

	above := store.GraphEdge{Fact: kgedgeStrangerFact, Embedding: EncodeVector(kgedgeUnitPair(float64(GraphCosineFloor) + 0.0005))}
	below := store.GraphEdge{Fact: kgedgeStrangerFact, Embedding: EncodeVector(kgedgeUnitPair(float64(GraphCosineFloor) - 0.0005))}

	if !RelevantEdge(above, q, terms) {
		t.Errorf("a cosine just ABOVE GraphCosineFloor (%v) was judged weak", GraphCosineFloor)
	}
	if RelevantEdge(below, q, terms) {
		t.Errorf("a cosine just BELOW GraphCosineFloor (%v) was judged strong", GraphCosineFloor)
	}
}

// TestKgedgeRelevantEdgeCosineWinsOverLexical is the "the lexical fallback is
// NOT consulted" half of the contract, in both directions. Without it the two
// judgments quietly OR together and the floor stops meaning anything: a fact
// the embedder placed far from the query gets in on two shared words.
func TestKgedgeRelevantEdgeCosineWinsOverLexical(t *testing.T) {
	q := kgedgeQueryVec()
	terms := kgedgeQueryTerms()

	// Lexically a perfect hit (3 shared words), geometrically nowhere near.
	lexicalHit := store.GraphEdge{
		Fact:      "kraken spawns nightly",
		Embedding: EncodeVector(kgedgeUnitPair(0.5)),
	}
	if RelevantEdge(lexicalHit, q, terms) {
		t.Error("a below-floor edge was rescued by term overlap — the fallback must not be consulted when a usable vector exists")
	}

	// The mirror: nothing lexical in common, but the vectors agree.
	vectorHit := store.GraphEdge{
		Fact:      kgedgeStrangerFact,
		Embedding: EncodeVector(kgedgeUnitPair(0.95)),
	}
	if !RelevantEdge(vectorHit, q, terms) {
		t.Error("an above-floor edge was judged weak because it shared no words — the cosine branch decides alone")
	}
}

// TestKgedgeRelevantEdgeFallsBackOnUnusableVectors is the degradation table:
// every shape of "there is no comparable pair of vectors here" must land on the
// lexical fallback rather than on a verdict of its own. Each case is run twice
// — once with a fact that clears GraphMinTermOverlap and once with a stranger —
// so the assertion is that the FALLBACK ran, not merely that some answer came
// back.
func TestKgedgeRelevantEdgeFallsBackOnUnusableVectors(t *testing.T) {
	terms := kgedgeQueryTerms()
	strongFact := "kraken spawns nightly in a worktree"

	cases := []struct {
		name  string
		query []float32
		blob  []byte
	}{
		{"nil query vector", nil, EncodeVector(kgedgeUnitPair(0.99))},
		{"empty query vector", []float32{}, EncodeVector(kgedgeUnitPair(0.99))},
		{"nil embedding", kgedgeQueryVec(), nil},
		{"empty embedding", kgedgeQueryVec(), []byte{}},
		// Not a multiple of 4: DecodeVector calls this a corrupt row rather
		// than a shorter vector, and so must RelevantEdge.
		{"undecodable blob (length 3)", kgedgeQueryVec(), []byte{0x01, 0x02, 0x03}},
		{"undecodable blob (length 5)", kgedgeQueryVec(), []byte{0x01, 0x02, 0x03, 0x04, 0x05}},
		// A model/dims change mid-corpus leaves old vectors behind; comparing
		// across dimensions is the caller's bug, never a similarity.
		{"edge has more dims than the query", kgedgeQueryVec(), EncodeVector([]float32{1, 0, 0})},
		{"edge has fewer dims than the query", kgedgeQueryVec(), EncodeVector([]float32{1})},
		{"both nil", nil, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			weak := store.GraphEdge{Fact: kgedgeStrangerFact, Embedding: tc.blob}
			if RelevantEdge(weak, tc.query, terms) {
				t.Error("a fact sharing no content words was judged strong — the fallback did not run")
			}
			strong := store.GraphEdge{Fact: strongFact, Embedding: tc.blob}
			if !RelevantEdge(strong, tc.query, terms) {
				t.Error("a fact sharing 3 content words was judged weak — the fallback did not run")
			}
		})
	}
}

// TestKgedgeRelevantEdgeNoUsableVectorAndNoTerms is the doubly-degraded state a
// fresh install searching a corpus with no embeddings is in: no query vector
// and a query whose every word is under the rune gate. The answer is "weak",
// which is the honest one — nothing was compared.
func TestKgedgeRelevantEdgeNoUsableVectorAndNoTerms(t *testing.T) {
	for _, terms := range []map[string]bool{nil, {}} {
		e := store.GraphEdge{Fact: "kraken spawns nightly"}
		if RelevantEdge(e, nil, terms) {
			t.Errorf("terms=%v: a fact was judged strong against an empty term set", terms)
		}
	}
}

// TestKgedgeRelevantEdgeSurvivesPoisonedVectors: a torn write decodes cleanly
// into NaNs, and a zero vector has no direction to compare. Neither may panic
// and neither may come back strong — a NaN fails every comparison, which is
// precisely how a poisoned row would otherwise take the top slot (the same
// hazard TopK guards in vector.go). The facts share nothing lexically, so the
// expected verdict is the same whichever branch the implementation lands on.
func TestKgedgeRelevantEdgeSurvivesPoisonedVectors(t *testing.T) {
	nan := float32(math.NaN())
	inf := float32(math.Inf(1))
	q := kgedgeQueryVec()

	for _, tc := range []struct {
		name string
		vec  []float32
	}{
		{"NaN components", []float32{nan, nan}},
		{"one NaN component", []float32{nan, 0}},
		{"+Inf components", []float32{inf, inf}},
		{"zero vector", []float32{0, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := store.GraphEdge{Fact: kgedgeStrangerFact, Embedding: EncodeVector(tc.vec)}
			if RelevantEdge(e, q, kgedgeQueryTerms()) {
				t.Error("a poisoned stored vector produced a STRONG verdict")
			}
		})
	}

	// A poisoned QUERY vector is the same hazard from the other side.
	e := store.GraphEdge{Fact: kgedgeStrangerFact, Embedding: EncodeVector(kgedgeUnitPair(0.99))}
	if RelevantEdge(e, []float32{nan, nan}, kgedgeQueryTerms()) {
		t.Error("a NaN QUERY vector produced a strong verdict")
	}
	if RelevantEdge(e, []float32{0, 0}, kgedgeQueryTerms()) {
		t.Error("a zero QUERY vector produced a strong verdict")
	}
}

// TestKgedgeRelevantEdgeZeroValueEdgeIsWeak: the zero GraphEdge is what a
// caller iterating a partially-filled slice hands over. Blank fact, no
// embedding, no panic, no strong verdict.
func TestKgedgeRelevantEdgeZeroValueEdgeIsWeak(t *testing.T) {
	if RelevantEdge(store.GraphEdge{}, nil, kgedgeQueryTerms()) {
		t.Error("the zero-value edge was judged strong")
	}
	if RelevantEdge(store.GraphEdge{}, kgedgeQueryVec(), kgedgeQueryTerms()) {
		t.Error("the zero-value edge was judged strong with a query vector present")
	}
	if RelevantEdge(store.GraphEdge{}, nil, nil) {
		t.Error("the zero-value edge was judged strong against a nil term set")
	}
}

// TestKgedgeRelevanceConstantsAreTheOneSourceOfTruth guards the reason S1a
// exists at all: the spawn path and the search route must read the SAME floor.
// A constant edited to a different value here is the whole regression this
// slice was written to prevent, and it is invisible in behaviour until the two
// paths disagree about one fact.
func TestKgedgeRelevanceConstantsAreTheOneSourceOfTruth(t *testing.T) {
	if GraphCosineFloor != 0.72 {
		t.Errorf("GraphCosineFloor = %v, want 0.72 — the spawn path's floor", GraphCosineFloor)
	}
	if GraphMinTermOverlap != 2 {
		t.Errorf("GraphMinTermOverlap = %d, want 2", GraphMinTermOverlap)
	}
	if GraphTermMinRunes != 4 {
		t.Errorf("GraphTermMinRunes = %d, want 4", GraphTermMinRunes)
	}
}
