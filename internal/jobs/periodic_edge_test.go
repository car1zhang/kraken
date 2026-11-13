package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/store"
)

// periodic_edge_test.go — athena-edge's blind suite for slice 2 of the
// periodic-jobs contract (50-contract.md, "Slice 2: cadence, firing and the
// tick"): ParseCadence's refusals and range ends, the due/next-due boundaries
// (the exact occurrence instant, ArmedAt at and past it, LastDate today and
// later, a zone whose date differs from UTC's), AddPeriodic's every refusal,
// the built-in and not-found errors, RunPeriodicNow's slug ladder against
// active and archived holders, periodicPass's three branches and its return
// count, and RenameJob's prefix refusal. Written from the contract alone.
//
// Every test runs under a scratch HOME and repos dir with its own store, and
// with periodicSubmit and periodicNotify replaced, so nothing starts a cell or
// runs osascript. periodicPass tests each use a DIFFERENT calendar date far
// from any real one: the contract's implementer note asks for an in-process
// fired-date record per row, and fresh stores reuse row ids, so two tests
// firing "row 1 on the same date" could otherwise suppress each other.

// periodicEdgeLoc is a fixed zone ten hours behind UTC, so a late-evening
// local time is already the next UTC date.
var periodicEdgeLoc = time.FixedZone("periodic-edge", -10*3600)

const periodicEdgeTemplate = "---\nrepo: kraken\n---\nreport for {{slug}} on {{date}}\n"

// periodicEdgeSubmit records RunPeriodicNow's submits.
type periodicEdgeSubmit struct {
	calls []*Job
	err   error
}

// periodicEdgeEnv sandboxes HOME and the repos dir (with a resolvable
// "kraken" checkout), wires a scratch store, and replaces both seams.
func periodicEdgeEnv(t *testing.T) (*store.Store, *periodicEdgeSubmit) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	repos := filepath.Join(home, "Developer")
	if err := os.MkdirAll(filepath.Join(repos, "kraken", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() { UseStore(nil); st.Close() })

	sub := &periodicEdgeSubmit{}
	prevSubmit, prevNotify := periodicSubmit, periodicNotify
	periodicSubmit = func(j *Job) error {
		sub.calls = append(sub.calls, j)
		return sub.err
	}
	periodicNotify = func(title, msg string) {}
	t.Cleanup(func() { periodicSubmit, periodicNotify = prevSubmit, prevNotify })
	return st, sub
}

func periodicEdgeDate(y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, periodicEdgeLoc)
}

func periodicEdgeMS(t time.Time) int64 { return t.UnixMilli() }

// periodicEdgeStoreRow inserts a row directly, bypassing AddPeriodic's gates.
func periodicEdgeStoreRow(t *testing.T, st *store.Store, p store.PeriodicJob) store.PeriodicJob {
	t.Helper()
	id, err := st.CreatePeriodicJob(&p)
	if err != nil {
		t.Fatalf("CreatePeriodicJob(%q): %v", p.Name, err)
	}
	got, err := st.GetPeriodicJob(p.Name)
	if err != nil || got.ID != id {
		t.Fatalf("GetPeriodicJob(%q) = %+v, %v (id %d)", p.Name, got, err, id)
	}
	return *got
}

func periodicEdgeGetRow(t *testing.T, st *store.Store, name string) store.PeriodicJob {
	t.Helper()
	got, err := st.GetPeriodicJob(name)
	if err != nil {
		t.Fatalf("GetPeriodicJob(%q): %v", name, err)
	}
	return *got
}

func periodicEdgeActiveJob(slug string) *Job { return JobBySlug(LoadJobs(), slug) }

func periodicEdgeMakeJob(t *testing.T, st *store.Store, slug string) *store.Job {
	t.Helper()
	j := store.Job{Slug: slug, Repo: "kraken", Body: "spec\n"}
	id, err := st.CreateJob(&j)
	if err != nil {
		t.Fatalf("CreateJob(%q): %v", slug, err)
	}
	j.ID = id
	return &j
}

// ---------------------------------------------------------------- ParseCadence

// Contract: "`at` is `H:MM` or `HH:MM` on a 24-hour clock, hour 0 to 23 and
// minute 00 to 59; `AtMinute` is hour*60+minute. `days` is trimmed and
// case-insensitive: `\"\"` and `daily` give `Days == 0`; `weekdays` gives Monday
// to Friday; otherwise a comma-separated list ... (spaces around an element are
// trimmed, a repeated name is fine) gives exactly the named bits."
func TestPeriodicEdgeParseCadenceAccepts(t *testing.T) {
	mon, tue, wed, thu, fri := 1<<1, 1<<2, 1<<3, 1<<4, 1<<5
	for _, tc := range []struct {
		at, days  string
		min, bits int
	}{
		{"0:00", "", 0, 0},
		{"00:00", "daily", 0, 0},
		{"23:59", "  DAILY ", 1439, 0},
		{"9:05", "Weekdays", 545, mon | tue | wed | thu | fri},
		{"09:05", "  WEEKDAYS", 545, mon | tue | wed | thu | fri},
		{"12:00", "sun", 720, 1},
		{"12:00", "sat", 720, 1 << 6},
		{"12:00", " mon , MON ", 720, mon},
		{"7:30", "fri,mon", 450, mon | fri},
		{"7:30", "   ", 450, 0},
		// all seven named is the named bits, not the "every day" zero
		{"12:00", "SUN,mon,tue,wed,thu,fri,sat", 720, 127},
	} {
		c, err := ParseCadence(tc.at, tc.days)
		if err != nil {
			t.Errorf("ParseCadence(%q, %q): %v", tc.at, tc.days, err)
			continue
		}
		if c.AtMinute != tc.min || c.Days != tc.bits {
			t.Errorf("ParseCadence(%q, %q) = %+v, want AtMinute %d Days %d", tc.at, tc.days, c, tc.min, tc.bits)
		}
	}
}

// Contract: "Anything else, including an empty list element, is an error for
// which `errors.Is(err, ErrPeriodicInvalid)` holds."
func TestPeriodicEdgeParseCadenceRefusesAt(t *testing.T) {
	for _, at := range []string{
		"", "9", "0900", "24:00", "23:60", "9:5", "9:005", "123:00", "09:00:00",
		"-1:00", "+9:00", "09:+5", "a:bc", "09:0a", ":30", "9:", "12-30",
	} {
		if _, err := ParseCadence(at, ""); !errors.Is(err, ErrPeriodicInvalid) {
			t.Errorf("ParseCadence(%q, \"\"): err = %v, want ErrPeriodicInvalid", at, err)
		}
	}
}

