package jobs

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kraken/internal/store"
	"kraken/internal/util"
)

// archive.go — archived jobs. Archiving flips a job's store row into the
// archived table (store.ArchiveJob), which frees its slug entirely: LoadJobs
// never sees archived rows, so an archived job is invisible to the
// scheduler and slug-collision marking. The row itself is untouched apart
// from that move — state included — so unarchiving restores the job exactly
// as it was: a done job comes back done, a draft comes back draft. Archived
// is therefore a *location*, distinct from the done state.
//
// Naming conflicts inside the archive are resolved by numbering:
// foo, then foo-2, foo-3, … (store.ArchiveJob picks the number). The
// numbered name (ArchivedName) is the identity every archive ROW action uses
// — two archived jobs can share a Slug, never an ArchivedName.
//
// Preserved pluto artifacts (still plain files on disk — Q12) are named for
// the job's cell instead: <slug>-<id>.<name>, the same prefix they carried
// while the job was active, so archiving MOVES them without renaming. The id
// is what keeps two generations of one slug apart, which is what the archive
// numbering used to do for files and now does only for rows. Every reader and
// writer of that prefix goes through PlutoArtifactPrefix — they must agree,
// or a job's artifacts silently vanish from its card (and, worse, survive its
// delete forever).

func ArchiveDir() string {
	return filepath.Join(PlutoDir(), "archive")
}

// LoadArchivedJobs returns every archived job, store-backed. Callers all run
// inside kraken serve, where a store is always wired (`kraken import`'s
// one-shot migration reads archive-dir files directly via its own
// readArchivedJobFiles instead, since it runs before/without a live serve
// process).
func LoadArchivedJobs() []*Job {
	jobs, err := loadArchivedJobsFromStore(ActiveStore())
	if err != nil {
		// Log before degrading to empty — see LoadJobs.
		dlogErr("load archived jobs from store failed: %v", err)
		return nil
	}
	return jobs
}

// ArchivedJobByName resolves an archived job by its numbered file base
// (with or without the .job.md suffix).
func ArchivedJobByName(jobs []*Job, name string) *Job {
	name = strings.TrimSuffix(name, JobSuffix)
	for _, j := range jobs {
		if j.ArchivedName == name {
			return j
		}
	}
	return nil
}

// moveSkip is one file movePrefixed did NOT move: where it still is, where it
// was headed, and why it stayed.
//
// Src is the load-bearing field. Every caller's message tells a human about a
// file that kept its old name, and that file is the SOURCE — reporting the
// destination named the blocker instead, so acting on the sentence literally
// meant deleting the wrong file while the stranded copy stayed stranded.
//
// Err separates the two skips, which are different problems with different
// remedies and used to be indistinguishable: nil means the destination was
// already occupied (a genuine name conflict — resolve it by hand), non-nil
// means the rename itself failed (permissions, ENAMETOOLONG, a full disk) and
// carries the reason that was previously discarded.
type moveSkip struct {
	Src, Dst string
	Err      error
}

// Conflict reports whether this skip is a destination that already existed,
// as opposed to a rename that failed. The two are worth telling apart at
// every call site: a conflict is carl's to resolve, a failure is kraken's to
// report with its cause.
func (s moveSkip) Conflict() bool { return s.Err == nil }

// String is the one rendering of a skip for a human, so the three callers
// that log one cannot describe the same situation three ways.
func (s moveSkip) String() string {
	if s.Err != nil {
		return fmt.Sprintf("%s (move to %s failed: %v)", s.Src, s.Dst, s.Err)
	}
	return fmt.Sprintf("%s (%s already exists)", s.Src, s.Dst)
}

// movePrefixed renames every non-dir file in fromDir named
// fromPrefix.<rest> to toDir/toPrefix.<rest>, skipping job files (the job
// file itself moves separately) and never overwriting: a conflicting
// target leaves the source in place and reports it, since silently
// clobbering either side would lose a deliverable.
func movePrefixed(fromDir, fromPrefix, toDir, toPrefix string) (moved []string, skipped []moveSkip) {
	entries, err := os.ReadDir(fromDir)
	if err != nil {
		return nil, nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, fromPrefix+".") || strings.HasSuffix(name, JobSuffix) {
			continue
		}
		rest := strings.TrimPrefix(name, fromPrefix+".")
		src := filepath.Join(fromDir, name)
		dst := filepath.Join(toDir, toPrefix+"."+rest)
		if _, err := os.Stat(dst); err == nil {
			skipped = append(skipped, moveSkip{Src: src, Dst: dst})
			continue
		}
		if err := os.Rename(src, dst); err != nil {
			skipped = append(skipped, moveSkip{Src: src, Dst: dst, Err: err})
			continue
		}
		moved = append(moved, dst)
	}
	return moved, skipped
}

