package store

import (
	"errors"
	"testing"
)

func TestQuestionsListOrderedByPosition(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "q-job"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SetQuestions(id, []DocQuestion{
		{QID: "Q2", Text: "second", Importance: "P1", Position: 1},
		{QID: "Q1", Text: "first", Importance: "P0", Position: 0},
	}); err != nil {
		t.Fatalf("SetQuestions: %v", err)
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 2 || qs[0].QID != "Q1" || qs[1].QID != "Q2" {
		t.Fatalf("order wrong: %+v", qs)
	}
}

// TestSetQuestionsBulkSemantics covers SetQuestions' full contract: upsert
// by qid, delete anything not present in the new set, and PRESERVE
// answer/answered_at on every surviving row — the kraken-owned columns a
// zeus bulk-set must never touch.
func TestSetQuestionsBulkSemantics(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "q-preserve"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SetQuestions(id, []DocQuestion{
		{QID: "Q1", Text: "t1", Importance: "P0", Position: 0},
		{QID: "Q2", Text: "t2", Importance: "P1", Position: 1},
	}); err != nil {
		t.Fatalf("SetQuestions initial: %v", err)
	}
	if err := s.AnswerQuestion(id, "Q1", "yes", "2026-01-01T00:00:00Z"); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	// Bulk-set again: Q1 survives with updated zeus-owned fields but must
	// keep its answer; Q2 is absent from the new set and must be deleted.
	if err := s.SetQuestions(id, []DocQuestion{
		{QID: "Q1", Text: "t1-updated", Context: "c", Importance: "P2", Recommendation: "r", Position: 0},
	}); err != nil {
		t.Fatalf("SetQuestions bulk update: %v", err)
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 1 {
		t.Fatalf("Q2 should have been deleted by the bulk-set: %+v", qs)
	}
	q := qs[0]
	if q.Text != "t1-updated" || q.Context != "c" || q.Importance != "P2" || q.Recommendation != "r" {
		t.Fatalf("zeus-owned fields not updated: %+v", q)
	}
	if q.Answer != "yes" || q.AnsweredAt != "2026-01-01T00:00:00Z" {
		t.Fatalf("kraken-owned answer/answered_at not preserved: %+v", q)
	}
}

// TestSetQuestionsRejectsInvalidWithoutPartialApply pins that a bad item
// anywhere in the batch fails the WHOLE call — validation runs before any
// write, so a partially-invalid bulk-set never partially applies.
func TestSetQuestionsRejectsInvalidWithoutPartialApply(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "q-invalid-batch"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	err = s.SetQuestions(id, []DocQuestion{
		{QID: "Q1", Text: "ok", Importance: "P0", Position: 0},
		{QID: "not-a-qid", Text: "bad id", Importance: "P0", Position: 1},
	})
	if err == nil {
		t.Fatal("want error for bad qid shape in batch")
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 0 {
		t.Fatalf("rejected SetQuestions must not partially apply: %+v", qs)
	}
}

// TestUpsertQuestionPreservesAnswerOnUpdate covers the per-question style
// of the same preserve rule (Q12: both bulk-set and per-question ops
// preserve kraken-owned columns).
func TestUpsertQuestionPreservesAnswerOnUpdate(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "q-single"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "Q1", Text: "t", Importance: "P0", Position: 0}); err != nil {
		t.Fatalf("UpsertQuestion add: %v", err)
	}
	if err := s.AnswerQuestion(id, "Q1", "ans", "at"); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "Q1", Text: "t2", Importance: "P1", Position: 0}); err != nil {
		t.Fatalf("UpsertQuestion update: %v", err)
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 1 || qs[0].Text != "t2" || qs[0].Importance != "P1" {
		t.Fatalf("zeus-owned fields not updated: %+v", qs)
	}
	if qs[0].Answer != "ans" || qs[0].AnsweredAt != "at" {
		t.Fatalf("answer not preserved across UpsertQuestion: %+v", qs)
	}
}

func TestDeleteQuestion(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "q-delete"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "Q1", Text: "t", Importance: "P0"}); err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	if err := s.DeleteQuestion(id, "Q1"); err != nil {
		t.Fatalf("DeleteQuestion: %v", err)
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 0 {
		t.Fatalf("question not deleted: %+v", qs)
	}
	// deleting an already-absent qid is a no-op, not an error
	if err := s.DeleteQuestion(id, "Q404"); err != nil {
		t.Fatalf("DeleteQuestion on missing qid: %v", err)
	}
}

// TestAnswerQuestionReAnswerOverwrites covers Q13: rows are editable
// forever — a second AnswerQuestion call is the same UPDATE as the first,
// never refused.
func TestAnswerQuestionReAnswerOverwrites(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "q-answer"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "Q1", Text: "t", Importance: "P0"}); err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	if err := s.AnswerQuestion(id, "Q1", "first", "t1"); err != nil {
		t.Fatalf("AnswerQuestion first: %v", err)
	}
	if err := s.AnswerQuestion(id, "Q1", "second", "t2"); err != nil {
		t.Fatalf("AnswerQuestion re-answer: %v", err)
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 1 || qs[0].Answer != "second" || qs[0].AnsweredAt != "t2" {
		t.Fatalf("re-answer didn't overwrite: %+v", qs)
	}

	if err := s.AnswerQuestion(id, "Q404", "x", "y"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("AnswerQuestion unknown qid: want ErrNotFound, got %v", err)
	}
}

// TestQuestionValidation pins that qid shape and importance are validated
// here (store), with a descriptive error — the API layer surfaces these as
// 400s rather than the old "one malformed line hides every question"
// failure mode.
func TestQuestionValidation(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "q-validate"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "bad", Text: "t", Importance: "P0"}); err == nil {
		t.Fatal("want error for bad qid shape")
	}
	if err := s.UpsertQuestion(id, DocQuestion{QID: "Q1", Text: "t", Importance: "P5"}); err == nil {
		t.Fatal("want error for bad importance")
	}
	qs, err := s.ListQuestions(id)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 0 {
		t.Fatalf("invalid UpsertQuestion calls must not have written anything: %+v", qs)
	}
}
