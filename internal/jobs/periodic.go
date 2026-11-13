package jobs

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"kraken/internal/store"
	"kraken/internal/util"
)

// periodic.go — periodic jobs: a stored job template (store.PeriodicJob) that
// the daemon tick turns into one job per cadence day, at or after a local
// time of day. It is kickDream's cadence generalized: the gate is the same
// LOCAL DATE comparison ("the last firing's date sorts before today"), so a
// Mac asleep through the occurrence fires on the first tick after it wakes
// and a day is never fired twice. What dream.go owns that this file does not
// is the evidence gate and the sweep window; a periodic row always fires on a
// due day, and its job carries whatever its template says.
//
// The built-in dream pass is listed beside the stored rows (ListPeriodic) so
// the operator sees every scheduled job in one place, but it is not a row:
// it keeps its own watermark and its own trigger, and every write aimed at
// the name `dream` is refused with ErrPeriodicBuiltin.

// PeriodicDateLayout is the local calendar date a periodic row compares on,
// and the date part of every slug it fires.
const PeriodicDateLayout = "2006-01-02"

// PeriodicBuiltinDream is the name the built-in dream pass is listed under.
const PeriodicBuiltinDream = "dream"

// ErrPeriodicInvalid wraps every validation refusal (a bad cadence, name,
// prefix or template), so the API layer can answer 400 without matching text.
var ErrPeriodicInvalid = errors.New("invalid periodic job")

// ErrPeriodicBuiltin refuses a write aimed at the built-in dream row, which
// lives in dream.go and has no stored row to pause, run or remove.
var ErrPeriodicBuiltin = errors.New("the dream pass is built in and cannot be changed here")

// maxPeriodicNameRunes and maxPeriodicPrefixRunes bound the two names. The
// prefix bound leaves room for "-YYYY-MM-DD-run-NN" under RenameJob's 100-rune
// slug ceiling (maxRenameSlugRunes), since every runtime name is built from a
// slug and a fired slug is the prefix plus that suffix.
const (
	maxPeriodicNameRunes   = 64
	maxPeriodicPrefixRunes = 60
)

// Cadence is when a row fires: a time of day and a weekday mask.
type Cadence struct {
	AtMinute int
	Days     int
}

// PeriodicRun is what an on-demand firing produced.
type PeriodicRun struct {
	Slug    string
	Started bool
	Note    string
}

// PeriodicRow is one entry of ListPeriodic: a stored row, or the built-in
// dream pass dressed as one.
type PeriodicRow struct {
	store.PeriodicJob
	Builtin bool
	NextDue int64 // unix millis, 0 when there is none
}

// periodicSubmit is the start RunPeriodicNow goes through; tests replace it.
var periodicSubmit = SubmitJob

// periodicNotify is every notification this file sends; tests replace it.
var periodicNotify = notify

// dayNames is indexed by time.Weekday, which is also the bit index in Days.
var dayNames = [7]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

// weekdaysMask is Monday through Friday.
const weekdaysMask = 1<<time.Monday | 1<<time.Tuesday | 1<<time.Wednesday | 1<<time.Thursday | 1<<time.Friday

func invalidf(format string, a ...any) error {
	return fmt.Errorf("%s: %w", fmt.Sprintf(format, a...), ErrPeriodicInvalid)
}

// ParseCadence reads a time of day (`H:MM` or `HH:MM`, 24-hour) and a day
// list (`daily`, `weekdays`, or comma-separated three-letter names).
func ParseCadence(at, days string) (Cadence, error) {
	var c Cadence
	h, m, ok := strings.Cut(at, ":")
	if !ok || len(h) < 1 || len(h) > 2 || len(m) != 2 || !allDigits(h) || !allDigits(m) {
		return c, invalidf("time %q is not H:MM or HH:MM", at)
	}
	hour, _ := strconv.Atoi(h)
	minute, _ := strconv.Atoi(m)
	if hour > 23 || minute > 59 {
		return c, invalidf("time %q is out of range (00:00 to 23:59)", at)
	}
	c.AtMinute = hour*60 + minute

	d := strings.ToLower(strings.TrimSpace(days))
	switch d {
	case "", "daily":
		return c, nil
	case "weekdays":
		c.Days = weekdaysMask
		return c, nil
	}
	for _, part := range strings.Split(d, ",") {
		part = strings.TrimSpace(part)
		bit := -1
		for i, n := range dayNames {
			if part == n {
				bit = i
				break
			}
		}
		if bit < 0 {
			return Cadence{}, invalidf("day %q is not one of sun mon tue wed thu fri sat (or daily, weekdays)", part)
		}
		c.Days |= 1 << bit
	}
	return c, nil
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// At is the time of day as zero-padded HH:MM.
func (c Cadence) At() string {
	return fmt.Sprintf("%02d:%02d", c.AtMinute/60, c.AtMinute%60)
}

// DayNames lists the set days Monday first and Sunday last, empty for every day.
func (c Cadence) DayNames() []string {
	out := []string{}
	if c.Days == 0 {
		return out
	}
	for _, wd := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday} {
		if c.Days&(1<<wd) != 0 {
			out = append(out, dayNames[wd])
		}
	}
	return out
}

