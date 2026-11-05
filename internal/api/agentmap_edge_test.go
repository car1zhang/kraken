package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// agentmap_edge_test.go — athena-edge's blind suite for the agent map's wire
// (50-contract.md G5, G7): every 400/404/405 the three routes promise, the
// mirror gate on the two POSTs, never-null empty answers, the message route's
// recorded:false refusals, and talksFor's omissions and tie-breaks.

func agentMapEdgeMux(t *testing.T) (*http.ServeMux, *store.Store) {
	t.Helper()
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	st := useTestStore(t)
	mux := http.NewServeMux()
	Register(mux)
	return mux, st
}

func agentMapEdgeDo(t *testing.T, mux http.Handler, method, target, body, fromCell string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	if fromCell != "" {
		req.Header.Set(fromCellHeader, fromCell)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func agentMapEdgeMessages(t *testing.T, st *store.Store) []store.JobMessage {
	t.Helper()
	rows, err := st.ListJobMessages()
	if err != nil {
		t.Fatalf("ListJobMessages: %v", err)
	}
	return rows
}

// --- GET /api/job/events ------------------------------------------------------

func TestAgentMapEdgeEventsRouteStatuses(t *testing.T) {
	mux, st := agentMapEdgeMux(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "quiet", Repo: "kraken", State: "working", Body: "spec\n"})

	for _, tc := range []struct {
		name, method, target string
		want                 int
	}{
		{"missing slug", http.MethodGet, "/api/job/events", http.StatusBadRequest},
		{"empty slug", http.MethodGet, "/api/job/events?slug=", http.StatusBadRequest},
		{"unknown slug", http.MethodGet, "/api/job/events?slug=nobody-here", http.StatusNotFound},
		{"POST", http.MethodPost, "/api/job/events?slug=quiet", http.StatusMethodNotAllowed},
		{"PUT", http.MethodPut, "/api/job/events?slug=quiet", http.StatusMethodNotAllowed},
		{"DELETE", http.MethodDelete, "/api/job/events?slug=quiet", http.StatusMethodNotAllowed},
	} {
		if rec := agentMapEdgeDo(t, mux, tc.method, tc.target, "", ""); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
		}
	}
}

// A job with nothing to read answers 200 with events [] — never null — and
// that holds for the n extremes too.
func TestAgentMapEdgeEventsRouteEmptyIsArray(t *testing.T) {
	mux, st := agentMapEdgeMux(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "quiet", Repo: "kraken", State: "working", Body: "spec\n"})
	for _, target := range []string{
		"/api/job/events?slug=quiet",
		"/api/job/events?slug=quiet&n=0",
		"/api/job/events?slug=quiet&n=-4",
		"/api/job/events?slug=quiet&n=100000",
	} {
		rec := agentMapEdgeDo(t, mux, http.MethodGet, target, "", "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body.String())
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
			t.Fatalf("%s: body %q: %v", target, rec.Body.String(), err)
		}
		if got := strings.TrimSpace(string(raw["events"])); got != "[]" {
			t.Errorf("%s: events = %s, want []", target, got)
		}
	}
}

