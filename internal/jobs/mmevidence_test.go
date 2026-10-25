package jobs

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// mmevidence_test.go — the surviving half of job 639's mind-pass tests
// (mindpass_contract_test.go, mindpass_edge_test.go), moved here when job 674
// retired the nightly mind job, its propose gate and its watermark: what
// outlived them is CollectMMEvidence (the evidence route), the extended
// ValidCaptureSource, and the dream creator's `kraken` author stamp. The
// bodies are those files' tests unchanged.

// "additionally accepts `job` (needs `Slug`), `feedback` (needs `FeedbackID >
// 0`), `question` (needs `Slug` and `QID`)"; existing fields unchanged.
func TestMpcValidCaptureSourceAcceptsTheThreeNewKinds(t *testing.T) {
	for _, s := range []CaptureSource{
		{Kind: "job", Slug: "spec-job"},
		{Kind: "feedback", FeedbackID: 1},
		{Kind: "question", Slug: "spec-job", QID: "Q1"},
		{Kind: "artifact", Name: "00-overview.md"},
		{Kind: "transcript", Session: "s"},
		{Kind: "dir", Repo: "kraken"},
		{Kind: "item"},
		{Kind: "manual"},
	} {
		if err := ValidCaptureSource(s); err != nil {
			t.Errorf("ValidCaptureSource(%+v) = %v, want accepted", s, err)
		}
	}
	js := captureSourceJSON(CaptureSource{Kind: "question", Slug: "j", QID: "Q2"})
	if !strings.Contains(js, `"qid":"Q2"`) {
		t.Errorf("question pointer JSON = %s, want a qid key", js)
	}
	js = captureSourceJSON(CaptureSource{Kind: "feedback", FeedbackID: 9})
	if !strings.Contains(js, `"feedback_id":9`) {
		t.Errorf("feedback pointer JSON = %s, want a feedback_id key", js)
	}
	if js := captureSourceJSON(CaptureSource{Kind: "job", Slug: "j"}); strings.Contains(js, "qid") || strings.Contains(js, "feedback_id") {
		t.Errorf("job pointer JSON = %s, want the new keys omitted when empty", js)
	}
}

// "additionally accepts `job` (needs `Slug`), `feedback` (needs `FeedbackID >
// 0`), `question` (needs `Slug` and `QID`); the refusal sentence lists all
// eight kinds".
func TestMPEValidCaptureSourceNewKindRefusals(t *testing.T) {
	for _, s := range []CaptureSource{
		{Kind: "job"},
		{Kind: "job", Slug: "   "},
		{Kind: "feedback"},
		{Kind: "feedback", FeedbackID: -3},
		{Kind: "question"},
		{Kind: "question", Slug: "some-job"},
		{Kind: "question", QID: "Q1"},
		{Kind: "question", Slug: "some-job", QID: "  "},
	} {
		if err := ValidCaptureSource(s); err == nil {
			t.Errorf("ValidCaptureSource(%+v) = nil, want a refusal", s)
		}
	}
	err := ValidCaptureSource(CaptureSource{Kind: "nightly"})
	if err == nil {
		t.Fatal("ValidCaptureSource(unknown kind) = nil, want a refusal")
	}
	for _, k := range []string{"artifact", "transcript", "dir", "item", "manual", "job", "feedback", "question"} {
		if !strings.Contains(err.Error(), k) {
			t.Errorf("refusal %q does not list %q", err, k)
		}
	}
}

// `CreateDreamJob` "stamps `Author: "kraken"`".
func TestMpcCreateDreamJobStampsKraken(t *testing.T) {
	st := useTestStore(t)
	since, until := int64(1761000000000), int64(1761086400000)
	if _, err := CreateDreamJob(st, "dream-2026-10-22", since, until, false); err != nil {
		t.Fatalf("CreateDreamJob: %v", err)
	}
	row, err := st.GetJobBySlug("dream-2026-10-22")
	if err != nil {
		t.Fatal(err)
	}
	if row.Author != "kraken" {
		t.Errorf("dream job author = %q, want kraken", row.Author)
	}
}

