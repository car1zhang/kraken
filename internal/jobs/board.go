package jobs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kraken/internal/util"
)

// --- k j ---

// JobStateColor maps the five-state job board model to the shared palette.
func JobStateColor(state string) string {
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
// jobs last. Within a rank, LoadJobs' slug order holds.
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

// PrereqCell compacts a job's prereq list: "a✓ b✗ c⚑" (✓ done, ✗ pending,
// ⚑ pending human gate).
func PrereqCell(j *Job, bySlug map[string]*Job, color bool) string {
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

// SortJobs orders the board by lifecycle rank, keeping slug order within a
// rank (LoadJobs already sorted by slug; the sort is stable).
func SortJobs(jobs []*Job, bySlug map[string]*Job) {
	ranks := map[*Job]int{}
	for _, j := range jobs {
		ranks[j] = stateRank(DisplayState(j, bySlug))
	}
	sort.SliceStable(jobs, func(a, b int) bool { return ranks[jobs[a]] < ranks[jobs[b]] })
}

// RepoCandidates lists git checkouts under ~/Developer once for table
// rendering (ResolveRepo re-reads for the start path, where freshness wins).
func RepoCandidates() []string {
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
