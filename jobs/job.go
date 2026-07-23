package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"kraken/core"
	"kraken/util"
)

// --- jobs ---

// A Job is one <slug>.job.md file in the pluto dir: the durable record of a
// unit of work. The frontmatter is a deliberately tiny YAML subset (scalars,
// one block scalar, a list of prereq maps) parsed by hand — and parsed
// STRICTLY: any construct the parser doesn't fully understand (unknown or
// duplicate keys, bad booleans, stray lines, malformed prereqs) marks the job
// Err. An erred job never runs, and the error is surfaced on the board.
// Parsing never crashes —
// strict means loud, not fragile.
type Job struct {
	Slug string `json:"slug"`
	File string `json:"file"`
	Type string `json:"type"` // build | info (legacy explore/question parse as info)
	Repo string `json:"repo"`
	// Base is the ref the cell's worktree branches from; empty means the
	// default (origin/main, falling back to main).
	Base  string `json:"base,omitempty"`
	Ready bool   `json:"ready"`
	Solo  bool   `json:"solo"`
	// Model is the Claude model zeus runs on (`claude --model <value>`),
	// hand-authored like solo. Consumed exactly once per cell start
	// (StartCell/ReactivateJob) — never re-read mid-run, so a live cell's
	// model can't change out from under it. Empty means claude's default.
	Model string `json:"model,omitempty"`
	// SkipDoc (build jobs only) skips the /doc → /send phase: zeus implements
	// the spec directly, keeping the manual-test gate and PR pipeline.
	SkipDoc bool     `json:"skip_doc,omitempty"`
	Prereqs []Prereq `json:"prereqs,omitempty"`
	// State is kraken-managed ("" = never started); the human-authored spec
	// body below the frontmatter is never rewritten.
	State   string `json:"state,omitempty"`
	Outcome string `json:"outcome,omitempty"`
	// Session is zeus' Claude session id, kraken-managed: stamped into the
	// job file as soon as the live cell's zeus reports a real (non-guessed)
	// id. It is what makes a done job reactivatable into the same
	// conversation after its cell is long gone.
	Session string `json:"session,omitempty"`
	// Teardown is the kraken-managed teardown-intent marker: written by
	// DoneJob/KillJob after artifact preservation but BEFORE the first
	// destructive step ("done:<outcome>" or "kill"), and cleared by the same
	// call's final state write. A surviving marker means a teardown was
	// interrupted mid-flight (crash, ^C, the process dying with the session);
	// the daemon finishes it on a later tick once the cell session is gone.
	// The marker only ever exists downstream of carl's explicit done/kill, so
	// completing it — including the force worktree discard — is carl-sanctioned.
	Teardown string `json:"teardown,omitempty"`
	// Run is the kraken-managed cell-run counter for branch versioning:
	// 0/1 → carl/<slug>, N≥2 → carl/<slug>-rN. Since branch recycling
	// (checkout -B off the base once the old branch is fully merged) it is
	// only a fallback: run advances only when an existing branch still
	// holds unmerged commits that a forced reset would orphan.
	Run int `json:"run,omitempty"`
	// PRs is the kraken-managed record of every PR shipped from the job's
	// cell branch: appended and state-updated by the daemon's PR tracker,
	// never cleared — it is the job's shipping history across branch
	// recycles and reactivations. A merge here is a signal (board chip +
	// one notification), never a teardown trigger.
	PRs []JobPR `json:"prs,omitempty"`
	// ArchivedName is set only on jobs loaded from the archive dir: the
	// numbered file base (e.g. "foo-2") that uniquely identifies the
	// archived file even when its Slug collides with an active job's.
	ArchivedName string    `json:"archived_name,omitempty"`
	Body         string    `json:"-"`
	Err          string    `json:"err,omitempty"`
	ModTime      time.Time `json:"-"`
}

type Prereq struct {
	Job   string `json:"job"`
	Gated bool   `json:"gated,omitempty"`
}

// JobPR is one PR shipped from a job's cell branch: url + state
// (open|merged|closed), kraken-owned like state/session/run. The PR number
// clients display is derived from the URL, so nothing else is stored.
type JobPR struct {
	URL   string `json:"url"`
	State string `json:"state"`
}

