package jobs

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// graph_test.go — the graph pass' pacing and the graph job creator.
//
// It used to be mostly cursor arithmetic: five ingest sweeps whose watermark
// rules were core.SumTokensDelta's restated for a different payload, every one
// of which failed SILENTLY when wrong. Those sweeps and their tests are gone
// (see graph.go's head comment) — the graph is compiled from carl's mental
// model now, so what is left here is the throttle, the nightly kick's guards,
// and the job creator's contract.

func graphTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// TestCapStringKeepsRunesWhole guards the content column: a cut through a
// multi-byte character is a corrupt row, not a shorter one.
func TestCapStringKeepsRunesWhole(t *testing.T) {
	s := strings.Repeat("知", 10) // 30 bytes
	got := capString(s, 10)      // 10 is not a rune boundary (3 | 10 is false)
	head := strings.TrimSuffix(got, "\n…[truncated]")
	if len(head)%3 != 0 {
		t.Errorf("cut mid-rune: head is %d bytes", len(head))
	}
	if !strings.HasSuffix(got, "[truncated]") {
		t.Error("truncation is not marked")
	}
	if capString("short", 100) != "short" {
		t.Error("a string under the cap was altered")
	}
}

// RETIRED WITH THE SWEEPS. TestAdvanceCursorNeverRegresses,
// TestSweepRowsIngestsOnceThenSettles, TestSweepRowsReIngestsAnEditedSpec,
// TestSweepTranscriptsAdvancesAndSettles, TestTranscriptFilesFindsSidecars,
// TestTranscriptFilesAbsentProjectsDirIsNotAnError and the six readSpan cases
// above them stood here. They were the cursor arithmetic's whole safety net,
// and every one of them tested a function this change deletes — the graph is
// compiled from carl's mental model now, not swept from rows and transcripts,
// so there is no cursor left to park and no span left to split. The coverage
// they carried moves with the mechanism: mm.go's mint and verify pass, and the
// blind suites written against them.

// TestCreateGraphJobBothModes pins the creator's contract: a row the existing
// queue pass can start unmodified, whose spec reads the skill BY PATH.
func TestCreateGraphJobBothModes(t *testing.T) {
	for _, c := range []struct {
		name, mode, instruction string
		wants                   []string
	}{
		{"ingest", GraphModeIngest, "", []string{"Mode: ingest", "k graph pending", "opens no PR"}},
		{"revision", GraphModeRevision, "merge `zeus` and `zeus agent` — they are one thing",
			[]string{"Mode: revision", "merge `zeus` and `zeus agent`"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			st := graphTestStore(t)
			j, err := CreateGraphJob(st, "graph-"+c.name, c.mode, c.instruction, 0)
			if err != nil {
				t.Fatalf("CreateGraphJob: %v", err)
			}
			if j.ID == 0 || j.Slug != "graph-"+c.name {
				t.Fatalf("job = %+v", j)
			}
			if j.State != "" {
				t.Errorf("a freshly created graph job must be a draft, got state %q — the tick submits it and SubmitJob refuses a started job", j.State)
			}
			if j.Err != "" {
				t.Errorf("job has a parse error: %s", j.Err)
			}
			// By PATH, never by /name: a personal skill of the same name
			// shadows the repo-owned copy materialized into the worktree, and
			// only the repo-owned copy knows about cells.
			if !strings.Contains(j.Body, ".claude/skills/graph/SKILL.md") {
				t.Error("spec does not point at the skill by path")
			}
			if strings.Contains(j.Body, "invoke `/graph`") && !strings.Contains(j.Body, "rather than invoking `/graph`") {
				t.Error("spec invokes the skill by name")
			}
			for _, want := range c.wants {
				if !strings.Contains(j.Body, want) {
					t.Errorf("spec is missing %q:\n%s", want, j.Body)
				}
			}
		})
	}
}

// TestCreateGraphJobRefusesBadInput: a revision job with nothing to revise
// would spawn a real cell to do nothing, and an unknown mode would spawn one
// with no instructions at all. Both are cheaper to refuse than to explain.
func TestCreateGraphJobRefusesBadInput(t *testing.T) {
	st := graphTestStore(t)
	if _, err := CreateGraphJob(st, "graph-x", GraphModeRevision, "   ", 0); err == nil {
		t.Error("a revision job with a blank instruction was created")
	}
	if _, err := CreateGraphJob(st, "graph-y", "somethingelse", "x", 0); err == nil {
		t.Error("a job with an unknown mode was created")
	}
	if _, err := CreateGraphJob(nil, "graph-z", GraphModeIngest, "", 0); err == nil {
		t.Error("a job was created against a nil store")
	}
}

