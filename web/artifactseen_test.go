package web

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/api"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// artifactseen_test.go — a served artifact is the operator's read: the GET
// stamps `artifacts_seen:<job id>` (jobs/artseen.go) for a store-served
// ("pluto") and a disk-served ("cell") artifact, not for a 404, not for an
// archived job, and never when the serve is a mirror.

func TestArtifactHandlerStampsTheRead(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "seen-art", Repo: "monolith"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("o"), Mtime: 1},
		{Path: "sub dir/a#b.png", Bytes: []byte("p"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	get := func(url string) int {
		rec := httptest.NewRecorder()
		artifactHandler(rec, httptest.NewRequest("GET", url, nil))
		return rec.Code
	}
	if code := get("/api/artifact/seen-art/pluto/00-overview.md"); code != 200 {
		t.Fatalf("status = %d", code)
	}
	if code := get("/api/artifact/seen-art/pluto/sub%20dir/a%23b.png"); code != 200 {
		t.Fatalf("nested status = %d", code)
	}
	if code := get("/api/artifact/seen-art/pluto/missing.md"); code != 404 {
		t.Fatalf("missing status = %d", code)
	}
	seen := jobs.ArtifactSeenIndex(st)[id]
	if seen["00-overview.md"] == 0 || seen["sub dir/a#b.png"] == 0 {
		t.Errorf("served artifacts not stamped: %v", seen)
	}
	if _, ok := seen["missing.md"]; ok {
		t.Errorf("a 404 was stamped as a read: %v", seen)
	}

	// a live cell's artifact, served off disk, is stamped under the same name
	j := jobs.JobBySlug(jobs.LoadJobs(), "seen-art")
	if j == nil {
		t.Fatal("job not loaded")
	}
	dir := jobs.CellArtifacts(j)
	if err := os.MkdirAll(filepath.Join(dir, "cards"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cards", "10-map.md"), []byte("m"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := get("/api/artifact/seen-art/cell/cards/10-map.md"); code != 200 {
		t.Fatalf("cell status = %d", code)
	}
	if jobs.ArtifactSeenIndex(st)[id]["cards/10-map.md"] == 0 {
		t.Error("a disk-served artifact was not stamped")
	}
}

func TestArtifactHandlerDoesNotStampAnArchiveOrAMirror(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "seen-arch", Repo: "monolith", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{{Path: "00-overview.md", Bytes: []byte("o"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	// a mirror serve never writes
	api.SetMirrorMode(true)
	t.Cleanup(func() { api.SetMirrorMode(false) })
	rec := httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/seen-arch/pluto/00-overview.md", nil))
	if rec.Code != 200 {
		t.Fatalf("mirror status = %d", rec.Code)
	}
	if got := jobs.ArtifactSeenIndex(st); len(got) != 0 {
		t.Errorf("a mirror stamped a read: %v", got)
	}
	api.SetMirrorMode(false)

	name, err := st.ArchiveJob(id)
	if err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/"+name+"/archive/00-overview.md", nil))
	if rec.Code != 200 {
		t.Fatalf("archive status = %d", rec.Code)
	}
	if got := jobs.ArtifactSeenIndex(st); len(got) != 0 {
		t.Errorf("an archived job's read was stamped: %v", got)
	}
}
