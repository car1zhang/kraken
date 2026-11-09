package jobs

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// loadjudge_test.go — the load judge: the rubric's landing rule, the request
// it builds, and the pass' behaviour per tick with every side effect faked
// (no network, no transcript on disk).

// judgeAnswers builds a response whose unblock ladder reads yes up to and
// including rung u and whose scope ladder reads yes up to s.
func judgeAnswers(u, s int) core.SystemOneResponse {
	resp := core.SystemOneResponse{Model: "jev-test", Answers: map[string]core.SystemOneAnswer{}}
	for r := 2; r <= 10; r++ {
		resp.Answers[loadUnblockKey(r)] = core.SystemOneAnswer{Type: "noul", Noul: yesNo(r <= u)}
		resp.Answers[loadScopeKey(r)] = core.SystemOneAnswer{Type: "noul", Noul: yesNo(r <= s)}
	}
	return resp
}

func yesNo(b bool) float64 {
	if b {
		return 0.9
	}
	return 0.1
}

func TestLoadJudgeLandClimbsConsecutiveYeses(t *testing.T) {
	for _, c := range []struct{ u, s int }{{1, 1}, {1, 2}, {4, 1}, {5, 2}, {3, 7}, {10, 10}} {
		resp := judgeAnswers(c.u, c.s)
		u, okU := loadJudgeLand(resp, loadUnblockKey)
		s, okS := loadJudgeLand(resp, loadScopeKey)
		if !okU || !okS || u != c.u || s != c.s {
			t.Errorf("yes through u%d/s%d landed %d/%d (ok %v/%v)", c.u, c.s, u, s, okU, okS)
		}
	}
}

// TestLoadJudgeLandStopsAtTheFirstNo: a stray yes above a no does not lift
// the rung — the ladder is climbed from the bottom, and the first rung that
// does not hold is where it stops.
func TestLoadJudgeLandStopsAtTheFirstNo(t *testing.T) {
	resp := judgeAnswers(3, 1)
	resp.Answers[loadUnblockKey(7)] = core.SystemOneAnswer{Noul: 0.95}
	if u, ok := loadJudgeLand(resp, loadUnblockKey); !ok || u != 3 {
		t.Fatalf("landed %d (ok %v), want 3: the yes at rung 7 sits above the no at 4", u, ok)
	}
	// exactly the threshold reads yes; just under it reads no
	resp = judgeAnswers(1, 1)
	resp.Answers[loadUnblockKey(2)] = core.SystemOneAnswer{Noul: loadJudgeYes}
	if u, _ := loadJudgeLand(resp, loadUnblockKey); u != 2 {
		t.Fatalf("a noul of exactly %.2f landed %d, want 2", loadJudgeYes, u)
	}
	resp.Answers[loadUnblockKey(2)] = core.SystemOneAnswer{Noul: loadJudgeYes - 0.01}
	if u, _ := loadJudgeLand(resp, loadUnblockKey); u != 1 {
		t.Fatalf("a noul just under %.2f landed %d, want 1", loadJudgeYes, u)
	}
}

// TestLoadJudgeLandRefusesAMissingRung: a response missing any rung's answer
// is malformed, not a shorter ladder — a missing rung would otherwise land
// every job below it silently.
func TestLoadJudgeLandRefusesAMissingRung(t *testing.T) {
	resp := judgeAnswers(5, 5)
	delete(resp.Answers, loadScopeKey(8))
	if _, ok := loadJudgeLand(resp, loadScopeKey); ok {
		t.Fatal("a ladder missing s8 landed anyway")
	}
	if u, ok := loadJudgeLand(resp, loadUnblockKey); !ok || u != 5 {
		t.Fatalf("the intact unblock ladder landed %d (ok %v), want 5", u, ok)
	}
	if _, ok := loadJudgeLand(core.SystemOneResponse{}, loadUnblockKey); ok {
		t.Fatal("an empty response landed")
	}
}