// String is `daily HH:MM` or `mon,wed,fri HH:MM`.
func (c Cadence) String() string {
	if c.Days == 0 {
		return "daily " + c.At()
	}
	return strings.Join(c.DayNames(), ",") + " " + c.At()
}

// periodicOccurrence is the row's firing time on the date of day, in day's
// location. time.Date normalizes, so a minute that a DST jump skips lands on
// the next real instant instead of failing.
func periodicOccurrence(atMinute int, day time.Time) time.Time {
	return time.Date(day.Year(), day.Month(), day.Day(), atMinute/60, atMinute%60, 0, 0, day.Location())
}

func periodicDayOK(days int, day time.Time) bool {
	return days == 0 || days&(1<<day.Weekday()) != 0
}

// PeriodicDue reports whether p should fire on this tick. The ArmedAt rule
// is what keeps a row from firing the moment it is created or resumed after
// its occurrence has already passed today: the first firing is the first
// occurrence at or after the arming.
func PeriodicDue(p store.PeriodicJob, now time.Time) bool {
	if !p.Enabled || !periodicDayOK(p.Days, now) {
		return false
	}
	occ := periodicOccurrence(p.AtMinute, now)
	if now.Before(occ) || occ.UnixMilli() < p.ArmedAt {
		return false
	}
	return p.LastDate < now.Format(PeriodicDateLayout)
}

// PeriodicNextDue is when p will next fire: today's occurrence when it is due
// now, else the earliest later occurrence on a cadence day that is not before
// the arming. A LastDate later than today (a clock that jumped backwards)
// also pushes the answer past that date, since PeriodicDue refuses every day
// up to it.
func PeriodicNextDue(p store.PeriodicJob, now time.Time) time.Time {
	if !p.Enabled {
		return time.Time{}
	}
	if PeriodicDue(p, now) {
		return periodicOccurrence(p.AtMinute, now)
	}
	today := now.Format(PeriodicDateLayout)
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	if p.ArmedAt > 0 {
		a := time.UnixMilli(p.ArmedAt).In(now.Location())
		if as := time.Date(a.Year(), a.Month(), a.Day(), 0, 0, 0, 0, now.Location()); as.After(start) {
			start = as
		}
	}
	if p.LastDate > today {
		if ld, err := time.ParseInLocation(PeriodicDateLayout, p.LastDate, now.Location()); err == nil {
			if next := ld.AddDate(0, 0, 1); next.After(start) {
				start = next
			}
		}
	}
	// Eight days from start always reaches a cadence day; one more covers a
	// start whose own occurrence is before now or before the arming.
	for i := 0; i < 9; i++ {
		day := time.Date(start.Year(), start.Month(), start.Day()+i, 0, 0, 0, 0, now.Location())
		if !periodicDayOK(p.Days, day) {
			continue
		}
		occ := periodicOccurrence(p.AtMinute, day)
		if !occ.After(now) || occ.UnixMilli() < p.ArmedAt {
			continue
		}
		if p.LastDate > today && day.Format(PeriodicDateLayout) <= p.LastDate {
			continue
		}
		return occ
	}
	return time.Time{}
}

// RenderPeriodicTemplate substitutes the two placeholders a template may use.
func RenderPeriodicTemplate(template, slug, date string) string {
	return strings.NewReplacer("{{slug}}", slug, "{{date}}", date).Replace(template)
}

