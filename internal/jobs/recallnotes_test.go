package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// recallnotes_test.go — the unread-notes block (recallnotes.go): carl's
// feedback rows and question answers delivered once each, to the job's lead
// session, at the top of its next prompt.
//
// LEAD-AUTHORED, sighted: this slice was one coherent edit zeus made itself
// with nothing delegated, so there was no blind author and no join. The
// identifiers are prefixed leadNotes/TestLeadNotes so a later blind
// `recallnotes_contract_test.go` / `_edge_test.go` in this package cannot
// collide with them (the recall_serial_test.go convention).

// leadNotesJob seeds one working job with a stamped lead session and returns the
// store row (its ID is what feedback and questions key on).
func leadNotesJob(t *testing.T, st *store.Store, slug, session string) *store.Job {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, State: "working", Session: session})
	if err != nil {
		t.Fatalf("create job: %v", err)
	}
	j, err := st.GetJobByID(id)
	if err != nil {
		t.Fatalf("get job: %v", err)
	}
	return j
}

func leadNotesFire(t *testing.T, st *store.Store, slug, session, prompt string) string {
	t.Helper()
	return Recall(context.Background(), st, RecallRequest{
		SessionID: session, Event: "UserPromptSubmit", Slug: slug, Prompt: prompt,
	})
}

func TestLeadNotesRecallDeliversFeedbackAndAnswersOnce(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-once", "sess-notes-once"
	j := leadNotesJob(t, st, slug, sess)

	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Artifact: "00-overview.md", Note: "tighten the overview"}); err != nil {
		t.Fatalf("add feedback: %v", err)
	}
	if err := st.SetQuestions(j.ID, []store.DocQuestion{{QID: "Q1", Text: "ship as one PR?", Importance: "P0"}}); err != nil {
		t.Fatalf("set questions: %v", err)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "yes, one PR", time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("answer: %v", err)
	}

	got := leadNotesFire(t, st, slug, sess, "first")
	for _, want := range []string{"tighten the overview", "00-overview.md", "Q1 answered", "yes, one PR", "ship as one PR?", "kraken feedback get " + slug, "kraken questions get " + slug} {
		if !strings.Contains(got, want) {
			t.Errorf("block lacks %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(got, strings.ReplaceAll(notesContextHeader(), "<slug>", slug)) {
		t.Errorf("block does not open with the notes header:\n%s", got)
	}
	// The job's first delivery ever carries the first-contact caveat.
	if !strings.Contains(got, notesFirstContactLine) {
		t.Errorf("first delivery lacks the first-contact line:\n%s", got)
	}
	// A second prompt: both notes are recorded, nothing repeats.
	if got := leadNotesFire(t, st, slug, sess, "second"); got != "" {
		t.Fatalf("delivered notes repeated:\n%s", got)
	}
}

func TestLeadNotesRecallGoesOnlyToTheLeadSession(t *testing.T) {
	st := storeForRecall(t)
	const slug = "notes-lead"
	j := leadNotesJob(t, st, slug, "sess-zeus")
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "a note"}); err != nil {
		t.Fatalf("add feedback: %v", err)
	}
	// A teammate or side thread fires with the same slug and its own session.
	if got := leadNotesFire(t, st, slug, "sess-teammate", "hello"); strings.Contains(got, "a note") {
		t.Fatalf("a non-lead session received the notes:\n%s", got)
	}
	// Which must NOT have consumed the delivery: the lead still gets it.
	if got := leadNotesFire(t, st, slug, "sess-zeus", "hello"); !strings.Contains(got, "a note") {
		t.Fatalf("lead session did not receive the note:\n%s", got)
	}
}

func TestLeadNotesRecallUnstampedJobDeliversNothing(t *testing.T) {
	st := storeForRecall(t)
	const slug = "notes-unstamped"
	j := leadNotesJob(t, st, slug, "")
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "a note"}); err != nil {
		t.Fatalf("add feedback: %v", err)
	}
	if got := leadNotesFire(t, st, slug, "sess-any", "hello"); strings.Contains(got, "a note") {
		t.Fatalf("an unstamped job delivered its notes to an arbitrary session:\n%s", got)
	}
}

