package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"testing/iotest"
	"time"

	"kraken/internal/store"
)

// recall_edge_test.go — the EDGE dimension of 50-contract.md's Slice B:
// jobs.Recall's refusals and budget boundaries, and jobs.RunRecallHook's
// every-failure-is-silence posture. Happy paths and the ordinary
// retrieve-render-mark invariant belong to the sibling blind author.
//
// The feature's one absolute rule is that a hook which breaks a conversation
// is a bug by definition, so almost every assertion below is of the same
// shape: given something wrong, the observable result is "" or an untouched
// output stream — never an error, never a panic, never a partial block.
//
// Package-level identifiers are prefixed `edgeRecall`: this package's test
// namespace already carries an `edge`/`edge*` family (graphctx_test.go's
// `edge`, linear_edge_test.go's helpers) and a second blind author is writing
// beside this file.

// --- fixtures --------------------------------------------------------------

// edgeRecallEnv isolates the embedder credential for one test, and it is what
// makes this file's lexical-gate premise TRUE rather than merely assumed.
//
// The contract's note for authors says tests run with no OPENAI_API_KEY, so
// the query vector is nil and core.RelevantEdge runs its lexical arm. That is
// only half the story: core.NewEmbedder falls back to ~/.kraken/openai.env
// (core/embed.go's EmbedEnvPath), which EXISTS on the machine this suite runs
// on — so without this helper every test that reaches Recall's embed step made
// a live OpenAI call, and the gate under test was the cosine arm against a
// real vector rather than the lexical arm the fixtures are built for. Pointing
// HOME at a temp dir removes the file from reach; blanking the variable
// removes the process-env override that wins over it.
//
// Called by every Recall test in this file, including the ones that refuse
// before the embed step is reached: the refusals' ORDER is Recall's business,
// not this file's, and a test that relies on returning early would silently
// start making network calls the day that order changes.
func edgeRecallEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
}

func edgeRecallStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// edgeRecallWithFacts swaps the retrieval seam for one test and restores it,
// so a failure in one case cannot leak a stub into the next. Retrieval itself
// is core.GraphSearch's business and has its own tests; what is under test
// here is the gate, the budget, the dedup and the rendering.
func edgeRecallWithFacts(t *testing.T, edges []store.GraphEdge) {
	t.Helper()
	prev := graphFacts
	graphFacts = func(*store.Store, []float32, string) ([]store.GraphEdge, map[int64]bool) { return edges, nil }
	t.Cleanup(func() { graphFacts = prev })
}

// edgeRecallFact builds an un-embedded fact. Paired with edgeRecallEnv above
// — which is what actually leaves NewEmbedder with nothing to read — the query
// vector is nil, so every one of these is judged by core.RelevantEdge's
// lexical arm (>= 2 shared content words).
func edgeRecallFact(id int64, fact string) store.GraphEdge {
	return store.GraphEdge{ID: id, EpisodeID: id * 10, Fact: fact, Occurrences: 1}
}

// edgeRecallQuery and the facts below share three content words, which is
// what clears the lexical gate. Kept as one constant so a fixture can never
// drift out of agreement with the gate it is meant to pass.
const edgeRecallQuery = "daemon throttle watermark stamps"

func edgeRecallRelevantFacts(n int) []store.GraphEdge {
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel",
		"india", "juliett", "kilo", "lima"}
	var out []store.GraphEdge
	for i := 0; i < n && i < len(words); i++ {
		out = append(out, edgeRecallFact(int64(i+1),
			"the daemon throttle stamps watermark "+words[i]))
	}
	return out
}

func edgeRecallIDs(edges []store.GraphEdge) []int64 {
	var out []int64
	for _, e := range edges {
		out = append(out, e.ID)
	}
	return out
}

func edgeRecallLines(block string) []string {
	body := strings.TrimPrefix(block, graphContextHeader)
	body = strings.TrimSuffix(body, "\n")
	if body == "" {
		return nil
	}
	return strings.Split(body, "\n")
}