// TestLoadJudgeRequestIsTheRubric pins the request's shape: eighteen noul
// questions keyed u2..u10 and s2..s10, each naming its rung's label and
// recognizer and asking "at least"; the directive, the job context and the
// message as state; no choice question and no number asked for.
func TestLoadJudgeRequestIsTheRubric(t *testing.T) {
	ctx := loadJudgeContext{Slug: "demo", PRs: []loadJudgePR{{URL: "https://github.com/o/r/pull/1", State: "open"}}, Questions: []loadJudgeQuestion{}, Stack: []loadJudgeStack{}}
	req := loadJudgeRequest(loadDirective, ctx, "manual test ready: https://github.com/o/r/pull/1", "jev-latest")
	if req.Model != "jev-latest" {
		t.Errorf("model = %q", req.Model)
	}
	if len(req.Questions) != 18 {
		t.Fatalf("%d questions, want 18", len(req.Questions))
	}
	for r := 2; r <= 10; r++ {
		for _, q := range []struct {
			key    string
			ladder [10]LoadRung
		}{{loadUnblockKey(r), UnblockLadder}, {loadScopeKey(r), ScopeLadder}} {
			got, ok := req.Questions[q.key]
			if !ok {
				t.Errorf("no question %s", q.key)
				continue
			}
			if got.Type != "noul" {
				t.Errorf("%s type = %q, want noul: the rubric is yes/no questions, never a number", q.key, got.Type)
			}
			ins, _ := got.Instructions.(string)
			if !strings.Contains(ins, q.ladder[r-1].Label) || !strings.Contains(ins, q.ladder[r-1].Recognize) || !strings.Contains(ins, "at least") {
				t.Errorf("%s instructions do not name rung %d's label and recognizer as an at-least question: %q", q.key, r, ins)
			}
		}
	}
	state, ok := req.State.(map[string]any)
	if !ok {
		t.Fatalf("state is %T", req.State)
	}
	if state["directive"] != loadDirective || state["last_message"] != "manual test ready: https://github.com/o/r/pull/1" {
		t.Errorf("state = %v", state)
	}
	if _, ok := state["job"].(loadJudgeContext); !ok {
		t.Errorf("state.job is %T", state["job"])
	}
}

func TestLoadJudgeNoteNamesBothRungs(t *testing.T) {
	if got := loadJudgeNote(1, 1); got != "jev: Mechanical · One-message close-out" {
		t.Fatalf("note = %q", got)
	}
	if got := loadJudgeNote(5, 2); got != "jev: Live E2E drive · One small round" {
		t.Fatalf("note = %q", got)
	}
}

func TestLoadJudgeMessageKeepsHeadAndTail(t *testing.T) {
	short := "ready\x1b[0m now\tplease"
	if got := loadJudgeMessage(short); got != "ready [0m now please" {
		t.Fatalf("short = %q", got)
	}
	long := strings.Repeat("a", 3000) + "\n" + strings.Repeat("b", 3000)
	got := loadJudgeMessage(long)
	if n := len([]rune(got)); n > loadJudgeMessageMax+3 {
		t.Fatalf("long message is %d runes", n)
	}
	if !strings.HasPrefix(got, "aaaa") || !strings.HasSuffix(got, "bbbb") || !strings.Contains(got, "…") {
		t.Fatalf("long = %q…%q", got[:8], got[len(got)-8:])
	}
}

// judgeFixture wires a temp store with one started job and fakes every side
// effect. It returns the job and the recorded calls.
type judgeFixture struct {
	st    *store.Store
	j     *Job
	calls []core.SystemOneRequest
	resp  core.SystemOneResponse
	err   error
	turn  func(path string) (string, string, bool)
	logs  []string
}

func newJudgeFixture(t *testing.T) *judgeFixture {
	t.Helper()
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "triage", Repo: "kraken", State: "blocked"})
	if err != nil {
		t.Fatal(err)
	}
	f := &judgeFixture{st: st, resp: judgeAnswers(1, 1)}
	f.j = &Job{Slug: "triage", ID: id, State: "blocked", Repo: "kraken"}
	f.turn = func(string) (string, string, bool) { return "uuid-1", "manual test ready: pr", true }

	oldClient, oldSys, oldTurn := loadJudgeClient, loadJudgeSystemOne, loadJudgeTurn
	t.Cleanup(func() { loadJudgeClient, loadJudgeSystemOne, loadJudgeTurn = oldClient, oldSys, oldTurn })
	loadJudgeClient = func() *core.TypeSafeClient { return core.NewTypeSafeClient(core.TypeSafeConfig{APIKey: "k"}) }
	loadJudgeSystemOne = func(_ *core.TypeSafeClient, req core.SystemOneRequest) (core.SystemOneResponse, error) {
		f.calls = append(f.calls, req)
		return f.resp, f.err
	}
	loadJudgeTurn = func(path string) (string, string, bool) { return f.turn(path) }
	return f
}

