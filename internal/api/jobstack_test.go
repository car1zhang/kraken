package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobstack_test.go — GET/POST /api/job/stack (jobstack.go). Store-backed
// throughout: stack links have no frontmatter spelling. The obligations are
// 52-testing-plan.md's route rows: every refusal with its status and an
// unchanged store, the archived withholding, and the board key.

func seedStackJob(t *testing.T, slug, state string) *store.Store {
	t.Helper()
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: slug, Repo: "kraken", State: state}); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	return st
}

func postStack(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	var raw []byte
	switch b := body.(type) {
	case string:
		raw = []byte(b)
	default:
		raw = mustJSON(t, b)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/job/stack", bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	jobStackHandler(rec, req)
	return rec
}

func stackRows(t *testing.T, st *store.Store, slug string) []store.StackLink {
	t.Helper()
	j, err := st.GetJobBySlug(slug)
	if err != nil {
		t.Fatalf("GetJobBySlug %s: %v", slug, err)
	}
	rows, err := st.ListStacks(j.ID)
	if err != nil {
		t.Fatalf("ListStacks: %v", err)
	}
	return rows
}

func TestJobStackAddRemoveClear(t *testing.T) {
	st := seedStackJob(t, "s1", "working")
	if rec := postStack(t, map[string]any{"slug": "s1", "op": "add", "url": "http://127.0.0.1:7878", "label": "board"}); rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	if rec := postStack(t, map[string]any{"slug": "s1", "op": "add", "url": "http://localhost:5173"}); rec.Code != http.StatusOK {
		t.Fatalf("add unlabelled: %d %s", rec.Code, rec.Body.String())
	}
	rows := stackRows(t, st, "s1")
	if len(rows) != 2 || rows[0].Label != "board" || rows[1].Label != "" || rows[0].Status != jobs.StackUnknown {
		t.Fatalf("rows = %+v", rows)
	}
	// remove by label
	if rec := postStack(t, map[string]any{"slug": "s1", "op": "remove", "url": "board"}); rec.Code != http.StatusOK {
		t.Fatalf("remove by label: %d %s", rec.Code, rec.Body.String())
	}
	if rows := stackRows(t, st, "s1"); len(rows) != 1 || rows[0].URL != "http://localhost:5173" {
		t.Fatalf("after remove: %+v", rows)
	}
	// remove of nothing is 404 naming the token, never the store's bare sentence
	if rec := postStack(t, map[string]any{"slug": "s1", "op": "remove", "url": "nope"}); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "nope") || strings.Contains(rec.Body.String(), "store:") {
		t.Fatalf("remove absent: %d %s", rec.Code, rec.Body.String())
	}
	// clear, twice (second is still 200)
	for i := 0; i < 2; i++ {
		if rec := postStack(t, map[string]any{"slug": "s1", "op": "clear"}); rec.Code != http.StatusOK {
			t.Fatalf("clear %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if rows := stackRows(t, st, "s1"); len(rows) != 0 {
		t.Fatalf("after clear: %+v", rows)
	}
}

func TestJobStackRefusals(t *testing.T) {
	st := seedStackJob(t, "s2", "working")
	cases := []struct {
		name string
		body any
		code int
		want string
	}{
		{"unknown key", `{"slug":"s2","op":"add","url":"http://localhost:1","lable":"x"}`, 400, "unrecognized field"},
		{"bad json", `{"slug":`, 400, "bad json"},
		{"missing op", map[string]any{"slug": "s2"}, 400, "op must be"},
		{"bad op", map[string]any{"slug": "s2", "op": "replace"}, 400, "op must be"},
		{"add without url", map[string]any{"slug": "s2", "op": "add"}, 400, "needs a url"},
		{"javascript scheme", map[string]any{"slug": "s2", "op": "add", "url": "javascript:alert(1)"}, 400, "scheme must be"},
		{"file scheme", map[string]any{"slug": "s2", "op": "add", "url": "file:///etc/hosts"}, 400, "scheme must be"},
		{"userinfo", map[string]any{"slug": "s2", "op": "add", "url": "http://u:p@localhost:1"}, 400, "credentials"},
		{"no host", map[string]any{"slug": "s2", "op": "add", "url": "http://"}, 400, "no host"},
		{"remove without token", map[string]any{"slug": "s2", "op": "remove"}, 400, "needs a url or label"},
		{"unknown slug", map[string]any{"slug": "ghost", "op": "add", "url": "http://localhost:1"}, 404, ""},
	}
	for _, c := range cases {
		rec := postStack(t, c.body)
		if rec.Code != c.code || (c.want != "" && !strings.Contains(rec.Body.String(), c.want)) {
			t.Errorf("%s: %d %s (want %d containing %q)", c.name, rec.Code, rec.Body.String(), c.code, c.want)
		}
	}
	if rows := stackRows(t, st, "s2"); len(rows) != 0 {
		t.Fatalf("a refusal wrote a row: %+v", rows)
	}
}

func TestJobStackAddRefusedWithoutCell(t *testing.T) {
	st := useTestStore(t)
	for _, c := range []struct{ slug, state string }{{"draft-job", ""}, {"done-job", "done"}} {
		if _, err := st.CreateJob(&store.Job{Slug: c.slug, Repo: "kraken", State: c.state}); err != nil {
			t.Fatal(err)
		}
		rec := postStack(t, map[string]any{"slug": c.slug, "op": "add", "url": "http://localhost:1"})
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no running cell") {
			t.Errorf("%s add: %d %s, want 409 naming the missing cell", c.slug, rec.Code, rec.Body.String())
		}
		// clear is allowed in any state: it only ever makes the list smaller
		if rec := postStack(t, map[string]any{"slug": c.slug, "op": "clear"}); rec.Code != http.StatusOK {
			t.Errorf("%s clear: %d %s, want 200", c.slug, rec.Code, rec.Body.String())
		}
	}
	// blocked is a live cell too
	if _, err := st.CreateJob(&store.Job{Slug: "blocked-job", Repo: "kraken", State: "blocked"}); err != nil {
		t.Fatal(err)
	}
	if rec := postStack(t, map[string]any{"slug": "blocked-job", "op": "add", "url": "http://localhost:1"}); rec.Code != http.StatusOK {
		t.Errorf("blocked add: %d %s, want 200", rec.Code, rec.Body.String())
	}
}

func TestJobStackLabelTruncatedAndCapEnforced(t *testing.T) {
	st := seedStackJob(t, "s3", "working")
	long := strings.Repeat("é", jobs.StackLabelMax+10) + "\nsecond line"
	if rec := postStack(t, map[string]any{"slug": "s3", "op": "add", "url": "http://localhost:1", "label": long}); rec.Code != http.StatusOK {
		t.Fatalf("long label: %d %s", rec.Code, rec.Body.String())
	}
	rows := stackRows(t, st, "s3")
	if n := len([]rune(rows[0].Label)); n > jobs.StackLabelMax || strings.Contains(rows[0].Label, "\n") {
		t.Fatalf("label = %q (%d runes), want ≤ %d runes on one line", rows[0].Label, n, jobs.StackLabelMax)
	}
	for i := 2; i <= jobs.StackLinkMax; i++ {
		if rec := postStack(t, map[string]any{"slug": "s3", "op": "add", "url": "http://localhost:" + strings.Repeat("1", i)}); rec.Code != http.StatusOK {
			t.Fatalf("add %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	rec := postStack(t, map[string]any{"slug": "s3", "op": "add", "url": "http://localhost:9999"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "the cap") {
		t.Fatalf("ninth: %d %s, want 400 naming the cap", rec.Code, rec.Body.String())
	}
	if rows := stackRows(t, st, "s3"); len(rows) != jobs.StackLinkMax {
		t.Fatalf("rows after cap refusal = %d", len(rows))
	}
}

func TestJobStackGetShapes(t *testing.T) {
	seedStackJob(t, "s4", "working")
	get := func(q string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/job/stack"+q, nil)
		rec := httptest.NewRecorder()
		jobStackHandler(rec, req)
		return rec
	}
	if rec := get(""); rec.Code != http.StatusBadRequest {
		t.Fatalf("no slug: %d", rec.Code)
	}
	if rec := get("?slug=ghost"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown slug: %d", rec.Code)
	}
	rec := get("?slug=s4")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"stack":[]}` {
		t.Fatalf("empty get: %d %q, want {\"stack\":[]}", rec.Code, rec.Body.String())
	}
	postStack(t, map[string]any{"slug": "s4", "op": "add", "url": "http://localhost:5173", "label": "ui"})
	rec = get("?slug=s4")
	if !strings.Contains(rec.Body.String(), `"url":"http://localhost:5173"`) || !strings.Contains(rec.Body.String(), `"status":"unknown"`) {
		t.Fatalf("get after add: %s", rec.Body.String())
	}
}

func TestJobStackMethodGate(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/job/stack", nil)
	rec := httptest.NewRecorder()
	jobStackHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE: %d", rec.Code)
	}
}

// TestWebStackLinksFromNilForEmpty pins the wire's absence rule: no rows → nil
// → omitempty drops webJob.stack; the GET re-adds the empty array itself.
func TestWebStackLinksFromNilForEmpty(t *testing.T) {
	if got := webStackLinksFrom(nil); got != nil {
		t.Fatalf("nil rows → %v, want nil", got)
	}
	if got := webStackLinksFrom([]store.StackLink{}); got != nil {
		t.Fatalf("empty rows → %v, want nil", got)
	}
	got := webStackLinksFrom([]store.StackLink{{URL: "http://localhost:1", Label: "a", Status: "up", At: 7, ProbedAt: 9}})
	if len(got) != 1 || got[0] != (webStackLink{URL: "http://localhost:1", Label: "a", Status: "up", At: 7}) {
		t.Fatalf("shaped = %+v", got)
	}
}

// TestArchivedWebJobWithholdsStack: an archived job never carries the key,
// even if a row somehow survived (teardown deletes them; this pins the
// builder's own omission, the same way Load's is pinned).
func TestArchivedWebJobWithholdsStack(t *testing.T) {
	st := useTestStore(t)
	id, err := st.CreateJob(&store.Job{Slug: "arch", Repo: "kraken", State: "done"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertStack(id, "http://localhost:1", "", 1); err != nil {
		t.Fatal(err)
	}
	j, _ := st.GetJobBySlug("arch")
	a := &jobs.Job{ID: j.ID, Slug: j.Slug, State: "done"}
	wj := archivedWebJob(a, st, nil, nil)
	if wj.Stack != nil {
		t.Fatalf("archived card carries stack %+v", wj.Stack)
	}
}
