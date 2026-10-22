package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"kraken/internal/store"
)

// mindpass_edge_test.go — the EDGE dimension of 50-contract.md slice C (the
// nightly creator, propose, the pointer kinds, evidence). Written blind from
// the frozen contract: mindpass.go, mmpropose.go, mmevidence.go and every
// edited jobs file were never read — only the committed tests at HEAD, whose
// helpers (useTestStore, mustCreateJob, hasEvent, blindProjectsDir, today,
// dreamNoon) this file reuses. Gates, refusals, boundaries; the happy paths
// are athena-logic's.
//
// Every package-level identifier is prefixed `mpe`.

// ---------------------------------------------------------------------------
// C1 — kickMind cadence
// ---------------------------------------------------------------------------

// mpeResetMind clears the in-memory date backstop and gives ResolveRepo a
// kraken checkout (a bare .git dir), as resetDreamGuards does for dream.
func mpeResetMind(t *testing.T) {
	t.Helper()
	lastMindDate.Store("")
	t.Cleanup(func() { lastMindDate.Store("") })
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
}

type mpeMindStub struct {
	calls atomic.Int32
	next  MindRun
	ok    bool
}

// mpeStubMind swaps the `mindPass` seam ("the test seam, exactly as dream's").
func mpeStubMind(t *testing.T, next MindRun, ok bool) *mpeMindStub {
	t.Helper()
	s := &mpeMindStub{next: next, ok: ok}
	prev := mindPass
	mindPass = func(now time.Time, p MindRun) (*Job, MindRun, bool) {
		s.calls.Add(1)
		return nil, s.next, s.ok
	}
	t.Cleanup(func() { mindPass = prev })
	return s
}

// "unreadable → ok false" and kickMind's watermark gate: an unreadable
// watermark is "already ran" — the pass never fires.
func TestMPEKickMindUnreadableWatermarkDoesNothing(t *testing.T) {
	mpeResetMind(t)
	t.Cleanup(func() { UseStore(nil) })
	UseStore(nil)
	s := mpeStubMind(t, MindRun{}, true)

	if got := kickMind(dreamNoon()); got != nil {
		t.Fatalf("kickMind = %+v with no store, want nil", got)
	}
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("mind pass fired %d times on an unreadable watermark, want 0", n)
	}
}

// "unparseable → reset loudly … and ok true" — and the next kick fires.
func TestMPEKickMindUnparseableWatermarkResetsAndFires(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	for _, raw := range []string{"", "not json", "{", `{"date":`} {
		if err := st.SetKV(mindRunKVKey, raw); err != nil {
			t.Fatal(err)
		}
		run, ok := readMindRun()
		if !ok || run != (MindRun{}) {
			t.Fatalf("readMindRun(%q) = (%+v, %v), want (zero, true)", raw, run, ok)
		}
	}
	if err := st.SetKV(mindRunKVKey, "not json"); err != nil {
		t.Fatal(err)
	}
	now := dreamNoon()
	s := mpeStubMind(t, MindRun{Date: today(now), SweptTo: now.UnixMilli()}, true)
	kickMind(now)
	if n := s.calls.Load(); n != 1 {
		t.Fatalf("mind pass fired %d times after a corrupt watermark, want 1", n)
	}
}

