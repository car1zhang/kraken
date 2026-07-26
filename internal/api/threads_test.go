package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
)

// threads_test.go — coverage for threads.go's HTTP surface: POST
// /api/threads' op dispatch and its required-field refusals, GET
// /api/threads' envelope, and GET /api/thread/read's read path (including
// the inherited/fork split, which is the whole reason block.UUID exists).
//
// Every op's SUCCESS path drives real tmux (jobs.BranchThread and friends
// open windows and shell out), so what's exercised here is exactly the layer
// this file owns: decode, resolve, validate, dispatch, map errors. The
// world-facing guards and their refusal text belong to jobs/thread_test.go.

func postThreads(t *testing.T, req threadsReq) *httptest.ResponseRecorder {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodPost, "/api/threads", bytes.NewReader(mustJSON(t, req)))
	rec := httptest.NewRecorder()
	threadsHandler(rec, httpReq)
	return rec
}

func getThreads(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodGet, "/api/threads?"+query, nil)
	rec := httptest.NewRecorder()
	threadsHandler(rec, httpReq)
	return rec
}

func getThreadRead(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()
	httpReq := httptest.NewRequest(http.MethodGet, "/api/thread/read?"+query, nil)
	rec := httptest.NewRecorder()
	threadReadHandler(rec, httpReq)
	return rec
}

// --- GET /api/threads ---

func TestThreadsGetEmptyServesArrayNotNull(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-empty", Repo: "monolith"})

	rec := getThreads(t, "slug=th-empty")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	// B3: never null — a client iterating the array must not have to nil-check
	if got := strings.TrimSpace(rec.Body.String()); got != `{"threads":[]}` {
		t.Fatalf("body = %s, want an empty array", got)
	}
}

