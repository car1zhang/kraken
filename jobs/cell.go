package jobs

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"kraken/core"
	"kraken/util"
)

// --- cells ---

// A cell is a job's runtime: a dedicated git worktree plus a dedicated tmux
// session holding the job's agents. Phase 2 cells are manual (k start / k
// reap); the daemon takes over creation and teardown in phase 3.

func cellsRoot() string {
	return filepath.Join(util.Home(), ".kraken", "cells")
}

func cellDir(j *Job) string {
	return filepath.Join(cellsRoot(), util.Slugify(j.Slug))
}

// CellWorktree names the worktree wt-<repo>: the dir basename is what shows
// up as the PROJECT label and editor root inside the cell, so it must read as
// the repo, not as "wt".
func CellWorktree(j *Job, repo string) string {
	return filepath.Join(cellDir(j), "wt-"+filepath.Base(repo))
}

// baseRef resolves the ref a cell's worktree branches from: the job's base:
// field when set, otherwise origin/main, falling back to main for repos with
// no origin. A ref on origin gets a best-effort fetch first so "based on
// origin main" means the remote's main, not whenever it was last fetched.
func baseRef(j *Job, repo string) (string, error) {
	base := j.Base
	if base == "" {
		if refExists(repo, "origin/main") {
			base = "origin/main"
		} else if refExists(repo, "main") {
			base = "main"
		} else {
			return "", fmt.Errorf("neither origin/main nor main exists in %s — set base: in the job spec", repo)
		}
	}
	if branch, ok := strings.CutPrefix(base, "origin/"); ok {
		if out, err := exec.Command("git", "-C", repo, "fetch", "origin", branch).CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "kraken: fetch origin %s failed (%s) — using the last-fetched %s\n",
				branch, strings.TrimSpace(string(out)), base)
		}
	}
	if !refExists(repo, base) {
		return "", fmt.Errorf("base ref %q not found in %s", base, repo)
	}
	return base, nil
}

func refExists(repo, ref string) bool {
	return exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", ref).Run() == nil
}

// ExistingWorktree finds the cell's worktree on disk, tolerating the legacy
// bare "wt" name from cells created before worktrees were named wt-<repo>.
// Empty means no worktree exists.
func ExistingWorktree(j *Job, repo string) string {
	wt := CellWorktree(j, repo)
	if _, err := os.Stat(wt); err == nil {
		return wt
	}
	legacy := filepath.Join(cellDir(j), "wt")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return ""
}

func CellSession(j *Job) string {
	return "cell-" + util.Slugify(j.Slug)
}

func CellArtifacts(j *Job) string {
	return filepath.Join(cellDir(j), "artifacts")
}

// AllArtifactPaths lists a job's artifacts from both homes: the live cell's
// artifacts dir and the preserved <slug>.<name> copies in pluto (which are
// all that remain after teardown).
func AllArtifactPaths(j *Job) []string {
	out := ListArtifacts(j)
	prefix := util.Slugify(j.Slug) + "."
	entries, err := os.ReadDir(PlutoDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || strings.HasSuffix(e.Name(), JobSuffix) {
			continue
		}
		out = append(out, filepath.Join(PlutoDir(), e.Name()))
	}
	return out
}

// branchForRun names the branch for a given cell run: the bare slug branch
// normally, -rN only for fallback runs (an existing branch with unmerged
// commits that a forced reuse would orphan — see ensureUsableBranch).
func branchForRun(slug string, run int) string {
	if run >= 2 {
		return fmt.Sprintf("carl/%s-r%d", util.Slugify(slug), run)
	}
	return "carl/" + util.Slugify(slug)
}

func CellBranch(j *Job) string {
	return branchForRun(j.Slug, j.Run)
}

// ensureUsableBranch makes CellBranch(j) safe to (re)create the worktree on
// and returns the git worktree-add branch flag to use: "-b" when the branch
// doesn't exist, "-B" (reset) when it exists but is fully merged into base —
// its commits are already in the base, so the name recycles (many PRs share
// one head-branch name over a job's life; the prs: record, not the branch,
// is the history). Only a branch holding unmerged commits advances run to
// the next -rN name, persisting it so every later CellBranch reader (the PR
// tracker, the zeus brief, the board) agrees.
func ensureUsableBranch(j *Job, repo, base string) (string, error) {
	for {
		br := CellBranch(j)
		if !refExists(repo, br) {
			return "-b", nil
		}
		if exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", br, base).Run() == nil {
			return "-B", nil
		}
		run := j.Run
		if run < 1 {
			run = 1
		}
		if err := WriteJobFields(j, "run", strconv.Itoa(run+1)); err != nil {
			return "", err
		}
	}
}

