package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/store"
)

// graphremoval_edge_test.go — the EDGE dimension of 50-contract.md slice B
// (knowledge-graph removal) for package jobs: Recall's refusals after the
// graph block is gone, the recall hook against a serve that only knows the old
// route or answers non-200, and the pipeline-stage set's boundaries. (The
// kronos settings tests were withdrawn with contract amendment 1.)
// Written blind from the contract: recall.go, recallnotes.go,
// stage code and the hook were not read. Happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `edgeGR`.

// edgeGREnv is the contract's "Test environment obligations": HOME isolated,
// OPENAI_API_KEY and KRAKEN_DB_PATH unset, before any store or Recall.
func edgeGREnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("KRAKEN_DB_PATH", "")
}

func edgeGRStore(t *testing.T) *store.Store {
	t.Helper()
	edgeGREnv(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// edgeGRJob seeds a working job whose lead session (`jobs.session`) is session.
func edgeGRJob(t *testing.T, st *store.Store, slug, session string) *store.Job {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, State: "working", Session: session})
	if err != nil {
		t.Fatalf("create job %q: %v", slug, err)
	}
	j, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return j
}

func edgeGRNote(t *testing.T, st *store.Store, j *store.Job, note string) {
	t.Helper()
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: j.Slug, Artifact: "00-overview.md", Note: note}); err != nil {
		t.Fatalf("add feedback: %v", err)
	}
}

// edgeGRGoal adds a current-week goal; current-period goals reach every job's
// goals block.
func edgeGRGoal(t *testing.T, st *store.Store, text string) {
	t.Helper()
	week, _ := store.GoalPeriods(time.Now())
	if _, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: text}, time.Now().UnixMilli()); err != nil {
		t.Fatalf("add goal: %v", err)
	}
}

const edgeGRGraphHeader = "Known from past work"

// ---------------------------------------------------------------------------
// Recall
// ---------------------------------------------------------------------------

// "A nil store or blank `session_id` returns `""`." — nil store with every
// other field populated as a lead UserPromptSubmit would be.
func TestGraphRemovalEdgeRecallNilStoreIsEmpty(t *testing.T) {
	edgeGREnv(t)
	got := Recall(context.Background(), nil, RecallRequest{
		SessionID: "sess-lead", Event: "UserPromptSubmit", Prompt: "anything", Slug: "edge-nil",
	})
	if got != "" {
		t.Fatalf("Recall(nil store) = %q, want \"\"", got)
	}
}

// "A nil store or blank `session_id` returns `""`." The sharp case: a job whose
// lead session was never stamped (jobs.session == ""), so an empty session_id
// would compare EQUAL to the lead's — it must still be "" and must not consume
// the note. Whitespace-only ids are blank too.
func TestGraphRemovalEdgeRecallBlankSessionIsEmptyEvenAgainstAnUnstampedLead(t *testing.T) {
	st := edgeGRStore(t)
	unstamped := edgeGRJob(t, st, "edge-unstamped", "")
	edgeGRNote(t, st, unstamped, "note for an unstamped job")
	edgeGRGoal(t, st, "edge goal blank session")
	stamped := edgeGRJob(t, st, "edge-stamped", "sess-lead-blank")
	edgeGRNote(t, st, stamped, "note the lead must still get")

	for _, slug := range []string{unstamped.Slug, stamped.Slug} {
		for _, sess := range []string{"", " ", "\t", "\n", "  \r\n "} {
			got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Event: "UserPromptSubmit", Prompt: "p", Slug: slug})
			if got != "" {
				t.Errorf("Recall(slug %q, session %q) = %q, want \"\"", slug, sess, got)
			}
		}
	}
	// The blank firings consumed nothing: the stamped lead still receives its note.
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-lead-blank", Event: "UserPromptSubmit", Prompt: "p", Slug: stamped.Slug})
	if !strings.Contains(got, "note the lead must still get") {
		t.Fatalf("after blank-session firings the lead did not receive its note:\n%s", got)
	}
}

// "For any other event it is `""`." — from the LEAD session, with an unread
// note and a goal both pending, every non-UserPromptSubmit event (including
// the retired PostToolUse arm, the never-wired Stop, an empty event, and
// case/whitespace variants of the one wired name) yields "".
//
// Inference (not stated in the contract, disclosed in the report): since those
// firings returned nothing, they delivered nothing, so the next real
// UserPromptSubmit must still carry both the note and the goal.
func TestGraphRemovalEdgeRecallOtherEventsAreEmptyAndConsumeNothing(t *testing.T) {
	st := edgeGRStore(t)
	j := edgeGRJob(t, st, "edge-events", "sess-lead-events")
	edgeGRNote(t, st, j, "an unread events note")
	edgeGRGoal(t, st, "edge goal events")

	for _, ev := range []string{"PostToolUse", "Stop", "", "PreToolUse", "SessionStart", "PostCompact", "userpromptsubmit", "USERPROMPTSUBMIT", "UserPromptSubmit ", " UserPromptSubmit"} {
		got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-lead-events", Event: ev, Prompt: "edge prompt " + ev, Slug: j.Slug})
		if got != "" {
			t.Errorf("Recall(event %q) = %q, want \"\"", ev, got)
		}
	}
	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-lead-events", Event: "UserPromptSubmit", Prompt: "now for real", Slug: j.Slug})
	for _, want := range []string{"an unread events note", "edge goal events"} {
		if !strings.Contains(got, want) {
			t.Errorf("first real UserPromptSubmit lacks %q after non-UPS firings:\n%s", want, got)
		}
	}
	if strings.Contains(got, edgeGRGraphHeader) {
		t.Errorf("lead block carries the retired graph header:\n%s", got)
	}
}

