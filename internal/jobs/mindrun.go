package jobs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// mindrun.go — the mind run: how kraken turns what the operator said and
// marked into items in their mental model, with no cell and no confirm step.
//
// WHAT A RUN IS. One row in kraken.db's mind_runs table plus one headless
// `claude -p` process. The row is a ticket and a record at once: it is
// enqueued `queued`, moved to `running` when the worker picks it up, and
// closed `done` (with counts, a report, token usage) or `failed` (with a
// one-line note). Nothing about a run lives anywhere but that row and its
// blob row (mind_run_blobs: the evidence bundle, its manifest, the raw answer).
//
// WHEN A RUN IS QUEUED. Two triggers. Clicking Done on a job (DoneJob)
// queues a `done` run over that job's own sessions. Once per local calendar
// day (kickMindDaily) a `daily` run is queued over every session no done run
// swept, which is also what reads marks left on jobs that were already done.
// Each run covers a window [Since, Until]; a job's next done run starts where
// its last one ended, so clicking Done twice on a reactivated job never reads
// the same prompts twice.
//
// HOW THE QUEUE DRAINS. The daemon tick calls mindRunPass. One run at a time:
// a flag (mindRunning) is set while a run is in flight, and the oldest queued
// row is claimed only when the flag is clear. The run itself happens in a
// goroutine so the tick never waits on a model call. A serve restart finds
// any row still `running` (its process died with the old serve) and puts it
// back in the queue, once, on the first tick.
//
// WHAT A RUN DOES, in order (RunMindRun):
//  1. build the bundle — the run's evidence as one JSON document;
//  2. store the bundle (gzipped) and its manifest BEFORE anything else, so a
//     crash later still leaves the record of what was read;
//  3. start claude with no tools, the mind skill as its system prompt and the
//     bundle on stdin, under a 10-minute deadline;
//  4. store the raw answer, then parse it;
//  5. apply the answer's operations to the mental model (ApplyMindOps) —
//     kraken applies them, the model never writes anything;
//  6. close the row with counts, usage and a markdown report.
//
// Any step failing closes the row `failed` with a note, and the steps already
// done keep what they wrote — so a failed run's bundle and answer can still
// be read and replayed.
//
// WHY APPLY IS KRAKEN'S. The answer is a list of operations — add an item,
// supersede one (a new row replaces an old one, the old row kept dead with a
// pointer forward), retire one, flag one — and ApplyMindOps checks every
// operation before applying any, so one bad operation applies nothing.
// Folding a duplicate quote, suppressing a quote the operator dropped, and the
// per-run cap are all decided here, in one function.

// MindRunTimeout is the deadline one run's child process gets; past it the
// child is killed and the run fails with a note starting "timed out".
var MindRunTimeout = 10 * time.Minute

// MindBundleRetention is how long a run's bundle bytes are kept. Older
// bundles are pruned by the nightly backup pass; rows, manifests and answers
// are kept forever.
const MindBundleRetention = 90 * 24 * time.Hour

// mindBundleMaxBytes caps one bundle's JSON size. Over it, the oldest prompts
// are dropped first, each drop named in the bundle's Truncated list.
const mindBundleMaxBytes = 2 << 20

// mindSavedMaxBytes caps one saved artifact's text inside the bundle.
const mindSavedMaxBytes = 256 << 10

// mindDailyKVKey holds the daily run's watermark: {date, until}.
const mindDailyKVKey = "mind_daily"

// mindPromptBefore / mindPromptAfter are the reduced context blocks carried
// around each prompt — the same counts `kraken mine operator` defaults to.
const (
	mindPromptBefore = 6
	mindPromptAfter  = 2
)

// The run's sentinels. ErrMarkNoJob refuses a mark naming a slug no job
// (active or archived) carries; ErrMindBundlePruned refuses a replay whose
// bundle has passed retention.
var (
	ErrMarkNoJob        = errors.New("no job with that slug")
	ErrMindBundlePruned = errors.New("mind run bundle was pruned")
	// ErrCommitInvalid wraps every refusal of an item an operation carries.
	ErrCommitInvalid = errors.New("invalid mm item")
)

// ---------------------------------------------------------------------------
// the bundle
// ---------------------------------------------------------------------------

// MindBundle is everything one run reads, as the child receives it on stdin.
// Every slice is non-nil so the JSON always carries every key.
type MindBundle struct {
	Run       MindBundleRun         `json:"run"`
	Prompts   []core.OperatorRecord `json:"prompts"`
	Spec      *MMEvidenceJob        `json:"spec"`
	Feedback  []MMEvidenceFeedback  `json:"feedback"`
	Answers   []MMEvidenceAnswer    `json:"answers"`
	Saved     []MindBundleSaved     `json:"saved"`
	Marks     []MindBundleMark      `json:"marks"`
	Items     []MindBundleItem      `json:"items"`
	Truncated []string              `json:"truncated"`
}

// MindBundleRun names the run the bundle was built for.
type MindBundleRun struct {
	RunID   int64  `json:"run_id"`
	JobID   int64  `json:"job_id"`
	Slug    string `json:"slug"`
	Repo    string `json:"repo"`
	Trigger string `json:"trigger"`
	Since   int64  `json:"since"`
	Until   int64  `json:"until"`
}

// MindBundleSaved is one ★-saved artifact of the job, whole.
type MindBundleSaved struct {
	ID   int64  `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
	SHA  string `json:"sha"`
	Text string `json:"text"`
}

// MindBundleMark is one pending read mark.
type MindBundleMark struct {
	ID     int64         `json:"id"`
	Text   string        `json:"text"`
	Source CaptureSource `json:"source"`
}

// MindBundleItem is one live mental-model item, the run's basis for folding,
// superseding and retiring.
type MindBundleItem struct {
	ID     int64  `json:"id"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	Quote  string `json:"quote"`
	Scope  string `json:"scope"`
	Repo   string `json:"repo"`
	Dir    string `json:"dir"`
}

// MindManifest lists exactly what a bundle holds, so a run can be traced
// without opening its bundle: the sessions and transcript files its prompts
// came from, every prompt's record uuid, and the ids of every mark, saved
// artifact and item in it. Bytes is the uncompressed bundle size.
type MindManifest struct {
	Sessions        []string `json:"sessions"`
	TranscriptPaths []string `json:"transcript_paths"`
	UUIDs           []string `json:"uuids"`
	MarkIDs         []int64  `json:"mark_ids"`
	SavedIDs        []int64  `json:"saved_ids"`
	ItemIDs         []int64  `json:"item_ids"`
	Bytes           int      `json:"bytes"`
	Truncated       []string `json:"truncated"`
}

// mindOwnCellRe matches the project dirs of kraken's own date-named cells —
// the nightly `dream-YYYY-MM-DD` job and the retired `mind-YYYY-MM-DD` one,
// as munged by Claude Code (`…-kraken-cells-mind-2026-10-21-512-wt-kraken`,
// the `<id>-` optional for a pre-id cell dir). The daily run skips them:
// those cells' "human" prompts are kraken's own sends. Only the date shape
// matches, so an operator job whose slug merely starts with `mind-` or
// `dream-` (this very feature's `mind-pass-improvements` cell) is read.
var mindOwnCellRe = regexp.MustCompile(`-kraken-cells-(dream|mind)-\d{4}-\d{2}-\d{2}-(\d+-)?`)

// BuildMindBundle gathers one run's evidence. A job run (JobID != 0) reads
// that job only: the operator's prompts in its zeus session and its thread
// sessions, its spec when the operator wrote it, its feedback notes and typed
// answers in the window, its ★-saved artifacts and its pending marks. The
// daily run (JobID 0) reads prompts from every session no done run already
// swept, plus pending marks on done or archived jobs and marks with no job.
// Both carry every live item. A read error is returned, never papered over.
func BuildMindBundle(st *store.Store, run store.MindRun) (MindBundle, MindManifest, error) {
	if st == nil {
		return MindBundle{}, MindManifest{}, errors.New("no store")
	}
	b := MindBundle{
		Run: MindBundleRun{RunID: run.ID, JobID: run.JobID, Slug: run.Slug,
			Trigger: run.Trigger, Since: run.Since, Until: run.Until},
		Prompts: []core.OperatorRecord{}, Feedback: []MMEvidenceFeedback{},
		Answers: []MMEvidenceAnswer{}, Saved: []MindBundleSaved{},
		Marks: []MindBundleMark{}, Items: []MindBundleItem{}, Truncated: []string{},
	}
	var err error
	if run.JobID != 0 {
		err = buildJobBundle(st, run, &b)
	} else {
		err = buildDailyBundle(st, run, &b)
	}
	if err != nil {
		return MindBundle{}, MindManifest{}, err
	}
	items, err := st.ListMMItems(false)
	if err != nil {
		return MindBundle{}, MindManifest{}, err
	}
	for _, it := range items {
		b.Items = append(b.Items, MindBundleItem{ID: it.ID, Kind: it.Kind, Source: it.Source,
			Title: it.Title, Body: it.Content, Quote: it.Quote, Scope: it.Scope, Repo: it.Repo, Dir: it.Dir})
	}
	size, err := capMindBundle(&b)
	if err != nil {
		return MindBundle{}, MindManifest{}, err
	}
	return b, mindManifestOf(b, size), nil
}