// cellPalette: tmux 256-color indices picked for contrast against a dark
// status bar.
var cellPalette = []int{33, 39, 45, 77, 113, 141, 171, 208, 214, 203, 81, 155}

// jobColor picks a stable tmux colour for a job so its cell's window is
// recognizable at a glance across restarts.
func jobColor(slug string) int {
	h := fnv.New32a()
	h.Write([]byte(slug))
	return cellPalette[h.Sum32()%uint32(len(cellPalette))]
}

// liveSessionTimes snapshots current tmux session names along with each
// one's creation time (tmux's session_created, unix seconds); no tmux server
// reads as no sessions. One tmux call covers both liveness and freshness —
// callers that only need liveness use the LiveSessions() wrapper below.
func liveSessionTimes() map[string]time.Time {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name} #{session_created}").Output()
	if err != nil {
		return map[string]time.Time{}
	}
	live := map[string]time.Time{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		name, ts, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		secs, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			continue
		}
		live[name] = time.Unix(secs, 0)
	}
	return live
}

// LiveSessions snapshots current tmux session names; a thin bool-only view
// over liveSessionTimes for the many callers that just need liveness.
func LiveSessions() map[string]bool {
	live := map[string]bool{}
	for name := range liveSessionTimes() {
		live[name] = true
	}
	return live
}

// cellClaudeArgs mirrors carl's `cc` alias (claude --dangerously-skip-permissions
// --rc): cell agents work unattended in their own worktree, so they run with
// bypass permissions from birth. tmux commands never see shell aliases, so the
// flags are spelled out here. A non-empty model (the job's model: field) rides
// along as --model — both spawn paths (StartCell, ReactivateJob) share this,
// so a job's model choice survives reactivation.
func cellClaudeArgs(model string) []string {
	args := []string{core.ClaudeLauncher(), "--dangerously-skip-permissions", "--rc"}
	if model != "" {
		args = append(args, "--model", model)
	}
	return args
}

// jobPrompt is the spec portion of the job body — everything up to the
// kraken-owned "## Mid-run log" section, which is bookkeeping for the file,
// not part of zeus' initial prompt.
func jobPrompt(j *Job) string {
	body := j.Body
	if i := strings.Index(body, "\n## Mid-run log"); i >= 0 {
		body = body[:i]
	} else if strings.HasPrefix(body, "## Mid-run log") {
		body = ""
	}
	return strings.TrimSpace(body)
}

// StartCell creates a job's cell: worktree off the base ref, tmux session,
// zeus pane, identity marker, state write. Callers have validated the job
// (parseable, ready, unstarted, prereqs met) — this owns mechanics only, and
// is shared by k start and the daemon.
func StartCell(j *Job, repo string) error {
	sess := CellSession(j)
	if LiveSessions()[sess] {
		return fmt.Errorf("cell %s is already live (k o %s to jump in)", sess, sess)
	}
	if old := ExistingWorktree(j, repo); old != "" {
		return fmt.Errorf("worktree %s already exists — clean it up first (git -C %s worktree remove %s)", old, repo, old)
	}
	wt := CellWorktree(j, repo)
	base, err := baseRef(j, repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	// a leftover branch from a killed or done earlier run: reuse it (reset
	// off base) when fully merged, else fall back to a fresh -rN name
	branchFlag, err := ensureUsableBranch(j, repo, base)
	if err != nil {
		return fmt.Errorf("job %s: pick branch: %v", j.Slug, err)
	}
	for _, d := range []string{cellDir(j), CellArtifacts(j)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %v", d, err)
		}
	}
	out, err := exec.Command("git", "-C", repo, "worktree", "add", wt, branchFlag, CellBranch(j), base).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
	}
	// gate passed: from here on, failures must report the worktree so a
	// half-built cell is cleanable by hand
	argv := append(cellClaudeArgs(j.Model), "--name", ZeusName(j),
		"--append-system-prompt", zeusBrief(j))
	if prompt := jobPrompt(j); prompt != "" {
		argv = append(argv, "--", prompt)
	}
	if err := exec.Command("tmux", "new-session", "-d", "-s", sess, "-c", wt,
		"-n", util.Slugify(j.Slug), core.ShellCmdString(argv)).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %v (worktree left at %s)", err, wt)
	}
	// cell identity marker: reap verifies it before any teardown
	_ = exec.Command("tmux", "set-option", "-t", sess, "@kraken-job", util.Slugify(j.Slug)).Run()
	_ = exec.Command("tmux", "set-window-option", "-t", sess+":0",
		"window-status-style", fmt.Sprintf("fg=colour%d", jobColor(util.Slugify(j.Slug)))).Run()
	// fresh worktree paths trigger claude's folder-trust dialog, which blocks
	// startup until answered
	acceptTrustPrompts(sess)
	// always "working": there is no plan gate to sit in — every build job
	// waits on /send before phase 2, but that's zeus' own brief, not a state
	if err := WriteJobState(j, "working"); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: cell is up but state write failed: %v\n", err)
	}
	return nil
}

