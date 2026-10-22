package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kraken/internal/store"
)

// mindpass_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 639, the mind pass), slice C: the nightly
// creator (`kickMind`, `runMindPass`, `CreateMindJob`, `mindJobContent`, the
// tick wiring), `ProposeMMItems` and its notification seam, the extended
// `ValidCaptureSource`, `CollectMMEvidence`, and the `kraken` author stamp on
// `CreateDreamJob`.
//
// Written from the frozen contract alone; internal/jobs' working tree was never
// read. The committed package test helpers this file leans on (useTestStore,
// mustCreateJob, hasEvent, dreamNoon, today, writeTranscriptFixture,
// stubNoUsagePass, stubAvailCheck, waitAvailIdle) were read through
// `git show HEAD:`.
//
// Dimension: LOGIC — invariants and happy paths. The gate's refusals,
// validation refusals and the evidence route's error arms are athena-edge's.
// Every package-level identifier here is prefixed `mpc`.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// mpcResetMindGuards clears the in-memory date backstop (process-global, like
// dream's) and gives kickMind a kraken checkout to resolve.
func mpcResetMindGuards(t *testing.T) {
	t.Helper()
	lastMindDate.Store("")
	t.Cleanup(func() { lastMindDate.Store("") })
	mpcReposDir(t)
}

// mpcReposDir makes `kraken` resolvable (a bare .git dir is all a repo needs).
func mpcReposDir(t *testing.T) string {
	t.Helper()
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
	return repos
}

type mpcMindStub struct {
	calls   atomic.Int32
	fired   chan struct{}
	block   chan struct{}
	gotPrev atomic.Value
	next    MindRun
	ok      bool
}

// mpcStubMindPass replaces the `mindPass` seam ("the test seam, exactly as
// dream's").
func mpcStubMindPass(t *testing.T, next MindRun, ok bool) *mpcMindStub {
	t.Helper()
	mpcResetMindGuards(t)
	s := &mpcMindStub{fired: make(chan struct{}, 8), next: next, ok: ok}
	prev := mindPass
	mindPass = func(now time.Time, p MindRun) (*Job, MindRun, bool) {
		s.calls.Add(1)
		s.gotPrev.Store(p)
		s.fired <- struct{}{}
		if s.block != nil {
			<-s.block
		}
		return nil, s.next, s.ok
	}
	t.Cleanup(func() { mindPass = prev })
	return s
}

func (s *mpcMindStub) waitFired(t *testing.T) {
	t.Helper()
	select {
	case <-s.fired:
	case <-time.After(2 * time.Second):
		t.Fatal("mind pass never fired")
	}
}

func mpcSeedMindRun(t *testing.T, r MindRun) {
	t.Helper()
	if err := writeMindRun(r); err != nil {
		t.Fatal(err)
	}
}

type mpcNote struct{ title, msg string }

// mpcCaptureNotify swaps `mmProposeNotify` ("the seam a test captures").
func mpcCaptureNotify(t *testing.T) *[]mpcNote {
	t.Helper()
	var mu sync.Mutex
	got := &[]mpcNote{}
	prev := mmProposeNotify
	mmProposeNotify = func(title, msg string) {
		mu.Lock()
		defer mu.Unlock()
		*got = append(*got, mpcNote{title, msg})
	}
	t.Cleanup(func() { mmProposeNotify = prev })
	return got
}

// mpcMindSetup stands up the mind job the propose gate reads: a working job
// row with a stamped lead session, named by `mind_last_run.job`.
func mpcMindSetup(t *testing.T) (*store.Store, *store.Job, string) {
	t.Helper()
	st := useTestStore(t)
	mpcReposDir(t)
	const lead = "mind-lead-session"
	j := mustCreateJob(t, st, store.Job{Slug: "mind-2026-10-21", Repo: "kraken", State: "working", Session: lead, Body: "x", Author: "kraken"})
	mpcSeedMindRun(t, MindRun{Date: "2026-10-21", SweptTo: 1, Job: j.Slug})
	return st, j, lead
}

func mpcProposal(quote string) MMProposal {
	return MMProposal{
		Title: "the trade mapping lives in dispatch groups", Scope: "repo", Repo: "kraken", Dir: "internal/jobs",
		Kind: "decision", Body: "Carl stated the trade mapping belongs in dispatch groups, not areas.", Quote: quote,
		Rung:   "rung 2: plain assertion",
		Source: CaptureSource{Kind: "transcript", Session: "sess-1", UUID: "uuid-1"},
	}
}