// autoArchivable reports whether the daemon should sweep j into the archive
// on its own: done, with a real ModTime, older than autoArchiveAfter. An
// unparseable job (Err set) still archives on this rule — done wins over a
// parse error, same precedence as DisplayState/deriveState.
func autoArchivable(j *Job, now time.Time) bool {
	return j.State == "done" && !j.ModTime.IsZero() && now.Sub(j.ModTime) > autoArchiveAfter
}

// ArchiveJob moves an active job (store row + preserved pluto artifacts)
// into the archive. Only a job with no cell in flight can be archived — same
// rule as editableJob: never started, or done. A working/blocked job must
// be killed first (kill returns it to draft, which then archives cleanly).
// Returns the archived name (numbered when the slug was already taken in
// the archive).
func ArchiveJob(j *Job, live map[string]bool) (string, error) {
	if j.Started() && j.State != "done" {
		return "", fmt.Errorf("%s has a cell in flight (state %s) — kill it first (kill returns the job to draft), then archive", j.Slug, j.State)
	}
	// FindCellSession rather than the current name alone. Archiving is
	// destructive — it removes every uploads dir, moves the preserved
	// artifacts into archive/, and frees the slug — and a renamed cell whose
	// tmux convergence failed is live under an OLDER session name, so the
	// current-name read walks this refusal past a running zeus and leaves it
	// in a worktree belonging to a job no longer in the active namespace.
	if sess, ok := FindCellSession(j, live); ok {
		return "", fmt.Errorf("%s still has a live cell session %s — kill it first", j.Slug, sess)
	}
	// Release the cell's git worktree registration BEFORE the row moves.
	// Archiving frees the slug, so from here on another job may legitimately
	// take the name — and an archived job that still holds a checkout of its
	// branch is a checkout nothing will ever clean up. Refuses on a dirty
	// tree rather than discarding work; the cell DIRECTORY deliberately
	// stays, since cellName is what makes a reused name safe now, and this is
	// hygiene rather than the guarantee.
	if err := releaseCellWorktree(j); err != nil {
		return "", err
	}
	st := ActiveStore()
	name, err := st.ArchiveJob(j.ID)
	if err != nil {
		return "", fmt.Errorf("archive %s: %w", j.Slug, err)
	}
	// uploaded images are already consumed into zeus' transcript by now —
	// best-effort cleanup, both the DB rows and the materialized files
	if err := st.DeleteUploadsForJob(j.ID); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: uploads not deleted for %s: %v\n", j.Slug, err)
	}
	// every name the job has held: a rename materializes a fresh uploads dir
	// under the new name and moves nothing, so the images of a renamed job are
	// spread across one dir per name. All of them carry this job's id, so none
	// can belong to another generation of a shared name.
	for _, s := range append([]string{j.Slug}, j.FormerSlugs...) {
		if dir := UploadsDir(s, j.ID); dir != "" {
			os.RemoveAll(dir)
		}
	}
	// Preserved pluto artifacts are still real files even though the job
	// row is store-backed (Q12 — live cells serve straight off disk, and
	// api/board.go lists preserved copies from the pluto/archive dirs), so
	// they still need moving into the archive dir here.
	//
	// The prefix is <slug>-<id> on BOTH sides, so this is a plain move rather
	// than a rename: the id already disambiguates two generations of one
	// name, which is what the archive's -2/-3 numbering used to be for. The
	// numbered archived_name still exists as the archived job's identity in
	// the jobs table; it just no longer has to name files.
	if err := os.MkdirAll(ArchiveDir(), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: mkdir %s: %v\n", ArchiveDir(), err)
	} else {
		prefix := cellName(j)
		_, skipped := movePrefixed(PlutoDir(), prefix, ArchiveDir(), prefix)
		// ALSO sweep the pre-cutover bare-slug prefix, renaming onto the id
		// form as it moves. Without this there is a window that strands
		// artifacts permanently: every copy preserved before the id landed
		// carries the bare slug, so the move above matches nothing, and once
		// the row is archived `kraken cutover` can never recover the files —
		// it builds its PlutoDir prefix map from ACTIVE jobs, and this job is
		// no longer one. The window is not hypothetical: autoArchivePass
		// sweeps any done job older than 24h on its own, so it opens the
		// moment this ships and closes only when cutover is run.
		if legacy := util.Slugify(j.Slug); legacy != "" && legacy != prefix {
			_, legacySkipped := movePrefixed(PlutoDir(), legacy, ArchiveDir(), prefix)
			skipped = append(skipped, legacySkipped...)
		}
		for _, s := range skipped {
			// s.String() names the file that STAYED and why — the old form
			// printed the destination, which is the blocker rather than the
			// stranded copy, and could not say which of the two skips it was.
			fmt.Fprintf(os.Stderr, "kraken: artifact not moved: %s\n", s)
		}
	}
	j.ArchivedName = name
	return name, nil
}

