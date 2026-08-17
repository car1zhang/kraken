package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// library_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md's wire table (the artifact library's four routes) and the
// registration obligations under Slice A.
//
// Written from the frozen contract alone: internal/api/library.go and
// internal/api/register.go's new lines were never read by any route — no Read,
// no grep, no git show. The harness this file leans on (useTestStore,
// mustCreateStoreJob, mustJSON) is pre-existing package state at the base
// commit, and every route is driven through the real mux (api.Register), the
// same wiring web.Serve uses, so the registration clauses ("/api/library exact,
// /api/library/artifact/ prefix") are exercised rather than assumed.
//
// Dimension: CONTRACT — the stated wire shape, the resolve/freeze semantics and
// the happy paths. The 400 arms (missing/empty field, unknown field, the 1 MB
// cap's 413) are athena-edge's ground and are deliberately not duplicated here;
// the 404 arms ARE here, because "unresolvable job or artifact → 404" and
// "unknown/malformed id → 404" are what give "resolves" and "lookup is by id"
// their meaning.
//
// Every package-level identifier declared here is prefixed `contractLibrary` —
// athena-edge shares this package's test namespace, and several existing
// contract files already own shorter `contract…` names in it.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractLibraryMux builds the real mux. Route tests go through it rather than
// calling handlers directly so that a handler which exists but is registered on
// the wrong pattern still fails.
func contractLibraryMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractLibraryGET(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractLibraryPOST(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// contractLibraryDecode decodes a JSON body into loose maps. Loose rather than
// typed because the contract pins key PRESENCE ("all five keys always present,
// no omitempty"), which a struct with omitempty would silently paper over.
func contractLibraryDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

// contractLibraryItems drives GET /api/library and returns the raw items array.
func contractLibraryItems(t *testing.T, mux http.Handler) []any {
	t.Helper()
	rec := contractLibraryGET(t, mux, "/api/library")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library: status %d, body %s", rec.Code, rec.Body.String())
	}
	body := contractLibraryDecode(t, rec)
	raw, ok := body["items"]
	if !ok {
		t.Fatalf("GET /api/library body has no `items` key: %s", rec.Body.String())
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("`items` = %#v, want an array — the contract says items is ALWAYS an array, never null", raw)
	}
	return items
}

// contractLibrarySaveOK posts a save and requires the contract's 200 shape,
// returning the row id it reports.
func contractLibrarySaveOK(t *testing.T, mux http.Handler, slug, home, name string) int64 {
	t.Helper()
	rec := contractLibraryPOST(t, mux, "/api/library", map[string]any{
		"slug": slug, "home": home, "name": name,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/library {%s,%s,%s}: status %d, body %s", slug, home, name, rec.Code, rec.Body.String())
	}
	body := contractLibraryDecode(t, rec)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("POST /api/library body = %s, want {\"ok\":true,\"id\":<row id>}", rec.Body.String())
	}
	raw, present := body["id"]
	if !present {
		t.Fatalf("POST /api/library body = %s, want an `id` (the saved row's id)", rec.Body.String())
	}
	n, ok := raw.(float64)
	if !ok {
		t.Fatalf("`id` = %#v, want a number", raw)
	}
	if int64(n) <= 0 {
		t.Fatalf("`id` = %d, want a real row id", int64(n))
	}
	return int64(n)
}

// contractLibrarySeedJob creates a store-backed job with artifacts mirrored
// into the artifacts table (the block-event sync's own write path), which is
// what the save's resolution helper reads for a non-live job's "pluto" home.
func contractLibrarySeedJob(t *testing.T, st *store.Store, slug string, files map[string]string) *jobs.Job {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "done"})
	contractLibrarySyncArtifacts(t, st, j.ID, files)
	return j
}

