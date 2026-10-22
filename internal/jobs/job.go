package jobs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// --- jobs ---

// A Job is one row in kraken.db's jobs table, whose text is a <slug>.job.md
// document (frontmatter + spec body): the durable record of a unit of work.
// The frontmatter is a deliberately tiny YAML subset (scalars,
// one block scalar, a list of PR maps) parsed by hand — and parsed STRICTLY:
// any construct the parser doesn't fully understand (unknown or duplicate
// keys, bad booleans, stray lines, malformed prs entries) marks the job Err.
// An erred job never runs, and the error is surfaced on the board. Parsing
// never crashes — strict means loud, not fragile.
type Job struct {
	Slug string `json:"slug"`
	Repo string `json:"repo"`
	// Base is the ref the cell's worktree branches from; empty means the
	// default (origin/main, falling back to main).
	Base string `json:"base,omitempty"`
	Solo bool   `json:"solo"`
	// Model is the Claude model zeus runs on (`claude --model <value>`),
	// hand-authored like solo. Consumed exactly once per cell start
	// (StartCell/ReactivateJob) — never re-read mid-run, so a live cell's
	// model can't change out from under it. Empty means core.ZeusModel,
	// kraken's own default for zeus — never "whatever claude's config says".
	// A cell's side threads read this same field (thread.go), so an override
	// set here governs them too; what it does NOT reach is the crew, pinned
	// to opus in zeus' brief. The two differ only in where they land when it
	// is EMPTY: zeus on core.ZeusModel, a thread on core.DefaultModel.
	Model string `json:"model,omitempty"`
	// Color is the job's chosen colour — one of the eight names Claude
	// Code's /color accepts (see color.go's Palette). Same ownership class
	// as Model: hand-authored, kraken never writes it on its own. Empty
	// means NO colour, not "pick one" — every surface renders exactly what
	// it rendered before the field existed.
	Color string `json:"color,omitempty"`
	// Agent is the coding agent zeus runs on: "" (default, = claude),
	// "claude", or "codex". Hand-authored like Model — kraken never writes it
	// on its own. A value other than claude/codex errs the job (ParseJob).
	Agent string `json:"agent,omitempty"`
	// Visibility is who on the tailnet may see or drive this job: private
	// (never leaves this machine), readonly (visible to every node, drivable
	// by none) or public (any node may drive it). Hand-authored like Color;
	// "" from a job text with no visibility: line, which EffectiveVisibility
	// reads as readonly. A job loaded from the store carries its column
	// (schema v34), which is readonly for every row that predates the field.
	Visibility string `json:"visibility,omitempty"`
	// Author is the store row's jobs.author (schema v37) — who wrote the job,
	// carried here only so the board can serve it as webJob.author. It has no
	// frontmatter spelling and is never parsed from or written into job text.
	Author string `json:"-"`
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
	// DoneJob/KillJob after the self-teardown guard but BEFORE the first
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
	// Load is zeus' triage estimate for a blocked job: how much effort
	// unblocking it costs carl against how much claude work that reply buys
	// (store.JobLoad — the store's own type rather than a second copy, since
	// this package already imports store). It has no frontmatter spelling,
	// so a Job built by ParseJob alone reads as the zero value ("never
	// estimated"), and jobs.SetJobLoad refuses without a store rather than
	// pretending otherwise.
	//
	// No omitempty: Go's omitempty has no effect on a struct value, so
	// writing one here would document an omission that never happens. It is
	// moot in practice — the client-visible form is api's own *webJobLoad,
	// and nothing marshals jobs.Job itself.
	Load store.JobLoad `json:"load"`
	// FormerSlugs is every name this job has been renamed away from
	// (store.Job.FormerSlugs, schema v20), empty for a never-renamed job.
	// It is what keeps a renamed job's old names
	// resolving: the cell-dir and worktree probes, the branch family, the
	// transcript project dirs and the tmux session lookup all derive one
	// candidate per entry, because a rename deliberately moves almost none of
	// those (see rename.go).
	//
	// `json:"-"` on purpose: it is provenance for kraken's own resolvers, not
	// a client-visible field, and adding it to the wire would put a second
	// spelling of a job's identity in front of clients that key on the slug.
	FormerSlugs []string `json:"-"`
	// GoalIDs is every goal this job serves (store.Job.GoalIDs, schema v27's
	// job_goals rows), in link position order and empty for none — which is
	// every job until carl labels one: like Load and FormerSlugs it has no
	// frontmatter spelling, and its only writers are store's three link verbs
	// (AddJobGoal/RemoveJobGoal/ClearJobGoals).
	//
	// The head of the slice is the PRIMARY link, which is what lets every
	// surface promise that the single `goal` it still serves is `goals[0]`.
	//
	// `json:"-"` on purpose: the client-visible form is api's own
	// webJobGoal, which carries each goal's TEXT (bare ids would make every
	// card render numbers), so putting them on this struct's wire shape would
	// be a second, less useful spelling of the same links.
	GoalIDs []int64 `json:"-"`
	// AccountSlug, AccountEmail and AccountAt are the cell's per-job account
	// binding (store.Job's three account_* columns, schema v28): which
	// ~/.claude-pool slot this job's claudes bill, who that slot belongs to,
	// and when the binding was stamped (unix millis). All three empty/zero
	// means UNBOUND — the cell runs on the machine's main login with no
	// CLAUDE_SECURESTORAGE_CONFIG_DIR in its spawn command, which is how
	// every cell ran before per-cell binding existed.
	//
	// Like Load and GoalIDs there is no frontmatter spelling, so a Job built
	// by ParseJob alone reads as unbound. Written by
	// bindAccount at spawn and by the limit rescue's rebind, both through
	// store.SetJobAccount.
	//
	// `json:"-"` on the pair the wire does carry differently: api builds its
	// own webJobAccount object, so putting these on this struct's JSON shape
	// would be a second spelling of the same three values.
	AccountSlug  string `json:"-"`
	AccountEmail string `json:"-"`
	AccountAt    int64  `json:"-"`
	// ID and Rev are storage-internal, populated only when this Job was
	// loaded from the store (jobFromStoreRow in jobs/store.go): ID is the
	// store row id, Rev the optimistic-concurrency counter WriteJobFields/
	// WriteJobState pass to store.UpdateJobFields. Both are zero for a Job
	// built by ParseJob alone — never wire fields, never meaningful to a
	// client.
	ID  int64 `json:"-"`
	Rev int64 `json:"-"`
}

