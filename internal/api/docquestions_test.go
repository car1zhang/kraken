package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// docquestions_test.go — coverage for board.go's jobDocQuestions (the
// doc_questions/doc_questions_err population — mirrors jobArtifacts' live/
// dead split but keyed on the cell dir's own existence, not tmux liveness;
// see jobDocQuestions' doc comment) and actions.go's webAnswerDocQuestion
// (POST /api/doc-question). Follows artifacts_storemode_test.go's harness
// patterns: store rows seeded directly via the store package, file-mode
// fixtures via writeTestJob + direct file writes under
// jobs.QuestionsPath/jobs.PlutoDir.

const validQuestionsYAML = `questions:
  - id: Q1
    text: which approach?
    importance: P1
  - id: Q2
    text: already answered?
    importance: P0
    answer: yep
    answered-at: 2026-07-20T00:00:00Z
`

// answerFlowQuestionsYAML adds a THIRD, unanswered question (Q3) on top of
// validQuestionsYAML's Q1/Q2 — needed so the answer-flow test can exercise
// the empty-answer guard unambiguously. Q2 is answered from the moment this
// fixture is written, so targeting the empty-text case at Q2 would leave it
// ambiguous which guard fired (already-answered vs empty-answer both return
// non-nil, and — before this fix — the SAME message by accident of handler
// ordering); Q1 gets consumed by the test's own successful-answer step, so
// neither Q1 nor Q2 is safe to reuse for the empty-answer case either.
const answerFlowQuestionsYAML = `questions:
  - id: Q1
    text: which approach?
    importance: P1
  - id: Q2
    text: already answered?
    importance: P0
    answer: yep
    answered-at: 2026-07-20T00:00:00Z
  - id: Q3
    text: a third, still-open question
    importance: P2
`

// mustSeedCellQuestions creates j's cell dir (if needed) and writes data to
// its questions.yaml — the live-cell-dir fixture jobDocQuestions and
// webAnswerDocQuestion both key off. Callers touching a job's cell dir must
// sandbox HOME first (cellDir derives from util.Home()) — the mistake log's
// standing rule for anything under ~/.kraken.
func mustSeedCellQuestions(t *testing.T, j *jobs.Job, data string) {
	t.Helper()
	path := jobs.QuestionsPath(j)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// --- jobDocQuestions: live cell (cell dir exists) ---

func TestJobDocQuestionsLiveCellPopulated(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &jobs.Job{Slug: "docq-live"}
	mustSeedCellQuestions(t, j, validQuestionsYAML)

	qs, errMsg := jobDocQuestions(j)
	if errMsg != "" {
		t.Fatalf("unexpected err: %s", errMsg)
	}
	if len(qs) != 2 {
		t.Fatalf("want 2 questions, got %+v", qs)
	}
	if qs[0].ID != "Q1" || qs[0].Importance != "P1" || qs[0].Answer != "" {
		t.Errorf("Q1 = %+v", qs[0])
	}
	if qs[1].ID != "Q2" || qs[1].Answer != "yep" || qs[1].AnsweredAt != "2026-07-20T00:00:00Z" {
		t.Errorf("Q2 = %+v", qs[1])
	}
}

func TestJobDocQuestionsLiveCellMalformed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &jobs.Job{Slug: "docq-malformed"}
	mustSeedCellQuestions(t, j, "questions:\n  - id: Q1\n    text: x\n    importance: BOGUS\n")

	qs, errMsg := jobDocQuestions(j)
	if qs != nil {
		t.Errorf("want nil questions on parse failure, got %+v", qs)
	}
	if errMsg == "" {
		t.Fatal("want a non-empty doc_questions_err")
	}
}

// --- jobDocQuestions: dead job, store mode ---

