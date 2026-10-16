package web

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/store"
)

// portability_edge_test.go — the EDGE dimension of artifacts/50-contract.md
// (kraken-portability-audit) for web's artifactHandler: "a non-cell home whose
// path is not in the table → 404", probed with a legacy flat copy present at
// every place the retired pluto readers used to look. Written blind.

func edgeWebLegacy(t *testing.T, slug string, id int64, name string) {
	t.Helper()
	home := os.Getenv("HOME")
	alt := filepath.Join(home, "alt-pluto")
	t.Setenv("KRAKEN_PLUTO_DIR", alt)
	for _, dir := range []string{
		filepath.Join(home, "Documents", "pluto"),
		filepath.Join(home, "Documents", "pluto", "archive"),
		alt, filepath.Join(alt, "archive"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.%s", slug, id, name)), []byte("legacy"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestEdgeArtifactHandlerNonCellHomeNotInTableIs404(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "edge-web-art", Repo: "x", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{{Path: "in-table.md", Bytes: []byte("row"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	edgeWebLegacy(t, "edge-web-art", id, "legacy-only.md")

	for _, url := range []string{
		"/api/artifact/edge-web-art/pluto/legacy-only.md",
		"/api/artifact/edge-web-art/archive/legacy-only.md",
		"/api/artifact/edge-web-art/pluto/missing.md",
		"/api/artifact/edge-web-art/pluto/" + fmt.Sprintf("edge-web-art-%d.legacy-only.md", id),
		"/api/artifact/edge-web-art/pluto/../pluto/legacy-only.md",
		"/api/artifact/no-such-job/pluto/in-table.md",
	} {
		rec := httptest.NewRecorder()
		artifactHandler(rec, httptest.NewRequest("GET", url, nil))
		if rec.Code != 404 {
			t.Errorf("GET %s = %d (%q), want 404", url, rec.Code, rec.Body.String())
		}
	}

	// the control: the row itself is served
	rec := httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/edge-web-art/pluto/in-table.md", nil))
	if rec.Code != 200 || rec.Body.String() != "row" {
		t.Errorf("GET the stored row = %d %q, want 200 \"row\"", rec.Code, rec.Body.String())
	}
}

// Amended: "artifact URL path segments are url.PathEscaped per segment, and
// the server decodes them, so a name with #, % or a space round-trips" — a
// root/ row serves like any nested path.
func TestEdgeArtifactHandlerEscapedNamesRoundTrip(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "edge-web-esc", Repo: "x", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{"notes #2.md", "50% plan.md", "a b.md", "root/x y.md", "root/NOTES.md", "dir/q?.md", "ü.md"}
	var files []store.ArtifactFile
	for _, n := range names {
		files = append(files, store.ArtifactFile{Path: n, Bytes: []byte("b:" + n), Mtime: 1})
	}
	if err := st.SyncArtifacts(id, files, nil); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		segs := strings.Split(n, "/")
		for i, s := range segs {
			segs[i] = url.PathEscape(s)
		}
		u := "/api/artifact/edge-web-esc/pluto/" + strings.Join(segs, "/")
		rec := httptest.NewRecorder()
		artifactHandler(rec, httptest.NewRequest("GET", u, nil))
		if rec.Code != 200 || rec.Body.String() != "b:"+n {
			t.Errorf("GET %s = %d %q, want 200 %q", u, rec.Code, rec.Body.String(), "b:"+n)
		}
	}
	// a doubly-escaped name is a DIFFERENT name: %2523 decodes once to %23
	rec := httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/edge-web-esc/pluto/notes%2520%25232.md", nil))
	if rec.Code != 404 {
		t.Errorf("a doubly-escaped name served %d, want 404 (decode exactly once)", rec.Code)
	}
}