// --- Recall: refusals ------------------------------------------------------

// TestEdgeRecallRefusesABlankSession pins the dedup key's precondition. A
// session id that is empty or nothing but whitespace cannot be recorded, so a
// block delivered under it would be delivered again on every following turn
// of the same conversation — which is the one failure mode "at most once per
// session" exists to prevent.
func TestEdgeRecallRefusesABlankSession(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	edgeRecallWithFacts(t, edgeRecallRelevantFacts(3))
	for _, sess := range []string{"", " ", "\t", "\n", "  \t\r\n  "} {
		got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Event: "UserPromptSubmit", Prompt: edgeRecallQuery})
		if got != "" {
			t.Errorf("Recall with session %q returned a block:\n%s", sess, got)
		}
	}
}

// TestEdgeRecallRefusesANilStore is the same posture graphContext holds on
// the spawn path: a cell running outside `kraken serve` has no store, and
// that is a reason to inject nothing, never a reason to fail.
func TestEdgeRecallRefusesANilStore(t *testing.T) {
	edgeRecallEnv(t)
	edgeRecallWithFacts(t, edgeRecallRelevantFacts(3))
	if got := Recall(context.Background(), nil, RecallRequest{SessionID: "s1", Prompt: edgeRecallQuery}); got != "" {
		t.Errorf("Recall(nil store) = %q, want \"\"", got)
	}
}

// TestEdgeRecallEmptyQueryYieldsNothing covers every way both query sources
// can come up empty at once: no prompt and no transcript, a whitespace-only
// prompt and a transcript path that does not exist, and a transcript that
// exists but holds no assistant text. There is nothing to retrieve AGAINST,
// and retrieving against "" would return the corpus' most generic facts.
func TestEdgeRecallEmptyQueryYieldsNothing(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	edgeRecallWithFacts(t, edgeRecallRelevantFacts(3))
	dir := t.TempDir()

	empty := filepath.Join(dir, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("write empty transcript: %v", err)
	}
	userOnly := filepath.Join(dir, "useronly.jsonl")
	if err := os.WriteFile(userOnly,
		[]byte(`{"type":"user","message":{"content":"the daemon throttle watermark stamps"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write user-only transcript: %v", err)
	}

	for name, req := range map[string]RecallRequest{
		"no prompt, no transcript":     {SessionID: "s1", Event: "Stop"},
		"blank prompt, no transcript":  {SessionID: "s1", Event: "Stop", Prompt: "   \n\t "},
		"blank prompt, missing file":   {SessionID: "s1", Event: "Stop", Prompt: "  ", TranscriptPath: filepath.Join(dir, "nope.jsonl")},
		"blank prompt, empty file":     {SessionID: "s1", Event: "Stop", Prompt: "", TranscriptPath: empty},
		"blank prompt, no assistant":   {SessionID: "s1", Event: "Stop", Prompt: "", TranscriptPath: userOnly},
		"blank prompt, transcript dir": {SessionID: "s1", Event: "Stop", Prompt: "", TranscriptPath: dir},
	} {
		if got := Recall(context.Background(), st, req); got != "" {
			t.Errorf("Recall(%s) returned a block:\n%s", name, got)
		}
	}
}

// TestEdgeRecallFallsBackToTheTranscript is the boundary between the two
// query sources: a whitespace-only (or absent) prompt must fall through to
// the last thing the assistant SAID rather than refusing.
//
// Round 2 wired PostToolUse, whose payload carries no prompt — so this
// fallback went from "unreached escape hatch" to the busiest query path in
// the feature, and this test went from guarding rot to pinning the hot path.
// (Comment updated by the lead when amendment 5 landed; the assertions are
// the blind author's, unchanged.)
func TestEdgeRecallFallsBackToTheTranscript(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	edgeRecallWithFacts(t, edgeRecallRelevantFacts(1))
	path := filepath.Join(t.TempDir(), "t.jsonl")
	body := `{"type":"user","message":{"content":"unrelated ios testflight upload chatter"}}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"the daemon throttle stamps its watermark before any early bail"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	got := Recall(context.Background(), st, RecallRequest{SessionID: "s-fallback", Event: "Stop", Prompt: "   ", TranscriptPath: path})
	if got == "" {
		t.Fatal("Recall with a blank prompt did not fall back to the transcript's last assistant text")
	}
	if !strings.HasPrefix(got, graphContextHeader) {
		t.Errorf("block does not open with the shared header:\n%q", got)
	}
}

// TestEdgeRecallDropsIrrelevantCandidates pins that the shared gate really is
// applied here too. Retrieval ranks candidates against each other, so the
// best of a bad set still comes back first; injecting it mid-conversation
// would be worse than on the spawn path, since the reader is mid-thought.
func TestEdgeRecallDropsIrrelevantCandidates(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	edgeRecallWithFacts(t, []store.GraphEdge{
		edgeRecallFact(1, "ios TestFlight uploads burn a bundle version permanently"),
		edgeRecallFact(2, "kraken signs its binary with a real identity"),
	})
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s-gate", Prompt: edgeRecallQuery}); got != "" {
		t.Errorf("facts sharing at most one content word with the query were injected:\n%s", got)
	}
}

// TestEdgeRecallFailsClosedWhenTheSkipSetCannotBeRead is the contract's
// explicit fail-closed clause. Without dedup knowledge a delivery cannot be
// recorded as one, so injecting would risk repeating the same block on every
// turn for the rest of the conversation — strictly worse than injecting
// nothing.
func TestEdgeRecallFailsClosedWhenTheSkipSetCannotBeRead(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	edgeRecallWithFacts(t, edgeRecallRelevantFacts(3))
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s-closed", Prompt: edgeRecallQuery}); got != "" {
		t.Errorf("Recall against an unreadable store returned a block:\n%s", got)
	}
}

