package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- cells ---

// A cell is a job's runtime: a dedicated git worktree plus a dedicated tmux
// session holding the job's agents. Phase 2 cells are manual (k start / k
// reap); the daemon takes over creation and teardown in phase 3.

func cellsRoot() string {
	return filepath.Join(home(), ".kraken", "cells")
}

func cellDir(j *Job) string {
	return filepath.Join(cellsRoot(), slugify(j.Slug))
}

// cellWorktree names the worktree wt-<repo>: the dir basename is what shows
// up as the PROJECT label and editor root inside the cell, so it must read as
// the repo, not as "wt".
func cellWorktree(j *Job, repo string) string {
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

// existingWorktree finds the cell's worktree on disk, tolerating the legacy
// bare "wt" name from cells created before worktrees were named wt-<repo>.
// Empty means no worktree exists.
func existingWorktree(j *Job, repo string) string {
	wt := cellWorktree(j, repo)
	if _, err := os.Stat(wt); err == nil {
		return wt
	}
	legacy := filepath.Join(cellDir(j), "wt")
	if _, err := os.Stat(legacy); err == nil {
		return legacy
	}
	return ""
}

func cellSession(j *Job) string {
	return "cell-" + slugify(j.Slug)
}

func cellArtifacts(j *Job) string {
	return filepath.Join(cellDir(j), "artifacts")
}

// allArtifactPaths lists a job's artifacts from both homes: the live cell's
// artifacts dir and the preserved <slug>.<name> copies in pluto (which are
// all that remain after teardown).
func allArtifactPaths(j *Job) []string {
	out := listArtifacts(j)
	prefix := slugify(j.Slug) + "."
	entries, err := os.ReadDir(plutoDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || strings.HasSuffix(e.Name(), jobSuffix) {
			continue
		}
		out = append(out, filepath.Join(plutoDir(), e.Name()))
	}
	return out
}

// branchForRun names the branch for a given cell run: the bare slug branch
// for a first run, -rN for later runs (reactivations, or a re-queue whose
// original branch still exists — worktree teardown never deletes branches).
func branchForRun(slug string, run int) string {
	if run >= 2 {
		return fmt.Sprintf("carl/%s-r%d", slugify(slug), run)
	}
	return "carl/" + slugify(slug)
}

func cellBranch(j *Job) string {
	return branchForRun(j.Slug, j.Run)
}

// ensureFreshBranch advances the job's run until cellBranch names a branch
// that doesn't exist yet in repo, persisting the new run so every later
// cellBranch reader (reap's PR watch, the zeus brief, the board) agrees.
// No-op when the current branch name is free — a first run stays on
// carl/<slug> with no run field written.
func ensureFreshBranch(j *Job, repo string) error {
	run := j.Run
	for refExists(repo, branchForRun(j.Slug, run)) {
		if run < 1 {
			run = 1
		}
		run++
	}
	if run == j.Run {
		return nil
	}
	return writeJobFields(j, "run", strconv.Itoa(run))
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
// callers that only need liveness use the liveSessions() wrapper below.
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

// liveSessions snapshots current tmux session names; a thin bool-only view
// over liveSessionTimes for the many callers that just need liveness.
func liveSessions() map[string]bool {
	live := map[string]bool{}
	for name := range liveSessionTimes() {
		live[name] = true
	}
	return live
}

// cellClaudeArgs mirrors carl's `cc` alias (claude --dangerously-skip-permissions
// --rc): cell agents work unattended in their own worktree, so they run with
// bypass permissions from birth. tmux commands never see shell aliases, so the
// flags are spelled out here.
func cellClaudeArgs() []string {
	return []string{claudeLauncher(), "--dangerously-skip-permissions", "--rc"}
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

// --- k start ---

func cmdStart(args []string) {
	if len(args) != 1 {
		die("usage: k start <job-slug>")
	}
	jobs := loadJobs()
	j := jobBySlug(jobs, args[0])
	if j == nil {
		die("no job %q in %s (k j lists them)", args[0], plutoDir())
	}
	if j.Err != "" {
		die("job %s is unparseable: %s", j.Slug, j.Err)
	}
	if !j.Ready {
		die("job %s is not ready (flip ready: true in %s)", j.Slug, j.File)
	}
	if j.started() {
		die("job %s already started (state: %s)", j.Slug, j.State)
	}
	bySlug := map[string]*Job{}
	for _, dep := range jobs {
		bySlug[dep.Slug] = dep
	}
	if unmet := unmetPrereqs(j, bySlug); len(unmet) > 0 {
		var names []string
		for _, p := range unmet {
			names = append(names, p.Job)
		}
		die("job %s has unmet prereqs: %s", j.Slug, strings.Join(names, ", "))
	}
	// starting a gate-parked job is the human release: show what's being
	// signed off so the outcome review is explicit, not accidental
	for _, p := range j.Prereqs {
		if p.Gated {
			outcome := "(no outcome recorded)"
			if dep := bySlug[p.Job]; dep != nil && dep.Outcome != "" {
				outcome = dep.Outcome
			}
			fmt.Printf("releasing gate on %s — outcome: %s\n", p.Job, outcome)
		}
	}
	repo, err := resolveRepo(j.Repo)
	if err != nil {
		die("job %s: %v", j.Slug, err)
	}
	if err := startCell(j, repo); err != nil {
		die("%v", err)
	}
	fmt.Printf("started %s: worktree %s (branch %s), zeus in tmux session %s, state: working\n",
		j.Slug, cellWorktree(j, repo), cellBranch(j), cellSession(j))
	fmt.Printf("attach: tmux attach -t %s  (or k o %s)\n", cellSession(j), cellSession(j))
}

// startCell creates a job's cell: worktree off the base ref, tmux session,
// zeus pane, identity marker, state write. Callers have validated the job
// (parseable, ready, unstarted, prereqs met) — this owns mechanics only, and
// is shared by k start and the daemon.
func startCell(j *Job, repo string) error {
	sess := cellSession(j)
	if liveSessions()[sess] {
		return fmt.Errorf("cell %s is already live (k o %s to jump in)", sess, sess)
	}
	if old := existingWorktree(j, repo); old != "" {
		return fmt.Errorf("worktree %s already exists — clean it up first (git -C %s worktree remove %s)", old, repo, old)
	}
	wt := cellWorktree(j, repo)
	base, err := baseRef(j, repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	// a leftover branch from a killed or reaped earlier run would make the
	// -b worktree add below fail; every cell run gets a fresh branch
	if err := ensureFreshBranch(j, repo); err != nil {
		return fmt.Errorf("job %s: pick branch: %v", j.Slug, err)
	}
	for _, d := range []string{cellDir(j), cellArtifacts(j)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %v", d, err)
		}
	}
	out, err := exec.Command("git", "-C", repo, "worktree", "add", wt, "-b", cellBranch(j), base).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
	}
	// gate passed: from here on, failures must report the worktree so a
	// half-built cell is cleanable by hand
	argv := append(cellClaudeArgs(), "--name", zeusName(j),
		"--append-system-prompt", zeusBrief(j))
	if prompt := jobPrompt(j); prompt != "" {
		argv = append(argv, "--", prompt)
	}
	if err := exec.Command("tmux", "new-session", "-d", "-s", sess, "-c", wt,
		"-n", slugify(j.Slug), shellCmdString(argv)).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %v (worktree left at %s)", err, wt)
	}
	// cell identity marker: reap verifies it before any teardown
	_ = exec.Command("tmux", "set-option", "-t", sess, "@kraken-job", slugify(j.Slug)).Run()
	_ = exec.Command("tmux", "set-window-option", "-t", sess+":0",
		"window-status-style", fmt.Sprintf("fg=colour%d", jobColor(slugify(j.Slug)))).Run()
	// fresh worktree paths trigger claude's folder-trust dialog, which blocks
	// startup until answered
	acceptTrustPrompts(sess)
	// always "working": there is no plan gate to sit in — AutoApprove only
	// changes what zeus' brief tells it to do once it starts
	if err := writeJobState(j, "working"); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: cell is up but state write failed: %v\n", err)
	}
	return nil
}