const JobSuffix = ".job.md"

func PlutoDir() string {
	if d := os.Getenv("KRAKEN_PLUTO_DIR"); d != "" {
		return d // tests point at a temp dir; daily use never sets this
	}
	return filepath.Join(util.Home(), "Documents", "pluto")
}

// started reports whether the job has ever entered a cell: "" means never
// started; "working", "blocked", and "done" all count as started.
func (j *Job) Started() bool {
	return j.State == "working" || j.State == "blocked" || j.State == "done"
}

// stripComment drops an inline "  # ..." comment from a frontmatter value.
// A comment hash must be preceded by whitespace (or start the value) AND
// followed by whitespace or end-of-line — so "fix bug #123" keeps its issue
// reference while "monolith   # substring-matched" loses the comment. Control
// characters are stripped too: single-line values render raw into table lines
// and must not smuggle ANSI escapes or breaks into kraken's own output.
func stripComment(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] != '#' {
			continue
		}
		before := i == 0 || s[i-1] == ' ' || s[i-1] == '\t'
		after := i+1 >= len(s) || s[i+1] == ' ' || s[i+1] == '\t'
		if before && after {
			s = s[:i]
			break
		}
	}
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
}

// isFence matches a frontmatter fence: an UNINDENTED "---" (trailing
// whitespace tolerated). Indentation disqualifies it on purpose — an indented
// "  ---" inside a legacy success: | block scalar is content, not the
// closing fence.
func isFence(s string) bool {
	return strings.TrimRight(s, " \t\r") == "---"
}

// addErr accumulates parse problems; the first one decides the board's
// error note, but all of them ride along for detail views.
func (j *Job) addErr(msg string) {
	if j.Err == "" {
		j.Err = msg
	} else {
		j.Err += "; " + msg
	}
}

// boolField validates a frontmatter boolean strictly: "true", "false", or
// empty (= absent = false). Anything else — "yes", "True", typos — errors the
// job rather than guessing what the author meant.
func (j *Job) boolField(key, val string) bool {
	switch val {
	case "true":
		return true
	case "", "false":
		return false
	default:
		j.addErr(fmt.Sprintf("bad %s value %q", key, val))
		return false
	}
}

// modelTokenRe bounds what a model: value may look like — a plain CLI token.
// It is a shape check, not an allowlist: claude's model list isn't kraken's
// to own, and claude itself errors visibly in the pane on an unknown model.
var modelTokenRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validStates: kraken itself writes working/blocked/done; draft and queued
// are derived for display and never written to a job file, so hand-authoring
// either is a dead end rather than a valid starting point. A hand-mangled
// state must error rather than silently reading as never-started.
var validStates = map[string]bool{
	"working": true, "blocked": true, "done": true,
}

