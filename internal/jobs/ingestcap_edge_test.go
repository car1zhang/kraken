package jobs

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// ingestcap_edge_test.go — the EDGE dimension of Fix C: the clamp at every one
// of its inclusive bounds, the two ways the cap line can be absent from a job
// body, and the storm guards that must survive a change that only meant to
// resize the batch.
//
// Every identifier is prefixed `kgedge`; a sibling author shares this package's
// test binary.
//
// Why the boundaries and not the middle: clamp(pending/20, 25, 150) is written
// once and read by a nightly job that spends real tokens. An off-by-one at the
// lower bound spends 25 tokens' worth of cell on a backlog of 3,000 and the
// queue never drains; an off-by-one at the upper bound turns a 50,000-episode
// backlog into a cell that runs until its context dies. Neither shows up
// anywhere but the arithmetic.

// kgedgeCapLinePrefix is the exact line the ingest spec must carry. The skill
// parses it by this prefix, so the prefix — not merely "a number somewhere in
// the body" — is the contract.
const kgedgeCapLinePrefix = "Per-run episode cap:"

// kgedgeCapLine renders the whole expected line for a cap of n.
func kgedgeCapLine(n int) string { return fmt.Sprintf("%s %d", kgedgeCapLinePrefix, n) }

// TestKgedgeGraphIngestCapClampBoundaries walks every inclusive bound of
// clamp(pending/20, 25, 150), plus the two values on each side of each bound.
// Integer division is part of the contract: 999/20 is 49, not 49.95 rounded up.
func TestKgedgeGraphIngestCapClampBoundaries(t *testing.T) {
	for _, tc := range []struct {
		pending int64
		want    int
	}{
		// Below the lower knee: the floor holds regardless of how small the
		// backlog is. A nightly job that found one episode still gets a batch
		// worth spawning a cell for.
		{0, 25},
		{1, 25},
		{499, 25},
		// 500/20 = 25 exactly: the clamp's lower bound and the formula's
		// output agree here, which is what makes the knee continuous.
		{500, 25},
		{501, 25}, // 501/20 = 25 by integer division
		{519, 25},
		{520, 26}, // the first pending count the formula lifts off the floor
		// The middle of the proportional band.
		{999, 49}, // integer division: NOT 50
		{1000, 50},
		{2091, 104},
		// The upper knee. 2999/20 = 149 and 3000/20 = 150, so the ceiling is
		// reached exactly at 3,000 and holds above it.
		{2999, 149},
		{3000, 150},
		{3001, 150},
		{10000, 150},
	} {
		t.Run(fmt.Sprintf("pending=%d", tc.pending), func(t *testing.T) {
			if got := graphIngestCap(tc.pending); got != tc.want {
				t.Errorf("graphIngestCap(%d) = %d, want %d", tc.pending, got, tc.want)
			}
		})
	}
}

// TestKgedgeGraphIngestCapNeverEscapesItsBounds is the property behind the
// table: no reachable pending count — including the impossible ones a COUNT(*)
// can never return but an int64 can hold — produces a cap outside [25, 150].
// The huge value is the overflow guard: pending/20 on MaxInt64 is still an
// int64 that does not fit an int32, and a conversion before the clamp would
// wrap it negative on a 32-bit build.
func TestKgedgeGraphIngestCapNeverEscapesItsBounds(t *testing.T) {
	for _, pending := range []int64{
		math.MinInt64, -1_000_000, -1, 0, 1, 24, 25, 499, 500, 3000, 100_000,
		math.MaxInt32, math.MaxInt64,
	} {
		got := graphIngestCap(pending)
		if got < 25 || got > 150 {
			t.Errorf("graphIngestCap(%d) = %d, outside the clamp's [25, 150]", pending, got)
		}
	}
}

// TestKgedgeGraphIngestCapIsMonotonic: a bigger backlog never earns a smaller
// batch. Stated separately from the table because the table cannot see a
// non-monotonic formula that happens to pass at every sampled point.
func TestKgedgeGraphIngestCapIsMonotonic(t *testing.T) {
	prev := graphIngestCap(0)
	for pending := int64(1); pending <= 4000; pending += 7 {
		got := graphIngestCap(pending)
		if got < prev {
			t.Fatalf("graphIngestCap(%d) = %d, below the previous %d — the cap is not monotonic", pending, got, prev)
		}
		prev = got
	}
}