func TestLeadNotesRecallEditedNoteIsDeliveredAgain(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-edit", "sess-notes-edit"
	j := leadNotesJob(t, st, slug, sess)
	id, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "first wording"})
	if err != nil {
		t.Fatalf("add feedback: %v", err)
	}
	if got := leadNotesFire(t, st, slug, sess, "p1"); !strings.Contains(got, "first wording") {
		t.Fatalf("first delivery missing:\n%s", got)
	}
	if got := leadNotesFire(t, st, slug, sess, "p2"); got != "" {
		t.Fatalf("repeat:\n%s", got)
	}
	if err := st.UpdateFeedbackNote(id, "second wording", nil, ""); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := leadNotesFire(t, st, slug, sess, "p3")
	if !strings.Contains(got, "second wording") {
		t.Fatalf("edited note not re-delivered:\n%s", got)
	}
	if strings.Contains(got, notesFirstContactLine) {
		t.Fatalf("a later delivery still carries the first-contact line:\n%s", got)
	}
	if strings.Contains(got, "first wording") {
		t.Fatalf("stale wording delivered:\n%s", got)
	}
	// The stale key was pruned from the record; the live one is in it.
	rec, _, _ := readNotesDelivered(st, j.ID)
	if len(rec) != 1 {
		t.Fatalf("record holds %d keys after an edit, want 1: %v", len(rec), rec)
	}
}

func TestLeadNotesRecallReAnswerIsDeliveredAgainAndUnansweredIsNot(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-reanswer", "sess-notes-reanswer"
	j := leadNotesJob(t, st, slug, sess)
	if err := st.SetQuestions(j.ID, []store.DocQuestion{
		{QID: "Q1", Text: "one?", Importance: "P1"},
		{QID: "Q2", Text: "two?", Importance: "P2"},
	}); err != nil {
		t.Fatalf("set questions: %v", err)
	}
	// Nothing answered: an open question is zeus' own and is not a note.
	if got := leadNotesFire(t, st, slug, sess, "p0"); got != "" {
		t.Fatalf("unanswered questions produced a block:\n%s", got)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "alpha", "2026-10-08T10:00:00Z"); err != nil {
		t.Fatalf("answer: %v", err)
	}
	got := leadNotesFire(t, st, slug, sess, "p1")
	if !strings.Contains(got, "alpha") || strings.Contains(got, "Q2") {
		t.Fatalf("want Q1's answer and no Q2:\n%s", got)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "beta", "2026-10-08T11:00:00Z"); err != nil {
		t.Fatalf("re-answer: %v", err)
	}
	if got := leadNotesFire(t, st, slug, sess, "p2"); !strings.Contains(got, "beta") {
		t.Fatalf("re-answer not delivered:\n%s", got)
	}
}

func TestLeadNotesRecallIsChronologicalAcrossKinds(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-order", "sess-notes-order"
	j := leadNotesJob(t, st, slug, sess)
	base := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "LATE note", TS: base.Add(2 * time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "EARLY note", TS: base.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuestions(j.ID, []store.DocQuestion{{QID: "Q1", Text: "q", Importance: "P1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "MIDDLE answer", base.Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	got := leadNotesFire(t, st, slug, sess, "p")
	e, m, l := strings.Index(got, "EARLY note"), strings.Index(got, "MIDDLE answer"), strings.Index(got, "LATE note")
	if e < 0 || m < 0 || l < 0 || !(e < m && m < l) {
		t.Fatalf("order is not chronological (early %d, middle %d, late %d):\n%s", e, m, l, got)
	}
}

func TestLeadNotesRecallDefersWhatTheCapCannotCarry(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-cap", "sess-notes-cap"
	j := leadNotesJob(t, st, slug, sess)
	// Ten notes of ~3,900 runes each: the block carries as many as fit under
	// notesContextBytes, in order, and the rest wait for the next prompt.
	body := strings.Repeat("x", 3900)
	for i := 0; i < 10; i++ {
		note := "N" + string(rune('A'+i)) + " " + body
		if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: note, TS: int64(1000 + i)}); err != nil {
			t.Fatal(err)
		}
	}
	first := leadNotesFire(t, st, slug, sess, "p1")
	if len(first) > notesContextBytes+200 {
		t.Fatalf("block is %d bytes, cap %d", len(first), notesContextBytes)
	}
	if !strings.Contains(first, "NA ") || strings.Contains(first, "NJ ") {
		t.Fatalf("first block should carry the oldest and not the newest:\n%.300s", first)
	}
	if !strings.Contains(first, "more, held for your next prompt") {
		t.Fatalf("first block does not say more are waiting:\n%.300s", first)
	}
	second := leadNotesFire(t, st, slug, sess, "p2")
	if strings.Contains(second, "NA ") {
		t.Fatalf("second block repeats the first's notes")
	}
	// Everything arrives eventually, each exactly once.
	all := first + second
	for i := 0; i < 10 && !strings.Contains(all, "NJ "); i++ {
		all += leadNotesFire(t, st, slug, sess, "p"+string(rune('3'+i)))
	}
	for i := 0; i < 10; i++ {
		tag := "N" + string(rune('A'+i)) + " "
		if n := strings.Count(all, tag); n != 1 {
			t.Errorf("%s delivered %d times, want 1", strings.TrimSpace(tag), n)
		}
	}
	if got := leadNotesFire(t, st, slug, sess, "pz"); got != "" {
		t.Fatalf("notes keep arriving after all were delivered:\n%.200s", got)
	}
}

func TestLeadNotesRecallCutsAnOversizedItemWithAMarker(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-long", "sess-notes-long"
	j := leadNotesJob(t, st, slug, sess)
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: strings.Repeat("y", notesItemRunes+500)}); err != nil {
		t.Fatal(err)
	}
	got := leadNotesFire(t, st, slug, sess, "p")
	if !strings.Contains(got, "[cut — the whole note: `kraken feedback get "+slug+"`") {
		t.Fatalf("oversized note carries no cut marker:\n%.200s", got)
	}
	item := got[strings.Index(got, "- feedback #"):]
	if n := strings.Count(item, "y"); n > notesItemRunes+len("your") {
		t.Fatalf("oversized note was not cut: %d y's in the item", n)
	}
}

