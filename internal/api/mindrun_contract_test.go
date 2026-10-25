package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// mindrun_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md (job 674, mind runs), slice D: `POST /api/mm/mark`,
// the `POST /api/mm/capture` alias, `GET /api/mm/marks`, `POST
// /api/mm/mark/remove`, `GET /api/mm/runs`, `GET /api/mm/run`, `POST
// /api/mm/replay`, the item DTO's additive keys, the `saved_id`/`mark_id`
// arms of `/api/mm/source`, and the retired propose/commit/revise routes.
//
// Written from the frozen contract alone; internal/api/mm.go and register.go's
// working tree were never read. Routes are driven through the real mux
// (`Register`), as the committed mind contract tests do. Dimension: LOGIC —
// the 200 bodies. The refusal arms are athena-edge's. Every package-level
// identifier is prefixed `contractMRA`.

func contractMRAEnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t) // isolates HOME, installs the store
	for _, k := range []string{"KRAKEN_DB_PATH", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	t.Setenv("KRAKEN_OPERATOR", "Test Operator")
	prev := jobs.SkillsFS
	jobs.SkillsFS = fstest.MapFS{"mind/SKILL.md": &fstest.MapFile{Data: []byte("# mind\n\nanswer for {{operator}} with one JSON object\n")}}
	t.Cleanup(func() { jobs.SkillsFS = prev })
	return st
}

