package jobs

// submit_edge_test.go — BLIND edge tests for "eliminate the queued status"
// (artifacts/50-contract.md, slice S1 Go core). Authored from the frozen
// contract with no sight of the implementation.
//
// Dimension: boundaries, error paths, refusals. The happy path and the
// invariants are athena-logic's file; everything here is a case where
// SubmitJob must say NO, or must say no in a particular ORDER, or must not
// reach the seam at all.
//
// Every package-level identifier below is prefixed `edge`, and every test
// func `TestSubmitEdge`, so this file can sit beside the logic author's
// twin in the same package.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- fixtures -------------------------------------------------------------

// edgeSandbox isolates everything a SubmitJob call can reach on disk. HOME is
// what ResolveRepo walks (~/Developer) and what every cell path derives from
// via util.Home(); KRAKEN_PLUTO_DIR keeps any incidental job-file read off
// the real ~/Documents/pluto. Package TestMain already quarantines the event
// log and no-ops bindGate.
func edgeSandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
}

// edgeFakeRepo makes ResolveRepo(name) resolve inside the sandboxed $HOME — a
// .git entry is the whole of the repo test, so no real checkout (and no git)
// is involved. Same shape as update_test.go's fakeKrakenRepo.
func edgeFakeRepo(t *testing.T, name string) string {
	t.Helper()
	repo := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("fake repo %s: %v", name, err)
	}
	return repo
}

// edgeStartRecorder captures what the submitStartCell seam was handed, and
// how many times. "Exactly once" and "not at all" are both obligations, so
// the count is the payload — not a bool.
type edgeStartRecorder struct {
	calls int
	job   *Job
	repo  string
}

// edgeStubStartCell swaps the package var the contract names as the test
// seam, restoring it on cleanup. A blind test must never reach a real
// StartCell: that is a git worktree add plus a tmux session spawn.
func edgeStubStartCell(t *testing.T, ret error) *edgeStartRecorder {
	t.Helper()
	rec := &edgeStartRecorder{}
	prev := submitStartCell
	submitStartCell = func(j *Job, repo string) error {
		rec.calls++
		rec.job = j
		rec.repo = repo
		return ret
	}
	t.Cleanup(func() { submitStartCell = prev })
	return rec
}

// edgeMidRunLog is the exact section header + kraken banner a real job file
// carries once the mid-run log has been appended (job_test.go's sampleJob).
const edgeMidRunLog = "## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n" +
	"- [2026-08-12 10:00 me→zeus] a steer that is not a spec\n"

// --- refusal ORDER --------------------------------------------------------

// TestSubmitEdgeRefusalOrder pins the sequence, not just the set. Each job
// below trips EVERY refusal from its own onward, so the substring that comes
// back is proof of which gate ran FIRST. A handler that checked "already
// started" before "unparseable" would pass a set-only test and report the
// wrong reason on every erred, running job.
func TestSubmitEdgeRefusalOrder(t *testing.T) {
	cases := []struct {
		name string
		job  *Job
		want string
	}{
		{
			// trips all four: erred AND done AND started AND empty-bodied
			name: "unparseable wins over everything",
			job:  &Job{Slug: "edge-order-1", Err: "unknown key: raedy", State: "done", Body: ""},
			want: "unparseable",
		},
		{
			// trips three: done AND started AND empty-bodied
			name: "done wins over started and empty",
			job:  &Job{Slug: "edge-order-2", State: "done", Body: ""},
			want: "done",
		},
		{
			// trips two: started AND empty-bodied
			name: "already started wins over empty spec",
			job:  &Job{Slug: "edge-order-3", State: "working", Body: ""},
			want: "already started",
		},
		{
			// blocked is started too — the middle state, easy to miss
			name: "blocked counts as already started",
			job:  &Job{Slug: "edge-order-4", State: "blocked", Body: "a real spec\n"},
			want: "already started",
		},
		{
			// the last refusal before ResolveRepo is reached
			name: "empty spec is the final refusal",
			job:  &Job{Slug: "edge-order-5", State: "", Body: ""},
			want: "empty spec",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			edgeSandbox(t)
			rec := edgeStubStartCell(t, nil)
			before := c.job.State

			err := SubmitJob(c.job)
			if err == nil {
				t.Fatalf("SubmitJob(%+v) = nil, want a refusal containing %q", c.job, c.want)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("SubmitJob error = %q, want it to contain %q", err.Error(), c.want)
			}
			if rec.calls != 0 {
				t.Fatalf("submitStartCell called %d times on a refusal — a refused submit must never reach a start", rec.calls)
			}
			if c.job.State != before {
				t.Fatalf("job state = %q after a refusal, want it unchanged at %q — SubmitJob writes no job state", c.job.State, before)
			}
		})
	}
}