// TestGraphPassThrottles pins the pacing rule tokensPass states: the stamp is
// written BEFORE any work can bail, so a pass that fails on its first query
// has still had its turn rather than retrying every 15-second tick.
func TestGraphPassThrottles(t *testing.T) {
	// A real HOME would put this test's transcript walk over the machine's
	// whole ~/.claude/projects tree — 3,050 files — for no assertion.
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)
	// Registered AFTER graphTestStore's Close, so it runs BEFORE it (cleanups
	// are LIFO): the background half of the pass holds this store handle, and
	// closing it underneath that goroutine is a panic in a goroutine no test
	// can recover.
	t.Cleanup(waitGraphSlow)
	graphLastPass.Store(0)
	t.Cleanup(func() { graphLastPass.Store(0) })

	now := time.UnixMilli(1_700_000_000_000)
	graphPass(st, now)
	stamped := graphLastPass.Load()
	if stamped != now.UnixMilli() {
		t.Fatalf("throttle stamp = %d, want %d", stamped, now.UnixMilli())
	}
	// A tick a second later must be a no-op, which is observable as the stamp
	// not moving.
	graphPass(st, now.Add(time.Second))
	if graphLastPass.Load() != stamped {
		t.Error("the pass ran again inside its own interval")
	}
	later := now.Add(graphPassInterval + time.Second)
	graphPass(st, later)
	if graphLastPass.Load() != later.UnixMilli() {
		t.Error("the pass did not run after its interval elapsed")
	}
}

// waitGraphSlow blocks until the background half of the pass has finished with
// its store handle.
func waitGraphSlow() {
	for graphSlowRunning.Load() {
		time.Sleep(time.Millisecond)
	}
}

// TestGraphPassNilStoreIsANoOp: file-mode entrypoints (the viewer, `k` itself)
// have no graph, and reaching for one would panic on every tick.
func TestGraphPassNilStoreIsANoOp(t *testing.T) {
	graphLastPass.Store(0)
	t.Cleanup(func() { graphLastPass.Store(0) })
	graphPass(nil, time.UnixMilli(1))
	if graphLastPass.Load() != 0 {
		t.Error("a nil store consumed the pass' throttle slot")
	}
}

// kickGraph's guards, each of which prevents a cell storm: no pending → no
// job; one per local day; an active graph job blocks a second.
func TestKickGraphGuards(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)
	now := time.Date(2026, 7, 31, 3, 0, 0, 0, time.Local)

	// Empty queue: no job, and the date must stay unstamped so a queue that
	// fills later the same day still gets its job.
	if j := kickGraph(st, now); j != nil {
		t.Fatalf("empty queue created %q", j.Slug)
	}
	if _, err := st.GetKV(graphIngestKVKey); err == nil {
		t.Fatal("empty-queue decline stamped the date; a later fill today would be skipped")
	}

	if _, _, err := st.AddEpisode(store.Episode{Kind: "feedback", Hash: "k1"}); err != nil {
		t.Fatal(err)
	}
	j := kickGraph(st, now)
	if j == nil {
		t.Fatal("pending queue produced no job")
	}
	if j.Slug != "graph-2026-07-31" || j.State != "" {
		t.Fatalf("job = %+v", j)
	}
	if !strings.Contains(j.Body, ".claude/skills/graph/SKILL.md") {
		t.Errorf("spec does not read the skill by path: %q", j.Body)
	}

	// Same day again: the date stamp holds.
	if j2 := kickGraph(st, now.Add(time.Hour)); j2 != nil {
		t.Fatalf("second kick same day created %q", j2.Slug)
	}
	// Next day, queue still pending, and yesterday's job left as a DRAFT: its
	// start failed, so it will never drain anything on its own. Declining for
	// it would stop the ingest queue forever with nothing on screen saying
	// why — so the kick goes ahead.
	j3 := kickGraph(st, now.Add(24*time.Hour))
	if j3 == nil {
		t.Fatal("a stuck graph DRAFT blocked tonight's kick — only a running one may")
	}

	// Now put that job into a state where a cell actually exists. The day
	// after, the guard declines: two cells writing k graph add concurrently
	// doubles the extraction spend on one queue.
	row, err := st.GetJobBySlug(j3.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateJobFields(row.ID, row.Rev, map[string]any{"state": "working"}); err != nil {
		t.Fatal(err)
	}
	if j4 := kickGraph(st, now.Add(48*time.Hour)); j4 != nil {
		t.Fatalf("kick with a RUNNING graph job created %q", j4.Slug)
	}
}
