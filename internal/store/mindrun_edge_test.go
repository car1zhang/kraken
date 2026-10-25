package store

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// mindrun_edge_test.go — the EDGE dimension of 50-contract.md slice A (schema
// v39: mind_runs, mind_run_blobs, mm_marks, SupersedeMMItem). Written blind
// from the frozen contract: mindruns.go, mmmarks.go and the mm.go/schema.go
// diffs were never read. Refusals, boundaries and "must not change"; the
// round trips are athena-logic's.
//
// Every package-level identifier is prefixed `edgeMR`.

const edgeMRNow int64 = 1_780_000_000_000

func edgeMROpen(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func edgeMRJob(t *testing.T, s *Store, slug string) int64 {
	t.Helper()
	id, err := s.CreateJob(&Job{Slug: slug, Body: "b"})
	if err != nil {
		t.Fatalf("CreateJob(%s): %v", slug, err)
	}
	return id
}

func edgeMREnqueue(t *testing.T, s *Store, r MindRun) int64 {
	t.Helper()
	if r.Trigger == "" {
		r.Trigger = "daily"
	}
	if r.Until == 0 {
		r.Until = r.Since + 1000
	}
	if r.CreatedAt == 0 {
		r.CreatedAt = edgeMRNow
	}
	id, err := s.EnqueueMindRun(r)
	if err != nil {
		t.Fatalf("EnqueueMindRun(%+v): %v", r, err)
	}
	return id
}

func edgeMRGet(t *testing.T, s *Store, id int64) MindRun {
	t.Helper()
	r, err := s.GetMindRun(id)
	if err != nil {
		t.Fatalf("GetMindRun(%d): %v", id, err)
	}
	return r
}

// edgeMRRunning enqueues one run and claims it; the claim must return it.
func edgeMRRunning(t *testing.T, s *Store, r MindRun) int64 {
	t.Helper()
	id := edgeMREnqueue(t, s, r)
	got, err := s.ClaimNextMindRun(edgeMRNow + 1)
	if err != nil || got == nil || got.ID != id {
		t.Fatalf("ClaimNextMindRun = (%+v, %v), want run %d", got, err, id)
	}
	return id
}

func edgeMRItem(t *testing.T, s *Store, quote string) int64 {
	t.Helper()
	id, err := s.AddMMItem(MMItem{Kind: "note", Source: "manual", Title: "title of " + quote, Content: "body of " + quote, Quote: quote, CreatedAt: edgeMRNow})
	if err != nil {
		t.Fatalf("AddMMItem(%q): %v", quote, err)
	}
	return id
}

func edgeMRHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func edgeMRWithSource(m MMMark, src string) MMMark {
	m.SourceJSON = src
	m.CreatedAt += 10
	return m
}

func edgeMRMarkByID(t *testing.T, s *Store, id int64) (MMMark, bool) {
	t.Helper()
	all, err := s.ListMMMarks(0, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if m.ID == id {
			return m, true
		}
	}
	return MMMark{}, false
}

// ---------------------------------------------------------------------------
// EnqueueMindRun
// ---------------------------------------------------------------------------

// "refuses an unknown trigger with an error naming it" — and stores nothing.
func TestEdgeMREnqueueRefusesUnknownTrigger(t *testing.T) {
	s := edgeMROpen(t)
	for _, trig := range []string{"nightly", "Done", "manual", "daily "} {
		_, err := s.EnqueueMindRun(MindRun{Trigger: trig, Since: 1, Until: 2, CreatedAt: edgeMRNow})
		if err == nil {
			t.Errorf("EnqueueMindRun(trigger %q) = nil error, want a refusal", trig)
			continue
		}
		if !strings.Contains(err.Error(), trig) {
			t.Errorf("trigger %q refusal %q does not name it", trig, err)
		}
	}
	if _, err := s.EnqueueMindRun(MindRun{Trigger: "", Since: 1, Until: 2, CreatedAt: edgeMRNow}); err == nil {
		t.Error("EnqueueMindRun(empty trigger) = nil error, want a refusal")
	}
	if runs, err := s.ListMindRuns(0, 0); err != nil || len(runs) != 0 {
		t.Fatalf("refused enqueues left %d rows (err %v)", len(runs), err)
	}
}

// "`Until <= Since` is refused" — both the equal and the inverted window.
func TestEdgeMREnqueueRefusesEmptyOrInvertedWindow(t *testing.T) {
	s := edgeMROpen(t)
	for _, w := range [][2]int64{{5000, 5000}, {5000, 4999}, {5000, 0}} {
		if _, err := s.EnqueueMindRun(MindRun{Trigger: "daily", Since: w[0], Until: w[1], CreatedAt: edgeMRNow}); err == nil {
			t.Errorf("EnqueueMindRun(since %d, until %d) = nil error, want a refusal", w[0], w[1])
		}
	}
	if runs, _ := s.ListMindRuns(0, 0); len(runs) != 0 {
		t.Fatalf("refused enqueues left %d rows", len(runs))
	}
	// one millisecond is a window
	if _, err := s.EnqueueMindRun(MindRun{Trigger: "daily", Since: 5000, Until: 5001, CreatedAt: edgeMRNow}); err != nil {
		t.Fatalf("EnqueueMindRun(5000, 5001) = %v, want accepted", err)
	}
}

// ---------------------------------------------------------------------------
// ClaimNextMindRun
// ---------------------------------------------------------------------------

// "`nil, nil` when none is queued" — an empty store, and a store whose rows
// are all running/done/failed.
func TestEdgeMRClaimNothingQueuedIsNilNil(t *testing.T) {
	s := edgeMROpen(t)
	got, err := s.ClaimNextMindRun(edgeMRNow)
	if err != nil || got != nil {
		t.Fatalf("ClaimNextMindRun on an empty store = (%+v, %v), want (nil, nil)", got, err)
	}
	running := edgeMRRunning(t, s, MindRun{Since: 1})
	done := edgeMRRunning(t, s, MindRun{Since: 2})
	if err := s.FinishMindRun(done, MindRunFinish{At: edgeMRNow + 5}); err != nil {
		t.Fatal(err)
	}
	failed := edgeMREnqueue(t, s, MindRun{Since: 3})
	if err := s.FailMindRun(failed, edgeMRNow+6, "x"); err != nil {
		t.Fatal(err)
	}
	got, err = s.ClaimNextMindRun(edgeMRNow + 10)
	if err != nil || got != nil {
		t.Fatalf("ClaimNextMindRun with no queued row = (%+v, %v), want (nil, nil)", got, err)
	}
	if r := edgeMRGet(t, s, running); r.State != "running" || r.StartedAt != edgeMRNow+1 {
		t.Fatalf("running row after a nil claim = %s started %d, want untouched", r.State, r.StartedAt)
	}
}

// Two sequential claims never return the same row; the third is nil.
func TestEdgeMRSequentialClaimsAreDistinct(t *testing.T) {
	s := edgeMROpen(t)
	a := edgeMREnqueue(t, s, MindRun{Since: 1})
	b := edgeMREnqueue(t, s, MindRun{Since: 2})
	c1, err := s.ClaimNextMindRun(edgeMRNow + 1)
	if err != nil || c1 == nil {
		t.Fatalf("first claim = (%+v, %v)", c1, err)
	}
	c2, err := s.ClaimNextMindRun(edgeMRNow + 2)
	if err != nil || c2 == nil {
		t.Fatalf("second claim = (%+v, %v)", c2, err)
	}
	if c1.ID == c2.ID {
		t.Fatalf("two claims returned the same row %d", c1.ID)
	}
	if got := map[int64]bool{c1.ID: true, c2.ID: true}; !got[a] || !got[b] {
		t.Fatalf("claims returned %d/%d, want %d/%d", c1.ID, c2.ID, a, b)
	}
	if c3, err := s.ClaimNextMindRun(edgeMRNow + 3); err != nil || c3 != nil {
		t.Fatalf("third claim = (%+v, %v), want (nil, nil)", c3, err)
	}
}

// "two callers racing claim two different rows or one claims and one gets
// nil, never both the same row".
func TestEdgeMRRacingClaimsNeverShareARow(t *testing.T) {
	s := edgeMROpen(t)
	edgeMREnqueue(t, s, MindRun{Since: 1})
	edgeMREnqueue(t, s, MindRun{Since: 2})
	const callers = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	seen := map[int64]int{}
	var errs []error
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			r, err := s.ClaimNextMindRun(edgeMRNow + int64(i))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if r != nil {
				seen[r.ID]++
			}
		}(i)
	}
	wg.Wait()
	if len(errs) > 0 {
		t.Fatalf("racing claims errored: %v", errs)
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("row %d claimed %d times, want once", id, n)
		}
	}
	if len(seen) != 2 {
		t.Errorf("racing claims took %d distinct rows, want both queued rows (2)", len(seen))
	}
}

