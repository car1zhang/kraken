package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// spawn_test.go — the spawn-link stamp (spawn.go) on its two routes and the
// board's two keys (board.go's spawnIndex). Obligations from
// 52-testing-plan.md's /api/job/draft, /api/queue and board rows.

func spawnRows(t *testing.T, st *store.Store) []store.SpawnLink {
	t.Helper()
	rows, err := st.ListJobSpawns()
	if err != nil {
		t.Fatalf("ListJobSpawns: %v", err)
	}
	return rows
}

func postDraft(t *testing.T, slug, fromCell string) *httptest.ResponseRecorder {
	t.Helper()
	content := "---\njob: " + slug + "\nrepo: kraken\n---\n\nreply with the single word pong\n"
	body := mustJSON(t, map[string]any{"slug": slug, "content": content})
	req := httptest.NewRequest(http.MethodPost, "/api/job/draft", bytes.NewReader(body))
	if fromCell != "" {
		req.Header.Set(fromCellHeader, fromCell)
	}
	rec := httptest.NewRecorder()
	jobDraftHandler(rec, req)
	return rec
}

func postSubmit(t *testing.T, mux http.Handler, slug, fromCell string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/queue", bytes.NewReader(mustJSON(t, webReq{Slug: slug})))
	if fromCell != "" {
		req.Header.Set(fromCellHeader, fromCell)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func idOf(t *testing.T, st *store.Store, slug string) int64 {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug %s: %v", slug, err)
	}
	return j.ID
}

// A draft create carrying another active job's slug stamps that job as the
// parent, via draft; the create's answer is unchanged.
func TestSpawnDraftCreateStampsParent(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "parent", Repo: "kraken", State: "working", Body: "spec\n"})
	if rec := postDraft(t, "child", "parent"); rec.Code != http.StatusOK {
		t.Fatalf("draft: %d %s", rec.Code, rec.Body.String())
	}
	rows := spawnRows(t, st)
	if len(rows) != 1 || rows[0].ChildID != idOf(t, st, "child") || rows[0].ParentID != idOf(t, st, "parent") || rows[0].Via != jobs.SpawnViaDraft {
		t.Fatalf("rows = %+v, want child <- parent via draft", rows)
	}
}

// No header, a header naming no active job, and a header naming the job being
// created all record nothing and leave the answer alone.
func TestSpawnDraftCreateIgnoresUnusableHeaders(t *testing.T) {
	st := useTestStore(t)
	for _, tc := range []struct{ slug, from string }{
		{"no-header", ""},
		{"unknown-parent", "nobody-here"},
		{"self", "self"},
	} {
		if rec := postDraft(t, tc.slug, tc.from); rec.Code != http.StatusOK {
			t.Fatalf("%s: draft %d %s", tc.slug, rec.Code, rec.Body.String())
		}
	}
	if rows := spawnRows(t, st); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// The edit arm never stamps, header or not.
func TestSpawnDraftEditNeverStamps(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "parent", Repo: "kraken", State: "working", Body: "spec\n"})
	if rec := postDraft(t, "child", ""); rec.Code != http.StatusOK {
		t.Fatalf("create: %d", rec.Code)
	}
	if rec := postDraft(t, "child", "parent"); rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", rec.Code, rec.Body.String())
	}
	if rows := spawnRows(t, st); len(rows) != 0 {
		t.Fatalf("an edit stamped a parent: %+v", rows)
	}
}

// A successful submit from a cell stamps via submit when the child has no
// parent yet; a child the draft already stamped keeps its first parent.
func TestSpawnSubmitStampsOnceAfterStart(t *testing.T) {
	SetMirrorMode(false)
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "parent", Repo: "kraken", State: "working", Body: "spec\n"})
	mustCreateStoreJob(t, st, store.Job{Slug: "other", Repo: "kraken", State: "working", Body: "spec\n"})
	mustCreateStoreJob(t, st, store.Job{Slug: "child", Repo: "kraken", Body: "spec\n"})
	contractStubSubmitJob(t, nil)
	mux := http.NewServeMux()
	Register(mux)

	if rec := postSubmit(t, mux, "child", "parent"); rec.Code != http.StatusOK {
		t.Fatalf("submit: %d %s", rec.Code, rec.Body.String())
	}
	rows := spawnRows(t, st)
	if len(rows) != 1 || rows[0].ParentID != idOf(t, st, "parent") || rows[0].Via != jobs.SpawnViaSubmit {
		t.Fatalf("rows = %+v, want child <- parent via submit", rows)
	}
	// a second cell submitting the same child changes nothing
	if rec := postSubmit(t, mux, "child", "other"); rec.Code != http.StatusOK {
		t.Fatalf("resubmit: %d %s", rec.Code, rec.Body.String())
	}
	rows = spawnRows(t, st)
	if len(rows) != 1 || rows[0].ParentID != idOf(t, st, "parent") {
		t.Fatalf("rows after resubmit = %+v, want the first parent kept", rows)
	}
}