func (f *judgeFixture) logf(level, event, msg string) {
	f.logs = append(f.logs, level+" "+event+" "+msg)
}

func (f *judgeFixture) row(t *testing.T) store.JobLoad {
	t.Helper()
	j, err := f.st.GetJobBySlug("triage")
	if err != nil {
		t.Fatal(err)
	}
	return j.Load
}

var judgeNow = time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)

// TestLoadJudgePassJudgesANewTurnOnce: a blocked, live job with an unjudged
// turn gets one call and one write; the same turn on later ticks costs
// nothing.
func TestLoadJudgePassJudgesANewTurnOnce(t *testing.T) {
	f := newJudgeFixture(t)
	f.resp = judgeAnswers(4, 1)
	s := newLoadJudgeState()

	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
	if len(f.calls) != 1 {
		t.Fatalf("%d calls, want 1", len(f.calls))
	}
	got := f.row(t)
	if got.Unblock != 4 || got.Scope != 1 || got.Note != "jev: Local smoke, or a credential · One-message close-out" || got.At != judgeNow.UnixMilli() {
		t.Fatalf("row = %+v", got)
	}
	if f.j.Load != got {
		t.Fatalf("in-memory job not updated: %+v", f.j.Load)
	}
	raw, err := f.st.GetKV("load_judged:" + itoa(f.j.ID))
	if err != nil {
		t.Fatalf("kv: %v", err)
	}
	var v loadVerdict
	if json.Unmarshal([]byte(raw), &v) != nil || v.Key != "uuid-1" || v.Unblock != 4 || v.Scope != 1 {
		t.Fatalf("kv verdict = %s", raw)
	}

	for tick := 2; tick <= 4; tick++ {
		s.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(time.Duration(tick)*15*time.Second), tick, f.logf)
	}
	if len(f.calls) != 1 {
		t.Fatalf("same turn re-judged: %d calls", len(f.calls))
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "action=judged key=uuid-1 unblock=4 scope=1") {
		t.Fatalf("no judged log line: %v", f.logs)
	}
}

// TestLoadJudgePassJudgesEachTurn: a new final message is a new judgment.
func TestLoadJudgePassJudgesEachTurn(t *testing.T) {
	f := newJudgeFixture(t)
	s := newLoadJudgeState()
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
	f.turn = func(string) (string, string, bool) { return "uuid-2", "questions pending: Q3 (P0)", true }
	f.resp = judgeAnswers(3, 6)
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(time.Minute), 2, f.logf)
	if len(f.calls) != 2 {
		t.Fatalf("%d calls, want 2", len(f.calls))
	}
	if got := f.row(t); got.Unblock != 3 || got.Scope != 6 {
		t.Fatalf("row = %+v", got)
	}
	state := f.calls[1].State.(map[string]any)
	if state["last_message"] != "questions pending: Q3 (P0)" {
		t.Fatalf("second call judged %q", state["last_message"])
	}
}

// TestLoadJudgePassWorkingClearsAndReblockReapplies: a working tick clears
// the estimate; the same turn back under blocked gets the remembered
// verdict without a call.
func TestLoadJudgePassWorkingClearsAndReblockReapplies(t *testing.T) {
	f := newJudgeFixture(t)
	f.resp = judgeAnswers(5, 2)
	s := newLoadJudgeState()
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
	if got := f.row(t); got.Unblock != 5 {
		t.Fatalf("row = %+v", got)
	}

	s.loadJudgePass(f.j, "working", true, nil, judgeNow.Add(15*time.Second), 2, f.logf)
	if got := f.row(t); got != (store.JobLoad{}) {
		t.Fatalf("working tick left %+v", got)
	}
	if f.j.Load != (store.JobLoad{}) {
		t.Fatalf("in-memory job still carries %+v", f.j.Load)
	}

	later := judgeNow.Add(30 * time.Second)
	s.loadJudgePass(f.j, "blocked", true, nil, later, 3, f.logf)
	if len(f.calls) != 1 {
		t.Fatalf("re-block of the same turn made a call: %d", len(f.calls))
	}
	if got := f.row(t); got.Unblock != 5 || got.Scope != 2 || got.At != later.UnixMilli() {
		t.Fatalf("reapplied row = %+v", got)
	}
}

