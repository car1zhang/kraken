package store

import (
	"errors"
	"reflect"
	"testing"
)

func TestJobCRUDRoundTrip(t *testing.T) {
	s := openTest(t)
	j := &Job{
		Slug: "roundtrip", Type: "build", Repo: "kraken", Base: "main",
		Ready: true, Solo: false, Model: "opus", SkipDoc: true,
		Body: "do the thing", State: "", Outcome: "", Session: "",
	}
	id, err := s.CreateJob(j)
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if id == 0 {
		t.Fatalf("CreateJob returned id 0")
	}
	if j.Rev != 1 || j.Run != 1 {
		t.Fatalf("CreateJob didn't default Rev/Run: rev=%d run=%d", j.Rev, j.Run)
	}

	got, err := s.GetJobBySlug("roundtrip")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if got.ID != id || got.Repo != "kraken" || got.Base != "main" || !got.Ready || !got.SkipDoc || got.Model != "opus" || got.Body != "do the thing" {
		t.Fatalf("round trip mismatch: %+v", got)
	}

	byID, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if byID.Slug != "roundtrip" {
		t.Fatalf("GetJobByID mismatch: %+v", byID)
	}

	list, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != 1 || list[0].Slug != "roundtrip" {
		t.Fatalf("ListJobs = %+v", list)
	}
}

