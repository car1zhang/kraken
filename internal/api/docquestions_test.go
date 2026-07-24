package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// docquestions_test.go — coverage for board.go's jobQuestions (the
// doc_questions board field, now a straight read of `questions` rows in
// kraken.db — no file, no cell-dir liveness split, no malformed-parse case)
// and actions.go's webAnswerDocQuestion (POST /api/doc-question, now a
// plain row UPDATE). Follows artifacts_storemode_test.go's harness pattern:
// store rows seeded directly via the store package.

// --- jobQuestions ---

func TestJobQuestionsPopulated(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-populated", Repo: "monolith", State: "blocked"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "which approach?", Importance: "P1", Position: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q2", Text: "already answered?", Importance: "P0", Position: 2}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerQuestion(j.ID, "Q2", "yep", "2026-07-20T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	qs := jobQuestions(st, j)
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

func TestJobQuestionsEmpty(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-empty", Repo: "monolith", State: "blocked"})

	if qs := jobQuestions(st, j); qs != nil {
		t.Fatalf("want nil for a job with no question rows, got %+v", qs)
	}
}

func TestJobQuestionsNoStore(t *testing.T) {
	// no useTestStore: ActiveStore() is nil, mirroring a file-mode job (no
	// question rows can exist without a store to hold them).
	j := &jobs.Job{Slug: "docq-nostore"}
	if qs := jobQuestions(nil, j); qs != nil {
		t.Fatalf("want nil with no store wired, got %+v", qs)
	}
}

func TestJobQuestionsZeroID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	// a Job with ID 0 (never persisted to the store) must not be queried —
	// there is no row to find, and job_id 0 must never be treated as a real
	// job's rows.
	j := &jobs.Job{Slug: "docq-zeroid"}
	if qs := jobQuestions(st, j); qs != nil {
		t.Fatalf("want nil for a zero-ID job, got %+v", qs)
	}
}

// --- webAnswerDocQuestion: the answer flow (store-only now) ---

func TestWebAnswerDocQuestionAnswersAndReAnswers(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-answer", Repo: "monolith", State: "blocked"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "which approach?", Importance: "P1"}); err != nil {
		t.Fatal(err)
	}

	if err := webAnswerDocQuestion(webReq{Slug: "docq-answer", ID: "Q1", Text: "go with A"}); err != nil {
		t.Fatalf("answer Q1: %v", err)
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 1 || qs[0].Answer != "go with A" {
		t.Fatalf("row not updated: qs=%+v err=%v", qs, err)
	}

	// Q13: a re-answer is allowed — same UPDATE, no "already answered" refusal.
	if err := webAnswerDocQuestion(webReq{Slug: "docq-answer", ID: "Q1", Text: "actually go with B"}); err != nil {
		t.Fatalf("re-answer Q1: %v", err)
	}
	qs, err = st.ListQuestions(j.ID)
	if err != nil || len(qs) != 1 || qs[0].Answer != "actually go with B" {
		t.Fatalf("re-answer not applied: qs=%+v err=%v", qs, err)
	}
}

func TestWebAnswerDocQuestionUnknownID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "docq-unknown", Repo: "monolith", State: "blocked"})

	err := webAnswerDocQuestion(webReq{Slug: "docq-unknown", ID: "Q99", Text: "x"})
	if err == nil || !strings.Contains(err.Error(), `no such question "Q99"`) {
		t.Fatalf("want a no-such-question error, got %v", err)
	}
}

func TestWebAnswerDocQuestionAllowedRegardlessOfCellLiveness(t *testing.T) {
	// Q13: rows are answerable live or done — there is no more "cell is
	// gone — read-only" refusal. A job that never started a cell at all
	// (draft) still answers cleanly as long as the question row exists.
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-done", Repo: "monolith", State: "done"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "which approach?", Importance: "P1"}); err != nil {
		t.Fatal(err)
	}

	if err := webAnswerDocQuestion(webReq{Slug: "docq-done", ID: "Q1", Text: "go with A"}); err != nil {
		t.Fatalf("answering a done job's question should succeed, got %v", err)
	}
}

func TestWebAnswerDocQuestionMissingID(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "docq-noid", Repo: "monolith", State: "blocked"})

	if err := webAnswerDocQuestion(webReq{Slug: "docq-noid", Text: "x"}); err == nil || !strings.Contains(err.Error(), "missing question id") {
		t.Fatalf("want a missing-question-id error, got %v", err)
	}
}

func TestWebAnswerDocQuestionEmptyAnswer(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-empty-answer", Repo: "monolith", State: "blocked"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "which approach?", Importance: "P1"}); err != nil {
		t.Fatal(err)
	}

	if err := webAnswerDocQuestion(webReq{Slug: "docq-empty-answer", ID: "Q1", Text: "   "}); err == nil || !strings.Contains(err.Error(), "answer must not be empty") {
		t.Fatalf("want empty-answer error, got %v", err)
	}
}

// TestWebAnswerDocQuestionAnswerTooLong pins the 4000-char cap (after trim,
// counted in runes) at its exact boundary: 4000 must still succeed, 4001
// must be refused before ever reaching the store.
func TestWebAnswerDocQuestionAnswerTooLong(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "docq-toolong", Repo: "monolith", State: "blocked"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "q1", Importance: "P1"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q2", Text: "q2", Importance: "P1"}); err != nil {
		t.Fatal(err)
	}

	ok := strings.Repeat("a", 4000)
	if err := webAnswerDocQuestion(webReq{Slug: "docq-toolong", ID: "Q1", Text: ok}); err != nil {
		t.Fatalf("4000 chars should be accepted, got %v", err)
	}

	tooLong := strings.Repeat("a", 4001)
	if err := webAnswerDocQuestion(webReq{Slug: "docq-toolong", ID: "Q2", Text: tooLong}); err == nil || !strings.Contains(err.Error(), "too long") {
		t.Fatalf("want a too-long error for 4001 chars, got %v", err)
	}
}

func TestWebAnswerDocQuestionUnknownJob(t *testing.T) {
	useTestStore(t)
	if err := webAnswerDocQuestion(webReq{Slug: "docq-no-such-job", ID: "Q1", Text: "x"}); err == nil {
		t.Fatal("want an error for an unknown job")
	}
}

// --- pluto flat-copy artifact listing: the questions.yaml exclusion is
// gone, mirroring jobs.AllArtifactPaths' own removal of it (questions live
// in kraken.db now, so a leftover *.questions.yaml file on disk is just
// another ordinary preserved artifact, not a reserved name). ---

func TestPlutoFlatArtifactsNoLongerExcludesQuestionsCopy(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	j := &jobs.Job{Slug: "docq-artifacts"}
	if err := os.MkdirAll(jobs.PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "docq-artifacts.explain.md"), []byte("doc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "docq-artifacts.questions.yaml"), []byte("stale legacy copy"), 0o644); err != nil {
		t.Fatal(err)
	}

	arts := plutoFlatArtifacts(j)
	names := map[string]bool{}
	for _, a := range arts {
		names[a.Name] = true
	}
	if len(arts) != 2 || !names["explain.md"] || !names["questions.yaml"] {
		t.Fatalf("want both explain.md and questions.yaml listed (no more reserved-name exclusion), got %+v", arts)
	}
}