func TestJobDocQuestionsStoreModeDeadJob(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()

	populated := mustCreateStoreJob(t, st, store.Job{Slug: "docq-store-populated", Repo: "monolith", State: "blocked"})
	if err := st.UpsertQuestions(populated.ID, []byte(validQuestionsYAML), 1); err != nil {
		t.Fatal(err)
	}
	qs, errMsg := jobDocQuestions(populated)
	if errMsg != "" || len(qs) != 2 {
		t.Fatalf("populated row: qs=%+v errMsg=%q, want 2 questions, no err", qs, errMsg)
	}

	empty := mustCreateStoreJob(t, st, store.Job{Slug: "docq-store-empty", Repo: "monolith", State: "blocked"})
	if err := st.UpsertQuestions(empty.ID, []byte{}, 1); err != nil {
		t.Fatal(err)
	}
	qs, errMsg = jobDocQuestions(empty)
	if qs != nil || errMsg != "" {
		t.Fatalf("empty-bytes row: qs=%+v errMsg=%q, want neither field set", qs, errMsg)
	}

	none := mustCreateStoreJob(t, st, store.Job{Slug: "docq-store-none", Repo: "monolith", State: "blocked"})
	qs, errMsg = jobDocQuestions(none)
	if qs != nil || errMsg != "" {
		t.Fatalf("no row: qs=%+v errMsg=%q, want neither field set", qs, errMsg)
	}
}

// TestJobDocQuestionsStoreModeMalformed pins C-E1's "keep the early return
// only on a successful non-empty parse" boundary: malformed store bytes are
// NOT "nothing usable" — the row exists and has content, it just fails
// strict parse — so this must return DocQuestionsErr directly rather than
// falling through to the (here, absent) pluto copy.
func TestJobDocQuestionsStoreModeMalformed(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-store-malformed", Repo: "monolith", State: "blocked"})
	if err := st.UpsertQuestions(j.ID, []byte("questions:\n  - id: Q1\n    text: x\n    importance: BOGUS\n"), 1); err != nil {
		t.Fatal(err)
	}

	qs, errMsg := jobDocQuestions(j)
	if qs != nil {
		t.Errorf("want nil questions on parse failure, got %+v", qs)
	}
	if !strings.Contains(errMsg, "malformed") {
		t.Fatalf("want a malformed doc_questions_err, got %q", errMsg)
	}
}

// TestJobDocQuestionsStoreModeFallsBackToPluto is the C-E1 regression: it
// mirrors the reviewer's 5-step reactivation scenario at the jobDocQuestions
// level directly (a full reactivate → second-teardown → sync-delete
// sequence isn't reproducible from this package, so the end STATE is seeded
// instead) — store row absent (ErrNotFound, same as after a sync tick
// deletes a stale row) + pluto copy present (preserveArtifacts' write from
// the FIRST teardown, never touched since) → the questions must still be
// served, not silently dropped.
func TestJobDocQuestionsStoreModeFallsBackToPluto(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-reactivate", Repo: "monolith", State: "done"})
	// deliberately no UpsertQuestions call: GetQuestions returns ErrNotFound,
	// same as the reviewer's step 5 ("second teardown -> store has no row").
	if err := os.MkdirAll(jobs.PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), util.Slugify(j.Slug)+".questions.yaml"), []byte(validQuestionsYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	qs, errMsg := jobDocQuestions(j)
	if errMsg != "" || len(qs) != 2 {
		t.Fatalf("want 2 questions from the pluto fallback, got qs=%+v errMsg=%q", qs, errMsg)
	}
}

// TestJobDocQuestionsLiveCellEmpty covers an empty/comment-only live
// questions.yaml: a valid "no questions yet" state, neither wire field set.
func TestJobDocQuestionsLiveCellEmpty(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &jobs.Job{Slug: "docq-empty"}
	mustSeedCellQuestions(t, j, "# just a comment\n\n")

	qs, errMsg := jobDocQuestions(j)
	if qs != nil || errMsg != "" {
		t.Fatalf("want neither field set, got qs=%+v errMsg=%q", qs, errMsg)
	}
}

