package jobs

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// ingestcap_contract_test.go — the CORRECTNESS pins for slice S2a of
// artifacts/50-contract.md: the nightly graph ingest job's per-run episode cap
// now scales with the backlog instead of sitting at a fixed 25.
//
// The cap is the ONE number that connects three processes: kickGraph computes
// it, the job body states it, and a cell reads it back out of that body hours
// later. Nothing in the running system re-derives it, so the body's line is
// the whole interface — which is why it is pinned as an exact line here and
// not as a substring.

const kgcontractCapPrefix = "Per-run episode cap:"

// kgcontractCapLines returns every line of a job body that states a cap. A
// body with two of them is as broken as a body with none: the reader takes
// "the" cap from "the" line.
func kgcontractCapLines(body string) []string {
	var out []string
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(ln), kgcontractCapPrefix) {
			out = append(out, strings.TrimSpace(ln))
		}
	}
	return out
}

// kgcontractOneCapLine asserts the body carries exactly one cap line and
// returns it.
func kgcontractOneCapLine(t *testing.T, body string) string {
	t.Helper()
	lines := kgcontractCapLines(body)
	if len(lines) != 1 {
		t.Fatalf("job body carries %d %q lines, want exactly 1:\n%s", len(lines), kgcontractCapPrefix, body)
	}
	return lines[0]
}

// TestKgcontractGraphIngestCapTable is the contract's executed value table,
// transcribed row for row. It is a table and not a formula on purpose: the
// clamp bounds and the integer division are each a place a plausible
// implementation differs (round-up, float division, exclusive bounds), and the
// rows at 499/500 and 2999/3000 are where those differences show.
func TestKgcontractGraphIngestCapTable(t *testing.T) {
	for _, c := range []struct {
		pending int64
		want    int
	}{
		{0, 25},      // no backlog at all — the floor, though kickGraph never gets here
		{1, 25},      // 1/20 = 0, clamped up
		{499, 25},    // 499/20 = 24, still under the floor
		{500, 25},    // 500/20 = 25 — the floor met exactly, not exceeded
		{999, 49},    // 999/20 = 49 by INTEGER division, not 49.95 rounded
		{1000, 50},   // the linear middle
		{2091, 104},  // 2091/20 = 104 (integer), not 105
		{2999, 149},  // 2999/20 = 149 — one under the ceiling
		{3000, 150},  // 3000/20 = 150 — the ceiling met exactly
		{10000, 150}, // far past it: clamped, never 500
	} {
		t.Run(fmt.Sprintf("pending_%d", c.pending), func(t *testing.T) {
			if got := graphIngestCap(c.pending); got != c.want {
				t.Errorf("graphIngestCap(%d) = %d, want %d", c.pending, got, c.want)
			}
		})
	}

	// Monotonic across the whole range: a bigger backlog never earns a smaller
	// cap. A clamp written with its bounds swapped satisfies several of the
	// rows above and fails this.
	prev := graphIngestCap(0)
	for pending := int64(0); pending <= 4000; pending += 7 {
		got := graphIngestCap(pending)
		if got < prev {
			t.Fatalf("graphIngestCap(%d) = %d, below the previous %d — the cap must never shrink as the backlog grows", pending, got, prev)
		}
		if got < 25 || got > 150 {
			t.Fatalf("graphIngestCap(%d) = %d, outside the [25, 150] clamp", pending, got)
		}
		prev = got
	}
}

// TestKgcontractCreateGraphJobStatesTheCapForIngest pins the body line an
// ingest cell reads. The cap arrives as a NUMBER on its own line because the
// reader is a skill parsing prose: `Per-run episode cap: 87` is what
// `.claude/skills/graph/SKILL.md` step 1 is told to look for, and a body that
// stated it any other way would silently fall back to 25 for every run.
func TestKgcontractCreateGraphJobStatesTheCapForIngest(t *testing.T) {
	st := graphTestStore(t)

	j, err := CreateGraphJob(st, "graph-ingest-capped", GraphModeIngest, "", 87)
	if err != nil {
		t.Fatalf("CreateGraphJob: %v", err)
	}
	if got, want := kgcontractOneCapLine(t, j.Body), kgcontractCapPrefix+" 87"; got != want {
		t.Errorf("cap line = %q, want %q", got, want)
	}
	// The job is still an ordinary submittable ingest job — the cap is an
	// addition, not a replacement for anything the tick's submit or the cell
	// already reads.
	if j.State != "" || j.Err != "" {
		t.Errorf("job = {state:%q err:%q}, want a draft with no parse error", j.State, j.Err)
	}
	if !strings.Contains(j.Body, ".claude/skills/graph/SKILL.md") {
		t.Errorf("spec no longer points at the skill by path:\n%s", j.Body)
	}
	if !strings.Contains(j.Body, "Mode: ingest") {
		t.Errorf("spec no longer names its mode:\n%s", j.Body)
	}
}

// TestKgcontractCreateGraphJobFallsBackToTwentyFive: an unstated cap is the
// old default, not zero episodes. Hand-made jobs and any caller that has not
// been taught about the parameter must keep producing a night's work.
func TestKgcontractCreateGraphJobFallsBackToTwentyFive(t *testing.T) {
	st := graphTestStore(t)

	j, err := CreateGraphJob(st, "graph-ingest-uncapped", GraphModeIngest, "", 0)
	if err != nil {
		t.Fatalf("CreateGraphJob: %v", err)
	}
	if got, want := kgcontractOneCapLine(t, j.Body), kgcontractCapPrefix+" 25"; got != want {
		t.Errorf("cap line = %q, want %q — an ingestCap of 0 means the historical default, not no work", got, want)
	}
}

