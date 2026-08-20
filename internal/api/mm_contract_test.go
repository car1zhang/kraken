package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
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

// mm_contract_test.go — athena-logic's BLIND contract tests for
// artifacts/50-contract.md, slice S3's route table: the nine `/api/mm` paths,
// the `webMMItem` wire shape, the idempotent `already` arms and the mirror
// posture's READ half.
//
// Written from the frozen contract alone. internal/api/mm.go,
// internal/api/watchlist.go and internal/api/register.go were never read by any
// route — no Read, no grep, no git show, no build output quoting them. The
// harness this file leans on (useTestStore, mustCreateStoreJob, mustJSON) is
// pre-existing package state at the base commit.
//
// Every route is driven through the REAL mux (api.Register), the same wiring
// web.Serve uses, so a handler that exists but is registered on the wrong
// pattern still fails.
//
// Dimension: LOGIC — the stated shapes, the compositions and the happy paths.
// The decode-discipline arms (1 MB -> 413, DisallowUnknownFields -> 400 naming
// the accepted field set, empty-required-field -> 400) and the mirror REFUSAL
// body are athena-edge's ground and are deliberately not duplicated here; the
// mirror READ arm IS here, because "GET stays reachable under --mirror" is a
// stated liveness rather than a refusal.
//
// Every package-level identifier declared here is prefixed `contractMM`.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

func contractMMMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func contractMMGET(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractMMPOST(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(mustJSON(t, body)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func contractMMDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

// contractMMCreated drives one create arm and requires the stated 200 shape:
// `{"id":N,"episode":E}` on EVERY successful create, since the episode id is
// what a caller dereferences with `k graph get`.
func contractMMCreated(t *testing.T, mux http.Handler, path string, body any) (id int64, episode int64, already bool) {
	t.Helper()
	rec := contractMMPOST(t, mux, path, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST %s %s: status %d, body %s", path, mustJSON(t, body), rec.Code, rec.Body.String())
	}
	obj := contractMMDecode(t, rec)
	raw, ok := obj["id"]
	if !ok {
		t.Fatalf("POST %s body = %s, want an `id`", path, rec.Body.String())
	}
	n, ok := raw.(float64)
	if !ok || int64(n) <= 0 {
		t.Fatalf("POST %s `id` = %#v, want a real row id", path, raw)
	}
	already, _ = obj["already"].(bool)
	if ep, present := obj["episode"]; present {
		f, ok := ep.(float64)
		if !ok {
			t.Fatalf("POST %s `episode` = %#v, want a number", path, ep)
		}
		episode = int64(f)
	} else if !already {
		t.Fatalf("POST %s body = %s, want `episode` on every successful create", path, rec.Body.String())
	}
	return int64(n), episode, already
}

// contractMMRemove drives POST /api/mm/remove and requires a plain 200. Its
// response shape is not frozen (remove is not a create, so the
// `{"id":N,"episode":E}` clause does not reach it), so nothing here reads the
// body.
func contractMMRemove(t *testing.T, mux http.Handler, id int64) {
	t.Helper()
	rec := contractMMPOST(t, mux, "/api/mm/remove", map[string]any{"id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/mm/remove {id:%d}: status %d, body %s", id, rec.Code, rec.Body.String())
	}
}

// contractMMItems drives GET /api/mm (optionally ?live=1) and returns the raw
// items array — loose maps rather than a struct, because the contract pins key
// PRESENCE and ABSENCE, which a typed decode with omitempty would paper over.
func contractMMItems(t *testing.T, mux http.Handler, query string) []map[string]any {
	t.Helper()
	rec := contractMMGET(t, mux, "/api/mm"+query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm%s: status %d, body %s", query, rec.Code, rec.Body.String())
	}
	body := contractMMDecode(t, rec)
	raw, ok := body["items"]
	if !ok {
		t.Fatalf("GET /api/mm%s has no `items` key: %s", query, rec.Body.String())
	}
	arr, ok := raw.([]any)
	if !ok {
		t.Fatalf("`items` = %#v, want an array — the contract says the array is never null", raw)
	}
	out := make([]map[string]any, 0, len(arr))
	for _, e := range arr {
		obj, ok := e.(map[string]any)
		if !ok {
			t.Fatalf("item %#v is not an object", e)
		}
		out = append(out, obj)
	}
	return out
}

func contractMMItemByID(t *testing.T, items []map[string]any, id int64) map[string]any {
	t.Helper()
	for _, obj := range items {
		if n, ok := obj["id"].(float64); ok && int64(n) == id {
			return obj
		}
	}
	t.Fatalf("item %d absent from the list: %s", id, mustJSON(t, items))
	return nil
}

func contractMMStr(t *testing.T, obj map[string]any, key string) string {
	t.Helper()
	raw, ok := obj[key]
	if !ok {
		t.Fatalf("key %q absent from %s", key, mustJSON(t, obj))
	}
	s, ok := raw.(string)
	if !ok {
		t.Fatalf("key %q = %#v, want a string", key, raw)
	}
	return s
}

// contractMMSeedJob creates a store-backed job with its artifacts mirrored into
// the artifacts table — the block-event sync's own write path, which is what
// the shared `resolveArtifact` seam reads for a non-live job's "pluto" home.
func contractMMSeedJob(t *testing.T, st *store.Store, slug string, files map[string]string) *jobs.Job {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "done"})
	arts := make([]store.ArtifactFile, 0, len(files))
	for name, body := range files {
		arts = append(arts, store.ArtifactFile{Path: name, Bytes: []byte(body), Mtime: 1})
	}
	if err := st.SyncArtifacts(j.ID, arts, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	return j
}

func contractMMSHA(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ---------------------------------------------------------------------------
// GET /api/mm
// ---------------------------------------------------------------------------

// TestContractMMGETAlwaysServesAnArray: the pane maps over this
// unconditionally, so an empty mental model is `[]`, never null.
func TestContractMMGETAlwaysServesAnArray(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)

	rec := contractMMGET(t, mux, "/api/mm")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm on an empty mental model: status %d, body %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("body = %s, want items serialized as [] (never null)", rec.Body.String())
	}
	if got := contractMMItems(t, mux, ""); len(got) != 0 {
		t.Fatalf("items = %#v, want empty", got)
	}
}

// TestContractMMGETCarriesTheFrozenKeySetAndOmitsTheLiveOnes walks the
// webMMItem row: the always-present keys are always present (a live item shows
// no invalid_* and no anchor), and the invalidation keys appear exactly when
// the item is dead.
func TestContractMMGETCarriesTheFrozenKeySetAndOmitsTheLiveOnes(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)

	id, _, _ := contractMMCreated(t, mux, "/api/mm/note", map[string]any{
		"text": "the graph is downstream of the mental model", "context": "Q9",
	})

	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	for _, key := range []string{
		"id", "kind", "source", "slug", "source_ref", "content", "context", "created_at", "episode_id",
	} {
		if _, ok := obj[key]; !ok {
			t.Errorf("key %q absent from a live webMMItem: %s", key, mustJSON(t, obj))
		}
	}
	for _, key := range []string{"invalidated_at", "invalid_reason", "invalid_detail", "anchor"} {
		if _, ok := obj[key]; ok {
			t.Errorf("key %q present on a LIVE, anchorless item (%#v) — absent is how the wire says live",
				key, obj[key])
		}
	}
	if contractMMStr(t, obj, "kind") != "note" || contractMMStr(t, obj, "source") != "manual" {
		t.Errorf("a note came back kind/source %q/%q, want note/manual",
			contractMMStr(t, obj, "kind"), contractMMStr(t, obj, "source"))
	}
	if contractMMStr(t, obj, "content") != "the graph is downstream of the mental model" {
		t.Errorf("content = %q — it is served WHOLE", contractMMStr(t, obj, "content"))
	}
	if contractMMStr(t, obj, "context") != "Q9" {
		t.Errorf("context = %q, want Q9", contractMMStr(t, obj, "context"))
	}
	if n, ok := obj["episode_id"].(float64); !ok || int64(n) <= 0 {
		t.Errorf("episode_id = %#v, want the minted episode's id", obj["episode_id"])
	}

	// Kill it, and the three invalidation keys arrive together.
	contractMMRemove(t, mux, id)
	dead := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if n, ok := dead["invalidated_at"].(float64); !ok || int64(n) <= 0 {
		t.Errorf("invalidated_at = %#v on a removed item, want its stamp", dead["invalidated_at"])
	}
	if contractMMStr(t, dead, "invalid_reason") != "removed" {
		t.Errorf("invalid_reason = %q, want removed", contractMMStr(t, dead, "invalid_reason"))
	}
	if contractMMStr(t, dead, "content") != "the graph is downstream of the mental model" {
		t.Error("the dead item's frozen content changed — the pane renders what carl KNEW")
	}
}