// Contract: as above, for `days`.
func TestPeriodicEdgeParseCadenceRefusesDays(t *testing.T) {
	for _, days := range []string{
		"mon,", ",mon", "mon,,tue", "mon, ,tue", "sun,", ",",
		"monday", "weekend", "every day", "mon;tue", "mon tue", "1",
		"daily,mon", "weekdays,sat", "m", "thur",
	} {
		if _, err := ParseCadence("09:00", days); !errors.Is(err, ErrPeriodicInvalid) {
			t.Errorf("ParseCadence(\"09:00\", %q): err = %v, want ErrPeriodicInvalid", days, err)
		}
	}
}

// Contract: "`Cadence.At` — The time as zero-padded `HH:MM`."
// "`Cadence.DayNames` — Zero length when `Days == 0`. Otherwise the set days as
// lowercase three-letter names, Monday first and Sunday last."
// "`Cadence.String` — `daily HH:MM` when `Days == 0`, otherwise the `DayNames`
// joined by commas, a space, and `HH:MM`."
func TestPeriodicEdgeCadenceRendering(t *testing.T) {
	for _, tc := range []struct {
		c     Cadence
		at    string
		names []string
		str   string
	}{
		{Cadence{AtMinute: 0, Days: 0}, "00:00", nil, "daily 00:00"},
		{Cadence{AtMinute: 1439, Days: 0}, "23:59", nil, "daily 23:59"},
		{Cadence{AtMinute: 65, Days: 1}, "01:05", []string{"sun"}, "sun 01:05"},
		{Cadence{AtMinute: 600, Days: 1 | 1<<6}, "10:00", []string{"sat", "sun"}, "sat,sun 10:00"},
		{Cadence{AtMinute: 450, Days: 127}, "07:30",
			[]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"},
			"mon,tue,wed,thu,fri,sat,sun 07:30"},
	} {
		if got := tc.c.At(); got != tc.at {
			t.Errorf("%+v.At() = %q, want %q", tc.c, got, tc.at)
		}
		names := tc.c.DayNames()
		if len(names) != len(tc.names) || strings.Join(names, ",") != strings.Join(tc.names, ",") {
			t.Errorf("%+v.DayNames() = %v, want %v", tc.c, names, tc.names)
		}
		if got := tc.c.String(); got != tc.str {
			t.Errorf("%+v.String() = %q, want %q", tc.c, got, tc.str)
		}
	}
}

// ---------------------------------------------------------------- PeriodicDue

// periodicEdgeDueRow: enabled, daily at 10:00, never fired, armed long ago.
func periodicEdgeDueRow() store.PeriodicJob {
	return store.PeriodicJob{ID: 1, Name: "r", Prefix: "r", Template: periodicEdgeTemplate, AtMinute: 600, Enabled: true, ArmedAt: 0}
}

// Contract: PeriodicDue "True exactly when all of these hold: `p.Enabled`;
// `p.Days == 0` or today's weekday bit is set; `now` is at or after today's
// occurrence; today's occurrence is not before `p.ArmedAt`; `p.LastDate` sorts
// before today. A `LastDate` equal to today or later than today is false."
// 2026-11-13 is a Friday.
func TestPeriodicEdgeDueBoundaries(t *testing.T) {
	occ := periodicEdgeDate(2026, time.November, 13, 10, 0)
	type tc struct {
		name string
		mod  func(p *store.PeriodicJob)
		now  time.Time
		want bool
	}
	none := func(*store.PeriodicJob) {}
	for _, c := range []tc{
		{"exactly at the occurrence", none, occ, true},
		{"one nanosecond before", none, occ.Add(-time.Nanosecond), false},
		{"last instant of the day", none, periodicEdgeDate(2026, time.November, 13, 23, 59).Add(59*time.Second + 999*time.Millisecond), true},
		{"disabled", func(p *store.PeriodicJob) { p.Enabled = false }, occ, false},
		{"LastDate yesterday", func(p *store.PeriodicJob) { p.LastDate = "2026-11-12" }, occ, true},
		{"LastDate today", func(p *store.PeriodicJob) { p.LastDate = "2026-11-13" }, occ, false},
		{"LastDate tomorrow", func(p *store.PeriodicJob) { p.LastDate = "2026-11-14" }, occ, false},
		{"LastDate next year", func(p *store.PeriodicJob) { p.LastDate = "2027-01-01" }, occ, false},
		{"ArmedAt exactly the occurrence", func(p *store.PeriodicJob) { p.ArmedAt = periodicEdgeMS(occ) }, occ.Add(time.Minute), true},
		{"ArmedAt one ms after the occurrence", func(p *store.PeriodicJob) { p.ArmedAt = periodicEdgeMS(occ) + 1 }, occ.Add(time.Minute), false},
		{"Friday bit set", func(p *store.PeriodicJob) { p.Days = 1 << 5 }, occ, true},
		{"only Thursday", func(p *store.PeriodicJob) { p.Days = 1 << 4 }, occ, false},
		{"every bit", func(p *store.PeriodicJob) { p.Days = 127 }, occ, true},
		{"all but Friday", func(p *store.PeriodicJob) { p.Days = 127 &^ (1 << 5) }, occ, false},
		{"AtMinute 0 at midnight", func(p *store.PeriodicJob) { p.AtMinute = 0 }, periodicEdgeDate(2026, time.November, 13, 0, 0), true},
		{"AtMinute 1439 one minute early", func(p *store.PeriodicJob) { p.AtMinute = 1439 }, periodicEdgeDate(2026, time.November, 13, 23, 58), false},
		{"AtMinute 1439 on time", func(p *store.PeriodicJob) { p.AtMinute = 1439 }, periodicEdgeDate(2026, time.November, 13, 23, 59), true},
	} {
		p := periodicEdgeDueRow()
		c.mod(&p)
		if got := PeriodicDue(p, c.now); got != c.want {
			t.Errorf("%s: PeriodicDue = %v, want %v", c.name, got, c.want)
		}
	}
}

// Contract: "\"Today\" is `now`'s date in `now`'s own location", and the
// occurrence is "in `now`'s location". At 22:00 on Friday 2026-11-13 ten hours
// behind UTC it is already Saturday the 14th in UTC; the local reading must win
// for the date, the weekday and the occurrence.
func TestPeriodicEdgeDueUsesNowsLocation(t *testing.T) {
	now := periodicEdgeDate(2026, time.November, 13, 22, 0)
	p := periodicEdgeDueRow()
	p.LastDate = "2026-11-13"
	if PeriodicDue(p, now) {
		t.Error("LastDate equal to the LOCAL today read as due (today taken from UTC?)")
	}
	p.LastDate = "2026-11-12"
	p.Days = 1 << 5 // Friday only
	if !PeriodicDue(p, now) {
		t.Error("Friday-only row not due on a local Friday evening (weekday taken from UTC?)")
	}
	p.Days = 1 << 6 // Saturday only
	if PeriodicDue(p, now) {
		t.Error("Saturday-only row due on a local Friday (weekday taken from UTC?)")
	}
	// an occurrence at 09:00 local is 19:00 UTC; a 21:00-local occurrence has
	// already passed at 22:00 local
	p.Days, p.AtMinute = 0, 21*60
	if !PeriodicDue(p, now) {
		t.Error("21:00 local occurrence not due at 22:00 local")
	}
	p.AtMinute = 23 * 60
	if PeriodicDue(p, now) {
		t.Error("23:00 local occurrence due at 22:00 local")
	}
}