func contractLibrarySyncArtifacts(t *testing.T, st *store.Store, jobID int64, files map[string]string) {
	t.Helper()
	arts := make([]store.ArtifactFile, 0, len(files))
	for name, body := range files {
		arts = append(arts, store.ArtifactFile{Path: name, Bytes: []byte(body), Mtime: 1})
	}
	if err := st.SyncArtifacts(jobID, arts, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
}

// contractLibraryItemByID finds one wire item by id.
func contractLibraryItemByID(t *testing.T, items []any, id int64) map[string]any {
	t.Helper()
	for _, raw := range items {
		obj, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("item %#v is not an object", raw)
		}
		if n, ok := obj["id"].(float64); ok && int64(n) == id {
			return obj
		}
	}
	t.Fatalf("item %d absent from the list: %#v", id, items)
	return nil
}

// contractLibraryArtifactURL is the contract's url rule, restated here so the
// assertions pin the RULE rather than whatever the handler happens to build.
func contractLibraryArtifactURL(id int64, name string) string {
	return fmt.Sprintf("/api/library/artifact/%d/%s", id, name)
}

// ---------------------------------------------------------------------------
// GET /api/library
// ---------------------------------------------------------------------------

// TestContractLibraryGETAlwaysServesAnArray pins the empty case: no saved rows
// is `{"items":[]}` — an array, never null. Both clients map over this
// unconditionally.
func TestContractLibraryGETAlwaysServesAnArray(t *testing.T) {
	useTestStore(t)
	mux := contractLibraryMux(t)

	rec := contractLibraryGET(t, mux, "/api/library")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library on an empty library: status %d, body %s", rec.Code, rec.Body.String())
	}
	if items := contractLibraryItems(t, mux); len(items) != 0 {
		t.Fatalf("items = %#v, want an empty array", items)
	}
	if got := strings.TrimSpace(rec.Body.String()); !strings.Contains(got, `"items":[]`) {
		t.Fatalf("body = %s, want items serialized as [] (never null)", got)
	}
}