// TestContractMMGETLiveFilterAndOrder pins `?live=1` and "all items newest
// first".
func TestContractMMGETLiveFilterAndOrder(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)

	first, _, _ := contractMMCreated(t, mux, "/api/mm/note", map[string]any{"text": "one", "context": ""})
	second, _, _ := contractMMCreated(t, mux, "/api/mm/note", map[string]any{"text": "two", "context": ""})
	third, _, _ := contractMMCreated(t, mux, "/api/mm/note", map[string]any{"text": "three", "context": ""})

	all := contractMMItems(t, mux, "")
	if len(all) != 3 {
		t.Fatalf("GET /api/mm returned %d items, want 3", len(all))
	}
	// newest first: the last created leads.
	if n, _ := all[0]["id"].(float64); int64(n) != third {
		t.Errorf("first item = %v, want the newest (%d)", all[0]["id"], third)
	}
	if n, _ := all[2]["id"].(float64); int64(n) != first {
		t.Errorf("last item = %v, want the oldest (%d)", all[2]["id"], first)
	}

	contractMMRemove(t, mux, second)

	live := contractMMItems(t, mux, "?live=1")
	if len(live) != 2 {
		t.Fatalf("GET /api/mm?live=1 returned %d items, want 2", len(live))
	}
	for _, obj := range live {
		if n, _ := obj["id"].(float64); int64(n) == second {
			t.Fatal("?live=1 served an invalidated item")
		}
	}
	if len(contractMMItems(t, mux, "")) != 3 {
		t.Fatal("the unfiltered list dropped the invalidated item — `all items` means all")
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/note and /api/mm/detail
// ---------------------------------------------------------------------------

// TestContractMMNoteIsKindNoteSourceManual: the quick-add's whole contract.
func TestContractMMNoteIsKindNoteSourceManual(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)

	id, ep, _ := contractMMCreated(t, mux, "/api/mm/note", map[string]any{
		"text": "agents never write the mental model", "context": "Q5",
	})
	if ep <= 0 {
		t.Fatalf("episode = %d, want the minted episode's id", ep)
	}
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if contractMMStr(t, obj, "kind") != "note" {
		t.Errorf("kind = %q, want note", contractMMStr(t, obj, "kind"))
	}
	if contractMMStr(t, obj, "source") != "manual" {
		t.Errorf("source = %q, want manual", contractMMStr(t, obj, "source"))
	}
	if contractMMStr(t, obj, "content") != "agents never write the mental model" {
		t.Errorf("content = %q", contractMMStr(t, obj, "content"))
	}
	if contractMMStr(t, obj, "context") != "Q5" {
		t.Errorf("context = %q, want Q5", contractMMStr(t, obj, "context"))
	}
}

