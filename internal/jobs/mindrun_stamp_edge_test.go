package jobs

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// mindrun_stamp_edge_test.go — the EDGE dimension of contract-mind.md: a
// mind run stamps prompt_sha, model and bundle_bytes BEFORE its launch, so a
// failed row still says what it ran under. Written blind from the frozen
// contract: mindrun.go's implementation was never read. Fixtures (slugs,
// bodies, error strings, stamp values) are invented. The launcher is always
// the edgeMRLaunch seam; no claude ever runs.

// athenaEdgeStampClaimedOperatorRun files an operator-authored done job whose
// spec makes the bundle non-empty, enqueues its done run and claims it.
func athenaEdgeStampClaimedOperatorRun(t *testing.T, st *store.Store, slug string) store.MindRun {
	t.Helper()
	now := time.Now().UnixMilli()
	j := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", Body: "teach the gizmo to fold paper cranes", Author: "operator", State: "done", CreatedAt: now - 5000, UpdatedAt: now - 5000})
	queued := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: now - 5000, Until: now})
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim = (%+v, %v), want run %d", claimed, err, queued.ID)
	}
	return *claimed
}

// athenaEdgeStampWantStamps asserts the three launch stamps on a row.
func athenaEdgeStampWantStamps(t *testing.T, r store.MindRun) {
	t.Helper()
	_, sha, err := MindPrompt()
	if err != nil || sha == "" {
		t.Fatalf("premise: MindPrompt sha = %q, err %v", sha, err)
	}
	if r.PromptSHA != sha {
		t.Errorf("PromptSHA = %q, want MindPrompt's %q", r.PromptSHA, sha)
	}
	if r.Model != core.DefaultModel {
		t.Errorf("Model = %q, want %q", r.Model, core.DefaultModel)
	}
	if r.BundleBytes <= 0 {
		t.Errorf("BundleBytes = %d, want > 0", r.BundleBytes)
	}
}

// --- the store verb ---

func TestAthenaEdgeStampLaunchRefusesQueued(t *testing.T) {
	st := edgeMREnv(t)
	now := time.Now().UnixMilli()
	q := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: now - 60000, Until: now})
	err := st.StampMindRunLaunch(q.ID, "sha-of-nothing-1", "model-zzz", 4242)
	if err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("stamp on a queued row = %v, want an error naming queued", err)
	}
	r := edgeMRGetRun(t, st, q.ID)
	if r.State != "queued" || r.PromptSHA != "" || r.Model != "" || r.BundleBytes != 0 {
		t.Fatalf("a refused stamp changed the queued row: %+v", r)
	}
}

func TestAthenaEdgeStampLaunchRefusesDone(t *testing.T) {
	st := edgeMREnv(t)
	now := time.Now().UnixMilli()
	q := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: now - 60000, Until: now})
	if _, err := st.ClaimNextMindRun(now); err != nil {
		t.Fatal(err)
	}
	fin := store.MindRunFinish{At: now + 10, Report: "invented report", PromptSHA: "sha-finished-7", Model: "model-finished", InTokens: 11, OutTokens: 22, BundleBytes: 333}
	if err := st.FinishMindRun(q.ID, fin); err != nil {
		t.Fatalf("premise: FinishMindRun: %v", err)
	}
	err := st.StampMindRunLaunch(q.ID, "sha-late", "model-late", 9)
	if err == nil || !strings.Contains(err.Error(), "done") {
		t.Fatalf("stamp on a done row = %v, want an error naming done", err)
	}
	r := edgeMRGetRun(t, st, q.ID)
	if r.State != "done" || r.PromptSHA != "sha-finished-7" || r.Model != "model-finished" || r.BundleBytes != 333 {
		t.Fatalf("a refused stamp changed the done row: %+v", r)
	}
}