// TestContractLibraryGETShapeOrderAndURLs pins the whole list obligation in one
// walk: the sort (saved_at DESC, ties id DESC), all five keys present on every
// item with no omitempty, saved_at carried through as the stored unix millis,
// and the url built as /api/library/artifact/<id>/<name> with the name exactly
// as stored — slashes intact, never escaped away.
func TestContractLibraryGETShapeOrderAndURLs(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "listing", map[string]string{"a.md": "a"})

	// Seeded through the frozen store accessor rather than through the POST, so
	// this test measures the READ alone: saved_at and slug are exactly what a
	// row holds, including the degenerate values that would vanish under
	// omitempty.
	oldest, err := st.SaveArtifact(j.ID, "listing", "10-oldest.md", []byte("o"), 100)
	if err != nil {
		t.Fatal(err)
	}
	tieLow, err := st.SaveArtifact(j.ID, "listing", "20-tie-low.md", []byte("a"), 500)
	if err != nil {
		t.Fatal(err)
	}
	tieHigh, err := st.SaveArtifact(j.ID, "listing", "icons/deep/30-tie-high.svg", []byte("b"), 500)
	if err != nil {
		t.Fatal(err)
	}
	newest, err := st.SaveArtifact(j.ID, "listing", "40-newest.md", []byte("n"), 900)
	if err != nil {
		t.Fatal(err)
	}
	// A row whose slug and saved_at are both zero values: their keys must still
	// be on the wire.
	zeroed, err := st.SaveArtifact(j.ID, "", "50-zeroed.md", []byte("z"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if tieHigh <= tieLow {
		t.Fatalf("fixture: expected ascending row ids for the tie pair, got %d then %d", tieLow, tieHigh)
	}

	items := contractLibraryItems(t, mux)
	if len(items) != 5 {
		t.Fatalf("items = %#v, want 5", items)
	}
	var gotOrder []int64
	for _, raw := range items {
		obj := raw.(map[string]any)
		gotOrder = append(gotOrder, int64(obj["id"].(float64)))
	}
	wantOrder := []int64{newest, tieHigh, tieLow, oldest, zeroed}
	for i := range wantOrder {
		if gotOrder[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v (saved_at DESC, ties id DESC)", gotOrder, wantOrder)
		}
	}

	// Every item carries exactly the five frozen keys, each of the frozen kind.
	wantKeys := []string{"id", "slug", "name", "saved_at", "url"}
	for _, raw := range items {
		obj := raw.(map[string]any)
		for _, k := range wantKeys {
			if _, ok := obj[k]; !ok {
				t.Fatalf("item %#v is missing the %q key — all five keys are always present (no omitempty)", obj, k)
			}
		}
		if len(obj) != len(wantKeys) {
			t.Fatalf("item %#v carries %d keys, want exactly %v", obj, len(obj), wantKeys)
		}
		if _, ok := obj["id"].(float64); !ok {
			t.Fatalf("id = %#v, want a number", obj["id"])
		}
		if _, ok := obj["saved_at"].(float64); !ok {
			t.Fatalf("saved_at = %#v, want a number (unix millis)", obj["saved_at"])
		}
		for _, k := range []string{"slug", "name", "url"} {
			if _, ok := obj[k].(string); !ok {
				t.Fatalf("%s = %#v, want a string", k, obj[k])
			}
		}
	}

	// saved_at is the stored value verbatim, in unix millis.
	if got := contractLibraryItemByID(t, items, newest)["saved_at"].(float64); int64(got) != 900 {
		t.Fatalf("saved_at = %d, want the stored 900 (unix millis, passed through)", int64(got))
	}
	if got := contractLibraryItemByID(t, items, zeroed)["saved_at"].(float64); int64(got) != 0 {
		t.Fatalf("saved_at = %d, want 0 — a zero saved_at is a value, not an absence", int64(got))
	}
	if got := contractLibraryItemByID(t, items, zeroed)["slug"].(string); got != "" {
		t.Fatalf("slug = %q, want the stored empty string", got)
	}

	// The url rule, including the nested name whose slashes must survive.
	for id, name := range map[int64]string{
		newest:  "40-newest.md",
		tieHigh: "icons/deep/30-tie-high.svg",
		oldest:  "10-oldest.md",
	} {
		obj := contractLibraryItemByID(t, items, id)
		if want := contractLibraryArtifactURL(id, name); obj["url"] != want {
			t.Fatalf("url = %q, want %q (name exactly as stored, slashes intact)", obj["url"], want)
		}
		if obj["name"] != name {
			t.Fatalf("name = %q, want %q", obj["name"], name)
		}
	}
}

// TestContractLibraryGETIsOpenUnderMirror pins the one mirror exception the
// contract states for this family: the list is a READ and stays reachable on a
// mirror board.
func TestContractLibraryGETIsOpenUnderMirror(t *testing.T) {
	st := useTestStore(t)
	j := contractLibrarySeedJob(t, st, "mirror-read", map[string]string{"a.md": "a"})
	if _, err := st.SaveArtifact(j.ID, "mirror-read", "a.md", []byte("a"), 10); err != nil {
		t.Fatal(err)
	}

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	mux := contractLibraryMux(t)

	rec := contractLibraryGET(t, mux, "/api/library")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library under --mirror: status %d, body %s — the read stays open", rec.Code, rec.Body.String())
	}
	if items := contractLibraryItems(t, mux); len(items) != 1 {
		t.Fatalf("items under --mirror = %#v, want the one saved row", items)
	}
}

// ---------------------------------------------------------------------------
// POST /api/library — resolve + freeze
// ---------------------------------------------------------------------------