// ---------------------------------------------------------------- PeriodicNextDue

// Contract: PeriodicNextDue "The zero time when `!p.Enabled`. Today's
// occurrence when `PeriodicDue(p, now)`. Otherwise, for a `LastDate` not later
// than today, the earliest occurrence after `now` that falls on a cadence day
// and is not before `p.ArmedAt`."
func TestPeriodicEdgeNextDueBoundaries(t *testing.T) {
	occ := periodicEdgeDate(2026, time.November, 13, 10, 0) // Friday
	tomorrow := periodicEdgeDate(2026, time.November, 14, 10, 0)
	for _, c := range []struct {
		name string
		mod  func(p *store.PeriodicJob)
		now  time.Time
		want time.Time
	}{
		{"due now is today's occurrence", func(p *store.PeriodicJob) {}, occ.Add(3 * time.Hour), occ},
		{"exactly at the occurrence, already fired", func(p *store.PeriodicJob) { p.LastDate = "2026-11-13" }, occ, tomorrow},
		{"after the occurrence, already fired", func(p *store.PeriodicJob) { p.LastDate = "2026-11-13" }, occ.Add(time.Minute), tomorrow},
		{"before today's occurrence", func(p *store.PeriodicJob) {}, occ.Add(-time.Minute), occ},
		{"Monday only, from a fired Friday", func(p *store.PeriodicJob) { p.Days = 1 << 1; p.LastDate = "2026-11-13" }, occ.Add(time.Hour),
			periodicEdgeDate(2026, time.November, 16, 10, 0)},
		{"Monday only, from a Friday morning", func(p *store.PeriodicJob) { p.Days = 1 << 1 }, occ.Add(-time.Hour),
			periodicEdgeDate(2026, time.November, 16, 10, 0)},
		{"ArmedAt equals a later occurrence", func(p *store.PeriodicJob) {
			p.LastDate = "2026-11-13"
			p.ArmedAt = periodicEdgeMS(periodicEdgeDate(2026, time.November, 15, 10, 0))
		}, occ.Add(time.Hour), periodicEdgeDate(2026, time.November, 15, 10, 0)},
		{"ArmedAt one ms past a later occurrence", func(p *store.PeriodicJob) {
			p.LastDate = "2026-11-13"
			p.ArmedAt = periodicEdgeMS(periodicEdgeDate(2026, time.November, 15, 10, 0)) + 1
		}, occ.Add(time.Hour), periodicEdgeDate(2026, time.November, 16, 10, 0)},
		{"ArmedAt past today's occurrence, never fired", func(p *store.PeriodicJob) {
			p.ArmedAt = periodicEdgeMS(occ) + 1
		}, occ.Add(time.Hour), tomorrow},
	} {
		p := periodicEdgeDueRow()
		c.mod(&p)
		got := PeriodicNextDue(p, c.now)
		if !got.Equal(c.want) {
			t.Errorf("%s: PeriodicNextDue = %v, want %v", c.name, got, c.want)
		}
	}
}

// Contract: "The zero time when `!p.Enabled`" — whatever else the row says.
func TestPeriodicEdgeNextDueDisabledIsZero(t *testing.T) {
	now := periodicEdgeDate(2026, time.November, 13, 10, 0)
	for _, last := range []string{"", "2026-11-12", "2026-11-13"} {
		p := periodicEdgeDueRow()
		p.Enabled = false
		p.LastDate = last
		if got := PeriodicNextDue(p, now); !got.IsZero() {
			t.Errorf("disabled, LastDate %q: PeriodicNextDue = %v, want the zero time", last, got)
		}
	}
}

// ---------------------------------------------------------------- RenderPeriodicTemplate