// releaseCellWorktree drops the job's git worktree registration so an
// archived job stops holding a checkout of its branch, leaving the cell
// directory itself alone.
//
// It refuses on a dirty worktree, naming the paths. Archive is REVERSIBLE
// today — UnarchiveJob puts the row straight back — so it must not be the
// operation that quietly discards uncommitted work.
//
// Everything here is best-effort past that check: a job whose worktree was
// never created, or was already removed by hand, archives cleanly. That
// matters because this runs on every archive, including the many jobs that
// never started a cell at all.
func releaseCellWorktree(j *Job) error {
	repo, err := ResolveRepo(j.Repo)
	if err != nil || repo == "" {
		return nil // no repo resolved: nothing was ever registered
	}
	wt := ExistingWorktree(j, repo)
	if wt == "" {
		return nil
	}
	if dirty, err := worktreeDirty(repo, wt); err == nil && dirty != "" {
		return fmt.Errorf("%s has uncommitted changes in %s — commit, stash or discard them first, then archive:\n%s", j.Slug, wt, dirty)
	}
	// NOT `git worktree remove`: that DELETES the worktree directory, and
	// --force only relaxes its clean-tree requirement. We are dropping the
	// REGISTRATION and keeping the files — archive is reversible, and a
	// worktree can hold plenty git never reports as dirty (build outputs,
	// node_modules — gitignored, so `status --porcelain` says nothing about
	// them; kraken's own settings merge and skill materialization DO show
	// up, and worktreeDirty filters exactly those two write shapes).
	//
	// Deregistering is deleting the admin dir git keeps for this worktree
	// under <repo>/.git/worktrees/<name>, then pruning so git forgets it.
	// `prune` alone cannot do it: prune only reaps registrations whose
	// directory has already gone.
	admin, err := exec.Command("git", "-C", wt, "rev-parse", "--absolute-git-dir").Output()
	if err != nil {
		// Not a registered worktree (or git unavailable) — nothing to release.
		return nil
	}
	adminDir := strings.TrimSpace(string(admin))
	// Only ever remove something that really is a worktree admin dir inside
	// the repo's own .git/worktrees/. Anything else means git told us
	// something we did not expect, and guessing would be destructive.
	if !strings.Contains(filepath.ToSlash(adminDir), "/.git/worktrees/") {
		return nil
	}
	if err := os.RemoveAll(adminDir); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: worktree not released for %s: %v\n", j.Slug, err)
		return nil
	}
	// Tidies git's bookkeeping now that the admin dir is gone. Best-effort:
	// the registration is already dropped either way.
	_ = exec.Command("git", "-C", repo, "worktree", "prune").Run()
	return nil
}

