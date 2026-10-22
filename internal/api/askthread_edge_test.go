package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// askthread_edge_test.go — the EDGE dimension of slice C of
// artifacts/50-contract.md (ask threads ported into kraken): threadErrStatus
// for the five new sentinels and the two bound refusals, the
// /api/thread/read live rule and its always-present keys, the GET
// /api/threads omitempty boundaries (kind and role on every row), the
// webBoard node_id boundary, POST /api/threads branch's headless routing
// (missing text, the mind+headless refusal, the bounds), and
// /api/node/action's thread actions: visibility gate (incl. fleet off),
// payload refusals, the cap, origin from the verified identity (never the
// body), any-caller send, thread-kill's ownership check.
//
// Written BLIND against the frozen contract text; no implementation file was
// read. No real claude can start: the jobs ask seams are swapped through
// jobs.SetAskSeamsForTest in every test (a scripted launcher records argv and
// stdin), tmux is a /bin/sh STUB alone on PATH that lists no session,
// tailscale is pinned to a path that does not exist, and nodeSelf is a
// fixture envelope restored at cleanup.
//
// Every package-level identifier is `askEdge`-prefixed.

const (
	askEdgeSlug     = "tech-comms-rollout"
	askEdgeQuestion = "why is CI red on the rollout PR?"
	askEdgeZeus     = "b4a2e1f0-0000-4000-8000-000000000001"
	askEdgeSession  = "7c1e9d44-0000-4000-8000-000000000003"
)

// askEdgeStudio is the asker's verified tailnet identity.
var askEdgeStudio = core.TailnetIdentity{ID: "nSTUDIO", Name: "studio-mini.tail0000.ts.net", Login: "jess@probook.ai"}

// askEdgeLauncher records every start and runs each process per behave
// (nil: hold until the run's ctx ends).
type askEdgeLauncher struct {
	mu     sync.Mutex
	argv   [][]string
	stdin  []string
	behave func(ctx context.Context) (string, error)
}

func (l *askEdgeLauncher) n() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.argv)
}

func (l *askEdgeLauncher) fn() jobs.AskLauncherFunc {
	return func(ctx context.Context, argv []string, dir string, env []string, stdin string) (func() (string, error), error) {
		l.mu.Lock()
		l.argv = append(l.argv, append([]string(nil), argv...))
		l.stdin = append(l.stdin, stdin)
		behave := l.behave
		l.mu.Unlock()
		return func() (string, error) {
			if behave != nil {
				return behave(ctx)
			}
			<-ctx.Done()
			return "", errors.New("signal: killed")
		}, nil
	}
}

// askEdgeEnv: an isolated store, fleet on, no tailscale, a stub tmux that
// lists no session, nodeSelf a fixture, the ask seams swapped (cell live iff
// live; zeus forkable iff live), and the running example's job created
// (readonly by default — the store's default visibility).
func askEdgeEnv(t *testing.T, live bool) (*store.Store, *jobs.Job, *askEdgeLauncher) {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("KRAKEN_NODES", "")
	t.Setenv("KRAKEN_DEVICE_NAME", "owner-mac")
	t.Setenv("KRAKEN_OPERATOR", "Owner")
	t.Setenv("KRAKEN_TAILSCALE", filepath.Join(t.TempDir(), "no-tailscale-here"))
	SetMirrorMode(false)
	t.Cleanup(func() { SetMirrorMode(false) })
	prevSelf := nodeSelf
	nodeSelf = func() jobs.NodeEnvelope {
		return jobs.NodeEnvelope{ID: "nOWNER", Label: "owner-mac", Operator: "Owner", IPs: []string{}}
	}
	t.Cleanup(func() { nodeSelf = prevSelf })

	if _, err := st.CreateJob(&store.Job{Slug: askEdgeSlug, Repo: "kraken", Body: "# Spec\n", Run: 1, State: "working"}); err != nil {
		t.Fatal(err)
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), askEdgeSlug)
	if j == nil {
		t.Fatal("job not loadable")
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	l := &askEdgeLauncher{}
	restore := jobs.SetAskSeamsForTest(
		func(j *jobs.Job) (string, bool) {
			if !live {
				return "", false
			}
			return jobs.CellSession(j), true
		},
		func(j *jobs.Job) (*core.Peer, error) {
			if !live {
				return nil, errors.New(j.Slug + " has no live zeus to branch from")
			}
			return &core.Peer{TmuxSession: jobs.CellSession(j), Agent: "claude", Pane: "%1", Name: jobs.ZeusName(j), SessionID: askEdgeZeus}, nil
		},
		l.fn(),
	)
	t.Cleanup(func() {
		// kill whatever a test left running before the seams go back
		if list, err := st.ListThreads(j.ID); err == nil {
			for _, r := range list {
				jobs.KillAskRun(j.ID, r.TID)
			}
		}
		restore()
	})
	return st, j, l
}

// askEdgeVis lands a visibility through the job-field writer.
func askEdgeVis(t *testing.T, slug, vis string) {
	t.Helper()
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not loadable", slug)
	}
	if err := jobs.WriteJobFields(j, "visibility", vis); err != nil {
		t.Fatalf("WriteJobFields visibility: %v", err)
	}
}

func askEdgeAsk(j *jobs.Job, node, label, login string) store.Thread {
	return store.Thread{
		JobID: j.ID, Kind: "headless", Role: "ask", OriginNode: node, OriginLabel: label, OriginLogin: login,
		Title: "ci red", SessionID: askEdgeSession, ParentSession: askEdgeZeus, State: "live", Seed: askEdgeQuestion,
	}
}

