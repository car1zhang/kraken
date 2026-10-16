package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// dream.go — the nightly corroboration pass' TRIGGER. It owns none of the
// thinking: all it does is decide that a night is due, decide that the night
// had any evidence worth reading, and create the `dream-<date>` job row whose
// spec sends zeus into `.claude/skills/dream/SKILL.md`. The existing queue
// pass then starts its cell like any other job's.
//
// Two things here are firsts for this binary and both are deliberate:
//
//   - It is the first WALL-CLOCK cadence. Every other one is an elapsed
//     duration (kickUpdateAvail's millis delta, runNightlyBackups' 24h gate).
//   - It is the first PROGRAMMATIC job creator. Every other job is born from
//     the board's POST /api/job/create (internal/api/jobedit.go's
//     webJobCreate), which this package cannot call — api sits ABOVE jobs in
//     the util ← store ← core ← jobs ← api ← web chain.

// dreamRunKVKey is the kv row holding the watermark, following
// update_avail's precedent exactly (see ReadUpdateAvail): a small JSON
// singleton that doesn't warrant a table of its own, written by the daemon,
// which is alive exactly when it writes and already holds the store's single
// writer handle.
const dreamRunKVKey = "dream_last_run"

// dreamDateLayout is the LOCAL calendar date the cadence compares on, and
// the suffix of the job slug. Local rather than UTC because "one pass per
// night" means carl's night.
const dreamDateLayout = "2006-01-02"

// dreamFirstWindow is how far back the very first sweep ever looks, when
// there is no SweptTo floor to start from. One day, matching the loop's own
// "the past day's transcripts" contract — a first run must not try to read
// every transcript on the machine.
const dreamFirstWindow = 24 * time.Hour

// dreamMaxWindow caps how wide a single sweep can be. The floor never moves
// backwards, so a long gap (kraken serve off for a month) would otherwise
// hand one pass ~30x the corpus the transcript reduction was sized against.
// A clamp loses the oldest evidence, which is why runDreamPass reports it
// into the job body rather than clamping silently.
const dreamMaxWindow = 7 * 24 * time.Hour

// DreamRun is the nightly pass' watermark: what the last pass decided, and
// the floor the next sweep starts from.
type DreamRun struct {
	Date    string `json:"date"`     // local calendar date, "2006-01-02"
	SweptTo int64  `json:"swept_to"` // unix millis — the floor the NEXT sweep uses
	Job     string `json:"job"`      // slug created, "" if the night was skipped
}

// ReadDreamRun loads the watermark, degrading EVERY failure — no store
// wired, an unset key, a failed query, unparseable JSON — to the zero value,
// exactly as ReadUpdateAvail does for its own row. Callers that need to tell
// "never run" apart from "could not be read" use readDreamRun below; this
// exported form is for display, where a blank watermark is a correct-looking
// answer and an error would not be.
func ReadDreamRun() DreamRun {
	r, _ := readDreamRun()
	return r
}

// readDreamRun is ReadDreamRun with the distinction the cadence needs:
// ok=false means the watermark could not be READ at all (no store wired, a
// failed query, unparseable JSON), as opposed to a genuinely absent key,
// which is ok=true with a zero-value run — the honest "never ran" that must
// fire a first pass.
//
// The difference is load-bearing and it is the INVERSION of kickUpdateAvail's
// degradation. There, an unreadable row reads as CheckedAt 0, which is always
// due, and the cost of being wrong is one extra `git fetch`. Here "always
// due" would create a job row and spawn a cell every 15-second tick, so a
// store that cannot answer must degrade to "already ran today" instead. A
// missing NIGHT is recoverable; a cell storm is not.
func readDreamRun() (DreamRun, bool) {
	st := ActiveStore()
	if st == nil {
		return DreamRun{}, false
	}
	raw, err := st.GetKV(dreamRunKVKey)
	if errors.Is(err, store.ErrNotFound) {
		return DreamRun{}, true // never run — genuinely due
	}
	if err != nil {
		return DreamRun{}, false
	}
	var r DreamRun
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		// A present-but-unparseable row is garbage, and treating it as
		// unreadable would kill the loop PERMANENTLY and in total silence:
		// the only writer of this key sits downstream of the gate, so
		// nothing would ever repair it, and every tick would return here
		// having logged nothing. Every other branch in this file says
		// something. Reset instead — a self-repairing loop beats one that
		// waits for a human to notice it died — and say so loudly, because
		// the reset means the previous floor is gone and this night's
		// window falls back to dreamFirstWindow.
		dlogErr("dream watermark unparseable (%q) — resetting it; this night's window falls back to %s", raw, dreamFirstWindow)
		return DreamRun{}, true
	}
	return r, true
}

