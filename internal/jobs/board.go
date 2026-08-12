package jobs

import (
	"os"
	"path/filepath"
	"sort"

	"kraken/internal/util"
)

// --- board data ---

// JobStateColor maps the four-state job board model to the shared palette.
func JobStateColor(state string) string {
	switch state {
	case "working":
		return util.Green
	case "blocked":
		return util.Yellow
	default: // draft, done
		return util.Gray
	}
}

// stateRank orders the job board: live cells first (working/blocked need
// attention), then draft paperwork, then done, with archived jobs last.
// Within a rank, LoadJobs' slug order holds.
func stateRank(state string) int {
	switch state {
	case "working", "blocked":
		return 0
	case "draft":
		return 1
	case "done":
		return 2
	default: // archived
		return 3
	}
}

// SortJobs orders the board by lifecycle rank, keeping slug order within a
// rank (LoadJobs already sorted by slug; the sort is stable).
func SortJobs(jobs []*Job) {
	ranks := map[*Job]int{}
	for _, j := range jobs {
		ranks[j] = stateRank(DisplayState(j))
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
