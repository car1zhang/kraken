package api

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// artifacts_storemode_test.go — deliverable 2 (Q12 artifact serving):
// jobArtifacts/archivedWebArtifacts listing from the store, and
// ArtifactBytes resolving DB-backed bytes for a non-live job. Rows are
// seeded directly via store.SyncArtifacts (the block-event sync's own
// write path, jobs/sync.go) rather than through a real cell dir, since
// that's out of this slice's ownership.

func TestJobArtifactsStoreModeNonLive(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-done", Repo: "monolith", State: "done"})

	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "report.html", Bytes: []byte("<html></html>"), Mtime: 1},
		{Path: "nested/diagram.svg", Bytes: []byte("<svg></svg>"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}

	arts := jobArtifacts(j, false)
	if len(arts) != 2 {
		t.Fatalf("want 2 artifacts, got %d: %+v", len(arts), arts)
	}
	byName := map[string]webArtifact{}
	for _, a := range arts {
		byName[a.Name] = a
	}
	if a, ok := byName["report.html"]; !ok || a.URL != "/api/artifact/art-done/pluto/report.html" {
		t.Fatalf("report.html artifact: %+v", byName)
	}
	if a, ok := byName["nested/diagram.svg"]; !ok || a.URL != "/api/artifact/art-done/pluto/nested/diagram.svg" {
		t.Fatalf("nested artifact (dropped by the old flat pluto-copy mechanism) missing: %+v", byName)
	}
}

// TestJobArtifactsStoreModeLiveSkipsDB covers Q12's "a live cell always
// serves from the cell dir" rule: with live=true, DB rows must never
// surface even if present, since the cell dir (empty in this test) is
// authoritative.
func TestJobArtifactsStoreModeLiveSkipsDB(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-live", Repo: "monolith"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "x.txt", Bytes: []byte("x"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	arts := jobArtifacts(j, true)
	if len(arts) != 0 {
		t.Fatalf("want no artifacts for a live cell with an empty cell dir, got %+v", arts)
	}
}

// TestJobArtifactsNonLiveIgnoresDisk pins that a job whose cell is not live
// lists the artifacts table and nothing else: a file still sitting in its
// cell dir (a dead cell, a teardown that died mid-flight) is not added, so
// the card shows exactly what the store can serve.
func TestJobArtifactsNonLiveIgnoresDisk(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-disk", Repo: "monolith", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "kept.md", Bytes: []byte("row"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	root := jobs.CellArtifacts(j)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "disk-only.md"), []byte("file"), 0o644); err != nil {
		t.Fatal(err)
	}
	arts := jobArtifacts(j, false)
	if len(arts) != 1 || arts[0].Name != "kept.md" || arts[0].URL != "/api/artifact/art-disk/pluto/kept.md" {
		t.Fatalf("want exactly the store row kept.md under home pluto, got %+v", arts)
	}
}

// TestJobArtifactsNonLiveArchivedHome pins the archived branch: an archived
// job's rows are listed under its archived name and the "archive" home.
func TestJobArtifactsNonLiveArchivedHome(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-arch-home", Repo: "monolith", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "a.md", Bytes: []byte("a"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	name, err := st.ArchiveJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), name)
	if a == nil {
		t.Fatal("archived job not found")
	}
	arts := jobArtifacts(a, false)
	if want := "/api/artifact/" + name + "/archive/a.md"; len(arts) != 1 || arts[0].URL != want {
		t.Fatalf("want [%s], got %+v", want, arts)
	}
}

// TestJobArtifactsStoreModeIsSorted pins the ORDER of a non-live listing,
// not just its membership: the clients page through this array with
// next/prev, so the wire order is the reading order.
func TestJobArtifactsStoreModeIsSorted(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-order", Repo: "monolith", State: "done"})

	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "90-build-report.md", Bytes: []byte("c"), Mtime: 1},
		{Path: "10-architecture.html", Bytes: []byte("a"), Mtime: 1},
		{Path: "00-overview.md", Bytes: []byte("d"), Mtime: 1},
		{Path: "50-components.md", Bytes: []byte("b"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, a := range jobArtifacts(j, false) {
		got = append(got, a.Name)
	}
	want := []string{"00-overview.md", "10-architecture.html", "50-components.md", "90-build-report.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order:\n got %v\nwant %v", got, want)
	}
}

