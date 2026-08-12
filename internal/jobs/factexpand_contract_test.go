package jobs

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// factexpand_contract_test.go — the LOGIC pins for slice S1b of
// artifacts/50-contract.md: the injection renderer once a fact line carries
// BOTH handles and the header advertises the verb that dereferences them.
//
// The three things this file holds are the three the contract freezes as
// literals: the line format, the header string, and the byte-cap arithmetic.
// Each is checked against a hand-computed expectation rather than against the
// implementation's own output — a rendered line is a wire format read by an
// agent in another process, so "whatever it produces" is not an answer.
//
// Refusals (a fact too long to fit, a header alone, an over-cap block) are the
// sibling edge suite's; what is here is the happy path and the arithmetic that
// makes the happy path fit.

// fxcLineRE decomposes one rendered fact line into its four fields. It is the
// contract's format written as a pattern, so a drift in ANY of the four
// separators fails to match rather than matching something else.
var fxcLineRE = regexp.MustCompile(`^- (.+) \[F(\d+) E(\d+), seen (\d+)x\]$`)

// fxcOldHeaderBytes is the byte length of the header as it stood before this
// slice — "\nKnown from past work (may be stale — verify before relying):\n",
// the 64 the contract's "old 600/64-byte arithmetic" names. It is a constant
// rather than a measurement because the OLD header no longer exists in the
// tree; the capacity claim is a comparison against something historical.
const fxcOldHeaderBytes = 64

// fxcOldCapBytes is the byte cap the header growth had to be bought out of.
const fxcOldCapBytes = 600

// fxcAdLine is the ONE line the header gains, quoted from the contract. Its 42
// bytes are the first term of the cap bump.
const fxcAdLine = "full fact for any [F#]: k graph get F<id>\n"

// TestFxcGraphContextHeaderIsTheFrozenString pins graphContextHeader verbatim.
// The contract states the whole constant, so this is a transcription check —
// and it is worth having because the header is the only place the block ever
// tells its reader that the handles are dereferenceable at all. A line that
// silently stopped being emitted would leave every [F#] in every brief as
// noise the agent has no verb for.
func TestFxcGraphContextHeaderIsTheFrozenString(t *testing.T) {
	const want = "\nKnown from past work (may be stale — verify before relying):\n" +
		"full fact for any [F#]: k graph get F<id>\n"

	if graphContextHeader != want {
		t.Fatalf("graphContextHeader = %q, want %q", graphContextHeader, want)
	}
	// Stated as two halves so a failure says WHICH half moved: the old line is
	// unchanged, and exactly one advertisement line is appended to it.
	if n := len(graphContextHeader); n != fxcOldHeaderBytes+len(fxcAdLine) {
		t.Errorf("header is %d bytes, want %d (the old %d plus the %d-byte advertisement)",
			n, fxcOldHeaderBytes+len(fxcAdLine), fxcOldHeaderBytes, len(fxcAdLine))
	}
	if !strings.HasSuffix(graphContextHeader, fxcAdLine) {
		t.Errorf("header does not END with the advertisement line: %q", graphContextHeader)
	}
	if n := strings.Count(graphContextHeader, "k graph get"); n != 1 {
		t.Errorf("header names the verb %d times, want exactly 1: %q", n, graphContextHeader)
	}
}

// TestFxcGraphContextByteCapArithmetic pins graphContextBytes and the two
// unchanged budget constants.
//
// 682 is not a round number chosen for headroom; the contract derives it as
// 600 + 42 + 40, where 42 is the advertisement line and 40 is five lines of
// eight bytes of handle growth ("F" + up to six digits + a space). Writing the
// sum out is what makes a future change to either term visible as an
// arithmetic failure rather than as a quietly tighter budget.
func TestFxcGraphContextByteCapArithmetic(t *testing.T) {
	if len(fxcAdLine) != 42 {
		t.Fatalf("the advertisement line is %d bytes, want 42 — the contract's arithmetic is built on it", len(fxcAdLine))
	}
	const perLineGrowth = 8 // "F" + up to 6 digits + " "
	want := fxcOldCapBytes + len(fxcAdLine) + graphContextFacts*perLineGrowth
	if graphContextBytes != want {
		t.Errorf("graphContextBytes = %d, want %d (= %d + %d + %d×%d)",
			graphContextBytes, want, fxcOldCapBytes, len(fxcAdLine), graphContextFacts, perLineGrowth)
	}
	if graphContextBytes != 682 {
		t.Errorf("graphContextBytes = %d, want the contract's 682", graphContextBytes)
	}
	if graphContextFacts != 5 {
		t.Errorf("graphContextFacts = %d, want 5 (unchanged)", graphContextFacts)
	}
	if graphFactRunes != 180 {
		t.Errorf("graphFactRunes = %d, want 180 (unchanged)", graphFactRunes)
	}
}

