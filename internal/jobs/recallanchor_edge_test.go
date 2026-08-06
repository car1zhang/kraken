package jobs

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// recallanchor_edge_test.go — the EDGE dimension of 50-contract.md Slice 3:
// the gate exemption's LIMITS. Written blind; graphctx.go and recall.go were
// not read.
//
// Anchoring buys an edge exactly one thing — it is kept without consulting
// core.RelevantEdge — and every case below is about something it does NOT buy.
// That framing is the whole risk of this slice: the exemption is applied by
// checking two node ids against a set, and the cheapest way to implement it is
// an early `return true` that skips the closed-window check, the skip set, the
// dedup and the cap along with the relevance call. Each of those four would
// then fail silently and in the worst possible place: a fact the graph knows
// stopped being true, injected mid-conversation as a bare claim, on every
// firing forever because it was never recorded as delivered.
//
// Every package-level identifier is prefixed `edgeAnchor`. This package's test
// namespace already holds `edge` (graphctx_test.go), `edgeRecall*`
// (recall_edge_test.go) and a `contractRecall*` family, and a sibling blind
// author is writing beside this file.

// --- fixtures --------------------------------------------------------------

// edgeAnchorEnv isolates the embedder credential so the query vector really is
// nil and core.RelevantEdge really does run its lexical arm. core.NewEmbedder
// falls back to ~/.kraken/openai.env, which exists on the machine this suite
// runs on, so pointing HOME at a temp dir is what makes "no embedder" true
// rather than assumed.
func edgeAnchorEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
}

func edgeAnchorStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// edgeAnchorWithFacts swaps the retrieval seam for one test, handing back both
// halves of its new pair, and restores it afterwards.
func edgeAnchorWithFacts(t *testing.T, edges []store.GraphEdge, anchors map[int64]bool) {
	t.Helper()
	prev := graphFacts
	graphFacts = func(*store.Store, []float32, string) ([]store.GraphEdge, map[int64]bool) {
		return edges, anchors
	}
	t.Cleanup(func() { graphFacts = prev })
}

// edgeAnchorNodeID is the anchored node every fixture edge hangs off.
const edgeAnchorNodeID int64 = 42

// edgeAnchorQuery carries four content words, none of which appears in
// edgeAnchorStranger below. That gap is the whole fixture: a stranger fact is
// dropped by the ordinary gate and kept only by the exemption, so every
// assertion in this file reads as "the anchor path did / did not run".
const edgeAnchorQuery = "tell me about lewis the cofounder"

// edgeAnchorStranger shares NOTHING with edgeAnchorQuery, so
// core.RelevantEdge judges it weak with no vector in play.
const edgeAnchorStranger = "quiet wiring number seventeen hums"

// edgeAnchorRelevant shares two content words with the query — exactly
// core.GraphMinTermOverlap — so it clears the gate on its own merits.
const edgeAnchorRelevant = "lewis is listed as cofounder"

// edgeAnchorFact builds a stranger fact hanging off the anchored node.
func edgeAnchorFact(id int64, fact string) store.GraphEdge {
	return store.GraphEdge{ID: id, EpisodeID: id * 10, SrcID: edgeAnchorNodeID, DstID: 1000 + id,
		Fact: fact, Occurrences: 1}
}

// edgeAnchorStrangers builds n DISTINCT stranger facts, all anchored: distinct
// because the dedup rule would otherwise collapse them and turn a cap test
// into a dedup test.
func edgeAnchorStrangers(n int) []store.GraphEdge {
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot",
		"golf", "hotel", "india", "juliett", "kilo", "lima"}
	var out []store.GraphEdge
	for i := 0; i < n && i < len(words); i++ {
		out = append(out, edgeAnchorFact(int64(i+1), "quiet wiring number "+words[i]+" hums"))
	}
	return out
}

func edgeAnchorSet() map[int64]bool { return map[int64]bool{edgeAnchorNodeID: true} }

func edgeAnchorTerms() map[string]bool { return core.ContentTerms(edgeAnchorQuery) }

func edgeAnchorKeptIDs(edges []store.GraphEdge) []int64 {
	out := make([]int64, 0, len(edges))
	for _, e := range edges {
		out = append(out, e.ID)
	}
	return out
}