// writeDreamRun persists the watermark. No store wired is a no-op rather
// than an error, matching writeUpdateAvail — but note that unlike
// availability, losing this write is NOT free: the in-memory guard below is
// what keeps a store that never records from re-firing the pass forever.
func writeDreamRun(r DreamRun) error {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return st.SetKV(dreamRunKVKey, string(data))
}

// dreamRunning single-flights the pass across ticks, the same shape
// usagePassRunning and updateAvailRunning use: the pass reads the store,
// walks ~/.claude/projects, and inserts a job row, and two of those
// overlapping would race to create the same slug.
var dreamRunning atomic.Bool

// lastDreamDate is the in-memory backstop for the store watermark: the local
// date on which THIS process last completed a pass, regardless of whether the
// write of it ever landed. Necessary for the reason spelled out on
// readDreamRun — a watermark that never records reads as "never run", and
// "never run" is always due. Without this, a broken kv write would turn one
// pass per night into one job row and one cell per 15-second tick.
//
// Deliberately the SECOND gate, not the first (same ordering rationale as
// lastUpdateAvailCheck): it is lost on restart while the stored date survives
// one, so the store stays the source of truth and this only catches what the
// store cannot tell us. And it is stamped only on a SUCCESSFUL pass, so a
// failed create leaves both gates open and the next tick retries.
var lastDreamDate atomic.Value // string

func loadLastDreamDate() string {
	s, _ := lastDreamDate.Load().(string)
	return s
}

// dreamPass is the seam kickDream calls through, so tests can drive the
// cadence and the single-flight without a real sweep.
var dreamPass = runDreamPass

// kickDream runs at most one corroboration pass per LOCAL CALENDAR DAY,
// returning the job it created (nil when the night was skipped, not due, or
// failed) so the caller can queue it on this same tick.
//
// The cadence rule is "today's local date is later than the watermark's
// stored local date" — a DATE comparison, not an elapsed duration, and that
// is the whole non-obvious point of this function. An elapsed-duration gate
// ("fire when 24h have passed") drifts an hour later every night and, worse,
// silently skips a night when the Mac is asleep at the moment it comes due.
// Comparing stored date against today's date gives both behaviors that
// matter, with no catch-up logic at all: it fires shortly after midnight when
// the machine is awake, and it fires on the FIRST tick after the machine
// wakes when it was asleep through midnight. A night is never skipped and a
// night is never doubled.
func kickDream(now time.Time) *Job {
	today := now.Format(dreamDateLayout)
	// in-memory guard first — see lastDreamDate: this is the gate that holds
	// when the store cannot record.
	if loadLastDreamDate() == today {
		return nil
	}
	// The nightly pass improves kraken's own source, so a machine with no
	// kraken checkout under the repos dir has nothing for it to work on:
	// skip the night with one event instead of creating a job whose submit
	// can only fail. Only the in-memory date is stamped — the stored
	// watermark keeps its sweep floor, so the night's evidence is still in
	// the window once a checkout appears (and a restart re-checks, logging
	// at most once more).
	if _, err := ResolveRepo("kraken"); err != nil {
		lastDreamDate.Store(today)
		dlogEvent("info", "dream-skipped", "dream: skipped for %s — no kraken checkout: %v", today, err)
		return nil
	}
	run, ok := readDreamRun()
	if !ok {
		return nil // unreadable watermark degrades to "already ran today"
	}
	// >= rather than == so a clock that jumped backwards (or a hand-edited
	// row stamped into the future) does not fire a pass a day at a time on
	// its way back to the present.
	if run.Date >= today {
		return nil
	}
	// The seam is captured before the CAS for the same reason kickUpdateAvail
	// captures checkUpdateAvail: a test that swaps it back must not race a
	// call already inside it.
	pass := dreamPass
	if !dreamRunning.CompareAndSwap(false, true) {
		return nil
	}
	defer dreamRunning.Store(false)
	// run is handed to the pass rather than re-read inside it: readDreamRun's
	// answer is already known-good here, and a SECOND read that failed would
	// silently degrade prev.SweptTo to 0, collapsing the window to the last
	// 24h — and a pass that then succeeds writes SweptTo: now, marking a
	// deliberately-wider window swept without having read it. That wider
	// window is not hypothetical: the slug-collision and failed-create paths
	// exist precisely to leave one behind.
	created, next, ok := pass(now, run)
	if !ok {
		// A failed create leaves BOTH watermarks alone, so the next tick
		// retries rather than losing the night.
		return nil
	}
	lastDreamDate.Store(today)
	if err := writeDreamRun(next); err != nil {
		// The in-memory guard already covers this process; the worst a lost
		// write costs is one extra pass after a restart.
		dlogErr("dream watermark write failed: %v", err)
	}
	return created
}