// The watermark gate is `run.Date >= today` → nil: a watermark dated in the
// FUTURE never fires.
func TestMPEKickMindFutureDatedWatermarkNeverFires(t *testing.T) {
	useTestStore(t)
	mpeResetMind(t)
	now := dreamNoon()
	if err := writeMindRun(MindRun{Date: today(now.AddDate(0, 0, 3)), SweptTo: now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	s := mpeStubMind(t, MindRun{Date: today(now)}, true)
	kickMind(now)
	kickMind(now.Add(2 * time.Hour))
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("mind pass fired %d times under a future-dated watermark, want 0", n)
	}
}

// "the `ResolveRepo("kraken")` gate (event `mind-skipped`, in-memory date
// stamped only)": no pass, no watermark write.
func TestMPEKickMindSkipsWithoutAKrakenCheckout(t *testing.T) {
	st := useTestStore(t)
	lastMindDate.Store("")
	t.Cleanup(func() { lastMindDate.Store("") })
	t.Setenv("KRAKEN_REPOS_DIR", t.TempDir()) // empty: no kraken checkout
	s := mpeStubMind(t, MindRun{Date: "x"}, true)

	if got := kickMind(dreamNoon()); got != nil {
		t.Fatalf("kickMind = %+v without a kraken checkout, want nil", got)
	}
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("mind pass fired %d times without a kraken checkout, want 0", n)
	}
	if got := ReadMindRun(); got != (MindRun{}) {
		t.Fatalf("watermark = %+v, want it untouched (in-memory date only)", got)
	}
	if !hasEvent(t, st, "mind-skipped") {
		t.Fatal("want a mind-skipped event")
	}
}

// "ok false leaves BOTH watermarks": the stored one is unwritten and the
// in-memory date unstamped, so the next tick retries.
func TestMPEKickMindFailedPassLeavesBothWatermarks(t *testing.T) {
	useTestStore(t)
	mpeResetMind(t)
	now := dreamNoon()
	s := mpeStubMind(t, MindRun{Date: today(now), SweptTo: now.UnixMilli(), Job: "mind-x"}, false)

	kickMind(now)
	if got := ReadMindRun(); got != (MindRun{}) {
		t.Fatalf("watermark = %+v after a failed pass, want the zero value", got)
	}
	kickMind(now.Add(time.Minute))
	if n := s.calls.Load(); n != 2 {
		t.Fatalf("mind pass fired %d times, want 2 — a failed pass is retried", n)
	}
}

// ---------------------------------------------------------------------------
// C1 — runMindPass edges (the real pass, no stub)
// ---------------------------------------------------------------------------

func mpeFeedback(t *testing.T, st *store.Store, ts int64) {
	t.Helper()
	if _, err := st.AddFeedback(store.Feedback{JobSlug: "x", Artifact: "a.md", Note: "n", TS: ts}); err != nil {
		t.Fatal(err)
	}
}

// "a taken slug returns `{Date: today, SweptTo: prev.SweptTo}` with ok true
// (event `mind-skip`)" — through kickMind, so the stored watermark is that value.
func TestMPEMindPassSlugCollisionKeepsSweptTo(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	now := dreamNoon()
	mustCreateJob(t, st, store.Job{Slug: "mind-" + today(now), Repo: "kraken", Body: "hand-made"})
	mpeFeedback(t, st, now.Add(-time.Hour).UnixMilli())
	floor := now.Add(-3 * time.Hour).UnixMilli()
	if err := writeMindRun(MindRun{Date: today(now.AddDate(0, 0, -1)), SweptTo: floor}); err != nil {
		t.Fatal(err)
	}

	if got := kickMind(now); got != nil {
		t.Fatalf("kickMind created %s over a taken slug, want nil", got.Slug)
	}
	got := ReadMindRun()
	if got.Date != today(now) || got.SweptTo != floor {
		t.Fatalf("watermark = %+v, want {Date: %s, SweptTo: %d}", got, today(now), floor)
	}
	if got.Job != "" {
		t.Fatalf("watermark names job %q after a collision, want none", got.Job)
	}
	if !hasEvent(t, st, "mind-skip") {
		t.Fatal("want a mind-skip event")
	}
}