// TestKgedgeCreateGraphJobCapLineEdges covers the ingest body's cap line at the
// values where "what N should I print" is decided: a real cap, the 0 that means
// "the caller did not compute one", and the negatives a bug upstream could
// hand over. All three must produce EXACTLY ONE cap line — a second one, from
// (say) a template that already carried a default, gives the cell two
// instructions and no way to choose.
func TestKgedgeCreateGraphJobCapLineEdges(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ingestCap int
		want      int
	}{
		{"explicit cap", 137, 137},
		{"the clamp floor", 25, 25},
		{"the clamp ceiling", 150, 150},
		{"one is still one", 1, 1},
		{"zero means the 25 default", 0, 25},
		{"negative means the 25 default", -1, 25},
		{"very negative means the 25 default", -1000, 25},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := graphTestStore(t)
			j, err := CreateGraphJob(st, "graph-cap", GraphModeIngest, "", tc.ingestCap)
			if err != nil {
				t.Fatalf("CreateGraphJob: %v", err)
			}
			if n := strings.Count(j.Body, kgedgeCapLinePrefix); n != 1 {
				t.Fatalf("the spec carries %d %q lines, want exactly 1:\n%s", n, kgedgeCapLinePrefix, j.Body)
			}
			want := kgedgeCapLine(tc.want)
			if !strings.Contains(j.Body, want) {
				t.Errorf("spec is missing %q:\n%s", want, j.Body)
			}
			// The line must stand alone: the skill reads it off a line of its
			// own, so a cap glued to the end of a sentence is unparseable.
			found := false
			for _, ln := range strings.Split(j.Body, "\n") {
				if strings.TrimSpace(ln) == want {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%q is not a line of its own in:\n%s", want, j.Body)
			}
			if j.State != "" {
				t.Errorf("job must be created as a draft for the tick to submit, got state %q", j.State)
			}
			if j.Err != "" {
				t.Errorf("job has a parse error: %s", j.Err)
			}
		})
	}
}

// TestKgedgeCreateGraphJobRevisionCarriesNoCapLine: revision mode drains no
// queue, so a cap line there is an instruction with nothing to apply to — and
// a cell that reads one would cap the number of CORRECTIONS it makes. The
// ingestCap argument must be ignored entirely, at every value.
func TestKgedgeCreateGraphJobRevisionCarriesNoCapLine(t *testing.T) {
	for _, ingestCap := range []int{0, -1, 25, 137, 150, 10_000} {
		t.Run(fmt.Sprintf("cap=%d", ingestCap), func(t *testing.T) {
			st := graphTestStore(t)
			j, err := CreateGraphJob(st, "graph-rev", GraphModeRevision,
				"merge `zeus` and `zeus agent` — they are one thing", ingestCap)
			if err != nil {
				t.Fatalf("CreateGraphJob: %v", err)
			}
			if strings.Contains(j.Body, kgedgeCapLinePrefix) {
				t.Errorf("a revision spec carries a %q line:\n%s", kgedgeCapLinePrefix, j.Body)
			}
			// Still the revision job it was before Fix C.
			if !strings.Contains(j.Body, "Mode: revision") {
				t.Errorf("revision spec lost its mode line:\n%s", j.Body)
			}
			if !strings.Contains(j.Body, "merge `zeus` and `zeus agent`") {
				t.Errorf("revision spec lost carl's instruction:\n%s", j.Body)
			}
		})
	}
}

// TestKgedgeCreateGraphJobRefusalsIgnoreTheCap: the cap argument must not
// become a way past the existing gates. A blank revision instruction, an
// unknown mode and a nil store are refusals whatever cap is passed.
func TestKgedgeCreateGraphJobRefusalsIgnoreTheCap(t *testing.T) {
	for _, ingestCap := range []int{0, 25, 150} {
		st := graphTestStore(t)
		if _, err := CreateGraphJob(st, "graph-x", GraphModeRevision, "   ", ingestCap); err == nil {
			t.Errorf("cap %d: a revision job with a blank instruction was created", ingestCap)
		}
		if _, err := CreateGraphJob(st, "graph-y", "somethingelse", "x", ingestCap); err == nil {
			t.Errorf("cap %d: a job with an unknown mode was created", ingestCap)
		}
		if _, err := CreateGraphJob(nil, "graph-z", GraphModeIngest, "", ingestCap); err == nil {
			t.Errorf("cap %d: a job was created against a nil store", ingestCap)
		}
	}
}

