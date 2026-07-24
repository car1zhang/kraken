package api

import (
	"os"
	"path/filepath"
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