// runDreamPass is the pass itself: decide whether the window held any
// evidence, and if so create the night's job. prev is the watermark kickDream
// already read and validated — never re-read here (see the call site).
// Returns the created job (nil on a skip), the watermark the caller should
// store, and ok=false when nothing should be advanced at all.
func runDreamPass(now time.Time, prev DreamRun) (*Job, DreamRun, bool) {
	st := ActiveStore()
	if st == nil {
		return nil, DreamRun{}, false
	}
	today := now.Format(dreamDateLayout)
	nowMS := now.UnixMilli()
	since := prev.SweptTo
	if since <= 0 {
		// <= rather than == 0: a corrupt or hand-edited negative floor would
		// otherwise render as a 1969 date in the job body and a negative
		// --since on the sweep command line.
		since = now.Add(-dreamFirstWindow).UnixMilli()
	}
	// Cap the window. Unbounded width is reachable in ordinary operation —
	// kraken serve off for a month yields a 30-day window, ~30x the corpus
	// the reduction budget was sized against — and the job body tells zeus
	// not to widen the window, which gives it no license to narrow one
	// either. Clamping here rather than there keeps the decision in code.
	// The clamp is REPORTED in the body (see dreamJobContent): a truncated
	// sweep that reads as a complete one is the failure this whole feature
	// is least able to afford.
	clamped := false
	if floor := now.Add(-dreamMaxWindow).UnixMilli(); since < floor {
		dlogErr("dream window clamped from %s to %s — evidence older than the cap is not swept",
			time.UnixMilli(since).Format(dreamDateLayout), time.UnixMilli(floor).Format(dreamDateLayout))
		since = floor
		clamped = true
	}

	// The gate: at least one feedback row OR at least one claude session in
	// the window. In practice that is every night — intended and
	// simplifying, not an accident. What it buys is that a machine left
	// untouched for a week does not manufacture seven empty jobs.
	//
	// BOTH halves must be able to say "I could not answer", distinctly from
	// "the answer is none". Advancing SweptTo past a window nobody actually
	// read does not cost a night — it costs that night's EVIDENCE, permanently,
	// since the floor never moves backwards and nothing records the loss.
	feedback, ferr := st.CountFeedbackSince(since)
	if ferr != nil {
		dlogErr("dream feedback count failed: %v", ferr)
		feedback = 0
	}
	sessions, sok := anyTranscriptSince(since)
	if !sok {
		dlogErr("dream transcript scan failed — leaving the watermark where it is")
	}
	if feedback == 0 && !sessions {
		if ferr != nil || !sok {
			// No positive evidence AND at least one of the two queries that
			// could have found some failed: that is not a quiet night, it is
			// an unanswered question. Leave the watermark where it is so
			// tomorrow's sweep still covers this window.
			return nil, DreamRun{}, false
		}
		// The common case, and it must be genuinely free: no job, no cell, no
		// notification. Advancing SweptTo loses nothing — there was no
		// evidence in the window to lose.
		dlogEvent("info", "dream-skip", "dream %s: nothing to corroborate (0 feedback, 0 sessions since %s)",
			today, time.UnixMilli(since).Format(time.RFC3339))
		return nil, DreamRun{Date: today, SweptTo: nowMS}, true
	}

	slug := "dream-" + today
	// The date makes collisions structurally impossible BETWEEN PASSES, since
	// the cadence fires once per local day. What can still collide is a job
	// of the same name that this pass did not create: a hand-made one, or a
	// reactivated older dream job. Log and skip — never retry, never mutate
	// the slug into dream-<date>-2, which would quietly produce two reports
	// for one night. SweptTo deliberately does NOT advance here: no sweep
	// happened, so tomorrow's window must still cover today's evidence.
	//
	// Note the ONE case this check does not catch: GetJobBySlug filters
	// archived_at IS NULL, so an ARCHIVED dream-<today> does not block a
	// second creation. Reaching it needs an archived same-day job AND a
	// watermark that has lost today's date, so it cannot happen on a healthy
	// machine — and the failure is benign (a duplicate night's report), which
	// is why this stays a documented hole rather than an extra query on the
	// hot path.
	if existing, err := st.GetJobBySlug(slug); err == nil && existing != nil {
		dlogEvent("info", "dream-skip", "dream %s: slug %s already taken — skipping tonight", today, slug)
		return nil, DreamRun{Date: today, SweptTo: prev.SweptTo, Job: ""}, true
	}
	j, err := CreateDreamJob(st, slug, since, nowMS, clamped)
	if err != nil {
		dlogErr("dream job create failed: %v", err)
		return nil, DreamRun{}, false
	}
	dlogEvent("info", "dream-job-created", "dream %s: created %s (%d feedback rows, sessions %v, window %s → %s)",
		today, slug, feedback, sessions,
		time.UnixMilli(since).Format(time.RFC3339), time.UnixMilli(nowMS).Format(time.RFC3339))
	return j, DreamRun{Date: today, SweptTo: nowMS, Job: slug}, true
}