// "clamped to `now - dreamMaxWindow` (clamped reported in the body)": the
// body carries clampNote(true) and the CLAMPED floor in both forms the
// contract names.
func TestMPEMindPassClampsAWideWindowIntoTheBody(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	now := dreamNoon()
	mpeFeedback(t, st, now.Add(-time.Hour).UnixMilli())

	created, next, ok := runMindPass(now, MindRun{Date: "2026-01-01", SweptTo: now.Add(-30 * 24 * time.Hour).UnixMilli()})
	if !ok || created == nil {
		t.Fatalf("runMindPass = (%v, %+v, %v), want a created job", created, next, ok)
	}
	floor := now.Add(-dreamMaxWindow).UnixMilli()
	if note := clampNote(true); note == "" || !strings.Contains(created.Body, strings.TrimSpace(note)) {
		t.Errorf("body lacks clampNote(true):\n%s", created.Body)
	}
	for _, want := range []string{
		time.UnixMilli(floor).Format(time.RFC3339),
		"kraken mine operator --since " + strconv.FormatInt(floor, 10) + " --until " + strconv.FormatInt(now.UnixMilli(), 10),
		"kraken mm evidence --since " + strconv.FormatInt(floor, 10) + " --until " + strconv.FormatInt(now.UnixMilli(), 10),
	} {
		if !strings.Contains(created.Body, want) {
			t.Errorf("body lacks %q:\n%s", want, created.Body)
		}
	}
}

// "`since = prev.SweptTo`, falling back to `now - dreamFirstWindow` when `<=
// 0`" — a negative floor renders the fallback, never a negative --since, and
// is not reported as clamped.
func TestMPEMindPassNegativeFloorFallsBack(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	now := dreamNoon()
	mpeFeedback(t, st, now.Add(-time.Hour).UnixMilli())

	created, _, ok := runMindPass(now, MindRun{Date: "2026-01-01", SweptTo: -1})
	if !ok || created == nil {
		t.Fatal("want a created job")
	}
	fallback := now.Add(-dreamFirstWindow).UnixMilli()
	if want := "kraken mine operator --since " + strconv.FormatInt(fallback, 10); !strings.Contains(created.Body, want) {
		t.Errorf("body lacks the fallback floor %q:\n%s", want, created.Body)
	}
	if strings.Contains(created.Body, "--since -") || strings.Contains(created.Body, "1969") {
		t.Errorf("negative floor leaked into the body:\n%s", created.Body)
	}
	if note := strings.TrimSpace(clampNote(true)); note != "" && strings.Contains(created.Body, note) {
		t.Errorf("a fallback window is reported as clamped:\n%s", created.Body)
	}
}

// "a gate where a query FAILED and nothing was found returns ok false" — an
// unreadable projects dir with no feedback and no jobs touched.
func TestMPEMindPassFailedQueryAndNothingFoundIsNotOK(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	now := dreamNoon()
	blindProjectsDir(t)

	created, _, ok := runMindPass(now, MindRun{})
	if ok || created != nil {
		t.Fatalf("runMindPass over an unanswered gate = (%v, ok %v), want (nil, false)", created, ok)
	}
	if hasEvent(t, st, "mind-skip") {
		t.Fatal("logged mind-skip although the transcript query never answered")
	}
}

// ---------------------------------------------------------------------------
// C2 — ProposeMMItems gate
// ---------------------------------------------------------------------------

const mpeLeadSession = "sess-mind-lead"

// mpeMindLead creates the mind job, gives it a lead session and names it in
// `mind_last_run`, which is what `mindLeadSession` reads.
func mpeMindLead(t *testing.T, st *store.Store) *store.Job {
	t.Helper()
	j := mustCreateJob(t, st, store.Job{Slug: "mind-2026-10-21", Repo: "kraken", Body: "b", State: "working", Session: mpeLeadSession})
	if err := writeMindRun(MindRun{Date: "2026-10-21", SweptTo: 1, Job: j.Slug}); err != nil {
		t.Fatal(err)
	}
	return j
}

func mpeProposal(quote string) MMProposal {
	return MMProposal{
		Title:  "the board owns card order",
		Scope:  "universal",
		Kind:   "note",
		Body:   "Carl said the board owns card order.",
		Quote:  quote,
		Rung:   "asserted plainly",
		Source: CaptureSource{Kind: "transcript", Session: "sess-t", UUID: "u-1"},
	}
}

