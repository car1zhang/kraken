package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kraken/jobs"
)

// writeFeedbackFixture writes a minimal job file plus a pluto artifact for
// it, mirroring the board's real layout (<slug>.<name> flat under PlutoDir).
func writeFeedbackFixture(t *testing.T, slug string) {
	t.Helper()
	writeTestJob(t, slug, "")
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), slug+".report.html"), []byte("<html>report</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func feedbackPostReq(t *testing.T, body any) *http.Request {
	t.Helper()
	return httptest.NewRequest(http.MethodPost, "/api/feedback", bytes.NewReader(mustJSON(t, body)))
}

func TestFeedbackHandlerHappyPath(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeFeedbackFixture(t, "fb-happy")

	rec := httptest.NewRecorder()
	feedbackHandler(rec, feedbackPostReq(t, feedbackReq{
		Slug: "fb-happy", Home: "pluto", Name: "report.html", Feedback: "the flow diagram is backwards",
	}))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("bad response json: %v", err)
	}
	if resp["ok"] != true {
		t.Errorf("resp = %+v, want ok:true", resp)
	}
	dir, _ := resp["dir"].(string)
	if dir == "" {
		t.Fatal("resp missing dir")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("entry dir doesn't exist: %v", err)
	}
	snap, err := os.ReadFile(filepath.Join(dir, "snapshot.html"))
	if err != nil {
		t.Fatalf("snapshot.html missing: %v", err)
	}
	if string(snap) != "<html>report</html>" {
		t.Errorf("snapshot content = %q", snap)
	}
	fb, err := os.ReadFile(filepath.Join(dir, "feedback.txt"))
	if err != nil {
		t.Fatalf("feedback.txt missing: %v", err)
	}
	if !bytes.Contains(fb, []byte("the flow diagram is backwards")) {
		t.Errorf("feedback.txt missing note body: %s", fb)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

func TestFeedbackHandlerGetNotAllowed(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	rec := httptest.NewRecorder()
	feedbackHandler(rec, httptest.NewRequest(http.MethodGet, "/api/feedback", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func TestFeedbackHandlerUnknownSlug(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	rec := httptest.NewRecorder()
	feedbackHandler(rec, feedbackPostReq(t, feedbackReq{
		Slug: "nope", Home: "pluto", Name: "report.html", Feedback: "hello",
	}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestFeedbackHandlerUnknownArtifact(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeFeedbackFixture(t, "fb-noart")

	rec := httptest.NewRecorder()
	feedbackHandler(rec, feedbackPostReq(t, feedbackReq{
		Slug: "fb-noart", Home: "pluto", Name: "missing.html", Feedback: "hello",
	}))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

func TestFeedbackHandlerEmptyFeedback(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeFeedbackFixture(t, "fb-empty")

	rec := httptest.NewRecorder()
	feedbackHandler(rec, feedbackPostReq(t, feedbackReq{
		Slug: "fb-empty", Home: "pluto", Name: "report.html", Feedback: "   ",
	}))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestFeedbackHandlerBadJSON(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	rec := httptest.NewRecorder()
	feedbackHandler(rec, httptest.NewRequest(http.MethodPost, "/api/feedback", bytes.NewReader([]byte("not json"))))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