func askEdgeAdd(t *testing.T, st *store.Store, r store.Thread) store.Thread {
	t.Helper()
	got, err := st.AddThreadNext(r)
	if err != nil {
		t.Fatalf("AddThreadNext: %v", err)
	}
	return got
}

// askEdgeNodeAction is a POST /api/node/action from a tailnet node carrying
// identity id as the gate would attach it. from is the body's `from`, which
// the route must ignore for origin.
func askEdgeNodeAction(t *testing.T, id core.TailnetIdentity, slug, action string, payload map[string]any, from map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	if payload == nil {
		payload = map[string]any{}
	}
	if from == nil {
		from = map[string]any{"cell": ""}
	}
	body, _ := json.Marshal(map[string]any{"slug": slug, "action": action, "payload": payload, "from": from})
	r := httptest.NewRequest(http.MethodPost, "/api/node/action", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "100.90.0.1:1"
	ctx := context.WithValue(r.Context(), gateTierKey, TierNode)
	ctx = context.WithValue(ctx, gateIdentityKey, id)
	r = r.WithContext(ctx)
	rec := httptest.NewRecorder()
	nodeActionHandler(rec, r)
	return rec
}

func askEdgeErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("body is not JSON: %q", rec.Body.String())
	}
	s, _ := m["error"].(string)
	return s
}

func askEdgeExpect(t *testing.T, label string, rec *httptest.ResponseRecorder, code int, errText string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("%s: status %d (%s), want %d", label, rec.Code, rec.Body.String(), code)
	}
	if errText != "" {
		if got := askEdgeErr(t, rec); got != errText {
			t.Fatalf("%s: error %q, want %q", label, got, errText)
		}
	}
}

func askEdgeKeys(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not a JSON object: %q", raw)
	}
	return m
}

