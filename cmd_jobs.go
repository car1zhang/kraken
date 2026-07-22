package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"kraken/jobs"
	"kraken/util"
)

// --- pr-hook ---

// cmdPrHook is the thin dispatch wrapper for `kraken pr-hook <slug>` — a
// Claude Code PostToolUse hook (jobs.PrHook does the work), not a
// user-facing verb, so it's kept out of usage() like _complete. A malformed
// invocation (no slug) is silently ignored rather than util.Die'd: this
// runs as a hook, and must never fail zeus' tool call.
func cmdPrHook(args []string) {
	if len(args) != 1 {
		return
	}
	jobs.PrHook(args[0])
}

// --- k start ---

func cmdStart(args []string) {
	if len(args) != 1 {
		util.Die("usage: k start <job-slug>")
	}
	js := jobs.LoadJobs()
	j := jobs.JobBySlug(js, args[0])
	if j == nil {
		util.Die("no job %q in %s (k j lists them)", args[0], jobs.PlutoDir())
	}
	if j.Err != "" {
		util.Die("job %s is unparseable: %s", j.Slug, j.Err)
	}
	if !j.Ready {
		util.Die("job %s is not ready (flip ready: true in %s)", j.Slug, j.File)
	}
	if j.Started() {
		util.Die("job %s already started (state: %s)", j.Slug, j.State)
	}
	bySlug := map[string]*jobs.Job{}
	for _, dep := range js {
		bySlug[dep.Slug] = dep
	}
	if unmet := jobs.UnmetPrereqs(j, bySlug); len(unmet) > 0 {
		var names []string
		for _, p := range unmet {
			names = append(names, p.Job)
		}
		util.Die("job %s has unmet prereqs: %s", j.Slug, strings.Join(names, ", "))
	}
	// starting a gate-parked job is the human release: show what's being
	// signed off so the outcome review is explicit, not accidental
	for _, p := range j.Prereqs {
		if p.Gated {
			outcome := "(no outcome recorded)"
			if dep := bySlug[p.Job]; dep != nil && dep.Outcome != "" {
				outcome = dep.Outcome
			}
			fmt.Printf("releasing gate on %s — outcome: %s\n", p.Job, outcome)
		}
	}
	repo, err := jobs.ResolveRepo(j.Repo)
	if err != nil {
		util.Die("job %s: %v", j.Slug, err)
	}
	if err := jobs.StartCell(j, repo); err != nil {
		util.Die("%v", err)
	}
	util.LogEvent("cli", "info", "start", "k start "+j.Slug+" — cell "+jobs.CellSession(j))
	fmt.Printf("started %s: worktree %s (branch %s), zeus in tmux session %s, state: working\n",
		j.Slug, jobs.CellWorktree(j, repo), jobs.CellBranch(j), jobs.CellSession(j))
	fmt.Printf("attach: tmux attach -t %s  (or k o %s)\n", jobs.CellSession(j), jobs.CellSession(j))
}

// --- k done ---

// cmdDone marks a started job done by hand: the acceptance path for every
// job type — info jobs have no PR at all, and build jobs' merges are only
// tracked (prs: chips), never auto-completed.
func cmdDone(args []string) {
	if len(args) < 1 {
		util.Die("usage: k done <job-slug> [outcome...]")
	}
	js := jobs.LoadJobs()
	j := jobs.JobBySlug(js, args[0])
	if j == nil {
		util.Die("no job %q in %s (k j lists them)", args[0], jobs.PlutoDir())
	}
	if j.Err != "" {
		util.Die("job %s is unparseable (%s) — fix the frontmatter first", j.Slug, j.Err)
	}
	if !j.Started() {
		util.Die("job %s hasn't started — nothing to complete", j.Slug)
	}
	if j.State == "done" {
		util.Die("job %s is already done", j.Slug)
	}
	msgs, err := jobs.DoneJob(j, jobs.OneLine(strings.Join(args[1:], " ")))
	for _, m := range msgs {
		fmt.Println(m)
	}
	if err != nil {
		util.Die("%v", err)
	}
	util.LogEvent("cli", "info", "done", "k done "+j.Slug)
}

// --- k reactivate ---