// TestSubmitEdgeErredJobIsRefusedWhateverElseIsTrue is the first refusal in
// isolation: an erred job with a perfectly good spec, a clear state and a
// resolvable repo still never starts. The parse error is the whole reason a
// job displays as draft, and starting one would hand zeus a brief built from
// frontmatter kraken could not read.
func TestSubmitEdgeErredJobIsRefusedWhateverElseIsTrue(t *testing.T) {
	edgeSandbox(t)
	edgeFakeRepo(t, "kraken")
	rec := edgeStubStartCell(t, nil)

	j := &Job{Slug: "edge-erred", Repo: "kraken", Err: "duplicate key: repo", Body: "# Spec\ndo the thing\n"}
	err := SubmitJob(j)
	if err == nil || !strings.Contains(err.Error(), "unparseable") {
		t.Fatalf("SubmitJob = %v, want a refusal containing %q", err, "unparseable")
	}
	if rec.calls != 0 {
		t.Fatalf("submitStartCell called %d times for an erred job", rec.calls)
	}
}

// --- the empty-spec boundary ---------------------------------------------

// TestSubmitEdgeEmptySpecBoundary walks the exact definition the contract
// freezes: the body MINUS the mid-run log section, whitespace-trimmed. The
// two cases that matter are the ones a naive `len(j.Body) == 0` check gets
// wrong — a body that is ONLY the appended log (a job carl steered before it
// ever had a spec) and a body that is only whitespace.
func TestSubmitEdgeEmptySpecBoundary(t *testing.T) {
	empties := []struct {
		name string
		body string
	}{
		{"nothing at all", ""},
		{"whitespace only", "   \n\t\n \r\n  "},
		{"newlines only", "\n\n\n"},
		{"mid-run log with no leading newline", "## Mid-run log\n- stuff\n"},
		{"mid-run log after a leading newline", "\n" + edgeMidRunLog},
		{"mid-run log preceded by blank lines only", "\n   \n\n" + edgeMidRunLog},
	}
	for _, c := range empties {
		t.Run(c.name, func(t *testing.T) {
			edgeSandbox(t)
			edgeFakeRepo(t, "kraken")
			rec := edgeStubStartCell(t, nil)

			j := &Job{Slug: "edge-empty", Repo: "kraken", Body: c.body}
			err := SubmitJob(j)
			if err == nil || !strings.Contains(err.Error(), "empty spec") {
				t.Fatalf("body %q: SubmitJob = %v, want a refusal containing %q", c.body, err, "empty spec")
			}
			if rec.calls != 0 {
				t.Fatalf("body %q: submitStartCell called %d times", c.body, rec.calls)
			}
		})
	}
}

// TestSubmitEdgeSpecAboveAMidRunLogIsNotEmpty is the other side of that
// boundary, and it is the one that would silently break every resume: a job
// with a real spec AND an appended mid-run log must get PAST the empty
// check. Proven without a start by pointing it at an unresolvable repo — the
// error that comes back must be the repo's, never "empty spec".
func TestSubmitEdgeSpecAboveAMidRunLogIsNotEmpty(t *testing.T) {
	edgeSandbox(t)
	rec := edgeStubStartCell(t, nil)

	j := &Job{
		Slug: "edge-spec-plus-log",
		Repo: "edge-no-such-repo-anywhere",
		Body: "\n# Spec\nDedupe the listings.\n\n" + edgeMidRunLog,
	}
	err := SubmitJob(j)
	if err == nil {
		t.Fatal("SubmitJob = nil, want the unresolvable-repo error")
	}
	if strings.Contains(err.Error(), "empty spec") {
		t.Fatalf("SubmitJob error = %q — a spec sitting above a mid-run log is NOT an empty spec", err.Error())
	}
	if rec.calls != 0 {
		t.Fatalf("submitStartCell called %d times despite an unresolvable repo", rec.calls)
	}
}

