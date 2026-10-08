package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// mm_edge_test.go — the EDGE dimension of 50-contract.md's slice S3: what the
// /api/mm route family REFUSES. The decode discipline (413, unknown field, empty
// required field), the 404s, the mirror gate, the method gate, and the two
// refusals that are about MEANING rather than about shape — a decision minted
// from a question with no answer, and a remove of an id that names nothing.
// Written blind from the contract text alone — internal/api/mm.go,
// watchlist.go, register.go and cmd/kraken/cmd_mm.go were never read.
//
// The sibling blind author (athena-logic) owns the round trips, the wire item's
// key set and the `already` idempotence. Nothing here duplicates those.
//
// Every package-level identifier is prefixed `edgeMM`: this package's test
// namespace is shared with the sibling blind author and with a large existing
// suite that already owns shorter `edge…` names.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// edgeMMPostPaths is every POST arm of the family. Kept as one list because
// three separate obligations are stated for ALL of them at once — POST-only,
// mirror-gated at registration, and the shared decode discipline — and a route
// added later that misses one of the three should fail here rather than in
// whichever single-route test happened to be copied.
var edgeMMPostPaths = []string{
	"/api/mm/decision",
	"/api/mm/artifact",
	"/api/mm/element",
	"/api/mm/code",
	"/api/mm/note",
	"/api/mm/detail",
	"/api/mm/remove",
}

// edgeMMFields is each POST arm's accepted field set, from the contract's route
// table. The unknown-field refusal must NAME this set (so a client can fix
// itself) and must never echo the offending key (so a caller cannot choose the
// size or content of the response).
var edgeMMFields = map[string][]string{
	"/api/mm/decision": {"slug", "qid"},
	"/api/mm/artifact": {"slug", "home", "name"},
	"/api/mm/element":  {"slug", "home", "name", "text"},
	"/api/mm/code":     {"worktree", "file", "start_row", "text", "note"},
	"/api/mm/note":     {"text", "context"},
	"/api/mm/detail":   {"slug", "text"},
	"/api/mm/remove":   {"id"},
}