// TestLoadJudgePassRestartReappliesFromKV: a fresh state (a serve restart)
// finds the verdict in kv and never re-asks for a turn already judged.
func TestLoadJudgePassRestartReappliesFromKV(t *testing.T) {
	f := newJudgeFixture(t)
	f.resp = judgeAnswers(2, 3)
	newLoadJudgeState().loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)

	// the estimate survives on the row; a restart with it present costs nothing
	s2 := newLoadJudgeState()
	s2.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(time.Minute), 1, f.logf)
	if len(f.calls) != 1 {
		t.Fatalf("restart re-judged: %d calls", len(f.calls))
	}

	// cleared row + restart: the kv verdict is re-applied, not re-asked
	if err := SetJobLoad(f.j, store.JobLoad{}); err != nil {
		t.Fatal(err)
	}
	s3 := newLoadJudgeState()
	s3.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(2*time.Minute), 1, f.logf)
	if len(f.calls) != 1 {
		t.Fatalf("restart over a cleared row re-judged: %d calls", len(f.calls))
	}
	if got := f.row(t); got.Unblock != 2 || got.Scope != 3 {
		t.Fatalf("row = %+v", got)
	}
}

// TestLoadJudgePassAPIErrorWritesNothingAndRetriesLater: a failed call
// leaves the row alone, logs once for the turn, and is retried only after
// the retry gap.
func TestLoadJudgePassAPIErrorWritesNothingAndRetriesLater(t *testing.T) {
	f := newJudgeFixture(t)
	f.err = errors.New("typesafe: HTTP 500")
	s := newLoadJudgeState()
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(15*time.Second), 2, f.logf)
	if len(f.calls) != 1 {
		t.Fatalf("%d calls inside the retry gap, want 1", len(f.calls))
	}
	if got := f.row(t); got != (store.JobLoad{}) {
		t.Fatalf("an API error wrote %+v", got)
	}
	errLines := 0
	for _, l := range f.logs {
		if strings.Contains(l, "action=api-error") {
			errLines++
		}
	}
	if errLines != 1 {
		t.Fatalf("%d api-error lines, want 1: %v", errLines, f.logs)
	}

	f.err = nil
	f.resp = judgeAnswers(1, 2)
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(loadJudgeRetryAfter+time.Second), 3, f.logf)
	if len(f.calls) != 2 {
		t.Fatalf("%d calls after the retry gap, want 2", len(f.calls))
	}
	if got := f.row(t); got.Unblock != 1 || got.Scope != 2 {
		t.Fatalf("row = %+v", got)
	}
}

// TestLoadJudgePassBadAnswerWritesNothing: a response missing a rung is
// logged and not written, and the same turn is not re-asked.
func TestLoadJudgePassBadAnswerWritesNothing(t *testing.T) {
	f := newJudgeFixture(t)
	f.resp = judgeAnswers(3, 3)
	delete(f.resp.Answers, loadUnblockKey(6))
	s := newLoadJudgeState()
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(2*time.Minute), 2, f.logf)
	if len(f.calls) != 1 {
		t.Fatalf("a bad answer was re-asked: %d calls", len(f.calls))
	}
	if got := f.row(t); got != (store.JobLoad{}) {
		t.Fatalf("a bad answer wrote %+v", got)
	}
	if !strings.Contains(strings.Join(f.logs, "\n"), "action=bad-answer") {
		t.Fatalf("no bad-answer log: %v", f.logs)
	}
}

// TestLoadJudgePassSkips: no call and no write for a cell that is not live,
// a job in any state but blocked or working, a transcript with no text
// block, a done job, or a daemon with no TypeSafe key.
func TestLoadJudgePassSkips(t *testing.T) {
	f := newJudgeFixture(t)
	s := newLoadJudgeState()
	s.loadJudgePass(f.j, "blocked", false, nil, judgeNow, 1, f.logf)
	s.loadJudgePass(f.j, "draft", true, nil, judgeNow, 1, f.logf)
	done := *f.j
	done.State = "done"
	s.loadJudgePass(&done, "blocked", true, nil, judgeNow, 1, f.logf)
	f.turn = func(string) (string, string, bool) { return "", "", false }
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
	f.turn = func(string) (string, string, bool) { return "uuid-1", "text", true }
	loadJudgeClient = func() *core.TypeSafeClient { return nil }
	s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
	if len(f.calls) != 0 {
		t.Fatalf("%d calls, want none", len(f.calls))
	}
	if got := f.row(t); got != (store.JobLoad{}) {
		t.Fatalf("a skipped pass wrote %+v", got)
	}
}