func cmdReactivate(args []string) {
	session := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--session" {
			i++
			if i >= len(args) {
				util.Die("--session needs a session uuid")
			}
			session = args[i]
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) < 1 {
		util.Die("usage: k reactivate <job-slug> [--session <uuid>] [message...]")
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), rest[0])
	if j == nil {
		util.Die("no job %q in %s (k j lists them)", rest[0], jobs.PlutoDir())
	}
	if err := jobs.ReactivateJob(j, session, jobs.OneLine(strings.Join(rest[1:], " "))); err != nil {
		util.Die("%v", err)
	}
	util.LogEvent("cli", "info", "reactivate", "k reactivate "+j.Slug)
	fmt.Printf("reactivated %s: zeus session resumed in tmux session %s (branch %s), state: working\n",
		j.Slug, jobs.CellSession(j), jobs.CellBranch(j))
	fmt.Printf("attach: tmux attach -t %s  (or k o %s)\n", jobs.CellSession(j), jobs.CellSession(j))
}

// --- k migrate ---

// cmdMigrate requeues jobs left behind by the pre-five-state model: their
// frontmatter state (e.g. "planning", "executing", "pr-open", "merged") no
// longer parses under validStates, so LoadJobs marks them Err "unknown state
// ..." and they sit inert — invisible to the daemon, unstartable, un-reapable.
// OldJobState recovers the actual old value (the parser only left it in the
// error string) and cmdMigrate branches on it:
//   - merged/abandoned/failed (the old model's terminal states): any leftover
//     cell is torn down and the job is stamped done — never re-run, ready
//     untouched.
//   - planning/plan-review/executing/testing/pr-open (still in flight):
//     artifacts preserved, cell torn down, state cleared to "" — ready
//     untouched, so the job's own ready flag decides whether/when it requeues.
//   - queued: state cleared to "" only; no ready write.
//   - anything else unrecognized: skipped untouched, printed loudly, since a
//     value this function doesn't know isn't safe to guess at.
func cmdMigrate(args []string) {
	if len(args) != 0 {
		util.Die("usage: k migrate")
	}
	js := jobs.LoadJobs()
	live := jobs.LiveSessions()
	migrated := 0
	for _, j := range js {
		if !strings.Contains(j.Err, "unknown state") {
			continue
		}
		old, ok := jobs.OldJobState(j)
		if !ok {
			fmt.Printf("%-24s skipped: could not read the old state: line from %s\n", j.Slug, j.File)
			continue
		}
		switch old {
		case "merged":
			if err := jobs.MigrateTerminal(j, live, "PR merged"); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: was merged, marked done\n", j.Slug)
			migrated++
		case "abandoned", "failed":
			if err := jobs.MigrateTerminal(j, live, "migrated: was "+old); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: was %s, marked done\n", j.Slug, old)
			migrated++
		case "planning", "plan-review", "executing", "testing", "pr-open":
			if err := jobs.MigrateInFlight(j, live); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: old cell torn down, state cleared — requeues via its own ready flag\n", j.Slug)
			migrated++
		case "queued":
			if err := jobs.MigrateQueued(j, live); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: state cleared\n", j.Slug)
			migrated++
		default:
			fmt.Printf("%-24s skipped: unrecognized old state %q\n", j.Slug, old)
		}
	}
	if migrated == 0 {
		fmt.Println("nothing to migrate")
	}
}

// --- k archive / k unarchive ---

func cmdArchive(args []string) {
	if len(args) != 1 {
		util.Die("usage: k archive <job-slug>")
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), args[0])
	if j == nil {
		util.Die("no job %q in %s (k j lists them)", args[0], jobs.PlutoDir())
	}
	name, err := jobs.ArchiveJob(j, jobs.LiveSessions())
	if err != nil {
		util.Die("%v", err)
	}
	util.LogEvent("cli", "info", "archive", "k archive "+j.Slug+" → "+name)
	fmt.Printf("archived %s → %s (k unarchive %s restores it)\n", j.Slug, filepath.Join(jobs.ArchiveDir(), name+jobs.JobSuffix), name)
}

func cmdUnarchive(args []string) {
	if len(args) != 1 {
		util.Die("usage: k unarchive <archived-name>   (k j lists archived jobs last)")
	}
	a := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), args[0])
	if a == nil {
		util.Die("no archived job %q in %s", args[0], jobs.ArchiveDir())
	}
	if err := jobs.UnarchiveJob(a); err != nil {
		util.Die("%v", err)
	}
	util.LogEvent("cli", "info", "unarchive", "k unarchive "+a.ArchivedName)
	fmt.Printf("unarchived %s → %s (state %q preserved)\n", a.ArchivedName, filepath.Join(jobs.PlutoDir(), util.Slugify(a.Slug)+jobs.JobSuffix), a.State)
}

// --- k logevent ---

// cmdLogEvent appends one entry to the unified event log — plumbing for
// non-Go writers (the Makefile's install target logs through this), not a
// user-facing command, so it stays out of k help.
func cmdLogEvent(args []string) {
	if len(args) < 3 {
		util.Die("usage: k logevent <src> <event> <msg...>")
	}
	util.LogEvent(args[0], "info", args[1], strings.Join(args[2:], " "))
}