// TestContractLibraryPOSTFreezesCurrentBytes is the feature's central promise:
// the save snapshots the artifact's bytes AS THEY ARE AT SAVE TIME, and a
// later rewrite of the source artifact does not change what the library
// serves.
func TestContractLibraryPOSTFreezesCurrentBytes(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "freeze", map[string]string{"report.html": "<h1>version one</h1>"})

	id := contractLibrarySaveOK(t, mux, "freeze", "pluto", "report.html")

	// The row the route reports is the row the store holds.
	metas, err := st.ListSavedArtifacts()
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].ID != id || metas[0].JobID != j.ID || metas[0].Name != "report.html" {
		t.Fatalf("store rows after the save = %+v, want one row %d for job %d", metas, id, j.ID)
	}

	// The source artifact moves on...
	contractLibrarySyncArtifacts(t, st, j.ID, map[string]string{"report.html": "<h1>version two</h1>"})

	// ...and the library still serves what was frozen.
	rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(id, "report.html"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET snapshot: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<h1>version one</h1>" {
		t.Fatalf("snapshot bytes = %q, want the FROZEN bytes %q — the route serves the snapshot, never the live artifact", got, "<h1>version one</h1>")
	}
	if items := contractLibraryItems(t, mux); len(items) != 1 {
		t.Fatalf("items = %#v, want the one saved row", items)
	}
}

// TestContractLibraryPOSTUpsertsKeepingRowID pins the re-save: same (job, name)
// replaces the snapshot and saved_at, keeps the same row id, and never
// duplicates. The row id is what every url and every remove is addressed by.
func TestContractLibraryPOSTUpsertsKeepingRowID(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "resave", map[string]string{"doc.md": "first"})

	before := time.Now().UnixMilli()
	first := contractLibrarySaveOK(t, mux, "resave", "pluto", "doc.md")
	after := time.Now().UnixMilli()

	// saved_at is stamped by the server: the request body carries no such
	// field, and the wire declares it unix millis.
	items := contractLibraryItems(t, mux)
	at := int64(contractLibraryItemByID(t, items, first)["saved_at"].(float64))
	if at < before || at > after {
		t.Fatalf("saved_at = %d, want the save's own moment in [%d, %d] (server-stamped unix millis)", at, before, after)
	}

	contractLibrarySyncArtifacts(t, st, j.ID, map[string]string{"doc.md": "second"})
	second := contractLibrarySaveOK(t, mux, "resave", "pluto", "doc.md")
	if second != first {
		t.Fatalf("re-save returned row id %d, want the same row %d — the upsert keys on (job_id, name)", second, first)
	}

	items = contractLibraryItems(t, mux)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want exactly 1 — a re-save never duplicates", items)
	}
	reAt := int64(contractLibraryItemByID(t, items, first)["saved_at"].(float64))
	if reAt < at {
		t.Fatalf("saved_at went backwards on re-save (%d then %d) — a re-save replaces saved_at", at, reAt)
	}

	rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(first, "doc.md"))
	if got := rec.Body.String(); got != "second" {
		t.Fatalf("snapshot after re-save = %q, want the re-frozen bytes %q", got, "second")
	}
}

// TestContractLibraryPOSTResolvesTheArchivedNamespace pins the home rule:
// `home == "archive"` resolves the slug in the ARCHIVED namespace and anything
// else in active jobs. The same string means two different jobs depending on
// home, which is exactly why the request carries one.
func TestContractLibraryPOSTResolvesTheArchivedNamespace(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "to-archive", map[string]string{"final.md": "the frozen doc"})

	archivedName, err := st.ArchiveJob(j.ID)
	if err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	id := contractLibrarySaveOK(t, mux, archivedName, "archive", "final.md")
	rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(id, "final.md"))
	if rec.Code != http.StatusOK || rec.Body.String() != "the frozen doc" {
		t.Fatalf("archived save served %d %q, want the archived job's bytes", rec.Code, rec.Body.String())
	}
	if metas, err := st.ListSavedArtifacts(); err != nil || len(metas) != 1 || metas[0].JobID != j.ID {
		t.Fatalf("store rows = %+v (%v), want one row keyed on the job's immutable id %d", metas, err, j.ID)
	}

	// The same slug under a NON-archive home resolves in the active namespace,
	// where it names nothing.
	rec = contractLibraryPOST(t, mux, "/api/library", map[string]any{
		"slug": archivedName, "home": "pluto", "name": "final.md",
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("archived name under home=pluto: status %d, want 404 — home decides the namespace", rec.Code)
	}
}