// TestJobDocQuestionsCellDirNoFile covers the cell-dir-exists-but-file-
// missing case: still a live cell dir (so this never falls to store/pluto),
// but no questions.yaml written into it yet — a valid "no questions yet"
// state, same as a missing file anywhere else.
func TestJobDocQuestionsCellDirNoFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &jobs.Job{Slug: "docq-nofile"}
	if err := os.MkdirAll(filepath.Dir(jobs.QuestionsPath(j)), 0o755); err != nil {
		t.Fatal(err)
	}

	qs, errMsg := jobDocQuestions(j)
	if qs != nil || errMsg != "" {
		t.Fatalf("want neither field set, got qs=%+v errMsg=%q", qs, errMsg)
	}
}

// --- jobDocQuestions: dead job, file mode fallback (pluto flat copy) ---

func TestJobDocQuestionsFileModeDeadJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	j := &jobs.Job{Slug: "docq-file-dead"}
	if err := os.MkdirAll(jobs.PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "docq-file-dead.questions.yaml"), []byte(validQuestionsYAML), 0o644); err != nil {
		t.Fatal(err)
	}

	qs, errMsg := jobDocQuestions(j)
	if errMsg != "" || len(qs) != 2 {
		t.Fatalf("qs=%+v errMsg=%q, want 2 questions, no err", qs, errMsg)
	}
}

// --- pluto artifact listing must not surface the questions copy, and only
// the EXACT questions.yaml name (C-E4) ---

func TestPlutoFlatArtifactsExcludesQuestions(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	j := &jobs.Job{Slug: "docq-artifacts"}
	if err := os.MkdirAll(jobs.PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "docq-artifacts.explain.md"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "docq-artifacts.questions.yaml"), []byte(validQuestionsYAML), 0o644); err != nil {
		t.Fatal(err)
	}
	// a genuine artifact that merely ENDS in ".questions.yaml" must survive
	// a suffix-match exclusion would wrongly swallow it too.
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "docq-artifacts.notes.questions.yaml"), []byte("real deliverable"), 0o644); err != nil {
		t.Fatal(err)
	}

	arts := plutoFlatArtifacts(j)
	names := map[string]bool{}
	for _, a := range arts {
		names[a.Name] = true
	}
	if len(arts) != 2 || !names["explain.md"] || !names["notes.questions.yaml"] {
		t.Fatalf("want exactly [explain.md notes.questions.yaml] (only the exact questions.yaml excluded), got %+v", arts)
	}
}

// --- webAnswerDocQuestion: the answer flow ---

func TestWebAnswerDocQuestionFileMode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "docq-answer", "state: blocked\n")
	j := jobs.JobBySlug(jobs.LoadJobs(), "docq-answer")
	mustSeedCellQuestions(t, j, answerFlowQuestionsYAML)

	if err := webAnswerDocQuestion(webReq{Slug: "docq-answer", ID: "Q1", Text: "go with A"}); err != nil {
		t.Fatalf("answer Q1: %v", err)
	}
	data, err := os.ReadFile(jobs.QuestionsPath(j))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "answer: go with A") {
		t.Fatalf("file missing answer: %s", data)
	}

	// second answer to the same question: already answered
	if err := webAnswerDocQuestion(webReq{Slug: "docq-answer", ID: "Q1", Text: "again"}); err == nil || !strings.Contains(err.Error(), `question Q1 already answered`) {
		t.Fatalf("want already-answered error, got %v", err)
	}

	// unknown id
	if err := webAnswerDocQuestion(webReq{Slug: "docq-answer", ID: "Q99", Text: "x"}); err == nil || !strings.Contains(err.Error(), `no such question "Q99"`) {
		t.Fatalf("want no-such-question error, got %v", err)
	}

	// empty text (whitespace-only), targeted at Q3 — still unanswered, unlike
	// Q1 (just consumed above) and Q2 (answered from the fixture itself) —
	// so this unambiguously exercises the emptiness guard rather than
	// possibly tripping the already-answered guard instead.
	if err := webAnswerDocQuestion(webReq{Slug: "docq-answer", ID: "Q3", Text: "   "}); err == nil || !strings.Contains(err.Error(), "answer must not be empty") {
		t.Fatalf("want empty-answer error, got %v", err)
	}
}