// The events route is ungated: a mirror serve still answers it.
func TestAgentMapEdgeEventsRouteUngatedInMirror(t *testing.T) {
	mux, st := agentMapEdgeMux(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "quiet", Repo: "kraken", State: "working", Body: "spec\n"})
	SetMirrorMode(true)
	if rec := agentMapEdgeDo(t, mux, http.MethodGet, "/api/job/events?slug=quiet", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("mirror GET: %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// --- POST /api/job/message ----------------------------------------------------

func agentMapEdgeMessageJobs(t *testing.T, st *store.Store) {
	t.Helper()
	mustCreateStoreJob(t, st, store.Job{Slug: "alpha", Repo: "kraken", State: "working", Body: "spec\n"})
	mustCreateStoreJob(t, st, store.Job{Slug: "beta", Repo: "kraken", State: "blocked", Body: "spec\n"})
}

func TestAgentMapEdgeMessageRouteStatuses(t *testing.T) {
	mux, st := agentMapEdgeMux(t)
	agentMapEdgeMessageJobs(t, st)
	for _, tc := range []struct {
		name, method, body, from string
		want                     int
	}{
		{"GET", http.MethodGet, "", "alpha", http.StatusMethodNotAllowed},
		{"PUT", http.MethodPut, `{"to":"zeus-beta"}`, "alpha", http.StatusMethodNotAllowed},
		{"malformed", http.MethodPost, `{"to":`, "alpha", http.StatusBadRequest},
		{"not an object", http.MethodPost, `["zeus-beta"]`, "alpha", http.StatusBadRequest},
		{"to not a string", http.MethodPost, `{"to":42}`, "alpha", http.StatusBadRequest},
		{"unknown key", http.MethodPost, `{"to":"zeus-beta","message":"hi"}`, "alpha", http.StatusBadRequest},
		{"empty to", http.MethodPost, `{"to":""}`, "alpha", http.StatusBadRequest},
		{"no to", http.MethodPost, `{}`, "alpha", http.StatusBadRequest},
		{"missing header", http.MethodPost, `{"to":"zeus-beta"}`, "", http.StatusBadRequest},
	} {
		if rec := agentMapEdgeDo(t, mux, tc.method, "/api/job/message", tc.body, tc.from); rec.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.name, rec.Code, rec.Body.String(), tc.want)
		}
	}
	if rows := agentMapEdgeMessages(t, st); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none after refusals", rows)
	}
}

