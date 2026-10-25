package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// mindrun_edge_test.go — the EDGE dimension of 50-contract.md slice D (the
// mark, marks, runs, run and replay routes, the capture alias, the retired
// mind thread role). Written blind from the frozen contract: mm.go,
// register.go and threads.go were never read in their edited form. Every
// request goes through the REAL mux (Register). Refusals and boundaries only;
// the happy paths are athena-logic's.
//
// Every package-level identifier is prefixed `edgeMR`.

func edgeMRStore(t *testing.T) *store.Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_DB_PATH", "")
	t.Setenv("KRAKEN_NODES", "off")
	st := useTestStore(t)
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	return st
}

func edgeMRMux() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func edgeMRDo(t *testing.T, mux http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func edgeMRObj(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not a JSON object (%d): %q", rec.Code, rec.Body.String())
	}
	return m
}

func edgeMRMarks(t *testing.T, st *store.Store) []store.MMMark {
	t.Helper()
	all, err := st.ListMMMarks(0, false)
	if err != nil {
		t.Fatal(err)
	}
	return all
}

func edgeMRJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

const edgeMRSlug = "edge-mr-draft"

func edgeMRDraft(t *testing.T, st *store.Store) int64 {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: edgeMRSlug, Repo: "kraken", Body: "b", State: ""}) // never started: the store spells draft ""
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// edgeMRRun enqueues one daily run and, when withBlobs, stores blobs for it.
func edgeMRRun(t *testing.T, st *store.Store, withBlobs bool) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	id, err := st.EnqueueMindRun(store.MindRun{Trigger: "daily", Since: now - 3_600_000, Until: now, CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	if withBlobs {
		if err := st.SetMindRunBlobs(id, []byte("not-really-gzip"), `{"sessions":[]}`); err != nil {
			t.Fatal(err)
		}
		if err := st.SetMindRunAnswer(id, `{"ops":[],"excluded":[],"coverage":{}}`); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// ---------------------------------------------------------------------------
// POST /api/mm/mark (and its /api/mm/capture alias)
// ---------------------------------------------------------------------------

// "400 on empty text, too-long text, bad source; 404 when `slug` names no
// job" — on both the route and its alias, storing nothing.
func TestEdgeMRMarkRouteRefusals(t *testing.T) {
	st := edgeMRStore(t)
	edgeMRDraft(t, st)
	mux := edgeMRMux()
	manual := map[string]any{"kind": "manual"}
	cases := []struct {
		name string
		body map[string]any
		code int
	}{
		{"empty text", map[string]any{"slug": edgeMRSlug, "text": "", "source": manual}, http.StatusBadRequest},
		{"too long", map[string]any{"slug": edgeMRSlug, "text": strings.Repeat("é", 32001), "source": manual}, http.StatusBadRequest},
		{"bad source", map[string]any{"slug": edgeMRSlug, "text": "words", "source": map[string]any{"kind": "bogus"}}, http.StatusBadRequest},
		{"unknown slug", map[string]any{"slug": "no-such-job", "text": "words", "source": manual}, http.StatusNotFound},
	}
	for _, path := range []string{"/api/mm/mark", "/api/mm/capture"} {
		for _, c := range cases {
			rec := edgeMRDo(t, mux, http.MethodPost, path, edgeMRJSON(c.body))
			if rec.Code != c.code {
				t.Errorf("POST %s %s = %d (%s), want %d", path, c.name, rec.Code, rec.Body.String(), c.code)
			}
		}
	}
	if n := len(edgeMRMarks(t, st)); n != 0 {
		t.Fatalf("refused marks stored %d rows", n)
	}
}

// "never 409 for a missing cell" — a draft job with no cell is a 200, and so
// is an empty slug; the alias keeps `tid` and `created` keys.
func TestEdgeMRMarkRouteNeedsNoCell(t *testing.T) {
	st := edgeMRStore(t)
	edgeMRDraft(t, st)
	mux := edgeMRMux()
	for _, slug := range []string{edgeMRSlug, ""} {
		rec := edgeMRDo(t, mux, http.MethodPost, "/api/mm/mark", edgeMRJSON(map[string]any{"slug": slug, "text": "words for " + slug, "source": map[string]any{"kind": "manual"}}))
		if rec.Code != http.StatusOK {
			t.Errorf("POST /api/mm/mark slug %q with no cell = %d (%s), want 200", slug, rec.Code, rec.Body.String())
		}
	}
	rec := edgeMRDo(t, mux, http.MethodPost, "/api/mm/capture", edgeMRJSON(map[string]any{"slug": edgeMRSlug, "text": "alias words", "source": map[string]any{"kind": "manual"}}))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/mm/capture with no cell = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	got := edgeMRObj(t, rec)
	if got["tid"] != "" || got["created"] != false {
		t.Fatalf("capture alias body = %v, want tid \"\" and created false present", got)
	}
}

// Contract silence (see the r1 report): the route is named POST; the repo's
// convention for a POST-only route is 405 on GET. Nothing is stored.
func TestEdgeMRMarkRouteGetIs405(t *testing.T) {
	st := edgeMRStore(t)
	edgeMRDraft(t, st)
	rec := edgeMRDo(t, edgeMRMux(), http.MethodGet, "/api/mm/mark?slug="+edgeMRSlug+"&text=x", "")
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/mm/mark = %d (%s), want 405", rec.Code, rec.Body.String())
	}
	if n := len(edgeMRMarks(t, st)); n != 0 {
		t.Fatalf("GET stored %d marks", n)
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/mark/remove
// ---------------------------------------------------------------------------

func TestEdgeMRMarkRemoveRefusals(t *testing.T) {
	st := edgeMRStore(t)
	mux := edgeMRMux()
	if rec := edgeMRDo(t, mux, http.MethodPost, "/api/mm/mark/remove", `{"id":987654}`); rec.Code != http.StatusNotFound {
		t.Errorf("remove unknown = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	id, _, err := st.AddMMMark(store.MMMark{Text: "consumed words", SourceJSON: `{"kind":"manual"}`, CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	run := edgeMRRun(t, st, false)
	if err := st.ConsumeMMMarks([]int64{id}, run, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	rec := edgeMRDo(t, mux, http.MethodPost, "/api/mm/mark/remove", `{"id":`+strconv.FormatInt(id, 10)+`}`)
	if rec.Code != http.StatusConflict {
		t.Errorf("remove consumed = %d (%s), want 409", rec.Code, rec.Body.String())
	}
	if n := len(edgeMRMarks(t, st)); n != 1 {
		t.Errorf("refused remove left %d marks, want the consumed one kept", n)
	}
}

// ---------------------------------------------------------------------------
// GET /api/mm/run, GET /api/mm/runs
// ---------------------------------------------------------------------------

func TestEdgeMRRunRouteRefusals(t *testing.T) {
	st := edgeMRStore(t)
	mux := edgeMRMux()
	id := edgeMRRun(t, st, true)
	ids := strconv.FormatInt(id, 10)

	for _, blob := range []string{"bundle_gz", "Report", "transcript", "flags"} {
		if rec := edgeMRDo(t, mux, http.MethodGet, "/api/mm/run?id="+ids+"&blob="+blob, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("GET /api/mm/run blob=%s = %d (%s), want 400", blob, rec.Code, rec.Body.String())
		}
	}
	for _, blob := range []string{"report", "manifest", "answer", "bundle"} {
		if rec := edgeMRDo(t, mux, http.MethodGet, "/api/mm/run?id=987654&blob="+blob, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET /api/mm/run unknown id blob=%s = %d (%s), want 404", blob, rec.Code, rec.Body.String())
		}
	}

	if n, err := st.PruneMindRunBundles(time.Now().Add(time.Hour).UnixMilli(), time.Now().UnixMilli()); err != nil || n != 1 {
		t.Fatalf("premise: prune = (%d, %v)", n, err)
	}
	rec := edgeMRDo(t, mux, http.MethodGet, "/api/mm/run?id="+ids+"&blob=bundle", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET pruned bundle = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "bundle pruned") {
		t.Fatalf("pruned 404 body = %q, want it to say \"bundle pruned\"", rec.Body.String())
	}
}

// "never a blob": no run object carries bundle/answer/manifest keys, and a
// pruned run reads `has_bundle: false`.
func TestEdgeMRRunsListCarriesNoBlobs(t *testing.T) {
	st := edgeMRStore(t)
	mux := edgeMRMux()
	edgeMRRun(t, st, true)
	edgeMRRun(t, st, false)
	rec := edgeMRDo(t, mux, http.MethodGet, "/api/mm/runs", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm/runs = %d (%s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Runs []map[string]json.RawMessage `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 2 {
		t.Fatalf("runs = %d, want 2", len(body.Runs))
	}
	for i, r := range body.Runs {
		for _, k := range []string{"bundle", "bundle_gz", "answer", "manifest", "blob"} {
			if _, ok := r[k]; ok {
				t.Errorf("run %d carries %q", i, k)
			}
		}
		if string(r["has_bundle"]) != "true" && string(r["has_bundle"]) != "false" {
			t.Errorf("run %d has_bundle = %s, want a bool", i, r["has_bundle"])
		}
	}
	if strings.Contains(rec.Body.String(), "not-really-gzip") || strings.Contains(rec.Body.String(), `\"excluded\"`) {
		t.Fatalf("runs list leaked blob bytes: %s", rec.Body.String())
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/replay
// ---------------------------------------------------------------------------

func TestEdgeMRReplayRouteRefusals(t *testing.T) {
	st := edgeMRStore(t)
	mux := edgeMRMux()
	if rec := edgeMRDo(t, mux, http.MethodPost, "/api/mm/replay", `{"id":987654,"write":false}`); rec.Code != http.StatusNotFound {
		t.Errorf("replay unknown = %d (%s), want 404", rec.Code, rec.Body.String())
	}
	id := edgeMRRun(t, st, true)
	if n, err := st.PruneMindRunBundles(time.Now().Add(time.Hour).UnixMilli(), time.Now().UnixMilli()); err != nil || n != 1 {
		t.Fatalf("premise: prune = (%d, %v)", n, err)
	}
	before, _ := st.ListMindRuns(0, 0)
	for _, write := range []string{"false", "true"} {
		rec := edgeMRDo(t, mux, http.MethodPost, "/api/mm/replay", `{"id":`+strconv.FormatInt(id, 10)+`,"write":`+write+`}`)
		if rec.Code != http.StatusConflict {
			t.Errorf("replay pruned write=%s = %d (%s), want 409", write, rec.Code, rec.Body.String())
		}
	}
	if after, _ := st.ListMindRuns(0, 0); len(after) != len(before) {
		t.Errorf("refused replays created %d run rows", len(after)-len(before))
	}
}

// ---------------------------------------------------------------------------
// Mirror
// ---------------------------------------------------------------------------

// "Every new POST route is `mirrorGated`" — 403 `disabled in mirror mode`,
// before any decode, writing nothing.
func TestEdgeMRNewPostsAreMirrorGated(t *testing.T) {
	st := edgeMRStore(t)
	edgeMRDraft(t, st)
	pending, _, err := st.AddMMMark(store.MMMark{Text: "pending words", SourceJSON: `{"kind":"manual"}`, CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	run := edgeMRRun(t, st, true)
	SetMirrorMode(true)
	mux := edgeMRMux()
	valid := edgeMRJSON(map[string]any{"slug": edgeMRSlug, "text": "mirror words", "source": map[string]any{"kind": "manual"}})
	for path, body := range map[string]string{
		"/api/mm/mark":        valid,
		"/api/mm/capture":     valid,
		"/api/mm/mark/remove": `{"id":` + strconv.FormatInt(pending, 10) + `}`,
		"/api/mm/replay":      `{"id":` + strconv.FormatInt(run, 10) + `,"write":true}`,
	} {
		for _, b := range []string{body, `{`} {
			rec := edgeMRDo(t, mux, http.MethodPost, path, b)
			if rec.Code != http.StatusForbidden {
				t.Errorf("mirror POST %s %q = %d (%s), want 403", path, b, rec.Code, rec.Body.String())
				continue
			}
			if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
				t.Errorf("mirror POST %s body = %q, want %q", path, got, "disabled in mirror mode")
			}
		}
	}
	marks := edgeMRMarks(t, st)
	if len(marks) != 1 || marks[0].ID != pending {
		t.Fatalf("mirror POSTs changed the marks: %+v", marks)
	}
	if runs, _ := st.ListMindRuns(0, 0); len(runs) != 1 {
		t.Fatalf("mirror replay created runs: %d rows", len(runs))
	}
}

// ---------------------------------------------------------------------------
// Retired and kept routes
// ---------------------------------------------------------------------------

// "`role: "mind"` → 409 with `ErrThreadRoleRetired`'s message" — no thread
// row is created.
func TestEdgeMRThreadsMindRoleIs409(t *testing.T) {
	st := edgeMRStore(t)
	t.Setenv("PATH", t.TempDir())
	id, err := st.CreateJob(&store.Job{Slug: "edge-mr-thread", Repo: "kraken", Body: "b", State: "working", Session: "sess-lead"})
	if err != nil {
		t.Fatal(err)
	}
	mux := edgeMRMux()
	for _, headless := range []any{nil, false} {
		req := map[string]any{"slug": "edge-mr-thread", "op": "branch", "title": "why", "text": "a question", "role": "mind"}
		if headless != nil {
			req["headless"] = headless
		}
		rec := edgeMRDo(t, mux, http.MethodPost, "/api/threads", edgeMRJSON(req))
		if rec.Code != http.StatusConflict {
			t.Errorf("branch role mind (headless %v) = %d (%s), want 409", headless, rec.Code, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), jobs.ErrThreadRoleRetired.Error()) {
			t.Errorf("branch role mind body = %q, want ErrThreadRoleRetired's message %q", rec.Body.String(), jobs.ErrThreadRoleRetired.Error())
		}
	}
	if threads, err := st.ListThreads(id); err != nil || len(threads) != 0 {
		t.Fatalf("retired role left (%d rows, %v)", len(threads), err)
	}
}

// "`/api/mm/confirm` kept … a live row is a 200 no-op".
func TestEdgeMRConfirmLiveRowStill200(t *testing.T) {
	st := edgeMRStore(t)
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Title: "a live claim", Content: "b", Quote: "a live claim", CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetMMItem(id)
	rec := edgeMRDo(t, edgeMRMux(), http.MethodPost, "/api/mm/confirm", `{"id":`+strconv.FormatInt(id, 10)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm live row = %d (%s), want 200", rec.Code, rec.Body.String())
	}
	if after, _ := st.GetMMItem(id); after.State != before.State || after.InvalidatedAt != 0 {
		t.Fatalf("confirm of a live row changed it: %+v → %+v", before, after)
	}
}

// "`/api/mm/propose`, `/api/mm/commit`, `/api/mm/revise` … answer 404 like any
// unregistered path".
func TestEdgeMRRetiredWriteRoutesAre404(t *testing.T) {
	edgeMRStore(t)
	mux := edgeMRMux()
	for _, p := range []string{"/api/mm/propose", "/api/mm/commit", "/api/mm/revise"} {
		if rec := edgeMRDo(t, mux, http.MethodPost, p, `{}`); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d (%s), want 404", p, rec.Code, rec.Body.String())
		}
	}
}
