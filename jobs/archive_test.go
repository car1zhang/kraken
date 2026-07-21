package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeArchiveFixture writes a minimal parseable job file into dir.
func writeArchiveFixture(t *testing.T, dir, name, frontmatter string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := "---\n" + frontmatter + "---\n\n# Spec\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFreeArchiveName(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	if got := freeArchiveName("foo"); got != "foo" {
		t.Fatalf("empty archive: got %q, want foo", got)
	}
	if err := os.MkdirAll(ArchiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, ArchiveDir(), "foo.job.md", "job: foo\nrepo: x\n")
	if got := freeArchiveName("foo"); got != "foo-2" {
		t.Fatalf("one taken: got %q, want foo-2", got)
	}
	writeArchiveFixture(t, ArchiveDir(), "foo-2.job.md", "job: foo\nrepo: x\n")
	if got := freeArchiveName("foo"); got != "foo-3" {
		t.Fatalf("two taken: got %q, want foo-3", got)
	}
}

func TestArchiveRefusesInFlight(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeArchiveFixture(t, PlutoDir(), "hot.job.md", "job: hot\nrepo: x\nready: true\nstate: working\n")
	j := JobBySlug(LoadJobs(), "hot")
	if j == nil {
		t.Fatal("fixture didn't load")
	}
	if _, err := ArchiveJob(j, map[string]bool{}); err == nil || !strings.Contains(err.Error(), "kill it first") {
		t.Fatalf("want kill-first error, got %v", err)
	}
	// a live session blocks archiving even for a done job
	writeArchiveFixture(t, PlutoDir(), "warm.job.md", "job: warm\nrepo: x\nready: true\nstate: done\n")
	j = JobBySlug(LoadJobs(), "warm")
	if _, err := ArchiveJob(j, map[string]bool{CellSession(j): true}); err == nil || !strings.Contains(err.Error(), "live cell") {
		t.Fatalf("want live-cell error, got %v", err)
	}
}

func TestArchiveUnarchiveRoundTrip(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeArchiveFixture(t, PlutoDir(), "foo.job.md", "job: foo\nrepo: x\nready: true\nstate: done\noutcome: PR merged\n")
	if err := os.WriteFile(filepath.Join(PlutoDir(), "foo.report.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := JobBySlug(LoadJobs(), "foo")
	name, err := ArchiveJob(j, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "foo" {
		t.Fatalf("archived name: got %q, want foo", name)
	}
	if len(LoadJobs()) != 0 {
		t.Fatal("job still active after archive")
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "foo.report.md")); err != nil {
		t.Fatal("artifact didn't move to archive")
	}

	// the slug is free: a new active foo can exist, and archiving it numbers
	// the archive copy
	writeArchiveFixture(t, PlutoDir(), "foo.job.md", "job: foo\nrepo: x\n")
	j2 := JobBySlug(LoadJobs(), "foo")
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
	restored := JobBySlug(LoadJobs(), "foo")
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
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	if err := os.MkdirAll(ArchiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, ArchiveDir(), "foo.job.md", "job: foo\nrepo: x\n")
	writeArchiveFixture(t, ArchiveDir(), "foo-2.job.md", "job: foo\nrepo: x\n")
	for _, n := range []string{"foo.report.md", "foo-2.doc.html"} {
		if err := os.WriteFile(filepath.Join(ArchiveDir(), n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := ArchivedJobByName(LoadArchivedJobs(), "foo")
	if err := DeleteArchivedJob(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "foo.job.md")); !os.IsNotExist(err) {
		t.Fatal("job file survived delete")
	}
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "foo.report.md")); !os.IsNotExist(err) {
		t.Fatal("artifact survived delete")
	}
	// foo-2 (a different archived job that shares the slug) must be untouched
	if _, err := os.Stat(filepath.Join(ArchiveDir(), "foo-2.job.md")); err != nil {
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