// --- PR listing ---

// listBranchPRs asks gh for every PR (any state) whose head is the cell's
// branch. ok=false on any failure (auth, rate limit, no gh) — unknown must
// never touch the recorded history. States are normalized to lowercase
// open|merged|closed; anything else is dropped.
func listBranchPRs(dir, branch string) ([]JobPR, bool) {
	cmd := exec.Command("gh", "pr", "list", "--head", branch, "--state", "all",
		"--limit", "50", "--json", "url,state")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, false
	}
	var recs []struct {
		URL   string `json:"url"`
		State string `json:"state"`
	}
	if json.Unmarshal(out, &recs) != nil {
		return nil, false
	}
	var prs []JobPR
	for _, r := range recs {
		state := strings.ToLower(r.State)
		if r.URL == "" || (state != "open" && state != "merged" && state != "closed") {
			continue
		}
		prs = append(prs, JobPR{URL: r.URL, State: state})
	}
	return prs, true
}

// VerifyCellSession confirms a tmux session is the cell kraken created for
// this job (via the @kraken-job marker) before killing it.
func VerifyCellSession(sess, slug string) error {
	out, err := exec.Command("tmux", "show-options", "-t", sess, "-v", "@kraken-job").Output()
	if err != nil {
		return fmt.Errorf("session %s has no @kraken-job marker — not killing it; investigate manually", sess)
	}
	if got := strings.TrimSpace(string(out)); got != slug {
		return fmt.Errorf("session %s is marked for job %q, not %q — not killing it; investigate manually", sess, got, slug)
	}
	return nil
}

// ownTmuxSession names the tmux session the current process runs inside, ""
// when outside tmux (or when tmux can't be asked). Every descendant of a pane
// inherits $TMUX/$TMUX_PANE — including servers and test builds an agent
// launches from its own pane.
func ownTmuxSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{session_name}").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// guardSelfTeardown refuses to kill the tmux session the calling process
// lives in: killing it takes the caller down mid-teardown, stranding the job
// between states (the status-investigation incident — a cell-resident test
// web server confirmed /api/done against its own job and decapitated itself
// before the worktree removal and state write could run).
func guardSelfTeardown(target, own string) error {
	if own != "" && own == target {
		return fmt.Errorf("refusing to tear down %s from inside it — run k done/k k from outside the cell (or use the board)", target)
	}
	return nil
}

// teardownCell shuts a job's cell down: verified tmux session kill, then
// worktree removal. Partial teardown returns an error so the caller leaves
// the job's state alone for a retry.
//
// All-or-nothing: when not forcing, worktree dirt is checked BEFORE the
// session kill and refuses the whole teardown up front — killing first and
// failing on the dirty worktree left a dead cell stuck in "working" (the
// demo-job incident). force is the explicit human sign-off (k done, kill),
// where dirt is listed and discarded instead.
func teardownCell(j *Job, repo string, live map[string]bool, force bool) error {
	if err := guardSelfTeardown(CellSession(j), ownTmuxSession()); err != nil {
		return err
	}
	if !force {
		if wt := ExistingWorktree(j, repo); wt != "" {
			if dirt, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(dirt))) > 0 {
				return fmt.Errorf("worktree %s has uncommitted changes — not touching the cell (k done force-discards them)", wt)
			}
		}
	}
	sess := CellSession(j)
	if live[sess] {
		if err := VerifyCellSession(sess, util.Slugify(j.Slug)); err != nil {
			return err
		}
		if err := exec.Command("tmux", "kill-session", "-t", sess).Run(); err != nil {
			return fmt.Errorf("tmux kill-session: %v", err)
		}
	}
	if wt := ExistingWorktree(j, repo); wt != "" {
		args := []string{"-C", repo, "worktree", "remove"}
		if force {
			if dirt, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(dirt))) > 0 {
				fmt.Printf("discarding uncommitted changes in %s:\n", wt)
				for _, line := range strings.Split(strings.TrimSpace(string(dirt)), "\n") {
					fmt.Printf("  %s\n", line)
				}
			}
			args = append(args, "--force")
		}
		args = append(args, wt)
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("git worktree remove: %s", strings.TrimSpace(string(out)))
		}
	}
	// the cell dir (doc source, artifacts) goes too: artifacts were
	// preserved into pluto before teardown, and the mid-run log in the job
	// file is the durable trail
	os.RemoveAll(cellDir(j))
	return nil
}