// --- Recall: dedup ---------------------------------------------------------

// TestEdgeRecallSkipsCandidatesAlreadyDelivered covers the case where the
// skip-set swallows EVERYTHING: the answer is "" (a header with nothing under
// it claims the graph knows something and then says nothing), and no new
// recall row is written, since nothing was delivered.
func TestEdgeRecallSkipsCandidatesAlreadyDelivered(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	facts := edgeRecallRelevantFacts(4)
	edgeRecallWithFacts(t, facts)
	const sess = "s-alldelivered"
	if err := st.MarkRecalled(sess, edgeRecallIDs(facts), 1_700_000_000_000); err != nil {
		t.Fatalf("seed skip-set: %v", err)
	}
	before, err := st.RecalledEdgeIDs(sess)
	if err != nil {
		t.Fatalf("read skip-set: %v", err)
	}

	if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: edgeRecallQuery}); got != "" {
		t.Errorf("every candidate was already delivered, yet a block was returned:\n%s", got)
	}

	after, err := st.RecalledEdgeIDs(sess)
	if err != nil {
		t.Fatalf("re-read skip-set: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("recall set grew from %d to %d ids on a delivery of nothing", len(before), len(after))
	}
	for id := range after {
		if !before[id] {
			t.Errorf("edge %d was marked despite nothing being injected", id)
		}
	}
}

// TestEdgeRecallInjectsAMemoryAtMostOncePerSession is the feature's headline
// guarantee observed from the outside, and it is also the only way to see
// that the successful path really did write its rows: a second identical call
// in the same session must come back empty.
func TestEdgeRecallInjectsAMemoryAtMostOncePerSession(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	edgeRecallWithFacts(t, edgeRecallRelevantFacts(3))
	const sess = "s-once"

	first := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: edgeRecallQuery})
	if first == "" {
		t.Fatal("the first call injected nothing — fixtures do not clear the gate")
	}
	second := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: edgeRecallQuery})
	if second != "" {
		t.Errorf("the same facts were injected twice into one session:\n%s", second)
	}
	// A DIFFERENT session is untouched by the first session's deliveries.
	other := Recall(context.Background(), st, RecallRequest{SessionID: sess + "-other", Prompt: edgeRecallQuery})
	if other == "" {
		t.Error("a second session was muted by the first session's recall rows")
	}
}

