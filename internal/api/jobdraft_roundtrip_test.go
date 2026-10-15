package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/store"
)

// jobdraft_roundtrip_test.go pins the one property iris' edit instruction
// rests on: the text `/api/job/raw` prints can be sent back through
// `/api/job/draft` with a change applied. Raw prints kraken's own keys
// beside the authored ones, so an edit built from it CARRIES them, while
// slack_edge_test.go's draft matrix posts `state:` only as a refusal row and
// never posts `session`, `run`, `prs` or `color` at all.

// roundtripRaw reads one job through GET /api/job/raw on the real mux.
func roundtripRaw(t *testing.T, mux http.Handler, slug string) (content string, mtime int64) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/job/raw?slug="+slug, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("raw %s: status %d, body %s", slug, rec.Code, rec.Body.String())
	}
	var out struct {
		Content string `json:"content"`
		MTime   int64  `json:"mtime"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("raw %s: decode: %v (body %s)", slug, err, rec.Body.String())
	}
	return out.Content, out.MTime
}

// roundtripSeed stores one draft carrying every kind of field raw prints:
// authored ones, kraken-owned ones, and a PR. Its colour is a real palette
// name, because raw's text goes back through the strict parser.
func roundtripSeed(t *testing.T, st *store.Store, slug string) *store.Job {
	t.Helper()
	j := &store.Job{
		Slug: slug, Repo: "kraken", Base: "main", Solo: true,
		Model: "sonnet", Color: "cyan", Session: "aaaa-bbbb-cccc", Run: 3,
		Body: "# Spec\n\noriginal body\n\n## Source\n\noriginal source\n",
	}
	if _, err := st.CreateJob(j); err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	if err := st.UpsertPR(j.ID, "https://github.com/example/kraken/pull/7", "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}
	return edgeJob(t, st, slug)
}

// TestJobDraftAcceptsRawTextRoundTrip: read a draft, amend its body, send the
// whole text back under the mtime that read reported. The edit lands, and
// every field the route does not own is exactly what it was.
func TestJobDraftAcceptsRawTextRoundTrip(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	before := roundtripSeed(t, st, "roundtrip-job")

	content, mtime := roundtripRaw(t, mux, "roundtrip-job")
	// The premise: raw's text carries keys the draft route does not write. If
	// it ever stops printing them this test stops exercising the round trip.
	for _, key := range []string{"run: 3", "session: aaaa-bbbb-cccc", "color: cyan", "state: open"} {
		if !strings.Contains(content, key) {
			t.Fatalf("raw text lacks %q, so this is not the round trip it claims to be:\n%s", key, content)
		}
	}
	if !strings.Contains(content, "original body") {
		t.Fatalf("raw text lacks the stored body:\n%s", content)
	}

	amended := strings.Replace(content, "original body", "original body\n\nADDED by the edit", 1)
	// Move the kraken-owned values the text carries. Sent back unchanged they
	// would equal the stored ones whether the route ignored them or wrote them
	// through; changed, only "ignored" leaves the row as it was.
	amended = strings.Replace(amended, "session: aaaa-bbbb-cccc", "session: stale-from-iris", 1)
	amended = strings.Replace(amended, "run: 3", "run: 9", 1)
	amended = strings.Replace(amended, "color: cyan", "color: red", 1)
	amended = strings.Replace(amended, "state: open", "state: merged", 1)
	rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{
		"slug": "roundtrip-job", "content": amended, "mtime": mtime,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 — raw's own text, body amended and kraken's values moved, was refused (body %s)",
			rec.Code, rec.Body.String())
	}

	after := edgeJob(t, st, "roundtrip-job")
	if !strings.Contains(after.Body, "ADDED by the edit") || !strings.Contains(after.Body, "original body") {
		t.Errorf("body after the edit:\n%s", after.Body)
	}
	if !strings.Contains(after.Body, "original source") {
		t.Errorf("the Source section did not survive the round trip:\n%s", after.Body)
	}
	if after.Repo != before.Repo || after.Base != before.Base || after.Solo != before.Solo || after.Model != before.Model {
		t.Errorf("authored fields moved on a body-only edit: before %+v, after %+v", before, after)
	}
	if after.State != before.State || after.Session != before.Session || after.Run != before.Run ||
		after.Color != before.Color || after.Outcome != before.Outcome || after.Teardown != before.Teardown {
		t.Errorf("kraken-owned fields moved: before %+v, after %+v", before, after)
	}
	afterPRs, err := st.ListPRs(after.ID)
	if err != nil {
		t.Fatalf("ListPRs: %v", err)
	}
	if len(afterPRs) != 1 || afterPRs[0].State != "open" {
		t.Errorf("prs after the edit = %+v, want the one stored PR, still open", afterPRs)
	}
}

// TestJobDraftRoundTripCarriesBaseAndAgent: base and agent are authored keys
// the route WRITES. Raw prints both when they are set, so an edit that keeps
// the lines keeps the fields, and one that drops them resets both.
func TestJobDraftRoundTripCarriesBaseAndAgent(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	j := &store.Job{
		Slug: "carry-job", Repo: "kraken", Base: "release-2", Agent: "codex",
		Body: "# Spec\n\noriginal body\n\n## Source\n\noriginal source\n",
	}
	if _, err := st.CreateJob(j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}

	content, mtime := roundtripRaw(t, mux, "carry-job")
	for _, line := range []string{"base: release-2\n", "agent: codex\n"} {
		if !strings.Contains(content, line) {
			t.Fatalf("raw text lacks %q:\n%s", line, content)
		}
	}

	kept := strings.Replace(content, "original body", "original body\n\nADDED by the edit", 1)
	rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{
		"slug": "carry-job", "content": kept, "mtime": mtime,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	after := edgeJob(t, st, "carry-job")
	if after.Base != "release-2" || after.Agent != "codex" {
		t.Errorf("an edit that kept both lines stored base=%q agent=%q, want release-2 and codex", after.Base, after.Agent)
	}

	content, mtime = roundtripRaw(t, mux, "carry-job")
	dropped := strings.Replace(content, "base: release-2\n", "", 1)
	dropped = strings.Replace(dropped, "agent: codex\n", "", 1)
	rec = edgePostAPI(t, mux, "/api/job/draft", map[string]any{
		"slug": "carry-job", "content": dropped, "mtime": mtime,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	after = edgeJob(t, st, "carry-job")
	if after.Base != "" || after.Agent != "" {
		t.Errorf("an edit that dropped both lines stored base=%q agent=%q, want both reset", after.Base, after.Agent)
	}
}

// TestJobDraftRoundTripBodyIsStable: sending raw's text back UNCHANGED stores
// the same body. An edit that amends what raw printed must not pick up drift
// from the serializer on the way through.
func TestJobDraftRoundTripBodyIsStable(t *testing.T) {
	st, mux := edgeSlackAPI(t)
	before := roundtripSeed(t, st, "stable-job")

	content, mtime := roundtripRaw(t, mux, "stable-job")
	rec := edgePostAPI(t, mux, "/api/job/draft", map[string]any{
		"slug": "stable-job", "content": content, "mtime": mtime,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	after := edgeJob(t, st, "stable-job")
	if strings.TrimSpace(after.Body) != strings.TrimSpace(before.Body) {
		t.Errorf("an unchanged round trip moved the body:\nbefore:\n%s\nafter:\n%s", before.Body, after.Body)
	}
}
