package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/store"
)

// recall_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice B's two package-level obligations: jobs.Recall (the
// server-side retrieval + dedup + render) and jobs.RunRecallHook (the
// `kraken recall-hook` body). Written from the contract text alone, with no
// sight of internal/jobs/recall.go or of the pending diffs to graphctx.go and
// cell.go.
//
// Dimension: LOGIC — the stated behaviour: which query is used, that
// retrieval goes through the seam, that the block is the SPAWN block byte for
// byte, that a memory is delivered at most once per session, and that the
// hook's output shape is exactly the one Claude Code reads. Refusals,
// boundaries and hostile payloads are athena-edge's ground; where an
// obligation IS a refusal that the happy path depends on (the "" answers that
// make "every failure degrades to inject nothing" true) it is pinned here too.
//
// Every package-level identifier is prefixed `contractRecall` — two sibling
// authors share this package namespace and `contract…` alone is already taken
// here.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractRecallEnv isolates the embedder credential for a test. The contract
// asserts tests run with no OPENAI_API_KEY, which makes the query vector nil
// and puts core.RelevantEdge on its lexical arm; core.NewEmbedder ALSO reads
// ~/.kraken/openai.env, so a real one on the machine running this suite would
// otherwise put a network call on the path under test. Pointing HOME at a
// temp dir makes "no embedder" true rather than assumed.
func contractRecallEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
}

// contractRecallSeam records what graphFacts was called with, so a test can
// assert on the QUERY Recall chose rather than inferring it from which facts
// happened to clear the gate.
type contractRecallSeam struct {
	calls   int
	queries []string
	vecs    [][]float32
}

// contractRecallWithFacts swaps the retrieval seam for one test and restores
// it. The contract requires retrieval to go through this var and never
// through a direct core.GraphSearch call; a Recall that bypassed it would
// leave calls at 0 with a real (empty) store behind it, which every
// non-empty-block assertion below would then fail.
func contractRecallWithFacts(t *testing.T, edges []store.GraphEdge) *contractRecallSeam {
	t.Helper()
	rec := &contractRecallSeam{}
	prev := graphFacts
	graphFacts = func(_ *store.Store, vec []float32, query string) ([]store.GraphEdge, map[int64]bool) {
		rec.calls++
		rec.queries = append(rec.queries, query)
		rec.vecs = append(rec.vecs, vec)
		return edges, nil
	}
	t.Cleanup(func() { graphFacts = prev })
	return rec
}

