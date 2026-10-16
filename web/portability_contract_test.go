package web

import (
	"fmt"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/store"
	"kraken/internal/util"
)

// portability_contract_test.go — athena-logic's BLIND contract test for the
// portability audit's web half (artifacts/50-contract.md slice P row `web`
// `artifactHandler`: "a non-cell home whose path is not in the table → 404").
// Written from the contract text alone; serve.go was never opened. Fixture
// (useTestStore) is serve_storemode_test.go's own.

// Contract: artifactHandler serves a non-cell home from the artifacts table
// only: a name in the table is 200 with its bytes; a name absent from the
// table is 404 even when a legacy ~/Documents/pluto flat copy of it exists.
func TestContractPortArtifactHandlerNonCellIsTableOnly(t *testing.T) {
	st := useTestStore(t)
	for _, k := range []string{"KRAKEN_PLUTO_DIR", "KRAKEN_OPERATOR"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	id, err := st.CreateJob(&store.Job{Slug: "port-web", Repo: "kraken", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{{Path: "in-table.md", Bytes: []byte("from the table"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(util.Home(), "Documents", "pluto")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, fmt.Sprintf("port-web-%d.flat-only.md", id)), []byte("legacy"), 0o644); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/port-web/pluto/in-table.md", nil))
	if rec.Code != 200 || rec.Body.String() != "from the table" {
		t.Errorf("in-table artifact: status %d body %q, want 200 \"from the table\"", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/port-web/pluto/flat-only.md", nil))
	if rec.Code != 404 {
		t.Errorf("name only in the legacy pluto dir: status %d, want 404", rec.Code)
	}

	rec = httptest.NewRecorder()
	artifactHandler(rec, httptest.NewRequest("GET", "/api/artifact/port-web/pluto/nowhere.md", nil))
	if rec.Code != 404 {
		t.Errorf("name in neither: status %d, want 404", rec.Code)
	}
}

// Contract (review-round-1 amendment): "artifact URL path segments are
// url.PathEscape'd per segment, and the server decodes them, so a name with
// #, % or a space round-trips"; "a root/… row … serves like any nested path".
func TestContractPortArtifactHandlerDecodesEscapedNames(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_PLUTO_DIR", "")
	os.Unsetenv("KRAKEN_PLUTO_DIR")
	id, err := st.CreateJob(&store.Job{Slug: "port-web-esc", Repo: "kraken", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SyncArtifacts(id, []store.ArtifactFile{
		{Path: "a b#c%d.md", Bytes: []byte("odd name"), Mtime: 1},
		{Path: "root/NOTES.md", Bytes: []byte("root note"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]string{
		"/api/artifact/port-web-esc/pluto/" + url.PathEscape("a b#c%d.md"): "odd name",
		"/api/artifact/port-web-esc/pluto/root/NOTES.md":                   "root note",
	} {
		rec := httptest.NewRecorder()
		artifactHandler(rec, httptest.NewRequest("GET", target, nil))
		if rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("GET %s: status %d body %q, want 200 %q", target, rec.Code, rec.Body.String(), want)
		}
	}
}