// buildJobBundle fills a job run's sections.
func buildJobBundle(st *store.Store, run store.MindRun, b *MindBundle) error {
	sr, err := st.GetJobByID(run.JobID)
	if err != nil {
		return fmt.Errorf("mind run %d: job %d: %w", run.ID, run.JobID, err)
	}
	b.Run.Slug, b.Run.Repo = sr.Slug, sr.Repo
	if b.Run.Slug == "" {
		b.Run.Slug = run.Slug
	}

	// Prompts: the job's lead session and every thread it branched, never a
	// teammate root. An empty set must not reach OperatorProse, where empty
	// means "no filter" — a job that never stamped a session has no prompts.
	sessions := []string{}
	if sr.Session != "" {
		sessions = append(sessions, sr.Session)
	}
	threads, err := st.ListThreads(sr.ID)
	if err != nil {
		return err
	}
	for _, t := range threads {
		if t.SessionID != "" {
			sessions = append(sessions, t.SessionID)
		}
	}
	if len(sessions) > 0 {
		res, err := core.OperatorProse(core.OperatorOptions{Since: run.Since, Until: run.Until,
			Login: Operator(), Before: mindPromptBefore, After: mindPromptAfter, Sessions: sessions})
		if err != nil {
			return err
		}
		b.Prompts = res.Records
		if res.Truncated > 0 {
			b.Truncated = append(b.Truncated, fmt.Sprintf("operator reader: %d transcripts or projects could not be read in full", res.Truncated))
		}
	}

	if sr.Author == "operator" || sr.Author == "unknown" {
		body := JobSpecText(sr.Body)
		b.Spec = &MMEvidenceJob{Slug: sr.Slug, ID: sr.ID, Author: sr.Author,
			CreatedAt: sr.CreatedAt, UpdatedAt: sr.UpdatedAt,
			BodySHA: store.SHA256Hex([]byte(body)), Body: body}
	}

	inWindow := func(t int64) bool { return t >= run.Since && t <= run.Until }
	fb, err := st.ListFeedbackMeta(sr.ID)
	if err != nil {
		return err
	}
	for _, f := range fb {
		if inWindow(f.TS) {
			b.Feedback = append(b.Feedback, MMEvidenceFeedback{ID: f.ID, Slug: sr.Slug, Artifact: f.Artifact, TS: f.TS, Note: f.Note})
		}
	}
	qs, err := st.ListQuestions(sr.ID)
	if err != nil {
		return err
	}
	for _, q := range qs {
		if q.Answer == "" {
			continue
		}
		at, perr := time.Parse(time.RFC3339, q.AnsweredAt)
		if perr != nil || !inWindow(at.UnixMilli()) {
			continue
		}
		b.Answers = append(b.Answers, MMEvidenceAnswer{Slug: sr.Slug, QID: q.QID, Text: q.Text,
			Answer: q.Answer, AnsweredAt: q.AnsweredAt,
			AcceptedRec: strings.TrimSpace(q.Answer) == strings.TrimSpace(q.Recommendation)})
	}

	// ★-saved artifacts only: the synced artifacts table is never read, since
	// an artifact the operator did not save is not something they kept.
	saved, err := st.ListSavedArtifacts()
	if err != nil {
		return err
	}
	sort.SliceStable(saved, func(i, j int) bool { return saved[i].ID < saved[j].ID })
	for _, m := range saved {
		if m.JobID != sr.ID {
			continue
		}
		a, err := st.GetSavedArtifact(m.ID)
		if err != nil {
			return err
		}
		text := string(a.Snapshot)
		if !utf8.Valid(a.Snapshot) {
			text = ""
			b.Truncated = append(b.Truncated, fmt.Sprintf("saved %d (%s): not text, carried without its bytes", a.ID, a.Name))
		} else if len(text) > mindSavedMaxBytes {
			// capped on its own, so one huge save cannot evict every prompt
			// from the bundle; cut back to a rune boundary
			cut := mindSavedMaxBytes
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
			dropped := len(text) - cut
			text = text[:cut] + fmt.Sprintf("\n[truncated %d bytes]", dropped)
			b.Truncated = append(b.Truncated, fmt.Sprintf("saved %d (%s): text cut at %d bytes, %d bytes dropped", a.ID, a.Name, cut, dropped))
		}
		b.Saved = append(b.Saved, MindBundleSaved{ID: a.ID, Slug: a.Slug, Name: a.Name,
			SHA: store.SHA256Hex(a.Snapshot), Text: text})
	}

	marks, err := st.ListMMMarks(sr.ID, true)
	if err != nil {
		return err
	}
	b.Marks = mindBundleMarks(marks)
	return nil
}

// buildDailyBundle fills the daily run's sections.
func buildDailyBundle(st *store.Store, run store.MindRun, b *MindBundle) error {
	manifests, err := st.MindRunManifests("done", "done")
	if err != nil {
		return err
	}
	swept := map[string]bool{}
	for _, raw := range manifests {
		var m MindManifest
		if json.Unmarshal([]byte(raw), &m) != nil {
			continue // an unreadable manifest excludes nothing; worst case a duplicate quote, which folds
		}
		for _, s := range m.Sessions {
			swept[s] = true
		}
	}
	// Every job not yet done (and not archived) will be read by its own Done
	// run, so its lead and thread sessions are left to that run rather than
	// judged here as well.
	active, err := st.ListJobs()
	if err != nil {
		return err
	}
	for _, j := range active {
		if j.State == "done" {
			continue
		}
		if j.Session != "" {
			swept[j.Session] = true
		}
		threads, err := st.ListThreads(j.ID)
		if err != nil {
			return err
		}
		for _, t := range threads {
			if t.SessionID != "" {
				swept[t.SessionID] = true
			}
		}
	}
	exclude := make([]string, 0, len(swept))
	for s := range swept {
		exclude = append(exclude, s)
	}
	sort.Strings(exclude)
	res, err := core.OperatorProse(core.OperatorOptions{Since: run.Since, Until: run.Until,
		Login: Operator(), Before: mindPromptBefore, After: mindPromptAfter, ExcludeSessions: exclude})
	if err != nil {
		return err
	}
	for _, r := range res.Records {
		if mindOwnCell(r.ProjectDir) {
			continue
		}
		b.Prompts = append(b.Prompts, r)
	}
	if res.Truncated > 0 {
		b.Truncated = append(b.Truncated, fmt.Sprintf("operator reader: %d transcripts or projects could not be read in full", res.Truncated))
	}

	marks, err := st.ListMMMarks(0, true)
	if err != nil {
		return err
	}
	var keep []store.MMMark
	settled := map[int64]bool{}
	for _, m := range marks {
		if m.JobID == 0 {
			keep = append(keep, m)
			continue
		}
		ok, seen := settled[m.JobID]
		if !seen {
			j, err := st.GetJobByID(m.JobID)
			switch {
			case errors.Is(err, store.ErrNotFound):
				ok = true // the job is gone: its marks would otherwise never be read
			case err != nil:
				return err
			default:
				ok = j.State == "done" || j.ArchivedAt != 0
			}
			settled[m.JobID] = ok
		}
		if ok {
			keep = append(keep, m)
		}
	}
	b.Marks = mindBundleMarks(keep)
	return nil
}

func mindOwnCell(projectDir string) bool { return mindOwnCellRe.MatchString(projectDir) }

// mindBundleMarks converts mark rows, oldest first so the bundle reads in the
// order the operator marked.
func mindBundleMarks(marks []store.MMMark) []MindBundleMark {
	out := make([]MindBundleMark, 0, len(marks))
	for i := len(marks) - 1; i >= 0; i-- {
		m := marks[i]
		var src CaptureSource
		if json.Unmarshal([]byte(m.SourceJSON), &src) != nil || src.Kind == "" {
			src = CaptureSource{Kind: "manual"}
		}
		src.MarkID = m.ID
		out = append(out, MindBundleMark{ID: m.ID, Text: m.Text, Source: src})
	}
	return out
}