// TestJobArtifactsLiveCellIsSorted pins the ordering guarantee on the OTHER
// branch — the live cell, whose source is the cell dir rather than the
// table, and so is not covered by the sibling above. ios/API.md promises the `artifacts`
// array is sorted unconditionally, and a live cell is exactly where the
// clients' next/prev matters most (it is the job you are reading while it is
// being written), so the branch that reaches the wire without passing through
// the union needs its own guard.
//
// The nested entries are the interesting part: ListArtifacts sorts ABSOLUTE
// paths while the wire carries names made relative to the artifacts root.
// Those two orderings agree only because every path shares the identical root
// prefix — true today, and this test is what notices if it ever stops being.
func TestJobArtifactsLiveCellIsSorted(t *testing.T) {
	useTestStore(t) // sandboxes HOME, so the cell dir below lands in a temp tree
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-live-order", Repo: "monolith", State: "working"})

	root := jobs.CellArtifacts(j)
	if err := os.MkdirAll(filepath.Join(root, "icons"), 0o755); err != nil {
		t.Fatal(err)
	}
	// written in deliberately wrong order; the walk + sort is what fixes it
	for _, rel := range []string{
		"90-build-report.md", "10-architecture.html", "00-overview.md",
		filepath.Join("icons", "b.png"), filepath.Join("icons", "a.png"),
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	for _, a := range jobArtifacts(j, true) {
		got = append(got, a.Name)
	}
	want := []string{
		"00-overview.md", "10-architecture.html", "90-build-report.md",
		"icons/a.png", "icons/b.png",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("live cell order:\n got %v\nwant %v", got, want)
	}
}

func TestArtifactBytesResolvesFromStore(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-bytes", Repo: "monolith"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "doc.html", Bytes: []byte("<h1>hi</h1>"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}

	data, mimeType, ok := ArtifactBytes(j, "pluto", "doc.html")
	if !ok {
		t.Fatal("want ok=true")
	}
	if string(data) != "<h1>hi</h1>" {
		t.Fatalf("bytes = %q", data)
	}
	if mimeType != "text/html; charset=utf-8" {
		t.Fatalf("mimeType = %q", mimeType)
	}

	if _, _, ok := ArtifactBytes(j, "pluto", "../../etc/passwd"); ok {
		t.Fatal("traversal-shaped name must be refused")
	}
	if _, _, ok := ArtifactBytes(j, "pluto", "missing.html"); ok {
		t.Fatal("want ok=false for an unsynced path")
	}
}

// TestArtifactBytesRefusesCellHome covers L11: "cell" artifacts are NEVER
// DB-backed (Q12) — ArtifactBytes must refuse outright regardless of
// whether a same-named row happens to exist in the table, so a caller that
// falls back to it after an ArtifactPath("cell", ...) miss can never have
// the DB silently serve bytes mislabeled as belonging to a different home.
func TestArtifactBytesRefusesCellHome(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-cell-refuse", Repo: "monolith"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "same-name.txt", Bytes: []byte("db bytes"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ArtifactBytes(j, "cell", "same-name.txt"); ok {
		t.Fatal("want ok=false for home=cell even though a same-named row exists")
	}
}

// TestArtifactBytesRefusesJobSuffix covers L11: a name reconstructing to a
// job file's own suffix must never be servable as an artifact, mirroring
// ArtifactPath's own "job files are never artifacts" refusal.
func TestArtifactBytesRefusesJobSuffix(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-jobsuffix", Repo: "monolith"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "sneaky.job.md", Bytes: []byte("nope"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := ArtifactBytes(j, "pluto", "sneaky.job.md"); ok {
		t.Fatal("want ok=false for a name ending in the job-file suffix")
	}
}

func TestArtifactBytesNoStoreWired(t *testing.T) {
	j := &jobs.Job{Slug: "no-store", ID: 1}
	if _, _, ok := ArtifactBytes(j, "pluto", "x.html"); ok {
		t.Fatal("want ok=false with no store wired")
	}
}

func TestArchivedWebArtifactsStoreMode(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-archived", Repo: "monolith", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "final.md", Bytes: []byte("done"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	name, err := st.ArchiveJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	archived := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), name)
	if archived == nil {
		t.Fatal("archived job not found")
	}
	arts := archivedWebArtifacts(archived)
	if len(arts) != 1 || arts[0].Name != "final.md" {
		t.Fatalf("archived artifacts = %+v", arts)
	}
	if want := "/api/artifact/" + name + "/archive/final.md"; arts[0].URL != want {
		t.Fatalf("url = %q, want %q", arts[0].URL, want)
	}
}

// TestArchivedWebArtifactsIsSorted covers the THIRD wire producer of an
// artifact list. archivedWebArtifacts never touches jobArtifacts, so neither
// ordering test above reaches it, and its single source (the store's own
// ORDER BY path) already satisfies the guarantee — which is exactly why it
// needs a test rather than a shrug: the sortedArtifacts call there is a no-op
// today, and a no-op with no test is the kind of thing a later reader deletes.
func TestArchivedWebArtifactsIsSorted(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-arch-order", Repo: "monolith", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "90-build-report.md", Bytes: []byte("c"), Mtime: 1},
		{Path: "00-overview.md", Bytes: []byte("a"), Mtime: 1},
		{Path: "10-architecture.html", Bytes: []byte("b"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	name, err := st.ArchiveJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	archived := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), name)
	if archived == nil {
		t.Fatal("archived job not found")
	}

	var got []string
	for _, a := range archivedWebArtifacts(archived) {
		got = append(got, a.Name)
	}
	want := []string{"00-overview.md", "10-architecture.html", "90-build-report.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("archived order:\n got %v\nwant %v", got, want)
	}
}