func contractMRAMux() http.Handler {
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractMRADo(t *testing.T, mux http.Handler, method, path string, body any) *httptest.ResponseRecorder {
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

func contractMRAWant(t *testing.T, rec *httptest.ResponseRecorder, code int, what string) map[string]any {
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

func contractMRANum(v any) int64 {
	f, _ := v.(float64)
	return int64(f)
}

func contractMRAKeys(m map[string]any) []string {
	out := []string{}
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func contractMRAJob(t *testing.T, st *store.Store, slug, state string) int64 {
	t.Helper()
	id, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", Body: "x", State: state, CreatedAt: 1, UpdatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func contractMRAGzip(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Write(b)
	w.Close()
	return buf.Bytes()
}

// ---------------------------------------------------------------------------
// marks
// ---------------------------------------------------------------------------

// `POST /api/mm/mark` → 200 `{"id":N,"pending":P,"folded":bool}`; never 409
// for a missing cell (a done job has none).
func TestContractMRAMarkRoute(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	jid := contractMRAJob(t, st, "mark-job", "done")
	body := map[string]any{"slug": "mark-job", "text": "the contract is frozen", "source": map[string]any{"kind": "artifact", "slug": "mark-job", "name": "50-contract.md"}}
	got := contractMRAWant(t, contractMRADo(t, mux, http.MethodPost, "/api/mm/mark", body), 200, "POST /api/mm/mark")
	if keys := contractMRAKeys(got); strings.Join(keys, ",") != "folded,id,pending" {
		t.Errorf("mark keys = %v, want exactly id/pending/folded", keys)
	}
	id := contractMRANum(got["id"])
	if id <= 0 || contractMRANum(got["pending"]) != 1 || got["folded"] != false {
		t.Errorf("mark = %v, want id, pending 1, folded false", got)
	}
	again := contractMRAWant(t, contractMRADo(t, mux, http.MethodPost, "/api/mm/mark", body), 200, "POST /api/mm/mark again")
	if contractMRANum(again["id"]) != id || again["folded"] != true || contractMRANum(again["pending"]) != 1 {
		t.Errorf("repeat = %v, want folded onto %d", again, id)
	}
	marks, _ := st.ListMMMarks(jid, true)
	if len(marks) != 1 || marks[0].Text != "the contract is frozen" {
		t.Errorf("stored marks = %+v", marks)
	}
	// with no slug at all
	free := contractMRAWant(t, contractMRADo(t, mux, http.MethodPost, "/api/mm/mark",
		map[string]any{"slug": "", "text": "a library passage", "source": map[string]any{"kind": "manual"}}), 200, "POST /api/mm/mark no slug")
	if contractMRANum(free["id"]) <= 0 {
		t.Errorf("job-less mark = %v", free)
	}
}

// `POST /api/mm/capture` alias: 200 `{"tid":"","created":false,"mark_id":N,
// "pending":P}`, `tid` and `created` always present, and a mark recorded.
func TestContractMRACaptureAliasRecordsAMark(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	jid := contractMRAJob(t, st, "cap-job", "working")
	got := contractMRAWant(t, contractMRADo(t, mux, http.MethodPost, "/api/mm/capture",
		map[string]any{"slug": "cap-job", "text": "captured words", "source": map[string]any{"kind": "artifact", "name": "00-overview.md"}}), 200, "POST /api/mm/capture")
	if keys := contractMRAKeys(got); strings.Join(keys, ",") != "created,mark_id,pending,tid" {
		t.Errorf("capture keys = %v, want tid/created/mark_id/pending", keys)
	}
	if got["tid"] != "" || got["created"] != false || contractMRANum(got["mark_id"]) <= 0 || contractMRANum(got["pending"]) != 1 {
		t.Errorf("capture = %v", got)
	}
	marks, _ := st.ListMMMarks(jid, true)
	if len(marks) != 1 || marks[0].ID != contractMRANum(got["mark_id"]) || marks[0].Text != "captured words" {
		t.Errorf("stored marks = %+v", marks)
	}
	if runs, _ := st.ListMindRuns(0, 0); len(runs) != 0 {
		t.Errorf("a capture enqueued a run: %+v", runs)
	}
}

// `GET /api/mm/marks` → `{"marks":[webMMMark]}` newest first with the eight
// keys, `source` an object; `?slug=` scopes, `pending=1` filters, no slug =
// every mark. `POST /api/mm/mark/remove` → 200 `{"id":N}`.
func TestContractMRAMarksListAndRemove(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	a := contractMRAJob(t, st, "list-a", "done")
	b := contractMRAJob(t, st, "list-b", "done")
	m1, _, _ := st.AddMMMark(store.MMMark{JobID: a, Slug: "list-a", Text: "one", SourceJSON: `{"kind":"artifact","name":"x.md"}`, CreatedAt: 10})
	m2, _, _ := st.AddMMMark(store.MMMark{JobID: b, Slug: "list-b", Text: "two", SourceJSON: `{"kind":"manual"}`, CreatedAt: 20})
	m3, _, _ := st.AddMMMark(store.MMMark{JobID: a, Slug: "list-a", Text: "three", SourceJSON: `{"kind":"manual"}`, CreatedAt: 30})
	if err := st.ConsumeMMMarks([]int64{m1}, 4, 40); err != nil {
		t.Fatal(err)
	}

	ids := func(body map[string]any) []int64 {
		arr, ok := body["marks"].([]any)
		if !ok {
			t.Fatalf("marks = %#v", body["marks"])
		}
		out := []int64{}
		for _, x := range arr {
			out = append(out, contractMRANum(x.(map[string]any)["id"]))
		}
		return out
	}
	all := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, "/api/mm/marks", nil), 200, "GET /api/mm/marks")
	if got := ids(all); fmt.Sprint(got) != fmt.Sprint([]int64{m3, m2, m1}) {
		t.Errorf("all marks = %v, want %v newest first", got, []int64{m3, m2, m1})
	}
	first := all["marks"].([]any)[2].(map[string]any)
	for _, k := range []string{"id", "job_id", "slug", "text", "source", "created_at", "consumed_run", "consumed_at"} {
		if _, ok := first[k]; !ok {
			t.Errorf("webMMMark lacks %q: %v", k, first)
		}
	}
	src, ok := first["source"].(map[string]any)
	if !ok || src["kind"] != "artifact" || src["name"] != "x.md" {
		t.Errorf("source = %#v, want the CaptureSource object", first["source"])
	}
	if contractMRANum(first["consumed_run"]) != 4 || contractMRANum(first["consumed_at"]) != 40 || contractMRANum(first["job_id"]) != a || first["slug"] != "list-a" || first["text"] != "one" || contractMRANum(first["created_at"]) != 10 {
		t.Errorf("consumed mark = %v", first)
	}
	scoped := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, "/api/mm/marks?slug=list-a", nil), 200, "GET ?slug")
	if got := ids(scoped); fmt.Sprint(got) != fmt.Sprint([]int64{m3, m1}) {
		t.Errorf("slug-scoped = %v", got)
	}
	pend := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, "/api/mm/marks?slug=list-a&pending=1", nil), 200, "GET ?pending")
	if got := ids(pend); fmt.Sprint(got) != fmt.Sprint([]int64{m3}) {
		t.Errorf("pending = %v", got)
	}

	rm := contractMRAWant(t, contractMRADo(t, mux, http.MethodPost, "/api/mm/mark/remove", map[string]any{"id": m3}), 200, "POST remove")
	if contractMRANum(rm["id"]) != m3 || len(rm) != 1 {
		t.Errorf("remove = %v, want {id:%d}", rm, m3)
	}
	left, _ := st.ListMMMarks(0, false)
	if len(left) != 2 {
		t.Errorf("marks after remove = %+v", left)
	}
}