// capMindBundle drops the oldest prompts until the bundle's JSON fits
// mindBundleMaxBytes and returns the final size. Prompts are sorted oldest
// first by OperatorProse. Each prompt is marshalled once and the drop is
// worked out by subtraction (a prompt costs its JSON plus one comma), so a
// large bundle is not re-encoded once per dropped prompt; the drop is
// recorded as ONE Truncated line with the count. A last re-encode measures
// the real size, and the rare estimate that still lands over the cap drops
// one more prompt at a time.
func capMindBundle(b *MindBundle) (int, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return 0, err
	}
	if len(data) <= mindBundleMaxBytes {
		return len(data), nil
	}
	size := len(data)
	dropped := 0
	// headroom for the Truncated line this function will add
	const lineRoom = 160
	for dropped < len(b.Prompts) && size+lineRoom > mindBundleMaxBytes {
		pj, err := json.Marshal(b.Prompts[dropped])
		if err != nil {
			return 0, err
		}
		size -= len(pj) + 1
		dropped++
	}
	all := b.Prompts
	for {
		b.Prompts = all[dropped:]
		note := fmt.Sprintf("%d oldest prompts dropped: bundle over %d bytes", dropped, mindBundleMaxBytes)
		if dropped == len(all) {
			note = fmt.Sprintf("all %d prompts dropped and the bundle is still over %d bytes", dropped, mindBundleMaxBytes)
		}
		b.Truncated = append(b.Truncated, note)
		data, err = json.Marshal(b)
		if err != nil {
			return 0, err
		}
		if len(data) <= mindBundleMaxBytes || dropped == len(all) {
			return len(data), nil
		}
		b.Truncated = b.Truncated[:len(b.Truncated)-1]
		dropped++
	}
}

// mindManifestOf lists what b holds.
func mindManifestOf(b MindBundle, size int) MindManifest {
	m := MindManifest{Sessions: []string{}, TranscriptPaths: []string{}, UUIDs: []string{},
		MarkIDs: []int64{}, SavedIDs: []int64{}, ItemIDs: []int64{}, Bytes: size,
		Truncated: append([]string{}, b.Truncated...)}
	seenS, seenP := map[string]bool{}, map[string]bool{}
	for _, p := range b.Prompts {
		if p.Session != "" && !seenS[p.Session] {
			seenS[p.Session] = true
			m.Sessions = append(m.Sessions, p.Session)
		}
		if p.TranscriptPath != "" && !seenP[p.TranscriptPath] {
			seenP[p.TranscriptPath] = true
			m.TranscriptPaths = append(m.TranscriptPaths, p.TranscriptPath)
		}
		if p.UUID != "" {
			m.UUIDs = append(m.UUIDs, p.UUID)
		}
	}
	for _, x := range b.Marks {
		m.MarkIDs = append(m.MarkIDs, x.ID)
	}
	for _, x := range b.Saved {
		m.SavedIDs = append(m.SavedIDs, x.ID)
	}
	for _, x := range b.Items {
		m.ItemIDs = append(m.ItemIDs, x.ID)
	}
	return m
}

// ---------------------------------------------------------------------------
// the child process
// ---------------------------------------------------------------------------

// MindPrompt is the run's system prompt: the embedded mind skill with the
// operator's name substituted, and the sha256 of exactly that text (stamped
// on the run row, so a run says which version of the skill judged it).
func MindPrompt() (text, sha string, err error) {
	if SkillsFS == nil {
		return "", "", errors.New("no embedded skills: the mind skill is unavailable")
	}
	data, err := fs.ReadFile(SkillsFS, "mind/SKILL.md")
	if err != nil {
		return "", "", fmt.Errorf("read mind skill: %w", err)
	}
	data = bytes.ReplaceAll(data, operatorPlaceholder, []byte(Operator()))
	return string(data), store.SHA256Hex(data), nil
}

// mindModel is the model every run uses (Q7): the default tier, in one place.
func mindModel() string { return core.DefaultModel }

// mindDir is where a run starts when it has no worktree and no cell dir to
// start in — the daily run, and a done job whose cell was torn down.
func mindDir() string { return filepath.Join(util.Home(), ".kraken", "mind") }

// MindArgv builds one run's command, working directory and environment.
// --tools "" leaves the child no tools at all — it reads stdin and answers —
// so nothing it says can touch a file. There is no fork and no resume: a run
// is a fresh session every time. The cwd only decides which project dir its
// transcript lands under. The account binding follows AskArgv's rule.
func MindArgv(j *Job, model string) (argv []string, cwd string, env []string, err error) {
	prompt, _, err := MindPrompt()
	if err != nil {
		return nil, "", nil, err
	}
	argv = []string{core.ClaudeLauncher(), "-p",
		"--strict-mcp-config", "--restricted", "--disable-slash-commands",
		"--permission-mode", "default", "--permission-prompts", "none",
		"--system-prompt-snapshot", "off",
		"--output-format", "json",
		"--tools", ""}
	argv = append(argv, core.ModelArgs(model)...)
	argv = append(argv, "--append-system-prompt", prompt)

	if j != nil {
		cwd = threadWorktree(j)
		if cwd == "" {
			for _, d := range cellDirCandidates(j) {
				if fi, serr := os.Stat(d); serr == nil && fi.IsDir() {
					cwd = d
					break
				}
			}
		}
	}
	if cwd == "" {
		cwd = mindDir()
		if err := os.MkdirAll(cwd, 0o755); err != nil {
			return nil, "", nil, fmt.Errorf("create %s: %w", cwd, err)
		}
	}
	env = cellSpawnEnv()
	if env == nil {
		env = os.Environ()
	}
	if j != nil && j.AccountSlug != "" && j.Agent != "codex" {
		env = append(env, "CLAUDE_SECURESTORAGE_CONFIG_DIR="+slotDir(j.AccountSlug))
	}
	return argv, cwd, env, nil
}

// MindLauncherFunc runs one child to completion and returns both streams.
type MindLauncherFunc func(ctx context.Context, argv []string, dir string, env []string, stdin string) (stdout, stderr string, err error)

// mindLauncher is the seam a test swaps to script the child's answer.
var mindLauncher MindLauncherFunc = func(ctx context.Context, argv []string, dir string, env []string, stdin string) (string, string, error) {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	errBuf := &capBuffer{max: askStderrMax}
	cmd.Stdout = &out
	cmd.Stderr = errBuf
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	return out.String(), errBuf.String(), err
}

// SetMindLauncherForTest swaps mindLauncher and returns the restore.
func SetMindLauncherForTest(l MindLauncherFunc) (restore func()) {
	prev := mindLauncher
	mindLauncher = l
	return func() { mindLauncher = prev }
}

// ---------------------------------------------------------------------------
// the answer
// ---------------------------------------------------------------------------

// MindAnswer is what a run answers: the operations to apply, every candidate
// it excluded with the rung that excluded it, and free-form coverage.
type MindAnswer struct {
	Ops      []MindOp        `json:"ops"`
	Excluded []MindExclusion `json:"excluded"`
	Coverage map[string]any  `json:"coverage"`
}

// MindOp is one operation. add and supersede carry Item; supersede, retire
// and flag carry Target (an item id). Quote is the operator's words that
// justify it; Note is a flag's sentence.
type MindOp struct {
	Op     string      `json:"op"`
	Target int64       `json:"target,omitempty"`
	Item   *MMProposal `json:"item,omitempty"`
	Quote  string      `json:"quote,omitempty"`
	Note   string      `json:"note,omitempty"`
}

// MindExclusion is one candidate a run kept out, with the ladder rung.
type MindExclusion struct {
	Rung  string `json:"rung"`
	Quote string `json:"quote"`
	Why   string `json:"why"`
}

// MindUsage is the child's token usage, from --output-format json.
type MindUsage struct {
	In, Out int64
}

// MMProposal is one candidate item as a run proposes it. Rung is the ladder
// rung the candidate cleared, stored as the row's context.
type MMProposal struct {
	Title  string        `json:"title"`
	Scope  string        `json:"scope"`
	Repo   string        `json:"repo"`
	Dir    string        `json:"dir"`
	Kind   string        `json:"kind"`
	Body   string        `json:"body"`
	Quote  string        `json:"quote"`
	Rung   string        `json:"rung"`
	Source CaptureSource `json:"source"`
}

// mindEnvelope is the --output-format json wrapper claude -p prints.
type mindEnvelope struct {
	Result *string `json:"result"`
	Usage  struct {
		InputTokens  int64 `json:"input_tokens"`
		OutputTokens int64 `json:"output_tokens"`
	} `json:"usage"`
}

// ParseMindAnswer reads a run's stdout: either claude's JSON envelope (whose
// result string is the answer and whose usage fills usage) or the bare answer
// object, with a surrounding ``` fence tolerated. raw is the answer text as
// found, before the fence is stripped.
func ParseMindAnswer(stdout string) (ans MindAnswer, raw string, usage MindUsage, err error) {
	raw = stdout
	trimmed := strings.TrimSpace(stdout)
	var env mindEnvelope
	if json.Unmarshal([]byte(trimmed), &env) == nil && env.Result != nil {
		raw = *env.Result
		usage = MindUsage{In: env.Usage.InputTokens, Out: env.Usage.OutputTokens}
	}
	body := stripMindFence(raw)
	var probe map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body), &probe); err != nil {
		return MindAnswer{}, raw, usage, &mindParseError{kind: "is not a JSON object", detail: fmt.Sprintf("%v: %s", err, firstBytes(raw, 500))}
	}
	if _, ok := probe["ops"]; !ok {
		return MindAnswer{}, raw, usage, &mindParseError{kind: "has no ops key", detail: firstBytes(raw, 500)}
	}
	if err := json.Unmarshal([]byte(body), &ans); err != nil {
		return MindAnswer{}, raw, usage, &mindParseError{kind: "does not match the output contract", detail: fmt.Sprintf("%v: %s", err, firstBytes(raw, 500))}
	}
	if ans.Ops == nil {
		ans.Ops = []MindOp{}
	}
	if ans.Excluded == nil {
		ans.Excluded = []MindExclusion{}
	}
	if ans.Coverage == nil {
		ans.Coverage = map[string]any{}
	}
	return ans, raw, usage, nil
}

