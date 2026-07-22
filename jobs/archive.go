package jobs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"kraken/util"
)

// archive.go — archived jobs. Archiving moves a job file into
// PlutoDir()/archive/, which frees its slug entirely: LoadJobs never
// descends into subdirectories, so an archived job is invisible to the
// daemon, the scheduler, and slug-collision marking. The move is pure — the
// frontmatter (including state) is untouched, so unarchiving restores the
// job exactly as it was: a done job comes back done, a draft comes back
// draft. Archived is therefore a *location*, distinct from the done state.
//
// Naming conflicts inside the archive are resolved by numbering the file:
// foo.job.md, then foo-2.job.md, foo-3.job.md, … The numbered file base
// (ArchivedName) is the identity every archive action uses — two archived
// jobs can share a Slug, never a file. Preserved pluto artifacts
// (<slug>.<name>) move along under the same numbered prefix; leaving them
// behind would leak the old job's deliverables onto a new job that reuses
// the freed slug (AllArtifactPaths matches by prefix).

func ArchiveDir() string {
	return filepath.Join(PlutoDir(), "archive")
}

// LoadArchivedJobs reads every *.job.md in the archive dir, sorted by file
// name, stamping each job's ArchivedName. Missing dir or unreadable files
// degrade to an empty/partial list, never an error — same contract as
// LoadJobs.
func LoadArchivedJobs() []*Job {
	entries, err := os.ReadDir(ArchiveDir())
	if err != nil {
		return nil
	}
	var jobs []*Job
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), JobSuffix) {
			continue
		}
		path := filepath.Join(ArchiveDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		j := ParseJob(e.Name(), data)
		j.File = path
		j.ArchivedName = strings.TrimSuffix(e.Name(), JobSuffix)
		if info, err := e.Info(); err == nil {
			j.ModTime = info.ModTime()
		}
		jobs = append(jobs, j)
	}
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].ArchivedName < jobs[b].ArchivedName })
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

// freeArchiveName picks the archive file base for a job being archived:
// the slug itself when free, else slug-2, slug-3, … The probe checks the
// job file only — artifacts always ride under the same prefix as their job
// file, so a free job name implies free artifact names.
func freeArchiveName(slug string) string {
	name := slug
	for n := 2; ; n++ {
		if _, err := os.Stat(filepath.Join(ArchiveDir(), name+JobSuffix)); os.IsNotExist(err) {
			return name
		}
		name = fmt.Sprintf("%s-%d", slug, n)
	}
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

// ArchiveJob moves an active job (file + preserved pluto artifacts) into
// the archive. Only a job with no cell in flight can be archived — same
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
	if err := os.MkdirAll(ArchiveDir(), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %v", ArchiveDir(), err)
	}
	name := freeArchiveName(util.Slugify(j.Slug))
	if err := os.Rename(j.File, filepath.Join(ArchiveDir(), name+JobSuffix)); err != nil {
		return "", fmt.Errorf("archive %s: %v", filepath.Base(j.File), err)
	}
	// artifacts follow the job file; a skipped one is loud, not fatal —
	// the job file move (the namespace release) already happened
	_, skipped := movePrefixed(PlutoDir(), util.Slugify(j.Slug), ArchiveDir(), name)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "kraken: artifact not moved (target exists or move failed): %s\n", s)
	}
	// uploaded images are already consumed into zeus' transcript by now —
	// best-effort like the cell dir removal in cell.go, nothing left to keep
	os.RemoveAll(UploadsDir(j.Slug))
	return name, nil
}

// UnarchiveJob moves an archived job back into the active pluto dir under
// its original slug, refusing when that slug is taken again — the caller
// archives (or renames) the newer job first.
func UnarchiveJob(a *Job) error {
	slug := util.Slugify(a.Slug)
	if slug == "" {
		return fmt.Errorf("%s has an unusable slug — fix the job: line in %s first", a.ArchivedName, a.File)
	}
	target := filepath.Join(PlutoDir(), slug+JobSuffix)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("slug %s is taken by an active job — archive or rename that one first", slug)
	}
	if active := JobBySlug(LoadJobs(), a.Slug); active != nil {
		return fmt.Errorf("slug %s is taken by an active job (%s) — archive or rename that one first", a.Slug, filepath.Base(active.File))
	}
	if err := os.Rename(a.File, target); err != nil {
		return fmt.Errorf("unarchive %s: %v", a.ArchivedName, err)
	}
	_, skipped := movePrefixed(ArchiveDir(), a.ArchivedName, PlutoDir(), slug)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "kraken: artifact not moved (target exists or move failed): %s\n", s)
	}
	return nil
}

// DeleteArchivedJob permanently removes an archived job: its file and its
// archived artifact copies. Only archived jobs can be hard-deleted —
// anything active goes through archive first, so deletion is always the
// reversible-then-irreversible two-step.
func DeleteArchivedJob(a *Job) error {
	if err := os.Remove(a.File); err != nil {
		return fmt.Errorf("delete %s: %v", a.ArchivedName, err)
	}
	// no uploads cleanup here: ArchiveJob already removed them at archive
	// time, and the freed slug may since belong to a NEW active job whose
	// uploads this would silently destroy
	entries, err := os.ReadDir(ArchiveDir())
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, a.ArchivedName+".") || strings.HasSuffix(name, JobSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(ArchiveDir(), name)); err != nil {
			fmt.Fprintf(os.Stderr, "kraken: artifact not deleted: %v\n", err)
		}
	}
	return nil
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