// ---------------------------------------------------------------------------
// runs
// ---------------------------------------------------------------------------

var contractMRARunKeys = []string{"id", "job_id", "slug", "trigger", "since", "until", "state", "created_at", "started_at", "finished_at",
	"note", "prompt_sha", "model", "in_tokens", "out_tokens", "bundle_bytes", "added", "superseded", "retired", "flagged",
	"excluded", "folded", "suppressed", "refused", "source_run", "has_bundle"}

const contractMRABundle = `{"run":{"run_id":1},"prompts":[{"text":"hello"}]}`

// contractMRASeedRuns: a finished run with blobs, a pruned one, and one with
// no blob row.
func contractMRASeedRuns(t *testing.T, st *store.Store) (done, pruned, bare int64) {
	t.Helper()
	jid := contractMRAJob(t, st, "runs-job", "done")
	var err error
	done, err = st.EnqueueMindRun(store.MindRun{JobID: jid, Slug: "runs-job", Trigger: "done", Since: 100, Until: 200, CreatedAt: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimNextMindRun(1_100); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishMindRun(done, store.MindRunFinish{At: 1_200, Counts: store.MindRunCounts{Added: 2, Superseded: 1, Excluded: 3},
		Report: "# the report\n\n## Proposed\n\nNone\n", PromptSHA: "abc", Model: "claude-opus-5", InTokens: 10, OutTokens: 5, BundleBytes: 99}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMindRunBlobs(done, contractMRAGzip(t, []byte(contractMRABundle)), `{"sessions":["s1"]}`); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMindRunAnswer(done, `{"ops":[],"excluded":[],"coverage":{}}`); err != nil {
		t.Fatal(err)
	}
	pruned, _ = st.EnqueueMindRun(store.MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 500})
	if err := st.SetMindRunBlobs(pruned, contractMRAGzip(t, []byte("{}")), "{}"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.PruneMindRunBundles(600, 700); err != nil {
		t.Fatal(err)
	}
	bare, _ = st.EnqueueMindRun(store.MindRun{Trigger: "daily", Since: 1, Until: 2, CreatedAt: 2_000})
	return done, pruned, bare
}

// `GET /api/mm/runs` → `{"runs":[webMindRun]}` with every MindRun field in
// snake_case plus `has_bundle`, never a blob; `?slug=` scopes, `?n=` caps.
func TestContractMRARunsList(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	done, pruned, bare := contractMRASeedRuns(t, st)
	body := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, "/api/mm/runs", nil), 200, "GET /api/mm/runs")
	arr, ok := body["runs"].([]any)
	if !ok || len(arr) != 3 {
		t.Fatalf("runs = %#v, want 3", body["runs"])
	}
	byID := map[int64]map[string]any{}
	order := []int64{}
	for _, x := range arr {
		m := x.(map[string]any)
		byID[contractMRANum(m["id"])] = m
		order = append(order, contractMRANum(m["id"]))
	}
	if fmt.Sprint(order) != fmt.Sprint([]int64{bare, done, pruned}) {
		t.Errorf("order = %v, want newest first %v", order, []int64{bare, done, pruned})
	}
	d := byID[done]
	for _, k := range contractMRARunKeys {
		if _, ok := d[k]; !ok {
			t.Errorf("webMindRun lacks %q", k)
		}
	}
	for _, k := range []string{"report", "bundle", "bundle_gz", "manifest", "answer"} {
		if _, ok := d[k]; ok {
			t.Errorf("runs list carries %q", k)
		}
	}
	if d["slug"] != "runs-job" || d["trigger"] != "done" || d["state"] != "done" || contractMRANum(d["added"]) != 2 ||
		contractMRANum(d["superseded"]) != 1 || contractMRANum(d["excluded"]) != 3 || d["prompt_sha"] != "abc" ||
		contractMRANum(d["in_tokens"]) != 10 || contractMRANum(d["bundle_bytes"]) != 99 || contractMRANum(d["since"]) != 100 || contractMRANum(d["until"]) != 200 {
		t.Errorf("done run = %v", d)
	}
	if d["has_bundle"] != true || byID[pruned]["has_bundle"] != false || byID[bare]["has_bundle"] != false {
		t.Errorf("has_bundle = %v/%v/%v, want true/false/false", d["has_bundle"], byID[pruned]["has_bundle"], byID[bare]["has_bundle"])
	}
	scoped := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, "/api/mm/runs?slug=runs-job", nil), 200, "GET ?slug")
	if a, _ := scoped["runs"].([]any); len(a) != 1 || contractMRANum(a[0].(map[string]any)["id"]) != done {
		t.Errorf("slug-scoped runs = %v", scoped["runs"])
	}
	capped := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, "/api/mm/runs?n=1", nil), 200, "GET ?n=1")
	if a, _ := capped["runs"].([]any); len(a) != 1 {
		t.Errorf("n=1 runs = %v", capped["runs"])
	}
}