// ---------------------------------------------------------------------------
// FinishMindRun / FailMindRun
// ---------------------------------------------------------------------------

func TestEdgeMRFinishUnknownIDIsErrNotFound(t *testing.T) {
	s := edgeMROpen(t)
	for _, id := range []int64{0, -1, 424242} {
		if err := s.FinishMindRun(id, MindRunFinish{At: edgeMRNow}); !errors.Is(err, ErrNotFound) {
			t.Errorf("FinishMindRun(%d) = %v, want ErrNotFound", id, err)
		}
	}
}

// "refuses a row not `running` with an error naming its state" — queued,
// done and failed rows, none of which changes.
func TestEdgeMRFinishRefusesRowNotRunning(t *testing.T) {
	s := edgeMROpen(t)
	queued := edgeMREnqueue(t, s, MindRun{Since: 1})

	// older by created_at, so the claim takes it and `queued` stays queued
	// (edgeMRRunning asserts the claim returned this row)
	done := edgeMRRunning(t, s, MindRun{Since: 0, CreatedAt: edgeMRNow - 10})
	if err := s.FinishMindRun(done, MindRunFinish{At: edgeMRNow + 2, Report: "first"}); err != nil {
		t.Fatal(err)
	}
	failed := edgeMREnqueue(t, s, MindRun{Since: 2})
	if err := s.FailMindRun(failed, edgeMRNow+3, "boom"); err != nil {
		t.Fatal(err)
	}

	for id, state := range map[int64]string{queued: "queued", done: "done", failed: "failed"} {
		before := edgeMRGet(t, s, id)
		err := s.FinishMindRun(id, MindRunFinish{At: edgeMRNow + 99, Report: "second", Counts: MindRunCounts{Added: 7}})
		if err == nil {
			t.Errorf("FinishMindRun(%s row) = nil error, want a refusal", state)
			continue
		}
		if !strings.Contains(err.Error(), state) {
			t.Errorf("FinishMindRun(%s row) refusal %q does not name the state", state, err)
		}
		if after := edgeMRGet(t, s, id); after != before {
			t.Errorf("refused finish changed the %s row:\nbefore %+v\nafter  %+v", state, before, after)
		}
	}
}