// contractRecallTranscript writes a one-record assistant transcript whose
// text is body, and returns its path.
func contractRecallTranscript(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	rec := map[string]any{
		"type":    "assistant",
		"message": map[string]any{"content": []any{map[string]any{"type": "text", "text": body}}},
	}
	line, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("marshal transcript record: %v", err)
	}
	if err := os.WriteFile(path, append(line, '\n'), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// The fixture vocabulary: every fact below shares at least
// core.GraphMinTermOverlap distinct content words with contractRecallSpec, so
// the lexical arm accepts it, mirroring the existing TestGraphContext* tests.
const contractRecallSpec = "the throttled daemon pass reads transcripts past a byte watermark"

func contractRecallFact(n int64) store.GraphEdge {
	return edge(n, "the throttled daemon pass stamps its watermark before any early bail "+
		strings.Repeat("x", int(n)), n)
}

// contractRecallWideFact is a fact that PASSES the keep-gate and then LOSES
// renderGraphBlock's byte race — the one shape amendment 3 is about.
//
// It is renderGraphBlock's own worked example, built to its stated arithmetic:
// exactly graphFactRunes runes (so the summary cut returns it untouched — it
// only cuts ABOVE the limit), of which everything but the two content words is
// a FOUR-byte padding rune. The two words are "byte" and "pass", both ≥
// core.GraphTermMinRunes and both present in contractRecallSpec, so the
// lexical arm scores exactly the overlap of 2 the gate demands: this fact is
// relevant beyond question, and the only reason it does not render is its
// width. The episode id and sighting count are wide too — they are part of the
// line the cap measures, and the margin is worth having.
//
// The padding was a 3-byte em dash while the cap was 600 (522-byte fact →
// 660-byte block). Under 682 that FITS, so it is four bytes now: 9 + 171×4 =
// 693 bytes of fact, a 725-byte line, 831 with the header. The rune is a
// SYMBOL rather than a letter on purpose — core.ContentTerms splits on
// non-letters, so a 4-byte ideograph would glue onto "pass" and cost the
// fixture the very lexical overlap it is built to have.
//
// The test using it asserts the arithmetic rather than trusting it.
func contractRecallWideFact() store.GraphEdge {
	return store.GraphEdge{
		ID:          1,
		EpisodeID:   987654321,
		Fact:        "byte pass" + strings.Repeat("\U0001D11E", graphFactRunes-len("byte pass")),
		Occurrences: 4321,
	}
}

// ---------------------------------------------------------------------------
// Recall — the "" answers that make the feature safe
// ---------------------------------------------------------------------------

// TestContractRecallEmptyWithoutStore pins the first of Recall's stated ""
// cases. A hook fires inside a cell whose `kraken serve` may be mid-restart;
// nil st must be an empty injection, never a panic on the wire path.
func TestContractRecallEmptyWithoutStore(t *testing.T) {
	contractRecallEnv(t)
	rec := contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})
	got := Recall(context.Background(), nil, RecallRequest{SessionID: "sess", Event: "UserPromptSubmit", Prompt: contractRecallSpec})
	if got != "" {
		t.Errorf("Recall with a nil store = %q, want \"\"", got)
	}
	if rec.calls != 0 {
		t.Errorf("retrieval ran %d times with no store; it must not be reached at all", rec.calls)
	}
}

// TestContractRecallEmptyForABlankSession pins the dedup-key rule as a
// refusal: without a session id there is no key to record a delivery under,
// so injecting would mean injecting the same fact on every turn forever.
func TestContractRecallEmptyForABlankSession(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	rec := contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})
	for _, sid := range []string{"", "   ", "\t\n"} {
		if got := Recall(context.Background(), st, RecallRequest{SessionID: sid, Prompt: contractRecallSpec}); got != "" {
			t.Errorf("Recall with session %q = %q, want \"\"", sid, got)
		}
	}
	if rec.calls != 0 {
		t.Errorf("retrieval ran %d times for a keyless session; it must not be reached", rec.calls)
	}
}