func mpcAllItems(t *testing.T, st *store.Store) []store.MMItem {
	t.Helper()
	all, err := st.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func mpcAddMM(t *testing.T, st *store.Store, it store.MMItem) int64 {
	t.Helper()
	if it.Kind == "" {
		it.Kind = "note"
	}
	if it.Content == "" {
		it.Content = "existing body"
	}
	id, err := st.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	return id
}

func mpcPropose(t *testing.T, st *store.Store, session string, items ...MMProposal) MMProposeSummary {
	t.Helper()
	sum, err := ProposeMMItems(st, session, items)
	if err != nil {
		t.Fatalf("ProposeMMItems: %v", err)
	}
	return sum
}

// mpcCheckCounts: "the counts equal the outcomes in `Results`".
func mpcCheckCounts(t *testing.T, sum MMProposeSummary) {
	t.Helper()
	c := map[string]int{}
	for _, r := range sum.Results {
		c[r.Outcome]++
	}
	if c["added"] != sum.Added || c["folded"] != sum.Folded || c["suppressed"] != sum.Suppressed || c["refused"] != sum.Refused {
		t.Errorf("summary counts %d/%d/%d/%d disagree with results %v", sum.Added, sum.Folded, sum.Suppressed, sum.Refused, c)
	}
}

func mpcMindNoon() time.Time { return dreamNoon() }

// ---------------------------------------------------------------------------
// C1 — kickMind cadence ("the dream cadence verbatim")
// ---------------------------------------------------------------------------

func TestMpcKickMindFiresWhenNeverRun(t *testing.T) {
	useTestStore(t)
	now := mpcMindNoon()
	s := mpcStubMindPass(t, MindRun{Date: today(now), SweptTo: now.UnixMilli()}, true)

	kickMind(now)
	s.waitFired(t)

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times with no watermark, want 1", n)
	}
	if got := ReadMindRun(); got.Date != today(now) || got.SweptTo != now.UnixMilli() {
		t.Fatalf("watermark = %+v, want the pass' returned run written", got)
	}
}

// "the watermark gate (`run.Date >= today` → nil)".
func TestMpcKickMindSkipsTheSameLocalDate(t *testing.T) {
	useTestStore(t)
	now := mpcMindNoon()
	s := mpcStubMindPass(t, MindRun{}, true)
	mpcSeedMindRun(t, MindRun{Date: today(now), SweptTo: now.Add(-time.Hour).UnixMilli(), Job: "mind-" + today(now)})

	kickMind(now)
	kickMind(now.Add(3 * time.Hour))

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("pass fired %d times on a date already swept, want 0", n)
	}
}

func TestMpcKickMindFiresOnTheNextLocalDate(t *testing.T) {
	useTestStore(t)
	now := mpcMindNoon()
	y := now.AddDate(0, 0, -1)
	s := mpcStubMindPass(t, MindRun{Date: today(now), SweptTo: now.UnixMilli()}, true)
	mpcSeedMindRun(t, MindRun{Date: today(y), SweptTo: y.UnixMilli(), Job: "mind-" + today(y)})

	kickMind(now)
	s.waitFired(t)

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times on a new date, want 1", n)
	}
	if prev, _ := s.gotPrev.Load().(MindRun); prev.SweptTo != y.UnixMilli() || prev.Date != today(y) {
		t.Errorf("pass handed prev %+v, want the stored watermark", prev)
	}
}

// "success stamps the in-memory date" — the backstop holds when the stored
// watermark never records.
func TestMpcKickMindBackstopsAWatermarkThatNeverRecords(t *testing.T) {
	useTestStore(t)
	now := mpcMindNoon()
	s := mpcStubMindPass(t, MindRun{}, true)

	kickMind(now)
	s.waitFired(t)
	kickMind(now.Add(time.Minute))
	kickMind(now.Add(time.Hour))

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times, want 1 — the in-memory date is the gate left", n)
	}
}

// "then CAS" — single flight.
func TestMpcKickMindSingleFlights(t *testing.T) {
	useTestStore(t)
	now := mpcMindNoon()
	s := mpcStubMindPass(t, MindRun{Date: today(now)}, true)
	s.block = make(chan struct{})

	done := make(chan struct{})
	go func() {
		defer close(done)
		kickMind(now)
	}()
	s.waitFired(t)
	kickMind(now)
	close(s.block)
	<-done

	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass fired %d times with one in flight, want 1", n)
	}
}

// "ok false leaves BOTH watermarks".
func TestMpcKickMindFailedPassLeavesBothWatermarks(t *testing.T) {
	useTestStore(t)
	now := mpcMindNoon()
	s := mpcStubMindPass(t, MindRun{Date: today(now), SweptTo: now.UnixMilli()}, false)

	kickMind(now)
	s.waitFired(t)
	if got := ReadMindRun(); got != (MindRun{}) {
		t.Fatalf("watermark = %+v after a failed pass, want the zero value", got)
	}
	kickMind(now.Add(time.Minute))
	s.waitFired(t)
	if n := s.calls.Load(); n != 2 {
		t.Fatalf("pass fired %d times, want 2 — the in-memory date must be untouched too", n)
	}
}