// JobPR is one PR shipped from a job's cell branch: url + state
// (open|merged|closed), kraken-owned like state/session/run. The PR number
// clients display is derived from the URL, so nothing else is sent to clients.
//
// LastCommentAt, BumpedSHA, and ConflictState are the daemon's watchPRs
// watermarks (RFC3339 newest-comment-or-review timestamp already bumped for,
// the head OID whose failing checks were already bumped for, and the last-
// known merge-conflict state: "" never fetched, "clean", or "conflicting" —
// bumped for on the clean→conflicting transition) — frontmatter-only,
// kraken-owned exactly like url/state, but deliberately `json:"-"`: they
// must never reach the API wire (see api/contract_test.go's
// TestJobPRWireShape, which pins jobs.JobPR's exact JSON shape to
// url+state).
type JobPR struct {
	URL           string `json:"url"`
	State         string `json:"state"`
	LastCommentAt string `json:"-"`
	BumpedSHA     string `json:"-"`
	ConflictState string `json:"-"`
}

const JobSuffix = ".job.md"

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

// The three visibility words (Job.Visibility).
const (
	VisibilityPrivate  = "private"
	VisibilityReadOnly = "readonly"
	VisibilityPublic   = "public"
)

// ValidVisibility reports whether v is exactly one of the three words —
// case-sensitive, and "" is not one (the absent line is a separate state that
// EffectiveVisibility resolves).
func ValidVisibility(v string) bool {
	return v == VisibilityPrivate || v == VisibilityReadOnly || v == VisibilityPublic
}

