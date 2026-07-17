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

// jobStateColor maps job-board states to the shared palette. Derived states
// ("waits:x", "gated:x") color by their prefix.
func jobStateColor(state string) string {
	switch {
	case state == "executing", state == "planning":
		return green
	case state == "ready", state == "queued":
		return cyan
	case state == "pr-open", state == "testing", state == "plan-review", state == "blocked":
		return yellow
	case state == "failed", state == "error":
		return red
	case state == "gate-review", strings.HasPrefix(state, "waits:"), strings.HasPrefix(state, "gated:"):
		return magenta
	default: // draft, done
		return gray
	}
}

// stateRank orders the job board: live cells first, then actionable, then
// paperwork. Within a rank, loadJobs' slug order holds.
func stateRank(state string) int {
	switch {
	case state == "executing", state == "planning", state == "plan-review",
		state == "blocked", state == "testing", state == "pr-open":
		return 0
	case state == "ready", state == "queued", state == "gate-review":
		return 1
	case strings.HasPrefix(state, "waits:"), strings.HasPrefix(state, "gated:"):
		return 2
	case state == "draft":
		return 3
	case state == "error", state == "failed":
		return 4
	default: // done
		return 5
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
		data, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			die("marshal: %v", err)
		}
		fmt.Println(string(data))
		return
	}
	if len(jobs) == 0 {
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
