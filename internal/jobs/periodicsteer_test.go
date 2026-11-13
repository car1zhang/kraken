package jobs

import (
	"errors"
	"testing"
	"time"

	"kraken/internal/store"
)

// periodicsteer_test.go — tests for the two changes the operator asked for
// at the manual-test gate: interval rows (`every 2h`), and the dream pass as
// a stored row of the periodic table. Written by the lead with the
// implementation in view; the blind suites cover the calendar cadence.

var periodicSteerLoc = time.FixedZone("periodic-steer", -5*3600)

const periodicSteerTemplate = "---\nrepo: kraken\n---\nsteer {{slug}} on {{date}}\n"

func periodicSteerAt(d, hh, mm int) time.Time {
	return time.Date(2032, time.March, d, hh, mm, 0, 0, periodicSteerLoc)
}

func TestPeriodicSteerParseInterval(t *testing.T) {
	for in, want := range map[string]int{
		"5m": 5, "30m": 30, "2h": 120, "3d": 3 * 1440, "1w": 7 * 1440, " 2H ": 120, "366d": 366 * 1440,
	} {
		if got, err := ParseInterval(in); err != nil || got != want {
			t.Errorf("ParseInterval(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "4m", "0h", "2", "h", "2x", "-5m", "1.5h", "367d", "53w", "99999999999999999999m", "2 h"} {
		if got, err := ParseInterval(in); !errors.Is(err, ErrPeriodicInvalid) {
			t.Errorf("ParseInterval(%q) = %d, %v; want ErrPeriodicInvalid", in, got, err)
		}
	}
}

func TestPeriodicSteerFormatInterval(t *testing.T) {
	for minutes, want := range map[int]string{0: "", 5: "5m", 90: "90m", 120: "2h", 1440: "1d", 2880: "2d", 10080: "1w", 10081: "10081m"} {
		if got := FormatInterval(minutes); got != want {
			t.Errorf("FormatInterval(%d) = %q, want %q", minutes, got, want)
		}
	}
	if got := PeriodicCadenceString(store.PeriodicJob{EveryMinutes: 120}); got != "every 2h" {
		t.Errorf("cadence of an interval row = %q, want every 2h", got)
	}
	if got := PeriodicCadenceString(store.PeriodicJob{AtMinute: 9 * 60, Days: 1 << time.Monday}); got != "mon 09:00" {
		t.Errorf("cadence of a calendar row = %q, want mon 09:00", got)
	}
}

// An interval row waits one interval after it is added, fires under a slug
// that carries the local time, and then counts the next interval from that
// firing, so a tick that arrives hours late fires once and not once per
// interval missed.
func TestPeriodicSteerIntervalRowFires(t *testing.T) {
	st := periodicFieldsEnv(t)
	resetDreamGuards(t)
	added := periodicSteerAt(3, 8, 0)
	if _, err := AddPeriodicEvery("pulse", "pulse", "2h", periodicSteerTemplate, added); err != nil {
		t.Fatalf("AddPeriodicEvery: %v", err)
	}
	row, err := st.GetPeriodicJob("pulse")
	if err != nil || row.EveryMinutes != 120 || !row.Enabled {
		t.Fatalf("stored row = %+v, %v; want EveryMinutes 120, enabled", row, err)
	}
	if PeriodicDue(*row, periodicSteerAt(3, 9, 59)) {
		t.Fatal("due one minute before the first interval has passed")
	}
	if next := PeriodicNextDue(*row, added); !next.Equal(periodicSteerAt(3, 10, 0)) {
		t.Fatalf("next due = %v, want 10:00", next)
	}

	var started []string
	start := func(j *Job) bool { started = append(started, j.Slug); return true }
	if n := periodicPass(periodicSteerAt(3, 9, 59), start); n != 0 {
		t.Fatalf("pass before the interval started %d", n)
	}
	first := periodicSteerAt(3, 10, 0)
	if n := periodicPass(first, start); n != 1 || len(started) != 1 || started[0] != "pulse-2032-03-03-1000" {
		t.Fatalf("first firing: n=%d started=%v, want pulse-2032-03-03-1000", n, started)
	}
	row, _ = st.GetPeriodicJob("pulse")
	if row.LastSlug != "pulse-2032-03-03-1000" || row.LastAt != first.UnixMilli() || row.LastNote != "" {
		t.Fatalf("stamp after first firing = %+v", row)
	}
	if n := periodicPass(periodicSteerAt(3, 11, 59), start); n != 0 {
		t.Fatalf("fired again inside the interval: %v", started)
	}

	// The machine sleeps from 11:59 to 19:30: three intervals pass, one firing.
	late := periodicSteerAt(3, 19, 30)
	if n := periodicPass(late, start); n != 1 || started[len(started)-1] != "pulse-2032-03-03-1930" {
		t.Fatalf("late firing: n=%d started=%v", n, started)
	}
	if n := periodicPass(late.Add(15*time.Second), start); n != 0 {
		t.Fatalf("a second tick replayed a missed interval: %v", started)
	}
	row, _ = st.GetPeriodicJob("pulse")
	if next := PeriodicNextDue(*row, late); !next.Equal(periodicSteerAt(3, 21, 30)) {
		t.Fatalf("next due after the late firing = %v, want 21:30", next)
	}
}

// A resumed interval row waits a full interval from the resume.
func TestPeriodicSteerIntervalResumeRearms(t *testing.T) {
	st := periodicFieldsEnv(t)
	added := periodicSteerAt(4, 8, 0)
	if _, err := AddPeriodicEvery("pulse", "pulse", "30m", periodicSteerTemplate, added); err != nil {
		t.Fatalf("AddPeriodicEvery: %v", err)
	}
	if _, err := SetPeriodicPaused("pulse", true, periodicSteerAt(4, 8, 5)); err != nil {
		t.Fatal(err)
	}
	row, _ := st.GetPeriodicJob("pulse")
	if PeriodicDue(*row, periodicSteerAt(4, 12, 0)) || !PeriodicNextDue(*row, periodicSteerAt(4, 12, 0)).IsZero() {
		t.Fatal("a paused interval row is due or has a next due time")
	}
	if _, err := SetPeriodicPaused("pulse", false, periodicSteerAt(4, 12, 0)); err != nil {
		t.Fatal(err)
	}
	row, _ = st.GetPeriodicJob("pulse")
	if PeriodicDue(*row, periodicSteerAt(4, 12, 29)) || !PeriodicDue(*row, periodicSteerAt(4, 12, 30)) {
		t.Fatal("a resumed interval row should be due exactly one interval after the resume")
	}
}

func TestPeriodicSteerAddEveryRefusals(t *testing.T) {
	st := periodicFieldsEnv(t)
	now := periodicSteerAt(5, 8, 0)
	for _, every := range []string{"", "1m", "soon", "2"} {
		if _, err := AddPeriodicEvery("pulse", "pulse", every, periodicSteerTemplate, now); !errors.Is(err, ErrPeriodicInvalid) {
			t.Errorf("AddPeriodicEvery(every %q): err = %v, want ErrPeriodicInvalid", every, err)
		}
	}
	if _, err := AddPeriodicEvery("pulse", "pulse", "1h", "no frontmatter", now); !errors.Is(err, ErrPeriodicInvalid) {
		t.Errorf("a template that does not parse: err = %v, want ErrPeriodicInvalid", err)
	}
	if rows, _ := st.ListPeriodicJobs(); len(rows) != 0 {
		t.Fatalf("refused adds stored rows: %+v", rows)
	}
}

// periodicSteerDream replaces the dream pass with one that creates the
// night's job (or reports that it could not answer) and counts its calls.
func periodicSteerDream(t *testing.T, st *store.Store, ok bool) *int {
	t.Helper()
	resetDreamGuards(t)
	calls := 0
	prev := dreamPass
	dreamPass = func(now time.Time, run DreamRun) (*Job, DreamRun, bool) {
		calls++
		if !ok {
			return nil, DreamRun{}, false
		}
		today := now.Format(dreamDateLayout)
		j, err := CreateDreamJob(st, "dream-"+today, now.Add(-time.Hour).UnixMilli(), now.UnixMilli(), false)
		if err != nil {
			t.Fatalf("CreateDreamJob: %v", err)
		}
		return j, DreamRun{Date: today, SweptTo: now.UnixMilli(), Job: j.Slug}, true
	}
	t.Cleanup(func() { dreamPass = prev })
	return &calls
}

// The dream pass fires from its own row: the tick's pass seeds the row, asks
// the dream pass once a day, starts the job it created and stamps the row.
func TestPeriodicSteerDreamRowFires(t *testing.T) {
	st := periodicFieldsEnv(t)
	calls := periodicSteerDream(t, st, true)
	var started []string
	start := func(j *Job) bool { started = append(started, j.Slug); return true }

	now := periodicSteerAt(6, 0, 1)
	if n := periodicPass(now, start); n != 1 || len(started) != 1 || started[0] != "dream-2032-03-06" {
		t.Fatalf("dream firing: n=%d started=%v", n, started)
	}
	row, err := st.GetPeriodicJob(PeriodicBuiltinDream)
	if err != nil || row.LastDate != "2032-03-06" || row.LastSlug != "dream-2032-03-06" || row.LastNote != "" || !row.Enabled {
		t.Fatalf("dream row after firing = %+v, %v", row, err)
	}
	if n := periodicPass(now.Add(15*time.Second), start); n != 0 || *calls != 1 {
		t.Fatalf("second tick the same day: n=%d, dream pass calls=%d", n, *calls)
	}

	rows, err := ListPeriodic(now)
	if err != nil || len(rows) != 1 || !rows[0].Builtin || rows[0].Name != PeriodicBuiltinDream || rows[0].LastSlug != "dream-2032-03-06" {
		t.Fatalf("ListPeriodic = %+v, %v; want the one dream row, builtin, with its last slug", rows, err)
	}
}

// A paused dream row does not ask the dream pass at all, and a resumed one
// waits for the next midnight.
func TestPeriodicSteerDreamRowPaused(t *testing.T) {
	st := periodicFieldsEnv(t)
	calls := periodicSteerDream(t, st, true)
	start := func(*Job) bool { return true }
	if _, err := SetPeriodicPaused(PeriodicBuiltinDream, true, periodicSteerAt(7, 23, 0)); err != nil {
		t.Fatalf("pause dream: %v", err)
	}
	if n := periodicPass(periodicSteerAt(8, 0, 5), start); n != 0 || *calls != 0 {
		t.Fatalf("paused dream row fired: n=%d calls=%d", n, *calls)
	}
	if _, err := SetPeriodicPaused(PeriodicBuiltinDream, false, periodicSteerAt(8, 10, 0)); err != nil {
		t.Fatalf("resume dream: %v", err)
	}
	if n := periodicPass(periodicSteerAt(8, 10, 1), start); n != 0 || *calls != 0 {
		t.Fatalf("resumed dream row fired for the midnight it was paused through: n=%d calls=%d", n, *calls)
	}
	if n := periodicPass(periodicSteerAt(9, 0, 1), start); n != 1 || *calls != 1 {
		t.Fatalf("resumed dream row did not fire at the next midnight: n=%d calls=%d", n, *calls)
	}
}

// When the dream pass cannot answer, the row stays unstamped and the next
// tick asks again.
func TestPeriodicSteerDreamRowRetries(t *testing.T) {
	st := periodicFieldsEnv(t)
	calls := periodicSteerDream(t, st, false)
	start := func(*Job) bool { t.Fatal("start called with no job"); return false }
	now := periodicSteerAt(10, 0, 1)
	periodicPass(now, start)
	periodicPass(now.Add(15*time.Second), start)
	if *calls != 2 {
		t.Fatalf("dream pass calls = %d, want 2 (asked again on the next tick)", *calls)
	}
	row, err := st.GetPeriodicJob(PeriodicBuiltinDream)
	if err != nil || row.LastDate != "" {
		t.Fatalf("dream row after two unanswered ticks = %+v, %v; want it unstamped", row, err)
	}
}

// On a machine with no kraken checkout kickDream settles the night without
// writing its watermark. The row's own stamp then has to carry the night, or
// the list would show it as still due.
func TestPeriodicSteerDreamRowNoCheckout(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_REPOS_DIR", t.TempDir())
	lastDreamDate.Store("")
	t.Cleanup(func() { lastDreamDate.Store("") })
	prev := periodicNotify
	periodicNotify = func(title, msg string) {}
	t.Cleanup(func() { periodicNotify = prev })

	now := periodicSteerAt(12, 0, 1)
	if n := periodicPass(now, func(*Job) bool { t.Fatal("start called with no job"); return false }); n != 0 {
		t.Fatalf("pass started %d", n)
	}
	row, err := st.GetPeriodicJob(PeriodicBuiltinDream)
	if err != nil || row.LastDate != "2032-03-12" || row.LastSlug != "" || row.LastNote == "" {
		t.Fatalf("dream row = %+v, %v; want it stamped for the night with a note", row, err)
	}
	rows, err := ListPeriodic(now)
	if err != nil || len(rows) != 1 || rows[0].LastDate != "2032-03-12" {
		t.Fatalf("ListPeriodic = %+v, %v; want the dream row with the night's date", rows, err)
	}
	if want := periodicSteerAt(13, 0, 0).UnixMilli(); rows[0].NextDue != want {
		t.Fatalf("next due = %d, want the next midnight %d", rows[0].NextDue, want)
	}
}

// Listing never creates the dream row (a --mirror serve lists and must not
// write), and the row can be neither removed nor run on demand.
func TestPeriodicSteerDreamRowGuards(t *testing.T) {
	st := periodicFieldsEnv(t)
	now := periodicSteerAt(11, 9, 0)
	rows, err := ListPeriodic(now)
	if err != nil || len(rows) != 1 || !rows[0].Builtin || !rows[0].Enabled {
		t.Fatalf("ListPeriodic on an unseeded table = %+v, %v", rows, err)
	}
	if stored, _ := st.ListPeriodicJobs(); len(stored) != 0 {
		t.Fatalf("ListPeriodic wrote rows: %+v", stored)
	}
	if err := RemovePeriodic(PeriodicBuiltinDream); !errors.Is(err, ErrPeriodicBuiltin) {
		t.Errorf("RemovePeriodic(dream): err = %v, want ErrPeriodicBuiltin", err)
	}
	if _, err := RunPeriodicNow(PeriodicBuiltinDream, now); !errors.Is(err, ErrPeriodicBuiltin) {
		t.Errorf("RunPeriodicNow(dream): err = %v, want ErrPeriodicBuiltin", err)
	}
}