// worktreeDirty reports the porcelain status of a worktree, empty when
// clean. An error means the question could not be answered — callers treat
// that as "not proven dirty" rather than blocking an archive on a git that
// would not run.
//
// Kraken's OWN writes are filtered out before deciding. installHookSettings
// merges `.claude/settings.json` in every cell worktree and skill
// materialization drops `.claude/skills/` beside it — and in repos that
// TRACK `.claude/settings.json` (monolith does) that merge shows up as a
// porcelain ` M` line on every cell forever. Unfiltered, that line made
// archiving ANY such job with a live worktree refuse with a message naming
// only files kraken itself wrote, which no amount of carl-side hygiene can
// fix. The filter is deliberately narrower than "anything under .claude/":
// the skills tree is filtered only on UNTRACKED (`??`) lines AND only for a
// skill name kraken actually ships (isEmbeddedSkill). Untracked, because
// that is the only status kraken's materialization can produce
// (installSkills skips any skill the repo tracks) — a ` M .claude/skills/…`
// line is an agent EDITING a tracked skill, which in the kraken repo is
// exactly a dream job's uncommitted deliverable and must still refuse. And
// name-checked, because an untracked skill kraken does not ship is an agent
// AUTHORING a new one, which is that same deliverable arriving as a new
// file rather than an edit. settings.json
// is filtered at any status: tracked-and-merged in monolith (` M`),
// created-fresh elsewhere (`??`, sometimes collapsed to `?? .claude/` when
// the repo had no .claude at all). Rename lines are never filtered — kraken
// renames nothing, so a rename touching these paths is real work.
func worktreeDirty(repo, wt string) (string, error) {
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	var kept []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if isKrakenWorktreeDirt(line) {
			continue
		}
		if strings.TrimSpace(line) != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n"), nil
}

// isKrakenWorktreeDirt reports whether one porcelain status line names only
// a write kraken itself makes into every cell worktree. See worktreeDirty's
// comment for why the skills arm is untracked-only.
func isKrakenWorktreeDirt(line string) bool {
	if len(line) < 4 {
		return false
	}
	status := line[:2]
	path := strings.TrimSpace(line[2:])
	if strings.Contains(path, " -> ") {
		return false // a rename is never kraken's write
	}
	path = strings.Trim(path, `"`)
	if path == ".claude/settings.json" {
		return true
	}
	if status != "??" {
		return false
	}
	// untracked-only from here: kraken's materialization, plus the collapsed
	// directory form a repo with no .claude/ at all produces
	if path == ".claude/" || path == ".claude" {
		return true
	}
	rest, ok := strings.CutPrefix(path, ".claude/skills/")
	if !ok {
		return false
	}
	// ...and only for a skill kraken actually ships. A line naming anything
	// else is an agent AUTHORING a new skill, which is a dream or aim job's
	// own deliverable and must still refuse. The bare `.claude/skills/` form
	// is git collapsing a wholly-untracked directory, which is exactly what
	// kraken's materialization creates in a repo that tracks none of them
	// (monolith); there the collapse is all-or-nothing and a new
	// agent-authored skill rides inside it — unavoidable at that
	// granularity, and the tracked-edit case is the one that mattered.
	if rest == "" {
		return true
	}
	name, _, _ := strings.Cut(strings.TrimSuffix(rest, "/"), "/")
	return isEmbeddedSkill(name)
}

// isEmbeddedSkill reports whether name is one of the skill directories
// SkillsFS carries — the exact set installSkills materializes, read from the
// same source rather than from a hand-kept list that could drift from it. A
// nil SkillsFS (a test binary that never linked the embed) answers false,
// which errs toward refusing an archive rather than filtering a real edit.
func isEmbeddedSkill(name string) bool {
	if SkillsFS == nil || name == "" {
		return false
	}
	entries, err := fs.ReadDir(SkillsFS, ".")
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() && e.Name() == name {
			return true
		}
	}
	return false
}

// UnarchiveJob moves an archived job back into the active job set under its
// original slug, refusing when that slug is taken again — the caller
// archives (or renames) the newer job first.
func UnarchiveJob(a *Job) error {
	slug := util.Slugify(a.Slug)
	if slug == "" {
		return fmt.Errorf("%s has an unusable slug — fix the job first", a.ArchivedName)
	}
	if err := ActiveStore().UnarchiveJob(a.ID); err != nil {
		if errors.Is(err, store.ErrSlugTaken) {
			return fmt.Errorf("%s", unarchiveTakenReason(slug))
		}
		return fmt.Errorf("unarchive %s: %w", a.ArchivedName, err)
	}
	// Move the preserved artifact file copies back — they never lived in
	// the DB to begin with. The prefix is <slug>-<id> on BOTH sides, exactly
	// as ArchiveJob wrote it: archiving no longer renames these files to the
	// numbered ArchivedName, so unarchiving must not rename them back off it.
	// Using ArchivedName here would match nothing and strand every artifact
	// in the archive dir while its job returned to the active board.
	prefix := cellName(a)
	_, skipped := movePrefixed(ArchiveDir(), prefix, PlutoDir(), prefix)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "kraken: artifact not moved: %s\n", s)
	}
	return nil
}