// TestLoadJudgePassCapsCallsPerTick: across jobs, one tick makes at most
// loadJudgeMaxCallsPerTick calls; the rest wait for the next tick.
func TestLoadJudgePassCapsCallsPerTick(t *testing.T) {
	f := newJudgeFixture(t)
	var jobs []*Job
	for i := 0; i < loadJudgeMaxCallsPerTick+2; i++ {
		slug := "cap-" + itoa(int64(i))
		id, err := f.st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", State: "blocked"})
		if err != nil {
			t.Fatal(err)
		}
		jobs = append(jobs, &Job{Slug: slug, ID: id, State: "blocked", Repo: "kraken"})
	}
	s := newLoadJudgeState()
	for _, j := range jobs {
		s.loadJudgePass(j, "blocked", true, nil, judgeNow, 1, f.logf)
	}
	if len(f.calls) != loadJudgeMaxCallsPerTick {
		t.Fatalf("%d calls on one tick, want %d", len(f.calls), loadJudgeMaxCallsPerTick)
	}
	for _, j := range jobs {
		s.loadJudgePass(j, "blocked", true, nil, judgeNow.Add(15*time.Second), 2, f.logf)
	}
	if len(f.calls) != len(jobs) {
		t.Fatalf("%d calls after two ticks, want %d", len(f.calls), len(jobs))
	}
}

// TestLoadJudgeContextRedactsAndBounds: question text is redacted and
// one-lined, PRs and stack links ride, and a nil store yields empty lists
// rather than nil (the judge sees `[]`, never `null`).
func TestLoadJudgeContextRedactsAndBounds(t *testing.T) {
	f := newJudgeFixture(t)
	f.j.PRs = []JobPR{{URL: "https://github.com/o/r/pull/9", State: "open"}}
	f.j.FrozenAt = 5
	if err := f.st.UpsertQuestion(f.j.ID, store.DocQuestion{QID: "Q1", Importance: "P0", Text: "token sk-ant-api03-" + strings.Repeat("x", 90) + "\nsecond line"}); err != nil {
		t.Fatal(err)
	}
	ctx := loadJudgeContextFor(f.st, f.j, "")
	if ctx.Slug != "triage" || !ctx.Frozen || len(ctx.PRs) != 1 || ctx.PRs[0].State != "open" {
		t.Fatalf("ctx = %+v", ctx)
	}
	if len(ctx.Questions) != 1 || ctx.Questions[0].Importance != "P0" {
		t.Fatalf("questions = %+v", ctx.Questions)
	}
	if q := ctx.Questions[0].Text; strings.Contains(q, "\n") || strings.Contains(q, "sk-ant-api03-xxxx") {
		t.Fatalf("question text not one-lined and redacted: %q", q)
	}
	bare := loadJudgeContextFor(nil, f.j, "")
	raw, _ := json.Marshal(bare)
	if strings.Contains(string(raw), "null") {
		t.Fatalf("nil-store context serializes a null list: %s", raw)
	}
}

// TestLoadJudgeContextSendsOpenQuestionsOnlyAndCaps: an answered question no
// longer blocks anything, so it is left out; past the cap the rest ride as a
// count.
func TestLoadJudgeContextSendsOpenQuestionsOnlyAndCaps(t *testing.T) {
	f := newJudgeFixture(t)
	if err := f.st.UpsertQuestion(f.j.ID, store.DocQuestion{QID: "Q1", Importance: "P1", Text: "settled"}); err != nil {
		t.Fatal(err)
	}
	// the answer is kraken's own column, written by its verb, never by the
	// cell's upsert
	if err := f.st.AnswerQuestion(f.j.ID, "Q1", "yes", "2026-11-09"); err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= loadJudgeQuestionsMax+4; i++ {
		if err := f.st.UpsertQuestion(f.j.ID, store.DocQuestion{QID: "Q" + itoa(int64(i)), Importance: "P2", Text: "open"}); err != nil {
			t.Fatal(err)
		}
	}
	ctx := loadJudgeContextFor(f.st, f.j, "")
	if len(ctx.Questions) != loadJudgeQuestionsMax {
		t.Fatalf("%d questions sent, want %d", len(ctx.Questions), loadJudgeQuestionsMax)
	}
	if ctx.QuestionsOmitted != 3 {
		t.Fatalf("omitted = %d, want 3 (the answered one is not omitted, it is not open)", ctx.QuestionsOmitted)
	}
	for _, q := range ctx.Questions {
		if q.QID == "Q1" {
			t.Fatal("the answered question rode in the request")
		}
	}
}

