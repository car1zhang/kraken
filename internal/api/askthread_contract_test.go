package api

// askthread_contract_test.go — athena-logic's BLIND contract tests for the
// ask-threads port (kraken-core PR #5), Slice C (api): the webThread and
// thread-read wire keys, webBoard.node_id, threadsReq.headless and the POST
// /api/threads branch/send/kill/revive dispatch, threadErrStatus' new codes,
// nodeThreadActions, /api/node/action admitting the three thread actions on
// readonly and public with the origin taken from the verified identity, the
// paired proxy forwarding them, and Register wiring jobs.AskDoneHook.
//
// Authored from artifacts/50-contract.md alone; pre-existing TEST helpers
// (useTestStore, mustCreateStoreJob, boardCache's fields as board_test.go
// builds one) are reused.
//
// Isolation: a temp store under an isolated HOME; nodeSelf is always swapped
// to a fixture envelope (so cachedNodeSelf's cache is never primed by this
// file); KRAKEN_TAILSCALE names a missing file; every path that would start
// an ask swaps the jobs seams through jobs.SetAskSeamsForTest (no tmux, no
// claude). buildWebBoard is never called (it runs discovery).
//
// LOGIC dimension. Every test is TestAskContract…; every package-level
// identifier is prefixed askContract.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

const (
	askContractSlug          = "tech-comms-rollout"
	askContractThreadSession = "7c1e9d44-0000-4000-8000-000000000003"
	askContractZeusSession   = "b4a2e1f0-0000-4000-8000-000000000001"
	askContractQuestion      = "why is CI red on the rollout PR?"
)

var askContractStudio = core.TailnetIdentity{ID: "nSTUDIO", Name: "studio-mini.tail0000.ts.net", Login: "jess@probook.ai"}

var askContractStudioOrigin = map[string]any{"node_id": "nSTUDIO", "label": "studio-mini", "login": "jess@probook.ai"}

// askContractEnv: scratch store under a sandboxed HOME, no tailscale, fixed
// device label and operator, fleet on, nodeSelf answering the owner fixture.
func askContractEnv(t *testing.T) *store.Store {
	t.Helper()
	st := useTestStore(t)
	t.Setenv("KRAKEN_TAILSCALE", filepath.Join(t.TempDir(), "no-such-tailscale"))
	t.Setenv("KRAKEN_DEVICE_NAME", "owner-mac")
	t.Setenv("KRAKEN_OPERATOR", "Owner")
	t.Setenv("KRAKEN_NODES", "")
	os.Unsetenv("KRAKEN_NODES")
	askContractNodeSelf(t, jobs.NodeEnvelope{ID: "nOWNER", Label: "owner-mac", Operator: "Owner"})
	webBoardCache.invalidate()
	t.Cleanup(webBoardCache.invalidate)
	return st
}

func askContractNodeSelf(t *testing.T, env jobs.NodeEnvelope) {
	t.Helper()
	orig := nodeSelf
	nodeSelf = func() jobs.NodeEnvelope { return env }
	t.Cleanup(func() { nodeSelf = orig })
}

// askContractCountingCache swaps webBoardCache for one that only an
// invalidate can rebuild inside its ttl, and returns its build counter.
func askContractCountingCache(t *testing.T) *int32 {
	t.Helper()
	orig := webBoardCache
	var builds int32
	webBoardCache = &boardCache{
		ttl:   time.Hour,
		now:   time.Now,
		build: func() webBoard { atomic.AddInt32(&builds, 1); return webBoard{} },
	}
	t.Cleanup(func() { webBoardCache = orig })
	return &builds
}

// askContractInvalidates fails unless fn invalidated the (counting) board
// cache: a primed cache rebuilds on the next get only after an invalidate.
func askContractInvalidates(t *testing.T, builds *int32, what string, fn func()) {
	t.Helper()
	webBoardCache.get()
	was := atomic.LoadInt32(builds)
	fn()
	webBoardCache.get()
	if got := atomic.LoadInt32(builds); got != was+1 {
		t.Errorf("%s did not invalidate the board cache (builds %d → %d)", what, was, got)
	}
}

func askContractJob(t *testing.T, st *store.Store, slug, vis, agent string) *jobs.Job {
	t.Helper()
	j := mustCreateStoreJob(t, st, store.Job{Slug: slug, Repo: "monolith", State: "working", Visibility: vis, Agent: agent})
	webBoardCache.invalidate()
	return j
}

// askContractAsk reserves the running example's headless row from nSTUDIO.
func askContractAsk(t *testing.T, st *store.Store, j *jobs.Job, answering bool) store.Thread {
	t.Helper()
	row, err := st.AddThreadNext(store.Thread{
		JobID: j.ID, Title: "why is CI red", SessionID: askContractThreadSession, ParentSession: askContractZeusSession,
		State: "live", Seed: askContractQuestion, Kind: "headless", Role: "ask",
		OriginNode: "nSTUDIO", OriginLabel: "studio-mini", OriginLogin: "jess@probook.ai", Answering: answering,
	})
	if err != nil {
		t.Fatalf("AddThreadNext(headless): %v", err)
	}
	return row
}

func askContractServe(h http.Handler, method, target, body string, ctx context.Context) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if ctx != nil {
		req = req.WithContext(ctx)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// askContractNodeCtx is the context the request gate leaves for a node-tier
// caller with the given verified tailnet identity.
func askContractNodeCtx(id core.TailnetIdentity) context.Context {
	ctx := context.WithValue(context.Background(), gateTierKey, TierNode)
	return context.WithValue(ctx, gateIdentityKey, id)
}

func askContractNodeAction(t *testing.T, id core.TailnetIdentity, slug, action string, payload map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(map[string]any{"slug": slug, "action": action, "payload": payload, "from": map[string]any{"cell": ""}})
	if err != nil {
		t.Fatal(err)
	}
	return askContractServe(http.HandlerFunc(nodeActionHandler), http.MethodPost, "/api/node/action", string(b), askContractNodeCtx(id))
}

func askContractObj(t *testing.T, label string, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%s: not a JSON object: %v (%s)", label, err, b)
	}
	return m
}