// Contract: "Replaces every `{{slug}}` with `slug` and every `{{date}}` with
// `date`. Changes nothing else."
func TestPeriodicEdgeRenderTemplate(t *testing.T) {
	const slug, date = "nightly-2026-11-13", "2026-11-13"
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{"plain text\n", "plain text\n"},
		{"{slug} {{ slug }} {{SLUG}} {{date }} {{Date}} {{slug", "{slug} {{ slug }} {{SLUG}} {{date }} {{Date}} {{slug"},
		{"{{slug}}{{slug}}/{{date}}{{date}}", slug + slug + "/" + date + date},
		{"{{{slug}}}", "{" + slug + "}"},
		{"---\njob: {{slug}}\n---\n\n  {{date}}  \r\n", "---\njob: " + slug + "\n---\n\n  " + date + "  \r\n"},
	} {
		if got := RenderPeriodicTemplate(c.in, slug, date); got != c.want {
			t.Errorf("RenderPeriodicTemplate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// ---------------------------------------------------------------- AddPeriodic

type periodicEdgeAddArgs struct{ name, prefix, at, days, template string }

func periodicEdgeValidAdd() periodicEdgeAddArgs {
	return periodicEdgeAddArgs{"nightly report", "nightly", "09:00", "", periodicEdgeTemplate}
}

// Contract: AddPeriodic — "Each refusal below returns an error for which
// `errors.Is(err, ErrPeriodicInvalid)` holds and stores nothing." Each case
// changes exactly one argument of an otherwise valid add.
func TestPeriodicEdgeAddRefusals(t *testing.T) {
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	for _, c := range []struct {
		why string
		mod func(a *periodicEdgeAddArgs)
	}{
		{"empty name", func(a *periodicEdgeAddArgs) { a.name = "" }},
		{"blank name", func(a *periodicEdgeAddArgs) { a.name = "  \t " }},
		{"two-line name", func(a *periodicEdgeAddArgs) { a.name = "nightly\nreport" }},
		{"65-rune name", func(a *periodicEdgeAddArgs) { a.name = strings.Repeat("é", 65) }},
		{"name dream", func(a *periodicEdgeAddArgs) { a.name = "dream" }},
		{"name dream padded", func(a *periodicEdgeAddArgs) { a.name = "  dream " }},
		{"empty prefix", func(a *periodicEdgeAddArgs) { a.prefix = "" }},
		{"uppercase prefix", func(a *periodicEdgeAddArgs) { a.prefix = "Nightly" }},
		{"prefix with a space", func(a *periodicEdgeAddArgs) { a.prefix = "night ly" }},
		{"prefix with a double dash", func(a *periodicEdgeAddArgs) { a.prefix = "night--ly" }},
		{"prefix with a leading dash", func(a *periodicEdgeAddArgs) { a.prefix = "-nightly" }},
		{"prefix with a trailing dash", func(a *periodicEdgeAddArgs) { a.prefix = "nightly-" }},
		{"prefix with an underscore", func(a *periodicEdgeAddArgs) { a.prefix = "night_ly" }},
		{"61-rune prefix", func(a *periodicEdgeAddArgs) { a.prefix = strings.Repeat("a", 61) }},
		{"prefix dream", func(a *periodicEdgeAddArgs) { a.prefix = "dream" }},
		{"prefix dream-x", func(a *periodicEdgeAddArgs) { a.prefix = "dream-x" }},
		{"prefix graph", func(a *periodicEdgeAddArgs) { a.prefix = "graph" }},
		{"prefix graph-1", func(a *periodicEdgeAddArgs) { a.prefix = "graph-1" }},
		{"bad at", func(a *periodicEdgeAddArgs) { a.at = "24:00" }},
		{"empty at", func(a *periodicEdgeAddArgs) { a.at = "" }},
		{"bad days", func(a *periodicEdgeAddArgs) { a.days = "mon,,tue" }},
		{"unknown day", func(a *periodicEdgeAddArgs) { a.days = "funday" }},
		{"template with no frontmatter", func(a *periodicEdgeAddArgs) { a.template = "just a body\n" }},
		{"template with unterminated frontmatter", func(a *periodicEdgeAddArgs) { a.template = "---\nrepo: kraken\nbody\n" }},
		{"template with an unknown key", func(a *periodicEdgeAddArgs) { a.template = "---\nrepo: kraken\nbogus: 1\n---\nbody\n" }},
		{"empty template", func(a *periodicEdgeAddArgs) { a.template = "" }},
		{"template with a blank body", func(a *periodicEdgeAddArgs) { a.template = "---\nrepo: kraken\n---\n  \n\t\n" }},
		{"template setting state: working", func(a *periodicEdgeAddArgs) { a.template = "---\nrepo: kraken\nstate: working\n---\nbody\n" }},
		{"template setting state: done", func(a *periodicEdgeAddArgs) { a.template = "---\nrepo: kraken\nstate: done\n---\nbody\n" }},
		{"template naming no repo", func(a *periodicEdgeAddArgs) { a.template = "---\nsolo: false\n---\nbody\n" }},
		{"template naming an unresolvable repo", func(a *periodicEdgeAddArgs) { a.template = "---\nrepo: no-such-checkout-anywhere\n---\nbody\n" }},
		// the template is checked RENDERED: repo {{slug}} renders to
		// "nightly-2026-11-13", which no checkout name contains
		{"template whose rendered repo resolves nowhere", func(a *periodicEdgeAddArgs) { a.template = "---\nrepo: {{slug}}\n---\nbody\n" }},
	} {
		t.Run(c.why, func(t *testing.T) {
			st, sub := periodicEdgeEnv(t)
			a := periodicEdgeValidAdd()
			c.mod(&a)
			p, err := AddPeriodic(a.name, a.prefix, a.at, a.days, a.template, now)
			if !errors.Is(err, ErrPeriodicInvalid) {
				t.Fatalf("AddPeriodic: (%+v, %v), want ErrPeriodicInvalid", p, err)
			}
			rows, lerr := st.ListPeriodicJobs()
			if lerr != nil || len(rows) != 0 {
				t.Fatalf("rows after refusal = %+v, %v; want none stored", rows, lerr)
			}
			if n := len(LoadJobs()); n != 0 || len(sub.calls) != 0 {
				t.Fatalf("refusal created %d jobs / %d submits", n, len(sub.calls))
			}
		})
	}
}

// Contract: AddPeriodic's limits are inclusive at the boundary — a name "at
// most 64 runes", a prefix "at most 60 runes" — and the reserved check is on
// `prefix + "-"` starting with `dream-` or `graph-`, so a prefix that merely
// begins with those letters is fine. A body made only of a placeholder is
// non-blank once rendered. "On success ... `Template` is the text as given,
// unrendered. Adding creates no job."
func TestPeriodicEdgeAddBoundaryAccepts(t *testing.T) {
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	for i, c := range []periodicEdgeAddArgs{
		{strings.Repeat("é", 64), "edge-a", "0:00", "", periodicEdgeTemplate},
		{"edge b", strings.Repeat("a", 60), "23:59", "sun", periodicEdgeTemplate},
		{"edge c", "dreamy", "12:00", "weekdays", periodicEdgeTemplate},
		{"edge d", "graphs", "12:00", "", periodicEdgeTemplate},
		{"edge e", "my-dream", "12:00", "", periodicEdgeTemplate},
		{"edge f", "edge-f", "12:00", "", "---\nrepo: kraken\n---\n{{date}}\n"},
	} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			st, sub := periodicEdgeEnv(t)
			p, err := AddPeriodic(c.name, c.prefix, c.at, c.days, c.template, now)
			if err != nil {
				t.Fatalf("AddPeriodic(%q, %q, %q, %q): %v", c.name, c.prefix, c.at, c.days, err)
			}
			if p == nil || p.Template != c.template || p.Prefix != c.prefix {
				t.Fatalf("returned row = %+v", p)
			}
			rows, _ := st.ListPeriodicJobs()
			if len(rows) != 1 || rows[0].Template != c.template {
				t.Fatalf("stored rows = %+v, want one with the template unrendered", rows)
			}
			if !rows[0].Enabled || rows[0].ArmedAt != now.UnixMilli() || rows[0].CreatedAt != now.UnixMilli() {
				t.Fatalf("stored row = %+v, want enabled with ArmedAt/CreatedAt %d", rows[0], now.UnixMilli())
			}
			if n := len(LoadJobs()); n != 0 || len(sub.calls) != 0 {
				t.Fatalf("add created %d jobs / %d submits", n, len(sub.calls))
			}
		})
	}
}

// Contract: "A name or prefix another row holds surfaces the store's error
// (`errors.Is(err, store.ErrPeriodicTaken)`)."
func TestPeriodicEdgeAddTaken(t *testing.T) {
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	st, _ := periodicEdgeEnv(t)
	if _, err := AddPeriodic("nightly report", "nightly", "09:00", "", periodicEdgeTemplate, now); err != nil {
		t.Fatalf("first add: %v", err)
	}
	if _, err := AddPeriodic("nightly report", "other", "09:00", "", periodicEdgeTemplate, now); !errors.Is(err, store.ErrPeriodicTaken) {
		t.Errorf("same name: err = %v, want store.ErrPeriodicTaken", err)
	}
	if _, err := AddPeriodic("another report", "nightly", "09:00", "", periodicEdgeTemplate, now); !errors.Is(err, store.ErrPeriodicTaken) {
		t.Errorf("same prefix: err = %v, want store.ErrPeriodicTaken", err)
	}
	if rows, _ := st.ListPeriodicJobs(); len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
}