// TestContractMMDetailKeysOnItsOwnTextHash pins the steer-modal arm (Q1): there
// is no artifact and no question behind a rendered detail line, so its identity
// IS its text — the first 16 hex of its sha256 — which is what makes the second
// press of the same button idempotent rather than a duplicate row.
func TestContractMMDetailKeysOnItsOwnTextHash(t *testing.T) {
	st := useTestStore(t)
	mux := contractMMMux(t)
	contractMMSeedJob(t, st, "steered", nil)

	const detail = "zeus: waiting on the manual-test gate; the PR is open and green"
	id, _, already := contractMMCreated(t, mux, "/api/mm/detail", map[string]any{
		"slug": "steered", "text": detail,
	})
	if already {
		t.Fatal("the first detail mark reported already=true")
	}
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if contractMMStr(t, obj, "kind") != "reading" {
		t.Errorf("kind = %q, want reading", contractMMStr(t, obj, "kind"))
	}
	if contractMMStr(t, obj, "source") != "agent-output" {
		t.Errorf("source = %q, want agent-output", contractMMStr(t, obj, "source"))
	}
	want := contractMMSHA(detail)[:16]
	if got := contractMMStr(t, obj, "source_ref"); got != want {
		t.Errorf("source_ref = %q, want the first 16 hex of sha256(text) %q", got, want)
	}
	if contractMMStr(t, obj, "content") != detail {
		t.Errorf("content = %q, want the detail text verbatim", contractMMStr(t, obj, "content"))
	}
	if contractMMStr(t, obj, "slug") != "steered" {
		t.Errorf("slug = %q, want steered", contractMMStr(t, obj, "slug"))
	}

	again, _, already := contractMMCreated(t, mux, "/api/mm/detail", map[string]any{
		"slug": "steered", "text": detail,
	})
	if !already || again != id {
		t.Fatalf("re-marking the same detail = (%d, already=%t), want (%d, true)", again, already, id)
	}
	if got := contractMMItems(t, mux, ""); len(got) != 1 {
		t.Fatalf("%d items after two identical detail marks, want 1", len(got))
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/decision
// ---------------------------------------------------------------------------

// TestContractMMDecisionFreezesTheWholeQuestion pins the composition rule
// exactly: `<q.text>` + "\n→ " + `<q.answer>`, context from the question, and
// the recommendation appended as "\nrecommendation: <r>" only when it is
// non-empty. Getting this wrong is silent — the row still exists, it just says
// something other than what carl decided.
func TestContractMMDecisionFreezesTheWholeQuestion(t *testing.T) {
	st := useTestStore(t)
	mux := contractMMMux(t)
	j := contractMMSeedJob(t, st, "decided", nil)

	if err := st.UpsertQuestion(j.ID, store.DocQuestion{
		QID: "Q3", Text: "Does an edit anywhere in a marked artifact invalidate the mark?",
		Context:    "the whole-file arm compares snapshot bytes",
		Importance: "P0", Recommendation: "yes, any byte change",
	}); err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	if err := st.AnswerQuestion(j.ID, "Q3", "yes — any byte change", "2026-08-19T10:00:00Z"); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	id, ep, _ := contractMMCreated(t, mux, "/api/mm/decision", map[string]any{"slug": "decided", "qid": "Q3"})
	if ep <= 0 {
		t.Fatalf("episode = %d", ep)
	}
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if contractMMStr(t, obj, "kind") != "decision" {
		t.Errorf("kind = %q, want decision", contractMMStr(t, obj, "kind"))
	}
	if contractMMStr(t, obj, "source") != "question" {
		t.Errorf("source = %q, want question", contractMMStr(t, obj, "source"))
	}
	if contractMMStr(t, obj, "source_ref") != "Q3" {
		t.Errorf("source_ref = %q, want the qid Q3", contractMMStr(t, obj, "source_ref"))
	}
	wantContent := "Does an edit anywhere in a marked artifact invalidate the mark?" +
		"\n→ " + "yes — any byte change" +
		"\nrecommendation: " + "yes, any byte change"
	if got := contractMMStr(t, obj, "content"); got != wantContent {
		t.Errorf("content =\n%q\nwant\n%q", got, wantContent)
	}
	if got := contractMMStr(t, obj, "context"); got != "the whole-file arm compares snapshot bytes" {
		t.Errorf("context = %q, want the question's context", got)
	}
	if contractMMStr(t, obj, "slug") != "decided" {
		t.Errorf("slug = %q, want decided", contractMMStr(t, obj, "slug"))
	}

	// Carl's own columns on the question row are untouched by the copy.
	qs, err := st.ListQuestions(j.ID)
	if err != nil {
		t.Fatalf("ListQuestions: %v", err)
	}
	if len(qs) != 1 || qs[0].Answer != "yes — any byte change" || qs[0].AnsweredAt != "2026-08-19T10:00:00Z" {
		t.Fatalf("the decision copy rewrote the question row: %+v", qs)
	}

	// Idempotent: the same quad answers with the existing id.
	again, _, already := contractMMCreated(t, mux, "/api/mm/decision", map[string]any{"slug": "decided", "qid": "Q3"})
	if !already || again != id {
		t.Fatalf("re-marking the same decision = (%d, already=%t), want (%d, true)", again, already, id)
	}
	if got := contractMMItems(t, mux, ""); len(got) != 1 {
		t.Fatalf("%d items after two identical decision marks, want 1", len(got))
	}
}

// TestContractMMDecisionOmitsAnEmptyRecommendation: the "recommendation: " line
// is conditional, and a question with none must not carry an empty one — the
// content is hashed into the episode, so a stray blank line is a second
// spelling of the same decision.
func TestContractMMDecisionOmitsAnEmptyRecommendation(t *testing.T) {
	st := useTestStore(t)
	mux := contractMMMux(t)
	j := contractMMSeedJob(t, st, "plain", nil)

	if err := st.UpsertQuestion(j.ID, store.DocQuestion{
		QID: "Q1", Text: "Which table owns the watchlist?", Importance: "P1",
	}); err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	if err := st.AnswerQuestion(j.ID, "Q1", "its own", "2026-08-19T10:00:00Z"); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}

	id, _, _ := contractMMCreated(t, mux, "/api/mm/decision", map[string]any{"slug": "plain", "qid": "Q1"})
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	want := "Which table owns the watchlist?\n→ its own"
	if got := contractMMStr(t, obj, "content"); got != want {
		t.Fatalf("content = %q, want %q — no recommendation line when there is no recommendation", got, want)
	}
	if got := contractMMStr(t, obj, "context"); got != "" {
		t.Errorf("context = %q, want empty (the question carried none)", got)
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/artifact and GET /api/mm/snapshot
// ---------------------------------------------------------------------------

// TestContractMMArtifactFreezesBytesAndServesThemBack walks the whole-artifact
// arm: the bytes are frozen at mark time, source_ref is the name with its
// leading slash trimmed, content IS the source_ref, and the snapshot comes back
// with the stored ext's MIME.
func TestContractMMArtifactFreezesBytesAndServesThemBack(t *testing.T) {
	st := useTestStore(t)
	mux := contractMMMux(t)
	const frozen = "<html><body>the report as carl read it</body></html>"
	j := contractMMSeedJob(t, st, "frozen", map[string]string{"90-build-report.html": frozen})

	id, ep, already := contractMMCreated(t, mux, "/api/mm/artifact", map[string]any{
		"slug": "frozen", "home": "pluto", "name": "90-build-report.html",
	})
	if already {
		t.Fatal("the first artifact mark reported already=true")
	}
	if ep <= 0 {
		t.Fatalf("episode = %d", ep)
	}

	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if contractMMStr(t, obj, "kind") != "reading" || contractMMStr(t, obj, "source") != "artifact" {
		t.Errorf("kind/source = %q/%q, want reading/artifact",
			contractMMStr(t, obj, "kind"), contractMMStr(t, obj, "source"))
	}
	if got := contractMMStr(t, obj, "source_ref"); got != "90-build-report.html" {
		t.Errorf("source_ref = %q, want the name with any leading slash trimmed", got)
	}
	if got := contractMMStr(t, obj, "content"); got != "90-build-report.html" {
		t.Errorf("content = %q, want the source_ref — the BYTES ride the snapshot, never the list", got)
	}
	if strings.Contains(contractMMStr(t, obj, "content"), "the report as carl read it") {
		t.Fatal("the frozen bytes are on the list payload — the contract says they never ride it")
	}

	rec := contractMMGET(t, mux, fmt.Sprintf("/api/mm/snapshot?id=%d", id))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm/snapshot?id=%d: status %d, body %s", id, rec.Code, rec.Body.String())
	}
	if rec.Body.String() != frozen {
		t.Fatalf("snapshot body = %q, want the bytes frozen at mark time %q", rec.Body.String(), frozen)
	}
	if got, want := rec.Header().Get("Content-Type"), artifactMimeType("90-build-report.html"); got != want {
		t.Errorf("snapshot Content-Type = %q, want the stored ext's MIME %q", got, want)
	}

	// The freeze is a SNAPSHOT: rewriting the artifact leaves the served bytes
	// exactly as they were.
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "90-build-report.html", Bytes: []byte("<html>rewritten</html>"), Mtime: 2},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts (rewrite): %v", err)
	}
	rec = contractMMGET(t, mux, fmt.Sprintf("/api/mm/snapshot?id=%d", id))
	if rec.Body.String() != frozen {
		t.Fatalf("snapshot body = %q after the artifact was rewritten, want the FROZEN %q", rec.Body.String(), frozen)
	}
}

