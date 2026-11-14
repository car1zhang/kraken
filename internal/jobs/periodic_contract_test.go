package jobs

// periodic_contract_test.go — athena-logic's BLIND tests for 50-contract.md
// (kraken-periodic-jobs), Slice 2: cadence parsing, PeriodicDue /
// PeriodicNextDue, template rendering, AddPeriodic / SetPeriodicPaused /
// RemovePeriodic / RunPeriodicNow / ListPeriodic, the tick's periodicPass,
// RenameJob's allowed direction, and the mirror tick. Written from the frozen
// contract alone; internal/jobs/periodic.go and the working-tree daemon.go and
// rename.go were never opened. Dimension: LOGIC. Refusals and validation
// errors are athena-edge's.
//
// Isolation (contract, "Test isolation"): every store-backed test runs under
// useTestStore (temp HOME + own store), a temp KRAKEN_REPOS_DIR holding a bare
// kraken/.git, and periodicSubmit / periodicNotify replaced; periodicPass is
// always handed a recording start func. Times are built in a fixed UTC-5 zone
// and never read from the real clock.
//
// The contract's implementer note asks for an in-process "date each row last
// fired" record with no reset seam, so every periodicPass test uses its OWN
// calendar date and its own row names: a record left by one test can then
// never suppress another's firing.
//
// Every package-level identifier here starts with periodicContract or
// TestPeriodicContract.

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"kraken/internal/store"
)

// periodicContractZone is UTC-5 with no DST, so a late-evening `now` falls on
// the NEXT day in UTC: any code that takes "today" from UTC instead of from
// now's own location gets the date wrong.
var periodicContractZone = time.FixedZone("contract-5", -5*3600)

func periodicContractAt(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, periodicContractZone)
}

const periodicContractTemplate = "---\nrepo: kraken\n---\nRun the {{slug}} pass for {{date}}.\n"

// periodicContractEnv wires a store, a repos dir resolving "kraken", and the
// notify/submit seams (submit refuses loudly unless a test installs its own).
func periodicContractEnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)

	prevNotify := periodicNotify
	periodicNotify = func(title, msg string) {}
	t.Cleanup(func() { periodicNotify = prevNotify })

	prevSubmit := periodicSubmit
	periodicSubmit = func(j *Job) error {
		t.Errorf("unexpected periodicSubmit(%s): this test installed no submit stub", j.Slug)
		return errors.New("contract test: no submit stub")
	}
	t.Cleanup(func() { periodicSubmit = prevSubmit })
	return st
}

// periodicContractSubmits replaces periodicSubmit with a recorder answering err.
func periodicContractSubmits(t *testing.T, err error) *[]*Job {
	t.Helper()
	var got []*Job
	periodicSubmit = func(j *Job) error {
		got = append(got, j)
		return err
	}
	return &got
}

// periodicContractStarter is a recording start func for periodicPass.
type periodicContractStarter struct {
	answer bool
	calls  []*Job
}

func (s *periodicContractStarter) start(j *Job) bool {
	s.calls = append(s.calls, j)
	return s.answer
}

// periodicContractSeed inserts a row straight through the store, so the pass
// tests control every field (including a template AddPeriodic would refuse).
func periodicContractSeed(t *testing.T, st *store.Store, p store.PeriodicJob) store.PeriodicJob {
	t.Helper()
	id, err := st.CreatePeriodicJob(&p)
	if err != nil {
		t.Fatalf("CreatePeriodicJob(%s): %v", p.Name, err)
	}
	p.ID = id
	return p
}

func periodicContractRowNamed(t *testing.T, st *store.Store, name string) *store.PeriodicJob {
	t.Helper()
	p, err := st.GetPeriodicJob(name)
	if err != nil {
		t.Fatalf("GetPeriodicJob(%s): %v", name, err)
	}
	return p
}

func periodicContractJobCount(t *testing.T, st *store.Store) int {
	t.Helper()
	all, err := st.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	return len(all)
}

// Days bits: 1 << time.Weekday, Sunday bit 0.
const (
	periodicContractSun = 1 << time.Sunday
	periodicContractMon = 1 << time.Monday
	periodicContractTue = 1 << time.Tuesday
	periodicContractWed = 1 << time.Wednesday
	periodicContractThu = 1 << time.Thursday
	periodicContractFri = 1 << time.Friday
	periodicContractSat = 1 << time.Saturday
)

// ---------------------------------------------------------------- ParseCadence

