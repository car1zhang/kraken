package jobs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"kraken/internal/store"
)

// archive_test.go — ArchiveJob/UnarchiveJob/DeleteArchivedJob are
// store-backed (job rows), but preserved pluto artifacts stay real files
// under PlutoDir()/ArchiveDir() even so (Q12) — every test here wires a
// store (useTestStore) for the job rows and still exercises the artifact
// file moves directly.

func TestArchiveRefusesInFlight(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "hot", Repo: "x", State: "working"})
	j := LoadJobBySlug("hot")
	if j == nil {
		t.Fatal("fixture didn't load")
	}
	if _, err := ArchiveJob(j, map[string]bool{}); err == nil || !strings.Contains(err.Error(), "kill it first") {
		t.Fatalf("want kill-first error, got %v", err)
	}
	// a live session blocks archiving even for a done job
	mustCreateJob(t, st, store.Job{Slug: "warm", Repo: "x", State: "done"})
	j = LoadJobBySlug("warm")
	if _, err := ArchiveJob(j, map[string]bool{CellSession(j): true}); err == nil || !strings.Contains(err.Error(), "live cell") {
		t.Fatalf("want live-cell error, got %v", err)
	}
}

func TestArchiveUnarchiveRoundTrip(t *testing.T) {
	st := useTestStore(t)
	if err := os.MkdirAll(PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	r1 := mustCreateJob(t, st, store.Job{Slug: "foo", Repo: "x", State: "done", Outcome: "PR merged"})
	// artifact copies are prefixed <slug>-<id>., the same name they carried
	// while the job was live — archiving moves them without renaming
	if err := os.WriteFile(filepath.Join(PlutoDir(), fmt.Sprintf("foo-%d.report.md", r1.ID)), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := LoadJobBySlug("foo")
	name, err := ArchiveJob(j, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "foo" {
		t.Fatalf("archived name: got %q, want foo", name)
	}
	if LoadJobBySlug("foo") != nil {
		t.Fatal("job still active after archive")
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), fmt.Sprintf("foo-%d.report.md", r1.ID))); err != nil {
		t.Fatal("artifact didn't move to archive")
	}

	// the slug is free: a new active foo can exist, and archiving it numbers
	// the archive copy
	mustCreateJob(t, st, store.Job{Slug: "foo", Repo: "x"})
	j2 := LoadJobBySlug("foo")
	name2, err := ArchiveJob(j2, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if name2 != "foo-2" {
		t.Fatalf("second archive: got %q, want foo-2", name2)
	}

	archived := LoadArchivedJobs()
	if len(archived) != 2 {
		t.Fatalf("want 2 archived, got %d", len(archived))
	}
	a := ArchivedJobByName(archived, "foo")
	if a == nil || a.State != "done" || a.Slug != "foo" {
		t.Fatalf("archived foo lost its identity: %+v", a)
	}

	// restore the original: slug is free again (foo-2 still archived)
	if err := UnarchiveJob(a); err != nil {
		t.Fatal(err)
	}
	restored := LoadJobBySlug("foo")
	if restored == nil || restored.State != "done" || restored.Outcome != "PR merged" {
		t.Fatalf("unarchive didn't preserve state/outcome: %+v", restored)
	}
	if _, err := os.Stat(filepath.Join(PlutoDir(), fmt.Sprintf("foo-%d.report.md", r1.ID))); err != nil {
		t.Fatal("artifact didn't move back")
	}

	// now the slug is taken: unarchiving foo-2 must refuse
	a2 := ArchivedJobByName(LoadArchivedJobs(), "foo-2")
	if err := UnarchiveJob(a2); err == nil || !strings.Contains(err.Error(), "taken") {
		t.Fatalf("want slug-taken error, got %v", err)
	}
}

func TestDeleteArchivedJob(t *testing.T) {
	st := useTestStore(t)
	if err := os.MkdirAll(PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// Two generations of the slug "foo". Their artifact files are kept apart
	// by the job ID in the prefix, not by the archive's -2 numbering — which
	// is why deleting one can no longer touch the other's files.
	r1 := mustCreateJob(t, st, store.Job{Slug: "foo", Repo: "x"})
	f1 := fmt.Sprintf("foo-%d.report.md", r1.ID)
	if err := os.WriteFile(filepath.Join(PlutoDir(), f1), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveJob(LoadJobBySlug("foo"), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	r2 := mustCreateJob(t, st, store.Job{Slug: "foo", Repo: "x"})
	f2 := fmt.Sprintf("foo-%d.doc.html", r2.ID)
	if err := os.WriteFile(filepath.Join(PlutoDir(), f2), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveJob(LoadJobBySlug("foo"), map[string]bool{}); err != nil {
		t.Fatal(err)
	}

	a := ArchivedJobByName(LoadArchivedJobs(), "foo")
	if err := DeleteArchivedJob(a); err != nil {
		t.Fatal(err)
	}
	if ArchivedJobByName(LoadArchivedJobs(), "foo") != nil {
		t.Fatal("job row survived delete")
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), f1)); !os.IsNotExist(err) {
		t.Fatal("artifact survived delete")
	}
	// foo-2 (a different archived job that shares the slug) must be untouched
	if ArchivedJobByName(LoadArchivedJobs(), "foo-2") == nil {
		t.Fatal("delete of foo took foo-2 with it")
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), f2)); err != nil {
		t.Fatal("delete of foo took foo-2's artifact with it")
	}
}

func TestAutoArchivable(t *testing.T) {
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		j    *Job
		want bool
	}{
		{"done 25h old", &Job{State: "done", ModTime: now.Add(-25 * time.Hour)}, true},
		{"done 23h old", &Job{State: "done", ModTime: now.Add(-23 * time.Hour)}, false},
		{"done exactly 24h old", &Job{State: "done", ModTime: now.Add(-24 * time.Hour)}, false},
		{"working, old mtime", &Job{State: "working", ModTime: now.Add(-48 * time.Hour)}, false},
		{"blocked, old mtime", &Job{State: "blocked", ModTime: now.Add(-48 * time.Hour)}, false},
		{"never started, old mtime", &Job{State: "", ModTime: now.Add(-48 * time.Hour)}, false},
		{"done, old, with parse err", &Job{State: "done", ModTime: now.Add(-48 * time.Hour), Err: "duplicate slug"}, true},
		{"done, zero ModTime", &Job{State: "done"}, false},
	}
	for _, c := range cases {
		if got := autoArchivable(c.j, now); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestParseSessionAndRun(t *testing.T) {
	j := ParseJob("x.job.md", []byte("---\njob: x\nrepo: r\nsession: 0b0e6f7e-1111-2222-3333-444455556666\nrun: 3\n---\nbody"))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if j.Session != "0b0e6f7e-1111-2222-3333-444455556666" || j.Run != 3 {
		t.Fatalf("session/run not parsed: %+v", j)
	}
	bad := ParseJob("x.job.md", []byte("---\njob: x\nrun: nope\n---\n"))
	if !strings.Contains(bad.Err, "bad run value") {
		t.Fatalf("want bad-run error, got %q", bad.Err)
	}
}

func TestBranchForRun(t *testing.T) {
	cases := []struct {
		run  int
		want string
	}{
		{0, "carl/my-job-3"},
		{1, "carl/my-job-3"},
		{2, "carl/my-job-3-r2"},
		{7, "carl/my-job-3-r7"},
	}
	for _, c := range cases {
		if got := branchForRun(&Job{Slug: "My Job", ID: 3}, c.run); got != c.want {
			t.Errorf("run %d: got %q, want %q", c.run, got, c.want)
		}
		if got := CellBranch(&Job{Slug: "My Job", ID: 3, Run: c.run}); got != c.want {
			t.Errorf("CellBranch run %d: got %q, want %q", c.run, got, c.want)
		}
	}
}

// TestIsKrakenWorktreeDirt pins which porcelain lines the archive-refusal
// filter treats as kraken's own writes. Monolith TRACKS .claude/settings.json,
// so installHookSettings' merge dirties every cell worktree there forever —
// unfiltered, that one line made archiving any such job refuse while naming
// only files kraken itself wrote (and the old comment beside the release
// claimed the opposite: that porcelain never sees them).
func TestIsKrakenWorktreeDirt(t *testing.T) {
	// SkillsFS is nil in a bare jobs-package test binary (the embed lives at
	// the repo root), so stand in a fixture carrying the names this test
	// asserts on — isEmbeddedSkill reads the same source installSkills does.
	prev := SkillsFS
	SkillsFS = fstest.MapFS{
		"do/SKILL.md":  {Data: []byte("x")},
		"aim/SKILL.md": {Data: []byte("x")},
	}
	t.Cleanup(func() { SkillsFS = prev })

	dirt := []string{
		" M .claude/settings.json",
		"?? .claude/settings.json",
		"?? .claude/", // a repo with no .claude at all collapses to this
		"?? .claude/skills/",
		"?? .claude/skills/do/SKILL.md",
		`?? ".claude/skills/aim/"`,
	}
	for _, line := range dirt {
		if !isKrakenWorktreeDirt(line) {
			t.Errorf("line %q should be filtered as kraken's own write", line)
		}
	}
	// installSkills SKIPS tracked skills, so a tracked-status line under
	// .claude/skills/ can only be an agent editing one — in the kraken repo
	// that is a dream job's uncommitted deliverable, and it must refuse.
	real := []string{
		" M internal/jobs/cell.go",
		" M .claude/skills/do/SKILL.md",
		" D .claude/skills/aim/SKILL.md",
		"?? scratch.md",
		// an agent AUTHORING a new skill is a deliverable, not kraken's write
		"?? .claude/skills/newskill/SKILL.md",
		" M .claude/settings.json.bak",
		"?? .claude/hooks/guard.sh",
		"R  .claude/skills/do/SKILL.md -> docs/moved.md",
		"R  internal/jobs/cell.go -> .claude/skills/x.md",
		"",
	}
	for _, line := range real {
		if isKrakenWorktreeDirt(line) {
			t.Errorf("line %q is real work and must still refuse the archive", line)
		}
	}
}

// TestWorktreeDirtyFiltersKrakenWrites runs the filter against a real git
// worktree whose only dirt is kraken's own two paths, then adds real work
// and holds that the refusal comes back.
func TestWorktreeDirtyFiltersKrakenWrites(t *testing.T) {
	repo := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.MkdirAll(filepath.Join(repo, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".claude/settings.json")
	run("commit", "-q", "-m", "track settings")
	// kraken's writes: modify the tracked settings, materialize skills
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte("{\"hooks\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo, ".claude", "skills", "do"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "skills", "do", "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if dirty, err := worktreeDirty(repo, repo); err != nil || dirty != "" {
		t.Fatalf("kraken-only dirt must read clean, got %q err %v", dirty, err)
	}
	// real uncommitted work must still refuse
	if err := os.WriteFile(filepath.Join(repo, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirty, err := worktreeDirty(repo, repo)
	if err != nil || !strings.Contains(dirty, "main.go") {
		t.Fatalf("real work must still report dirty, got %q err %v", dirty, err)
	}
	if strings.Contains(dirty, ".claude") {
		t.Fatalf("kraken's paths must stay filtered even beside real dirt: %q", dirty)
	}
}
