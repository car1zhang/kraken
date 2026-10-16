package jobs

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// rename.go — giving a job a new name.
//
// The shape of this feature is decided by one fact: a job's runtime names are
// DERIVED from its slug, and almost none of them can be moved. The cell dir
// and its worktree cannot (renaming a directory under a running agent breaks
// its pane), the git branch and
// every open PR's head cannot (a `git branch -m` detaches the PR), and the
// ~/.claude/projects transcript dirs are Claude Code's rather than kraken's.
// So a rename moves almost NOTHING: it changes one column, records the old
// name as a former slug, and every resolver in the package goes on probing the
// old names — cellDirCandidates, ExistingWorktree, branchFamily,
// JobProjectDirs, FindCellSession, threadNamePrefixes.
//
// One thing DOES move, because it can: the tmux session name (one `tmux
// rename-session` on a live cell, converged straight away so `k ls` and the
// board read the new name). A job's stored artifacts need no move at all —
// they are rows keyed on the job's immutable id.
//
// Everything after the store transaction commits is BEST-EFFORT and lands in
// Notes rather than in an error. The rename is a fact the moment the row is
// written; a tmux call that failed is a cell running under an old name, which
// every resolver above already tolerates and the daemon self-heals — reporting
// it as a failed rename would invite carl to retry a rename that has already
// happened.

// RenameResult is what a completed rename has to say for itself: the stored
// slug and any best-effort step that failed after the row was written (a
// Note is a step kraken will retry — the daemon reconverges tmux — or that
// costs only a missing pane message).
//
// Skipped is always empty. It listed preserved flat artifact copies that
// kept the old name, and nothing is preserved as a file any more; the field
// stays because the web and iOS clients decode it.
type RenameResult struct {
	Slug    string
	Skipped []string
	Notes   []string
}

// reservedRenamePrefixes are the job-name prefixes kraken MINTS (or minted)
// itself: dream.go's `dream-<date>`, and `graph-<date>` from the retired
// nightly graph-ingest job, archived generations of which still exist. The
// dream job is keyed on the date and looked up by prefix to decide whether
// tonight's job already exists, so renaming INTO one would make a hand-named
// job answer to that check, and renaming one AWAY would make the pass create
// a second job for a night that already had one.
var reservedRenamePrefixes = []string{"dream-", "graph-"}

// maxRenameSlugRunes bounds a renamed slug. Runes rather than bytes because
// Slugify's output is what the bound is about, and the number is a headroom
// figure rather than a filesystem constant: macOS caps one path COMPONENT at
// 255 bytes, and the longest component derived from a slug is the cell dir's
// "<slug>-<id>", so 100 runes leaves room for a multi-byte name plus the id
// and still refuses anything a human typed by accident.
const maxRenameSlugRunes = 100

