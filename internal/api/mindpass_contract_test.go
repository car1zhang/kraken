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
	"time"

	"kraken/internal/store"
)

// mindpass_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 639, the mind pass), the api half of slices C3,
// C4 and C5: `POST /api/mm/propose`, `POST /api/mm/confirm`, `GET
// /api/mm/evidence`, the `job`/`feedback`/`question` arms of `GET
// /api/mm/source`, `webMMItem.state` and `webJob.author`.
//
// Written from the frozen contract alone; internal/api's working tree was never
// read. Routes are driven through the real mux (`Register`), as the committed
// mindlayer contract tests do. Dimension: LOGIC — the 200 bodies. The 400 /
// 403 / 404 / 409 arms are athena-edge's.
//
// No test here makes a propose call that ADDS a row: an add fires
// `mmProposeNotify`, whose default is a real macOS notification, and the jobs
// package's seam is unexported from here. The add path is pinned in the jobs
// package; here the 200 body shape is pinned through a fold.
//
// Every package-level identifier here is prefixed `mpc`.

func mpcEnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t) // isolates HOME, installs the store
	for _, k := range []string{"KRAKEN_DB_PATH", "KRAKEN_THREAD_SESSION", "KRAKEN_THREAD_ROLE"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return st
}

func mpcMux() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func mpcDo(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	rd := bytes.NewReader(nil)
	if body != nil {
		rd = bytes.NewReader(mustJSON(t, body))
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func mpcWant(t *testing.T, rec *httptest.ResponseRecorder, code int, what string) map[string]any {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("%s: status %d, want %d; body %s", what, rec.Code, code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("%s: decode %q: %v", what, rec.Body.String(), err)
	}
	return m
}

// mpcRepo makes `<HOME>/Developer/<name>` resolvable as a repo.
func mpcRepo(t *testing.T, name string) {
	t.Helper()
	repos := filepath.Join(os.Getenv("HOME"), "Developer")
	if err := os.MkdirAll(filepath.Join(repos, name, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
}

// mpcMindLead stands up the mind job the propose gate reads: a working row with
// a lead session, named by the `mind_last_run` kv row (`MindRun`'s JSON tags:
// date, swept_to, job).
func mpcMindLead(t *testing.T, st *store.Store) string {
	t.Helper()
	const lead = "mind-lead-sess"
	if _, err := st.CreateJob(&store.Job{Slug: "mind-2026-10-21", Repo: "kraken", State: "working", Session: lead, Body: "x", Author: "kraken"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV("mind_last_run", `{"date":"2026-10-21","swept_to":1761000000000,"job":"mind-2026-10-21"}`); err != nil {
		t.Fatal(err)
	}
	return lead
}

func mpcNum(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

// ---------------------------------------------------------------------------
// POST /api/mm/propose
// ---------------------------------------------------------------------------

// "body `{"session": string, "items": [MMProposal…]}` … else 200 with
// `MMProposeSummary`" — `{results:[{index, id, outcome, reason?}], added,
// folded, suppressed, refused}`.
func TestMpcProposeRouteAnswersTheSummary(t *testing.T) {
	st := mpcEnv(t)
	mpcRepo(t, "kraken")
	lead := mpcMindLead(t, st)
	known, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "capture", Content: "c", Quote: "a quote carl already holds", CreatedAt: 5})
	if err != nil {
		t.Fatal(err)
	}
	mux := mpcMux()
	body := map[string]any{
		"session": lead,
		"items": []map[string]any{{
			"title": "the tracker owns CI", "scope": "repo", "repo": "kraken", "dir": "internal/jobs",
			"kind": "decision", "body": "Carl said the daemon watches CI, not zeus.", "quote": "a quote carl already holds",
			"rung": "rung 2", "source": map[string]any{"kind": "transcript", "session": "s", "uuid": "u"},
		}},
	}
	got := mpcWant(t, mpcDo(t, mux, http.MethodPost, "/api/mm/propose", body), 200, "POST /api/mm/propose")
	for _, k := range []string{"results", "added", "folded", "suppressed", "refused"} {
		if _, ok := got[k]; !ok {
			t.Errorf("summary lacks %q: %v", k, got)
		}
	}
	if mpcNum(got["added"]) != 0 || mpcNum(got["folded"]) != 1 || mpcNum(got["suppressed"]) != 0 || mpcNum(got["refused"]) != 0 {
		t.Errorf("counts = %v, want one fold", got)
	}
	res, _ := got["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("results = %v", got["results"])
	}
	r := res[0].(map[string]any)
	if mpcNum(r["index"]) != 0 || r["outcome"] != "folded" || mpcNum(r["id"]) != known {
		t.Errorf("results[0] = %v, want {index 0, outcome folded, id %d}", r, known)
	}
	if _, ok := r["reason"]; ok {
		t.Errorf("results[0] carries a reason on a fold: %v", r)
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/confirm
// ---------------------------------------------------------------------------

// "success and already-live → 200 `{"ok":true,"id":<id>,"state":"live"}`".
func TestMpcConfirmRouteMakesTheItemLive(t *testing.T) {
	st := mpcEnv(t)
	mux := mpcMux()
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "nightly", State: "proposed", Content: "p", Quote: "q", CreatedAt: 5})
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		got := mpcWant(t, mpcDo(t, mux, http.MethodPost, "/api/mm/confirm", map[string]any{"id": id}), 200, "POST /api/mm/confirm")
		if got["ok"] != true || mpcNum(got["id"]) != id || got["state"] != "live" || len(got) != 3 {
			t.Errorf("round %d: body = %v, want {ok:true, id:%d, state:live}", round, got, id)
		}
	}
	it, err := st.GetMMItem(id)
	if err != nil {
		t.Fatal(err)
	}
	if it.State != "live" {
		t.Errorf("stored state = %q, want live", it.State)
	}
}

// ---------------------------------------------------------------------------
// GET /api/mm/evidence
// ---------------------------------------------------------------------------

// "`?since=` required …, `?until=` optional (default 0 → the handler passes
// `time.Now().UnixMilli()`); body is `MMEvidence` marshalled".
func TestMpcEvidenceRouteServesTheEvidence(t *testing.T) {
	st := mpcEnv(t)
	mux := mpcMux()
	now := time.Now().UnixMilli()
	since := now - 3_600_000
	id, err := st.CreateJob(&store.Job{Slug: "operator-spec", Repo: "kraken", Author: "operator",
		Body: "Spec words.\n\n## Mid-run log\n- noise", CreatedAt: now - 60_000, UpdatedAt: now - 60_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateJob(&store.Job{Slug: "agent-draft", Repo: "kraken", Author: "agent", Body: "x",
		CreatedAt: now - 60_000, UpdatedAt: now - 60_000}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AddFeedback(store.Feedback{JobID: id, JobSlug: "operator-spec", Artifact: "50-contract.md", Note: "the cap is wrong", TS: now - 30_000}); err != nil {
		t.Fatal(err)
	}

	before := time.Now().UnixMilli()
	got := mpcWant(t, mpcDo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/evidence?since=%d", since), nil), 200, "GET /api/mm/evidence")
	for _, k := range []string{"since", "until", "jobs", "feedback", "answers"} {
		if _, ok := got[k]; !ok {
			t.Errorf("evidence lacks %q: %v", k, got)
		}
	}
	if mpcNum(got["since"]) != since || mpcNum(got["until"]) < before {
		t.Errorf("since/until = %v/%v, want %d and a defaulted now", got["since"], got["until"], since)
	}
	jobs, _ := got["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v, want only the operator spec", got["jobs"])
	}
	j := jobs[0].(map[string]any)
	for _, k := range []string{"slug", "id", "author", "created_at", "updated_at", "body_sha", "body"} {
		if _, ok := j[k]; !ok {
			t.Errorf("evidence job lacks %q: %v", k, j)
		}
	}
	if j["slug"] != "operator-spec" || j["author"] != "operator" || j["body"] != "Spec words." ||
		j["body_sha"] != store.SHA256Hex([]byte("Spec words.")) || mpcNum(j["id"]) != id {
		t.Errorf("evidence job = %v", j)
	}
	fb, _ := got["feedback"].([]any)
	if len(fb) != 1 {
		t.Fatalf("feedback = %v", got["feedback"])
	}
	f := fb[0].(map[string]any)
	for _, k := range []string{"id", "slug", "artifact", "ts", "note"} {
		if _, ok := f[k]; !ok {
			t.Errorf("evidence feedback lacks %q: %v", k, f)
		}
	}
	if f["note"] != "the cap is wrong" || f["artifact"] != "50-contract.md" || f["slug"] != "operator-spec" {
		t.Errorf("evidence feedback = %v", f)
	}
	if a, ok := got["answers"].([]any); !ok || len(a) != 0 {
		t.Errorf("answers = %#v, want []", got["answers"])
	}

	// an explicit ceiling below the rows excludes them, and is echoed
	early := mpcWant(t, mpcDo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/evidence?since=%d&until=%d", since, since+1000), nil), 200, "GET with until")
	if mpcNum(early["until"]) != since+1000 {
		t.Errorf("until = %v, want %d", early["until"], since+1000)
	}
	if j, _ := early["jobs"].([]any); len(j) != 0 {
		t.Errorf("jobs under an early ceiling = %v", j)
	}
}

// ---------------------------------------------------------------------------
// GET /api/mm/source — the three new pointer kinds
// ---------------------------------------------------------------------------

func mpcSource(t *testing.T, st *store.Store, mux http.Handler, sourceJSON string) map[string]any {
	t.Helper()
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "nightly", Content: "b", Quote: "q " + sourceJSON, SourceJSON: sourceJSON, CreatedAt: 5})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	return mpcWant(t, mpcDo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/source?id=%d", id), nil), 200, "GET /api/mm/source "+sourceJSON)
}

// `job` → "`{kind:"job", available:true, text:<the job's body with "## Mid-run
// log" stripped, as jobPrompt strips it>, changed: <SHA256Hex(text) != src.SHA
// when src.SHA is non-empty>}`, resolved by `Slug` among active then archived
// jobs".
func TestMpcSourceRouteResolvesAJobPointer(t *testing.T) {
	st := mpcEnv(t)
	mux := mpcMux()
	if _, err := st.CreateJob(&store.Job{Slug: "spec-job", Repo: "kraken", Body: "Spec text here.\n\n## Mid-run log\n- 01:00 x\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateArchivedJob(&store.Job{Slug: "old-job", ArchivedName: "old-job", Repo: "kraken", Body: "Old spec."}); err != nil {
		t.Fatal(err)
	}
	text := "Spec text here."
	sha := store.SHA256Hex([]byte(text))

	same := mpcSource(t, st, mux, fmt.Sprintf(`{"kind":"job","slug":"spec-job","sha":"%s"}`, sha))
	if same["kind"] != "job" || same["available"] != true || same["text"] != text || same["changed"] != false {
		t.Errorf("unchanged job pointer = %v", same)
	}
	moved := mpcSource(t, st, mux, `{"kind":"job","slug":"spec-job","sha":"0000"}`)
	if moved["available"] != true || moved["text"] != text || moved["changed"] != true {
		t.Errorf("moved job pointer = %v, want changed:true", moved)
	}
	noSHA := mpcSource(t, st, mux, `{"kind":"job","slug":"spec-job"}`)
	if noSHA["available"] != true || noSHA["changed"] == true {
		t.Errorf("sha-less job pointer = %v, want changed false", noSHA)
	}
	archived := mpcSource(t, st, mux, `{"kind":"job","slug":"old-job"}`)
	if archived["available"] != true || archived["text"] != "Old spec." {
		t.Errorf("archived job pointer = %v, want the archived body", archived)
	}
}

// `feedback` → "the note text by `GetFeedback(FeedbackID)`"; `question` →
// "`text: "<question text>\n\nanswer: <answer>"` from `ListQuestions(job id)` of
// the slug's job".
func TestMpcSourceRouteResolvesFeedbackAndQuestionPointers(t *testing.T) {
	st := mpcEnv(t)
	mux := mpcMux()
	jid, err := st.CreateJob(&store.Job{Slug: "spec-job", Repo: "kraken", Body: "x"})
	if err != nil {
		t.Fatal(err)
	}
	fid, err := st.AddFeedback(store.Feedback{JobID: jid, JobSlug: "spec-job", Artifact: "00-overview.md", Note: "this diagram is wrong", TS: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetQuestions(jid, []store.DocQuestion{{QID: "Q1", Text: "which cap?", Importance: "P1", Recommendation: "20"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AnswerQuestion(jid, "Q1", "twenty, per night", "2026-10-21T12:00:00Z"); err != nil {
		t.Fatal(err)
	}

	fb := mpcSource(t, st, mux, fmt.Sprintf(`{"kind":"feedback","feedback_id":%d}`, fid))
	if fb["kind"] != "feedback" || fb["available"] != true || fb["text"] != "this diagram is wrong" {
		t.Errorf("feedback pointer = %v", fb)
	}
	q := mpcSource(t, st, mux, `{"kind":"question","slug":"spec-job","qid":"Q1"}`)
	if q["kind"] != "question" || q["available"] != true || q["text"] != "which cap?\n\nanswer: twenty, per night" {
		t.Errorf("question pointer = %v", q)
	}
}

// ---------------------------------------------------------------------------
// webMMItem.state, webJob.author
// ---------------------------------------------------------------------------

// `webMMItem.State`: a State string tagged json "state", "always present" —
// "live" or "proposed".
func TestMpcWebMMItemCarriesState(t *testing.T) {
	for _, st := range []string{"proposed", "live"} {
		raw, err := json.Marshal(webMMItemFrom(store.MMItem{ID: 3, Kind: "note", Source: "nightly", Content: "b", State: st}, nil))
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		if m["state"] != st {
			t.Errorf("webMMItem state = %#v, want %q (%s)", m["state"], st, raw)
		}
		if m["source"] != "nightly" {
			t.Errorf("webMMItem source = %#v, want nightly", m["source"])
		}
	}
}

// `webJob.Author`: an Author string tagged json "author,omitempty"; "the stored
// value is served VERBATIM (`unknown` included …); the key is omitted only when
// the string is empty".
func TestMpcWebJobCarriesAuthor(t *testing.T) {
	for _, a := range []string{"operator", "agent", "linear", "kraken", "unknown"} {
		raw, err := json.Marshal(webJob{Slug: "j", Repo: "kraken", State: "working", Author: a})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(raw), fmt.Sprintf(`"author":%q`, a)) {
			t.Errorf("webJob author %q not served verbatim: %s", a, raw)
		}
	}
	raw, err := json.Marshal(webJob{Slug: "j", Repo: "kraken", State: "working"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"author"`) {
		t.Errorf("an empty author is served: %s", raw)
	}
}