// TestContractLibraryPOSTUnresolvableIs404 pins the two miss arms the resolve
// obligation implies: a slug naming no job, and a name naming no artifact of a
// job that does exist. Both answer 404 with an {"error":…} body.
func TestContractLibraryPOSTUnresolvableIs404(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	contractLibrarySeedJob(t, st, "resolvable", map[string]string{"present.md": "here"})

	for _, tc := range []struct {
		what       string
		slug, name string
	}{
		{"unknown job", "no-such-job", "present.md"},
		{"unknown artifact", "resolvable", "no-such-artifact.md"},
	} {
		t.Run(tc.what, func(t *testing.T) {
			rec := contractLibraryPOST(t, mux, "/api/library", map[string]any{
				"slug": tc.slug, "home": "pluto", "name": tc.name,
			})
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404; body %s", rec.Code, rec.Body.String())
			}
			body := contractLibraryDecode(t, rec)
			if msg, ok := body["error"].(string); !ok || msg == "" {
				t.Fatalf("body = %s, want {\"error\":…}", rec.Body.String())
			}
		})
	}
	// Nothing was written on either miss.
	if metas, err := st.ListSavedArtifacts(); err != nil || len(metas) != 0 {
		t.Fatalf("store rows after two 404s = %+v (%v), want none", metas, err)
	}
}

// ---------------------------------------------------------------------------
// POST /api/library/remove
// ---------------------------------------------------------------------------

// TestContractLibraryRemoveDeletesExactlyOneRow pins the remove: {"id":N} → 200
// {"ok":true}, the row leaves the list, its siblings stay, and a repeat is 404.
func TestContractLibraryRemoveDeletesExactlyOneRow(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	contractLibrarySeedJob(t, st, "removal", map[string]string{"a.md": "a", "b.md": "b"})

	a := contractLibrarySaveOK(t, mux, "removal", "pluto", "a.md")
	b := contractLibrarySaveOK(t, mux, "removal", "pluto", "b.md")

	rec := contractLibraryPOST(t, mux, "/api/library/remove", map[string]any{"id": a})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/library/remove: status %d, body %s", rec.Code, rec.Body.String())
	}
	body := contractLibraryDecode(t, rec)
	if ok, _ := body["ok"].(bool); !ok {
		t.Fatalf("remove body = %s, want {\"ok\":true}", rec.Body.String())
	}

	items := contractLibraryItems(t, mux)
	if len(items) != 1 {
		t.Fatalf("items after one remove = %#v, want exactly the surviving row", items)
	}
	contractLibraryItemByID(t, items, b)

	// The removed row is gone from the bytes route too.
	if rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(a, "a.md")); rec.Code != http.StatusNotFound {
		t.Fatalf("GET a removed row's bytes: status %d, want 404", rec.Code)
	}
	// A repeat has no such row.
	if rec := contractLibraryPOST(t, mux, "/api/library/remove", map[string]any{"id": a}); rec.Code != http.StatusNotFound {
		t.Fatalf("repeat remove: status %d, want 404 (no such row)", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// GET /api/library/artifact/<id>/<name>
// ---------------------------------------------------------------------------

// TestContractLibraryArtifactRouteTypesAndNoStore pins the bytes route's
// headers over the extensions whose mapping is not in dispute: the content
// type is decided by the extension of the STORED name, and every response
// carries Cache-Control: no-store.
func TestContractLibraryArtifactRouteTypesAndNoStore(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "types", nil)

	for _, name := range []string{"doc.md", "shot.png", "paper.pdf", "photo.jpg", "notes.txt"} {
		id, err := st.SaveArtifact(j.ID, "types", name, []byte("bytes of "+name), 10)
		if err != nil {
			t.Fatal(err)
		}
		rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(id, name))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body %s", name, rec.Code, rec.Body.String())
		}
		if got, want := rec.Header().Get("Content-Type"), artifactMimeType(name); got != want {
			t.Fatalf("%s Content-Type = %q, want %q (by extension of the stored name)", name, got, want)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", name, got)
		}
		if got := rec.Body.String(); got != "bytes of "+name {
			t.Fatalf("%s body = %q, want the snapshot bytes", name, got)
		}
	}
}