// periodicTemplateJob renders template for slug and date and checks that the
// result is a job SubmitJob could start: it parses cleanly, has a spec,
// claims no state (a template saying `state: done` would create a job that
// is already finished), and names a repo that resolves. The same check gates
// AddPeriodic and every firing, so a template that stopped resolving (its
// repo was deleted) is caught before a job row is written.
func periodicTemplateJob(template, slug, date string) (*Job, error) {
	parsed := ParseJob(slug+JobSuffix, []byte(RenderPeriodicTemplate(template, slug, date)))
	if parsed.Err != "" {
		return nil, invalidf("template does not parse: %s", parsed.Err)
	}
	if strings.TrimSpace(parsed.Body) == "" {
		return nil, invalidf("template has an empty spec body")
	}
	if parsed.State != "" {
		return nil, invalidf("template sets state: %s; kraken sets a job's state itself", parsed.State)
	}
	if _, err := ResolveRepo(parsed.Repo); err != nil {
		return nil, invalidf("template repo: %v", err)
	}
	return parsed, nil
}

// createPeriodicJob writes the job row for slug from a validated parse and
// returns it loaded, the way CreateDreamJob does. The slug is always the
// caller's, never a `job:` line of the template's: the cadence's dedupe is
// keyed on it.
func createPeriodicJob(st *store.Store, parsed *Job, slug string) (*Job, error) {
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Solo: parsed.Solo, Model: parsed.Model, Agent: parsed.Agent,
		Color: parsed.Color, Visibility: parsed.Visibility,
		Body:   parsed.Body,
		Author: "kraken",
	}); err != nil {
		return nil, err
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return nil, fmt.Errorf("periodic job %s created but could not be reloaded: %w", slug, err)
	}
	return loadJobFull(st, sr)
}

func periodicStore() (*store.Store, error) {
	st := ActiveStore()
	if st == nil {
		return nil, fmt.Errorf("periodic jobs need kraken serve's store; none is wired")
	}
	return st, nil
}

func isBuiltinPeriodic(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), PeriodicBuiltinDream)
}

// AddPeriodic validates and stores a new row. It creates no job: the first
// firing is the first occurrence at or after now.
func AddPeriodic(name, prefix, at, days, template string, now time.Time) (*store.PeriodicJob, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return nil, invalidf("a periodic job needs a name")
	case strings.ContainsAny(name, "\r\n"):
		return nil, invalidf("the name must be one line")
	case utf8.RuneCountInString(name) > maxPeriodicNameRunes:
		return nil, invalidf("the name is longer than %d characters", maxPeriodicNameRunes)
	case isBuiltinPeriodic(name):
		return nil, invalidf("%q is the built-in dream pass's name", PeriodicBuiltinDream)
	}
	switch {
	case prefix == "":
		return nil, invalidf("a periodic job needs a prefix")
	case prefix != util.Slugify(prefix):
		return nil, invalidf("prefix %q is not a slug (try %q)", prefix, util.Slugify(prefix))
	case utf8.RuneCountInString(prefix) > maxPeriodicPrefixRunes:
		return nil, invalidf("prefix %q is longer than %d characters", prefix, maxPeriodicPrefixRunes)
	}
	for _, p := range reservedRenamePrefixes {
		if strings.HasPrefix(prefix+"-", p) {
			return nil, invalidf("prefix %q collides with %q, which kraken mints itself", prefix, p)
		}
	}
	c, err := ParseCadence(at, days)
	if err != nil {
		return nil, err
	}
	today := now.Format(PeriodicDateLayout)
	if _, err := periodicTemplateJob(template, prefix+"-"+today, today); err != nil {
		return nil, err
	}
	st, err := periodicStore()
	if err != nil {
		return nil, err
	}
	p := &store.PeriodicJob{
		Name: name, Prefix: prefix, Template: template,
		AtMinute: c.AtMinute, Days: c.Days, Enabled: true,
		ArmedAt: now.UnixMilli(), CreatedAt: now.UnixMilli(),
	}
	if _, err := st.CreatePeriodicJob(p); err != nil {
		return nil, err
	}
	dlogEvent("info", "periodic-add", "periodic %s: added (prefix %s, %s)", name, prefix, c)
	return st.GetPeriodicJob(name)
}