// "For a non-lead session it is the goals block or `""`." With a note AND a
// goal pending, a teammate's firing carries the goal and never the note — and
// never a graph block.
func TestGraphRemovalEdgeRecallNonLeadGetsGoalsOnlyNeverNotes(t *testing.T) {
	st := edgeGRStore(t)
	j := edgeGRJob(t, st, "edge-nonlead", "sess-zeus-nonlead")
	edgeGRNote(t, st, j, "a lead-only note")
	edgeGRGoal(t, st, "edge goal nonlead")

	got := Recall(context.Background(), st, RecallRequest{SessionID: "sess-teammate-nonlead", Event: "UserPromptSubmit", Prompt: "hello", Slug: j.Slug})
	if strings.Contains(got, "a lead-only note") {
		t.Fatalf("a non-lead session received the lead's note:\n%s", got)
	}
	if !strings.Contains(got, "edge goal nonlead") {
		t.Fatalf("a non-lead session's block lacks the goal (want the goals block):\n%s", got)
	}
	if strings.Contains(got, edgeGRGraphHeader) {
		t.Fatalf("non-lead block carries the retired graph header:\n%s", got)
	}
	// And the note is still waiting for the lead.
	lead := Recall(context.Background(), st, RecallRequest{SessionID: "sess-zeus-nonlead", Event: "UserPromptSubmit", Prompt: "hello", Slug: j.Slug})
	if !strings.Contains(lead, "a lead-only note") {
		t.Fatalf("the lead did not receive its note after a teammate firing:\n%s", lead)
	}
}

// "For a non-lead session it is the goals block or `""`." — with no goal at all
// and a note pending, the non-lead result is exactly "".
func TestGraphRemovalEdgeRecallNonLeadWithNoGoalsIsExactlyEmpty(t *testing.T) {
	st := edgeGRStore(t)
	j := edgeGRJob(t, st, "edge-nonlead-empty", "sess-zeus-empty")
	edgeGRNote(t, st, j, "lead only")
	for _, sess := range []string{"sess-teammate-a", "sess-thread-b"} {
		if got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Event: "UserPromptSubmit", Prompt: "p", Slug: j.Slug}); got != "" {
			t.Errorf("Recall(non-lead %q, no goals) = %q, want \"\"", sess, got)
		}
	}
}

// "the result is `notes + goals` exactly as today, with NO third block". The
// graph half used to short-circuit a byte-identical repeat query; with it gone,
// a note written between two IDENTICAL prompts must still land on the second
// (inference from "exactly as today": notes were never subject to the graph's
// same-query short-circuit — disclosed).
func TestGraphRemovalEdgeRecallIdenticalPromptStillDeliversANewNote(t *testing.T) {
	st := edgeGRStore(t)
	j := edgeGRJob(t, st, "edge-sameprompt", "sess-lead-same")
	req := RecallRequest{SessionID: "sess-lead-same", Event: "UserPromptSubmit", Prompt: "the very same prompt", Slug: j.Slug}
	if got := Recall(context.Background(), st, req); strings.Contains(got, edgeGRGraphHeader) {
		t.Fatalf("first firing carries the graph header:\n%s", got)
	}
	edgeGRNote(t, st, j, "written between identical prompts")
	got := Recall(context.Background(), st, req)
	if !strings.Contains(got, "written between identical prompts") {
		t.Fatalf("a byte-identical prompt suppressed a new note:\n%q", got)
	}
}

// "The per-session serialization (`recallSessionState`) stays." Parallel
// firings for one lead session deliver a note exactly once between them.
func TestGraphRemovalEdgeRecallParallelLeadFiringsDeliverANoteOnce(t *testing.T) {
	st := edgeGRStore(t)
	j := edgeGRJob(t, st, "edge-parallel", "sess-lead-par")
	edgeGRNote(t, st, j, "deliver me once")

	const n = 8
	var wg sync.WaitGroup
	out := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			out[i] = Recall(context.Background(), st, RecallRequest{SessionID: "sess-lead-par", Event: "UserPromptSubmit", Prompt: "parallel", Slug: j.Slug})
		}(i)
	}
	wg.Wait()
	count := 0
	for _, o := range out {
		count += strings.Count(o, "deliver me once")
	}
	if count != 1 {
		t.Fatalf("note delivered %d times across %d parallel firings, want exactly 1: %q", count, n, out)
	}
}