func TestEdgeMRFailUnknownIDIsErrNotFound(t *testing.T) {
	s := edgeMROpen(t)
	if err := s.FailMindRun(31337, edgeMRNow, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("FailMindRun(unknown) = %v, want ErrNotFound", err)
	}
}

// "accepted from `queued` or `running`"; the note is one-lined and capped at
// 2000 runes.
func TestEdgeMRFailFromQueuedAndRunningOneLinesAndCapsNote(t *testing.T) {
	s := edgeMROpen(t)
	queued := edgeMREnqueue(t, s, MindRun{Since: 1, CreatedAt: edgeMRNow + 50})
	running := edgeMRRunning(t, s, MindRun{Since: 2, CreatedAt: edgeMRNow - 50})

	long := "line one\nline two\r\n" + strings.Repeat("é", 5000)
	for _, id := range []int64{queued, running} {
		if err := s.FailMindRun(id, edgeMRNow+7, long); err != nil {
			t.Fatalf("FailMindRun(%d) = %v, want accepted", id, err)
		}
		r := edgeMRGet(t, s, id)
		if r.State != "failed" || r.FinishedAt != edgeMRNow+7 {
			t.Errorf("row %d = state %s finished %d, want failed/%d", id, r.State, r.FinishedAt, edgeMRNow+7)
		}
		if strings.ContainsAny(r.Note, "\n\r") {
			t.Errorf("row %d note is not one line: %q", id, edgeMRHead(r.Note, 40))
		}
		if n := utf8.RuneCountInString(r.Note); n > 2000 {
			t.Errorf("row %d note is %d runes, want at most 2000", id, n)
		}
		if !strings.HasPrefix(r.Note, "line one") {
			t.Errorf("row %d note %q lost its head", id, edgeMRHead(r.Note, 20))
		}
	}
}

