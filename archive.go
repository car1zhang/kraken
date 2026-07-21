package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"kraken/util"
)

// archive.go — archived jobs. Archiving moves a job file into
// plutoDir()/archive/, which frees its slug entirely: loadJobs never
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
// the freed slug (allArtifactPaths matches by prefix).

func archiveDir() string {
	return filepath.Join(plutoDir(), "archive")
}

// loadArchivedJobs reads every *.job.md in the archive dir, sorted by file
// name, stamping each job's ArchivedName. Missing dir or unreadable files
// degrade to an empty/partial list, never an error — same contract as
// loadJobs.
func loadArchivedJobs() []*Job {
	entries, err := os.ReadDir(archiveDir())
	if err != nil {
		return nil
	}
	var jobs []*Job
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), jobSuffix) {
			continue
		}
		path := filepath.Join(archiveDir(), e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		j := parseJob(e.Name(), data)
		j.File = path
		j.ArchivedName = strings.TrimSuffix(e.Name(), jobSuffix)
		if info, err := e.Info(); err == nil {
			j.ModTime = info.ModTime()
		}
		jobs = append(jobs, j)
	}
	sort.SliceStable(jobs, func(a, b int) bool { return jobs[a].ArchivedName < jobs[b].ArchivedName })
	return jobs
}

// archivedJobByName resolves an archived job by its numbered file base
// (with or without the .job.md suffix).
func archivedJobByName(jobs []*Job, name string) *Job {
	name = strings.TrimSuffix(name, jobSuffix)
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
		if _, err := os.Stat(filepath.Join(archiveDir(), name+jobSuffix)); os.IsNotExist(err) {
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
		if e.IsDir() || !strings.HasPrefix(name, fromPrefix+".") || strings.HasSuffix(name, jobSuffix) {
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

// archiveJob moves an active job (file + preserved pluto artifacts) into
// the archive. Only a job with no cell in flight can be archived — same
// rule as editableJob: never started, or done. A working/blocked job must
// be killed first (kill returns it to draft, which then archives cleanly).
// Returns the archived name (numbered when the slug was already taken in
// the archive).
func archiveJob(j *Job, live map[string]bool) (string, error) {
	if j.started() && j.State != "done" {
		return "", fmt.Errorf("%s has a cell in flight (state %s) — kill it first (kill returns the job to draft), then archive", j.Slug, j.State)
	}
	if live[cellSession(j)] {
		return "", fmt.Errorf("%s still has a live cell session %s — kill it first", j.Slug, cellSession(j))
	}
	if err := os.MkdirAll(archiveDir(), 0o755); err != nil {
		return "", fmt.Errorf("mkdir %s: %v", archiveDir(), err)
	}
	name := freeArchiveName(util.Slugify(j.Slug))
	if err := os.Rename(j.File, filepath.Join(archiveDir(), name+jobSuffix)); err != nil {
		return "", fmt.Errorf("archive %s: %v", filepath.Base(j.File), err)
	}
	// artifacts follow the job file; a skipped one is loud, not fatal —
	// the job file move (the namespace release) already happened
	_, skipped := movePrefixed(plutoDir(), util.Slugify(j.Slug), archiveDir(), name)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "kraken: artifact not moved (target exists or move failed): %s\n", s)
	}
	return name, nil
}

// unarchiveJob moves an archived job back into the active pluto dir under
// its original slug, refusing when that slug is taken again — the caller
// archives (or renames) the newer job first.
func unarchiveJob(a *Job) error {
	slug := util.Slugify(a.Slug)
	if slug == "" {
		return fmt.Errorf("%s has an unusable slug — fix the job: line in %s first", a.ArchivedName, a.File)
	}
	target := filepath.Join(plutoDir(), slug+jobSuffix)
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("slug %s is taken by an active job — archive or rename that one first", slug)
	}
	if active := jobBySlug(loadJobs(), a.Slug); active != nil {
		return fmt.Errorf("slug %s is taken by an active job (%s) — archive or rename that one first", a.Slug, filepath.Base(active.File))
	}
	if err := os.Rename(a.File, target); err != nil {
		return fmt.Errorf("unarchive %s: %v", a.ArchivedName, err)
	}
	_, skipped := movePrefixed(archiveDir(), a.ArchivedName, plutoDir(), slug)
	for _, s := range skipped {
		fmt.Fprintf(os.Stderr, "kraken: artifact not moved (target exists or move failed): %s\n", s)
	}
	return nil
}

// deleteArchivedJob permanently removes an archived job: its file and its
// archived artifact copies. Only archived jobs can be hard-deleted —
// anything active goes through archive first, so deletion is always the
// reversible-then-irreversible two-step.
func deleteArchivedJob(a *Job) error {
	if err := os.Remove(a.File); err != nil {
		return fmt.Errorf("delete %s: %v", a.ArchivedName, err)
	}
	entries, err := os.ReadDir(archiveDir())
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, a.ArchivedName+".") || strings.HasSuffix(name, jobSuffix) {
			continue
		}
		if err := os.Remove(filepath.Join(archiveDir(), name)); err != nil {
			fmt.Fprintf(os.Stderr, "kraken: artifact not deleted: %v\n", err)
		}
	}
	return nil
}

// archivedArtifactPaths lists an archived job's artifact copies (absolute
// paths), the archive twin of allArtifactPaths.
func archivedArtifactPaths(a *Job) []string {
	entries, err := os.ReadDir(archiveDir())
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, a.ArchivedName+".") || strings.HasSuffix(name, jobSuffix) {
			continue
		}
		out = append(out, filepath.Join(archiveDir(), name))
	}
	sort.Strings(out)
	return out
}

// --- k archive / k unarchive ---

func cmdArchive(args []string) {
	if len(args) != 1 {
		util.Die("usage: k archive <job-slug>")
	}
	j := jobBySlug(loadJobs(), args[0])
	if j == nil {
		util.Die("no job %q in %s (k j lists them)", args[0], plutoDir())
	}
	name, err := archiveJob(j, liveSessions())
	if err != nil {
		util.Die("%v", err)
	}
	fmt.Printf("archived %s → %s (k unarchive %s restores it)\n", j.Slug, filepath.Join(archiveDir(), name+jobSuffix), name)
}

func cmdUnarchive(args []string) {
	if len(args) != 1 {
		util.Die("usage: k unarchive <archived-name>   (k j lists archived jobs last)")
	}
	a := archivedJobByName(loadArchivedJobs(), args[0])
	if a == nil {
		util.Die("no archived job %q in %s", args[0], archiveDir())
	}
	if err := unarchiveJob(a); err != nil {
		util.Die("%v", err)
	}
	fmt.Printf("unarchived %s → %s (state %q preserved)\n", a.ArchivedName, filepath.Join(plutoDir(), util.Slugify(a.Slug)+jobSuffix), a.State)
}
