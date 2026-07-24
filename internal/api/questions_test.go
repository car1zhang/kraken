package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// questions_test.go — coverage for questions.go's POST /api/questions
// dispatch (bulk-set, add/update/remove) and GET /api/questions, following
// docquestions_test.go's store-backed harness pattern. B7: this file (plus
// feedback_test.go) is what contract_test.go's "op dispatch and validation
// are covered by feedback_test.go/questions_test.go instead" comment
// refers to.

func postQuestions(t *testing.T, req questionsReq) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(t, req)
	httpReq := httptest.NewRequest(http.MethodPost, "/api/questions", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	questionsHandler(rec, httpReq)
	return rec
}

// --- bulk set ---

func TestQuestionsBulkSetEmptyArrayRefused(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "q-bulk-empty", Repo: "monolith"})

	rec := postQuestions(t, questionsReq{Slug: "q-bulk-empty", Questions: []questionWire{}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec); !strings.Contains(got, "non-empty") {
		t.Fatalf("error = %q", got)
	}
}

func TestQuestionsBulkSetUpsertsAndDeletesAbsent(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-bulk-set", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q9", Text: "stale", Importance: "P2", Position: 1}); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-bulk-set", Questions: []questionWire{
		{ID: "Q1", Text: "first", Importance: "P0", Position: 1},
		{ID: "Q2", Text: "second", Importance: "P1", Position: 2},
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 2 {
		t.Fatalf("want 2 questions (Q9 dropped, absent from the set), got %+v", qs)
	}
	if qs[0].QID != "Q1" || qs[1].QID != "Q2" {
		t.Fatalf("questions = %+v", qs)
	}
}

// TestQuestionsBulkSetOrdersByArrayIndexNotPosition pins B8: an 11-question
// bulk-set, posted the way zeus actually sends it (every entry's Position
// left at its zero value, since webDocQuestion never returns position on
// GET and nothing tells zeus to send it), must read back in POSTED array
// order — Q1..Q11 — not TEXT order (Q1 Q10 Q11 Q2 Q3 ... Q9), which is what
// ListQuestions' `ORDER BY position, qid` degrades to the moment every row
// shares position 0.
func TestQuestionsBulkSetOrdersByArrayIndexNotPosition(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-bulk-order-11", Repo: "monolith"})

	want := []string{"Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7", "Q8", "Q9", "Q10", "Q11"}
	qw := make([]questionWire, len(want))
	for i, id := range want {
		qw[i] = questionWire{ID: id, Text: "text for " + id, Importance: "P1"} // Position left zero, as zeus actually sends
	}

	rec := postQuestions(t, questionsReq{Slug: "q-bulk-order-11", Questions: qw})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != len(want) {
		t.Fatalf("got %d questions, want %d: %+v", len(qs), len(want), qs)
	}
	for i, id := range want {
		if qs[i].QID != id {
			t.Fatalf("order[%d] = %q, want %q — full order: %v", i, qs[i].QID, id, qidsOf(qs))
		}
		if qs[i].Position != int64(i+1) {
			t.Fatalf("Q%d position = %d, want %d (1-based array index)", i+1, qs[i].Position, i+1)
		}
	}
}