// TestEdgeRecallMarksNothingForACallerThatIsGone pins amendment 2: a done
// context immediately before the mark means return "" and record NOTHING.
//
// This is the asymmetry the whole feature turns on. The hook gives up at
// recallHookTimeout (4s) while the embedder's own client budget is 60s
// (core/embed.go), so a single slow embedding call lets the server finish a
// block for a caller that stopped listening. Marking it would be worse than
// useless: a recall row is permanent — nothing updates one and nothing deletes
// one — so the session's TOP-ranked facts would be recorded as delivered
// while the conversation never saw a byte of them, and no pass anywhere could
// recover them.
//
// A pre-canceled context stands in for the timed-out caller: what Recall can
// observe is `ctx.Done()`, and it cannot tell a client that hung up from one
// that was canceled outright. The second call is the assertion that matters —
// checking `RecalledEdgeIDs` is empty only proves no ROW was written, while
// re-running with a live context proves the facts are still ELIGIBLE, which is
// what "burned" would actually cost.
func TestEdgeRecallMarksNothingForACallerThatIsGone(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	edgeRecallWithFacts(t, edgeRecallRelevantFacts(3))
	const sess = "s-gone"

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Recall(ctx, st, RecallRequest{SessionID: sess, Prompt: edgeRecallQuery}); got != "" {
		t.Errorf("Recall with a done context returned a block nobody can receive:\n%s", got)
	}

	marked, err := st.RecalledEdgeIDs(sess)
	if err != nil {
		t.Fatalf("read recall rows: %v", err)
	}
	if len(marked) != 0 {
		t.Errorf("%d edge(s) recorded as delivered for a caller that was already gone: %v", len(marked), marked)
	}

	// The facts must still be on offer. If the canceled call had marked them,
	// this call would return "" — indistinguishable from "the graph had
	// nothing", which is exactly how the bug hid.
	if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: edgeRecallQuery}); got == "" {
		t.Error("the canceled call burned the session's facts — a delivery that never happened must leave them eligible")
	}
}

// TestEdgeRecallNeverInjectsWorldTimeClosedFacts pins the gate's temporal
// half. "We used X until March" is stored as one LIVE edge with both bounds
// and retrieval serves it; injecting it as a bare claim would present the
// graph's own known-ended beliefs as current fact — and mid-conversation
// there is no brief around it to hedge with.
func TestEdgeRecallNeverInjectsWorldTimeClosedFacts(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	now := time.Now().UnixMilli()

	closed := edgeRecallRelevantFacts(3)
	for i := range closed {
		closed[i].ValidUntil = now - 60_000
	}
	edgeRecallWithFacts(t, closed)
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s-closed-facts", Prompt: edgeRecallQuery}); got != "" {
		t.Errorf("facts whose world-time window has closed were injected:\n%s", got)
	}

	// The other side of the boundary: a FUTURE valid_until is a fact that is
	// currently true, and dropping it would silently shrink the graph to
	// facts nobody ever bounded.
	future := edgeRecallRelevantFacts(3)
	for i := range future {
		future[i].ValidUntil = now + 3_600_000
	}
	edgeRecallWithFacts(t, future)
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "s-future-facts", Prompt: edgeRecallQuery}); got == "" {
		t.Error("a fact valid until a future instant was dropped — \"true until March\" is currently true")
	}
}

// --- Recall: the shared caps ----------------------------------------------

