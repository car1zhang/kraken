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
// viewPRHead also serves daemon.go's PR-repair pass, which needs a
// recorded PR's head branch (not its state) to tell a job's own PRs apart
// from foreign ones.

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

// viewPRHead asks gh for one PR's head branch name by URL — the repair
// pass's evidence for whether a recorded PR actually shipped from one of the
// job's own branches. ok=false on any failure (auth, rate limit, PR deleted,
// no gh, empty head) — same defensive style as viewPRState: unknown must
// never drive a delete.
func viewPRHead(dir, url string) (headRefName string, ok bool) {
	cmd := exec.Command("gh", "pr", "view", url, "--json", "headRefName")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	var rec struct {
		HeadRefName string `json:"headRefName"`
	}
	if json.Unmarshal(out, &rec) != nil || rec.HeadRefName == "" {
		return "", false
	}
	return rec.HeadRefName, true
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