func askContractErr(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	m := askContractObj(t, "error body", rec.Body.Bytes())
	s, _ := m["error"].(string)
	return s
}

// askContractThreadsWire is GET /api/threads?slug=… as raw objects by tid.
func askContractThreadsWire(t *testing.T, slug string) map[string]map[string]any {
	t.Helper()
	rec := askContractServe(http.HandlerFunc(threadsHandler), http.MethodGet, "/api/threads?slug="+slug, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/threads: %d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Threads []map[string]any `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, th := range resp.Threads {
		tid, _ := th["tid"].(string)
		out[tid] = th
	}
	return out
}

func askContractRead(t *testing.T, slug, tid string) map[string]any {
	t.Helper()
	rec := askContractServe(http.HandlerFunc(threadReadHandler), http.MethodGet, "/api/thread/read?slug="+slug+"&tid="+tid, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/thread/read %s: %d %s", tid, rec.Code, rec.Body)
	}
	return askContractObj(t, "thread read", rec.Body.Bytes())
}

func askContractPostThreads(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	return askContractServe(http.HandlerFunc(threadsHandler), http.MethodPost, "/api/threads", body, nil)
}

func askContractRegistry(t *testing.T, st *store.Store, id, label, ip string, port int) {
	t.Helper()
	if err := st.UpsertNode(&store.NodeRow{ID: id, Label: label, Operator: "Jess",
		DNSName: label + ".tail0000.ts.net", IP: ip, Port: port, Build: "b", Contract: 7,
		FirstSeen: 1, LastSeen: 1, Online: true}); err != nil {
		t.Fatal(err)
	}
}

// askContractLaunch records every launcher call; each run hangs until killed.
type askContractLaunch struct {
	mu    sync.Mutex
	stdin []string
	argv  [][]string
}

func (f *askContractLaunch) snapshot() ([]string, [][]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.stdin...), append([][]string(nil), f.argv...)
}

// askContractSeams swaps the three jobs seams for j's world (live cell,
// forkable zeus, hanging launcher) and, on cleanup, kills every run still
// registered for j's rows before restoring.
func askContractSeams(t *testing.T, st *store.Store, j *jobs.Job) *askContractLaunch {
	t.Helper()
	f := &askContractLaunch{}
	launch := jobs.AskLauncherFunc(func(ctx context.Context, argv []string, dir string, env []string, stdin string) (func() (string, error), error) {
		f.mu.Lock()
		f.stdin = append(f.stdin, stdin)
		f.argv = append(f.argv, append([]string(nil), argv...))
		f.mu.Unlock()
		return func() (string, error) {
			<-ctx.Done()
			return "", fmt.Errorf("signal: killed")
		}, nil
	})
	restore := jobs.SetAskSeamsForTest(
		func(*jobs.Job) (string, bool) { return jobs.CellSession(j), true },
		func(*jobs.Job) (*core.Peer, error) {
			return &core.Peer{Pane: "%1", Name: jobs.ZeusName(j), Agent: "claude", Depth: 0,
				TmuxSession: jobs.CellSession(j), SessionID: askContractZeusSession}, nil
		},
		launch,
	)
	t.Cleanup(func() {
		rows, _ := st.ListThreads(j.ID)
		for _, r := range rows {
			jobs.KillAskRun(j.ID, r.TID)
		}
		time.Sleep(50 * time.Millisecond)
		restore()
	})
	return f
}

// askContractSoon fails unless fn returns within 5 s (the route answers
// before the answer exists, while the fake launcher still hangs).
func askContractSoon(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("%s did not answer while the ask was still running", what)
	}
}

// askContractSelfWorktree makes the self cwd (the job's worktree) resolve.
func askContractSelfWorktree(t *testing.T, j *jobs.Job) {
	t.Helper()
	repos := filepath.Join(t.TempDir(), "Developer")
	if err := os.MkdirAll(filepath.Join(repos, j.Repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)
	repo, err := jobs.ResolveRepo(j.Repo)
	if err != nil {
		t.Fatalf("ResolveRepo: %v", err)
	}
	wt := jobs.CellWorktree(j, repo)
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+filepath.Join(repos, j.Repo, ".git")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// webThread, the thread read, webBoard.node_id, the request types
// ---------------------------------------------------------------------------

// Contract C `webThread`: "kind is present on every thread object; the
// existing role key stays present on every thread object; origin is present
// iff any of the three row fields is non-empty; answering present iff true;
// note present iff non-empty" — origin as {node_id, label, login}.
func TestAskContractWebThreadKeys(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", Title: "legacy", SessionID: "s1", State: "live", Pane: "%7", Window: "@3"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t2", Title: "mind", SessionID: "s2", State: "live", Role: "mind", Pane: "%8", Window: "@4"}); err != nil {
		t.Fatal(err)
	}
	asking := askContractAsk(t, st, j, true)
	noted := askContractAsk(t, st, j, true)
	if err := st.FinishThreadAnswering(j.ID, noted.TID, "ask failed: exit status 1"); err != nil {
		t.Fatal(err)
	}
	loginOnly, err := st.AddThreadNext(store.Thread{JobID: j.ID, Title: "login only", SessionID: "s5", State: "live", Kind: "headless", OriginLogin: "jess@probook.ai"})
	if err != nil {
		t.Fatal(err)
	}

	wire := askContractThreadsWire(t, askContractSlug)
	for tid, wantRole := range map[string]string{"t1": "ask", "t2": "mind"} {
		pane := wire[tid]
		if pane == nil || pane["kind"] != "pane" || pane["role"] != wantRole {
			t.Fatalf("pane row %s = %v, want kind pane, role %s", tid, pane, wantRole)
		}
		for _, k := range []string{"origin", "answering", "note"} {
			if _, ok := pane[k]; ok {
				t.Errorf("pane row %s carries %q: %v", tid, k, pane)
			}
		}
	}

	a := wire[asking.TID]
	if a == nil || a["kind"] != "headless" || a["role"] != "ask" || a["answering"] != true {
		t.Fatalf("answering headless row = %v, want kind headless, role ask, answering true", a)
	}
	if !reflect.DeepEqual(a["origin"], askContractStudioOrigin) {
		t.Errorf("headless origin = %#v, want %#v", a["origin"], askContractStudioOrigin)
	}
	if _, ok := a["note"]; ok {
		t.Errorf("a headless row with no note carries note: %v", a)
	}

	n := wire[noted.TID]
	if n == nil || n["kind"] != "headless" || n["note"] != "ask failed: exit status 1" {
		t.Fatalf("noted headless row = %v, want its note", n)
	}
	if _, ok := n["answering"]; ok {
		t.Errorf("a finished headless row carries answering: %v", n)
	}

	lo := wire[loginOnly.TID]
	if !reflect.DeepEqual(lo["origin"], map[string]any{"node_id": "", "label": "", "login": "jess@probook.ai"}) {
		t.Errorf("a row with only an origin login: origin = %#v, want present with the three keys", lo["origin"])
	}
}

// Contract C `GET /api/thread/read`: "live is t.State == "live" && (kind ==
// "headless" || t.Pane != ""); the response additionally carries kind
// (always), answering (bool, always), note (string, always, "" when none),
// origin (object, omitted when the row has no origin)".
func TestAskContractThreadReadKeysAndLive(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", SessionID: "s-pane", State: "live"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t2", SessionID: "s-pane2", State: "live", Pane: "%9", Window: "@9"}); err != nil {
		t.Fatal(err)
	}
	live := askContractAsk(t, st, j, true)
	closed := askContractAsk(t, st, j, false)
	if err := st.FinishThreadAnswering(j.ID, closed.TID, "ask cancelled"); err != nil {
		t.Fatal(err)
	}
	if err := st.CloseThread(j.ID, closed.TID, 5); err != nil {
		t.Fatal(err)
	}

	a := askContractRead(t, askContractSlug, live.TID)
	if a["live"] != true || a["kind"] != "headless" || a["answering"] != true || a["note"] != "" {
		t.Errorf("live paneless headless read = live %v kind %v answering %v note %#v; want true, headless, true, \"\"", a["live"], a["kind"], a["answering"], a["note"])
	}
	if !reflect.DeepEqual(a["origin"], askContractStudioOrigin) {
		t.Errorf("headless read origin = %#v", a["origin"])
	}
	for _, k := range []string{"blocks", "counted", "live", "pane", "fork_uuid", "source", "session"} {
		if _, ok := a[k]; !ok {
			t.Errorf("headless read lacks the pre-existing key %q: %v", k, a)
		}
	}

	c := askContractRead(t, askContractSlug, closed.TID)
	if c["live"] != false || c["kind"] != "headless" || c["answering"] != false || c["note"] != "ask cancelled" {
		t.Errorf("closed headless read = live %v kind %v answering %v note %#v; want false, headless, false, ask cancelled", c["live"], c["kind"], c["answering"], c["note"])
	}

	p := askContractRead(t, askContractSlug, "t1")
	if p["live"] != false || p["kind"] != "pane" || p["answering"] != false || p["note"] != "" {
		t.Errorf("live paneless PANE read = live %v kind %v answering %v note %#v; want false, pane, false, \"\"", p["live"], p["kind"], p["answering"], p["note"])
	}
	if _, ok := p["origin"]; ok {
		t.Errorf("a row with no origin carries origin: %v", p)
	}

	p2 := askContractRead(t, askContractSlug, "t2")
	if p2["live"] != true || p2["kind"] != "pane" {
		t.Errorf("live PANE read with a pane = live %v kind %v; want true, pane", p2["live"], p2["kind"])
	}
}

// Contract C `webBoard`: "NodeID string `json:"node_id,omitempty"`" — present
// when set, absent when empty.
func TestAskContractWebBoardNodeIDKey(t *testing.T) {
	b, err := json.Marshal(webBoard{NodeID: "nOWNER"})
	if err != nil {
		t.Fatal(err)
	}
	if m := askContractObj(t, "board", b); m["node_id"] != "nOWNER" {
		t.Errorf("webBoard{NodeID} = %s, want node_id nOWNER", b)
	}
	b, err = json.Marshal(webBoard{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := askContractObj(t, "board", b)["node_id"]; ok {
		t.Errorf("an empty NodeID still serialises node_id: %s", b)
	}
}

// Contract C `threadsReq`: "Headless *bool `json:"headless,omitempty"`" — an
// explicit false is distinguishable from absent.
func TestAskContractThreadsReqHeadless(t *testing.T) {
	var absent, off, on threadsReq
	for _, c := range []struct {
		body string
		into *threadsReq
	}{
		{`{"op":"branch","slug":"x"}`, &absent},
		{`{"op":"branch","slug":"x","headless":false}`, &off},
		{`{"op":"branch","slug":"x","headless":true}`, &on},
	} {
		if err := json.Unmarshal([]byte(c.body), c.into); err != nil {
			t.Fatalf("decode %s: %v", c.body, err)
		}
	}
	if absent.Headless != nil {
		t.Errorf("absent headless decoded to %v, want nil", *absent.Headless)
	}
	if off.Headless == nil || *off.Headless {
		t.Errorf("headless:false decoded to %v, want a pointer to false", off.Headless)
	}
	if on.Headless == nil || !*on.Headless {
		t.Errorf("headless:true decoded to %v, want a pointer to true", on.Headless)
	}
}

// Contract C `nodeActionPayload`: fields Title/TID/Model under the keys
// title/tid/model.
func TestAskContractNodeActionPayloadFields(t *testing.T) {
	var p nodeActionPayload
	if err := json.Unmarshal([]byte(`{"title":"why is CI red","tid":"t3","model":"opus"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.Title != "why is CI red" || p.TID != "t3" || p.Model != "opus" {
		t.Errorf("nodeActionPayload = %+v, want title/tid/model decoded", p)
	}
}

// ---------------------------------------------------------------------------
// POST /api/threads
// ---------------------------------------------------------------------------

// Contract C `POST /api/threads branch`: with role absent or "ask", headless
// absent or true → jobs.AskThread with text required (400 missing text);
// headless false → BranchThreadWithRole exactly as today (text optional).
// BranchThreadWithRole's first refusal today is a codex job's, so a codex job
// shows which path a text-less branch took without reaching a cell.
func TestAskContractBranchHeadlessDispatch(t *testing.T) {
	st := askContractEnv(t)
	askContractJob(t, st, "codex-job", "readonly", "codex")

	for _, body := range []string{
		`{"slug":"codex-job","op":"branch","title":"why is CI red"}`,
		`{"slug":"codex-job","op":"branch","title":"why is CI red","headless":true}`,
		`{"slug":"codex-job","op":"branch","title":"why is CI red","role":"ask"}`,
		`{"slug":"codex-job","op":"branch","title":"why is CI red","role":"ask","headless":true}`,
	} {
		rec := askContractPostThreads(t, body)
		if rec.Code != http.StatusBadRequest || askContractErr(t, rec) != "missing text" {
			t.Errorf("%s = %d %s, want 400 missing text", body, rec.Code, rec.Body)
		}
	}

	for _, body := range []string{
		`{"slug":"codex-job","op":"branch","title":"why is CI red","headless":false}`,
		`{"slug":"codex-job","op":"branch","title":"why is CI red","role":"mind"}`,
		`{"slug":"codex-job","op":"branch","title":"why is CI red","role":"mind","headless":false}`,
	} {
		rec := askContractPostThreads(t, body)
		msg := askContractErr(t, rec)
		if rec.Code == http.StatusBadRequest || rec.Code == http.StatusOK || !strings.Contains(strings.ToLower(msg), "codex") {
			t.Errorf("%s = %d %q, want the pane path's codex refusal (text optional)", body, rec.Code, msg)
		}
	}
}

// Contract C `POST /api/threads branch`: "With role: "mind": … headless: true
// → 400 {"error":"a mind thread cannot be headless"}".
func TestAskContractBranchMindHeadlessRefused(t *testing.T) {
	st := askContractEnv(t)
	askContractJob(t, st, askContractSlug, "readonly", "")
	rec := askContractPostThreads(t, fmt.Sprintf(`{"slug":%q,"op":"branch","title":"t","text":"q","role":"mind","headless":true}`, askContractSlug))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mind+headless = %d %s, want 400", rec.Code, rec.Body)
	}
	if m := askContractObj(t, "refusal", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"error": "a mind thread cannot be headless"}) {
		t.Errorf("mind+headless body = %v, want {\"error\":\"a mind thread cannot be headless\"}", m)
	}
}

