package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// library_edge_test.go — the EDGE dimension of 50-contract.md's `/api/library`
// route family: every refusal (405, 400, 404, 413, the mirror 403), every
// boundary of the bytes route, and the two headers that decide what a snapshot
// can do once a browser has it. Authored blind from the frozen contract, with no
// sight of internal/api/library.go or register.go.
//
// The sibling blind author (athena-logic) owns the happy shapes — a save
// round-trips, the list carries all five keys, the order is newest-first. What
// lives here is what the routes must SAY NO to, and what they must serve ANYWAY
// (a snapshot whose source artifact, and whose source job, are gone).
//
// Existing package helpers are CALLED, never redeclared: useTestStore
// (storemode_test.go), mustCreateStoreJob (jobedit_storemode_test.go), mustJSON
// (board_test.go), postJSON (mirror_test.go). useTestStore points HOME at a
// temp dir, which is what keeps every cell/pluto path this file touches inside
// t.TempDir() rather than in the real ~/.kraken.
//
// Every package-level identifier is prefixed `edgeLib` — this package's test
// namespace is shared with the sibling blind file written in the same round.

// ---------------------------------------------------------------------------
// wire shapes (decoded locally, so a failure names the bytes that failed)
// ---------------------------------------------------------------------------

type edgeLibItem struct {
	ID      int64  `json:"id"`
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	SavedAt int64  `json:"saved_at"`
	URL     string `json:"url"`
}

type edgeLibPage struct {
	Items []edgeLibItem `json:"items"`
}

type edgeLibSaveResp struct {
	OK bool  `json:"ok"`
	ID int64 `json:"id"`
}

// ---------------------------------------------------------------------------
// request helpers
// ---------------------------------------------------------------------------

// edgeLibMux builds the real mux the way web.Serve does, so every assertion
// below covers the WIRING (which path is registered, which arm is gated) rather
// than a handler in isolation.
func edgeLibMux() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func edgeLibGet(mux http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func edgeLibDo(mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(body)))
	return rec
}

// edgeLibPost posts a raw JSON string — raw rather than a struct because most of
// the cases here are bodies no Go type of ours can express (an unknown key, a
// truncated object, a nested shape).
func edgeLibPost(mux http.Handler, path, body string) *httptest.ResponseRecorder {
	return edgeLibDo(mux, http.MethodPost, path, body)
}

// edgeLibErr pulls the {"error": "..."} sentence out of a refusal, failing the
// test if the refusal is not an error object at all — a 4xx with an empty or
// non-JSON body renders as nothing in the board's clock corner.
func edgeLibErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var e struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("response is not an {\"error\":...} object: %v (body %q)", err, rec.Body.String())
	}
	if strings.TrimSpace(e.Error) == "" {
		t.Errorf("refusal carried no error sentence (body %q)", rec.Body.String())
	}
	return e.Error
}

// edgeLibSeedJob creates a job with one artifact row, the ordinary shape a save
// addresses: a done job whose artifacts live in the store.
func edgeLibSeedJob(t *testing.T, st *store.Store, slug, name string, body []byte) *jobs.Job {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: name, Bytes: body, Mtime: 1}}, nil); err != nil {
		t.Fatalf("SyncArtifacts(%s): %v", slug, err)
	}
	return j
}

// edgeLibSaveBody is the one legal request shape, as a raw JSON string.
func edgeLibSaveBody(slug, home, name string) string {
	b, _ := json.Marshal(map[string]string{"slug": slug, "home": home, "name": name})
	return string(b)
}

// edgeLibMustSave posts a save that is expected to succeed and returns the row
// id, which the bytes route's path is then built from.
func edgeLibMustSave(t *testing.T, mux http.Handler, slug, home, name string) int64 {
	t.Helper()
	rec := edgeLibPost(mux, "/api/library", edgeLibSaveBody(slug, home, name))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/library (%s/%s/%s): status = %d, want 200 (body %s)", slug, home, name, rec.Code, rec.Body.String())
	}
	var got edgeLibSaveResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode save response: %v (body %s)", err, rec.Body.String())
	}
	if !got.OK {
		t.Fatalf("save response ok = false: %s", rec.Body.String())
	}
	if got.ID <= 0 {
		t.Fatalf("save response id = %d, want the row id: %s", got.ID, rec.Body.String())
	}
	return got.ID
}

