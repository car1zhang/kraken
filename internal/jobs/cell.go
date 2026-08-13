package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// --- cells ---

// A cell is a job's runtime: a dedicated git worktree plus a dedicated tmux
// session holding the job's agents. Cells are created via the board's start
// action or the daemon (StartCell), and torn down via the board's Done/Kill
// actions or the daemon's dead-cell watch (teardownCell).

func cellsRoot() string {
	return filepath.Join(util.Home(), ".kraken", "cells")
}

// cellName is the basename every one of a job's runtime names is built from:
// the slugified job name with the job's immutable store id appended.
//
// The id is what makes it collision-proof, and that is the whole point. A
// slug is unique only among ACTIVE jobs (the jobs_active_slug partial index),
// and archiving deliberately FREES it — store.ArchiveJob stamps
// archived_at/archived_name and leaves jobs.slug untouched — so a new job may
// legitimately hold a name an archived job still has files under. Before the
// id was here, both generations resolved to one directory: an archived job's
// leftover git worktree made every StartCell for the new job fail with
// "worktree ... already exists" (24 consecutive daemon ticks, in the incident
// this was written for), and the board served the OLD job's artifacts off the
// new job's card because jobArtifacts walks a slug-named directory.
//
// Slug FIRST, id second, because this name is read by a human: `k ls`, the
// editor root inside a cell, and the tmux session all show it, and leading
// with the slug keeps it searchable. The id only disambiguates.
func cellName(j *Job) string {
	return util.Slugify(j.Slug) + "-" + strconv.FormatInt(j.ID, 10)
}

func cellDir(j *Job) string {
	return filepath.Join(cellsRoot(), cellName(j))
}

// formerCellName is cellName under one of the job's earlier slugs. Empty for
// a slug that slugifies to nothing, which a caller must skip rather than join
// — a bare "-<id>" would name a directory belonging to no job.
func formerCellName(j *Job, former string) string {
	s := util.Slugify(former)
	if s == "" {
		return ""
	}
	return s + "-" + strconv.FormatInt(j.ID, 10)
}

// cellNameCandidate is one name a job's runtime state may be filed under: the
// slugified slug (which is what the @kraken-job marker and the tmux window
// carry) and the cellName built from it (which the dir and session carry).
type cellNameCandidate struct {
	Slug string // util.Slugify of the job slug
	Name string // cellName: Slug + "-" + id
}

// cellNameCandidates is the ONE list every former-name probe derives from —
// dirs, tmux sessions, and marker values — current name first, then one per
// former slug, deduped.
//
// One list rather than three parallel ones because the daemon's self-heal has
// to go BACKWARDS: given the session a cell was found under, it needs the
// marker value that session should be carrying. Three independently built
// lists would have to agree by index, which is exactly the coupling that
// silently breaks the first time one of them skips an entry the others keep.
func cellNameCandidates(j *Job) []cellNameCandidate {
	out := []cellNameCandidate{{Slug: util.Slugify(j.Slug), Name: cellName(j)}}
	seen := map[string]bool{out[0].Name: true}
	for _, f := range j.FormerSlugs {
		name := formerCellName(j, f)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, cellNameCandidate{Slug: util.Slugify(f), Name: name})
	}
	return out
}

// cellDirCandidates lists every directory this job's cell could be sitting in:
// the current name first, then one per former slug. A rename NEVER moves the
// cell dir — renaming a worktree under a running agent breaks its pane, and
// the ruling is permanent (cutover.go states it) — so after a rename the live
// cell is still on disk under the name it was created with, and every read
// path has to probe for it.
//
// Current first is the ordering contract, not a preference: a job renamed
// away from a name that a LATER cell of its own then took back would otherwise
// resolve to the older directory.
func cellDirCandidates(j *Job) []string {
	cands := cellNameCandidates(j)
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, filepath.Join(cellsRoot(), c.Name))
	}
	return out
}

// existingCellDir is the cell dir that actually EXISTS, current name first.
// It falls back to the current name when none of them does, so a caller that
// is about to create something still gets the name a fresh cell should carry
// — the probe answers "where is it", and "nowhere yet" has to answer with the
// place it will be.
func existingCellDir(j *Job) string {
	for _, d := range cellDirCandidates(j) {
		if _, err := os.Stat(d); err == nil {
			return d
		}
	}
	return cellDir(j)
}

// legacyCellDir is the pre-id naming: the bare slugified slug. Nothing
// CREATES this any more — it exists so readers that must still find state
// written before the cutover can look there too. Its one live caller is
// JobProjectDirs (tokens.go): Claude Code buckets a transcript by the agent's
// startup cwd, so every transcript written before the cutover is filed under
// a path derived from this name, and a token scan that only knew the new name
// would report zero for a job's entire history.
//
// Deliberately NOT used by anything that creates, starts, or deletes a cell —
// see cellName for why sharing this name across two jobs is the bug.
func legacyCellDir(j *Job) string {
	return filepath.Join(cellsRoot(), util.Slugify(j.Slug))
}

// LegacyCellWorktree is CellWorktree under the pre-id cell naming. Same
// read-only, find-what-was-already-written purpose as legacyCellDir.
func LegacyCellWorktree(j *Job, repo string) string {
	return cellWorktreeIn(legacyCellDir(j), repo)
}

// checkCellIdentity refuses a job that cannot name a cell safely. It is
// called at the two points that actually CREATE or DESTROY cell state
// (StartCell, teardownCell) rather than inside cellDir, because cellDir has
// fifteen call sites that only ever derive a read path — and a read against a
// wrong path is a harmless "not found", while a CREATE against one silently
// makes ~/.kraken/cells/foo-0 and a DELETE against one removes the wrong
// tree.
//
// A zero id is the real hazard and it is reachable: ParseJob never sets ID
// (the id is minted by the store and never appears in the .job.md file), and
// LoadJobs falls back to LoadJobsFromFiles whenever no store is wired, so a
// file-backed *Job carries ID == 0. Failing loudly here is what stops that
// becoming a directory named for a job that does not exist.
func checkCellIdentity(j *Job) error {
	if j == nil {
		return fmt.Errorf("no job")
	}
	if util.Slugify(j.Slug) == "" {
		return fmt.Errorf("job slug %q slugifies to nothing — it cannot name a cell", j.Slug)
	}
	if j.ID < 1 {
		return fmt.Errorf("job %q has no store id (got %d) — a cell is named <slug>-<id>, so it cannot be created from a job that was never persisted", j.Slug, j.ID)
	}
	return nil
}

// CellWorktree names the worktree wt-<repo>: the dir basename is what shows
// up as the PROJECT label and editor root inside the cell, so it must read as
// the repo, not as "wt".
func CellWorktree(j *Job, repo string) string {
	return cellWorktreeIn(cellDir(j), repo)
}

// cellWorktreeIn is the ONE spelling of a worktree's directory name, the way
// cellArtifactsIn is the one spelling of the artifacts subdirectory. The
// derivation used to be re-typed at each of the places that need it — the two
// name functions below and above, ExistingWorktree's probe, JobProjectDirs'
// bases, ReactivateJob's create — and a creator and a probe that disagreed
// about this string is a worktree nothing can find.
func cellWorktreeIn(dir, repo string) string {
	return filepath.Join(dir, "wt-"+filepath.Base(repo))
}

// worktreeNamesIn is both names a cell's worktree may carry inside dir, the
// current one first: wt-<repo>, then the bare "wt" of cells created before
// worktrees were named for their repo. Probes iterate it; creators take
// cellWorktreeIn, since nothing writes the legacy name any more.
func worktreeNamesIn(dir, repo string) []string {
	return []string{cellWorktreeIn(dir, repo), filepath.Join(dir, "wt")}
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
//
// It probes every cell-dir candidate (cellDirCandidates), current name first,
// because a rename leaves a live cell's worktree exactly where it was: this is
// the function teardown, StartCell's already-exists refusal and the dirt check
// all resolve through, and each of them reading "no worktree" for a renamed
// live cell is its own distinct wrong answer — a leaked checkout, a spurious
// start, a discarded dirty tree.
func ExistingWorktree(j *Job, repo string) string {
	for _, dir := range cellDirCandidates(j) {
		for _, wt := range worktreeNamesIn(dir, repo) {
			if _, err := os.Stat(wt); err == nil {
				return wt
			}
		}
	}
	return ""
}

// CellSession names the job's tmux session. It carries the id for a
// correctness reason, not merely to match cellDir: CellZeus, CellHealth,
// InterruptCell, RetryCell and /api/steer all resolve a job to its running
// agent THROUGH this name, so if an archived job's session were still alive
// when a new same-slug job started, the new job would adopt the old job's
// zeus — and carl's steer messages would go to the wrong agent, silently.
// ArchiveJob does guard against a live cell, but liveSessionTimes fails OPEN
// (an empty map when `tmux list-sessions` errors), so that guard is not
// absolute.
func CellSession(j *Job) string {
	return "cell-" + cellName(j)
}

// FindCellSession returns the live tmux session holding this job's cell, and
// whether one was found at all: the current name when it is live, else the
// session a former slug would have named. A rename converges the session name
// straight away (rename.go's convergeCellTmux), so the former arm covers the
// two cases where it did not — the convergence failed, or this tick landed in
// the gap between the store commit and the tmux call.
//
// The bool is what the dead-cell watch keys on. Reading a renamed-but-not-yet-
// converged cell as GONE would notify carl that a running job's cell had
// vanished, which is exactly the alert he has previously acted on with a
// teardown that destroyed a day's uncommitted work.
func FindCellSession(j *Job, live map[string]bool) (string, bool) {
	for _, sess := range cellSessionCandidates(j) {
		if live[sess] {
			return sess, true
		}
	}
	return "", false
}

// ProbeCellSession answers what can be ESTABLISHED about a job's cell right
// now, rather than what a snapshot happened to contain. FindCellSession reads
// a map somebody else built; this one goes and asks, and reports whether the
// answer is worth anything.
//
// Three outcomes, because a caller that must not act on a guess needs to tell
// them apart:
//
//	(sess, true)  — sess exists and is this job's cell.
//	("",  true)   — tmux answered, and holds none of this job's names: the
//	                cell is CONFIRMED gone.
//	("",  false)  — tmux could not be asked at all. Absence proves nothing.
//
// The third case is the reason this function exists. LiveSessions() folds an
// unreachable tmux into an EMPTY map, which is byte-identical to a machine
// with no sessions — so "not in the snapshot" cannot distinguish a dead cell
// from an unanswered question, and a caller that writes irreversible state on
// that distinction (the viewer's abandon verb) would orphan a live agent on a
// bad tmux call. Each candidate absent from the snapshot is additionally
// re-probed with core.SessionAlive, the same positive-evidence rule the
// daemon applies before flagging a cell gone.
func ProbeCellSession(j *Job) (string, bool) {
	live, err := listSessionTimes()
	if err != nil {
		return "", false
	}
	for _, sess := range cellSessionCandidates(j) {
		if _, ok := live[sess]; ok || core.SessionAlive(sess) {
			return sess, true
		}
	}
	return "", true
}

// cellSessionCandidates lists every tmux session name this job's cell could
// be running under, current first — the session-name sibling of
// cellDirCandidates, and derived from the same list for the same reason.
func cellSessionCandidates(j *Job) []string {
	cands := cellNameCandidates(j)
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, "cell-"+c.Name)
	}
	return out
}