// RenameJob gives j a new name. The gates below are ordered, and the order is
// the contract rather than an implementation detail: an unslugifiable name is
// refused before anything is looked up, a no-op is answered before any refusal
// can fire on a name the job already has, and the reserved-prefix check
// precedes the archive one so a `dream-` job is refused for the reason that
// actually applies to it.
//
// On success j is updated IN PLACE (Slug, Rev, FormerSlugs), because the
// caller is holding the only copy: a handler that re-read the job would read
// it by the name it no longer has.
func RenameJob(j *Job, to string) (*RenameResult, error) {
	if j == nil {
		return nil, fmt.Errorf("no job to rename")
	}
	newSlug := util.Slugify(to)
	if newSlug == "" {
		return nil, fmt.Errorf("%q slugifies to nothing — pick a name with letters or digits in it", to)
	}
	// gate 1b, beside gate 1 because it is the same question — can this name
	// be a name at all — and it has to be asked BEFORE the row moves. Every
	// runtime name is derived from the slug and is LONGER than it: the cell
	// dir, the worktree inside it, the branch, the tmux session. A committed
	// over-long slug is not a cosmetic problem — the next StartCell or
	// ReactivateJob fails on MkdirAll with ENAMETOOLONG — and by then the
	// store row has already moved, so the refusal has to happen here.
	if n := len([]rune(newSlug)); n > maxRenameSlugRunes {
		return nil, fmt.Errorf("%q is too long (%d characters, max %d) — every cell dir, branch and tmux session name is built from the slug", newSlug, n, maxRenameSlugRunes)
	}
	// gate 2, before every refusal below: renaming a job to the name it
	// already has is a no-op, not a collision with itself. Answering
	// otherwise would make an idempotent retry (a double-clicked button, an
	// iOS retry) look like a failure.
	if newSlug == j.Slug {
		return &RenameResult{Slug: j.Slug}, nil
	}
	// Two messages, not one, because the two directions are different
	// situations and a single sentence describing only the PREFIX reads as a
	// complaint about whichever name carl is looking at. Renaming
	// dream-2026-08-12 → nightly-notes answered "\"dream-\" is a reserved
	// prefix" while he was staring at "nightly-notes". Both still say
	// "reserved prefix", and each names the offending string, which is the
	// register every other refusal in this file uses.
	for _, p := range reservedRenamePrefixes {
		if strings.HasPrefix(j.Slug, p) {
			return nil, fmt.Errorf("%s carries the reserved prefix %q — kraken mints that name itself and looks it up by prefix, so renaming it away would make tonight's pass create a second job for a date that already has one", j.Slug, p)
		}
		if strings.HasPrefix(newSlug, p) {
			return nil, fmt.Errorf("%q starts with the reserved prefix %q — kraken mints those names itself (dream and graph jobs are keyed on their date)", newSlug, p)
		}
	}
	if j.ArchivedName != "" {
		return nil, fmt.Errorf("%s is archived — unarchive it first, then rename it", j.Slug)
	}
	st := ActiveStore()
	if st == nil {
		return nil, fmt.Errorf("renaming needs kraken serve's store — none is wired")
	}
	if row, err := st.GetJobByID(j.ID); err == nil && row.Archived() {
		// the in-memory ArchivedName above is only as fresh as the caller's
		// load; the row is the truth, and renaming an archived job would move
		// a slug that is not in the active namespace at all.
		return nil, fmt.Errorf("%s is archived — unarchive it first, then rename it", j.Slug)
	}
	if err := checkRenameTarget(st, j, newSlug); err != nil {
		return nil, err
	}

	// everything derived from the OLD name, read before the row moves — after
	// the store call j answers to the new one and none of these can be
	// recovered from it.
	oldSlug := j.Slug
	oldSlugified := util.Slugify(oldSlug)
	oldSess := CellSession(j)
	live := LiveSessions()[oldSess]

	rev, err := st.RenameJob(j.ID, j.Rev, newSlug)
	if err != nil {
		// the store's own reservation checks are the RACE backstop behind the
		// pre-checks above, so they report the same refusal rather than a
		// second vocabulary for the same situation.
		if errors.Is(err, store.ErrSlugTaken) {
			return nil, fmt.Errorf("%q is taken — another job already answers to that name", newSlug)
		}
		return nil, err
	}

	j.Slug = newSlug
	j.Rev = rev
	j.FormerSlugs = appendFormerSlug(j.FormerSlugs, oldSlug, newSlug)

	res := &RenameResult{Slug: newSlug}
	// tmux convergence, on a live cell only. VerifyCellSession inside is what
	// makes this safe to run against a name discovery reported: renaming a
	// session that is not this job's cell would be a rename of somebody else's
	// window.
	if live {
		res.Notes = append(res.Notes, convergeCellTmux(j, oldSess, oldSlugified)...)
	}
	if live {
		res.Notes = append(res.Notes, noticeCellRename(j, oldSlug, newSlug)...)
	}
	return res, nil
}