// --- cell health ---

// acceptTrustPrompts answers claude's folder-trust dialog in a cell's fresh
// (single) pane. The worktree is kraken-made from a repo carl queued a job
// on — that IS the trust decision — and an unanswered dialog blocks startup
// entirely: no session file, no name, no work, and a cell that reads as
// broken.
func acceptTrustPrompts(sess string) {
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		out, err := exec.Command("tmux", "list-panes", "-t", sess, "-F", "#{pane_id}").Output()
		if err != nil {
			return
		}
		sawDialog := false
		rendered := 0
		for _, pane := range strings.Fields(string(out)) {
			text := core.PaneTail(pane, 30)
			if strings.Contains(text, "Is this a project you created or one you trust") {
				sawDialog = true
				// "Yes, I trust this folder" is the pre-selected option
				_ = exec.Command("tmux", "send-keys", "-t", pane, "Enter").Run()
			} else if strings.TrimSpace(text) != "" {
				rendered++
			}
		}
		// stop once the pane has rendered past the dialog; keep polling while
		// it's still blank (claude may not have drawn it yet)
		if !sawDialog && rendered >= 1 {
			return
		}
		time.Sleep(time.Second)
	}
}

// CellZeus finds a cell's zeus: by session-file name when claude has fully
// started, positionally otherwise — a single-pane cell means the fallback is
// simply the lowest-pane-id depth-0 claude in the cell session. Name-only
// lookup reads a still-starting agent as "gone", which is how a trust dialog
// once became "zeus process is gone".
func CellZeus(j *Job, all []*core.Peer) *core.Peer {
	sess := CellSession(j)
	var inSess []*core.Peer
	for _, p := range all {
		if p.Name == ZeusName(j) && p.Depth == 0 && p.TmuxSession == sess {
			return p
		}
		if p.TmuxSession == sess && p.Agent == "claude" && p.Depth == 0 {
			inSess = append(inSess, p)
		}
	}
	if len(inSess) == 0 {
		return nil
	}
	sort.Slice(inSess, func(a, b int) bool {
		ai, _ := strconv.Atoi(strings.TrimPrefix(inSess[a].Pane, "%"))
		bi, _ := strconv.Atoi(strings.TrimPrefix(inSess[b].Pane, "%"))
		return ai < bi
	})
	return inSess[0]
}

// stampSession records zeus' real Claude session id into the job file the
// moment it's known: the daemon calls this every tick for live cells (so
// every job that reaches working carries its id from the first minutes,
// per carl's steer — not only at teardown), and the teardown paths
// re-check right before the cell disappears as a daemon-down fallback.
// Guessed ids (cwd + newest-transcript inference) are never written —
// resuming a guessed id later could resume some other claude's session.
// Returns nil when there's nothing new to write.
func stampSession(j *Job, all []*core.Peer) error {
	zeus := CellZeus(j, all)
	if zeus == nil || zeus.SessionID == "" || zeus.SessionIDGuessed || zeus.SessionID == j.Session {
		return nil
	}
	return WriteJobFields(j, "session", zeus.SessionID)
}

// stampSessionBeforeTeardown is the best-effort teardown-path variant of
// stampSession: it runs its own discovery (the callers don't have one) and
// only warns on failure — the cell is going away either way, and the
// daemon has normally stamped the id long before this point.
func stampSessionBeforeTeardown(j *Job) {
	all, _ := core.OrderAll(core.DiscoverAll())
	if err := stampSession(j, all); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: session stamp for %s failed: %v\n", j.Slug, err)
	}
}

