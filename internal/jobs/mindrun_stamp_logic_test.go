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

// mindrun_stamp_logic_test.go — the LOGIC dimension of the mind-run launch
// stamp contract (contract-mind.md). Written blind from the frozen contract:
// the RunMindRun change and StampMindRunLaunch were never read. Every fixture
// (slug, body, error string) is invented. The launcher is always the
// edgeMRLaunch seam — no claude ever runs.

// athenaLogicStampClaim creates an operator-authored done job (a bundle that
// launches), enqueues a done run over it and claims it.
func athenaLogicStampClaim(t *testing.T, st *store.Store, slug string) store.MindRun {
	t.Helper()
	now := time.Now().UnixMilli()
	j := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", Body: "make the teapot whistle in a minor key", Author: "operator", State: "done", CreatedAt: now - 5000, UpdatedAt: now - 5000})
	queued := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: now - 5000, Until: now})
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim = (%+v, %v), want run %d", claimed, err, queued.ID)
	}
	return *claimed
}

func athenaLogicStampSHA(t *testing.T) string {
	t.Helper()
	_, sha, err := MindPrompt()
	if err != nil || sha == "" {
		t.Fatalf("MindPrompt sha = %q, err %v", sha, err)
	}
	return sha
}

// TestAthenaLogicStampIsWrittenBeforeLaunch pins the ordering obligation: by
// the time the launcher runs, the running row already carries the prompt
// sha, the model and a positive bundle size.
func TestAthenaLogicStampIsWrittenBeforeLaunch(t *testing.T) {
	st := edgeMREnv(t)
	sha := athenaLogicStampSHA(t)
	var seen store.MindRun
	var seenErr error
	var runID int64
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		seen, seenErr = st.GetMindRun(runID)
		return edgeMREmptyAnswer, "", nil
	})
	claimed := athenaLogicStampClaim(t, st, "teapot-before-launch")
	runID = claimed.ID
	if err := RunMindRun(st, claimed); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	if n := l.calls.Load(); n != 1 {
		t.Fatalf("launcher called %d times, want 1", n)
	}
	if seenErr != nil {
		t.Fatalf("GetMindRun inside the launcher: %v", seenErr)
	}
	if seen.State != "running" {
		t.Fatalf("row state during launch = %q, want running", seen.State)
	}
	if seen.PromptSHA != sha {
		t.Fatalf("prompt sha during launch = %q, want %q", seen.PromptSHA, sha)
	}
	if seen.Model != core.DefaultModel {
		t.Fatalf("model during launch = %q, want %q", seen.Model, core.DefaultModel)
	}
	if seen.BundleBytes <= 0 {
		t.Fatalf("bundle bytes during launch = %d, want > 0", seen.BundleBytes)
	}
}

// TestAthenaLogicStampSurvivesLaunchError pins that a launch returning an
// error leaves a failed row whose note carries the error and whose three
// launch stamps are kept.
func TestAthenaLogicStampSurvivesLaunchError(t *testing.T) {
	st := edgeMREnv(t)
	sha := athenaLogicStampSHA(t)
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return "", "", errors.New("kettle exploded at launch")
	})
	claimed := athenaLogicStampClaim(t, st, "teapot-launch-error")
	_ = RunMindRun(st, claimed) // the failure is recorded on the row; the return value is not this contract's
	got := edgeMRGetRun(t, st, claimed.ID)
	if got.State != "failed" {
		t.Fatalf("state = %q, want failed", got.State)
	}
	if !strings.Contains(got.Note, "kettle exploded at launch") {
		t.Fatalf("note %q does not carry the launch error", got.Note)
	}
	if got.PromptSHA != sha {
		t.Fatalf("failed row prompt sha = %q, want %q", got.PromptSHA, sha)
	}
	if got.Model != core.DefaultModel {
		t.Fatalf("failed row model = %q, want %q", got.Model, core.DefaultModel)
	}
	if got.BundleBytes <= 0 {
		t.Fatalf("failed row bundle bytes = %d, want > 0", got.BundleBytes)
	}
}

// TestAthenaLogicStampSurvivesParseFailure pins the same three stamps on a
// run that launched but answered garbage.
func TestAthenaLogicStampSurvivesParseFailure(t *testing.T) {
	st := edgeMREnv(t)
	sha := athenaLogicStampSHA(t)
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return "the teapot hums but writes no json", "", nil
	})
	claimed := athenaLogicStampClaim(t, st, "teapot-garbage")
	_ = RunMindRun(st, claimed)
	got := edgeMRGetRun(t, st, claimed.ID)
	if got.State != "failed" {
		t.Fatalf("state = %q, want failed", got.State)
	}
	if got.PromptSHA != sha || got.Model != core.DefaultModel || got.BundleBytes <= 0 {
		t.Fatalf("parse-failed row lost its launch stamps: sha %q (want %q), model %q (want %q), bytes %d",
			got.PromptSHA, sha, got.Model, core.DefaultModel, got.BundleBytes)
	}
}