// TestKgedgeKickGraphWritesTheBacklogScaledCap is the wiring: kickGraph must
// pass graphIngestCap(stats.PendingEpisodes), not a constant. The fixture sits
// deliberately just past the lower knee — 520 pending is the smallest backlog
// whose cap (26) is distinguishable from the 25 default, so a kickGraph that
// forgot to compute anything fails here rather than passing by coincidence.
func TestKgedgeKickGraphWritesTheBacklogScaledCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)

	const pending = 520
	for i := 0; i < pending; i++ {
		if _, _, err := st.AddEpisode(store.Episode{
			Kind: "feedback", Ref: "r", Content: "c", Hash: fmt.Sprintf("kgedge-%d", i), TS: 1,
		}); err != nil {
			t.Fatalf("seed episode %d: %v", i, err)
		}
	}
	stats, err := st.GetGraphStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.PendingEpisodes != pending {
		t.Fatalf("fixture has %d pending episodes, want %d", stats.PendingEpisodes, pending)
	}

	j := kickGraph(st, time.Date(2026, 7, 31, 3, 0, 0, 0, time.Local))
	if j == nil {
		t.Fatal("a 520-episode backlog produced no job")
	}
	want := kgedgeCapLine(graphIngestCap(pending))
	if !strings.Contains(j.Body, want) {
		t.Errorf("spec is missing %q — kickGraph did not scale the cap to the backlog:\n%s", want, j.Body)
	}
	if strings.Contains(j.Body, kgedgeCapLine(25)) {
		t.Errorf("spec carries the DEFAULT cap for a 520-episode backlog:\n%s", j.Body)
	}
}

// TestKgedgeKickGraphStormGuardsSurviveTheCap re-pins the three refusals beside
// the change that could break them. The pending==0 case is the load-bearing
// one: graphIngestCap(0) is a perfectly good 25, so a kickGraph restructured
// around "compute the cap, then create the job" would spawn a nightly cell to
// drain an empty queue — a real token spend with nothing to do — and the only
// symptom would be a job in the morning.
func TestKgedgeKickGraphStormGuardsSurviveTheCap(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := graphTestStore(t)
	now := time.Date(2026, 7, 31, 3, 0, 0, 0, time.Local)

	if j := kickGraph(st, now); j != nil {
		t.Fatalf("an EMPTY queue created %q — graphIngestCap(0) is 25, but there is nothing to cap", j.Slug)
	}
	// And the decline must not stamp the day: a queue that fills at noon still
	// deserves tonight's job.
	if _, err := st.GetKV(graphIngestKVKey); err == nil {
		t.Fatal("the empty-queue decline stamped the date watermark")
	}

	if _, _, err := st.AddEpisode(store.Episode{Kind: "feedback", Hash: "kgedge-one", TS: 1}); err != nil {
		t.Fatal(err)
	}
	first := kickGraph(st, now)
	if first == nil {
		t.Fatal("a pending queue produced no job")
	}
	// One pending episode is under the lower knee, so the floor applies.
	if !strings.Contains(first.Body, kgedgeCapLine(25)) {
		t.Errorf("a 1-episode backlog did not get the floor cap of 25:\n%s", first.Body)
	}

	// Same local day: the watermark holds, cap or no cap.
	if j := kickGraph(st, now.Add(11*time.Hour)); j != nil {
		t.Errorf("a second kick on the same local day created %q", j.Slug)
	}
	// A new day with the previous job RUNNING: blocked by the running-job
	// guard rather than by the date. The guard reads state working/blocked
	// rather than "not done", so it has to be armed — a leftover draft is a
	// FAILED start, and declining for one would wedge the ingest queue.
	row, err := st.GetJobBySlug(first.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateJobFields(row.ID, row.Rev, map[string]any{"state": "working"}); err != nil {
		t.Fatal(err)
	}
	if j := kickGraph(st, now.Add(24*time.Hour)); j != nil {
		t.Errorf("a kick with a running graph-* job created %q", j.Slug)
	}
}