// ---------------------------------------------------------------- SetPeriodicPaused / RemovePeriodic

// Contract: SetPeriodicPaused — "`dream`: `ErrPeriodicBuiltin`. No such row:
// `store.ErrNotFound`." RemovePeriodic — the same two. Both directions of
// pause, with no stored row of either name.
func TestPeriodicEdgeBuiltinAndMissing(t *testing.T) {
	periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	for _, paused := range []bool{true, false} {
		if _, err := SetPeriodicPaused(PeriodicBuiltinDream, paused, now); !errors.Is(err, ErrPeriodicBuiltin) {
			t.Errorf("SetPeriodicPaused(dream, %v): err = %v, want ErrPeriodicBuiltin", paused, err)
		}
		if _, err := SetPeriodicPaused("ghost", paused, now); !errors.Is(err, store.ErrNotFound) {
			t.Errorf("SetPeriodicPaused(ghost, %v): err = %v, want store.ErrNotFound", paused, err)
		}
	}
	if err := RemovePeriodic(PeriodicBuiltinDream); !errors.Is(err, ErrPeriodicBuiltin) {
		t.Errorf("RemovePeriodic(dream): err = %v, want ErrPeriodicBuiltin", err)
	}
	if err := RemovePeriodic("ghost"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("RemovePeriodic(ghost): err = %v, want store.ErrNotFound", err)
	}
}

// Contract: "`paused == false` enables it and sets `ArmedAt` to `now`. Returns
// the row as stored afterwards." — also when the row is already enabled.
// "`paused == true` disables the row" — also when it is already disabled.
func TestPeriodicEdgePauseResumeIdempotentShapes(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	created := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := AddPeriodic("nightly report", "nightly", "09:00", "", periodicEdgeTemplate, created); err != nil {
		t.Fatalf("add: %v", err)
	}
	later := created.Add(26 * time.Hour)
	got, err := SetPeriodicPaused("nightly report", false, later)
	if err != nil {
		t.Fatalf("resume an enabled row: %v", err)
	}
	if !got.Enabled || got.ArmedAt != later.UnixMilli() {
		t.Fatalf("returned = %+v, want enabled and ArmedAt %d", got, later.UnixMilli())
	}
	if row := periodicEdgeGetRow(t, st, "nightly report"); row.ArmedAt != later.UnixMilli() {
		t.Fatalf("stored ArmedAt = %d, want %d", row.ArmedAt, later.UnixMilli())
	}
	for i := 0; i < 2; i++ {
		got, err := SetPeriodicPaused("nightly report", true, later)
		if err != nil {
			t.Fatalf("pause #%d: %v", i, err)
		}
		if got.Enabled {
			t.Fatalf("pause #%d returned an enabled row", i)
		}
		if row := periodicEdgeGetRow(t, st, "nightly report"); row.Enabled {
			t.Fatalf("pause #%d: stored row still enabled", i)
		}
	}
}