func edgeLibListItems(t *testing.T, mux http.Handler) []edgeLibItem {
	t.Helper()
	rec := edgeLibGet(mux, "/api/library")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/library: status = %d (body %s)", rec.Code, rec.Body.String())
	}
	var page edgeLibPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode library page: %v (body %s)", err, rec.Body.String())
	}
	return page.Items
}

// ---------------------------------------------------------------------------
// method refusals
// ---------------------------------------------------------------------------

// Obligation: "`/api/library` exact (GET+POST on one path, 405 JSON otherwise —
// the jobload posture, internal/api/jobload.go:96-109)". jobload's default arm
// is `webJSON(w, 405, {"error": "GET or POST only"})`, so the sentence is named
// by the contract's own citation rather than derived.
//
// A verb this route does not serve must be a 405 and not a fall-through into the
// GET: a DELETE that listed the library would read as a successful deletion.
func TestEdgeLibRefusesUnsupportedMethodsOnTheListRoute(t *testing.T) {
	useTestStore(t)
	mux := edgeLibMux()

	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch, http.MethodHead} {
		t.Run(method, func(t *testing.T) {
			rec := edgeLibDo(mux, method, "/api/library", "")
			if rec.Code != http.StatusMethodNotAllowed {
				t.Fatalf("status = %d, want 405 (body %s)", rec.Code, rec.Body.String())
			}
			// HEAD carries no body by definition, so the sentence is only
			// asserted for the verbs that can show one.
			if method == http.MethodHead {
				return
			}
			if got := edgeLibErr(t, rec); got != "GET or POST only" {
				t.Errorf("error = %q, want %q (the jobload posture the contract cites)", got, "GET or POST only")
			}
		})
	}
}

// Obligation, derived: `/api/library/remove` is stated as a POST route only, and
// the bytes route as a GET only. The contract does not spell either 405
// SENTENCE, so the STATUS is what is asserted strictly here and the body is only
// required to be a non-empty error object — the sentence itself is disclosed as
// a silence rather than guessed at.
func TestEdgeLibRefusesUnsupportedMethodsOnRemoveAndBytes(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "verbs", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()
	id := edgeLibMustSave(t, mux, "verbs", "pluto", "00-overview.md")
	bytesPath := fmt.Sprintf("/api/library/artifact/%d/00-overview.md", id)

	for _, c := range []struct {
		path    string
		methods []string
	}{
		{"/api/library/remove", []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch}},
		{bytesPath, []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch}},
	} {
		for _, method := range c.methods {
			t.Run(c.path+"/"+method, func(t *testing.T) {
				rec := edgeLibDo(mux, method, c.path, `{"id":1}`)
				if rec.Code != http.StatusMethodNotAllowed {
					t.Fatalf("status = %d, want 405 (body %s)", rec.Code, rec.Body.String())
				}
				edgeLibErr(t, rec)
			})
		}
	}
}

// ---------------------------------------------------------------------------
// POST /api/library — the 400 arms
// ---------------------------------------------------------------------------

// Obligation: "Unknown field → 400", enforced by `DisallowUnknownFields on both
// POST bodies`.
//
// The keys probed are the ones a real client would send by mistake, and each is
// a body that means something ELSE: a server-stamped field (`id`, `saved_at`,
// `url`) that the request has no business choosing, and a misspelling that would
// otherwise decode to a request with that field empty — which for `name` is a
// different artifact entirely.
func TestEdgeLibSaveRefusesUnknownFields(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "unknown-field", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()

	for _, body := range []string{
		`{"slug":"unknown-field","home":"pluto","name":"00-overview.md","id":7}`,
		`{"slug":"unknown-field","home":"pluto","name":"00-overview.md","saved_at":1770000000000}`,
		`{"slug":"unknown-field","home":"pluto","name":"00-overview.md","url":"/api/library/artifact/1/x.md"}`,
		`{"slug":"unknown-field","home":"pluto","name":"00-overview.md","snapshot":"bytes"}`,
		`{"slug":"unknown-field","home":"pluto","nme":"00-overview.md"}`,
		`{"artifact":{"slug":"unknown-field","home":"pluto","name":"00-overview.md"}}`,
	} {
		rec := edgeLibPost(mux, "/api/library", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
			continue
		}
		edgeLibErr(t, rec)
	}

	// A refused request must not have written anything on its way out.
	if items := edgeLibListItems(t, mux); len(items) != 0 {
		t.Errorf("a refused save wrote a row: %+v", items)
	}
}