func askEdgeRows(t *testing.T, st *store.Store, j *jobs.Job) int {
	t.Helper()
	list, err := st.ListThreads(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	return len(list)
}

// ------------------------------------------------------- threadErrStatus

// TestAskEdgeThreadErrStatusSentinels: the five sentinels map to 409 bare
// and wrapped, through threadErr too; neighbours unchanged.
func TestAskEdgeThreadErrStatusSentinels(t *testing.T) {
	for _, err := range []error{
		store.ErrThreadAnswering, store.ErrThreadClosed, jobs.ErrAskThreadCap,
		store.ErrAskThreadPane, store.ErrThreadKind,
	} {
		for _, e := range []error{err, fmt.Errorf("thread t3 of job 7: %w", err)} {
			if got := threadErrStatus(e); got != http.StatusConflict {
				t.Errorf("threadErrStatus(%v) = %d, want 409", e, got)
			}
			rec := httptest.NewRecorder()
			threadErr(rec, "thread-send", askEdgeSlug, e)
			if rec.Code != http.StatusConflict {
				t.Errorf("threadErr(%v) wrote %d, want 409", e, rec.Code)
			}
		}
	}
	if got := threadErrStatus(store.ErrNotFound); got != http.StatusNotFound {
		t.Errorf("threadErrStatus(ErrNotFound) = %d, want 404", got)
	}
	if got := threadErrStatus(fmt.Errorf("add thread t3 for job 7: %w", store.ErrThreadIDTaken)); got != http.StatusConflict {
		t.Errorf("threadErrStatus(ErrThreadIDTaken) = %d, want 409", got)
	}
	if got := threadErrStatus(fmt.Errorf("list threads: %w", errors.New("disk I/O error"))); got != http.StatusInternalServerError {
		t.Errorf("threadErrStatus(wrapped db error) = %d, want 500", got)
	}
}

// TestAskEdgeThreadErrStatusBoundsAre400: a real bound refusal from
// jobs.AskThreadSend (an IsAskInputError) maps to 400 with jobs' text.
func TestAskEdgeThreadErrStatusBoundsAre400(t *testing.T) {
	st, j, _ := askEdgeEnv(t, false)
	row := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	err := jobs.AskThreadSend(j, row.TID, strings.Repeat("q", 16385), jobs.ThreadOrigin{Node: "nSTUDIO"})
	if err == nil || !jobs.IsAskInputError(err) {
		t.Fatalf("fixture: AskThreadSend(16385 bytes) = %v, want a bound refusal", err)
	}
	if got := threadErrStatus(err); got != http.StatusBadRequest {
		t.Fatalf("threadErrStatus(bound) = %d, want 400", got)
	}
	rec := httptest.NewRecorder()
	threadErr(rec, "thread-send", askEdgeSlug, err)
	askEdgeExpect(t, "threadErr(bound)", rec, http.StatusBadRequest, err.Error())
}

// ------------------------------------------------------- /api/thread/read

func askEdgeRead(t *testing.T, tid string) map[string]json.RawMessage {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/thread/read?slug="+askEdgeSlug+"&tid="+tid, nil)
	rec := httptest.NewRecorder()
	threadReadHandler(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("thread/read %s: status %d (%s)", tid, rec.Code, rec.Body.String())
	}
	return askEdgeKeys(t, rec.Body.Bytes())
}

// TestAskEdgeThreadReadLiveRuleAndKeys: live is false for a closed headless
// row and for a live pane row with no pane, true for a live headless row
// with no pane; kind/answering/note are present on every reply, zero-valued
// or not; origin is omitted on a row with none and carries all three keys
// when present; the pre-existing keys stay.
func TestAskEdgeThreadReadLiveRuleAndKeys(t *testing.T) {
	st, j, _ := askEdgeEnv(t, false)

	closedAsk := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	if err := st.CloseThread(j.ID, closedAsk.TID, 0); err != nil {
		t.Fatal(err)
	}
	paneNoPane := askEdgeAdd(t, st, store.Thread{JobID: j.ID, Kind: "pane", SessionID: "pane-sess", State: "live"})
	liveAsk := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	busy := askEdgeAsk(j, "", "owner-mac", "")
	busy.Answering = true
	busy.Note = "ask failed: earlier"
	busyAsk := askEdgeAdd(t, st, busy)

	cases := []struct {
		tid       string
		live      bool
		kind      string
		origin    bool
		answering bool
		note      string
	}{
		{closedAsk.TID, false, "headless", true, false, ""},
		{paneNoPane.TID, false, "pane", false, false, ""},
		{liveAsk.TID, true, "headless", true, false, ""},
		{busyAsk.TID, true, "headless", true, true, "ask failed: earlier"},
	}
	for _, c := range cases {
		m := askEdgeRead(t, c.tid)
		var live bool
		if err := json.Unmarshal(m["live"], &live); err != nil || live != c.live {
			t.Errorf("%s: live = %s, want %v", c.tid, m["live"], c.live)
		}
		for _, k := range []string{"kind", "answering", "note", "live"} {
			if _, ok := m[k]; !ok {
				t.Errorf("%s: reply lacks %q (always present): %v", c.tid, k, m)
			}
		}
		var kind, note string
		var answering bool
		if err := json.Unmarshal(m["kind"], &kind); err != nil {
			t.Errorf("%s: kind %s is not a string", c.tid, m["kind"])
		}
		if err := json.Unmarshal(m["note"], &note); err != nil {
			t.Errorf("%s: note %s is not a string", c.tid, m["note"])
		}
		if err := json.Unmarshal(m["answering"], &answering); err != nil {
			t.Errorf("%s: answering %s is not a bool", c.tid, m["answering"])
		}
		if kind != c.kind || note != c.note || answering != c.answering {
			t.Errorf("%s: kind %q note %q answering %v, want %q %q %v", c.tid, kind, note, answering, c.kind, c.note, c.answering)
		}
		raw, ok := m["origin"]
		if ok != c.origin {
			t.Errorf("%s: origin present = %v, want %v (%s)", c.tid, ok, c.origin, raw)
		}
		if ok {
			o := askEdgeKeys(t, raw)
			for _, k := range []string{"node_id", "label", "login"} {
				if _, has := o[k]; !has {
					t.Errorf("%s: origin %s lacks %q", c.tid, raw, k)
				}
			}
		}
	}
}

// ------------------------------------------------------- GET /api/threads wire

// TestAskEdgeWebThreadOmitBoundaries: kind and role on every row; origin iff
// any of the three fields is non-empty (one alone suffices); answering and
// note omitted at their zero values and present when set.
func TestAskEdgeWebThreadOmitBoundaries(t *testing.T) {
	st, j, _ := askEdgeEnv(t, false)
	askEdgeAdd(t, st, store.Thread{JobID: j.ID, SessionID: "legacy", State: "live", Pane: "%3", Window: "@1"})                                           // t1: pane, no origin, role ask
	askEdgeAdd(t, st, store.Thread{JobID: j.ID, Kind: "pane", SessionID: "p2", State: "live", Pane: "%4", Window: "@2", OriginLogin: "jess@probook.ai"}) // t2: login only
	askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))                                                                        // t3: headless, full origin
	askEdgeAdd(t, st, store.Thread{JobID: j.ID, Kind: "pane", Role: "mind", SessionID: "m4", State: "live", Pane: "%5", Window: "@3"})                   // t4: mind pane
	h := askEdgeAsk(j, "nSTUDIO", "", "")
	h.SessionID = "h5"
	h.Answering = true
	h.Note = "ask timed out after 5m0s"
	askEdgeAdd(t, st, h) // t5: node only, answering, note

	r := httptest.NewRequest(http.MethodGet, "/api/threads?slug="+askEdgeSlug, nil)
	rec := httptest.NewRecorder()
	threadsHandler(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d (%s)", rec.Code, rec.Body.String())
	}
	var resp struct {
		Threads []json.RawMessage `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Threads) != 5 {
		t.Fatalf("threads = %d, want 5", len(resp.Threads))
	}
	want := []struct {
		kind, role string
		origin     bool
		answering  bool
		note       string
	}{
		{"pane", "ask", false, false, ""},
		{"pane", "ask", true, false, ""},
		{"headless", "ask", true, false, ""},
		{"pane", "mind", false, false, ""},
		{"headless", "ask", true, true, "ask timed out after 5m0s"},
	}
	for i, raw := range resp.Threads {
		m := askEdgeKeys(t, raw)
		var kind, role string
		json.Unmarshal(m["kind"], &kind)
		json.Unmarshal(m["role"], &role)
		if kind != want[i].kind {
			t.Errorf("thread %d kind = %q, want %q: %s", i, kind, want[i].kind, raw)
		}
		if role != want[i].role {
			t.Errorf("thread %d role = %q, want %q (role stays on every object): %s", i, role, want[i].role, raw)
		}
		if _, ok := m["origin"]; ok != want[i].origin {
			t.Errorf("thread %d origin present = %v, want %v: %s", i, ok, want[i].origin, raw)
		}
		ans, hasAns := m["answering"]
		if hasAns != want[i].answering || (hasAns && string(ans) != "true") {
			t.Errorf("thread %d answering = %s (present %v), want present iff true: %s", i, ans, hasAns, raw)
		}
		note, hasNote := m["note"]
		if hasNote != (want[i].note != "") {
			t.Errorf("thread %d note present = %v, want %v: %s", i, hasNote, want[i].note != "", raw)
		}
		if hasNote {
			var s string
			json.Unmarshal(note, &s)
			if s != want[i].note {
				t.Errorf("thread %d note = %q, want %q", i, s, want[i].note)
			}
		}
	}
	var o struct {
		Origin *webThreadOrigin `json:"origin"`
	}
	json.Unmarshal(resp.Threads[1], &o)
	if o.Origin == nil || o.Origin.Login != "jess@probook.ai" || o.Origin.NodeID != "" || o.Origin.Label != "" {
		t.Errorf("login-only origin = %+v", o.Origin)
	}
}

func TestAskEdgeWebBoardNodeIDOmittedWhenEmpty(t *testing.T) {
	raw, err := json.Marshal(webBoard{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := askEdgeKeys(t, raw)["node_id"]; ok {
		t.Fatalf("empty webBoard carries node_id: %s", raw)
	}
	raw, _ = json.Marshal(webBoard{NodeID: "nOWNER"})
	if got := askEdgeKeys(t, raw)["node_id"]; string(got) != `"nOWNER"` {
		t.Fatalf("node_id = %s, want \"nOWNER\"", got)
	}
}

// ------------------------------------------------------- POST /api/threads branch

func askEdgePostThreads(t *testing.T, req map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(req)
	r := httptest.NewRequest(http.MethodPost, "/api/threads", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	threadsHandler(rec, r)
	return rec
}

// TestAskEdgeBranchHeadlessRouting: headless absent or true demands text
// (400 `missing text`, after the title check) and routes to AskThread (its
// dead-cell wording, its bound refusals as 400); headless false keeps the
// pane path (text optional; BranchThread's dead-cell wording). Nothing is
// reserved and nothing starts.
func TestAskEdgeBranchHeadlessRouting(t *testing.T) {
	st, j, l := askEdgeEnv(t, false) // dead cell

	for _, h := range []any{nil, true} {
		name := "absent"
		base := func() map[string]any {
			m := map[string]any{"slug": askEdgeSlug, "op": "branch"}
			if h != nil {
				m["headless"] = h
			}
			return m
		}
		if h != nil {
			name = "true"
		}
		for _, text := range []string{"", "   \n"} {
			req := base()
			req["title"], req["text"] = "ci red", text
			askEdgeExpect(t, "headless "+name+" blank text", askEdgePostThreads(t, req), http.StatusBadRequest, "missing text")
		}
		req := base()
		req["title"], req["text"] = "", ""
		askEdgeExpect(t, "headless "+name+" blank title and text", askEdgePostThreads(t, req), http.StatusBadRequest, "missing title")

		req = base()
		req["title"], req["text"], req["model"] = "ci red", askEdgeQuestion, "gpt-5"
		askEdgeExpect(t, "headless "+name+" bad model", askEdgePostThreads(t, req), http.StatusBadRequest, "bad model — want haiku, sonnet, opus or fable")

		req = base()
		req["title"], req["text"] = strings.Repeat("é", 121), askEdgeQuestion
		askEdgeExpect(t, "headless "+name+" title over 120 runes", askEdgePostThreads(t, req), http.StatusBadRequest,
			askEdgeSlug+": ask title is too long (max 120 characters)")

		req = base()
		req["title"], req["text"] = "ci red", strings.Repeat("q", 16385)
		askEdgeExpect(t, "headless "+name+" question over 16 KB", askEdgePostThreads(t, req), http.StatusBadRequest,
			askEdgeSlug+": ask question is too long (max 16 KB)")

		req = base()
		req["title"], req["text"] = "ci red", askEdgeQuestion
		rec := askEdgePostThreads(t, req)
		if rec.Code != http.StatusConflict || !strings.Contains(askEdgeErr(t, rec), "has no live cell to ask") {
			t.Fatalf("headless %s on a dead cell: %d %s, want 409 with AskThread's dead-cell wording", name, rec.Code, rec.Body.String())
		}
	}

	rec := askEdgePostThreads(t, map[string]any{"slug": askEdgeSlug, "op": "branch", "title": "ci red", "text": "", "headless": false})
	if rec.Code == http.StatusBadRequest {
		t.Fatalf("headless false with no text = 400 %s, want text optional (pane path)", rec.Body.String())
	}
	if rec.Code != http.StatusConflict || !strings.Contains(askEdgeErr(t, rec), "has no live cell to branch") {
		t.Fatalf("headless false on a dead cell: %d %s, want 409 with BranchThread's wording", rec.Code, rec.Body.String())
	}

	if n := askEdgeRows(t, st, j); n != 0 {
		t.Fatalf("refused branches left %d rows", n)
	}
	if l.n() != 0 {
		t.Fatalf("refused branches started %d processes", l.n())
	}
}

// TestAskEdgeBranchMindHeadless: role "mind" with headless true is 400
// `a mind thread cannot be headless`; role "mind" with headless absent or
// false keeps the pane path; role "ask" with headless true is the ask path.
func TestAskEdgeBranchMindHeadless(t *testing.T) {
	st, j, l := askEdgeEnv(t, false) // dead cell

	askEdgeExpect(t, "mind + headless true", askEdgePostThreads(t, map[string]any{
		"slug": askEdgeSlug, "op": "branch", "title": "ci red", "text": askEdgeQuestion, "role": "mind", "headless": true,
	}), http.StatusBadRequest, "a mind thread cannot be headless")

	for _, h := range []any{nil, false} {
		req := map[string]any{"slug": askEdgeSlug, "op": "branch", "title": "ci red", "text": askEdgeQuestion, "role": "mind"}
		if h != nil {
			req["headless"] = h
		}
		rec := askEdgePostThreads(t, req)
		if rec.Code != http.StatusConflict || !strings.Contains(askEdgeErr(t, rec), "has no live cell to branch") {
			t.Fatalf("mind with headless %v on a dead cell: %d %s, want 409 with BranchThread's wording", h, rec.Code, rec.Body.String())
		}
	}

	rec := askEdgePostThreads(t, map[string]any{"slug": askEdgeSlug, "op": "branch", "title": "ci red", "text": askEdgeQuestion, "role": "ask", "headless": true})
	if rec.Code != http.StatusConflict || !strings.Contains(askEdgeErr(t, rec), "has no live cell to ask") {
		t.Fatalf("role ask + headless true on a dead cell: %d %s, want AskThread's dead-cell wording", rec.Code, rec.Body.String())
	}
	if n := askEdgeRows(t, st, j); n != 0 {
		t.Fatalf("refused branches left %d rows", n)
	}
	if l.n() != 0 {
		t.Fatalf("refused branches started %d processes", l.n())
	}
}

// TestAskEdgeSendHeadlessAnswersBeforeTheAnswer: op send into a live
// headless row answers 200 {"ok":true} while the run is still in flight; a
// second send into the same row is 409 with the answering sentinel's text.
func TestAskEdgeSendHeadlessAnswersBeforeTheAnswer(t *testing.T) {
	st, j, l := askEdgeEnv(t, true)
	// a remote-origin row: its cwd is the cell dir, so no worktree is needed
	row := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))

	rec := askEdgePostThreads(t, map[string]any{"slug": askEdgeSlug, "op": "send", "tid": row.TID, "text": askEdgeQuestion})
	askEdgeExpect(t, "send", rec, http.StatusOK, "")
	var ok struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || !ok.OK {
		t.Fatalf("send body = %s, want {\"ok\":true}", rec.Body.String())
	}
	if !jobs.AskRunning(j.ID, row.TID) {
		t.Fatal("send answered 200 but no run is registered")
	}
	askEdgeExpect(t, "second send", askEdgePostThreads(t, map[string]any{"slug": askEdgeSlug, "op": "send", "tid": row.TID, "text": "again"}),
		http.StatusConflict, store.ErrThreadAnswering.Error())
	if l.n() != 1 {
		t.Fatalf("launcher called %d times, want 1", l.n())
	}
}

// ------------------------------------------------------- /api/node/action

// TestAskEdgeNodeThreadActionsTable pins the action set and the ask relay
// timeout.
func TestAskEdgeNodeThreadActionsTable(t *testing.T) {
	want := map[string]bool{"thread-ask": true, "thread-send": true, "thread-kill": true}
	if len(nodeThreadActions) != len(want) {
		t.Fatalf("nodeThreadActions = %v, want %v", nodeThreadActions, want)
	}
	for k := range want {
		if !nodeThreadActions[k] {
			t.Fatalf("nodeThreadActions lacks %q: %v", k, nodeThreadActions)
		}
	}
	if nodesProxyAskTimeout != 30*time.Second {
		t.Fatalf("nodesProxyAskTimeout = %v, want 30s", nodesProxyAskTimeout)
	}
}

// TestAskEdgeNodeActionVisibilityGate: the thread actions 404 on unknown and
// private jobs, and on readonly and public get PAST the 403 — each lands on
// its own payload refusal instead. Existing actions keep 403 on readonly,
// and an unknown action on readonly is still 403 first.
func TestAskEdgeNodeActionVisibilityGate(t *testing.T) {
	askEdgeEnv(t, false)

	for _, a := range []string{"thread-ask", "thread-send", "thread-kill"} {
		rec := askEdgeNodeAction(t, askEdgeStudio, "never-existed", a, nil, nil)
		askEdgeExpect(t, a+" unknown job", rec, http.StatusNotFound, "no such job")
	}

	askEdgeVis(t, askEdgeSlug, "private")
	for _, a := range []string{"thread-ask", "thread-send", "thread-kill"} {
		rec := askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, a, map[string]any{"title": "ci red", "text": askEdgeQuestion, "tid": "t1"}, nil)
		askEdgeExpect(t, a+" private", rec, http.StatusNotFound, "no such job")
	}

	for _, vis := range []string{"readonly", "public"} {
		askEdgeVis(t, askEdgeSlug, vis)
		askEdgeExpect(t, "thread-ask "+vis+" blank", askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-ask", nil, nil), http.StatusBadRequest, "missing title")
		askEdgeExpect(t, "thread-send "+vis+" blank", askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-send", nil, nil), http.StatusBadRequest, "missing tid")
		askEdgeExpect(t, "thread-kill "+vis+" blank", askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-kill", nil, nil), http.StatusBadRequest, "missing tid")
	}

	askEdgeVis(t, askEdgeSlug, "readonly")
	askEdgeExpect(t, "steer readonly", askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "steer", map[string]any{"text": "hi"}, nil), http.StatusForbidden, "not public")
	askEdgeExpect(t, "unknown action readonly", askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-bogus", nil, nil), http.StatusForbidden, "not public")
	askEdgeVis(t, askEdgeSlug, "public")
	if rec := askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-bogus", nil, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown action on public: %d %s, want 400", rec.Code, rec.Body.String())
	}
}

// TestAskEdgeNodeActionFleetOff: with KRAKEN_NODES=off every thread action
// is 404 `no such job`, even on a public job with a complete payload.
func TestAskEdgeNodeActionFleetOff(t *testing.T) {
	st, j, l := askEdgeEnv(t, true)
	askEdgeVis(t, askEdgeSlug, "public")
	row := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	t.Setenv("KRAKEN_NODES", "off")
	for _, a := range []string{"thread-ask", "thread-send", "thread-kill"} {
		rec := askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, a, map[string]any{"title": "ci red", "text": askEdgeQuestion, "tid": row.TID}, nil)
		askEdgeExpect(t, a+" fleet off", rec, http.StatusNotFound, "no such job")
	}
	if got, err := st.GetThread(j.ID, row.TID); err != nil || got.State != "live" || got.Answering {
		t.Fatalf("a fleet-off refusal touched the row: %+v %v", got, err)
	}
	if n := askEdgeRows(t, st, j); n != 1 {
		t.Fatalf("fleet-off refusals left %d rows, want the 1 seeded", n)
	}
	if l.n() != 0 {
		t.Fatalf("fleet-off refusals started %d processes", l.n())
	}
}

// TestAskEdgeNodeActionThreadAskRefusals: blanks, bad models (an exact id
// and a case variant included), the bounds (jobs' text, 400), the dead cell
// and the cap (409). Nothing is reserved, nothing starts.
func TestAskEdgeNodeActionThreadAskRefusals(t *testing.T) {
	st, j, l := askEdgeEnv(t, false) // dead cell
	askEdgeVis(t, askEdgeSlug, "readonly")

	ask := func(p map[string]any) *httptest.ResponseRecorder {
		return askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-ask", p, nil)
	}
	askEdgeExpect(t, "blank title", ask(map[string]any{"title": "  ", "text": askEdgeQuestion}), http.StatusBadRequest, "missing title")
	askEdgeExpect(t, "blank title and text", ask(map[string]any{"title": "", "text": ""}), http.StatusBadRequest, "missing title")
	askEdgeExpect(t, "blank text", ask(map[string]any{"title": "ci red", "text": " \n"}), http.StatusBadRequest, "missing text")
	for _, m := range []string{"gpt-5", "claude-opus-5", "Opus", " haiku"} {
		askEdgeExpect(t, "bad model "+m, ask(map[string]any{"title": "ci red", "text": askEdgeQuestion, "model": m}), http.StatusBadRequest, "bad model — want haiku, sonnet, opus or fable")
	}
	askEdgeExpect(t, "title over 120 runes", ask(map[string]any{"title": strings.Repeat("界", 121), "text": askEdgeQuestion}), http.StatusBadRequest,
		askEdgeSlug+": ask title is too long (max 120 characters)")
	askEdgeExpect(t, "question over 16 KB", ask(map[string]any{"title": "ci red", "text": strings.Repeat("q", 16385)}), http.StatusBadRequest,
		askEdgeSlug+": ask question is too long (max 16 KB)")
	rec := ask(map[string]any{"title": "ci red", "text": askEdgeQuestion, "model": "opus"})
	if rec.Code != http.StatusConflict || !strings.Contains(askEdgeErr(t, rec), "has no live cell to ask") {
		t.Fatalf("thread-ask on a dead cell: %d %s, want 409 with jobs' dead-cell wording", rec.Code, rec.Body.String())
	}
	if n := askEdgeRows(t, st, j); n != 0 {
		t.Fatalf("refused asks left %d rows", n)
	}
	if l.n() != 0 {
		t.Fatalf("refused asks started %d processes", l.n())
	}
}

// TestAskEdgeNodeActionThreadAskCap: at the non-self cap a remote ask is
// 409 with the cap text and nothing is reserved.
func TestAskEdgeNodeActionThreadAskCap(t *testing.T) {
	st, j, l := askEdgeEnv(t, true)
	askEdgeVis(t, askEdgeSlug, "readonly")
	prev := jobs.MaxAskThreads
	jobs.MaxAskThreads = 1
	t.Cleanup(func() { jobs.MaxAskThreads = prev })
	askEdgeAdd(t, st, askEdgeAsk(j, "nTHIRD", "third", "who@else"))

	rec := askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-ask", map[string]any{"title": "ci red", "text": askEdgeQuestion}, nil)
	askEdgeExpect(t, "thread-ask at the cap", rec, http.StatusConflict, jobs.ErrAskThreadCap.Error())
	if n := askEdgeRows(t, st, j); n != 1 {
		t.Fatalf("rows = %d after the cap refusal, want 1", n)
	}
	if l.n() != 0 {
		t.Fatalf("the cap refusal started %d processes", l.n())
	}
}

// TestAskEdgeNodeActionThreadAskOriginFromIdentity: the reserved row's
// origin node and login are the VERIFIED identity's, whatever the body's
// `from` claims; the success body is {"ok":true,"tid":<tid>}; a stranger's
// ask gets the empty tool list.
func TestAskEdgeNodeActionThreadAskOriginFromIdentity(t *testing.T) {
	st, j, l := askEdgeEnv(t, true)
	askEdgeVis(t, askEdgeSlug, "readonly")

	spoof := map[string]any{"cell": "nOWNER"}
	rec := askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-ask",
		map[string]any{"title": "ci red", "text": askEdgeQuestion, "model": "", "node_id": "nOWNER", "login": "Owner"}, spoof)
	askEdgeExpect(t, "thread-ask", rec, http.StatusOK, "")
	var body struct {
		OK  bool   `json:"ok"`
		TID string `json:"tid"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK || body.TID == "" {
		t.Fatalf("thread-ask body = %s, want {\"ok\":true,\"tid\":...}", rec.Body.String())
	}
	got, err := st.GetThread(j.ID, body.TID)
	if err != nil {
		t.Fatalf("GetThread(%s): %v", body.TID, err)
	}
	if got.OriginNode != "nSTUDIO" || got.OriginLogin != "jess@probook.ai" {
		t.Fatalf("origin = %q/%q, want the verified identity nSTUDIO/jess@probook.ai", got.OriginNode, got.OriginLogin)
	}
	if got.Kind != "headless" || got.Role != "ask" || !got.Answering {
		t.Fatalf("row = %+v, want an answering headless ask row", got)
	}
	if l.n() != 1 {
		t.Fatalf("launcher called %d times, want 1", l.n())
	}
	l.mu.Lock()
	argv := l.argv[0]
	l.mu.Unlock()
	for i, a := range argv {
		if a == "--tools" {
			if i+1 >= len(argv) || argv[i+1] != "" {
				t.Fatalf("a remote ask's --tools value = %q, want \"\"", argv[i+1:])
			}
		}
		if strings.Contains(a, askEdgeQuestion) {
			t.Fatalf("the question rides argv element %q", a)
		}
	}
}