// Contract: RemovePeriodic "Deletes the row. Every job it fired stays as it
// is."
func TestPeriodicEdgeRemoveKeepsFiredJobs(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := AddPeriodic("nightly report", "nightly", "09:00", "", periodicEdgeTemplate, now); err != nil {
		t.Fatalf("add: %v", err)
	}
	run, err := RunPeriodicNow("nightly report", now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := RemovePeriodic("nightly report"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := st.GetPeriodicJob("nightly report"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("row survives remove: %v", err)
	}
	if periodicEdgeActiveJob(run.Slug) == nil {
		t.Fatalf("fired job %q gone after remove", run.Slug)
	}
	if err := RemovePeriodic("nightly report"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("second remove: err = %v, want store.ErrNotFound", err)
	}
}

// ---------------------------------------------------------------- RunPeriodicNow

// Contract: RunPeriodicNow — "`dream`: `ErrPeriodicBuiltin`. No such row:
// `store.ErrNotFound`." Neither creates a job or reaches the submit.
func TestPeriodicEdgeRunBuiltinAndMissing(t *testing.T) {
	_, sub := periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := RunPeriodicNow(PeriodicBuiltinDream, now); !errors.Is(err, ErrPeriodicBuiltin) {
		t.Errorf("run dream: err = %v, want ErrPeriodicBuiltin", err)
	}
	if _, err := RunPeriodicNow("ghost", now); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("run ghost: err = %v, want store.ErrNotFound", err)
	}
	if len(sub.calls) != 0 || len(LoadJobs()) != 0 {
		t.Fatalf("refused runs made %d submits, %d jobs", len(sub.calls), len(LoadJobs()))
	}
}

// Contract: "The slug is the first of `<prefix>-<today>-run`,
// `<prefix>-<today>-run-2`, `<prefix>-<today>-run-3`, and so on, that no
// active job holds." Two holders push it to -run-3; the template is rendered
// with THAT slug; "calls `periodicSubmit` with it exactly once".
func TestPeriodicEdgeRunSlugLadderSkipsActiveHolders(t *testing.T) {
	st, sub := periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := AddPeriodic("nightly report", "nightly", "09:00", "", periodicEdgeTemplate, now); err != nil {
		t.Fatalf("add: %v", err)
	}
	periodicEdgeMakeJob(t, st, "nightly-2026-11-13-run")
	periodicEdgeMakeJob(t, st, "nightly-2026-11-13-run-2")
	run, err := RunPeriodicNow("nightly report", now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Slug != "nightly-2026-11-13-run-3" {
		t.Fatalf("Slug = %q, want nightly-2026-11-13-run-3", run.Slug)
	}
	if len(sub.calls) != 1 || sub.calls[0].Slug != run.Slug {
		t.Fatalf("submits = %d (first %v), want exactly one for %q", len(sub.calls), sub.calls, run.Slug)
	}
	j := periodicEdgeActiveJob(run.Slug)
	if j == nil {
		t.Fatalf("job %q not created", run.Slug)
	}
	if !strings.Contains(j.Body, "report for nightly-2026-11-13-run-3 on 2026-11-13") {
		t.Fatalf("body = %q, want the template rendered with the -run-3 slug", j.Body)
	}
}

// Contract: the ladder skips slugs that an ACTIVE job holds ("An \"active job\"
// is a job row that is not archived"), so an archived holder of -run does not
// push it. The scheduled slug `<prefix>-<today>` is not on the ladder at all.
func TestPeriodicEdgeRunSlugIgnoresArchivedAndScheduledHolders(t *testing.T) {
	st, sub := periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := AddPeriodic("nightly report", "nightly", "09:00", "", periodicEdgeTemplate, now); err != nil {
		t.Fatalf("add: %v", err)
	}
	old := periodicEdgeMakeJob(t, st, "nightly-2026-11-13-run")
	if _, err := st.ArchiveJob(old.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	periodicEdgeMakeJob(t, st, "nightly-2026-11-13")
	run, err := RunPeriodicNow("nightly report", now)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if run.Slug != "nightly-2026-11-13-run" {
		t.Fatalf("Slug = %q, want nightly-2026-11-13-run", run.Slug)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("submits = %d, want 1", len(sub.calls))
	}
}

// Contract: the ladder is consulted on every run — a second run on the same
// day finds the first one's job active and takes -run-2.
func TestPeriodicEdgeRunTwiceSameDay(t *testing.T) {
	_, sub := periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := AddPeriodic("nightly report", "nightly", "09:00", "", periodicEdgeTemplate, now); err != nil {
		t.Fatalf("add: %v", err)
	}
	first, err := RunPeriodicNow("nightly report", now)
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	second, err := RunPeriodicNow("nightly report", now.Add(time.Minute))
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if first.Slug != "nightly-2026-11-13-run" || second.Slug != "nightly-2026-11-13-run-2" {
		t.Fatalf("slugs = %q, %q; want -run then -run-2", first.Slug, second.Slug)
	}
	if len(sub.calls) != 2 {
		t.Fatalf("submits = %d, want 2", len(sub.calls))
	}
}

// Contract: "Fires the row once, on demand, whether or not it is enabled or
// due" and "today" is now's date in now's location: 22:00 local on the 13th is
// the 14th in UTC, and the slug carries the 13th.
func TestPeriodicEdgeRunDisabledAndLocalDate(t *testing.T) {
	st, sub := periodicEdgeEnv(t)
	created := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := AddPeriodic("nightly report", "nightly", "23:59", "mon", periodicEdgeTemplate, created); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := st.SetPeriodicEnabled("nightly report", false, 0); err != nil {
		t.Fatalf("disable: %v", err)
	}
	run, err := RunPeriodicNow("nightly report", periodicEdgeDate(2026, time.November, 13, 22, 0))
	if err != nil {
		t.Fatalf("run a disabled, not-due row: %v", err)
	}
	if run.Slug != "nightly-2026-11-13-run" || !run.Started || run.Note != "" {
		t.Fatalf("run = %+v, want nightly-2026-11-13-run started with no note", run)
	}
	if len(sub.calls) != 1 {
		t.Fatalf("submits = %d, want 1", len(sub.calls))
	}
}

// Contract: "Returns the slug, `Started` true when the submit returned nil, and
// the submit's error text in `Note` otherwise (the job then stays on the board
// as a draft). Leaves all four `Last*` fields alone."
func TestPeriodicEdgeRunSubmitRefusedKeepsDraftAndLastFields(t *testing.T) {
	st, sub := periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	if _, err := AddPeriodic("nightly report", "nightly", "09:00", "", periodicEdgeTemplate, now); err != nil {
		t.Fatalf("add: %v", err)
	}
	row := periodicEdgeGetRow(t, st, "nightly report")
	if err := st.StampPeriodicFiring(row.ID, "2026-11-12", "nightly-2026-11-12", "earlier note", 123); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	sub.err = errors.New("periodic-edge: submit refused for the test")
	run, err := RunPeriodicNow("nightly report", now)
	if err != nil {
		t.Fatalf("run with a refused submit returned an error: %v", err)
	}
	if run.Started || run.Note != "periodic-edge: submit refused for the test" || run.Slug != "nightly-2026-11-13-run" {
		t.Fatalf("run = %+v, want Started false, the submit's text as Note", run)
	}
	if periodicEdgeActiveJob(run.Slug) == nil {
		t.Fatalf("job %q not left on the board", run.Slug)
	}
	after := periodicEdgeGetRow(t, st, "nightly report")
	if after.LastDate != "2026-11-12" || after.LastSlug != "nightly-2026-11-12" || after.LastNote != "earlier note" || after.LastAt != 123 {
		t.Fatalf("Last* moved: %+v", after)
	}
}

// ---------------------------------------------------------------- ListPeriodic

// Contract: ListPeriodic — "First the built-in dream row: `Name` and `Prefix`
// `dream`, `AtMinute` 0, `Days` 0, `Enabled` true, `Builtin` true, `Template`
// empty, `LastDate` and `LastSlug` taken from `ReadDreamRun()`" — present even
// when no row is stored and no dream has ever run.
func TestPeriodicEdgeListEmptyStoreHasDreamOnly(t *testing.T) {
	periodicEdgeEnv(t)
	rows, err := ListPeriodic(periodicEdgeDate(2026, time.November, 13, 8, 0))
	if err != nil {
		t.Fatalf("ListPeriodic: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want exactly the dream row", rows)
	}
	d := rows[0]
	if d.Name != "dream" || d.Prefix != "dream" || d.AtMinute != 0 || d.Days != 0 || !d.Enabled || !d.Builtin || d.Template != "" {
		t.Fatalf("dream row = %+v", d)
	}
	if d.LastDate != "" || d.LastSlug != "" {
		t.Fatalf("dream row with no dream run: LastDate %q LastSlug %q, want empty", d.LastDate, d.LastSlug)
	}
}

// Contract: the dream row's "`LastDate` and `LastSlug` taken from
// `ReadDreamRun()`'s `Date` and `Job`", including a night that created no job.
func TestPeriodicEdgeListDreamFollowsDreamRun(t *testing.T) {
	periodicEdgeEnv(t)
	if err := writeDreamRun(DreamRun{Date: "2026-11-12", Job: ""}); err != nil {
		t.Fatalf("writeDreamRun: %v", err)
	}
	rows, err := ListPeriodic(periodicEdgeDate(2026, time.November, 13, 8, 0))
	if err != nil || len(rows) == 0 {
		t.Fatalf("ListPeriodic = %+v, %v", rows, err)
	}
	if rows[0].LastDate != "2026-11-12" || rows[0].LastSlug != "" {
		t.Fatalf("dream row Last = %q %q, want 2026-11-12 and empty", rows[0].LastDate, rows[0].LastSlug)
	}
}

// Contract: "`NextDue` is `PeriodicNextDue` for that row in unix millis, 0 for
// the zero time" — a paused row's NextDue is 0; stored rows are "`Builtin`
// false" and follow the dream row in creation order.
func TestPeriodicEdgeListPausedRowNextDueZero(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2026, time.November, 13, 8, 0)
	for _, n := range []string{"zz", "aa"} {
		if _, err := AddPeriodic(n, "p"+n, "09:00", "", periodicEdgeTemplate, now); err != nil {
			t.Fatalf("add %s: %v", n, err)
		}
	}
	if err := st.SetPeriodicEnabled("zz", false, 0); err != nil {
		t.Fatalf("disable: %v", err)
	}
	rows, err := ListPeriodic(now)
	if err != nil {
		t.Fatalf("ListPeriodic: %v", err)
	}
	if len(rows) != 3 || rows[1].Name != "zz" || rows[2].Name != "aa" {
		t.Fatalf("rows = %+v, want dream, zz, aa", rows)
	}
	if rows[1].Builtin || rows[2].Builtin {
		t.Fatal("a stored row reads Builtin")
	}
	if rows[1].NextDue != 0 {
		t.Fatalf("paused row NextDue = %d, want 0", rows[1].NextDue)
	}
	want := periodicEdgeDate(2026, time.November, 13, 9, 0).UnixMilli()
	if rows[2].NextDue != want {
		t.Fatalf("enabled row NextDue = %d, want %d", rows[2].NextDue, want)
	}
}

// ---------------------------------------------------------------- periodicPass

type periodicEdgeStart struct {
	calls []*Job
	ok    func(j *Job) bool
}

func (s *periodicEdgeStart) fn(j *Job) bool {
	s.calls = append(s.calls, j)
	if s.ok == nil {
		return true
	}
	return s.ok(j)
}

// periodicEdgePassRow stores an enabled daily row armed long before now.
func periodicEdgePassRow(t *testing.T, st *store.Store, name, prefix, template string, atMinute, days int) store.PeriodicJob {
	t.Helper()
	return periodicEdgeStoreRow(t, st, store.PeriodicJob{
		Name: name, Prefix: prefix, Template: template, AtMinute: atMinute, Days: days,
		Enabled: true, ArmedAt: 1, CreatedAt: 1,
	})
}

// Contract: "With no store wired it does nothing and returns 0."
func TestPeriodicEdgePassNoStore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prev := periodicNotify
	periodicNotify = func(string, string) {}
	t.Cleanup(func() { periodicNotify = prev })
	UseStore(nil)
	s := &periodicEdgeStart{}
	if n := periodicPass(periodicEdgeDate(2031, time.March, 3, 12, 0), s.fn); n != 0 || len(s.calls) != 0 {
		t.Fatalf("periodicPass with no store = %d (%d start calls), want 0 and none", n, len(s.calls))
	}
}

// Contract: "A row that is not due is neither read further nor written": a
// disabled row, a row whose day is not today, a row whose occurrence is still
// ahead and a row armed after today's occurrence all keep their Last* empty,
// and no job is created.
func TestPeriodicEdgePassNotDueUntouched(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2032, time.April, 7, 12, 0) // a Wednesday
	periodicEdgeStoreRow(t, st, store.PeriodicJob{Name: "off", Prefix: "off", Template: periodicEdgeTemplate, AtMinute: 0, Enabled: false, ArmedAt: 1, CreatedAt: 1})
	periodicEdgePassRow(t, st, "thursday", "thursday", periodicEdgeTemplate, 0, 1<<4)
	periodicEdgePassRow(t, st, "later", "later", periodicEdgeTemplate, 13*60, 0)
	periodicEdgeStoreRow(t, st, store.PeriodicJob{Name: "armed", Prefix: "armed", Template: periodicEdgeTemplate, AtMinute: 600, Enabled: true,
		ArmedAt: periodicEdgeDate(2032, time.April, 7, 10, 0).UnixMilli() + 1, CreatedAt: 1})
	s := &periodicEdgeStart{}
	if n := periodicPass(now, s.fn); n != 0 || len(s.calls) != 0 {
		t.Fatalf("pass = %d with %d start calls, want 0 and none", n, len(s.calls))
	}
	for _, name := range []string{"off", "thursday", "later", "armed"} {
		r := periodicEdgeGetRow(t, st, name)
		if r.LastDate != "" || r.LastSlug != "" || r.LastNote != "" || r.LastAt != 0 {
			t.Errorf("not-due row %q was written: %+v", name, r)
		}
	}
	if n := len(LoadJobs()); n != 0 {
		t.Fatalf("jobs = %d, want 0", n)
	}
}