// ParseJob parses one job file's bytes. name is the file's base name, used to
// derive the slug when the frontmatter omits or mangles `job:`.
func ParseJob(name string, data []byte) *Job {
	j := &Job{Slug: strings.TrimSuffix(name, JobSuffix), Type: "build"}
	// CRLF-saved files would otherwise bake \r into values (and straight into
	// the zeus prompt); job files are line-oriented text, so normalize
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 || !isFence(lines[0]) {
		j.Err = "no frontmatter"
		j.Body = string(data)
		return j
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			end = i
			break
		}
	}
	if end < 0 {
		j.Err = "unterminated frontmatter"
		j.Body = string(data)
		return j
	}
	j.Body = strings.Join(lines[end+1:], "\n")

	fm := lines[1:end]
	seen := map[string]bool{}
	for i := 0; i < len(fm); i++ {
		line := fm[i]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			j.addErr(fmt.Sprintf("stray indented line %q", core.TruncateOneLine(trimmed, 24)))
			continue
		}
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			j.addErr(fmt.Sprintf("bad line %q", core.TruncateOneLine(trimmed, 24)))
			continue
		}
		k := strings.TrimSpace(key)
		if seen[k] {
			j.addErr("duplicate key " + k)
			continue
		}
		seen[k] = true
		val := stripComment(rest)
		switch k {
		case "job":
			if val != "" {
				j.Slug = val
			}
		case "type":
			switch val {
			case "build", "info":
				j.Type = val
			case "explore", "question":
				// legacy values: existing pluto job files must keep working
				j.Type = "info"
			case "":
			default:
				j.addErr(fmt.Sprintf("unknown type %q", val))
			}
		case "repo":
			j.Repo = val
		case "base":
			j.Base = val
		case "ready":
			j.Ready = j.boolField(k, val)
		case "solo":
			j.Solo = j.boolField(k, val)
		case "model":
			if val != "" && !modelTokenRe.MatchString(val) {
				j.addErr(fmt.Sprintf("bad model value %q", core.TruncateOneLine(val, 24)))
			} else {
				j.Model = val
			}
		case "skip-doc":
			j.SkipDoc = j.boolField(k, val)
		case "auto-approve":
			// legacy field: removed from the schema, but existing pluto job
			// files still carry auto-approve: keys (always a scalar). Keep
			// consuming the line so those files still parse with Err == "" —
			// just discard the value.
		case "state":
			if val != "" && !validStates[val] {
				j.addErr(fmt.Sprintf("unknown state %q", val))
			} else {
				j.State = val
			}
		case "outcome":
			j.Outcome = val
		case "session":
			j.Session = val
		case "teardown":
			j.Teardown = val
		case "run":
			if val == "" {
				break
			}
			n, err := strconv.Atoi(val)
			if err != nil || n < 1 {
				j.addErr(fmt.Sprintf("bad run value %q", val))
			} else {
				j.Run = n
			}
		case "success":
			// legacy field: removed from the schema, but existing pluto job
			// files still carry success: keys (scalar or "success: |" block
			// scalar). Keep consuming the same lines so those files still
			// parse with Err == "" — just discard the value.
			if val == "|" || val == "" {
				for i+1 < len(fm) && (strings.HasPrefix(fm[i+1], "  ") || strings.TrimSpace(fm[i+1]) == "") {
					i++
				}
			}
		case "prereqs":
			switch val {
			case "[]", "":
				j.Prereqs = parsePrereqs(j, fm, &i)
			default:
				j.addErr(fmt.Sprintf("bad prereqs value %q", val))
			}
		case "prs":
			switch val {
			case "[]", "":
				j.PRs = parsePRs(j, fm, &i)
			default:
				j.addErr(fmt.Sprintf("bad prs value %q", val))
			}
		default:
			j.addErr("unknown key " + k)
		}
	}
	if j.Slug == "" {
		j.Slug = strings.TrimSuffix(name, JobSuffix)
	}
	// cross-field check after the loop — key order in the file is free, so
	// type may not be known yet when skip-doc parses
	if j.SkipDoc && j.Type != "build" {
		j.addErr("skip-doc only applies to build jobs")
	}
	return j
}

// parsePRs consumes indented "- url: x" items (with a "state: open|merged|closed"
// continuation line) following a prs: key at fm[*i]. Strict like parsePrereqs:
// a half-read entry would silently break the tracker's dedup and re-notify, so
// anything not fully understood errors the job.
func parsePRs(j *Job, fm []string, i *int) []JobPR {
	var out []JobPR
	for *i+1 < len(fm) {
		line := fm[*i+1]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			*i++
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "-") {
			return out // next top-level key
		}
		if !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
			*i++
			j.addErr(fmt.Sprintf("bad prs line %q", core.TruncateOneLine(trimmed, 40)))
			continue
		}
		*i++
		item := stripComment(strings.TrimPrefix(trimmed, "-"))
		var p JobPR
		if k, v, ok := strings.Cut(item, ":"); ok && strings.TrimSpace(k) == "url" {
			p.URL = strings.TrimSpace(v)
		} else if item != "" {
			j.addErr(fmt.Sprintf("bad prs item %q", core.TruncateOneLine(item, 40)))
		}
		for *i+1 < len(fm) {
			next := strings.TrimSpace(fm[*i+1])
			indented := len(fm[*i+1]) > 0 && (fm[*i+1][0] == ' ' || fm[*i+1][0] == '\t')
			if !indented || next == "" || strings.HasPrefix(next, "- ") {
				break
			}
			*i++
			if strings.HasPrefix(next, "#") {
				continue
			}
			k, v, ok := strings.Cut(next, ":")
			if !ok {
				j.addErr(fmt.Sprintf("bad prs line %q", core.TruncateOneLine(next, 40)))
				continue
			}
			switch strings.TrimSpace(k) {
			case "url":
				if p.URL == "" {
					p.URL = stripComment(v)
				}
			case "state":
				p.State = stripComment(v)
			default:
				j.addErr("unknown prs key " + strings.TrimSpace(k))
			}
		}
		switch {
		case p.URL == "":
			j.addErr("prs entry missing url")
		case p.State != "open" && p.State != "merged" && p.State != "closed":
			j.addErr(fmt.Sprintf("bad prs state %q for %s", p.State, core.TruncateOneLine(p.URL, 40)))
		default:
			out = append(out, p)
		}
	}
	return out
}