// checkRenameTarget runs the three collision gates, in the order their
// refusals are most useful: an archived job holding the name (the one carl
// cannot resolve from the board without unarchiving something), an active job
// holding it, and an active job that used to.
//
// All three compare SLUGIFIED forms on both sides. A stored slug is normally
// already slugified, but nothing enforces that on a hand-edited job file, and
// two names that slugify alike would collide on every derived path — the cell
// dir, the branch, the session — while comparing unequal here.
func checkRenameTarget(st *store.Store, j *Job, newSlug string) error {
	archived, err := st.ListArchivedJobs()
	if err != nil {
		return err
	}
	for _, a := range archived {
		if util.Slugify(a.Slug) == newSlug {
			return fmt.Errorf("%q belongs to an archived job — pick another name (an archived job's slug is still its own)", newSlug)
		}
	}
	active, err := st.ListJobs()
	if err != nil {
		return err
	}
	for _, o := range active {
		if o.ID == j.ID {
			continue
		}
		if util.Slugify(o.Slug) == newSlug {
			return fmt.Errorf("%q is taken — another job already answers to that name", newSlug)
		}
		for _, f := range o.FormerSlugs {
			if util.Slugify(f) == newSlug {
				return fmt.Errorf("%q is a former name of %s — taking it would make one name resolve to two jobs", newSlug, o.Slug)
			}
		}
	}
	return nil
}

// appendFormerSlug mirrors, in memory, what store.RenameJob just wrote: the
// old name added, the new one removed. Rebuilt rather than appended blindly
// because renaming A→B→A must leave the caller's slice holding {"B"} — the
// set never contains the job's current name, and a duplicate would make every
// probe derive the same path twice.
func appendFormerSlug(former []string, oldSlug, newSlug string) []string {
	out := make([]string, 0, len(former)+1)
	seen := map[string]bool{newSlug: true}
	for _, f := range append(former, oldSlug) {
		if f == "" || seen[f] {
			continue
		}
		seen[f] = true
		out = append(out, f)
	}
	return out
}

// convergeCellTmux renames a live cell's tmux session and window to the job's
// new name and restamps the @kraken-job marker. Every step is best-effort: a
// note per failure, never fatal, because every reader already probes the old
// names and the daemon retries this on its next tick.
//
// verifyConvergeTarget FIRST is the verify-before-acting rule this repo
// applies to every targeted action: discovery output goes stale, and a `tmux
// rename-session` aimed at a session that is no longer this job's cell renames
// a window belonging to someone else, silently.
//
// The window is found by NAME rather than by index. StartCell names the cell's
// first window for the slug, but a cell with side threads has several windows
// and tmux's base-index is configurable, so ":0" is a guess — matching the
// name kraken itself wrote is the only identification that cannot pick the
// wrong one.
func convergeCellTmux(j *Job, oldSess, oldSlugified string) []string {
	var notes []string
	if err := verifyConvergeTarget(j, oldSess); err != nil {
		return append(notes, "tmux left as it was: "+err.Error())
	}
	newSess, newSlugified := CellSession(j), util.Slugify(j.Slug)
	if err := exec.Command("tmux", "rename-session", "-t", oldSess, newSess).Run(); err != nil {
		// the two steps below address the session, so a failed rename leaves
		// them addressing the OLD name — which is still correct, and is what
		// the daemon's self-heal will find next tick.
		notes = append(notes, fmt.Sprintf("tmux rename-session %s → %s failed: %v", oldSess, newSess, err))
		newSess = oldSess
	}
	if note := renameCellWindow(newSess, oldSlugified, newSlugified); note != "" {
		notes = append(notes, note)
	}
	if err := exec.Command("tmux", "set-option", "-t", newSess, "@kraken-job", newSlugified).Run(); err != nil {
		// not cosmetic: the marker is what VerifyCellSession checks before a
		// teardown, and a stale one is why that check accepts former slugs too.
		notes = append(notes, fmt.Sprintf("tmux @kraken-job restamp failed: %v", err))
	}
	return notes
}