// Contract (a): "An active job already holds that slug: no job is created and
// `start` is not called." Stamped "`LastDate` today and `LastAt` `now`;
// `LastSlug` is ... empty in (a)"; "`LastNote` is empty only when (c)'s `start`
// returned true, and says what happened otherwise."
func TestPeriodicEdgePassSlugHeldByActiveJob(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2033, time.May, 9, 12, 0)
	periodicEdgePassRow(t, st, "held", "held", periodicEdgeTemplate, 600, 0)
	periodicEdgeMakeJob(t, st, "held-2033-05-09")
	s := &periodicEdgeStart{}
	if n := periodicPass(now, s.fn); n != 0 || len(s.calls) != 0 {
		t.Fatalf("pass = %d with %d start calls, want 0 and none", n, len(s.calls))
	}
	if n := len(LoadJobs()); n != 1 {
		t.Fatalf("jobs = %d, want only the existing holder", n)
	}
	r := periodicEdgeGetRow(t, st, "held")
	if r.LastDate != "2033-05-09" || r.LastSlug != "" || r.LastNote == "" || r.LastAt != now.UnixMilli() {
		t.Fatalf("stamp = %+v, want LastDate today, LastSlug empty, a note, LastAt now", r)
	}
}

// Contract: (a) is about an ACTIVE job ("a job row that is not archived"); an
// archived holder of the slug does not stop (c).
func TestPeriodicEdgePassArchivedHolderDoesNotBlock(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2034, time.June, 14, 12, 0)
	periodicEdgePassRow(t, st, "arch", "arch", periodicEdgeTemplate, 600, 0)
	old := periodicEdgeMakeJob(t, st, "arch-2034-06-14")
	if _, err := st.ArchiveJob(old.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	s := &periodicEdgeStart{}
	if n := periodicPass(now, s.fn); n != 1 || len(s.calls) != 1 {
		t.Fatalf("pass = %d with %d start calls, want 1 and 1", n, len(s.calls))
	}
	if s.calls[0].Slug != "arch-2034-06-14" || periodicEdgeActiveJob("arch-2034-06-14") == nil {
		t.Fatalf("start got %q; active job missing", s.calls[0].Slug)
	}
	r := periodicEdgeGetRow(t, st, "arch")
	if r.LastSlug != "arch-2034-06-14" || r.LastNote != "" {
		t.Fatalf("stamp = %+v, want LastSlug set and no note", r)
	}
}