// CellHealth reports why a live cell looks unhealthy ("" = healthy).
// Conditions, in order: zeus' process must exist; no API-error banner may be
// the last thing in its transcript. Idleness comes from the PANE ("esc to
// interrupt" absent), never from the session file — an API error that kills
// a turn leaves the session status stuck on busy forever, and the error
// banner itself scrolls out of view once subagent rosters grow, so neither
// signal alone can be trusted. Every reason except "is gone" is retryable: a
// poke resumes a claude sitting at its prompt.
func CellHealth(j *Job, all []*core.Peer) string {
	zeus := CellZeus(j, all)
	if zeus == nil {
		return "zeus process is gone — restart the cell (kill session + k start after cleanup)"
	}
	// found positionally but not yet named: claude is still starting — a
	// lingering startup prompt needs a human, plain slowness needs patience
	if zeus.Name != ZeusName(j) {
		if zeus.Status == "question" {
			return "cell agent is stuck on a startup prompt — k o " + CellSession(j) + " to answer it"
		}
		return ""
	}
	// an error banner only counts when it is the last CONTENT in the pane's
	// transcript — an agent that recovered leaves newer output below its old
	// banners, and a visible-but-superseded error must not flag forever.
	// (The session file can't arbitrate: observed stuck on busy for 20h
	// across both crashes and clean turns.)
	erroredLast := func(p *core.Peer) bool {
		full := core.PaneTail(p.Pane, 200)
		if full == "" || strings.Contains(full, "esc to interrupt") {
			return false // mid-turn = recovering, not stuck
		}
		tail := strings.Join(core.TranscriptTail(p.Pane, 6), "\n")
		return core.ApiErrorRe.MatchString(tail)
	}
	if erroredLast(zeus) {
		return "zeus hit an API error"
	}
	return ""
}

// RetryCell pokes a stalled cell agent: a nudge message resumes a claude
// sitting at its prompt after an errored or interrupted turn. Dead processes
// can't be poked — that's reported, not papered over. Shared by the web
// board's retry and the viewer's health rows.
func RetryCell(j *Job) error {
	if !j.Started() || j.State == "done" || !LiveSessions()[CellSession(j)] {
		return fmt.Errorf("%s has no live cell to retry", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	reason := CellHealth(j, all)
	if strings.Contains(reason, "is gone") {
		return fmt.Errorf("%s: %s", j.Slug, reason)
	}
	// a plain nudge resumes a claude sitting at its prompt after an errored
	// or abandoned turn
	zeus := CellZeus(j, all)
	if zeus == nil {
		return fmt.Errorf("%s: agent not found — rerun discovery", j.Slug)
	}
	return core.SendMessage(zeus, "you stalled (API error or an interrupted turn) — pick up exactly where you left off and continue the job")
}

// KillJob tears a live job's cell down completely and returns the job to
// draft: state cleared AND ready flipped false — leaving ready: true would
// hand the job straight back to the daemon's queue loop. Artifacts are
// preserved first; the worktree goes too (a draft re-queued later needs a
// clean StartCell).
func KillJob(j *Job) ([]string, error) {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return nil, fmt.Errorf("job %s: %v", j.Slug, err)
	}
	stampSessionBeforeTeardown(j)
	copied, perr := preserveArtifacts(j, repo)
	var msgs []string
	for _, c := range copied {
		msgs = append(msgs, "preserved artifact "+c)
	}
	if perr != nil {
		return msgs, fmt.Errorf("artifact preservation failed: %v — not tearing down", perr)
	}
	// guard before the intent write: a refused self-teardown must not leave a
	// dangling marker on a live cell for the daemon to act on later
	if err := guardSelfTeardown(CellSession(j), ownTmuxSession()); err != nil {
		return msgs, err
	}
	if err := WriteJobFields(j, "teardown", "kill"); err != nil {
		return msgs, fmt.Errorf("teardown-intent write failed: %v — not tearing down", err)
	}
	if err := teardownCell(j, repo, LiveSessions(), true); err != nil {
		return msgs, fmt.Errorf("%v (job left in state %s)", err, j.State)
	}
	if err := WriteJobFields(j, "state", "", "ready", "false", "teardown", ""); err != nil {
		return msgs, fmt.Errorf("cell torn down, but state write failed: %v", err)
	}
	return append(msgs, "killed "+j.Slug+" — back to draft"), nil
}

// --- k done ---

// preserveArtifacts copies a finishing cell's deliverables into the pluto dir
// as <slug>.<name> (never overwriting): everything in the cell's artifacts/
// dir (all job types), plus, for info jobs, uncommitted root-level *.md in
// the worktree (where a solo zeus might leave a doc outside artifacts/).
// Teardown deletes both origins.
func preserveArtifacts(j *Job, repo string) ([]string, error) {
	var copied []string
	var firstErr error
	keep := func(src, name string) {
		data, err := os.ReadFile(src)
		if err != nil {
			// a failed copy must block teardown — teardown deletes the source
			if firstErr == nil {
				firstErr = fmt.Errorf("preserve %s: %v", src, err)
			}
			return
		}
		dst := filepath.Join(PlutoDir(), util.Slugify(j.Slug)+"."+name)
		if _, err := os.Stat(dst); err == nil {
			return // never overwrite an earlier copy
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("preserve %s: %v", dst, err)
			}
			return
		}
		copied = append(copied, dst)
	}
	if entries, err := os.ReadDir(CellArtifacts(j)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				keep(filepath.Join(CellArtifacts(j), e.Name()), e.Name())
			}
		}
	}
	if j.Type == "build" {
		return copied, firstErr // build deliverables travel via the PR
	}
	wt := ExistingWorktree(j, repo)
	if wt == "" {
		return copied, firstErr
	}
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if err != nil {
		return copied, firstErr
	}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		name := strings.TrimSpace(line[3:])
		if _, renamed, ok := strings.Cut(name, " -> "); ok {
			name = renamed
		}
		if strings.Contains(name, "/") || !strings.HasSuffix(name, ".md") {
			continue
		}
		keep(filepath.Join(wt, name), name)
	}
	return copied, firstErr
}

