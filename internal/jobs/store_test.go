package jobs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// useTestStore opens a temp-file store, wires the jobs package onto it
// (UseStore), and unwires it (ActiveStore() back to nil) plus closes the DB
// on test cleanup — every store-backed test in this file must call this
// first so it never leaks a wired store into a later, unrelated test.
func useTestStore(t *testing.T) *store.Store {
	t.Helper()
	// Sandbox $HOME so any accidental file-path fallthrough (a
	// code path that should have gone through ActiveStore but didn't) hits
	// a throwaway temp dir instead of a developer's real ~/.kraken or
	// ~/Documents/pluto.
	t.Setenv("HOME", t.TempDir())
	st, err := store.Open(filepath.Join(t.TempDir(), "kraken.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	UseStore(st)
	t.Cleanup(func() {
		// S14: UseStore(nil), not the raw activeStore.Store(nil) — the
		// latter bypassed UseStore's own nil-handling (it also resets
		// inProcessDaemonStatus and clears the event sink), leaking those
		// two into whatever test ran next. UseStore(nil) already does both,
		// so the separate util.SetEventSink(nil) this used to carry is
		// redundant too (S13's same fix, mirrored here).
		UseStore(nil)
		st.Close()
	})
	return st
}

func mustCreateJob(t *testing.T, st *store.Store, j store.Job) *store.Job {
	t.Helper()
	id, err := st.CreateJob(&j)
	if err != nil {
		t.Fatalf("create job %s: %v", j.Slug, err)
	}
	j.ID = id
	return &j
}

func TestUseStoreClearsInProcessDaemonStatus(t *testing.T) {
	inProcessDaemonStatus.Store(&DaemonStatus{PID: 999})
	useTestStore(t)
	if _, ok := ReadDaemonStatus(); ok {
		t.Fatal("want no status right after UseStore, got one")
	}
}

// TestUseStoreNilUninstallsEventSink covers: UseStore(nil) must
// clear util's event sink rather than install a closure that captures a nil
// *store.Store — logging anything after that (a background goroutine mid
// test-teardown, or a later test that forgot its own wiring) would call a
// method on a nil receiver and panic. A LogEvent call after UseStore(nil)
// must not panic and must not touch the DB the sink used to point at.
func TestUseStoreNilUninstallsEventSink(t *testing.T) {
	st := useTestStore(t)
	UseStore(nil)
	if ActiveStore() != nil {
		t.Fatal("want ActiveStore() nil after UseStore(nil)")
	}
	// must not panic: a stray LogEvent call after unwiring must not reach a
	// closure holding a nil *store.Store.
	util.LogEvent("test", "info", "after-unwire", "should not panic")
	events, err := st.ListEvents(10, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Event == "after-unwire" {
			t.Fatal("event logged after UseStore(nil) still reached the old store")
		}
	}
}

func TestLoadJobsStoreBacked(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "bbb", State: "", Ready: true})
	mustCreateJob(t, st, store.Job{Slug: "aaa"})

	jobs := LoadJobs()
	if len(jobs) != 2 {
		t.Fatalf("want 2 jobs, got %d", len(jobs))
	}
	if jobs[0].Slug != "aaa" || jobs[1].Slug != "bbb" {
		t.Fatalf("want slug-sorted [aaa bbb], got [%s %s]", jobs[0].Slug, jobs[1].Slug)
	}
	if jobs[0].ID == 0 {
		t.Fatal("want a populated store ID")
	}
	if jobs[1].Rev != 1 {
		t.Fatalf("want initial rev 1, got %d", jobs[1].Rev)
	}
}

func TestLoadJobBySlugStoreBacked(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "solo-job"})

	j := LoadJobBySlug("solo-job")
	if j == nil {
		t.Fatal("want a job, got nil")
	}
	if j.Slug != "solo-job" {
		t.Fatalf("unexpected job: %+v", j)
	}
	if LoadJobBySlug("nope") != nil {
		t.Fatal("want nil for an unknown slug")
	}
}