func edgeAnchorLines(block string) []string {
	body := strings.TrimSuffix(strings.TrimPrefix(block, graphContextHeader), "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// --- keepRelevantFacts: what the exemption does ----------------------------

// TestEdgeAnchorKeepRelevantFactsExemptionIsPerEndpointAndTruthy walks the
// membership test itself at its edges: src, dst, both, neither, and — the one
// that only a map can produce — an id PRESENT in the set with the value false.
// `anchors[id]` and `_, ok := anchors[id]` are the same expression to a reader
// and different expressions to the compiler, and only one of them is right.
func TestEdgeAnchorKeepRelevantFactsExemptionIsPerEndpointAndTruthy(t *testing.T) {
	terms := edgeAnchorTerms()
	bySrc := edgeAnchorFact(1, edgeAnchorStranger)
	byDst := store.GraphEdge{ID: 2, EpisodeID: 20, SrcID: 900, DstID: edgeAnchorNodeID,
		Fact: edgeAnchorStranger + " twice", Occurrences: 1}
	selfLoop := store.GraphEdge{ID: 3, EpisodeID: 30, SrcID: edgeAnchorNodeID, DstID: edgeAnchorNodeID,
		Fact: edgeAnchorStranger + " thrice", Occurrences: 1}
	stranger := store.GraphEdge{ID: 4, EpisodeID: 40, SrcID: 900, DstID: 901,
		Fact: edgeAnchorStranger + " again", Occurrences: 1}
	all := []store.GraphEdge{bySrc, byDst, selfLoop, stranger}

	kept := keepRelevantFacts(all, nil, terms, nil, edgeAnchorSet())
	if got := edgeAnchorKeptIDs(kept); !reflect.DeepEqual(got, []int64{1, 2, 3}) {
		t.Errorf("kept = %v, want [1 2 3] — an edge is exempt when EITHER endpoint anchors, and only then", got)
	}

	// A key present with a false value is not an anchor. Nothing here shares
	// two content words with the query, so if the exemption misfires every one
	// of these is kept and the gate has stopped existing.
	falsey := keepRelevantFacts(all, nil, terms, nil, map[int64]bool{edgeAnchorNodeID: false, 900: false})
	if len(falsey) != 0 {
		t.Errorf("kept %v with an all-false anchor set — membership is not the same as being anchored",
			edgeAnchorKeptIDs(falsey))
	}
}

// TestEdgeAnchorKeepRelevantFactsNilAnchorsBehavesLikeEmpty pins the contract's
// "Nil anchors behaves as empty" beside the invariant the whole feature rests
// on: with no anchors the kept list is what it was before this slice existed.
// Nil is the value every unchanged caller and every failed retrieval supplies,
// so a nil-map read that panicked — or an implementation that treated nil as
// "exempt everything" — would break the common path, not the new one.
func TestEdgeAnchorKeepRelevantFactsNilAnchorsBehavesLikeEmpty(t *testing.T) {
	terms := edgeAnchorTerms()
	edges := []store.GraphEdge{
		edgeAnchorFact(1, edgeAnchorStranger),
		edgeAnchorFact(2, edgeAnchorRelevant),
	}
	withNil := keepRelevantFacts(edges, nil, terms, nil, nil)
	withEmpty := keepRelevantFacts(edges, nil, terms, map[int64]bool{}, map[int64]bool{})
	if !reflect.DeepEqual(edgeAnchorKeptIDs(withNil), edgeAnchorKeptIDs(withEmpty)) {
		t.Errorf("nil anchors kept %v, empty anchors kept %v — the two must be the same request",
			edgeAnchorKeptIDs(withNil), edgeAnchorKeptIDs(withEmpty))
	}
	if got := edgeAnchorKeptIDs(withNil); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("kept = %v, want [2] — with no anchors the ordinary relevance gate decides alone", got)
	}
}

// TestEdgeAnchorKeepRelevantFactsEmptyInputs: nil edges is what a failed
// retrieval hands over on every spawn and every hook firing. It must be an
// empty answer, never a panic, whatever the anchor set says.
func TestEdgeAnchorKeepRelevantFactsEmptyInputs(t *testing.T) {
	for name, got := range map[string][]store.GraphEdge{
		"everything nil":       keepRelevantFacts(nil, nil, nil, nil, nil),
		"nil edges, anchors":   keepRelevantFacts(nil, nil, edgeAnchorTerms(), nil, edgeAnchorSet()),
		"empty edges, anchors": keepRelevantFacts([]store.GraphEdge{}, nil, nil, map[int64]bool{}, edgeAnchorSet()),
		"anchors, no terms":    keepRelevantFacts([]store.GraphEdge{edgeAnchorFact(1, edgeAnchorStranger)}, nil, nil, nil, nil),
	} {
		if len(got) != 0 {
			t.Errorf("%s: kept %v, want nothing", name, edgeAnchorKeptIDs(got))
		}
	}
	// The zero-value edge is what a caller iterating a partially-filled slice
	// hands over: both endpoints are 0, and a 0 that is not in the anchor set
	// must not be rescued by one that is.
	zero := keepRelevantFacts([]store.GraphEdge{{}}, nil, edgeAnchorTerms(), nil, edgeAnchorSet())
	if len(zero) != 0 {
		t.Errorf("the zero-value edge was kept against anchors %v", edgeAnchorSet())
	}
}

// --- keepRelevantFacts: what the exemption does NOT buy --------------------

// TestEdgeAnchorKeepRelevantFactsClosedWorldWindowStillDrops is the most
// consequential of the four. "We used X until March" is stored as one LIVE
// edge with a closed world-time window; retrieval serves it and the anchor
// path would inject it as a bare present-tense claim mid-conversation, with no
// brief around it to hedge with. Anchoring says "this fact is about what you
// asked", never "this fact is still true".
func TestEdgeAnchorKeepRelevantFactsClosedWorldWindowStillDrops(t *testing.T) {
	now := time.Now().UnixMilli()
	terms := edgeAnchorTerms()

	closed := edgeAnchorFact(1, edgeAnchorStranger)
	closed.ValidUntil = now - 60_000
	if kept := keepRelevantFacts([]store.GraphEdge{closed}, nil, terms, nil, edgeAnchorSet()); len(kept) != 0 {
		t.Errorf("an anchored fact whose world-time window closed was kept: %v", edgeAnchorKeptIDs(kept))
	}

	// The other side of the boundary: a FUTURE valid_until is a fact that is
	// currently true, and the exemption must not have cost it its keep either.
	future := edgeAnchorFact(2, edgeAnchorStranger)
	future.ValidUntil = now + 3_600_000
	if kept := keepRelevantFacts([]store.GraphEdge{future}, nil, terms, nil, edgeAnchorSet()); len(kept) != 1 {
		t.Errorf("an anchored fact valid until a future instant was dropped: %v", edgeAnchorKeptIDs(kept))
	}
	// And an OPEN window (0 = still true) is untouched.
	open := edgeAnchorFact(3, edgeAnchorStranger)
	if kept := keepRelevantFacts([]store.GraphEdge{open}, nil, terms, nil, edgeAnchorSet()); len(kept) != 1 {
		t.Errorf("an anchored fact with no world-time bound was dropped: %v", edgeAnchorKeptIDs(kept))
	}
}

// TestEdgeAnchorKeepRelevantFactsSkipSetStillDrops: the skip set is the
// session's memory of what it has already said. A fact exempted past it would
// be re-injected on every firing for the rest of the conversation — the one
// failure "at most once per session" exists to prevent, now reachable through
// the new path.
func TestEdgeAnchorKeepRelevantFactsSkipSetStillDrops(t *testing.T) {
	terms := edgeAnchorTerms()
	edges := []store.GraphEdge{
		edgeAnchorFact(1, edgeAnchorStranger),
		edgeAnchorFact(2, edgeAnchorStranger+" elsewhere"),
	}
	kept := keepRelevantFacts(edges, nil, terms, map[int64]bool{1: true}, edgeAnchorSet())
	if got := edgeAnchorKeptIDs(kept); !reflect.DeepEqual(got, []int64{2}) {
		t.Errorf("kept = %v, want [2] — an already-delivered anchored fact is still already delivered", got)
	}
	all := keepRelevantFacts(edges, nil, terms, map[int64]bool{1: true, 2: true}, edgeAnchorSet())
	if len(all) != 0 {
		t.Errorf("kept %v with every candidate in the skip set", edgeAnchorKeptIDs(all))
	}
	// A skip entry present-but-false is not a skip.
	none := keepRelevantFacts(edges, nil, terms, map[int64]bool{1: false, 2: false}, edgeAnchorSet())
	if len(none) != 2 {
		t.Errorf("kept %v with an all-false skip set, want both", edgeAnchorKeptIDs(none))
	}
}

// TestEdgeAnchorKeepRelevantFactsDuplicateWordingStillCollapses covers the
// dedup at the boundary the contract words carefully: duplicate TRUNCATED
// wording. Two facts that differ only past graphFactRunes render as the same
// line, so keeping both would print the same sentence twice under two episode
// ids — and anchoring makes that likelier, since a node's edges often restate
// one thing.
func TestEdgeAnchorKeepRelevantFactsDuplicateWordingStillCollapses(t *testing.T) {
	terms := edgeAnchorTerms()

	identical := []store.GraphEdge{
		edgeAnchorFact(1, edgeAnchorStranger),
		edgeAnchorFact(2, edgeAnchorStranger),
	}
	if kept := keepRelevantFacts(identical, nil, terms, nil, edgeAnchorSet()); len(kept) != 1 {
		t.Errorf("two anchored copies of one sentence kept %v, want one", edgeAnchorKeptIDs(kept))
	}

	// Same visible line, different tails: identical for the first
	// graphFactRunes runes and divergent only past the cut.
	prefix := "quiet wiring " + strings.Repeat("hums ", graphFactRunes)
	truncated := []store.GraphEdge{
		edgeAnchorFact(1, prefix+"alpha"),
		edgeAnchorFact(2, prefix+"omega"),
	}
	if kept := keepRelevantFacts(truncated, nil, terms, nil, edgeAnchorSet()); len(kept) != 1 {
		t.Errorf("two anchored facts identical up to the truncation point kept %v, want one — they render as the same line",
			edgeAnchorKeptIDs(kept))
	}
}

// TestEdgeAnchorKeepRelevantFactsCapStillBinds: the per-firing fact cap is the
// injection's attention budget, and anchoring a well-connected node is exactly
// the case that produces more exempt candidates than the budget allows. An
// exemption applied before the cap would let one `k graph`-heavy person's node
// take over a whole brief.
func TestEdgeAnchorKeepRelevantFactsCapStillBinds(t *testing.T) {
	kept := keepRelevantFacts(edgeAnchorStrangers(12), nil, edgeAnchorTerms(), nil, edgeAnchorSet())
	if len(kept) != graphContextFacts {
		t.Errorf("twelve anchored facts kept %d, want exactly graphContextFacts (%d): %v",
			len(kept), graphContextFacts, edgeAnchorKeptIDs(kept))
	}
	// Under the cap nothing is trimmed, so the number above is the cap's doing.
	few := keepRelevantFacts(edgeAnchorStrangers(2), nil, edgeAnchorTerms(), nil, edgeAnchorSet())
	if len(few) != 2 {
		t.Errorf("two anchored facts kept %d, want both", len(few))
	}
}

// --- the seam and graphContext ---------------------------------------------

// TestEdgeAnchorSearchFactsFailurePathReturnsNilNil pins the widened seam's
// error contract. Two return values means two ways to leak a non-nil on a
// failure, and an anchor set returned beside a nil edge list would exempt
// whatever a later caller happened to pass — nothing today, and a silent
// bypass the day the call order changes.
func TestEdgeAnchorSearchFactsFailurePathReturnsNilNil(t *testing.T) {
	edges, anchors := searchFacts(nil, nil, "anything at all")
	if edges != nil {
		t.Errorf("searchFacts against a nil store returned %d edges, want nil", len(edges))
	}
	if anchors != nil {
		t.Errorf("searchFacts against a nil store returned anchors %v, want nil", anchors)
	}
}

// TestEdgeAnchorGraphContextThreadsAnchorsFromTheSeam is the spawn path's half
// of the threading, asserted through the one observable difference anchoring
// makes: a fact sharing no content word with the spec is emitted when the seam
// says its node anchored, and is not when the seam says nothing anchored.
func TestEdgeAnchorGraphContextThreadsAnchorsFromTheSeam(t *testing.T) {
	storeForGraph(t)
	fact := []store.GraphEdge{edgeAnchorFact(1, edgeAnchorStranger)}

	edgeAnchorWithFacts(t, fact, nil)
	if got := graphContext(&Job{ID: 1, Body: edgeAnchorQuery}); got != "" {
		t.Fatalf("a fact sharing no content word with the spec was emitted with NO anchors:\n%s", got)
	}

	edgeAnchorWithFacts(t, fact, edgeAnchorSet())
	got := graphContext(&Job{ID: 1, Body: edgeAnchorQuery})
	if got == "" {
		t.Fatal("an anchored fact was not injected — the seam's anchors did not reach the keep-gate")
	}
	if !strings.Contains(got, "[E10, seen 1x]") {
		t.Errorf("the anchored fact's provenance is missing:\n%s", got)
	}
	if !strings.HasPrefix(got, graphContextHeader) {
		t.Errorf("block does not open with the hedged header:\n%s", got)
	}

	// An anchor set naming a node no candidate touches changes nothing.
	edgeAnchorWithFacts(t, fact, map[int64]bool{777: true})
	if got := graphContext(&Job{ID: 1, Body: edgeAnchorQuery}); got != "" {
		t.Errorf("an anchor set touching no candidate still emitted a block:\n%s", got)
	}
}

// --- Recall -----------------------------------------------------------------

// TestEdgeAnchorRecallMarksAnchoredDeliveriesLikeAnyOther is the contract's
// last sentence for this slice, and it is the one an exemption written as an
// early return breaks invisibly: a fact injected via the anchor path is not
// re-injected on a later firing. Without the mark, the SAME block arrives on
// every firing of the conversation — the anchor path being the one path that
// keeps re-qualifying, since it needs no term overlap to come back.
func TestEdgeAnchorRecallMarksAnchoredDeliveriesLikeAnyOther(t *testing.T) {
	edgeAnchorEnv(t)
	st := edgeAnchorStore(t)
	facts := edgeAnchorStrangers(2)
	edgeAnchorWithFacts(t, facts, edgeAnchorSet())
	const sess = "s-anchor-once"

	first := Recall(context.Background(), st, RecallRequest{SessionID: sess, Event: "UserPromptSubmit", Prompt: edgeAnchorQuery})
	if first == "" {
		t.Fatal("an anchored fact was not injected; every assertion below would be vacuous")
	}
	ids, err := st.RecalledEdgeIDs(sess)
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	for _, e := range facts {
		if !ids[e.ID] {
			t.Errorf("anchored edge %d was rendered but not recorded in kg_recall (set %v)", e.ID, ids)
		}
	}

	second := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: edgeAnchorQuery})
	if second != "" {
		t.Errorf("the same anchored facts were injected twice into one session:\n%s", second)
	}
	// A different query in the same session must not resurrect them either:
	// the skip set is keyed on the session, not on the question.
	again := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: "something else about lewis entirely"})
	if again != "" {
		t.Errorf("a second firing with a new query re-injected the anchored facts:\n%s", again)
	}
}

