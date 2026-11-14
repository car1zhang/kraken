package jobs

import (
	"strings"
	"testing"
	"time"
)

// dreamoff_test.go — KRAKEN_DREAM=off is the operator's off switch for the
// nightly pass, and the periodic dream row is how main schedules that pass,
// so the knob has to be honoured on the row's path: a fresh row is seeded
// paused, and a firing of an existing row is skipped with a note the board
// can show. The stored watermark is left alone either way, so the first
// night back on sweeps from where the last pass stopped.

// Only the word off (any case, surrounding whitespace ignored) turns the pass
// off; unset, empty or any other value leaves it on, so a typo cannot
// silently switch it off.
func TestDreamEnabledReadsOnlyTheWordOff(t *testing.T) {
	for _, v := range []string{"off", "OFF", " Off "} {
		t.Setenv("KRAKEN_DREAM", v)
		if DreamEnabled() {
			t.Fatalf("DreamEnabled() = true under KRAKEN_DREAM=%q, want false", v)
		}
	}
	for _, v := range []string{"", "no", "false", "0", "on", "paused"} {
		t.Setenv("KRAKEN_DREAM", v)
		if !DreamEnabled() {
			t.Fatalf("DreamEnabled() = false under KRAKEN_DREAM=%q, want true: only the word off turns the pass off", v)
		}
	}
}

// A due dream row under the knob answers decided with a note naming the knob
// and starts nothing: kickDream is never consulted, so the pass seam is not
// called and the stored watermark keeps the previous pass's values.
func TestFireDreamSkipsUnderDreamOff(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	s := stubDreamPass(t, DreamRun{Date: today(now), SweptTo: now.UnixMilli()}, true)
	prev := DreamRun{Date: today(now.Add(-48 * time.Hour)), SweptTo: now.Add(-40 * time.Hour).UnixMilli(), Job: "dream-" + today(now.Add(-48*time.Hour))}
	seedDreamRun(t, prev)
	t.Setenv("KRAKEN_DREAM", "off")

	started := 0
	slug, note, ok, decided := fireDream(now, func(*Job) bool { started++; return true })

	if !decided {
		t.Fatal("fireDream under KRAKEN_DREAM=off is undecided; the row would be asked again every tick")
	}
	if ok || slug != "" || started != 0 {
		t.Fatalf("fireDream under KRAKEN_DREAM=off = (slug %q, ok %v, started %d), want nothing created or started", slug, ok, started)
	}
	if !strings.Contains(note, "KRAKEN_DREAM=off") {
		t.Fatalf("note = %q, want it to name KRAKEN_DREAM=off", note)
	}
	if n := s.calls.Load(); n != 0 {
		t.Fatalf("pass seam called %d times under KRAKEN_DREAM=off, want 0", n)
	}
	if got := ReadDreamRun(); got != prev {
		t.Fatalf("watermark = %+v, want the seeded %+v untouched", got, prev)
	}
}

// The same row with the knob unset fires through kickDream as before: the
// knob changes nothing when it is not set.
func TestFireDreamStillFiresWithoutTheKnob(t *testing.T) {
	useTestStore(t)
	now := dreamNoon()
	s := stubDreamPass(t, DreamRun{Date: today(now), SweptTo: now.UnixMilli()}, true)
	t.Setenv("KRAKEN_DREAM", "")

	_, _, _, decided := fireDream(now, func(*Job) bool { return true })

	if !decided {
		t.Fatal("fireDream with the knob unset is undecided")
	}
	if n := s.calls.Load(); n != 1 {
		t.Fatalf("pass seam called %d times with the knob unset, want 1", n)
	}
}

// A database seeded while the knob is off gets its dream row paused, so the
// board's PERIODIC view shows the operator's standing choice and a resume
// there is what turns the pass back on; seeded with the knob unset the row
// is enabled as before.
func TestEnsureDreamRowSeedsPausedUnderDreamOff(t *testing.T) {
	now := dreamNoon()
	for _, tc := range []struct {
		knob string
		want bool
	}{{"off", false}, {"", true}} {
		t.Run("KRAKEN_DREAM="+tc.knob, func(t *testing.T) {
			st := useTestStore(t)
			t.Setenv("KRAKEN_DREAM", tc.knob)
			if err := ensureDreamRow(st, now); err != nil {
				t.Fatalf("ensureDreamRow: %v", err)
			}
			row, err := st.GetPeriodicJob(PeriodicBuiltinDream)
			if err != nil {
				t.Fatalf("GetPeriodicJob(%q): %v", PeriodicBuiltinDream, err)
			}
			if row.Enabled != tc.want {
				t.Fatalf("seeded dream row Enabled = %v under KRAKEN_DREAM=%q, want %v", row.Enabled, tc.knob, tc.want)
			}
		})
	}
}