// TestKgcontractCreateGraphJobRevisionCarriesNoCap: revision mode drains no
// episode queue, so a cap there would be a number with nothing to bound — and
// a cell that read one would have to decide what it meant. The contract is
// that the revision body is unchanged WHATEVER ingestCap says, so a nonzero
// one is the case that matters.
func TestKgcontractCreateGraphJobRevisionCarriesNoCap(t *testing.T) {
	st := graphTestStore(t)

	const instruction = "merge `zeus` and `zeus agent` — they are one thing"
	for _, capValue := range []int{0, 25, 150} {
		t.Run(fmt.Sprintf("ingestCap_%d", capValue), func(t *testing.T) {
			j, err := CreateGraphJob(st, fmt.Sprintf("graph-revision-%d", capValue), GraphModeRevision, instruction, capValue)
			if err != nil {
				t.Fatalf("CreateGraphJob: %v", err)
			}
			if lines := kgcontractCapLines(j.Body); len(lines) != 0 {
				t.Errorf("a revision job states a cap %v:\n%s", lines, j.Body)
			}
			if !strings.Contains(j.Body, "Mode: revision") || !strings.Contains(j.Body, instruction) {
				t.Errorf("revision body no longer carries its mode and instruction:\n%s", j.Body)
			}
		})
	}
}

// TestKgcontractKickGraphWritesTheComputedCap is the wiring pin: the number in
// the body must be the one graphIngestCap derives from the backlog kickGraph
// measured. A correct cap function wired to a constant call site is the
// failure mode this catches, and it is invisible in every other test.
func TestKgcontractKickGraphWritesTheComputedCap(t *testing.T) {
	// A real HOME would put kickGraph's neighbours over this machine's whole
	// ~/.claude/projects tree for no assertion.
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)

	// 600 pending episodes → 600/20 = 30, comfortably inside the clamp so the
	// assertion distinguishes the computed value from BOTH bounds.
	const pending = 600
	for i := 0; i < pending; i++ {
		if _, _, err := st.AddEpisode(store.Episode{
			Kind: "feedback", Ref: "r", Content: "c", Hash: fmt.Sprintf("kgcontract-cap-%04d", i), TS: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := st.GetGraphStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingEpisodes != pending {
		t.Fatalf("fixture is wrong: PendingEpisodes = %d, want %d", stats.PendingEpisodes, pending)
	}

	j := kickGraph(st, time.Date(2026, 8, 3, 3, 0, 0, 0, time.Local))
	if j == nil {
		t.Fatal("a 600-episode backlog produced no job")
	}
	want := fmt.Sprintf("%s %d", kgcontractCapPrefix, graphIngestCap(pending))
	if got := kgcontractOneCapLine(t, j.Body); got != want {
		t.Errorf("cap line = %q, want %q — kickGraph must pass graphIngestCap(PendingEpisodes)", got, want)
	}
	// And that value is genuinely the scaled one, not either clamp bound
	// coincidentally matching.
	if graphIngestCap(pending) != 30 {
		t.Errorf("graphIngestCap(%d) = %d, want 30", pending, graphIngestCap(pending))
	}
}

// TestKgcontractKickGraphStormGuardsSurviveTheCap re-pins the three guards
// S2a says are unchanged, against the version of kickGraph that now computes a
// cap. Each one prevents a cell storm, and each one costs real tokens when it
// breaks — which is why they are re-stated beside the change rather than left
// only to the suite that predates it.
func TestKgcontractKickGraphStormGuardsSurviveTheCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)
	now := time.Date(2026, 8, 3, 3, 0, 0, 0, time.Local)

	// Guard 1: an empty queue creates nothing — and, just as importantly,
	// stamps no date, so a queue that fills later the same day still gets its
	// job.
	if j := kickGraph(st, now); j != nil {
		t.Fatalf("an empty queue created %q", j.Slug)
	}
	if _, err := st.GetKV(graphIngestKVKey); err == nil {
		t.Fatal("an empty-queue decline stamped the date; a later fill today would be skipped")
	}

	if _, _, err := st.AddEpisode(store.Episode{Kind: "feedback", Hash: "kgcontract-guard-1"}); err != nil {
		t.Fatal(err)
	}
	j := kickGraph(st, now)
	if j == nil {
		t.Fatal("a pending queue produced no job")
	}
	if j.State != "" || j.Err != "" {
		t.Errorf("job = %+v, want a clean draft the tick can submit", j)
	}
	// One pending episode is a backlog of 1: the floor, stated in the job.
	if got, want := kgcontractOneCapLine(t, j.Body), kgcontractCapPrefix+" 25"; got != want {
		t.Errorf("cap line = %q, want %q for a one-episode backlog", got, want)
	}

	// Guard 2: at most one job per local day.
	if j2 := kickGraph(st, now.Add(2*time.Hour)); j2 != nil {
		t.Fatalf("a second kick on the same local day created %q", j2.Slug)
	}

	// Guard 3: none while a RUNNING graph-* job exists. A leftover DRAFT is
	// not one — its start failed, and declining for it would wedge the queue
	// silently — so the guard has to be armed by putting the job into a state
	// where a cell exists.
	row, err := st.GetJobBySlug(j.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateJobFields(row.ID, row.Rev, map[string]any{"state": "working"}); err != nil {
		t.Fatal(err)
	}
	if j3 := kickGraph(st, now.Add(24*time.Hour)); j3 != nil {
		t.Fatalf("a kick with a running graph job created %q", j3.Slug)
	}
}