// TestContractRecallEmptyWithNoQuery pins "both empty → \"\"" in the three
// shapes a real hook produces it: a Stop event with no prompt and no
// transcript path, a whitespace prompt with an unreadable transcript, and a
// transcript that holds nothing an assistant said.
func TestContractRecallEmptyWithNoQuery(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	rec := contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})

	missing := filepath.Join(t.TempDir(), "gone.jsonl")
	userOnly := filepath.Join(t.TempDir(), "user.jsonl")
	if err := os.WriteFile(userOnly, []byte(`{"type":"user","message":{"role":"user","content":"hi"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	for _, req := range []RecallRequest{
		{SessionID: "s", Event: "Stop"},
		{SessionID: "s", Event: "Stop", Prompt: "   \t ", TranscriptPath: missing},
		{SessionID: "s", Event: "Stop", TranscriptPath: userOnly},
	} {
		if got := Recall(context.Background(), st, req); got != "" {
			t.Errorf("Recall(%+v) = %q, want \"\" — there is nothing to retrieve against", req, got)
		}
	}
	if rec.calls != 0 {
		t.Errorf("retrieval ran %d times with an empty query; it must not be reached", rec.calls)
	}
}

// ---------------------------------------------------------------------------
// Recall — which query
// ---------------------------------------------------------------------------

// TestContractRecallPrefersThePromptOverTheTranscript is the query rule's
// central claim, asserted on the query Recall actually passed to retrieval
// rather than on which facts survived the gate. UserPromptSubmit carries both
// fields; the new message is what the memory should be about.
func TestContractRecallPrefersThePromptOverTheTranscript(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	rec := contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})
	path := contractRecallTranscript(t, "an entirely different sentence about TestFlight uploads")

	Recall(context.Background(), st, RecallRequest{SessionID: "s", Event: "UserPromptSubmit",
		Prompt: contractRecallSpec, TranscriptPath: path})

	if rec.calls != 1 {
		t.Fatalf("retrieval ran %d times, want 1", rec.calls)
	}
	if rec.queries[0] != contractRecallSpec {
		t.Errorf("retrieval query = %q, want the PROMPT %q", rec.queries[0], contractRecallSpec)
	}
}

// TestContractRecallTrimsThePrompt pins `strings.TrimSpace(req.Prompt)`
// literally: the hook payload's prompt arrives with whatever whitespace the
// user's editor left on it, and an untrimmed query changes neither retrieval
// nor the gate but does change what a future cache key would hash.
func TestContractRecallTrimsThePrompt(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	rec := contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})

	Recall(context.Background(), st, RecallRequest{SessionID: "s", Prompt: "  \n" + contractRecallSpec + "\t "})

	if rec.calls != 1 {
		t.Fatalf("retrieval ran %d times, want 1", rec.calls)
	}
	if rec.queries[0] != contractRecallSpec {
		t.Errorf("retrieval query = %q, want the trimmed prompt %q", rec.queries[0], contractRecallSpec)
	}
}

// TestContractRecallFallsBackToTheTranscriptTail pins the promptless path —
// since round 2, PostToolUse's: with no prompt, the query is what claude last
// said. That is the whole reason core.LastAssistantText exists. (Comment
// updated by the lead when amendment 5 landed; the assertions are the blind
// author's, unchanged.)
func TestContractRecallFallsBackToTheTranscriptTail(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	rec := contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})
	path := contractRecallTranscript(t, contractRecallSpec)

	got := Recall(context.Background(), st, RecallRequest{SessionID: "s", Event: "Stop", Prompt: "   ", TranscriptPath: path})

	if rec.calls != 1 {
		t.Fatalf("retrieval ran %d times, want 1", rec.calls)
	}
	if rec.queries[0] != contractRecallSpec {
		t.Errorf("retrieval query = %q, want the transcript's last assistant text %q", rec.queries[0], contractRecallSpec)
	}
	if got == "" {
		t.Error("a Stop event with a matching transcript tail injected nothing")
	}
}

// TestContractRecallQueryVectorIsNilWithoutAnEmbedder pins the BM25-only
// degradation the contract states: NewEmbedder failing is never a failure of
// Recall, it is a nil vector handed to the seam.
func TestContractRecallQueryVectorIsNilWithoutAnEmbedder(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	rec := contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})

	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s", Prompt: contractRecallSpec}); got == "" {
		t.Fatal("Recall injected nothing with no embedder configured; the vector leg is optional, the lexical one is not")
	}
	if rec.calls != 1 {
		t.Fatalf("retrieval ran %d times, want 1", rec.calls)
	}
	if len(rec.vecs[0]) != 0 {
		t.Errorf("retrieval got a %d-dim query vector with no embedder configured, want nil", len(rec.vecs[0]))
	}
}

// ---------------------------------------------------------------------------
// Recall — the block
// ---------------------------------------------------------------------------

// TestContractRecallRendersTheSpawnBlockByteForByte is the shared-decision
// this feature is held to: the injected block is byte-identical in format to
// the one graphContext appends to a cell's brief. Comparing against
// graphContext itself, over the same facts and the same text, is the only
// assertion that cannot pass while the two have quietly diverged — a
// hand-written expected string would keep passing if BOTH changed, and would
// fail spuriously if only the header's wording moved.
func TestContractRecallRendersTheSpawnBlockByteForByte(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	edges := []store.GraphEdge{contractRecallFact(1), contractRecallFact(2)}
	contractRecallWithFacts(t, edges)

	// graphContext first: it writes no kg_recall rows, so it cannot influence
	// the Recall call that follows.
	want := graphContext(&Job{ID: 1, Body: contractRecallSpec})
	if want == "" {
		t.Fatal("the spawn block is empty for these fixtures; the comparison below would be vacuous")
	}
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-render", Prompt: contractRecallSpec})
	if got != want {
		t.Errorf("Recall block differs from the spawn block:\n got: %q\nwant: %q", got, want)
	}
	if !strings.HasPrefix(got, graphContextHeader) {
		t.Errorf("block does not open with the shared header:\n%s", got)
	}
	if !strings.Contains(got, "[F1 E10, seen 1x]") {
		t.Errorf("block does not carry the shared fact line's ids and sighting count:\n%s", got)
	}
}

// TestContractRecallHonoursTheSharedCaps pins that the extracted keep-gate is
// the SAME one, budget included: more relevant facts than graphContextFacts
// must still yield a block within both caps, exactly as the spawn path does.
func TestContractRecallHonoursTheSharedCaps(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	var edges []store.GraphEdge
	for i := int64(1); i <= 12; i++ {
		edges = append(edges, contractRecallFact(i))
	}
	contractRecallWithFacts(t, edges)

	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-caps", Prompt: contractRecallSpec})
	if got == "" {
		t.Fatal("Recall injected nothing for twelve relevant facts")
	}
	if len(got) > graphContextBytes {
		t.Errorf("block is %d bytes, over the shared %d-byte cap:\n%s", len(got), graphContextBytes, got)
	}
	if n := strings.Count(got, "\n- "); n > graphContextFacts {
		t.Errorf("block carries %d facts, over the shared %d cap", n, graphContextFacts)
	}
}

// TestContractRecallDropsIrrelevantFacts pins that the shared gate is really
// applied on this path and not just the rendering: retrieval returning a fact
// is not the same as the fact being about the message.
func TestContractRecallDropsIrrelevantFacts(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	contractRecallWithFacts(t, []store.GraphEdge{
		edge(9, "ios TestFlight uploads burn a CFBundleVersion permanently", 4),
	})
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-gate", Prompt: contractRecallSpec}); got != "" {
		t.Errorf("a fact sharing no content words with the message was injected:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// Recall — at most once per session
// ---------------------------------------------------------------------------

// TestContractRecallMarksWhatItDelivered pins the write half of the dedup
// promise: a non-empty block is recorded in kg_recall BEFORE it is returned,
// under exactly the edge ids the block carries. Both fixtures render here, so
// this case cannot tell "rendered" from "gate-kept" apart — that separation is
// TestContractRecallMarksOnlyTheRenderedIDs' job. What this one adds is the
// tie between the two sets: every id recorded is checked against the id
// actually PRESENT in the returned text, not merely against the fixture list.
func TestContractRecallMarksWhatItDelivered(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1), contractRecallFact(2)})

	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-mark", Prompt: contractRecallSpec})
	if got == "" {
		t.Fatal("Recall injected nothing; there is no delivery to have recorded")
	}
	ids, err := st.RecalledEdgeIDs("sess-mark")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	// The line handle carries BOTH ids (factLine renders
	// "[F<edge> E<episode>, seen <n>x]"), and edge() sets EpisodeID = ID*10 —
	// so the marker below pins the edge id and its episode together, which is
	// the tie this test is about.
	for _, id := range []int64{1, 2} {
		if !ids[id] {
			t.Errorf("edge %d was rendered into the block but not recorded in kg_recall (set %v)", id, ids)
		}
		if marker := "[F" + strconv.FormatInt(id, 10) + " E" + strconv.FormatInt(id*10, 10) + ","; !strings.Contains(got, marker) {
			t.Errorf("edge %d is recorded as delivered but its line %q is not in the block:\n%s", id, marker, got)
		}
	}
	if len(ids) != 2 {
		t.Errorf("recorded set = %v, want exactly the two delivered edge ids", ids)
	}
}

// TestContractRecallMarksOnlyTheRenderedIDs pins amendment 3, and it is the
// only test in this file that can: it needs a fact that clears the GATE and
// then loses renderGraphBlock's byte race, so that "gate-kept" and "rendered"
// are two different sets rather than the same one.
//
// The rule it holds is that a byte-cap-skipped fact stays ELIGIBLE. Marking it
// would retire a fact nobody ever saw — and the reason that is safe to fix
// rather than accept is the skip-set itself: delivered facts leave the
// candidate pool, so the skipped fact rises on later firings and eventually
// renders. Marking it would have made "eventually" into "never".
func TestContractRecallMarksOnlyTheRenderedIDs(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	wide := contractRecallWideFact() // id 1, episode 987654321
	narrow := contractRecallFact(2)  // id 2, episode 20

	// Assert the fixture rather than trusting the arithmetic: this test is
	// meaningless if `wide` happens to fit, and a fixture that quietly fits
	// would make it pass while proving nothing.
	if len(graphContextHeader)+len(factLine(wide)) <= graphContextBytes {
		t.Fatalf("fixture is not wide enough to lose the byte race: header %d + line %d <= cap %d",
			len(graphContextHeader), len(factLine(wide)), graphContextBytes)
	}
	if len(graphContextHeader)+len(factLine(narrow)) > graphContextBytes {
		t.Fatalf("fixture cannot render at all: header %d + line %d > cap %d",
			len(graphContextHeader), len(factLine(narrow)), graphContextBytes)
	}

	contractRecallWithFacts(t, []store.GraphEdge{wide, narrow})
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-wide", Prompt: contractRecallSpec})
	if got == "" {
		t.Fatal("Recall injected nothing though the narrow fact fits well inside the cap")
	}
	if strings.Contains(got, "[F1 E987654321,") {
		t.Fatalf("the wide fact rendered after all; the byte cap did not skip it:\n%s", got)
	}
	if !strings.Contains(got, "[F2 E20,") {
		t.Errorf("the narrow fact did not render:\n%s", got)
	}

	ids, err := st.RecalledEdgeIDs("sess-wide")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if ids[wide.ID] {
		t.Errorf("edge %d was marked recalled but never rendered — a byte-cap-skipped fact must stay eligible (recorded set %v)", wide.ID, ids)
	}
	if !ids[narrow.ID] {
		t.Errorf("edge %d rendered but was not recorded (set %v)", narrow.ID, ids)
	}
	if len(ids) != 1 {
		t.Errorf("recorded set = %v, want exactly the one rendered id", ids)
	}
}

// TestContractRecallInjectsAMemoryAtMostOncePerSession is the feature's
// headline promise. The second turn of the same conversation asks about the
// same thing; repeating the block would spend the brief's attention budget on
// something already in context.
func TestContractRecallInjectsAMemoryAtMostOncePerSession(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1), contractRecallFact(2)})

	first := Recall(context.Background(), st, RecallRequest{SessionID: "sess-once", Prompt: contractRecallSpec})
	if first == "" {
		t.Fatal("the first call injected nothing")
	}
	second := Recall(context.Background(), st, RecallRequest{SessionID: "sess-once", Prompt: contractRecallSpec})
	if second != "" {
		t.Errorf("the second call in the same session re-injected:\n%s", second)
	}
}

// TestContractRecallDedupIsPerSession pins the key: "at most one delivery per
// session_id", so a fresh conversation gets the memory even though another
// one already has it.
func TestContractRecallDedupIsPerSession(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1)})

	first := Recall(context.Background(), st, RecallRequest{SessionID: "sess-a", Prompt: contractRecallSpec})
	if first == "" {
		t.Fatal("sess-a injected nothing")
	}
	second := Recall(context.Background(), st, RecallRequest{SessionID: "sess-b", Prompt: contractRecallSpec})
	if second != first {
		t.Errorf("a different session got %q, want the same block %q — the dedup key is the session", second, first)
	}
}

// TestContractRecallHonoursExistingRecallRows pins the skip-set as an INPUT to
// the gate rather than as a post-filter of the block: an edge already
// delivered is dropped and the next candidate takes its slot, which is what
// makes a long conversation keep surfacing new memories instead of
// progressively emptier blocks.
func TestContractRecallHonoursExistingRecallRows(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1), contractRecallFact(2)})

	if err := st.MarkRecalled("sess-skip", []int64{1}, 1); err != nil {
		t.Fatalf("seed MarkRecalled: %v", err)
	}
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-skip", Prompt: contractRecallSpec})
	if got == "" {
		t.Fatal("Recall injected nothing though edge 2 had never been delivered")
	}
	if strings.Contains(got, "[F1 E10,") {
		t.Errorf("an already-delivered edge was injected again:\n%s", got)
	}
	if !strings.Contains(got, "[F2 E20,") {
		t.Errorf("the undelivered edge is missing from the block:\n%s", got)
	}
}

// TestContractRecallEmptyWhenEverythingWasAlreadyDelivered pins the natural
// consequence: nothing new is nothing at all, not a header with no facts under
// it.
func TestContractRecallEmptyWhenEverythingWasAlreadyDelivered(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1), contractRecallFact(2)})

	if err := st.MarkRecalled("sess-all", []int64{1, 2}, 1); err != nil {
		t.Fatalf("seed MarkRecalled: %v", err)
	}
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-all", Prompt: contractRecallSpec}); got != "" {
		t.Errorf("Recall = %q, want \"\" — every candidate had already been delivered", got)
	}
}

// TestContractRecallMarksNothingForACallerThatIsGone pins amendment 2, whose
// whole subject is the gap between two clocks: the hook gives up at
// recallHookTimeout (4s) while the embedder's own budget is 60s, so a slow
// embed produces a block that lands on a closed connection. Marking it would
// retire the session's top facts as delivered-but-never-seen — permanently and
// silently, since nothing ages a kg_recall row out and no pass can tell a
// burned row from an honest one.
//
// Asserted in two halves, because the first alone is satisfiable by a Recall
// that simply refuses everything: the canceled call must record nothing, AND
// the very same session must still get the block afterwards. The second half
// is the actual promise — that the memories survived the failure.
func TestContractRecallMarksNothingForACallerThatIsGone(t *testing.T) {
	contractRecallEnv(t)
	st := storeForGraph(t)
	contractRecallWithFacts(t, []store.GraphEdge{contractRecallFact(1), contractRecallFact(2)})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the hook has already hung up

	if got := Recall(ctx, st, RecallRequest{SessionID: "sess-gone", Prompt: contractRecallSpec}); got != "" {
		t.Errorf("Recall on a canceled context = %q, want \"\" — nobody is listening", got)
	}
	ids, err := st.RecalledEdgeIDs("sess-gone")
	if err != nil {
		t.Fatalf("RecalledEdgeIDs: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("a canceled call recorded %v as delivered; those facts can never be injected into this session again", ids)
	}

	// Same session, live context: the facts must still be there to deliver.
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-gone", Prompt: contractRecallSpec})
	if got == "" {
		t.Fatal("the retry injected nothing — the canceled call burned the session's facts after all")
	}
	if !strings.Contains(got, "[F1 E10,") || !strings.Contains(got, "[F2 E20,") {
		t.Errorf("the retry is missing facts the canceled call should have left untouched:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// RunRecallHook
// ---------------------------------------------------------------------------

// contractRecallHookServer is a stand-in for `kraken serve`'s
// /api/graph/recall, recording what the hook sent.
type contractRecallHookServer struct {
	srv *httptest.Server

	mu      sync.Mutex
	hits    int
	paths   []string
	methods []string
	reqs    []RecallRequest
}

// contractRecallHookServe starts the stand-in, answers every POST with the
// given context string, and points KRAKEN_SERVE_ADDR at it.
func contractRecallHookServe(t *testing.T, context string) *contractRecallHookServer {
	t.Helper()
	s := &contractRecallHookServer{}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req RecallRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		s.mu.Lock()
		s.hits++
		s.paths = append(s.paths, r.URL.Path)
		s.methods = append(s.methods, r.Method)
		s.reqs = append(s.reqs, req)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"context": context})
	}))
	t.Cleanup(s.srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(s.srv.URL, "http://"))
	return s
}

func (s *contractRecallHookServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

// contractRecallHookPayload serializes a Claude Code hook payload.
func contractRecallHookPayload(t *testing.T, fields map[string]any) io.Reader {
	t.Helper()
	b, err := json.Marshal(fields)
	if err != nil {
		t.Fatalf("marshal hook payload: %v", err)
	}
	return bytes.NewReader(b)
}

// contractRecallHookDecode asserts out holds EXACTLY one JSON object and
// returns it. "Exactly one and nothing else" is load-bearing: Claude Code
// parses the hook's whole stdout, and a second value or stray prose there is
// the hook failing in the way the contract's opening line forbids.
func contractRecallHookDecode(t *testing.T, out string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var got map[string]any
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("hook stdout is not one JSON object (%v): %q", err, out)
	}
	if dec.More() {
		t.Errorf("hook stdout carries more than one JSON value: %q", out)
	}
	rest, _ := io.ReadAll(dec.Buffered())
	if strings.TrimSpace(string(rest)) != "" {
		t.Errorf("hook stdout carries trailing content after the JSON object: %q", out)
	}
	return got
}

// contractRecallHookContext digs additionalContext out of the hook's output
// and reports the event name beside it.
func contractRecallHookContext(t *testing.T, got map[string]any) (event, context string) {
	t.Helper()
	outer, ok := got["hookSpecificOutput"].(map[string]any)
	if !ok {
		t.Fatalf("hook output has no hookSpecificOutput object: %#v", got)
	}
	if len(got) != 1 {
		t.Errorf("hook output carries keys beside hookSpecificOutput: %#v", got)
	}
	event, _ = outer["hookEventName"].(string)
	context, _ = outer["additionalContext"].(string)
	return event, context
}

// TestContractRunRecallHookEmitsAdditionalContext is the hook's happy path
// end to end: the payload's fields reach the route as a RecallRequest, and the
// route's context comes back out as additionalContext under the payload's own
// event name, verbatim on both ends.
//
// The "Stop" subtest is deliberate and does NOT contradict amendment 1. That
// amendment drops Stop from recallHookEvents — the WIRING — while leaving
// RunRecallHook event-agnostic, precisely so re-enabling Stop is a one-literal
// change. This subtest is what makes that claim true rather than hopeful: it
// pins that the verb echoes whatever hook_event_name it is handed, including a
// name kraken no longer installs.
func TestContractRunRecallHookEmitsAdditionalContext(t *testing.T) {
	const block = "\nKnown from past work (may be stale — verify before relying):\n" +
		"full fact for any [F#]: k graph get F<id>\n- something true. [F3 E7, seen 3x]\n"
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		t.Run(event, func(t *testing.T) {
			srv := contractRecallHookServe(t, block)
			in := contractRecallHookPayload(t, map[string]any{
				"hook_event_name": event,
				"session_id":      "sess-hook",
				"prompt":          "why is the daemon slow",
				"transcript_path": "/tmp/session.jsonl",
			})
			var out bytes.Buffer
			RunRecallHook(in, &out)

			if srv.count() != 1 {
				t.Fatalf("the route was called %d times, want 1", srv.count())
			}
			srv.mu.Lock()
			path, method, req := srv.paths[0], srv.methods[0], srv.reqs[0]
			srv.mu.Unlock()
			if path != "/api/graph/recall" {
				t.Errorf("POST path = %q, want /api/graph/recall", path)
			}
			if method != http.MethodPost {
				t.Errorf("method = %q, want POST", method)
			}
			want := RecallRequest{SessionID: "sess-hook", Event: event,
				Prompt: "why is the daemon slow", TranscriptPath: "/tmp/session.jsonl"}
			if req != want {
				t.Errorf("request body = %+v, want %+v", req, want)
			}

			gotEvent, gotContext := contractRecallHookContext(t, contractRecallHookDecode(t, out.String()))
			if gotEvent != event {
				t.Errorf("hookEventName = %q, want the payload's %q verbatim", gotEvent, event)
			}
			if gotContext != block {
				t.Errorf("additionalContext = %q, want the route's context verbatim %q", gotContext, block)
			}
		})
	}
}

// TestContractRunRecallHookRespectsStopHookActive pins the loop guard, and
// pins it at the strongest point available: not merely "writes nothing" but
// "never calls the route at all". A Stop hook that injected context would
// continue the turn, whose end fires Stop again — the chain the flag exists to
// break.
func TestContractRunRecallHookRespectsStopHookActive(t *testing.T) {
	srv := contractRecallHookServe(t, "\nKnown from past work:\n- anything. [E1, seen 1x]\n")
	in := contractRecallHookPayload(t, map[string]any{
		"hook_event_name":   "Stop",
		"session_id":        "sess-loop",
		"transcript_path":   "/tmp/session.jsonl",
		"stop_hook_active":  true,
		"unrelated_field_x": "ignored",
	})
	var out bytes.Buffer
	RunRecallHook(in, &out)

	if out.Len() != 0 {
		t.Errorf("stop_hook_active wrote %q to stdout, want nothing", out.String())
	}
	if srv.count() != 0 {
		t.Errorf("stop_hook_active still called the route %d times, want 0", srv.count())
	}
}

// TestContractRunRecallHookWritesNothingForAnEmptyContext pins the common
// case: most turns have no new memory, and the hook must be invisible then —
// an empty additionalContext is still a hook output claiming to add context.
func TestContractRunRecallHookWritesNothingForAnEmptyContext(t *testing.T) {
	srv := contractRecallHookServe(t, "")
	in := contractRecallHookPayload(t, map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "sess-empty",
		"prompt":          "anything at all",
	})
	var out bytes.Buffer
	RunRecallHook(in, &out)

	if srv.count() != 1 {
		t.Errorf("the route was called %d times, want 1 — an empty answer is still an answer", srv.count())
	}
	if out.Len() != 0 {
		t.Errorf("an empty context wrote %q to stdout, want nothing", out.String())
	}
}

// TestContractRunRecallHookWritesNothingOnATransportError is the degradation
// that matters most in practice: `kraken serve` is down or restarting, and the
// hook must be a silent no-op on stdout rather than a broken turn.
func TestContractRunRecallHookWritesNothingOnATransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // nothing is listening there any more
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	in := contractRecallHookPayload(t, map[string]any{
		"hook_event_name": "UserPromptSubmit",
		"session_id":      "sess-down",
		"prompt":          "the daemon is not running",
	})
	var out bytes.Buffer
	RunRecallHook(in, &out) // must not panic
	if out.Len() != 0 {
		t.Errorf("an unreachable daemon wrote %q to stdout, want nothing", out.String())
	}
}

// TestContractRecallHookTimeoutIsFourSeconds pins the number the contract
// names. It is the only part of the timeout obligation a test can assert
// without spending the timeout itself: the point of the constant is that a
// wedged daemon costs a turn 4s rather than whatever the hook config allows,
// and a test that actually waited would cost the suite the same 4s to learn
// less.
func TestContractRecallHookTimeoutIsFourSeconds(t *testing.T) {
	if recallHookTimeout != 4*time.Second {
		t.Errorf("recallHookTimeout = %v, want 4s", recallHookTimeout)
	}
}