// --- k j ---

func cmdJobs(args []string) {
	jsonOut := false
	for _, a := range args {
		switch a {
		case "--json":
			jsonOut = true
		default:
			util.Die("unknown argument: %s (usage: k j [--json])", a)
		}
	}
	js := jobs.LoadJobs()
	archived := jobs.LoadArchivedJobs()
	bySlug := map[string]*jobs.Job{}
	for _, j := range js {
		bySlug[j.Slug] = j
	}
	if jsonOut {
		type jobOut struct {
			*jobs.Job
			DisplayState string `json:"display_state"`
			Cell         string `json:"cell,omitempty"`
		}
		out := []jobOut{}
		live := jobs.LiveSessions()
		for _, j := range js {
			cell := ""
			if live[jobs.CellSession(j)] {
				cell = jobs.CellSession(j)
			}
			out = append(out, jobOut{Job: j, DisplayState: jobs.DisplayState(j, bySlug), Cell: cell})
		}
		for _, a := range archived {
			out = append(out, jobOut{Job: a, DisplayState: "archived"})
		}
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			util.Die("marshal: %v", err)
		}
		fmt.Println(string(data))
		return
	}
	if len(js) == 0 && len(archived) == 0 {
		fmt.Printf("no jobs in %s (create one with {{name}} in a pluto note)\n", jobs.PlutoDir())
		return
	}
	color := util.UseColor()
	live := jobs.LiveSessions()
	repos := jobs.RepoCandidates()
	rows := [][]string{{" #", "STATE", "JOB", "TYPE", "REPO", "PREREQS", "CELL", "ART", "AGE"}}
	for i, h := range rows[0] {
		rows[0][i] = util.Paint(color, util.Cyan, h)
	}
	ordered := append([]*jobs.Job(nil), js...)
	jobs.SortJobs(ordered, bySlug)
	now := time.Now()
	for i, j := range ordered {
		state := jobs.DisplayState(j, bySlug)
		repo := util.Paint(color, util.Dim, "-")
		if j.Repo != "" {
			if hit := jobs.PickRepo(j.Repo, repos); hit != "" {
				repo = hit
			} else {
				repo = util.Paint(color, util.Red, j.Repo+"?")
			}
		}
		cell := util.Paint(color, util.Dim, "-")
		if live[jobs.CellSession(j)] {
			cell = jobs.CellSession(j)
		}
		ageStr := ""
		if !j.ModTime.IsZero() {
			ageStr = util.Age(now.Sub(j.ModTime))
		}
		stateCell := util.Paint(color, jobs.JobStateColor(state), "● "+state)
		if j.Err != "" {
			stateCell += util.Paint(color, util.Red, " ("+j.Err+")")
		}
		art := util.Paint(color, util.Dim, "-")
		if n := len(jobs.AllArtifactPaths(j)); n > 0 {
			art = fmt.Sprintf("%d", n)
		}
		rows = append(rows, []string{
			util.Paint(color, util.Dim, fmt.Sprintf("%2d", i+1)),
			stateCell,
			util.Paint(color, util.Cyan, j.Slug),
			j.Type,
			repo,
			jobs.PrereqCell(j, bySlug, color),
			cell,
			art,
			util.Paint(color, util.Dim, ageStr),
		})
	}
	// archived jobs ride last: identity is the numbered archive file base
	// (what k unarchive takes), shown alongside the original slug when the
	// archive numbering renamed it
	for i, a := range archived {
		name := a.ArchivedName
		if util.Slugify(a.Slug) != a.ArchivedName {
			name += " (" + a.Slug + ")"
		}
		art := util.Paint(color, util.Dim, "-")
		if n := len(jobs.ArchivedArtifactPaths(a)); n > 0 {
			art = fmt.Sprintf("%d", n)
		}
		ageStr := ""
		if !a.ModTime.IsZero() {
			ageStr = util.Age(now.Sub(a.ModTime))
		}
		rows = append(rows, []string{
			util.Paint(color, util.Dim, fmt.Sprintf("%2d", len(ordered)+i+1)),
			util.Paint(color, util.Gray, "● archived"),
			util.Paint(color, util.Dim, name),
			a.Type,
			a.Repo,
			util.Paint(color, util.Dim, "-"),
			util.Paint(color, util.Dim, "-"),
			art,
			util.Paint(color, util.Dim, ageStr),
		})
	}
	util.PrintTable(rows)
}

// --- k daemon ---

func cmdDaemon(args []string) {
	if len(args) != 0 {
		util.Die("usage: k daemon")
	}
	jobs.RunDaemon()
}
