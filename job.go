package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
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
	Base        string   `json:"base,omitempty"`
	Ready       bool     `json:"ready"`
	Solo        bool     `json:"solo"`
	AutoApprove bool     `json:"auto_approve"`
	Prereqs     []Prereq `json:"prereqs,omitempty"`
	Success     string   `json:"success,omitempty"`
	// State is kraken-managed ("" = never started); the human-authored spec
	// body below the frontmatter is never rewritten.
	State   string    `json:"state,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	Body    string    `json:"-"`
	Err     string    `json:"err,omitempty"`
	ModTime time.Time `json:"-"`
}

type Prereq struct {
	Job   string `json:"job"`
	Gated bool   `json:"gated,omitempty"`
}

const jobSuffix = ".job.md"

func plutoDir() string {
	if d := os.Getenv("KRAKEN_PLUTO_DIR"); d != "" {
		return d // tests point at a temp dir; daily use never sets this
	}
	return filepath.Join(home(), "Documents", "pluto")
}

// started reports whether the job has ever entered a cell: "" means never
// started; "working", "blocked", and "done" all count as started.
func (j *Job) started() bool {
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
// "  ---" inside a success block scalar is content, not the closing fence.
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

// validStates: kraken itself writes working/blocked/done; draft and queued
// are derived for display and never written to a job file, so hand-authoring
// either is a dead end rather than a valid starting point. A hand-mangled
// state must error rather than silently reading as never-started.
var validStates = map[string]bool{
	"working": true, "blocked": true, "done": true,
}

// parseJob parses one job file's bytes. name is the file's base name, used to
// derive the slug when the frontmatter omits or mangles `job:`.
func parseJob(name string, data []byte) *Job {
	j := &Job{Slug: strings.TrimSuffix(name, jobSuffix), Type: "build"}
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
			j.addErr(fmt.Sprintf("stray indented line %q", truncateOneLine(trimmed, 24)))
			continue
		}
		key, rest, ok := strings.Cut(line, ":")
		if !ok {
			j.addErr(fmt.Sprintf("bad line %q", truncateOneLine(trimmed, 24)))
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
		case "auto-approve":
			j.AutoApprove = j.boolField(k, val)
		case "state":
			if val != "" && !validStates[val] {
				j.addErr(fmt.Sprintf("unknown state %q", val))
			} else {
				j.State = val
			}
		case "outcome":
			j.Outcome = val
		case "success":
			if val == "|" || val == "" {
				var b []string
				for i+1 < len(fm) && (strings.HasPrefix(fm[i+1], "  ") || strings.TrimSpace(fm[i+1]) == "") {
					i++
					b = append(b, strings.TrimPrefix(fm[i], "  "))
				}
				j.Success = strings.TrimSpace(strings.Join(b, "\n"))
			} else {
				j.Success = val
			}
		case "prereqs":
			switch val {
			case "[]", "":
				j.Prereqs = parsePrereqs(j, fm, &i)
			default:
				j.addErr(fmt.Sprintf("bad prereqs value %q", val))
			}
		default:
			j.addErr("unknown key " + k)
		}
	}
	if j.Slug == "" {
		j.Slug = strings.TrimSuffix(name, jobSuffix)
	}
	return j
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
				j.addErr(fmt.Sprintf("bad prereq item %q", truncateOneLine(item, 24)))
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
					j.addErr(fmt.Sprintf("bad prereq line %q", truncateOneLine(next, 24)))
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
		j.addErr(fmt.Sprintf("bad prereq line %q", truncateOneLine(trimmed, 24)))
	}
	return out
}

// jobTemplate is the fill-in skeleton for a new job — kept in lockstep with
// the {{name}} expansion in ~/.config/nvim/lua/config/pluto.lua (the nvim
// authoring path) and the spec doc.
func jobTemplate(slug string) string {
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
		"auto-approve: false       # true → skip the plan-approval gate (default: plan is gated)\n" +
		"success: |\n" +
		"  what \"done\" looks like\n" +
		"---\n\n# Spec\n\n## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n"
}

// loadJobs reads every *.job.md in the pluto dir, sorted by slug. Missing dir
// or unreadable files degrade to an empty/partial list, never an error.
func loadJobs() []*Job {
	entries, err := os.ReadDir(plutoDir())
	if err != nil {
		return nil
	}
	var jobs []*Job
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), jobSuffix) {
			continue
		}
		path := filepath.Join(plutoDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		j := parseJob(e.Name(), data)
		j.File = path
		if info, err := e.Info(); err == nil {
			j.ModTime = info.ModTime()
		}
		jobs = append(jobs, j)
	}
	// stable: for duplicate slugs the ReadDir filename order decides which
	// file jobBySlug resolves — surfaced as an Err either way, but stable
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].Slug < jobs[b].Slug })
	markSlugIssues(jobs)
	markPrereqCycles(jobs)
	return jobs
}

// markSlugIssues flags jobs whose slug can't safely name a cell: cell
// identity (worktree path, tmux session, branch, @kraken-job marker) is
// keyed on slugify(Slug), so an empty result would alias the cells root and
// a collision would let one job's teardown kill another's live cell —
// defeating verifyCellSession entirely.
func markSlugIssues(jobs []*Job) {
	bySlugified := map[string][]*Job{}
	for _, j := range jobs {
		bySlugified[slugify(j.Slug)] = append(bySlugified[slugify(j.Slug)], j)
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
				if j.started() {
					started++
				}
			}
			for _, j := range group {
				if j.Err == "" && (!j.started() || started > 1) {
					j.Err = "duplicate slug"
				}
			}
		}
	}
}

// jobBySlug resolves a user-given slug (with or without the .job.md suffix)
// against the loaded jobs.
func jobBySlug(jobs []*Job, slug string) *Job {
	slug = strings.TrimSuffix(slug, jobSuffix)
	for _, j := range jobs {
		if j.Slug == slug {
			return j
		}
	}
	return nil
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

// unmetPrereqs returns j's prereqs whose job is not done (missing prereq
// files count as unmet — a typo must block, not unblock).
func unmetPrereqs(j *Job, bySlug map[string]*Job) []Prereq {
	var unmet []Prereq
	for _, p := range j.Prereqs {
		dep := bySlug[p.Job]
		if dep == nil || dep.State != "done" {
			unmet = append(unmet, p)
		}
	}
	return unmet
}

// displayState derives the job-board's five-state model: a terminal "done"
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
func displayState(j *Job, bySlug map[string]*Job) string {
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
	if len(unmetPrereqs(j, bySlug)) > 0 {
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
// would keep the job erred.
func patchField(content, key, val string) (string, error) {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 || !isFence(lines[0]) {
		return "", fmt.Errorf("no frontmatter")
	}
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			lines[i] = key + ": " + val + "\n---"
			return strings.Join(lines, "\n"), nil
		}
		if strings.HasPrefix(strings.TrimSpace(lines[i]), key+":") {
			lines[i] = key + ": " + val
			return strings.Join(lines, "\n"), nil
		}
	}
	return "", fmt.Errorf("unterminated frontmatter")
}

// patchState is patchField for the state key (the common case).
func patchState(content, state string) (string, error) {
	return patchField(content, "state", state)
}

// writeJobFields patches kraken-owned fields atomically (temp file + rename
// in the same dir), re-reading the file first so a concurrent nvim save isn't
// clobbered with stale bytes. keyvals alternates key, value.
func writeJobFields(j *Job, keyvals ...string) error {
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
	if err := atomicWrite(j.File, content); err != nil {
		return err
	}
	for i := 0; i+1 < len(keyvals); i += 2 {
		switch keyvals[i] {
		case "state":
			j.State = keyvals[i+1]
		case "outcome":
			j.Outcome = keyvals[i+1]
		}
	}
	return nil
}

func writeJobState(j *Job, state string) error {
	return writeJobFields(j, "state", state)
}

// --- repo resolution ---

func repoRoot() string {
	return filepath.Join(home(), "Developer")
}

// isGitRepo reports whether dir is a git checkout (.git dir or worktree file).
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

// pickRepo applies the matching rule to candidate dir basenames: an exact
// name match wins outright (repo: kraken must never resolve to
// another-kraken), otherwise the job's repo field must be a substring
// (case-insensitive) of the dir name, ties breaking alphabetically.
func pickRepo(name string, candidates []string) string {
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

// resolveRepo maps a job's repo field to a git checkout under ~/Developer:
// substring match on the dir name, alphabetic tiebreak, error when nothing
// matches (the job errors — kraken never creates repos).
func resolveRepo(name string) (string, error) {
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
	hit := pickRepo(name, candidates)
	if hit == "" {
		return "", fmt.Errorf("no repo under %s matches %q", repoRoot(), name)
	}
	return filepath.Join(repoRoot(), hit), nil
}