// TestQuestionsBulkSetIgnoresSuppliedPosition covers the "instead of
// copying q.Position" half of B8: even when the caller DOES supply
// (wrong/stale) position values, a bulk-set must still stamp from array
// index, never trust the field.
func TestQuestionsBulkSetIgnoresSuppliedPosition(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-bulk-ignores-position", Repo: "monolith"})

	rec := postQuestions(t, questionsReq{Slug: "q-bulk-ignores-position", Questions: []questionWire{
		{ID: "Q1", Text: "first", Importance: "P0", Position: 999},
		{ID: "Q2", Text: "second", Importance: "P1", Position: 1},
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 2 {
		t.Fatalf("qs = %+v, err %v", qs, err)
	}
	if qs[0].QID != "Q1" || qs[0].Position != 1 || qs[1].QID != "Q2" || qs[1].Position != 2 {
		t.Fatalf("bulk-set trusted the caller-supplied position instead of array index: %+v", qs)
	}
}

// TestQuestionsBulkSetThenUpdatePreservesPositionThenResetOnNextBulkSet is
// the B4/B8 interaction the team lead flagged: a bulk `set` numbers every
// row by array order (B8); a subsequent per-question `update` with no
// position must leave that row's bulk-set-assigned position alone (B4); and
// a LATER bulk `set` still re-numbers everything by its own new array
// order, since B8's array-index rule applies to every bulk-set call, not
// just the first one ever made for a job.
func TestQuestionsBulkSetThenUpdatePreservesPositionThenResetOnNextBulkSet(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-bulk-update-bulk", Repo: "monolith"})

	// initial bulk-set: Q1, Q2, Q3 -> positions 1, 2, 3.
	rec := postQuestions(t, questionsReq{Slug: "q-bulk-update-bulk", Questions: []questionWire{
		{ID: "Q1", Text: "one", Importance: "P0"},
		{ID: "Q2", Text: "two", Importance: "P1"},
		{ID: "Q3", Text: "three", Importance: "P2"},
	}})
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk-set 1: status %d, body %s", rec.Code, rec.Body.String())
	}

	// per-question update on Q2, no position supplied — B4 says its
	// bulk-set-assigned position (2) must survive untouched.
	rec2 := postQuestions(t, questionsReq{Slug: "q-bulk-update-bulk", Op: "update", Question: &questionWire{ID: "Q2", Text: "two, edited", Importance: "P1"}})
	if rec2.Code != http.StatusOK {
		t.Fatalf("update: status %d, body %s", rec2.Code, rec2.Body.String())
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var q2 *store.DocQuestion
	for i := range qs {
		if qs[i].QID == "Q2" {
			q2 = &qs[i]
		}
	}
	if q2 == nil || q2.Position != 2 || q2.Text != "two, edited" {
		t.Fatalf("Q2 after update = %+v, want position 2 preserved", q2)
	}

	// a SECOND bulk-set, posted in a new order, must re-number by ITS OWN
	// array order — B8 applies every time, not just to the first ever set.
	rec3 := postQuestions(t, questionsReq{Slug: "q-bulk-update-bulk", Questions: []questionWire{
		{ID: "Q3", Text: "three", Importance: "P2"},
		{ID: "Q1", Text: "one", Importance: "P0"},
		{ID: "Q2", Text: "two, edited", Importance: "P1"},
	}})
	if rec3.Code != http.StatusOK {
		t.Fatalf("bulk-set 2: status %d, body %s", rec3.Code, rec3.Body.String())
	}
	qs2, err := st.ListQuestions(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs2) != 3 || qs2[0].QID != "Q3" || qs2[0].Position != 1 ||
		qs2[1].QID != "Q1" || qs2[1].Position != 2 ||
		qs2[2].QID != "Q2" || qs2[2].Position != 3 {
		t.Fatalf("order after 2nd bulk-set = %+v, want Q3,Q1,Q2 at positions 1,2,3", qs2)
	}
}

func qidsOf(qs []store.DocQuestion) []string {
	out := make([]string, len(qs))
	for i, q := range qs {
		out[i] = q.QID
	}
	return out
}

func TestQuestionsBulkSetBadImportance400(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "q-bulk-bad-importance", Repo: "monolith"})

	rec := postQuestions(t, questionsReq{Slug: "q-bulk-bad-importance", Questions: []questionWire{
		{ID: "Q1", Text: "x", Importance: "P9"},
	}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// --- add ---

// TestQuestionsAddAppendsPosition covers "add with no (or zero) position
// appends after the current last question".
func TestQuestionsAddAppendsPosition(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-add-append", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "first", Importance: "P0", Position: 5}); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-add-append", Op: "add", Question: &questionWire{ID: "Q2", Text: "second", Importance: "P1"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var q2 *store.DocQuestion
	for i := range qs {
		if qs[i].QID == "Q2" {
			q2 = &qs[i]
		}
	}
	if q2 == nil || q2.Position != 6 {
		t.Fatalf("Q2 position = %+v, want 6 (appended after position 5)", q2)
	}
}

// TestQuestionsAddExplicitPositionHonored covers add with a caller-supplied
// (non-zero) position, which must NOT be overridden by the append logic.
func TestQuestionsAddExplicitPositionHonored(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-add-explicit", Repo: "monolith"})

	rec := postQuestions(t, questionsReq{Slug: "q-add-explicit", Op: "add", Question: &questionWire{ID: "Q1", Text: "x", Importance: "P0", Position: 42}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 1 || qs[0].Position != 42 {
		t.Fatalf("qs = %+v, err %v", qs, err)
	}
}

// TestQuestionsAddExistingQIDConflict covers non-blocking#5: "add" on an
// already-existing qid is a 409 (use "update" instead), not a silent
// upsert-overwrite.
func TestQuestionsAddExistingQIDConflict(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-add-conflict", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "original", Importance: "P0", Position: 1}); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-add-conflict", Op: "add", Question: &questionWire{ID: "Q1", Text: "overwritten?", Importance: "P1"}})
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409, body %s", rec.Code, rec.Body.String())
	}

	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 1 || qs[0].Text != "original" {
		t.Fatalf("existing question mutated by a refused add: %+v (err %v)", qs, err)
	}
}

// --- update: B4 ---

// TestQuestionsUpdatePreservesPosition pins B4: a text-only update (no
// position sent, so it decodes to zero) must leave the question's existing
// position untouched, not reset it to 0.
func TestQuestionsUpdatePreservesPosition(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-update-position", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "first", Importance: "P0", Position: 3}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q2", Text: "second", Importance: "P1", Position: 4}); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-update-position", Op: "update", Question: &questionWire{ID: "Q1", Text: "first, edited", Importance: "P0"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	var q1 *store.DocQuestion
	for i := range qs {
		if qs[i].QID == "Q1" {
			q1 = &qs[i]
		}
	}
	if q1 == nil || q1.Position != 3 {
		t.Fatalf("Q1 position = %+v, want 3 (preserved, not reset to 0)", q1)
	}
	if q1.Text != "first, edited" {
		t.Fatalf("Q1 text not updated: %+v", q1)
	}
	// order must still read Q1 (position 3) before Q2 (position 4).
	if qs[0].QID != "Q1" || qs[1].QID != "Q2" {
		t.Fatalf("order after update = %+v, want Q1 before Q2", qs)
	}
}

// TestQuestionsUpdateExplicitPositionHonored covers an update that DOES
// supply a non-zero position — that must still move the question, since
// B4 only protects the "position omitted" case.
func TestQuestionsUpdateExplicitPositionHonored(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-update-explicit-position", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "first", Importance: "P0", Position: 1}); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-update-explicit-position", Op: "update", Question: &questionWire{ID: "Q1", Text: "moved", Importance: "P0", Position: 99}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 1 || qs[0].Position != 99 {
		t.Fatalf("qs = %+v, err %v", qs, err)
	}
}

// TestQuestionsUpdatePreservesAnswer covers the store-layer "preserve"
// contract from the API side: updating zeus-owned columns must never touch
// a surviving row's kraken-owned answer/answered_at.
func TestQuestionsUpdatePreservesAnswer(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-update-preserves-answer", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "first", Importance: "P0", Position: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "yes", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-update-preserves-answer", Op: "update", Question: &questionWire{ID: "Q1", Text: "edited", Importance: "P0"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 1 || qs[0].Answer != "yes" || qs[0].AnsweredAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("answer not preserved across update: %+v (err %v)", qs, err)
	}
}

func TestQuestionsUpdateMissingQuestion400(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "q-update-missing", Repo: "monolith"})

	rec := postQuestions(t, questionsReq{Slug: "q-update-missing", Op: "update"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// TestQuestionsUpdateUnknownQIDConflict covers non-blocking#2: "update" on a
// qid that doesn't exist yet used to silently succeed as an UpsertQuestion
// INSERT with Position left at 0 (found is false, so B4's preserve-position
// branch never ran) — jumping a phantom question to the top of the list.
// It must now 404, symmetric with "add"'s own 409 on the opposite mismatch
// (non-blocking#5), and must not have created anything.
func TestQuestionsUpdateUnknownQIDConflict(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-update-unknown", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "first", Importance: "P0", Position: 1}); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-update-unknown", Op: "update", Question: &questionWire{ID: "Q9", Text: "phantom", Importance: "P0"}})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}

	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 1 || qs[0].QID != "Q1" {
		t.Fatalf("update on an unknown qid must not create a row: %+v (err %v)", qs, err)
	}
}