// `readMindRun`: "unreadable → ok false" — and kickMind treats that as
// already ran (the dream trio's semantics).
func TestMpcKickMindSkipsAnUnreadableWatermark(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Cleanup(func() { UseStore(nil) })
	UseStore(nil)
	s := mpcStubMindPass(t, MindRun{}, true)

	kickMind(time.Now())

	if n := s.calls.Load(); n != 0 {
		t.Fatalf("pass fired %d times with no store, want 0", n)
	}
}

// "the `ResolveRepo("kraken")` gate (event `mind-skipped`, in-memory date
// stamped only)".
func TestMpcKickMindSkipsWithoutAKrakenCheckout(t *testing.T) {
	st := useTestStore(t)
	now := mpcMindNoon()
	s := mpcStubMindPass(t, MindRun{Date: today(now), SweptTo: now.UnixMilli()}, true)
	t.Setenv("KRAKEN_REPOS_DIR", t.TempDir()) // no kraken under it

	if got := kickMind(now); got != nil {
		t.Fatalf("created %s with no checkout", got.Slug)
	}
	kickMind(now.Add(time.Minute))
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("pass fired %d times with no checkout, want 0", n)
	}
	if got := ReadMindRun(); got != (MindRun{}) {
		t.Errorf("stored watermark = %+v, want untouched", got)
	}
	if !hasEvent(t, st, "mind-skipped") {
		t.Error("want a mind-skipped event")
	}
}

// `ReadMindRun`: "absent key → zero value".
func TestMpcReadMindRunAbsentIsZero(t *testing.T) {
	useTestStore(t)
	if got := ReadMindRun(); got != (MindRun{}) {
		t.Fatalf("ReadMindRun with no row = %+v", got)
	}
	r, ok := readMindRun()
	if !ok || r != (MindRun{}) {
		t.Fatalf("readMindRun with no row = %+v, %v; want zero, true", r, ok)
	}
	want := MindRun{Date: "2026-10-21", SweptTo: 1761000000000, Job: "mind-2026-10-21"}
	mpcSeedMindRun(t, want)
	if got := ReadMindRun(); got != want {
		t.Fatalf("round trip = %+v, want %+v", got, want)
	}
	raw, err := ActiveStore().GetKV(mindRunKVKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{`"date"`, `"swept_to"`, `"job"`} {
		if !strings.Contains(raw, k) {
			t.Errorf("kv %s = %s, want key %s", mindRunKVKey, raw, k)
		}
	}
	if mindRunKVKey != "mind_last_run" {
		t.Errorf("mindRunKVKey = %q", mindRunKVKey)
	}
}

// ---------------------------------------------------------------------------
// C1 — runMindPass (driven through kickMind, the dream tests' shape)
// ---------------------------------------------------------------------------

// "a gate whose every query answered and found nothing advances `SweptTo` with
// no job (event `mind-skip`)".
func TestMpcMindPassSkipsAnEmptyNight(t *testing.T) {
	st := useTestStore(t)
	mpcResetMindGuards(t)
	now := mpcMindNoon()

	if created := kickMind(now); created != nil {
		t.Fatalf("created %s on an empty night", created.Slug)
	}
	got := ReadMindRun()
	if got.Date != today(now) || got.SweptTo == 0 || got.Job != "" {
		t.Fatalf("watermark = %+v, want today, an advanced floor, no job", got)
	}
	if !hasEvent(t, st, "mind-skip") {
		t.Error("want a mind-skip event")
	}
}

func mpcCheckCreated(t *testing.T, st *store.Store, created *Job, now time.Time) {
	t.Helper()
	slug := "mind-" + today(now)
	if created == nil || created.Slug != slug {
		t.Fatalf("created = %+v, want %s", created, slug)
	}
	if created.Err != "" || created.Started() || created.Repo != "kraken" || created.Solo {
		t.Errorf("created = err %q started %v repo %q solo %v, want a clean kraken draft", created.Err, created.Started(), created.Repo, created.Solo)
	}
	row, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatal(err)
	}
	if row.Author != "kraken" {
		t.Errorf("mind job author = %q, want kraken", row.Author)
	}
	// JobID rides the watermark since the review-round amendment ("MindRun
	// gains JobID … stamped at create with the new row's id"); the lead
	// applied this one sighted test fix after the authors were unblinded.
	got := ReadMindRun()
	if got != (MindRun{Date: today(now), SweptTo: now.UnixMilli(), Job: slug, JobID: row.ID}) {
		t.Errorf("watermark = %+v, want {%s %d %s %d}", got, today(now), now.UnixMilli(), slug, row.ID)
	}
	if !hasEvent(t, st, "mind-job-created") {
		t.Error("want a mind-job-created event")
	}
}

