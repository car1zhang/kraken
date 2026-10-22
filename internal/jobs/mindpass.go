package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"kraken/internal/store"
)

// mindpass.go — the nightly mind pass' TRIGGER: dream.go's cadence copied
// with `mind` in place of `dream`. It decides a night is due, that the window
// held any evidence, and creates the `mind-<date>` job whose spec sends zeus
// into `.claude/skills/mind/SKILL.md`. The delta from dream:
//
//   - the evidence gate also opens on the operator's own documents: it reads
//     CollectMMEvidence (operator-authored job specs, feedback notes and
//     answers — the same filter the skill reads) beside
//     anyOperatorTranscriptSince, never a raw count of jobs touched, which the
//     tick's own dream/mind rows would hold open every night;
//   - the job it creates is the one whose stamped lead session is the ONLY
//     caller ProposeMMItems accepts (mindLeadSession reads mind_last_run's
//     job_id to find it), so this watermark doubles as the propose gate's pointer.
//
// Window constants (dreamFirstWindow, dreamMaxWindow), the date layout,
// the transcript walk (anyTranscriptSinceWhere) and clampNote are dream's,
// shared rather than copied.

// mindRunKVKey is the kv row holding the pass' watermark.
const mindRunKVKey = "mind_last_run"

// MindRun is the mind pass' watermark, DreamRun's shape.
type MindRun struct {
	Date    string `json:"date"`     // local calendar date, "2006-01-02"
	SweptTo int64  `json:"swept_to"` // unix millis — the floor the NEXT sweep uses
	Job     string `json:"job"`      // slug created, "" if the night was skipped
	// JobID is the created row's immutable id — the propose gate's key
	// (mindLeadSession), since a slug is freed by archiving. 0 on a watermark
	// written before the field existed.
	JobID int64 `json:"job_id"`
}

// ReadMindRun loads the watermark with every failure degraded to the zero
// value — ReadDreamRun's display form.
func ReadMindRun() MindRun {
	r, _ := readMindRun()
	return r
}

// readMindRun is readDreamRun's semantics exactly: absent key → zero value,
// ok; unreadable → ok false (degrades to "already ran today"); unparseable →
// reset loudly and ok true.
func readMindRun() (MindRun, bool) {
	st := ActiveStore()
	if st == nil {
		return MindRun{}, false
	}
	return readMindRunFrom(st)
}

func readMindRunFrom(st *store.Store) (MindRun, bool) {
	raw, err := st.GetKV(mindRunKVKey)
	if errors.Is(err, store.ErrNotFound) {
		return MindRun{}, true
	}
	if err != nil {
		return MindRun{}, false
	}
	var r MindRun
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		dlogErr("mind watermark unparseable (%q) — resetting it; this night's window falls back to %s", raw, dreamFirstWindow)
		return MindRun{}, true
	}
	return r, true
}

// writeMindRun persists the watermark; no store wired is a no-op.
func writeMindRun(r MindRun) error {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return st.SetKV(mindRunKVKey, string(data))
}

// mindRunning single-flights the pass; lastMindDate is the in-memory date
// backstop; mindPass is the test seam — each exactly as dream's.
var (
	mindRunning  atomic.Bool
	lastMindDate atomic.Value // string
	mindPass     = runMindPass
)

func loadLastMindDate() string {
	s, _ := lastMindDate.Load().(string)
	return s
}

// kickMind runs at most one mind pass per LOCAL CALENDAR DAY — kickDream's
// cadence verbatim (see its comment for why the gate is a date comparison and
// why the in-memory date is checked first).
func kickMind(now time.Time) *Job {
	today := now.Format(dreamDateLayout)
	if loadLastMindDate() == today {
		return nil
	}
	if _, err := ResolveRepo("kraken"); err != nil {
		lastMindDate.Store(today)
		dlogEvent("info", "mind-skipped", "mind: skipped for %s — no kraken checkout: %v", today, err)
		return nil
	}
	run, ok := readMindRun()
	if !ok {
		return nil
	}
	if run.Date >= today {
		return nil
	}
	pass := mindPass
	if !mindRunning.CompareAndSwap(false, true) {
		return nil
	}
	defer mindRunning.Store(false)
	created, next, ok := pass(now, run)
	if !ok {
		return nil
	}
	lastMindDate.Store(today)
	if err := writeMindRun(next); err != nil {
		dlogErr("mind watermark write failed: %v", err)
	}
	return created
}