// markerSlugForSession returns the @kraken-job value a cell running under sess
// should be carrying — the slugified name that produced that session name — or
// the current slugified slug when sess is none of this job's. It is what lets
// the daemon's self-heal verify a cell it found under an OLD name before
// renaming it to the new one.
func markerSlugForSession(j *Job, sess string) string {
	for _, c := range cellNameCandidates(j) {
		if "cell-"+c.Name == sess {
			return c.Slug
		}
	}
	return util.Slugify(j.Slug)
}

// CellArtifacts is the cell's deliverables dir, resolved through
// existingCellDir so a renamed live cell keeps syncing and serving the
// artifacts it is still writing into the directory it was created in. The two
// CREATE paths (StartCell, ReactivateJob) deliberately do NOT go through here
// — they mkdir under the current name, since a cell being made now should
// carry the job's current name.
func CellArtifacts(j *Job) string {
	return cellArtifactsIn(existingCellDir(j))
}

// cellArtifactsIn is the one spelling of the artifacts subdirectory, so the
// resolving reader above and the creating writers cannot disagree about it.
func cellArtifactsIn(dir string) string {
	return filepath.Join(dir, "artifacts")
}

// AllArtifactPaths lists a job's artifacts from both homes: the live cell's
// artifacts dir and the preserved <slug>-<id>.<name> copies in pluto (which
// are all that remain after teardown). Questions are rows in kraken.db, not
// files, so there is no reserved name to exclude here any more.
//
// The prefix carries the id for the same reason cellName does: matching on
// the bare slug served an ARCHIVED job's preserved copies off a new
// same-slug job's card.
func AllArtifactPaths(j *Job) []string {
	out := ListArtifacts(j)
	prefix := PlutoArtifactPrefix(j)
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

// PlutoArtifactPrefix is the filename prefix a job's preserved flat artifact
// copies carry in ~/Documents/pluto (and, after archiving, in its archive/
// subdir): "<slug>-<id>.". One definition, because a writer and a reader that
// disagree about this string is a job whose artifacts silently vanish from
// its card.
func PlutoArtifactPrefix(j *Job) string {
	// No store wired means no ids at all: ParseJob never sets one, so every
	// file-backed job carries ID == 0. Fall back to the bare slug, which is
	// both the pre-cutover spelling and the only name available — and which
	// cannot be ambiguous in that mode, since without a store there is no
	// archive namespace for a second generation of the slug to live in.
	//
	// This is a READ-side accommodation only. The paths that create or
	// destroy cell state go through checkCellIdentity and refuse a zero id
	// outright, so this can never quietly produce a directory.
	if j.ID < 1 {
		return util.Slugify(j.Slug) + "."
	}
	return cellName(j) + "."
}

// branchForRun names the branch for a given cell run: carl/<slug>-<id>
// normally, -rN appended only for fallback runs (an existing branch with
// unmerged commits that a forced reuse would orphan — see ensureUsableBranch).
//
// The id is here on the same reasoning as cellName: a reused slug otherwise
// resolves to a branch another job already has checked out. It goes AFTER the
// slug so `git branch` stays readable and searchable by job name.
func branchForRun(j *Job, run int) string {
	if run >= 2 {
		return fmt.Sprintf("carl/%s-r%d", cellName(j), run)
	}
	return "carl/" + cellName(j)
}

func CellBranch(j *Job) string {
	return branchForRun(j, j.Run)
}

// branchFamily enumerates a job's legitimate head branches: every run the job
// has actually advanced through (branchForRun for r = 1..max(j.Run,1)),
// covering a recycled-then-advanced branch history — not just the current
// CellBranch. Used by the PR-repair pass (daemon.go) to tell a job's own PRs
// apart from another job's foreign ones attached by the historical raw-text
// scan; exact matches only; pure, so it's table-testable without gh.
//
// It enumerates TWO families: the current carl/<slug>-<id> names and the
// pre-cutover carl/<slug> ones. The legacy half is not tidiness — PRs opened
// before the id landed sit on bare-slug branches (four were open on the day
// this changed), and dropping those names from the family makes the repair
// pass stop recognising a job's own PRs as its own. That failure is silent
// and looks like PRs detaching from their cards.
//
// The legacy names are matched, never created: branchForRun is the only
// writer and it always emits the id form.
//
// A RENAMED job's earlier names are matched too, on exactly the same
// reasoning one level along: a rename does not touch git — there is no
// `git branch -m` anywhere in this feature — so every branch the job has ever
// pushed, and every open PR's head, still carries an older slug. Dropping
// those names would make the repair pass stop recognising the job's own PRs
// the moment it is renamed, which is the same silent PR-detachment the legacy
// half exists to prevent.
func branchFamily(j *Job) map[string]bool {
	maxRun := j.Run
	if maxRun < 1 {
		maxRun = 1
	}
	// job files are hand-editable; a run beyond 64 is certainly a typo (e.g.
	// `run: 2000000000`), and capping only shrinks the family — it never
	// drops a real branch, since runs never get anywhere near this bound.
	if maxRun > 64 {
		maxRun = 64
	}
	fam := make(map[string]bool, maxRun*2)
	addBranchRuns(fam, cellName(j), maxRun)
	addBranchRuns(fam, util.Slugify(j.Slug), maxRun)
	for _, f := range j.FormerSlugs {
		if name := formerCellName(j, f); name != "" {
			addBranchRuns(fam, name, maxRun)
		}
		if s := util.Slugify(f); s != "" {
			addBranchRuns(fam, s, maxRun)
		}
	}
	return fam
}

// addBranchRuns adds carl/<base> and carl/<base>-r2..rN to fam — one name's
// whole run history, in branchForRun's own spelling. Extracted so the current
// name, the pre-cutover bare-slug name and every former name are all
// enumerated by ONE rule: three hand-written loops is three chances for the
// families to disagree about what -rN looks like.
func addBranchRuns(fam map[string]bool, base string, maxRun int) {
	if base == "" {
		return
	}
	for r := 1; r <= maxRun; r++ {
		if r >= 2 {
			fam[fmt.Sprintf("carl/%s-r%d", base, r)] = true
		} else {
			fam["carl/"+base] = true
		}
	}
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

// cellWindowColor is the tmux colour for a job's cell window: the job's
// chosen colour when it has one, else the existing slug hash. Unset stays
// on jobColor rather than falling back to some palette default, so a job
// that never picked a colour looks exactly as it always did.
func cellWindowColor(j *Job) int {
	if c, ok := ColorByName(EffectiveColor(j)); ok {
		return c.Term256
	}
	return jobColor(util.Slugify(j.Slug))
}

// setCellWindowColor paints the cell's zeus window (window 0) with
// cellWindowColor. Best-effort like every other cosmetic tmux option here —
// a dead session just means nothing to paint. Called at both spawn paths
// AND from syncCellColor, so a live recolour moves the window label and the
// prompt bar together instead of leaving them disagreeing for the run.
func setCellWindowColor(j *Job) {
	_ = exec.Command("tmux", "set-window-option", "-t", CellSession(j)+":0",
		"window-status-style", fmt.Sprintf("fg=colour%d", cellWindowColor(j))).Run()
}

// liveSessionTimes snapshots current tmux session names along with each
// one's creation time (tmux's session_created, unix seconds); no tmux server
// reads as no sessions. One tmux call covers both liveness and freshness —
// callers that only need liveness use the LiveSessions() wrapper below.
func liveSessionTimes() map[string]time.Time {
	live, err := listSessionTimes()
	if err != nil {
		return map[string]time.Time{}
	}
	return live
}

// listSessionTimes is liveSessionTimes with the tmux failure SURFACED rather
// than folded into an empty map. Almost every caller wants the fold — a
// snapshot is advisory and an unreachable tmux legitimately reads as "nothing
// live" for a board render. ProbeCellSession is the exception, because it has
// to answer whether a cell is CONFIRMED dead, and an empty map is the exact
// signature of both "no sessions exist" and "tmux could not be asked": one is
// evidence, the other is the absence of it, and stamping a job on the second
// is how a live agent gets orphaned.
func listSessionTimes() (map[string]time.Time, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name} #{session_created}").Output()
	if err != nil {
		return nil, err
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
	return live, nil
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
// flags are spelled out here. The model always rides along as --model: the
// caller's resolved model when it has one, else core.DefaultModel. Three call
// sites share this — zeusSpawnArgv below (both zeus paths, resolved through
// zeusModel, so a job's model choice survives reactivation),
// threadSpawnCommand (a thread's branch and revive alike), which passes j.Model
// directly and so lands on core.DefaultModel when the job set none, and
// irisSpawnCommand, which passes core.IrisModel directly — the one caller with
// no fallback resolution at all, since iris belongs to no job.
func cellClaudeArgs(model string) []string {
	args := []string{core.ClaudeLauncher(), "--dangerously-skip-permissions", "--rc"}
	return append(args, core.ModelArgs(model)...)
}

// zeusModel resolves the model a cell's zeus is spawned on: the job's model:
// field when it set one, else core.ZeusModel — Fable, not the core.DefaultModel
// everything else in the binary starts on. The FALLBACK is what is zeus-only: a
// side thread passes j.Model to cellClaudeArgs unresolved (thread.go), so it
// honours the same override but falls back to DefaultModel, since a thread is
// carl asking about the work rather than doing it. Zeus' teammates are a third
// mechanism again and take neither (`model: opus`, pinned in its brief —
// crew.go).
func zeusModel(j *Job) string {
	if j.Model != "" {
		return j.Model
	}
	return core.ZeusModel
}

// zeusSpawnArgv is the argv both zeus spawn paths build — StartCell with no
// resume args, ReactivateJob with `-r <session>`. They were two hand-written
// lines that nothing pinned: zeusModel and both model constants have their own
// tests, but the line WIRING them had none, and an unreferenced func is not a
// compile error — so reverting the two call sites would have left this helper
// as dead code with nothing failing. One seam both paths go through is a seam a
// test can hold; TestZeusSpawnPathsResolveTheModel (cell_test.go) holds it.
func zeusSpawnArgv(j *Job, resume ...string) []string {
	argv := append(cellClaudeArgs(zeusModel(j)), "--name", ZeusName(j))
	return append(argv, resume...)
}

// zeusBriefPath and spawnPromptPath name the two spawn inputs that are staged
// as FILES rather than passed on the tmux command line: zeus' system prompt
// and its initial prompt. Both are kraken-owned and live beside the worktree
// in the cell dir (never in CellArtifacts — they are plumbing, not
// deliverables), and both are rewritten on every spawn. See spawnCommand.
//
// existingCellDir rather than cellDir, and it is load-bearing rather than
// tidy: stagedCommand os.WriteFile's these with no MkdirAll, so a path under a
// directory that does not exist is a FAILED SPAWN. A renamed job's cell dir is
// still under its old name (a rename moves no directory), which would make the
// limit rescue's respawn — the one spawn that happens to an already-running
// cell — fail outright on every renamed job. For a never-renamed job this
// resolves to cellDir either way.
func zeusBriefPath(j *Job) string {
	return filepath.Join(existingCellDir(j), "zeus-brief.md")
}

func spawnPromptPath(j *Job) string {
	return filepath.Join(existingCellDir(j), "zeus-prompt.md")
}

// spawnInput is one content-driven argument of a spawn command: the flag it
// rides on, its body, and the file the body is staged into. An empty body
// means "omit this argument entirely" — see stagedCommand.
type spawnInput struct{ flag, body, path string }

// stagedCommand renders the shell command a kraken-spawned claude pane runs.
// Fixed arguments (the launcher, flags, --name, --session-id, -r) go through
// ShellCmdString; the inputs whose length is driven by content rather than by
// kraken — a system prompt, an initial prompt — are written to their files and
// referenced with command substitution instead. Inputs are appended in the
// order given, so the caller keeps `--` last.
//
// Passing them inline is what broke: tmux caps a command at ~16KB and rejects
// anything longer with a bare `command too long`, so once the brief grew past
// that ceiling EVERY cell spawn failed, and each failure left its
// already-created worktree behind — which then masked the real error with a
// stale "worktree already exists" on every subsequent daemon tick. Staged
// files keep the command a few hundred bytes regardless of how long a brief or
// a job spec gets, and leaving them on disk afterwards makes what the agent was
// actually launched with readable after the fact.
//
// Both spawn families share this one implementation: zeus (spawnCommand, just
// below) and a side thread (threadSpawnCommand, thread.go). They differ only in
// which files they stage into — the ~16KB ceiling and the empty-input rule are
// identical for both, and a fix to either belongs in one place.
func stagedCommand(argv []string, inputs ...spawnInput) (string, error) {
	cmd := core.ShellCmdString(argv)
	for _, in := range inputs {
		if in.body == "" {
			continue
		}
		if err := os.WriteFile(in.path, []byte(in.body), 0o644); err != nil {
			return "", fmt.Errorf("write spawn input %s: %v", in.path, err)
		}
		cmd += " " + in.flag + " " + core.ShellReadFileArg(in.path)
	}
	return cmd, nil
}

// spawnCommand is stagedCommand for zeus: its brief and its initial prompt,
// staged into the cell dir's two well-known paths. `--` must stay last, so the
// prompt is appended after the brief flag.
func spawnCommand(j *Job, argv []string, brief, prompt string) (string, error) {
	return stagedCommand(argv,
		spawnInput{"--append-system-prompt", brief, zeusBriefPath(j)},
		spawnInput{"--", prompt, spawnPromptPath(j)})
}

// cellSpawnEnv is the environment the `tmux new-session` that creates a cell
// runs under: this process' own, with the directory holding the resolved `go`
// prepended to PATH. nil means "changed nothing" — an exec.Cmd with a nil Env
// inherits, so the spawn is byte-for-byte what it was before this existed.
//
// The problem it fixes. `kraken serve` is a launchd agent, and launchd hands
// its jobs their own PATH rather than a login shell's; on this machine the only
// `go` lives in /usr/local/go/bin. The plist does name that directory, but
// `launchctl kickstart -k` — how every install restarts the daemon — relaunches
// from launchd's cached in-memory job definition and never re-reads the plist,
// so a daemon that has been kickstarted since before that line was added is
// still running without it. The result inside a cell was that a bare `go test`
// / `go build` / `gofmt` failed, and agent after agent independently
// rediscovered the same `export PATH=$PATH:/usr/local/go/bin` workaround, then
// prefixed every later command with it. One of them never recovered and closed
// its review with "formatting is tooling-unverified".
//
// Why the CLIENT's environment is the lever, and why this reaches the whole
// crew. tmux builds a new pane's environment from the environment of the
// process that issued the tmux command, not from the server's and not from the
// session's — `new-session -e PATH=…` is set in the session environment and
// then overridden, so it does nothing at all (other variables set with -e do
// survive, which makes it a convincing dead end). Since a pane's PATH is
// whatever its spawning client had, and zeus is what issues the `tmux
// new-window` for each teammate Claude Code spawns under teammateMode: tmux,
// setting it once on this one command propagates transitively: zeus' pane, the
// hephaestus/athena/hermes panes it opens, and anything they open in turn.
//
// Prepended, never replaced (util.PrependToolDir): an operator-set PATH is a
// preference, and everything else a cell shells out to — git, gh, claude,
// kraken itself in ~/.local/bin — still has to be findable.
func cellSpawnEnv() []string {
	goPath, err := lookToolchain("go") // PATH first, then the known install dirs
	if err != nil {
		// Best effort by construction. A cell that refused to start because a
		// toolchain probe failed would be far worse than the papercut above.
		return nil
	}
	return util.ToolchainEnv(os.Environ(), goPath)
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

// rotationGate is a package-level indirection to RotationGate. TestMain
// (testmain_test.go) swaps it to a no-op for the whole jobs test binary —
// StartCell/ReactivateJob must never shell out to the real keychain/network
// from a test that spawns a cell, even one that doesn't care about rotation
// at all; a test that specifically wants to assert on gate invocation
// re-swaps it locally.
var rotationGate = RotationGate

// StartCell creates a job's cell: worktree off the base ref, tmux session,
// zeus pane, identity marker, state write. Callers have validated the job
// (parseable, unstarted, non-empty spec) — this owns mechanics only. Its one
// caller is SubmitJob (submit.go), which is the single start surface every
// door funnels through; the `working` write is StartCell's own, made only
// once the cell is actually up, so a failure here leaves the job draft.
func StartCell(j *Job, repo string) error {
	// Loud refusal before anything is created — see checkCellIdentity. A job
	// with no store id would otherwise silently produce ~/.kraken/cells/<slug>-0.
	if err := checkCellIdentity(j); err != nil {
		return fmt.Errorf("cannot start cell: %w", err)
	}
	sess := CellSession(j)
	if LiveSessions()[sess] {
		return fmt.Errorf("cell %s is already live (k o %s to jump in)", sess, sess)
	}
	if old := ExistingWorktree(j, repo); old != "" {
		return fmt.Errorf("worktree %s already exists — clean it up first (git -C %s worktree remove %s)", old, repo, old)
	}
	// below the cheap rejections above (already-live, leftover worktree): a
	// rejected spawn must not pay for a usage fetch or a swap
	rotationGate("start " + j.Slug)
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
	// cellDir, not existingCellDir: a cell being created now carries the job's
	// CURRENT name. The probe exists for reading a cell that already exists
	// under an older one.
	for _, d := range []string{cellDir(j), cellArtifactsIn(cellDir(j))} {
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
	installHookSettings(wt, j)
	installSkills(wt, j)
	linkDevEnv(repo, wt, j)
	// graphContext appends what the knowledge graph already knows about this
	// job, or "" — which is the common case and the one that matters: a job
	// with no match gets a byte-identical brief. It is concatenated HERE
	// rather than inside zeusBrief because two tests pin that function
	// (TestZeusBriefDoesNotRestateSkills, TestZeusBriefStaysLean's 13,500-byte
	// ceiling), and retrieved facts are variable-length and job-dependent:
	// folding them in would make the lean ceiling start failing on graph
	// content rather than on the policy creep it exists to catch.
	launch, err := spawnCommand(j, zeusSpawnArgv(j), zeusBrief(j)+graphContext(j), jobPrompt(j))
	if err != nil {
		return fmt.Errorf("%v (worktree left at %s)", err, wt)
	}
	var newSessionStderr bytes.Buffer
	newSessionCmd := exec.Command("tmux", "new-session", "-d",
		"-x", strconv.Itoa(core.DetachedSessionCols), "-y", strconv.Itoa(core.DetachedSessionRows),
		"-s", sess, "-c", wt,
		"-n", util.Slugify(j.Slug), launch)
	// the pane's PATH is this process' PATH: see cellSpawnEnv
	newSessionCmd.Env = cellSpawnEnv()
	newSessionCmd.Stderr = &newSessionStderr
	if err := newSessionCmd.Run(); err != nil {
		if newSessionStderr.Len() > 0 {
			return fmt.Errorf("tmux new-session: %v: %s (worktree left at %s)", err, strings.TrimSpace(newSessionStderr.String()), wt)
		}
		return fmt.Errorf("tmux new-session: %v (worktree left at %s)", err, wt)
	}
	// cell identity marker: reap verifies it before any teardown
	_ = exec.Command("tmux", "set-option", "-t", sess, "@kraken-job", util.Slugify(j.Slug)).Run()
	setCellWindowColor(j)
	// fresh worktree paths trigger claude's folder-trust dialog, which blocks
	// startup until answered
	acceptTrustPrompts(sess)
	// always "working": there is no plan gate to sit in — every job
	// waits on /do before phase 2, but that's zeus' own brief, not a state
	if err := WriteJobState(j, "working"); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: cell is up but state write failed: %v\n", err)
	}
	return nil
}

// prHookCommand builds the exact `kraken pr-hook` command string a cell's
// PostToolUse hook runs — shared by krakenHookEntry (the entry structure
// below) and mergeHookSettings' hasHookCommand idempotency check, so the
// two can never drift out of sync (a mismatch there would re-append a
// "new" hook entry on every single hook install/reactivation, even when
// nothing about it actually changed).
//
// E7: carries --serve-addr when kraken serve (SetHookServeAddr,
// prhook.go) recorded a bind address different from the hook's own
// fallback default (defaultServeAddr) — a pr-hook invocation is a brand
// new OS process spawned by this command, inheriting none of serve's
// flags/env, so anything serve resolved that isn't the default has to be
// baked into the command string itself. Likewise --db-path when
// KRAKEN_DB_PATH is set in this (kraken serve's own) process environment.
// File-mode `k daemon`/`k web` cells never call SetHookServeAddr, so their
// hook commands are byte-identical to before E7.
func prHookCommand(slug, krakenBin string) string {
	return krakenBin + " pr-hook" + hookCommandFlags() + " " + slug
}

// hookCommandFlags renders the flag run every kraken hook command carries,
// shared by prHookCommand and limitHookCommand so the two verbs can never
// disagree about which serve a cell's hooks talk to. Both hooks run as fresh
// OS processes spawned by Claude Code, inheriting none of kraken serve's
// flags or env, so anything serve resolved that isn't the default has to be
// baked into the command string itself. Empty in the common case, which is
// what keeps a default-configured cell's hook commands byte-identical to the
// ones that predate this.
func hookCommandFlags() string {
	flags := ""
	if addr := currentHookServeAddr(); addr != "" && addr != defaultServeAddr {
		flags += " --serve-addr " + addr
	}
	if dbPath := os.Getenv("KRAKEN_DB_PATH"); dbPath != "" {
		flags += " --db-path " + dbPath
	}
	return flags
}

// limitHookCommand builds the exact `kraken limit-hook` command string a
// cell's StopFailure hook runs — prHookCommand's sibling in every respect,
// including being shared by limitHookEntry and mergeHookSettings'
// idempotency check for the reason spelled out above: a drift between the
// builder and the dedup check re-appends a "new" entry on every install.
func limitHookCommand(slug, krakenBin string) string {
	return krakenBin + " limit-hook" + hookCommandFlags() + " " + slug
}

// krakenHookEntry is the PostToolUse matcher group kraken installs into a
// cell's worktree: a Bash hook that reports every `gh pr create` zeus runs
// straight to `kraken pr-hook <slug>`, so a PR attaches to the board the
// instant it's opened instead of waiting for the next scan/poll. krakenBin
// is the resolved kraken binary — hooks run in a non-interactive shell where
// ~/.local/bin may not be on PATH, so a bare "kraken" only works as a
// fallback.
func krakenHookEntry(slug, krakenBin string) map[string]any {
	return map[string]any{
		"matcher": "Bash",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": prHookCommand(slug, krakenBin),
			},
		},
	}
}

// recallHookCommand builds the exact `kraken recall-hook` command string a
// cell's recall hooks run. Unlike prHookCommand it is not doing
// double duty as an identity test — mergeHookSettings replaces the recall
// entry rather than matching it, precisely because this string CHANGES
// between installs when serve's address does.
//
// It renders its flags through the family's shared hookCommandFlags — the
// propagation problem is identical to pr-hook's and limit-hook's: a
// recall-hook invocation is a brand new OS process spawned by this string,
// inheriting none of serve's flags or env. What it does NOT carry is a slug:
// recall is keyed on the SESSION named in the hook payload, and a cell's
// zeus, its side threads and its teammates all fire this hook from the same
// worktree with different sessions.
func recallHookCommand(krakenBin string) string {
	return krakenBin + " recall-hook" + hookCommandFlags()
}

// recallHookEntry is the hook group kraken installs under each of
// recallHookEvents. Unlike krakenHookEntry it carries NO matcher, for a
// different reason per event: UserPromptSubmit has no tool to match on (a
// matcher key there is at best ignored and at worst a config Claude Code
// refuses to load), and on PostToolUse the absence is the point — every tool
// call qualifies, since which tool ran says nothing about whether the
// conversation just turned toward something the graph knows.
func recallHookEntry(krakenBin string) map[string]any {
	return map[string]any{
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": recallHookCommand(krakenBin),
			},
		},
	}
}