// TestAthenaLogicStampMatchesSuccessfulFinish pins that a successful run
// records the same sha and model as before, and that the bundle size the
// launch stamp wrote is exactly the one FinishMindRun records.
func TestAthenaLogicStampMatchesSuccessfulFinish(t *testing.T) {
	st := edgeMREnv(t)
	sha := athenaLogicStampSHA(t)
	var duringBytes int64
	var runID int64
	edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		if r, err := st.GetMindRun(runID); err == nil {
			duringBytes = r.BundleBytes
		}
		return edgeMREmptyAnswer, "", nil
	})
	claimed := athenaLogicStampClaim(t, st, "teapot-success")
	runID = claimed.ID
	if err := RunMindRun(st, claimed); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	got := edgeMRGetRun(t, st, claimed.ID)
	if got.State != "done" {
		t.Fatalf("state = %q (note %q), want done", got.State, got.Note)
	}
	if got.PromptSHA != sha || got.Model != core.DefaultModel {
		t.Fatalf("done row sha %q model %q, want %q / %q", got.PromptSHA, got.Model, sha, core.DefaultModel)
	}
	if got.BundleBytes <= 0 || got.BundleBytes != duringBytes {
		t.Fatalf("bundle bytes: launch stamp %d, finish %d; want equal and > 0", duringBytes, got.BundleBytes)
	}
}

// TestAthenaLogicEmptyBundleStillRecordsNoModel pins the unchanged empty
// bundle path: never launched, so no model, while the sha is still stamped.
func TestAthenaLogicEmptyBundleStillRecordsNoModel(t *testing.T) {
	st := edgeMREnv(t)
	sha := athenaLogicStampSHA(t)
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return edgeMREmptyAnswer, "", nil
	})
	now := time.Now().UnixMilli()
	j := mustCreateJob(t, st, store.Job{Slug: "teapot-agent-only", Repo: "kraken", Body: "an agent described the kettle", Author: "agent", State: "done", CreatedAt: now - 5000, UpdatedAt: now - 5000})
	queued := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: now - 5000, Until: now})
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim = (%+v, %v)", claimed, err)
	}
	if err := RunMindRun(st, *claimed); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	if n := l.calls.Load(); n != 0 {
		t.Fatalf("launcher called %d times on an empty bundle, want 0", n)
	}
	got := edgeMRGetRun(t, st, claimed.ID)
	if got.Model != "" {
		t.Fatalf("an unlaunched run recorded model %q, want none", got.Model)
	}
	if got.PromptSHA != sha {
		t.Fatalf("an unlaunched run's sha = %q, want %q", got.PromptSHA, sha)
	}
}

// TestAthenaLogicStampMindRunLaunchStates pins the store verb's state gate:
// a running row takes the three values and nothing else changes; a queued
// row and a done row are refused with their state named and left as they
// were.
func TestAthenaLogicStampMindRunLaunchStates(t *testing.T) {
	st := edgeMREnv(t)
	now := time.Now().UnixMilli()

	// Running: the stamp lands, state and note untouched.
	running := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: now - 60000, Until: now})
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != running.ID {
		t.Fatalf("claim = (%+v, %v)", claimed, err)
	}
	if err := st.StampMindRunLaunch(running.ID, "sha-fake-0451", "claude-fake-model", 7321); err != nil {
		t.Fatalf("StampMindRunLaunch on a running row: %v", err)
	}
	got := edgeMRGetRun(t, st, running.ID)
	if got.PromptSHA != "sha-fake-0451" || got.Model != "claude-fake-model" || got.BundleBytes != 7321 {
		t.Fatalf("stamp not written: sha %q model %q bytes %d", got.PromptSHA, got.Model, got.BundleBytes)
	}
	if got.State != "running" || got.Note != "" || got.FinishedAt != 0 || got.Report != "" || got.OutTokens != 0 || got.InTokens != 0 {
		t.Fatalf("stamp touched other columns: %+v", got)
	}
	if got.StartedAt != claimed.StartedAt {
		t.Fatalf("stamp moved started_at: %d -> %d", claimed.StartedAt, got.StartedAt)
	}

	// Done: finish it, then a stamp is refused naming "done" and changes nothing.
	if err := st.FinishMindRun(running.ID, store.MindRunFinish{At: now + 1, Report: "teapot report", PromptSHA: "sha-final-9", Model: "claude-final", BundleBytes: 99}); err != nil {
		t.Fatalf("FinishMindRun: %v", err)
	}
	err = st.StampMindRunLaunch(running.ID, "sha-late", "claude-late", 1)
	if err == nil || !strings.Contains(err.Error(), "done") {
		t.Fatalf("stamp on a done row: err %v, want an error naming done", err)
	}
	after := edgeMRGetRun(t, st, running.ID)
	if after.PromptSHA != "sha-final-9" || after.Model != "claude-final" || after.BundleBytes != 99 {
		t.Fatalf("refused stamp changed a done row: %+v", after)
	}

	// Queued: refused naming "queued", row unchanged.
	queued := contractMRRun(t, st, store.MindRun{Trigger: "daily", Since: now - 30000, Until: now + 2})
	err = st.StampMindRunLaunch(queued.ID, "sha-early", "claude-early", 5)
	if err == nil || !strings.Contains(err.Error(), "queued") {
		t.Fatalf("stamp on a queued row: err %v, want an error naming queued", err)
	}
	q := edgeMRGetRun(t, st, queued.ID)
	if q.State != "queued" || q.PromptSHA != "" || q.Model != "" || q.BundleBytes != 0 {
		t.Fatalf("refused stamp changed a queued row: %+v", q)
	}
}
