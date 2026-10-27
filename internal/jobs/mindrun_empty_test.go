package jobs

import (
	"context"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// mindrun_empty_test.go — a mind run whose bundle holds nothing to judge is
// recorded as done without launching a claude: the record (bundle, manifest,
// report) is kept, the launcher is never called, and the same job with an
// operator-authored spec still launches.

func TestMindBundleEmpty(t *testing.T) {
	cases := []struct {
		name string
		b    MindBundle
		want bool
	}{
		{"nothing", MindBundle{}, true},
		{"items only", MindBundle{Items: []MindBundleItem{{}}}, true},
		{"blank spec body", MindBundle{Spec: &MMEvidenceJob{Author: "agent", Body: "   "}}, true},
		{"a spec body counts whoever wrote it (the author filter is BuildMindBundle's)", MindBundle{Spec: &MMEvidenceJob{Author: "agent", Body: "text"}}, false},
		{"evidence that could not be read is not nothing", MindBundle{Truncated: []string{"operator reader: 1 transcripts or projects could not be read in full"}}, false},
		{"a prompt", MindBundle{Prompts: []core.OperatorRecord{{}}}, false},
		{"an operator spec", MindBundle{Spec: &MMEvidenceJob{Author: "operator", Body: "build it"}}, false},
		{"a feedback note", MindBundle{Feedback: []MMEvidenceFeedback{{}}}, false},
		{"an answer", MindBundle{Answers: []MMEvidenceAnswer{{}}}, false},
		{"a saved artifact", MindBundle{Saved: []MindBundleSaved{{}}}, false},
		{"a mark", MindBundle{Marks: []MindBundleMark{{}}}, false},
	}
	for _, c := range cases {
		if got := mindBundleEmpty(c.b); got != c.want {
			t.Errorf("%s: mindBundleEmpty = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRunMindRunDoesNotLaunchOnAnEmptyBundle(t *testing.T) {
	st := edgeMREnv(t)
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return edgeMREmptyAnswer, "", nil
	})
	now := time.Now().UnixMilli()
	j := mustCreateJob(t, st, store.Job{Slug: "agent-filed", Repo: "kraken", Body: "an agent wrote this spec", Author: "agent", State: "done", CreatedAt: now - 5000, UpdatedAt: now - 5000})
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
	got := edgeMRGetRun(t, st, claimed.ID)
	if got.State != "done" {
		t.Fatalf("state = %q, want done", got.State)
	}
	if !strings.Contains(got.Report, "nothing to judge") || !strings.Contains(got.Report, "no claude was launched") {
		t.Fatalf("report does not say the run was not launched:\n%s", got.Report)
	}
	if got.Model != "" || got.OutTokens != 0 {
		t.Fatalf("an unlaunched run recorded model %q and out %d, want neither", got.Model, got.OutTokens)
	}
	if _, sha, _ := MindPrompt(); got.PromptSHA != sha || sha == "" {
		t.Fatalf("an unlaunched run must still stamp the prompt sha it would have judged under: got %q want %q", got.PromptSHA, sha)
	}
	blobs, err := st.GetMindRunBlobs(claimed.ID)
	if err != nil {
		t.Fatalf("blobs: %v", err)
	}
	if blobs.Manifest == "" {
		t.Fatal("the record must keep its manifest even when nothing was judged")
	}
}

func TestRunMindRunLaunchesOnAnOperatorSpec(t *testing.T) {
	st := edgeMREnv(t)
	l := edgeMRLaunch(t, func(ctx context.Context, stdin string) (string, string, error) {
		return edgeMREmptyAnswer, "", nil
	})
	now := time.Now().UnixMilli()
	j := mustCreateJob(t, st, store.Job{Slug: "operator-filed", Repo: "kraken", Body: "please build the thing", Author: "operator", State: "done", CreatedAt: now - 5000, UpdatedAt: now - 5000})
	queued := contractMRRun(t, st, store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: "done", Since: now - 5000, Until: now})
	claimed, err := st.ClaimNextMindRun(now)
	if err != nil || claimed == nil || claimed.ID != queued.ID {
		t.Fatalf("claim = (%+v, %v)", claimed, err)
	}
	if err := RunMindRun(st, *claimed); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	if n := l.calls.Load(); n != 1 {
		t.Fatalf("launcher called %d times with an operator spec, want 1", n)
	}
}