// limitHookEntry is the StopFailure matcher group kraken installs beside the
// PostToolUse one: a hook that reports a claude dying on a usage limit to
// `kraken limit-hook <slug>`, which is how the daemon learns a cell is stuck
// behind an exhausted account without anyone watching the pane.
//
// The matcher is "rate_limit" because that is the value Claude Code puts in
// the payload's `error` field for EVERY branch of its HTTP-429 handler,
// subscription-window limits included, and the StopFailure matcher matches
// against that value rather than a tool name (which is what the PostToolUse
// entry above matches on).
func limitHookEntry(slug, krakenBin string) map[string]any {
	return map[string]any{
		"matcher": "rate_limit",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": limitHookCommand(slug, krakenBin),
			},
		},
	}
}

// recallHookEvents are the hook events mid-conversation recall rides on.
// UserPromptSubmit queries on carl's new message, landing in front of the
// agent before it starts thinking; PostToolUse queries on the assistant's
// last text after every tool call, which is what keeps memory arriving as a
// long turn moves through new territory. PostToolUse is safe where Stop is
// not because its additionalContext lands in a turn that is ALREADY
// continuing — nothing is resumed. Its per-tool-call frequency is paid for
// server-side: concurrent firings for one session serialize on a per-session
// mutex in Recall (parallel tool calls fire parallel hooks, which would
// otherwise race the skip-set read against the mark and double-inject), and
// a firing whose query matches the session's last completed one — every
// firing after the first within one assistant message — short-circuits
// before retrieval, so the steady-state cost is one local round trip.
//
// STOP IS DELIBERATELY NOT HERE, and the reason is a property of Claude Code
// rather than a preference. Its hooks reference is explicit that
// additionalContext at Stop CONTINUES the turn — "The turn doesn't stop; it
// continues so Claude can read the feedback" — so a Stop injection is not an
// injection, it is a resume. Wired, it would resume every agent straight past
// zeus' own stop-gates (the manual-test gate, the /do gate), hold blocked
// cells at "working" in the daemon's roll call by keeping their zeus running,
// and chain paid model calls until retrieval finally ran dry. A feature whose
// whole contract is "invisible when it has nothing to say" cannot be wired to
// an event where saying something restarts the conversation.
//
// It is a list rather than a constant because RunRecallHook is event-agnostic
// (it echoes hook_event_name verbatim and Recall never reads it): wiring or
// unwiring an event is a change here and nowhere else.
var recallHookEvents = []string{"UserPromptSubmit", "PostToolUse"}