// TestEdgeRecallHonoursTheFactCap pins graphContextFacts through Recall. The
// fixtures are sized so the BYTE cap alone would let all eight through: an
// implementation that reached the shared renderer without the fact cap would
// otherwise pass every other test here.
func TestEdgeRecallHonoursTheFactCap(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	facts := edgeRecallRelevantFacts(8)
	edgeRecallWithFacts(t, facts)
	got := Recall(context.Background(), st, RecallRequest{SessionID: "s-cap", Prompt: edgeRecallQuery})
	if got == "" {
		t.Fatal("eight clearing facts produced no block")
	}
	lines := edgeRecallLines(got)
	if len(lines) != graphContextFacts {
		t.Errorf("block carries %d facts, want exactly %d:\n%s", len(lines), graphContextFacts, got)
	}
	if len(got) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap:\n%s", len(got), graphContextBytes, got)
	}
}

// TestEdgeRecallSkipsAnOversizedFactRatherThanBreaking pins the rendering
// rule the shared-decisions section hands to renderGraphBlock, observed
// through Recall's output. The two caps are not independent — one caps RUNES
// and the other BYTES — so a single fact in multi-byte prose can exceed the
// byte budget on its own. Breaking there would zero the whole block; the rule
// is to skip that fact and let the shorter ones behind it land.
func TestEdgeRecallSkipsAnOversizedFactRatherThanBreaking(t *testing.T) {
	edgeRecallEnv(t)
	st := edgeRecallStore(t)
	// 170 runes, ~630 bytes: under the rune cap, over the byte budget on its
	// own. The repeated rune is a SYMBOL rather than a letter, so it
	// contributes no content term and the two leading words carry the gate.
	huge := edgeRecallFact(1, "daemon throttle "+strings.Repeat("\U0001D11E", 154))
	short := edgeRecallFact(2, "the daemon throttle stamps watermark alpha")
	edgeRecallWithFacts(t, []store.GraphEdge{huge, short})

	got := Recall(context.Background(), st, RecallRequest{SessionID: "s-oversized", Prompt: edgeRecallQuery})
	if got == "" {
		t.Fatal("an oversized leading fact zeroed the whole block — it must be skipped, not break the loop")
	}
	if strings.Contains(got, "\U0001D11E") {
		t.Errorf("the oversized fact was rendered anyway:\n%q", got)
	}
	if len(got) > graphContextBytes {
		t.Errorf("block is %d bytes, over the %d-byte cap", len(got), graphContextBytes)
	}
	if !strings.Contains(got, "[E20, seen 1x]") {
		t.Errorf("the short fact behind the oversized one did not land:\n%s", got)
	}
	// Every emitted line is whole: a half sentence with an episode id after
	// it reads as a complete claim, which is the one way this block could
	// actively mislead.
	for _, line := range edgeRecallLines(got) {
		if !strings.HasPrefix(line, "- ") || !strings.HasSuffix(line, "x]") {
			t.Errorf("truncated mid-fact: %q", line)
		}
	}
}

// --- RunRecallHook ---------------------------------------------------------

// edgeRecallHookServer stands up a counting httptest server and points
// ServeBaseURL at it. The counter is what makes "no request was made"
// assertable, which is the only way to see the stop_hook_active loop guard
// from outside the process.
func edgeRecallHookServer(t *testing.T, h http.HandlerFunc) *int64 {
	t.Helper()
	var hits int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	return &hits
}

// edgeRecallHookOut runs the hook over payload and returns everything it
// wrote.
func edgeRecallHookOut(t *testing.T, payload string) string {
	t.Helper()
	var out bytes.Buffer
	RunRecallHook(strings.NewReader(payload), &out)
	return out.String()
}

// TestEdgeRecallHookTimeoutIsBounded pins the frozen constant. A hook blocks
// the turn it fires on, so this bound is the difference between a wedged
// daemon costing four seconds and it costing whatever Claude Code's own hook
// timeout happens to be.
func TestEdgeRecallHookTimeoutIsBounded(t *testing.T) {
	if recallHookTimeout != 4*time.Second {
		t.Errorf("recallHookTimeout = %v, want 4s", recallHookTimeout)
	}
}