// --- k reap ---

// prState asks gh for the PR state of a cell's branch. Any failure (no PR
// yet, auth, rate limit) degrades to "" — unknown must never tear down.
func prState(dir, branch string) string {
	cmd := exec.Command("gh", "pr", "view", branch, "--json", "state")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	var rec struct {
		State string `json:"state"`
	}
	if json.Unmarshal(out, &rec) != nil {
		return ""
	}
	return rec.State
}

// verifyCellSession confirms a tmux session is the cell kraken created for
// this job (via the @kraken-job marker) before killing it.
func verifyCellSession(sess, slug string) error {
	out, err := exec.Command("tmux", "show-options", "-t", sess, "-v", "@kraken-job").Output()
	if err != nil {
		return fmt.Errorf("session %s has no @kraken-job marker — not killing it; investigate manually", sess)
	}
	if got := strings.TrimSpace(string(out)); got != slug {
		return fmt.Errorf("session %s is marked for job %q, not %q — not killing it; investigate manually", sess, got, slug)
	}
	return nil
}

func cmdReap(args []string) {
	if len(args) != 0 {
		die("usage: k reap")
	}
	jobs := loadJobs()
	live := liveSessions()
	reaped, checked := 0, 0
	for _, j := range jobs {
		if j.Err != "" {
			// strict parsing: never act on a job kraken can't fully read —
			// but a live cell behind it deserves a loud line, not silence
			if j.started() || live[cellSession(j)] {
				fmt.Printf("%-24s unparseable (%s) — fix the frontmatter, then rerun\n", j.Slug, j.Err)
			}
			continue
		}
		if !j.started() || j.State == "done" {
			continue
		}
		checked++
		msg, changed := reapOne(j, live)
		fmt.Printf("%-24s %s\n", j.Slug, msg)
		if changed {
			reaped++
		}
	}
	switch {
	case checked == 0:
		fmt.Println("no started jobs to check")
	case reaped == 0:
		fmt.Println("nothing to reap")
	}
}