// `GET /api/mm/run?id=&blob=…` → `{"run":webMindRun,"blob":"<text>"}`;
// `report` is the row's report; `bundle` is the gunzipped JSON text.
func TestContractMRARunBlobs(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	done, _, _ := contractMRASeedRuns(t, st)
	want := map[string]string{
		"report":   "# the report\n\n## Proposed\n\nNone\n",
		"manifest": `{"sessions":["s1"]}`,
		"answer":   `{"ops":[],"excluded":[],"coverage":{}}`,
		"bundle":   contractMRABundle,
	}
	for blob, text := range want {
		got := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/run?id=%d&blob=%s", done, blob), nil), 200, "GET /api/mm/run "+blob)
		if got["blob"] != text {
			t.Errorf("blob=%s = %q, want %q", blob, got["blob"], text)
		}
		run, ok := got["run"].(map[string]any)
		if !ok || contractMRANum(run["id"]) != done || run["has_bundle"] != true {
			t.Errorf("blob=%s run = %#v", blob, got["run"])
		}
		if keys := contractMRAKeys(got); strings.Join(keys, ",") != "blob,run" {
			t.Errorf("blob=%s keys = %v", blob, keys)
		}
	}
}

// `POST /api/mm/replay` (dry run) → 200 with `source_run, old_prompt_sha,
// new_prompt_sha, diff{ops_added, ops_lost, exclusions_changed}, new_run`.
func TestContractMRAReplayDryRunShape(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	answer := `{"ops":[],"excluded":[{"rung":"hedge","quote":"maybe","why":"hedged"}],"coverage":{}}`
	restore := jobs.SetMindLauncherForTest(func(ctx context.Context, argv []string, dir string, env []string, stdin string) (string, string, error) {
		return answer, "", nil
	})
	t.Cleanup(restore)
	id, err := st.EnqueueMindRun(store.MindRun{Trigger: "daily", Since: time.Now().Add(-time.Hour).UnixMilli(), Until: time.Now().UnixMilli(), CreatedAt: time.Now().UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	run, err := st.ClaimNextMindRun(time.Now().UnixMilli())
	if err != nil || run == nil {
		t.Fatalf("claim: %v %v", run, err)
	}
	if err := jobs.RunMindRun(st, *run); err != nil {
		t.Fatalf("RunMindRun: %v", err)
	}
	before, _ := st.ListMindRuns(0, 0)

	got := contractMRAWant(t, contractMRADo(t, mux, http.MethodPost, "/api/mm/replay", map[string]any{"id": id, "write": false}), 200, "POST /api/mm/replay")
	for _, k := range []string{"source_run", "old_prompt_sha", "new_prompt_sha", "diff", "new_run"} {
		if _, ok := got[k]; !ok {
			t.Errorf("replay body lacks %q: %v", k, got)
		}
	}
	if contractMRANum(got["source_run"]) != id || contractMRANum(got["new_run"]) != 0 {
		t.Errorf("replay = source %v new %v", got["source_run"], got["new_run"])
	}
	if got["old_prompt_sha"] == "" || got["old_prompt_sha"] != got["new_prompt_sha"] {
		t.Errorf("prompt shas = %v / %v, want equal and non-empty", got["old_prompt_sha"], got["new_prompt_sha"])
	}
	diff, ok := got["diff"].(map[string]any)
	if !ok {
		t.Fatalf("diff = %#v", got["diff"])
	}
	for _, k := range []string{"ops_added", "ops_lost", "exclusions_changed"} {
		if _, ok := diff[k]; !ok {
			t.Errorf("diff lacks %q: %v", k, diff)
		}
		if a, _ := diff[k].([]any); len(a) != 0 {
			t.Errorf("self-replay diff %s = %v, want empty", k, diff[k])
		}
	}
	after, _ := st.ListMindRuns(0, 0)
	if len(after) != len(before) {
		t.Errorf("a dry replay created a run row: %d → %d", len(before), len(after))
	}
}

// ---------------------------------------------------------------------------
// item DTO, source resolver, retired routes
// ---------------------------------------------------------------------------

// item DTO: `run_id`, `supersedes`, `superseded_by` additive omitempty ints;
// `episode_id` still emitted — on `/api/mm` and `/api/mm/show`.
func TestContractMRAItemDTOCarriesTheRunKeys(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	old, err := st.AddMMItem(store.MMItem{Kind: "decision", Source: "capture", Content: "old", Quote: "old q", Title: "old", CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	nw, err := st.AddMMItem(store.MMItem{Kind: "decision", Source: "nightly", Content: "new", Quote: "new q", Title: "new", CreatedAt: 2, RunID: 9, Supersedes: old})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Content: "plain", CreatedAt: 3})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SupersedeMMItem(old, nw, 10, "run 9: new q"); err != nil {
		t.Fatal(err)
	}
	show := func(id int64) map[string]any {
		return contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/show?id=%d", id), nil), 200, "GET /api/mm/show")
	}
	n := show(nw)
	if contractMRANum(n["run_id"]) != 9 || contractMRANum(n["supersedes"]) != old {
		t.Errorf("new item run_id/supersedes = %v/%v, want 9/%d", n["run_id"], n["supersedes"], old)
	}
	if _, ok := n["superseded_by"]; ok {
		t.Errorf("live item carries superseded_by (omitempty): %v", n["superseded_by"])
	}
	o := show(old)
	if contractMRANum(o["superseded_by"]) != nw {
		t.Errorf("old item superseded_by = %v, want %d", o["superseded_by"], nw)
	}
	p := show(plain)
	for _, k := range []string{"run_id", "supersedes", "superseded_by"} {
		if _, ok := p[k]; ok {
			t.Errorf("plain item carries %q (omitempty)", k)
		}
	}
	for _, m := range []map[string]any{n, o, p} {
		if _, ok := m["episode_id"]; !ok {
			t.Errorf("item %v lacks episode_id", m["id"])
		}
	}
	list := contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, "/api/mm", nil), 200, "GET /api/mm")
	arr, _ := list["items"].([]any)
	seen := false
	for _, x := range arr {
		m := x.(map[string]any)
		if contractMRANum(m["id"]) == nw {
			seen = true
			if contractMRANum(m["run_id"]) != 9 || contractMRANum(m["supersedes"]) != old {
				t.Errorf("/api/mm new item = %v", m)
			}
			if _, ok := m["episode_id"]; !ok {
				t.Errorf("/api/mm item lacks episode_id")
			}
		}
	}
	if !seen {
		t.Errorf("/api/mm lacks the new item: %v", arr)
	}
}