// Evidence gate arm 3: `CountJobsTouchedSince(since) > 0`.
func TestMpcMindPassCreatesTheJobOnAJobTouched(t *testing.T) {
	st := useTestStore(t)
	mpcResetMindGuards(t)
	now := mpcMindNoon()
	mustCreateJob(t, st, store.Job{Slug: "operator-spec", Repo: "kraken", Body: "x",
		CreatedAt: now.Add(-2 * time.Hour).UnixMilli(), UpdatedAt: now.Add(-time.Hour).UnixMilli()})

	mpcCheckCreated(t, st, kickMind(now), now)
}

// Evidence gate arm 2: `CountFeedbackSince(since) > 0`.
func TestMpcMindPassCreatesTheJobOnFeedback(t *testing.T) {
	st := useTestStore(t)
	mpcResetMindGuards(t)
	now := mpcMindNoon()
	if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Note: "n", TS: now.Add(-time.Hour).UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	mpcCheckCreated(t, st, kickMind(now), now)
}

// Evidence gate arm 1: `anyTranscriptSince(since)`.
func TestMpcMindPassCreatesTheJobOnATranscript(t *testing.T) {
	st := useTestStore(t)
	mpcResetMindGuards(t)
	now := mpcMindNoon()
	writeTranscriptFixture(t, "-Users-carl-Developer-kraken", "a.jsonl", now.Add(-30*time.Minute))
	mpcCheckCreated(t, st, kickMind(now), now)
}

// "a taken slug returns `{Date: today, SweptTo: prev.SweptTo}` with ok true
// (event `mind-skip`)".
func TestMpcMindPassSkipsATakenSlug(t *testing.T) {
	st := useTestStore(t)
	mpcResetMindGuards(t)
	now := mpcMindNoon()
	mustCreateJob(t, st, store.Job{Slug: "mind-" + today(now), Repo: "kraken", Body: "hand-made",
		CreatedAt: now.Add(-3 * 24 * time.Hour).UnixMilli(), UpdatedAt: now.Add(-3 * 24 * time.Hour).UnixMilli()})
	if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Note: "n", TS: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	floor := now.Add(-2 * time.Hour).UnixMilli()
	mpcSeedMindRun(t, MindRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: floor})

	if created := kickMind(now); created != nil {
		t.Fatalf("created %s over a taken slug", created.Slug)
	}
	got := ReadMindRun()
	if got.Date != today(now) || got.SweptTo != floor || got.Job != "" {
		t.Errorf("watermark = %+v, want {%s %d \"\"}", got, today(now), floor)
	}
	if !hasEvent(t, st, "mind-skip") {
		t.Error("want a mind-skip event")
	}
}

