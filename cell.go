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

func cellInbox(j *Job) string {
	return filepath.Join(cellDir(j), "inbox")
}

func cellArtifacts(j *Job) string {
	return filepath.Join(cellDir(j), "artifacts")
}

func cellPlan(j *Job) string {
	return filepath.Join(cellDir(j), "plan.md")
}

// cellDoneMarker is hermes' completion signal: its first line becomes the
// job's outcome. The daemon reads it and moves the job to the review column —
// never straight to done, so carl keeps the final call.
func cellDoneMarker(j *Job) string {
	return filepath.Join(cellDir(j), "done.md")
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

func cellBranch(j *Job) string {
	return "carl/" + slugify(j.Slug)
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

// liveSessions snapshots current tmux session names; no tmux server reads as
// no sessions.
func liveSessions() map[string]bool {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return map[string]bool{}
	}
	live := map[string]bool{}
	for _, s := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if s != "" {
			live[s] = true
		}
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
	fmt.Printf("started %s: worktree %s (branch %s), zeus + hermes in tmux session %s\n",
		j.Slug, cellWorktree(j, repo), cellBranch(j), cellSession(j))
	fmt.Printf("attach: tmux attach -t %s  (or k o %s)\n", cellSession(j), cellSession(j))
}

// startCell creates a job's cell: worktree off the base ref, tmux session,
// zeus + hermes panes, identity marker, state write. Callers have validated
// the job (parseable, ready, unstarted, prereqs met) — this owns mechanics
// only, and is shared by k start and the daemon.
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
	for _, d := range []string{cellDir(j), cellInbox(j), cellArtifacts(j)} {
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
	if err := spawnCrew(j, sess, wt); err != nil {
		// roll back the half-cell: leaving the zeus session up would leak an
		// uncapped cell the daemon retries against forever
		_ = exec.Command("tmux", "kill-session", "-t", sess).Run()
		if out, rmErr := exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt).CombinedOutput(); rmErr != nil {
			return fmt.Errorf("%v; rollback left worktree %s (%s)", err, wt, strings.TrimSpace(string(out)))
		}
		return fmt.Errorf("%v — cell rolled back, will retry", err)
	}
	// fresh worktree paths trigger claude's folder-trust dialog, which blocks
	// startup until answered
	acceptTrustPrompts(sess)
	state := "planning"
	if j.AutoApprove {
		state = "executing" // no plan gate to sit in
	}
	if err := writeJobState(j, state); err != nil {
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
		copied, perr := preserveArtifacts(j, repo) // before teardown deletes the sources
		if perr != nil {
			return fmt.Sprintf("merged, but artifact preservation failed (%v) — NOT tearing down", perr), false
		}
		if err := teardownCell(j, repo, live, false); err != nil {
			return fmt.Sprintf("merged, but teardown incomplete: %v", err), false
		}
		// done is always carl's drag: the merge frees the cell, but the job
		// parks in the review column until he moves it himself
		if err := writeJobState(j, "merged"); err != nil {
			return fmt.Sprintf("merged + torn down, but state write failed: %v", err), false
		}
		msg := "merged → cell torn down, awaiting carl's done drag"
		if len(copied) > 0 {
			msg += fmt.Sprintf(" (%d artifacts preserved)", len(copied))
		}
		return msg, true
	case "OPEN":
		if j.State != "pr-open" {
			if err := writeJobState(j, "pr-open"); err == nil {
				return "PR open (state → pr-open)", true
			}
		}
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
	// the cell dir (inbox, plan, artifacts) goes too: artifacts were
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
// panes. The worktree is kraken-made from a repo carl queued a job on — that
// IS the trust decision — and an unanswered dialog blocks startup entirely:
// no session file, no name, no work, and a cell that reads as broken.
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
		// stop once every pane has rendered past the dialog; keep polling
		// while panes are still blank (claude may not have drawn it yet)
		if !sawDialog && rendered >= 2 {
			return
		}
		time.Sleep(time.Second)
	}
}

// cellAgents finds a cell's zeus and hermes: by session-file name when claude
// has fully started, positionally otherwise — zeus spawns first, so it holds
// the lowest pane id among the cell session's claudes. Name-only lookup reads
// a still-starting agent as "gone", which is how a trust dialog once became
// "zeus process is gone".
func cellAgents(j *Job, all []*Peer) (zeus, hermes *Peer) {
	sess := cellSession(j)
	var inSess []*Peer
	for _, p := range all {
		switch p.Name {
		case zeusName(j):
			zeus = p
		case hermesName(j):
			hermes = p
		}
		if p.TmuxSession == sess && p.Agent == "claude" && p.Depth == 0 {
			inSess = append(inSess, p)
		}
	}
	sort.Slice(inSess, func(a, b int) bool {
		ai, _ := strconv.Atoi(strings.TrimPrefix(inSess[a].Pane, "%"))
		bi, _ := strconv.Atoi(strings.TrimPrefix(inSess[b].Pane, "%"))
		return ai < bi
	})
	if zeus == nil && len(inSess) > 0 {
		zeus = inSess[0]
	}
	if hermes == nil && len(inSess) > 1 {
		hermes = inSess[len(inSess)-1]
	}
	return zeus, hermes
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
// Conditions, in order: agent processes must exist; no API-error banner in an
// idle pane; and zeus must not sit idle for stallAfter with work in flight
// and nothing escalated. Idleness comes from the PANE ("esc to interrupt"
// absent), never from the session file — an API error that kills a turn
// leaves the session status stuck on busy forever, and the error banner
// itself scrolls out of view once subagent rosters grow, so neither signal
// alone can be trusted. Every reason except "is gone" is retryable: a poke
// resumes a claude sitting at its prompt.
func cellHealth(j *Job, all []*Peer) string {
	zeus, hermes := cellAgents(j, all)
	if zeus == nil {
		return "zeus process is gone — restart the cell (kill session + k start after cleanup)"
	}
	if hermes == nil {
		return "hermes process is gone — escalations won't flow; retry respawns nothing, investigate the pane"
	}
	// found positionally but not yet named: claude is still starting — a
	// lingering startup prompt needs a human, plain slowness needs patience
	if zeus.Name != zeusName(j) || hermes.Name != hermesName(j) {
		if zeus.Status == "question" || hermes.Status == "question" {
			return "cell agents are stuck on a startup prompt — k o " + cellSession(j) + " to answer it"
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
	if erroredLast(hermes) {
		return "hermes hit an API error"
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
	// poke whichever agent stalled (default zeus): a plain nudge resumes a
	// claude sitting at its prompt after an errored or abandoned turn
	zeus, hermes := cellAgents(j, all)
	target := zeus
	if strings.HasPrefix(reason, "hermes") {
		target = hermes
	}
	if target == nil {
		return fmt.Errorf("%s: agent not found — rerun discovery", j.Slug)
	}
	return sendMessage(target, "you stalled (API error or an interrupted turn) — pick up exactly where you left off and continue the job")
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
// dir (all job types — hermes' packets are worth keeping), plus, for
// explore/question jobs, uncommitted root-level *.md in the worktree (where
// pre-crew zeuses left their docs). Teardown deletes both origins.
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
