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

// TestJobArtifactsStoreModeUnionsStoreAndPluto covers L4+E6: store mode
// must union the artifacts-table listing WITH the pluto flat-copy listing
// (not just the table alone), deduped by name with cell > store > pluto
// precedence. A name present in BOTH the store and a pluto flat copy
// ("dup.md") must appear exactly once, as the store's URL (higher
// precedence than pluto); a name present in pluto ONLY ("pluto-only.md")
// must still show up.
func TestJobArtifactsStoreModeUnionsStoreAndPluto(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-union", Repo: "monolith", State: "done"})

	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "report.html", Bytes: []byte("<html></html>"), Mtime: 1},
		{Path: "dup.md", Bytes: []byte("store version"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jobs.PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// dup.md: also exists as a pluto flat copy — store must win the dedup.
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "art-union.dup.md"), []byte("pluto version"), 0o644); err != nil {
		t.Fatal(err)
	}
	// pluto-only.md: exists ONLY as a pluto flat copy (e.g. a job's
	// root-level doc, preserved straight from the worktree root by
	// preserveArtifacts — never synced into the artifacts table at all
	// since syncCellArtifacts only mirrors the artifacts/ subdir).
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "art-union.pluto-only.md"), []byte("pluto only"), 0o644); err != nil {
		t.Fatal(err)
	}

	arts := jobArtifacts(j, false)
	byName := map[string]webArtifact{}
	for _, a := range arts {
		byName[a.Name] = a
	}
	if len(arts) != 3 {
		t.Fatalf("want 3 artifacts (report.html, dup.md once, pluto-only.md), got %d: %+v", len(arts), byName)
	}
	if _, ok := byName["report.html"]; !ok {
		t.Fatalf("report.html missing: %+v", byName)
	}
	if a, ok := byName["dup.md"]; !ok || a.URL != "/api/artifact/art-union/pluto/dup.md" {
		t.Fatalf("dup.md must resolve to the store-mode url (precedence over the pluto flat copy): %+v", byName)
	}
	if a, ok := byName["pluto-only.md"]; !ok || a.URL != "/api/artifact/art-union/pluto/pluto-only.md" {
		t.Fatalf("pluto-only.md (info-job doc with no store row) missing: %+v", byName)
	}
}

// TestJobArtifactsStoreModeInfoJobPlutoOnly is L4+E6's named "info-job
// pluto-only case": a store-backed job with NO artifacts-table rows
// at all (the common case for a job whose only deliverable is a root-level
// doc preserveArtifacts copied straight into pluto) must still list that
// doc — before L4/E6, store mode's union of cell ∪ store ONLY made this
// artifact vanish entirely once the cell was torn down.
func TestJobArtifactsStoreModeInfoJobPlutoOnly(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "info-only", Repo: "monolith", State: "done"})
	// deliberately NO st.SyncArtifacts call — nothing in the artifacts table.

	if err := os.MkdirAll(jobs.PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "info-only.explain.md"), []byte("the doc"), 0o644); err != nil {
		t.Fatal(err)
	}

	arts := jobArtifacts(j, false)
	if len(arts) != 1 || arts[0].Name != "explain.md" {
		t.Fatalf("want exactly [explain.md], got %+v", arts)
	}
	if want := "/api/artifact/info-only/pluto/explain.md"; arts[0].URL != want {
		t.Fatalf("url = %q, want %q", arts[0].URL, want)
	}
}

// TestJobArtifactsStoreModeUnionIsGloballySorted pins the ORDER of the union, not just
// its membership. Each source arrives sorted within itself, but jobArtifacts
// appends three of them, so before the sort a job like this one served
// 00-overview.md (a pluto flat copy, added last) AFTER 90-build-report.md (a
// store row, added first) — the exact inversion this fixture reproduces. The
// clients page through this array with next/prev now, so the wire order is
// the reading order: an unsorted union puts the overview four steps from the
// front of its own set.
func TestJobArtifactsStoreModeUnionIsGloballySorted(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "art-order", Repo: "monolith", State: "done"})

	// The store holds the mirrored artifacts/ subdir; note 90- sorts LAST
	// within the store but is added to the union FIRST.
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "10-architecture.html", Bytes: []byte("a"), Mtime: 1},
		{Path: "50-components.md", Bytes: []byte("b"), Mtime: 1},
		{Path: "90-build-report.md", Bytes: []byte("c"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(jobs.PlutoDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// 00-overview.md reaches the board ONLY as a pluto flat copy, which is
	// the lowest-precedence source and therefore appended last.
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "art-order.00-overview.md"), []byte("d"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	for _, a := range jobArtifacts(j, false) {
		got = append(got, a.Name)
	}
	want := []string{"00-overview.md", "10-architecture.html", "50-components.md", "90-build-report.md"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("union order:\n got %v\nwant %v", got, want)
	}
}

// TestJobArtifactsLiveCellIsSorted pins the ordering guarantee on the OTHER
// branch — the live cell, which returns before the union is ever built and so
// is not covered by the sibling above. ios/API.md promises the `artifacts`
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