// A refused submit stamps nothing, and a submit with no header or a self header
// stamps nothing either.
func TestSpawnSubmitRefusedOrUnusableStampsNothing(t *testing.T) {
	SetMirrorMode(false)
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "parent", Repo: "kraken", State: "working", Body: "spec\n"})
	mustCreateStoreJob(t, st, store.Job{Slug: "child", Repo: "kraken", Body: "spec\n"})
	mux := http.NewServeMux()
	Register(mux)

	contractStubSubmitJob(t, contractSubmitErr("worktree already exists"))
	if rec := postSubmit(t, mux, "child", "parent"); rec.Code != http.StatusConflict {
		t.Fatalf("refused submit: %d, want 409", rec.Code)
	}
	if rows := spawnRows(t, st); len(rows) != 0 {
		t.Fatalf("a refused submit stamped: %+v", rows)
	}

	contractStubSubmitJob(t, nil)
	if rec := postSubmit(t, mux, "child", ""); rec.Code != http.StatusOK {
		t.Fatalf("submit no header: %d", rec.Code)
	}
	if rec := postSubmit(t, mux, "child", "child"); rec.Code != http.StatusOK {
		t.Fatalf("submit self header: %d", rec.Code)
	}
	if rows := spawnRows(t, st); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// The board index: a parent lists its children in stamp order with their
// display state, a child names its parent, an archived side shows under its
// slug as archived, and a side that exists nowhere is dropped.
func TestSpawnIndexResolvesBothDirections(t *testing.T) {
	st := useTestStore(t)
	parent := mustCreateStoreJob(t, st, store.Job{Slug: "parent", Repo: "kraken", State: "done", Body: "spec\n"})
	c1 := mustCreateStoreJob(t, st, store.Job{Slug: "c1", Repo: "kraken", State: "working", Body: "spec\n"})
	c2 := mustCreateStoreJob(t, st, store.Job{Slug: "c2", Repo: "kraken", Body: "spec\n"})
	c3 := mustCreateStoreJob(t, st, store.Job{Slug: "c3", Repo: "kraken", State: "done", Body: "spec\n"})
	ghost := mustCreateStoreJob(t, st, store.Job{Slug: "ghost", Repo: "kraken", Body: "spec\n"})
	for i, c := range []*jobs.Job{c1, c2, c3, ghost} {
		if _, err := st.SetJobSpawn(c.ID, parent.ID, int64(100+i), jobs.SpawnViaDraft); err != nil {
			t.Fatal(err)
		}
	}
	// c3 archived, ghost hard-deleted
	if _, err := st.ArchiveJob(c3.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArchiveJob(ghost.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteJob(ghost.ID); err != nil {
		t.Fatal(err)
	}

	active := jobs.LoadJobs()
	archived := jobs.LoadArchivedJobs()
	ix := buildSpawnIndex(active, archived)

	got := ix.spawnedOf(parent.ID)
	want := []webSpawnLink{{Slug: "c1", State: "working"}, {Slug: "c2", State: "draft"}, {Slug: "c3", State: "archived"}}
	if len(got) != len(want) {
		t.Fatalf("spawned = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("spawned[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if by := ix.spawnedByOf(c1.ID); by == nil || *by != (webSpawnLink{Slug: "parent", State: "done"}) {
		t.Fatalf("c1 spawned_by = %+v, want parent/done", by)
	}
	if by := ix.spawnedByOf(c3.ID); by == nil || by.Slug != "parent" {
		t.Fatalf("archived c3 spawned_by = %+v, want parent", by)
	}
	if by := ix.spawnedByOf(parent.ID); by != nil {
		t.Fatalf("parent spawned_by = %+v, want nil", by)
	}
	if got := ix.spawnedOf(c1.ID); got != nil {
		t.Fatalf("c1 spawned = %+v, want nil", got)
	}
}

// A hard-deleted PARENT leaves its children with no spawned_by rather than a
// dangling one.
func TestSpawnIndexDropsDeletedParent(t *testing.T) {
	st := useTestStore(t)
	parent := mustCreateStoreJob(t, st, store.Job{Slug: "parent", Repo: "kraken", State: "done", Body: "spec\n"})
	child := mustCreateStoreJob(t, st, store.Job{Slug: "child", Repo: "kraken", State: "working", Body: "spec\n"})
	if _, err := st.SetJobSpawn(child.ID, parent.ID, 1, jobs.SpawnViaDraft); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ArchiveJob(parent.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteJob(parent.ID); err != nil {
		t.Fatal(err)
	}
	ix := buildSpawnIndex(jobs.LoadJobs(), jobs.LoadArchivedJobs())
	if by := ix.spawnedByOf(child.ID); by != nil {
		t.Fatalf("spawned_by after parent hard-delete = %+v, want nil", by)
	}
}