// parsePrereqs consumes indented "- job: x" items (with optional "gated: true"
// continuation lines) following a prereqs: key at fm[*i]. Anything under
// prereqs it can't fully understand errors the job — a half-read prereq could
// silently skip a gate.
func parsePrereqs(j *Job, fm []string, i *int) []Prereq {
	var out []Prereq
	for *i+1 < len(fm) {
		line := fm[*i+1]
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			*i++
			continue
		}
		if line[0] != ' ' && line[0] != '\t' && !strings.HasPrefix(line, "-") {
			return out // next top-level key
		}
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			*i++
			item := stripComment(strings.TrimPrefix(trimmed, "-"))
			var p Prereq
			if k, v, ok := strings.Cut(item, ":"); ok && strings.TrimSpace(k) == "job" {
				p.Job = strings.TrimSpace(v)
			} else if item != "" {
				j.addErr(fmt.Sprintf("bad prereq item %q", core.TruncateOneLine(item, 24)))
			}
			// continuation lines: indented key: value pairs under this item
			// (tab indentation counts — a tab-indented gated: true silently
			// dropped would defeat the human gate)
			for *i+1 < len(fm) {
				next := strings.TrimSpace(fm[*i+1])
				indented := len(fm[*i+1]) > 0 && (fm[*i+1][0] == ' ' || fm[*i+1][0] == '\t')
				if !indented || next == "" || strings.HasPrefix(next, "- ") {
					break
				}
				*i++
				if strings.HasPrefix(next, "#") {
					continue
				}
				k, v, ok := strings.Cut(next, ":")
				if !ok {
					j.addErr(fmt.Sprintf("bad prereq line %q", core.TruncateOneLine(next, 24)))
					continue
				}
				switch strings.TrimSpace(k) {
				case "job":
					if p.Job == "" {
						p.Job = stripComment(v)
					}
				case "gated":
					p.Gated = j.boolField("gated", stripComment(v))
				default:
					j.addErr("unknown prereq key " + strings.TrimSpace(k))
				}
			}
			if p.Job != "" {
				out = append(out, p)
			} else {
				j.addErr("prereq missing job")
			}
			continue
		}
		*i++
		j.addErr(fmt.Sprintf("bad prereq line %q", core.TruncateOneLine(trimmed, 24)))
	}
	return out
}

// JobTemplate is the fill-in skeleton for a new job — kept in lockstep with
// the {{name}} expansion in ~/.config/nvim/lua/config/pluto.lua (the nvim
// authoring path) and the spec doc.
func JobTemplate(slug string) string {
	return "---\n" +
		"job: " + slug + " # slug; this file is " + slug + ".job.md\n" +
		"type: build               # build | info\n" +
		"repo: monolith            # substring-matched against existing repos (alphabetic tiebreak);\n" +
		"                          # no match → job errors, surfaced in the viewer\n" +
		"# base: origin/main       # optional: ref the worktree branches from (default origin/main, else main)\n" +
		"ready: false              # flip to true to queue\n" +
		"prereqs: []               # - job: other-slug\n" +
		"                          #   gated: true   → when it completes, you review its outcome\n" +
		"                          #                   in the TUI and release this job (human gate)\n" +
		"solo: false               # true → zeus works alone: no subagent crew, prose-only doc (no diagram subagents)\n" +
		"# model: sonnet           # optional: claude model for zeus (any --model value, e.g. fable | opus | sonnet | haiku);\n" +
		"                          # applied once at cell start, default = claude's own default\n" +
		"# skip-doc: true          # build only: skip the /doc → /send phase — zeus implements the spec directly\n" +
		"---\n"
}

