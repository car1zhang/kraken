package api

import (
	"bytes"
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

// mindlayer_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (PR 2, the mental-model rework), slice C (api): the
// new `/api/mm` keys and filter params, `/api/mm/flags`, `/api/mm/show`,
// `/api/mm/source`, the commit/revise/flag/resolve success shapes,
// `/api/job/transcript`, the capture route's no-tmux arms and `webThread.role`.
//
// Written from the frozen contract alone; no non-test file under internal/ was
// read by any route. Every route is driven through the real mux (Register).
// Dimension: LOGIC — shapes and happy paths. Decode discipline and refusals are
// athena-edge's (`edgeML…`).
//
// No test here creates a flag from a lead session or raises one: a raise fires
// a real `notify`, and the jobs package's notify seam is unexported from here.
//
// Every package-level identifier here is prefixed `contractML`.

func contractMLEnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t) // isolates HOME, installs the store
	for _, k := range []string{"KRAKEN_DB_PATH", "KRAKEN_THREAD_SESSION", "KRAKEN_THREAD_ROLE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return st
}

func contractMLMux() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractMLDo(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		rd = bytes.NewReader(mustJSON(t, body))
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractMLObj(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return m
}

func contractMLWant(t *testing.T, rec *httptest.ResponseRecorder, code int, what string) map[string]any {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("%s: status %d, want %d; body %s", what, rec.Code, code, rec.Body.String())
	}
	return contractMLObj(t, rec)
}

func contractMLItems(t *testing.T, mux http.Handler, query string) []map[string]any {
	t.Helper()
	body := contractMLWant(t, contractMLDo(t, mux, http.MethodGet, "/api/mm"+query, nil), 200, "GET /api/mm"+query)
	arr, ok := body["items"].([]any)
	if !ok {
		t.Fatalf("GET /api/mm%s items = %#v", query, body["items"])
	}
	out := []map[string]any{}
	for _, e := range arr {
		out = append(out, e.(map[string]any))
	}
	return out
}

func contractMLIDs(items []map[string]any) []int64 {
	out := []int64{}
	for _, it := range items {
		n, _ := it["id"].(float64)
		out = append(out, int64(n))
	}
	return out
}

func contractMLAdd(t *testing.T, st *store.Store, it store.MMItem) int64 {
	t.Helper()
	if it.Kind == "" {
		it.Kind = "note"
	}
	if it.Source == "" {
		it.Source = "capture"
	}
	id, err := st.AddMMItem(it)
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	return id
}

// contractMLRepo makes `<HOME>/Developer/<name>` resolvable as a repo.
func contractMLRepo(t *testing.T, name string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// contractMLMindJob creates a job with lead session `lead` and a live mind
// thread carrying `mindSess`.
func contractMLMindJob(t *testing.T, st *store.Store, slug, lead, mindSess string) int64 {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", State: "working", Session: lead})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if mindSess != "" {
		if _, err := st.AddThreadNext(store.Thread{JobID: id, Title: "mind", SessionID: mindSess, State: "live", Role: "mind"}); err != nil {
			t.Fatalf("AddThreadNext: %v", err)
		}
	}
	return id
}

func contractMLCommitBody() jobs.MMCommit {
	return jobs.MMCommit{
		Title: "the tracker bumps zeus on red CI", Scope: "repo", Repo: "kraken", Dir: "internal/jobs",
		Kind: "decision", Body: "kraken serve's PR tracker watches CI, not zeus.", Quote: "the daemon watches CI",
		Source: jobs.CaptureSource{Kind: "manual"},
	}
}

// ---------------------------------------------------------------------------
// GET /api/mm
// ---------------------------------------------------------------------------