// "All five keys still decode; `Prompt` and `TranscriptPath` are read by
// nothing but are accepted (old hook binaries send them)." Edge shapes an old
// binary can send: JSON null for the unread two, and an empty object.
func TestGraphRemovalEdgeRecallRequestDecodesNullsAndEmpty(t *testing.T) {
	var r RecallRequest
	if err := json.Unmarshal([]byte(`{"session_id":"s","event":"UserPromptSubmit","prompt":null,"transcript_path":null,"slug":"x"}`), &r); err != nil {
		t.Fatalf("decode with nulls: %v", err)
	}
	if r.SessionID != "s" || r.Event != "UserPromptSubmit" || r.Slug != "x" || r.Prompt != "" || r.TranscriptPath != "" {
		t.Fatalf("decoded %+v", r)
	}
	var empty RecallRequest
	if err := json.Unmarshal([]byte(`{}`), &empty); err != nil {
		t.Fatalf("decode {}: %v", err)
	}
	if !reflect.DeepEqual(empty, RecallRequest{}) {
		t.Fatalf("decode {} = %+v, want zero value", empty)
	}
}

// ---------------------------------------------------------------------------
// RunRecallHook
// ---------------------------------------------------------------------------

type edgeGRHookHit struct {
	path, method string
}

func edgeGRHookServer(t *testing.T, h http.HandlerFunc) *[]edgeGRHookHit {
	t.Helper()
	var mu sync.Mutex
	hits := &[]edgeGRHookHit{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*hits = append(*hits, edgeGRHookHit{r.URL.Path, r.Method})
		mu.Unlock()
		io.Copy(io.Discard, r.Body)
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	return hits
}

const edgeGRHookPayload = `{"hook_event_name":"UserPromptSubmit","session_id":"sess-hook","prompt":"p","transcript_path":"/tmp/edge.jsonl"}`

// "POSTs to `ServeBaseURL() + "/api/recall"`; … a non-200 … emits nothing on
// `out`." A NEW hook against an OLD serve that only answers the retired
// /api/graph/recall: the hook hits /api/recall, gets the old serve's 404, and
// stays silent — it never falls back to the retired path.
func TestGraphRemovalEdgeRecallHookAgainstAnOldServeIsSilent(t *testing.T) {
	edgeGREnv(t)
	hits := edgeGRHookServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/graph/recall" {
			io.WriteString(w, `{"context":"FROM THE RETIRED ROUTE"}`)
			return
		}
		http.NotFound(w, r)
	})
	var out bytes.Buffer
	RunRecallHook(strings.NewReader(edgeGRHookPayload), &out, "edge-slug")
	if strings.TrimSpace(out.String()) != "" {
		t.Errorf("hook wrote %q against a serve answering 404, want nothing", out.String())
	}
	for _, h := range *hits {
		if h.path == "/api/graph/recall" {
			t.Errorf("hook reached the retired route %s", h.path)
		}
	}
	if len(*hits) == 0 || (*hits)[0].path != "/api/recall" || (*hits)[0].method != http.MethodPost {
		t.Errorf("hook requests = %+v, want a POST to /api/recall", *hits)
	}
}

// "a non-200 … emits nothing on `out`." — every non-200 status, each carrying
// a body that WOULD be a valid context if the status were ignored.
func TestGraphRemovalEdgeRecallHookNon200IsSilent(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusBadRequest, http.StatusMethodNotAllowed, http.StatusRequestEntityTooLarge, http.StatusInternalServerError, http.StatusAccepted, http.StatusNoContent} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			edgeGREnv(t)
			edgeGRHookServer(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(code)
				io.WriteString(w, `{"context":"SHOULD NOT BE EMITTED"}`)
			})
			var out bytes.Buffer
			RunRecallHook(strings.NewReader(edgeGRHookPayload), &out)
			if strings.TrimSpace(out.String()) != "" {
				t.Errorf("status %d: hook wrote %q, want nothing", code, out.String())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Pipeline stages
// ---------------------------------------------------------------------------

// "`IsPipelineStage("graph") == false`; `PipelineStages()` returns exactly
// `[aim do dream info pr]`." Edge half: the reference skill `load` and the
// retired `graph` are both outside the set (with near-miss spellings), and the
// two functions agree on every member.
func TestGraphRemovalEdgePipelineStageBoundaries(t *testing.T) {
	for _, s := range []string{"graph", "load", "", "graphs", "graph ", "/graph"} {
		if IsPipelineStage(s) {
			t.Errorf("IsPipelineStage(%q) = true, want false", s)
		}
	}
	for _, s := range PipelineStages() {
		if !IsPipelineStage(s) {
			t.Errorf("PipelineStages() lists %q but IsPipelineStage(%q) = false", s, s)
		}
		if s == "graph" {
			t.Errorf("PipelineStages() still lists graph")
		}
	}
}
