package jobs

import (
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// factexpand_edge_test.go — the injection renderer's refusals and budget
// boundaries under the two-tier memory contract (50-contract.md, "S1b —
// renderer"), authored blind. The happy-path line shape (both handles
// present, matching the edge) belongs to the sibling contract suite; what
// lives here is what the block REFUSES to render: a fact whose world window
// has closed, a fact too big for the byte cap, the sixth fact, and the
// degenerate zero/negative sighting count.
//
// The package's existing helpers are CALLED, never redeclared:
// storeForGraph, withFacts and edge all live in graphctx_test.go.

// fxeOverCapFact builds one fact that cannot fit in graphContextBytes at any
// header size: graphFactRunes four-byte runes is 4*graphFactRunes bytes, so
// ClauseCut returns it unchanged (it is exactly at the rune limit) and the
// rendered line alone clears the cap. Sizing it off the constants rather
// than off a measured byte count keeps it honest if either moves.
func fxeOverCapFact(id int64) store.GraphEdge {
	return store.GraphEdge{
		ID:          id,
		EpisodeID:   id * 10,
		Occurrences: 1,
		Fact:        strings.Repeat("\U0001D356", graphFactRunes), // 4 bytes each
	}
}

// Obligation (S1b, keepRelevantFacts unchanged; contract's fixture-facts:
// "keepRelevantFacts drops facts whose world window has closed
// (ValidUntil != 0 && ValidUntil <= now)"). "However live the row" is the
// point: system time (expired_at) says the row is current, world time says
// the fact stopped holding, and world time wins.
func TestFxeGraphContextDropsAFactWhoseWorldWindowClosed(t *testing.T) {
	storeForGraph(t)
	spec := "the throttled daemon pass reads transcripts past a byte watermark"
	fact := "the throttled daemon pass reads transcripts past a byte watermark cursor"
	now := time.Now().UnixMilli()

	closed := edge(1, fact, 3)
	closed.ValidUntil = now - 60_000 // stopped holding a minute ago
	closed.ExpiredAt = 0             // ... but the ROW is live
	withFacts(t, []store.GraphEdge{closed})
	if got := graphContext(&Job{ID: 1, Body: spec}); got != "" {
		t.Errorf("a fact whose world window closed was rendered:\n%s", got)
	}

	// The control: the identical fact with an OPEN window is rendered, so the
	// case above proves the world-time gate rather than a broken fixture.
	open := edge(2, fact, 3)
	open.ValidUntil = now + 86_400_000
	withFacts(t, []store.GraphEdge{open})
	if got := graphContext(&Job{ID: 1, Body: spec}); got == "" {
		t.Error("a fact whose world window is still open was dropped — the fixture cannot reach the gate")
	}

	// valid_until 0 means "still true" everywhere in the schema.
	still := edge(3, fact, 3)
	still.ValidUntil = 0
	withFacts(t, []store.GraphEdge{still})
	if got := graphContext(&Job{ID: 1, Body: spec}); got == "" {
		t.Error("valid_until 0 means the fact still holds; it must be rendered")
	}
}

// Obligation (S1b): renderGraphBlock's "skip-don't-break byte-cap loop" is
// unchanged — an over-cap fact is SKIPPED in favour of the next one, never
// split and never used as a reason to stop. A split fact is the one way this
// block actively misleads: half a sentence with a handle after it reads as a
// complete claim.
func TestFxeRenderGraphBlockSkipsAnOverCapFactAndKeepsTheNext(t *testing.T) {
	big := fxeOverCapFact(91)
	small := store.GraphEdge{ID: 92, EpisodeID: 920, Occurrences: 2, Fact: "a short fact that fits easily"}

	block, ids := renderGraphBlock([]store.GraphEdge{big, small})
	if block == "" {
		t.Fatal("an over-cap fact followed by a fitting one produced no block — the loop broke instead of skipping")
	}
	if strings.Contains(block, "\U0001D356") {
		t.Errorf("the over-cap fact was rendered (whole or split):\n%q", block)
	}
	if !strings.Contains(block, small.Fact) {
		t.Errorf("the fitting fact after the over-cap one was not rendered:\n%q", block)
	}
	if len(block) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap", len(block), graphContextBytes)
	}
	if n := strings.Count(block, "\n- "); n != 1 {
		t.Errorf("block carries %d fact lines, want exactly the one that fit:\n%q", n, block)
	}
	if len(ids) != 1 || ids[0] != small.ID {
		t.Errorf("rendered ids = %v, want exactly [%d] — the ids are what recall marks as delivered, so a skipped fact must not be claimed", ids, small.ID)
	}
}