// TestContractLibraryArtifactRouteServesHTMLAndSVGByExtension pins the two
// extensions where the mapping is the contract's only ambiguity: it names
// `artifactMimeType` as the mapping, so .html serves as text/html and .svg as
// image/svg+xml — the live types, which is also what makes the web client's
// "row click opens the item in the existing ArtifactOverlay" obligation
// coherent. Split into its own test so a join can see exactly this half fail
// if the implementation declawed the two (the feedback-snapshot posture) rather
// than the whole header suite going red at once.
func TestContractLibraryArtifactRouteServesHTMLAndSVGByExtension(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "active-types", nil)

	for name, want := range map[string]string{
		"page.html":    "text/html; charset=utf-8",
		"diagram.svg":  "image/svg+xml",
		"nested/x.svg": "image/svg+xml",
	} {
		id, err := st.SaveArtifact(j.ID, "active-types", name, []byte("payload"), 10)
		if err != nil {
			t.Fatal(err)
		}
		rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(id, name))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: status %d, body %s", name, rec.Code, rec.Body.String())
		}
		if got := rec.Header().Get("Content-Type"); got != want {
			t.Fatalf("%s Content-Type = %q, want %q (the contract's stated artifactMimeType mapping)", name, got, want)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("%s Cache-Control = %q, want no-store", name, got)
		}
	}
}

// TestContractLibraryArtifactRouteLooksUpByID pins the clause that makes the
// url safe to build from a stored name: the <name> segment is display and
// extension only — the LOOKUP is by id. A request whose name segment disagrees
// with the row still serves the row, and the content type still comes from the
// STORED name.
func TestContractLibraryArtifactRouteLooksUpByID(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "byid", nil)

	id, err := st.SaveArtifact(j.ID, "byid", "real-name.png", []byte("the png bytes"), 10)
	if err != nil {
		t.Fatal(err)
	}
	rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(id, "something-else.md"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 — lookup is by id, the name segment is display only", rec.Code)
	}
	if got := rec.Body.String(); got != "the png bytes" {
		t.Fatalf("body = %q, want the row's snapshot", got)
	}
	if got, want := rec.Header().Get("Content-Type"), artifactMimeType("real-name.png"); got != want {
		t.Fatalf("Content-Type = %q, want %q — the type comes from the STORED name's extension", got, want)
	}
}

// TestContractLibraryArtifactRouteMissesAre404 pins the miss arms: an id naming
// no row and an id that is not a number both answer 404 (not 400, and not a
// zero-byte 200).
func TestContractLibraryArtifactRouteMissesAre404(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "misses", nil)
	live, err := st.SaveArtifact(j.ID, "misses", "a.md", []byte("a"), 10)
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		contractLibraryArtifactURL(live+1000, "a.md"),
		"/api/library/artifact/not-a-number/a.md",
		"/api/library/artifact/0/a.md",
	} {
		if rec := contractLibraryGET(t, mux, path); rec.Code != http.StatusNotFound {
			t.Fatalf("GET %s: status %d, want 404 (unknown/malformed id)", path, rec.Code)
		}
	}
	// The live row still serves, so the misses are about the id.
	if rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(live, "a.md")); rec.Code != http.StatusOK {
		t.Fatalf("the live row = %d, want 200", rec.Code)
	}
}

// ---------------------------------------------------------------------------
// mirror gate, method gate, contract number
// ---------------------------------------------------------------------------