// --- ResolveRepo ----------------------------------------------------------

// TestSubmitEdgeUnresolvableRepoNeverReachesTheSeam pins the contract's
// "its error returns without any start attempt". A StartCell against an
// unresolved repo path is a git worktree add against "" — it is the failure
// with real side effects, so the guard is a call count, not a message.
func TestSubmitEdgeUnresolvableRepoNeverReachesTheSeam(t *testing.T) {
	for _, repo := range []string{"edge-no-such-repo-anywhere", ""} {
		t.Run("repo="+repo, func(t *testing.T) {
			edgeSandbox(t) // $HOME/Developer does not exist at all
			rec := edgeStubStartCell(t, nil)

			j := &Job{Slug: "edge-bad-repo", Repo: repo, Body: "# Spec\nreal content\n"}
			err := SubmitJob(j)
			if err == nil {
				t.Fatalf("repo %q: SubmitJob = nil, want a repo-resolution error", repo)
			}
			for _, notWanted := range []string{"unparseable", "already started", "empty spec"} {
				if strings.Contains(err.Error(), notWanted) {
					t.Fatalf("repo %q: error = %q, want the repo failure, not %q", repo, err.Error(), notWanted)
				}
			}
			if rec.calls != 0 {
				t.Fatalf("repo %q: submitStartCell called %d times, want 0", repo, rec.calls)
			}
		})
	}
}

// --- the seam -------------------------------------------------------------

// TestSubmitEdgeSeamErrorIsVerbatimAndCalledOnce covers the failure the user
// actually sees: StartCell refusing (a leftover worktree, a taken tmux
// session, a rotation-gate failure). The text must come back unwrapped —
// the board renders it as the 409 body, and a "submit failed: %w" prefix
// would push the real reason off a card — and it must be attempted exactly
// once, since a retry here is a second worktree.
func TestSubmitEdgeSeamErrorIsVerbatimAndCalledOnce(t *testing.T) {
	edgeSandbox(t)
	edgeFakeRepo(t, "kraken")
	want := errors.New("worktree /Users/x/.kraken/cells/edge-seam-7/wt-kraken already exists")
	rec := edgeStubStartCell(t, want)

	j := &Job{Slug: "edge-seam", Repo: "kraken", Body: "# Spec\nreal content\n"}
	err := SubmitJob(j)
	if err == nil {
		t.Fatal("SubmitJob = nil, want the seam's error")
	}
	if err.Error() != want.Error() {
		t.Fatalf("SubmitJob error = %q, want it VERBATIM: %q", err.Error(), want.Error())
	}
	if rec.calls != 1 {
		t.Fatalf("submitStartCell called %d times, want exactly 1", rec.calls)
	}
	if rec.job != j {
		t.Fatalf("seam got job %+v, want the very *Job it was handed", rec.job)
	}
	if !strings.HasSuffix(rec.repo, "kraken") {
		t.Fatalf("seam got repo %q, want ResolveRepo's answer for %q", rec.repo, j.Repo)
	}
	if j.State != "" {
		t.Fatalf("job state = %q after a failed start, want it still draft — StartCell owns the working write", j.State)
	}
}