// TestAskEdgeNodeActionThreadSendRefusals: missing fields 400, unknown tid
// 404, a pane row 409, a busy and a closed headless row 409, the bound 400 —
// none reaching a runner. Any caller may send, so these refusals come from
// the row, never from the caller's identity.
func TestAskEdgeNodeActionThreadSendRefusals(t *testing.T) {
	st, j, l := askEdgeEnv(t, true)
	askEdgeVis(t, askEdgeSlug, "readonly")
	stranger := core.TailnetIdentity{ID: "nTHIRD", Login: "who@else"}

	send := func(id core.TailnetIdentity, p map[string]any) *httptest.ResponseRecorder {
		return askEdgeNodeAction(t, id, askEdgeSlug, "thread-send", p, nil)
	}
	askEdgeExpect(t, "missing tid", send(askEdgeStudio, map[string]any{"text": askEdgeQuestion}), http.StatusBadRequest, "missing tid")
	askEdgeExpect(t, "missing text", send(askEdgeStudio, map[string]any{"tid": "t1"}), http.StatusBadRequest, "missing text")
	askEdgeExpect(t, "blank text", send(askEdgeStudio, map[string]any{"tid": "t1", "text": "  "}), http.StatusBadRequest, "missing text")
	askEdgeExpect(t, "unknown tid", send(askEdgeStudio, map[string]any{"tid": "t99", "text": askEdgeQuestion}), http.StatusNotFound, "unknown thread")

	pane := askEdgeAdd(t, st, store.Thread{JobID: j.ID, Kind: "pane", SessionID: "pane-sess", State: "live", Pane: "%5", Window: "@2"})
	askEdgeExpect(t, "pane row", send(askEdgeStudio, map[string]any{"tid": pane.TID, "text": askEdgeQuestion}), http.StatusConflict, "")

	busy := askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai")
	busy.Answering = true
	busyRow := askEdgeAdd(t, st, busy)
	for _, id := range []core.TailnetIdentity{askEdgeStudio, stranger} {
		askEdgeExpect(t, "busy row from "+id.ID, send(id, map[string]any{"tid": busyRow.TID, "text": askEdgeQuestion}), http.StatusConflict, store.ErrThreadAnswering.Error())
	}

	closed := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	if err := st.CloseThread(j.ID, closed.TID, 0); err != nil {
		t.Fatal(err)
	}
	askEdgeExpect(t, "closed row", send(stranger, map[string]any{"tid": closed.TID, "text": askEdgeQuestion}), http.StatusConflict, store.ErrThreadClosed.Error())

	idle := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	askEdgeExpect(t, "too long", send(stranger, map[string]any{"tid": idle.TID, "text": strings.Repeat("q", 16385)}), http.StatusBadRequest,
		askEdgeSlug+": ask question is too long (max 16 KB)")
	if l.n() != 0 {
		t.Fatalf("refused sends started %d processes", l.n())
	}
}

