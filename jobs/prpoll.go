package jobs

import (
	"encoding/json"
	"os/exec"
	"strings"
)

// prpoll.go — trackPRs' per-URL backstop. listBranchPRs (jobs/cell.go) asks
// gh for every PR against the cell's current branch in one call, which is
// enough for the common case but misses a PR whose head no longer matches:
// a recycled carl/<slug> branch, or one opened by hand against a different
// head. For those, trackPRs asks gh about the recorded URL directly.

// viewPRState asks gh for one PR's current state by URL. ok=false on any
// failure (auth, rate limit, PR deleted, no gh) — like listBranchPRs,
// unknown must never touch recorded history.
func viewPRState(dir, url string) (string, bool) {
	cmd := exec.Command("gh", "pr", "view", url, "--json", "state")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	var rec struct {
		State string `json:"state"`
	}
	if json.Unmarshal(out, &rec) != nil {
		return "", false
	}
	state := strings.ToLower(rec.State)
	if state != "open" && state != "merged" && state != "closed" {
		return "", false
	}
	return state, true
}

// needsPoll reports which recorded PR URLs deserve an individual gh pr view:
// anything not already terminal (merged is terminal — never re-polled) whose
// URL the branch-list query didn't return this tick. Pure, so it's
// table-testable independent of gh.
func needsPoll(recorded, fetched []JobPR) []string {
	inFetched := map[string]bool{}
	for _, f := range fetched {
		inFetched[f.URL] = true
	}
	var urls []string
	for _, r := range recorded {
		if r.State == "merged" || inFetched[r.URL] {
			continue
		}
		urls = append(urls, r.URL)
	}
	return urls
}
