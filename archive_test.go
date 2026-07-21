package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	if err := os.MkdirAll(archiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, archiveDir(), "foo.job.md", "job: foo\nrepo: x\n")
	if got := freeArchiveName("foo"); got != "foo-2" {
		t.Fatalf("one taken: got %q, want foo-2", got)
	}
	writeArchiveFixture(t, archiveDir(), "foo-2.job.md", "job: foo\nrepo: x\n")
	if got := freeArchiveName("foo"); got != "foo-3" {
		t.Fatalf("two taken: got %q, want foo-3", got)
	}
}

func TestArchiveRefusesInFlight(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeArchiveFixture(t, plutoDir(), "hot.job.md", "job: hot\nrepo: x\nready: true\nstate: working\n")
	j := jobBySlug(loadJobs(), "hot")
	if j == nil {
		t.Fatal("fixture didn't load")
	}
	if _, err := archiveJob(j, map[string]bool{}); err == nil || !strings.Contains(err.Error(), "kill it first") {
		t.Fatalf("want kill-first error, got %v", err)
	}
	// a live session blocks archiving even for a done job
	writeArchiveFixture(t, plutoDir(), "warm.job.md", "job: warm\nrepo: x\nready: true\nstate: done\n")
	j = jobBySlug(loadJobs(), "warm")
	if _, err := archiveJob(j, map[string]bool{cellSession(j): true}); err == nil || !strings.Contains(err.Error(), "live cell") {
		t.Fatalf("want live-cell error, got %v", err)
	}
}