// edgeMMMux builds the real mux. Every case goes through it rather than calling
// a handler directly, so a handler that exists but is registered on the wrong
// pattern — or is not mirror-wrapped at registration — still fails.
func edgeMMMux(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func edgeMMPost(t *testing.T, mux http.Handler, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	return edgeMMPostRaw(t, mux, path, mustJSON(t, body))
}

func edgeMMPostRaw(t *testing.T, mux http.Handler, path string, raw []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func edgeMMGet(t *testing.T, mux http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// edgeMMItems reads GET /api/mm and returns the items array, requiring it to be
// an ARRAY on every call.
func edgeMMItems(t *testing.T, mux http.Handler, query string) []any {
	t.Helper()
	rec := edgeMMGet(t, mux, "/api/mm"+query)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/mm%s: status %d, body %s", query, rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET /api/mm%s body %q: %v", query, rec.Body.String(), err)
	}
	raw, present := body["items"]
	if !present {
		t.Fatalf("GET /api/mm%s has no `items` key: %s", query, rec.Body.String())
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("`items` = %#v, want an array — the contract says the array is NEVER null", raw)
	}
	return items
}

// edgeMMNote posts a valid note and returns the created item's id. The note arm
// is the family's smallest legal write — no job, no artifact, no git — which
// makes it the right fixture for the remove and snapshot cases below.
func edgeMMNote(t *testing.T, mux http.Handler, text string) int64 {
	t.Helper()
	rec := edgeMMPost(t, mux, "/api/mm/note", map[string]any{"text": text, "context": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/mm/note: status %d, body %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode note body %q: %v", rec.Body.String(), err)
	}
	id, ok := body["id"].(float64)
	if !ok || id <= 0 {
		t.Fatalf("POST /api/mm/note body = %s, want an `id`", rec.Body.String())
	}
	return int64(id)
}

func edgeMMBody(rec *httptest.ResponseRecorder) string { return rec.Body.String() }

// ---------------------------------------------------------------------------
// the mirror gate
// ---------------------------------------------------------------------------

// TestEdgeMMMirrorRefusesEveryPOSTWithTheExactBody is the cross-slice invariant
// stated verbatim: every mm POST is refused with EXACTLY `disabled in mirror
// mode`. The body is asserted by equality rather than by containment because a
// mirror board is a real board to whoever is looking at it, and this sentence is
// the only thing distinguishing "kraken refused" from "kraken broke".
//
// The refusal must also come BEFORE the body is decoded — asserted here by
// sending a body that is not JSON at all, which the same route answers with a
// 400 when the gate is open.
func TestEdgeMMMirrorRefusesEveryPOSTWithTheExactBody(t *testing.T) {
	useTestStore(t)
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	mux := edgeMMMux(t)

	for _, path := range edgeMMPostPaths {
		t.Run(path, func(t *testing.T) {
			rec := edgeMMPostRaw(t, mux, path, []byte(`{not json`))
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s: status %d, want 403 (body %s)", path, rec.Code, edgeMMBody(rec))
			}
			if got := strings.TrimSpace(edgeMMBody(rec)); got != "disabled in mirror mode" {
				t.Fatalf("%s: body = %q, want %q", path, got, "disabled in mirror mode")
			}
		})
	}
}

// TestEdgeMMMirrorLeavesTheReadsReachable is the gate's other half. A mirror
// exists to look at kraken's state, so the two GETs stay open — and the list
// route in particular, since the memory pane is the whole reason a mirror board
// renders anything about the mental model at all.
func TestEdgeMMMirrorLeavesTheReadsReachable(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	id := edgeMMNote(t, mux, "written before the mirror gate closed")

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	if items := edgeMMItems(t, mux, ""); len(items) != 1 {
		t.Errorf("GET /api/mm under --mirror returned %d items, want 1", len(items))
	}
	// the snapshot read is a GET too: a note has no snapshot, so 404 is the
	// right answer — but it must be the HANDLER's 404 rather than the gate's 403
	rec := edgeMMGet(t, mux, "/api/mm/snapshot?id=1")
	if rec.Code == http.StatusForbidden {
		t.Errorf("GET /api/mm/snapshot is mirror-gated; only the POSTs are (id %d)", id)
	}
}

// TestEdgeMMPostPathsAreMethodGated covers the other gate at registration: these
// are single-method paths, so a GET must be refused rather than falling through
// to a list. Asserted with mirror mode OFF, so a 403 here would mean the gate
// swallowed the method check.
func TestEdgeMMPostPathsAreMethodGated(t *testing.T) {
	useTestStore(t)
	SetMirrorMode(false)
	mux := edgeMMMux(t)
	for _, path := range edgeMMPostPaths {
		rec := edgeMMGet(t, mux, path)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("GET %s: status %d, want 405 (body %s)", path, rec.Code, edgeMMBody(rec))
		}
	}
}

// ---------------------------------------------------------------------------
// decode discipline
// ---------------------------------------------------------------------------

// TestEdgeMMUnknownFieldNamesTheSetAndNeverEchoesTheKey is the house rule with
// its two halves asserted together, because each is worthless alone: naming the
// accepted set is what lets a drifted client fix itself, and NOT echoing the
// offending key is what stops a caller choosing the response's contents.
func TestEdgeMMUnknownFieldNamesTheSetAndNeverEchoesTheKey(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	const offender = "zzUnexpectedKeyZz"

	for _, path := range edgeMMPostPaths {
		t.Run(path, func(t *testing.T) {
			body := map[string]any{offender: "whatever"}
			rec := edgeMMPost(t, mux, path, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, edgeMMBody(rec))
			}
			out := edgeMMBody(rec)
			if strings.Contains(out, offender) {
				t.Errorf("the refusal echoed the offending key: %s", out)
			}
			for _, field := range edgeMMFields[path] {
				if !strings.Contains(out, field) {
					t.Errorf("the refusal does not name the accepted field %q: %s", field, out)
				}
			}
		})
	}
}

// TestEdgeMMBadJSONIs400 covers the bodies that are not a JSON object at all.
// 400 rather than 500: a client bug must not read as "kraken is broken", which
// is the difference between carl re-clicking and carl opening the daemon log.
func TestEdgeMMBadJSONIs400(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	for _, path := range edgeMMPostPaths {
		for _, raw := range []string{``, `{`, `not json`, `[{"text":"x"}]`, `"text"`, `null`} {
			rec := edgeMMPostRaw(t, mux, path, []byte(raw))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s body %q: status %d, want 400 (body %s)", path, raw, rec.Code, edgeMMBody(rec))
			}
		}
	}
}