func TestAthenaEdgeStampLaunchRefusesFailed(t *testing.T) {
	st := edgeMREnv(t)
	now := time.Now().UnixMilli()
	q := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: now - 60000, Until: now})
	if _, err := st.ClaimNextMindRun(now); err != nil {
		t.Fatal(err)
	}
	if err := st.FailMindRun(q.ID, now+5, "invented breakage"); err != nil {
		t.Fatalf("premise: FailMindRun: %v", err)
	}
	err := st.StampMindRunLaunch(q.ID, "sha-after-fail", "model-after-fail", 17)
	if err == nil || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("stamp on a failed row = %v, want an error naming failed", err)
	}
	r := edgeMRGetRun(t, st, q.ID)
	if r.State != "failed" || r.Note != "invented breakage" || r.PromptSHA != "" || r.Model != "" || r.BundleBytes != 0 {
		t.Fatalf("a refused stamp changed the failed row: %+v", r)
	}
}

func TestAthenaEdgeStampLaunchRefusesMissingRow(t *testing.T) {
	st := edgeMREnv(t)
	if err := st.StampMindRunLaunch(987654, "sha-ghost", "model-ghost", 1); err == nil {
		t.Fatal("stamp on a run that does not exist = nil error")
	}
}

// A running row takes exactly the three columns and nothing else; a second
// stamp on the same running row overwrites the first.
func TestAthenaEdgeStampLaunchRunningTouchesOnlyThreeColumns(t *testing.T) {
	st := edgeMREnv(t)
	now := time.Now().UnixMilli()
	q := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: now - 60000, Until: now})
	if _, err := st.ClaimNextMindRun(now); err != nil {
		t.Fatal(err)
	}
	before := edgeMRGetRun(t, st, q.ID)
	if err := st.StampMindRunLaunch(q.ID, "sha-first-pass", "model-first", 1111); err != nil {
		t.Fatalf("stamp on a running row: %v", err)
	}
	if err := st.StampMindRunLaunch(q.ID, "sha-second-pass", "model-second", 2222); err != nil {
		t.Fatalf("second stamp on a running row: %v", err)
	}
	r := edgeMRGetRun(t, st, q.ID)
	if r.PromptSHA != "sha-second-pass" || r.Model != "model-second" || r.BundleBytes != 2222 {
		t.Fatalf("stamps = (%q, %q, %d), want the second pass", r.PromptSHA, r.Model, r.BundleBytes)
	}
	want := before
	want.PromptSHA, want.Model, want.BundleBytes = r.PromptSHA, r.Model, r.BundleBytes
	if r != want {
		t.Fatalf("stamp touched other columns:\n got %+v\nwant %+v", r, want)
	}
	if r.State != "running" || r.Note != "" || r.FinishedAt != 0 || r.Report != "" || r.InTokens != 0 || r.OutTokens != 0 {
		t.Fatalf("running row after stamp: %+v", r)
	}
}

// --- RunMindRun ---

// The stamp is on the row BEFORE the launcher runs: the launcher itself reads
// the row and finds the three values on a still-running run.
func TestAthenaEdgeStampVisibleInsideLauncher(t *testing.T) {
	st := edgeMREnv(t)
	run := athenaEdgeStampClaimedOperatorRun(t, st, "crane-folder")
	var seen store.MindRun
	var seenErr error
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		seen, seenErr = st.GetMindRun(run.ID)
		return edgeMREmptyAnswer, "", nil
	})
	if err := RunMindRun(st, run); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	if seenErr != nil {
		t.Fatalf("GetMindRun from inside the launcher: %v", seenErr)
	}
	if seen.State != "running" {
		t.Fatalf("state seen by the launcher = %q, want running", seen.State)
	}
	athenaEdgeStampWantStamps(t, seen)
	final := edgeMRGetRun(t, st, run.ID)
	if final.State != "done" {
		t.Fatalf("final state = %q, want done", final.State)
	}
	// A successful run's finish values are unchanged by the stamp: the
	// finish records the same sha, model and bundle size the stamp wrote.
	athenaEdgeStampWantStamps(t, final)
	if final.PromptSHA != seen.PromptSHA || final.Model != seen.Model || final.BundleBytes != seen.BundleBytes {
		t.Fatalf("finish (%q, %q, %d) disagrees with the launch stamp (%q, %q, %d)",
			final.PromptSHA, final.Model, final.BundleBytes, seen.PromptSHA, seen.Model, seen.BundleBytes)
	}
}