// TestSubmitEdgeSuccessWritesNoJobStateItself is the success side of that
// same division of labour: SubmitJob returns the seam's nil and touches
// nothing. A SubmitJob that stamped `working` itself would double-write with
// StartCell, and — worse — would leave a job reading as working after a
// StartCell that failed halfway.
func TestSubmitEdgeSuccessWritesNoJobStateItself(t *testing.T) {
	edgeSandbox(t)
	edgeFakeRepo(t, "kraken")
	rec := edgeStubStartCell(t, nil)

	j := &Job{Slug: "edge-ok", Repo: "kraken", Body: "# Spec\nreal content\n"}
	if err := SubmitJob(j); err != nil {
		t.Fatalf("SubmitJob = %v, want nil", err)
	}
	if rec.calls != 1 {
		t.Fatalf("submitStartCell called %d times, want exactly 1", rec.calls)
	}
	if j.State != "" {
		t.Fatalf("job state = %q, want SubmitJob itself to write nothing (the stub start wrote nothing)", j.State)
	}
	if j.Err != "" {
		t.Fatalf("job err = %q, want untouched", j.Err)
	}
}

// TestSubmitEdgeNeverStartsTwiceForOneCall guards the double-submit shape
// from the inside: whatever SubmitJob does internally (retry on a transient,
// loop over prereqs that no longer exist), one call is one start attempt.
func TestSubmitEdgeNeverStartsTwiceForOneCall(t *testing.T) {
	edgeSandbox(t)
	edgeFakeRepo(t, "monolith")
	rec := edgeStubStartCell(t, errors.New("transient tmux failure"))

	j := &Job{Slug: "edge-once", Repo: "monolith", Body: "spec\n"}
	_ = SubmitJob(j)
	if rec.calls != 1 {
		t.Fatalf("submitStartCell called %d times for ONE SubmitJob, want 1 — a retry loop here is a second worktree", rec.calls)
	}
}

// --- parser edges: the retired keys still on disk -------------------------

// TestSubmitEdgeParseJobDiscardsRetiredReadyValues covers the corpus rule.
// `ready` is gone from the model, but every job file in ~/Documents/pluto
// and every stored row's composed frontmatter still carries the key — so the
// parser must consume and DISCARD it, at any value, exactly as it already
// does for type/auto-approve/success/skip-doc. Erroring instead would tip
// the entire corpus into draft-with-a-parse-error on the first read after
// the update.
//
// Contract silence: the contract names `ready: true` and `ready: false`
// only. The strict reading asserted here is the legacy-discard precedent —
// a discarded key is not validated, so a hand-mangled value cannot error
// either.
func TestSubmitEdgeParseJobDiscardsRetiredReadyValues(t *testing.T) {
	for _, src := range []string{
		"---\njob: x\nrepo: monolith\nready: true\n---\n\n# Spec\n",
		"---\njob: x\nrepo: monolith\nready: false\n---\n\n# Spec\n",
		"---\nready: true\njob: x\nrepo: monolith\n---\n\n# Spec\n", // first key
		"---\njob: x\nrepo: monolith\nready:\n---\n\n# Spec\n",      // empty value
		"---\njob: x\nrepo: monolith\nready: yes\n---\n\n# Spec\n",  // was a bad-bool error
		"---\njob: x\nrepo: monolith\nready: sure\n---\n\n# Spec\n", // outright garbage
		"---\njob: x\nready: true\nrepo: monolith\n---\n\n# Spec\n", // mid-position
	} {
		j := ParseJob("x.job.md", []byte(src))
		if j == nil {
			t.Fatalf("%q: nil job (must never crash)", src)
		}
		if j.Err != "" {
			t.Errorf("%q: err = %q, want a clean parse — `ready` is accept-and-discard", src, j.Err)
		}
		if j.Repo != "monolith" {
			t.Errorf("%q: repo = %q — a discarded key must not swallow its neighbours", src, j.Repo)
		}
		if !strings.Contains(j.Body, "# Spec") {
			t.Errorf("%q: body = %q, want the spec intact", src, j.Body)
		}
	}
}

