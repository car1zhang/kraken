package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/internal/jobs"
)

// background_test.go — /api/background's three methods (background.go) plus
// currentBackground, the board payload's assembly half.

// multipartBackgroundRequest builds a POST /api/background carrying one
// "file" field — no slug: the background belongs to the board, not a job.
func multipartBackgroundRequest(t *testing.T, data []byte) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "bg.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/background", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

func TestBackgroundLifecycle(t *testing.T) {
	useTestStore(t)

	// Unset: GET is a 404 and the board key is absent.
	rec := httptest.NewRecorder()
	backgroundHandler(rec, httptest.NewRequest(http.MethodGet, "/api/background", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET with no background: status %d, want 404", rec.Code)
	}
	if bg := currentBackground(jobs.ActiveStore()); bg != nil {
		t.Errorf("currentBackground with no background = %+v, want nil", bg)
	}

	// Upload a real PNG.
	png := smallPNG(t)
	rec = httptest.NewRecorder()
	backgroundHandler(rec, multipartBackgroundRequest(t, png))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST: status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK        bool  `json:"ok"`
		UpdatedAt int64 `json:"updated_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || resp.UpdatedAt == 0 {
		t.Errorf("POST response = %+v, want ok with a nonzero updated_at", resp)
	}

	// GET now serves the exact bytes back, typed by the sniff.
	rec = httptest.NewRecorder()
	backgroundHandler(rec, httptest.NewRequest(http.MethodGet, "/api/background", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after upload: status %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), png) {
		t.Errorf("GET body differs from the uploaded bytes (%d vs %d)", rec.Body.Len(), len(png))
	}
	if bg := currentBackground(jobs.ActiveStore()); bg == nil || bg.UpdatedAt != resp.UpdatedAt {
		t.Errorf("currentBackground = %+v, want updated_at %d", bg, resp.UpdatedAt)
	}

	// DELETE restores the default; a second DELETE stays 200 (idempotent).
	for i := 0; i < 2; i++ {
		rec = httptest.NewRecorder()
		backgroundHandler(rec, httptest.NewRequest(http.MethodDelete, "/api/background", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("DELETE #%d: status %d", i+1, rec.Code)
		}
	}
	rec = httptest.NewRecorder()
	backgroundHandler(rec, httptest.NewRequest(http.MethodGet, "/api/background", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after DELETE: status %d, want 404", rec.Code)
	}
	if bg := currentBackground(jobs.ActiveStore()); bg != nil {
		t.Errorf("currentBackground after DELETE = %+v, want nil", bg)
	}
}

func TestBackgroundRejectsNonImage(t *testing.T) {
	useTestStore(t)
	rec := httptest.NewRecorder()
	backgroundHandler(rec, multipartBackgroundRequest(t, []byte("<html>not an image</html>")))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("POST non-image: status %d, want 415", rec.Code)
	}
	// A refused upload must not have half-written anything.
	rec = httptest.NewRecorder()
	backgroundHandler(rec, httptest.NewRequest(http.MethodGet, "/api/background", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET after refused upload: status %d, want 404", rec.Code)
	}
}

func TestBackgroundRejectsOversize(t *testing.T) {
	useTestStore(t)
	// A sniffable PNG header followed by padding past the 10MB cap: the size
	// check must fire, not the type check.
	data := append(smallPNG(t), make([]byte, maxUploadBytes)...)
	rec := httptest.NewRecorder()
	backgroundHandler(rec, multipartBackgroundRequest(t, data))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("POST oversize: status %d, want 413", rec.Code)
	}
}

func TestBackgroundMethodNotAllowed(t *testing.T) {
	useTestStore(t)
	// HEAD is in the table deliberately: it was in and out of the GET arm
	// during review, and API.md's "Any other method: 405." is load-bearing —
	// this route follows its gated byte-serving siblings (upload, library
	// artifact) in refusing it.
	for _, method := range []string{http.MethodPut, http.MethodPatch, http.MethodHead} {
		rec := httptest.NewRecorder()
		backgroundHandler(rec, httptest.NewRequest(method, "/api/background", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: status %d, want 405", method, rec.Code)
		}
	}
}

func TestBackgroundReplaceMovesStamp(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	if _, err := st.SetBoardBackground("image/png", []byte("old")); err != nil {
		t.Fatal(err)
	}
	before, err := st.BoardBackgroundUpdatedAt()
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	backgroundHandler(rec, multipartBackgroundRequest(t, smallPNG(t)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST replace: status %d", rec.Code)
	}
	after, err := st.BoardBackgroundUpdatedAt()
	if err != nil {
		t.Fatal(err)
	}
	// Strictly greater: SetBoardBackground guarantees a monotonic stamp even
	// when two writes share a millisecond, because the stamp is the image
	// URL's only cache-buster.
	if after <= before {
		t.Errorf("replace stamp %d is not strictly greater than %d", after, before)
	}
	bg, err := st.GetBoardBackground()
	if err != nil {
		t.Fatal(err)
	}
	if string(bg.Bytes) == "old" {
		t.Error("replace left the old bytes in place")
	}
}