// Contract, ParseCadence: "at is H:MM or HH:MM on a 24-hour clock ...
// AtMinute is hour*60+minute. ... "" and daily give Days == 0; weekdays gives
// Monday to Friday; otherwise a comma-separated list of sun mon tue wed thu fri
// sat (spaces around an element are trimmed, a repeated name is fine) gives
// exactly the named bits." days is "trimmed and case-insensitive".
func TestPeriodicContractParseCadence(t *testing.T) {
	for _, tc := range []struct {
		at, days string
		want     Cadence
	}{
		{"9:05", "", Cadence{AtMinute: 9*60 + 5, Days: 0}},
		{"09:05", "daily", Cadence{AtMinute: 9*60 + 5, Days: 0}},
		{"0:00", "", Cadence{AtMinute: 0, Days: 0}},
		{"00:00", "  DAILY ", Cadence{AtMinute: 0, Days: 0}},
		{"23:59", "", Cadence{AtMinute: 23*60 + 59, Days: 0}},
		{"13:30", "weekdays", Cadence{AtMinute: 13*60 + 30, Days: periodicContractMon | periodicContractTue | periodicContractWed | periodicContractThu | periodicContractFri}},
		{"13:30", " WeekDays ", Cadence{AtMinute: 13*60 + 30, Days: periodicContractMon | periodicContractTue | periodicContractWed | periodicContractThu | periodicContractFri}},
		{"7:00", "mon,wed,fri", Cadence{AtMinute: 7 * 60, Days: periodicContractMon | periodicContractWed | periodicContractFri}},
		{"7:00", "mon , WED,  Fri", Cadence{AtMinute: 7 * 60, Days: periodicContractMon | periodicContractWed | periodicContractFri}},
		{"7:00", "sun,sat", Cadence{AtMinute: 7 * 60, Days: periodicContractSun | periodicContractSat}},
		{"7:00", "tue,tue,thu", Cadence{AtMinute: 7 * 60, Days: periodicContractTue | periodicContractThu}},
		{"7:00", "sun,mon,tue,wed,thu,fri,sat", Cadence{AtMinute: 7 * 60, Days: 127}},
	} {
		got, err := ParseCadence(tc.at, tc.days)
		if err != nil {
			t.Errorf("ParseCadence(%q, %q): %v", tc.at, tc.days, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseCadence(%q, %q) = %+v, want %+v", tc.at, tc.days, got, tc.want)
		}
	}
}

// Contract, Cadence.At: "The time as zero-padded HH:MM."
func TestPeriodicContractCadenceAt(t *testing.T) {
	for _, tc := range []struct {
		min  int
		want string
	}{{0, "00:00"}, {9*60 + 5, "09:05"}, {23*60 + 59, "23:59"}, {12 * 60, "12:00"}} {
		if got := (Cadence{AtMinute: tc.min}).At(); got != tc.want {
			t.Errorf("Cadence{AtMinute:%d}.At() = %q, want %q", tc.min, got, tc.want)
		}
	}
}

// Contract, Cadence.DayNames: "Zero length when Days == 0. Otherwise the set
// days as lowercase three-letter names, Monday first and Sunday last."
func TestPeriodicContractCadenceDayNames(t *testing.T) {
	if got := (Cadence{Days: 0}).DayNames(); len(got) != 0 {
		t.Errorf("DayNames for Days 0 = %v, want zero length", got)
	}
	for _, tc := range []struct {
		days int
		want []string
	}{
		{periodicContractSun | periodicContractSat, []string{"sat", "sun"}},
		{periodicContractFri | periodicContractMon | periodicContractWed, []string{"mon", "wed", "fri"}},
		{periodicContractSun, []string{"sun"}},
		{127, []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}},
	} {
		if got := (Cadence{Days: tc.days}).DayNames(); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("DayNames for Days %d = %v, want %v", tc.days, got, tc.want)
		}
	}
}