// TestSubmitEdgeParseJobDiscardsPrereqBlocks covers the harder half of the
// same corpus rule: `prereqs:` is a BLOCK, so discarding it means consuming
// its indented `- job:` / `gated:` continuation lines too. The three shapes
// below are the ones a block-skipper gets wrong — a block in the middle (the
// next real key must still parse), a block as the LAST frontmatter key (the
// skip must stop at the closing fence rather than eating it), and a block
// whose items are malformed (a discarded key is not validated).
//
// Contract silence: the contract freezes well-formed `- job:` / `gated:`
// continuation lines only. The malformed cases below assert the same strict
// reading as the `ready` values above — wholesale discard, no validation.
func TestSubmitEdgeParseJobDiscardsPrereqBlocks(t *testing.T) {
	cases := map[string]string{
		"mid-frontmatter block": "---\njob: x\nprereqs:\n  - job: dep-a\n    gated: true\n  - job: dep-b\nrepo: monolith\n---\n\n# Spec\n",
		"block as the LAST key": "---\njob: x\nrepo: monolith\nprereqs:\n  - job: dep-a\n    gated: true\n---\n\n# Spec\n",
		"single ungated item":   "---\njob: x\nrepo: monolith\nprereqs:\n  - job: dep-a\n---\n\n# Spec\n",
		"empty block":           "---\njob: x\nrepo: monolith\nprereqs:\n---\n\n# Spec\n",
		"junk under prereqs":    "---\njob: x\nrepo: monolith\nprereqs:\n  not an item\n---\n\n# Spec\n",
		"item missing job":      "---\njob: x\nrepo: monolith\nprereqs:\n  - gated: true\n---\n\n# Spec\n",
		"bad gated bool":        "---\njob: x\nrepo: monolith\nprereqs:\n  - job: dep\n    gated: yep\n---\n\n# Spec\n",
		"scalar not a block":    "---\njob: x\nrepo: monolith\nprereqs: whenever\n---\n\n# Spec\n",
		"ready and prereqs":     "---\njob: x\nready: true\nprereqs:\n  - job: dep-a\n    gated: true\nrepo: monolith\n---\n\n# Spec\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			j := ParseJob("x.job.md", []byte(src))
			if j == nil {
				t.Fatal("nil job (must never crash)")
			}
			if j.Err != "" {
				t.Fatalf("err = %q, want a clean parse — `prereqs` is accept-and-discard\nsrc:\n%s", j.Err, src)
			}
			if j.Repo != "monolith" {
				t.Fatalf("repo = %q, want monolith — the block skip must not swallow the keys around it", j.Repo)
			}
			if !strings.Contains(j.Body, "# Spec") {
				t.Fatalf("body = %q, want the spec intact — the block skip must stop at the closing fence", j.Body)
			}
		})
	}
}

// TestSubmitEdgeParserStillRefusesRealMalformations is the boundary the two
// tests above must not have widened. Discarding `ready`/`prereqs` is a
// per-key tolerance, not a general one: an unknown key, a stray indented
// line that follows NO block key, a duplicate key and a bad state must all
// still set Err. A block-skipper written as "ignore every indented line"
// would pass the prereqs test and silently swallow the orphan below.
func TestSubmitEdgeParserStillRefusesRealMalformations(t *testing.T) {
	cases := map[string]string{
		"unknown key":                 "---\njob: x\nraedy: true\n---\n",
		"orphan indented line":        "---\njob: x\nrepo: monolith\n  orphan: true\n---\n",
		"orphan indented after ready": "---\njob: x\nready: true\n  orphan: true\nrepo: monolith\n---\n",
		"duplicate key":               "---\njob: x\nrepo: a\nrepo: b\n---\n",
		"bad state":                   "---\njob: x\nstate: bananas\n---\n",
		"queued is not a state":       "---\njob: x\nstate: queued\n---\n",
		"draft is not a state":        "---\njob: x\nstate: draft\n---\n",
		"colonless line":              "---\njob: x\nnonsense\n---\n",
		"no frontmatter":              "just some text\n",
		"unterminated frontmatter":    "---\njob: x\n",
	}
	for name, src := range cases {
		t.Run(name, func(t *testing.T) {
			j := ParseJob("f.job.md", []byte(src))
			if j == nil {
				t.Fatal("nil job (must never crash)")
			}
			if j.Err == "" {
				t.Fatalf("clean parse, want Err set: %+v", j)
			}
		})
	}
}