func agentMapEdgeRecorded(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s, want 200", rec.Code, rec.Body.String())
	}
	var out struct {
		Recorded *bool `json:"recorded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.Recorded == nil {
		t.Fatalf("body %q: want {\"recorded\":bool} (%v)", rec.Body.String(), err)
	}
	return *out.Recorded
}

// Unresolvable sender, unresolvable target, an archived job on either side,
// and a self-send all answer 200 recorded:false and store nothing.
func TestAgentMapEdgeMessageRouteNotRecorded(t *testing.T) {
	mux, st := agentMapEdgeMux(t)
	agentMapEdgeMessageJobs(t, st)
	gone := mustCreateStoreJob(t, st, store.Job{Slug: "gone", Repo: "kraken", State: "done", Body: "spec\n"})
	if _, err := st.ArchiveJob(gone.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	for _, tc := range []struct{ name, from, to string }{
		{"unknown sender", "nobody-here", "zeus-beta"},
		{"unknown target", "alpha", "zeus-nobody"},
		{"target not a zeus name", "alpha", "beta"},
		{"target a teammate", "alpha", "heph-1"},
		{"self", "alpha", "zeus-alpha"},
		{"archived target", "alpha", "zeus-gone"},
		{"archived sender", "gone", "zeus-alpha"},
	} {
		body := `{"to":"` + tc.to + `"}`
		if agentMapEdgeRecorded(t, agentMapEdgeDo(t, mux, http.MethodPost, "/api/job/message", body, tc.from)) {
			t.Errorf("%s: recorded true, want false", tc.name)
		}
	}
	if rows := agentMapEdgeMessages(t, st); len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// The sender header is trimmed before it is resolved.
func TestAgentMapEdgeMessageRouteTrimsHeader(t *testing.T) {
	mux, st := agentMapEdgeMux(t)
	agentMapEdgeMessageJobs(t, st)
	if !agentMapEdgeRecorded(t, agentMapEdgeDo(t, mux, http.MethodPost, "/api/job/message", `{"to":"zeus-beta"}`, "  alpha\t")) {
		t.Fatal("padded header: recorded false, want true")
	}
	rows := agentMapEdgeMessages(t, st)
	alpha := jobs.JobBySlug(jobs.LoadJobs(), "alpha")
	beta := jobs.JobBySlug(jobs.LoadJobs(), "beta")
	if len(rows) != 1 || rows[0].FromID != alpha.ID || rows[0].ToID != beta.ID || rows[0].N != 1 {
		t.Fatalf("rows = %+v, want one alpha->beta row", rows)
	}
}

// Both POSTs are mirror-gated: refused, nothing stored.
func TestAgentMapEdgePostsRefusedInMirror(t *testing.T) {
	mux, st := agentMapEdgeMux(t)
	agentMapEdgeMessageJobs(t, st)
	SetMirrorMode(true)
	if rec := agentMapEdgeDo(t, mux, http.MethodPost, "/api/job/message", `{"to":"zeus-beta"}`, "alpha"); rec.Code != http.StatusForbidden {
		t.Errorf("mirror message POST: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := agentMapEdgeDo(t, mux, http.MethodPost, "/api/map/positions", `{"positions":{"a":[1,2]}}`, ""); rec.Code != http.StatusForbidden {
		t.Errorf("mirror positions POST: %d %s, want 403", rec.Code, rec.Body.String())
	}
	// GET positions stays open under the mirror
	if rec := agentMapEdgeDo(t, mux, http.MethodGet, "/api/map/positions", "", ""); rec.Code != http.StatusOK {
		t.Errorf("mirror positions GET: %d %s, want 200", rec.Code, rec.Body.String())
	}
	SetMirrorMode(false)
	if rows := agentMapEdgeMessages(t, st); len(rows) != 0 {
		t.Errorf("rows = %+v, want none", rows)
	}
	if got := jobs.MapPositions(); len(got) != 0 {
		t.Errorf("positions = %v, want none", got)
	}
}

// --- /api/map/positions -------------------------------------------------------

func agentMapEdgePositionsBody(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("GET positions: %d %s", rec.Code, rec.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("body %q: %v", rec.Body.String(), err)
	}
	return strings.TrimSpace(string(raw["positions"]))
}

func TestAgentMapEdgePositionsGetEmptyIsObject(t *testing.T) {
	mux, _ := agentMapEdgeMux(t)
	if got := agentMapEdgePositionsBody(t, agentMapEdgeDo(t, mux, http.MethodGet, "/api/map/positions", "", "")); got != "{}" {
		t.Fatalf("positions = %s, want {}", got)
	}
}

func TestAgentMapEdgePositionsMethodNotAllowed(t *testing.T) {
	mux, _ := agentMapEdgeMux(t)
	for _, m := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if rec := agentMapEdgeDo(t, mux, m, "/api/map/positions", `{"positions":{"a":[1,2]}}`, ""); rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s: %d, want 405", m, rec.Code)
		}
	}
}

// Every refusal is a 400 and leaves the saved set alone.
func TestAgentMapEdgePositionsPostRefusals(t *testing.T) {
	mux, _ := agentMapEdgeMux(t)
	if rec := agentMapEdgeDo(t, mux, http.MethodPost, "/api/map/positions", `{"positions":{"kept":[1,2]}}`, ""); rec.Code != http.StatusOK {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body.String())
	}
	var many strings.Builder
	many.WriteString(`{"positions":{`)
	for i := 0; i < 2001; i++ {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`"k` + strconv.Itoa(i) + `":[0,0]`)
	}
	many.WriteString(`}}`)
	for _, tc := range []struct{ name, body string }{
		{"malformed", `{"positions":`},
		{"not an object", `[1,2]`},
		{"unknown key", `{"positions":{"a":[1,2]},"extra":true}`},
		{"positions wrong type", `{"positions":[1,2]}`},
		{"coordinate not a number", `{"positions":{"a":["x",2]}}`},
		{"empty changes", `{"positions":{}}`},
		{"null changes", `{"positions":null}`},
		{"no positions", `{}`},
		{"empty key", `{"positions":{"":[1,2]}}`},
		{"201-byte key", `{"positions":{"` + strings.Repeat("k", 201) + `":[1,2]}}`},
		// amendment 6: only a changes map that ITSELF holds over 2,000 keys is refused
		{"2001 keys in one body", many.String()},
		{"coordinate past 1e7", `{"positions":{"a":[10000000.5,0]}}`},
		{"coordinate far negative", `{"positions":{"a":[0,-1e300]}}`},
	} {
		if rec := agentMapEdgeDo(t, mux, http.MethodPost, "/api/map/positions", tc.body, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", tc.name, rec.Code, rec.Body.String())
		}
	}
	want := map[string][2]float64{"kept": {1, 2}}
	if got := jobs.MapPositions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("saved = %v, want %v", got, want)
	}
}

// A POST merges: a second write keeps the first's keys; the answer is ok:true.
func TestAgentMapEdgePositionsPostMerges(t *testing.T) {
	mux, _ := agentMapEdgeMux(t)
	for _, body := range []string{`{"positions":{"a":[1,2]}}`, `{"positions":{"b":[-3.5,4]}}`, `{"positions":{"a":[9,9]}}`} {
		rec := agentMapEdgeDo(t, mux, http.MethodPost, "/api/map/positions", body, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
		var out struct {
			OK *bool `json:"ok"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || out.OK == nil || !*out.OK {
			t.Fatalf("%s: body %q, want {\"ok\":true}", body, rec.Body.String())
		}
	}
	var got struct {
		Positions map[string][2]float64 `json:"positions"`
	}
	rec := agentMapEdgeDo(t, mux, http.MethodGet, "/api/map/positions", "", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]float64{"a": {9, 9}, "b": {-3.5, 4}}
	if !reflect.DeepEqual(got.Positions, want) {
		t.Fatalf("positions = %v, want %v", got.Positions, want)
	}
}