func TestGetJobBySlugNotFound(t *testing.T) {
	s := openTest(t)
	_, err := s.GetJobBySlug("nope")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateJobFieldsBumpsRevAndTouchesOnlyGivenColumns(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "update-me", Type: "build", Body: "orig"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	newRev, err := s.UpdateJobFields(id, 1, map[string]any{"state": "working"})
	if err != nil {
		t.Fatalf("UpdateJobFields: %v", err)
	}
	if newRev != 2 {
		t.Fatalf("newRev = %d, want 2", newRev)
	}

	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.State != "working" || got.Body != "orig" || got.Rev != 2 {
		t.Fatalf("update mismatch: %+v", got)
	}
}

// TestJobErrColumnRoundTrips covers L6: the err column is settable via
// CreateJob and UpdateJobFields, and round-trips through both GetJobByID
// and ListJobs (jobColumns' COALESCE(err,”)).
func TestJobErrColumnRoundTrips(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "err-job", Type: "build", Err: "bad frontmatter"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.Err != "bad frontmatter" {
		t.Fatalf("Err after CreateJob = %q, want %q", got.Err, "bad frontmatter")
	}

	if _, err := s.UpdateJobFields(id, got.Rev, map[string]any{"err": ""}); err != nil {
		t.Fatalf("UpdateJobFields clearing err: %v", err)
	}
	cleared, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Err != "" {
		t.Fatalf("Err after clearing = %q, want empty", cleared.Err)
	}

	jobs, err := s.ListJobs()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, j := range jobs {
		if j.Slug == "err-job" {
			found = true
			if j.Err != "" {
				t.Fatalf("ListJobs Err = %q, want empty", j.Err)
			}
		}
	}
	if !found {
		t.Fatal("err-job missing from ListJobs")
	}
}

// TestLoadJobsWithChildren covers L12: one call returns every active
// job plus a prereqs/PRs map keyed by job id, matching what ListJobs +
// per-job ListPrereqs/ListPRs would have returned separately.
func TestLoadJobsWithChildren(t *testing.T) {
	s := openTest(t)
	id1, err := s.CreateJob(&Job{Slug: "with-children-a", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob 1: %v", err)
	}
	id2, err := s.CreateJob(&Job{Slug: "with-children-b", Type: "info"})
	if err != nil {
		t.Fatalf("CreateJob 2: %v", err)
	}
	if err := s.ReplacePrereqs(id1, []Prereq{{Slug: "dep-x", Gated: true}}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpsertPR(id2, "https://github.com/x/y/pull/1", "open"); err != nil {
		t.Fatal(err)
	}

	rows, prereqs, prs, err := s.LoadJobsWithChildren()
	if err != nil {
		t.Fatalf("LoadJobsWithChildren: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 rows, got %d: %+v", len(rows), rows)
	}
	if !reflect.DeepEqual(prereqs[id1], []Prereq{{Slug: "dep-x", Gated: true}}) {
		t.Fatalf("prereqs[id1] = %+v", prereqs[id1])
	}
	if len(prereqs[id2]) != 0 {
		t.Fatalf("prereqs[id2] = %+v, want empty", prereqs[id2])
	}
	if len(prs[id1]) != 0 {
		t.Fatalf("prs[id1] = %+v, want empty", prs[id1])
	}
	if len(prs[id2]) != 1 || prs[id2][0].URL != "https://github.com/x/y/pull/1" {
		t.Fatalf("prs[id2] = %+v", prs[id2])
	}
}

// TestLoadArchivedJobsWithChildren mirrors TestLoadJobsWithChildren for
// the archive path (L12).
func TestLoadArchivedJobsWithChildren(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "archived-children", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.ReplacePrereqs(id, []Prereq{{Slug: "dep-y"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	rows, prereqs, prs, err := s.LoadArchivedJobsWithChildren()
	if err != nil {
		t.Fatalf("LoadArchivedJobsWithChildren: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != id {
		t.Fatalf("rows = %+v", rows)
	}
	if !reflect.DeepEqual(prereqs[id], []Prereq{{Slug: "dep-y"}}) {
		t.Fatalf("prereqs[id] = %+v", prereqs[id])
	}
	if len(prs[id]) != 0 {
		t.Fatalf("prs[id] = %+v, want empty", prs[id])
	}
}

func TestUpdateJobFieldsStaleRev(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "stale", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.UpdateJobFields(id, 1, map[string]any{"state": "working"}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// rev is now 2; retrying at the stale rev 1 must fail without applying
	_, err = s.UpdateJobFields(id, 1, map[string]any{"state": "blocked"})
	if !errors.Is(err, ErrStaleRev) {
		t.Fatalf("err = %v, want ErrStaleRev", err)
	}
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if got.State != "working" {
		t.Fatalf("stale update was applied: %+v", got)
	}
}

func TestUpdateJobFieldsMissingRowIsNotFound(t *testing.T) {
	s := openTest(t)
	if _, err := s.UpdateJobFields(999, 1, map[string]any{"state": "working"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateJobFieldsArchivedRowIsImmutable(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "frozen", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	// rev bumped by ArchiveJob to 2; even at the correct current rev, an
	// archived row must refuse the update rather than silently applying it.
	if _, err := s.UpdateJobFields(id, 2, map[string]any{"state": "working"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateJobFieldsSlugCollision(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateJob(&Job{Slug: "taken-slug", Type: "build"}); err != nil {
		t.Fatalf("CreateJob 1: %v", err)
	}
	id2, err := s.CreateJob(&Job{Slug: "movable", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob 2: %v", err)
	}
	if _, err := s.UpdateJobFields(id2, 1, map[string]any{"slug": "taken-slug"}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

func TestUpdateJobFieldsRejectsUnknownColumn(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "guard", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.UpdateJobFields(id, 1, map[string]any{"archived_at": 12345}); err == nil {
		t.Fatalf("expected error for non-allowlisted column")
	}
}

func TestArchiveFreesSlug(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "freeme", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	name, err := s.ArchiveJob(id)
	if err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if name != "freeme" {
		t.Fatalf("archived name = %q, want freeme", name)
	}

	// slug is now free for a brand new active job
	newID, err := s.CreateJob(&Job{Slug: "freeme", Type: "info"})
	if err != nil {
		t.Fatalf("CreateJob after archive: %v", err)
	}
	if newID == id {
		t.Fatalf("new job reused archived id")
	}

	active, err := s.GetJobBySlug("freeme")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if active.ID != newID || active.Type != "info" {
		t.Fatalf("active job lookup returned wrong row: %+v", active)
	}
}

// TestArchiveJobNumbersOffSlugifiedName covers E13: ArchiveJob's
// archived_name is derived from util.Slugify(slug), not the raw slug
// column — unified with jobs.ArchiveJob's file-mode path
// (freeArchiveName(util.Slugify(j.Slug))), since the archive name doubles
// as a filesystem-safe artifact-file prefix.
func TestArchiveJobNumbersOffSlugifiedName(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "Weird Slug!!", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	name, err := s.ArchiveJob(id)
	if err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if want := "weird-slug"; name != want {
		t.Fatalf("archived_name = %q, want the slugified form %q", name, want)
	}
}

func TestArchiveNumberedNameOnCollision(t *testing.T) {
	s := openTest(t)
	id1, err := s.CreateJob(&Job{Slug: "dup", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob 1: %v", err)
	}
	name1, err := s.ArchiveJob(id1)
	if err != nil {
		t.Fatalf("ArchiveJob 1: %v", err)
	}
	if name1 != "dup" {
		t.Fatalf("name1 = %q, want dup", name1)
	}

	id2, err := s.CreateJob(&Job{Slug: "dup", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob 2: %v", err)
	}
	name2, err := s.ArchiveJob(id2)
	if err != nil {
		t.Fatalf("ArchiveJob 2: %v", err)
	}
	if name2 != "dup-2" {
		t.Fatalf("name2 = %q, want dup-2", name2)
	}

	archived, err := s.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 2 {
		t.Fatalf("ListArchivedJobs = %+v", archived)
	}
}

func TestUnarchiveRefusesWhenSlugTaken(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "taken", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	// a new active job takes the freed slug
	if _, err := s.CreateJob(&Job{Slug: "taken", Type: "build"}); err != nil {
		t.Fatalf("CreateJob (reuse slug): %v", err)
	}

	err = s.UnarchiveJob(id)
	if !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("err = %v, want ErrSlugTaken", err)
	}
}

func TestUnarchiveRestoresJob(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "comeback", Type: "build", State: "done"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if _, err := s.ArchiveJob(id); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if err := s.UnarchiveJob(id); err != nil {
		t.Fatalf("UnarchiveJob: %v", err)
	}
	got, err := s.GetJobBySlug("comeback")
	if err != nil {
		t.Fatalf("GetJobBySlug after unarchive: %v", err)
	}
	if got.Archived() || got.State != "done" {
		t.Fatalf("unarchived job wrong: %+v", got)
	}
}

// TestListJobsToleratesRawNullColumns exercises the repo rule "degrade to
// blank fields, never errors" against a row written straight through the
// raw DB handle rather than CreateJob. archived_at/archived_name stay
// genuinely nullable (per schema.go) so they're inserted as explicit NULL
// here; every other optional column is simply omitted from the INSERT and
// picks up its NOT NULL DEFAULT — either way, nothing in this row went
// through the Go layer's usual zero-value initialization, so a scan bug
// (missing COALESCE / sql.Null*) would surface as a List error rather than
// a blank field.
func TestListJobsToleratesRawNullColumns(t *testing.T) {
	s := openTest(t)
	if _, err := s.DB().Exec(
		`INSERT INTO jobs (slug, type, archived_at, archived_name) VALUES (?, ?, NULL, NULL)`,
		"raw-null", "build",
	); err != nil {
		t.Fatalf("raw insert: %v", err)
	}

	list, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListJobs = %+v, want 1 row", list)
	}
	j := list[0]
	if j.Repo != "" || j.Base != "" || j.Model != "" || j.Body != "" || j.Outcome != "" ||
		j.Session != "" || j.Teardown != "" {
		t.Fatalf("expected blank text fields, got %+v", j)
	}
	if j.CreatedAt != 0 || j.UpdatedAt != 0 {
		t.Fatalf("expected zero timestamps, got %+v", j)
	}
	if j.Archived() || j.ArchivedName != "" {
		t.Fatalf("expected not-archived, got %+v", j)
	}

	got, err := s.GetJobBySlug("raw-null")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if got.ID != j.ID {
		t.Fatalf("GetJobBySlug mismatch: %+v", got)
	}
}

func TestCreateArchivedJobInsertsAndUpserts(t *testing.T) {
	s := openTest(t)
	j := &Job{
		Slug: "helper-catalog", Type: "info", Repo: "kraken", Ready: true,
		Body: "spec text", State: "done", Outcome: "closed",
		ArchivedAt: 1000, ArchivedName: "helper-catalog",
	}
	id, err := s.CreateArchivedJob(j)
	if err != nil {
		t.Fatalf("CreateArchivedJob: %v", err)
	}
	if id == 0 {
		t.Fatalf("CreateArchivedJob returned id 0")
	}

	archived, err := s.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 1 || archived[0].ArchivedName != "helper-catalog" || archived[0].Body != "spec text" {
		t.Fatalf("archived row: %+v", archived)
	}
	if archived[0].Rev != 1 {
		t.Fatalf("archived row rev = %d, want 1", archived[0].Rev)
	}

	// re-run with changed fields, same ArchivedName: must update in place,
	// not insert a second row
	j2 := &Job{
		Slug: "helper-catalog", Type: "info", Body: "updated spec",
		State: "done", Outcome: "closed", ArchivedAt: 2000, ArchivedName: "helper-catalog",
	}
	id2, err := s.CreateArchivedJob(j2)
	if err != nil {
		t.Fatalf("CreateArchivedJob (update): %v", err)
	}
	if id2 != id {
		t.Fatalf("update created a new row: id2=%d, want %d", id2, id)
	}
	archived2, err := s.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived2) != 1 || archived2[0].Body != "updated spec" {
		t.Fatalf("archived row after update: %+v", archived2)
	}
	// rev is untouched by an update — archived rows have no
	// optimistic-concurrency contract
	if archived2[0].Rev != 1 {
		t.Fatalf("archived row rev after update = %d, want unchanged 1", archived2[0].Rev)
	}
}

func TestCreateArchivedJobCoexistsWithActiveSlug(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateJob(&Job{Slug: "dup", Type: "build"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	// an archived row sharing the active job's slug (but a different,
	// numbered ArchivedName) must not collide on jobs_active_slug
	if _, err := s.CreateArchivedJob(&Job{Slug: "dup", Type: "build", ArchivedAt: 1000, ArchivedName: "dup-2"}); err != nil {
		t.Fatalf("CreateArchivedJob: %v", err)
	}
	active, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	if len(active) != 1 {
		t.Fatalf("active jobs = %+v, want 1", active)
	}
	archived, err := s.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	if len(archived) != 1 {
		t.Fatalf("archived jobs = %+v, want 1", archived)
	}
}

func TestDeleteJob(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "deleteme", Type: "build"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.DeleteJob(id); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	if _, err := s.GetJobByID(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if err := s.DeleteJob(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
}