// `/api/mm/source`: "A pointer with saved_id resolves to the library
// snapshot's text (available: true, changed compared by sha when the pointer
// carries one); a pointer with mark_id … prefixes the mark's text".
func TestContractMRASourceResolvesSavedAndMarkPointers(t *testing.T) {
	st := contractMRAEnv(t)
	mux := contractMRAMux()
	jid := contractMRAJob(t, st, "src-job", "done")
	snap := []byte("# the saved artifact\n")
	saved, err := st.SaveArtifact(jid, "src-job", "50-saved.md", snap, 5)
	if err != nil {
		t.Fatal(err)
	}
	add := func(src string) int64 {
		id, err := st.AddMMItem(store.MMItem{Kind: "reading", Source: "nightly", Content: "c", Quote: "q", SourceJSON: src, CreatedAt: 1})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	source := func(id int64) map[string]any {
		return contractMRAWant(t, contractMRADo(t, mux, http.MethodGet, fmt.Sprintf("/api/mm/source?id=%d", id), nil), 200, "GET /api/mm/source")
	}
	same := add(fmt.Sprintf(`{"kind":"artifact","slug":"src-job","name":"50-saved.md","sha":%q,"saved_id":%d}`, store.SHA256Hex(snap), saved))
	if b := source(same); b["available"] != true || b["text"] != string(snap) || b["changed"] != false {
		t.Errorf("saved pointer (same sha) = %v", b)
	}
	moved := add(fmt.Sprintf(`{"kind":"artifact","slug":"src-job","name":"50-saved.md","sha":"0000","saved_id":%d}`, saved))
	if b := source(moved); b["available"] != true || b["text"] != string(snap) || b["changed"] != true {
		t.Errorf("saved pointer (other sha) = %v", b)
	}
	markID, _, err := st.AddMMMark(store.MMMark{JobID: jid, Slug: "src-job", Text: "THE MARKED TEXT", SourceJSON: `{"kind":"manual"}`, CreatedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	mk := add(fmt.Sprintf(`{"kind":"manual","mark_id":%d}`, markID))
	if b := source(mk); !strings.HasPrefix(fmt.Sprint(b["text"]), "THE MARKED TEXT") {
		t.Errorf("mark pointer text = %q, want it prefixed with the mark's text", b["text"])
	}
}

// `/api/mm/propose`, `/api/mm/commit`, `/api/mm/revise` "removed from
// register.go … answers 404 like any unregistered path".
func TestContractMRARetiredRoutesAre404(t *testing.T) {
	contractMRAEnv(t)
	mux := contractMRAMux()
	for _, p := range []string{"/api/mm/propose", "/api/mm/commit", "/api/mm/revise"} {
		if rec := contractMRADo(t, mux, http.MethodPost, p, map[string]any{"title": "x"}); rec.Code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", p, rec.Code)
		}
	}
}
