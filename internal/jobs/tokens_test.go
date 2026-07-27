package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
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

// tokNamedTranscript is tokTranscript with an `agent-name` head record
// prepended — the signal core.isAgentTranscript actually classifies on, and
// the shape Claude Code writes for every agent in a cell today.
//
// Deliberately NOT the `agent-setting` record: that one only appears when a
// spawn carried a `subagent_type`, which zeus' crew does not, so a fixture
// built on it would test a shape 15% of real teammate transcripts do not have.
// A teammate name here (`hephaestus-…`) makes the file delegated; a lead name
// (`zeus-…`, `thread-…-t1`) makes it the lead's.
func tokNamedTranscript(t *testing.T, cwd, rel, agentName string, out int64, n int) string {
	t.Helper()
	path := tokTranscript(t, cwd, rel, out, n)
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back transcript: %v", err)
	}
	head, err := json.Marshal(map[string]any{"type": "agent-name", "agentName": agentName})
	if err != nil {
		t.Fatalf("marshal agent-name record: %v", err)
	}
	if err := os.WriteFile(path, append(append(head, '\n'), body...), 0o644); err != nil {
		t.Fatalf("write named transcript: %v", err)
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
	j := &Job{ID: 17, Slug: "demo"}
	repo := filepath.Join(util.Home(), "Developer", "monolith")

	// Nothing on disk at all — ExistingWorktree would give up here.
	if got := ExistingWorktree(j, repo); got != "" {
		t.Fatalf("fixture is wrong: ExistingWorktree = %q, want empty", got)
	}
	dirs, err := JobProjectDirs(j, repo)
	if err != nil {
		t.Fatalf("JobProjectDirs: %v", err)
	}
	// FOUR bases: {current <slug>-<id> cell, legacy bare-<slug> cell} x
	// {wt-<repo>, legacy bare wt}. See JobProjectDirs' doc comment — each
	// missing base reports as a clean zero rather than an error.
	if len(dirs) != 4 {
		t.Fatalf("dirs = %v, want both cell namings x both worktree namings", dirs)
	}
	wantMain := filepath.Join(util.Home(), ".claude", "projects",
		core.ProjectDirName(CellWorktree(j, repo)))
	if dirs[0] != wantMain {
		t.Errorf("dirs[0] = %q, want %q — the base a live cell writes into must come first", dirs[0], wantMain)
	}
	if !strings.HasSuffix(dirs[0], "-wt-monolith") {
		t.Errorf("dirs[0] = %q, want the repo basename in the munged name — a job may target any repo", dirs[0])
	}
	if !strings.HasSuffix(dirs[1], "-wt") {
		t.Errorf("dirs[1] = %q, want the legacy bare-wt name", dirs[1])
	}
	wantLegacy := filepath.Join(util.Home(), ".claude", "projects",
		core.ProjectDirName(LegacyCellWorktree(j, repo)))
	if dirs[2] != wantLegacy {
		t.Errorf("dirs[2] = %q, want the pre-cutover bare-slug base %q", dirs[2], wantLegacy)
	}
	if got, _ := JobProjectDirs(j, ""); got != nil {
		t.Errorf("JobProjectDirs with no resolved repo = %v, want nil", got)
	}
	if got, _ := JobProjectDirs(nil, repo); got != nil {
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
	j := &Job{ID: 11, Slug: "demo", Repo: "kraken", State: "working"}
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
	row, err := st.GetJobTokens(j.ID)
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

	offs, err := st.JobTokenOffsets(j.ID)
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
	offs, _ = st.JobTokenOffsets(j.ID)
	if wrote, err = refreshJobTokens(st, j, repo, offs); err != nil || !wrote {
		t.Fatalf("third refresh: wrote=%v err=%v", wrote, err)
	}
	row, _ = st.GetJobTokens(j.ID)
	if row.Out != 217 {
		t.Errorf("Out = %d, want 217 — the new file added as a DELTA, not a re-sum", row.Out)
	}
	if row.Sessions != 3 {
		t.Errorf("Sessions = %d, want 3", row.Sessions)
	}
}

// TestZeusAndThreadNamesMatchCoreLeadPrefixes is the anti-drift pin for the
// one piece of this feature that spans a package boundary it cannot express.
//
// core decides whether a transcript is the LEAD's or a delegated agent's from
// the `agentName` in its head, and the two names that mean "lead" are minted
// HERE — ZeusName and ThreadAgentName. core cannot import jobs (the dependency
// runs util ← store ← core ← jobs), so it carries the prefixes as literals;
// this test is the only thing that keeps the literals and the minters in step.
//
// What breaks without it is silent and unrecoverable: rename zeus' agent and
// every cell's LEAD spend starts landing in the subagent column, on
// accumulate-forward counters that cannot be corrected without another full
// wipe. A failing test here means core's constants need the same edit — not
// that this test needs relaxing.
func TestZeusAndThreadNamesMatchCoreLeadPrefixes(t *testing.T) {
	for _, slug := range []string{"demo", "token-count-restyling", "asr-architecture-diffs"} {
		j := &Job{Slug: slug}

		zeus := ZeusName(j)
		if !strings.HasPrefix(zeus, core.ZeusAgentNamePrefix) {
			t.Errorf("ZeusName(%q) = %q, want it to start with core.ZeusAgentNamePrefix (%q)",
				slug, zeus, core.ZeusAgentNamePrefix)
		}
		if !core.IsLeadAgentName(zeus) {
			t.Errorf("core.IsLeadAgentName(%q) = false — zeus' own spend would be booked to subagents", zeus)
		}

		thread := ThreadAgentName(j, "t1")
		if !strings.HasPrefix(thread, core.ThreadAgentNamePrefix) {
			t.Errorf("ThreadAgentName(%q, t1) = %q, want it to start with core.ThreadAgentNamePrefix (%q)",
				slug, thread, core.ThreadAgentNamePrefix)
		}
		if !core.IsLeadAgentName(thread) {
			t.Errorf("core.IsLeadAgentName(%q) = false — a side thread counts as the lead", thread)
		}

		// ...and the other direction: a teammate name must NOT read as a lead,
		// or the whole subagent half collapses to zero.
		for _, crew := range []string{"hephaestus-" + slug, "athena-logic-" + slug, "hermes-" + slug} {
			if core.IsLeadAgentName(crew) {
				t.Errorf("core.IsLeadAgentName(%q) = true, want false — that is a teammate", crew)
			}
		}
	}
}

// TestRefreshJobTokensStoresTheZeusHalf carries the split all the way to the
// stored row, over all four shapes a real cell dir holds: zeus' transcript, a
// SIDE THREAD's (which counts as the lead), a teammate's, and an in-process
// subagent's sidecar. The totals must be what they always were — the split
// partitions a sum, it does not change one — and the zeus columns must hold
// the two lead files' share alone.
//
// The two lead files are named through ZeusName/ThreadAgentName rather than
// with literals on purpose: this is the same coupling
// TestZeusAndThreadNamesMatchCoreLeadPrefixes pins, exercised end to end.
// The subagent half is deliberately absent from the row and derived here by
// subtraction, which is the only form it ever takes.
func TestRefreshJobTokensStoresTheZeusHalf(t *testing.T) {
	st := useTestStore(t)
	// A real store id: token rows are keyed on it since v12, and
	// refreshJobTokens refuses a zero one rather than pooling every
	// unpersisted job into a shared row.
	j := &Job{Slug: "demo", ID: 7, Repo: "kraken", State: "working"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	wt := CellWorktree(j, repo)
	tokNamedTranscript(t, wt, "zeus.jsonl", ZeusName(j), 100, 2)                     // the lead
	tokNamedTranscript(t, wt, "thread.jsonl", ThreadAgentName(j, "t1"), 20, 1)       // a side thread: also the lead
	tokNamedTranscript(t, wt, "hephaestus.jsonl", "hephaestus-demo", 30, 1)          // a teammate
	tokTranscript(t, wt, filepath.Join("zeus", "subagents", "agent-1.jsonl"), 10, 1) // a sidecar

	if wrote, err := refreshJobTokens(st, j, repo, nil); err != nil || !wrote {
		t.Fatalf("refreshJobTokens: wrote=%v err=%v", wrote, err)
	}
	row, err := st.GetJobTokens(7)
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if row.Out != 260 {
		t.Fatalf("Out = %d, want 260 (2x100 zeus + 20 thread + 30 teammate + 10 sidecar) — the split must not change the total", row.Out)
	}
	if row.ZeusOut != 220 {
		t.Errorf("ZeusOut = %d, want 220 — zeus' 200 plus the side thread's 20, and nothing else", row.ZeusOut)
	}
	if got := row.Out - row.ZeusOut; got != 40 {
		t.Errorf("derived subagent Out = %d, want 40 (teammate + sidecar)", got)
	}
	// the other three counters split the same way: 1 in / 2 cache-write / 3
	// cache-read per record, 3 of the 5 records being a lead's.
	if row.ZeusIn != 3 || row.ZeusCacheWrite != 6 || row.ZeusCacheRead != 9 {
		t.Errorf("zeus in/cw/cr = %d/%d/%d, want 3/6/9", row.ZeusIn, row.ZeusCacheWrite, row.ZeusCacheRead)
	}
	if row.In != 5 || row.Sessions != 4 {
		t.Errorf("In = %d, Sessions = %d, want 5 records across 4 transcript files", row.In, row.Sessions)
	}
	if row.ZeusIn > row.In || row.ZeusCacheWrite > row.CacheWrite || row.ZeusCacheRead > row.CacheRead {
		t.Errorf("zeus half exceeds the total it is a subset of: %+v", row)
	}
}

// TestRefreshJobTokensNoTranscriptsWritesNothing pins that a job whose cell
// never came up leaves NO row: that absence is what the wire shape turns into
// "no key at all", which a reader can tell apart from a real zero.
func TestRefreshJobTokensNoTranscriptsWritesNothing(t *testing.T) {
	st := useTestStore(t)
	j := &Job{ID: 12, Slug: "ghost", Repo: "kraken", State: "working"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	wrote, err := refreshJobTokens(st, j, repo, nil)
	if err != nil {
		t.Fatalf("refreshJobTokens: %v", err)
	}
	if wrote {
		t.Fatal("a job with no transcripts must not write a row")
	}
	if _, err := st.GetJobTokens(j.ID); err == nil {
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

	idle := &Job{ID: 14, Slug: "idle", Repo: "kraken", State: "done"}
	tokTranscript(t, CellWorktree(idle, repo), "sess.jsonl", 5, 1)

	d := newDaemon()
	now := time.Now()
	d.tokensPass([]*Job{idle}, map[string]bool{}, now)
	row, err := st.GetJobTokens(idle.ID)
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
	row, _ = st.GetJobTokens(idle.ID)
	if row.Out != 5 {
		t.Fatalf("Out = %d, want the idle job to still be throttled", row.Out)
	}

	// a live cell is never throttled
	d.tokensPass([]*Job{idle}, map[string]bool{CellSession(idle): true}, now.Add(time.Minute))
	row, _ = st.GetJobTokens(idle.ID)
	if row.Out != 505 {
		t.Fatalf("Out = %d, want 505 — a live cell is summed every tick", row.Out)
	}

	// past the interval, an idle job is due again
	tokTranscript(t, CellWorktree(idle, repo), "sess3.jsonl", 1, 1)
	d.tokensPass([]*Job{idle}, map[string]bool{}, now.Add(tokensIdleInterval+time.Minute))
	row, _ = st.GetJobTokens(idle.ID)
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

	draft := &Job{ID: 15, Slug: "draft", Repo: "kraken"} // State "" — never started
	tokTranscript(t, CellWorktree(draft, repo), "sess.jsonl", 5, 1)

	d := newDaemon()
	d.tokensPass([]*Job{draft}, map[string]bool{}, time.Now())
	if _, err := st.GetJobTokens(draft.ID); err == nil {
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
	d.tokensPass([]*Job{{ID: 1, Slug: "x", Repo: "kraken", State: "working"}}, map[string]bool{}, time.Now())
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
		j := &Job{ID: int64(100 + i), Slug: "job" + string(rune('a'+i)), Repo: "kraken", State: "done"}
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
	j := &Job{ID: 13, Slug: "big", Repo: "kraken", State: "working"}
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
	row, gerr := st.GetJobTokens(j.ID)
	if gerr != nil {
		t.Fatalf("GetJobTokens: %v", gerr)
	}
	if row.Out != 100 {
		t.Fatalf("Out = %d, want the 100 it read before the scan stopped", row.Out)
	}
}

// --- fix round 1 ---

// TestJobProjectDirsIncludesDeeperCwds pins the coverage fix: Claude Code
// buckets a transcript by the agent's STARTUP CWD, not the worktree root, so
// a teammate spawned in a subpackage writes into a sibling project dir that
// extends the worktree's munged name. Missing those cost 5.0% of all cell
// spend on this machine and 26.2% for one job — a per-job-varying under-count,
// which is worse than a flat one because it makes jobs incomparable.
func TestJobProjectDirsIncludesDeeperCwds(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 18, Slug: "demo", Repo: "monolith"}
	repo := filepath.Join(util.Home(), "Developer", "monolith")
	root := filepath.Join(util.Home(), ".claude", "projects")
	base := core.ProjectDirName(CellWorktree(j, repo))

	// A DIFFERENT job whose slug merely shares a prefix — the shape archive
	// numbering produces — must not be absorbed.
	sibling := core.ProjectDirName(CellWorktree(&Job{ID: 20, Slug: "demo-2", Repo: "monolith"}, repo))
	for _, name := range []string{
		base,                         // the worktree root itself
		base + "-internal-dashboard", // an agent started in a subpackage
		base + "-ml-asr-pipeline",    // another
		sibling,                      // slug demo-2, NOT this job
		"-Users-somebody-else",       // unrelated
	} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	got := map[string]bool{}
	dirs, err := JobProjectDirs(j, repo)
	if err != nil {
		t.Fatalf("JobProjectDirs: %v", err)
	}
	for _, d := range dirs {
		got[filepath.Base(d)] = true
	}
	for _, want := range []string{base, base + "-internal-dashboard", base + "-ml-asr-pipeline"} {
		if !got[want] {
			t.Errorf("missing %q from %v", want, got)
		}
	}
	if got[sibling] {
		t.Errorf("slug demo-2's dir %q was absorbed into slug demo — prefix matching must stay inside the job", sibling)
	}
	if got["-Users-somebody-else"] {
		t.Error("an unrelated project dir must not be absorbed")
	}
}

// TestRefreshJobTokensCarriesWatermarksThroughAnIncompleteScan is the
// watermark-loss double count. An unlistable subtree leaves its files out of
// the returned offsets, and SetJobTokenOffsets REPLACES the set — so without
// the carry-forward those watermarks are deleted and the files are re-summed
// from zero next pass into counters that only ever accumulate.
func TestRefreshJobTokensCarriesWatermarksThroughAnIncompleteScan(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test needs")
	}
	st := useTestStore(t)
	j := &Job{ID: 11, Slug: "demo", Repo: "kraken", State: "working"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	wt := CellWorktree(j, repo)
	tokTranscript(t, wt, "sess.jsonl", 100, 1)
	tokTranscript(t, wt, filepath.Join("sess", "subagents", "agent-1.jsonl"), 500, 1)

	if _, err := refreshJobTokens(st, j, repo, nil); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	before, _ := st.GetJobTokens(j.ID)
	offs, _ := st.JobTokenOffsets(j.ID)
	if len(offs) != 2 {
		t.Fatalf("want 2 watermarks, got %v", offs)
	}

	sub := filepath.Join(util.Home(), ".claude", "projects",
		core.ProjectDirName(wt), "sess", "subagents")
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(sub, 0o755) })

	if _, err := refreshJobTokens(st, j, repo, offs); err == nil {
		t.Fatal("an incomplete scan must report itself")
	}
	kept, _ := st.JobTokenOffsets(j.ID)
	if len(kept) != 2 {
		t.Fatalf("watermarks after an incomplete scan = %v, want both kept", kept)
	}

	// readable again: the sidecar must NOT be re-added on top of itself
	os.Chmod(sub, 0o755)
	if _, err := refreshJobTokens(st, j, repo, kept); err != nil {
		t.Fatalf("recovery refresh: %v", err)
	}
	after, _ := st.GetJobTokens(j.ID)
	if after.Out != before.Out {
		t.Fatalf("Out %d -> %d: the sidecar was counted twice across the outage", before.Out, after.Out)
	}
}

// TestTokensPassUnresolvableRepoDoesNotStarveTheIdleBatch: a job whose repo no
// longer resolves used to consume an idle slot and bail WITHOUT stamping its
// throttle clock, so it was due again on the very next tick forever — with
// tokensIdleBatch such jobs on the board, no other idle job was ever refreshed
// again, and silently, since a repo bail logs nothing.
func TestTokensPassUnresolvableRepoDoesNotStarveTheIdleBatch(t *testing.T) {
	st := useTestStore(t)
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir fake repo: %v", err)
	}

	var jobs []*Job
	for i := 0; i < tokensIdleBatch+2; i++ {
		jobs = append(jobs, &Job{ID: int64(200 + i), Slug: "dead" + string(rune('a'+i)), Repo: "no-such-repo-xyz", State: "done"})
	}
	real := &Job{ID: 16, Slug: "real", Repo: "kraken", State: "done"}
	tokTranscript(t, CellWorktree(real, repo), "sess.jsonl", 42, 1)
	jobs = append(jobs, real)

	d := newDaemon()
	now := time.Now()
	for tick := 0; tick < 5; tick++ {
		d.tokensPass(jobs, map[string]bool{}, now.Add(time.Duration(tick)*15*time.Second))
	}
	if _, err := st.GetJobTokens(real.ID); err != nil {
		t.Fatalf("the resolvable job was starved out of every tick: %v", err)
	}
}

// TestBumpJobTokensDoesNotCreepUpdatedAtWithoutSpend: a new transcript file
// appearing bumps `sessions` with a zero delta, which on a live cell happens
// constantly (a session's first record is a user turn). Moving the timestamp
// then makes both drilldowns render "measured 3s ago" on a job that has been
// idle for hours.
func TestBumpJobTokensDoesNotCreepUpdatedAtWithoutSpend(t *testing.T) {
	st := useTestStore(t)
	if err := st.BumpJobTokens(21, store.JobTokens{Out: 7, Sessions: 1, UpdatedAt: 1000}); err != nil {
		t.Fatalf("first: %v", err)
	}
	// a pass that found a new file but no spend
	if err := st.BumpJobTokens(21, store.JobTokens{Sessions: 2, UpdatedAt: 9999}); err != nil {
		t.Fatalf("second: %v", err)
	}
	row, err := st.GetJobTokens(21)
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if row.UpdatedAt != 1000 {
		t.Errorf("UpdatedAt = %d, want it to stay at the last pass that found spend", row.UpdatedAt)
	}
	if row.Sessions != 2 {
		t.Errorf("Sessions = %d, want the file count to update regardless", row.Sessions)
	}
	// real spend moves it again
	if err := st.BumpJobTokens(21, store.JobTokens{Out: 1, Sessions: 2, UpdatedAt: 12345}); err != nil {
		t.Fatalf("third: %v", err)
	}
	row, _ = st.GetJobTokens(21)
	if row.UpdatedAt != 12345 {
		t.Errorf("UpdatedAt = %d, want 12345 once spend was found", row.UpdatedAt)
	}
}

// TestJobProjectDirsReportsAShortList is round 2's blocking finding. A
// projects root that cannot be listed drops every DEEPER dir from the answer,
// and a short list is indistinguishable from "those dirs went away" — so the
// caller's replace-all write deletes their watermarks and re-sums those files
// from zero into counters that only accumulate. An ABSENT root is different
// and must stay silent: that is a machine with no transcripts at all.
func TestJobProjectDirsReportsAShortList(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test needs")
	}
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 19, Slug: "demo", Repo: "kraken"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	root := filepath.Join(util.Home(), ".claude", "projects")

	// no projects dir at all — not a degradation
	if got, err := JobProjectDirs(j, repo); err != nil {
		t.Fatalf("an absent projects dir must not be an error: %v (dirs %v)", err, got)
	}

	base := core.ProjectDirName(CellWorktree(j, repo))
	for _, n := range []string{base, base + "-ios"} {
		if err := os.MkdirAll(filepath.Join(root, n), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}
	full, err := JobProjectDirs(j, repo)
	if err != nil || len(full) != 5 {
		t.Fatalf("dirs = %v (err %v), want the four bases plus the deeper one", full, err)
	}

	// 0111: traversable but NOT listable — the EMFILE / permission-blip shape
	if err := os.Chmod(root, 0o111); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) })

	short, err := JobProjectDirs(j, repo)
	if err == nil {
		t.Fatal("an unlistable projects root must be reported — a silently short list deletes watermarks")
	}
	if len(short) != 4 {
		t.Errorf("dirs = %v, want the four bases still returned alongside the error", short)
	}
}

