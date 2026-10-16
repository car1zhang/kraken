package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// portability_edge_test.go — the EDGE dimension of artifacts/50-contract.md
// (kraken-portability-audit) for internal/api: the retired pluto homes in
// ArtifactPath, jobArtifacts reading no filesystem for a non-live job even
// when legacy flat copies sit on disk, and the additive board key. Written
// blind from the contract text.

func edgeAPIStore(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	for _, k := range []string{"KRAKEN_PLUTO_DIR", "KRAKEN_OPERATOR", "KRAKEN_REPOS_DIR"} {
		t.Setenv(k, "x")
		os.Unsetenv(k)
	}
	return st
}

// edgeLegacyCopies writes flat copies where the retired pluto readers looked:
// ~/Documents/pluto, a KRAKEN_PLUTO_DIR, and their archive/ subdirs.
func edgeLegacyCopies(t *testing.T, j *jobs.Job, names ...string) {
	t.Helper()
	home := os.Getenv("HOME")
	alt := filepath.Join(home, "alt-pluto")
	t.Setenv("KRAKEN_PLUTO_DIR", alt)
	prefix := fmt.Sprintf("%s-%d.", j.Slug, j.ID)
	for _, dir := range []string{
		filepath.Join(home, "Documents", "pluto"),
		filepath.Join(home, "Documents", "pluto", "archive"),
		alt,
		filepath.Join(alt, "archive"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range names {
			if err := os.WriteFile(filepath.Join(dir, prefix+n), []byte("legacy "+n), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// Contract: "ArtifactPath: home cell → unchanged · any other home → ("",
// false)" — including the two retired homes, case variants, traversal, and
// the empty home, with a legacy file present at every place a pluto reader
// used to resolve.
func TestEdgeArtifactPathRefusesEveryNonCellHome(t *testing.T) {
	st := edgeAPIStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-ap", Repo: "x", State: "done"})
	edgeLegacyCopies(t, j, "report.md")
	for _, home := range []string{"pluto", "archive", "PLUTO", "Pluto", "../", "..", "", " ", "cell/..", "pluto/", "archive/../pluto", "store"} {
		for _, name := range []string{"report.md", "../report.md", fmt.Sprintf("edge-ap-%d.report.md", j.ID)} {
			p, ok := ArtifactPath(j, home, name)
			if ok || p != "" {
				t.Errorf("ArtifactPath(job, %q, %q) = (%q, %v), want (\"\", false)", home, name, p, ok)
			}
		}
	}
}

// Contract: "!live → store rows only … no filesystem read". A job with NO
// rows lists nothing, however many legacy flat copies sit on disk.
func TestEdgeJobArtifactsNoRowsIgnoresLegacyCopies(t *testing.T) {
	st := edgeAPIStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-norows", Repo: "x", State: "done"})
	edgeLegacyCopies(t, j, "00-overview.md", "report.html")
	if arts := jobArtifacts(j, false); len(arts) != 0 {
		t.Fatalf("jobArtifacts(no rows, !live) = %+v, want none — legacy flat copies must not be read", arts)
	}
}

// With rows AND legacy copies of other names: exactly the rows, every URL on
// the `pluto` home for an active job.
func TestEdgeJobArtifactsRowsOnlyNotTheUnion(t *testing.T) {
	st := edgeAPIStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-rows", Repo: "x", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "a.md", Bytes: []byte("a"), Mtime: 1},
		{Path: "NOTES.md", Bytes: []byte("n"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	edgeLegacyCopies(t, j, "legacy-only.md", "a.md")
	arts := jobArtifacts(j, false)
	got := map[string]string{}
	for _, a := range arts {
		got[a.Name] = a.URL
	}
	if len(arts) != 2 || len(got) != 2 {
		t.Fatalf("jobArtifacts = %+v, want exactly the two rows", arts)
	}
	for _, n := range []string{"a.md", "NOTES.md"} {
		if want := "/api/artifact/edge-rows/pluto/" + n; got[n] != want {
			t.Errorf("%s URL = %q, want %q", n, got[n], want)
		}
	}
	if _, ok := got["legacy-only.md"]; ok {
		t.Errorf("a legacy flat copy surfaced in the listing")
	}
}

// Contract: "URL home pluto for an active job and archive for an archived
// one" — an archived job's rows list under `archive`, again with no file on
// disk anywhere.
func TestEdgeJobArtifactsArchivedHomeFromRows(t *testing.T) {
	st := edgeAPIStore(t)
	row := mustCreateStoreJob(t, st, store.Job{Slug: "edge-arch-art", Repo: "x", State: "done"})
	if err := st.SyncArtifacts(row.ID, []store.ArtifactFile{{Path: "r.md", Bytes: []byte("r"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	name, err := st.ArchiveJob(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	a := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), name)
	if a == nil {
		t.Fatal("archived job not found")
	}
	arts := jobArtifacts(a, false)
	if len(arts) != 1 {
		t.Fatalf("archived jobArtifacts = %+v, want the one row", arts)
	}
	if !strings.Contains(arts[0].URL, "/archive/") || strings.Contains(arts[0].URL, "/pluto/") {
		t.Errorf("archived artifact URL = %q, want the archive home", arts[0].URL)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), "Documents")); err == nil {
		t.Errorf("listing an archived job created ~/Documents")
	}
}

// Contract: `Operator string json:"operator"` — no omitempty, so the key
// rides even when the value is empty; additive, the contract number stays.
func TestEdgeWebBoardOperatorKeyAlwaysPresent(t *testing.T) {
	b, err := json.Marshal(webBoard{})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	raw, ok := m["operator"]
	if !ok {
		t.Fatalf("zero webBoard JSON has no \"operator\" key: %s", b)
	}
	if string(raw) != `""` {
		t.Errorf("zero webBoard operator = %s, want \"\"", raw)
	}
	b2, _ := json.Marshal(webBoard{Operator: `O'Brien "x"`})
	var m2 map[string]any
	_ = json.Unmarshal(b2, &m2)
	if m2["operator"] != `O'Brien "x"` {
		t.Errorf("operator round-trip = %v", m2["operator"])
	}
}

// ---------------------------------------------------------------------------
// Review-round-1 amendments — escaped artifact URLs, the mirror uploads gate
// ---------------------------------------------------------------------------

func edgeEscapedPath(rel string) string {
	parts := strings.Split(rel, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// Amended: "a root/… row lists and serves like any nested path; artifact URL
// path segments are url.PathEscaped per segment … so a name with #, % or a
// space round-trips".
func TestEdgeJobArtifactsURLsArePathEscaped(t *testing.T) {
	st := edgeAPIStore(t)
	j := mustCreateStoreJob(t, st, store.Job{Slug: "edge-esc", Repo: "x", State: "done"})
	names := []string{"notes #2.md", "50% plan.md", "a b.md", "root/x y.md", "root/NOTES.md", "dir/q?.md", "ü.md", "plain.md"}
	var files []store.ArtifactFile
	for _, n := range names {
		files = append(files, store.ArtifactFile{Path: n, Bytes: []byte("bytes of " + n), Mtime: 1})
	}
	if err := st.SyncArtifacts(j.ID, files, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range jobArtifacts(j, false) {
		got[a.Name] = a.URL
	}
	for _, n := range names {
		want := "/api/artifact/edge-esc/pluto/" + edgeEscapedPath(n)
		if got[n] != want {
			t.Errorf("URL for %q = %q, want %q", n, got[n], want)
		}
		u, err := url.Parse(got[n])
		if err != nil {
			t.Errorf("URL for %q does not parse: %v", n, err)
			continue
		}
		if u.Fragment != "" || u.RawQuery != "" {
			t.Errorf("URL for %q parses with fragment %q / query %q — the name was not escaped", n, u.Fragment, u.RawQuery)
		}
		if !strings.HasSuffix(u.Path, "/pluto/"+n) {
			t.Errorf("URL for %q decodes to path %q, want it to end in the name", n, u.Path)
		}
		data, _, ok := ArtifactBytes(j, "pluto", n)
		if !ok || string(data) != "bytes of "+n {
			t.Errorf("ArtifactBytes(%q) = (%q, %v), want the row's bytes", n, data, ok)
		}
	}
}

// edgeMirrorPost posts {slug} to path with mirror mode ON.
func edgeMirrorPost(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	mux := http.NewServeMux()
	Register(mux)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"slug":"nonexistent"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// Amended: "in mirror mode archive, unarchive and delete are refused with 403
// naming KRAKEN_UPLOADS_DIR unless that variable points away from the default
// uploads dir". The boundary: unset, empty, the default itself (spelled with
// a trailing slash and with a `.` segment) are all "not away".
func TestEdgeMirrorArchiveRefusedUnlessUploadsSandboxed(t *testing.T) {
	routes := []string{"/api/archive", "/api/unarchive", "/api/delete"}
	notAway := map[string]func(home string) (string, bool){
		"unset":            func(string) (string, bool) { return "", false },
		"empty":            func(string) (string, bool) { return "", true },
		"default":          func(h string) (string, bool) { return filepath.Join(h, ".kraken", "uploads"), true },
		"default slash":    func(h string) (string, bool) { return filepath.Join(h, ".kraken", "uploads") + "/", true },
		"default dot path": func(h string) (string, bool) { return filepath.Join(h, ".kraken") + "/./uploads", true },
	}
	for name, mk := range notAway {
		t.Run(name, func(t *testing.T) {
			edgeAPIStore(t)
			home := os.Getenv("HOME")
			t.Setenv("KRAKEN_UPLOADS_DIR", "x")
			os.Unsetenv("KRAKEN_UPLOADS_DIR")
			if v, set := mk(home); set {
				t.Setenv("KRAKEN_UPLOADS_DIR", v)
			}
			for _, r := range routes {
				rec := edgeMirrorPost(t, r)
				if rec.Code != http.StatusForbidden {
					t.Errorf("%s with uploads %s: status %d, want 403", r, name, rec.Code)
				}
				if !strings.Contains(rec.Body.String(), "KRAKEN_UPLOADS_DIR") {
					t.Errorf("%s refusal %q does not name KRAKEN_UPLOADS_DIR", r, rec.Body.String())
				}
			}
		})
	}
	t.Run("sandboxed", func(t *testing.T) {
		edgeAPIStore(t)
		t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
		for _, r := range routes {
			if rec := edgeMirrorPost(t, r); rec.Code == http.StatusForbidden {
				t.Errorf("%s with a sandboxed uploads dir: 403 %q, want it past the gate", r, rec.Body.String())
			}
		}
	})
	t.Run("mirror off", func(t *testing.T) {
		edgeAPIStore(t)
		SetMirrorMode(false)
		mux := http.NewServeMux()
		Register(mux)
		for _, r := range routes {
			req := httptest.NewRequest(http.MethodPost, r, strings.NewReader(`{"slug":"nonexistent"}`))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			if rec.Code == http.StatusForbidden {
				t.Errorf("%s with mirror mode off: 403, want the gate inert", r)
			}
		}
	})
}