func mpeAllItems(t *testing.T, st *store.Store) []store.MMItem {
	t.Helper()
	items, err := st.ListMMItems(true)
	if err != nil {
		t.Fatal(err)
	}
	return items
}

type mpeNotified struct {
	calls  int
	title  string
	bodies []string
}

func mpeCaptureNotify(t *testing.T) *mpeNotified {
	t.Helper()
	n := &mpeNotified{}
	prev := mmProposeNotify
	mmProposeNotify = func(title, msg string) {
		n.calls++
		n.title = title
		n.bodies = append(n.bodies, msg)
	}
	t.Cleanup(func() { mmProposeNotify = prev })
	return n
}

// "GATE FIRST: `session` trimmed must be non-empty and equal the
// `mindLeadSession` session, else `ErrNotMindLead` with nothing read from
// `items`" — every non-lead caller, each with an INVALID batch so a refusal
// that came from validation instead of the gate shows up as the wrong error.
func TestMPEProposeGateRefusesEveryNonLead(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	n := mpeCaptureNotify(t)
	mpeMindLead(t, st)
	mustCreateJob(t, st, store.Job{Slug: "other-job", Repo: "kraken", Body: "b", State: "working", Session: "sess-other-lead"})
	mustCreateJob(t, st, store.Job{Slug: "mind-2026-10-20", Repo: "kraken", Body: "b", State: "working", Session: "sess-old-mind"})

	bad := mpeProposal("q")
	bad.Kind = "reading"
	for _, sess := range []string{"", "   ", "sess-thread-of-mind", "sess-other-lead", "sess-old-mind", "SESS-MIND-LEAD"} {
		_, err := ProposeMMItems(st, sess, []MMProposal{bad})
		if !errors.Is(err, ErrNotMindLead) {
			t.Errorf("ProposeMMItems(session %q) = %v, want ErrNotMindLead", sess, err)
		}
	}
	if items := mpeAllItems(t, st); len(items) != 0 {
		t.Fatalf("refused proposes stored %d rows", len(items))
	}
	if n.calls != 0 {
		t.Fatalf("refused proposes notified %d times", n.calls)
	}
	if ErrNotMindLead.Error() != "not the mind job's lead session" {
		t.Fatalf("ErrNotMindLead text = %q", ErrNotMindLead.Error())
	}
}

// "`ok` only when `Job` is non-empty, `GetJobBySlug(Job)` finds an ACTIVE row
// and that row's `Session` is non-empty" — no row at all, an empty Job, an
// archived mind job, and a mind job with no session all refuse even the
// session that would otherwise match.
func TestMPEProposeGateNeedsAResolvableMindLead(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, st *store.Store)
	}{
		{"no mind_last_run row", func(t *testing.T, st *store.Store) {
			mustCreateJob(t, st, store.Job{Slug: "mind-2026-10-21", Repo: "kraken", Body: "b", Session: mpeLeadSession})
		}},
		{"empty job in the watermark", func(t *testing.T, st *store.Store) {
			mustCreateJob(t, st, store.Job{Slug: "mind-2026-10-21", Repo: "kraken", Body: "b", Session: mpeLeadSession})
			if err := writeMindRun(MindRun{Date: "2026-10-21", SweptTo: 1}); err != nil {
				t.Fatal(err)
			}
		}},
		{"watermark names a missing job", func(t *testing.T, st *store.Store) {
			mustCreateJob(t, st, store.Job{Slug: "mind-2026-10-21", Repo: "kraken", Body: "b", Session: mpeLeadSession})
			if err := writeMindRun(MindRun{Date: "2026-10-22", SweptTo: 1, Job: "mind-2026-10-22"}); err != nil {
				t.Fatal(err)
			}
		}},
		{"archived mind job", func(t *testing.T, st *store.Store) {
			j := mpeMindLead(t, st)
			if _, err := st.ArchiveJob(j.ID); err != nil {
				t.Fatal(err)
			}
		}},
		{"mind job with no session", func(t *testing.T, st *store.Store) {
			mustCreateJob(t, st, store.Job{Slug: "mind-2026-10-21", Repo: "kraken", Body: "b"})
			if err := writeMindRun(MindRun{Date: "2026-10-21", SweptTo: 1, Job: "mind-2026-10-21"}); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := useTestStore(t)
			mpeResetMind(t)
			c.setup(t, st)
			for _, sess := range []string{mpeLeadSession, ""} {
				if _, err := ProposeMMItems(st, sess, []MMProposal{mpeProposal("q")}); !errors.Is(err, ErrNotMindLead) {
					t.Errorf("session %q: err = %v, want ErrNotMindLead", sess, err)
				}
			}
			if items := mpeAllItems(t, st); len(items) != 0 {
				t.Fatalf("refused proposes stored %d rows", len(items))
			}
		})
	}
}

