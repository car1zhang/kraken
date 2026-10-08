package jobs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// graphremoval_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice B's `jobs` rows (PR 1, knowledge-graph
// removal). Written from the frozen contract alone; no non-test file under
// internal/ was read. Harness patterns (UseStore, JobBySlug/LoadJobs,
// AddGoal/GoalPeriods, AddFeedback)
// were learned from sibling _test.go files only.
//
// Dimension: LOGIC — stated invariants and happy paths. Refusals and degenerate
// inputs beyond the contract's own `""` arms are athena-edge's.
//
// Every package-level identifier here is prefixed `contract`.

// contractGREnv is the Test environment obligation: isolate HOME and unset
// OPENAI_API_KEY and KRAKEN_DB_PATH before opening a store or calling Recall.
func contractGREnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{"OPENAI_API_KEY", "KRAKEN_DB_PATH"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// contractGRStore opens a scratch store under the isolated env and wires it as
// the package's active store.
func contractGRStore(t *testing.T) *store.Store {
	t.Helper()
	contractGREnv(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })
	return st
}

// contractGRJob creates a working job row stamped with the given lead session
// and returns its store id.
func contractGRJob(t *testing.T, st *store.Store, slug, session string) int64 {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", State: "working", Session: session, Body: "rework the pane discovery loop"})
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return id
}

// contractGRDayGoal seeds one current-period day goal.
func contractGRDayGoal(t *testing.T, st *store.Store, text string) {
	t.Helper()
	_, day := store.GoalPeriods(time.Now())
	if _, err := st.AddGoal(store.GoalAdd{Scope: "day", Period: day, Text: text}, 1_700_000_000_000); err != nil {
		t.Fatalf("AddGoal: %v", err)
	}
}

func contractGRFire(st *store.Store, slug, session, event string) string {
	return Recall(context.Background(), st, RecallRequest{
		SessionID: session, Event: event, Slug: slug, Prompt: "carry on with the pane loop",
	})
}

const contractGRThirdBlock = "Known from past work"

// "For `Event == "UserPromptSubmit"` from the job's lead session (`jobs.session`)
// the result is `notes + goals` exactly as today, with NO third block: the string
// never contains `Known from past work`." / "No embedder is constructed and no
// network call is made".
func TestGraphRemovalRecallLeadGetsNotesThenGoalsOnly(t *testing.T) {
	st := contractGRStore(t)
	const slug, sess = "graphremoval-lead", "sess-graphremoval-lead"
	id := contractGRJob(t, st, slug, sess)
	const note, goal = "tighten the pane discovery overview", "ship the memory rework"
	if _, err := st.AddFeedback(store.Feedback{JobID: id, JobSlug: slug, Artifact: "00-overview.md", Note: note}); err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	contractGRDayGoal(t, st, goal)

	got := contractGRFire(st, slug, sess, "UserPromptSubmit")
	ni, gi := strings.Index(got, note), strings.Index(got, goal)
	if ni < 0 {
		t.Fatalf("lead recall lacks the unread note %q:\n%s", note, got)
	}
	if gi < 0 {
		t.Fatalf("lead recall lacks the goal %q:\n%s", goal, got)
	}
	if ni > gi {
		t.Errorf("notes must precede goals (notes + goals):\n%s", got)
	}
	if strings.Contains(got, contractGRThirdBlock) {
		t.Errorf("lead recall carries the retired graph block %q:\n%s", contractGRThirdBlock, got)
	}
}

// "For a non-lead session it is the goals block or `""`." — never the lead's
// notes, never the graph block.
func TestGraphRemovalRecallNonLeadGetsGoalsOrNothing(t *testing.T) {
	st := contractGRStore(t)
	const slug = "graphremoval-teammate"
	id := contractGRJob(t, st, slug, "sess-zeus")
	const note, goal = "a note for zeus alone", "keep the watch store intact"
	if _, err := st.AddFeedback(store.Feedback{JobID: id, JobSlug: slug, Note: note}); err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	contractGRDayGoal(t, st, goal)

	got := contractGRFire(st, slug, "sess-teammate", "UserPromptSubmit")
	if strings.Contains(got, note) {
		t.Errorf("non-lead session received the lead's notes:\n%s", got)
	}
	if strings.Contains(got, contractGRThirdBlock) {
		t.Errorf("non-lead recall carries the retired graph block:\n%s", got)
	}
	if got != "" && !strings.Contains(got, goal) {
		t.Errorf("non-lead recall is neither \"\" nor the goals block:\n%s", got)
	}
}