// "window `[since, now]` with `since = prev.SweptTo`, falling back to `now -
// dreamFirstWindow` when `<= 0`, clamped to `now - dreamMaxWindow` (clamped
// reported in the body)".
func TestMpcMindPassWindowEdgesReachTheBody(t *testing.T) {
	for _, c := range []struct {
		name      string
		prev      int64
		wantSince func(now time.Time) int64
		clamped   bool
	}{
		{"fallback", 0, func(n time.Time) int64 { return n.Add(-dreamFirstWindow).UnixMilli() }, false},
		{"floor", -1, nil, false}, // set below to a 3h floor
		{"clamped", -2, func(n time.Time) int64 { return n.Add(-dreamMaxWindow).UnixMilli() }, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := useTestStore(t)
			mpcResetMindGuards(t)
			now := mpcMindNoon()
			if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Note: "n", TS: now.Add(-time.Minute).UnixMilli()}); err != nil {
				t.Fatal(err)
			}
			want := int64(0)
			switch c.prev {
			case 0:
				want = c.wantSince(now)
			case -1:
				want = now.Add(-3 * time.Hour).UnixMilli()
				mpcSeedMindRun(t, MindRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: want})
			case -2:
				mpcSeedMindRun(t, MindRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: now.AddDate(0, 0, -10).UnixMilli()})
				want = c.wantSince(now)
			}
			created := kickMind(now)
			if created == nil {
				t.Fatal("no job created")
			}
			cmd := fmt.Sprintf("kraken mine operator --since %d --until %d", want, now.UnixMilli())
			if !strings.Contains(created.Body, cmd) {
				t.Errorf("body lacks %q:\n%s", cmd, created.Body)
			}
			if got := strings.Contains(created.Body, "This window was clamped"); got != c.clamped {
				t.Errorf("clamp note present = %v, want %v", got, c.clamped)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// C1 — mindJobContent / CreateMindJob / CreateDreamJob
// ---------------------------------------------------------------------------

var mpcRFC3339 = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})`)

// `mindJobContent`: "frontmatter `job: <slug>`, `repo: kraken`, `solo: false`;
// the body names `.claude/skills/mind/SKILL.md` and says to read it BY PATH;
// states both window edges in RFC3339 AND as unix millis; names the exact
// commands …; includes `clampNote(clamped)`".
func TestMpcMindJobContentCarriesTheWindowAndTheSkill(t *testing.T) {
	since := time.Date(2026, 10, 21, 3, 0, 0, 0, time.UTC).UnixMilli()
	until := time.Date(2026, 10, 22, 3, 0, 0, 0, time.UTC).UnixMilli()
	slug := "mind-2026-10-22"
	for _, clamped := range []bool{false, true} {
		content := mindJobContent(slug, since, until, clamped)
		for _, line := range []string{"job: " + slug + "\n", "repo: kraken\n", "solo: false\n"} {
			if !strings.Contains(content, line) {
				t.Errorf("frontmatter lacks %q:\n%s", line, content)
			}
		}
		j := ParseJob(slug+JobSuffix, []byte(content))
		if j.Err != "" || j.Slug != slug || j.Repo != "kraken" || j.Solo {
			t.Fatalf("parse = err %q slug %q repo %q solo %v", j.Err, j.Slug, j.Repo, j.Solo)
		}
		body := j.Body
		if !strings.Contains(body, ".claude/skills/mind/SKILL.md") {
			t.Error("body does not name .claude/skills/mind/SKILL.md")
		}
		if !strings.Contains(strings.ToLower(body), "by path") {
			t.Error("body does not say to read the skill by path")
		}
		for _, cmd := range []string{
			fmt.Sprintf("kraken mine operator --since %d --until %d", since, until),
			fmt.Sprintf("kraken mm evidence --since %d --until %d", since, until),
		} {
			if !strings.Contains(body, cmd) {
				t.Errorf("body lacks the exact command %q", cmd)
			}
		}
		var sawSince, sawUntil bool
		for _, m := range mpcRFC3339.FindAllString(body, -1) {
			ts, err := time.Parse(time.RFC3339, m)
			if err != nil {
				continue
			}
			sawSince = sawSince || ts.Unix() == since/1000
			sawUntil = sawUntil || ts.Unix() == until/1000
		}
		if !sawSince || !sawUntil {
			t.Errorf("body states the edges in RFC3339: since %v until %v\n%s", sawSince, sawUntil, body)
		}
		if note := strings.TrimSpace(clampNote(clamped)); note != "" && !strings.Contains(content, note) {
			t.Errorf("clamped content lacks clampNote(true)")
		}
		if !clamped && strings.Contains(content, strings.TrimSpace(clampNote(true))) {
			t.Errorf("unclamped content carries the clamp note")
		}
	}
}

// `CreateMindJob`: "text → `ParseJob` → `CreateJob` with `Author: "kraken"`,
// reloaded through `GetJobBySlug` + `loadJobFull`"; `CreateDreamJob`: "now
// stamps `Author: "kraken"`".
func TestMpcCreateMindAndDreamJobsStampKraken(t *testing.T) {
	st := useTestStore(t)
	since, until := int64(1761000000000), int64(1761086400000)
	j, err := CreateMindJob(st, "mind-2026-10-22", since, until, false)
	if err != nil {
		t.Fatalf("CreateMindJob: %v", err)
	}
	if j.Slug != "mind-2026-10-22" || j.Repo != "kraken" || j.Err != "" || strings.TrimSpace(j.Body) == "" {
		t.Errorf("created = %+v", j)
	}
	if !strings.Contains(j.Body, ".claude/skills/mind/SKILL.md") {
		t.Errorf("created body lacks the skill path")
	}
	for _, slug := range []string{"mind-2026-10-22"} {
		row, err := st.GetJobBySlug(slug)
		if err != nil {
			t.Fatal(err)
		}
		if row.Author != "kraken" {
			t.Errorf("%s author = %q, want kraken", slug, row.Author)
		}
	}
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

// ---------------------------------------------------------------------------
// C1 — the tick
// ---------------------------------------------------------------------------

// daemon: "`kickDream` runs first, then `kickMind` … a mirror serve runs
// neither".
func TestMpcTickRunsDreamThenMind(t *testing.T) {
	useTestStore(t)
	stubNoUsagePass(t)
	stubAvailCheck(t, UpdateAvail{})
	mpcResetMindGuards(t)
	lastDreamDate.Store("")
	t.Cleanup(func() { lastDreamDate.Store("") })

	var mu sync.Mutex
	var order []string
	prevDream, prevMind := dreamPass, mindPass
	dreamPass = func(n time.Time, p DreamRun) (*Job, DreamRun, bool) {
		mu.Lock()
		order = append(order, "dream")
		mu.Unlock()
		return nil, DreamRun{Date: n.Format(dreamDateLayout)}, true
	}
	mindPass = func(n time.Time, p MindRun) (*Job, MindRun, bool) {
		mu.Lock()
		order = append(order, "mind")
		mu.Unlock()
		return nil, MindRun{Date: n.Format(dreamDateLayout)}, true
	}
	t.Cleanup(func() { dreamPass, mindPass = prevDream, prevMind })

	d := newDaemon()
	if _, err := d.tick(false); err != nil {
		t.Fatalf("tick: %v", err)
	}
	waitAvailIdle(t)
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(order, []string{"dream", "mind"}) {
		t.Fatalf("passes ran %v, want [dream mind]", order)
	}
}

func TestMpcMirrorTickRunsNeitherNightlyPass(t *testing.T) {
	useTestStore(t)
	s := mpcStubMindPass(t, MindRun{}, true)
	d := newDaemon()
	if _, err := d.tick(true); err != nil {
		t.Fatalf("mirror tick: %v", err)
	}
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("mind pass fired %d times under --mirror", n)
	}
}

// ---------------------------------------------------------------------------
// C2 — ProposeMMItems
// ---------------------------------------------------------------------------

// `mindLeadSession`: "`ok` only when `Job` is non-empty, `GetJobBySlug(Job)`
// finds an ACTIVE row and that row's `Session` is non-empty".
func TestMpcMindLeadSessionReadsTheWatermarkJob(t *testing.T) {
	st, j, lead := mpcMindSetup(t)
	id, slug, sess, ok := mindLeadSession(st)
	if !ok || id != j.ID || slug != j.Slug || sess != lead {
		t.Fatalf("mindLeadSession = %d %q %q %v, want %d %q %q true", id, slug, sess, ok, j.ID, j.Slug, lead)
	}
}

// ProposeMMItems' ADD arm: the stored row is `{Kind, Source: "nightly", State:
// "proposed", JobID: the mind job id, Slug: its slug, Content: Body, Context:
// Rung, Title, Scope, Repo, Dir, Quote: verbatim, SourceJSON:
// captureSourceJSON(Source), ThreadSession: "", CreatedAt: now}` → `added` with
// its id; "exactly ONE `mmProposeNotify` call when `Added > 0`" with the stated
// title and sentence. GATE: `session` trimmed equals the lead session.
func TestMpcProposeAddsProposedNightlyRows(t *testing.T) {
	st, j, lead := mpcMindSetup(t)
	notes := mpcCaptureNotify(t)
	a := mpcProposal("the trade mapping is not in areas, it should be in dispatch groups.")
	b := mpcProposal("ofc it should be editable by the config agent")
	b.Kind, b.Title, b.Scope, b.Repo, b.Dir = "note", "the config agent edits the boolean snippet", "universal", "", ""
	b.Source = CaptureSource{Kind: "job", Slug: "prebrief-644", SHA: "abc"}
	b.Rung = "rung 1: sure (ofc)"

	before := time.Now().UnixMilli()
	sum := mpcPropose(t, st, "  "+lead+"\n", a, b)
	after := time.Now().UnixMilli()

	if sum.Added != 2 || sum.Folded != 0 || sum.Suppressed != 0 || sum.Refused != 0 || len(sum.Results) != 2 {
		t.Fatalf("summary = %+v, want 2 added", sum)
	}
	mpcCheckCounts(t, sum)
	for i, p := range []MMProposal{a, b} {
		r := sum.Results[i]
		if r.Index != i || r.Outcome != "added" || r.ID <= 0 {
			t.Fatalf("results[%d] = %+v, want added with an id", i, r)
		}
		it, err := st.GetMMItem(r.ID)
		if err != nil {
			t.Fatalf("GetMMItem(%d): %v", r.ID, err)
		}
		want := store.MMItem{
			ID: r.ID, Kind: p.Kind, Source: "nightly", State: "proposed", JobID: j.ID, Slug: j.Slug,
			Content: p.Body, Context: p.Rung, Title: p.Title, Scope: p.Scope, Repo: p.Repo, Dir: p.Dir,
			Quote: p.Quote, SourceJSON: captureSourceJSON(p.Source), ThreadSession: "",
		}
		got := store.MMItem{
			ID: it.ID, Kind: it.Kind, Source: it.Source, State: it.State, JobID: it.JobID, Slug: it.Slug,
			Content: it.Content, Context: it.Context, Title: it.Title, Scope: it.Scope, Repo: it.Repo, Dir: it.Dir,
			Quote: it.Quote, SourceJSON: it.SourceJSON, ThreadSession: it.ThreadSession,
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("stored row %d =\n%+v\nwant\n%+v", i, got, want)
		}
		if it.CreatedAt < before || it.CreatedAt > after || it.InvalidatedAt != 0 {
			t.Errorf("row %d created_at %d (want in [%d,%d]) invalidated %d", i, it.CreatedAt, before, after, it.InvalidatedAt)
		}
	}
	live, err := st.ListMMItems(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 0 {
		t.Errorf("ListMMItems(false) = %d rows, want 0 — proposed rows stay out of the live list", len(live))
	}
	want := []mpcNote{{"kraken ◆ mental model", "2 proposed from last night (0 folded, 0 suppressed) — confirm on the board"}}
	if !reflect.DeepEqual(*notes, want) {
		t.Errorf("notifications = %+v, want %+v", *notes, want)
	}
}

// FOLD: "when `FindMMItemsByQuote(quote)` holds any row with `invalidated_at ==
// 0` (live or proposed, any source) → `folded` with that row's id (newest), no
// write"; "none otherwise" for the notification.
func TestMpcProposeFoldsOntoAnyLiveOrProposedRow(t *testing.T) {
	st, _, lead := mpcMindSetup(t)
	notes := mpcCaptureNotify(t)
	capLive := mpcAddMM(t, st, store.MMItem{Source: "capture", Quote: "quote held by a capture", CreatedAt: 10})
	nightProp := mpcAddMM(t, st, store.MMItem{Source: "nightly", State: "proposed", Quote: "quote held by a proposal", CreatedAt: 20})
	mpcAddMM(t, st, store.MMItem{Source: "manual", Quote: "quote held twice", CreatedAt: 30})
	newer := mpcAddMM(t, st, store.MMItem{Source: "nightly", Quote: "quote held twice", CreatedAt: 40})
	n := len(mpcAllItems(t, st))

	sum := mpcPropose(t, st, lead,
		mpcProposal("quote held by a capture"),
		mpcProposal("quote held by a proposal"),
		mpcProposal("quote held twice"),
	)
	mpcCheckCounts(t, sum)
	wantIDs := []int64{capLive, nightProp, newer}
	if sum.Folded != 3 || len(sum.Results) != 3 {
		t.Fatalf("summary = %+v, want 3 folded", sum)
	}
	for i, r := range sum.Results {
		if r.Outcome != "folded" || r.ID != wantIDs[i] || r.Index != i {
			t.Errorf("results[%d] = %+v, want folded onto %d", i, r, wantIDs[i])
		}
	}
	if got := len(mpcAllItems(t, st)); got != n {
		t.Errorf("rows = %d after folds, want %d — a fold writes nothing", got, n)
	}
	if len(*notes) != 0 {
		t.Errorf("notifications = %+v, want none with nothing added", *notes)
	}
}

// SUPPRESS: "when it holds no such row but holds an invalidated row whose
// `InvalidReason` is `removed` or `contradicted` (any source) → `suppressed`, no
// write"; any other invalidation reason does not suppress, and a one-character
// difference is a different quote.
func TestMpcProposeSuppressesDroppedQuotes(t *testing.T) {
	st, _, lead := mpcMindSetup(t)
	mpcCaptureNotify(t)
	kill := func(quote, reason string) {
		t.Helper()
		id := mpcAddMM(t, st, store.MMItem{Source: "nightly", Quote: quote, CreatedAt: 10})
		if err := st.InvalidateMMItem(id, 11, reason, "test"); err != nil {
			t.Fatal(err)
		}
	}
	kill("carl removed this one", "removed")
	kill("a flag contradicted this one", "contradicted")
	capID := mpcAddMM(t, st, store.MMItem{Source: "capture", Quote: "a capture removed this one", CreatedAt: 12})
	if err := st.InvalidateMMItem(capID, 13, "removed", "test"); err != nil {
		t.Fatal(err)
	}
	kill("superseded is not a rejection", "superseded")
	n := len(mpcAllItems(t, st))

	sum := mpcPropose(t, st, lead,
		mpcProposal("carl removed this one"),
		mpcProposal("a flag contradicted this one"),
		mpcProposal("a capture removed this one"),
		mpcProposal("superseded is not a rejection"),
		mpcProposal("carl removed this one."),
	)
	mpcCheckCounts(t, sum)
	want := []string{"suppressed", "suppressed", "suppressed", "added", "added"}
	for i, r := range sum.Results {
		if r.Outcome != want[i] {
			t.Errorf("results[%d] = %+v, want %s", i, r, want[i])
		}
		if r.Outcome == "suppressed" && r.ID != 0 {
			t.Errorf("results[%d] suppressed carries id %d", i, r.ID)
		}
	}
	if got := len(mpcAllItems(t, st)); got != n+2 {
		t.Errorf("rows = %d, want %d — only the two adds write", got, n+2)
	}
}

// Per item IN ORDER: an earlier item's add is a live (proposed) row the later
// identical quote folds onto.
func TestMpcProposeFoldsADuplicateWithinOneBatch(t *testing.T) {
	st, _, lead := mpcMindSetup(t)
	mpcCaptureNotify(t)
	sum := mpcPropose(t, st, lead, mpcProposal("said twice in one night"), mpcProposal("said twice in one night"))
	if len(sum.Results) != 2 || sum.Results[0].Outcome != "added" || sum.Results[1].Outcome != "folded" ||
		sum.Results[1].ID != sum.Results[0].ID {
		t.Fatalf("results = %+v, want added then folded onto it", sum.Results)
	}
}

// CAP: "when 20 items have already been `added` in this call → `refused` with
// reason `cap`, no write"; `mmProposeCap = 20`; the notification names the
// three counts.
func TestMpcProposeCapsAddsAtTwenty(t *testing.T) {
	st, _, lead := mpcMindSetup(t)
	notes := mpcCaptureNotify(t)
	if mmProposeCap != 20 {
		t.Fatalf("mmProposeCap = %d, want 20", mmProposeCap)
	}
	mpcAddMM(t, st, store.MMItem{Source: "capture", Quote: "already known", CreatedAt: 1})
	dead := mpcAddMM(t, st, store.MMItem{Source: "nightly", Quote: "already dropped", CreatedAt: 2})
	if err := st.InvalidateMMItem(dead, 3, "removed", "test"); err != nil {
		t.Fatal(err)
	}
	items := []MMProposal{mpcProposal("already known"), mpcProposal("already dropped")}
	for i := 0; i < 22; i++ {
		items = append(items, mpcProposal(fmt.Sprintf("distinct operator sentence number %02d", i)))
	}
	n := len(mpcAllItems(t, st))

	sum := mpcPropose(t, st, lead, items...)
	mpcCheckCounts(t, sum)
	if sum.Added != 20 || sum.Folded != 1 || sum.Suppressed != 1 || sum.Refused != 2 {
		t.Fatalf("summary = added %d folded %d suppressed %d refused %d, want 20/1/1/2", sum.Added, sum.Folded, sum.Suppressed, sum.Refused)
	}
	for i, r := range sum.Results {
		switch {
		case i < 2:
		case i < 22:
			if r.Outcome != "added" {
				t.Errorf("results[%d] = %+v, want added (in order)", i, r)
			}
		default:
			if r.Outcome != "refused" || r.Reason != "cap" || r.ID != 0 {
				t.Errorf("results[%d] = %+v, want refused: cap", i, r)
			}
		}
	}
	if got := len(mpcAllItems(t, st)); got != n+20 {
		t.Errorf("rows = %d, want %d", got, n+20)
	}
	want := []mpcNote{{"kraken ◆ mental model", "20 proposed from last night (1 folded, 1 suppressed) — confirm on the board"}}
	if !reflect.DeepEqual(*notes, want) {
		t.Errorf("notifications = %+v, want %+v", *notes, want)
	}
}

// The other three pointer kinds pass the proposal validation (`Source.Kind`
// one of `transcript | job | feedback | question`).
func TestMpcProposeAcceptsEachProposalPointerKind(t *testing.T) {
	st, _, lead := mpcMindSetup(t)
	mpcCaptureNotify(t)
	srcs := []CaptureSource{
		{Kind: "transcript", Session: "s", UUID: "u"},
		{Kind: "job", Slug: "spec-job"},
		{Kind: "feedback", FeedbackID: 7},
		{Kind: "question", Slug: "spec-job", QID: "Q3"},
	}
	var items []MMProposal
	for i, s := range srcs {
		p := mpcProposal(fmt.Sprintf("pointer kind sentence %d", i))
		p.Source = s
		items = append(items, p)
	}
	sum := mpcPropose(t, st, lead, items...)
	if sum.Added != 4 {
		t.Fatalf("summary = %+v, want all four added", sum)
	}
	for i, r := range sum.Results {
		it, err := st.GetMMItem(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		if it.SourceJSON != captureSourceJSON(srcs[i]) {
			t.Errorf("source_json[%d] = %s, want %s", i, it.SourceJSON, captureSourceJSON(srcs[i]))
		}
	}
}

// ---------------------------------------------------------------------------
// C3 — ValidCaptureSource
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// C4 — CollectMMEvidence
// ---------------------------------------------------------------------------

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

// The sentinel's text is part of the wire (`403 {"error":"not the mind job's
// lead session"}`).
func TestMpcErrNotMindLeadText(t *testing.T) {
	if ErrNotMindLead == nil || ErrNotMindLead.Error() != "not the mind job's lead session" {
		t.Fatalf("ErrNotMindLead = %v", ErrNotMindLead)
	}
	if errors.Is(ErrNotMindLead, ErrCommitInvalid) {
		t.Error("ErrNotMindLead must be its own sentinel")
	}
}