// TestContractMMArtifactRemarkIsAlreadyThenSupersedes pins the two arms the
// contract distinguishes by BYTES: an identical mark is `already` (no second
// row), and a mark of changed bytes supersedes the live rows for that
// (source, job, name) before inserting a fresh one.
func TestContractMMArtifactRemarkIsAlreadyThenSupersedes(t *testing.T) {
	st := useTestStore(t)
	mux := contractMMMux(t)
	j := contractMMSeedJob(t, st, "resend", map[string]string{"00-overview.md": "# v1\n"})

	first, _, _ := contractMMCreated(t, mux, "/api/mm/artifact", map[string]any{
		"slug": "resend", "home": "pluto", "name": "00-overview.md",
	})
	same, _, already := contractMMCreated(t, mux, "/api/mm/artifact", map[string]any{
		"slug": "resend", "home": "pluto", "name": "00-overview.md",
	})
	if !already || same != first {
		t.Fatalf("an identical artifact mark = (%d, already=%t), want (%d, true)", same, already, first)
	}
	if got := contractMMItems(t, mux, ""); len(got) != 1 {
		t.Fatalf("%d items after two identical artifact marks, want 1", len(got))
	}

	// Different bytes: the old mark is superseded, and a new row records what
	// carl has now read.
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{
		{Path: "00-overview.md", Bytes: []byte("# v2\n"), Mtime: 2},
	}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	second, _, already := contractMMCreated(t, mux, "/api/mm/artifact", map[string]any{
		"slug": "resend", "home": "pluto", "name": "00-overview.md",
	})
	if already {
		t.Fatal("a mark of CHANGED bytes reported already=true")
	}
	if second == first {
		t.Fatal("a mark of changed bytes reused the old row — the old reading is a different reading")
	}
	items := contractMMItems(t, mux, "")
	if len(items) != 2 {
		t.Fatalf("%d items after the re-mark, want 2 (the superseded one and the fresh one)", len(items))
	}
	old := contractMMItemByID(t, items, first)
	if contractMMStr(t, old, "invalid_reason") != "superseded" {
		t.Errorf("the previous mark's invalid_reason = %q, want superseded", contractMMStr(t, old, "invalid_reason"))
	}
	fresh := contractMMItemByID(t, items, second)
	if _, ok := fresh["invalidated_at"]; ok {
		t.Errorf("the fresh mark reads invalidated: %s", mustJSON(t, fresh))
	}
	// Each snapshot stayed with its own row.
	if body := contractMMGET(t, mux, fmt.Sprintf("/api/mm/snapshot?id=%d", first)).Body.String(); body != "# v1\n" {
		t.Errorf("the superseded row's snapshot = %q, want the v1 bytes it froze", body)
	}
	if body := contractMMGET(t, mux, fmt.Sprintf("/api/mm/snapshot?id=%d", second)).Body.String(); body != "# v2\n" {
		t.Errorf("the fresh row's snapshot = %q, want the v2 bytes", body)
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/element
// ---------------------------------------------------------------------------

// TestContractMMElementMarksExactlyTheSelectedText: a prose selection, a table
// or a figure — the element's own text is the anchor, so the content IS the
// text rather than the file.
func TestContractMMElementMarksExactlyTheSelectedText(t *testing.T) {
	st := useTestStore(t)
	mux := contractMMMux(t)
	contractMMSeedJob(t, st, "elements", map[string]string{
		"50-components.md": "# components\n\n| table | mm_items |\n\nprose.\n",
	})

	const selected = "| table | mm_items |"
	id, ep, already := contractMMCreated(t, mux, "/api/mm/element", map[string]any{
		"slug": "elements", "home": "pluto", "name": "50-components.md", "text": selected,
	})
	if already {
		t.Fatal("the first element mark reported already=true")
	}
	if ep <= 0 {
		t.Fatalf("episode = %d", ep)
	}
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if contractMMStr(t, obj, "kind") != "reading" || contractMMStr(t, obj, "source") != "artifact-element" {
		t.Errorf("kind/source = %q/%q, want reading/artifact-element",
			contractMMStr(t, obj, "kind"), contractMMStr(t, obj, "source"))
	}
	if got := contractMMStr(t, obj, "source_ref"); got != "50-components.md" {
		t.Errorf("source_ref = %q, want the trimmed artifact name", got)
	}
	if got := contractMMStr(t, obj, "content"); got != selected {
		t.Errorf("content = %q, want the selected text %q", got, selected)
	}

	again, _, already := contractMMCreated(t, mux, "/api/mm/element", map[string]any{
		"slug": "elements", "home": "pluto", "name": "50-components.md", "text": selected,
	})
	if !already || again != id {
		t.Fatalf("re-marking the same element = (%d, already=%t), want (%d, true)", again, already, id)
	}

	// A DIFFERENT selection out of the same artifact is a different reading.
	other, _, already := contractMMCreated(t, mux, "/api/mm/element", map[string]any{
		"slug": "elements", "home": "pluto", "name": "50-components.md", "text": "prose.",
	})
	if already || other == id {
		t.Fatal("a different selection out of the same artifact folded onto the first — the element's TEXT is the identity")
	}
}

// TestContractMMElementNormalizesCRLFBeforeStoring: the stored content is what
// the verify pass substring-matches against `\r\n`-normalized artifact bytes,
// so a selection captured with CRLF must be normalized on the way IN or it can
// never match again.
func TestContractMMElementNormalizesCRLFBeforeStoring(t *testing.T) {
	st := useTestStore(t)
	mux := contractMMMux(t)
	contractMMSeedJob(t, st, "crlf", map[string]string{"a.md": "one\ntwo\nthree\n"})

	id, _, _ := contractMMCreated(t, mux, "/api/mm/element", map[string]any{
		"slug": "crlf", "home": "pluto", "name": "a.md", "text": "one\r\ntwo",
	})
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if got := contractMMStr(t, obj, "content"); got != "one\ntwo" {
		t.Fatalf("content = %q, want the \\r\\n-normalized %q", got, "one\ntwo")
	}
	// ... and the normalized form is the identity, so the CRLF and LF spellings
	// of one selection are ONE mark.
	again, _, already := contractMMCreated(t, mux, "/api/mm/element", map[string]any{
		"slug": "crlf", "home": "pluto", "name": "a.md", "text": "one\ntwo",
	})
	if !already || again != id {
		t.Fatalf("the LF spelling of the same selection = (%d, already=%t), want (%d, true)", again, already, id)
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/code
// ---------------------------------------------------------------------------

// contractMMWorktree writes one file into a throwaway directory that is NOT a
// git checkout, which is what drives the anchor ladder to its bottom rung.
func contractMMWorktree(t *testing.T, name, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return dir
}

// TestContractMMCodeMarkLandsAsADirtyAnchorOutsideACheckout walks the code arm
// on the rung every other rung falls back to: no origin/main, no merge-base, no
// HEAD, so the anchor is `dirty` with an EMPTY anchor_sha (never a fabricated
// one) and the stored text is the normalized buffer.
func TestContractMMCodeMarkLandsAsADirtyAnchorOutsideACheckout(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)
	wt := contractMMWorktree(t, "notes.go", "package p\n\nfunc f() {\n\treturn\n}\n")
	repo := filepath.Base(wt)

	const selection = "func f() {\n\treturn\n}"
	id, ep, already := contractMMCreated(t, mux, "/api/mm/code", map[string]any{
		"worktree": wt, "file": "notes.go", "start_row": 3, "text": selection, "note": "the whole function",
	})
	if already {
		t.Fatal("the first code mark reported already=true")
	}
	if ep <= 0 {
		t.Fatalf("episode = %d", ep)
	}

	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if contractMMStr(t, obj, "kind") != "reading" || contractMMStr(t, obj, "source") != "code" {
		t.Errorf("kind/source = %q/%q, want reading/code",
			contractMMStr(t, obj, "kind"), contractMMStr(t, obj, "source"))
	}
	// end row = start + newlines(selection) = 3 + 2 = 5 (no trailing newline)
	wantRef := fmt.Sprintf("%s/notes.go:3-5", repo)
	if got := contractMMStr(t, obj, "source_ref"); got != wantRef {
		t.Errorf("source_ref = %q, want %q (repo/file:start-end)", got, wantRef)
	}
	if got := contractMMStr(t, obj, "content"); got != selection {
		t.Errorf("content = %q, want the stored text %q", got, selection)
	}
	if got := contractMMStr(t, obj, "context"); got != "the whole function" {
		t.Errorf("context = %q, want the note", got)
	}

	raw, ok := obj["anchor"]
	if !ok {
		t.Fatalf("a code item carries no `anchor` object: %s", mustJSON(t, obj))
	}
	anchor, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("`anchor` = %#v, want an object", raw)
	}
	if got, _ := anchor["anchor_kind"].(string); got != "dirty" {
		t.Errorf("anchor_kind = %q, want dirty — no commit rung can answer outside a checkout", got)
	}
	if got, present := anchor["anchor_sha"]; present && got != "" {
		t.Errorf("anchor_sha = %#v on a dirty anchor, want empty — never a fabricated SHA", got)
	}
	if got, _ := anchor["repo"].(string); got != repo {
		t.Errorf("anchor repo = %q, want the worktree basename %q", got, repo)
	}
	if got, _ := anchor["path"].(string); got != "notes.go" {
		t.Errorf("anchor path = %q, want the relative file", got)
	}
	if got, _ := anchor["start_line"].(float64); int64(got) != 3 {
		t.Errorf("anchor start_line = %v, want 3", anchor["start_line"])
	}
	if got, _ := anchor["end_line"].(float64); int64(got) != 5 {
		t.Errorf("anchor end_line = %v, want 5", anchor["end_line"])
	}
	if got, _ := anchor["text_sha"].(string); got != contractMMSHA(selection) {
		t.Errorf("anchor text_sha = %q, want sha256 of the stored text", got)
	}

	// Idempotent on (repo/path/range/text_sha) — the Zed keystroke fires twice
	// more often than anyone would like.
	again, _, already := contractMMCreated(t, mux, "/api/mm/code", map[string]any{
		"worktree": wt, "file": "notes.go", "start_row": 3, "text": selection, "note": "the whole function",
	})
	if !already || again != id {
		t.Fatalf("re-marking the same lines = (%d, already=%t), want (%d, true)", again, already, id)
	}
	if got := contractMMItems(t, mux, ""); len(got) != 1 {
		t.Fatalf("%d items after two identical code marks, want 1", len(got))
	}
}

// TestContractMMCodeMarkWithNoSelectionIsTheSingleLineAtStartRow pins the
// server-side decision the Zed task depends on: the keystroke with no selection
// means "this one line", read off disk rather than guessed.
func TestContractMMCodeMarkWithNoSelectionIsTheSingleLineAtStartRow(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)
	wt := contractMMWorktree(t, "a.go", "package p\n\nconst answer = 42\n\nfunc f() {}\n")
	repo := filepath.Base(wt)

	id, _, _ := contractMMCreated(t, mux, "/api/mm/code", map[string]any{
		"worktree": wt, "file": "a.go", "start_row": 3, "text": "", "note": "",
	})
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if got := contractMMStr(t, obj, "content"); got != "const answer = 42" {
		t.Fatalf("content = %q, want the single line at start_row read from disk", got)
	}
	wantRef := fmt.Sprintf("%s/a.go:3-3", repo)
	if got := contractMMStr(t, obj, "source_ref"); got != wantRef {
		t.Fatalf("source_ref = %q, want %q — one line means start == end", got, wantRef)
	}
}

// TestContractMMCodeMarkWholeLineSelectionDoesNotOvershoot: the Zed selection
// carries its trailing newline, and counting it would push every whole-line
// mark one line past where carl was looking.
func TestContractMMCodeMarkWholeLineSelectionDoesNotOvershoot(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)
	wt := contractMMWorktree(t, "a.go", "one\ntwo\nthree\nfour\n")
	repo := filepath.Base(wt)

	id, _, _ := contractMMCreated(t, mux, "/api/mm/code", map[string]any{
		"worktree": wt, "file": "a.go", "start_row": 2, "text": "two\nthree\n", "note": "",
	})
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	wantRef := fmt.Sprintf("%s/a.go:2-3", repo)
	if got := contractMMStr(t, obj, "source_ref"); got != wantRef {
		t.Fatalf("source_ref = %q, want %q — a whole-line selection ends on its LAST line", got, wantRef)
	}
}

