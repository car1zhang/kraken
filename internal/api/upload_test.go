package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// smallPNG returns a minimal but fully valid, sniffable 1x1 PNG.
func smallPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// multipartUploadRequest builds a POST /api/upload request carrying "slug"
// and a "file" field named filename with contents data — filename is
// deliberately allowed to lie about the type, since sniffing (not the
// client-supplied name) decides the saved extension.
func multipartUploadRequest(t *testing.T, slug, filename string, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("slug", slug); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/upload", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestSniffImageExt(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		ext  string
		ok   bool
	}{
		{"png", []byte("\x89PNG\x0D\x0A\x1A\x0Arest"), "png", true},
		{"jpeg", []byte("\xFF\xD8\xFFrest"), "jpg", true},
		{"gif87a", []byte("GIF87arest"), "gif", true},
		{"gif89a", []byte("GIF89arest"), "gif", true},
		{"webp", append([]byte("RIFF\x00\x00\x00\x00WEBPVP"), []byte("8 rest")...), "webp", true},
		{"text", []byte("just plain text, not an image"), "", false},
		{"empty", []byte{}, "", false},
	}
	for _, c := range cases {
		ext, ok := sniffImageExt(c.data)
		if ok != c.ok || ext != c.ext {
			t.Errorf("%s: sniffImageExt = (%q, %v), want (%q, %v)", c.name, ext, ok, c.ext, c.ok)
		}
	}
}

func TestUploadPathValidation(t *testing.T) {
	// jobs.SaveUpload is store-only now (H1: no nil-store fallback), so a
	// store row for the seed slug must exist before it can write anything.
	useTestStore(t)
	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	uj := mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "upload-path-test", Repo: "monolith"})

	// seed a real uploaded file so the "valid" case can resolve to a real
	// path, plus a four-digit name — the sequence is unbounded past 999 and
	// the GET side must keep resolving it
	if _, _, err := jobs.SaveUpload("upload-path-test", "png", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(jobs.UploadsDir("upload-path-test", uj.ID), "img-1000.png"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		slug    string
		file    string
		wantErr bool
	}{
		{"valid", "upload-path-test", "img-001.png", false},
		{"four-digit sequence", "upload-path-test", "img-1000.png", false},
		{"missing file", "upload-path-test", "img-999.png", true},
		{"traversal in name", "upload-path-test", "../../etc/passwd", true},
		{"traversal-shaped encoded literal", "upload-path-test", "..%2f..%2fetc", true},
		{"bad extension", "upload-path-test", "img-001.txt", true},
		{"non-numeric sequence", "upload-path-test", "img-abc.png", true},
		{"empty slug", "", "img-001.png", true},
		{"slug with slash", "a/b", "img-001.png", true},
		{"slug slugifies to empty", "!!!", "img-001.png", true},
	}
	for _, c := range cases {
		_, err := UploadPath(c.slug, c.file)
		if (err != nil) != c.wantErr {
			t.Errorf("%s: UploadPath(%q, %q) err = %v, wantErr %v", c.name, c.slug, c.file, err, c.wantErr)
		}
	}
}