func TestLeadNotesRecallMultilineNoteStaysOneItem(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-multi", "sess-notes-multi"
	j := leadNotesJob(t, st, slug, sess)
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "line one\nline two\n\nline four"}); err != nil {
		t.Fatal(err)
	}
	got := leadNotesFire(t, st, slug, sess, "p")
	if !strings.Contains(got, "line one\n  line two\n  \n  line four\n") {
		t.Fatalf("continuation lines are not indented under their item:\n%s", got)
	}
}

func TestLeadNotesRecallMarksNothingForACallerThatIsGone(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-gone", "sess-notes-gone"
	j := leadNotesJob(t, st, slug, sess)
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "still owed"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = Recall(ctx, st, RecallRequest{SessionID: sess, Event: "UserPromptSubmit", Slug: slug, Prompt: "p"})
	if rec, _, _ := readNotesDelivered(st, j.ID); len(rec) != 0 {
		t.Fatalf("a delivery nobody received was recorded: %v", rec)
	}
	if got := leadNotesFire(t, st, slug, sess, "p2"); !strings.Contains(got, "still owed") {
		t.Fatalf("the note was lost to a gone caller:\n%s", got)
	}
}

func TestLeadNotesRecallRidesOnlyUserPromptSubmit(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-event", "sess-notes-event"
	j := leadNotesJob(t, st, slug, sess)
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "a note"}); err != nil {
		t.Fatal(err)
	}
	got := Recall(context.Background(), st, RecallRequest{SessionID: sess, Event: "PostToolUse", Slug: slug, Prompt: "p"})
	if strings.Contains(got, "a note") {
		t.Fatalf("a non-prompt event carried notes:\n%s", got)
	}
}

func TestLeadNotesRecallSurvivesARepeatedPrompt(t *testing.T) {
	// A repeated, identical prompt must not swallow a note that arrived
	// between the two firings.
	st := storeForRecall(t)
	const slug, sess = "notes-repeat", "sess-notes-repeat"
	j := leadNotesJob(t, st, slug, sess)
	if got := leadNotesFire(t, st, slug, sess, "same"); got != "" {
		t.Fatalf("unexpected block: %s", got)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "arrived between"}); err != nil {
		t.Fatal(err)
	}
	if got := leadNotesFire(t, st, slug, sess, "same"); !strings.Contains(got, "arrived between") {
		t.Fatalf("a note was swallowed on a repeated prompt:\n%s", got)
	}
}

func TestLeadNotesRecallPrecedesGoals(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-first", "sess-notes-first"
	j := leadNotesJob(t, st, slug, sess)
	week, _ := store.GoalPeriods(time.Now())
	if _, err := st.AddGoal(store.GoalAdd{Scope: "week", Period: week, Text: "a week goal"}, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "a note"}); err != nil {
		t.Fatal(err)
	}
	got := leadNotesFire(t, st, slug, sess, "p")
	n, g := strings.Index(got, "a note"), strings.Index(got, "a week goal")
	if n < 0 || g < 0 || n > g {
		t.Fatalf("notes (%d) should precede goals (%d):\n%s", n, g, got)
	}
}

func TestLeadNotesRecallDeletedRowIsPrunedFromTheRecord(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-prune", "sess-notes-prune"
	j := leadNotesJob(t, st, slug, sess)
	id, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "doomed"})
	if err != nil {
		t.Fatal(err)
	}
	leadNotesFire(t, st, slug, sess, "p1")
	if err := st.DeleteFeedback(id); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "survivor"}); err != nil {
		t.Fatal(err)
	}
	leadNotesFire(t, st, slug, sess, "p2")
	rec, _, _ := readNotesDelivered(st, j.ID)
	if len(rec) != 1 {
		t.Fatalf("record holds %d keys, want only the survivor's: %v", len(rec), rec)
	}
}