// "For any other event it is `""`."
func TestGraphRemovalRecallOtherEventIsEmpty(t *testing.T) {
	st := contractGRStore(t)
	const slug, sess = "graphremoval-event", "sess-graphremoval-event"
	id := contractGRJob(t, st, slug, sess)
	if _, err := st.AddFeedback(store.Feedback{JobID: id, JobSlug: slug, Note: "unread note"}); err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	contractGRDayGoal(t, st, "some goal")
	for _, ev := range []string{"PostToolUse", "Stop", "SessionStart"} {
		if got := contractGRFire(st, slug, sess, ev); got != "" {
			t.Errorf("event %q returned a block, want \"\":\n%s", ev, got)
		}
	}
}

// "A nil store or blank `session_id` returns `""`."
func TestGraphRemovalRecallNilStoreOrBlankSessionIsEmpty(t *testing.T) {
	st := contractGRStore(t)
	const slug, sess = "graphremoval-nil", "sess-graphremoval-nil"
	id := contractGRJob(t, st, slug, sess)
	if _, err := st.AddFeedback(store.Feedback{JobID: id, JobSlug: slug, Note: "unread note"}); err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}
	contractGRDayGoal(t, st, "some goal")
	if got := contractGRFire(nil, slug, sess, "UserPromptSubmit"); got != "" {
		t.Errorf("nil store returned a block:\n%s", got)
	}
	if got := contractGRFire(st, slug, "", "UserPromptSubmit"); got != "" {
		t.Errorf("blank session returned a block:\n%s", got)
	}
}

// "`jobs.RecallRequest` fields `SessionID, Event, Prompt, TranscriptPath, Slug`
// with JSON tags `session_id, event, prompt, transcript_path, slug` — All five
// keys still decode".
func TestGraphRemovalRecallRequestDecodesAllFiveKeys(t *testing.T) {
	raw := `{"session_id":"s1","event":"UserPromptSubmit","prompt":"p","transcript_path":"/t.jsonl","slug":"sl"}`
	var r RecallRequest
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatalf("decode: %v", err)
	}
	want := RecallRequest{SessionID: "s1", Event: "UserPromptSubmit", Prompt: "p", TranscriptPath: "/t.jsonl", Slug: "sl"}
	if r != want {
		t.Fatalf("decoded %+v, want %+v", r, want)
	}
}

// "`zeusLaunchBrief` returns exactly `zeusBrief(j) + goalContext(j)` ... for a
// job with no linked goals and no current-period goals the staged brief equals
// `zeusBrief(j)` byte for byte."
func TestGraphRemovalZeusLaunchBriefIsBriefPlusGoals(t *testing.T) {
	st := contractGRStore(t)
	contractGRJob(t, st, "graphremoval-brief", "")
	j := JobBySlug(LoadJobs(), "graphremoval-brief")
	if j == nil {
		t.Fatal("job not found after create")
	}
	if got, want := zeusLaunchBrief(j), zeusBrief(j); got != want {
		t.Fatalf("no-goal zeusLaunchBrief differs from zeusBrief (len %d vs %d)", len(got), len(want))
	}

	contractGRDayGoal(t, st, "a goal for the launch brief")
	j = JobBySlug(LoadJobs(), "graphremoval-brief")
	gc := goalContext(j)
	if gc == "" {
		t.Fatal("goalContext empty with a current-period goal — fixture did not seed one")
	}
	if got, want := zeusLaunchBrief(j), zeusBrief(j)+gc; got != want {
		t.Fatalf("zeusLaunchBrief != zeusBrief + goalContext (len %d vs %d)", len(got), len(want))
	}
}

// "`IsPipelineStage("graph") == false`; `PipelineStages()` returns exactly
// `[aim do dream info pr]`."
func TestGraphRemovalPipelineStagesDropGraph(t *testing.T) {
	if IsPipelineStage("graph") {
		t.Error(`IsPipelineStage("graph") = true, want false`)
	}
	want := []string{"aim", "do", "dream", "info", "pr"}
	if got := PipelineStages(); !reflect.DeepEqual(got, want) {
		t.Errorf("PipelineStages() = %v, want %v", got, want)
	}
	for _, s := range want {
		if !IsPipelineStage(s) {
			t.Errorf("IsPipelineStage(%q) = false, want true", s)
		}
	}
}
