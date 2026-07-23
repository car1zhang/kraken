package jobs

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"
)

// prpoll.go — trackPRs' per-URL backstop. listBranchPRs (jobs/cell.go) asks
// gh for every PR against the cell's current branch in one call, which is
// enough for the common case but misses a PR whose head no longer matches:
// a recycled carl/<slug> branch, or one opened by hand against a different
// head. For those, trackPRs asks gh about the recorded URL directly.
// viewPRHead also serves daemon.go's PR-repair pass, which needs a
// recorded PR's head branch (not its state) to tell a job's own PRs apart
// from foreign ones. viewPRActivity is watchPRs' (daemon.go) one gh call per
// open PR per pass: state, head SHA, comments, reviews, and CI rollup.

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

// prWatchFetch is one open PR's freshly fetched comment/review/CI state —
// exactly what watchDecision (daemon.go) needs to decide whether to bump.
// State is lowercased open|merged|closed, same normalization as
// viewPRState/listBranchPRs.
type prWatchFetch struct {
	State         string
	HeadSHA       string
	LastCommentAt string   // newest non-viewer comment / any review createdAt/submittedAt (RFC3339), "" if none
	FailingChecks []string // statusCheckRollup entries with a failing conclusion (see failingConclusions), by name
}

// prWatchTimeout bounds viewPRActivity's gh subprocess: watchPRs runs this
// call synchronously inside tick(), so a hung network must not stall the
// whole daemon loop.
const prWatchTimeout = 10 * time.Second

// failingConclusions are the CheckConclusionState values that represent an
// actionable CI failure worth bumping zeus for: FAILURE (the check itself
// failed), TIMED_OUT (ran past its limit), STARTUP_FAILURE (the runner/
// environment never came up), ACTION_REQUIRED (a required workflow is
// waiting on manual approval to even run). Deliberately excluded:
// CANCELLED (aborted, often just superseded by a newer push — not "broken"),
// NEUTRAL (a check's own "not applicable" result), SKIPPED (conditionally
// didn't run) — none of these are a "go fix this" signal for zeus.
var failingConclusions = map[string]bool{
	"FAILURE":         true,
	"TIMED_OUT":       true,
	"STARTUP_FAILURE": true,
	"ACTION_REQUIRED": true,
}

// prActivityComment and prActivityReview are viewPRActivity's raw gh JSON
// shapes for one comment/review, factored out so the newest-timestamp
// decision below (latestActivityAt) is pure and table-testable without gh.
type prActivityComment struct {
	CreatedAt string `json:"createdAt"`
	// ViewerDidAuthor: "viewer" is the gh auth this daemon runs as — the
	// same auth zeus uses to reply via `gh pr comment`. Without filtering
	// these out, zeus' own replies would advance LastCommentAt and re-bump
	// zeus every pass ("address it" → reply → bump → ...), an infinite
	// self-bump loop.
	ViewerDidAuthor bool `json:"viewerDidAuthor"`
}
type prActivityReview struct {
	SubmittedAt string `json:"submittedAt"`
	// Author matters because GitHub DOES allow COMMENT-type reviews by the
	// PR's own author — an inline reply to a review thread lands as exactly
	// that, with a fresh submittedAt. Zeus opens the PR and replies to
	// threads, so author-authored reviews must not advance the watermark or
	// the self-bump loop survives on the inline-reply path. (Approve /
	// request-changes on your own PR is what GitHub forbids, not comments.)
	Author struct {
		Login string `json:"login"`
	} `json:"author"`
}

// latestActivityAt is watchPRs' newest-comment-or-review computation: the
// max createdAt/submittedAt across comments the viewer (this daemon's gh
// auth, same as zeus') did NOT author and reviews the PR's author did NOT
// write (zeus' own inline replies). Pure, so the self-bump-loop fix is
// table-testable independent of gh. An empty prAuthor disables the review
// filter — unknown must fail open (bump) rather than silently drop carl's
// review.
func latestActivityAt(comments []prActivityComment, reviews []prActivityReview, prAuthor string) string {
	var latest string
	for _, c := range comments {
		if c.ViewerDidAuthor {
			continue
		}
		if c.CreatedAt > latest {
			latest = c.CreatedAt
		}
	}
	for _, r := range reviews {
		if prAuthor != "" && r.Author.Login == prAuthor {
			continue
		}
		if r.SubmittedAt > latest {
			latest = r.SubmittedAt
		}
	}
	return latest
}

// viewPRActivity asks gh for one PR's state, head SHA, comments, reviews, and
// CI rollup — the one gh call watchPRs makes per open PR per pass. ok=false
// on any failure (auth, rate limit, no gh, bad JSON, timeout): unknown must
// never touch a recorded watermark, same defensive style as viewPRState/
// viewPRHead above. Bounded by prWatchTimeout so a hung network can't stall
// the daemon's tick loop.
func viewPRActivity(dir, url string) (prWatchFetch, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), prWatchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", url, "--json",
		"state,headRefOid,author,comments,reviews,statusCheckRollup")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return prWatchFetch{}, false
	}
	var rec struct {
		State      string `json:"state"`
		HeadRefOid string `json:"headRefOid"`
		Author     struct {
			Login string `json:"login"`
		} `json:"author"`
		Comments          []prActivityComment `json:"comments"`
		Reviews           []prActivityReview  `json:"reviews"`
		StatusCheckRollup []struct {
			Name       string `json:"name"`
			Context    string `json:"context"`
			Conclusion string `json:"conclusion"`
		} `json:"statusCheckRollup"`
	}
	if json.Unmarshal(out, &rec) != nil {
		return prWatchFetch{}, false
	}
	f := prWatchFetch{
		State:         strings.ToLower(rec.State),
		HeadSHA:       rec.HeadRefOid,
		LastCommentAt: latestActivityAt(rec.Comments, rec.Reviews, rec.Author.Login),
	}
	for _, c := range rec.StatusCheckRollup {
		// gh's statusCheckRollup mixes check-runs (name) and legacy commit
		// statuses (context) under one JSON shape; conclusion is the GraphQL
		// CheckConclusionState enum, upper-case — legacy statuses report
		// through "state", not "conclusion", so they never match here, which
		// is fine: this is a CI-failure signal, and GitHub itself is moving
		// everything to check-runs.
		if failingConclusions[c.Conclusion] {
			name := c.Name
			if name == "" {
				name = c.Context
			}
			if name == "" {
				name = "check"
			}
			f.FailingChecks = append(f.FailingChecks, name)
		}
	}
	return f, true
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