func TestLeadNotesRecallIgnoresAnUndecodableRecord(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-corrupt", "sess-notes-corrupt"
	j := leadNotesJob(t, st, slug, sess)
	if err := st.SetKV(notesDeliveredKey(j.ID), "not json"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "a note"}); err != nil {
		t.Fatal(err)
	}
	if got := leadNotesFire(t, st, slug, sess, "p"); !strings.Contains(got, "a note") {
		t.Fatalf("a corrupt record withheld the notes:\n%s", got)
	}
	if got := leadNotesFire(t, st, slug, sess, "p2"); got != "" {
		t.Fatalf("the record was not rewritten after a corrupt read:\n%s", got)
	}
}

func TestLeadNotesRecallBacklogIsDeliveredInOrderWithTheCaveat(t *testing.T) {
	// A job live when the feature ships, or reactivated after it, has a
	// backlog zeus may already have pulled by hand: it arrives, oldest
	// first, under the first-contact caveat, and never again after that.
	st := storeForRecall(t)
	const slug, sess = "notes-backlog", "sess-notes-backlog"
	j := leadNotesJob(t, st, slug, sess)
	base := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	for i := 0; i < 5; i++ {
		if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "old note " + string(rune('A'+i)), TS: base.Add(time.Duration(i) * time.Hour).UnixMilli()}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetQuestions(j.ID, []store.DocQuestion{{QID: "Q1", Text: "q", Importance: "P1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "old answer", base.Add(2*time.Hour+30*time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	got := leadNotesFire(t, st, slug, sess, "p1")
	if !strings.Contains(got, notesFirstContactLine) {
		t.Fatalf("backlog delivery lacks the first-contact caveat:\n%s", got)
	}
	last := -1
	for _, want := range []string{"old note A", "old note B", "old note C", "old answer", "old note D", "old note E"} {
		i := strings.Index(got, want)
		if i < 0 || i < last {
			t.Fatalf("%q missing or out of order (at %d, previous %d):\n%s", want, i, last, got)
		}
		last = i
	}
	if got := leadNotesFire(t, st, slug, sess, "p2"); got != "" {
		t.Fatalf("the backlog was re-delivered:\n%s", got)
	}
}

func TestLeadNotesRecallRecordReadDistinguishesMissingFromCorrupt(t *testing.T) {
	st := storeForRecall(t)
	j := leadNotesJob(t, st, "notes-read", "sess-notes-read")
	rec, present, err := readNotesDelivered(st, j.ID)
	if err != nil || present || len(rec) != 0 {
		t.Fatalf("missing row: rec=%v present=%v err=%v; want empty, absent, nil", rec, present, err)
	}
	if err := st.SetKV(notesDeliveredKey(j.ID), "{not json"); err != nil {
		t.Fatal(err)
	}
	rec, present, err = readNotesDelivered(st, j.ID)
	if err != nil || present || len(rec) != 0 {
		t.Fatalf("corrupt row: rec=%v present=%v err=%v; want empty, absent, nil", rec, present, err)
	}
	if err := st.SetKV(notesDeliveredKey(j.ID), `{"delivered":{"f:1:abc":5}}`); err != nil {
		t.Fatal(err)
	}
	rec, present, err = readNotesDelivered(st, j.ID)
	if err != nil || !present || rec["f:1:abc"] != 5 {
		t.Fatalf("valid row: rec=%v present=%v err=%v", rec, present, err)
	}
}

func TestLeadNotesRecallControlCharactersAndCRLF(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-ctl", "sess-notes-ctl"
	j := leadNotesJob(t, st, slug, sess)
	if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: slug, Note: "one\r\ntwo\x1b[0m\tthree\x00"}); err != nil {
		t.Fatal(err)
	}
	got := leadNotesFire(t, st, slug, sess, "p")
	if !strings.Contains(got, "one\n  two[0m\tthree\n") {
		t.Fatalf("control characters not normalized:\n%q", got)
	}
}

func TestLeadNotesRecallEmptyAnsweredAtRendersNoParens(t *testing.T) {
	st := storeForRecall(t)
	const slug, sess = "notes-noat", "sess-notes-noat"
	j := leadNotesJob(t, st, slug, sess)
	if err := st.SetQuestions(j.ID, []store.DocQuestion{{QID: "Q1", Text: "q", Importance: "P1"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "ans", ""); err != nil {
		t.Fatal(err)
	}
	got := leadNotesFire(t, st, slug, sess, "p")
	if !strings.Contains(got, "- Q1 answered: ans\n") {
		t.Fatalf("empty answered_at rendered badly:\n%s", got)
	}
}