// LoadJobs reads every *.job.md in the pluto dir, sorted by slug. Missing dir
// or unreadable files degrade to an empty/partial list, never an error.
func LoadJobs() []*Job {
	entries, err := os.ReadDir(PlutoDir())
	if err != nil {
		return nil
	}
	var jobs []*Job
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), JobSuffix) {
			continue
		}
		path := filepath.Join(PlutoDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		j := ParseJob(e.Name(), data)
		j.File = path
		if info, err := e.Info(); err == nil {
			j.ModTime = info.ModTime()
		}
		jobs = append(jobs, j)
	}
	// stable: for duplicate slugs the ReadDir filename order decides which
	// file JobBySlug resolves — surfaced as an Err either way, but stable
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].Slug < jobs[b].Slug })
	markSlugIssues(jobs)
	markPrereqCycles(jobs)
	return jobs
}

// markSlugIssues flags jobs whose slug can't safely name a cell: cell
// identity (worktree path, tmux session, branch, @kraken-job marker) is
// keyed on util.Slugify(Slug), so an empty result would alias the cells root and
// a collision would let one job's teardown kill another's live cell —
// defeating VerifyCellSession entirely.
func markSlugIssues(jobs []*Job) {
	bySlugified := map[string][]*Job{}
	for _, j := range jobs {
		bySlugified[util.Slugify(j.Slug)] = append(bySlugified[util.Slugify(j.Slug)], j)
	}
	for key, group := range bySlugified {
		switch {
		case key == "":
			for _, j := range group {
				if j.Err == "" {
					j.Err = "unusable slug"
				}
			}
		case len(group) > 1:
			// a collision must never retroactively disable an already-running
			// job (erred jobs lose stall-poking and reap): the started member
			// keeps its identity, the newcomers get flagged
			started := 0
			for _, j := range group {
				if j.Started() {
					started++
				}
			}
			for _, j := range group {
				if j.Err == "" && (!j.Started() || started > 1) {
					j.Err = "duplicate slug"
				}
			}
		}
	}
}

// JobBySlug resolves a user-given slug (with or without the .job.md suffix)
// against the loaded jobs.
func JobBySlug(jobs []*Job, slug string) *Job {
	slug = strings.TrimSuffix(slug, JobSuffix)
	for _, j := range jobs {
		if j.Slug == slug {
			return j
		}
	}
	return nil
}

// LoadJobBySlug parses exactly one job file, for a caller (the pr-hook verb)
// that only knows its own slug and runs once per zeus tool call — the full
// LoadJobs() sweep (every file in the dir, plus slug-collision and
// prereq-cycle bookkeeping across the whole board) is wasted work at that
// call rate. Fast path reads "<slug>.job.md" directly; a mismatch (the
// file's own `job:` frontmatter overrides the filename) falls back to a full
// LoadJobs()+JobBySlug scan.
func LoadJobBySlug(slug string) *Job {
	slug = strings.TrimSuffix(slug, JobSuffix)
	path := filepath.Join(PlutoDir(), slug+JobSuffix)
	if data, err := os.ReadFile(path); err == nil {
		j := ParseJob(slug+JobSuffix, data)
		j.File = path
		if info, err := os.Stat(path); err == nil {
			j.ModTime = info.ModTime()
		}
		if j.Slug == slug {
			return j
		}
	}
	return JobBySlug(LoadJobs(), slug)
}

// --- DAG ---

