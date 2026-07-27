package jobs

import (
	"errors"
	"fmt"
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

// movePrefixed renames every non-dir file in fromDir named
// fromPrefix.<rest> to toDir/toPrefix.<rest>, skipping job files (the job
// file itself moves separately) and never overwriting: a conflicting
// target leaves the source in place and reports it, since silently
// clobbering either side would lose a deliverable.
func movePrefixed(fromDir, fromPrefix, toDir, toPrefix string) (moved, skipped []string) {
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
		dst := filepath.Join(toDir, toPrefix+"."+rest)
		if _, err := os.Stat(dst); err == nil {
			skipped = append(skipped, dst)
			continue
		}
		if err := os.Rename(filepath.Join(fromDir, name), dst); err != nil {
			skipped = append(skipped, dst)
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
	if live[CellSession(j)] {
		return "", fmt.Errorf("%s still has a live cell session %s — kill it first", j.Slug, CellSession(j))
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
	os.RemoveAll(UploadsDir(j.Slug, j.ID))
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
			fmt.Fprintf(os.Stderr, "kraken: artifact not moved (target exists or move failed): %s\n", s)
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
	// node_modules, the cell's own .claude/settings.json — all gitignored,
	// so `status --porcelain` says nothing about them).
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
func worktreeDirty(repo, wt string) (string, error) {
	out, err := exec.Command("git", "-C", wt, "status", "--porcelain").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
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
			return fmt.Errorf("slug %s is taken by an active job — archive or rename that one first", slug)
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
		fmt.Fprintf(os.Stderr, "kraken: artifact not moved (target exists or move failed): %s\n", s)
	}
	return nil
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
