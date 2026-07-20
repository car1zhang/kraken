package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// --- k j ---

// jobStateColor maps the five-state job board model to the shared palette.
func jobStateColor(state string) string {
	switch state {
	case "working":
		return green
	case "queued":
		return cyan
	case "blocked":
		return yellow
	default: // draft, done
		return gray
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
		return paint(color, dim, "-")
	}
	var parts []string
	for _, p := range j.Prereqs {
		dep := bySlug[p.Job]
		switch {
		case dep != nil && dep.State == "done":
			parts = append(parts, paint(color, dim, p.Job+"✓"))
		case p.Gated:
			parts = append(parts, paint(color, magenta, p.Job+"⚑"))
		default:
			parts = append(parts, paint(color, yellow, p.Job+"✗"))
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
			die("unknown argument: %s (usage: k j [--json])", a)
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
			die("marshal: %v", err)
		}
		fmt.Println(string(data))
		return
	}
	if len(jobs) == 0 && len(archived) == 0 {
		fmt.Printf("no jobs in %s (create one with {{name}} in a pluto note)\n", plutoDir())
		return
	}
	color := useColor()
	live := liveSessions()
	repos := repoCandidates()
	rows := [][]string{{" #", "STATE", "JOB", "TYPE", "REPO", "PREREQS", "CELL", "ART", "AGE"}}
	for i, h := range rows[0] {
		rows[0][i] = paint(color, cyan, h)
	}
	ordered := append([]*Job(nil), jobs...)
	sortJobs(ordered, bySlug)
	now := time.Now()
	for i, j := range ordered {
		state := displayState(j, bySlug)
		repo := paint(color, dim, "-")
		if j.Repo != "" {
			if hit := pickRepo(j.Repo, repos); hit != "" {
				repo = hit
			} else {
				repo = paint(color, red, j.Repo+"?")
			}
		}
		cell := paint(color, dim, "-")
		if live[cellSession(j)] {
			cell = cellSession(j)
		}
		ageStr := ""
		if !j.ModTime.IsZero() {
			ageStr = age(now.Sub(j.ModTime))
		}
		stateCell := paint(color, jobStateColor(state), "● "+state)
		if j.Err != "" {
			stateCell += paint(color, red, " ("+j.Err+")")
		}
		art := paint(color, dim, "-")
		if n := len(allArtifactPaths(j)); n > 0 {
			art = fmt.Sprintf("%d", n)
		}
		rows = append(rows, []string{
			paint(color, dim, fmt.Sprintf("%2d", i+1)),
			stateCell,
			paint(color, cyan, j.Slug),
			j.Type,
			repo,
			prereqCell(j, bySlug, color),
			cell,
			art,
			paint(color, dim, ageStr),
		})
	}
	// archived jobs ride last: identity is the numbered archive file base
	// (what k unarchive takes), shown alongside the original slug when the
	// archive numbering renamed it
	for i, a := range archived {
		name := a.ArchivedName
		if slugify(a.Slug) != a.ArchivedName {
			name += " (" + a.Slug + ")"
		}
		art := paint(color, dim, "-")
		if n := len(archivedArtifactPaths(a)); n > 0 {
			art = fmt.Sprintf("%d", n)
		}
		ageStr := ""
		if !a.ModTime.IsZero() {
			ageStr = age(now.Sub(a.ModTime))
		}
		rows = append(rows, []string{
			paint(color, dim, fmt.Sprintf("%2d", len(ordered)+i+1)),
			paint(color, gray, "● archived"),
			paint(color, dim, name),
			a.Type,
			a.Repo,
			paint(color, dim, "-"),
			paint(color, dim, "-"),
			art,
			paint(color, dim, ageStr),
		})
	}
	printTable(rows)
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