// SetPeriodicPaused pauses a row, or resumes it armed at now: a resumed row
// does not fire for an occurrence that passed while it was paused.
func SetPeriodicPaused(name string, paused bool, now time.Time) (*store.PeriodicJob, error) {
	if isBuiltinPeriodic(name) {
		return nil, ErrPeriodicBuiltin
	}
	st, err := periodicStore()
	if err != nil {
		return nil, err
	}
	if err := st.SetPeriodicEnabled(name, !paused, now.UnixMilli()); err != nil {
		return nil, err
	}
	return st.GetPeriodicJob(name)
}

// RemovePeriodic deletes a row. The jobs it fired stay as they are.
func RemovePeriodic(name string) error {
	if isBuiltinPeriodic(name) {
		return ErrPeriodicBuiltin
	}
	st, err := periodicStore()
	if err != nil {
		return err
	}
	return st.DeletePeriodicJob(name)
}

// maxPeriodicRunSuffix bounds RunPeriodicNow's slug search; reaching it means
// something is wrong, not that a day has had that many manual runs.
const maxPeriodicRunSuffix = 1000

// RunPeriodicNow fires a row once, on demand, under a `-run` slug so it never
// takes the slug a scheduled firing will want later the same day. It leaves
// the row's watermark alone for the same reason: a manual run is not today's
// scheduled firing.
func RunPeriodicNow(name string, now time.Time) (PeriodicRun, error) {
	if isBuiltinPeriodic(name) {
		return PeriodicRun{}, ErrPeriodicBuiltin
	}
	st, err := periodicStore()
	if err != nil {
		return PeriodicRun{}, err
	}
	p, err := st.GetPeriodicJob(name)
	if err != nil {
		return PeriodicRun{}, err
	}
	today := now.Format(PeriodicDateLayout)
	var j *Job
	for n := 1; n <= maxPeriodicRunSuffix && j == nil; n++ {
		slug := p.Prefix + "-" + today + "-run"
		if n > 1 {
			slug += "-" + strconv.Itoa(n)
		}
		if existing, err := st.GetJobBySlug(slug); err == nil && existing != nil {
			continue
		}
		parsed, err := periodicTemplateJob(p.Template, slug, today)
		if err != nil {
			return PeriodicRun{}, err
		}
		j, err = createPeriodicJob(st, parsed, slug)
		if err != nil && periodicSlugTaken(st, slug, err) {
			j = nil
			continue
		}
		if err != nil {
			return PeriodicRun{}, fmt.Errorf("create %s: %w", slug, err)
		}
	}
	if j == nil {
		return PeriodicRun{}, fmt.Errorf("no free run slug for %s today", p.Prefix)
	}
	dlogEvent("info", "periodic-run", "periodic %s: run now created %s", name, j.Slug)
	if err := periodicSubmit(j); err != nil {
		dlogEvent("error", "periodic-run-failed", "periodic %s: %s created but could not start: %v", name, j.Slug, err)
		return PeriodicRun{Slug: j.Slug, Note: err.Error()}, nil
	}
	return PeriodicRun{Slug: j.Slug, Started: true}, nil
}

// periodicSlugTaken reports whether a failed create of slug failed because
// the name is taken, in which case RunPeriodicNow moves to the next suffix.
// Two ways: the slug is a former name of a renamed job (ErrSlugTaken: not an
// active holder, but still not creatable), or another request created an
// active job under it between the lookup and the insert, which the re-check
// sees directly rather than by reading the insert's error text.
func periodicSlugTaken(st *store.Store, slug string, err error) bool {
	if errors.Is(err, store.ErrSlugTaken) {
		return true
	}
	existing, gerr := st.GetJobBySlug(slug)
	return gerr == nil && existing != nil
}

// ListPeriodic is the built-in dream row followed by every stored row. With
// no store wired it degrades to the dream row alone, the way ReadDreamRun
// degrades to a blank watermark.
func ListPeriodic(now time.Time) ([]PeriodicRow, error) {
	dr := ReadDreamRun()
	dream := PeriodicRow{
		PeriodicJob: store.PeriodicJob{
			Name: PeriodicBuiltinDream, Prefix: PeriodicBuiltinDream, Enabled: true,
			LastDate: dr.Date, LastSlug: dr.Job,
		},
		Builtin: true,
	}
	out := []PeriodicRow{dream}
	if st := ActiveStore(); st != nil {
		rows, err := st.ListPeriodicJobs()
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, PeriodicRow{PeriodicJob: r})
		}
	}
	for i := range out {
		if t := PeriodicNextDue(out[i].PeriodicJob, now); !t.IsZero() {
			out[i].NextDue = t.UnixMilli()
		}
	}
	return out, nil
}