// reapOne checks one started job's PR and tears its cell down when merged.
// Shared by k reap and the daemon; returns a status line and whether the job
// converged to done. Unknown PR state must never tear down.
func reapOne(j *Job, live map[string]bool) (string, bool) {
	repo, err := resolveRepo(j.Repo)
	if err != nil {
		return fmt.Sprintf("%s (repo unresolved: %v)", j.State, err), false
	}
	dir := existingWorktree(j, repo)
	if dir == "" {
		// worktree already gone: still ask in the repo so state converges
		dir = repo
	}
	switch prState(dir, cellBranch(j)) {
	case "MERGED":
		stampSessionBeforeTeardown(j)              // last chance to record zeus' session id
		copied, perr := preserveArtifacts(j, repo) // before teardown deletes the sources
		if perr != nil {
			return fmt.Sprintf("merged, but artifact preservation failed (%v) — NOT tearing down", perr), false
		}
		if err := teardownCell(j, repo, live, false); err != nil {
			return fmt.Sprintf("merged, but teardown incomplete: %v", err), false
		}
		// the merge is the completion signal: done is the terminal state, no
		// intermediate review column to park in
		if err := writeJobState(j, "done"); err != nil {
			return fmt.Sprintf("merged + torn down, but state write failed: %v", err), false
		}
		if j.Outcome == "" {
			if err := writeJobFields(j, "outcome", "PR merged"); err != nil {
				return fmt.Sprintf("merged + torn down, but outcome write failed: %v", err), false
			}
		}
		msg := "merged → cell torn down, done"
		if len(copied) > 0 {
			msg += fmt.Sprintf(" (%d artifacts preserved)", len(copied))
		}
		return msg, true
	case "OPEN":
		// no state write: PR-open still derives to "working" or "blocked" from
		// transcript activity, matching the five-state model
		return "PR open", false
	case "CLOSED":
		return "PR closed without merge — decide manually (k done / respec)", false
	default:
		return fmt.Sprintf("%s (no PR found or gh unavailable — leaving alone)", j.State), false
	}
}