// --- talksFor -----------------------------------------------------------------

// Nothing to say is nil: no rows, rows not naming the job, and rows whose
// peer is not in peers.
func TestAgentMapEdgeTalksForNil(t *testing.T) {
	peers := map[int64]webSpawnLink{2: {Slug: "beta", State: "working"}}
	if got := talksFor(1, nil, peers); got != nil {
		t.Errorf("no rows: %+v, want nil", got)
	}
	unrelated := []store.JobMessage{{FromID: 2, ToID: 3, N: 4, FirstAt: 1, LastAt: 9}}
	if got := talksFor(1, unrelated, peers); got != nil {
		t.Errorf("unrelated rows: %+v, want nil", got)
	}
	unknownPeer := []store.JobMessage{{FromID: 1, ToID: 7, N: 1, FirstAt: 1, LastAt: 1}, {FromID: 7, ToID: 1, N: 2, FirstAt: 2, LastAt: 2}}
	if got := talksFor(1, unknownPeer, peers); got != nil {
		t.Errorf("peer missing from peers: %+v, want nil", got)
	}
	if got := talksFor(1, unknownPeer, nil); got != nil {
		t.Errorf("nil peers: %+v, want nil", got)
	}
}

// One direction only: the other count is 0, LastAt the row's own.
func TestAgentMapEdgeTalksForOneDirection(t *testing.T) {
	peers := map[int64]webSpawnLink{2: {Slug: "beta", State: "working"}, 3: {Slug: "gamma", State: "blocked"}}
	msgs := []store.JobMessage{
		{FromID: 1, ToID: 2, N: 5, FirstAt: 10, LastAt: 50},
		{FromID: 3, ToID: 1, N: 2, FirstAt: 20, LastAt: 40},
	}
	got := talksFor(1, msgs, peers)
	want := []webTalk{
		{Slug: "beta", State: "working", Sent: 5, Received: 0, LastAt: 50},
		{Slug: "gamma", State: "blocked", Sent: 0, Received: 2, LastAt: 40},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("talks = %+v, want %+v", got, want)
	}
}

// Both directions fold into one entry with the larger LastAt; equal LastAt
// sorts by Slug; a missing peer among present ones is dropped alone.
func TestAgentMapEdgeTalksForFoldAndTies(t *testing.T) {
	peers := map[int64]webSpawnLink{
		2: {Slug: "zeta", State: "working"},
		3: {Slug: "alpha", State: "done"},
		4: {Slug: "mid", State: "draft"},
	}
	msgs := []store.JobMessage{
		{FromID: 1, ToID: 2, N: 1, FirstAt: 1, LastAt: 30},
		{FromID: 2, ToID: 1, N: 3, FirstAt: 2, LastAt: 70},
		{FromID: 3, ToID: 1, N: 1, FirstAt: 3, LastAt: 70},
		{FromID: 1, ToID: 9, N: 8, FirstAt: 4, LastAt: 999},
		{FromID: 1, ToID: 4, N: 2, FirstAt: 5, LastAt: 10},
		{FromID: 2, ToID: 4, N: 6, FirstAt: 6, LastAt: 500},
	}
	got := talksFor(1, msgs, peers)
	want := []webTalk{
		{Slug: "alpha", State: "done", Sent: 0, Received: 1, LastAt: 70},
		{Slug: "zeta", State: "working", Sent: 1, Received: 3, LastAt: 70},
		{Slug: "mid", State: "draft", Sent: 2, Received: 0, LastAt: 10},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("talks = %+v, want %+v", got, want)
	}
}
