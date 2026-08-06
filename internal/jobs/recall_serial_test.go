package jobs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"kraken/internal/store"
)

// recall_serial_test.go — lead-authored pins for the round-2 contract
// amendments (6 and 7): per-session serialization of Recall and the
// consecutive-identical-query short-circuit. Authored by the lead rather than
// the blind authors because the amendments landed after the blind round
// closed; identifiers are prefixed leadRecall to keep this file's namespace
// off the contract/edge suites'.

// leadRecallEnv isolates the embedder exactly as contractRecallEnv does:
// ~/.kraken/openai.env exists on this machine, so without a scratch HOME
// every call below would embed against the live API (and take the cosine arm
// instead of the lexical one the fixtures are built for).
func leadRecallEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
}

// leadRecallFacts swaps the graphFacts seam for a fixed result AND counts the
// calls — the counter is what separates "the short-circuit answered" from
// "retrieval ran and the dedup answered", which return the same "".
func leadRecallFacts(t *testing.T, edges []store.GraphEdge) *atomic.Int64 {
	t.Helper()
	var calls atomic.Int64
	prev := graphFacts
	graphFacts = func(st *store.Store, vec []float32, query string) ([]store.GraphEdge, map[int64]bool) {
		calls.Add(1)
		// nil anchors: serialization and dedup are what these cases pin, and
		// they hold for anchored and unanchored deliveries alike.
		return edges, nil
	}
	t.Cleanup(func() { graphFacts = prev })
	return &calls
}

// The fixture facts share ≥2 content words with leadRecallQuery, so they
// clear core.RelevantEdge's lexical arm under leadRecallEnv's nil vector.
const leadRecallQuery = "how does the rotation gate pick accounts"

func leadRecallEdges() []store.GraphEdge {
	return []store.GraphEdge{
		edge(1, "the rotation gate swaps accounts at cell spawn", 2),
		edge(2, "the rotation gate reads pool accounts from claude-pool", 1),
	}
}

func TestLeadRecallShortCircuitsARepeatedQuery(t *testing.T) {
	leadRecallEnv(t)
	st := storeForGraph(t)
	calls := leadRecallFacts(t, leadRecallEdges())
	sess := "lead-sess-" + t.Name()

	first := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: leadRecallQuery})
	if first == "" || !strings.Contains(first, "rotation gate") {
		t.Fatalf("first firing = %q, want a block", first)
	}
	if calls.Load() != 1 {
		t.Fatalf("retrieval calls after first firing = %d, want 1", calls.Load())
	}
	// Identical query: the dedup would blank this anyway — the assertion that
	// matters is the COUNTER, which proves retrieval was never consulted.
	if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: leadRecallQuery}); got != "" {
		t.Fatalf("repeat firing = %q, want \"\"", got)
	}
	if calls.Load() != 1 {
		t.Fatalf("retrieval calls after repeat firing = %d, want still 1 (short-circuit precedes retrieval)", calls.Load())
	}
	// A DIFFERENT query re-arms retrieval, even though everything it can
	// deliver is already marked — the "" here is the dedup's, not the
	// short-circuit's, and the counter is again what tells them apart.
	if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: "why do pool accounts rotate at spawn"}); got != "" {
		t.Fatalf("new-query firing = %q, want \"\" (all facts already delivered)", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("retrieval calls after new query = %d, want 2", calls.Load())
	}
}

func TestLeadRecallShortCircuitIsPerSession(t *testing.T) {
	leadRecallEnv(t)
	st := storeForGraph(t)
	calls := leadRecallFacts(t, leadRecallEdges())

	if got := Recall(context.Background(), st, RecallRequest{SessionID: "lead-a-" + t.Name(), Prompt: leadRecallQuery}); got == "" {
		t.Fatal("session A got no block")
	}
	// Same query, different session: A's short-circuit record must not answer
	// for B — B has been told nothing.
	if got := Recall(context.Background(), st, RecallRequest{SessionID: "lead-b-" + t.Name(), Prompt: leadRecallQuery}); got == "" {
		t.Fatal("session B got no block — the short-circuit leaked across sessions")
	}
	if calls.Load() != 2 {
		t.Fatalf("retrieval calls = %d, want 2 (one per session)", calls.Load())
	}
}