// teardownFinalFields maps a teardown-intent marker to the WriteJobFields
// args that finish it: the final state, plus clearing the marker itself.
// Pure, so the daemon's interrupted-teardown completion is testable without
// disk. ok is false for markers this binary doesn't understand — those are
// left alone rather than guessed at.
func teardownFinalFields(marker string) ([]string, bool) {
	switch {
	case marker == "kill":
		return []string{"state", "", "ready", "false", "teardown", ""}, true
	case marker == "done":
		return []string{"state", "done", "teardown", ""}, true
	case strings.HasPrefix(marker, "done:"):
		fields := []string{"state", "done"}
		if outcome := strings.TrimPrefix(marker, "done:"); outcome != "" {
			fields = append(fields, "outcome", outcome)
		}
		return append(fields, "teardown", ""), true
	}
	return nil, false
}

// ResumeTeardown finishes an interrupted teardown recorded by a surviving
// teardown: marker. The caller (the daemon's dead-cell watch) has verified
// the cell session is gone — the destructive part left is worktree removal,
// which the marker's provenance (carl's explicit done/kill) sanctions.
func ResumeTeardown(j *Job) error {
	fields, ok := teardownFinalFields(j.Teardown)
	if !ok {
		return fmt.Errorf("unknown teardown marker %q — clear it by hand", j.Teardown)
	}
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	if err := teardownCell(j, repo, LiveSessions(), true); err != nil {
		return err
	}
	return WriteJobFields(j, fields...)
}

// DoneJob completes a started job: preserve artifacts, force-teardown the
// cell, stamp state done + outcome. Shared by k done and the web UI's
// review→done move. The caller has validated the job is started and not done.
// The teardown intent lands in the job file before the first destructive step
// so an interrupted run is resumable (ResumeTeardown) instead of stranding
// the job between states.
func DoneJob(j *Job, outcome string) ([]string, error) {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return nil, fmt.Errorf("job %s: %v", j.Slug, err)
	}
	stampSessionBeforeTeardown(j)
	copied, perr := preserveArtifacts(j, repo)
	var msgs []string
	for _, c := range copied {
		msgs = append(msgs, "preserved artifact "+c)
	}
	if perr != nil {
		return msgs, fmt.Errorf("artifact preservation failed: %v — not tearing down (teardown deletes the source)", perr)
	}
	// guard before the intent write: a refused self-teardown must not leave a
	// dangling marker on a live cell for the daemon to act on later
	if err := guardSelfTeardown(CellSession(j), ownTmuxSession()); err != nil {
		return msgs, err
	}
	if err := WriteJobFields(j, "teardown", "done:"+outcome); err != nil {
		return msgs, fmt.Errorf("teardown-intent write failed: %v — not tearing down", err)
	}
	if err := teardownCell(j, repo, LiveSessions(), true); err != nil {
		return msgs, fmt.Errorf("%v (job left in state %s)", err, j.State)
	}
	fields := []string{"state", "done"}
	if outcome != "" {
		fields = append(fields, "outcome", outcome)
	}
	fields = append(fields, "teardown", "")
	if err := WriteJobFields(j, fields...); err != nil {
		return msgs, fmt.Errorf("cell torn down, but state write failed: %v", err)
	}
	return append(msgs, "done "+j.Slug+": cell torn down, state: done"), nil
}

// OneLine flattens arbitrary text into a single frontmatter-safe scalar line.
func OneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s))
}

