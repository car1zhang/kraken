package api

import (
	"testing"

	"kraken/jobs"
	"kraken/store"
)

// TestUploadBytesRoundTrip covers deliverable 3 (uploads GET store
// mode): jobs.SaveUpload already writes both a DB row and a materialized
// file (verified unchanged); UploadBytes is the GET-side read of the DB
// row.
func TestUploadBytesRoundTrip(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "upload-store", Type: "build", Repo: "monolith"})

	png := []byte("\x89PNG\x0D\x0A\x1A\x0Afakepngbytes")
	name, path, err := jobs.SaveUpload("upload-store", "png", png)
	if err != nil {
		t.Fatal(err)
	}
	if name != "img-001.png" {
		t.Fatalf("name = %q, want img-001.png", name)
	}
	if path == "" {
		t.Fatal("SaveUpload must still return a materialized path store-backed")
	}

	data, mimeType, ok := UploadBytes("upload-store", name)
	if !ok {
		t.Fatal("UploadBytes: want ok=true for a just-saved upload")
	}
	if string(data) != string(png) {
		t.Fatalf("bytes mismatch: got %q want %q", data, png)
	}
	if mimeType != "image/png" {
		t.Fatalf("mimeType = %q, want image/png", mimeType)
	}
}

func TestUploadBytesFallsThroughWhenNotFound(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "upload-store-miss", Type: "build", Repo: "monolith"})

	if _, _, ok := UploadBytes("upload-store-miss", "img-001.png"); ok {
		t.Fatal("want ok=false for an upload that was never saved")
	}
	if _, _, ok := UploadBytes("no-such-job", "img-001.png"); ok {
		t.Fatal("want ok=false for an unknown job")
	}
}

// TestUploadBytesNoStoreWired covers the file-mode fallthrough
// contract: with no store wired, UploadBytes must always report
// ok=false so callers fall through to UploadPath.
func TestUploadBytesNoStoreWired(t *testing.T) {
	if _, _, ok := UploadBytes("anything", "img-001.png"); ok {
		t.Fatal("want ok=false with no store wired")
	}
}