// TestFxcFactLineCarriesBothHandlesAndSeenCount is the headline of S1b: a
// rendered line carries F<edge id> and E<episode id> matching the edge it
// renders, and a sighting count of at least one.
//
// The contract's own worked example — edge 1091, episode 148111, occurrences 1
// — is the first row, so the format is pinned against the artifact set's own
// illustration rather than against a fixture invented here.
func TestFxcFactLineCarriesBothHandlesAndSeenCount(t *testing.T) {
	for _, c := range []struct {
		name string
		in   store.GraphEdge
		want string
	}{
		{
			"the contract's worked example",
			store.GraphEdge{ID: 1091, EpisodeID: 148111, Occurrences: 1,
				Fact: "kraken serve is the single writer of kraken.db"},
			"- kraken serve is the single writer of kraken.db. [F1091 E148111, seen 1x]\n",
		},
		{
			"a fact carrying its own period gains no second one",
			store.GraphEdge{ID: 4, EpisodeID: 40, Occurrences: 3,
				Fact: "a fact with its own period."},
			"- a fact with its own period. [F4 E40, seen 3x]\n",
		},
		{
			"a fact with no terminator gets one",
			store.GraphEdge{ID: 5, EpisodeID: 50, Occurrences: 12,
				Fact: "a fact without one"},
			"- a fact without one. [F5 E50, seen 12x]\n",
		},
		{
			// occ = max(1, e.Occurrences): a stored edge was seen at least
			// once whatever the column says, and "seen 0x" would read as a
			// fact nobody ever observed.
			"a zeroed count still reads as one sighting",
			store.GraphEdge{ID: 6, EpisodeID: 60, Occurrences: 0,
				Fact: "a fact with a zeroed count"},
			"- a fact with a zeroed count. [F6 E60, seen 1x]\n",
		},
		{
			"a multi-line fact is one-lined",
			store.GraphEdge{ID: 7, EpisodeID: 70, Occurrences: 2,
				Fact: "a fact\nsplit over  lines"},
			"- a fact split over lines. [F7 E70, seen 2x]\n",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := factLine(c.in); got != c.want {
				t.Fatalf("factLine(%+v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// TestFxcFactLineEndsAClauseCutFactWithTheEllipsisAlone pins the terminator
// rule's first branch: "if the ClauseCut result ends with `…`, it is used
// as-is (the ellipsis IS the terminator)".
//
// The fixture is 100 "ab" tokens, 299 runes once one-lined. At graphFactRunes
// 180 the budget is 179 and the floor 90; there is no clause marker anywhere,
// and the token spaces sit at rune 3k+2, so the largest within budget is 179 —
// keeping 60 whole tokens (59×"ab " plus a final "ab", exactly 179 runes) and
// appending the ellipsis for 180.
//
// The expected string is that arithmetic written out, so this case pins the
// JOIN between the cutter and the renderer without re-deriving it from
// core.ClauseCut's own output.
func TestFxcFactLineEndsAClauseCutFactWithTheEllipsisAlone(t *testing.T) {
	in := store.GraphEdge{ID: 9, EpisodeID: 90, Occurrences: 4, Fact: strings.Repeat("ab ", 100)}
	want := "- " + strings.Repeat("ab ", 59) + "ab…" + " [F9 E90, seen 4x]\n"

	got := factLine(in)
	if got != want {
		t.Fatalf("factLine on a 299-rune fact = %q, want %q", got, want)
	}
	// The two shapes the terminator rule exists to forbid.
	if strings.Contains(got, "….") {
		t.Errorf("the ellipsis gained a period after it: %q", got)
	}
	if strings.Contains(got, ".…") {
		t.Errorf("a period survived in front of the ellipsis: %q", got)
	}
}

// TestFxcRenderGraphBlockHeaderAppearsExactlyOncePerBlock pins the header's
// cardinality. It is the block's only hedge and its only pointer to the verb,
// so twice reads as two blocks and never reads as an unsourced instruction.
func TestFxcRenderGraphBlockHeaderAppearsExactlyOncePerBlock(t *testing.T) {
	edges := []store.GraphEdge{
		edge(1, "the throttled daemon pass reads transcripts past a cursor", 3),
		edge(2, "the daemon pass stamps its throttle before any early bail", 2),
		edge(3, "transcripts are append-only so cursors only move forward", 7),
	}
	block, rendered := renderGraphBlock(edges)
	if block == "" {
		t.Fatal("renderGraphBlock returned nothing for three short facts")
	}
	if !strings.HasPrefix(block, graphContextHeader) {
		t.Fatalf("block does not open with the header:\n%q", block)
	}
	if n := strings.Count(block, graphContextHeader); n != 1 {
		t.Errorf("header appears %d times, want 1:\n%q", n, block)
	}
	if n := strings.Count(block, fxcAdLine); n != 1 {
		t.Errorf("the advertisement line appears %d times, want 1:\n%q", n, block)
	}
	if len(rendered) != len(edges) {
		t.Errorf("rendered %d ids for %d facts, want all three", len(rendered), len(edges))
	}
}

// TestFxcRenderGraphBlockEmptyIsAByteIdenticalNoOp pins the other half of the
// header's contract: no facts means no block at all, "" rather than a header
// announcing that the graph knows something with nothing under it.
//
// This is the guarantee every cell spawn rests on — a job with no match must
// get the brief it would have got before the knowledge graph existed — so it
// is asserted as byte equality with the empty string, not as "short".
func TestFxcRenderGraphBlockEmptyIsAByteIdenticalNoOp(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []store.GraphEdge
	}{
		{"nil", nil},
		{"empty slice", []store.GraphEdge{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			block, rendered := renderGraphBlock(c.in)
			if block != "" {
				t.Errorf("renderGraphBlock(%s) = %q, want \"\"", c.name, block)
			}
			if len(rendered) != 0 {
				t.Errorf("renderGraphBlock(%s) reported %d rendered ids, want none", c.name, rendered)
			}
			if strings.Contains(block, "k graph get") {
				t.Errorf("an empty block still advertised the verb: %q", block)
			}
		})
	}
}

// TestFxcRenderGraphBlockRenderedIdsAreExactlyTheEmittedLines pins the
// contract recall's dedup depends on: the second return is the ids of the
// lines the block ACTUALLY carries, in the order it carries them — never the
// wider set the gate kept.
//
// A fact nobody read is not a fact delivered, and the skip set is durable: an
// id reported here that never reached the conversation is a memory withheld
// from that session forever.
//
// The candidates go through the GATE before the renderer, because that is the
// pair recall.go itself calls — keepRelevantFacts then renderGraphBlock, in
// that order with nothing between them — and the graphContextFacts cap is an
// obligation on the gate. renderGraphBlock's own skip-don't-break loop is
// bounded by BYTES and is unchanged by this slice, so handing it raw
// candidates would measure a rule the contract places somewhere else.
func TestFxcRenderGraphBlockRenderedIdsAreExactlyTheEmittedLines(t *testing.T) {
	// Shares "distinct", "daemon" and "pass" with every fixture sentence, which
	// is more than the gate's lexical arm asks for when no vector exists.
	const spec = "the daemon pass and its distinct facts"

	var edges []store.GraphEdge
	for i := int64(1); i <= 12; i++ {
		// edge() sets EpisodeID = id*10, which is what lets each line's two
		// handles be checked against each other.
		edges = append(edges, edge(i, fmt.Sprintf("distinct fact number %02d about the daemon pass", i), i))
	}

	kept := keepRelevantFacts(edges, nil, core.ContentTerms(spec), nil, nil)
	if len(kept) == 0 {
		t.Fatal("the gate rejected all twelve fixture facts — they share several content words with the spec, so there is nothing left to measure")
	}

	block, rendered := renderGraphBlock(kept)
	if block == "" {
		t.Fatal("renderGraphBlock returned nothing for twelve short facts")
	}

	body := strings.TrimSuffix(strings.TrimPrefix(block, graphContextHeader), "\n")
	lines := strings.Split(body, "\n")
	if len(lines) > graphContextFacts {
		t.Fatalf("block carries %d lines, over the %d cap:\n%s", len(lines), graphContextFacts, block)
	}
	if len(rendered) != len(lines) {
		t.Fatalf("rendered %d ids for %d emitted lines — the two must be the same set", len(rendered), len(lines))
	}

	for i, line := range lines {
		m := fxcLineRE.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("line %d does not match the frozen format: %q", i, line)
		}
		factID, err := strconv.ParseInt(m[2], 10, 64)
		if err != nil {
			t.Fatalf("line %d: unparseable F handle %q", i, m[2])
		}
		epID, err := strconv.ParseInt(m[3], 10, 64)
		if err != nil {
			t.Fatalf("line %d: unparseable E handle %q", i, m[3])
		}
		seen, err := strconv.ParseInt(m[4], 10, 64)
		if err != nil {
			t.Fatalf("line %d: unparseable sighting count %q", i, m[4])
		}
		if rendered[i] != factID {
			t.Errorf("line %d carries F%d but rendered[%d] = %d — the ids must line up with the lines", i, factID, i, rendered[i])
		}
		// The edge fixture's own invariant: this line's episode handle is the
		// episode of the edge whose id the line names, not of some other edge.
		if epID != factID*10 {
			t.Errorf("line %d pairs F%d with E%d, but edge %d's episode is %d", i, factID, epID, factID, factID*10)
		}
		if seen < 1 {
			t.Errorf("line %d reports seen %dx, want at least 1", i, seen)
		}
	}
}

// fxcOldFactLine reproduces the PRE-slice line format, which the capacity
// claim below is a comparison against. It exists for the same reason
// fxcOldHeaderBytes does: the thing being compared to no longer exists in the
// tree, so the test has to carry it.
func fxcOldFactLine(e store.GraphEdge) string {
	occ := e.Occurrences
	if occ < 1 {
		occ = 1
	}
	return fmt.Sprintf("- %s. [E%d, seen %dx]\n", e.Fact, e.EpisodeID, occ)
}

// TestFxcRenderGraphBlockPreservesFactCapacity pins the sentence the cap bump
// exists to make true: "any fact set that fit under 600 with the old format
// fits under 682 with the new one."
//
// The fixture is the tightest set that satisfies the premise — five facts with
// six-digit edge and episode ids (the widest handle the 8-byte per-line growth
// budgets for) and 84-rune sentences, which renders to 599 bytes in the old
// format, one under the old cap. If the new arithmetic is short by even a
// byte, the fifth fact is dropped and this fails with a count rather than with
// a size, which is the failure that actually matters: a cap that silently
// costs a fact looks exactly like a graph that had nothing more to say.
func TestFxcRenderGraphBlockPreservesFactCapacity(t *testing.T) {
	var edges []store.GraphEdge
	for i := 0; i < graphContextFacts; i++ {
		edges = append(edges, store.GraphEdge{
			ID:          int64(100001 + i),
			EpisodeID:   int64(200001 + i),
			Occurrences: 1,
			Fact:        strings.Repeat("q", 80) + fmt.Sprintf("%04d", i), // 84 runes, distinct
		})
	}

	// The premise: this set fit under the OLD cap.
	oldTotal := fxcOldHeaderBytes
	for _, e := range edges {
		oldTotal += len(fxcOldFactLine(e))
	}
	if oldTotal > fxcOldCapBytes {
		t.Fatalf("fixture is %d bytes in the old format, over the old %d-byte cap — "+
			"the capacity claim is about sets that DID fit, so this fixture proves nothing", oldTotal, fxcOldCapBytes)
	}

	block, rendered := renderGraphBlock(edges)
	if len(rendered) != len(edges) {
		t.Fatalf("a set that fit under the old %d-byte cap (%d bytes) lost %d of its %d facts under the new %d-byte cap "+
			"(block is %d bytes):\n%s",
			fxcOldCapBytes, oldTotal, len(edges)-len(rendered), len(edges), graphContextBytes, len(block), block)
	}
	if len(block) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(block), graphContextBytes, block)
	}
	// And every one of them is a whole line, since a set that "fits" by being
	// cut mid-fact would satisfy the count and lie about the facts.
	for _, e := range edges {
		want := factLine(e)
		if !strings.Contains(block, want) {
			t.Errorf("block does not carry edge %d's line verbatim (%q):\n%s", e.ID, want, block)
		}
	}
}