// --- k reactivate ---

// reactivateMode decides which reactivation applies — pure, so the
// state × worktree matrix is testable without git or tmux:
//   - a done job gets "recreate": fresh worktree at the same path, branch
//     recycled off the base (an existing worktree refuses — done jobs were
//     torn down, leftovers need a human look);
//   - a started, non-done job whose cell died gets "resume": the existing
//     worktree is reused EXACTLY as the dead zeus left it — uncommitted WIP
//     preserved, branch untouched (missing worktree falls back to recreate);
//   - anything else refuses.
func reactivateMode(state string, worktreeExists bool) (string, error) {
	switch state {
	case "done":
		if worktreeExists {
			return "", fmt.Errorf("worktree already exists — clean it up first")
		}
		return "recreate", nil
	case "working", "blocked":
		if worktreeExists {
			return "resume", nil
		}
		return "recreate", nil
	}
	return "", fmt.Errorf("job has never started — k start it instead")
}

// ReactivateJob resurrects a job into its original zeus session. For a done
// job: the worktree is recreated at the SAME path (the claude project dir —
// and with it the transcript history — is keyed on that path), the branch
// recycles per ensureUsableBranch (reset off the base when the old one is
// fully merged, -rN only for unmerged leftovers), and claude is resumed with
// -r <session> rather than started fresh. For a started job whose cell died
// mid-run (hand-killed session, tmux restart, interrupted teardown): the
// existing worktree — dirty files and all — is reused in place, so the one
// recovery path that preserves WIP is also the one-command one. session
// overrides the recorded id (hand-rescue for jobs done before kraken
// stamped session ids); message rides along in the resume prompt as carl's
// steering.
func ReactivateJob(j *Job, session, message string) error {
	if j.Err != "" {
		return fmt.Errorf("job %s is unparseable (%s) — fix the frontmatter first", j.Slug, j.Err)
	}
	if session == "" {
		session = j.Session
	}
	if session == "" {
		return fmt.Errorf("%s has no recorded zeus session (done before kraken stamped session ids) — pass one explicitly: k reactivate %s --session <uuid>", j.Slug, j.Slug)
	}
	sess := CellSession(j)
	if LiveSessions()[sess] {
		return fmt.Errorf("cell %s is already live (k o %s to jump in)", sess, sess)
	}
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	wt := ExistingWorktree(j, repo)
	mode, err := reactivateMode(j.State, wt != "")
	if err != nil {
		if wt != "" && j.State == "done" {
			return fmt.Errorf("worktree %s already exists — clean it up first (git -C %s worktree remove %s)", wt, repo, wt)
		}
		return fmt.Errorf("%s: %v", j.Slug, err)
	}
	for _, d := range []string{cellDir(j), CellArtifacts(j)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %v", d, err)
		}
	}
	var prompt string
	if mode == "resume" {
		prompt = "your cell died mid-run (the tmux session was lost), and carl has reactivated it. Your worktree is EXACTLY as you left it — uncommitted changes preserved, branch " + CellBranch(j) + " untouched. Re-orient with git status and the job's mid-run log, then continue the job from where it stopped."
	} else {
		base, err := baseRef(j, repo)
		if err != nil {
			return fmt.Errorf("job %s: %v", j.Slug, err)
		}
		// recycle the branch when its PRs are all merged into the base; only
		// unmerged leftovers force a fresh -rN name (persisted inside)
		branchFlag, err := ensureUsableBranch(j, repo, base)
		if err != nil {
			return fmt.Errorf("job %s: pick branch: %v", j.Slug, err)
		}
		wt = CellWorktree(j, repo)
		out, err := exec.Command("git", "-C", repo, "worktree", "add", wt, branchFlag, CellBranch(j), base).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
		}
		if j.State == "done" {
			prompt = "this job was marked done, but carl has reactivated it for adjustments — it was not truly finished. You are resumed in a fresh cell: same worktree path, on branch " + CellBranch(j) + " freshly reset off " + base + ". Your earlier PRs are recorded on the job and already merged into the base, so re-read the current repo state before acting, then ship adjustments as a new PR from the current branch."
		} else {
			prompt = "your cell died mid-run and its worktree was lost, so it was recreated fresh: branch " + CellBranch(j) + " reset off " + base + ". Any uncommitted work from the previous run is gone — re-read the job's mid-run log and the repo state, then continue the job."
		}
	}
	if message != "" {
		prompt += "\nsteering from carl: " + message
	}
	argv := append(cellClaudeArgs(j.Model), "--name", ZeusName(j), "-r", session, "--", prompt)
	if err := exec.Command("tmux", "new-session", "-d", "-s", sess, "-c", wt,
		"-n", util.Slugify(j.Slug), core.ShellCmdString(argv)).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %v (worktree left at %s)", err, wt)
	}
	_ = exec.Command("tmux", "set-option", "-t", sess, "@kraken-job", util.Slugify(j.Slug)).Run()
	_ = exec.Command("tmux", "set-window-option", "-t", sess+":0",
		"window-status-style", fmt.Sprintf("fg=colour%d", jobColor(util.Slugify(j.Slug)))).Run()
	// the worktree path is the same as the previous run's, so claude's
	// folder trust normally persists — but a fresh machine or cleared
	// config would block startup on the dialog just like a first start
	acceptTrustPrompts(sess)
	// a reactivated job was "not truly finished" — drop the recorded outcome
	// along with the state flip so it reads like it was never completed
	if err := WriteJobFields(j, "state", "working", "outcome", ""); err != nil {
		return fmt.Errorf("cell is up but state write failed: %v", err)
	}
	return nil
}