// ---------------------------------------------------------------------------
// POST /api/mm/remove
// ---------------------------------------------------------------------------

// TestContractMMRemoveInvalidatesWithReasonRemoved pins carl's own delete: it
// is an invalidation like any other, so the row stays and says who killed it.
func TestContractMMRemoveInvalidatesWithReasonRemoved(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)
	id, _, _ := contractMMCreated(t, mux, "/api/mm/note", map[string]any{"text": "wrong", "context": ""})

	rec := contractMMPOST(t, mux, "/api/mm/remove", map[string]any{"id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/mm/remove: status %d, body %s", rec.Code, rec.Body.String())
	}
	obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id)
	if got := contractMMStr(t, obj, "invalid_reason"); got != "removed" {
		t.Errorf("invalid_reason = %q, want removed", got)
	}
	if _, ok := obj["invalidated_at"]; !ok {
		t.Error("a removed item carries no invalidated_at")
	}

	// "already-invalidated 200" — and the first verdict stands.
	rec = contractMMPOST(t, mux, "/api/mm/remove", map[string]any{"id": id})
	if rec.Code != http.StatusOK {
		t.Fatalf("a second remove: status %d, body %s — already-invalidated is 200", rec.Code, rec.Body.String())
	}
	if got := contractMMItems(t, mux, "?live=1"); len(got) != 0 {
		t.Fatalf("?live=1 = %s after the removal, want empty", mustJSON(t, got))
	}
}