// teardownCell shuts a job's cell down: verified tmux session kill, then
// worktree removal. Partial teardown returns an error so the caller leaves
// the job's state alone for a retry.
//
// force distinguishes the two callers: k done is an explicit human sign-off
// (artifacts already preserved), so a dirty worktree — usually just the
// artifact itself — is listed and force-removed; k reap runs unattended, so
// it refuses dirt and leaves the worktree for a human look.
func teardownCell(j *Job, repo string, live map[string]bool, force bool) error {
	sess := cellSession(j)
	if live[sess] {
		if err := verifyCellSession(sess, slugify(j.Slug)); err != nil {
			return err
		}
		if err := exec.Command("tmux", "kill-session", "-t", sess).Run(); err != nil {
			return fmt.Errorf("tmux kill-session: %v", err)
		}
	}
	if wt := existingWorktree(j, repo); wt != "" {
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

// apiErrorRe matches the error banners Claude Code paints into its pane when
// the API is unhappy (overload, rate limit, connectivity). Scoped to obvious
// phrasings — a false positive only surfaces a retry button, never acts.
var apiErrorRe = regexp.MustCompile(`(?i)API Error|overloaded|rate.?limit(ed)?|connection (error|refused|reset)|network error`)

// paneTail returns the last visible lines of a pane ("" on any failure).
func paneTail(pane string, n int) string {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

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
			text := paneTail(pane, 30)
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

// cellZeus finds a cell's zeus: by session-file name when claude has fully
// started, positionally otherwise — a single-pane cell means the fallback is
// simply the lowest-pane-id depth-0 claude in the cell session. Name-only
// lookup reads a still-starting agent as "gone", which is how a trust dialog
// once became "zeus process is gone".
func cellZeus(j *Job, all []*Peer) *Peer {
	sess := cellSession(j)
	var inSess []*Peer
	for _, p := range all {
		if p.Name == zeusName(j) && p.Depth == 0 && p.TmuxSession == sess {
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
func stampSession(j *Job, all []*Peer) error {
	zeus := cellZeus(j, all)
	if zeus == nil || zeus.SessionID == "" || zeus.SessionIDGuessed || zeus.SessionID == j.Session {
		return nil
	}
	return writeJobFields(j, "session", zeus.SessionID)
}

// stampSessionBeforeTeardown is the best-effort teardown-path variant of
// stampSession: it runs its own discovery (the callers don't have one) and
// only warns on failure — the cell is going away either way, and the
// daemon has normally stamped the id long before this point.
func stampSessionBeforeTeardown(j *Job) {
	all, _ := orderAll(discoverAll())
	if err := stampSession(j, all); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: session stamp for %s failed: %v\n", j.Slug, err)
	}
}

// chromeLineRe matches claude's UI furniture: task lists, agent roster,
// recaps, spinners, separators, the input box, status bars, update nags.
// Everything it matches is presentation, not transcript content.
var chromeLineRe = regexp.MustCompile(`^\s*(✔|◼|◯|⧉|※|✻|✘|⏵⏵|📂|❯|↓|─|⎿|⏺ main$|\d+ tasks? \(|Jump to bottom)`)

// transcriptTail returns the last n content lines of a pane's transcript:
// the visible text minus chrome. Used to decide whether an error banner is
// the LAST thing the agent said (a dead turn) or old history that later
// output has superseded (an agent that recovered and finished).
func transcriptTail(pane string, n int) []string {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return nil
	}
	var content []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == "" || chromeLineRe.MatchString(line) {
			continue
		}
		content = append(content, line)
	}
	if len(content) > n {
		content = content[len(content)-n:]
	}
	return content
}

// cellHealth reports why a live cell looks unhealthy ("" = healthy).
// Conditions, in order: zeus' process must exist; no API-error banner may be
// the last thing in its transcript. Idleness comes from the PANE ("esc to
// interrupt" absent), never from the session file — an API error that kills
// a turn leaves the session status stuck on busy forever, and the error
// banner itself scrolls out of view once subagent rosters grow, so neither
// signal alone can be trusted. Every reason except "is gone" is retryable: a
// poke resumes a claude sitting at its prompt.
func cellHealth(j *Job, all []*Peer) string {
	zeus := cellZeus(j, all)
	if zeus == nil {
		return "zeus process is gone — restart the cell (kill session + k start after cleanup)"
	}
	// found positionally but not yet named: claude is still starting — a
	// lingering startup prompt needs a human, plain slowness needs patience
	if zeus.Name != zeusName(j) {
		if zeus.Status == "question" {
			return "cell agent is stuck on a startup prompt — k o " + cellSession(j) + " to answer it"
		}
		return ""
	}
	// an error banner only counts when it is the last CONTENT in the pane's
	// transcript — an agent that recovered leaves newer output below its old
	// banners, and a visible-but-superseded error must not flag forever.
	// (The session file can't arbitrate: observed stuck on busy for 20h
	// across both crashes and clean turns.)
	erroredLast := func(p *Peer) bool {
		full := paneTail(p.Pane, 200)
		if full == "" || strings.Contains(full, "esc to interrupt") {
			return false // mid-turn = recovering, not stuck
		}
		tail := strings.Join(transcriptTail(p.Pane, 6), "\n")
		return apiErrorRe.MatchString(tail)
	}
	if erroredLast(zeus) {
		return "zeus hit an API error"
	}
	return ""
}

// retryCell pokes a stalled cell agent: a nudge message resumes a claude
// sitting at its prompt after an errored or interrupted turn. Dead processes
// can't be poked — that's reported, not papered over. Shared by the web
// board's retry and the viewer's health rows.
func retryCell(j *Job) error {
	if !j.started() || j.State == "done" || !liveSessions()[cellSession(j)] {
		return fmt.Errorf("%s has no live cell to retry", j.Slug)
	}
	all, _ := orderAll(discoverAll())
	reason := cellHealth(j, all)
	if strings.Contains(reason, "is gone") {
		return fmt.Errorf("%s: %s", j.Slug, reason)
	}
	// a plain nudge resumes a claude sitting at its prompt after an errored
	// or abandoned turn
	zeus := cellZeus(j, all)
	if zeus == nil {
		return fmt.Errorf("%s: agent not found — rerun discovery", j.Slug)
	}
	return sendMessage(zeus, "you stalled (API error or an interrupted turn) — pick up exactly where you left off and continue the job")
}

// killJob tears a live job's cell down completely and returns the job to
// draft: state cleared AND ready flipped false — leaving ready: true would
// hand the job straight back to the daemon's queue loop. Artifacts are
// preserved first; the worktree goes too (a draft re-queued later needs a
// clean startCell).
func killJob(j *Job) ([]string, error) {
	repo, err := resolveRepo(j.Repo)
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
	if err := teardownCell(j, repo, liveSessions(), true); err != nil {
		return msgs, fmt.Errorf("%v (job left in state %s)", err, j.State)
	}
	if err := writeJobFields(j, "state", "", "ready", "false"); err != nil {
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
		dst := filepath.Join(plutoDir(), slugify(j.Slug)+"."+name)
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
	if entries, err := os.ReadDir(cellArtifacts(j)); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				keep(filepath.Join(cellArtifacts(j), e.Name()), e.Name())
			}
		}
	}
	if j.Type == "build" {
		return copied, firstErr // build deliverables travel via the PR
	}
	wt := existingWorktree(j, repo)
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

// cmdDone marks a started job done by hand: the phase-2 stand-in for artifact
// acceptance (explore/question jobs have no PR for k reap to notice).
func cmdDone(args []string) {
	if len(args) < 1 {
		die("usage: k done <job-slug> [outcome...]")
	}
	jobs := loadJobs()
	j := jobBySlug(jobs, args[0])
	if j == nil {
		die("no job %q in %s (k j lists them)", args[0], plutoDir())
	}
	if j.Err != "" {
		die("job %s is unparseable (%s) — fix the frontmatter first", j.Slug, j.Err)
	}
	if !j.started() {
		die("job %s hasn't started — nothing to complete", j.Slug)
	}
	if j.State == "done" {
		die("job %s is already done", j.Slug)
	}
	msgs, err := doneJob(j, oneLine(strings.Join(args[1:], " ")))
	for _, m := range msgs {
		fmt.Println(m)
	}
	if err != nil {
		die("%v", err)
	}
}

// doneJob completes a started job: preserve artifacts, force-teardown the
// cell, stamp state done + outcome. Shared by k done and the web UI's
// review→done move. The caller has validated the job is started and not done.
func doneJob(j *Job, outcome string) ([]string, error) {
	repo, err := resolveRepo(j.Repo)
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
	if err := teardownCell(j, repo, liveSessions(), true); err != nil {
		return msgs, fmt.Errorf("%v (job left in state %s)", err, j.State)
	}
	fields := []string{"state", "done"}
	if outcome != "" {
		fields = append(fields, "outcome", outcome)
	}
	if err := writeJobFields(j, fields...); err != nil {
		return msgs, fmt.Errorf("cell torn down, but state write failed: %v", err)
	}
	return append(msgs, "done "+j.Slug+": cell torn down, state: done"), nil
}

// oneLine flattens arbitrary text into a single frontmatter-safe scalar line.
func oneLine(s string) string {
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

// reactivateJob resurrects a done job into its original zeus session for
// adjustments: the worktree is recreated at the SAME path (the claude
// project dir — and with it the transcript history — is keyed on that
// path), the branch is a fresh -rN (the old branch's PR is already merged,
// and reap watching a merged branch would tear the new cell down on its
// next pass), and claude is resumed with -r <session> rather than started
// fresh. session overrides the recorded id (hand-rescue for jobs done
// before kraken stamped session ids); message rides along in the resume
// prompt as carl's steering.
func reactivateJob(j *Job, session, message string) error {
	if j.Err != "" {
		return fmt.Errorf("job %s is unparseable (%s) — fix the frontmatter first", j.Slug, j.Err)
	}
	if j.State != "done" {
		return fmt.Errorf("%s is not done (state %q) — only done jobs reactivate", j.Slug, j.State)
	}
	if session == "" {
		session = j.Session
	}
	if session == "" {
		return fmt.Errorf("%s has no recorded zeus session (done before kraken stamped session ids) — pass one explicitly: k reactivate %s --session <uuid>", j.Slug, j.Slug)
	}
	sess := cellSession(j)
	if liveSessions()[sess] {
		return fmt.Errorf("cell %s is already live (k o %s to jump in)", sess, sess)
	}
	repo, err := resolveRepo(j.Repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	if old := existingWorktree(j, repo); old != "" {
		return fmt.Errorf("worktree %s already exists — clean it up first (git -C %s worktree remove %s)", old, repo, old)
	}
	// always advance the run, even when the old branch was deleted locally:
	// gh pr view <branch> would still find the old MERGED PR by name
	run := j.Run
	if run < 1 {
		run = 1
	}
	run++
	for refExists(repo, branchForRun(j.Slug, run)) {
		run++
	}
	j.Run = run // cellBranch below must see it; persisted right after the worktree lands
	base, err := baseRef(j, repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	for _, d := range []string{cellDir(j), cellArtifacts(j)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %v", d, err)
		}
	}
	wt := cellWorktree(j, repo)
	out, err := exec.Command("git", "-C", repo, "worktree", "add", wt, "-b", cellBranch(j), base).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
	}
	if err := writeJobFields(j, "run", strconv.Itoa(run)); err != nil {
		return fmt.Errorf("worktree is up but run write failed: %v — fix %s by hand (run: %d) or reap will watch the wrong branch", err, filepath.Base(j.File), run)
	}
	prompt := "this job was marked done, but carl has reactivated it for adjustments — it was not truly finished. You are resumed in a fresh cell: same worktree path, but a FRESH branch " + cellBranch(j) + " off " + base + ". Your previous branch's PR is already merged — never push to it; your earlier changes are already in the base, so re-read the current repo state before acting, then ship adjustments as a new PR from the current branch."
	if message != "" {
		prompt += "\nsteering from carl: " + message
	}
	argv := append(cellClaudeArgs(), "--name", zeusName(j), "-r", session, "--", prompt)
	if err := exec.Command("tmux", "new-session", "-d", "-s", sess, "-c", wt,
		"-n", slugify(j.Slug), shellCmdString(argv)).Run(); err != nil {
		return fmt.Errorf("tmux new-session: %v (worktree left at %s)", err, wt)
	}
	_ = exec.Command("tmux", "set-option", "-t", sess, "@kraken-job", slugify(j.Slug)).Run()
	_ = exec.Command("tmux", "set-window-option", "-t", sess+":0",
		"window-status-style", fmt.Sprintf("fg=colour%d", jobColor(slugify(j.Slug)))).Run()
	// the worktree path is the same as the previous run's, so claude's
	// folder trust normally persists — but a fresh machine or cleared
	// config would block startup on the dialog just like a first start
	acceptTrustPrompts(sess)
	if err := writeJobState(j, "working"); err != nil {
		return fmt.Errorf("cell is up but state write failed: %v", err)
	}
	return nil
}

func cmdReactivate(args []string) {
	session := ""
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--session" {
			i++
			if i >= len(args) {
				die("--session needs a session uuid")
			}
			session = args[i]
			continue
		}
		rest = append(rest, args[i])
	}
	if len(rest) < 1 {
		die("usage: k reactivate <job-slug> [--session <uuid>] [message...]")
	}
	j := jobBySlug(loadJobs(), rest[0])
	if j == nil {
		die("no job %q in %s (k j lists them)", rest[0], plutoDir())
	}
	if err := reactivateJob(j, session, oneLine(strings.Join(rest[1:], " "))); err != nil {
		die("%v", err)
	}
	fmt.Printf("reactivated %s: zeus session resumed in tmux session %s (branch %s), state: working\n",
		j.Slug, cellSession(j), cellBranch(j))
	fmt.Printf("attach: tmux attach -t %s  (or k o %s)\n", cellSession(j), cellSession(j))
}

// --- k migrate ---

// oldJobState re-reads a job file's raw frontmatter `state:` value directly,
// bypassing the strict parser that already rejected it into j.Err. This is
// more robust than trying to regexp the old value back out of the error
// string: cmdMigrate needs the ACTUAL old value to branch correctly (a
// terminal "merged"/"abandoned"/"failed" must never be re-run; only the
// in-flight old states should requeue), and the file is the one place that
// value still lives. "" (not found) reads as not-found, never an error.
func oldJobState(j *Job) (string, bool) {
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

// migrateTerminal handles an old state that already meant the job was done
// (merged/abandoned/failed): any leftover cell is torn down (verified,
// forced) and the job is stamped state: done. ready is never touched — these
// jobs are finished, not requeued.
func migrateTerminal(j *Job, live map[string]bool, defaultOutcome string) error {
	repo, err := resolveRepo(j.Repo)
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
	if err := writeJobFields(j, fields...); err != nil {
		return fmt.Errorf("teardown ok, but state write failed: %v", err)
	}
	return nil
}

// migrateInFlight handles an old state that meant the job's cell was still
// mid-run (planning/plan-review/executing/testing/pr-open) under the
// pre-five-state model: preserve whatever the old cell produced, force-tear
// it down, and clear state back to "" — NOT touching ready, since a file
// already ready: true requeues naturally under the daemon and a hand-set
// ready: false correctly stays parked in draft.
func migrateInFlight(j *Job, live map[string]bool) error {
	repo, err := resolveRepo(j.Repo)
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
	if err := writeJobFields(j, "state", ""); err != nil {
		return fmt.Errorf("teardown ok, but state write failed: %v", err)
	}
	return nil
}

// migrateQueued handles an old "queued" state: just clear state back to "" (no
// ready write — a queued job was already ready: true by definition under the
// old model). No teardown is expected — a queued job never had a cell — but
// one runs defensively anyway in case reality drifted from the file.
func migrateQueued(j *Job, live map[string]bool) error {
	repo, err := resolveRepo(j.Repo)
	if err != nil {
		return err
	}
	if err := teardownCell(j, repo, live, true); err != nil {
		return fmt.Errorf("defensive teardown failed: %v", err)
	}
	if err := writeJobFields(j, "state", ""); err != nil {
		return fmt.Errorf("state write failed: %v", err)
	}
	return nil
}

// cmdMigrate requeues jobs left behind by the pre-five-state model: their
// frontmatter state (e.g. "planning", "executing", "pr-open", "merged") no
// longer parses under validStates, so loadJobs marks them Err "unknown state
// ..." and they sit inert — invisible to the daemon, unstartable, un-reapable.
// oldJobState recovers the actual old value (the parser only left it in the
// error string) and cmdMigrate branches on it:
//   - merged/abandoned/failed (the old model's terminal states): any leftover
//     cell is torn down and the job is stamped done — never re-run, ready
//     untouched.
//   - planning/plan-review/executing/testing/pr-open (still in flight):
//     artifacts preserved, cell torn down, state cleared to "" — ready
//     untouched, so the job's own ready flag decides whether/when it requeues.
//   - queued: state cleared to "" only; no ready write.
//   - anything else unrecognized: skipped untouched, printed loudly, since a
//     value this function doesn't know isn't safe to guess at.
func cmdMigrate(args []string) {
	if len(args) != 0 {
		die("usage: k migrate")
	}
	jobs := loadJobs()
	live := liveSessions()
	migrated := 0
	for _, j := range jobs {
		if !strings.Contains(j.Err, "unknown state") {
			continue
		}
		old, ok := oldJobState(j)
		if !ok {
			fmt.Printf("%-24s skipped: could not read the old state: line from %s\n", j.Slug, j.File)
			continue
		}
		switch old {
		case "merged":
			if err := migrateTerminal(j, live, "PR merged"); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: was merged, marked done\n", j.Slug)
			migrated++
		case "abandoned", "failed":
			if err := migrateTerminal(j, live, "migrated: was "+old); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: was %s, marked done\n", j.Slug, old)
			migrated++
		case "planning", "plan-review", "executing", "testing", "pr-open":
			if err := migrateInFlight(j, live); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: old cell torn down, state cleared — requeues via its own ready flag\n", j.Slug)
			migrated++
		case "queued":
			if err := migrateQueued(j, live); err != nil {
				fmt.Printf("%-24s skipped: %v\n", j.Slug, err)
				continue
			}
			fmt.Printf("%-24s migrated: state cleared\n", j.Slug)
			migrated++
		default:
			fmt.Printf("%-24s skipped: unrecognized old state %q\n", j.Slug, old)
		}
	}
	if migrated == 0 {
		fmt.Println("nothing to migrate")
	}
}