// TestEdgeAnchorRecallAnchorsDoNotBypassTheSkipSet is the skip-set refusal seen
// from outside the gate, through the store rather than through a hand-built
// map — which is the only place a "read the skip set, then exempt past it"
// ordering bug shows up.
func TestEdgeAnchorRecallAnchorsDoNotBypassTheSkipSet(t *testing.T) {
	edgeAnchorEnv(t)
	st := edgeAnchorStore(t)
	facts := edgeAnchorStrangers(3)
	edgeAnchorWithFacts(t, facts, edgeAnchorSet())
	const sess = "s-anchor-skip"

	var ids []int64
	for _, e := range facts {
		ids = append(ids, e.ID)
	}
	if err := st.MarkRecalled(sess, ids, 1_700_000_000_000); err != nil {
		t.Fatalf("seed skip set: %v", err)
	}
	if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: edgeAnchorQuery}); got != "" {
		t.Errorf("every anchored candidate was already delivered, yet a block came back:\n%s", got)
	}
}

// TestEdgeAnchorRecallAnchorsDoNotBypassTheCaps pins the fact cap through the
// verb, where an exemption applied at the wrong point in the pipeline would
// show up as a block longer than any the spawn path can produce.
func TestEdgeAnchorRecallAnchorsDoNotBypassTheCaps(t *testing.T) {
	edgeAnchorEnv(t)
	st := edgeAnchorStore(t)
	edgeAnchorWithFacts(t, edgeAnchorStrangers(9), edgeAnchorSet())

	got := Recall(context.Background(), st, RecallRequest{SessionID: "s-anchor-cap", Prompt: edgeAnchorQuery})
	if got == "" {
		t.Fatal("nine anchored facts produced no block")
	}
	if n := len(edgeAnchorLines(got)); n != graphContextFacts {
		t.Errorf("block carries %d facts, want exactly %d:\n%s", n, graphContextFacts, got)
	}
	if len(got) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(got), graphContextBytes, got)
	}
}