// Contract, Cadence.String: "daily HH:MM when Days == 0, otherwise the DayNames
// joined by commas, a space, and HH:MM (mon,wed,fri 09:00)."
func TestPeriodicContractCadenceString(t *testing.T) {
	for _, tc := range []struct {
		c    Cadence
		want string
	}{
		{Cadence{AtMinute: 9 * 60, Days: 0}, "daily 09:00"},
		{Cadence{AtMinute: 9 * 60, Days: periodicContractMon | periodicContractWed | periodicContractFri}, "mon,wed,fri 09:00"},
		{Cadence{AtMinute: 23*60 + 5, Days: periodicContractSun | periodicContractSat}, "sat,sun 23:05"},
		{Cadence{AtMinute: 0, Days: periodicContractTue}, "tue 00:00"},
	} {
		if got := tc.c.String(); got != tc.want {
			t.Errorf("%+v.String() = %q, want %q", tc.c, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------- PeriodicDue

// periodicContractDueRow: enabled, daily at 09:00, armed long before, never fired.
func periodicContractDueRow() store.PeriodicJob {
	return store.PeriodicJob{
		Name: "due", Prefix: "due", Template: periodicContractTemplate,
		AtMinute: 9 * 60, Days: 0, Enabled: true,
		ArmedAt: periodicContractAt(2026, 1, 1, 0, 0).UnixMilli(),
	}
}

// Contract, PeriodicDue: "True exactly when all of these hold: p.Enabled;
// p.Days == 0 or today's weekday bit is set; now is at or after today's
// occurrence; today's occurrence is not before p.ArmedAt; p.LastDate sorts
// before today. A LastDate equal to today or later than today is false."
func TestPeriodicContractDue(t *testing.T) {
	fri := periodicContractAt(2026, 11, 13, 9, 0) // a Friday
	for _, tc := range []struct {
		name string
		edit func(p *store.PeriodicJob)
		now  time.Time
		want bool
	}{
		{"at the occurrence", func(p *store.PeriodicJob) {}, fri, true},
		{"after the occurrence, late evening (next day in UTC)", func(p *store.PeriodicJob) {}, periodicContractAt(2026, 11, 13, 22, 30), true},
		{"before the occurrence", func(p *store.PeriodicJob) {}, periodicContractAt(2026, 11, 13, 8, 59), false},
		{"disabled", func(p *store.PeriodicJob) { p.Enabled = false }, fri, false},
		{"today's bit set", func(p *store.PeriodicJob) { p.Days = periodicContractMon | periodicContractFri }, fri, true},
		{"today's bit not set", func(p *store.PeriodicJob) { p.Days = periodicContractMon | periodicContractThu }, fri, false},
		{"fired yesterday", func(p *store.PeriodicJob) { p.LastDate = "2026-11-12" }, fri, true},
		{"fired today", func(p *store.PeriodicJob) { p.LastDate = "2026-11-13" }, fri, false},
		{"LastDate later than today", func(p *store.PeriodicJob) { p.LastDate = "2026-11-14" }, fri, false},
		{"armed exactly at the occurrence", func(p *store.PeriodicJob) { p.ArmedAt = fri.UnixMilli() }, periodicContractAt(2026, 11, 13, 10, 0), true},
		{"armed after today's occurrence", func(p *store.PeriodicJob) {
			p.ArmedAt = periodicContractAt(2026, 11, 13, 9, 30).UnixMilli()
		}, periodicContractAt(2026, 11, 13, 10, 0), false},
	} {
		p := periodicContractDueRow()
		tc.edit(&p)
		if got := PeriodicDue(p, tc.now); got != tc.want {
			t.Errorf("%s: PeriodicDue = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------- PeriodicNextDue

// Contract, PeriodicNextDue: "The zero time when !p.Enabled. Today's occurrence
// when PeriodicDue(p, now). Otherwise, for a LastDate not later than today, the
// earliest occurrence after now that falls on a cadence day and is not before
// p.ArmedAt."
func TestPeriodicContractNextDue(t *testing.T) {
	mwf := periodicContractMon | periodicContractWed | periodicContractFri
	for _, tc := range []struct {
		name string
		edit func(p *store.PeriodicJob)
		now  time.Time
		want time.Time
	}{
		{"due now: today's occurrence", func(p *store.PeriodicJob) {},
			periodicContractAt(2026, 11, 13, 10, 0), periodicContractAt(2026, 11, 13, 9, 0)},
		{"later today", func(p *store.PeriodicJob) {},
			periodicContractAt(2026, 11, 13, 8, 0), periodicContractAt(2026, 11, 13, 9, 0)},
		{"fired today, daily: tomorrow", func(p *store.PeriodicJob) { p.LastDate = "2026-11-13" },
			periodicContractAt(2026, 11, 13, 10, 0), periodicContractAt(2026, 11, 14, 9, 0)},
		{"fired Friday, mon/wed/fri: Monday", func(p *store.PeriodicJob) { p.LastDate = "2026-11-13"; p.Days = mwf },
			periodicContractAt(2026, 11, 13, 10, 0), periodicContractAt(2026, 11, 16, 9, 0)},
		{"not a cadence day: next one", func(p *store.PeriodicJob) { p.Days = mwf },
			periodicContractAt(2026, 11, 14, 12, 0), periodicContractAt(2026, 11, 16, 9, 0)},
		{"armed after today's occurrence: next cadence day", func(p *store.PeriodicJob) {
			p.Days = mwf
			p.ArmedAt = periodicContractAt(2026, 11, 13, 9, 30).UnixMilli()
		}, periodicContractAt(2026, 11, 13, 10, 0), periodicContractAt(2026, 11, 16, 9, 0)},
		{"armed in the future: first occurrence not before it", func(p *store.PeriodicJob) {
			p.ArmedAt = periodicContractAt(2026, 11, 15, 12, 0).UnixMilli()
		}, periodicContractAt(2026, 11, 13, 10, 0), periodicContractAt(2026, 11, 16, 9, 0)},
	} {
		p := periodicContractDueRow()
		tc.edit(&p)
		got := PeriodicNextDue(p, tc.now)
		if !got.Equal(tc.want) {
			t.Errorf("%s: PeriodicNextDue = %v, want %v", tc.name, got, tc.want)
		}
	}

	p := periodicContractDueRow()
	p.Enabled = false
	if got := PeriodicNextDue(p, periodicContractAt(2026, 11, 13, 10, 0)); !got.IsZero() {
		t.Errorf("disabled: PeriodicNextDue = %v, want the zero time", got)
	}
}

// ---------------------------------------------------------------- RenderPeriodicTemplate

// Contract, RenderPeriodicTemplate: "Replaces every {{slug}} with slug and every
// {{date}} with date. Changes nothing else."
func TestPeriodicContractRenderTemplate(t *testing.T) {
	in := "---\nrepo: kraken\n---\n{{slug}} / {{date}} / {{slug}}{{date}}\n{{ slug }} {{SLUG}} {date} $slug\n"
	want := "---\nrepo: kraken\n---\nnotes-2026-11-13 / 2026-11-13 / notes-2026-11-132026-11-13\n{{ slug }} {{SLUG}} {date} $slug\n"
	if got := RenderPeriodicTemplate(in, "notes-2026-11-13", "2026-11-13"); got != want {
		t.Fatalf("RenderPeriodicTemplate =\n%q\nwant\n%q", got, want)
	}
	if got := RenderPeriodicTemplate("no placeholders\n", "s", "d"); got != "no placeholders\n" {
		t.Fatalf("placeholder-free template changed: %q", got)
	}
}

// ---------------------------------------------------------------- AddPeriodic

// Contract, AddPeriodic: "On success the stored row is enabled, ArmedAt and
// CreatedAt are now in unix millis, and Template is the text as given,
// unrendered. Adding creates no job." Cadence fields come from ParseCadence.
func TestPeriodicContractAddStoresRow(t *testing.T) {
	st := periodicContractEnv(t)
	now := periodicContractAt(2026, 11, 13, 22, 30)

	got, err := AddPeriodic("Standup Notes", "standup", "9:05", "mon,wed,fri", periodicContractTemplate, now)
	if err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}
	stored := periodicContractRowNamed(t, st, "Standup Notes")
	if !reflect.DeepEqual(*got, *stored) {
		t.Fatalf("returned row %+v differs from stored %+v", *got, *stored)
	}
	want := store.PeriodicJob{
		ID:        stored.ID,
		Name:      "Standup Notes",
		Prefix:    "standup",
		Template:  periodicContractTemplate,
		AtMinute:  9*60 + 5,
		Days:      periodicContractMon | periodicContractWed | periodicContractFri,
		Enabled:   true,
		ArmedAt:   now.UnixMilli(),
		CreatedAt: now.UnixMilli(),
	}
	if !reflect.DeepEqual(*stored, want) {
		t.Fatalf("stored row = %+v\nwant         %+v", *stored, want)
	}
	if n := periodicContractJobCount(t, st); n != 0 {
		t.Fatalf("AddPeriodic created %d job(s), want 0", n)
	}
}

// Contract, AddPeriodic: days "" means every day (ParseCadence), and a second
// row with a different name and prefix is stored beside the first.
func TestPeriodicContractAddDailyRow(t *testing.T) {
	st := periodicContractEnv(t)
	now := periodicContractAt(2026, 11, 13, 8, 0)
	if _, err := AddPeriodic("first", "first", "08:00", "", periodicContractTemplate, now); err != nil {
		t.Fatalf("AddPeriodic first: %v", err)
	}
	if _, err := AddPeriodic("second", "second", "23:59", "daily", periodicContractTemplate, now); err != nil {
		t.Fatalf("AddPeriodic second: %v", err)
	}
	rows, err := st.ListPeriodicJobs()
	if err != nil {
		t.Fatalf("ListPeriodicJobs: %v", err)
	}
	if len(rows) != 2 || rows[0].Name != "first" || rows[1].Name != "second" {
		t.Fatalf("rows = %+v, want first then second", rows)
	}
	if rows[0].Days != 0 || rows[0].AtMinute != 8*60 || rows[1].Days != 0 || rows[1].AtMinute != 23*60+59 {
		t.Fatalf("cadence fields = %d@%d, %d@%d", rows[0].Days, rows[0].AtMinute, rows[1].Days, rows[1].AtMinute)
	}
}

// ---------------------------------------------------------------- SetPeriodicPaused

// Contract, SetPeriodicPaused: "paused == true disables the row. paused ==
// false enables it and sets ArmedAt to now. Returns the row as stored
// afterwards."
func TestPeriodicContractPauseResume(t *testing.T) {
	st := periodicContractEnv(t)
	added := periodicContractAt(2026, 11, 10, 9, 0)
	if _, err := AddPeriodic("standup", "standup", "9:00", "", periodicContractTemplate, added); err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}

	paused, err := SetPeriodicPaused("standup", true, periodicContractAt(2026, 11, 11, 9, 0))
	if err != nil {
		t.Fatalf("pause: %v", err)
	}
	stored := periodicContractRowNamed(t, st, "standup")
	if !reflect.DeepEqual(*paused, *stored) {
		t.Fatalf("pause returned %+v, stored %+v", *paused, *stored)
	}
	if stored.Enabled {
		t.Fatalf("after pause Enabled = true")
	}

	resumeAt := periodicContractAt(2026, 11, 13, 22, 30)
	resumed, err := SetPeriodicPaused("standup", false, resumeAt)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	stored = periodicContractRowNamed(t, st, "standup")
	if !reflect.DeepEqual(*resumed, *stored) {
		t.Fatalf("resume returned %+v, stored %+v", *resumed, *stored)
	}
	if !stored.Enabled || stored.ArmedAt != resumeAt.UnixMilli() {
		t.Fatalf("after resume Enabled %v ArmedAt %d, want true / %d", stored.Enabled, stored.ArmedAt, resumeAt.UnixMilli())
	}
}

// ---------------------------------------------------------------- RemovePeriodic

// Contract, RemovePeriodic: "Deletes the row. Every job it fired stays as it is."
func TestPeriodicContractRemoveKeepsFiredJobs(t *testing.T) {
	st := periodicContractEnv(t)
	if _, err := AddPeriodic("standup", "standup", "9:00", "", periodicContractTemplate, periodicContractAt(2026, 11, 10, 9, 0)); err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}
	fired := mustCreateJob(t, st, store.Job{Slug: "standup-2026-11-12", Repo: "kraken", Body: "b", Author: "kraken", State: "blocked"})

	if err := RemovePeriodic("standup"); err != nil {
		t.Fatalf("RemovePeriodic: %v", err)
	}
	if rows, err := st.ListPeriodicJobs(); err != nil || len(rows) != 0 {
		t.Fatalf("rows after remove = %+v (err %v), want none", rows, err)
	}
	j, err := st.GetJobBySlug("standup-2026-11-12")
	if err != nil {
		t.Fatalf("fired job gone after RemovePeriodic: %v", err)
	}
	if j.ID != fired.ID || j.State != "blocked" || j.Body != "b" {
		t.Fatalf("fired job changed: %+v", j)
	}
}

// ---------------------------------------------------------------- RunPeriodicNow

const periodicContractFullTemplate = "---\njob: ignored-name\nrepo: kraken\nbase: origin/dev\nsolo: true\nmodel: opus\nagent: codex\n---\nRun the {{slug}} pass for {{date}}.\nSecond line.\n"

// periodicContractCheckFired asserts a created job carries the fields the
// contract names for both a scheduled and an on-demand firing: Author kraken,
// and Repo/Base/Solo/Model/Agent/Body from parsing the rendered template.
func periodicContractCheckFired(t *testing.T, st *store.Store, slug, template, date string) *store.Job {
	t.Helper()
	got, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("job %s not created: %v", slug, err)
	}
	parsed := ParseJob(slug, []byte(RenderPeriodicTemplate(template, slug, date)))
	if got.Author != "kraken" {
		t.Errorf("%s Author = %q, want kraken", slug, got.Author)
	}
	if got.Repo != parsed.Repo || got.Base != parsed.Base || got.Solo != parsed.Solo || got.Model != parsed.Model || got.Agent != parsed.Agent {
		t.Errorf("%s fields = repo %q base %q solo %v model %q agent %q; want %q %q %v %q %q",
			slug, got.Repo, got.Base, got.Solo, got.Model, got.Agent,
			parsed.Repo, parsed.Base, parsed.Solo, parsed.Model, parsed.Agent)
	}
	if got.Body != parsed.Body {
		t.Errorf("%s Body = %q, want %q", slug, got.Body, parsed.Body)
	}
	return got
}

// Contract, RunPeriodicNow: "The slug is the first of <prefix>-<today>-run,
// <prefix>-<today>-run-2, <prefix>-<today>-run-3 ... that no active job holds.
// Creates that job from the template rendered with that slug and today's date
// (same fields and author as a scheduled firing), then calls periodicSubmit
// with it exactly once. Returns the slug, Started true when the submit returned
// nil ... Leaves all four Last* fields alone." It fires "whether or not it is
// enabled or due".
func TestPeriodicContractRunNowCreatesAndSubmits(t *testing.T) {
	st := periodicContractEnv(t)
	if _, err := AddPeriodic("standup", "standup", "9:00", "", periodicContractFullTemplate, periodicContractAt(2026, 11, 10, 9, 0)); err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}
	// paused, and fired earlier today: neither stops an on-demand run
	if _, err := SetPeriodicPaused("standup", true, periodicContractAt(2026, 11, 11, 9, 0)); err != nil {
		t.Fatalf("pause: %v", err)
	}
	row := periodicContractRowNamed(t, st, "standup")
	if err := st.StampPeriodicFiring(row.ID, "2026-11-13", "standup-2026-11-13", "earlier", 1769000000000); err != nil {
		t.Fatalf("seed stamp: %v", err)
	}
	before := periodicContractRowNamed(t, st, "standup")
	submits := periodicContractSubmits(t, nil)

	now := periodicContractAt(2026, 11, 13, 22, 30) // 2026-11-14 in UTC
	run, err := RunPeriodicNow("standup", now)
	if err != nil {
		t.Fatalf("RunPeriodicNow: %v", err)
	}
	if run != (PeriodicRun{Slug: "standup-2026-11-13-run", Started: true, Note: ""}) {
		t.Fatalf("run = %+v, want standup-2026-11-13-run started", run)
	}
	if len(*submits) != 1 || (*submits)[0].Slug != "standup-2026-11-13-run" {
		t.Fatalf("periodicSubmit calls = %d, want exactly one with the run slug", len(*submits))
	}
	periodicContractCheckFired(t, st, "standup-2026-11-13-run", periodicContractFullTemplate, "2026-11-13")

	after := periodicContractRowNamed(t, st, "standup")
	if after.LastDate != before.LastDate || after.LastSlug != before.LastSlug || after.LastNote != before.LastNote || after.LastAt != before.LastAt {
		t.Fatalf("RunPeriodicNow changed Last*: before %+v after %+v", before, after)
	}
}

// Contract, RunPeriodicNow: successive runs on one day take -run, -run-2,
// -run-3, the first slug no active job holds.
func TestPeriodicContractRunNowSuffixes(t *testing.T) {
	st := periodicContractEnv(t)
	if _, err := AddPeriodic("standup", "standup", "9:00", "", periodicContractTemplate, periodicContractAt(2026, 11, 10, 9, 0)); err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}
	submits := periodicContractSubmits(t, nil)
	now := periodicContractAt(2026, 11, 13, 12, 0)
	for _, want := range []string{"standup-2026-11-13-run", "standup-2026-11-13-run-2", "standup-2026-11-13-run-3"} {
		run, err := RunPeriodicNow("standup", now)
		if err != nil {
			t.Fatalf("RunPeriodicNow: %v", err)
		}
		if run.Slug != want {
			t.Fatalf("slug = %q, want %q", run.Slug, want)
		}
		periodicContractCheckFired(t, st, want, periodicContractTemplate, "2026-11-13")
	}
	if len(*submits) != 3 {
		t.Fatalf("periodicSubmit calls = %d, want 3", len(*submits))
	}
}

// Contract, RunPeriodicNow: a slug held only by an ARCHIVED job is free ("that
// no active job holds"; an active job is one that is not archived).
func TestPeriodicContractRunNowReusesArchivedSlug(t *testing.T) {
	st := periodicContractEnv(t)
	if _, err := AddPeriodic("standup", "standup", "9:00", "", periodicContractTemplate, periodicContractAt(2026, 11, 10, 9, 0)); err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}
	old := mustCreateJob(t, st, store.Job{Slug: "standup-2026-11-13-run", Repo: "kraken", Body: "b", State: "done"})
	if _, err := st.ArchiveJob(old.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	periodicContractSubmits(t, nil)
	run, err := RunPeriodicNow("standup", periodicContractAt(2026, 11, 13, 12, 0))
	if err != nil {
		t.Fatalf("RunPeriodicNow: %v", err)
	}
	if run.Slug != "standup-2026-11-13-run" {
		t.Fatalf("slug = %q, want standup-2026-11-13-run (the holder is archived)", run.Slug)
	}
}

// Contract, RunPeriodicNow: "Started true when the submit returned nil, and the
// submit's error text in Note otherwise (the job then stays on the board as a
// draft)."
func TestPeriodicContractRunNowSubmitRefused(t *testing.T) {
	st := periodicContractEnv(t)
	if _, err := AddPeriodic("standup", "standup", "9:00", "", periodicContractTemplate, periodicContractAt(2026, 11, 10, 9, 0)); err != nil {
		t.Fatalf("AddPeriodic: %v", err)
	}
	submits := periodicContractSubmits(t, errors.New("no qualifying launcher"))
	run, err := RunPeriodicNow("standup", periodicContractAt(2026, 11, 13, 12, 0))
	if err != nil {
		t.Fatalf("RunPeriodicNow: %v (a refused submit is not a returned error)", err)
	}
	if run != (PeriodicRun{Slug: "standup-2026-11-13-run", Started: false, Note: "no qualifying launcher"}) {
		t.Fatalf("run = %+v", run)
	}
	if len(*submits) != 1 {
		t.Fatalf("periodicSubmit calls = %d, want 1", len(*submits))
	}
	j := periodicContractCheckFired(t, st, "standup-2026-11-13-run", periodicContractTemplate, "2026-11-13")
	if j.State != "" || j.ArchivedAt != 0 {
		t.Fatalf("job after refused submit: state %q archived %d, want an active draft", j.State, j.ArchivedAt)
	}
}

// ---------------------------------------------------------------- ListPeriodic

// Contract, ListPeriodic: "First the built-in dream row: Name and Prefix dream,
// AtMinute 0, Days 0, Enabled true, Builtin true, Template empty, LastDate and
// LastSlug taken from ReadDreamRun()'s Date and Job. Then every stored row in
// creation order with Builtin false. NextDue is PeriodicNextDue for that row in
// unix millis, 0 for the zero time."
func TestPeriodicContractListPeriodic(t *testing.T) {
	st := periodicContractEnv(t)
	if err := writeDreamRun(DreamRun{Date: "2026-11-12", Job: "dream-2026-11-12"}); err != nil {
		t.Fatalf("writeDreamRun: %v", err)
	}
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "zeta", Prefix: "zeta", Template: periodicContractTemplate,
		AtMinute: 9 * 60, Days: periodicContractMon, Enabled: true,
		ArmedAt: periodicContractAt(2026, 11, 1, 0, 0).UnixMilli(), CreatedAt: 1,
	})
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "alpha", Prefix: "alpha", Template: periodicContractTemplate,
		AtMinute: 7 * 60, Days: 0, Enabled: false,
		ArmedAt: periodicContractAt(2026, 11, 1, 0, 0).UnixMilli(), CreatedAt: 2,
	})
	now := periodicContractAt(2026, 11, 13, 22, 30)

	rows, err := ListPeriodic(now)
	if err != nil {
		t.Fatalf("ListPeriodic: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("ListPeriodic = %d rows, want 3 (dream + 2)", len(rows))
	}
	d := rows[0]
	if d.Name != "dream" || d.Prefix != "dream" || d.AtMinute != 0 || d.Days != 0 || !d.Enabled || !d.Builtin || d.Template != "" {
		t.Fatalf("dream row = %+v", d)
	}
	if d.LastDate != "2026-11-12" || d.LastSlug != "dream-2026-11-12" {
		t.Fatalf("dream row LastDate/LastSlug = %q/%q, want the dream watermark's", d.LastDate, d.LastSlug)
	}

	if rows[1].Name != "zeta" || rows[2].Name != "alpha" {
		t.Fatalf("stored rows = %q, %q; want zeta, alpha (creation order)", rows[1].Name, rows[2].Name)
	}
	for _, r := range rows[1:] {
		if r.Builtin {
			t.Fatalf("stored row %q has Builtin true", r.Name)
		}
		stored := periodicContractRowNamed(t, st, r.Name)
		if !reflect.DeepEqual(r.PeriodicJob, *stored) {
			t.Fatalf("row %q = %+v, stored %+v", r.Name, r.PeriodicJob, *stored)
		}
	}
	// zeta: Mondays 09:00 → Monday 2026-11-16 09:00
	if want := periodicContractAt(2026, 11, 16, 9, 0).UnixMilli(); rows[1].NextDue != want {
		t.Fatalf("zeta NextDue = %d, want %d", rows[1].NextDue, want)
	}
	if want := PeriodicNextDue(rows[1].PeriodicJob, now).UnixMilli(); rows[1].NextDue != want {
		t.Fatalf("zeta NextDue = %d, want PeriodicNextDue's %d", rows[1].NextDue, want)
	}
	// alpha is disabled: the zero time, served as 0
	if rows[2].NextDue != 0 {
		t.Fatalf("disabled alpha NextDue = %d, want 0", rows[2].NextDue)
	}
}