// --- remove ---

func TestQuestionsRemove(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-remove", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "x", Importance: "P0", Position: 1}); err != nil {
		t.Fatal(err)
	}

	rec := postQuestions(t, questionsReq{Slug: "q-remove", Op: "remove", QID: "Q1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	qs, err := st.ListQuestions(j.ID)
	if err != nil || len(qs) != 0 {
		t.Fatalf("qs = %+v, err %v", qs, err)
	}
}

func TestQuestionsRemoveMissingQID400(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "q-remove-missing-qid", Repo: "monolith"})

	rec := postQuestions(t, questionsReq{Slug: "q-remove-missing-qid", Op: "remove"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// --- request-shape errors ---

func TestQuestionsPostUnknownJob404(t *testing.T) {
	useTestStore(t)
	rec := postQuestions(t, questionsReq{Slug: "no-such-job", Op: "remove", QID: "Q1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestQuestionsPostUnrecognized400(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "q-unrecognized", Repo: "monolith"})

	rec := postQuestions(t, questionsReq{Slug: "q-unrecognized"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestQuestionsPostBadJSON(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/questions", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	questionsHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// TestQuestionsPostOversizeBody413 covers non-blocking#7: an oversize body
// is a 413, distinct from an ordinary malformed-JSON 400.
func TestQuestionsPostOversizeBody413(t *testing.T) {
	useTestStore(t)
	huge := `{"slug":"x","op":"add","question":{"id":"Q1","text":"` + strings.Repeat("a", 2<<20) + `","importance":"P0"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/questions", strings.NewReader(huge))
	rec := httptest.NewRecorder()
	questionsHandler(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413, body %s", rec.Code, rec.Body.String())
	}
}

// --- GET /api/questions ---

// TestQuestionsGetEmptyArray pins B3: a job with no question rows yet must
// serve {"questions":[]}, never {"questions":null}.
func TestQuestionsGetEmptyArray(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "q-get-empty", Repo: "monolith"})

	req := httptest.NewRequest(http.MethodGet, "/api/questions?slug=q-get-empty", nil)
	rec := httptest.NewRecorder()
	questionsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"questions":[]}` {
		t.Fatalf("body = %q, want an empty array, never null", got)
	}
}

func TestQuestionsGetUnknownJob404(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/questions?slug=no-such-job", nil)
	rec := httptest.NewRecorder()
	questionsHandler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestQuestionsGetMissingSlug(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodGet, "/api/questions", nil)
	rec := httptest.NewRecorder()
	questionsHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestQuestionsGetPopulated(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "q-get-populated", Repo: "monolith"})
	if err := st.UpsertQuestion(j.ID, store.DocQuestion{QID: "Q1", Text: "x", Importance: "P0", Position: 1}); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/questions?slug=q-get-populated", nil)
	rec := httptest.NewRecorder()
	questionsHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Questions []webDocQuestion `json:"questions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Questions) != 1 || resp.Questions[0].ID != "Q1" {
		t.Fatalf("questions = %+v", resp.Questions)
	}
}