// `CollectMMEvidence`: "jobs are `ListJobsTouchedSince(since, until)` MINUS:
// rows whose `Author` is `agent`, `linear` or `kraken`; rows whose slug has
// prefix `dream-` or `mind-`; rows whose id appears as `JobID` in
// `ListLinearIssues()`; rows with `Author` `operator` or `unknown` otherwise
// pass"; `Body` strips `## Mid-run log` (the `jobPrompt` rule); `BodySHA =
// store.SHA256Hex([]byte(Body))`.
func TestMpcCollectMMEvidenceJobs(t *testing.T) {
	st := useTestStore(t)
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	since, until := base.UnixMilli(), base.Add(2*time.Hour).UnixMilli()
	at := func(m int) int64 { return base.Add(time.Duration(m) * time.Minute).UnixMilli() }
	raw := "Build the evidence route.\n\nIt reads three tables.\n\n## Mid-run log\n- 01:00 started\n"
	mk := func(slug, author string, created int64) *store.Job {
		return mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", Body: raw, Author: author, CreatedAt: created, UpdatedAt: created})
	}
	op := mk("operator-spec", "operator", at(1))
	unk := mk("pre-v37-spec", "", at(2))
	mk("agent-draft", "agent", at(3))
	mk("linear-seeded", "linear", at(4))
	mk("kraken-made", "kraken", at(5))
	mk("dream-2026-10-21", "operator", at(6))
	mk("mind-2026-10-21", "operator", at(7))
	lin := mk("linked-to-linear", "operator", at(8))
	if err := st.PutLinearIssue(&store.LinearIssueRow{IssueID: "iss-1", Identifier: "ENG-1", JobID: lin.ID, CreatedAt: at(8)}); err != nil {
		t.Fatal(err)
	}
	mk("outside-window", "operator", base.Add(-time.Hour).UnixMilli())
	late := mk("edited-late", "operator", at(9))

	ev, err := CollectMMEvidence(st, since, until)
	if err != nil {
		t.Fatalf("CollectMMEvidence: %v", err)
	}
	if ev.Since != since || ev.Until != until {
		t.Errorf("since/until = %d/%d", ev.Since, ev.Until)
	}
	body := jobPrompt(&Job{Body: raw})
	var got []string
	for _, j := range ev.Jobs {
		got = append(got, j.Slug)
	}
	if !reflect.DeepEqual(got, []string{op.Slug, unk.Slug, late.Slug}) {
		t.Fatalf("evidence jobs = %v, want [operator-spec pre-v37-spec edited-late]", got)
	}
	wantJob := MMEvidenceJob{Slug: op.Slug, ID: op.ID, Author: "operator", CreatedAt: at(1), UpdatedAt: at(1),
		BodySHA: store.SHA256Hex([]byte(body)), Body: body}
	if ev.Jobs[0] != wantJob {
		t.Errorf("jobs[0] = %+v, want %+v", ev.Jobs[0], wantJob)
	}
	if strings.Contains(ev.Jobs[0].Body, "Mid-run log") {
		t.Error("the mid-run log was not stripped")
	}
	if ev.Jobs[1].Author != "unknown" {
		t.Errorf("pre-v37 author = %q, want unknown", ev.Jobs[1].Author)
	}
	if ev.Feedback == nil || ev.Answers == nil {
		t.Error("feedback/answers slices are nil, want empty")
	}
}

// Feedback: "from `FeedbackSince(since)` filtered `ts <= until` when `until >
// 0`".
func TestMpcCollectMMEvidenceFeedback(t *testing.T) {
	st := useTestStore(t)
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	since, until := base.UnixMilli(), base.Add(2*time.Hour).UnixMilli()
	j := mustCreateJob(t, st, store.Job{Slug: "fb-job", Repo: "kraken", Body: "x", CreatedAt: since - 10_000, UpdatedAt: since - 10_000})
	add := func(note string, ts int64) {
		t.Helper()
		if _, err := st.AddFeedback(store.Feedback{JobID: j.ID, JobSlug: j.Slug, Artifact: "50-contract.md", Note: note, TS: ts}); err != nil {
			t.Fatal(err)
		}
	}
	add("before the floor", since-1)
	add("on the floor", since)
	add("inside", since+60_000)
	add("on the ceiling", until)
	add("past the ceiling", until+1)

	ev, err := CollectMMEvidence(st, since, until)
	if err != nil {
		t.Fatal(err)
	}
	all, err := st.FeedbackSince(since)
	if err != nil {
		t.Fatal(err)
	}
	want := []MMEvidenceFeedback{}
	for _, f := range all {
		if f.TS <= until {
			want = append(want, MMEvidenceFeedback{ID: f.ID, Slug: f.JobSlug, Artifact: f.Artifact, TS: f.TS, Note: f.Note})
		}
	}
	if len(want) != 3 {
		t.Fatalf("fixture: %d in-window rows, want 3", len(want))
	}
	if !reflect.DeepEqual(ev.Feedback, want) {
		t.Errorf("feedback = %+v, want %+v", ev.Feedback, want)
	}
	if len(ev.Jobs) != 0 {
		t.Errorf("jobs = %+v, want none (the job predates the window)", ev.Jobs)
	}
}