// EffectiveVisibility is the level a job is served at: its own value, or
// readonly when it has none.
func (j *Job) EffectiveVisibility() string {
	if j.Visibility != "" {
		return j.Visibility
	}
	return VisibilityReadOnly
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

// validStates: kraken itself writes working/blocked/done; draft is derived
// for display and never written to a job file, so hand-authoring it is a dead
// end rather than a valid starting point. A hand-mangled
// state must error rather than silently reading as never-started.
var validStates = map[string]bool{
	"working": true, "blocked": true, "done": true,
}

// ParseJob parses one job file's bytes. name is the file's base name, used to
// derive the slug when the frontmatter omits or mangles `job:`.
func ParseJob(name string, data []byte) *Job {
	j := &Job{Slug: strings.TrimSuffix(name, JobSuffix)}
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
			// legacy field: removed from the schema (Q14 — job type is gone;
			// the pipeline distinction now lives in /aim vs /info skill
			// choice plus carl's /do steering, never a job field), but
			// older jobs' text still carries type: keys (build, info,
			// and the older explore/question aliases). Keep consuming the
			// line so those jobs still parse with Err == "" — just discard
			// the value, exactly like auto-approve/success below.
		case "repo":
			j.Repo = val
		case "base":
			j.Base = val
		case "ready":
			// retired field: the queued status is gone, and with it the flag
			// that used to hand a draft to the daemon's queue pass — submit
			// starts the cell in the same request now. Every store row's
			// composed frontmatter written before the cutover still carries
			// a ready: line, so the key keeps being CONSUMED here and its
			// value discarded, exactly like type/skip-doc/auto-approve below.
			// Deleting the case would send the default: arm every one of
			// those rows into Err and pin the whole corpus to draft.
		case "solo":
			j.Solo = j.boolField(k, val)
		case "model":
			if val != "" && !modelTokenRe.MatchString(val) {
				j.addErr(fmt.Sprintf("bad model value %q", core.TruncateOneLine(val, 24)))
			} else {
				j.Model = val
			}
		case "color":
			// Mirrors model's validate-or-err shape. This case is
			// load-bearing beyond validation: the default: arm below marks
			// any unknown key Err, and an erred job never runs — so without
			// it every job file carrying color: would pin to draft.
			if val != "" && !ValidColor(val) {
				j.addErr(fmt.Sprintf("bad color value %q", core.TruncateOneLine(val, 24)))
			} else {
				j.Color = val
			}
		case "agent":
			// Which coding agent zeus runs on — claude (default) or codex. It is
			// lowercased so agent: Codex works (model: is a case-sensitive id). Like
			// color, this case is load-bearing beyond validation: the default:
			// arm marks any unknown key Err, so without it every job file
			// carrying agent: would pin to draft.
			a := strings.ToLower(strings.TrimSpace(val))
			if a != "" && a != "claude" && a != "codex" {
				j.addErr("agent must be claude or codex")
			} else {
				j.Agent = a
			}
		case "visibility":
			// The colour arm's shape: a value off the three words errs the
			// job (pinning it to draft) rather than being read as some
			// default, because a typo here decides whether another machine
			// may drive the cell.
			if val != "" && !ValidVisibility(val) {
				j.addErr(fmt.Sprintf("bad visibility value %q", core.TruncateOneLine(val, 24)))
			} else {
				j.Visibility = val
			}
		case "skip-doc":
			// tolerated legacy key: jobs predating the
			// doc-iff-/aim-or-/info rule (Q14) may still carry a skip-doc:
			// line. Keep consuming it, like type/auto-approve/success below,
			// so those jobs still parse with Err == "" — just discard the
			// value, never validate or error on it.
		case "auto-approve":
			// legacy field: removed from the schema, but older jobs' text
			// still carries auto-approve: keys (always a scalar). Keep
			// consuming the line so those jobs still parse with Err == "" —
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
			// legacy field: removed from the schema, but older jobs' text
			// still carries success: keys (scalar or "success: |" block
			// scalar). Keep consuming the same lines so those jobs still
			// parse with Err == "" — just discard the value.
			if val == "|" || val == "" {
				for i+1 < len(fm) && (strings.HasPrefix(fm[i+1], "  ") || strings.TrimSpace(fm[i+1]) == "") {
					i++
				}
			}
		case "prereqs":
			// retired feature (Q1): prereqs existed only to park a ready job
			// until its dependencies finished, and the queue pass that read
			// them is gone. Same accept-and-ignore treatment as ready: above,
			// with one extra step — a prereqs: key can be followed by an
			// INDENTED block ("  - job: x" / "    gated: true", or the
			// template's indented comment lines), and every line of it has to
			// be consumed too or the parser's stray-indented-line arm would
			// err the job the moment the key stopped being parsed.
			for i+1 < len(fm) {
				next := fm[i+1]
				indented := strings.HasPrefix(next, " ") || strings.HasPrefix(next, "\t")
				if !indented && strings.TrimSpace(next) != "" && !strings.HasPrefix(next, "-") {
					break // next top-level key
				}
				i++
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
	return j
}

// parsePRs consumes indented "- url: x" items (with a "state: open|merged|closed"
// continuation line) following a prs: key at fm[*i]. Strict:
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
			case "last-comment-at":
				p.LastCommentAt = stripComment(v)
			case "bumped-sha":
				p.BumpedSHA = stripComment(v)
			case "conflict-state":
				p.ConflictState = stripComment(v)
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

// JobTemplate is the fill-in skeleton for a new job: the board's
// /api/job/create (webJobCreate) both builds the new row from it, parsed,
// and hands it back verbatim as the editor's starting content. The repo:
// value is DefaultRepo(), which is empty unless exactly one checkout sits
// under the repos dir — an empty repo: still parses, and submit refuses it
// with "job has no repo".
func JobTemplate(slug string) string {
	repoLine := "repo: " + DefaultRepo()
	if pad := 26 - len(repoLine); pad > 0 {
		repoLine += strings.Repeat(" ", pad)
	} else {
		repoLine += " "
	}
	return "---\n" +
		"job: " + slug + " # slug\n" +
		repoLine + "# substring-matched against existing repos (alphabetic tiebreak);\n" +
		"                          # no match → job errors, surfaced in the viewer\n" +
		"# base: origin/main       # optional: ref the worktree branches from (default origin/main, else main)\n" +
		"solo: false               # true → zeus works alone: no teammate crew at all, prose-only doc (no diagram agents)\n" +
		"# model: opus             # optional: model for zeus (any claude --model / codex -m value, e.g. fable | opus | sonnet | haiku);\n" +
		"                          # applied once at cell start, default = " + core.ZeusModel + " (codex cells: " + core.CodexZeusModel + ")\n" +
		"# color: purple           # optional: board/pane colour (red | blue | green | yellow | purple | orange | pink | cyan);\n" +
		"                          # unset = no colour\n" +
		"# visibility: readonly   # optional: private (never leaves this machine) | readonly (default) | public (other krakens may drive it)\n" +
		"# agent: codex            # optional: which coding agent zeus runs (claude | codex); default claude\n" +
		"---\n"
}

// LoadJobs returns every active job, sorted by slug, from the store kraken
// serve wires (UseStore). With no store wired — a bare CLI process — it
// returns nil: kraken.db is the only place jobs live.
func LoadJobs() []*Job {
	st := ActiveStore()
	if st == nil {
		return nil
	}
	jobs, err := loadJobsFromStore(st)
	if err != nil {
		// A DB error must not render as a silently-empty board without a
		// trace: a store query failure is a real fault that deserves a loud
		// event, not just a quiet board wipe.
		dlogErr("load jobs from store failed: %v", err)
		return nil // still degrade to empty
	}
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

// LoadJobBySlug loads exactly one job row, for a caller that only knows its
// own slug and runs at a high call rate — the full LoadJobs() sweep (every
// row, plus slug-collision bookkeeping across the whole board) is wasted
// work there. With no store wired it returns nil: kraken.db is the only
// place jobs live.
//
// A slug that matches no active job falls back to former-slug
// resolution, which is what makes a renamed job's OLD name keep working from
// inside its own cell. Everything a live cell knows about itself was written
// at spawn — the brief, the hooks' baked-in slug, zeus' own habit of typing
// its job name — and a rename deliberately rewrites none of it, so the old
// name is the name those callers still use. The CURRENT-slug lookup runs
// first and always wins: a name that is somebody's current slug must resolve
// to that job, never to whoever used to hold it.
func LoadJobBySlug(slug string) *Job {
	slug = strings.TrimSuffix(slug, JobSuffix)
	st := ActiveStore()
	if st == nil {
		return nil
	}
	sr, err := st.GetJobBySlug(slug)
	if err != nil {
		sr, err = st.GetJobByFormerSlug(slug)
	}
	if err != nil {
		return nil
	}
	j, err := loadJobFull(st, sr)
	if err != nil {
		return nil
	}
	return j
}

// DisplayState derives the job-board's four-state model: a terminal "done"
// wins even over a parse error (a job that finished successfully before
// picking up an unrelated error still shows done). Otherwise an erred job
// surfaces as "draft" — the board's draft column shows the error detail. A
// kraken-managed State always wins next. Otherwise the job has never started,
// which is exactly "draft": since submit starts the cell synchronously there
// is no longer any state between "carl hasn't submitted it" and "it is
// running".
//
// Shared precedence (kept in lockstep with deriveState in status.go): done
// wins first, then an error reads as draft, then any kraken-managed state
// wins, else draft.
func DisplayState(j *Job) string {
	if j.State == "done" {
		return "done"
	}
	if j.Err != "" {
		return "draft"
	}
	if j.State != "" {
		return j.State
	}
	return "draft"
}

// --- state write-back ---

// errNoJobStore is what every job write returns when no store is wired:
// kraken.db is the only place a job lives, so there is nothing to write to.
var errNoJobStore = errors.New("no job store wired — jobs live only in kraken.db, written through kraken serve")

// writeJobPRs persists the tracker's PR record (writeJobPRsToStore, which
// never drops a URL already recorded — a PR attached mid-tick survives a
// tracker write built from a stale j.PRs).
func writeJobPRs(j *Job, prs []JobPR) error {
	st := ActiveStore()
	if st == nil {
		return errNoJobStore
	}
	return writeJobPRsToStore(st, j, prs)
}

// appendJobPR is the shared attach path for recording one shipped PR against
// a job: the pr-hook verb's real-time path and the transcript-scanning
// backstop both fold through here. No-ops (attached=false) if the URL is
// already recorded; a hook re-run on the same gh pr create output must never
// duplicate an entry.
func appendJobPR(j *Job, pr JobPR) (attached bool, err error) {
	st := ActiveStore()
	if st == nil {
		return false, errNoJobStore
	}
	return appendJobPRToStore(st, j, pr)
}

// removeJobPRs drops recorded PRs by URL — the repair pass's write path.
// Deliberately NOT writeJobPRs: that one unions in already-recorded URLs,
// which would resurrect exactly the records this call means to delete.
func removeJobPRs(j *Job, drop map[string]bool) (removed []string, err error) {
	st := ActiveStore()
	if st == nil {
		return nil, errNoJobStore
	}
	return removeJobPRsFromStore(st, j, drop)
}

// updatePRWatermark persists watchPRs' per-URL comment/CI/conflict
// watermarks for one recorded PR via store.SetPRWatermark.
func updatePRWatermark(j *Job, url, lastCommentAt, bumpedSHA, conflictState string) error {
	st := ActiveStore()
	if st == nil {
		return errNoJobStore
	}
	return updatePRWatermarkToStore(st, j, url, lastCommentAt, bumpedSHA, conflictState)
}

// WriteJobFields patches kraken-owned fields through store.UpdateJobFields
// (gated on j.ID/j.Rev, refreshing j.Rev on success). keyvals alternates
// key, value.
func WriteJobFields(j *Job, keyvals ...string) error {
	st := ActiveStore()
	if st == nil {
		return errNoJobStore
	}
	return writeJobFieldsToStore(st, j, keyvals...)
}

func WriteJobState(j *Job, state string) error {
	return WriteJobFields(j, "state", state)
}

// --- repo resolution ---

// repoRoot is the directory whose git children are the repos a job may
// target: KRAKEN_REPOS_DIR when it holds an absolute path (cleaned, contents
// not validated here), else ~/Developer. An empty or relative value falls
// back to the default rather than resolving against whatever directory the
// process happens to run in. Read on every call, never cached, so every
// reader (ResolveRepo, RepoCandidates and through it the job editor's picker
// and DefaultRepo, the mm render pass) lists the same root.
func repoRoot() string {
	if v := strings.TrimSpace(os.Getenv("KRAKEN_REPOS_DIR")); v != "" && filepath.IsAbs(v) {
		return filepath.Clean(v)
	}
	return filepath.Join(util.Home(), "Developer")
}

// DefaultRepo is the repo a job gets when nobody named one: the sole git
// checkout under repoRoot() when there is exactly one, else "" (zero or
// several candidates have no honest default, and an alphabetic pick would
// silently aim jobs at the wrong checkout).
func DefaultRepo() string {
	if c := RepoCandidates(); len(c) == 1 {
		return c[0]
	}
	return ""
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

// ResolveRepo maps a job's repo field to a git checkout under repoRoot()
// (~/Developer unless KRAKEN_REPOS_DIR says otherwise): substring match on
// the dir name, alphabetic tiebreak, error when nothing matches (the job
// errors — kraken never creates repos).
func ResolveRepo(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", fmt.Errorf("job has no repo")
	}
	root := repoRoot()
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", fmt.Errorf("read %s: %v", root, err)
	}
	var candidates []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		if isGitRepo(dir) {
			candidates = append(candidates, e.Name())
		}
	}
	hit := PickRepo(name, candidates)
	if hit == "" {
		return "", fmt.Errorf("no repo under %s matches %q", root, name)
	}
	return filepath.Join(root, hit), nil
}