func TestArchiveUnarchiveRoundTrip(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeArchiveFixture(t, plutoDir(), "foo.job.md", "job: foo\nrepo: x\nready: true\nstate: done\noutcome: PR merged\n")
	if err := os.WriteFile(filepath.Join(plutoDir(), "foo.report.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	j := jobBySlug(loadJobs(), "foo")
	name, err := archiveJob(j, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if name != "foo" {
		t.Fatalf("archived name: got %q, want foo", name)
	}
	if len(loadJobs()) != 0 {
		t.Fatal("job still active after archive")
	}
	if _, err := os.Stat(filepath.Join(archiveDir(), "foo.report.md")); err != nil {
		t.Fatal("artifact didn't move to archive")
	}

	// the slug is free: a new active foo can exist, and archiving it numbers
	// the archive copy
	writeArchiveFixture(t, plutoDir(), "foo.job.md", "job: foo\nrepo: x\n")
	j2 := jobBySlug(loadJobs(), "foo")
	name2, err := archiveJob(j2, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if name2 != "foo-2" {
		t.Fatalf("second archive: got %q, want foo-2", name2)
	}

	archived := loadArchivedJobs()
	if len(archived) != 2 {
		t.Fatalf("want 2 archived, got %d", len(archived))
	}
	a := archivedJobByName(archived, "foo")
	if a == nil || a.State != "done" || a.Slug != "foo" {
		t.Fatalf("archived foo lost its identity: %+v", a)
	}

	// restore the original: slug is free again (foo-2 still archived)
	if err := unarchiveJob(a); err != nil {
		t.Fatal(err)
	}
	restored := jobBySlug(loadJobs(), "foo")
	if restored == nil || restored.State != "done" || restored.Outcome != "PR merged" {
		t.Fatalf("unarchive didn't preserve state/outcome: %+v", restored)
	}
	if _, err := os.Stat(filepath.Join(plutoDir(), "foo.report.md")); err != nil {
		t.Fatal("artifact didn't move back")
	}

	// now the slug is taken: unarchiving foo-2 must refuse
	a2 := archivedJobByName(loadArchivedJobs(), "foo-2")
	if err := unarchiveJob(a2); err == nil || !strings.Contains(err.Error(), "taken") {
		t.Fatalf("want slug-taken error, got %v", err)
	}
}

func TestDeleteArchivedJob(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	if err := os.MkdirAll(archiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, archiveDir(), "foo.job.md", "job: foo\nrepo: x\n")
	writeArchiveFixture(t, archiveDir(), "foo-2.job.md", "job: foo\nrepo: x\n")
	for _, n := range []string{"foo.report.md", "foo-2.doc.html"} {
		if err := os.WriteFile(filepath.Join(archiveDir(), n), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a := archivedJobByName(loadArchivedJobs(), "foo")
	if err := deleteArchivedJob(a); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(archiveDir(), "foo.job.md")); !os.IsNotExist(err) {
		t.Fatal("job file survived delete")
	}
	if _, err := os.Stat(filepath.Join(archiveDir(), "foo.report.md")); !os.IsNotExist(err) {
		t.Fatal("artifact survived delete")
	}
	// foo-2 (a different archived job that shares the slug) must be untouched
	if _, err := os.Stat(filepath.Join(archiveDir(), "foo-2.job.md")); err != nil {
		t.Fatal("delete of foo took foo-2 with it")
	}
	if _, err := os.Stat(filepath.Join(archiveDir(), "foo-2.doc.html")); err != nil {
		t.Fatal("delete of foo took foo-2's artifact with it")
	}
}

func TestParseSessionAndRun(t *testing.T) {
	j := parseJob("x.job.md", []byte("---\njob: x\nrepo: r\nsession: 0b0e6f7e-1111-2222-3333-444455556666\nrun: 3\n---\nbody"))
	if j.Err != "" {
		t.Fatalf("unexpected err: %s", j.Err)
	}
	if j.Session != "0b0e6f7e-1111-2222-3333-444455556666" || j.Run != 3 {
		t.Fatalf("session/run not parsed: %+v", j)
	}
	bad := parseJob("x.job.md", []byte("---\njob: x\nrun: nope\n---\n"))
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
		if got := cellBranch(&Job{Slug: "My Job", Run: c.run}); got != c.want {
			t.Errorf("cellBranch run %d: got %q, want %q", c.run, got, c.want)
		}
	}
}

func TestSerializeJobFilePreservesSessionAndRun(t *testing.T) {
	f := jobFormFields{Slug: "x", Type: "build", Repo: "r", Success: "works"}
	content := serializeJobFile(f, "done", "shipped", "abc-123", 2, nil, "")
	j := parseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("round-trip err: %s", j.Err)
	}
	if j.State != "done" || j.Outcome != "shipped" || j.Session != "abc-123" || j.Run != 2 {
		t.Fatalf("kraken-owned fields lost: %+v", j)
	}
	// and absent fields stay absent
	content = serializeJobFile(f, "", "", "", 0, nil, "")
	if strings.Contains(content, "session:") || strings.Contains(content, "run:") {
		t.Fatalf("empty session/run serialized anyway:\n%s", content)
	}
}

func TestArchivedArtifactServing(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	if err := os.MkdirAll(archiveDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	writeArchiveFixture(t, archiveDir(), "foo-2.job.md", "job: foo\nrepo: x\n")
	if err := os.WriteFile(filepath.Join(archiveDir(), "foo-2.report.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := archivedJobByName(loadArchivedJobs(), "foo-2")
	arts := archivedWebArtifacts(a)
	if len(arts) != 1 || arts[0].Name != "report.md" || arts[0].URL != "/api/artifact/foo-2/archive/report.md" {
		t.Fatalf("unexpected artifacts: %+v", arts)
	}
	if p, ok := artifactPath(a, "archive", "report.md"); !ok || p != filepath.Join(archiveDir(), "foo-2.report.md") {
		t.Fatalf("artifactPath archive: %q %v", p, ok)
	}
	// traversal and job files are refused
	if _, ok := artifactPath(a, "archive", "../foo-2.report.md"); ok {
		t.Fatal("traversal allowed")
	}
	if _, ok := artifactPath(a, "archive", "job.md"); ok {
		// foo-2.job.md must not be reachable as artifact "job.md"
		t.Fatal("job file served as artifact")
	}
	// an active job (no ArchivedName) never resolves in the archive home
	if _, ok := artifactPath(&Job{Slug: "foo"}, "archive", "report.md"); ok {
		t.Fatal("active job resolved in archive home")
	}
}
