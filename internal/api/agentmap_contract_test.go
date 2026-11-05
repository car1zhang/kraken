package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// agentmap_contract_test.go — athena-logic's blind tests for the agent map's
// wire half (50-contract.md G5 and G7): talksFor, the webJob keys, and the
// success answers of GET /api/job/events, POST /api/job/message and
// GET/POST /api/map/positions. Routes go through the real mux (Register) from
// a loopback address, the owner tier.

const agentMapContractLocal = "127.0.0.1:54321"

func agentMapContractMux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	Register(mux)
	return mux
}

func agentMapContractDo(t *testing.T, mux http.Handler, method, target, body string, header map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = agentMapContractLocal
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func agentMapContractDecode(t *testing.T, rec *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return m
}

// ---------------------------------------------------------------------------
// talksFor
// ---------------------------------------------------------------------------

// G5: one webTalk per peer opposite id, Sent/Received from each direction's N,
// LastAt the larger, sorted LastAt desc then Slug, peers missing from the map
// omitted.
func TestAgentMapContractTalksFor(t *testing.T) {
	msgs := []store.JobMessage{
		{FromID: 1, ToID: 2, N: 3, FirstAt: 10, LastAt: 100},
		{FromID: 2, ToID: 1, N: 1, FirstAt: 50, LastAt: 300},
		{FromID: 1, ToID: 3, N: 2, FirstAt: 20, LastAt: 200},
		{FromID: 4, ToID: 1, N: 5, FirstAt: 5, LastAt: 400}, // 4 not in peers
		{FromID: 5, ToID: 1, N: 7, FirstAt: 5, LastAt: 200}, // ties 3 on LastAt
		{FromID: 2, ToID: 3, N: 9, FirstAt: 1, LastAt: 900}, // does not involve 1
	}
	peers := map[int64]webSpawnLink{
		2: {Slug: "beta", State: "working"},
		3: {Slug: "gamma", State: "blocked"},
		5: {Slug: "alpha", State: "done"},
	}
	got := talksFor(1, msgs, peers)
	want := []webTalk{
		{Slug: "beta", State: "working", Sent: 3, Received: 1, LastAt: 300},
		{Slug: "alpha", State: "done", Sent: 0, Received: 7, LastAt: 200},
		{Slug: "gamma", State: "blocked", Sent: 2, Received: 0, LastAt: 200},
	}
	if len(got) != len(want) {
		t.Fatalf("talksFor = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("talksFor[%d] = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
}

// G5: "nil when empty."
func TestAgentMapContractTalksForEmpty(t *testing.T) {
	peers := map[int64]webSpawnLink{2: {Slug: "beta", State: "working"}}
	if got := talksFor(1, nil, peers); got != nil {
		t.Fatalf("talksFor(no msgs) = %+v, want nil", got)
	}
	msgs := []store.JobMessage{{FromID: 2, ToID: 3, N: 1, FirstAt: 1, LastAt: 1}}
	if got := talksFor(1, msgs, peers); got != nil {
		t.Fatalf("talksFor(unrelated msgs) = %+v, want nil", got)
	}
	msgs = []store.JobMessage{{FromID: 1, ToID: 9, N: 1, FirstAt: 1, LastAt: 1}}
	if got := talksFor(1, msgs, peers); got != nil {
		t.Fatalf("talksFor(peer absent) = %+v, want nil", got)
	}
}

// G5: webTalk's JSON keys, and the two webJob keys are omitempty.
func TestAgentMapContractWireKeys(t *testing.T) {
	b, err := json.Marshal(webTalk{Slug: "s", State: "working", Sent: 1, Received: 2, LastAt: 3})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"slug", "state", "sent", "received", "last_at"} {
		if _, ok := m[k]; !ok {
			t.Errorf("webTalk JSON %s missing %q", b, k)
		}
	}

	b, err = json.Marshal(webJob{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), `"activity"`) || strings.Contains(string(b), `"talks"`) {
		t.Fatalf("zero webJob JSON carries activity/talks: %s", b)
	}
	b, err = json.Marshal(webJob{
		Activity: &jobs.JobActivity{Act: "edit", At: 1},
		Talks:    []webTalk{{Slug: "s", State: "working", Sent: 1, LastAt: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"activity"`) || !strings.Contains(string(b), `"talks"`) {
		t.Fatalf("webJob JSON lacks activity/talks when set: %s", b)
	}
}

// ---------------------------------------------------------------------------
// GET /api/job/events
// ---------------------------------------------------------------------------

func agentMapContractTranscript(t *testing.T, home, sid string) {
	t.Helper()
	t0 := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	var lines []string
	for i, cmd := range []string{"go test ./...", "make build", "ls -la"} {
		rec := map[string]any{
			"type":      "assistant",
			"uuid":      "a" + string(rune('0'+i)),
			"timestamp": t0.Add(time.Duration(i) * time.Second).Format("2006-01-02T15:04:05.000Z"),
			"message": map[string]any{
				"role": "assistant",
				"content": []any{map[string]any{
					"type": "tool_use", "id": "toolu_" + string(rune('a'+i)), "name": "Bash",
					"input": map[string]any{"command": cmd},
				}},
			},
		}
		b, _ := json.Marshal(rec)
		lines = append(lines, string(b))
	}
	path := filepath.Join(home, ".claude", "projects", "-agentmap-proj", sid+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// G5: 200 {"events":[…]} with JobEvents' result, newest n oldest first.
func TestAgentMapContractJobEventsRoute(t *testing.T) {
	st := useTestStore(t)
	home := os.Getenv("HOME") // useTestStore sandboxed it
	sid := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	agentMapContractTranscript(t, home, sid)
	if _, err := st.CreateJob(&store.Job{Slug: "ev-job", Repo: "kraken", State: "working", Session: sid, Body: "spec\n"}); err != nil {
		t.Fatal(err)
	}
	mux := agentMapContractMux(t)

	rec := agentMapContractDo(t, mux, http.MethodGet, "/api/job/events?slug=ev-job", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET events: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Events []struct {
			ID    string `json:"id"`
			Act   string `json:"act"`
			Label string `json:"label"`
		} `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 3 || body.Events[0].ID != "toolu_a" || body.Events[0].Act != "test" ||
		body.Events[1].Label != "make build" || body.Events[2].Act != "read" {
		t.Fatalf("events = %+v, want go test, make build, ls in order", body.Events)
	}

	rec = agentMapContractDo(t, mux, http.MethodGet, "/api/job/events?slug=ev-job&n=2", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET events n=2: %d %s", rec.Code, rec.Body.String())
	}
	body.Events = nil
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 2 || body.Events[0].ID != "toolu_b" || body.Events[1].ID != "toolu_c" {
		t.Fatalf("events n=2 = %+v, want toolu_b, toolu_c", body.Events)
	}
}

// G5: "events is [], never null".
func TestAgentMapContractJobEventsRouteEmpty(t *testing.T) {
	st := useTestStore(t)
	if _, err := st.CreateJob(&store.Job{Slug: "quiet-job", Repo: "kraken", Body: "spec\n"}); err != nil {
		t.Fatal(err)
	}
	rec := agentMapContractDo(t, agentMapContractMux(t), http.MethodGet, "/api/job/events?slug=quiet-job", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET events: %d %s", rec.Code, rec.Body.String())
	}
	m := agentMapContractDecode(t, rec)
	if strings.TrimSpace(string(m["events"])) != "[]" {
		t.Fatalf("events = %s, want []", m["events"])
	}
}

// ---------------------------------------------------------------------------
// POST /api/job/message
// ---------------------------------------------------------------------------

func agentMapContractMessageJobs(t *testing.T) (*store.Store, int64, int64) {
	t.Helper()
	st := useTestStore(t)
	a, err := st.CreateJob(&store.Job{Slug: "talk-alpha", Repo: "kraken", State: "working", Body: "spec\n"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.CreateJob(&store.Job{Slug: "talk-beta", Repo: "kraken", State: "blocked", Body: "spec\n"})
	if err != nil {
		t.Fatal(err)
	}
	return st, a, b
}

func agentMapContractRecorded(t *testing.T, rec *httptest.ResponseRecorder) bool {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("POST message: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Recorded *bool `json:"recorded"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Recorded == nil {
		t.Fatalf("body %q: want {\"recorded\":bool} (err %v)", rec.Body.String(), err)
	}
	return *body.Recorded
}

// G5: 200 {"recorded":true} and the bump stored when the header names an
// active job and `to` resolves to a different active job.
func TestAgentMapContractMessageRouteRecords(t *testing.T) {
	st, a, b := agentMapContractMessageJobs(t)
	mux := agentMapContractMux(t)
	hdr := map[string]string{fromCellHeader: "talk-alpha"}

	if !agentMapContractRecorded(t, agentMapContractDo(t, mux, http.MethodPost, "/api/job/message", `{"to":"zeus-talk-beta"}`, hdr)) {
		t.Fatal("recorded = false, want true")
	}
	// the header is trimmed
	if !agentMapContractRecorded(t, agentMapContractDo(t, mux, http.MethodPost, "/api/job/message", `{"to":"zeus-talk-beta"}`,
		map[string]string{fromCellHeader: "  talk-alpha  "})) {
		t.Fatal("recorded = false with a padded header, want true")
	}
	rows, err := st.ListJobMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].FromID != a || rows[0].ToID != b || rows[0].N != 2 {
		t.Fatalf("rows = %+v, want alpha->beta N 2", rows)
	}
}

// G5: 200 {"recorded":false} when either side does not resolve, or both are
// the same job; nothing stored.
func TestAgentMapContractMessageRouteNotRecorded(t *testing.T) {
	st, _, _ := agentMapContractMessageJobs(t)
	mux := agentMapContractMux(t)
	cases := []struct{ from, to string }{
		{"talk-alpha", "zeus-no-such-job"},
		{"no-such-job", "zeus-talk-beta"},
		{"talk-alpha", "zeus-talk-alpha"},
	}
	for _, c := range cases {
		rec := agentMapContractDo(t, mux, http.MethodPost, "/api/job/message", `{"to":"`+c.to+`"}`, map[string]string{fromCellHeader: c.from})
		if agentMapContractRecorded(t, rec) {
			t.Errorf("from %q to %q: recorded = true, want false", c.from, c.to)
		}
	}
	rows, err := st.ListJobMessages()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %+v, want none", rows)
	}
}

// ---------------------------------------------------------------------------
// /api/map/positions
// ---------------------------------------------------------------------------

// G7: GET answers {"positions":{}} when nothing is saved.
func TestAgentMapContractPositionsGetEmpty(t *testing.T) {
	useTestStore(t)
	rec := agentMapContractDo(t, agentMapContractMux(t), http.MethodGet, "/api/map/positions", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	m := agentMapContractDecode(t, rec)
	if strings.TrimSpace(string(m["positions"])) != "{}" {
		t.Fatalf("positions = %s, want {}", m["positions"])
	}
}

// G7: POST merges, answers {"ok":true}; GET reads every saved key back.
func TestAgentMapContractPositionsRoundTrip(t *testing.T) {
	useTestStore(t)
	mux := agentMapContractMux(t)
	for _, body := range []string{
		`{"positions":{"alpha":[10,20],"archive:old":[-5.5,7.25]}}`,
		`{"positions":{"nodeX/beta":[300,400]}}`,
		`{"positions":{"alpha":[11,21]}}`,
	} {
		rec := agentMapContractDo(t, mux, http.MethodPost, "/api/map/positions", body, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s: %d %s", body, rec.Code, rec.Body.String())
		}
		var ok struct {
			OK bool `json:"ok"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || !ok.OK {
			t.Fatalf("POST %s answered %q, want {\"ok\":true}", body, rec.Body.String())
		}
	}
	rec := agentMapContractDo(t, mux, http.MethodGet, "/api/map/positions", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Positions map[string][2]float64 `json:"positions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string][2]float64{"alpha": {11, 21}, "archive:old": {-5.5, 7.25}, "nodeX/beta": {300, 400}}
	if len(got.Positions) != len(want) {
		t.Fatalf("positions = %v, want %v", got.Positions, want)
	}
	for k, v := range want {
		if got.Positions[k] != v {
			t.Errorf("positions[%q] = %v, want %v", k, got.Positions[k], v)
		}
	}
	// the jobs-side reader agrees with the wire
	if p := jobs.MapPositions(); p["alpha"] != [2]float64{11, 21} {
		t.Errorf("jobs.MapPositions()[alpha] = %v, want [11 21]", p["alpha"])
	}
}