// Obligation (S1b): "it renders once per non-empty block ... and never on an
// empty block (renderGraphBlock returns "" then, unchanged)". A header
// announcing that the graph knows something, with nothing under it, is worse
// than no block at all — and it would also cost the brief its
// byte-identical-no-op guarantee.
func TestFxeRenderGraphBlockRefusesAHeaderAloneAndReportsNoIDs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		edges []store.GraphEdge
	}{
		{"nil input", nil},
		{"empty input", []store.GraphEdge{}},
		{"one fact too large to ever fit", []store.GraphEdge{fxeOverCapFact(1)}},
		{"several facts, all too large", []store.GraphEdge{fxeOverCapFact(1), fxeOverCapFact(2), fxeOverCapFact(3)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			block, ids := renderGraphBlock(tc.edges)
			if block != "" {
				t.Errorf("block = %q, want \"\" — nothing was kept, so there is no header to write", block)
			}
			if strings.Contains(block, graphContextHeader) {
				t.Errorf("the header was written with no facts under it:\n%q", block)
			}
			if len(ids) != 0 {
				t.Errorf("rendered ids = %v, want none — nothing was delivered", ids)
			}
		})
	}
}

// Obligation (S1b): "graphContextFacts, graphFactRunes | unchanged (5, 180)"
// and graphContextBytes = 682. Nine relevant, distinct, short facts are
// offered; exactly graphContextFacts come back, the block stays under the
// byte cap, and the header appears exactly once.
func TestFxeGraphContextCapsFactsAndBytesAndHeader(t *testing.T) {
	storeForGraph(t)
	spec := "the daemon pass reads transcripts past a byte watermark cursor"
	tails := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india"}
	var edges []store.GraphEdge
	for i, tail := range tails {
		edges = append(edges, edge(int64(i+1), "the daemon pass reads transcripts past a watermark "+tail, int64(i+1)))
	}
	withFacts(t, edges)

	got := graphContext(&Job{ID: 1, Body: spec})
	if got == "" {
		t.Fatal("nine facts sharing four content words with the spec produced no block — the fixture cannot reach the budget")
	}
	if n := strings.Count(got, "\n- "); n != graphContextFacts {
		t.Errorf("block carries %d facts, want exactly the %d cap:\n%s", n, graphContextFacts, got)
	}
	if len(got) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(got), graphContextBytes, got)
	}
	if n := strings.Count(got, graphContextHeader); n != 1 {
		t.Errorf("the header appears %d times, want exactly once per non-empty block", n)
	}
	if !strings.HasPrefix(got, graphContextHeader) {
		t.Errorf("block does not open with the header:\n%s", got)
	}
	// Every emitted line is whole — the byte cap must fall BETWEEN facts.
	body := strings.TrimPrefix(got, graphContextHeader)
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if !strings.HasPrefix(line, "- ") || !strings.HasSuffix(line, "x]") {
			t.Errorf("truncated mid-fact: %q", line)
		}
	}
}