// --- k migrate ---

// OldJobState re-reads a job file's raw frontmatter `state:` value directly,
// bypassing the strict parser that already rejected it into j.Err. This is
// more robust than trying to regexp the old value back out of the error
// string: cmdMigrate needs the ACTUAL old value to branch correctly (a
// terminal "merged"/"abandoned"/"failed" must never be re-run; only the
// in-flight old states should requeue), and the file is the one place that
// value still lives. "" (not found) reads as not-found, never an error.
func OldJobState(j *Job) (string, bool) {
	data, err := os.ReadFile(j.File)
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if len(lines) == 0 || !isFence(lines[0]) {
		return "", false
	}
	for i := 1; i < len(lines); i++ {
		if isFence(lines[i]) {
			break
		}
		line := lines[i]
		if line == "" || line[0] == ' ' || line[0] == '\t' || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		key, rest, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(key) == "state" {
			return stripComment(rest), true
		}
	}
	return "", false
}

// MigrateTerminal handles an old state that already meant the job was done
// (merged/abandoned/failed): any leftover cell is torn down (verified,
// forced) and the job is stamped state: done. ready is never touched — these
// jobs are finished, not requeued.
func MigrateTerminal(j *Job, live map[string]bool, defaultOutcome string) error {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return err
	}
	if err := teardownCell(j, repo, live, true); err != nil {
		return fmt.Errorf("teardown failed: %v", err)
	}
	fields := []string{"state", "done"}
	if j.Outcome == "" {
		fields = append(fields, "outcome", defaultOutcome)
	}
	if err := WriteJobFields(j, fields...); err != nil {
		return fmt.Errorf("teardown ok, but state write failed: %v", err)
	}
	return nil
}

// MigrateInFlight handles an old state that meant the job's cell was still
// mid-run (planning/plan-review/executing/testing/pr-open) under the
// pre-five-state model: preserve whatever the old cell produced, force-tear
// it down, and clear state back to "" — NOT touching ready, since a file
// already ready: true requeues naturally under the daemon and a hand-set
// ready: false correctly stays parked in draft.
func MigrateInFlight(j *Job, live map[string]bool) error {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return err
	}
	copied, perr := preserveArtifacts(j, repo)
	for _, c := range copied {
		fmt.Printf("%-24s preserved artifact %s\n", j.Slug, c)
	}
	if perr != nil {
		return fmt.Errorf("artifact preservation failed: %v", perr)
	}
	if err := teardownCell(j, repo, live, true); err != nil {
		return fmt.Errorf("teardown failed: %v", err)
	}
	if err := WriteJobFields(j, "state", ""); err != nil {
		return fmt.Errorf("teardown ok, but state write failed: %v", err)
	}
	return nil
}

// MigrateQueued handles an old "queued" state: just clear state back to "" (no
// ready write — a queued job was already ready: true by definition under the
// old model). No teardown is expected — a queued job never had a cell — but
// one runs defensively anyway in case reality drifted from the file.
func MigrateQueued(j *Job, live map[string]bool) error {
	repo, err := ResolveRepo(j.Repo)
	if err != nil {
		return err
	}
	if err := teardownCell(j, repo, live, true); err != nil {
		return fmt.Errorf("defensive teardown failed: %v", err)
	}
	if err := WriteJobFields(j, "state", ""); err != nil {
		return fmt.Errorf("state write failed: %v", err)
	}
	return nil
}