// TestLoadJudgePassNewTurnClearsTheOldEstimateBeforeAsking: a new turn found
// under blocked, with no working tick in between (a turn shorter than a
// tick), must not keep the previous gate's price on the card when its own
// judgment fails — on an API error, a bad answer, or no client at all.
func TestLoadJudgePassNewTurnClearsTheOldEstimateBeforeAsking(t *testing.T) {
	for _, c := range []struct {
		name string
		arm  func(f *judgeFixture)
	}{
		{"api error", func(f *judgeFixture) { f.err = errors.New("typesafe: HTTP 500") }},
		{"bad answer", func(f *judgeFixture) {
			f.resp = judgeAnswers(2, 2)
			delete(f.resp.Answers, loadScopeKey(4))
		}},
		{"no client", func(f *judgeFixture) {
			loadJudgeClient = func() *core.TypeSafeClient { return nil }
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newJudgeFixture(t)
			f.resp = judgeAnswers(5, 2)
			s := newLoadJudgeState()
			s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
			if got := f.row(t); got.Unblock != 5 {
				t.Fatalf("turn 1 row = %+v", got)
			}
			c.arm(f)
			f.turn = func(string) (string, string, bool) { return "uuid-2", "manual test ready: v2", true }
			s.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(15*time.Second), 2, f.logf)
			if got := f.row(t); got != (store.JobLoad{}) {
				t.Fatalf("turn 1's estimate survived turn 2's failed judgment: %+v", got)
			}
			if !strings.Contains(strings.Join(f.logs, "\n"), "action=cleared why=new-turn") {
				t.Fatalf("no new-turn clear logged: %v", f.logs)
			}
		})
	}
}

// TestLoadJudgePassIgnoresAnOffLadderKVVerdict: a kv row whose pair is off
// the ladder (hand-edited, or written by a build with more rungs) is not
// indexed into the ladders; the turn is judged afresh and the row replaced.
func TestLoadJudgePassIgnoresAnOffLadderKVVerdict(t *testing.T) {
	for _, bad := range []loadVerdict{
		{Key: "uuid-1", Unblock: 11, Scope: 2},
		{Key: "uuid-1", Unblock: 3, Scope: 0},
		{Key: "uuid-1", Unblock: -1, Scope: 40},
	} {
		f := newJudgeFixture(t)
		raw, _ := json.Marshal(bad)
		if err := f.st.SetKV("load_judged:"+itoa(f.j.ID), string(raw)); err != nil {
			t.Fatal(err)
		}
		f.resp = judgeAnswers(2, 3)
		s := newLoadJudgeState()
		// the same key, with no estimate on the row: the reapply path, which
		// must refuse the off-ladder pair rather than index with it
		s.loadJudgePass(f.j, "blocked", true, nil, judgeNow, 1, f.logf)
		if len(f.calls) != 0 {
			t.Fatalf("%+v: the same turn was re-asked (%d calls) instead of left alone", bad, len(f.calls))
		}
		if got := f.row(t); got != (store.JobLoad{}) {
			t.Fatalf("%+v: an off-ladder verdict was written: %+v", bad, got)
		}
		// a new turn replaces it
		f.turn = func(string) (string, string, bool) { return "uuid-2", "text", true }
		s.loadJudgePass(f.j, "blocked", true, nil, judgeNow.Add(time.Minute), 2, f.logf)
		if got := f.row(t); got.Unblock != 2 || got.Scope != 3 {
			t.Fatalf("%+v: next turn row = %+v", bad, got)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