// mergeHookSettings renders a cell's .claude/settings.json: kraken's
// PostToolUse hook entry (krakenHookEntry) appended into whatever's already
// there. existing empty/nil renders a fresh file. Non-empty existing bytes
// are merged non-destructively — unrelated top-level keys and any PostToolUse
// groups the repo's own checked-in settings.json already declares survive
// alongside kraken's, never clobbered. An error return means existing wasn't
// valid JSON; the caller must leave the file alone rather than overwrite a
// repo's own config. Idempotent: if an existing PostToolUse group already
// carries the exact command kraken would install (matched by command string,
// since that's what encodes slug + binary path), nothing is appended — a
// second installation path (e.g. reactivation recreate alongside a preserved
// StartCell install) can't duplicate the entry.
//
// It renders FIVE kraken groups across FOUR hook events: the pr-hook group
// under PostToolUse, the bash-lint group under PreToolUse, the limit-hook
// group under StopFailure (limitHookEntry, the usage-limit rescue), and a
// recall group under each of recallHookEvents — which includes PostToolUse,
// so that key ends up holding the pr-hook group AND a recall group side by
// side. The two kinds use DIFFERENT merge rules and the difference is
// deliberate — pr-hook, bash-lint and limit-hook append when their exact
// command is absent, recall REPLACES — for the reason spelled out at the
// recall loop below; the non-obvious consequence is that the recall loop runs
// its removal pass over the very list the pr-hook append just wrote, which is
// why withoutRecallHooks must never match a pr-hook command. Either way,
// groups kraken did not write and unrelated top-level keys survive untouched.
func mergeHookSettings(existing []byte, slug, krakenBin string) ([]byte, error) {
	parsed := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &parsed); err != nil {
			return nil, err
		}
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		hooks = map[string]any{}
	}
	ensureHookGroup(hooks, "PostToolUse", prHookCommand(slug, krakenBin), krakenHookEntry(slug, krakenBin))
	ensureHookGroup(hooks, "PreToolUse", bashLintCommand(krakenBin), bashLintHookEntry(krakenBin))
	// The usage-limit hook (limithook.go): StopFailure fires when a turn dies
	// on an API error, and the entry's matcher narrows it to the rate_limit
	// error type — the bucket every 429 lands in, subscription plan limits
	// included. Append-if-absent like its two neighbours above, since the
	// command carries the slug and the binary path and is therefore its own
	// identity test.
	ensureHookGroup(hooks, "StopFailure", limitHookCommand(slug, krakenBin), limitHookEntry(slug, krakenBin))
	// The recall hook (recall.go) is REPLACED rather than appended-if-absent,
	// which is the one place this function's two halves differ. PostToolUse's
	// entry is keyed by slug AND binary path, so an exact-command match is a
	// good identity test for it. A recall command carries neither — it is the
	// binary plus flags — so the only thing that varies between installs is
	// --serve-addr/--db-path, and append-if-absent handled that variation in
	// the worst possible way: a serve restart on a different port left the OLD
	// entry in place and added a second one, so every prompt in that cell then
	// ran two hooks, one of them POSTing at a dead address. Dropping every
	// group whose command mentions " recall-hook" and re-adding the current
	// one kills the stale entry and the duplicate together, and makes re-merge
	// byte-identical rather than merely non-duplicating.
	for _, event := range recallHookEvents {
		groups, _ := hooks[event].([]any)
		// A fresh entry per event rather than one shared map: with two wired
		// events, one shared entry would be the SAME mutable map in two
		// places in the tree, where a later mutation through one key silently
		// edits the other.
		hooks[event] = append(withoutRecallHooks(groups), recallHookEntry(krakenBin))
	}
	// Assigned once, unconditionally: with the PostToolUse merge and the
	// recall loop above both writing into `hooks`, every branch that could
	// leave it detached from `parsed` (an absent key, a "hooks" that wasn't
	// an object) is covered by one line rather than by each branch
	// remembering.
	parsed["hooks"] = hooks
	mergeTeammateSettings(parsed)
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// ensureHookGroup appends entry to hooks[kind] unless some group there
// already carries exactly command (hasHookCommand) — the one shared
// implementation of the append-if-absent dance kraken's two matcher hooks
// do, so a third is a one-liner. It does NOT assign parsed["hooks"]; the
// caller does that once, unconditionally, at the end.
//
// A hooks[kind] that EXISTS but is not a JSON array is left alone entirely:
// overwriting it would clobber a repo's own (however malformed) config, the
// exact move mergeHookSettings promises not to make. The recall loop below
// is deliberately not routed through this — it REPLACES rather than
// appends, for the reason stated there.
func ensureHookGroup(hooks map[string]any, kind, command string, entry map[string]any) {
	groups, ok := hooks[kind].([]any)
	if !ok {
		if _, exists := hooks[kind]; exists {
			return
		}
		groups = nil
	}
	if !hasHookCommand(groups, command) {
		hooks[kind] = append(groups, entry)
	}
}