// CreateDreamJob writes the night's job row and returns it loaded, for the
// caller to submit. It is this codebase's first programmatic job creator, and
// it mirrors webJobCreate's store-mode shape on purpose: build the job's TEXT,
// ParseJob it, and create the row from the parsed fields — so the row can
// never disagree with what the same text would have produced had a human
// pasted it into the board's editor. A parse error is a bug in
// dreamJobContent and is returned as one rather than silently creating a job
// the board would render with an Err badge.
//
// The board's own board cache (webBoardCache, private to internal/api) cannot
// be invalidated from here — api sits above jobs in the dependency chain — so
// the new job appears on the board on its next poll rather than instantly.
// That is a second or two, and it is the only cost of creating a job from
// below the API layer.
func CreateDreamJob(st *store.Store, slug string, since, until int64, clamped bool) (*Job, error) {
	content := dreamJobContent(slug, since, until, clamped)
	parsed := ParseJob(slug+JobSuffix, []byte(content))
	if parsed.Err != "" {
		return nil, fmt.Errorf("dream job %s does not parse: %s", slug, parsed.Err)
	}
	if strings.TrimSpace(parsed.Body) == "" {
		// SubmitJob refuses an empty spec too, so this is belt and braces on
		// top of the template below — but it fails the CREATE rather than the
		// start, which is the difference between "no job tonight, said loudly"
		// and "a permanently unstartable draft on the board".
		return nil, fmt.Errorf("dream job %s has an empty body", slug)
	}
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Solo: parsed.Solo, Model: parsed.Model,
		Body: parsed.Body,
	}); err != nil {
		return nil, err
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		return nil, fmt.Errorf("dream job %s created but could not be reloaded: %w", slug, err)
	}
	return loadJobFull(st, sr)
}

// dreamJobContent renders the night's job file text. solo: false, so zeus
// gets the scout crew the skill's semantic pass depends on. Nothing in the
// frontmatter asks for a start: the tick that created this job submits it
// itself on the same pass (daemon.go's startCreatedJob).
//
// The body says as little as possible: the procedure lives in the skill, and
// the only things the skill cannot know are the window's two edges. It is
// read BY PATH because a personal ~/.claude/skills/dream would otherwise
// shadow the repo-owned copy materialized into the cell's worktree.
func dreamJobContent(slug string, since, until int64, clamped bool) string {
	sinceISO := time.UnixMilli(since).Format(time.RFC3339)
	untilISO := time.UnixMilli(until).Format(time.RFC3339)
	return "---\n" +
		"job: " + slug + "\n" +
		"repo: kraken\n" +
		"solo: false\n" +
		"---\n" +
		"\n" +
		"Run tonight's corroboration pass.\n" +
		"\n" +
		"Read `.claude/skills/dream/SKILL.md` **by path** from this worktree and follow it\n" +
		"end to end. Read it by path rather than invoking `/dream`: a personal skill of the\n" +
		"same name shadows the repo-owned copy, and the repo-owned copy is the one that\n" +
		"knows about cells.\n" +
		"\n" +
		"## The sweep window\n" +
		"\n" +
		"Everything from **" + sinceISO + "** to **" + untilISO + "**.\n" +
		"\n" +
		"- Feedback: `kraken feedback sweep --since " + fmt.Sprint(since) + "`\n" +
		"- Transcripts: every claude session touched in that window.\n" +
		"\n" +
		"Both edges are unix millis in kraken's own watermark (`dream_last_run.swept_to`),\n" +
		"but only the FLOOR is a tool argument: `kraken mine digest`/`reduce` take `--since`\n" +
		"alone, so apply the ceiling yourself: skip sessions whose records all postdate it —\n" +
		"they are tomorrow's evidence — while a session that STRADDLES the ceiling is read\n" +
		"whole and its post-ceiling tail comes back tomorrow, expected rather than a bug\n" +
		"(two earlier passes, 2026-08-01 and 2026-08-02, each had to rediscover the\n" +
		"ceiling). The floor is where the last pass stopped, so a row is normally\n" +
		"read exactly once. The skill names the two seams where it is not, and what to do\n" +
		"about each: a long enough gap between passes drops the oldest evidence entirely,\n" +
		"and a note written between this job's creation and your sweep comes back\n" +
		"tomorrow. Do not widen the window on your own initiative.\n" +
		clampNote(clamped) +
		"\n" +
		"Findings never enter git. The PR carries the hardened diff and nothing else; the\n" +
		"evidence goes in `00-dream-report.md` and the watch store (`kraken watchlist`).\n"
}