// markPrereqCycles flags every job on a prereq cycle: a cycle can never
// become ready and would otherwise just look permanently "waits on X".
func markPrereqCycles(jobs []*Job) {
	bySlug := map[string]*Job{}
	for _, j := range jobs {
		bySlug[j.Slug] = j
	}
	const (
		white = 0
		gray  = 1
		black = 2
	)
	state := map[string]int{}
	onCycle := map[string]bool{}
	var visit func(slug string, stack []string)
	visit = func(slug string, stack []string) {
		j := bySlug[slug]
		if j == nil || state[slug] == black {
			return
		}
		if state[slug] == gray {
			// mark everyone from the first occurrence of slug in the stack
			for i := len(stack) - 1; i >= 0; i-- {
				onCycle[stack[i]] = true
				if stack[i] == slug {
					break
				}
			}
			return
		}
		state[slug] = gray
		for _, p := range j.Prereqs {
			visit(p.Job, append(stack, slug))
		}
		state[slug] = black
	}
	for _, j := range jobs {
		visit(j.Slug, nil)
	}
	for slug := range onCycle {
		j := bySlug[slug]
		if j.Err == "" {
			j.Err = "prereq cycle"
		}
	}
}

// UnmetPrereqs returns j's prereqs whose job is not done (missing prereq
// files count as unmet — a typo must block, not unblock).
func UnmetPrereqs(j *Job, bySlug map[string]*Job) []Prereq {
	var unmet []Prereq
	for _, p := range j.Prereqs {
		dep := bySlug[p.Job]
		if dep == nil || dep.State != "done" {
			unmet = append(unmet, p)
		}
	}
	return unmet
}

// DisplayState derives the job-board's five-state model: a terminal "done"
// wins even over a parse error (a job that finished successfully before
// picking up an unrelated error still shows done). Otherwise an erred job
// (even one that's Ready) surfaces as "draft" — the board's draft column
// shows the error detail. A kraken-managed State always wins next. Otherwise
// !Ready is "draft", and everything else — unmet prereqs, a gated prereq
// waiting on human release, or a ready job with no cell yet — collapses to
// "queued": ready-but-not-yet-startable is still just queued.
//
// Shared precedence (kept in lockstep with deriveState in status.go): done
// wins first, then an error reads as draft, then any kraken-managed state
// wins, then !Ready reads as draft, else queued.
func DisplayState(j *Job, bySlug map[string]*Job) string {
	if j.State == "done" {
		return "done"
	}
	if j.Err != "" {
		return "draft"
	}
	if j.State != "" {
		return j.State
	}
	if !j.Ready {
		return "draft"
	}
	return "queued"
}

// startable reports whether j is ready for kraken to auto-start into a cell
// right now: no error, ready, never started, every prereq met, and no prereq
// is gated. A gated prereq — even one whose dep is done — parks the job for a
// human to release by hand (k start / the board); startable() must return
// false there so the daemon never auto-starts a gated job, matching the
// pre-five-state gate-review-never-auto-starts rule.
func startable(j *Job, bySlug map[string]*Job) bool {
	if j.Err != "" || !j.Ready || j.State != "" {
		return false
	}
	if len(UnmetPrereqs(j, bySlug)) > 0 {
		return false
	}
	for _, p := range j.Prereqs {
		if p.Gated {
			return false
		}
	}
	return true
}

// --- state write-back ---