// ---------------------------------------------------------------- periodicPass

// Contract, periodicPass: "With no store wired it does nothing and returns 0."
func TestPeriodicContractPassNoStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	UseStore(nil)
	s := &periodicContractStarter{answer: true}
	if n := periodicPass(periodicContractAt(2026, 3, 2, 12, 0), s.start); n != 0 {
		t.Fatalf("periodicPass with no store = %d, want 0", n)
	}
	if len(s.calls) != 0 {
		t.Fatalf("start called %d times with no store", len(s.calls))
	}
}

// Contract, periodicPass case (c): "it creates the job row with that slug,
// Author kraken, and Repo, Base, Solo, Model, Agent and Body from parsing the
// rendered template, then calls start with it exactly once ... the row is
// stamped with LastDate today and LastAt now; LastSlug is the slug ...
// LastNote is empty only when (c)'s start returned true ... The return value is
// the number of start calls that returned true. A job: line in the template
// does not name the job; the slug above always does."
func TestPeriodicContractPassFiresDueRow(t *testing.T) {
	st := periodicContractEnv(t)
	row := periodicContractSeed(t, st, store.PeriodicJob{
		Name: "pass-c", Prefix: "pass-c", Template: periodicContractFullTemplate,
		AtMinute: 9 * 60, Days: 0, Enabled: true,
		ArmedAt: periodicContractAt(2026, 3, 1, 0, 0).UnixMilli(), CreatedAt: 1,
	})
	now := periodicContractAt(2026, 3, 3, 22, 30) // 2026-03-04 in UTC
	s := &periodicContractStarter{answer: true}

	if n := periodicPass(now, s.start); n != 1 {
		t.Fatalf("periodicPass = %d, want 1", n)
	}
	if len(s.calls) != 1 || s.calls[0].Slug != "pass-c-2026-03-03" {
		t.Fatalf("start calls = %d, want one with slug pass-c-2026-03-03", len(s.calls))
	}
	periodicContractCheckFired(t, st, "pass-c-2026-03-03", periodicContractFullTemplate, "2026-03-03")
	if _, err := st.GetJobBySlug("ignored-name"); err == nil {
		t.Fatalf("the template's job: line named a job")
	}

	got := periodicContractRowNamed(t, st, "pass-c")
	if got.LastDate != "2026-03-03" || got.LastSlug != "pass-c-2026-03-03" || got.LastNote != "" || got.LastAt != now.UnixMilli() {
		t.Fatalf("stamp = date %q slug %q note %q at %d; want 2026-03-03 / pass-c-2026-03-03 / \"\" / %d",
			got.LastDate, got.LastSlug, got.LastNote, got.LastAt, now.UnixMilli())
	}
	if got.ID != row.ID {
		t.Fatalf("row id moved")
	}
}