// "`session` trimmed" — the lead's session with surrounding whitespace passes.
func TestMPEProposeGateTrimsTheSession(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	mpeCaptureNotify(t)
	mpeMindLead(t, st)
	sum, err := ProposeMMItems(st, "  "+mpeLeadSession+"\n", []MMProposal{mpeProposal("padded session quote")})
	if err != nil {
		t.Fatalf("padded lead session refused: %v", err)
	}
	if sum.Added != 1 {
		t.Fatalf("summary = %+v, want one added", sum)
	}
}

// ---------------------------------------------------------------------------
// C2 — validation: one bad item stores NOTHING
// ---------------------------------------------------------------------------

// "EVERY item is validated before ANY is stored — a failure returns
// `ErrCommitInvalid` (wrapped …) and stores nothing". Each case breaks ONE
// field of the SECOND item of a batch whose first item is valid.
func TestMPEProposeValidationRefusesTheWholeBatch(t *testing.T) {
	cases := map[string]func(p *MMProposal){
		"kind reading":                func(p *MMProposal) { p.Kind = "reading" },
		"kind empty":                  func(p *MMProposal) { p.Kind = "" },
		"pointer artifact":            func(p *MMProposal) { p.Source = CaptureSource{Kind: "artifact", Name: "00-overview.md"} },
		"pointer manual":              func(p *MMProposal) { p.Source = CaptureSource{Kind: "manual"} },
		"pointer dir":                 func(p *MMProposal) { p.Source = CaptureSource{Kind: "dir", Repo: "kraken"} },
		"pointer item":                func(p *MMProposal) { p.Source = CaptureSource{Kind: "item", ItemID: 1} },
		"pointer unknown":             func(p *MMProposal) { p.Source = CaptureSource{Kind: "nightly"} },
		"transcript without session":  func(p *MMProposal) { p.Source = CaptureSource{Kind: "transcript"} },
		"job without slug":            func(p *MMProposal) { p.Source = CaptureSource{Kind: "job"} },
		"feedback id 0":               func(p *MMProposal) { p.Source = CaptureSource{Kind: "feedback"} },
		"question without qid":        func(p *MMProposal) { p.Source = CaptureSource{Kind: "question", Slug: "some-job"} },
		"question without slug":       func(p *MMProposal) { p.Source = CaptureSource{Kind: "question", QID: "Q1"} },
		"quote empty":                 func(p *MMProposal) { p.Quote = "" },
		"quote whitespace":            func(p *MMProposal) { p.Quote = " \n\t " },
		"quote over 32000 runes":      func(p *MMProposal) { p.Quote = strings.Repeat("é", 32001) },
		"rung over 200 runes":         func(p *MMProposal) { p.Rung = strings.Repeat("é", 201) },
		"title over 120 runes":        func(p *MMProposal) { p.Title = strings.Repeat("é", 121) },
		"title empty":                 func(p *MMProposal) { p.Title = "  " },
		"body over 8000 runes":        func(p *MMProposal) { p.Body = strings.Repeat("é", 8001) },
		"body empty":                  func(p *MMProposal) { p.Body = "" },
		"scope unknown":               func(p *MMProposal) { p.Scope = "galaxy" },
		"repo scope, unresolvable":    func(p *MMProposal) { p.Scope, p.Repo = "repo", "no-such-repo-anywhere" },
		"repo scope, no repo":         func(p *MMProposal) { p.Scope, p.Repo = "repo", "" },
		"repo scope, dir climbs out":  func(p *MMProposal) { p.Scope, p.Repo, p.Dir = "repo", "kraken", "../etc" },
		"repo scope, dir is absolute": func(p *MMProposal) { p.Scope, p.Repo, p.Dir = "repo", "kraken", "/etc" },
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			st := useTestStore(t)
			mpeResetMind(t)
			n := mpeCaptureNotify(t)
			mpeMindLead(t, st)
			good := mpeProposal("a perfectly valid first quote")
			bad := mpeProposal("the second, broken one")
			breakIt(&bad)
			_, err := ProposeMMItems(st, mpeLeadSession, []MMProposal{good, bad})
			if !errors.Is(err, ErrCommitInvalid) {
				t.Fatalf("err = %v, want ErrCommitInvalid", err)
			}
			if items := mpeAllItems(t, st); len(items) != 0 {
				t.Fatalf("a refused batch stored %d rows (the valid first item must not land either)", len(items))
			}
			if n.calls != 0 {
				t.Fatalf("a refused batch notified %d times", n.calls)
			}
		})
	}
}