// mergeTeammateSettings turns on the agent-teams backing that zeus' brief
// spends a paragraph depending on. The brief tells zeus that naming an Agent
// call buys it a real tmux pane, a row core.nestTeams can hang under the cell,
// and a SendMessage address — but every one of those is a property of Claude
// Code's teams feature, which is off unless CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS
// is set and teammateMode picks the tmux backend. Left to the user's personal
// ~/.claude/settings.json, the whole crew silently degrades to in-process
// subagents the moment that file changes — with the brief still confidently
// describing panes that no longer exist. Writing both into the cell's own
// settings.json makes the claim self-contained: kraken states the requirement
// where it enforces it.
//
// Merged, never clobbered, matching the hook entry beside it: an existing env
// block keeps its other keys, and a repo that has deliberately chosen its own
// teammateMode keeps that choice rather than having kraken overrule it.
func mergeTeammateSettings(parsed map[string]any) {
	env, ok := parsed["env"].(map[string]any)
	if !ok {
		env = map[string]any{}
	}
	if _, set := env[teamsEnvVar]; !set {
		env[teamsEnvVar] = "1"
	}
	parsed["env"] = env
	if _, set := parsed["teammateMode"]; !set {
		parsed["teammateMode"] = "tmux"
	}
}

// teamsEnvVar gates Claude Code's agent-teams feature. Named here rather than
// inlined because it is the single string the cell's whole delegation model
// rests on: if it is renamed upstream (or the feature graduates and the flag
// disappears), this constant is what a grep for the breakage lands on.
const teamsEnvVar = "CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS"

// hasHookCommand reports whether any group in groups (one hook event's list)
// already contains a hook whose command exactly matches command.
func hasHookCommand(groups []any, command string) bool {
	for _, g := range groups {
		group, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hookList, ok := group["hooks"].([]any)
		if !ok {
			continue
		}
		for _, h := range hookList {
			hook, ok := h.(map[string]any)
			if !ok {
				continue
			}
			if cmd, ok := hook["command"].(string); ok && cmd == command {
				return true
			}
		}
	}
	return false
}

// recallHookMarker is what identifies a hook group as kraken's recall entry
// when the exact command string cannot be relied on — which is every install
// after the first with a different --serve-addr or --db-path. It is the verb
// with its leading space, so it matches the command kraken writes and cannot
// match a path component of some unrelated tool.
const recallHookMarker = " recall-hook"

// withoutRecallHooks returns groups with every group dropped that contains a
// hook whose command mentions recallHookMarker — the removal half of
// mergeHookSettings' replace semantics. A command that also mentions
// " pr-hook" is exempt: `kraken pr-hook recall-hook` — the pr-hook command of
// a job SLUGGED recall-hook — contains the marker as its slug argument, and
// without the exemption the recall replace pass (which runs over PostToolUse,
// where the pr-hook group lives) would silently drop that cell's pr-hook on
// every install.
//
// A whole GROUP goes, not the matching hook inside it, because kraken writes
// its recall entry as a group of exactly one hook: a group carrying kraken's
// command plus somebody else's would have to be a hand-edit of kraken's own
// entry, and there is no reading of that under which the right answer is to
// keep half of it. A group whose shape isn't the expected object/list is left
// alone — this function's job is removing what kraken put there, not
// normalizing a repo's own config.
func withoutRecallHooks(groups []any) []any {
	var out []any
	for _, g := range groups {
		if group, ok := g.(map[string]any); ok &&
			groupMentions(group, recallHookMarker) && !groupMentions(group, " pr-hook") {
			continue
		}
		out = append(out, g)
	}
	return out
}

// groupMentions reports whether any hook in one settings group carries a
// command containing sub.
func groupMentions(group map[string]any, sub string) bool {
	hookList, ok := group["hooks"].([]any)
	if !ok {
		return false
	}
	for _, h := range hookList {
		hook, ok := h.(map[string]any)
		if !ok {
			continue
		}
		if cmd, ok := hook["command"].(string); ok && strings.Contains(cmd, sub) {
			return true
		}
	}
	return false
}

// installHookSettings writes (or merges kraken's entries into) the cell's
// project-scoped Claude Code hook config, <wt>/.claude/settings.json. Three
// kraken groups go in, across two events: a Bash-matcher PostToolUse group so
// zeus' `gh pr create` calls self-report to `kraken pr-hook`, plus a
// matcher-less recall group under each of recallHookEvents (UserPromptSubmit
// and PostToolUse) so every prompt AND every tool call in the cell asks
// `kraken recall-hook` whether the knowledge graph has anything to add
// (recall.go). Called from StartCell after the worktree is up and before
// claude launches in it.
//
// Never fails the cell, and each kind is safe to lose for its own reason:
// PR attachment still happens via the daemon's scan/poll paths, and recall is
// an enhancement whose absence is a cell that simply remembers nothing. So any
// failure here is a stderr warning, not an error return.
func installHookSettings(wt string, j *Job) {
	slug := util.Slugify(j.Slug)
	dir := filepath.Join(wt, ".claude")
	path := filepath.Join(dir, "settings.json")
	var existing []byte
	if data, err := os.ReadFile(path); err == nil {
		existing = data
	} else if !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "kraken: hook install for %s: read %s: %v\n", j.Slug, path, err)
		return
	}
	krakenBin, err := os.Executable()
	if err != nil {
		krakenBin = "kraken"
	}
	merged, err := mergeHookSettings(existing, slug, krakenBin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "kraken: hook install for %s: %s is not valid JSON — leaving it alone: %v\n", j.Slug, path, err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: hook install for %s: mkdir %s: %v\n", j.Slug, dir, err)
		return
	}
	if err := os.WriteFile(path, merged, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: hook install for %s: write %s: %v\n", j.Slug, path, err)
	}
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
// this job (via the @kraken-job marker) before killing it. slugs is every name
// the marker may legitimately carry, most-current first; a session marked with
// any of them is this job's.
//
// More than one name because the marker is stamped at SPAWN and a rename
// restamps it best-effort (rename.go's convergeCellTmux): a cell whose
// convergence failed and was never healed still carries an older slug, and a
// teardown that refuses on that would leave carl with a cell he cannot kill
// from the board at all. The check is still a real one — an unmarked session,
// or one marked for a name that is not this job's, is refused exactly as
// before.
func VerifyCellSession(sess string, slugs ...string) error {
	got, marked := cellSessionMarker(sess)
	if !marked {
		return fmt.Errorf("session %s has no @kraken-job marker — not killing it; investigate manually", sess)
	}
	for _, s := range slugs {
		if got == s {
			return nil
		}
	}
	want := "(none given)"
	if len(slugs) > 0 {
		want = strconv.Quote(slugs[0])
	}
	return fmt.Errorf("session %s is marked for job %q, not %s — not killing it; investigate manually", sess, got, want)
}

// cellSessionMarker reads a session's @kraken-job marker: its value, and
// whether the session carries one AT ALL. Absence covers both shapes tmux
// answers an unset user option with — a non-zero exit and an empty value —
// because every caller's question is "is there evidence here", not "which
// tmux is installed".
//
// Absence is a real steady state rather than a suspicious one: StartCell
// stamps the marker with a DISCARDED error, and every cell spawned before the
// marker existed has none. The two callers weigh that differently on purpose
// (see VerifyCellSession, which refuses, and convergeCellTmux's
// verifyConvergeTarget, which accepts the session NAME as evidence instead),
// which is why reading it is split out from judging it.
func cellSessionMarker(sess string) (string, bool) {
	out, err := exec.Command("tmux", "show-options", "-t", sess, "-v", "@kraken-job").Output()
	if err != nil {
		return "", false
	}
	got := strings.TrimSpace(string(out))
	return got, got != ""
}