// Contract, periodicPass case (c) with start returning false: the job is
// created and start called once, the return value counts only true starts,
// LastSlug is the slug and LastNote "says what happened" (non-empty).
func TestPeriodicContractPassStartRefused(t *testing.T) {
	st := periodicContractEnv(t)
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "pass-refused", Prefix: "pass-refused", Template: periodicContractTemplate,
		AtMinute: 6 * 60, Days: 0, Enabled: true,
		ArmedAt: periodicContractAt(2026, 3, 1, 0, 0).UnixMilli(), CreatedAt: 1,
	})
	now := periodicContractAt(2026, 3, 5, 6, 0)
	s := &periodicContractStarter{answer: false}

	if n := periodicPass(now, s.start); n != 0 {
		t.Fatalf("periodicPass = %d, want 0 (start returned false)", n)
	}
	if len(s.calls) != 1 {
		t.Fatalf("start calls = %d, want 1", len(s.calls))
	}
	periodicContractCheckFired(t, st, "pass-refused-2026-03-05", periodicContractTemplate, "2026-03-05")
	got := periodicContractRowNamed(t, st, "pass-refused")
	if got.LastDate != "2026-03-05" || got.LastSlug != "pass-refused-2026-03-05" || got.LastNote == "" || got.LastAt != now.UnixMilli() {
		t.Fatalf("stamp = %+v; want today, the slug, a non-empty note, LastAt now", got)
	}
}