func TestWriteJobFieldsStoreBacked(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "wf"})
	j := LoadJobBySlug("wf")

	if err := WriteJobFields(j, "state", "working", "session", "sess-1"); err != nil {
		t.Fatalf("write fields: %v", err)
	}
	if j.State != "working" || j.Session != "sess-1" {
		t.Fatalf("local job not updated: %+v", j)
	}
	if j.Rev != 2 {
		t.Fatalf("want rev bumped to 2, got %d", j.Rev)
	}

	fresh, err := st.GetJobByID(sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.State != "working" || fresh.Session != "sess-1" || fresh.Rev != 2 {
		t.Fatalf("store row not updated: %+v", fresh)
	}

	// Stale rev: writing again with the OLD rev captured before
	// the first write must now retry against the freshly-fetched row rather
	// than fail outright — the caller's own field still lands, on top of
	// (not instead of) whatever the first write already committed.
	stale := &Job{ID: j.ID, Rev: 1, Slug: j.Slug}
	if err := WriteJobFields(stale, "state", "blocked"); err != nil {
		t.Fatalf("want the stale write to retry and succeed, got %v", err)
	}
	if stale.State != "blocked" || stale.Rev != 3 {
		t.Fatalf("retried write didn't update the local job: %+v", stale)
	}
	fresh2, err := st.GetJobByID(sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fresh2.State != "blocked" || fresh2.Session != "sess-1" || fresh2.Rev != 3 {
		t.Fatalf("store row not updated after retry: %+v", fresh2)
	}
}

// TestWriteJobFieldsStoreBackedConcurrentDifferentFields: two writers, each
// holding their own in-memory Job snapshot
// taken before either writes, touch DIFFERENT fields. Without the stale-rev
// retry, the second writer's call would fail outright (its captured Rev is
// stale the instant the first writer commits) and its field would be lost.
func TestWriteJobFieldsStoreBackedConcurrentDifferentFields(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "concurrent-wf"})
	a := LoadJobBySlug("concurrent-wf")
	b := LoadJobBySlug("concurrent-wf") // independent snapshot, same starting rev as a

	if err := WriteJobFields(a, "state", "working"); err != nil {
		t.Fatalf("writer a: %v", err)
	}
	// b still holds the pre-a rev: its write must retry against the fresh
	// row instead of failing, and must not clobber a's field.
	if err := WriteJobFields(b, "session", "sess-xyz"); err != nil {
		t.Fatalf("writer b: %v", err)
	}

	fresh := LoadJobBySlug("concurrent-wf")
	if fresh.State != "working" {
		t.Fatalf("writer a's field lost: %+v", fresh)
	}
	if fresh.Session != "sess-xyz" {
		t.Fatalf("writer b's field lost: %+v", fresh)
	}
}

func TestPRHelpersStoreBacked(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "pr-job"})
	j := LoadJobBySlug("pr-job")

	attached, err := appendJobPR(j, JobPR{URL: "https://github.com/x/y/pull/1", State: "open"})
	if err != nil || !attached {
		t.Fatalf("attach 1: attached=%v err=%v", attached, err)
	}
	// re-attaching the same URL is a no-op
	attached, err = appendJobPR(j, JobPR{URL: "https://github.com/x/y/pull/1", State: "open"})
	if err != nil || attached {
		t.Fatalf("re-attach: attached=%v err=%v (want false, nil)", attached, err)
	}

	// writeJobPRs must never drop a URL only present in the store (the
	// concurrent-attach race writeJobPRs' file-path union guards against).
	if _, err := appendJobPRToStore(st, j, JobPR{URL: "https://github.com/x/y/pull/2", State: "open"}); err != nil {
		t.Fatal(err)
	}
	if err := writeJobPRs(j, []JobPR{{URL: "https://github.com/x/y/pull/1", State: "merged"}}); err != nil {
		t.Fatal(err)
	}
	urls := map[string]string{}
	for _, p := range j.PRs {
		urls[p.URL] = p.State
	}
	if urls["https://github.com/x/y/pull/1"] != "merged" {
		t.Fatalf("pull/1 state: %+v", j.PRs)
	}
	if _, ok := urls["https://github.com/x/y/pull/2"]; !ok {
		t.Fatalf("pull/2 dropped, want it preserved: %+v", j.PRs)
	}

	removed, err := removeJobPRs(j, map[string]bool{"https://github.com/x/y/pull/2": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0] != "https://github.com/x/y/pull/2" {
		t.Fatalf("removed: %+v", removed)
	}
	for _, p := range j.PRs {
		if p.URL == "https://github.com/x/y/pull/2" {
			t.Fatal("pull/2 still present after remove")
		}
	}
}

