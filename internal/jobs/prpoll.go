package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"
	"time"

	"kraken/internal/core"
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
// execErrDetail is the shared decoration those defensive callers hand their
// failure logs, since ok=false carries no reason of its own.

// execErrDetail names why an exec'd command failed, for failure logs that
// would otherwise say only "failed": the first stderr line when the command
// ran and exited non-zero (gh puts the reason there — auth, rate limit,
// missing repo), else the exec error itself (no binary, a killed process).
// Only ever a log decoration — callers' ok=false handling is unchanged — and
// bounded through core.TruncateOneLine, since stderr is subprocess-controlled
// text headed for the daemon's stdout and the event log: a newline-free
// GraphQL error blob can run to the 32 KiB capture cap, and raw control
// characters would reach a real terminal. Callers own the judgment that the
// command's stderr is loggable at all (gh's never carries a credential; the
// next caller's might).
func execErrDetail(err error) string {
	if err == nil {
		return ""
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if line := strings.TrimSpace(string(ee.Stderr)); line != "" {
			if i := strings.IndexByte(line, '\n'); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			return core.TruncateOneLine(line, 200)
		}
	}
	return core.TruncateOneLine(err.Error(), 200)
}

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

// prWatchFetch is one open PR's freshly fetched comment/review/CI/merge
// state — exactly what watchDecision (daemon.go) needs to decide whether to
// bump. State is lowercased open|merged|closed, same normalization as
// viewPRState/listBranchPRs. Mergeable is gh's mergeable enum lowercased
// (mergeable|conflicting|unknown, "" if absent) — GitHub computes it lazily
// after a push, so unknown is a normal transient state, never a conflict
// signal. BaseRef is the PR's base branch name, used only to word the
// conflict bump message.
type prWatchFetch struct {
	State         string
	HeadSHA       string
	Mergeable     string
	BaseRef       string
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
	// State and Body decide whether the review is anything to ADDRESS. gh
	// serialises State as APPROVED, CHANGES_REQUESTED, COMMENTED or
	// DISMISSED. A BOT's bare approval — an `Auto-approved: …` template
	// body, or an empty body from a `[bot]` login — asks nothing of the
	// author; counted as activity it would bump zeus with an "address it"
	// notice the moment a PR opens, for a turn that finds nothing to
	// address.
	State string `json:"state"`
	Body  string `json:"body"`
}

// reviewNeedsAttention is the one test for whether a review is activity the
// PR's author has to act on. Pure, so the bare-approval skip is
// table-testable. Everything is attention except a bot's bare approval: an
// APPROVED review whose trimmed body starts with the auto-approve template
// `Auto-approved`, or whose body is empty and whose author login carries
// GitHub's `[bot]` suffix. A HUMAN's empty-bodied approval still counts,
// because `gh pr view --json comments` carries no inline review comments
// and the review's timestamp is the only sign an approve-with-nits left
// them; an approval carrying any other text may hold a request, so it
// counts too; and an unknown or empty state fails OPEN — a review gh
// serialises in a shape this code has not seen must bump rather than
// vanish.
func reviewNeedsAttention(r prActivityReview) bool {
	if r.State != "APPROVED" {
		return true
	}
	body := strings.TrimSpace(r.Body)
	if strings.HasPrefix(body, "Auto-approved") {
		return false
	}
	return body != "" || !strings.HasSuffix(r.Author.Login, "[bot]")
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
		if !reviewNeedsAttention(r) {
			continue
		}
		if r.SubmittedAt > latest {
			latest = r.SubmittedAt
		}
	}
	return latest
}

// viewPRActivity asks gh for one PR's state, head SHA, mergeability, base
// branch, comments, reviews, and CI rollup — the one gh call watchPRs makes
// per open PR per pass. ok=false
// on any failure (auth, rate limit, no gh, bad JSON, timeout): unknown must
// never touch a recorded watermark, same defensive style as viewPRState/
// viewPRHead above. detail (non-empty exactly when ok=false) says WHY, for
// the caller's failure log — the defensive posture swallows errors, and a
// bare "fetch failed" line is undiagnosable across an outage. Bounded by
// prWatchTimeout so a hung network can't stall the daemon's tick loop.
func viewPRActivity(dir, url string) (prWatchFetch, string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), prWatchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "pr", "view", url, "--json",
		"state,headRefOid,mergeable,baseRefName,author,comments,reviews,statusCheckRollup")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		// A deadline kill surfaces as the process's own *ExitError
		// ("signal: killed", or whatever stderr it managed first), never as
		// context.DeadlineExceeded — so ask the ctx, or the one failure this
		// timeout exists for gets logged as an unrelated stderr line.
		if ctx.Err() != nil {
			return prWatchFetch{}, "timed out after " + prWatchTimeout.String(), false
		}
		return prWatchFetch{}, execErrDetail(err), false
	}
	var rec struct {
		State       string `json:"state"`
		HeadRefOid  string `json:"headRefOid"`
		Mergeable   string `json:"mergeable"`
		BaseRefName string `json:"baseRefName"`
		Author      struct {
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
	if err := json.Unmarshal(out, &rec); err != nil {
		return prWatchFetch{}, core.TruncateOneLine("unparseable gh output: "+err.Error(), 200), false
	}
	f := prWatchFetch{
		State:         strings.ToLower(rec.State),
		HeadSHA:       rec.HeadRefOid,
		Mergeable:     strings.ToLower(rec.Mergeable),
		BaseRef:       rec.BaseRefName,
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
	return f, "", true
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