// TestEdgeMMOversizeBodyIs413 pins the 1 MB cap as its own status. Every
// legitimate body here is a few short strings — the largest, an element mark, is
// capped at 32K runes long before this — so a megabyte is a client that has gone
// wrong, and 413 (rather than the 400 a decode failure gets) is what says so.
func TestEdgeMMOversizeBodyIs413(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	huge := strings.Repeat("a", 2<<20) // 2 MB, comfortably past the cap
	for _, c := range []struct {
		path string
		body string
	}{
		{"/api/mm/note", `{"text":"` + huge + `"}`},
		{"/api/mm/detail", `{"slug":"x","text":"` + huge + `"}`},
		{"/api/mm/element", `{"slug":"x","home":"pluto","name":"a.md","text":"` + huge + `"}`},
		{"/api/mm/code", `{"worktree":"/tmp/x","file":"a.go","start_row":1,"text":"` + huge + `"}`},
	} {
		rec := edgeMMPostRaw(t, mux, c.path, []byte(c.body))
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("POST %s with a 2 MB body: status %d, want 413 (body %s)", c.path, rec.Code, edgeMMBody(rec))
		}
	}
}

// TestEdgeMMEmptyRequiredFieldNamesTheField walks each arm's required strings.
// The refusal names the ONE field that is missing rather than listing them all,
// which is what makes a 400 actionable from a CLI where the caller assembled the
// body out of shell variables that may each be empty.
func TestEdgeMMEmptyRequiredFieldNamesTheField(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	for _, c := range []struct {
		name  string
		path  string
		body  map[string]any
		field string
	}{
		{"decision without a slug", "/api/mm/decision", map[string]any{"slug": "", "qid": "Q1"}, "slug"},
		{"decision without a qid", "/api/mm/decision", map[string]any{"slug": "a-job", "qid": ""}, "qid"},
		{"artifact without a name", "/api/mm/artifact", map[string]any{"slug": "a-job", "home": "pluto", "name": ""}, "name"},
		{"artifact without a slug", "/api/mm/artifact", map[string]any{"slug": "", "home": "pluto", "name": "a.md"}, "slug"},
		{"element without text", "/api/mm/element", map[string]any{"slug": "a-job", "home": "pluto", "name": "a.md", "text": ""}, "text"},
		{"note without text", "/api/mm/note", map[string]any{"text": "", "context": "some context"}, "text"},
		{"detail without text", "/api/mm/detail", map[string]any{"slug": "a-job", "text": ""}, "text"},
		{"code without a worktree", "/api/mm/code", map[string]any{"worktree": "", "file": "a.go", "start_row": 1}, "worktree"},
		{"code without a file", "/api/mm/code", map[string]any{"worktree": "/tmp/wt", "file": "", "start_row": 1}, "file"},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeMMPost(t, mux, c.path, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400 (body %s)", rec.Code, edgeMMBody(rec))
			}
			if !strings.Contains(edgeMMBody(rec), c.field) {
				t.Errorf("the refusal does not name %q: %s", c.field, edgeMMBody(rec))
			}
		})
	}
	if items := edgeMMItems(t, mux, ""); len(items) != 0 {
		t.Errorf("refused requests created %d items", len(items))
	}
}

// TestEdgeMMCodeRefusesPathShapes covers the two path rules and the row floor.
// A relative worktree cannot be resolved to a repo at all; an absolute `file`
// would escape the worktree; and start_row is 1-BASED, so a 0 is an editor
// integration that has handed over a zero-based row — which would anchor every
// mark one line above what carl selected.
func TestEdgeMMCodeRefusesPathShapes(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"relative worktree", map[string]any{"worktree": "Developer/kraken", "file": "a.go", "start_row": 1}},
		{"bare worktree", map[string]any{"worktree": "kraken", "file": "a.go", "start_row": 1}},
		{"absolute file", map[string]any{"worktree": "/tmp/wt", "file": "/etc/passwd", "start_row": 1}},
		{"start_row 0", map[string]any{"worktree": "/tmp/wt", "file": "a.go", "start_row": 0}},
		{"start_row negative", map[string]any{"worktree": "/tmp/wt", "file": "a.go", "start_row": -3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeMMPost(t, mux, "/api/mm/code", c.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (body %s)", rec.Code, edgeMMBody(rec))
			}
		})
	}
}

