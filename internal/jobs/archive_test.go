package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	mustCreateJob(t, st, store.Job{Slug: "hot", Repo: "x", Ready: true, State: "working"})
	j := LoadJobBySlug("hot")
	if j == nil {
		t.Fatal("fixture didn't load")
	}
	if _, err := ArchiveJob(j, map[string]bool{}); err == nil || !strings.Contains(err.Error(), "kill it first") {
		t.Fatalf("want kill-first error, got %v", err)
	}
	// a live session blocks archiving even for a done job
	mustCreateJob(t, st, store.Job{Slug: "warm", Repo: "x", Ready: true, State: "done"})
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
	mustCreateJob(t, st, store.Job{Slug: "foo", Repo: "x", Ready: true, State: "done", Outcome: "PR merged"})
	if err := os.WriteFile(filepath.Join(PlutoDir(), "foo.report.md"), []byte("hi"), 0o644); err != nil {
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
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "foo.report.md")); err != nil {
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
	if _, err := os.Stat(filepath.Join(PlutoDir(), "foo.report.md")); err != nil {
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
	mustCreateJob(t, st, store.Job{Slug: "foo", Repo: "x"})
	if err := os.WriteFile(filepath.Join(PlutoDir(), "foo.report.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ArchiveJob(LoadJobBySlug("foo"), map[string]bool{}); err != nil {
		t.Fatal(err)
	}
	mustCreateJob(t, st, store.Job{Slug: "foo", Repo: "x"})
	if err := os.WriteFile(filepath.Join(PlutoDir(), "foo.doc.html"), []byte("x"), 0o644); err != nil {
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
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "foo.report.md")); !os.IsNotExist(err) {
		t.Fatal("artifact survived delete")
	}
	// foo-2 (a different archived job that shares the slug) must be untouched
	if ArchivedJobByName(LoadArchivedJobs(), "foo-2") == nil {
		t.Fatal("delete of foo took foo-2 with it")
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "foo-2.doc.html")); err != nil {
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
		{0, "carl/my-job"},
		{1, "carl/my-job"},
		{2, "carl/my-job-r2"},
		{7, "carl/my-job-r7"},
	}
	for _, c := range cases {
		if got := branchForRun("My Job", c.run); got != c.want {
			t.Errorf("run %d: got %q, want %q", c.run, got, c.want)
		}
		if got := CellBranch(&Job{Slug: "My Job", Run: c.run}); got != c.want {
			t.Errorf("CellBranch run %d: got %q, want %q", c.run, got, c.want)
		}
	}
}