// The caps are inclusive: exactly 32000-rune quote, 200-rune rung and
// 120-rune title are accepted, and the quote is stored verbatim.
func TestMPEProposeAcceptsValuesAtTheirCaps(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	mpeCaptureNotify(t)
	mpeMindLead(t, st)
	p := mpeProposal(strings.Repeat("é", 32000))
	p.Rung = strings.Repeat("r", 200)
	p.Title = strings.Repeat("t", 120)
	p.Body = strings.Repeat("b", 8000)
	sum, err := ProposeMMItems(st, mpeLeadSession, []MMProposal{p})
	if err != nil {
		t.Fatalf("at-cap proposal refused: %v", err)
	}
	if sum.Added != 1 || len(sum.Results) != 1 || sum.Results[0].ID == 0 {
		t.Fatalf("summary = %+v, want one added with an id", sum)
	}
	got, err := st.GetMMItem(sum.Results[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Quote != p.Quote {
		t.Fatalf("stored quote is %d runes, want the 32000-rune quote verbatim", len([]rune(got.Quote)))
	}
}

// ---------------------------------------------------------------------------
// C2 — fold, suppress, cap, notify
// ---------------------------------------------------------------------------

func mpeSeedItem(t *testing.T, st *store.Store, quote, source, state string) int64 {
	t.Helper()
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: source, State: state, Content: "seed " + quote, Quote: quote, Title: "seed", CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mpeOutcomes(sum MMProposeSummary) []string {
	out := []string{}
	for _, r := range sum.Results {
		out = append(out, r.Outcome)
	}
	return out
}

// "FOLD when `FindMMItemsByQuote(quote)` holds any row with `invalidated_at ==
// 0`" — matching is byte-exact, so a ONE-CHARACTER difference is a new item.
func TestMPEProposeOneCharacterDifferenceIsAdded(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	mpeCaptureNotify(t)
	mpeMindLead(t, st)
	mpeSeedItem(t, st, "the board owns card order", "capture", "")
	mpeSeedItem(t, st, "dropped card order quote", "nightly", "")
	sum, err := ProposeMMItems(st, mpeLeadSession, []MMProposal{
		mpeProposal("the board owns card ordex"),
		mpeProposal("dropped card order quotE"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := mpeOutcomes(sum); strings.Join(got, ",") != "added,added" {
		t.Fatalf("outcomes = %v, want [added added]", got)
	}
}

// "SUPPRESS when it holds no such row but holds an invalidated row whose
// `InvalidReason` is `removed` or `contradicted`" — any OTHER reason does not
// suppress.
func TestMPEProposeOtherInvalidReasonsDoNotSuppress(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	mpeCaptureNotify(t)
	mpeMindLead(t, st)
	for _, reason := range []string{"content-changed", "superseded", "artifact-gone", ""} {
		q := "invalidated for " + reason + " quote"
		id := mpeSeedItem(t, st, q, "nightly", "")
		if err := st.InvalidateMMItem(id, time.Now().UnixMilli(), reason, ""); err != nil {
			t.Fatal(err)
		}
		sum, err := ProposeMMItems(st, mpeLeadSession, []MMProposal{mpeProposal(q)})
		if err != nil {
			t.Fatal(err)
		}
		if got := mpeOutcomes(sum); len(got) != 1 || got[0] != "added" {
			t.Errorf("reason %q: outcomes = %v, want [added]", reason, got)
		}
	}
}

// Fold outranks suppress: a quote held by both a live row and a removed one
// folds, with the LIVE row's id; and among two live rows the newest wins.
func TestMPEProposeFoldOutranksSuppressAndPicksNewest(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	mpeCaptureNotify(t)
	mpeMindLead(t, st)
	const q = "the daemon owns the render"
	dead := mpeSeedItem(t, st, q, "nightly", "")
	if err := st.InvalidateMMItem(dead, time.Now().UnixMilli(), "removed", ""); err != nil {
		t.Fatal(err)
	}
	older := mpeSeedItem(t, st, q, "capture", "")
	newer := mpeSeedItem(t, st, q, "nightly", "proposed")

	sum, err := ProposeMMItems(st, mpeLeadSession, []MMProposal{mpeProposal(q)})
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Results) != 1 || sum.Results[0].Outcome != "folded" || sum.Results[0].ID != newer {
		t.Fatalf("results = %+v, want folded onto the newest live row %d (older %d, dead %d)", sum.Results, newer, older, dead)
	}
	if sum.Folded != 1 || sum.Suppressed != 0 || sum.Added != 0 {
		t.Fatalf("counts = %+v, want folded 1 only", sum)
	}
}

// Within ONE call: the second of two same-quote items finds the row the first
// just added and folds onto it.
func TestMPEProposeSameQuoteTwiceInOneBatchFolds(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	mpeCaptureNotify(t)
	mpeMindLead(t, st)
	sum, err := ProposeMMItems(st, mpeLeadSession, []MMProposal{mpeProposal("twice in a batch"), mpeProposal("twice in a batch")})
	if err != nil {
		t.Fatal(err)
	}
	if got := mpeOutcomes(sum); strings.Join(got, ",") != "added,folded" {
		t.Fatalf("outcomes = %v, want [added folded]", got)
	}
	if sum.Results[1].ID != sum.Results[0].ID {
		t.Fatalf("fold id %d, want the just-added row %d", sum.Results[1].ID, sum.Results[0].ID)
	}
	if items := mpeAllItems(t, st); len(items) != 1 {
		t.Fatalf("stored %d rows, want 1", len(items))
	}
}

// "CAP when 20 items have already been `added` in this call → `refused` with
// reason `cap`, no write"; "exactly ONE `mmProposeNotify` call when `Added >
// 0`" with the frozen title and body.
func TestMPEProposeTwentyFirstAddIsRefusedCap(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	n := mpeCaptureNotify(t)
	mpeMindLead(t, st)
	var batch []MMProposal
	for i := 0; i < 21; i++ {
		batch = append(batch, mpeProposal(fmt.Sprintf("distinct quote number %02d", i)))
	}
	sum, err := ProposeMMItems(st, mpeLeadSession, batch)
	if err != nil {
		t.Fatal(err)
	}
	if sum.Added != 20 || sum.Refused != 1 || len(sum.Results) != 21 {
		t.Fatalf("summary = added %d refused %d results %d, want 20/1/21", sum.Added, sum.Refused, len(sum.Results))
	}
	last := sum.Results[20]
	if last.Index != 20 || last.Outcome != "refused" || last.Reason != "cap" || last.ID != 0 {
		t.Fatalf("21st result = %+v, want {Index 20, refused, cap, no id}", last)
	}
	if items := mpeAllItems(t, st); len(items) != 20 {
		t.Fatalf("stored %d rows, want 20", len(items))
	}
	if mmProposeCap != 20 {
		t.Fatalf("mmProposeCap = %d, want 20", mmProposeCap)
	}
	if n.calls != 1 {
		t.Fatalf("notified %d times, want exactly 1", n.calls)
	}
	if n.title != "kraken ◆ mental model" || n.bodies[0] != "20 proposed from last night (0 folded, 0 suppressed) — confirm on the board" {
		t.Fatalf("notification = %q / %q", n.title, n.bodies[0])
	}
}

// "none otherwise": a call whose every item folded or was suppressed sends no
// notification.
func TestMPEProposeZeroAddsSendsNoNotification(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	n := mpeCaptureNotify(t)
	mpeMindLead(t, st)
	mpeSeedItem(t, st, "already known", "capture", "")
	dead := mpeSeedItem(t, st, "already dropped", "nightly", "")
	if err := st.InvalidateMMItem(dead, time.Now().UnixMilli(), "contradicted", ""); err != nil {
		t.Fatal(err)
	}
	sum, err := ProposeMMItems(st, mpeLeadSession, []MMProposal{mpeProposal("already known"), mpeProposal("already dropped")})
	if err != nil {
		t.Fatal(err)
	}
	if got := mpeOutcomes(sum); strings.Join(got, ",") != "folded,suppressed" {
		t.Fatalf("outcomes = %v, want [folded suppressed]", got)
	}
	if n.calls != 0 {
		t.Fatalf("notified %d times with zero adds, want 0", n.calls)
	}
	if items := mpeAllItems(t, st); len(items) != 2 {
		t.Fatalf("stored %d rows, want the 2 seeds only", len(items))
	}
}

// An empty batch from the lead is not a refusal: nothing to do, nothing sent.
func TestMPEProposeEmptyBatch(t *testing.T) {
	st := useTestStore(t)
	mpeResetMind(t)
	n := mpeCaptureNotify(t)
	mpeMindLead(t, st)
	sum, err := ProposeMMItems(st, mpeLeadSession, nil)
	if err != nil {
		t.Fatalf("empty batch: %v", err)
	}
	if sum.Added+sum.Folded+sum.Suppressed+sum.Refused != 0 || n.calls != 0 {
		t.Fatalf("empty batch: summary %+v, %d notifications", sum, n.calls)
	}
}

// ---------------------------------------------------------------------------
// C3 — ValidCaptureSource
// ---------------------------------------------------------------------------

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

// ---------------------------------------------------------------------------
// C4 — CollectMMEvidence
// ---------------------------------------------------------------------------

// "`since <= 0` is an error".
func TestMPECollectMMEvidenceRefusesNonPositiveSince(t *testing.T) {
	st := useTestStore(t)
	for _, since := range []int64{0, -1} {
		if _, err := CollectMMEvidence(st, since, 0); err == nil {
			t.Errorf("CollectMMEvidence(since %d) = nil error, want a refusal", since)
		}
	}
}

// "`MMEvidence` … slices never nil" — an empty window.
func TestMPECollectMMEvidenceEmptyWindowSlicesNotNil(t *testing.T) {
	st := useTestStore(t)
	ev, err := CollectMMEvidence(st, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Jobs == nil || ev.Feedback == nil || ev.Answers == nil {
		t.Fatalf("evidence slices nil: %+v", ev)
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
