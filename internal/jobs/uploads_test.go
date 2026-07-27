package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/store"
	"kraken/internal/util"
)

func TestUploadsDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KRAKEN_UPLOADS_DIR", root)
	got := UploadsDir("My Job!", 4)
	if want := filepath.Join(root, "my-job-4"); got != want {
		t.Fatalf("UploadsDir(%q) = %q, want %q", "My Job!", got, want)
	}
	// an empty-slugifying slug must never resolve to the shared root
	if got := UploadsDir("---", 4); got != "" {
		t.Fatalf("UploadsDir(---) = %q, want empty", got)
	}
}

func TestUploadsDirHomeRelative(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", "")
	got := UploadsDir("foo", 4)
	want := filepath.Join(util.Home(), ".kraken", "uploads", "foo-4")
	if got != want {
		t.Fatalf("UploadsDir default root = %q, want %q", got, want)
	}
}

// TestSaveUploadSequencing covers the jobs-package wrapper's own contract —
// slug→job-id resolution and materializing a real, byte-identical file
// alongside the DB row — not the NNN-numbering itself, which is
// store.SaveUpload's job (see internal/store/uploads_test.go for that
// coverage).
func TestSaveUploadSequencing(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "job-a"})

	// binary payload with a NUL and high bytes — SaveUpload claims byte
	// fidelity through the string round-trip, so pin it
	first := []byte{0x89, 'P', 'N', 'G', 0x00, 0xFF, 0xFE}
	name, path, err := SaveUpload("job-a", "png", first)
	if err != nil {
		t.Fatal(err)
	}
	if name != "img-001.png" {
		t.Fatalf("first upload name = %q, want img-001.png", name)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(first) {
		t.Fatalf("round-trip mismatch: %q, %v", got, err)
	}

	name, path, err = SaveUpload("job-a", "gif", []byte("second"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "img-002.gif" {
		t.Fatalf("second upload name = %q, want img-002.gif", name)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "second" {
		t.Fatalf("round-trip mismatch: %q, %v", got, err)
	}
}

func TestSaveUploadExtAndSlugGuards(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "job-d"})

	// a leading dot is normalized rather than doubling up (img-001..png)
	if name, _, err := SaveUpload("job-d", ".png", []byte("x")); err != nil || name != "img-001.png" {
		t.Fatalf("dotted ext: name=%q err=%v, want img-001.png", name, err)
	}
	for _, ext := range []string{"", "a/b", "a.b", `a\b`} {
		if _, _, err := SaveUpload("job-d", ext, []byte("x")); err == nil {
			t.Fatalf("ext %q: expected error", ext)
		}
	}
	if _, _, err := SaveUpload("---", "png", []byte("x")); err == nil {
		t.Fatal("empty-slugifying slug: expected error")
	}
	// no active job under this slug: the store lookup itself must fail
	if _, _, err := SaveUpload("no-such-job", "png", []byte("x")); err == nil {
		t.Fatal("no active job: expected error")
	}
}

func TestRemoveUploadsIdempotent(t *testing.T) {
	st := useTestStore(t)
	j := mustCreateJob(t, st, store.Job{Slug: "job-c"})

	if _, _, err := SaveUpload("job-c", "png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	dir := UploadsDir("job-c", j.ID)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("expected uploads dir to exist: %v", err)
	}
	if err := RemoveUploads("job-c"); err != nil {
		t.Fatalf("first remove: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("expected dir removed, stat err = %v", err)
	}
	// removing again (already gone) must not error
	if err := RemoveUploads("job-c"); err != nil {
		t.Fatalf("second remove (already gone): %v", err)
	}
	// removing a slug that never had uploads (and has no active job either)
	// must not error either
	if err := RemoveUploads("never-uploaded"); err != nil {
		t.Fatalf("remove with no dir: %v", err)
	}
}
