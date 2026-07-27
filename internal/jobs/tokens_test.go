package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// --- fixture helpers ---

// tokTranscript writes a transcript full of assistant records carrying a
// usage object, into the ~/.claude/projects dir Claude Code would name for
// the given cwd. rel is the path inside that dir, so a caller can write a
// root transcript ("sess.jsonl") or a subagent sidecar
// ("sess/subagents/agent-1.jsonl").
func tokTranscript(t *testing.T, cwd, rel string, out int64, n int) string {
	t.Helper()
	dir := filepath.Join(util.Home(), ".claude", "projects", core.ProjectDirName(cwd))
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var b strings.Builder
	for i := 0; i < n; i++ {
		line, err := json.Marshal(map[string]any{
			"type":      "assistant",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
			"message": map[string]any{
				"model": "claude-opus-5",
				"usage": map[string]any{
					"input_tokens": 1, "output_tokens": out,
					"cache_creation_input_tokens": 2, "cache_read_input_tokens": 3,
				},
			},
		})
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

// --- tests ---

// TestJobProjectDirsDerivesFromTheWorktreeString pins the rule that makes
// this feature retrospective at all: the project dir comes from
// CellWorktree's STRING, so it still resolves after teardownCell has removed
// the cell dir and ExistingWorktree returns "". The legacy bare-"wt" name is
// covered too, since cells predating the wt-<repo> naming munge differently.
func TestJobProjectDirsDerivesFromTheWorktreeString(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "demo"}
	repo := filepath.Join(util.Home(), "Developer", "monolith")

	// Nothing on disk at all — ExistingWorktree would give up here.
	if got := ExistingWorktree(j, repo); got != "" {
		t.Fatalf("fixture is wrong: ExistingWorktree = %q, want empty", got)
	}
	dirs := JobProjectDirs(j, repo)
	if len(dirs) != 2 {
		t.Fatalf("dirs = %v, want the wt-<repo> dir plus the legacy bare-wt one", dirs)
	}
	wantMain := filepath.Join(util.Home(), ".claude", "projects",
		core.ProjectDirName(CellWorktree(j, repo)))
	if dirs[0] != wantMain {
		t.Errorf("dirs[0] = %q, want %q", dirs[0], wantMain)
	}
	if !strings.HasSuffix(dirs[0], "-wt-monolith") {
		t.Errorf("dirs[0] = %q, want the repo basename in the munged name — a job may target any repo", dirs[0])
	}
	if !strings.HasSuffix(dirs[1], "-wt") {
		t.Errorf("dirs[1] = %q, want the legacy bare-wt name", dirs[1])
	}
	if got := JobProjectDirs(j, ""); got != nil {
		t.Errorf("JobProjectDirs with no resolved repo = %v, want nil", got)
	}
	if got := JobProjectDirs(nil, repo); got != nil {
		t.Errorf("JobProjectDirs(nil, …) = %v, want nil", got)
	}
}

// TestRefreshJobTokensSumsThenSettles is the pass' whole steady-state
// contract in one test: the first refresh writes, a second refresh over
// unchanged transcripts writes NOTHING (which is what makes a settled job
// cost one stat per file and no SQL), and an append is picked up as a delta
// rather than a re-sum.
func TestRefreshJobTokensSumsThenSettles(t *testing.T) {
	st := useTestStore(t)
	j := &Job{Slug: "demo", Repo: "kraken", State: "working"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	wt := CellWorktree(j, repo)
	tokTranscript(t, wt, "sess.jsonl", 100, 2)
	tokTranscript(t, wt, filepath.Join("sess", "subagents", "agent-1.jsonl"), 10, 1)

	wrote, err := refreshJobTokens(st, j, repo, nil)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if !wrote {
		t.Fatal("first refresh must write")
	}
	row, err := st.GetJobTokens("demo")
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if row.Out != 210 {
		t.Errorf("Out = %d, want 210 (2x100 root + 1x10 sidecar — sidecars count)", row.Out)
	}
	if row.In != 3 || row.CacheWrite != 6 || row.CacheRead != 9 {
		t.Errorf("counters = %d/%d/%d, want 3/6/9 across the 3 records", row.In, row.CacheWrite, row.CacheRead)
	}
	if row.Sessions != 2 {
		t.Errorf("Sessions = %d, want 2 transcript files", row.Sessions)
	}

	offs, err := st.JobTokenOffsets("demo")
	if err != nil {
		t.Fatalf("JobTokenOffsets: %v", err)
	}
	wrote, err = refreshJobTokens(st, j, repo, offs)
	if err != nil {
		t.Fatalf("second refresh: %v", err)
	}
	if wrote {
		t.Fatal("a settled job must write nothing at all")
	}

	// append one more record: only the delta lands
	tokTranscript(t, wt, "sess2.jsonl", 7, 1)
	offs, _ = st.JobTokenOffsets("demo")
	if wrote, err = refreshJobTokens(st, j, repo, offs); err != nil || !wrote {
		t.Fatalf("third refresh: wrote=%v err=%v", wrote, err)
	}
	row, _ = st.GetJobTokens("demo")
	if row.Out != 217 {
		t.Errorf("Out = %d, want 217 — the new file added as a DELTA, not a re-sum", row.Out)
	}
	if row.Sessions != 3 {
		t.Errorf("Sessions = %d, want 3", row.Sessions)
	}
}

// TestRefreshJobTokensNoTranscriptsWritesNothing pins that a job whose cell
// never came up leaves NO row: that absence is what the wire shape turns into
// "no key at all", which a reader can tell apart from a real zero.
func TestRefreshJobTokensNoTranscriptsWritesNothing(t *testing.T) {
	st := useTestStore(t)
	j := &Job{Slug: "ghost", Repo: "kraken", State: "working"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	wrote, err := refreshJobTokens(st, j, repo, nil)
	if err != nil {
		t.Fatalf("refreshJobTokens: %v", err)
	}
	if wrote {
		t.Fatal("a job with no transcripts must not write a row")
	}
	if _, err := st.GetJobTokens("ghost"); err == nil {
		t.Fatal("want no row at all for an unmeasured job")
	}
}

// TestSameOffsets covers the predicate the no-op path turns on.
func TestSameOffsets(t *testing.T) {
	cases := []struct {
		name string
		a, b map[string]int64
		want bool
	}{
		{"both empty", nil, map[string]int64{}, true},
		{"identical", map[string]int64{"/a": 1}, map[string]int64{"/a": 1}, true},
		{"grew", map[string]int64{"/a": 1}, map[string]int64{"/a": 2}, false},
		{"file appeared", map[string]int64{"/a": 1}, map[string]int64{"/a": 1, "/b": 0}, false},
		{"file vanished", map[string]int64{"/a": 1, "/b": 2}, map[string]int64{"/a": 1}, false},
	}
	for _, c := range cases {
		if got := sameOffsets(c.a, c.b); got != c.want {
			t.Errorf("%s: sameOffsets = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestTokensPassThrottlesIdleJobsAndPrunes covers the pacing: a job with a
// live cell is summed on every tick, a job without one is summed once and
// then throttled for tokensIdleInterval, and a slug that leaves the board
// loses its throttle stamp (so a later job reusing the name doesn't wait out
// an interval it never earned).
func TestTokensPassThrottlesIdleJobsAndPrunes(t *testing.T) {
	st := useTestStore(t)
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir fake repo: %v", err)
	}

	idle := &Job{Slug: "idle", Repo: "kraken", State: "done"}
	tokTranscript(t, CellWorktree(idle, repo), "sess.jsonl", 5, 1)

	d := newDaemon()
	now := time.Now()
	d.tokensPass([]*Job{idle}, map[string]bool{}, now)
	row, err := st.GetJobTokens("idle")
	if err != nil {
		t.Fatalf("first pass wrote no row: %v", err)
	}
	if row.Out != 5 {
		t.Fatalf("Out = %d, want 5", row.Out)
	}
	if d.TokenScan["idle"] == 0 {
		t.Fatal("the pass must stamp its throttle clock")
	}

	// more spend arrives, but the next tick is inside the idle interval
	tokTranscript(t, CellWorktree(idle, repo), "sess2.jsonl", 500, 1)
	d.tokensPass([]*Job{idle}, map[string]bool{}, now.Add(time.Minute))
	row, _ = st.GetJobTokens("idle")
	if row.Out != 5 {
		t.Fatalf("Out = %d, want the idle job to still be throttled", row.Out)
	}

	// a live cell is never throttled
	d.tokensPass([]*Job{idle}, map[string]bool{CellSession(idle): true}, now.Add(time.Minute))
	row, _ = st.GetJobTokens("idle")
	if row.Out != 505 {
		t.Fatalf("Out = %d, want 505 — a live cell is summed every tick", row.Out)
	}

	// past the interval, an idle job is due again
	tokTranscript(t, CellWorktree(idle, repo), "sess3.jsonl", 1, 1)
	d.tokensPass([]*Job{idle}, map[string]bool{}, now.Add(tokensIdleInterval+time.Minute))
	row, _ = st.GetJobTokens("idle")
	if row.Out != 506 {
		t.Fatalf("Out = %d, want 506 once the idle interval has elapsed", row.Out)
	}

	// the slug leaves the board: its throttle stamp goes with it
	d.tokensPass(nil, map[string]bool{}, now.Add(2*tokensIdleInterval))
	if _, ok := d.TokenScan["idle"]; ok {
		t.Fatal("a slug that left the board must lose its throttle stamp")
	}
}

// TestTokensPassSkipsUnstartedJobs: a draft or queued job has never had a
// worktree, so no transcripts can exist under its name and opening a project
// dir for it is pure waste on every tick.
func TestTokensPassSkipsUnstartedJobs(t *testing.T) {
	st := useTestStore(t)
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir fake repo: %v", err)
	}

	draft := &Job{Slug: "draft", Repo: "kraken"} // State "" — never started
	tokTranscript(t, CellWorktree(draft, repo), "sess.jsonl", 5, 1)

	d := newDaemon()
	d.tokensPass([]*Job{draft}, map[string]bool{}, time.Now())
	if _, err := st.GetJobTokens("draft"); err == nil {
		t.Fatal("an unstarted job must not be summed")
	}
	if _, ok := d.TokenScan["draft"]; ok {
		t.Fatal("an unstarted job must not even claim a throttle slot")
	}
}

// TestTokensPassWithoutAStoreIsANoOp: every entrypoint except kraken serve
// runs with no store wired, and the pass has nowhere to put a number there.
func TestTokensPassWithoutAStoreIsANoOp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	UseStore(nil)
	d := newDaemon()
	d.tokensPass([]*Job{{Slug: "x", Repo: "kraken", State: "working"}}, map[string]bool{}, time.Now())
	if len(d.TokenScan) != 0 {
		t.Fatalf("TokenScan = %v, want the pass to have done nothing", d.TokenScan)
	}
}

// TestTokensPassCapsTheIdleBatch pins the burst bound: a daemon restart makes
// every job due at once, and that is exactly the tick that can least afford
// to sum all of them.
func TestTokensPassCapsTheIdleBatch(t *testing.T) {
	st := useTestStore(t)
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir fake repo: %v", err)
	}

	var jobs []*Job
	for i := 0; i < tokensIdleBatch+3; i++ {
		j := &Job{Slug: "job" + string(rune('a'+i)), Repo: "kraken", State: "done"}
		tokTranscript(t, CellWorktree(j, repo), "sess.jsonl", 1, 1)
		jobs = append(jobs, j)
	}
	d := newDaemon()
	d.tokensPass(jobs, map[string]bool{}, time.Now())

	rows, err := st.ListJobTokens()
	if err != nil {
		t.Fatalf("ListJobTokens: %v", err)
	}
	if len(rows) != tokensIdleBatch {
		t.Fatalf("summed %d idle jobs in one tick, want the cap of %d", len(rows), tokensIdleBatch)
	}
}

// TestRefreshJobTokensReportsAPartialScan is the floor rule crossing the
// layer boundary: core.SumTokensDelta's partial-scan error must reach the
// caller AND the counters it did find must still be written, because a
// reported floor is worth more than a silent zero.
func TestRefreshJobTokensReportsAPartialScan(t *testing.T) {
	st := useTestStore(t)
	j := &Job{Slug: "big", Repo: "kraken", State: "working"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	path := tokTranscript(t, CellWorktree(j, repo), "sess.jsonl", 100, 1)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// a single line past core's 16 MB scanner ceiling ends the scan
	if _, err := f.WriteString(`{"type":"assistant","message":{"content":"` +
		strings.Repeat("x", (16<<20)+1) + `"}}` + "\n"); err != nil {
		t.Fatalf("append monster line: %v", err)
	}
	f.Close()

	wrote, err := refreshJobTokens(st, j, repo, nil)
	if err == nil {
		t.Fatal("a partial scan must be reported — presenting a floor as a total is the bug")
	}
	if !wrote {
		t.Fatal("the counters it did reach must still be written")
	}
	row, gerr := st.GetJobTokens("big")
	if gerr != nil {
		t.Fatalf("GetJobTokens: %v", gerr)
	}
	if row.Out != 100 {
		t.Fatalf("Out = %d, want the 100 it read before the scan stopped", row.Out)
	}
}