// runMindPass is runDreamPass with the operator-evidence gate (CollectMMEvidence OR a transcript touched since the floor, outside kraken's own nightly cells). A gate
// where every query answered and found nothing advances SweptTo with no job;
// one where a query FAILED and nothing was found returns ok false, so the
// window is not marked swept unread.
func runMindPass(now time.Time, prev MindRun) (*Job, MindRun, bool) {
	st := ActiveStore()
	if st == nil {
		return nil, MindRun{}, false
	}
	today := now.Format(dreamDateLayout)
	nowMS := now.UnixMilli()
	since := prev.SweptTo
	if since <= 0 {
		since = now.Add(-dreamFirstWindow).UnixMilli()
	}
	clamped := false
	if floor := now.Add(-dreamMaxWindow).UnixMilli(); since < floor {
		dlogErr("mind window clamped from %s to %s — evidence older than the cap is not swept",
			time.UnixMilli(since).Format(dreamDateLayout), time.UnixMilli(floor).Format(dreamDateLayout))
		since = floor
		clamped = true
	}

	// The gate reads the operator's evidence through the same filter the
	// skill reads it through (CollectMMEvidence), never a raw count of jobs
	// touched: that count includes the dream-<date> row this tick has just
	// created and last night's own mind-<date>, which would hold the gate
	// open every night with no operator activity at all.
	failed := false
	var feedback, jobsTouched, answers int
	ev, err := CollectMMEvidence(st, since, 0)
	if err != nil {
		dlogErr("mind evidence read failed: %v", err)
		failed = true
	} else {
		feedback, jobsTouched, answers = len(ev.Feedback), len(ev.Jobs), len(ev.Answers)
	}
	sessions, sok := anyOperatorTranscriptSince(since)
	if !sok {
		dlogErr("mind transcript scan failed — leaving the watermark where it is")
		failed = true
	}
	if feedback == 0 && jobsTouched == 0 && answers == 0 && !sessions {
		if failed {
			return nil, MindRun{}, false
		}
		dlogEvent("info", "mind-skip", "mind %s: nothing to read (0 feedback, 0 jobs, 0 answers, 0 sessions since %s)",
			today, time.UnixMilli(since).Format(time.RFC3339))
		return nil, MindRun{Date: today, SweptTo: nowMS}, true
	}

	slug := "mind-" + today
	// A taken slug skips the night without advancing the floor — dream's rule
	// and dream's documented archived-slug hole, unchanged. Job/JobID keep the
	// previous run's values, so a still-running mind zeus keeps its gate.
	if existing, err := st.GetJobBySlug(slug); err == nil && existing != nil {
		dlogEvent("info", "mind-skip", "mind %s: slug %s already taken — skipping tonight", today, slug)
		return nil, MindRun{Date: today, SweptTo: prev.SweptTo, Job: prev.Job, JobID: prev.JobID}, true
	}
	j, err := CreateMindJob(st, slug, since, nowMS, clamped)
	if err != nil {
		dlogErr("mind job create failed: %v", err)
		return nil, MindRun{}, false
	}
	dlogEvent("info", "mind-job-created", "mind %s: created %s (%d feedback rows, %d jobs, %d answers, sessions %v, window %s → %s)",
		today, slug, feedback, jobsTouched, answers, sessions,
		time.UnixMilli(since).Format(time.RFC3339), time.UnixMilli(nowMS).Format(time.RFC3339))
	return j, MindRun{Date: today, SweptTo: nowMS, Job: slug, JobID: j.ID}, true
}

// anyOperatorTranscriptSince is anyTranscriptSince's walk and (found, ok)
// semantics with kraken's own nightly cells left out: a project dir whose name
// contains -kraken-cells-dream- or -kraken-cells-mind- is never read, so last
// night's mind cell and tonight's dream cell cannot open the gate by
// themselves. The dream pass keeps the unfiltered anyTranscriptSince.
func anyOperatorTranscriptSince(sinceMS int64) (found, ok bool) {
	return anyTranscriptSinceWhere(sinceMS, func(projectDir string) bool {
		return strings.Contains(projectDir, "-kraken-cells-dream-") || strings.Contains(projectDir, "-kraken-cells-mind-")
	})
}

// CreateMindJob writes the night's job row and returns it loaded —
// CreateDreamJob's text → ParseJob → CreateJob shape, author "kraken".
func CreateMindJob(st *store.Store, slug string, since, until int64, clamped bool) (*Job, error) {
	content := mindJobContent(slug, since, until, clamped)
	parsed := ParseJob(slug+JobSuffix, []byte(content))
	if parsed.Err != "" {
		return nil, fmt.Errorf("mind job %s does not parse: %s", slug, parsed.Err)
	}
	if strings.TrimSpace(parsed.Body) == "" {
		return nil, fmt.Errorf("mind job %s has an empty body", slug)
	}
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Solo: parsed.Solo, Model: parsed.Model,
		Body:   parsed.Body,
		Author: "kraken",
	}); err != nil {
		return nil, err
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return nil, fmt.Errorf("mind job %s created but could not be reloaded: %w", slug, err)
	}
	return loadJobFull(st, sr)
}

// mindJobContent renders the night's job text. Like dreamJobContent it says
// only what the skill cannot know — the window's two edges and the two
// commands that read it — and names the skill BY PATH, since a personal
// ~/.claude/skills/mind would shadow the repo-owned copy.
func mindJobContent(slug string, since, until int64, clamped bool) string {
	sinceISO := time.UnixMilli(since).Format(time.RFC3339)
	untilISO := time.UnixMilli(until).Format(time.RFC3339)
	s, u := fmt.Sprint(since), fmt.Sprint(until)
	return "---\n" +
		"job: " + slug + "\n" +
		"repo: kraken\n" +
		"solo: false\n" +
		"---\n" +
		"\n" +
		"Run tonight's mind pass.\n" +
		"\n" +
		"Read `.claude/skills/mind/SKILL.md` **by path** from this worktree and follow it\n" +
		"end to end. Read it by path rather than invoking `/mind`: a personal skill of the\n" +
		"same name shadows the repo-owned copy, and the repo-owned copy is the one that\n" +
		"knows about cells.\n" +
		"\n" +
		"## The window\n" +
		"\n" +
		"Everything from **" + sinceISO + "** to **" + untilISO + "** —\n" +
		"unix millis `" + s + "` to `" + u + "`, both edges inclusive.\n" +
		"\n" +
		"- Operator prose: `kraken mine operator --since " + s + " --until " + u + "`\n" +
		"- Specs, feedback and answers: `kraken mm evidence --since " + s + " --until " + u + "`\n" +
		"\n" +
		"Both edges are kraken's own watermark (`mind_last_run.swept_to`). Do not widen\n" +
		"the window on your own initiative.\n" +
		clampNote(clamped)
}