// Obligation: "Missing/empty `slug`, `home`, or `name` → 400 naming the field."
//
// One field is withheld at a time, with the other two valid, so "naming the
// field" is a well-defined assertion: the sentence must contain the name of the
// key that is actually missing. A refusal that says only "bad request" leaves a
// client with three things to try.
func TestEdgeLibSaveRefusesMissingOrEmptyFieldsByName(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "half-request", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()

	for _, c := range []struct {
		field string
		body  string
	}{
		{"slug", `{"home":"pluto","name":"00-overview.md"}`},
		{"slug", `{"slug":"","home":"pluto","name":"00-overview.md"}`},
		{"home", `{"slug":"half-request","name":"00-overview.md"}`},
		{"home", `{"slug":"half-request","home":"","name":"00-overview.md"}`},
		{"name", `{"slug":"half-request","home":"pluto"}`},
		{"name", `{"slug":"half-request","home":"pluto","name":""}`},
	} {
		t.Run(c.field+" "+c.body, func(t *testing.T) {
			rec := edgeLibPost(mux, "/api/library", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (body %s)", rec.Code, rec.Body.String())
			}
			if got := edgeLibErr(t, rec); !strings.Contains(got, c.field) {
				t.Errorf("error = %q, want it to name the missing field %q", got, c.field)
			}
		})
	}

	// The empty body and a bare `null` are the same rule read one level up: a
	// request naming NONE of the three is not a smaller request, it is an
	// unshaped one.
	for _, body := range []string{`{}`, `null`} {
		rec := edgeLibPost(mux, "/api/library", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// Obligation, from the house pattern every POST in this package shares
// (jobload.go, feedback.go): a body that is not JSON at all is an ordinary 400,
// distinct from the 413 below.
func TestEdgeLibSaveRefusesMalformedJSON(t *testing.T) {
	useTestStore(t)
	mux := edgeLibMux()

	for _, body := range []string{
		``,
		`{`,
		`{"slug":"x","home":"pluto","name":}`,
		`"a string, not an object"`,
		`[{"slug":"x","home":"pluto","name":"y.md"}]`,
		`{"slug":5,"home":"pluto","name":"y.md"}`,
	} {
		rec := edgeLibPost(mux, "/api/library", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %q: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// Obligation: "Body cap 1 MB (413 distinct from 400)."
//
// Distinct is the whole assertion. Both are refusals, but they mean opposite
// things to a caller: 400 says the request was wrong and retrying it unchanged
// is pointless, 413 says the request was too big and a smaller one would work.
// Collapsing them (the shape you get by simply not checking for
// http.MaxBytesError) sends every oversize save into the wrong branch.
func TestEdgeLibSaveOversizeBodyIs413NotA400(t *testing.T) {
	useTestStore(t)
	mux := edgeLibMux()

	huge := `{"slug":"big","home":"pluto","name":"` + strings.Repeat("a", 2<<20) + `.md"}`
	rec := edgeLibPost(mux, "/api/library", huge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
	}
	edgeLibErr(t, rec)

	// The same cap on the remove route, whose whole legitimate body is one
	// integer.
	rec = edgeLibPost(mux, "/api/library/remove", `{"id":1,"pad":"`+strings.Repeat("b", 2<<20)+`"}`)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("remove: status = %d, want 413 (body %s)", rec.Code, rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /api/library — the 404 arms
// ---------------------------------------------------------------------------

// Obligation: "Unresolvable job or artifact → 404 {"error":…}", and "`home ==
// "archive"` resolves in the archived-jobs namespace, anything else in active
// jobs".
//
// The namespace half is the edge: a slug is unique only among ACTIVE jobs, so an
// active job addressed with home "archive" (and an archived one addressed with
// "pluto") must MISS rather than fall through to the other namespace — a
// fall-through would let a save freeze a different generation's bytes under the
// caller's name.
func TestEdgeLibSave404sAnUnresolvableJob(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "active-only", "00-overview.md", []byte("# active"))

	archivedSeed := edgeLibSeedJob(t, st, "to-be-archived", "00-overview.md", []byte("# archived"))
	archivedName, err := st.ArchiveJob(archivedSeed.ID)
	if err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	mux := edgeLibMux()
	for _, c := range []struct{ name, body string }{
		{"no such slug", edgeLibSaveBody("nonexistent", "pluto", "00-overview.md")},
		{"active job addressed in the archive namespace", edgeLibSaveBody("active-only", "archive", "00-overview.md")},
		{"archived job addressed in the active namespace", edgeLibSaveBody(archivedName, "pluto", "00-overview.md")},
		{"slug that is a substring of a real one", edgeLibSaveBody("active", "pluto", "00-overview.md")},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeLibPost(mux, "/api/library", c.body)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
			}
			edgeLibErr(t, rec)
		})
	}

	if items := edgeLibListItems(t, mux); len(items) != 0 {
		t.Errorf("a 404'd save wrote a row: %+v", items)
	}
}

// Obligation: the same 404, reached with a job that DOES resolve and a name that
// does not. The traversal and job-suffix shapes are included because they are
// what `resolveArtifact` (via ArtifactBytes/ArtifactPath) refuses rather than
// merely misses — a save that resolved either one would freeze bytes from
// outside the artifact set into a permanently readable row.
func TestEdgeLibSave404sAnUnresolvableArtifact(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "real-job", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()

	for _, name := range []string{
		"missing.md",
		"00-overview.MD", // artifact names are matched exactly, not case-folded
		"../../etc/passwd",
		"nested/../00-overview.md",
		"real-job.job.md",
	} {
		t.Run(name, func(t *testing.T) {
			rec := edgeLibPost(mux, "/api/library", edgeLibSaveBody("real-job", "pluto", name))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
			}
			edgeLibErr(t, rec)
		})
	}

	if items := edgeLibListItems(t, mux); len(items) != 0 {
		t.Errorf("a 404'd save wrote a row: %+v", items)
	}
}

// ---------------------------------------------------------------------------
// POST /api/library/remove
// ---------------------------------------------------------------------------

// Obligation: "`id` ≤ 0 or absent → 400. No such row → 404. Unknown field →
// 400."
//
// The 400/404 split is the load-bearing part: an id of 0 is a client bug (an
// absent key decoding to the zero value), while an id of 987654 is a row that is
// simply not there — and the board renders those two differently, one as "this
// build is broken" and one as "someone else removed it".
func TestEdgeLibRemoveRefusesBadIDs(t *testing.T) {
	useTestStore(t)
	mux := edgeLibMux()

	for _, body := range []string{
		`{}`,
		`{"id":0}`,
		`{"id":-1}`,
		`{"id":-987654}`,
		`null`,
	} {
		rec := edgeLibPost(mux, "/api/library/remove", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
			continue
		}
		edgeLibErr(t, rec)
	}

	for _, body := range []string{
		`{"id":1,"slug":"x"}`,
		`{"id":1,"name":"00-overview.md"}`,
		`{"ids":[1]}`,
		`{"Id ":1}`,
	} {
		rec := edgeLibPost(mux, "/api/library/remove", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("unknown field %s: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
	}

	for _, body := range []string{``, `{`, `"1"`} {
		rec := edgeLibPost(mux, "/api/library/remove", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("malformed %q: status = %d, want 400 (body %s)", body, rec.Code, rec.Body.String())
		}
	}
}

// Obligation: "No such row → 404", including the row that existed a moment ago.
// A remove that answered 200 for a row it did not delete would let the board
// drop a card that is still in the library.
func TestEdgeLibRemove404sAnUnknownRow(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "removable", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()

	rec := edgeLibPost(mux, "/api/library/remove", `{"id":987654}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	edgeLibErr(t, rec)

	id := edgeLibMustSave(t, mux, "removable", "pluto", "00-overview.md")
	if rec := edgeLibPost(mux, "/api/library/remove", fmt.Sprintf(`{"id":%d}`, id)); rec.Code != http.StatusOK {
		t.Fatalf("first remove: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	if rec := edgeLibPost(mux, "/api/library/remove", fmt.Sprintf(`{"id":%d}`, id)); rec.Code != http.StatusNotFound {
		t.Fatalf("second remove of the same id: status = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
	if items := edgeLibListItems(t, mux); len(items) != 0 {
		t.Errorf("list after remove = %+v, want empty", items)
	}
}

// ---------------------------------------------------------------------------
// GET /api/library/artifact/<id>/<name>
// ---------------------------------------------------------------------------

// Obligation: "Unknown/malformed id → 404. The `<name>` path segment is
// display/extension only — lookup is by id."
//
// The handler is driven DIRECTLY here rather than through the mux, on purpose:
// http.ServeMux cleans request paths, so a probe like `/api/library/artifact//x`
// would be answered with a 301 redirect by the mux before the handler ever saw
// it, and the redirect would mask whatever the route actually does with an empty
// id segment.
func TestEdgeLibBytesRouteRefusesMalformedIDs(t *testing.T) {
	useTestStore(t)

	for _, path := range []string{
		"/api/library/artifact/abc/x.md",
		"/api/library/artifact/0/x.md",
		"/api/library/artifact/-1/x.md",
		"/api/library/artifact/1.5/x.md",
		"/api/library/artifact/1e3/x.md",
		"/api/library/artifact/99999999999999999999/x.md",
		"/api/library/artifact//x.md",
		"/api/library/artifact/",
		"/api/library/artifact/7",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			libraryArtifactHandler(rec, httptest.NewRequest(http.MethodGet, path, nil))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
			}
		})
	}
}

// Obligation: the same 404 for an id that is well-formed but names no row —
// including the id of a row that has just been removed. A stale url on a phone
// screen is the ordinary way this is reached.
func TestEdgeLibBytesRoute404sAnUnknownRow(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "gone-row", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()

	if rec := edgeLibGet(mux, "/api/library/artifact/987654/00-overview.md"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown id: status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}

	id := edgeLibMustSave(t, mux, "gone-row", "pluto", "00-overview.md")
	path := fmt.Sprintf("/api/library/artifact/%d/00-overview.md", id)
	if rec := edgeLibGet(mux, path); rec.Code != http.StatusOK {
		t.Fatalf("pre-condition: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if rec := edgeLibPost(mux, "/api/library/remove", fmt.Sprintf(`{"id":%d}`, id)); rec.Code != http.StatusOK {
		t.Fatalf("remove: status = %d (body %s)", rec.Code, rec.Body.String())
	}
	if rec := edgeLibGet(mux, path); rec.Code != http.StatusNotFound {
		t.Fatalf("after remove: status = %d, want 404 (body %q)", rec.Code, rec.Body.String())
	}
}

// Obligation: "The `<name>` path segment is display/extension only — lookup is by
// id."
//
// So a WRONG name must still serve the row's bytes: the name in the url is
// whatever the client last saw, and a lookup that keyed on it too would 404 the
// moment a stored name and a url drifted apart. What the name DOES decide is the
// content type, which is asserted separately below.
func TestEdgeLibBytesRouteIgnoresTheNameSegmentForLookup(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "name-is-cosmetic", "00-overview.md", []byte("# the frozen bytes"))
	mux := edgeLibMux()
	id := edgeLibMustSave(t, mux, "name-is-cosmetic", "pluto", "00-overview.md")

	for _, name := range []string{"00-overview.md", "something-else.md", "renamed.md"} {
		rec := edgeLibGet(mux, fmt.Sprintf("/api/library/artifact/%d/%s", id, name))
		if rec.Code != http.StatusOK {
			t.Fatalf("name %q: status = %d, want 200 — the lookup is by id (body %q)", name, rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != "# the frozen bytes" {
			t.Errorf("name %q: body = %q, want the frozen snapshot", name, got)
		}
	}
}

// Obligation: "`url` is `/api/library/artifact/<id>/<name>` with `name` exactly
// as stored (slashes intact)."
//
// The nested name is the case the whole clause exists for: the served url has to
// be fetchable as-is, which means the extra slashes are path segments the route
// tolerates rather than something to escape or flatten. Both clients open an
// item by handing this string straight to a fetch.
func TestEdgeLibURLKeepsANestedNamesSlashesAndStaysFetchable(t *testing.T) {
	st := useTestStore(t)
	const nested = "icons/icon-variations/v2.svg"
	edgeLibSeedJob(t, st, "nested-name", nested, []byte("<svg/>"))
	mux := edgeLibMux()

	id := edgeLibMustSave(t, mux, "nested-name", "pluto", nested)

	items := edgeLibListItems(t, mux)
	if len(items) != 1 {
		t.Fatalf("want 1 item, got %+v", items)
	}
	want := fmt.Sprintf("/api/library/artifact/%d/%s", id, nested)
	if items[0].URL != want {
		t.Fatalf("url = %q, want %q (the name's slashes stay intact)", items[0].URL, want)
	}
	if items[0].Name != nested {
		t.Errorf("name = %q, want the store spelling %q", items[0].Name, nested)
	}

	rec := edgeLibGet(mux, items[0].URL)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s: status = %d, want 200 — the served url must be fetchable as served (body %q)", items[0].URL, rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "<svg/>" {
		t.Errorf("body = %q, want the frozen bytes", got)
	}
}

// Obligation: "`Cache-Control: no-store`."
//
// A saved snapshot is re-frozen in place under the same id on every re-save, so
// its url is stable while its bytes are not. Anything cacheable would serve the
// previous snapshot after a re-save with no way for the client to tell.
func TestEdgeLibBytesRouteIsNoStore(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "no-store", "00-overview.md", []byte("# v1"))
	mux := edgeLibMux()
	id := edgeLibMustSave(t, mux, "no-store", "pluto", "00-overview.md")

	rec := edgeLibGet(mux, fmt.Sprintf("/api/library/artifact/%d/00-overview.md", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want %q", got, "no-store")
	}
}

// Obligation: "content type by extension of the stored name (same mapping family
// as `artifactMimeType`)."
//
// These four extensions are the ones both candidate mappings agree on
// (artifactMimeType and feedback.go's inertSnapshotMimeType differ ONLY on
// text/html and image/svg+xml), so this case pins the mapping without taking a
// side on the fork — which is asserted alone, in the test below.
func TestEdgeLibBytesRouteTypesByTheStoredExtension(t *testing.T) {
	st := useTestStore(t)
	mux := edgeLibMux()

	for _, c := range []struct{ name, want string }{
		{"00-overview.md", "text/plain; charset=utf-8"},
		{"diagram.png", "image/png"},
		{"scan.pdf", "application/pdf"},
		{"photo.jpg", "image/jpeg"},
		{"noextension", "text/plain; charset=utf-8"},
	} {
		t.Run(c.name, func(t *testing.T) {
			slug := "mime-" + strings.ReplaceAll(c.name, ".", "-")
			edgeLibSeedJob(t, st, slug, c.name, []byte("bytes for "+c.name))
			id := edgeLibMustSave(t, mux, slug, "pluto", c.name)

			rec := edgeLibGet(mux, fmt.Sprintf("/api/library/artifact/%d/%s", id, c.name))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != c.want {
				t.Errorf("Content-Type = %q, want %q", got, c.want)
			}
		})
	}
}

// The fork, isolated: the contract names `artifactMimeType` as the mapping, and
// that mapping serves .html as text/html and .svg as image/svg+xml. The package
// also holds `inertSnapshotMimeType` — artifactMimeType with exactly those two
// declawed to text/plain — which is what the OTHER frozen-snapshot route
// (feedback.go's) serves, and which 52-testing-plan.md left as an open design
// choice. This file asserts the contract's named symbol; the disagreement, if
// there is one, surfaces here and only here.
func TestEdgeLibBytesRouteTypesActiveDocumentsPerTheNamedMapping(t *testing.T) {
	st := useTestStore(t)
	mux := edgeLibMux()

	for _, c := range []struct{ name, want string }{
		{"10-architecture.html", "text/html; charset=utf-8"},
		{"20-flow.svg", "image/svg+xml"},
	} {
		t.Run(c.name, func(t *testing.T) {
			slug := "active-" + strings.ReplaceAll(c.name, ".", "-")
			edgeLibSeedJob(t, st, slug, c.name, []byte("<!-- "+c.name+" -->"))
			id := edgeLibMustSave(t, mux, slug, "pluto", c.name)

			rec := edgeLibGet(mux, fmt.Sprintf("/api/library/artifact/%d/%s", id, c.name))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d (body %q)", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Content-Type"); got != c.want {
				t.Errorf("Content-Type = %q, want %q (artifactMimeType, the mapping the contract names)", got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// frozen-ness: what the bytes route serves once the source is gone
// ---------------------------------------------------------------------------

// Obligation: "Serves the FROZEN snapshot (never the live artifact)", and the
// store's survival rule one layer down.
//
// The edge is the source DISAPPEARING rather than merely changing: the artifact
// row is deleted out from under the save, then the job itself is hard-deleted.
// Either one turning the bytes route into a 404 would make the library a list of
// links to things that no longer open — which is the one thing saving is for.
func TestEdgeLibBytesSurviveTheSourceArtifactAndTheSourceJob(t *testing.T) {
	st := useTestStore(t)
	j := edgeLibSeedJob(t, st, "vanishing", "00-overview.md", []byte("# frozen at save time"))
	mux := edgeLibMux()
	id := edgeLibMustSave(t, mux, "vanishing", "pluto", "00-overview.md")
	path := fmt.Sprintf("/api/library/artifact/%d/00-overview.md", id)

	// 1. the artifact row goes (a sync that no longer lists it)
	if err := st.SyncArtifacts(j.ID, nil, nil); err != nil {
		t.Fatalf("SyncArtifacts(empty): %v", err)
	}
	rec := edgeLibGet(mux, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("after the artifact row went: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "# frozen at save time" {
		t.Errorf("body = %q, want the frozen bytes", got)
	}

	// 2. the job itself goes
	if err := st.DeleteJob(j.ID); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}
	rec = edgeLibGet(mux, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("after the job's hard delete: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "# frozen at save time" {
		t.Errorf("body = %q, want the frozen bytes", got)
	}

	// 3. and the item is still LISTED, with its slug frozen at save time
	items := edgeLibListItems(t, mux)
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("list after the source job's hard delete = %+v, want the saved item", items)
	}
	if items[0].Slug != "vanishing" {
		t.Errorf("slug = %q, want the display slug frozen at save time", items[0].Slug)
	}
}

// Obligation: "freezes the artifact's CURRENT bytes", with home "cell" being the
// live-job spelling (anything that is not "archive" resolves in active jobs, and
// resolveArtifact reads the cell dir for that home).
//
// The edge is that the freeze must be a COPY: rewriting the cell file after the
// save, and then deleting it outright, must leave the served bytes exactly as
// they were at save time.
func TestEdgeLibSaveFromACellFileFreezesACopy(t *testing.T) {
	st := useTestStore(t) // sandboxes HOME, so the cell dir lands under t.TempDir()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "live-cell", Repo: "kraken", State: "working"})

	root := jobs.CellArtifacts(j)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "00-overview.md")
	if err := os.WriteFile(file, []byte("# as it was at save time"), 0o644); err != nil {
		t.Fatal(err)
	}

	mux := edgeLibMux()
	id := edgeLibMustSave(t, mux, "live-cell", "cell", "00-overview.md")
	path := fmt.Sprintf("/api/library/artifact/%d/00-overview.md", id)

	if err := os.WriteFile(file, []byte("# rewritten by the agent an hour later"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := edgeLibGet(mux, path); rec.Body.String() != "# as it was at save time" {
		t.Errorf("after a rewrite the route served %q, want the frozen bytes", rec.Body.String())
	}
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	rec := edgeLibGet(mux, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("after the cell file was deleted: status = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "# as it was at save time" {
		t.Errorf("body = %q, want the frozen bytes", got)
	}
}

// Obligation: "Upsert by `(job_id, name)`: re-save replaces the snapshot and
// `saved_at`, keeps the same row `id`, never duplicates", asserted at the ROUTE
// (the store-level version of this rule lives in the sibling store file).
//
// The stable id is what keeps the served url valid across a re-save; the
// no-duplicate half is what keeps one artifact from occupying two rows in a list
// carl reads top to bottom.
func TestEdgeLibResaveThroughTheRouteKeepsOneRow(t *testing.T) {
	st := useTestStore(t)
	j := edgeLibSeedJob(t, st, "resave-route", "00-overview.md", []byte("# v1"))
	mux := edgeLibMux()

	first := edgeLibMustSave(t, mux, "resave-route", "pluto", "00-overview.md")
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "00-overview.md", Bytes: []byte("# v2"), Mtime: 2}}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	second := edgeLibMustSave(t, mux, "resave-route", "pluto", "00-overview.md")

	if second != first {
		t.Fatalf("re-save returned id %d, want the original %d — the url embeds it", second, first)
	}
	items := edgeLibListItems(t, mux)
	if len(items) != 1 {
		t.Fatalf("re-save left %d items, want 1: %+v", len(items), items)
	}
	rec := edgeLibGet(mux, fmt.Sprintf("/api/library/artifact/%d/00-overview.md", first))
	if got := rec.Body.String(); got != "# v2" {
		t.Errorf("body = %q, want the RE-frozen bytes", got)
	}
}

// ---------------------------------------------------------------------------
// the list's own edges
// ---------------------------------------------------------------------------

// Obligation: "`items` always an array, never null."
//
// Asserted on the RAW bytes rather than through a decode, because a Go decode of
// `null` into a slice and of `[]` into a slice are indistinguishable afterwards —
// and it is the wire that both clients branch on. A nil slice marshals to `null`,
// which is exactly the shape an unallocated result produces.
func TestEdgeLibEmptyListIsAnArrayNotNull(t *testing.T) {
	useTestStore(t)
	mux := edgeLibMux()

	rec := edgeLibGet(mux, "/api/library")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	items, ok := raw["items"]
	if !ok {
		t.Fatalf("the response has no `items` key at all: %s", rec.Body.String())
	}
	if string(items) != "[]" {
		t.Errorf("items = %s, want [] — an empty library is an empty array, never null", items)
	}
}

// Obligation: "All five keys always present (no omitempty)."
//
// The edge shape is a row whose every value is the zero one a client could
// mistake for absence — here the `saved_at` of a save stamped at the epoch is not
// reachable through the route, so what is asserted is the weaker but exact
// version: every key is present on a real row, checked against the raw object
// rather than a struct (a struct decode cannot tell an absent key from a zero
// value, which is the whole failure mode).
func TestEdgeLibListItemCarriesAllFiveKeys(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "five-keys", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()
	edgeLibMustSave(t, mux, "five-keys", "pluto", "00-overview.md")

	rec := edgeLibGet(mux, "/api/library")
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v (body %s)", err, rec.Body.String())
	}
	if len(raw.Items) != 1 {
		t.Fatalf("want 1 item, got %d: %s", len(raw.Items), rec.Body.String())
	}
	for _, key := range []string{"id", "slug", "name", "saved_at", "url"} {
		v, ok := raw.Items[0][key]
		if !ok {
			t.Errorf("item is missing the %q key: %s", key, rec.Body.String())
			continue
		}
		if string(v) == "null" {
			t.Errorf("item %q = null, want a value of its own type", key)
		}
	}
}

// ---------------------------------------------------------------------------
// mirror mode
// ---------------------------------------------------------------------------

// Obligation: "In `--mirror` mode → 403 with the exact body every gated route
// shares (see internal/api/mirror_test.go)" for both POSTs, and "Open under
// `--mirror`" for the list — with the bytes route a read like any other.
//
// The gate must sit AHEAD of the body decode, exactly as every other inline gate
// does, so an empty or invalid body cannot change the answer: a mirror board
// must refuse identically whatever it is asked to write.
func TestEdgeLibMirrorModeGatesBothPostsAndLeavesTheReadsOpen(t *testing.T) {
	st := useTestStore(t)
	edgeLibSeedJob(t, st, "mirrored", "00-overview.md", []byte("# overview"))
	mux := edgeLibMux()

	// Seed one row with mirror mode OFF, so the reads below have something real
	// to serve.
	id := edgeLibMustSave(t, mux, "mirrored", "pluto", "00-overview.md")

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	for _, c := range []struct{ path, body string }{
		{"/api/library", edgeLibSaveBody("mirrored", "pluto", "00-overview.md")},
		{"/api/library", ``},
		{"/api/library", `{`},
		{"/api/library/remove", fmt.Sprintf(`{"id":%d}`, id)},
		{"/api/library/remove", ``},
		{"/api/library/remove", `{"nonsense":true}`},
	} {
		rec := edgeLibPost(mux, c.path, c.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("POST %s (body %q): status = %d, want 403 (body %s)", c.path, c.body, rec.Code, rec.Body.String())
			continue
		}
		if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
			t.Errorf("POST %s: body = %q, want the exact shared refusal %q", c.path, got, "disabled in mirror mode")
		}
	}

	// The write never landed, which is the point of gating ahead of the decode.
	if items := edgeLibListItems(t, mux); len(items) != 1 || items[0].ID != id {
		t.Errorf("mirror-mode POSTs changed the library: %+v", items)
	}

	for _, path := range []string{
		"/api/library",
		fmt.Sprintf("/api/library/artifact/%d/00-overview.md", id),
	} {
		rec := edgeLibGet(mux, path)
		if rec.Code == http.StatusForbidden {
			t.Errorf("GET %s: 403 under mirror mode — a read must stay open", path)
			continue
		}
		if strings.Contains(rec.Body.String(), "disabled in mirror mode") {
			t.Errorf("GET %s: body carries the mirror refusal: %s", path, rec.Body.String())
		}
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status = %d, want 200 (body %q)", path, rec.Code, rec.Body.String())
		}
	}
}

// Obligation: "Additive: `apiContract` stays 6; no existing key retyped/renamed/
// removed."
//
// A bump would make every installed iOS build refuse the whole board over a
// feature it does not have. The number tracked here is whatever the contract
// currently is — this assertion moves only when a DIFFERENT, genuinely breaking
// change moves it, and never on the library's account.
func TestEdgeLibDoesNotBumpTheAPIContract(t *testing.T) {
	if apiContract != 7 {
		t.Errorf("apiContract = %d, want 7 (bumped by the fleet gate) — the library routes are additive", apiContract)
	}
}