// TestEdgeRecallHookEmitsTheEventNameVerbatim pins the echo. The
// hookEventName comes back from the payload rather than being chosen by
// kraken: Claude Code matches a hook's output against the event it fired, so
// a hardcoded name would make the hook silently inert.
//
// Both event names are exercised: UserPromptSubmit is wired, and the second
// subtest's "Stop" stands in for ANY unwired event, pinning that the verb
// stays event-agnostic (recallHookEvents is now UserPromptSubmit +
// PostToolUse, and amendment 1's escape hatch — "re-enabling Stop is a
// one-literal change" — is only true while something checks that the verb
// echoes whatever name it is handed). (Comment updated by the lead when
// amendment 5 landed; the assertions are the blind author's, unchanged.)
func TestEdgeRecallHookEmitsTheEventNameVerbatim(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "Stop"} {
		t.Run(event, func(t *testing.T) {
			var gotMethod, gotPath string
			var gotBody []byte
			hits := edgeRecallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath = r.Method, r.URL.Path
				gotBody, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, `{"context":"RECALLED BLOCK"}`)
			})

			payload := `{"hook_event_name":"` + event + `","session_id":"sess-42",` +
				`"prompt":"what did we decide about the throttle",` +
				`"transcript_path":"/tmp/edge-recall.jsonl","stop_hook_active":false}`
			out := edgeRecallHookOut(t, payload)

			if n := atomic.LoadInt64(hits); n != 1 {
				t.Fatalf("server saw %d requests, want exactly 1", n)
			}
			if gotMethod != http.MethodPost || gotPath != "/api/graph/recall" {
				t.Errorf("hook called %s %s, want POST /api/graph/recall", gotMethod, gotPath)
			}
			var req map[string]any
			if err := json.Unmarshal(gotBody, &req); err != nil {
				t.Fatalf("posted body is not JSON (%v): %q", err, gotBody)
			}
			if req["session_id"] != "sess-42" {
				t.Errorf("posted session_id = %v, want sess-42", req["session_id"])
			}
			if req["prompt"] != "what did we decide about the throttle" {
				t.Errorf("posted prompt = %v", req["prompt"])
			}
			if req["transcript_path"] != "/tmp/edge-recall.jsonl" {
				t.Errorf("posted transcript_path = %v", req["transcript_path"])
			}

			dec := json.NewDecoder(strings.NewReader(out))
			var emitted struct {
				HookSpecificOutput struct {
					HookEventName     string `json:"hookEventName"`
					AdditionalContext string `json:"additionalContext"`
				} `json:"hookSpecificOutput"`
			}
			if err := dec.Decode(&emitted); err != nil {
				t.Fatalf("hook output is not one JSON object (%v): %q", err, out)
			}
			if emitted.HookSpecificOutput.HookEventName != event {
				t.Errorf("hookEventName = %q, want %q verbatim from the payload",
					emitted.HookSpecificOutput.HookEventName, event)
			}
			if emitted.HookSpecificOutput.AdditionalContext != "RECALLED BLOCK" {
				t.Errorf("additionalContext = %q, want the server's context",
					emitted.HookSpecificOutput.AdditionalContext)
			}
			// "exactly one JSON object and nothing else"
			rest, _ := io.ReadAll(dec.Buffered())
			if strings.TrimSpace(string(rest)) != "" {
				t.Errorf("hook wrote trailing output after its object: %q", rest)
			}
		})
	}
}