func TestAthenaEdgeStampSurvivesLaunchError(t *testing.T) {
	st := edgeMREnv(t)
	run := athenaEdgeStampClaimedOperatorRun(t, st, "kiln-heater")
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return "", "", errors.New("invented launcher meltdown 7731")
	})
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun with a failing launcher = nil error")
	}
	if n := l.calls.Load(); n != 1 {
		t.Fatalf("launcher called %d times, want 1", n)
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "failed" || !strings.Contains(r.Note, "invented launcher meltdown 7731") {
		t.Fatalf("run = state %q note %q, want failed carrying the launcher error", r.State, r.Note)
	}
	athenaEdgeStampWantStamps(t, r)
}

func TestAthenaEdgeStampSurvivesTimeout(t *testing.T) {
	st := edgeMREnv(t)
	prev := MindRunTimeout
	MindRunTimeout = 100 * time.Millisecond
	t.Cleanup(func() { MindRunTimeout = prev })
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-time.After(10 * time.Second):
			return edgeMREmptyAnswer, "", nil
		}
	})
	run := edgeMRDaily(t, st)
	edgeMRPendingMark(t, st, "an invented mark about the teapot, so the daily bundle is not empty")
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun past MindRunTimeout = nil error")
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "failed" || !strings.HasPrefix(r.Note, "timed out") {
		t.Fatalf("run = state %q note %q, want failed with a timed-out note", r.State, r.Note)
	}
	athenaEdgeStampWantStamps(t, r)
}

func TestAthenaEdgeStampSurvivesGarbageAnswer(t *testing.T) {
	st := edgeMREnv(t)
	run := athenaEdgeStampClaimedOperatorRun(t, st, "lamp-tuner")
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return "~~ wobble {{ not an answer ]] ~~", "", nil
	})
	if err := RunMindRun(st, run); err == nil {
		t.Fatal("RunMindRun with a garbage answer = nil error")
	}
	r := edgeMRGetRun(t, st, run.ID)
	if r.State != "failed" {
		t.Fatalf("run state = %q, want failed", r.State)
	}
	athenaEdgeStampWantStamps(t, r)
	if r.OutTokens != 0 || r.Report != "" {
		t.Fatalf("a parse failure recorded finish-only values: out %d report %q", r.OutTokens, r.Report)
	}
}

// An empty bundle never launches and so never stamps a model; its sha is
// still recorded (the unchanged empty-bundle behaviour).
func TestAthenaEdgeStampEmptyBundleKeepsModelEmpty(t *testing.T) {
	st := edgeMREnv(t)
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return edgeMREmptyAnswer, "", nil
	})
	now := time.Now().UnixMilli()
	j := mustCreateJob(t, st, store.Job{Slug: "robot-wrote-it", Repo: "kraken", Body: "an agent-authored spec about sprockets", Author: "agent", State: "done", CreatedAt: now - 5000, UpdatedAt: now - 5000})
	queued := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: now - 5000, Until: now})
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim = (%+v, %v)", claimed, err)
	}
	if err := RunMindRun(st, *claimed); err != nil {
		t.Fatalf("RunMindRun on an empty bundle: %v", err)
	}
	if n := l.calls.Load(); n != 0 {
		t.Fatalf("launcher called %d times on an empty bundle, want 0", n)
	}
	r := edgeMRGetRun(t, st, claimed.ID)
	if r.State != "done" || r.Model != "" {
		t.Fatalf("empty-bundle run = state %q model %q, want done with no model", r.State, r.Model)
	}
	if _, sha, _ := MindPrompt(); r.PromptSHA != sha {
		t.Fatalf("empty-bundle PromptSHA = %q, want %q", r.PromptSHA, sha)
	}
}