// A8: "`FailMindRun` from `done` or `failed` is refused with an error naming
// the state" — and the row does not change.
func TestEdgeMRFailRefusesDoneAndFailedRows(t *testing.T) {
	s := edgeMROpen(t)
	done := edgeMRRunning(t, s, MindRun{Since: 1, CreatedAt: edgeMRNow - 10})
	if err := s.FinishMindRun(done, MindRunFinish{At: edgeMRNow + 1, Report: "r"}); err != nil {
		t.Fatal(err)
	}
	failed := edgeMREnqueue(t, s, MindRun{Since: 2})
	if err := s.FailMindRun(failed, edgeMRNow+2, "first failure"); err != nil {
		t.Fatal(err)
	}
	for id, state := range map[int64]string{done: "done", failed: "failed"} {
		before := edgeMRGet(t, s, id)
		err := s.FailMindRun(id, edgeMRNow+99, "second failure")
		if err == nil {
			t.Errorf("FailMindRun(%s row) = nil error, want a refusal", state)
			continue
		}
		if !strings.Contains(err.Error(), state) {
			t.Errorf("FailMindRun(%s row) refusal %q does not name the state", state, err)
		}
		if after := edgeMRGet(t, s, id); after != before {
			t.Errorf("refused fail changed the %s row:\nbefore %+v\nafter  %+v", state, before, after)
		}
	}
}

// ---------------------------------------------------------------------------
// RequeueRunningMindRuns
// ---------------------------------------------------------------------------

// Only `running` rows move; queued/done/failed rows keep state and note.
func TestEdgeMRRequeueTouchesOnlyRunningRows(t *testing.T) {
	s := edgeMROpen(t)
	running := edgeMRRunning(t, s, MindRun{Since: 1, CreatedAt: edgeMRNow - 100})
	done := edgeMRRunning(t, s, MindRun{Since: 2, CreatedAt: edgeMRNow - 90})
	if err := s.FinishMindRun(done, MindRunFinish{At: edgeMRNow + 1}); err != nil {
		t.Fatal(err)
	}
	failed := edgeMREnqueue(t, s, MindRun{Since: 3, CreatedAt: edgeMRNow - 80})
	if err := s.FailMindRun(failed, edgeMRNow+2, "old failure"); err != nil {
		t.Fatal(err)
	}
	queued := edgeMREnqueue(t, s, MindRun{Since: 4, CreatedAt: edgeMRNow - 70})

	before := map[int64]MindRun{}
	for _, id := range []int64{done, failed, queued} {
		before[id] = edgeMRGet(t, s, id)
	}
	n, err := s.RequeueRunningMindRuns("serve restarted")
	if err != nil || n != 1 {
		t.Fatalf("RequeueRunningMindRuns = (%d, %v), want (1, nil)", n, err)
	}
	r := edgeMRGet(t, s, running)
	if r.State != "queued" || r.StartedAt != 0 || !strings.Contains(r.Note, "serve restarted") {
		t.Fatalf("requeued row = state %s started %d note %q, want queued/0/…serve restarted", r.State, r.StartedAt, r.Note)
	}
	for id, b := range before {
		if a := edgeMRGet(t, s, id); a != b {
			t.Errorf("requeue changed a %s row:\nbefore %+v\nafter  %+v", b.State, b, a)
		}
	}
	// nothing running any more: a second pass moves nothing
	if n, err := s.RequeueRunningMindRuns("again"); err != nil || n != 0 {
		t.Fatalf("second RequeueRunningMindRuns = (%d, %v), want (0, nil)", n, err)
	}
}

// ---------------------------------------------------------------------------
// LastMindRunUntil
// ---------------------------------------------------------------------------

// "a failed run does not advance the floor"; 0 when none; scoped by job and
// trigger.
func TestEdgeMRLastUntilIgnoresFailedAndOtherScopes(t *testing.T) {
	s := edgeMROpen(t)
	j := edgeMRJob(t, s, "edge-mr-floor")
	other := edgeMRJob(t, s, "edge-mr-floor-other")

	if got, err := s.LastMindRunUntil(j, "done"); err != nil || got != 0 {
		t.Fatalf("LastMindRunUntil with no rows = (%d, %v), want (0, nil)", got, err)
	}
	f := edgeMREnqueue(t, s, MindRun{JobID: j, Trigger: "done", Since: 100, Until: 9000})
	if err := s.FailMindRun(f, edgeMRNow, "x"); err != nil {
		t.Fatal(err)
	}
	if got, err := s.LastMindRunUntil(j, "done"); err != nil || got != 0 {
		t.Fatalf("LastMindRunUntil with only a failed row = (%d, %v), want (0, nil)", got, err)
	}
	edgeMREnqueue(t, s, MindRun{JobID: j, Trigger: "done", Since: 100, Until: 500})
	edgeMREnqueue(t, s, MindRun{JobID: other, Trigger: "done", Since: 100, Until: 7000})
	edgeMREnqueue(t, s, MindRun{JobID: j, Trigger: "replay", Since: 100, Until: 8000})
	edgeMREnqueue(t, s, MindRun{JobID: 0, Trigger: "daily", Since: 100, Until: 6000})
	if got, err := s.LastMindRunUntil(j, "done"); err != nil || got != 500 {
		t.Fatalf("LastMindRunUntil(job, done) = (%d, %v), want (500, nil) — failed, other-job and other-trigger rows ignored", got, err)
	}
}