// TestEdgeRecallHookStopHookActiveMakesNoRequest is the loop guard. An
// injection-continued turn firing Stop again would chain another injection,
// and the request itself is what has to not happen — a server that answered
// would record a delivery for a turn nobody asked about.
func TestEdgeRecallHookStopHookActiveMakesNoRequest(t *testing.T) {
	hits := edgeRecallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"context":"SHOULD NEVER BE FETCHED"}`)
	})
	out := edgeRecallHookOut(t,
		`{"hook_event_name":"Stop","session_id":"sess-loop","stop_hook_active":true,"transcript_path":"/tmp/x.jsonl"}`)
	if n := atomic.LoadInt64(hits); n != 0 {
		t.Errorf("stop_hook_active made %d HTTP requests, want 0", n)
	}
	if strings.TrimSpace(out) != "" {
		t.Errorf("stop_hook_active wrote %q, want nothing", out)
	}
}

// TestEdgeRecallHookIsSilentOnEveryServerFailure walks the response side of
// "any failure writes nothing". Each of these is a live conversation waiting
// on the hook; a malformed or empty emission is worse than silence, because
// Claude Code would either reject it or inject an empty context block.
func TestEdgeRecallHookIsSilentOnEveryServerFailure(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"500 with a valid body": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			io.WriteString(w, `{"context":"do not use me"}`)
		},
		"404": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		},
		"200 with an undecodable body": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, "not json at all")
		},
		"200 with an empty body": func(w http.ResponseWriter, r *http.Request) {},
		"200 with an empty context": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"context":""}`)
		},
		"200 with no context key": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"other":"value"}`)
		},
		"200 with a context of the wrong type": func(w http.ResponseWriter, r *http.Request) {
			io.WriteString(w, `{"context":42}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			edgeRecallHookServer(t, h)
			out := edgeRecallHookOut(t,
				`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"p","transcript_path":"/tmp/t.jsonl"}`)
			if strings.TrimSpace(out) != "" {
				t.Errorf("wrote %q, want nothing", out)
			}
		})
	}
}

// TestEdgeRecallHookIsSilentOnAMalformedPayload walks the request side. The
// hook payload schema is Claude Code's, not kraken's, and can drift out from
// under this verb at any release.
func TestEdgeRecallHookIsSilentOnAMalformedPayload(t *testing.T) {
	edgeRecallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"context":"SHOULD NOT BE EMITTED"}`)
	})
	for name, payload := range map[string]string{
		"empty stdin":       ``,
		"truncated object":  `{"hook_event_name":"Stop","session_id":`,
		"not an object":     `[1,2,3]`,
		"a bare string":     `"just a string"`,
		"whitespace only":   "   \n\t\n",
		"nul bytes":         "\x00\x00",
		"wrongly typed ids": `{"hook_event_name":123,"session_id":{"nested":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if out := edgeRecallHookOut(t, payload); strings.TrimSpace(out) != "" {
				t.Errorf("wrote %q, want nothing", out)
			}
		})
	}
}

// TestEdgeRecallHookIsSilentWhenStdinErrors covers the read itself failing —
// a closed pipe, a hook harness that died mid-write. io.ReadAll returning an
// error must not panic and must not emit.
func TestEdgeRecallHookIsSilentWhenStdinErrors(t *testing.T) {
	edgeRecallHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"context":"SHOULD NOT BE EMITTED"}`)
	})
	var out bytes.Buffer
	RunRecallHook(iotest.ErrReader(errors.New("edge: stdin exploded")), &out)
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("wrote %q, want nothing", out.String())
	}
}

// TestEdgeRecallHookIsSilentWhenServeIsUnreachable is the common case in
// practice: a cell whose `kraken serve` is down or restarting. The hook must
// cost the turn a refused connection and nothing else.
func TestEdgeRecallHookIsSilentWhenServeIsUnreachable(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens there anymore: connections refuse immediately
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	out := edgeRecallHookOut(t,
		`{"hook_event_name":"UserPromptSubmit","session_id":"s","prompt":"p","transcript_path":"/tmp/t.jsonl"}`)
	if strings.TrimSpace(out) != "" {
		t.Errorf("wrote %q against an unreachable serve, want nothing", out)
	}
}