// TestEdgeMMTextCapsRefuseWhatIsObviouslyOver is the safe half of the two length
// caps: an element mark is capped at 32K runes and a note/detail at 8K, so a
// body an order of magnitude past either is a refusal under any reading of "K".
// The exact boundary is pinned separately below, because the contract does not
// say whether K is 1000 or 1024.
func TestEdgeMMTextCapsRefuseWhatIsObviouslyOver(t *testing.T) {
	st := useTestStore(t)
	mux := edgeMMMux(t)
	edgeMMSeedArtifactJob(t, st, "cap-job", "50-contract.md", "a doc")

	for _, c := range []struct {
		name string
		path string
		body map[string]any
	}{
		{"element far over 32K", "/api/mm/element", map[string]any{
			"slug": "cap-job", "home": "pluto", "name": "50-contract.md", "text": strings.Repeat("e", 40_000)}},
		{"note far over 8K", "/api/mm/note", map[string]any{"text": strings.Repeat("n", 12_000)}},
		{"detail far over 8K", "/api/mm/detail", map[string]any{"slug": "cap-job", "text": strings.Repeat("d", 12_000)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeMMPost(t, mux, c.path, c.body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("status %d, want 400 (body %s)", rec.Code, edgeMMBody(rec))
			}
		})
	}
}

// TestEdgeMMTextCapsFallOnTheDecimalThousand pins the exact boundary. The
// contract writes "≤32K runes" and "≤8K" without saying which K, and this repo's
// only precedents are decimal (feedbackNoteMaxLen = 8000, docAnswerMaxLen =
// 4000), so 32000/8000 is the reading asserted here. The counted unit is RUNES
// rather than bytes, which the multi-byte fixture makes load-bearing: 32000 é
// characters are 64000 bytes, so a byte-counting cap refuses a legal mark.
func TestEdgeMMTextCapsFallOnTheDecimalThousand(t *testing.T) {
	st := useTestStore(t)
	mux := edgeMMMux(t)
	edgeMMSeedArtifactJob(t, st, "cap-edge-job", "50-contract.md", "a doc")

	for _, c := range []struct {
		name     string
		path     string
		body     map[string]any
		wantCode int
	}{
		{"element at 32000 runes", "/api/mm/element", map[string]any{
			"slug": "cap-edge-job", "home": "pluto", "name": "50-contract.md",
			"text": strings.Repeat("é", 32_000)}, http.StatusOK},
		{"element at 32001 runes", "/api/mm/element", map[string]any{
			"slug": "cap-edge-job", "home": "pluto", "name": "50-contract.md",
			"text": strings.Repeat("é", 32_001)}, http.StatusBadRequest},
		{"note at 8000 runes", "/api/mm/note", map[string]any{
			"text": strings.Repeat("é", 8_000)}, http.StatusOK},
		{"note at 8001 runes", "/api/mm/note", map[string]any{
			"text": strings.Repeat("é", 8_001)}, http.StatusBadRequest},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeMMPost(t, mux, c.path, c.body)
			if rec.Code != c.wantCode {
				t.Errorf("status %d, want %d (body %s)", rec.Code, c.wantCode, edgeMMBody(rec))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// decision: the refusal that is about meaning
// ---------------------------------------------------------------------------

func edgeMMSeedArtifactJob(t *testing.T, st *store.Store, slug, name, body string) *jobs.Job {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "done"})
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: name, Bytes: []byte(body), Mtime: 1}}, nil); err != nil {
		t.Fatalf("SyncArtifacts: %v", err)
	}
	return j
}

func edgeMMSeedQuestion(t *testing.T, st *store.Store, jobID int64, qid, answer string) {
	t.Helper()
	if err := st.UpsertQuestion(jobID, store.DocQuestion{
		QID:            qid,
		Text:           "Should the watchlist become its own table?",
		Context:        "the overlay rides an edge today",
		Importance:     "P0",
		Recommendation: "yes, standalone",
		Position:       1,
	}); err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	if answer != "" {
		if err := st.AnswerQuestion(jobID, qid, answer, "2026-08-19T00:00:00Z"); err != nil {
			t.Fatalf("AnswerQuestion: %v", err)
		}
	}
}