func TestThreadsGetServesRowsInOrder(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "th-list", Repo: "monolith"})
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", Title: "why the retry loop", SessionID: "s1", Pane: "%9", Window: "cell-th-list:1", CreatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t2", SessionID: "s2", State: "closed", CreatedAt: 200, ClosedAt: 300}); err != nil {
		t.Fatal(err)
	}

	rec := getThreads(t, "slug=th-list")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Threads []webThread `json:"threads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Threads) != 2 {
		t.Fatalf("threads = %+v, want 2", resp.Threads)
	}
	if resp.Threads[0].TID != "t1" || resp.Threads[1].TID != "t2" {
		t.Fatalf("order = %+v, want t1 then t2", resp.Threads)
	}
	if resp.Threads[0].Title != "why the retry loop" || resp.Threads[0].Pane != "%9" || resp.Threads[0].State != "live" {
		t.Fatalf("t1 = %+v", resp.Threads[0])
	}
	// a closed thread keeps its row but loses its pane (CloseThread clears
	// it) — the wire must not resurrect a recycled pane id
	if resp.Threads[1].Pane != "" || resp.Threads[1].ClosedAt != 300 {
		t.Fatalf("t2 = %+v", resp.Threads[1])
	}
}

func TestThreadsGetMissingSlug(t *testing.T) {
	useTestStore(t)
	if rec := getThreads(t, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

func TestThreadsGetUnknownSlug(t *testing.T) {
	useTestStore(t)
	if rec := getThreads(t, "slug=nope"); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// --- POST /api/threads: dispatch + required fields ---

func TestThreadsPostUnknownOp(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-op", Repo: "monolith"})

	for _, op := range []string{"", "fork", "BRANCH"} {
		rec := postThreads(t, threadsReq{Slug: "th-op", Op: op})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("op %q: status %d, want 400, body %s", op, rec.Code, rec.Body.String())
		}
		// the refusal must name the valid forms, not just say "no"
		if got := decodeErr(t, rec); !strings.Contains(got, "branch") || !strings.Contains(got, "rename") {
			t.Fatalf("op %q: error = %q", op, got)
		}
	}
}

// TestThreadsPostMissingRequiredFields sweeps every op's own required
// fields. None of these may reach jobs/thread.go at all — a missing tid
// would otherwise surface as a store "not found" 404, and a missing text as
// a bare Enter typed into a live pane.
func TestThreadsPostMissingRequiredFields(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-req", Repo: "monolith"})

	tests := []struct {
		name string
		req  threadsReq
		want string
	}{
		{"branch without title", threadsReq{Op: "branch", Text: "why?"}, "missing title"},
		{"branch with blank title", threadsReq{Op: "branch", Title: "   "}, "missing title"},
		{"kill without tid", threadsReq{Op: "kill"}, "missing tid"},
		{"revive without tid", threadsReq{Op: "revive"}, "missing tid"},
		{"rename without tid", threadsReq{Op: "rename", Title: "x"}, "missing tid"},
		{"rename without title", threadsReq{Op: "rename", TID: "t1"}, "missing title"},
		{"send without tid", threadsReq{Op: "send", Text: "hi"}, "missing tid"},
		{"send without text", threadsReq{Op: "send", TID: "t1"}, "missing text"},
		{"send with blank text", threadsReq{Op: "send", TID: "t1", Text: "  \n "}, "missing text"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.req.Slug = "th-req"
			rec := postThreads(t, tt.req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
			}
			if got := decodeErr(t, rec); got != tt.want {
				t.Fatalf("error = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestThreadsPostUnknownSlug(t *testing.T) {
	useTestStore(t)
	rec := postThreads(t, threadsReq{Slug: "nope", Op: "kill", TID: "t1"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
}

// TestThreadsPostUnknownTID pins the one error mapping this file owns: an
// unknown tid comes back from store.GetThread as a bare ErrNotFound
// ("store: not found"), which says nothing to a client — it must surface as
// a 404 naming the thread instead.
func TestThreadsPostUnknownTID(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-tid", Repo: "monolith"})

	rec := postThreads(t, threadsReq{Slug: "th-tid", Op: "rename", TID: "t7", Title: "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec); got != "unknown thread" {
		t.Fatalf("error = %q", got)
	}
}

func TestThreadsPostBadJSON(t *testing.T) {
	useTestStore(t)
	req := httptest.NewRequest(http.MethodPost, "/api/threads", strings.NewReader("{not json"))
	rec := httptest.NewRecorder()
	threadsHandler(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400, body %s", rec.Code, rec.Body.String())
	}
}

// An oversize body must be refused with 413, distinct from a plain decode
// failure — same split feedback/questions use, so a client can tell "your
// JSON is wrong" from "your JSON is too big".
func TestThreadsPostOversizeBody(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-big", Repo: "monolith"})

	body := mustJSON(t, threadsReq{Slug: "th-big", Op: "send", TID: "t1", Text: strings.Repeat("x", 1<<21)})
	req := httptest.NewRequest(http.MethodPost, "/api/threads", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	threadsHandler(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413, body %s", rec.Code, rec.Body.String())
	}
}

// TestThreadsNoStoreWired: threads live ONLY in kraken.db, so a process
// running without one (a file-mode board) must say so plainly rather than
// serving an empty list that reads as "this job has no threads".
func TestThreadsNoStoreWired(t *testing.T) {
	seedFileModeJob(t, "th-nostore")

	rec := getThreads(t, "slug=th-nostore")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET status %d, want 500, body %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec); got != "no store wired" {
		t.Fatalf("GET error = %q", got)
	}

	rec = postThreads(t, threadsReq{Slug: "th-nostore", Op: "kill", TID: "t1"})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("POST status %d, want 500, body %s", rec.Code, rec.Body.String())
	}

	rec = getThreadRead(t, "slug=th-nostore&tid=t1")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("read status %d, want 500, body %s", rec.Code, rec.Body.String())
	}
}

// seedFileModeJob writes one job file into a sandboxed pluto dir with NO
// store wired, so webJobFor resolves it (from the file) while
// jobs.ActiveStore() stays nil — the only way to reach the "no store wired"
// branch, which sits behind the job lookup.
func seedFileModeJob(t *testing.T, slug string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", dir)
	body := "---\nrepo: monolith\n---\n\nsome spec text\n"
	if err := os.WriteFile(filepath.Join(dir, slug+jobs.JobSuffix), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	if j := jobs.JobBySlug(jobs.LoadJobs(), slug); j == nil {
		t.Fatalf("file-mode job %s not loadable — test seam broken", slug)
	}
}

// TestThreadsGetOpenInMirrorMode: the POST gate is swept by mirror_test.go;
// this pins the other half — a read must stay reachable on a mirror board,
// exactly like /api/peers/capture.
func TestThreadsGetOpenInMirrorMode(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-mirror", Repo: "monolith"})
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })

	if rec := getThreads(t, "slug=th-mirror"); rec.Code != http.StatusOK {
		t.Fatalf("GET /api/threads status %d under mirror mode, want 200", rec.Code)
	}
	if rec := getThreadRead(t, "slug=th-mirror&tid=t1"); rec.Code == http.StatusForbidden {
		t.Fatal("GET /api/thread/read was gated under mirror mode — a read must stay open")
	}
}

func TestThreadsMethodNotAllowed(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/api/threads", nil)
	rec := httptest.NewRecorder()
	threadsHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/thread/read", nil)
	rec = httptest.NewRecorder()
	threadReadHandler(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("read status %d, want 405", rec.Code)
	}
}

// --- GET /api/thread/read ---

func TestThreadReadMissingParams(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "th-read-args", Repo: "monolith"})

	if rec := getThreadRead(t, "tid=t1"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no slug: status %d, want 400", rec.Code)
	}
	if rec := getThreadRead(t, "slug=th-read-args"); rec.Code != http.StatusBadRequest {
		t.Fatalf("no tid: status %d, want 400", rec.Code)
	}
	if rec := getThreadRead(t, "slug=th-read-args&tid=t1"); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown tid: status %d, want 404", rec.Code)
	}
}

// A thread with no session id (or no transcript file yet) is the freshly
// branched case: 200 with an empty blocks array and source "none", NEVER an
// error — the repo rule for files another program owns.
func TestThreadReadNoTranscriptIsNotAnError(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "th-read-none", Repo: "monolith"})
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", Pane: "%3"}); err != nil {
		t.Fatal(err)
	}

	rec := getThreadRead(t, "slug=th-read-none&tid=t1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Blocks   []map[string]any `json:"blocks"`
		Counted  int              `json:"counted"`
		Live     bool             `json:"live"`
		Pane     string           `json:"pane"`
		ForkUUID string           `json:"fork_uuid"`
		Source   string           `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "none" || len(resp.Blocks) != 0 || resp.Counted != 0 {
		t.Fatalf("resp = %+v, want an empty source-none read", resp)
	}
	if !resp.Live || resp.Pane != "%3" {
		t.Fatalf("live/pane = %v/%q, want a live thread in %%3", resp.Live, resp.Pane)
	}
	// blocks must be [] on the wire, never null
	if !strings.Contains(rec.Body.String(), `"blocks":[]`) {
		t.Fatalf("body = %s, want an empty blocks array", rec.Body.String())
	}
}