// Contract, periodicPass case (a): "An active job already holds that slug: no
// job is created and start is not called." Stamped with LastDate today, LastAt
// now, LastSlug empty and a non-empty LastNote.
func TestPeriodicContractPassSlugTaken(t *testing.T) {
	st := periodicContractEnv(t)
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "pass-a", Prefix: "pass-a", Template: periodicContractTemplate,
		AtMinute: 9 * 60, Days: 0, Enabled: true,
		ArmedAt: periodicContractAt(2026, 3, 1, 0, 0).UnixMilli(), CreatedAt: 1,
	})
	holder := mustCreateJob(t, st, store.Job{Slug: "pass-a-2026-03-06", Repo: "kraken", Body: "hand made", State: "blocked"})
	before := periodicContractJobCount(t, st)
	now := periodicContractAt(2026, 3, 6, 10, 0)
	s := &periodicContractStarter{answer: true}

	if n := periodicPass(now, s.start); n != 0 {
		t.Fatalf("periodicPass = %d, want 0", n)
	}
	if len(s.calls) != 0 {
		t.Fatalf("start called %d times", len(s.calls))
	}
	if after := periodicContractJobCount(t, st); after != before {
		t.Fatalf("job count %d → %d, want unchanged", before, after)
	}
	j, err := st.GetJobBySlug("pass-a-2026-03-06")
	if err != nil || j.ID != holder.ID || j.Body != "hand made" {
		t.Fatalf("holder job changed: %+v (err %v)", j, err)
	}
	got := periodicContractRowNamed(t, st, "pass-a")
	if got.LastDate != "2026-03-06" || got.LastSlug != "" || got.LastNote == "" || got.LastAt != now.UnixMilli() {
		t.Fatalf("stamp = %+v; want today, empty slug, a note, LastAt now", got)
	}
}