// TestEdgeMMDecisionOfAnUnansweredQuestionIsRefused is the family's one refusal
// that is not about shape: the payload is perfectly well formed and names a real
// question, and it is still wrong, because an unanswered question is a decision
// that has not been TAKEN. Minting one would freeze a question as a thing carl
// knows.
func TestEdgeMMDecisionOfAnUnansweredQuestionIsRefused(t *testing.T) {
	st := useTestStore(t)
	mux := edgeMMMux(t)
	j := edgeMMSeedArtifactJob(t, st, "unanswered-job", "00-overview.md", "x")
	edgeMMSeedQuestion(t, st, j.ID, "Q1", "")

	rec := edgeMMPost(t, mux, "/api/mm/decision", map[string]any{"slug": "unanswered-job", "qid": "Q1"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (body %s)", rec.Code, edgeMMBody(rec))
	}
	if items := edgeMMItems(t, mux, ""); len(items) != 0 {
		t.Errorf("the refused decision created %d items — a refusal writes nothing", len(items))
	}
	// and an answer that is only whitespace is no answer either
	edgeMMSeedQuestion(t, st, j.ID, "Q2", "   ")
	rec = edgeMMPost(t, mux, "/api/mm/decision", map[string]any{"slug": "unanswered-job", "qid": "Q2"})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("a whitespace-only answer: status %d, want 400 (body %s)", rec.Code, edgeMMBody(rec))
	}
}

// TestEdgeMMDecisionUnknownJobOrQIDIs404 separates "the body is wrong" from "the
// thing you named is not here". Both arms matter to the board: the ✓ sits on a
// row the client is looking at, so a 404 says the view is stale, while a 400
// would say the client is broken.
func TestEdgeMMDecisionUnknownJobOrQIDIs404(t *testing.T) {
	st := useTestStore(t)
	mux := edgeMMMux(t)
	j := edgeMMSeedArtifactJob(t, st, "known-job", "00-overview.md", "x")
	edgeMMSeedQuestion(t, st, j.ID, "Q1", "yes")

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"unknown job", map[string]any{"slug": "no-such-job-anywhere", "qid": "Q1"}},
		{"unknown qid", map[string]any{"slug": "known-job", "qid": "Q99"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeMMPost(t, mux, "/api/mm/decision", c.body)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status %d, want 404 (body %s)", rec.Code, edgeMMBody(rec))
			}
		})
	}
}

// TestEdgeMMArtifactUnknownJobOrArtifactIs404 is the same separation on the
// artifact arm, where the miss is likelier: names come from a url the client
// already holds, so a miss means the doc was renamed or the job archived out from
// under the open overlay.
func TestEdgeMMArtifactUnknownJobOrArtifactIs404(t *testing.T) {
	st := useTestStore(t)
	mux := edgeMMMux(t)
	edgeMMSeedArtifactJob(t, st, "artifact-job", "00-overview.md", "the overview")

	for _, c := range []struct {
		name string
		body map[string]any
	}{
		{"unknown job", map[string]any{"slug": "no-such-job", "home": "pluto", "name": "00-overview.md"}},
		{"unknown artifact", map[string]any{"slug": "artifact-job", "home": "pluto", "name": "99-missing.md"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			rec := edgeMMPost(t, mux, "/api/mm/artifact", c.body)
			if rec.Code != http.StatusNotFound {
				t.Errorf("status %d, want 404 (body %s)", rec.Code, edgeMMBody(rec))
			}
		})
	}
}

// ---------------------------------------------------------------------------
// remove and snapshot
// ---------------------------------------------------------------------------