// "every item object additionally carries `title`, `scope`, `repo`, `dir`,
// `quote`, `thread_session`, `revised_at` and `source` (the parsed
// `CaptureSource` object, or `null` when `source_json` is empty); `episode_id`
// stays."
func TestMindLayerMMGetCarriesNewKeys(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	capID := contractMLAdd(t, st, store.MMItem{
		Content: "the body", CreatedAt: 20, Title: "a claim", Scope: "repo", Repo: "kraken", Dir: "internal/api",
		Quote: "quoted words", ThreadSession: "mind-sess",
		SourceJSON: `{"kind":"transcript","session":"zeus-1","uuid":"u-9"}`,
	})
	legacyID := contractMLAdd(t, st, store.MMItem{Source: "manual", Content: "legacy", CreatedAt: 10})
	items := contractMLItems(t, mux, "")
	byID := map[int64]map[string]any{}
	for _, it := range items {
		n, _ := it["id"].(float64)
		byID[int64(n)] = it
	}
	c, l := byID[capID], byID[legacyID]
	if c == nil || l == nil {
		t.Fatalf("GET /api/mm items = %v, want both", items)
	}
	for _, k := range []string{"title", "scope", "repo", "dir", "quote", "thread_session", "revised_at", "source", "capture_source", "episode_id"} {
		if _, ok := c[k]; !ok {
			t.Errorf("capture item lacks %q: %v", k, c)
		}
		if _, ok := l[k]; !ok {
			t.Errorf("legacy item lacks %q: %v", k, l)
		}
	}
	if c["title"] != "a claim" || c["scope"] != "repo" || c["repo"] != "kraken" || c["dir"] != "internal/api" ||
		c["quote"] != "quoted words" || c["thread_session"] != "mind-sess" {
		t.Errorf("capture item values = %v", c)
	}
	// Amendment 1: "The parsed `CaptureSource` object now rides the additive key
	// `capture_source` (null when `source_json` is empty) ...; `source` stays the
	// string kind (`capture` for new-flow rows)."
	if c["source"] != "capture" || l["source"] != "manual" {
		t.Errorf("source = %#v / %#v, want the string kinds capture / manual", c["source"], l["source"])
	}
	src, ok := c["capture_source"].(map[string]any)
	if !ok {
		t.Fatalf("capture item capture_source = %#v, want the parsed object", c["capture_source"])
	}
	if src["kind"] != "transcript" || src["session"] != "zeus-1" || src["uuid"] != "u-9" {
		t.Errorf("capture_source = %v", src)
	}
	if v, present := l["capture_source"]; !present || v != nil {
		t.Errorf("legacy item capture_source = %#v (present %v), want null", v, present)
	}
	if l["title"] != "" || l["scope"] != "repo" {
		t.Errorf("legacy item title/scope = %v/%v, want ''/repo", l["title"], l["scope"])
	}
}

// "New params pass straight into `store.MMFilter`" — `q`, `repo`, `dir`,
// `scope`.
func TestMindLayerMMGetFilters(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	root := contractMLAdd(t, st, store.MMItem{Title: "root", Content: "r", Repo: "kraken", Dir: "", CreatedAt: 1})
	a := contractMLAdd(t, st, store.MMItem{Title: "Capacity rule", Content: "a", Repo: "kraken", Dir: "a", CreatedAt: 2})
	ab := contractMLAdd(t, st, store.MMItem{Title: "deep", Content: "a/b capacity", Repo: "kraken", Dir: "a/b", CreatedAt: 3})
	other := contractMLAdd(t, st, store.MMItem{Title: "other", Content: "o", Repo: "monolith", Dir: "a/b", CreatedAt: 4})
	uni := contractMLAdd(t, st, store.MMItem{Title: "uni", Content: "u", Quote: "CAPACITY", Scope: "universal", CreatedAt: 5})

	check := func(q string, want []int64) {
		t.Helper()
		if got := contractMLIDs(contractMLItems(t, mux, q)); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("GET /api/mm%s ids = %v, want %v", q, got, want)
		}
	}
	check("?q=capacity", []int64{uni, ab, a})
	check("?repo=kraken", []int64{ab, a, root})
	check("?repo=kraken&dir=a/b", []int64{ab, a, root})
	check("?scope=universal", []int64{uni})
	check("?repo=monolith", []int64{other})
}