// Contract: "`LastSlug` is the slug in (c) ...; `LastNote` is empty only when
// (c)'s `start` returned true, and says what happened otherwise." "The return
// value is the number of `start` calls that returned true."
func TestPeriodicEdgePassStartRefused(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2035, time.July, 2, 12, 0)
	periodicEdgePassRow(t, st, "nope", "nope", periodicEdgeTemplate, 600, 0)
	s := &periodicEdgeStart{ok: func(*Job) bool { return false }}
	if n := periodicPass(now, s.fn); n != 0 || len(s.calls) != 1 {
		t.Fatalf("pass = %d with %d start calls, want 0 and exactly 1", n, len(s.calls))
	}
	if periodicEdgeActiveJob("nope-2035-07-02") == nil {
		t.Fatal("created job missing after a refused start")
	}
	r := periodicEdgeGetRow(t, st, "nope")
	if r.LastDate != "2035-07-02" || r.LastSlug != "nope-2035-07-02" || r.LastNote == "" || r.LastAt != now.UnixMilli() {
		t.Fatalf("stamp = %+v, want LastSlug the slug and a non-empty note", r)
	}
}

// Contract: "The return value is the number of `start` calls that returned
// true" — three due rows, two starts succeed.
func TestPeriodicEdgePassCountsOnlyTrueStarts(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2036, time.August, 20, 12, 0)
	for _, n := range []string{"c1", "c2", "c3"} {
		periodicEdgePassRow(t, st, n, n, periodicEdgeTemplate, 600, 0)
	}
	s := &periodicEdgeStart{ok: func(j *Job) bool { return j.Slug != "c2-2036-08-20" }}
	if n := periodicPass(now, s.fn); n != 2 {
		t.Fatalf("pass = %d, want 2", n)
	}
	if len(s.calls) != 3 {
		t.Fatalf("start calls = %d, want 3", len(s.calls))
	}
}

// Contract: "A `job:` line in the template does not name the job; the slug
// above always does."
func TestPeriodicEdgePassIgnoresTemplateJobLine(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2037, time.September, 11, 12, 0)
	periodicEdgePassRow(t, st, "named", "named", "---\njob: something-else\nrepo: kraken\n---\nbody\n", 600, 0)
	s := &periodicEdgeStart{}
	if n := periodicPass(now, s.fn); n != 1 || len(s.calls) != 1 {
		t.Fatalf("pass = %d with %d start calls, want 1 and 1", n, len(s.calls))
	}
	if s.calls[0].Slug != "named-2037-09-11" {
		t.Fatalf("started slug = %q, want named-2037-09-11", s.calls[0].Slug)
	}
	if periodicEdgeActiveJob("something-else") != nil {
		t.Fatal("a job was created under the template's job: name")
	}
}

// Contract: the slug is `<prefix>-<today>` with today in now's location, and
// once fired, `LastDate` today makes the row not due ("A `LastDate` equal to
// today ... is false"), so a second pass the same local day starts nothing.
func TestPeriodicEdgePassLocalDateAndOncePerDay(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	now := periodicEdgeDate(2038, time.October, 15, 22, 30) // 2038-10-16 in UTC
	periodicEdgePassRow(t, st, "late", "late", periodicEdgeTemplate, 22*60, 0)
	s := &periodicEdgeStart{}
	if n := periodicPass(now, s.fn); n != 1 || len(s.calls) != 1 {
		t.Fatalf("first pass = %d with %d start calls", n, len(s.calls))
	}
	if s.calls[0].Slug != "late-2038-10-15" {
		t.Fatalf("slug = %q, want late-2038-10-15 (the local date)", s.calls[0].Slug)
	}
	if n := periodicPass(now.Add(time.Minute), s.fn); n != 0 || len(s.calls) != 1 {
		t.Fatalf("second pass = %d, start calls now %d; want 0 and still 1", n, len(s.calls))
	}
}

// ---------------------------------------------------------------- RenameJob

// Contract: RenameJob "Additionally refuses a new name that starts with
// `<prefix>-` for the `Prefix` of any stored periodic row; the error text
// contains that prefix." That includes renaming one fired job to another name
// under the same prefix.
func TestPeriodicEdgeRenameIntoPrefixRefused(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	periodicEdgePassRow(t, st, "nightly report", "nightly", periodicEdgeTemplate, 600, 0)
	periodicEdgeMakeJob(t, st, "plain")
	periodicEdgeMakeJob(t, st, "nightly-2026-11-13")
	for _, c := range []struct{ from, to string }{
		{"plain", "nightly-foo"},
		{"plain", "nightly-2026-11-14"},
		{"nightly-2026-11-13", "nightly-renamed"},
	} {
		j := LoadJobBySlug(c.from)
		if j == nil {
			t.Fatalf("job %q missing", c.from)
		}
		res, err := RenameJob(j, c.to)
		if err == nil {
			t.Errorf("RenameJob(%q -> %q) = %+v, want a refusal", c.from, c.to, res)
			continue
		}
		if !strings.Contains(err.Error(), "nightly") {
			t.Errorf("RenameJob(%q -> %q) refusal %q does not name the prefix", c.from, c.to, err)
		}
		if LoadJobBySlug(c.from) == nil {
			t.Errorf("refused rename moved %q", c.from)
		}
	}
}

// Contract: the refusal is for names that start with `<prefix>-`; "Renaming a
// fired job away from its prefix is allowed." The bare prefix and a longer
// word that only begins with its letters do not start with `<prefix>-`.
func TestPeriodicEdgeRenameOutsidePrefixAllowed(t *testing.T) {
	st, _ := periodicEdgeEnv(t)
	periodicEdgePassRow(t, st, "nightly report", "nightly", periodicEdgeTemplate, 600, 0)
	periodicEdgeMakeJob(t, st, "nightly-2026-11-13")
	periodicEdgeMakeJob(t, st, "plain-one")
	periodicEdgeMakeJob(t, st, "plain-two")
	for _, c := range []struct{ from, to string }{
		{"nightly-2026-11-13", "kept-report"},
		{"plain-one", "nightly"},
		{"plain-two", "nightlyx-1"},
	} {
		j := LoadJobBySlug(c.from)
		if j == nil {
			t.Fatalf("job %q missing", c.from)
		}
		if _, err := RenameJob(j, c.to); err != nil {
			t.Errorf("RenameJob(%q -> %q): %v, want allowed", c.from, c.to, err)
			continue
		}
		if periodicEdgeActiveJob(c.to) == nil {
			t.Errorf("job not found under %q after rename", c.to)
		}
	}
}