// A live ROW with no pane can never be sent to, so it must not read as live
// — the clients gate their composer on this field.
func TestThreadReadLiveRequiresPane(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "th-read-nopane", Repo: "monolith"})
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", State: "live"}); err != nil {
		t.Fatal(err)
	}
	rec := getThreadRead(t, "slug=th-read-nopane&tid=t1")
	var resp struct {
		Live bool `json:"live"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Live {
		t.Fatal("live = true for a row with no pane")
	}
}

// TestThreadReadMarksInheritedHistory is the endpoint's whole point: a
// fork's transcript opens with zeus' history copied in, and everything at or
// before the fork anchor must come back flagged `inherited` so the clients
// can dim it and draw the divider at the first block without the flag.
func TestThreadReadMarksInheritedHistory(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "th-read-fork", Repo: "monolith"})
	sess := "11111111-2222-3333-4444-555555555555"
	if err := st.AddThread(store.Thread{
		JobID: j.ID, TID: "t1", SessionID: sess, ForkUUID: "a1", Pane: "%4", State: "live",
	}); err != nil {
		t.Fatal(err)
	}
	seedTranscript(t, sess,
		`{"type":"user","uuid":"u1","message":{"content":"zeus, do the thing"}}`,
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"doing it"},{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}]}}`,
		`{"type":"user","uuid":"u2","message":{"content":"why did you retry there?"}}`,
		`{"type":"assistant","uuid":"a2","message":{"content":[{"type":"text","text":"because the check flaked"}]}}`,
	)

	rec := getThreadRead(t, "slug=th-read-fork&tid=t1&n=50")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Blocks   []map[string]any `json:"blocks"`
		Counted  int              `json:"counted"`
		ForkUUID string           `json:"fork_uuid"`
		Source   string           `json:"source"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Source != "transcript" || resp.ForkUUID != "a1" {
		t.Fatalf("source/fork_uuid = %q/%q", resp.Source, resp.ForkUUID)
	}
	// u1 + a1's two blocks are inherited (the anchor record's blocks are all
	// on the inherited side); u2 and a2 are the thread's own conversation
	wantInherited := []bool{true, true, true, false, false}
	if len(resp.Blocks) != len(wantInherited) {
		t.Fatalf("got %d blocks, want %d: %+v", len(resp.Blocks), len(wantInherited), resp.Blocks)
	}
	for i, want := range wantInherited {
		_, got := resp.Blocks[i]["inherited"]
		if got != want {
			t.Errorf("block %d (%v) inherited=%v, want %v", i, resp.Blocks[i]["text"], got, want)
		}
	}
	// omitempty in spirit: a non-inherited block carries no key at all, so
	// the clients' `inherited == true` check reads false for it
	if v, ok := resp.Blocks[0]["inherited"]; !ok || v != true {
		t.Fatalf("first block's inherited = %v/%v, want true", v, ok)
	}
}

// An anchor that isn't in the window — a fork point older than the tail read,
// or one that was never recorded — must dim nothing rather than guess.
func TestThreadReadUnknownForkAnchorMarksNothing(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "th-read-noanchor", Repo: "monolith"})
	sess := "99999999-2222-3333-4444-555555555555"
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", SessionID: sess, ForkUUID: "gone"}); err != nil {
		t.Fatal(err)
	}
	seedTranscript(t, sess, `{"type":"user","uuid":"u1","message":{"content":"hello"}}`)

	rec := getThreadRead(t, "slug=th-read-noanchor&tid=t1")
	var resp struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Blocks) != 1 {
		t.Fatalf("blocks = %+v, want 1", resp.Blocks)
	}
	if _, ok := resp.Blocks[0]["inherited"]; ok {
		t.Fatal("marked a block inherited with an anchor that isn't in the transcript")
	}
}

// TestThreadReadSplitsAtTheRealForkAnchor runs the PRODUCER against the
// CONSUMER: the anchor is not hand-written here but taken from
// core.LastMessageUUID over zeus' own transcript, exactly as
// jobs.BranchThread takes it, and the fork's transcript is what claude
// actually writes — zeus' history copied in verbatim, then the thread's own
// turns, then whatever bookkeeping records the harness appends.
//
// That pairing is the whole point. Tested separately, both halves passed
// while the feature was silently dead: LastMessageUUID returned the uuid of
// the last record with a uuid — routinely a `system` record (every cell
// installs a PostToolUse `kraken pr-hook`, which writes one), which
// parseBlocks drops — so the anchor matched no block, the split was 0, and
// both clients rendered zeus' entire inherited history as the thread's own
// conversation with no divider.
func TestThreadReadSplitsAtTheRealForkAnchor(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "th-e2e", Repo: "monolith"})

	// zeus' transcript at the moment carl branches: two turns, then the
	// hook's system record — the shape 336 of 400 real transcripts end in
	zeusHistory := []string{
		`{"type":"user","uuid":"u1","message":{"content":"zeus, ship the retry fix"}}`,
		`{"type":"assistant","uuid":"a1","message":{"content":[{"type":"text","text":"shipping it"},{"type":"tool_use","name":"Read","input":{"file_path":"/x"}}]}}`,
		`{"type":"system","uuid":"sys-1","subtype":"post_tool_use_hook","content":"kraken pr-hook"}`,
	}
	zeusSession := "aaaaaaaa-1111-2222-3333-444444444444"
	zeusPath := seedTranscript(t, zeusSession, zeusHistory...)

	anchor := core.LastMessageUUID(zeusPath)
	if anchor == "" {
		t.Fatal("no fork anchor recorded — a branch would silently lose the divider")
	}

	// the fork: same uuids for the inherited half (claude preserves them),
	// then the thread's own conversation
	threadSession := "bbbbbbbb-1111-2222-3333-444444444444"
	seedTranscript(t, threadSession, append(append([]string{}, zeusHistory...),
		`{"type":"user","uuid":"tu1","message":{"content":"why retry there at all?"}}`,
		`{"type":"assistant","uuid":"ta1","message":{"content":[{"type":"text","text":"because the check flakes"}]}}`,
	)...)
	if err := st.AddThread(store.Thread{
		JobID: j.ID, TID: "t1", SessionID: threadSession, ForkUUID: anchor, Pane: "%7", State: "live",
	}); err != nil {
		t.Fatal(err)
	}

	rec := getThreadRead(t, "slug=th-e2e&tid=t1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	// u1 + a1's text + a1's tool = inherited; tu1 + ta1 = the thread's own
	wantInherited := []bool{true, true, true, false, false}
	if len(resp.Blocks) != len(wantInherited) {
		t.Fatalf("got %d blocks, want %d: %+v", len(resp.Blocks), len(wantInherited), resp.Blocks)
	}
	inheritedCount := 0
	for i, want := range wantInherited {
		_, got := resp.Blocks[i]["inherited"]
		if got != want {
			t.Errorf("block %d (%v) inherited=%v, want %v", i, resp.Blocks[i]["text"], got, want)
		}
		if got {
			inheritedCount++
		}
	}
	if inheritedCount == 0 {
		t.Fatal("nothing marked inherited — the divider is dead again")
	}
}

// --- threadErr's status mapping ---

// TestThreadErrStatus pins the split that matters to a client: a 409 says
// "your request conflicted with the current state, do something different",
// while a broken database says "the server failed at a valid request, retry
// it as-is" — a 500. Mapping a locked db or a full disk onto 409, as this
// route used to, tells the client the exact opposite of the truth.
func TestThreadErrStatus(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			"tid collision is recoverable",
			fmt.Errorf("add thread t3 for job 7: %w", store.ErrThreadIDTaken),
			http.StatusConflict,
		},
		{"unknown tid, bare", store.ErrNotFound, http.StatusNotFound},
		{
			"unknown tid, wrapped by the store",
			fmt.Errorf("set title of thread t3 for job 7: %w", store.ErrNotFound),
			http.StatusNotFound,
		},
		{
			"a locked database is the server's fault",
			fmt.Errorf("add thread t3 for job 7: %w", errors.New("database is locked")),
			http.StatusInternalServerError,
		},
		{
			"a failed query is the server's fault",
			fmt.Errorf("list threads for job 7: %w", errors.New("disk I/O error")),
			http.StatusInternalServerError,
		},
		{
			"no live cell is a live-state refusal",
			errors.New("foo has no live cell to branch — start or reactivate it first"),
			http.StatusConflict,
		},
		{
			"a moved pane is a live-state refusal",
			errors.New("pane %9 no longer holds thread t1 of foo — revive it first"),
			http.StatusConflict,
		},
		{
			"an already-live thread is a live-state refusal",
			errors.New("thread t1 of foo is already live in pane %9 — kill it before reviving"),
			http.StatusConflict,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := threadErrStatus(tt.err); got != tt.want {
				t.Fatalf("threadErrStatus(%v) = %d, want %d", tt.err, got, tt.want)
			}
			rec := httptest.NewRecorder()
			threadErr(rec, "thread-branch", "foo", tt.err)
			if rec.Code != tt.want {
				t.Fatalf("threadErr wrote %d, want %d", rec.Code, tt.want)
			}
			// jobs' own message is the recovery text — it must survive
			// verbatim on every code but 404, where ErrNotFound's bare
			// "store: not found" would say nothing to a client
			got := decodeErr(t, rec)
			if tt.want == http.StatusNotFound {
				if got != "unknown thread" {
					t.Fatalf("404 error = %q, want %q", got, "unknown thread")
				}
			} else if got != tt.err.Error() {
				t.Fatalf("error = %q, want %q verbatim", got, tt.err.Error())
			}
		})
	}
}

// seedTranscript writes a transcript for sessionID under the sandboxed HOME
// useTestStore already set, where core.TranscriptPathForSession globs for it,
// and returns its path.
func seedTranscript(t *testing.T, sessionID string, lines ...string) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "projects", "proj")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	body := strings.Join(lines, "\n") + "\n"
	path := filepath.Join(dir, sessionID+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// --- the fork split rule, in isolation ---

func TestThreadForkSplit(t *testing.T) {
	tests := []struct {
		name  string
		uuids []string
		fork  string
		want  int
	}{
		{"anchor mid-window", []string{"u1", "a1", "u2"}, "a1", 2},
		{"anchor repeated across one record's blocks", []string{"u1", "a1", "a1", "a1", "u2"}, "a1", 4},
		{"anchor is the last block — everything inherited", []string{"u1", "a1"}, "a1", 2},
		{"anchor absent", []string{"u1", "a1"}, "zzz", 0},
		{"no anchor recorded", []string{"u1", "a1"}, "", 0},
		{"empty window", nil, "a1", 0},
		{"blank uuids never match a blank anchor", []string{"", ""}, "", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := threadForkSplit(tt.uuids, tt.fork); got != tt.want {
				t.Errorf("threadForkSplit(%v, %q) = %d, want %d", tt.uuids, tt.fork, got, tt.want)
			}
		})
	}
}