// ---------------------------------------------------------------------------
// mirror mode: the READ half
// ---------------------------------------------------------------------------

// TestContractMMGETStaysReachableUnderMirror pins the liveness half of the
// mirror posture: a mirror board is read-only, not blind. Reading the mental
// model is a read.
func TestContractMMGETStaysReachableUnderMirror(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)
	id, _, _ := contractMMCreated(t, mux, "/api/mm/note", map[string]any{"text": "visible", "context": ""})

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	rec := contractMMGET(t, mux, "/api/mm")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm under --mirror: status %d, body %s", rec.Code, rec.Body.String())
	}
	if obj := contractMMItemByID(t, contractMMItems(t, mux, ""), id); contractMMStr(t, obj, "content") != "visible" {
		t.Fatal("the mirrored list did not carry the item")
	}
	if rec := contractMMGET(t, mux, "/api/mm?live=1"); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm?live=1 under --mirror: status %d", rec.Code)
	}
	// The snapshot read is a read too.
	if rec := contractMMGET(t, mux, fmt.Sprintf("/api/mm/snapshot?id=%d", id)); rec.Code == http.StatusForbidden {
		t.Fatal("GET /api/mm/snapshot is mirror-gated — the POST arms are gated, the reads are not")
	}
}

// TestContractMMBoardPayloadCarriesNoMMKeys is the cross-slice invariant the
// whole surface is designed around: marks ride their OWN route and their own
// poll, exactly like the library, so nothing here lands in the 3-second board
// payload.
func TestContractMMBoardPayloadCarriesNoMMKeys(t *testing.T) {
	useTestStore(t)
	mux := contractMMMux(t)
	contractMMCreated(t, mux, "/api/mm/note", map[string]any{"text": "not on the board", "context": ""})

	rec := contractMMGET(t, mux, "/api/board")
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/board: status %d, body %s", rec.Code, rec.Body.String())
	}
	for _, forbidden := range []string{`"mm"`, `"mm_items"`, `"mental_model"`, `"marks"`} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Errorf("the board payload carries %s — mm state rides its own routes and poll", forbidden)
		}
	}
}