// TestEdgeAnchorRecallAnchoredClosedWindowInjectsNothing is the world-time
// refusal through the verb. Mid-conversation is the worst place for a retired
// belief to arrive as a bare claim, and the anchor path is precisely the one
// that would carry it there without any lexical evidence that it is on topic.
func TestEdgeAnchorRecallAnchoredClosedWindowInjectsNothing(t *testing.T) {
	edgeAnchorEnv(t)
	st := edgeAnchorStore(t)
	now := time.Now().UnixMilli()

	closed := edgeAnchorStrangers(3)
	for i := range closed {
		closed[i].ValidUntil = now - 60_000
	}
	edgeAnchorWithFacts(t, closed, edgeAnchorSet())
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s-anchor-closed", Prompt: edgeAnchorQuery}); got != "" {
		t.Errorf("anchored facts whose world-time windows have closed were injected:\n%s", got)
	}
	marked, err := st.RecalledEdgeIDs("s-anchor-closed")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("%d dropped anchored fact(s) were recorded as delivered: %v", len(marked), marked)
	}
}

// TestEdgeAnchorRecallStillFailsToNothing re-pins the posture the whole verb is
// held to, now that a second value crosses the seam: none of Recall's refusals
// may be reachable past an anchor set. A blank session with anchors is still a
// blank session — there is no key to record the delivery under, so injecting
// would mean injecting forever.
func TestEdgeAnchorRecallStillFailsToNothing(t *testing.T) {
	edgeAnchorEnv(t)
	st := edgeAnchorStore(t)
	edgeAnchorWithFacts(t, edgeAnchorStrangers(3), edgeAnchorSet())

	for _, sess := range []string{"", "   ", "\t\n"} {
		if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: edgeAnchorQuery}); got != "" {
			t.Errorf("Recall with session %q returned an anchored block:\n%s", sess, got)
		}
	}
	if got := Recall(context.Background(), nil, RecallRequest{SessionID: "s", Prompt: edgeAnchorQuery}); got != "" {
		t.Errorf("Recall with a nil store returned an anchored block:\n%s", got)
	}
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s", Event: "Stop"}); got != "" {
		t.Errorf("Recall with no query at all returned an anchored block:\n%s", got)
	}

	// A caller that has already hung up gets nothing, and nothing is recorded
	// — the anchored facts stay eligible for the firing that follows.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Recall(ctx, st, RecallRequest{SessionID: "s-gone", Prompt: edgeAnchorQuery}); got != "" {
		t.Errorf("Recall on a done context returned a block nobody can receive:\n%s", got)
	}
	marked, err := st.RecalledEdgeIDs("s-gone")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("a canceled call recorded %v as delivered", marked)
	}
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s-gone", Prompt: edgeAnchorQuery}); got == "" {
		t.Error("the canceled call burned the session's anchored facts")
	}
}