// patchField rewrites one kraken-owned scalar frontmatter line (inserting it
// before the closing --- if absent), leaving every other byte of the file
// untouched. An accidentally indented line for the key is healed to the
// canonical unindented form — the parser rejects indented keys, so leaving it
// would keep the job erred. An empty val REMOVES the key's line instead of
// leaving a dangling "key:" (a missing key already parses as empty, so the
// two forms are equivalent — this just keeps hand-read files clean).
func patchField(content, key, val string) (string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || !isFence(lines[0]) {
		return "", fmt.Errorf("no frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			if val == "" {
				return content, nil // clearing an absent key: nothing to do
			}
			lines[i] = key + ": " + val + "\n---"
			return strings.Join(lines, "\n"), nil
		}
		if strings.HasPrefix(strings.TrimSpace(lines[i]), key+":") {
			if val == "" {
				lines = append(lines[:i], lines[i+1:]...)
			} else {
				lines[i] = key + ": " + val
			}
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", fmt.Errorf("unterminated frontmatter")
}

// patchState is patchField for the state key (the common case).
func patchState(content, state string) (string, error) {
	return patchField(content, "state", state)
}

// patchPRs rewrites the frontmatter prs: block wholesale (inserting it before
// the closing fence if absent), leaving every other byte untouched — the
// list-valued sibling of patchField. The old block (key line plus its
// indented/dash continuation lines) is replaced in place so hand-placed key
// ordering elsewhere survives.
func patchPRs(content string, prs []JobPR) (string, error) {
	var block []string
	if len(prs) > 0 {
		block = append(block, "prs:")
		for _, p := range prs {
			block = append(block, "  - url: "+OneLine(p.URL), "    state: "+OneLine(p.State))
		}
	}
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || !isFence(lines[0]) {
		return "", fmt.Errorf("no frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			if len(block) == 0 {
				return content, nil // nothing recorded and nothing to record
			}
			out := append(append([]string{}, lines[:i]...), block...)
			return strings.Join(append(out, lines[i:]...), "\n"), nil
		}
		// column-0 match only: an indented "prs:"-prefixed line can be a
		// legacy success: | block scalar's CONTENT, and healing it like
		// patchField heals indented keys would silently eat that line every
		// tracker tick
		if strings.HasPrefix(lines[i], "prs:") {
			end := i + 1
			for end < len(lines) && !isFence(lines[end]) {
				t := strings.TrimSpace(lines[end])
				if t != "" && lines[end][0] != ' ' && lines[end][0] != '\t' && !strings.HasPrefix(lines[end], "-") {
					break // next top-level key
				}
				end++
			}
			out := append(append([]string{}, lines[:i]...), block...)
			return strings.Join(append(out, lines[end:]...), "\n"), nil
		}
	}
	return "", fmt.Errorf("unterminated frontmatter")
}

// writeJobPRs persists the tracker's PR record atomically, mirroring
// WriteJobFields' re-read-then-patch pattern so a concurrent editor save
// isn't clobbered with stale bytes. Invariant: this write may add or update
// records, but must never DROP a URL already present in the file. The
// caller's `prs` is built from j.PRs loaded at the start of a daemon tick;
// the pr-hook verb's real-time appendJobPR path may have appended a fresh
// record straight to the file in the meantime. Re-parsing the freshly-read
// bytes and unioning in any file-only URL (state as last recorded there)
// before patching closes that race — without it, a PR attached mid-tick
// would be silently overwritten out of existence by the tracker's own write.
func writeJobPRs(j *Job, prs []JobPR) error {
	data, err := os.ReadFile(j.File)
	if err != nil {
		return err
	}
	fresh := ParseJob(filepath.Base(j.File), data)
	have := make(map[string]bool, len(prs))
	for _, p := range prs {
		have[p.URL] = true
	}
	merged := prs
	for _, p := range fresh.PRs {
		if !have[p.URL] {
			merged = append(merged, p)
		}
	}
	content, err := patchPRs(string(data), merged)
	if err != nil {
		return fmt.Errorf("%s: %v", filepath.Base(j.File), err)
	}
	if err := util.AtomicWrite(j.File, content); err != nil {
		return err
	}
	j.PRs = merged
	return nil
}

// appendJobPR is the shared attach path for recording one shipped PR against
// a job: the pr-hook verb's real-time path and the transcript-scanning
// backstop both fold through here. Re-reads the file fresh and parses its
// current prs — not j.PRs, which may be stale — so a concurrent daemon tick
// or nvim save between load and write can't be clobbered, mirroring
// writeJobPRs' re-read-then-patch semantics. No-ops (attached=false) if the
// URL is already recorded; a hook re-run on the same gh pr create output
// must never duplicate an entry.
func appendJobPR(j *Job, pr JobPR) (attached bool, err error) {
	data, err := os.ReadFile(j.File)
	if err != nil {
		return false, err
	}
	fresh := ParseJob(filepath.Base(j.File), data)
	for _, existing := range fresh.PRs {
		if existing.URL == pr.URL {
			return false, nil
		}
	}
	prs := append(fresh.PRs, pr)
	content, err := patchPRs(string(data), prs)
	if err != nil {
		return false, fmt.Errorf("%s: %v", filepath.Base(j.File), err)
	}
	if err := util.AtomicWrite(j.File, content); err != nil {
		return false, err
	}
	j.PRs = prs
	return true, nil
}