// anyTranscriptSince reports whether any claude transcript was touched at or
// after sinceMS — the cheapest correct answer to "was there a session
// yesterday?".
//
// Deliberately NOT core.MineTranscripts: that OPENS and streams every
// candidate file with a 16 MB scanner (measured at 118 MB across 178 files
// for one real day on this machine) to produce per-tool counts nobody has
// asked for yet. This gate runs on a daemon tick and answers a boolean, so it
// only ever stats directory entries and stops at the first hit. The real mine
// happens later, inside the cell, where its cost is the point.
//
// Root transcripts only — the subagents/agent-*.jsonl sidecars two levels
// down are skipped on purpose. A sidecar exists only because its parent
// session dispatched a Task, which is itself a tool call written into the
// root transcript, so the root's mtime already covers it; walking into every
// session's subdirectory to re-learn that would cost a ReadDir per session
// for no additional answer.
//
// It returns (found, ok). This is the ONE place in the package where a
// defensive reader does NOT collapse every failure into a zero value, and the
// exception is deliberate: the caller advances the sweep floor on a negative
// answer, so a failure silently reported as "no sessions" would mark a window
// swept that nobody read, and the floor never moves backwards. ok=false means
// "I could not answer" and pins the watermark for a retry — costing at most a
// delayed night, never the night's evidence.
//
// The one failure that is a real answer is a MISSING projects dir: claude has
// never run here, so there are provably zero transcripts and nothing is lost
// by advancing. Treating that as unanswerable would pin the watermark forever
// on such a machine and retry every tick. Everything else — an unreadable
// root, an unreadable project subtree, an entry that can't be stat'd — leaves
// part of the window unseen and so cannot be reported as a quiet night.
func anyTranscriptSince(sinceMS int64) (found, ok bool) {
	root := filepath.Join(util.Home(), ".claude", "projects")
	projects, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return false, true // claude has never run: a real "no sessions"
	}
	if err != nil {
		return false, false
	}
	ok = true
	for _, p := range projects {
		if !p.IsDir() {
			continue
		}
		entries, err := os.ReadDir(filepath.Join(root, p.Name()))
		if err != nil {
			// A project dir that vanished between the two reads is genuinely
			// empty of anything we could still read; anything else is a blind
			// spot. Either way keep sweeping — a later dir may still produce a
			// positive, which needs no trust in this one.
			if !os.IsNotExist(err) {
				ok = false
			}
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				if !os.IsNotExist(err) {
					ok = false
				}
				continue
			}
			if info.ModTime().UnixMilli() >= sinceMS {
				// A positive is self-evidencing: it stands regardless of what
				// the rest of the walk could or couldn't read.
				return true, true
			}
		}
	}
	return false, ok
}

// clampNote is the sweep window's truncation disclosure. A clamped window
// (dreamMaxWindow) means the oldest evidence in the gap was never read and
// never will be — the floor only moves forwards — so the job body has to SAY
// so. A truncated sweep that reads as a complete one is the failure this
// whole feature is least able to afford, and the agent writing the night's
// report is the only one positioned to record the gap.
func clampNote(clamped bool) string {
	if !clamped {
		return ""
	}
	return "\n**This window was clamped.** The gap since the last pass exceeded the cap, so\n" +
		"evidence older than the floor above was NOT swept and will not be swept later —\n" +
		"the floor only moves forwards. Say so in the report's coverage section.\n"
}