// CellMarkerSlugs is every @kraken-job value a cell of this job may carry:
// the current slugified slug first, then one per former slug. The order is
// the one VerifyCellSession reports on a mismatch, so the current name is what
// a refusal names.
//
// Exported for the one caller outside this package: cmd/kraken's abandon verb
// verifies a session before killing it exactly as teardownCell does, and a
// single-slug call there refuses every renamed cell whose marker restamp
// failed — with a message ("investigate manually") the viewer gives no way to
// act on.
func CellMarkerSlugs(j *Job) []string {
	cands := cellNameCandidates(j)
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.Slug)
	}
	return out
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
		return fmt.Errorf("refusing to tear down %s from inside it — use the board's Done action, or k k, from outside the cell", target)
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
// demo-job incident). force is the explicit human sign-off (board Done, kill),
// where dirt is listed and discarded instead.
func teardownCell(j *Job, repo string, live map[string]bool, force bool) error {
	// Same guard as StartCell, and here it is the DESTRUCTIVE direction that
	// matters: this function ends in os.RemoveAll(cellDir(j)), and a job with
	// no id would aim that at a directory belonging to no job — or, with an
	// empty slug, at something far worse.
	if err := checkCellIdentity(j); err != nil {
		return fmt.Errorf("cannot tear down cell: %w", err)
	}
	// every name this cell could be running under, not just the current one:
	// a renamed cell whose tmux convergence failed is still the session this
	// process may be living inside, and self-decapitation is the failure this
	// guard exists for.
	own := ownTmuxSession()
	for _, s := range cellSessionCandidates(j) {
		if err := guardSelfTeardown(s, own); err != nil {
			return err
		}
	}
	if !force {
		if wt := ExistingWorktree(j, repo); wt != "" {
			if dirt, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output(); err == nil && len(strings.TrimSpace(string(dirt))) > 0 {
				return fmt.Errorf("worktree %s has uncommitted changes — not touching the cell (the board's Done action force-discards them)", wt)
			}
		}
	}
	// FindCellSession, so a renamed cell still running under an earlier name
	// is killed rather than left behind as an orphaned session with a live
	// zeus in it — the whole point of teardown being all-or-nothing.
	if sess, ok := FindCellSession(j, live); ok {
		if err := VerifyCellSession(sess, CellMarkerSlugs(j)...); err != nil {
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
	// mandatory final pass (spec Q11): whatever's in the artifacts dir right
	// now is the last chance to mirror it into the store before it's gone —
	// no-op when no store is wired.
	syncArtifactsNow(j, "teardown")
	// the cell's side threads died with the session above (their windows were
	// part of it), so their rows must stop claiming live panes — tmux recycles
	// those ids. Rows survive, pointing at their transcripts; nothing is
	// snapshotted. Never fatal: the cell is already gone, and a stale row is a
	// far smaller problem than a teardown that reports failure after
	// succeeding.
	if err := CloseThreadsForTeardown(j); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: closing %s's thread rows failed: %v\n", j.Slug, err)
	}
	// the cell dir (doc source, artifacts) goes too: artifacts were
	// preserved into pluto before teardown, and the mid-run log in the job
	// file is the durable trail.
	//
	// existingCellDir, because a renamed job's dir is still under the name it
	// was created with — removing cellDir(j) would delete nothing and leave a
	// whole worktree-less cell tree behind with nothing that ever collects it.
	// checkCellIdentity above is what keeps this from ever resolving to
	// something that belongs to no job.
	os.RemoveAll(existingCellDir(j))
	// the colour watermark lived in that dir, and its in-memory half
	// (colorFallback, written only when the file couldn't be) did NOT — drop
	// it here so both halves of one record expire together. Miss this and a
	// job whose watermark write once failed never gets a /color again: the
	// next cell's brand-new zeus reads want == have off stale package memory
	// and stays uncoloured while the board shows the job coloured.
	forgetColorDelivery(j)
	return nil
}

// --- cell health ---

// acceptTrustPrompts answers claude's folder-trust dialog in a cell's fresh
// (single) pane. The worktree is kraken-made from a repo carl submitted a job
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
	// every session name and every thread prefix the job has held: a rename
	// respawns nothing, so a renamed cell may still be running under an older
	// session name with threads still named for an older prefix — and both
	// halves of this election have to see them. Missing the session name finds
	// no zeus at all (a live cell reading as "zeus process is gone"); missing
	// the thread prefix elects a THREAD as zeus, which is the failure the
	// filter below exists for.
	sessions := cellSessionCandidates(j)
	inCell := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		inCell[s] = true
	}
	threadPrefixes := threadNamePrefixes(j)
	// side threads (thread.go) are claudes at depth 0 in this very session, so
	// the positional fallback below would happily elect one as zeus during the
	// window where zeus' own session file isn't written yet — stampSession
	// would then write a THREAD's session id into the job file as zeus'
	// (refused only because it's guessed), CellHealth would report a healthy
	// cell while zeus is dead, and /api/steer would type carl's message into
	// the thread's pane.
	//
	// Both keys are needed, and the pane one carries the dangerous case: the
	// name is EXACTLY what a still-starting thread hasn't got yet, which is the
	// same race the fallback itself exists for. The recorded panes come from
	// the store (empty set on no store, or between a branch's reservation and
	// its window opening), so the name filter stays as the belt to that brace.
	threadPanes := liveThreadPanesFor(j)
	var inSess []*core.Peer
	for _, p := range all {
		if !inCell[p.TmuxSession] {
			continue
		}
		if hasAnyPrefix(p.Name, threadPrefixes) || threadPanes[p.Pane] {
			continue
		}
		if p.Name == ZeusName(j) && p.Depth == 0 {
			return p
		}
		if p.Agent == "claude" && p.Depth == 0 {
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
// Conditions, in order: zeus' process must exist; it must not be stuck on a
// startup prompt; its transcript's final message entry must not classify as
// a stall. That last check reads the TRANSCRIPT, not the pane: pane text is
// agent-writable (a grep result or a quoted error string used to false-
// positive the old ApiErrorRe scrape), and the "esc to interrupt" mid-turn
// guard the scrape relied on no longer appears in Claude Code's UI as of
// ≥2.1.217. core.LastStall's synthetic "api-error" entry, by contrast, is
// only ever appended after Claude Code's own internal retries have already
// given up — so classifying off it is inherently mid-turn-safe, with no
// pane-scrape race to guard against. A guessed session id is refused before
// any file read: resuming/reading a guessed id risks another claude's
// transcript entirely. Use RetryableHealth to decide whether a reason here
// is worth an automatic nudge.
func CellHealth(j *Job, all []*core.Peer) string {
	zeus := CellZeus(j, all)
	if zeus == nil {
		return "zeus process is gone — restart the cell (kill the session, then start it again from the board after cleanup)"
	}
	// found positionally but not yet named: claude is still starting — a
	// lingering startup prompt needs a human, plain slowness needs patience.
	//
	// The comparison is against every zeus name this job has held, not just
	// the current one. Zeus keeps the --name it was spawned with and a rename
	// respawns nothing, so a current-name-only test fails FOREVER on a renamed
	// live cell — CellZeus' positional fallback still finds the process, so
	// this returned "" and the cell read as healthy while the transcript was
	// never opened. Four things went quiet with it, all for exactly the cells
	// the live-rename feature exists for: the daemon's health notification and
	// its auto-retry, the usage-limit rescue's TRANSCRIPT trigger (the arm
	// that covers cells whose settings.json predates the StopFailure hook),
	// the board and viewer health lines, and RetryCell's specific nudge.
	//
	// Still a name check, and still this job's OWN names — the still-starting
	// meaning is preserved exactly: a positionally-found nameless zeus reads
	// as starting, and a peer named for another job still returns "".
	if !isZeusName(j, zeus.Name) {
		if zeus.Status == "question" {
			return "cell agent is stuck on a startup prompt — k o " + CellSession(j) + " to answer it"
		}
		return ""
	}
	if zeus.SessionID == "" || zeus.SessionIDGuessed {
		return ""
	}
	path := core.TranscriptPathForSession(zeus.SessionID)
	if path == "" {
		return ""
	}
	return stallReason(core.LastStall(path))
}

// stallReason turns a transcript stall classification into the CellHealth
// reason string ("" for none/interrupted — an interrupted turn, carl's
// deliberate esc, isn't a stall to auto-retry). Pulled out of CellHealth as
// its own pure function because RetryableHealth's prefix-matching against
// these exact strings couples the two: pinning stallReason with its own
// round-trip test (see cell_test.go) catches wording drift between them
// before it silently breaks auto-retry classification.
func stallReason(st core.Stall) string {
	switch st.Kind {
	case "usage-limit":
		return usageLimitReasonPrefix + st.Text
	case "api-error":
		if st.Transient {
			return "zeus hit an API error: " + st.Text
		}
		return "zeus needs attention: " + st.Text + " — fix and retry by hand"
	default:
		return ""
	}
}

// usageLimitReasonPrefix is the one spelling of a usage-limit CellHealth
// reason: stallReason writes it, IsUsageLimitReason recognizes it, and the
// daemon's health pass routes on that recognizer rather than rebuilding the
// string at the call site.
const usageLimitReasonPrefix = "zeus hit a usage limit: "

// IsUsageLimitReason reports whether a CellHealth reason is the one
// stallReason produces from a core.Stall of Kind "usage-limit" — the cell is
// alive but its account is exhausted. It exists as a named predicate so that
// routing on it is greppable and testable, instead of a prefix literal
// repeated wherever the decision is made.
func IsUsageLimitReason(reason string) bool {
	return strings.HasPrefix(reason, usageLimitReasonPrefix)
}

// RetryableHealth reports whether a CellHealth reason is fixable by a
// nudge: transient API errors are, dead processes and needs-attention
// (credit/auth/prompt-length) stalls are not — and neither is a usage
// limit, which is why it is refused explicitly here rather than left to the
// prefix test below. The two reasons already differ ("zeus hit an API
// error: …" vs "zeus hit a usage limit: …"), so the guard changes no answer
// today; it exists because they agree for ten characters and then diverge
// on wording alone, and the cost of that wording drifting back together is
// a nudge every ten minutes at an account that cannot answer.
func RetryableHealth(reason string) bool {
	if IsUsageLimitReason(reason) {
		return false
	}
	return strings.HasPrefix(reason, "zeus hit an API error")
}

// RetryCell pokes a stalled cell agent: a nudge message resumes a claude
// sitting at its prompt after an errored or interrupted turn. Dead processes
// can't be poked — that's reported, not papered over. Shared by the web
// board's retry and the viewer's health rows.
func RetryCell(j *Job) error {
	if !j.Started() || j.State == "done" {
		return fmt.Errorf("%s has no live cell to retry", j.Slug)
	}
	// FindCellSession rather than the current name alone: a renamed cell whose
	// tmux convergence failed is live under an older session name, and reading
	// it as gone here refuses the nudge for exactly the stalled cell the nudge
	// exists for. Below the two cheap tests above, which keep the tmux
	// shell-out off every call that was never going to reach it.
	if _, live := FindCellSession(j, LiveSessions()); !live {
		return fmt.Errorf("%s has no live cell to retry", j.Slug)
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	reason := CellHealth(j, all)
	// match the literal CellHealth emits, not a loose "is gone" substring:
	// stallReason's non-transient text can now embed arbitrary API-error
	// copy, and "is gone" is common enough in free-form error text that a
	// substring match risked refusing a retry that was actually poke-able.
	if strings.HasPrefix(reason, "zeus process is gone") {
		return fmt.Errorf("%s: %s", j.Slug, reason)
	}
	// a plain nudge resumes a claude sitting at its prompt after an errored
	// or abandoned turn
	zeus := CellZeus(j, all)
	if zeus == nil {
		return fmt.Errorf("%s: agent not found — rerun discovery", j.Slug)
	}
	if reason != "" {
		return core.SendMessage(zeus, "you stalled ("+reason+") — pick up exactly where you left off and continue the job")
	}
	return core.SendMessage(zeus, "you stalled (API error or an interrupted turn) — pick up exactly where you left off and continue the job")
}

// KillJob tears a live job's cell down completely and returns the job to
// draft by clearing state — the only field that ever made it not-a-draft.
// (It used to clear a `ready` flag too, since leaving that set would have
// handed the job straight back to the daemon's queue loop; there is no such
// flag and no such loop now, and nothing restarts a job but carl.) Artifacts
// are preserved first; the worktree goes too (a draft re-submitted later
// needs a clean StartCell).
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
	// dangling marker on a live cell for the daemon to act on later. Every
	// session candidate, exactly as teardownCell loops them — a renamed cell
	// whose convergence failed is running under an OLD name, so the current
	// name alone lets a kill issued from inside that very session past this
	// guard, write the marker, and only then be refused by teardownCell: the
	// dangling marker this ordering exists to prevent.
	own := ownTmuxSession()
	for _, s := range cellSessionCandidates(j) {
		if err := guardSelfTeardown(s, own); err != nil {
			return msgs, err
		}
	}
	if err := WriteJobFields(j, "teardown", "kill"); err != nil {
		return msgs, fmt.Errorf("teardown-intent write failed: %v — not tearing down", err)
	}
	if err := teardownCell(j, repo, LiveSessions(), true); err != nil {
		return msgs, fmt.Errorf("%v (job left in state %s)", err, j.State)
	}
	if err := WriteJobFields(j, "state", "", "teardown", ""); err != nil {
		return msgs, fmt.Errorf("cell torn down, but state write failed: %v", err)
	}
	return append(msgs, "killed "+j.Slug+" — back to draft"), nil
}

// --- accept/done ---

// preserveEntry pairs one preserved file's absolute source path with its
// flattened pluto dest name (artifactPreserveList/preserveArtifacts).
type preserveEntry struct {
	src, dest string
}

// dedupeDestName returns name unchanged the first time it's seen, or a name
// with a -2, -3, ... suffix inserted before its extension when name (or an
// already-generated suffixed variant) collides. Closed under its own
// output: every candidate it tries — the plain name AND every generated
// suffix — is checked against claimed before being returned, and then
// registered into claimed itself (shared across one artifactPreserveList
// call). Counting only the ORIGINAL name's collisions (the pre-fix
// behavior) isn't enough: a walk of x/a.svg, x-a.svg, x-a-2.svg would
// generate "x-a-2.svg" for both the second AND third file, since the third
// file's own literal name was never registered as claimed — the loser was
// silently overwritten in the caller's mind and then destroyed at teardown
// with nothing preserved for it. Deterministic, in walk order.
func dedupeDestName(name string, claimed map[string]int) string {
	cand := name
	for n := 1; claimed[cand] > 0; {
		n++
		ext := filepath.Ext(name)
		cand = fmt.Sprintf("%s-%d%s", strings.TrimSuffix(name, ext), n, ext)
	}
	claimed[cand]++
	return cand
}

// artifactPreserveList walks root (a cell's artifacts dir) and returns one
// preserveEntry (absolute src path, flattened dest name) for every
// non-hidden regular file, recursing into subdirectories exactly like
// ListArtifacts (internal/jobs/status.go) does for the live board view.
// Nested paths are flattened for pluto's flat <slug>.<name> namespace by
// replacing "/" with "-" (figures/a.svg becomes figures-a.svg);
// preserveArtifacts then prefixes <slug>. on top of dest as usual. A dest
// name collision (e.g. figures/a.svg and a top-level file literally named
// figures-a.svg both flatten to "figures-a.svg") is de-duped deterministically
// within this one call via dedupeDestName — otherwise the loser was silently
// dropped and then destroyed at teardown with nothing preserved for it.
// Entries are followed-then-checked for being a regular file (os.Stat, not
// d.Type().IsRegular()): a valid symlink to a regular file is preserved,
// same as ListArtifacts and the store sync both already treat it via
// ReadFile; only a dangling symlink, a directory, or a device is skipped
// (keep()'s ReadFile would otherwise hard-block Done forever on a dangling
// one). A root that's missing, or that exists but isn't a directory, reads
// as "nothing to preserve" (nil, nil). Any other walk error (permission
// denied, I/O failure mid-walk) is propagated rather than swallowed:
// preserveArtifacts gates cell-dir DESTRUCTION on having preserved
// everything, so a partial listing must never read as a complete one.
func artifactPreserveList(root string) ([]preserveEntry, error) {
	fi, err := os.Stat(root)
	if err != nil || !fi.IsDir() {
		return nil, nil
	}
	var out []preserveEntry
	claimed := map[string]int{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err // unreadable entry: abort — a partial listing must not read as complete
		}
		if d.IsDir() {
			if p != root && strings.HasPrefix(d.Name(), ".") {
				return fs.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if pfi, statErr := os.Stat(p); statErr != nil || !pfi.Mode().IsRegular() {
			return nil // dangling symlink, symlinked dir, device, etc: not a real file to copy
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return nil // outside root: shouldn't happen, skip rather than fail the walk
		}
		dest := dedupeDestName(strings.ReplaceAll(filepath.ToSlash(rel), "/", "-"), claimed)
		out = append(out, preserveEntry{src: p, dest: dest})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// preserveArtifacts copies a finishing cell's deliverables into the pluto dir
// as <slug>.<name> (never overwriting): everything in the cell's artifacts/
// dir, recursing into subdirectories, plus uncommitted root-level *.md in
// the worktree (where a solo zeus might leave a doc outside artifacts/) —
// unconditionally: Q14 removed the
// build/info job-type split, so this sweep no longer skips itself for jobs
// that also ship deliverables via a PR; a job's own uncommitted root notes
// are always worth preserving regardless of whether it opened one. Teardown
// deletes both origins.
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
		dst := filepath.Join(PlutoDir(), PlutoArtifactPrefix(j)+name)
		// A collision here used to be routine and silently lost the NEW
		// job's file: the prefix was the bare slug, so an earlier job of the
		// same name already owned the path and this returned quietly, leaving
		// teardown to delete the source. With the id in the prefix a
		// collision can only be this same job's own earlier copy — a genuine
		// re-teardown — so overwriting is the correct resolution rather than
		// skipping, and the destination is refreshed from the live artifact.
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("preserve %s: %v", dst, err)
			}
			return
		}
		copied = append(copied, dst)
	}
	entries, err := artifactPreserveList(CellArtifacts(j))
	if err != nil && firstErr == nil {
		firstErr = fmt.Errorf("list artifacts: %w", err)
	}
	for _, e := range entries {
		keep(e.src, e.dest)
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
		return []string{"state", "", "teardown", ""}, true
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
// cell, stamp state done + outcome. Called from the board's Done action
// (webDone). The caller has validated the job is started and not done.
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
	// dangling marker on a live cell for the daemon to act on later. Every
	// session candidate, exactly as teardownCell loops them — see KillJob's
	// note: the current name alone lets a done issued from inside a renamed,
	// unconverged cell write the marker before the inner guard refuses it.
	own := ownTmuxSession()
	for _, s := range cellSessionCandidates(j) {
		if err := guardSelfTeardown(s, own); err != nil {
			return msgs, err
		}
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

// --- reactivate ---

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
	return "", fmt.Errorf("job has never started — start it from the board instead")
}

// doneReorient builds the done-state reactivation reorient line. The PR
// clause reports only what the kraken-owned prs: record holds: an empty
// record is not proof the job never shipped a PR (the tracker is gh-driven
// and degrades; the repair pass can drop rows) and says nothing about the
// job's path, so the text states the record and points at the governing
// skill rather than asserting history or forbidding a PR — the old
// unconditional boilerplate asserted merged PRs at an explanation job that
// never opened one, and a careful zeus had to contradict its own resume
// prompt to answer correctly.
func doneReorient(j *Job, branch, branchDesc string) string {
	prTail := " No PRs are recorded on this job — re-read the current repo state and the skill governing the phase you are resuming; that skill decides whether this job's path opens a PR at all."
	if len(j.PRs) > 0 {
		prTail = " Your earlier PRs are recorded on the job — re-read the current repo state and their merge state before acting, then ship adjustments as a new PR from the current branch (or as follow-up commits onto a PR for this branch that is still open)."
	}
	return "this job was marked done, but carl has reactivated it for adjustments — it was not truly finished. You are resumed in a fresh cell: same worktree path, on branch " + branch + " " + branchDesc + "." + prTail
}

// reactivatePrompt assembles the resumed claude's prompt from three pure
// inputs: reorient (the mode-specific re-orientation text — resume vs
// recreate, done vs not), message (carl's optional steering), and rebaseNote
// (a rebase instruction, non-empty only in recreate mode when the remote
// branch has drifted from base). A blank message with no rebase needed means
// promptless resume (hasPrompt=false) — nothing is submitted, the session
// just comes up idle. A rebase note with a blank message IS the entire
// prompt, since it's the one thing that must happen before anything else;
// otherwise it's prepended ahead of the re-orientation text.
func reactivatePrompt(reorient, message, rebaseNote string) (prompt string, hasPrompt bool) {
	if rebaseNote == "" && message == "" {
		return "", false
	}
	if rebaseNote == "" {
		return reorient + "\nsteering from carl: " + message, true
	}
	if message == "" {
		return rebaseNote, true
	}
	return rebaseNote + "\n" + reorient + "\nsteering from carl: " + message, true
}

// recreateFromRemote checks whether the job's branch still lives on origin
// (carl pushed it before the cell was lost) and, if so, whether it has
// drifted behind base. ok=false means no usable remote PATH — a failed
// fetch (offline), a branch that was never pushed, a local branch ahead of
// the remote, or a remote tip already fully merged into base — and the
// caller falls back to ensureUsableBranch's local reset-or-rename logic
// unchanged. The fetch prunes so a branch deleted on origin (the merge
// button's usual cleanup) stops resolving here instead of surviving as a
// stale tracking ref that reads like a live remote.
func recreateFromRemote(repo, branch, base string) (needsRebase bool, ok bool) {
	if err := exec.Command("git", "-C", repo, "fetch", "--prune", "origin").Run(); err != nil {
		return false, false
	}
	remoteRef := "origin/" + branch
	if !refExists(repo, remoteRef) {
		return false, false
	}
	// A local branch with commits the remote lacks (pushed, then kept
	// working, then the cell died) must not be force-reset to the remote
	// tip — those commits would be reflog-only. Local ahead ⇒ fall back.
	if refExists(repo, branch) &&
		exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", branch, remoteRef).Run() != nil {
		return false, false
	}
	// A remote tip fully contained in base is a MERGED branch: it preserves
	// nothing base lacks, and the drift test below cannot tell merged from
	// behind (after a merge commit, base is an ancestor of neither), so
	// every merged-PR branch would earn the rebase instruction — telling a
	// reactivated zeus to rebase onto a base its commits are already in,
	// which replays them; and in the no-steer case that false instruction
	// is the ENTIRE prompt (see reactivatePrompt), with nothing to
	// contradict it. Merged ⇒ fall back to ensureUsableBranch, whose
	// recycle-off-base logic is built for exactly this state. Ancestry sees
	// merge-commit and fast-forward merges only: a SQUASH- or rebase-merged
	// tip is not an ancestor of base and still takes the remote path with a
	// rebase note — the same blind spot ensureUsableBranch has always had —
	// though the pruning fetch above closes the common case where the
	// repo's delete-branch-on-merge setting is on: the squash-merged branch
	// stops resolving here at all. (The prune's own trade: a branch deleted
	// upstream while still genuinely unmerged loses its stale tracking ref
	// here too and falls back to a base reset — recoverable from origin's
	// objects, and far rarer than the merge-cleanup case the prune fixes.)
	if exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", remoteRef, base).Run() == nil {
		return false, false
	}
	needsRebase = exec.Command("git", "-C", repo, "merge-base", "--is-ancestor", base, remoteRef).Run() != nil
	return needsRebase, true
}

// ReactivateJob resurrects a job into its original zeus session. For a done
// job (or a started job whose worktree was lost): the worktree is recreated
// at the SAME path (the claude project dir — and with it the transcript
// history — is keyed on that path). If carl pushed the branch before the
// cell was lost, the worktree recreates off the REMOTE tip (origin/<branch>)
// so pushed work is never silently dropped for a locally-reset one; a
// branch with no usable remote path — never pushed, origin unreachable,
// local ahead of remote, or a tip already merged into base — falls back to
// ensureUsableBranch's reset-or-rename. A remote branch that has drifted behind base earns a
// rebase instruction in the resumed prompt. For a started job whose cell
// died mid-run with its worktree intact: the existing worktree — dirty files
// and all — is reused in place, so the one recovery path that preserves WIP
// is also the one-command one. session overrides the recorded id
// (hand-rescue for jobs done before kraken stamped session ids); message
// rides along as carl's steering. A blank message (and no rebase needed)
// resumes the session promptless — nothing is submitted, it just comes up
// idle.
func ReactivateJob(j *Job, session, message string) error {
	if j.Err != "" {
		return fmt.Errorf("job %s is unparseable (%s) — fix the frontmatter first", j.Slug, j.Err)
	}
	// same identity gate as StartCell/teardownCell: everything below names
	// the cell <slug>-<id>, and an id-less job would silently build and
	// resume a `<slug>-0` cell
	if err := checkCellIdentity(j); err != nil {
		return err
	}
	if session == "" {
		session = j.Session
	}
	if session == "" {
		return fmt.Errorf("%s has no recorded zeus session (done before kraken stamped session ids) — the board's reactivate action can't resume it without one", j.Slug)
	}
	// The CURRENT name is what a reactivated cell comes up under, further down.
	// The already-live REFUSAL is a different question and has to ask about
	// every name the cell could be running under: a renamed cell whose tmux
	// convergence failed is live under an older session name, and missing it
	// here would start a SECOND claude session on the one worktree.
	sess := CellSession(j)
	if liveSess, ok := FindCellSession(j, LiveSessions()); ok {
		return fmt.Errorf("cell %s is already live (k o %s to jump in)", liveSess, liveSess)
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
	// teardown deleted the cell dir after mirroring artifacts/ into the
	// store; bring the mirror back so the resumed zeus finds its own
	// deliverables where it left them — and so the artifacts dir the loop
	// below creates isn't an empty true-mirror source that the NEXT
	// teardown's sync would read as "every artifact was deleted", wiping the
	// stored rows too. A restore failure aborts here, before any worktree or
	// tmux work, for that same reason. The call sits BEFORE the MkdirAll on
	// purpose: restore self-gates on the artifacts dir not existing (a
	// surviving dir's deletions are real — see restoreCellArtifacts), so it
	// must observe the dir's true pre-reactivation state, not the empty one
	// the loop is about to make. The store guard is defensive rather than a
	// real entrypoint split — the one caller is the board's reactivate route
	// inside kraken serve, where a store is always wired — so an unwired
	// store (a test, a future caller) means nothing to restore from, not an
	// error.
	if st := ActiveStore(); st != nil {
		if err := restoreCellArtifacts(st, j.ID, j); err != nil {
			return fmt.Errorf("job %s: restore artifacts: %v", j.Slug, err)
		}
	}
	// existingCellDir rather than cellDir, and the difference only ever shows
	// on a RENAMED job whose cell dir survived under an earlier name (the
	// mid-run resume path — a torn-down cell has no dir, so this resolves to
	// the current name and behaves exactly as it always has). Creating a
	// second, EMPTY dir under the new name beside a surviving one would make
	// it the dir CellArtifacts resolves to from then on, and the next
	// teardown's true-mirror sync would read that empty dir as "every artifact
	// was deleted" and wipe the stored rows.
	reDir := existingCellDir(j)
	for _, d := range []string{reDir, cellArtifactsIn(reDir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %v", d, err)
		}
	}
	// below every rejection above (unparseable job, bad cell identity, no
	// recorded session, already-live, unresolved repo, reactivateMode's
	// worktree-exists check, and a failed artifact restore): a rejected
	// spawn must not pay for a usage fetch or a swap
	rotationGate("reactivate " + j.Slug)
	var reorient, rebaseNote string
	if mode == "resume" {
		reorient = "your cell died mid-run (the tmux session was lost), and carl has reactivated it. Your worktree is EXACTLY as you left it — uncommitted changes preserved, branch " + CellBranch(j) + " untouched. Re-orient with git status and the job's mid-run log, then continue the job from where it stopped."
	} else {
		base, err := baseRef(j, repo)
		if err != nil {
			return fmt.Errorf("job %s: %v", j.Slug, err)
		}
		branch := CellBranch(j)
		var branchFlag, startPoint string
		fromRemote := false
		if needsRebase, ok := recreateFromRemote(repo, branch, base); ok {
			fromRemote = true
			// carl pushed this branch before the cell was lost — recreate off
			// the remote tip so that work is never silently dropped for a
			// locally-reset branch
			branchFlag, startPoint = "-B", "origin/"+branch
			if needsRebase {
				rebaseNote = "your branch " + branch + " is behind " + base + ". first run: git fetch origin && git rebase " + base + " — resolve any conflicts, verify the build still passes, and report the rebase result. then continue."
			}
		} else {
			// no usable remote path (never pushed, origin unreachable, local
			// ahead, or already merged into base): recycle the branch when
			// its PRs are all merged into base; only unmerged leftovers force
			// a fresh -rN name (persisted inside)
			branchFlag, err = ensureUsableBranch(j, repo, base)
			if err != nil {
				return fmt.Errorf("job %s: pick branch: %v", j.Slug, err)
			}
			branch, startPoint = CellBranch(j), base
		}
		// under reDir, the dir the staging above already chose — NOT
		// CellWorktree's current-name dir. On a renamed job whose old cell dir
		// survived, those are two different directories, and creating the
		// worktree in the second one splits the cell in half: `git worktree
		// add` makes the current-name dir as a side effect, existingCellDir
		// then flips to it (current-first is the ordering contract), and
		// CellArtifacts resolves to a new EMPTY artifacts dir while the
		// restored artifacts sit in the old one — at which point
		// syncCellArtifacts takes its dir-missing skip branch and the mirror
		// goes quiet with nothing reporting it. One dir per cell, chosen once.
		wt = cellWorktreeIn(reDir, repo)
		out, err := exec.Command("git", "-C", repo, "worktree", "add", wt, branchFlag, branch, startPoint).CombinedOutput()
		if err != nil {
			return fmt.Errorf("git worktree add: %s", strings.TrimSpace(string(out)))
		}
		// fresh worktree, same as StartCell: no hook until installed, and a
		// done→adjust reactivation is exactly when new PRs are about to ship
		installHookSettings(wt, j)
		installSkills(wt, j)
		// tell zeus where its branch actually points: the pushed remote tip
		// when recreateFromRemote took over, the base otherwise
		branchDesc := "freshly reset off " + base
		if fromRemote {
			branchDesc = "recreated from its pushed remote tip origin/" + branch
		}
		if j.State == "done" {
			reorient = doneReorient(j, branch, branchDesc)
		} else {
			reorient = "your cell died mid-run and its worktree was lost, so it was recreated fresh: branch " + branch + " " + branchDesc + ". Any uncommitted work from the previous run is gone — re-read the job's mid-run log and the repo state, then continue the job."
		}
	}
	// both modes land here with a real worktree: freshly recreated above, or
	// the surviving one the resume path found. linkDevEnv is idempotent, and
	// running it on the resume path is what retrofits links onto a worktree
	// created before this feature existed.
	linkDevEnv(repo, wt, j)
	prompt, hasPrompt := reactivatePrompt(reorient, message, rebaseNote)
	if !hasPrompt {
		prompt = "" // spawnCommand omits an empty prompt entirely
	}
	// the job's model rides along on reactivation too (Q2): a resumed zeus
	// keeps the model its job file specifies at reactivate time, and falls
	// back to the same core.ZeusModel a fresh cell would.
	//
	// The brief is staged HERE TOO, and this used to pass "" on the premise
	// that -r resumes a session already carrying the one it was born with.
	// thread.go:threadSpawnCommand is the one that actually measured it, and
	// it records the opposite: a resumed session does NOT retain an earlier
	// run's --append-system-prompt, so a briefless reactivation came back as a
	// plain claude holding zeus' history. The repo-owned skills also carry the
	// crew's `model: opus`, so the brief is not its only statement — it is the
	// only one in context on EVERY turn, where a skill is read once per phase.
	// That gap was survivable while every spawn in the binary was Opus, since
	// an Agent call with no model can inherit its parent's tier and the parent
	// was Opus anyway. With zeus on core.ZeusModel the two part company: no
	// brief means a crew that inherits zeus' tier rather than Opus, the
	// roll-call does not look at tiers at all, and the one surface that does —
	// the board's crew chip, which renders each teammate's model — is not
	// something anyone reads as an alarm.
	// Same graphContext append as StartCell's, and for a sharper reason: a
	// REACTIVATED job is by definition returning after time passed, which is
	// exactly when the graph is most likely to know something its zeus does
	// not. Same ""-on-no-match guarantee.
	launch, err := spawnCommand(j, zeusSpawnArgv(j, "-r", session), zeusBrief(j)+graphContext(j), prompt)
	if err != nil {
		return fmt.Errorf("%v (worktree left at %s)", err, wt)
	}
	var newSessionStderr bytes.Buffer
	newSessionCmd := exec.Command("tmux", "new-session", "-d",
		"-x", strconv.Itoa(core.DetachedSessionCols), "-y", strconv.Itoa(core.DetachedSessionRows),
		"-s", sess, "-c", wt,
		"-n", util.Slugify(j.Slug), launch)
	// the pane's PATH is this process' PATH: see cellSpawnEnv
	newSessionCmd.Env = cellSpawnEnv()
	newSessionCmd.Stderr = &newSessionStderr
	if err := newSessionCmd.Run(); err != nil {
		if newSessionStderr.Len() > 0 {
			return fmt.Errorf("tmux new-session: %v: %s (worktree left at %s)", err, strings.TrimSpace(newSessionStderr.String()), wt)
		}
		return fmt.Errorf("tmux new-session: %v (worktree left at %s)", err, wt)
	}
	_ = exec.Command("tmux", "set-option", "-t", sess, "@kraken-job", util.Slugify(j.Slug)).Run()
	setCellWindowColor(j)
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

// S2: the k-migrate section (OldJobState, MigrateTerminal, MigrateInFlight,
// MigrateQueued — a superseded state model's one-time state-migration
// helpers) lived here and is gone: dead code, no callers anywhere in the
// tree since `k migrate` itself was retired. See jobs/store.go's own
// comment for the matching note on its former mention.