// TestWebAnswerDocQuestionAnswerTooLong pins C-E3's 4000-char cap (after
// trim, counted in runes) at its exact boundary: 4000 must still succeed,
// 4001 must be refused before ever reaching jobs.AnswerDocQuestion.
func TestWebAnswerDocQuestionAnswerTooLong(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "docq-toolong", "state: blocked\n")
	j := jobs.JobBySlug(jobs.LoadJobs(), "docq-toolong")
	mustSeedCellQuestions(t, j, answerFlowQuestionsYAML)

	ok := strings.Repeat("a", 4000)
	if err := webAnswerDocQuestion(webReq{Slug: "docq-toolong", ID: "Q1", Text: ok}); err != nil {
		t.Fatalf("4000 chars should be accepted, got %v", err)
	}

	tooLong := strings.Repeat("a", 4001)
	if err := webAnswerDocQuestion(webReq{Slug: "docq-toolong", ID: "Q3", Text: tooLong}); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("want a too-long error for 4001 chars, got %v", err)
	}
}

// TestWebAnswerDocQuestionCellDirNoFile covers C-E2's write-side path-free
// requirement: the cell dir exists (so this isn't the "cell is gone"
// refusal) but questions.yaml itself was never written — jobs.
// AnswerDocQuestion's own "no questions file at <path>" must not leak the
// absolute path onto the wire.
func TestWebAnswerDocQuestionCellDirNoFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "docq-write-nofile", "state: blocked\n")
	j := jobs.JobBySlug(jobs.LoadJobs(), "docq-write-nofile")
	if err := os.MkdirAll(filepath.Dir(jobs.QuestionsPath(j)), 0o755); err != nil {
		t.Fatal(err)
	}
	// deliberately no mustSeedCellQuestions call: the cell dir exists but
	// questions.yaml inside it does not.

	err := webAnswerDocQuestion(webReq{Slug: "docq-write-nofile", ID: "Q1", Text: "x"})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), string(filepath.Separator)) {
		t.Fatalf("error must be path-free, got %q", err.Error())
	}
	if !strings.Contains(err.Error(), "no questions file for this job") {
		t.Fatalf("want a no-questions-file error, got %v", err)
	}
}

func TestWebAnswerDocQuestionStoreModeResyncs(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-answer-store", Repo: "monolith", State: "blocked"})
	mustSeedCellQuestions(t, j, validQuestionsYAML)

	if err := webAnswerDocQuestion(webReq{Slug: "docq-answer-store", ID: "Q1", Text: "go with A"}); err != nil {
		t.Fatalf("answer Q1: %v", err)
	}
	q, err := st.GetQuestions(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(q.Bytes), "answer: go with A") {
		t.Fatalf("store row not re-synced after answer: %s", q.Bytes)
	}
}

func TestWebAnswerDocQuestionRejectsDoneJob(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "docq-done", "state: done\n")
	j := jobs.JobBySlug(jobs.LoadJobs(), "docq-done")
	mustSeedCellQuestions(t, j, validQuestionsYAML)

	if err := webAnswerDocQuestion(webReq{Slug: "docq-done", ID: "Q1", Text: "x"}); err == nil || !strings.Contains(err.Error(), "no questions to answer") {
		t.Fatalf("want a no-questions-to-answer error for a done job, got %v", err)
	}
}

func TestWebAnswerDocQuestionRejectsGoneCellDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "docq-gone", "state: blocked\n")
	// deliberately no mustSeedCellQuestions call: the cell dir never existed
	// (or was already torn down) — answers must be refused read-only.

	err := webAnswerDocQuestion(webReq{Slug: "docq-gone", ID: "Q1", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("want a read-only error, got %v", err)
	}
}

func TestWebAnswerDocQuestionMissingID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "docq-noid", "state: blocked\n")
	j := jobs.JobBySlug(jobs.LoadJobs(), "docq-noid")
	mustSeedCellQuestions(t, j, validQuestionsYAML)

	if err := webAnswerDocQuestion(webReq{Slug: "docq-noid", Text: "x"}); err == nil || !strings.Contains(err.Error(), "missing question id") {
		t.Fatalf("want a missing-question-id error, got %v", err)
	}
}