// Contract, periodicPass case (b): "The rendered template no longer yields a
// creatable job: no job is created and start is not called." A template with
// no frontmatter at all is the clearest uncreatable case. Stamped with LastDate
// today, LastAt now, LastSlug empty and a non-empty LastNote.
func TestPeriodicContractPassUncreatable(t *testing.T) {
	st := periodicContractEnv(t)
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "pass-b", Prefix: "pass-b", Template: "no frontmatter here, {{slug}}\n",
		AtMinute: 9 * 60, Days: 0, Enabled: true,
		ArmedAt: periodicContractAt(2026, 3, 1, 0, 0).UnixMilli(), CreatedAt: 1,
	})
	now := periodicContractAt(2026, 3, 7, 10, 0)
	s := &periodicContractStarter{answer: true}

	if n := periodicPass(now, s.start); n != 0 {
		t.Fatalf("periodicPass = %d, want 0", n)
	}
	if len(s.calls) != 0 {
		t.Fatalf("start called %d times", len(s.calls))
	}
	if n := periodicContractJobCount(t, st); n != 0 {
		t.Fatalf("%d job(s) created, want 0", n)
	}
	got := periodicContractRowNamed(t, st, "pass-b")
	if got.LastDate != "2026-03-07" || got.LastSlug != "" || got.LastNote == "" || got.LastAt != now.UnixMilli() {
		t.Fatalf("stamp = %+v; want today, empty slug, a note, LastAt now", got)
	}
}

