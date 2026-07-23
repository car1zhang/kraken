package jobs

import (
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/util"
)

func TestUploadsDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("KRAKEN_UPLOADS_DIR", root)
	got := UploadsDir("My Job!")
	if want := filepath.Join(root, "my-job"); got != want {
		t.Fatalf("UploadsDir(%q) = %q, want %q", "My Job!", got, want)
	}
	// an empty-slugifying slug must never resolve to the shared root
	if got := UploadsDir("---"); got != "" {
		t.Fatalf("UploadsDir(---) = %q, want empty", got)
	}
}

func TestUploadsDirHomeRelative(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", "")
	got := UploadsDir("foo")
	want := filepath.Join(util.Home(), ".kraken", "uploads", "foo")
	if got != want {
		t.Fatalf("UploadsDir default root = %q, want %q", got, want)
	}
}

func TestSaveUploadSequencing(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
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

	dir := UploadsDir("job-a")
	if err := os.WriteFile(filepath.Join(dir, "img-002.jpg"), []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a non-matching stray file must not perturb the sequence
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	name, path, err = SaveUpload("job-a", "gif", []byte("third"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "img-003.gif" {
		t.Fatalf("third upload name = %q, want img-003.gif", name)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != "third" {
		t.Fatalf("round-trip mismatch: %q, %v", got, err)
	}
}

func TestSaveUploadExtAndSlugGuards(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
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
}

func TestSaveUploadPastThreeDigits(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	dir := UploadsDir("job-e")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// a four-digit name must stay visible to the scan so nothing overwrites it
	if err := os.WriteFile(filepath.Join(dir, "img-1000.png"), []byte("big"), 0o644); err != nil {
		t.Fatal(err)
	}
	name, _, err := SaveUpload("job-e", "png", []byte("x"))
	if err != nil {
		t.Fatal(err)
	}
	if name != "img-1001.png" {
		t.Fatalf("after img-1000, name = %q, want img-1001.png", name)
	}
}

func TestSaveUploadDifferentSlugsIndependent(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	nameA, _, err := SaveUpload("job-a", "png", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	nameB, _, err := SaveUpload("job-b", "png", []byte("b"))
	if err != nil {
		t.Fatal(err)
	}
	if nameA != "img-001.png" || nameB != "img-001.png" {
		t.Fatalf("expected independent sequences, got %q and %q", nameA, nameB)
	}
}

func TestRemoveUploadsIdempotent(t *testing.T) {
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	if _, _, err := SaveUpload("job-c", "png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	dir := UploadsDir("job-c")
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
	// removing a slug that never had uploads must not error either
	if err := RemoveUploads("never-uploaded"); err != nil {
		t.Fatalf("remove with no dir: %v", err)
	}
}