// periodicFired is the in-memory backstop for the stored watermark, the
// reason lastDreamDate exists: a stamp that fails to write leaves LastDate
// behind today, and PeriodicDue would then fire the row again on every tick.
// It records a row only when its stamp failed, keyed on the store as well as
// the id since ids restart in every fresh database.
var (
	periodicFiredMu sync.Mutex
	periodicFired   = map[periodicFiredKey]string{}
)

type periodicFiredKey struct {
	st *store.Store
	id int64
}

// periodicPass fires every due row once for today, starting each created job
// through start (the tick's startCreatedJob). Returns how many cells started.
func periodicPass(now time.Time, start func(*Job) bool) int {
	st := ActiveStore()
	if st == nil {
		return 0
	}
	rows, err := st.ListPeriodicJobs()
	if err != nil {
		dlogErr("periodic: list failed: %v", err)
		return 0
	}
	today := now.Format(PeriodicDateLayout)
	started := 0
	for _, p := range rows {
		key := periodicFiredKey{st, p.ID}
		periodicFiredMu.Lock()
		fired := periodicFired[key] == today
		periodicFiredMu.Unlock()
		if fired || !PeriodicDue(p, now) {
			continue
		}
		// Re-read just before firing: the list may be a moment old, and a
		// row paused, removed or re-armed through the API since then must
		// not fire on the stale copy.
		fresh, err := st.GetPeriodicJob(p.Name)
		if err != nil || fresh.ID != p.ID || !PeriodicDue(*fresh, now) {
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				dlogErr("periodic %s: re-read before firing failed: %v", p.Name, err)
			}
			continue
		}
		p = *fresh
		slug, note, ok := firePeriodic(st, p, now, start)
		if ok {
			started++
		}
		err = st.StampPeriodicFiring(p.ID, today, slug, note, now.UnixMilli())
		if errors.Is(err, store.ErrNotFound) {
			// removed while it fired: there is no watermark left to protect,
			// and the in-memory record would only pin a dead id.
			dlogEvent("info", "periodic-removed", "periodic %s: removed while firing; nothing to stamp", p.Name)
			continue
		}
		if err != nil {
			dlogErr("periodic %s: watermark write failed: %v", p.Name, err)
			periodicFiredMu.Lock()
			periodicFired[key] = today
			periodicFiredMu.Unlock()
		} else {
			periodicFiredMu.Lock()
			delete(periodicFired, key)
			periodicFiredMu.Unlock()
		}
	}
	return started
}

// firePeriodic is one scheduled firing. slug is the job created ("" when
// none was), note why nothing was created or started ("" when it started),
// and ok whether start brought the cell up. Every outcome but a clean start
// notifies, since a periodic job has nobody watching it fire; a refused
// start is announced by start itself (startCreatedJob notifies), so it only
// gets a note here.
func firePeriodic(st *store.Store, p store.PeriodicJob, now time.Time, start func(*Job) bool) (slug, note string, ok bool) {
	today := now.Format(PeriodicDateLayout)
	want := p.Prefix + "-" + today
	if existing, err := st.GetJobBySlug(want); err == nil && existing != nil {
		note = "skipped: " + want + " already exists"
		dlogEvent("info", "periodic-skip", "periodic %s: %s", p.Name, note)
		periodicNotify("kraken ⏭ "+p.Name, note)
		return "", note, false
	}
	parsed, err := periodicTemplateJob(p.Template, want, today)
	if err == nil {
		var j *Job
		j, err = createPeriodicJob(st, parsed, want)
		if err == nil {
			dlogEvent("info", "periodic-fire", "periodic %s: created %s", p.Name, want)
			if start(j) {
				return want, "", true
			}
			return want, "created but could not start; it is a draft on the board", false
		}
	}
	note = "no job created: " + err.Error()
	dlogEvent("error", "periodic-fail", "periodic %s: %s", p.Name, note)
	periodicNotify("kraken ⚠ "+p.Name, note)
	return "", note, false
}