// removeJobPRs drops recorded PRs by URL — the repair pass's write path.
// Deliberately NOT writeJobPRs: that helper re-reads the file and unions in
// any file-only URL missing from the caller's list, which exists so a
// concurrent appendJobPR attach survives a tracker write built from a stale
// j.PRs snapshot — but that same union would silently resurrect exactly the
// records this call means to delete. Instead this re-reads the file fresh
// and filters ITS current list by the drop set (mirroring appendJobPR's
// read-fresh-then-patch shape), so a PR attached concurrently between load
// and write still survives — it's simply absent from drop and passes through.
func removeJobPRs(j *Job, drop map[string]bool) (removed []string, err error) {
	data, err := os.ReadFile(j.File)
	if err != nil {
		return nil, err
	}
	fresh := ParseJob(filepath.Base(j.File), data)
	var kept []JobPR
	for _, p := range fresh.PRs {
		if drop[p.URL] {
			removed = append(removed, p.URL)
			continue
		}
		kept = append(kept, p)
	}
	if len(removed) == 0 {
		return nil, nil
	}
	content, err := patchPRs(string(data), kept)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.Base(j.File), err)
	}
	if err := util.AtomicWrite(j.File, content); err != nil {
		return nil, err
	}
	j.PRs = kept
	return removed, nil
}

// WriteJobFields patches kraken-owned fields atomically (temp file + rename
// in the same dir), re-reading the file first so a concurrent nvim save isn't
// clobbered with stale bytes. keyvals alternates key, value.
func WriteJobFields(j *Job, keyvals ...string) error {
	data, err := os.ReadFile(j.File)
	if err != nil {
		return err
	}
	content := string(data)
	for i := 0; i+1 < len(keyvals); i += 2 {
		content, err = patchField(content, keyvals[i], keyvals[i+1])
		if err != nil {
			return fmt.Errorf("%s: %v", filepath.Base(j.File), err)
		}
	}
	if err := util.AtomicWrite(j.File, content); err != nil {
		return err
	}
	for i := 0; i+1 < len(keyvals); i += 2 {
		switch keyvals[i] {
		case "state":
			j.State = keyvals[i+1]
		case "outcome":
			j.Outcome = keyvals[i+1]
		case "session":
			j.Session = keyvals[i+1]
		case "teardown":
			j.Teardown = keyvals[i+1]
		case "run":
			if n, err := strconv.Atoi(keyvals[i+1]); err == nil {
				j.Run = n
			}
		}
	}
	return nil
}

func WriteJobState(j *Job, state string) error {
	return WriteJobFields(j, "state", state)
}

// --- repo resolution ---

func repoRoot() string {
	return filepath.Join(util.Home(), "Developer")
}

// isGitRepo reports whether dir is a git checkout (.git dir or worktree file).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// PickRepo applies the matching rule to candidate dir basenames: an exact
// name match wins outright (repo: kraken must never resolve to
// another-kraken), otherwise the job's repo field must be a substring
// (case-insensitive) of the dir name, ties breaking alphabetically.
func PickRepo(name string, candidates []string) string {
	name = strings.ToLower(name)
	var hits []string
	for _, c := range candidates {
		lc := strings.ToLower(c)
		if lc == name {
			return c
		}
		if strings.Contains(lc, name) {
			hits = append(hits, c)
		}
	}
	if len(hits) == 0 {
		return ""
	}
	sort.Strings(hits)
	return hits[0]
}

// ResolveRepo maps a job's repo field to a git checkout under ~/Developer:
// substring match on the dir name, alphabetic tiebreak, error when nothing
// matches (the job errors — kraken never creates repos).
func ResolveRepo(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("job has no repo")
	}
	entries, err := os.ReadDir(repoRoot())
	if err != nil {
		return "", fmt.Errorf("read %s: %v", repoRoot(), err)
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(repoRoot(), e.Name())
		if isGitRepo(dir) {
			candidates = append(candidates, e.Name())
		}
	}
	hit := PickRepo(name, candidates)
	if hit == "" {
		return "", fmt.Errorf("no repo under %s matches %q", repoRoot(), name)
	}
	return filepath.Join(repoRoot(), hit), nil
}