// TestAskEdgeNodeActionThreadSendAnyCaller: a third node may send into
// another node's headless row; the reply is 200 {"ok":true} before the
// answer exists, the stdin carries the SENDER's identity, and the tool list
// is empty.
func TestAskEdgeNodeActionThreadSendAnyCaller(t *testing.T) {
	st, j, l := askEdgeEnv(t, true)
	askEdgeVis(t, askEdgeSlug, "readonly")
	row := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	stranger := core.TailnetIdentity{ID: "nTHIRD", Name: "third.tail0000.ts.net", Login: "who@else"}

	rec := askEdgeNodeAction(t, stranger, askEdgeSlug, "thread-send", map[string]any{"tid": row.TID, "text": askEdgeQuestion}, map[string]any{"cell": "nSTUDIO"})
	askEdgeExpect(t, "send by a third node", rec, http.StatusOK, "")
	var ok struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || !ok.OK {
		t.Fatalf("send body = %s, want {\"ok\":true}", rec.Body.String())
	}
	if !jobs.AskRunning(j.ID, row.TID) {
		t.Fatal("send answered 200 with no run registered")
	}
	if l.n() != 1 {
		t.Fatalf("launcher called %d times, want 1", l.n())
	}
	l.mu.Lock()
	argv, stdin := l.argv[0], l.stdin[0]
	l.mu.Unlock()
	// the stamp's `@` is fenced to U+FF20 (contract Amendment (s)), so the
	// sender reads `who＠else`; the opener's login must appear in neither form
	if !strings.Contains(stdin, "who＠else") || strings.Contains(stdin, "jess＠probook.ai") || strings.Contains(stdin, "jess@probook.ai") {
		t.Fatalf("stdin %q does not name the sender who@else (or names the row's opener)", stdin)
	}
	if !strings.Contains(stdin, askEdgeQuestion) {
		t.Fatalf("stdin %q lacks the question", stdin)
	}
	for i, a := range argv {
		if a == "--tools" && (i+1 >= len(argv) || argv[i+1] != "") {
			t.Fatalf("a stranger's send --tools value = %q, want \"\"", argv[i+1:])
		}
	}
}