// verifyConvergeTarget decides whether sess is a session this job's cell may
// be running in, and is deliberately ONE notch weaker than VerifyCellSession:
// a session carrying NO @kraken-job marker at all is accepted when its NAME is
// one this job's own cell names produce.
//
// Unmarked is not suspicious. StartCell stamps the marker with a discarded
// error, and every cell spawned before the marker existed has none — so
// refusing on absence makes such a cell permanently unconvergeable, and every
// gate that reads the CURRENT session name then stays wrong about it forever
// instead of for one tick. In that case the name IS the evidence: a cell
// session name carries the slugified slug AND the job's immutable id, so no
// other job can produce it.
//
// A marker that is PRESENT and names some other job is still refused, and any
// of this job's marker slugs is accepted rather than just the one this
// convergence expects — a previous attempt whose rename-session failed after
// its restamp succeeded leaves the OLD session carrying the NEW slug, and
// refusing that would wedge the very cell the daemon's self-heal is for.
func verifyConvergeTarget(j *Job, sess string) error {
	if got, marked := cellSessionMarker(sess); marked {
		for _, s := range CellMarkerSlugs(j) {
			if got == s {
				return nil
			}
		}
		return fmt.Errorf("session %s is marked for job %q, not %s — leaving it alone", sess, got, j.Slug)
	}
	for _, cand := range cellSessionCandidates(j) {
		if sess == cand {
			return nil
		}
	}
	return fmt.Errorf("session %s has no @kraken-job marker and is not a name %s's cell could carry — leaving it alone", sess, j.Slug)
}

// renameCellWindow renames the cell window still called oldName to newName,
// returning a note when tmux refused. An empty note also covers "no window
// carries that name", which is the ordinary state of an already-converged
// cell — this function is re-run by the daemon's self-heal.
func renameCellWindow(sess, oldName, newName string) string {
	out, err := exec.Command("tmux", "list-windows", "-t", sess, "-F", "#{window_id} #{window_name}").Output()
	if err != nil {
		return fmt.Sprintf("tmux list-windows for %s failed: %v", sess, err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || name != oldName {
			continue
		}
		if err := exec.Command("tmux", "rename-window", "-t", id, newName).Run(); err != nil {
			return fmt.Sprintf("tmux rename-window %s → %s failed: %v", oldName, newName, err)
		}
		return ""
	}
	return ""
}

// noticeCellRename tells a live cell's zeus that its job was renamed. Zeus'
// brief is deliberately NOT rewritten and zeus is NOT respawned — the alias
// fallback is what carries the stale name — so this one line is the only thing
// that stops zeus reporting a name the board no longer shows.
//
// No job-state write anywhere in here. Renaming a BLOCKED job must not flip it
// to working: the message is kraken talking, not the job resuming, and the
// roll call would sort that out a tick later while the board showed a job as
// working that nobody was working on.
func noticeCellRename(j *Job, oldSlug, newSlug string) []string {
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus := CellZeus(j, all)
	if zeus == nil {
		return []string{"rename notice not delivered: no zeus pane found in the cell"}
	}
	msg := fmt.Sprintf("kraken: this job was renamed %s → %s; old CLI names keep resolving, artifact paths unchanged", oldSlug, newSlug)
	if err := core.SendMessage(zeus, msg); err != nil {
		return []string{"rename notice not delivered: " + err.Error()}
	}
	return nil
}

// anyCellSessionAlive re-probes tmux for every session name this job's cell
// could be running under. It is the positive-evidence half of the daemon's
// dead-cell watch: a cell is GONE only when none of its names is alive, since
// a renamed cell that has not converged answers to an older one.
func anyCellSessionAlive(j *Job) bool {
	for _, sess := range cellSessionCandidates(j) {
		if core.SessionAlive(sess) {
			return true
		}
	}
	return false
}