// Contract C `POST /api/threads branch`: headless absent →
// jobs.AskThread(j, jobs.SelfThreadOrigin(), title, text, model); success
// body exactly {"ok": true, "tid": "<tid>"}, answered before the answer exists.
func TestAskContractLocalBranchHeadlessDefaultSucceeds(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	askContractSelfWorktree(t, j)
	fake := askContractSeams(t, st, j)

	var rec *httptest.ResponseRecorder
	askContractSoon(t, "POST /api/threads branch", func() {
		rec = askContractPostThreads(t, fmt.Sprintf(`{"slug":%q,"op":"branch","title":"why is CI red","text":%q,"model":"opus"}`,
			askContractSlug, askContractQuestion))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("headless branch = %d %s, want 200", rec.Code, rec.Body)
	}
	m := askContractObj(t, "branch reply", rec.Body.Bytes())
	tid, _ := m["tid"].(string)
	if m["ok"] != true || tid == "" || len(m) != 2 {
		t.Fatalf("branch reply = %v, want exactly {ok: true, tid}", m)
	}
	row, err := st.GetThread(j.ID, tid)
	if err != nil {
		t.Fatalf("the returned tid %q has no row: %v", tid, err)
	}
	self := jobs.SelfThreadOrigin()
	if row.Kind != "headless" || row.Role != "ask" || row.OriginNode != self.Node || row.OriginLabel != "owner-mac" || row.OriginLogin != "Owner" ||
		row.Pane != "" || row.Window != "" || row.State != "live" || !row.Answering || row.Model != "opus" ||
		row.ParentSession != askContractZeusSession || row.Seed != askContractQuestion {
		t.Errorf("headless branch row = %+v, want a live answering headless ask row from this node (owner-mac / Owner), no pane", row)
	}
	stdin, _ := fake.snapshot()
	if len(stdin) != 1 || !strings.HasPrefix(stdin[0], "[from owner-mac · Owner] "+askContractQuestion) {
		t.Errorf("launcher stdin = %q, want one run carrying the owner's prefix and the question", stdin)
	}
}

// Contract C `POST /api/threads branch`: "headless: true" explicitly takes
// the same AskThread path.
func TestAskContractLocalBranchHeadlessTrueSucceeds(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	askContractSelfWorktree(t, j)
	askContractSeams(t, st, j)

	var rec *httptest.ResponseRecorder
	askContractSoon(t, "POST /api/threads branch headless:true", func() {
		rec = askContractPostThreads(t, fmt.Sprintf(`{"slug":%q,"op":"branch","title":"why","text":"why?","role":"ask","headless":true}`, askContractSlug))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("headless:true branch = %d %s, want 200", rec.Code, rec.Body)
	}
	tid, _ := askContractObj(t, "reply", rec.Body.Bytes())["tid"].(string)
	row, err := st.GetThread(j.ID, tid)
	if err != nil || row.Kind != "headless" {
		t.Errorf("headless:true row = %+v, %v; want a headless row", row, err)
	}
}

// Contract C `POST /api/threads send` + B `SendToThread`: a headless row
// answers 200 {"ok": true} before the answer exists, as this node's operator.
func TestAskContractLocalSendOnAHeadlessRow(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	row := askContractAsk(t, st, j, false)
	fake := askContractSeams(t, st, j)

	var rec *httptest.ResponseRecorder
	askContractSoon(t, "POST /api/threads send", func() {
		rec = askContractPostThreads(t, fmt.Sprintf(`{"slug":%q,"op":"send","tid":%q,"text":"and the flake?"}`, askContractSlug, row.TID))
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("send to a headless row = %d %s, want 200", rec.Code, rec.Body)
	}
	if m := askContractObj(t, "send reply", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"ok": true}) {
		t.Errorf("send reply = %v, want {\"ok\": true}", m)
	}
	got, _ := st.GetThread(j.ID, row.TID)
	if got == nil || !got.Answering {
		t.Errorf("after send = %+v, want answering", got)
	}
	stdin, argv := fake.snapshot()
	if len(stdin) != 1 || !strings.HasPrefix(stdin[0], "[from owner-mac · Owner] and the flake?") {
		t.Errorf("launcher stdin = %q, want one run whose stdin carries the owner's prefix", stdin)
	}
	if len(argv) == 1 && (len(argv[0]) < 2 || argv[0][len(argv[0])-2] != "-r" || argv[0][len(argv[0])-1] != row.SessionID) {
		t.Errorf("send argv = %q, want a resume of %s", argv[0], row.SessionID)
	}
}

// Contract C `POST /api/threads` + B `KillThread` (headless: KillAskRun then
// CloseThread, no tmux) and `ReviveThread` (headless: ClaimClosedThread only).
func TestAskContractLocalKillAndReviveAHeadlessRow(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	row := askContractAsk(t, st, j, false)

	rec := askContractPostThreads(t, fmt.Sprintf(`{"slug":%q,"op":"kill","tid":%q}`, askContractSlug, row.TID))
	if rec.Code != http.StatusOK {
		t.Fatalf("kill headless row: %d %s", rec.Code, rec.Body)
	}
	got, err := st.GetThread(j.ID, row.TID)
	if err != nil || got.State != "closed" {
		t.Fatalf("after kill = %+v, %v; want closed", got, err)
	}

	rec = askContractPostThreads(t, fmt.Sprintf(`{"slug":%q,"op":"revive","tid":%q}`, askContractSlug, row.TID))
	if rec.Code != http.StatusOK {
		t.Fatalf("revive headless row: %d %s", rec.Code, rec.Body)
	}
	got, err = st.GetThread(j.ID, row.TID)
	if err != nil || got.State != "live" || got.Pane != "" || got.Kind != "headless" {
		t.Fatalf("after revive = %+v, %v; want live headless, no pane", got, err)
	}
}

// Contract C `threadErrStatus`: jobs.IsAskInputError → 400; the five new
// sentinels → 409 through errors.Is; ErrNotFound stays 404.
func TestAskContractThreadErrStatus(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")

	for _, err := range []error{
		store.ErrThreadAnswering, store.ErrThreadClosed, jobs.ErrAskThreadCap, store.ErrAskThreadPane, store.ErrThreadKind,
		fmt.Errorf("add thread for job 1: %w", store.ErrThreadKind),
		fmt.Errorf("set pane of thread t3 for job 1: %w", store.ErrAskThreadPane),
	} {
		if got := threadErrStatus(err); got != http.StatusConflict {
			t.Errorf("threadErrStatus(%v) = %d, want 409", err, got)
		}
	}
	if got := threadErrStatus(store.ErrNotFound); got != http.StatusNotFound {
		t.Errorf("threadErrStatus(ErrNotFound) = %d, want 404 as today", got)
	}

	_, tooLong := jobs.AskThread(j, jobs.SelfThreadOrigin(), strings.Repeat("x", 121), askContractQuestion, "")
	if !jobs.IsAskInputError(tooLong) {
		t.Fatalf("fixture: a 121-rune title = %v, want an ask input error", tooLong)
	}
	if got := threadErrStatus(tooLong); got != http.StatusBadRequest {
		t.Errorf("threadErrStatus(title too long) = %d, want 400", got)
	}
}

// ---------------------------------------------------------------------------
// /api/node/action
// ---------------------------------------------------------------------------

// Contract C `nodeThreadActions`: exactly the three.
func TestAskContractNodeThreadActions(t *testing.T) {
	want := map[string]bool{"thread-ask": true, "thread-send": true, "thread-kill": true}
	if !reflect.DeepEqual(nodeThreadActions, want) {
		t.Errorf("nodeThreadActions = %v, want %v", nodeThreadActions, want)
	}
}

// Contract C `POST /api/node/action`: "an action in nodeThreadActions
// proceeds WITHOUT the publicJob 403 — readonly and public alike"; its own
// validation answers where a non-thread action on readonly answers 403 not
// public.
func TestAskContractNodeActionAdmitsThreadActionsOnReadonlyAndPublic(t *testing.T) {
	st := askContractEnv(t)
	askContractJob(t, st, "ro-job", "readonly", "")
	askContractJob(t, st, "pub-job", "public", "")

	for _, slug := range []string{"ro-job", "pub-job"} {
		for _, c := range []struct {
			action  string
			payload map[string]any
			want    string
		}{
			{"thread-ask", map[string]any{"title": "", "text": askContractQuestion}, "missing title"},
			{"thread-ask", map[string]any{"title": "why is CI red", "text": "  "}, "missing text"},
			{"thread-send", map[string]any{"tid": "", "text": "and the flake?"}, "missing tid"},
			{"thread-send", map[string]any{"tid": "t3", "text": ""}, "missing text"},
			{"thread-kill", map[string]any{"tid": ""}, "missing tid"},
		} {
			rec := askContractNodeAction(t, askContractStudio, slug, c.action, c.payload)
			if rec.Code != http.StatusBadRequest || askContractErr(t, rec) != c.want {
				t.Errorf("%s %s %v = %d %s, want 400 %q", slug, c.action, c.payload, rec.Code, rec.Body, c.want)
			}
		}
	}
	rec := askContractNodeAction(t, askContractStudio, "ro-job", "color", map[string]any{"color": "red"})
	if rec.Code != http.StatusForbidden || askContractErr(t, rec) != "not public" {
		t.Errorf("color on readonly = %d %s, want today's 403 not public", rec.Code, rec.Body)
	}
}

// Contract C `POST /api/node/action` `thread-ask`: "400 bad model — want
// haiku, sonnet, opus or fable on a bad alias".
func TestAskContractNodeThreadAskBadModel(t *testing.T) {
	st := askContractEnv(t)
	askContractJob(t, st, askContractSlug, "readonly", "")
	rec := askContractNodeAction(t, askContractStudio, askContractSlug, "thread-ask",
		map[string]any{"title": "why is CI red", "text": askContractQuestion, "model": "gpt-5"})
	if rec.Code != http.StatusBadRequest || askContractErr(t, rec) != "bad model — want haiku, sonnet, opus or fable" {
		t.Errorf("bad model = %d %s", rec.Code, rec.Body)
	}
}

// Contract C `/api/node/action thread-ask`: on readonly and public, success
// {"ok": true, "tid"}; the row's origin is identityOf(r).ID, senderLabel(r)
// and identityOf(r).Login — never the body; the success invalidates the board
// cache and logs node-action (never the text).
func TestAskContractNodeThreadAskSucceeds(t *testing.T) {
	for _, vis := range []string{"readonly", "public"} {
		t.Run(vis, func(t *testing.T) {
			st := askContractEnv(t)
			j := askContractJob(t, st, askContractSlug, vis, "")
			askContractRegistry(t, st, "nSTUDIO", "studio-mini", "100.70.1.2", 7777)
			fake := askContractSeams(t, st, j)
			builds := askContractCountingCache(t)

			body := fmt.Sprintf(`{"slug":%q,"action":"thread-ask","payload":{"title":"why is CI red","text":%q,"model":"opus","node_id":"nEVIL","login":"evil@x"},"from":{"cell":"nEVIL"}}`,
				askContractSlug, askContractQuestion)
			var rec *httptest.ResponseRecorder
			askContractInvalidates(t, builds, "a successful thread-ask", func() {
				askContractSoon(t, "thread-ask", func() {
					rec = askContractServe(http.HandlerFunc(nodeActionHandler), http.MethodPost, "/api/node/action", body, askContractNodeCtx(askContractStudio))
				})
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("thread-ask on %s = %d %s, want 200", vis, rec.Code, rec.Body)
			}
			m := askContractObj(t, "thread-ask reply", rec.Body.Bytes())
			tid, _ := m["tid"].(string)
			if m["ok"] != true || tid == "" || len(m) != 2 {
				t.Fatalf("thread-ask reply = %v, want exactly {ok: true, tid}", m)
			}
			row, err := st.GetThread(j.ID, tid)
			if err != nil {
				t.Fatalf("no row for %q: %v", tid, err)
			}
			if row.Kind != "headless" || row.Role != "ask" || row.OriginNode != "nSTUDIO" || row.OriginLabel != "studio-mini" || row.OriginLogin != "jess@probook.ai" {
				t.Errorf("row kind/role/origin = %q/%q %q/%q/%q, want headless/ask from the verified nSTUDIO / studio-mini / jess@probook.ai",
					row.Kind, row.Role, row.OriginNode, row.OriginLabel, row.OriginLogin)
			}
			if row.Model != "opus" || row.Title != "why is CI red" || row.Seed != askContractQuestion || !row.Answering {
				t.Errorf("row = %+v, want model opus, the title, the question as seed, answering", row)
			}
			stdin, argv := fake.snapshot()
			// the stamp's `@` is fenced to U+FF20 on every run (contract Amendment (s))
			if len(stdin) != 1 || !strings.HasPrefix(stdin[0], "[from studio-mini · jess＠probook.ai] "+askContractQuestion) {
				t.Errorf("launcher stdin = %q, want one run prefixed with the verified sender", stdin)
			}
			if len(argv) == 1 {
				for i, a := range argv[0] {
					if a == "--tools" && (i+1 >= len(argv[0]) || argv[0][i+1] != "") {
						t.Errorf("a remote ask carries tools: %q", argv[0])
					}
				}
			}

			evs, err := st.ListEvents(500, 0)
			if err != nil {
				t.Fatal(err)
			}
			var found bool
			for _, e := range evs {
				if e.Event != "node-action" {
					continue
				}
				dump := fmt.Sprintf("%+v", e)
				if strings.Contains(dump, "thread-ask") && strings.Contains(dump, askContractSlug) {
					found = true
					if !strings.Contains(dump, "studio-mini") {
						t.Errorf("node-action event lacks the sender label: %s", dump)
					}
					if strings.Contains(dump, askContractQuestion) {
						t.Errorf("node-action event carries the question text: %s", dump)
					}
				}
			}
			if !found {
				t.Error("no node-action event naming thread-ask and the slug")
			}
		})
	}
}

// Contract C `/api/node/action thread-ask`: an empty model alias is allowed.
func TestAskContractNodeThreadAskEmptyModel(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	askContractRegistry(t, st, "nSTUDIO", "studio-mini", "100.70.1.2", 7777)
	askContractSeams(t, st, j)

	var rec *httptest.ResponseRecorder
	askContractSoon(t, "thread-ask", func() {
		rec = askContractNodeAction(t, askContractStudio, askContractSlug, "thread-ask",
			map[string]any{"title": "why is CI red", "text": askContractQuestion})
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("thread-ask with no model = %d %s, want 200", rec.Code, rec.Body)
	}
}

// Contract C `/api/node/action thread-send`: any caller may send — 200
// {"ok": true}; the sender's prefix is its verified identity; the row's
// origin stays the opener's; the cache is invalidated.
func TestAskContractNodeThreadSendSucceeds(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	row := askContractAsk(t, st, j, false)
	askContractRegistry(t, st, "nATTIC", "attic-mac", "100.70.1.9", 7777)
	fake := askContractSeams(t, st, j)
	builds := askContractCountingCache(t)
	attic := core.TailnetIdentity{ID: "nATTIC", Name: "attic-mac.tail0000.ts.net", Login: "sam@probook.ai"}

	var rec *httptest.ResponseRecorder
	askContractInvalidates(t, builds, "a successful thread-send", func() {
		askContractSoon(t, "thread-send", func() {
			rec = askContractNodeAction(t, attic, askContractSlug, "thread-send", map[string]any{"tid": row.TID, "text": "and the flake?"})
		})
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("thread-send = %d %s, want 200", rec.Code, rec.Body)
	}
	if m := askContractObj(t, "send reply", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"ok": true}) {
		t.Errorf("thread-send reply = %v, want {\"ok\": true}", m)
	}
	got, _ := st.GetThread(j.ID, row.TID)
	if got == nil || !got.Answering || got.OriginNode != "nSTUDIO" || got.OriginLabel != "studio-mini" {
		t.Errorf("after thread-send = %+v, want answering with the opener's origin kept", got)
	}
	stdin, _ := fake.snapshot()
	if len(stdin) != 1 || !strings.HasPrefix(stdin[0], "[from attic-mac · sam＠probook.ai] and the flake?") {
		t.Errorf("launcher stdin = %q, want one run prefixed with the verified sender attic-mac", stdin)
	}
}

// Contract C `thread-kill`: the asker's own headless row on a readonly (and a
// public) job → jobs.KillThread → 200 {"ok": true}; the row is closed and the
// board cache is invalidated.
func TestAskContractNodeThreadKillOwnRow(t *testing.T) {
	for _, vis := range []string{"readonly", "public"} {
		t.Run(vis, func(t *testing.T) {
			st := askContractEnv(t)
			j := askContractJob(t, st, askContractSlug, vis, "")
			row := askContractAsk(t, st, j, false)
			builds := askContractCountingCache(t)

			var rec *httptest.ResponseRecorder
			askContractInvalidates(t, builds, "a successful thread-kill", func() {
				rec = askContractNodeAction(t, askContractStudio, askContractSlug, "thread-kill", map[string]any{"tid": row.TID})
			})
			if rec.Code != http.StatusOK {
				t.Fatalf("thread-kill own row = %d %s, want 200", rec.Code, rec.Body)
			}
			if m := askContractObj(t, "kill reply", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"ok": true}) {
				t.Errorf("kill reply = %v, want {\"ok\": true}", m)
			}
			got, err := st.GetThread(j.ID, row.TID)
			if err != nil || got.State != "closed" {
				t.Errorf("after thread-kill = %+v, %v; want closed", got, err)
			}
		})
	}
}

// Contract C `thread-kill`: "a pane row → 409 {"error":"thread <tid> of
// <slug> is a pane thread — only its owner can close it"}; a row whose
// OriginNode != identityOf(r).ID → 403 {"error":"not your thread"}" — the
// origin is the verified identity, never the body.
func TestAskContractNodeThreadKillOwnership(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	row := askContractAsk(t, st, j, false)
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t9", SessionID: "sp", State: "live", Pane: "%3", Window: "@3"}); err != nil {
		t.Fatal(err)
	}
	other := core.TailnetIdentity{ID: "nATTIC", Name: "attic.tail0000.ts.net", Login: "sam@probook.ai"}

	rec := askContractNodeAction(t, other, askContractSlug, "thread-kill",
		map[string]any{"tid": row.TID, "node_id": "nSTUDIO", "origin": map[string]any{"node_id": "nSTUDIO"}})
	if rec.Code != http.StatusForbidden {
		t.Errorf("kill by another node claiming nSTUDIO in the body = %d %s, want 403", rec.Code, rec.Body)
	} else if m := askContractObj(t, "403", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"error": "not your thread"}) {
		t.Errorf("403 body = %v, want {\"error\":\"not your thread\"}", m)
	}
	if got, _ := st.GetThread(j.ID, row.TID); got == nil || got.State != "live" {
		t.Errorf("a refused kill changed the row: %+v", got)
	}

	rec = askContractNodeAction(t, askContractStudio, askContractSlug, "thread-kill", map[string]any{"tid": "t9"})
	want := map[string]any{"error": "thread t9 of " + askContractSlug + " is a pane thread — only its owner can close it"}
	if rec.Code != http.StatusConflict {
		t.Errorf("kill of a pane row = %d %s, want 409", rec.Code, rec.Body)
	} else if m := askContractObj(t, "409", rec.Body.Bytes()); !reflect.DeepEqual(m, want) {
		t.Errorf("409 body = %v, want %v", m, want)
	}
	if got, _ := st.GetThread(j.ID, "t9"); got == nil || got.State != "live" || got.Pane != "%3" {
		t.Errorf("a refused pane kill changed the row: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// the paired proxy
// ---------------------------------------------------------------------------

type askContractSibling struct {
	mu   sync.Mutex
	path string
	body []byte
}

func (s *askContractSibling) start(t *testing.T, reply string) (string, int) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.path, s.body = r.URL.Path, b
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	host, portS, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portS)
	return host, port
}

// Contract C `POST /api/nodes/action`: "forwards the three actions like any
// other (payload passed through raw)" and relays the sibling's reply;
// `nodesProxyAskTimeout` is 30 s.
func TestAskContractNodesActionForwardsThreadActions(t *testing.T) {
	if nodesProxyAskTimeout != 30*time.Second {
		t.Errorf("nodesProxyAskTimeout = %v, want 30s", nodesProxyAskTimeout)
	}
	st := askContractEnv(t)
	for _, c := range []struct {
		action, payload, reply string
	}{
		{"thread-ask", `{"title":"why is CI red","text":"why is CI red on the rollout PR?","model":"opus"}`, `{"ok":true,"tid":"t3"}`},
		{"thread-send", `{"tid":"t3","text":"and the flake on step 4?"}`, `{"ok":true}`},
		{"thread-kill", `{"tid":"t3"}`, `{"ok":true}`},
	} {
		t.Run(c.action, func(t *testing.T) {
			sib := &askContractSibling{}
			host, port := sib.start(t, c.reply)
			askContractRegistry(t, st, "nOWNER", "owner-mac", host, port)
			rec := askContractServe(http.HandlerFunc(nodesActionHandler), http.MethodPost, "/api/nodes/action",
				fmt.Sprintf(`{"node":"nOWNER","slug":%q,"action":%q,"payload":%s}`, askContractSlug, c.action, c.payload), nil)

			sib.mu.Lock()
			path, fwdBody := sib.path, sib.body
			sib.mu.Unlock()
			if path != "/api/node/action" {
				t.Fatalf("sibling saw %q, want /api/node/action", path)
			}
			var fwd struct {
				Slug    string          `json:"slug"`
				Action  string          `json:"action"`
				Payload json.RawMessage `json:"payload"`
			}
			if err := json.Unmarshal(fwdBody, &fwd); err != nil {
				t.Fatalf("forwarded body: %v (%s)", err, fwdBody)
			}
			if fwd.Slug != askContractSlug || fwd.Action != c.action {
				t.Errorf("forwarded slug/action = %q/%q", fwd.Slug, fwd.Action)
			}
			if !reflect.DeepEqual(askContractObj(t, "fwd payload", fwd.Payload), askContractObj(t, "sent payload", []byte(c.payload))) {
				t.Errorf("forwarded payload = %s, want %s", fwd.Payload, c.payload)
			}
			if rec.Code != http.StatusOK || !reflect.DeepEqual(askContractObj(t, "relay", rec.Body.Bytes()), askContractObj(t, "reply", []byte(c.reply))) {
				t.Errorf("relayed = %d %s, want 200 %s", rec.Code, rec.Body, c.reply)
			}
		})
	}
}

// Contract C `/api/nodes/action` → a sibling's `/api/node/action`, over real
// HTTP: the "owner" side is an httptest server serving nodeActionHandler with
// the gate's context for the verified asker nSTUDIO. Both sides share this
// process' store, so the row the owner side records is read back directly.
func TestAskContractNodesActionRoundTrip(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st, askContractSlug, "readonly", "")
	fake := askContractSeams(t, st, j)

	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/node/action" {
			http.NotFound(w, r)
			return
		}
		nodeActionHandler(w, r.WithContext(askContractNodeCtx(askContractStudio)))
	}))
	t.Cleanup(owner.Close)
	host, portS, err := net.SplitHostPort(owner.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portS)
	askContractRegistry(t, st, "nOWNER", "owner-mac", host, port)
	askContractRegistry(t, st, "nSTUDIO", "studio-mini", "100.70.1.2", 7777)

	var rec *httptest.ResponseRecorder
	askContractSoon(t, "proxied thread-ask", func() {
		rec = askContractServe(http.HandlerFunc(nodesActionHandler), http.MethodPost, "/api/nodes/action",
			fmt.Sprintf(`{"node":"nOWNER","slug":%q,"action":"thread-ask","payload":{"title":"why is CI red","text":%q}}`,
				askContractSlug, askContractQuestion), nil)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("proxied thread-ask = %d %s, want 200", rec.Code, rec.Body)
	}
	tid, _ := askContractObj(t, "proxied reply", rec.Body.Bytes())["tid"].(string)
	if tid == "" {
		t.Fatalf("proxied reply = %s, want {ok: true, tid}", rec.Body)
	}
	row, err := st.GetThread(j.ID, tid)
	if err != nil {
		t.Fatalf("owner side recorded no row for %q: %v", tid, err)
	}
	if row.Kind != "headless" || row.OriginNode != "nSTUDIO" || row.OriginLabel != "studio-mini" || row.OriginLogin != "jess@probook.ai" {
		t.Errorf("recorded row = %q %q/%q/%q, want headless from nSTUDIO/studio-mini/jess@probook.ai", row.Kind, row.OriginNode, row.OriginLabel, row.OriginLogin)
	}

	// end the first answer, then follow up through the proxy
	jobs.KillAskRun(j.ID, tid)
	askContractSoon(t, "proxied thread-send", func() {
		rec = askContractServe(http.HandlerFunc(nodesActionHandler), http.MethodPost, "/api/nodes/action",
			fmt.Sprintf(`{"node":"nOWNER","slug":%q,"action":"thread-send","payload":{"tid":%q,"text":"and the flake?"}}`, askContractSlug, tid), nil)
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("proxied thread-send = %d %s, want 200", rec.Code, rec.Body)
	}
	if m := askContractObj(t, "proxied send", rec.Body.Bytes()); !reflect.DeepEqual(m, map[string]any{"ok": true}) {
		t.Errorf("proxied send reply = %v, want {\"ok\": true}", m)
	}
	if stdin, _ := fake.snapshot(); len(stdin) != 2 {
		t.Errorf("launcher runs = %d, want 2 (the ask and the follow-up)", len(stdin))
	}
}

// ---------------------------------------------------------------------------
// Register
// ---------------------------------------------------------------------------

// Contract C `Register`: "sets jobs.AskDoneHook = webBoardCache.invalidate".
func TestAskContractRegisterWiresAskDoneHook(t *testing.T) {
	askContractEnv(t)
	orig := jobs.AskDoneHook
	t.Cleanup(func() { jobs.AskDoneHook = orig })
	jobs.AskDoneHook = nil
	builds := askContractCountingCache(t)

	Register(http.NewServeMux())
	if jobs.AskDoneHook == nil {
		t.Fatal("Register left jobs.AskDoneHook nil")
	}
	askContractInvalidates(t, builds, "jobs.AskDoneHook()", func() { jobs.AskDoneHook() })
}
