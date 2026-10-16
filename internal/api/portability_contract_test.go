package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// portability_contract_test.go — athena-logic's BLIND contract tests for the
// portability audit, internal/api half (artifacts/50-contract.md slice N row
// `api.webBoard.Operator`, slice P rows `api.jobArtifacts`,
// `api.ArtifactPath`, `api.webAction`). Written from the contract text alone;
// no non-test file in this package was opened by the author. Fixtures
// (useTestStore, mustCreateStoreJob, mustJSON, postJSON, webReq) are the
// existing test files' own.

// contractPortAPIEnv isolates HOME and the knobs these subjects read.
func contractPortAPIEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"KRAKEN_OPERATOR", "KRAKEN_PLUTO_DIR", "KRAKEN_REPOS_DIR", "KRAKEN_BRANCH_PREFIX"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

// contractPortLegacyFlatCopy writes what the retired preserve step used to
// leave in ~/Documents/pluto for a job: a `<slug>-<id>.<name>` flat copy. A
// reader that still consults the old directory would surface it.
func contractPortLegacyFlatCopy(t *testing.T, j *jobs.Job, name, body string) {
	t.Helper()
	dir := filepath.Join(util.Home(), "Documents", "pluto")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, fmt.Sprintf("%s-%d.%s", j.Slug, j.ID, name))
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Contract: `api.webBoard.Operator` — `Operator string json:"operator"`;
// "buildWebBoard() sets it to jobs.Operator(); additive, apiContract
// unchanged".
func TestContractPortWebBoardCarriesOperator(t *testing.T) {
	useTestStore(t)
	contractPortAPIEnv(t)
	t.Setenv("KRAKEN_OPERATOR", "Ada")

	b := buildWebBoard()
	if b.Operator != "Ada" {
		t.Fatalf("buildWebBoard().Operator = %q, want Ada (jobs.Operator())", b.Operator)
	}
	if b.Operator != jobs.Operator() {
		t.Fatalf("buildWebBoard().Operator = %q, jobs.Operator() = %q", b.Operator, jobs.Operator())
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"operator":"Ada"`) {
		t.Errorf("board JSON lacks \"operator\":\"Ada\": %.300s", raw)
	}
	if b.Contract != apiContract {
		t.Errorf("board contract = %d, want apiContract %d", b.Contract, apiContract)
	}
}

// Contract: `api.jobArtifacts` "!live → store rows only, URL home pluto for an
// active job …, no filesystem read" — a legacy flat copy is never listed.
func TestContractPortJobArtifactsNonLiveFromTableOnly(t *testing.T) {
	useTestStore(t)
	contractPortAPIEnv(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "port-art", Repo: "kraken", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("o"), Mtime: 1},
		{Path: "NOTES.md", Bytes: []byte("n"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	contractPortLegacyFlatCopy(t, j, "legacy-only.md", "old")

	arts := jobArtifacts(j, false)
	got := map[string]string{}
	for _, a := range arts {
		got[a.Name] = a.URL
	}
	want := map[string]string{
		"00-overview.md": "/api/artifact/port-art/pluto/00-overview.md",
		"NOTES.md":       "/api/artifact/port-art/pluto/NOTES.md",
	}
	if len(got) != len(want) || len(arts) != len(want) {
		t.Fatalf("jobArtifacts(!live) = %+v, want exactly %v", arts, want)
	}
	for name, url := range want {
		if got[name] != url {
			t.Errorf("%s url = %q, want %q", name, got[name], url)
		}
	}
}

// Contract: `api.jobArtifacts` "!live → … URL home … archive for an archived
// one (as today's storeArtifactList)".
func TestContractPortJobArtifactsArchivedHome(t *testing.T) {
	useTestStore(t)
	contractPortAPIEnv(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "port-arch-art", Repo: "kraken", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "final.md", Bytes: []byte("done"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}
	name, err := st.ArchiveJob(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	archived := jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), name)
	if archived == nil {
		t.Fatal("archived job not found")
	}
	arts := jobArtifacts(archived, false)
	if len(arts) != 1 || arts[0].Name != "final.md" {
		t.Fatalf("jobArtifacts(archived) = %+v, want final.md only", arts)
	}
	if want := "/api/artifact/" + name + "/archive/final.md"; arts[0].URL != want {
		t.Errorf("archived url = %q, want %q", arts[0].URL, want)
	}
}

// Contract: `api.ArtifactPath` "home cell → unchanged · any other home →
// ("", false)" — even when a legacy flat copy sits where the old pluto home
// used to resolve.
func TestContractPortArtifactPathNonCellIsFalse(t *testing.T) {
	useTestStore(t)
	contractPortAPIEnv(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "port-path", Repo: "kraken", State: "done"})
	contractPortLegacyFlatCopy(t, j, "x.md", "old")

	for _, home := range []string{"pluto", "archive", "anything"} {
		if p, ok := ArtifactPath(j, home, "x.md"); ok || p != "" {
			t.Errorf("ArtifactPath(%q) = (%q, %v), want (\"\", false)", home, p, ok)
		}
	}
}

// Contract (review-round-1 amendment): `api.webAction` "in mirror mode
// archive, unarchive and delete are refused with 403 naming KRAKEN_UPLOADS_DIR
// unless that variable points away from the default uploads dir". With it
// sandboxed, the three routes get past the gate to their own handler.
func TestContractPortMirrorArchiveGateFollowsUploadsDir(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	useTestStore(t)
	contractPortAPIEnv(t)
	t.Setenv("KRAKEN_UPLOADS_DIR", "")
	os.Unsetenv("KRAKEN_UPLOADS_DIR")
	mux := http.NewServeMux()
	Register(mux)
	routes := []string{"/api/archive", "/api/unarchive", "/api/delete"}

	for _, path := range routes {
		rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "port-nobody"}))
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "KRAKEN_UPLOADS_DIR") {
			t.Errorf("default uploads dir, %s in mirror mode: %d %q, want 403 naming KRAKEN_UPLOADS_DIR", path, rec.Code, strings.TrimSpace(rec.Body.String()))
		}
	}

	t.Setenv("KRAKEN_UPLOADS_DIR", t.TempDir())
	for _, path := range routes {
		rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "port-nobody"}))
		if rec.Code == http.StatusForbidden {
			t.Errorf("sandboxed uploads dir, %s in mirror mode: 403 (%s), want past the gate", path, strings.TrimSpace(rec.Body.String()))
		}
	}
}

// Contract (amended): `api.jobArtifacts` "a root/… row lists … like any
// nested path; artifact URL path segments are url.PathEscape'd per segment" —
// so a name with a space, # or % produces an escaped URL, and the root/
// prefix stays a real path separator.
func TestContractPortArtifactURLsAreEscapedPerSegment(t *testing.T) {
	useTestStore(t)
	contractPortAPIEnv(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "port-esc", Repo: "kraken", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "a b#c%d.md", Bytes: []byte("odd"), Mtime: 1},
		{Path: "root/NOTES.md", Bytes: []byte("notes"), Mtime: 1},
	}, nil); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, a := range jobArtifacts(j, false) {
		got[a.Name] = a.URL
	}
	want := map[string]string{
		"a b#c%d.md":    "/api/artifact/port-esc/pluto/" + url.PathEscape("a b#c%d.md"),
		"root/NOTES.md": "/api/artifact/port-esc/pluto/root/NOTES.md",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("jobArtifacts = %v, want %v", got, want)
	}
	if data, _, ok := ArtifactBytes(j, "pluto", "root/NOTES.md"); !ok || string(data) != "notes" {
		t.Errorf("ArtifactBytes(root/NOTES.md) = (%q, %v), want notes", data, ok)
	}
}