func TestLeadRecallFailedPassDoesNotArmTheShortCircuit(t *testing.T) {
	leadRecallEnv(t)
	st := storeForGraph(t)
	calls := leadRecallFacts(t, leadRecallEdges())
	sess := "lead-sess-" + t.Name()

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Recall(canceled, st, RecallRequest{SessionID: sess, Prompt: leadRecallQuery}); got != "" {
		t.Fatalf("canceled firing = %q, want \"\"", got)
	}
	// Amendment 10 bails a dead caller at the lock, BEFORE retrieval — so the
	// canceled firing must cost zero retrieval calls AND must not arm the
	// short-circuit: the retry with a live caller is exactly the case a
	// timed-out hook produces, and suppressing it would re-open the burn the
	// context checks exist to close.
	if calls.Load() != 0 {
		t.Fatalf("retrieval calls after the canceled firing = %d, want 0 (a dead caller bails at the lock)", calls.Load())
	}
	if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: leadRecallQuery}); got == "" {
		t.Fatal("retry after a canceled firing got no block — the failed pass armed the short-circuit")
	}
	if calls.Load() != 1 {
		t.Fatalf("retrieval calls = %d, want 1 (only the live retry retrieves)", calls.Load())
	}
}

func TestLeadRecallConcurrentFiringsDeliverOnce(t *testing.T) {
	leadRecallEnv(t)
	st := storeForGraph(t)
	leadRecallFacts(t, leadRecallEdges())
	sess := "lead-sess-" + t.Name()

	// Parallel tool calls fire parallel PostToolUse hooks for ONE session —
	// the producer that turned the round-1 accepted race into amendment 6.
	// Whatever the interleaving, the per-session mutex means exactly one
	// firing delivers; the rest short-circuit or see its marks.
	const firings = 8
	results := make([]string, firings)
	var wg sync.WaitGroup
	for i := 0; i < firings; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i] = Recall(context.Background(), st, RecallRequest{SessionID: sess, Prompt: leadRecallQuery})
		}(i)
	}
	wg.Wait()

	delivered := 0
	for _, r := range results {
		if r != "" {
			delivered++
		}
	}
	if delivered != 1 {
		t.Fatalf("%d of %d concurrent firings delivered a block, want exactly 1", delivered, firings)
	}
	ids, err := st.RecalledEdgeIDs(sess)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || !ids[1] || !ids[2] {
		t.Fatalf("kg_recall after the burst = %v, want exactly the two fixture ids marked once", ids)
	}
}

// TestLeadRecallHookSkipsSubagentFirings pins contract amendment 8: the hooks
// reference documents that settings hooks run inside in-process subagents and
// that their payloads carry agent_id, and a subagent sharing the parent's
// session is measured from sidecar transcripts — so a firing with an agent_id
// must write nothing AND make no request, or a read-only exploration
// subagent's tool calls would mark facts delivered under zeus' session into a
// context that is discarded when the subagent finishes.
func TestLeadRecallHookSkipsSubagentFirings(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"context":"should never be requested"}`))
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	var out strings.Builder
	RunRecallHook(strings.NewReader(
		`{"hook_event_name":"PostToolUse","session_id":"parent-sess","agent_id":"a87e4143adcb9957e","transcript_path":"/nonexistent"}`,
	), &out)
	if out.Len() != 0 {
		t.Fatalf("subagent firing wrote %q, want nothing", out.String())
	}
	if hits.Load() != 0 {
		t.Fatalf("subagent firing made %d requests, want 0", hits.Load())
	}
}