// TestEdgeMMRemoveUnknownIs404AndSecondRemoveIs200 is the store's one-way door
// seen through the route. Three answers, all of them saying something different:
// a positive id that names nothing is a stale view (404), an id ≤0 is a body the
// route cannot read (400), and an id that is already invalidated is simply
// already done (200) — answering 404 to that last one would make a double-click
// look like an error.
func TestEdgeMMRemoveUnknownIs404AndSecondRemoveIs200(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	id := edgeMMNote(t, mux, "a note to remove twice")

	// Two different refusals, per the amended contract. A POSITIVE id that names
	// no row is a stale view — 404. An id of 0 or below is a malformed BODY: JSON
	// cannot distinguish an explicit 0 from an absent key for a plain int, so the
	// decode discipline's "empty-required-field → 400 naming the field" is what
	// answers, and a negative is the same malformed shape.
	for _, c := range []struct {
		ghost    any
		wantCode int
	}{
		{id + 1000, http.StatusNotFound},
		{0, http.StatusBadRequest},
		{-1, http.StatusBadRequest},
	} {
		rec := edgeMMPost(t, mux, "/api/mm/remove", map[string]any{"id": c.ghost})
		if rec.Code != c.wantCode {
			t.Errorf("remove id %v: status %d, want %d (body %s)", c.ghost, rec.Code, c.wantCode, edgeMMBody(rec))
		}
	}

	if rec := edgeMMPost(t, mux, "/api/mm/remove", map[string]any{"id": id}); rec.Code != http.StatusOK {
		t.Fatalf("first remove: status %d, body %s", rec.Code, edgeMMBody(rec))
	}
	if rec := edgeMMPost(t, mux, "/api/mm/remove", map[string]any{"id": id}); rec.Code != http.StatusOK {
		t.Errorf("second remove: status %d, want 200 — invalidation is idempotent (body %s)", rec.Code, edgeMMBody(rec))
	}
	// and the item is still SERVED, just not live: an invalidated row is the
	// pane's whole "what you used to know" view
	if items := edgeMMItems(t, mux, ""); len(items) != 1 {
		t.Errorf("GET /api/mm returned %d items after the remove, want 1 — a remove invalidates, it never deletes", len(items))
	}
	if live := edgeMMItems(t, mux, "?live=1"); len(live) != 0 {
		t.Errorf("GET /api/mm?live=1 returned %d items after the remove, want 0", len(live))
	}
}

// TestEdgeMMSnapshotMissingIs404 covers every way the snapshot read can miss,
// and the line the amended contract draws through them. An id that PARSES and
// names no row, and a real row that simply has no snapshot (every note, decision
// and code reading — most of the table), are both 404: the question was
// well-formed and the answer is "not here". An id that is absent, non-numeric or
// ≤0 never asked a question at all, and is a 400.
func TestEdgeMMSnapshotMissingIs404(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	id := edgeMMNote(t, mux, "a note has no snapshot")

	for _, c := range []struct {
		q        string
		wantCode int
	}{
		// a well-formed id naming no row: the caller asked a sensible question
		// about a row that is not there
		{"?id=999999", http.StatusNotFound},
		// every shape that is not a usable id at all is a malformed REQUEST, per
		// the amended contract — the same ≤0/non-numeric rule /api/mm/remove takes
		{"?id=0", http.StatusBadRequest},
		{"?id=-1", http.StatusBadRequest},
		{"?id=abc", http.StatusBadRequest},
		{"?id=", http.StatusBadRequest},
		{"", http.StatusBadRequest},
	} {
		rec := edgeMMGet(t, mux, "/api/mm/snapshot"+c.q)
		if rec.Code != c.wantCode {
			t.Errorf("GET /api/mm/snapshot%s: status %d, want %d (body %s)", c.q, rec.Code, c.wantCode, edgeMMBody(rec))
		}
	}
	rec := edgeMMGet(t, mux, "/api/mm/snapshot?id="+strconv.FormatInt(id, 10))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET /api/mm/snapshot for an item with no snapshot: status %d, want 404 (body %s)", rec.Code, edgeMMBody(rec))
	}
}

// ---------------------------------------------------------------------------
// the empty state
// ---------------------------------------------------------------------------

// TestEdgeMMListIsAnArrayOnAnEmptyDatabase is the state every install is in
// before the first mark, and the one the pane renders first. `items` must be an
// ARRAY rather than null — a null makes `items.map` throw in the client, so the
// pane's empty state would render as a blank screen rather than as "nothing
// marked yet".
func TestEdgeMMListIsAnArrayOnAnEmptyDatabase(t *testing.T) {
	useTestStore(t)
	mux := edgeMMMux(t)
	if items := edgeMMItems(t, mux, ""); len(items) != 0 {
		t.Errorf("a fresh database returned %d items", len(items))
	}
	if items := edgeMMItems(t, mux, "?live=1"); len(items) != 0 {
		t.Errorf("a fresh database returned %d live items", len(items))
	}
}