// ---------------------------------------------------------------------------
// GET /api/mm/show, /api/mm/source
// ---------------------------------------------------------------------------

// "`GET /api/mm/show`: 200 with the full `webMMItem` plus `"source_hint": "kraken
// mm source <id>"`".
func TestMindLayerMMShow(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	id := contractMLAdd(t, st, store.MMItem{Title: "shown", Content: "body", Quote: "q", Repo: "kraken", CreatedAt: 1})
	body := contractMLWant(t, contractMLDo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/show?id=%d", id), nil), 200, "GET /api/mm/show")
	if want := fmt.Sprintf("kraken mm source %d", id); body["source_hint"] != want {
		t.Errorf("source_hint = %v, want %q", body["source_hint"], want)
	}
	if n, _ := body["id"].(float64); int64(n) != id {
		t.Errorf("show id = %v, want %d", body["id"], id)
	}
	for k, v := range map[string]any{"title": "shown", "content": "body", "quote": "q", "repo": "kraken"} {
		if body[k] != v {
			t.Errorf("show %s = %v, want %v", k, body[k], v)
		}
	}
	for _, k := range []string{"scope", "dir", "thread_session", "revised_at", "source", "capture_source", "episode_id"} {
		if _, ok := body[k]; !ok {
			t.Errorf("show lacks %q", k)
		}
	}
}

func contractMLSource(t *testing.T, mux http.Handler, id int64) map[string]any {
	t.Helper()
	body := contractMLWant(t, contractMLDo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/source?id=%d", id), nil), 200, "GET /api/mm/source")
	for _, k := range []string{"kind", "available", "changed", "text", "ext"} {
		if _, ok := body[k]; !ok {
			t.Errorf("source body lacks %q: %v", k, body)
		}
	}
	return body
}

// "`GET /api/mm/source`: 200 `{"kind", "available", "changed", "text", "ext"}`:
// `artifact` → the frozen `mm_items.snapshot` bytes as text when present ...;
// `dir` → a one-level listing of the repo-relative dir in the primary checkout;
// ... `item`/`manual` → `available:false`."
func TestMindLayerMMSourceKinds(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()

	man := contractMLAdd(t, st, store.MMItem{Title: "m", Content: "m", SourceJSON: `{"kind":"manual"}`, CreatedAt: 1})
	if b := contractMLSource(t, mux, man); b["kind"] != "manual" || b["available"] != false {
		t.Errorf("manual source = %v, want kind manual, available false", b)
	}
	itm := contractMLAdd(t, st, store.MMItem{Title: "i", Content: "i", SourceJSON: `{"kind":"item","item_id":1}`, CreatedAt: 2})
	if b := contractMLSource(t, mux, itm); b["kind"] != "item" || b["available"] != false {
		t.Errorf("item source = %v, want kind item, available false", b)
	}

	art := contractMLAdd(t, st, store.MMItem{
		Title: "a", Content: "a", CreatedAt: 3,
		SourceJSON: `{"kind":"artifact","slug":"gone-job","home":"cell","name":"00-overview.md","sha":"abc"}`,
	})
	if err := st.SetMMSnapshot(art, []byte("# frozen overview\n"), ".md"); err != nil {
		t.Fatalf("SetMMSnapshot: %v", err)
	}
	if b := contractMLSource(t, mux, art); b["kind"] != "artifact" || b["available"] != true || b["text"] != "# frozen overview\n" {
		t.Errorf("artifact source = %v, want the frozen snapshot text", b)
	}

	repo := contractMLRepo(t, "kraken")
	if err := os.MkdirAll(filepath.Join(repo, "internal", "jobs", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "internal", "jobs", "cell.go"), []byte("package jobs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := contractMLAdd(t, st, store.MMItem{
		Title: "d", Content: "d", Repo: "kraken", Dir: "internal/jobs", CreatedAt: 4,
		SourceJSON: `{"kind":"dir","repo":"kraken","dir":"internal/jobs"}`,
	})
	b := contractMLSource(t, mux, dir)
	if b["kind"] != "dir" || b["available"] != true {
		t.Errorf("dir source = %v, want kind dir, available", b)
	}
	if text, _ := b["text"].(string); !strings.Contains(text, "cell.go") || !strings.Contains(text, "sub") {
		t.Errorf("dir listing %q lacks the one-level entries cell.go and sub", text)
	}
}

// ---------------------------------------------------------------------------
// commit / revise / flag / resolve
// ---------------------------------------------------------------------------

// "`POST /api/mm/commit`: body `{"thread_session", "item"}` ... success → 200
// `{"id": <int>}`."
func TestMindLayerMMCommitRoute(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	contractMLRepo(t, "kraken")
	contractMLMindJob(t, st, "commit-route", "zeus-c", "mind-c")
	body := contractMLWant(t, contractMLDo(t, mux, http.MethodPost, "/api/mm/commit",
		map[string]any{"thread_session": "mind-c", "item": contractMLCommitBody()}), 200, "POST /api/mm/commit")
	n, ok := body["id"].(float64)
	if !ok || n <= 0 {
		t.Fatalf("commit body = %v, want {id:N}", body)
	}
	it, err := st.GetMMItem(int64(n))
	if err != nil {
		t.Fatalf("GetMMItem: %v", err)
	}
	if it.Source != "capture" || it.ThreadSession != "mind-c" || it.Title != contractMLCommitBody().Title {
		t.Errorf("committed item = %+v", it)
	}

	// "`POST /api/mm/revise` ... success → 200 `{"id": <int>}`."
	rev := contractMLCommitBody()
	rev.Body = "revised body"
	rb := contractMLWant(t, contractMLDo(t, mux, http.MethodPost, "/api/mm/revise",
		map[string]any{"thread_session": "mind-c", "id": int64(n), "item": rev}), 200, "POST /api/mm/revise")
	if rn, _ := rb["id"].(float64); rn != n {
		t.Errorf("revise body = %v, want id %v", rb, n)
	}
	if it, _ := st.GetMMItem(int64(n)); it.Content != "revised body" || it.RevisedAt <= 0 {
		t.Errorf("revised item = %+v", it)
	}
}

// "`ErrNotMindThread` → 403 `{"error":"not a mind thread"}`".
func TestMindLayerMMCommitRouteNonMind403Body(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	contractMLRepo(t, "kraken")
	contractMLMindJob(t, st, "commit-403", "zeus-403", "mind-403")
	body := contractMLWant(t, contractMLDo(t, mux, http.MethodPost, "/api/mm/commit",
		map[string]any{"thread_session": "zeus-403", "item": contractMLCommitBody()}), 403, "POST /api/mm/commit from zeus")
	if body["error"] != "not a mind thread" {
		t.Errorf("403 body = %v, want error \"not a mind thread\"", body)
	}
}

// "`POST /api/mm/flag` ... success → 200 `{"id": <int>, "state":
// "pending"|"raised"}`" / "`/api/mm/flag/dismiss` ... success → 200 `{"ok":
// true}`" / "`/api/mm/flag/resolve` ... success → 200 `{"ok": true}`".
func TestMindLayerMMFlagRoutes(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	contractMLMindJob(t, st, "flag-route", "zeus-f", "")
	item := contractMLAdd(t, st, store.MMItem{Title: "belief", Content: "b", CreatedAt: 1})

	fb := contractMLWant(t, contractMLDo(t, mux, http.MethodPost, "/api/mm/flag", map[string]any{
		"session": "athena-f", "slug": "flag-route", "item_id": item, "against_id": 0, "note": "contradicts the transcript",
	}), 200, "POST /api/mm/flag")
	fid, ok := fb["id"].(float64)
	if !ok || fid <= 0 || fb["state"] != "pending" {
		t.Fatalf("flag body = %v, want {id:N, state:pending}", fb)
	}

	db := contractMLWant(t, contractMLDo(t, mux, http.MethodPost, "/api/mm/flag/dismiss", map[string]any{
		"session": "zeus-f", "id": int64(fid), "note": "checked, fine",
	}), 200, "POST /api/mm/flag/dismiss")
	if db["ok"] != true {
		t.Errorf("dismiss body = %v, want {ok:true}", db)
	}
	if f, _ := st.GetMMFlag(int64(fid)); f.State != "dismissed" {
		t.Errorf("flag after dismiss = %+v", f)
	}

	// a raised flag (seeded directly — raising through the route notifies)
	rid, err := st.AddMMFlag(store.MMFlag{ItemID: item, Note: "really contradicted", State: "raised", RaisedBy: "zeus-f", RaisedAt: 5, CreatedAt: 5})
	if err != nil {
		t.Fatalf("AddMMFlag: %v", err)
	}
	rb := contractMLWant(t, contractMLDo(t, mux, http.MethodPost, "/api/mm/flag/resolve", map[string]any{
		"id": rid, "resolution": "drop",
	}), 200, "POST /api/mm/flag/resolve")
	if rb["ok"] != true {
		t.Errorf("resolve body = %v, want {ok:true}", rb)
	}
	if it, _ := st.GetMMItem(item); it.InvalidatedAt == 0 || it.InvalidReason != "contradicted" {
		t.Errorf("item after drop = %+v, want invalidated contradicted", it)
	}
}

// "`GET /api/mm/flags`: 200 `{"flags":[webMMFlag]}` with `webMMFlag` = every
// `store.MMFlag` field in snake_case ..., newest first" / query `state`.
func TestMindLayerMMFlagsList(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	p, _ := st.AddMMFlag(store.MMFlag{ItemID: 3, AgainstID: 4, JobID: 0, Slug: "s", Session: "sess", Note: "older", CreatedAt: 10})
	r, _ := st.AddMMFlag(store.MMFlag{Note: "newer", State: "raised", RaisedBy: "zeus", RaiseNote: "yes", RaisedAt: 21, CreatedAt: 20})

	body := contractMLWant(t, contractMLDo(t, mux, http.MethodGet, "/api/mm/flags", nil), 200, "GET /api/mm/flags")
	arr, ok := body["flags"].([]any)
	if !ok || len(arr) != 2 {
		t.Fatalf("flags = %#v, want 2", body["flags"])
	}
	first := arr[0].(map[string]any)
	second := arr[1].(map[string]any)
	if n, _ := first["id"].(float64); int64(n) != r {
		t.Errorf("flags[0] = %v, want newest (id %d)", first, r)
	}
	keys := []string{"id", "item_id", "against_id", "job_id", "slug", "session", "note", "state",
		"raised_by", "raise_note", "raised_at", "resolved_at", "resolution", "created_at"}
	for _, k := range keys {
		if _, ok := second[k]; !ok {
			t.Errorf("webMMFlag lacks %q: %v", k, second)
		}
	}
	if n, _ := second["id"].(float64); int64(n) != p {
		t.Errorf("flags[1] id = %v, want %d", second["id"], p)
	}
	if second["item_id"] != float64(3) || second["against_id"] != float64(4) || second["note"] != "older" ||
		second["state"] != "pending" || second["session"] != "sess" || second["created_at"] != float64(10) {
		t.Errorf("flags[1] values = %v", second)
	}
	if first["raised_by"] != "zeus" || first["raise_note"] != "yes" || first["raised_at"] != float64(21) {
		t.Errorf("flags[0] raise values = %v", first)
	}

	rb := contractMLWant(t, contractMLDo(t, mux, http.MethodGet, "/api/mm/flags?state=raised", nil), 200, "GET /api/mm/flags?state=raised")
	ra, _ := rb["flags"].([]any)
	if len(ra) != 1 || ra[0].(map[string]any)["note"] != "newer" {
		t.Errorf("flags?state=raised = %v, want only the raised one", rb)
	}
}

// ---------------------------------------------------------------------------
// /api/job/transcript, /api/threads role, /api/mm/capture
// ---------------------------------------------------------------------------

// "`GET /api/job/transcript` (Q5): 200 `{"session": string, "blocks":
// [TranscriptBlock]}` via `ZeusTranscriptBlocks`" — a job with no session
// answers the empty shape.
func TestMindLayerJobTranscriptShape(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	if _, err := st.CreateJob(&store.Job{Slug: "tx-job", Repo: "kraken", State: "working"}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	body := contractMLWant(t, contractMLDo(t, mux, http.MethodGet, "/api/job/transcript?slug=tx-job", nil), 200, "GET /api/job/transcript")
	if body["session"] != "" {
		t.Errorf("session = %v, want \"\" for an unstamped job", body["session"])
	}
	arr, ok := body["blocks"].([]any)
	if !ok || len(arr) != 0 {
		t.Errorf("blocks = %#v, want an empty array", body["blocks"])
	}
}

// "`webThread` (board payload and this route's list) carries `"role"`".
func TestMindLayerThreadsListCarriesRole(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	id, err := st.CreateJob(&store.Job{Slug: "role-list", Repo: "kraken"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := st.AddThread(store.Thread{JobID: id, TID: "t1", SessionID: "s1", State: "live", Role: "mind", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddThread(store.Thread{JobID: id, TID: "t2", SessionID: "s2", State: "live", CreatedAt: 2}); err != nil {
		t.Fatal(err)
	}
	body := contractMLWant(t, contractMLDo(t, mux, http.MethodGet, "/api/threads?slug=role-list", nil), 200, "GET /api/threads")
	arr, _ := body["threads"].([]any)
	if len(arr) != 2 {
		t.Fatalf("threads = %v", body)
	}
	got := map[string]any{}
	for _, e := range arr {
		m := e.(map[string]any)
		got[m["tid"].(string)] = m["role"]
	}
	if got["t1"] != "mind" || got["t2"] != "ask" {
		t.Errorf("thread roles = %v, want t1 mind, t2 ask", got)
	}
}

// "`POST /api/mm/capture` ... unknown slug → 404; ... a non-POST → 405" and
// "`ErrCaptureNoCell` (job has no live cell ...) → 409". The branch/send path
// needs tmux and is not blind-testable (Test environment obligations).
func TestMindLayerCaptureRouteNoTmuxArms(t *testing.T) {
	st := contractMLEnv(t)
	mux := contractMLMux()
	if _, err := st.CreateJob(&store.Job{Slug: "cap-draft", Repo: "kraken", State: ""}); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if rec := contractMLDo(t, mux, http.MethodGet, "/api/mm/capture", nil); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/mm/capture = %d, want 405", rec.Code)
	}
	req := map[string]any{"slug": "no-such-job", "text": "hello", "source": map[string]any{"kind": "manual"}}
	if rec := contractMLDo(t, mux, http.MethodPost, "/api/mm/capture", req); rec.Code != http.StatusNotFound {
		t.Errorf("capture unknown slug = %d, want 404; body %s", rec.Code, rec.Body.String())
	}
	req["slug"] = "cap-draft"
	if rec := contractMLDo(t, mux, http.MethodPost, "/api/mm/capture", req); rec.Code != http.StatusConflict {
		t.Errorf("capture on a job with no live cell = %d, want 409; body %s", rec.Code, rec.Body.String())
	}
}