// TestRefreshJobTokensSurvivesAnUnlistableProjectsRoot is the same defect end
// to end, and the number is the point: before the fix a single failed ReadDir
// turned a true 600 into a permanent 1100, with nothing logged, nothing on the
// wire, and no reset path.
func TestRefreshJobTokensSurvivesAnUnlistableProjectsRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the permission bits this test needs")
	}
	st := useTestStore(t)
	j := &Job{ID: 11, Slug: "demo", Repo: "kraken", State: "working"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")
	wt := CellWorktree(j, repo)
	tokTranscript(t, wt, "sess.jsonl", 100, 1)        // the base dir
	tokTranscript(t, wt+"/ios", "sess.jsonl", 500, 1) // a DEEPER dir
	root := filepath.Join(util.Home(), ".claude", "projects")

	if _, err := refreshJobTokens(st, j, repo, nil); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	row, _ := st.GetJobTokens(j.ID)
	offs, _ := st.JobTokenOffsets(j.ID)
	if row.Out != 600 || len(offs) != 2 {
		t.Fatalf("setup: Out=%d watermarks=%d, want 600 and 2", row.Out, len(offs))
	}

	if err := os.Chmod(root, 0o111); err != nil {
		t.Skipf("cannot chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(root, 0o755) })
	if _, err := refreshJobTokens(st, j, repo, offs); err == nil {
		t.Fatal("an unlistable projects root must be reported")
	}
	kept, _ := st.JobTokenOffsets(j.ID)
	if len(kept) != 2 {
		t.Fatalf("watermarks = %d, want both carried forward through the outage", len(kept))
	}

	os.Chmod(root, 0o755)
	if _, err := refreshJobTokens(st, j, repo, kept); err != nil {
		t.Fatalf("recovery refresh: %v", err)
	}
	row, _ = st.GetJobTokens(j.ID)
	if row.Out != 600 {
		t.Fatalf("Out = %d, want 600 — the deeper dir was re-summed on top of itself", row.Out)
	}
}

// --- the <slug>-<id> cutover ---

// TestJobProjectDirsKeepsLegacySlugBase is the single most consequential line
// of this change, and it is consequential precisely because its absence is
// SILENT. A cell dir is now `<slug>-<id>`, but every transcript written before
// the cutover is filed by Claude Code under the bare-`<slug>` path — Claude
// Code buckets by the agent's startup cwd and knows nothing about kraken's
// rename. The v11 migration DROPS every token row and the daemon re-sums from
// scratch, so without the legacy base every job on the board reports zero for
// its entire history and nothing anywhere says why.
func TestJobProjectDirsKeepsLegacySlugBase(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 36, Slug: "helper-catalog", Repo: "kraken"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")

	dirs, err := JobProjectDirs(j, repo)
	if err != nil {
		t.Fatalf("JobProjectDirs: %v", err)
	}
	got := map[string]bool{}
	for _, d := range dirs {
		got[filepath.Base(d)] = true
	}
	for _, want := range []string{
		core.ProjectDirName(CellWorktree(j, repo)),                 // …-cells-helper-catalog-36-wt-kraken
		core.ProjectDirName(LegacyCellWorktree(j, repo)),           // …-cells-helper-catalog-wt-kraken
		core.ProjectDirName(filepath.Join(cellDir(j), "wt")),       // …-cells-helper-catalog-36-wt
		core.ProjectDirName(filepath.Join(legacyCellDir(j), "wt")), // …-cells-helper-catalog-wt
	} {
		if !got[want] {
			t.Errorf("missing base %q from %v", want, dirs)
		}
	}

	// deduped: a job whose id somehow produced the same munged name must not
	// be scanned twice — SumTokensDelta would count it into an accumulating
	// counter both times.
	if len(got) != len(dirs) {
		t.Fatalf("dirs = %v, want no duplicate bases", dirs)
	}
}

// TestJobProjectDirsSeparatorAnchoring pins the pair the id itself cannot
// separate: slug `demo` id 25 gives cell name `demo-25`, and slug `demo-2` id
// 5 gives `demo-2-5`. Neither name is a prefix of the other, but `demo-2` IS a
// prefix of `demo-25` — so only the `-wt` anchor keeps their deeper-cwd
// project dirs apart. Prefixing on the cell dir instead of the worktree would
// collide on exactly this pair.
func TestJobProjectDirsSeparatorAnchoring(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(util.Home(), "Developer", "monolith")
	root := filepath.Join(util.Home(), ".claude", "projects")

	demo := &Job{ID: 25, Slug: "demo", Repo: "monolith"}
	demo2 := &Job{ID: 5, Slug: "demo-2", Repo: "monolith"}

	demoBase := core.ProjectDirName(CellWorktree(demo, repo))
	demo2Base := core.ProjectDirName(CellWorktree(demo2, repo))
	if demoBase == demo2Base {
		t.Fatal("fixture is wrong: the two jobs must munge to different bases")
	}
	for _, name := range []string{
		demoBase, demoBase + "-ios",
		demo2Base, demo2Base + "-internal-dashboard",
		// the legacy bare-slug dirs of both, which is where the prefixes
		// actually get close: `…-cells-demo-wt-monolith` vs
		// `…-cells-demo-2-wt-monolith`.
		core.ProjectDirName(LegacyCellWorktree(demo, repo)),
		core.ProjectDirName(LegacyCellWorktree(demo2, repo)),
	} {
		if err := os.MkdirAll(filepath.Join(root, name), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", name, err)
		}
	}

	claim := func(j *Job) map[string]bool {
		dirs, err := JobProjectDirs(j, repo)
		if err != nil {
			t.Fatalf("JobProjectDirs: %v", err)
		}
		out := map[string]bool{}
		for _, d := range dirs {
			out[filepath.Base(d)] = true
		}
		return out
	}
	demoGot, demo2Got := claim(demo), claim(demo2)

	if !demoGot[demoBase+"-ios"] {
		t.Error("job 25 lost its own deeper cwd")
	}
	if !demo2Got[demo2Base+"-internal-dashboard"] {
		t.Error("job 5 lost its own deeper cwd")
	}
	if demoGot[demo2Base] || demoGot[demo2Base+"-internal-dashboard"] {
		t.Errorf("slug demo (id 25) absorbed slug demo-2's dirs: %v", demoGot)
	}
	if demo2Got[demoBase] || demo2Got[demoBase+"-ios"] {
		t.Errorf("slug demo-2 (id 5) absorbed slug demo's dirs: %v", demo2Got)
	}
	// and the legacy bases stay apart too — `…-cells-demo-wt` must not sweep
	// in `…-cells-demo-2-wt-monolith`
	if demoGot[core.ProjectDirName(LegacyCellWorktree(demo2, repo))] {
		t.Errorf("the legacy bare-slug base absorbed a different slug's legacy dir: %v", demoGot)
	}
}

// TestRefreshJobTokensSumsThePreCutoverTranscripts is the same rule end to
// end: a job whose only transcripts are pre-cutover (written under the bare
// slug) must still report its spend after the v11 re-sum, not zero.
func TestRefreshJobTokensSumsThePreCutoverTranscripts(t *testing.T) {
	st := useTestStore(t)
	j := &Job{ID: 42, Slug: "old-job", Repo: "kraken", State: "done"}
	repo := filepath.Join(util.Home(), "Developer", "kraken")

	// nothing under the CURRENT cell name — this job predates the cutover
	tokTranscript(t, LegacyCellWorktree(j, repo), "sess.jsonl", 250, 2)

	wrote, err := refreshJobTokens(st, j, repo, nil)
	if err != nil {
		t.Fatalf("refreshJobTokens: %v", err)
	}
	if !wrote {
		t.Fatal("a job with only pre-cutover transcripts must still be measured")
	}
	row, err := st.GetJobTokens(j.ID)
	if err != nil {
		t.Fatalf("GetJobTokens: %v", err)
	}
	if row.Out != 500 {
		t.Fatalf("Out = %d, want 500 — the legacy bare-slug base was not summed", row.Out)
	}
}