// ---------------------------------------------------------------------------
// Blobs
// ---------------------------------------------------------------------------

func TestEdgeMRBlobRefusals(t *testing.T) {
	s := edgeMROpen(t)
	if err := s.SetMindRunBlobs(777, []byte{1, 2}, "{}"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMindRunBlobs(unknown run) = %v, want ErrNotFound", err)
	}
	if _, err := s.GetMindRunBlobs(777); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMindRunBlobs(unknown run) = %v, want ErrNotFound", err)
	}
	id := edgeMREnqueue(t, s, MindRun{Since: 1})
	if err := s.SetMindRunAnswer(id, `{"ops":[]}`); !errors.Is(err, ErrNotFound) {
		t.Errorf("SetMindRunAnswer before SetMindRunBlobs = %v, want ErrNotFound", err)
	}
	if _, err := s.GetMindRunBlobs(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetMindRunBlobs on a run with no blob row = %v, want ErrNotFound (the refused answer wrote nothing)", err)
	}
}

// A pruned row: `BundleGz == nil`, `PrunedAt > 0`; manifest, answer and the
// mind_runs row untouched; already-pruned rows skipped; `created_at < before`
// is strict.
func TestEdgeMRPruneKeepsRowManifestAndAnswer(t *testing.T) {
	s := edgeMROpen(t)
	old := edgeMRRunning(t, s, MindRun{Since: 1, CreatedAt: edgeMRNow - 1000})
	if err := s.SetMindRunBlobs(old, []byte("gzip-bytes-old"), `{"sessions":["s1"]}`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetMindRunAnswer(old, `{"ops":[],"excluded":[],"coverage":{}}`); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishMindRun(old, MindRunFinish{At: edgeMRNow - 900, Report: "the report", PromptSHA: "abc", Model: "m", InTokens: 3, OutTokens: 4, BundleBytes: 14}); err != nil {
		t.Fatal(err)
	}
	edge := edgeMREnqueue(t, s, MindRun{Since: 2, CreatedAt: edgeMRNow})
	if err := s.SetMindRunBlobs(edge, []byte("gzip-bytes-edge"), `{"sessions":["s2"]}`); err != nil {
		t.Fatal(err)
	}
	runBefore := edgeMRGet(t, s, old)
	edgeBefore := edgeMRGet(t, s, edge)

	const at = edgeMRNow + 5000
	n, err := s.PruneMindRunBundles(edgeMRNow, at) // before == edge's created_at: strict, not pruned
	if err != nil || n != 1 {
		t.Fatalf("PruneMindRunBundles = (%d, %v), want (1, nil)", n, err)
	}
	b, err := s.GetMindRunBlobs(old)
	if err != nil {
		t.Fatalf("GetMindRunBlobs(pruned) = %v, want the row", err)
	}
	if b.BundleGz != nil || b.PrunedAt != at {
		t.Fatalf("pruned blobs = bundle %v pruned_at %d, want nil/%d", b.BundleGz, b.PrunedAt, at)
	}
	if b.Manifest != `{"sessions":["s1"]}` || b.Answer != `{"ops":[],"excluded":[],"coverage":{}}` {
		t.Fatalf("prune touched manifest/answer: %q / %q", b.Manifest, b.Answer)
	}
	if after := edgeMRGet(t, s, old); after != runBefore {
		t.Fatalf("prune changed the mind_runs row:\nbefore %+v\nafter  %+v", runBefore, after)
	}
	kept, err := s.GetMindRunBlobs(edge)
	if err != nil || string(kept.BundleGz) != "gzip-bytes-edge" || kept.PrunedAt != 0 {
		t.Fatalf("run created at `before` = (%+v, %v), want untouched", kept, err)
	}
	if after := edgeMRGet(t, s, edge); after != edgeBefore {
		t.Fatalf("prune changed an unpruned run row")
	}

	// already pruned: skipped, pruned_at keeps its first stamp
	n, err = s.PruneMindRunBundles(edgeMRNow, at+1)
	if err != nil || n != 0 {
		t.Fatalf("second prune = (%d, %v), want (0, nil)", n, err)
	}
	if b2, _ := s.GetMindRunBlobs(old); b2.PrunedAt != at {
		t.Fatalf("second prune restamped pruned_at %d → %d", at, b2.PrunedAt)
	}
}

// ---------------------------------------------------------------------------
// mm_marks
// ---------------------------------------------------------------------------

func TestEdgeMRAddMarkRefusesEmptyText(t *testing.T) {
	s := edgeMROpen(t)
	if _, _, err := s.AddMMMark(MMMark{Text: "", SourceJSON: `{"kind":"manual"}`, CreatedAt: edgeMRNow}); err == nil {
		t.Fatal("AddMMMark(empty text) = nil error, want a refusal")
	}
	if all, _ := s.ListMMMarks(0, false); len(all) != 0 {
		t.Fatalf("refused mark stored %d rows", len(all))
	}
}

// Fold only within 60 000 ms and only when job, text and source are all
// byte-equal; a consumed row never folds.
func TestEdgeMRAddMarkFoldBoundaries(t *testing.T) {
	s := edgeMROpen(t)
	j := edgeMRJob(t, s, "edge-mr-marks")
	other := edgeMRJob(t, s, "edge-mr-marks-other")
	const src = `{"kind":"artifact","slug":"edge-mr-marks","name":"00-overview.md"}`
	base := MMMark{JobID: j, Slug: "edge-mr-marks", Text: "the board owns order", SourceJSON: src, CreatedAt: edgeMRNow}
	first, folded, err := s.AddMMMark(base)
	if err != nil || folded {
		t.Fatalf("first AddMMMark = (%d, %v, %v), want an insert", first, folded, err)
	}

	in := base
	in.CreatedAt = edgeMRNow + 59_999
	if id, folded, err := s.AddMMMark(in); err != nil || !folded || id != first {
		t.Fatalf("same mark 59 999 ms later = (%d, %v, %v), want folded onto %d", id, folded, err, first)
	}

	for name, m := range map[string]MMMark{
		"past the window": func() MMMark { m := base; m.CreatedAt = edgeMRNow + 60_001; return m }(),
		"other job":       func() MMMark { m := base; m.JobID = other; m.Slug = "edge-mr-marks-other"; m.CreatedAt += 10; return m }(),
		"no job":          func() MMMark { m := base; m.JobID = 0; m.Slug = ""; m.CreatedAt += 10; return m }(),
		"text +space":     func() MMMark { m := base; m.Text += " "; m.CreatedAt += 10; return m }(),
		"text case":       func() MMMark { m := base; m.Text = "The board owns order"; m.CreatedAt += 10; return m }(),
		"source differs":  edgeMRWithSource(base, `{"kind":"artifact","slug":"edge-mr-marks","name":"01-other.md"}`),
		"source spacing":  edgeMRWithSource(base, `{"kind": "artifact","slug":"edge-mr-marks","name":"00-overview.md"}`),
	} {
		id, folded, err := s.AddMMMark(m)
		if err != nil {
			t.Errorf("%s: AddMMMark = %v", name, err)
			continue
		}
		if folded || id == first {
			t.Errorf("%s: folded onto %d (folded=%v), want a fresh row", name, id, folded)
		}
	}

	// a consumed row never folds, even byte-equal and inside the window
	c, _, err := s.AddMMMark(MMMark{JobID: j, Text: "consumed text", SourceJSON: src, CreatedAt: edgeMRNow})
	if err != nil {
		t.Fatal(err)
	}
	run := edgeMREnqueue(t, s, MindRun{JobID: j, Trigger: "done", Since: 1})
	if err := s.ConsumeMMMarks([]int64{c}, run, edgeMRNow+1); err != nil {
		t.Fatal(err)
	}
	id, folded, err := s.AddMMMark(MMMark{JobID: j, Text: "consumed text", SourceJSON: src, CreatedAt: edgeMRNow + 2})
	if err != nil || folded || id == c {
		t.Fatalf("mark equal to a CONSUMED row = (%d, %v, %v), want a fresh pending row", id, folded, err)
	}
}

// A9: "the fold window is inclusive: |created_at − m.CreatedAt| <= 60000" —
// exactly 60 000 ms after, and exactly 60 000 ms before, both fold.
func TestEdgeMRAddMarkFoldWindowIsInclusive(t *testing.T) {
	s := edgeMROpen(t)
	base := MMMark{Text: "inclusive edge", SourceJSON: `{"kind":"manual"}`, CreatedAt: edgeMRNow}
	first, folded, err := s.AddMMMark(base)
	if err != nil || folded {
		t.Fatalf("first AddMMMark = (%d, %v, %v)", first, folded, err)
	}
	for _, d := range []int64{60_000, -60_000} {
		m := base
		m.CreatedAt = edgeMRNow + d
		id, folded, err := s.AddMMMark(m)
		if err != nil || !folded || id != first {
			t.Errorf("same mark at %+d ms = (%d, %v, %v), want folded onto %d", d, id, folded, err, first)
		}
	}
	if all, _ := s.ListMMMarks(0, false); len(all) != 1 {
		t.Fatalf("inclusive folds stored %d rows, want 1", len(all))
	}
}

// "an already-consumed row is left as it was (first run wins); unknown ids
// are ignored".
func TestEdgeMRConsumeFirstRunWinsAndIgnoresUnknown(t *testing.T) {
	s := edgeMROpen(t)
	a, _, err := s.AddMMMark(MMMark{Text: "a", SourceJSON: `{"kind":"manual"}`, CreatedAt: edgeMRNow})
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := s.AddMMMark(MMMark{Text: "b", SourceJSON: `{"kind":"manual"}`, CreatedAt: edgeMRNow})
	if err != nil {
		t.Fatal(err)
	}
	r1 := edgeMREnqueue(t, s, MindRun{Since: 1})
	r2 := edgeMREnqueue(t, s, MindRun{Since: 2})
	if err := s.ConsumeMMMarks([]int64{a}, r1, edgeMRNow+100); err != nil {
		t.Fatal(err)
	}
	if err := s.ConsumeMMMarks([]int64{a, b, 987654, -3}, r2, edgeMRNow+200); err != nil {
		t.Fatalf("ConsumeMMMarks with unknown ids = %v, want them ignored", err)
	}
	ma, _ := edgeMRMarkByID(t, s, a)
	if ma.ConsumedRun != r1 || ma.ConsumedAt != edgeMRNow+100 {
		t.Errorf("mark a = run %d at %d, want first run %d at %d", ma.ConsumedRun, ma.ConsumedAt, r1, edgeMRNow+100)
	}
	mb, _ := edgeMRMarkByID(t, s, b)
	if mb.ConsumedRun != r2 || mb.ConsumedAt != edgeMRNow+200 {
		t.Errorf("mark b = run %d at %d, want %d at %d", mb.ConsumedRun, mb.ConsumedAt, r2, edgeMRNow+200)
	}
	if all, _ := s.ListMMMarks(0, false); len(all) != 2 {
		t.Errorf("unknown ids created rows: %d marks, want 2", len(all))
	}
	if err := s.ConsumeMMMarks(nil, r2, edgeMRNow+300); err != nil {
		t.Errorf("ConsumeMMMarks(nil) = %v, want a no-op", err)
	}
}

func TestEdgeMRRemoveMarkRefusals(t *testing.T) {
	s := edgeMROpen(t)
	if err := s.RemoveMMMark(5555); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveMMMark(unknown) = %v, want ErrNotFound", err)
	}
	c, _, err := s.AddMMMark(MMMark{Text: "consumed", SourceJSON: `{"kind":"manual"}`, CreatedAt: edgeMRNow})
	if err != nil {
		t.Fatal(err)
	}
	run := edgeMREnqueue(t, s, MindRun{Since: 1})
	if err := s.ConsumeMMMarks([]int64{c}, run, edgeMRNow+1); err != nil {
		t.Fatal(err)
	}
	err = s.RemoveMMMark(c)
	if !errors.Is(err, ErrMMMarkConsumed) {
		t.Errorf("RemoveMMMark(consumed) = %v, want ErrMMMarkConsumed", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Errorf("consumed refusal also matches ErrNotFound: %v", err)
	}
	if _, ok := edgeMRMarkByID(t, s, c); !ok {
		t.Error("refused remove deleted the consumed mark")
	}
	p, _, err := s.AddMMMark(MMMark{Text: "pending", SourceJSON: `{"kind":"manual"}`, CreatedAt: edgeMRNow})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveMMMark(p); err != nil {
		t.Fatalf("RemoveMMMark(pending) = %v", err)
	}
	if err := s.RemoveMMMark(p); !errors.Is(err, ErrNotFound) {
		t.Errorf("RemoveMMMark twice = %v, want ErrNotFound", err)
	}
}

// ---------------------------------------------------------------------------
// SupersedeMMItem
// ---------------------------------------------------------------------------

func TestEdgeMRSupersedeRefusals(t *testing.T) {
	s := edgeMROpen(t)
	live := edgeMRItem(t, s, "live quote")
	repl := edgeMRItem(t, s, "replacement quote")
	dead := edgeMRItem(t, s, "dead quote")
	if err := s.InvalidateMMItem(dead, edgeMRNow+1, "removed", "dropped by hand"); err != nil {
		t.Fatal(err)
	}
	deadBefore, err := s.GetMMItem(dead)
	if err != nil {
		t.Fatal(err)
	}
	liveBefore, _ := s.GetMMItem(live)

	if err := s.SupersedeMMItem(dead, repl, edgeMRNow+2, "run 1: q"); !errors.Is(err, ErrMMItemInvalidated) {
		t.Errorf("SupersedeMMItem(dead) = %v, want ErrMMItemInvalidated", err)
	}
	if after, _ := s.GetMMItem(dead); after.InvalidReason != deadBefore.InvalidReason || after.InvalidDetail != deadBefore.InvalidDetail || after.InvalidatedAt != deadBefore.InvalidatedAt || after.SupersededBy != 0 {
		t.Errorf("refused supersede rewrote the dead row: %+v", after)
	}
	if err := s.SupersedeMMItem(999_999, repl, edgeMRNow+2, "d"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SupersedeMMItem(unknown old) = %v, want ErrNotFound", err)
	}
	if err := s.SupersedeMMItem(live, 999_999, edgeMRNow+2, "d"); !errors.Is(err, ErrNotFound) {
		t.Errorf("SupersedeMMItem(unknown new) = %v, want ErrNotFound", err)
	}
	if after, _ := s.GetMMItem(live); after.InvalidatedAt != 0 || after.SupersededBy != 0 || after.InvalidReason != liveBefore.InvalidReason {
		t.Errorf("supersede onto an unknown new id still invalidated the old row: %+v", after)
	}
}

// "never edits title, content or quote of either row" — and a second
// supersede of the now-dead row is ErrMMItemInvalidated.
func TestEdgeMRSupersedeNeverEditsText(t *testing.T) {
	s := edgeMROpen(t)
	old := edgeMRItem(t, s, "old quote")
	repl := edgeMRItem(t, s, "new quote")
	oldBefore, _ := s.GetMMItem(old)
	replBefore, _ := s.GetMMItem(repl)
	if err := s.SupersedeMMItem(old, repl, edgeMRNow+10, "run 3: new quote"); err != nil {
		t.Fatalf("SupersedeMMItem = %v", err)
	}
	oldAfter, _ := s.GetMMItem(old)
	replAfter, _ := s.GetMMItem(repl)
	if oldAfter.Title != oldBefore.Title || oldAfter.Content != oldBefore.Content || oldAfter.Quote != oldBefore.Quote {
		t.Errorf("old row text edited: %+v", oldAfter)
	}
	if replAfter.Title != replBefore.Title || replAfter.Content != replBefore.Content || replAfter.Quote != replBefore.Quote || replAfter.InvalidatedAt != 0 {
		t.Errorf("new row edited or invalidated: %+v", replAfter)
	}
	if oldAfter.InvalidReason != "superseded" || oldAfter.SupersededBy != repl || oldAfter.InvalidatedAt != edgeMRNow+10 {
		t.Errorf("old row = reason %q superseded_by %d at %d", oldAfter.InvalidReason, oldAfter.SupersededBy, oldAfter.InvalidatedAt)
	}
	other := edgeMRItem(t, s, "third quote")
	if err := s.SupersedeMMItem(old, other, edgeMRNow+20, "again"); !errors.Is(err, ErrMMItemInvalidated) {
		t.Errorf("second supersede of the dead row = %v, want ErrMMItemInvalidated", err)
	}
	if again, _ := s.GetMMItem(old); again.SupersededBy != repl {
		t.Errorf("refused re-supersede moved superseded_by %d → %d", repl, again.SupersededBy)
	}
}