func TestArchiveUnarchiveDeleteStoreBacked(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "arch-me", State: "done"})
	j := LoadJobBySlug("arch-me")

	name, err := ArchiveJob(j, map[string]bool{})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if name != "arch-me" {
		t.Fatalf("archived name: %q", name)
	}
	if LoadJobBySlug("arch-me") != nil {
		t.Fatal("archived job still resolves as active")
	}
	archived := LoadArchivedJobs()
	if len(archived) != 1 || archived[0].Slug != "arch-me" {
		t.Fatalf("archived jobs: %+v", archived)
	}

	if err := UnarchiveJob(archived[0]); err != nil {
		t.Fatalf("unarchive: %v", err)
	}
	if LoadJobBySlug("arch-me") == nil {
		t.Fatal("unarchived job doesn't resolve as active")
	}

	// re-archive, then hard-delete
	j = LoadJobBySlug("arch-me")
	if _, err := ArchiveJob(j, map[string]bool{}); err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	archived = LoadArchivedJobs()
	if err := DeleteArchivedJob(archived[0]); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(LoadArchivedJobs()) != 0 {
		t.Fatal("job survived DeleteArchivedJob")
	}
}

func TestBoardOrderStoreBacked(t *testing.T) {
	st := useTestStore(t)
	// Real job rows: board_order keys on jobs.id since schema v11, and
	// SaveColumnOrder resolves each slug through the active namespace.
	seedJobs(t, st, "a", "b")
	if err := SaveColumnOrder("working", []string{"b", "a", "b"}); err != nil {
		t.Fatal(err)
	}
	got := LoadBoardOrder()
	want := []string{"b", "a"}
	if len(got["working"]) != len(want) || got["working"][0] != want[0] || got["working"][1] != want[1] {
		t.Fatalf("order: %+v", got)
	}
}

func TestSaveUploadStoreBacked(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "up-job"})
	uploadsDir := t.TempDir()
	t.Setenv("KRAKEN_UPLOADS_DIR", uploadsDir)

	name, path, err := SaveUpload("up-job", "png", []byte("fake-png-bytes"))
	if err != nil {
		t.Fatalf("save upload: %v", err)
	}
	if name != "img-001.png" {
		t.Fatalf("name: %q", name)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("materialized file missing: %v", err)
	}
	if string(data) != "fake-png-bytes" {
		t.Fatalf("materialized bytes: %q", data)
	}
	uploads, err := st.ListUploads(sr.ID)
	if err != nil || len(uploads) != 1 {
		t.Fatalf("store uploads: %+v err=%v", uploads, err)
	}

	if err := RemoveUploads("up-job"); err != nil {
		t.Fatal(err)
	}
	uploads, _ = st.ListUploads(sr.ID)
	if len(uploads) != 0 {
		t.Fatalf("uploads survived RemoveUploads: %+v", uploads)
	}
}

// TestSaveUploadStoreBackedRejectsUnslugifiableSlugBeforeInsert covers
// E13: a slug that slugifies to nothing must refuse BEFORE ever inserting
// an uploads row — not insert first and only then discover it has nowhere
// to materialize the file. Uses a job whose slug DOES exist as a real job
// (so a "no active job" error can't be the thing short-circuiting this),
// via SaveUpload's own slug argument being the unslugifiable one directly
// (SaveUpload never requires the argument to already match a job's own
// slug column — it's UploadsDir's independent slugify check that matters
// here).
func TestSaveUploadStoreBackedRejectsUnslugifiableSlugBeforeInsert(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{Slug: "up-job-bad-slug"})
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())

	_, _, err := SaveUpload("!!!", "png", []byte("x"))
	if err == nil {
		t.Fatal("want an error for a slug that slugifies to nothing")
	}
	if !strings.Contains(err.Error(), "slugifies to nothing") {
		t.Fatalf("err = %v, want it to mention slugifies to nothing", err)
	}

	uploads, err := st.ListUploads(sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(uploads) != 0 {
		t.Fatalf("an uploads row was inserted despite the slug refusal: %+v", uploads)
	}
}

func TestUsageSnapshotStoreBacked(t *testing.T) {
	useTestStore(t)
	if _, ok := ReadUsageSnapshot(); ok {
		t.Fatal("want no snapshot before any write")
	}
	// exercise writeUsageSnapshot/ReadUsageSnapshot directly (the kv
	// round-trip) rather than the full UsagePass, which calls
	// core.FetchAllUsage — real account/network I/O this test must not
	// depend on, matching how usage_test.go already avoids it.
	want := core.UsageSnapshot{FetchedAt: 12345, Accounts: []core.AccountUsage{{Slug: "main", Current: true}}}
	if err := writeUsageSnapshot(want, UsageSnapshotPath()); err != nil {
		t.Fatalf("write snapshot: %v", err)
	}
	got, ok := ReadUsageSnapshot()
	if !ok {
		t.Fatal("want a snapshot after writeUsageSnapshot")
	}
	if got.FetchedAt != want.FetchedAt || len(got.Accounts) != 1 || got.Accounts[0].Slug != "main" {
		t.Fatalf("snapshot round-trip: %+v", got)
	}
}