// Answers: "every `questions` row (`ListQuestionsAll`) with a non-empty `Answer`
// whose `AnsweredAt` (RFC3339) parses into the window; `AcceptedRec =
// strings.TrimSpace(Answer) == strings.TrimSpace(Recommendation)`; `Slug` is the
// owning job's current slug".
func TestMpcCollectMMEvidenceAnswers(t *testing.T) {
	st := useTestStore(t)
	base := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	since, until := base.UnixMilli(), base.Add(2*time.Hour).UnixMilli()
	in := base.Add(30 * time.Minute).Format(time.RFC3339)
	a := mustCreateJob(t, st, store.Job{Slug: "spec-a", Repo: "kraken", Body: "x", CreatedAt: 1, UpdatedAt: 1})
	b := mustCreateJob(t, st, store.Job{Slug: "agent-b", Repo: "kraken", Body: "x", Author: "agent", CreatedAt: 1, UpdatedAt: 1})
	if err := st.SetQuestions(a.ID, []store.DocQuestion{
		{QID: "Q1", Text: "which cap?", Importance: "P1", Recommendation: "twenty"},
		{QID: "Q2", Text: "which scope?", Importance: "P1", Recommendation: "repo"},
		{QID: "Q3", Text: "unanswered?", Importance: "P2", Recommendation: "x"},
		{QID: "Q4", Text: "answered last week?", Importance: "P2", Recommendation: "x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuestions(b.ID, []store.DocQuestion{{QID: "Q1", Text: "agent job q", Importance: "P0", Recommendation: "r"}}); err != nil {
		t.Fatal(err)
	}
	answer := func(job int64, qid, ans, when string) {
		t.Helper()
		if err := st.AnswerQuestion(job, qid, ans, when); err != nil {
			t.Fatal(err)
		}
	}
	answer(a.ID, "Q1", "  twenty \n", in)
	answer(a.ID, "Q2", "universal, it applies everywhere", base.Format(time.RFC3339))
	answer(a.ID, "Q4", "x", base.Add(-7*24*time.Hour).Format(time.RFC3339))
	answer(b.ID, "Q1", "my own words", base.Add(2*time.Hour).Format(time.RFC3339))

	ev, err := CollectMMEvidence(st, since, until)
	if err != nil {
		t.Fatal(err)
	}
	qa, _ := st.ListQuestions(a.ID)
	qb, _ := st.ListQuestions(b.ID)
	row := func(qs []store.DocQuestion, qid string) store.DocQuestion {
		for _, q := range qs {
			if q.QID == qid {
				return q
			}
		}
		t.Fatalf("no %s", qid)
		return store.DocQuestion{}
	}
	mkWant := func(slug string, q store.DocQuestion, acc bool) MMEvidenceAnswer {
		return MMEvidenceAnswer{Slug: slug, QID: q.QID, Text: q.Text, Answer: q.Answer, AnsweredAt: q.AnsweredAt, AcceptedRec: acc}
	}
	want := []MMEvidenceAnswer{
		mkWant("spec-a", row(qa, "Q1"), true),
		mkWant("spec-a", row(qa, "Q2"), false),
		mkWant("agent-b", row(qb, "Q1"), false),
	}
	if !reflect.DeepEqual(ev.Answers, want) {
		t.Errorf("answers = %+v\nwant %+v", ev.Answers, want)
	}
}

// "slices never nil" on an empty window.
func TestMpcCollectMMEvidenceEmptyWindowIsEmptySlices(t *testing.T) {
	st := useTestStore(t)
	ev, err := CollectMMEvidence(st, 1_000, 2_000)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Jobs == nil || ev.Feedback == nil || ev.Answers == nil || len(ev.Jobs)+len(ev.Feedback)+len(ev.Answers) != 0 {
		t.Errorf("empty window evidence = %+v, want three empty non-nil slices", ev)
	}
}

// "`since <= 0` is an error".
func TestMPECollectMMEvidenceRefusesNonPositiveSince(t *testing.T) {
	st := useTestStore(t)
	for _, since := range []int64{0, -1} {
		if _, err := CollectMMEvidence(st, since, 0); err == nil {
			t.Errorf("CollectMMEvidence(since %d) = nil error, want a refusal", since)
		}
	}
}

// Feedback: "`FeedbackSince(since)` filtered `ts <= until` when `until > 0`"
// — inclusive at both edges.
func TestMPECollectMMEvidenceFeedbackWindowEdges(t *testing.T) {
	st := useTestStore(t)
	const since, until = int64(1_000_000), int64(2_000_000)
	for _, ts := range []int64{since - 1, since, until, until + 1} {
		if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Artifact: "a.md", Note: strconv.FormatInt(ts, 10), TS: ts}); err != nil {
			t.Fatal(err)
		}
	}
	ev, err := CollectMMEvidence(st, since, until)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{}
	for _, f := range ev.Feedback {
		got = append(got, f.Note)
	}
	sort.Strings(got) // the contract freezes membership, not order
	want := []string{strconv.FormatInt(since, 10), strconv.FormatInt(until, 10)}
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("feedback in [%d,%d] = %v, want %v", since, until, got, want)
	}
}