// mindParseError is ParseMindAnswer's refusal. Error() carries the first 500
// bytes of the answer for a caller reading it directly; note() is the form a
// run's note and the event log get, naming only the failure — the model's
// output is kept in the run's answer blob and nowhere else.
type mindParseError struct{ kind, detail string }

func (e *mindParseError) Error() string { return "mind answer " + e.kind + ": " + e.detail }

func (e *mindParseError) note() string {
	return "mind answer " + e.kind + " (answer kept on the run row)"
}

// stripMindFence removes one leading ``` line (with or without a language)
// and one trailing ```.
func stripMindFence(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	} else {
		s = strings.TrimPrefix(s, "```")
	}
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func firstBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---------------------------------------------------------------------------
// the applier
// ---------------------------------------------------------------------------

// mmProposeCap bounds the rows one run may insert (adds and supersedes'
// replacements together).
const mmProposeCap = 20

// The item caps (decimal runes): a title is one line, a body the 8000-rune
// cap the note/detail marks already use, a quote the capture cap.
const (
	mmTitleMaxRunes        = 120
	mmBodyMaxRunes         = 8000
	mmProposeQuoteMaxRunes = 32000
	mmProposeRungMaxRunes  = 200
)

// MindApply is ApplyMindOps' answer: one result per operation, in order, and
// the counts that tally them.
type MindApply struct {
	Results []MindOpResult      `json:"results"`
	Counts  store.MindRunCounts `json:"counts"`
}

