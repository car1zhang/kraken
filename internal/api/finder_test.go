package api

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// finder_test.go — /api/finder/reveal (finder.go). The reveal itself is
// behind revealInFinderFunc, so nothing here ever execs `open`.

// finderSwap replaces the reveal seam for the test and records every path
// it was handed.
func finderSwap(t *testing.T, err error) *[]string {
	t.Helper()
	old := revealInFinderFunc
	t.Cleanup(func() { revealInFinderFunc = old })
	var got []string
	revealInFinderFunc = func(abs string) error {
		got = append(got, abs)
		return err
	}
	return &got
}

// finderLiveJob creates a working job and writes rel (and its parents) into
// its cell's artifacts dir, returning the job and the file's absolute path.
func finderLiveJob(t *testing.T, st *store.Store, slug, rel string) (*jobs.Job, string) {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "demo", State: "working"})
	p := filepath.Join(jobs.CellArtifacts(j), rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("# hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return j, p
}

func finderPost(t *testing.T, remote string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/finder/reveal", bytes.NewReader(mustJSON(t, body)))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	finderRevealHandler(rec, req)
	return rec
}

func TestFinderRevealOpensLiveArtifact(t *testing.T) {
	st := useTestStore(t)
	got := finderSwap(t, nil)
	j, want := finderLiveJob(t, st, "finder-live", "00-overview.md")
	_, nested := finderLiveJob(t, st, "finder-nested", "icons/v1.png")

	rec := finderPost(t, "127.0.0.1:5000", map[string]string{"slug": j.Slug, "name": "00-overview.md"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	assertShape(t, "/api/finder/reveal", rec.Body.Bytes(), finderRevealSpec)

	// a subpath artifact resolves under its folder
	rec = finderPost(t, "[::1]:5000", map[string]string{"slug": "finder-nested", "name": "icons/v1.png"})
	if rec.Code != http.StatusOK {
		t.Fatalf("nested status = %d, body %s", rec.Code, rec.Body.String())
	}
	if len(*got) != 2 || (*got)[0] != want || (*got)[1] != nested {
		t.Errorf("revealed %v, want [%s %s]", *got, want, nested)
	}
}

func TestFinderRevealRefusals(t *testing.T) {
	st := useTestStore(t)
	got := finderSwap(t, nil)
	j, _ := finderLiveJob(t, st, "finder-refuse", "a.md")
	if err := os.WriteFile(filepath.Join(jobs.CellArtifacts(j), ".hidden"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		label  string
		remote string
		body   any
		code   int
	}{
		{"non-local caller", "100.81.0.1:5000", map[string]string{"slug": j.Slug, "name": "a.md"}, http.StatusForbidden},
		{"unknown slug", "127.0.0.1:1", map[string]string{"slug": "nope", "name": "a.md"}, http.StatusConflict},
		{"missing file", "127.0.0.1:1", map[string]string{"slug": j.Slug, "name": "b.md"}, http.StatusConflict},
		{"traversal", "127.0.0.1:1", map[string]string{"slug": j.Slug, "name": "../zeus-brief.md"}, http.StatusConflict},
		{"hidden file", "127.0.0.1:1", map[string]string{"slug": j.Slug, "name": ".hidden"}, http.StatusConflict},
		{"empty name", "127.0.0.1:1", map[string]string{"slug": j.Slug, "name": ""}, http.StatusConflict},
		{"bad json", "127.0.0.1:1", "not an object", http.StatusBadRequest},
	}
	for _, c := range cases {
		rec := finderPost(t, c.remote, c.body)
		if rec.Code != c.code {
			t.Errorf("%s: status = %d, want %d (body %s)", c.label, rec.Code, c.code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `"error"`) {
			t.Errorf("%s: body %s carries no error", c.label, rec.Body.String())
		}
	}
	if len(*got) != 0 {
		t.Errorf("a refused request reached the reveal: %v", *got)
	}
}

func TestFinderRevealSurfacesLaunchFailure(t *testing.T) {
	st := useTestStore(t)
	finderSwap(t, errors.New("exit status 1: boom"))
	j, _ := finderLiveJob(t, st, "finder-fail", "a.md")
	rec := finderPost(t, "127.0.0.1:1", map[string]string{"slug": j.Slug, "name": "a.md"})
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "boom") {
		t.Errorf("status = %d body %s, want 409 naming the launch error", rec.Code, rec.Body.String())
	}
}