// unarchiveTakenReason turns the store's one ErrSlugTaken into the refusal
// that names a remedy that WORKS, by re-asking which of the two reservations
// blocked the unarchive. The store returns a single sentinel for both, and its
// own message reports a numeric job id the board never shows.
//
// The two remedies genuinely differ, which is the whole reason for the split.
// A slug held as another job's CURRENT name is freed by archiving OR renaming
// that job. A slug held as another job's FORMER name is freed by archiving it
// alone: a rename ADDS to the former set rather than clearing it, and renaming
// that job back onto the contested name is itself refused (RenameJob gate 5,
// "belongs to an archived job") — so the old advice sent carl round a loop
// with no exit.
//
// The probes run in the store's own order, current before former, so a slug
// that is somehow both reports the arm that actually fired. If neither
// resolves — the state changed under us, or the store is unreachable — the
// generic sentence is the honest answer rather than a guess at which.
func unarchiveTakenReason(slug string) string {
	st := ActiveStore()
	if st == nil {
		return fmt.Sprintf("slug %s is taken by an active job", slug)
	}
	if _, err := st.GetJobBySlug(slug); err == nil {
		return fmt.Sprintf("slug %s is taken by an active job — archive or rename that one first", slug)
	}
	if owner, err := st.GetJobByFormerSlug(slug); err == nil && owner != nil {
		return fmt.Sprintf("slug %s is reserved: the active job %s was renamed away from it, and a former name stays reserved for as long as that job is active — archive %s first (renaming it again will NOT free the name)", slug, owner.Slug, owner.Slug)
	}
	return fmt.Sprintf("slug %s is taken by an active job", slug)
}

// DeleteArchivedJob permanently removes an archived job: its store row and
// its archived artifact copies. Only archived jobs can be hard-deleted —
// anything active goes through archive first, so deletion is always the
// reversible-then-irreversible two-step.
func DeleteArchivedJob(a *Job) error {
	if err := ActiveStore().DeleteJob(a.ID); err != nil {
		return fmt.Errorf("delete %s: %w", a.ArchivedName, err)
	}
	// The archived artifact FILE copies too — api/board.go lists them
	// straight from ArchiveDir(), so a store-only delete would leave
	// orphaned files behind forever.
	// The prefix is the job's <slug>-<id>., NOT its ArchivedName: ArchiveJob
	// moves the files across under that name and never renames them to the
	// numbered archive name any more. Passing ArchivedName here would match
	// nothing and silently orphan every one of this job's archived files.
	removeArchivedArtifactFiles(PlutoArtifactPrefix(a))
	return nil
}

// removeArchivedArtifactFiles deletes every preserved artifact copy under
// ArchiveDir() carrying the given prefix (which already ends in "." — see
// PlutoArtifactPrefix). The store row itself is deleted separately by the
// caller; this only cleans up the plain artifact files that ride alongside
// it. Missing dir or an individual removal failure is logged, not fatal —
// same best-effort contract as movePrefixed's skipped list.
func removeArchivedArtifactFiles(prefix string) {
	entries, err := os.ReadDir(ArchiveDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, JobSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(ArchiveDir(), name)); err != nil {
			fmt.Fprintf(os.Stderr, "kraken: artifact not deleted: %v\n", err)
		}
	}
}

// ArchivedArtifactPaths lists an archived job's artifact copies (absolute
// paths), the archive twin of AllArtifactPaths.
func ArchivedArtifactPaths(a *Job) []string {
	entries, err := os.ReadDir(ArchiveDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, PlutoArtifactPrefix(a)) || strings.HasSuffix(name, JobSuffix) {
			continue
		}
		out = append(out, filepath.Join(ArchiveDir(), name))
	}
	sort.Strings(out)
	return out
}