// TestAskEdgeNodeActionThreadKillOwnership: missing tid 400, unknown 404,
// pane row 409 (exact text), a different caller 403 `not your thread` even
// when the body's `from` claims the owner, and the true owner succeeds even
// when the body claims someone else.
func TestAskEdgeNodeActionThreadKillOwnership(t *testing.T) {
	st, j, _ := askEdgeEnv(t, false)
	askEdgeVis(t, askEdgeSlug, "readonly")

	kill := func(id core.TailnetIdentity, p map[string]any, from map[string]any) *httptest.ResponseRecorder {
		return askEdgeNodeAction(t, id, askEdgeSlug, "thread-kill", p, from)
	}
	askEdgeExpect(t, "missing tid", kill(askEdgeStudio, nil, nil), http.StatusBadRequest, "missing tid")
	askEdgeExpect(t, "blank tid", kill(askEdgeStudio, map[string]any{"tid": "  "}, nil), http.StatusBadRequest, "missing tid")
	askEdgeExpect(t, "unknown tid", kill(askEdgeStudio, map[string]any{"tid": "t99"}, nil), http.StatusNotFound, "unknown thread")

	pane := askEdgeAdd(t, st, store.Thread{JobID: j.ID, Kind: "pane", SessionID: "pane-sess", State: "live", Pane: "%5", Window: "@2", OriginNode: "nSTUDIO"})
	askEdgeExpect(t, "pane row", kill(askEdgeStudio, map[string]any{"tid": pane.TID}, nil), http.StatusConflict,
		"thread "+pane.TID+" of "+askEdgeSlug+" is a pane thread — only its owner can close it")
	if got, err := st.GetThread(j.ID, pane.TID); err != nil || got.State != "live" {
		t.Fatalf("a refused pane-row kill changed it: %+v %v", got, err)
	}

	row := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	spoof := map[string]any{"cell": "nSTUDIO"}
	for _, id := range []core.TailnetIdentity{
		{ID: "nTHIRD", Login: "who@else"},
		{ID: "nstudio", Login: "jess@probook.ai"}, // case differs: not the same id
		{ID: "", Login: "jess@probook.ai"},
		{ID: "nOWNER", Login: "Owner"}, // even the owner node's id is not the row's opener
	} {
		askEdgeExpect(t, "kill by "+id.ID, kill(id, map[string]any{"tid": row.TID}, spoof), http.StatusForbidden, "not your thread")
		got, err := st.GetThread(j.ID, row.TID)
		if err != nil || got.State != "live" {
			t.Fatalf("a refused kill changed the row: %+v %v", got, err)
		}
	}

	rec := kill(askEdgeStudio, map[string]any{"tid": row.TID}, map[string]any{"cell": "nTHIRD"})
	if rec.Code != http.StatusOK {
		t.Fatalf("owner kill with a spoofed body: %d %s, want 200", rec.Code, rec.Body.String())
	}
	var ok struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ok); err != nil || !ok.OK {
		t.Fatalf("owner kill body = %s, want {\"ok\":true}", rec.Body.String())
	}
	got, err := st.GetThread(j.ID, row.TID)
	if err != nil || got.State != "closed" || got.Answering {
		t.Fatalf("after an owner kill the row is %+v %v, want closed and not answering", got, err)
	}
}

