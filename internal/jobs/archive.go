package jobs

import (
	"errors"
	"fmt"
	"os"
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
// numbered name (ArchivedName) is the identity every archive action uses —
// two archived jobs can share a Slug, never an ArchivedName. Preserved
// pluto artifacts (<slug>.<name>, still plain files on disk — Q12) move
// along under the same numbered prefix; leaving them behind would leak the
// old job's deliverables onto a new job that reuses the freed slug
// (AllArtifactPaths matches by prefix).

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
	st := ActiveStore()
	// store.ArchiveJob numbers off util.Slugify(slug) itself — the raw-slug
	// and slugified forms only ever differ for characters slugify strips/
	// normalizes, which job slugs in practice never have, so it's the same
	// name movePrefixed below uses for the artifact-file prefix.
	name, err := st.ArchiveJob(j.ID)
	if err != nil {
		return "", fmt.Errorf("archive %s: %w", j.Slug, err)
	}
	// uploaded images are already consumed into zeus' transcript by now —
	// best-effort cleanup, both the DB rows and the materialized files
	if err := st.DeleteUploadsForJob(j.ID); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: uploads not deleted for %s: %v\n", j.Slug, err)
	}
	os.RemoveAll(UploadsDir(j.Slug))
	// Preserved pluto artifacts are still real files even though the job
	// row is store-backed (Q12 — live cells serve straight off disk, and
	// api/board.go lists preserved copies from the pluto/archive dirs), so
	// they still need moving into the archive dir here.
	if err := os.MkdirAll(ArchiveDir(), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "kraken: mkdir %s: %v\n", ArchiveDir(), err)
	} else {
		_, skipped := movePrefixed(PlutoDir(), util.Slugify(j.Slug), ArchiveDir(), name)
		for _, s := range skipped {
			fmt.Fprintf(os.Stderr, "kraken: artifact not moved (target exists or move failed): %s\n", s)
		}
	}
	j.ArchivedName = name
	return name, nil
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
	// the DB to begin with.
	_, skipped := movePrefixed(ArchiveDir(), a.ArchivedName, PlutoDir(), slug)
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
	removeArchivedArtifactFiles(a.ArchivedName)
	return nil
}

// removeArchivedArtifactFiles deletes every preserved artifact copy under
// ArchiveDir() prefixed archivedName+"." — the store row itself is deleted
// separately by the caller; this only cleans up the plain artifact files
// that ride alongside it. Missing dir or an individual removal failure is
// logged, not fatal — same best-effort contract as movePrefixed's skipped
// list.
func removeArchivedArtifactFiles(archivedName string) {
	entries, err := os.ReadDir(ArchiveDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, archivedName+".") || strings.HasSuffix(name, JobSuffix) {
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
		if e.IsDir() || !strings.HasPrefix(name, a.ArchivedName+".") || strings.HasSuffix(name, JobSuffix) {
			continue
		}
		out = append(out, filepath.Join(ArchiveDir(), name))
	}
	sort.Strings(out)
	return out
}
