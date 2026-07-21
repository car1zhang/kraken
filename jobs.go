package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kraken/util"
)

// --- k j ---

// jobStateColor maps the five-state job board model to the shared palette.
func jobStateColor(state string) string {
	switch state {
	case "working":
		return util.Green
	case "queued":
		return util.Cyan
	case "blocked":
		return util.Yellow
	default: // draft, done
		return util.Gray
	}
}

// stateRank orders the job board: live cells first (working/blocked need
// attention), then queued, then draft paperwork, then done, with archived
// jobs last. Within a rank, loadJobs' slug order holds.
func stateRank(state string) int {
	switch state {
	case "working", "blocked":
		return 0
	case "queued":
		return 1
	case "draft":
		return 2
	case "done":
		return 3
	default: // archived
		return 4
	}
}

// prereqCell compacts a job's prereq list: "a✓ b✗ c⚑" (✓ done, ✗ pending,
// ⚑ pending human gate).
func prereqCell(j *Job, bySlug map[string]*Job, color bool) string {
	if len(j.Prereqs) == 0 {
		return util.Paint(color, util.Dim, "-")
	}
	var parts []string
	for _, p := range j.Prereqs {
		dep := bySlug[p.Job]
		switch {
		case dep != nil && dep.State == "done":
			parts = append(parts, util.Paint(color, util.Dim, p.Job+"✓"))
		case p.Gated:
			parts = append(parts, util.Paint(color, util.Magenta, p.Job+"⚑"))
		default:
			parts = append(parts, util.Paint(color, util.Yellow, p.Job+"✗"))
		}
	}
	return strings.Join(parts, " ")
}

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
	jobs := loadJobs()
	archived := loadArchivedJobs()
	bySlug := map[string]*Job{}
	for _, j := range jobs {
		bySlug[j.Slug] = j
	}
	if jsonOut {
		type jobOut struct {
			*Job
			DisplayState string `json:"display_state"`
			Cell         string `json:"cell,omitempty"`
		}
		out := []jobOut{}
		live := liveSessions()
		for _, j := range jobs {
			cell := ""
			if live[cellSession(j)] {
				cell = cellSession(j)
			}
			out = append(out, jobOut{Job: j, DisplayState: displayState(j, bySlug), Cell: cell})
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
	if len(jobs) == 0 && len(archived) == 0 {
		fmt.Printf("no jobs in %s (create one with {{name}} in a pluto note)\n", plutoDir())
		return
	}
	color := util.UseColor()
	live := liveSessions()
	repos := repoCandidates()
	rows := [][]string{{" #", "STATE", "JOB", "TYPE", "REPO", "PREREQS", "CELL", "ART", "AGE"}}
	for i, h := range rows[0] {
		rows[0][i] = util.Paint(color, util.Cyan, h)
	}
	ordered := append([]*Job(nil), jobs...)
	sortJobs(ordered, bySlug)
	now := time.Now()
	for i, j := range ordered {
		state := displayState(j, bySlug)
		repo := util.Paint(color, util.Dim, "-")
		if j.Repo != "" {
			if hit := pickRepo(j.Repo, repos); hit != "" {
				repo = hit
			} else {
				repo = util.Paint(color, util.Red, j.Repo+"?")
			}
		}
		cell := util.Paint(color, util.Dim, "-")
		if live[cellSession(j)] {
			cell = cellSession(j)
		}
		ageStr := ""
		if !j.ModTime.IsZero() {
			ageStr = util.Age(now.Sub(j.ModTime))
		}
		stateCell := util.Paint(color, jobStateColor(state), "● "+state)
		if j.Err != "" {
			stateCell += util.Paint(color, util.Red, " ("+j.Err+")")
		}
		art := util.Paint(color, util.Dim, "-")
		if n := len(allArtifactPaths(j)); n > 0 {
			art = fmt.Sprintf("%d", n)
		}
		rows = append(rows, []string{
			util.Paint(color, util.Dim, fmt.Sprintf("%2d", i+1)),
			stateCell,
			util.Paint(color, util.Cyan, j.Slug),
			j.Type,
			repo,
			prereqCell(j, bySlug, color),
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
		if n := len(archivedArtifactPaths(a)); n > 0 {
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

// sortJobs orders the board by lifecycle rank, keeping slug order within a
// rank (loadJobs already sorted by slug; the sort is stable).
func sortJobs(jobs []*Job, bySlug map[string]*Job) {
	ranks := map[*Job]int{}
	for _, j := range jobs {
		ranks[j] = stateRank(displayState(j, bySlug))
	}
	sort.SliceStable(jobs, func(a, b int) bool { return ranks[jobs[a]] < ranks[jobs[b]] })
}

// repoCandidates lists git checkouts under ~/Developer once for table
// rendering (resolveRepo re-reads for the start path, where freshness wins).
func repoCandidates() []string {
	entries, err := os.ReadDir(repoRoot())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() && isGitRepo(filepath.Join(repoRoot(), e.Name())) {
			out = append(out, e.Name())
		}
	}
	return out
}