// Contract, periodicPass: "For every stored row with PeriodicDue(row, now) ...
// A row that is not due is neither read further nor written. The return value
// is the number of start calls that returned true."
func TestPeriodicContractPassOnlyDueRows(t *testing.T) {
	st := periodicContractEnv(t)
	armed := periodicContractAt(2026, 3, 1, 0, 0).UnixMilli()
	seed := func(name string, at, days int, enabled bool, lastDate string) {
		periodicContractSeed(t, st, store.PeriodicJob{
			Name: name, Prefix: name, Template: periodicContractTemplate,
			AtMinute: at, Days: days, Enabled: enabled, ArmedAt: armed, CreatedAt: 1, LastDate: lastDate,
		})
	}
	// 2026-03-09 is a Monday.
	seed("pm-due-one", 8*60, 0, true, "")
	seed("pm-due-two", 9*60, periodicContractMon, true, "2026-03-08")
	seed("pm-later", 11*60, 0, true, "")                    // occurrence not reached
	seed("pm-paused", 8*60, 0, false, "")                   // disabled
	seed("pm-tuesday", 8*60, periodicContractTue, true, "") // not a cadence day
	now := periodicContractAt(2026, 3, 9, 10, 0)
	s := &periodicContractStarter{answer: true}

	if n := periodicPass(now, s.start); n != 2 {
		t.Fatalf("periodicPass = %d, want 2", n)
	}
	started := map[string]bool{}
	for _, j := range s.calls {
		started[j.Slug] = true
	}
	if len(s.calls) != 2 || !started["pm-due-one-2026-03-09"] || !started["pm-due-two-2026-03-09"] {
		t.Fatalf("start slugs = %v, want the two due rows' slugs", started)
	}
	for _, name := range []string{"pm-later", "pm-paused", "pm-tuesday"} {
		got := periodicContractRowNamed(t, st, name)
		if got.LastDate != "" || got.LastSlug != "" || got.LastNote != "" || got.LastAt != 0 {
			t.Fatalf("not-due row %s was written: %+v", name, got)
		}
	}
	if n := periodicContractJobCount(t, st); n != 2 {
		t.Fatalf("%d jobs created, want 2", n)
	}
}

// Contract, periodicPass: once stamped with today, the row is no longer due
// (PeriodicDue: "A LastDate equal to today ... is false"), so a second pass the
// same day creates nothing more.
func TestPeriodicContractPassOncePerDay(t *testing.T) {
	st := periodicContractEnv(t)
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "pass-once", Prefix: "pass-once", Template: periodicContractTemplate,
		AtMinute: 9 * 60, Days: 0, Enabled: true,
		ArmedAt: periodicContractAt(2026, 3, 1, 0, 0).UnixMilli(), CreatedAt: 1,
	})
	s := &periodicContractStarter{answer: true}
	if n := periodicPass(periodicContractAt(2026, 3, 10, 9, 0), s.start); n != 1 {
		t.Fatalf("first pass = %d, want 1", n)
	}
	if n := periodicPass(periodicContractAt(2026, 3, 10, 9, 15), s.start); n != 0 {
		t.Fatalf("second pass = %d, want 0", n)
	}
	if len(s.calls) != 1 || periodicContractJobCount(t, st) != 1 {
		t.Fatalf("start calls %d, jobs %d; want 1 and 1", len(s.calls), periodicContractJobCount(t, st))
	}
}

// ---------------------------------------------------------------- RenameJob

// Contract, RenameJob: "Renaming a fired job away from its prefix is allowed."
func TestPeriodicContractRenameFiredJobAway(t *testing.T) {
	st := periodicContractEnv(t)
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "standup", Prefix: "standup", Template: periodicContractTemplate,
		AtMinute: 9 * 60, Enabled: true, CreatedAt: 1,
	})
	mustCreateJob(t, st, store.Job{Slug: "standup-2026-11-13", Repo: "kraken", Body: "b"})
	j := LoadJobBySlug("standup-2026-11-13")
	if j == nil {
		t.Fatal("fixture job did not load")
	}
	res, err := RenameJob(j, "notes-for-review")
	if err != nil {
		t.Fatalf("RenameJob away from the periodic prefix: %v", err)
	}
	if res.Slug != "notes-for-review" {
		t.Fatalf("renamed slug = %q", res.Slug)
	}
}

// ---------------------------------------------------------------- tick

// Contract, tick: "With mirror == true it never calls [periodicPass]." A row
// that is due at every instant (00:00 daily, armed at 0, never fired) stays
// unstamped and no job appears. The mirror == false arm is not exercised: its
// start func is d.startCreatedJob, which reaches the real SubmitJob, and the
// contract names no seam to replace it.
func TestPeriodicContractMirrorTickSkipsPass(t *testing.T) {
	st := periodicContractEnv(t)
	periodicContractSeed(t, st, store.PeriodicJob{
		Name: "mirror-row", Prefix: "mirror-row", Template: periodicContractTemplate,
		AtMinute: 0, Days: 0, Enabled: true, ArmedAt: 0, CreatedAt: 1,
	})
	d := newDaemon()
	if _, err := d.tick(true); err != nil {
		t.Fatalf("mirror tick: %v", err)
	}
	got := periodicContractRowNamed(t, st, "mirror-row")
	if got.LastDate != "" || got.LastAt != 0 || got.LastSlug != "" || got.LastNote != "" {
		t.Fatalf("mirror tick stamped the row: %+v", got)
	}
	if n := periodicContractJobCount(t, st); n != 0 {
		t.Fatalf("mirror tick created %d job(s)", n)
	}
}