// MindOpResult is one operation's outcome: added | folded | suppressed |
// refused | superseded | retired | flagged | noop (a retire of an item that
// was already invalidated: nothing written, nothing counted). ID is the new item for added and
// superseded, the existing item for folded, the flag for flagged.
type MindOpResult struct {
	Index   int    `json:"index"`
	Op      string `json:"op"`
	Outcome string `json:"outcome"`
	ID      int64  `json:"id,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// ApplyMindOps applies a run's answer to the mental model. Every operation is
// validated first and one invalid operation applies nothing; then each is
// applied in order (see the file header), and finally every mark the run's
// bundle carried is stamped consumed, whether or not an item came of it.
func ApplyMindOps(st *store.Store, run store.MindRun, ans MindAnswer, markIDs []int64, at int64) (MindApply, error) {
	if st == nil {
		return MindApply{}, errors.New("no store")
	}
	// One claim per target per answer: validation reads the store as it
	// stands BEFORE any op applies, so a second supersede or retire of the
	// same item would pass it and then meet a target this answer already
	// killed — after a supersede had already inserted its replacement.
	claimed := map[int64]int{}
	for i := range ans.Ops {
		op := &ans.Ops[i]
		op.Quote = mindNormQuote(op.Quote)
		if err := mindValidateOp(st, op); err != nil {
			return MindApply{}, fmt.Errorf("op %d: %w", i, err)
		}
		if op.Op == "supersede" || op.Op == "retire" {
			if prev, dup := claimed[op.Target]; dup {
				return MindApply{}, fmt.Errorf("op %d: %w: target %d is already claimed by op %d of this answer — one supersede or retire per target", i, ErrCommitInvalid, op.Target, prev)
			}
			claimed[op.Target] = i
		}
	}
	out := MindApply{Results: make([]MindOpResult, 0, len(ans.Ops))}
	inserted := 0
	for i, op := range ans.Ops {
		res, err := mindApplyOp(st, run, op, at, &inserted)
		if err != nil {
			return out, fmt.Errorf("op %d (%s): %w", i, op.Op, err)
		}
		res.Index, res.Op = i, op.Op
		out.Results = append(out.Results, res)
		switch res.Outcome {
		case "added":
			out.Counts.Added++
		case "superseded":
			out.Counts.Superseded++
		case "retired":
			out.Counts.Retired++
		case "flagged":
			out.Counts.Flagged++
		case "folded":
			out.Counts.Folded++
		case "suppressed":
			out.Counts.Suppressed++
		case "refused":
			out.Counts.Refused++
		}
	}
	// Counts.Excluded stays 0 here: RunMindRun tallies it when it finishes
	// the row, since an exclusion is a record of the answer, not an op applied.
	if err := st.ConsumeMMMarks(markIDs, run.ID, at); err != nil {
		return out, err
	}
	return out, nil
}

// mindValidateOp checks one operation against the store as it stands.
func mindValidateOp(st *store.Store, op *MindOp) error {
	switch op.Op {
	case "add", "supersede", "retire", "flag":
	default:
		return fmt.Errorf("%w: op %q: want add, supersede, retire or flag", ErrCommitInvalid, op.Op)
	}
	if op.Op == "add" || op.Op == "supersede" {
		if op.Item == nil {
			return fmt.Errorf("%w: %s needs an item", ErrCommitInvalid, op.Op)
		}
		if err := mmValidateProposal(op.Item); err != nil {
			return err
		}
	}
	if op.Op == "add" {
		return nil
	}
	if op.Target <= 0 {
		return fmt.Errorf("%w: %s needs a target item id", ErrCommitInvalid, op.Op)
	}
	t, err := st.GetMMItem(op.Target)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("%w: %s target %d: no such item", ErrCommitInvalid, op.Op, op.Target)
	}
	if err != nil {
		return err
	}
	if op.Op == "flag" {
		if strings.TrimSpace(op.Note) == "" && strings.TrimSpace(op.Quote) == "" {
			return fmt.Errorf("%w: flag on %d needs a note", ErrCommitInvalid, op.Target)
		}
		return nil
	}
	// supersede and retire (Q4): a code reading is anchored to lines the
	// verify pass watches, so only the code itself may kill it.
	if t.Source == "code" {
		return fmt.Errorf("%w: %s target %d is a code reading", ErrCommitInvalid, op.Op, op.Target)
	}
	// An already-invalidated target refuses a supersede (its replacement
	// would replace nothing) but NOT a retire: retiring a dead item is
	// idempotent, applied as outcome `noop` with no write.
	if t.InvalidatedAt != 0 && op.Op == "supersede" {
		return fmt.Errorf("%w: %w: supersede target %d is already invalidated (%s)", ErrCommitInvalid, store.ErrMMItemInvalidated, op.Target, t.InvalidReason)
	}
	return nil
}

// mindApplyOp applies one validated operation. inserted counts the rows this
// run has inserted so far, for the cap.
func mindApplyOp(st *store.Store, run store.MindRun, op MindOp, at int64, inserted *int) (MindOpResult, error) {
	switch op.Op {
	case "add", "supersede":
		res, err := mindFoldOrCap(st, op.Item.Quote, *inserted)
		if err != nil || res.Outcome != "" {
			return res, err
		}
		it := mindItemRow(run, *op.Item, at)
		if op.Op == "supersede" {
			it.Supersedes = op.Target
		}
		id, err := st.AddMMItem(it)
		if err != nil {
			return MindOpResult{}, err
		}
		*inserted++
		if op.Op == "add" {
			return MindOpResult{Outcome: "added", ID: id}, nil
		}
		detail := fmt.Sprintf("run %d: %s", run.ID, mindOpQuote(op))
		if err := st.SupersedeMMItem(op.Target, id, at, detail); err != nil {
			return MindOpResult{}, err
		}
		util.LogEvent("jobs", "info", "mm-supersede", fmt.Sprintf("mind run %d superseded mm item %d with %d", run.ID, op.Target, id))
		return MindOpResult{Outcome: "superseded", ID: id}, nil
	case "retire":
		// a target already dead — before the run, or by an earlier op of
		// this same answer — is a noop: nothing written, nothing counted
		t, err := st.GetMMItem(op.Target)
		if err != nil {
			return MindOpResult{}, err
		}
		if t.InvalidatedAt != 0 {
			return MindOpResult{Outcome: "noop", ID: op.Target}, nil
		}
		if err := st.InvalidateMMItem(op.Target, at, "outdated", fmt.Sprintf("run %d: %s", run.ID, mindOpQuote(op))); err != nil {
			return MindOpResult{}, err
		}
		return MindOpResult{Outcome: "retired", ID: op.Target}, nil
	case "flag":
		note := strings.TrimSpace(op.Note)
		if note == "" {
			note = strings.TrimSpace(op.Quote)
		}
		fid, err := st.AddMMFlag(store.MMFlag{ItemID: op.Target, JobID: run.JobID, Slug: run.Slug,
			Note: note, State: "raised", RaisedBy: fmt.Sprintf("run:%d", run.ID), RaisedAt: at, CreatedAt: at})
		if err != nil {
			return MindOpResult{}, err
		}
		return MindOpResult{Outcome: "flagged", ID: fid}, nil
	}
	return MindOpResult{}, fmt.Errorf("unknown op %q", op.Op) // unreachable past validation
}

// mindFoldOrCap decides a new row's fate before it is written: folded onto
// any row that is not invalidated and already carries the quote, suppressed when the
// operator dropped that quote before (removed or contradicted — their drop
// is the durable verdict), refused past the cap, or "" to insert.
func mindFoldOrCap(st *store.Store, quote string, inserted int) (MindOpResult, error) {
	rows, err := st.FindMMItemsByQuote(quote)
	if err != nil {
		return MindOpResult{}, err
	}
	for _, r := range rows { // newest first
		if r.InvalidatedAt == 0 {
			return MindOpResult{Outcome: "folded", ID: r.ID}, nil
		}
	}
	for _, r := range rows {
		if r.InvalidReason == "removed" || r.InvalidReason == "contradicted" {
			return MindOpResult{Outcome: "suppressed", ID: r.ID}, nil
		}
	}
	if inserted >= mmProposeCap {
		return MindOpResult{Outcome: "refused", Reason: "cap"}, nil
	}
	return MindOpResult{}, nil
}

// mindItemRow is the row an add or a supersede inserts: live, source nightly,
// owned by the run's job, with the run named both on the row and in the
// source pointer.
func mindItemRow(run store.MindRun, p MMProposal, at int64) store.MMItem {
	src := p.Source
	if src.RunID == 0 {
		src.RunID = run.ID
	}
	// Source "nightly" is kept for wire compatibility (shipped clients and
	// the source CHECK know that value); on a row it now means "written by a
	// mind run", whatever the trigger. The board labels it `pass`.
	return store.MMItem{
		Kind: p.Kind, Source: "nightly", State: "live",
		JobID: run.JobID, Slug: run.Slug,
		Content: p.Body, Context: p.Rung, Title: p.Title,
		Scope: p.Scope, Repo: p.Repo, Dir: p.Dir, Quote: p.Quote,
		SourceJSON: captureSourceJSON(src), CreatedAt: at, RunID: run.ID,
	}
}

// mindOpQuote is the quote an invalidation detail cites: the op's own, else
// its item's.
// mindNormQuote is the one form a quote is matched and stored in: CRLF
// folded to LF, then trimmed. Fold and suppression are byte-exact on it, so
// the same sentence copied with Windows line endings is still the same quote.
func mindNormQuote(q string) string {
	return strings.TrimSpace(strings.ReplaceAll(q, "\r\n", "\n"))
}

func mindOpQuote(op MindOp) string {
	if q := strings.TrimSpace(op.Quote); q != "" {
		return q
	}
	if op.Item != nil {
		return op.Item.Quote
	}
	return ""
}

// mmValidateProposal checks and normalizes one proposed item in place: a kind
// (reading only when the item is rooted in a mark or a saved artifact — the
// operator read those; the pass cannot know they read anything else), a
// pointer of one of the eight capture kinds, a non-empty quote within its cap
// (trimmed here so the fold lookup and the stored row see the same bytes), a
// short rung, then the shared item rules (mmValidateItem).
func mmValidateProposal(p *MMProposal) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrCommitInvalid, fmt.Sprintf(format, a...))
	}
	p.Quote = mindNormQuote(p.Quote)
	switch p.Kind {
	case "decision", "note":
	case "reading":
		if p.Source.MarkID == 0 && p.Source.SavedID == 0 {
			return bad("kind reading needs a source rooted in a mark (mark_id) or a saved artifact (saved_id)")
		}
	default:
		return bad("kind %q: want decision, note or reading", p.Kind)
	}
	switch p.Source.Kind {
	case "artifact", "transcript", "dir", "item", "manual", "job", "feedback", "question":
	default:
		return bad("source kind %q: want artifact, transcript, dir, item, manual, job, feedback or question", p.Source.Kind)
	}
	if n := utf8.RuneCountInString(p.Quote); n < 1 || n > mmProposeQuoteMaxRunes {
		return bad("quote must be 1-%d runes, got %d", mmProposeQuoteMaxRunes, n)
	}
	if n := utf8.RuneCountInString(p.Rung); n > mmProposeRungMaxRunes {
		return bad("rung must be at most %d runes, got %d", mmProposeRungMaxRunes, n)
	}
	return mmValidateItem(p)
}

// mmValidateItem is the item rules every writer of a pass item shares: title
// and body trimmed and within their caps, scope in its set, a universal item
// carrying no placement, a repo item naming a checkout ResolveRepo finds by
// its exact directory name and a repo-relative dir with no `..`, quotes,
// line breaks or backslashes, and a valid source.
func mmValidateItem(c *MMProposal) error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrCommitInvalid, fmt.Sprintf(format, a...))
	}
	c.Title = strings.TrimSpace(c.Title)
	if n := utf8.RuneCountInString(c.Title); n < 1 || n > mmTitleMaxRunes {
		return bad("title must be 1-%d runes, got %d", mmTitleMaxRunes, n)
	}
	c.Body = strings.TrimSpace(c.Body)
	if n := utf8.RuneCountInString(c.Body); n < 1 || n > mmBodyMaxRunes {
		return bad("body must be 1-%d runes, got %d", mmBodyMaxRunes, n)
	}
	switch c.Scope {
	case "universal":
		c.Repo, c.Dir = "", ""
	case "repo":
		name := strings.TrimSpace(c.Repo)
		if name == "" {
			return bad("a repo-scoped item needs a repo")
		}
		resolved, err := ResolveRepo(name)
		if err != nil {
			return bad("repo %q: %v", name, err)
		}
		// ResolveRepo is a case-insensitive SUBSTRING match — fine for finding
		// the checkout, wrong for accepting it: acceptance demands the exact name.
		if !strings.EqualFold(filepath.Base(resolved), name) {
			return bad("repo %q: must be the checkout's exact directory name under %s (closest match: %s)", name, repoRoot(), filepath.Base(resolved))
		}
		c.Repo = filepath.Base(resolved)
		dir := strings.TrimSpace(c.Dir)
		if strings.HasPrefix(dir, "/") {
			return bad("dir %q must be repo-relative", dir)
		}
		// the dir is rendered inside a quoted YAML glob in the rules file
		if strings.ContainsAny(dir, "\"\r\n") {
			return bad("dir %q must not contain quotes or line breaks", dir)
		}
		if strings.Contains(dir, `\`) {
			return bad("dir %q must not contain backslashes", dir)
		}
		for _, seg := range strings.Split(dir, "/") {
			if seg == ".." {
				return bad("dir %q must not contain ..", dir)
			}
		}
		c.Dir = path.Clean("/" + dir)[1:] // "" for the repo root, no trailing slash
	default:
		return bad("scope %q: want universal or repo", c.Scope)
	}
	if err := ValidCaptureSource(c.Source); err != nil {
		return bad("source: %v", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// the report
// ---------------------------------------------------------------------------

// MindRunReport renders a finished run as markdown: a headline, then the
// sections Proposed, Superseded, Retired, Folded, Excluded, Flagged and
// Coverage in that order, each saying None when it is empty. One line per
// entry, never hard-wrapped.
func MindRunReport(run store.MindRun, ans MindAnswer, apply MindApply, man MindManifest) string {
	c := apply.Counts
	who := run.Slug
	if run.JobID == 0 && who == "" {
		who = "daily catch-all"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# Mind run %d — %s (%s): %d added, %d superseded, %d retired, %d folded, %d excluded, %d flagged\n",
		run.ID, who, run.Trigger, c.Added, c.Superseded, c.Retired, c.Folded, len(ans.Excluded), c.Flagged)
	section := func(title string, lines []string) {
		b.WriteString("\n## " + title + "\n\n")
		if len(lines) == 0 {
			b.WriteString("None\n")
			return
		}
		for _, l := range lines {
			b.WriteString("- " + l + "\n")
		}
	}
	var proposed, superseded, retired, folded, flagged []string
	for _, r := range apply.Results {
		if r.Index < 0 || r.Index >= len(ans.Ops) {
			continue
		}
		op := ans.Ops[r.Index]
		title, quote := "", mindOneLine(mindOpQuote(op))
		if op.Item != nil {
			title = mindOneLine(op.Item.Title)
		}
		switch r.Outcome {
		case "added":
			proposed = append(proposed, fmt.Sprintf("#%d %s — “%s”", r.ID, title, quote))
		case "suppressed":
			proposed = append(proposed, fmt.Sprintf("suppressed (dropped before, #%d): %s — “%s”", r.ID, title, quote))
		case "refused":
			proposed = append(proposed, fmt.Sprintf("refused (%s): %s — “%s”", r.Reason, title, quote))
		case "superseded":
			superseded = append(superseded, fmt.Sprintf("#%d → #%d %s — “%s”", op.Target, r.ID, title, quote))
		case "retired":
			retired = append(retired, fmt.Sprintf("#%d — “%s”", op.Target, quote))
		case "folded":
			folded = append(folded, fmt.Sprintf("onto #%d: %s — “%s”", r.ID, title, quote))
		case "flagged":
			flagged = append(flagged, fmt.Sprintf("flag %d on #%d: %s", r.ID, op.Target, mindOneLine(op.Note)))
		}
	}
	var excluded []string
	for _, e := range ans.Excluded {
		excluded = append(excluded, fmt.Sprintf("%s — “%s” (%s)", mindOneLine(e.Rung), mindOneLine(e.Quote), mindOneLine(e.Why)))
	}
	coverage := []string{
		fmt.Sprintf("window %s → %s", time.UnixMilli(run.Since).Format(time.RFC3339), time.UnixMilli(run.Until).Format(time.RFC3339)),
		fmt.Sprintf("%d sessions, %d prompts, %d marks, %d saved artifacts, %d live items, %d bytes", len(man.Sessions), len(man.UUIDs), len(man.MarkIDs), len(man.SavedIDs), len(man.ItemIDs), man.Bytes),
	}
	for _, t := range man.Truncated {
		coverage = append(coverage, "truncated: "+mindOneLine(t))
	}
	keys := make([]string, 0, len(ans.Coverage))
	for k := range ans.Coverage {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v, _ := json.Marshal(ans.Coverage[k])
		coverage = append(coverage, k+": "+mindOneLine(string(v)))
	}
	section("Proposed", proposed)
	section("Superseded", superseded)
	section("Retired", retired)
	section("Folded", folded)
	section("Excluded", excluded)
	section("Flagged", flagged)
	section("Coverage", coverage)
	return b.String()
}

func mindOneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// ---------------------------------------------------------------------------
// running one
// ---------------------------------------------------------------------------

// mindRunJob loads a run's job for the argv (worktree, account binding); nil
// for the daily run or a job that no longer exists.
func mindRunJob(st *store.Store, run store.MindRun) *Job {
	if run.JobID == 0 {
		return nil
	}
	sr, err := st.GetJobByID(run.JobID)
	if err != nil || sr == nil {
		return nil
	}
	j, err := loadJobFull(st, sr)
	if err != nil {
		return nil
	}
	return j
}

// mindLaunch runs the child once over stdin under MindRunTimeout. A timeout
// comes back as an error whose text starts "timed out".
func mindLaunch(j *Job, stdin string) (stdout, model, sha string, err error) {
	model = mindModel()
	_, sha, err = MindPrompt()
	if err != nil {
		return "", model, "", err
	}
	argv, cwd, env, err := MindArgv(j, model)
	if err != nil {
		return "", model, sha, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), MindRunTimeout)
	defer cancel()
	out, stderr, lerr := mindLauncher(ctx, argv, cwd, env, stdin)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return out, model, sha, fmt.Errorf("timed out after %s", MindRunTimeout)
	}
	if lerr != nil {
		msg := lerr.Error()
		if s := strings.TrimSpace(stderr); s != "" {
			msg += ": " + core.TruncateOneLine(s, askNoteMax)
		}
		return out, model, sha, errors.New(msg)
	}
	return out, model, sha, nil
}

// RunMindRun executes one claimed (running) run end to end — see the file
// header for the order. Any failure closes the row failed with a one-line
// note and returns the error.
func RunMindRun(st *store.Store, run store.MindRun) error {
	if st == nil {
		return errors.New("no store")
	}
	fail := mindRunFailer(st, run)
	util.LogEvent("jobs", "info", "mind-run-started", fmt.Sprintf("mind run %d (%s %s) started", run.ID, run.Trigger, run.Slug))

	bundle, man, err := BuildMindBundle(st, run)
	if err != nil {
		return fail(fmt.Errorf("bundle: %w", err))
	}
	data, err := json.Marshal(bundle)
	if err != nil {
		return fail(fmt.Errorf("bundle: %w", err))
	}
	if err := storeMindBlobs(st, run.ID, data, man); err != nil {
		return fail(err)
	}
	return runMindOn(st, run, string(data), man, fail)
}

// mindRunFailer is how every path closes a run as failed: the row gets the
// note, the event log one line, and the operator one notification naming the
// job (or `daily`) and the note's first line — a failed run is otherwise
// silent, since nothing else ever shows a run that changed nothing.
func mindRunFailer(st *store.Store, run store.MindRun) func(error) error {
	return func(err error) error {
		if ferr := st.FailMindRun(run.ID, time.Now().UnixMilli(), err.Error()); ferr != nil {
			dlogErr("mind run %d: fail write: %v", run.ID, ferr)
		}
		if run.Trigger == "daily" {
			rewindMindDaily(st, run)
		}
		util.LogEvent("jobs", "error", "mind-run-failed", fmt.Sprintf("mind run %d (%s %s) failed: %v", run.ID, run.Trigger, run.Slug, err))
		who := run.Slug
		if run.JobID == 0 || who == "" {
			who = "daily"
		}
		first, _, _ := strings.Cut(err.Error(), "\n")
		mindRunNotify("kraken ◆ mental model", fmt.Sprintf("%s: mind run failed — %s", who, first))
		return err
	}
}

// storeMindBlobs gzips a bundle and writes it with its manifest.
func storeMindBlobs(st *store.Store, runID int64, data []byte, man MindManifest) error {
	gz, err := gzipBytes(data)
	if err != nil {
		return fmt.Errorf("bundle gzip: %w", err)
	}
	mj, err := json.Marshal(man)
	if err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	return st.SetMindRunBlobs(runID, gz, string(mj))
}

// runMindOn is RunMindRun past the bundle: launch, record, parse, apply,
// finish. Shared with ReplayMindRun --write, which launches on a stored bundle.
func runMindOn(st *store.Store, run store.MindRun, stdin string, man MindManifest, fail func(error) error) error {
	j := mindRunJob(st, run)
	out, model, sha, err := mindLaunch(j, stdin)
	if err != nil {
		if strings.TrimSpace(out) != "" {
			if aerr := st.SetMindRunAnswer(run.ID, out); aerr != nil {
				dlogErr("mind run %d: answer write: %v", run.ID, aerr)
			}
		}
		return fail(err)
	}
	return finishMindRun(st, run, out, model, sha, man, fail)
}

// finishMindRun records the answer, parses it, applies it and closes the row.
func finishMindRun(st *store.Store, run store.MindRun, out, model, sha string, man MindManifest, fail func(error) error) error {
	ans, raw, usage, perr := ParseMindAnswer(out)
	answer := raw
	if perr != nil {
		answer = out
	}
	if err := st.SetMindRunAnswer(run.ID, answer); err != nil {
		return fail(fmt.Errorf("answer write: %w", err))
	}
	if perr != nil {
		var pe *mindParseError
		if errors.As(perr, &pe) {
			return fail(errors.New(pe.note()))
		}
		return fail(perr)
	}
	at := time.Now().UnixMilli()
	apply, err := ApplyMindOps(st, run, ans, man.MarkIDs, at)
	if err != nil {
		return fail(fmt.Errorf("apply: %w", err))
	}
	apply.Counts.Excluded = len(ans.Excluded) // the row's tally; ApplyMindOps leaves it 0
	if err := st.FinishMindRun(run.ID, store.MindRunFinish{
		At: time.Now().UnixMilli(), Counts: apply.Counts,
		Report:    MindRunReport(run, ans, apply, man),
		PromptSHA: sha, Model: model,
		InTokens: usage.In, OutTokens: usage.Out, BundleBytes: int64(man.Bytes),
	}); err != nil {
		return fail(err)
	}
	c := apply.Counts
	util.LogEvent("jobs", "info", "mind-run-done", fmt.Sprintf("mind run %d (%s %s): %d added, %d superseded, %d retired, %d flagged, %d folded, %d suppressed, %d refused, %d excluded",
		run.ID, run.Trigger, run.Slug, c.Added, c.Superseded, c.Retired, c.Flagged, c.Folded, c.Suppressed, c.Refused, c.Excluded))
	return nil
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func gunzipBytes(b []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// ---------------------------------------------------------------------------
// queueing
// ---------------------------------------------------------------------------

// EnqueueMindRunForJob queues one run over a job: from the end of its last
// done run's window (else the job's creation), to now.
func EnqueueMindRunForJob(st *store.Store, j *Job, trigger string, now time.Time) (int64, error) {
	if st == nil {
		return 0, errors.New("no store: cannot queue a mind run")
	}
	if j == nil {
		return 0, errors.New("no job")
	}
	since, err := st.LastMindRunUntil(j.ID, "done")
	if err != nil {
		return 0, err
	}
	if since == 0 {
		sr, err := st.GetJobByID(j.ID)
		if err != nil {
			return 0, fmt.Errorf("job %s: %w", j.Slug, err)
		}
		since = sr.CreatedAt
	}
	id, err := st.EnqueueMindRun(store.MindRun{JobID: j.ID, Slug: j.Slug, Trigger: trigger,
		Since: since, Until: now.UnixMilli(), CreatedAt: now.UnixMilli()})
	if err != nil {
		return 0, err
	}
	util.LogEvent("jobs", "info", "mind-run-queued", fmt.Sprintf("mind run %d queued (%s %s)", id, trigger, j.Slug))
	return id, nil
}

// enqueueDoneMindRun is DoneJob's hook: never fatal, a failure is one event.
func enqueueDoneMindRun(j *Job) {
	if _, err := EnqueueMindRunForJob(ActiveStore(), j, "done", time.Now()); err != nil {
		util.LogEvent("jobs", "error", "mind-run-enqueue-failed", fmt.Sprintf("%s: mind run not queued: %v", j.Slug, err))
	}
}

// rewindMindDaily moves the daily watermark's `until` back to a FAILED daily
// run's `since`, so tomorrow's run covers the window this one never judged.
// kickMindDaily advances the watermark when it ENQUEUES, whatever the run's
// outcome, and a daily run that died — the first production day's daily run
// failed on a DNS outage at its first request — left a window of root
// sessions that no later run would ever list. The date is left alone, so the
// rewind never re-queues a run today: a failure that repeats (the outage
// still on) would otherwise re-enqueue every tick. A missing or unreadable
// watermark is left as it is, since there is then nothing to rewind, and a
// watermark already at or before the run's since is never moved, since a
// rewind must never advance it. When a LATER daily run was already queued
// before this one failed (a run straddling midnight, a laptop asleep
// mid-run), the rewind re-covers that newer run's window too: the next run
// judges it twice, and the byte-exact fold absorbs the repeats. Coverage is
// chosen over the tokens on purpose — a window judged twice costs one
// oversized run, a window judged never is gone.
func rewindMindDaily(st *store.Store, run store.MindRun) {
	raw, err := st.GetKV(mindDailyKVKey)
	if err != nil {
		return
	}
	var prev mindDaily
	if json.Unmarshal([]byte(raw), &prev) != nil || prev.Until == 0 || prev.Until <= run.Since {
		return
	}
	prev.Until = run.Since
	data, _ := json.Marshal(prev)
	if err := st.SetKV(mindDailyKVKey, string(data)); err != nil {
		dlogErr("mind daily watermark rewind failed: %v", err)
		return
	}
	util.LogEvent("jobs", "info", "mind-daily-rewound", fmt.Sprintf("mind run %d failed — the daily watermark is back at %s, so the next daily run re-covers its window", run.ID, time.UnixMilli(run.Since).Format(time.RFC3339)))
}

// mindDaily is the daily run's watermark.
type mindDaily struct {
	Date  string `json:"date"`  // local calendar date, "2006-01-02"
	Until int64  `json:"until"` // unix millis — the next daily run's since
}

// lastMindDailyDate backs the kv watermark up in memory, set only when the
// watermark write fails, so a store that cannot record does not enqueue a
// daily run on every tick.
var lastMindDailyDate atomic.Value // string

// kickMindDaily queues the daily run at most once per local calendar date —
// kickDream's date comparison, so a Mac asleep at midnight queues on the
// first tick after it wakes and no day is doubled. Its window runs from the
// previous daily run's until (else 24h back) to now.
func kickMindDaily(st *store.Store, now time.Time) (int64, bool) {
	if st == nil {
		return 0, false
	}
	today := now.Format(dreamDateLayout)
	if s, _ := lastMindDailyDate.Load().(string); s == today {
		return 0, false
	}
	var prev mindDaily
	raw, err := st.GetKV(mindDailyKVKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
	case err != nil:
		return 0, false // unreadable degrades to "already ran today"
	default:
		if json.Unmarshal([]byte(raw), &prev) != nil {
			dlogErr("mind daily watermark unparseable (%q) — resetting it", raw)
			prev = mindDaily{}
		}
	}
	if prev.Date >= today {
		return 0, false
	}
	nowMS := now.UnixMilli()
	since := prev.Until
	if since <= 0 {
		since = now.Add(-24 * time.Hour).UnixMilli()
	}
	if since >= nowMS {
		since = nowMS - 1 // a clock that moved backwards still queues a (tiny) window
	}
	id, err := st.EnqueueMindRun(store.MindRun{Trigger: "daily", Since: since, Until: nowMS, CreatedAt: nowMS})
	if err != nil {
		dlogErr("mind daily run not queued: %v", err)
		return 0, false
	}
	data, _ := json.Marshal(mindDaily{Date: today, Until: nowMS})
	if err := st.SetKV(mindDailyKVKey, string(data)); err != nil {
		lastMindDailyDate.Store(today)
		dlogErr("mind daily watermark write failed: %v", err)
	}
	util.LogEvent("jobs", "info", "mind-run-queued", fmt.Sprintf("mind run %d queued (daily %s)", id, today))
	return id, true
}

// The worker's state. mindRunning is set while a run is in flight and
// mindInFlight names that run's row (0 when none); mindRequeued records that
// this process already put orphaned running rows back; mindRunNotify is the
// notification seam; mindRunGo is the goroutine seam a test replaces to run
// synchronously.
var (
	mindRunning   atomic.Bool
	mindInFlight  atomic.Int64
	mindRequeued  atomic.Bool
	mindRunNotify = notify
	mindRunGo     = func(f func()) { go f() }
)

// mindStaleAfter is how long a row may sit `running` before the pass calls
// it stale: twice the child's own deadline, so a live run is never caught.
func mindStaleAfter() time.Duration { return 2 * MindRunTimeout }

// failStaleMindRuns fails every running row older than mindStaleAfter that
// is not this process's in-flight run — a row whose worker died without
// either finishing or failing it, which nothing else would ever close.
func failStaleMindRuns(st *store.Store, now time.Time) {
	rows, err := st.ListRunningMindRuns()
	if err != nil {
		dlogErr("mind stale-run sweep failed: %v", err)
		return
	}
	floor := now.Add(-mindStaleAfter()).UnixMilli()
	for _, r := range rows {
		if r.ID == mindInFlight.Load() || r.StartedAt >= floor {
			continue
		}
		_ = mindRunFailer(st, r)(errors.New("stale running row"))
	}
}

// mindRunPass is the daemon tick's mind step (never under --mirror): repair
// after a restart once, queue the daily run when due, then start the oldest
// queued run when none is in flight.
func (d *daemonState) mindRunPass(st *store.Store, now time.Time) {
	if st == nil {
		return
	}
	if mindRequeued.CompareAndSwap(false, true) {
		if n, err := st.RequeueRunningMindRuns("serve restarted"); err != nil {
			dlogErr("mind runs requeue failed: %v", err)
			mindRequeued.Store(false)
		} else if n > 0 {
			dlogEvent("info", "mind-run-queued", "mind: requeued %d run(s) left running by the previous serve", n)
		}
	}
	failStaleMindRuns(st, now)
	kickMindDaily(st, now)
	if !mindRunning.CompareAndSwap(false, true) {
		return
	}
	run, err := st.ClaimNextMindRun(now.UnixMilli())
	if err != nil || run == nil {
		if err != nil {
			dlogErr("mind run claim failed: %v", err)
		}
		mindRunning.Store(false)
		return
	}
	claimed := *run
	mindInFlight.Store(claimed.ID)
	notifyFn := mindRunNotify
	mindRunGo(func() {
		defer mindRunning.Store(false)
		defer mindInFlight.Store(0)
		if err := RunMindRun(st, claimed); err != nil {
			return
		}
		done, err := st.GetMindRun(claimed.ID)
		if err != nil {
			return
		}
		if done.Added+done.Superseded+done.Retired > 0 {
			who := done.Slug
			if who == "" {
				who = "daily catch-all"
			}
			notifyFn("kraken ◆ mental model", fmt.Sprintf("%s: %d added, %d rewritten, %d retired", who, done.Added, done.Superseded, done.Retired))
		}
	})
}

// PruneMindRunBundles drops bundle bytes older than MindBundleRetention —
// the nightly backup pass calls it after each backup.
func PruneMindRunBundles(st *store.Store, now time.Time) (int, error) {
	if st == nil {
		return 0, errors.New("no store")
	}
	n, err := st.PruneMindRunBundles(now.Add(-MindBundleRetention).UnixMilli(), now.UnixMilli())
	if err != nil {
		return 0, err
	}
	if n > 0 {
		util.LogEvent("jobs", "info", "mind-run-pruned", fmt.Sprintf("pruned %d mind run bundle(s) older than %s", n, MindBundleRetention))
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// marks
// ---------------------------------------------------------------------------

// MarkRequest is POST /api/mm/mark's body: the passage the operator marked
// as read, the job it belongs to ("" for none), and where it came from.
type MarkRequest struct {
	Slug   string        `json:"slug"`
	Text   string        `json:"text"`
	Source CaptureSource `json:"source"`
}

// MarkResult names the stored (or folded-onto) mark and how many marks are
// now pending on that job (job-less marks count among themselves).
type MarkResult struct {
	ID      int64 `json:"id"`
	Pending int   `json:"pending"`
	Folded  bool  `json:"folded"`
}

// MarkRead stores a read mark — no live cell needed, any job state, or no job
// at all. The next run that covers the job reads it: the job's done run, or
// the daily run once the job is done.
func MarkRead(st *store.Store, req MarkRequest, now time.Time) (MarkResult, error) {
	if st == nil {
		return MarkResult{}, errors.New("no store")
	}
	if strings.TrimSpace(req.Text) == "" {
		return MarkResult{}, fmt.Errorf("%w: the marked text is empty", ErrCaptureBadSource)
	}
	if n := utf8.RuneCountInString(req.Text); n > captureMaxRunes {
		return MarkResult{}, fmt.Errorf("%w: %d runes, the cap is %d", ErrCaptureTooLong, n, captureMaxRunes)
	}
	if err := ValidCaptureSource(req.Source); err != nil {
		return MarkResult{}, fmt.Errorf("%w: %v", ErrCaptureBadSource, err)
	}
	var jobID int64
	slug := strings.TrimSpace(req.Slug)
	if slug != "" {
		id, err := markJobID(st, slug)
		if err != nil {
			return MarkResult{}, err
		}
		jobID = id
	}
	id, folded, err := st.AddMMMark(store.MMMark{JobID: jobID, Slug: slug, Text: req.Text,
		SourceJSON: captureSourceJSON(req.Source), CreatedAt: now.UnixMilli()})
	if err != nil {
		return MarkResult{}, err
	}
	pending, err := st.ListMMMarks(jobID, true)
	if err != nil {
		return MarkResult{}, err
	}
	n := 0
	for _, m := range pending {
		if m.JobID == jobID {
			n++
		}
	}
	util.LogEvent("jobs", "info", "mm-mark", fmt.Sprintf("mark %d on %q (folded %v, %d pending)", id, slug, folded, n))
	return MarkResult{ID: id, Pending: n, Folded: folded}, nil
}

// markJobID resolves a mark's slug: the active job first, then the newest
// archived job carrying that slug (or that archived name).
func markJobID(st *store.Store, slug string) (int64, error) {
	j, err := st.GetJobBySlug(slug)
	if err == nil && j != nil {
		return j.ID, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return 0, err
	}
	archived, err := st.ListArchivedJobs()
	if err != nil {
		return 0, err
	}
	var best *store.Job
	for _, a := range archived {
		if a.Slug == slug || a.ArchivedName == slug {
			if best == nil || a.ArchivedAt > best.ArchivedAt {
				best = a
			}
		}
	}
	if best == nil {
		return 0, fmt.Errorf("%w: %s", ErrMarkNoJob, slug)
	}
	return best.ID, nil
}

// ---------------------------------------------------------------------------
// replay
// ---------------------------------------------------------------------------

// MindReplay is one replay: the recorded run's answer beside the answer the
// current skill gives the same bundle, and what changed.
type MindReplay struct {
	SourceRun    int64          `json:"source_run"`
	OldPromptSHA string         `json:"old_prompt_sha"`
	NewPromptSHA string         `json:"new_prompt_sha"`
	Old          MindAnswer     `json:"old"`
	New          MindAnswer     `json:"new"`
	Diff         MindReplayDiff `json:"diff"`
	NewRun       int64          `json:"new_run"`
}

// MindReplayDiff is what the new answer added and lost against the old one.
type MindReplayDiff struct {
	OpsAdded          []MindOp        `json:"ops_added"`
	OpsLost           []MindOp        `json:"ops_lost"`
	ExclusionsChanged []MindExclusion `json:"exclusions_changed"`
}

// ReplayMindRun judges a recorded run's bundle again under the current skill.
// write false changes nothing anywhere and only reports the diff; write true
// also records it as a new `replay` run over the same window and applies its
// operations.
func ReplayMindRun(st *store.Store, id int64, write bool, now time.Time) (MindReplay, error) {
	if st == nil {
		return MindReplay{}, errors.New("no store")
	}
	run, err := st.GetMindRun(id)
	if err != nil {
		return MindReplay{}, err
	}
	blobs, err := st.GetMindRunBlobs(id)
	if err != nil {
		return MindReplay{}, fmt.Errorf("mind run %d has no recorded bundle: %w", id, err)
	}
	if blobs.PrunedAt > 0 || blobs.BundleGz == nil {
		return MindReplay{}, fmt.Errorf("%w: run %d's bundle was pruned on %s", ErrMindBundlePruned, id,
			time.UnixMilli(blobs.PrunedAt).Format(dreamDateLayout))
	}
	data, err := gunzipBytes(blobs.BundleGz)
	if err != nil {
		return MindReplay{}, fmt.Errorf("mind run %d bundle: %w", id, err)
	}
	var man MindManifest
	_ = json.Unmarshal([]byte(blobs.Manifest), &man)

	rep := MindReplay{SourceRun: id, OldPromptSHA: run.PromptSHA}
	if old, _, _, perr := ParseMindAnswer(blobs.Answer); perr == nil {
		rep.Old = old
	} else {
		rep.Old = MindAnswer{Ops: []MindOp{}, Excluded: []MindExclusion{}, Coverage: map[string]any{}}
	}

	// --write records a `replay` row and applies its answer, so it takes the
	// worker's single-flight for the whole run: two appliers never interleave.
	// Its row is inserted already `running` — never queued, where the worker
	// could claim it and rebuild a bundle from today's store instead of
	// judging the recorded one.
	var newRun store.MindRun
	fail := func(e error) error { return e }
	if write {
		if !mindRunning.CompareAndSwap(false, true) {
			return MindReplay{}, errors.New("a mind run is in flight — replay with --write once it finishes")
		}
		defer mindRunning.Store(false)
		nid, err := st.InsertRunningMindRun(store.MindRun{JobID: run.JobID, Slug: run.Slug, Trigger: "replay",
			Since: run.Since, Until: run.Until, CreatedAt: now.UnixMilli(), SourceRun: id}, now.UnixMilli())
		if err != nil {
			return MindReplay{}, err
		}
		mindInFlight.Store(nid)
		defer mindInFlight.Store(0)
		if newRun, err = st.GetMindRun(nid); err != nil {
			return MindReplay{}, err
		}
		rep.NewRun = nid
		fail = mindRunFailer(st, newRun)
		if err := st.SetMindRunBlobs(nid, blobs.BundleGz, blobs.Manifest); err != nil {
			return MindReplay{}, fail(err)
		}
	}

	out, model, sha, lerr := mindLaunch(mindRunJob(st, run), string(data))
	rep.NewPromptSHA = sha
	if lerr != nil {
		if write && strings.TrimSpace(out) != "" {
			_ = st.SetMindRunAnswer(newRun.ID, out)
		}
		return rep, fail(lerr)
	}
	ans, _, _, perr := ParseMindAnswer(out)
	if perr != nil && !write {
		return rep, perr
	}
	if perr == nil {
		rep.New = ans
		rep.Diff = diffMindAnswers(rep.Old, rep.New)
	}
	if write {
		if err := finishMindRun(st, newRun, out, model, sha, man, fail); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

// diffMindAnswers compares two answers: operations present in one and not
// the other (compared as their JSON), and exclusions whose quote is new,
// gone, or excluded on a different rung. Every slice is non-nil.
func diffMindAnswers(old, cur MindAnswer) MindReplayDiff {
	d := MindReplayDiff{OpsAdded: []MindOp{}, OpsLost: []MindOp{}, ExclusionsChanged: []MindExclusion{}}
	key := func(op MindOp) string {
		b, _ := json.Marshal(op)
		return string(b)
	}
	count := func(ops []MindOp) map[string]int {
		m := map[string]int{}
		for _, op := range ops {
			m[key(op)]++
		}
		return m
	}
	oldN, curN := count(old.Ops), count(cur.Ops)
	for _, op := range cur.Ops {
		k := key(op)
		if oldN[k] > 0 {
			oldN[k]--
			continue
		}
		d.OpsAdded = append(d.OpsAdded, op)
	}
	for _, op := range old.Ops {
		k := key(op)
		if curN[k] > 0 {
			curN[k]--
			continue
		}
		d.OpsLost = append(d.OpsLost, op)
	}
	oldRung, curRung := map[string]string{}, map[string]string{}
	for _, e := range old.Excluded {
		oldRung[e.Quote] = e.Rung
	}
	for _, e := range cur.Excluded {
		curRung[e.Quote] = e.Rung
	}
	for _, e := range cur.Excluded {
		if r, ok := oldRung[e.Quote]; !ok || r != e.Rung {
			d.ExclusionsChanged = append(d.ExclusionsChanged, e)
		}
	}
	for _, e := range old.Excluded {
		if _, ok := curRung[e.Quote]; !ok {
			d.ExclusionsChanged = append(d.ExclusionsChanged, e)
		}
	}
	return d
}