// TestAskEdgeNodeActionThreadKillCancelsARun: the owner's kill of an
// answering row cancels its run (note "ask cancelled") and closes it.
func TestAskEdgeNodeActionThreadKillCancelsARun(t *testing.T) {
	st, j, _ := askEdgeEnv(t, true)
	askEdgeVis(t, askEdgeSlug, "public")
	row := askEdgeAdd(t, st, askEdgeAsk(j, "nSTUDIO", "studio-mini", "jess@probook.ai"))
	if err := jobs.AskThreadSend(j, row.TID, askEdgeQuestion, jobs.ThreadOrigin{Node: "nSTUDIO", Label: "studio-mini", Login: "jess@probook.ai"}); err != nil {
		t.Fatalf("fixture send: %v", err)
	}
	if !jobs.AskRunning(j.ID, row.TID) {
		t.Fatal("fixture: no run registered")
	}
	askEdgeExpect(t, "owner kill", askEdgeNodeAction(t, askEdgeStudio, askEdgeSlug, "thread-kill", map[string]any{"tid": row.TID}, nil), http.StatusOK, "")
	if jobs.AskRunning(j.ID, row.TID) {
		t.Fatal("run still registered after the owner's kill")
	}
	got, err := st.GetThread(j.ID, row.TID)
	if err != nil || got.State != "closed" || got.Answering || got.Note != "ask cancelled" {
		t.Fatalf("after kill: %+v %v, want closed, not answering, note \"ask cancelled\"", got, err)
	}
}

// TestAskEdgeRegisterSetsAskDoneHook: Register wires jobs.AskDoneHook.
func TestAskEdgeRegisterSetsAskDoneHook(t *testing.T) {
	prev := jobs.AskDoneHook
	t.Cleanup(func() { jobs.AskDoneHook = prev })
	jobs.AskDoneHook = nil
	Register(http.NewServeMux())
	if jobs.AskDoneHook == nil {
		t.Fatal("Register left jobs.AskDoneHook nil")
	}
}