// TestContractLibraryWritesAreMirrorGated pins the two POSTs' refusal under
// --mirror with the exact body every gated route in this package shares, and —
// in the same run — that the GET on the SAME path is not taken down with them.
// That pairing is the whole reason the contract puts the gate inline on the
// POST arms rather than wrapping the route at registration.
func TestContractLibraryWritesAreMirrorGated(t *testing.T) {
	st := useTestStore(t)
	j := contractLibrarySeedJob(t, st, "gated", map[string]string{"a.md": "a"})
	saved, err := st.SaveArtifact(j.ID, "gated", "a.md", []byte("a"), 10)
	if err != nil {
		t.Fatal(err)
	}

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	mux := contractLibraryMux(t)

	for path, body := range map[string]any{
		"/api/library":        map[string]any{"slug": "gated", "home": "pluto", "name": "a.md"},
		"/api/library/remove": map[string]any{"id": saved},
	} {
		rec := contractLibraryPOST(t, mux, path, body)
		if rec.Code != http.StatusForbidden {
			t.Fatalf("POST %s under --mirror: status %d, want 403", path, rec.Code)
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
			t.Fatalf("POST %s under --mirror: body = %q, want %q", path, got, "disabled in mirror mode")
		}
	}
	// The gate ran before anything was written.
	if metas, err := st.ListSavedArtifacts(); err != nil || len(metas) != 1 || metas[0].ID != saved {
		t.Fatalf("store rows after two refused writes = %+v (%v), want the one pre-seeded row", metas, err)
	}
	// ...and the read on the same path is untouched.
	if rec := contractLibraryGET(t, mux, "/api/library"); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library under --mirror: status %d — an inline POST gate must not take the GET down", rec.Code)
	}
}

// TestContractLibraryMethodNotAllowedIsJSON pins the jobload posture the
// contract names for the shared path: /api/library answers GET and POST, and
// any other method is a JSON 405 rather than the mux's own plain-text default.
func TestContractLibraryMethodNotAllowedIsJSON(t *testing.T) {
	useTestStore(t)
	mux := contractLibraryMux(t)

	for _, method := range []string{http.MethodDelete, http.MethodPut, http.MethodPatch} {
		req := httptest.NewRequest(method, "/api/library", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /api/library: status %d, want 405", method, rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s /api/library body %q is not JSON: %v", method, rec.Body.String(), err)
		}
		if msg, ok := body["error"].(string); !ok || msg == "" {
			t.Fatalf("%s /api/library body = %s, want a JSON {\"error\":…}", method, rec.Body.String())
		}
	}
}

// TestContractLibraryIsAdditiveToTheAPIContract pins the contract's "Additive:
// apiContract stays 6" clause. The whole library family is new keys on new
// routes; nothing existing is retyped, renamed or removed, so the number must
// not move — a bump would tell every shipped iOS build it is out of date over a
// feature it does not have.
func TestContractLibraryIsAdditiveToTheAPIContract(t *testing.T) {
	if apiContract != 6 {
		t.Fatalf("apiContract = %d, want 6 — the artifact library is additive", apiContract)
	}
}

// TestContractLibrarySurvivesTheJobsHardDelete walks the death march over the
// ROUTES, which is where carl actually meets it: save, hard-delete the source
// job, and the list still carries the item and its bytes route still serves the
// frozen snapshot.
func TestContractLibrarySurvivesTheJobsHardDelete(t *testing.T) {
	st := useTestStore(t)
	mux := contractLibraryMux(t)
	j := contractLibrarySeedJob(t, st, "death-march", map[string]string{"00-overview.html": "<h1>frozen</h1>"})

	id := contractLibrarySaveOK(t, mux, "death-march", "pluto", "00-overview.html")

	if err := st.DeleteJob(j.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	items := contractLibraryItems(t, mux)
	if len(items) != 1 {
		t.Fatalf("items after the source job was hard-deleted = %#v, want the saved row still listed", items)
	}
	obj := contractLibraryItemByID(t, items, id)
	if obj["name"] != "00-overview.html" || obj["slug"] != "death-march" {
		t.Fatalf("item = %#v, want the name and the display slug frozen at save time", obj)
	}
	if want := contractLibraryArtifactURL(id, "00-overview.html"); obj["url"] != want {
		t.Fatalf("url = %q, want %q", obj["url"], want)
	}

	rec := contractLibraryGET(t, mux, contractLibraryArtifactURL(id, "00-overview.html"))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET the snapshot after the hard delete: status %d, body %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<h1>frozen</h1>" {
		t.Fatalf("snapshot after the hard delete = %q, want the frozen bytes", got)
	}
}