// Obligation (S1b, factLine): "occ = max(1, e.Occurrences) (a stored edge was
// seen at least once, unchanged)". A "seen 0x" line would read as a fact
// nobody ever observed; a negative one as a corrupt count published as
// evidence.
func TestFxeFactLineFloorsTheSightingCountAtOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   store.GraphEdge
		want string
	}{
		{
			"zeroed count",
			store.GraphEdge{ID: 7, EpisodeID: 4, Fact: "a fact with a zeroed count", Occurrences: 0},
			"- a fact with a zeroed count. [F7 E4, seen 1x]\n",
		},
		{
			"negative count",
			store.GraphEdge{ID: 8, EpisodeID: 5, Fact: "a fact with a negative count", Occurrences: -5},
			"- a fact with a negative count. [F8 E5, seen 1x]\n",
		},
		{
			// The floor must not reach a real count of 1, nor a larger one.
			"count of one is untouched",
			store.GraphEdge{ID: 9, EpisodeID: 6, Fact: "a fact seen once", Occurrences: 1},
			"- a fact seen once. [F9 E6, seen 1x]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := factLine(tc.in); got != tc.want {
				t.Errorf("factLine(%+v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Obligation (S1b, terminator rule): "if the ClauseCut result ends with `…`,
// it is used as-is (the ellipsis is the terminator); otherwise
// strings.TrimRight(summary, " .") + "."". The cut case is the edge: a
// summary that already ends in an ellipsis must not gain a period after it.
func TestFxeFactLineKeepsTheEllipsisAsTheTerminator(t *testing.T) {
	long := store.GraphEdge{ID: 11, EpisodeID: 12, Occurrences: 1, Fact: strings.Repeat("word ", 60)}
	got := factLine(long)
	if !strings.HasSuffix(got, "… [F11 E12, seen 1x]\n") {
		t.Errorf("factLine of an over-length fact = %q, want the summary to end with `…` and no added period", got)
	}
	for _, bad := range []string{"….", "…,", "… .", ".…"} {
		if strings.Contains(got, bad) {
			t.Errorf("line %q contains %q — the ellipsis IS the terminator", got, bad)
		}
	}
	// A fact carrying its own period must not gain a second one either.
	own := factLine(store.GraphEdge{ID: 13, EpisodeID: 14, Occurrences: 2, Fact: "a fact with its own period."})
	if own != "- a fact with its own period. [F13 E14, seen 2x]\n" {
		t.Errorf("factLine = %q, want the single period preserved", own)
	}
	if strings.Contains(own, "..") {
		t.Errorf("line %q doubled the terminator", own)
	}
}

// Obligation (S1b): the byte cap holds against facts that are individually
// legal but collectively over budget — the loop must stop rather than run the
// block past graphContextBytes, and must stop BETWEEN lines.
func TestFxeRenderGraphBlockStaysUnderTheByteCapWithLongFacts(t *testing.T) {
	var edges []store.GraphEdge
	for i := int64(1); i <= 12; i++ {
		edges = append(edges, store.GraphEdge{
			ID: i, EpisodeID: i * 1000, Occurrences: i,
			// ~150 ASCII runes each: comfortably renderable alone, so several
			// fit and the cap is what stops the loop.
			Fact: strings.Repeat("cursor watermark transcript ", 5) + strings.Repeat("z", int(i)),
		})
	}
	block, ids := renderGraphBlock(edges)
	if block == "" {
		t.Fatal("twelve renderable facts produced no block")
	}
	if len(block) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(block), graphContextBytes, block)
	}
	lines := strings.Count(block, "\n- ")
	if lines > graphContextFacts {
		t.Errorf("block carries %d facts, over the %d cap", lines, graphContextFacts)
	}
	if len(ids) != lines {
		t.Errorf("rendered %d lines but reported %d ids — recall would mark facts it never delivered", lines, len(ids))
	}
	body := strings.TrimPrefix(block, graphContextHeader)
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if !strings.HasPrefix(line, "- ") || !strings.HasSuffix(line, "x]") {
			t.Errorf("truncated mid-fact: %q", line)
		}
	}
}

// Obligation (S1b, graphContextHeader): "Exactly this string". Pinned as a
// literal rather than by shape, because the whole point of the new line is
// that it advertises a verb an agent will type — a header that drifts from
// `k graph get F<id>` sends zeus to a command that does not exist.
func TestFxeGraphContextHeaderIsExactlyTheContractString(t *testing.T) {
	const want = "\nKnown from past work (may be stale — verify before relying):\nfull fact for any [F#]: k graph get F<id>\n"
	if graphContextHeader != want {
		t.Errorf("graphContextHeader = %q, want %q", graphContextHeader, want)
	}
	if graphContextBytes != 682 {
		t.Errorf("graphContextBytes = %d, want 682 (600 + 42 advertisement bytes + 40 handle-growth bytes)", graphContextBytes)
	}
	if graphContextFacts != 5 || graphFactRunes != 180 {
		t.Errorf("graphContextFacts/graphFactRunes = %d/%d, want 5/180 (unchanged)", graphContextFacts, graphFactRunes)
	}
}
