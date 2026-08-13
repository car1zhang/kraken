package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// threads.go — the HTTP surface for side threads: GET/POST /api/threads (the
// list plus all five ops) and GET /api/thread/read (one thread's
// conversation). Everything real happens in jobs/thread.go — this file only
// decodes, resolves the job, dispatches, and maps errors onto status codes;
// it never re-words a refusal, since jobs' own messages already name the
// recovery ("… — reactivate the job first", "… — revive it first").

// threadReadDefaultN / threadReadMaxN bound GET /api/thread/read's block
// count. The default is generous compared to /api/peers/read's 5 because a
// thread IS its conversation — the overlay renders the whole thing, not a
// tail peek — but both are deliberately kept near what core.LastBlocks' FIRST
// tail window serves: a thread's transcript is a fork, so it opens with zeus'
// whole history copied in (tens of MB on a long job), and every block asked
// for beyond that first window costs another doubling — read and re-parsed —
// on a poll that repeats every few seconds. Past core.LastBlocks' own scan
// ceiling the answer is simply shorter, never an error, so a high n buys
// latency rather than blocks.
const (
	threadReadDefaultN = 50
	threadReadMaxN     = 150
)

// threadsReq is every POST /api/threads form: op names the verb, and which
// of tid/title/text is required follows from it (see threadsPost's dispatch).
type threadsReq struct {
	Slug  string `json:"slug"`
	Op    string `json:"op"`
	TID   string `json:"tid,omitempty"`
	Title string `json:"title,omitempty"`
	Text  string `json:"text,omitempty"`
	// Model is read by `branch` alone — the tier carl picked for this thread,
	// one of core.ThreadModels' four aliases, empty for no pick. The other
	// four ops ignore it, for two different reasons: kill, rename and send
	// put nothing on a command line at all, while revive DOES render one and
	// deliberately re-reads the alias off the ROW instead of the request, so
	// no request can move a thread to another tier mid-conversation.
	Model string `json:"model,omitempty"`
}

// threadsHandler serves GET (the board/client read) and POST (branch, kill,
// revive, rename, send) /api/threads. POST is mirror-gated — every op but
// rename drives a real tmux window, and rename rides along under the same
// "a mirror board is read-only, full stop" policy feedback/questions use.
// The gate uses the plain-text http.Error(...) shape every gated route
// shares (mirror_test.go asserts the exact body), not a JSON body. GET stays
// open, like /api/peers/capture: a read touches nothing.
func threadsHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		threadsGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		threadsPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// threadsGet serves one job's threads in the same shape the board's per-job
// `threads` array carries — one uniform read path for every client. Always a
// JSON array, never null: a job with no threads yet gets "[]".
func threadsGet(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	j, _, err := webJobFor(slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	ts, err := st.ListThreads(j.ID)
	if err != nil {
		util.LogEvent("web", "error", "threads", "threads "+slug+": "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := webThreadsFrom(ts)
	if out == nil {
		out = []webThread{}
	}
	webJSON(w, http.StatusOK, map[string]any{"threads": out})
}

// threadsPost dispatches one op onto jobs/thread.go. Only the per-op
// required-field checks live here; every guard that needs the world (a live
// cell, a forkable zeus, a pane still held by this thread's claude) belongs
// to jobs and surfaces through threadErr below. An oversize body is refused
// with 413, distinct from an ordinary JSON-decode error (400).
func threadsPost(w http.ResponseWriter, r *http.Request) {
	var req threadsReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if jobs.ActiveStore() == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	op := strings.TrimSpace(req.Op)
	tid := strings.TrimSpace(req.TID)
	title := strings.TrimSpace(req.Title)
	action := "thread-" + op
	// tid is required by four of the five ops, so it's checked once here
	// rather than repeated in each branch below.
	if op == "kill" || op == "revive" || op == "rename" || op == "send" {
		if tid == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing tid"})
			return
		}
	}
	resp := map[string]any{"ok": true}
	switch op {
	case "branch":
		if title == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing title"})
			return
		}
		// after the title check, so a form missing both still gets the
		// message about the field that is actually required. An unknown
		// alias is refused HERE rather than left to jobs: this is the one
		// value that arrives from a picker, so a bad one means a client bug
		// or a hand-rolled request, and 400 says that where jobs' own
		// live-state refusals are 409s. Not trimmed, unlike title/tid — the
		// four aliases are an exact menu, not free text carl types.
		if req.Model != "" {
			if _, ok := core.ThreadModels[req.Model]; !ok {
				webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad model — want haiku, sonnet, opus or fable"})
				return
			}
		}
		// text is the seed prompt and is deliberately OPTIONAL: branching
		// with nothing to say yet is a legitimate move (the overlay's own
		// seed box may be left blank), and the thread comes up idle.
		t, err := jobs.BranchThread(j, title, req.Text, req.Model)
		if err != nil {
			threadErr(w, action, req.Slug, err)
			return
		}
		resp["tid"] = t.TID
	case "kill":
		if err := jobs.KillThread(j, tid); err != nil {
			threadErr(w, action, req.Slug, err)
			return
		}
	case "revive":
		if _, err := jobs.ReviveThread(j, tid); err != nil {
			threadErr(w, action, req.Slug, err)
			return
		}
	case "rename":
		if title == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing title"})
			return
		}
		if err := jobs.RenameThread(j, tid, title); err != nil {
			threadErr(w, action, req.Slug, err)
			return
		}
	case "send":
		// unlike branch's seed, a send with no text is meaningless — it would
		// type a bare Enter into the thread's pane
		if strings.TrimSpace(req.Text) == "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing text"})
			return
		}
		if err := jobs.SendToThread(j, tid, req.Text); err != nil {
			threadErr(w, action, req.Slug, err)
			return
		}
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": `unrecognized threads request — op must be one of branch (title), kill (tid), revive (tid), rename (tid, title), send (tid, text)`})
		return
	}
	util.LogEvent("web", "info", action, action+" "+req.Slug)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, resp)
}

// threadErr maps a jobs/thread.go failure onto the wire, VERBATIM: those
// messages already name the recovery, so this only picks the status code.
// An unknown tid (store.ErrNotFound, raised by GetThread before any tmux
// work) is a 404 with the feedback route's own wording — ErrNotFound's bare
// "store: not found" text says nothing to a client.
func threadErr(w http.ResponseWriter, action, slug string, err error) {
	util.LogEvent("web", "error", action, action+" "+slug+": "+err.Error())
	status := threadErrStatus(err)
	if status == http.StatusNotFound {
		webJSON(w, status, map[string]string{"error": "unknown thread"})
		return
	}
	webJSON(w, status, map[string]string{"error": err.Error()})
}

// threadErrStatus picks the status code for a thread op's failure. The split
// that matters is 409 vs 500: a 409 tells the client "your request conflicted
// with the current state — do something different", while a broken database
// (locked, disk full) is the SERVER failing at a request that was perfectly
// valid and should simply be retried as-is. Mapping the latter to 409, as this
// route used to, tells the client the exact opposite of the truth.
//
//   - store.ErrThreadIDTaken — a recoverable tid collision (renumber and
//     retry): 409, and the reason store exports it as a sentinel rather than
//     leaving the API to substring-match the message.
//   - store.ErrNotFound — an unknown tid: 404.
//   - a store query/exec failure: 500. store is the only layer here that
//     wraps (%w) the error it got, precisely so its sentinels stay
//     errors.Is-comparable through the jobs functions that pass them up
//     untouched — so an unwrappable chain that matched no sentinel above came
//     from the database, not from a guard.
//   - everything else: 409, the live-state refusals jobs/thread.go raises as
//     FLAT fmt.Errorf messages (no live cell, no forkable zeus, a pane that
//     moved on, a thread already live) — the same 409 every webAction-wrapped
//     board move uses.
func threadErrStatus(err error) int {
	switch {
	case errors.Is(err, store.ErrThreadIDTaken):
		return http.StatusConflict
	case errors.Is(err, store.ErrNotFound):
		return http.StatusNotFound
	case errors.Unwrap(err) != nil:
		return http.StatusInternalServerError
	default:
		return http.StatusConflict
	}
}

// threadReadHandler serves one thread's conversation: the last n blocks of
// its forked transcript, each flagged `inherited` while it is still zeus'
// pre-fork history, plus the liveness/pane the clients need to decide
// whether a composer and a pane mirror make sense.
//
// A thread with no session id yet, no transcript file, or an unreadable one
// is NOT an error — it answers 200 with an empty blocks array and
// source "none". That is the repo rule for files another program owns
// (claude writes these): a freshly branched thread that hasn't taken a turn
// has nothing to read, and the clients render "no messages yet" for it.
// Stays open in mirror mode — it is a read.
func threadReadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	slug := r.URL.Query().Get("slug")
	tid := strings.TrimSpace(r.URL.Query().Get("tid"))
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	if tid == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing tid"})
		return
	}
	n := threadReadDefaultN
	if v := r.URL.Query().Get("n"); v != "" {
		if iv, err := strconv.Atoi(v); err == nil {
			n = iv
		}
	}
	if n < 1 {
		n = 1
	}
	if n > threadReadMaxN {
		n = threadReadMaxN
	}
	j, _, err := webJobFor(slug)
	if err != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	t, err := st.GetThread(j.ID, tid)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown thread"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// live is the ROW's own answer (the daemon's per-tick ReconcileThreads
	// closes rows whose pane moved on), and it requires a pane: a live row
	// with no pane could never be sent to, so offering a composer for it
	// would be a lie.
	resp := map[string]any{
		"blocks":    []any{},
		"counted":   0,
		"live":      t.State == "live" && t.Pane != "",
		"pane":      t.Pane,
		"fork_uuid": t.ForkUUID,
		"source":    "none",
	}
	// Located by the thread's OWN session id, never zeus': a fork writes its
	// own transcript file, which begins with the whole inherited history
	// copied in. Every failure below degrades to source "none" with the
	// empty envelope above — this reads a file claude owns.
	path := ""
	if t.SessionID != "" {
		path = core.TranscriptPathForSession(t.SessionID)
	}
	if path == "" {
		webJSON(w, http.StatusOK, resp)
		return
	}
	blocks, counted, rerr := core.LastBlocks(path, n)
	if rerr != nil {
		webJSON(w, http.StatusOK, resp)
		return
	}
	// the fork anchor is a uuid in the INHERITED half, so every block up to
	// and including its record is zeus' history; the first block after it is
	// where the thread's own conversation starts. Not found (the anchor is
	// older than this tail window, or was never recorded) means nothing is
	// dimmed and no divider is drawn — the graceful path both clients handle.
	uuids := make([]string, len(blocks))
	for i, b := range blocks {
		uuids[i] = b.UUID
	}
	split := threadForkSplit(uuids, t.ForkUUID)
	out := make([]map[string]any, 0, len(blocks))
	for i, b := range blocks {
		m := map[string]any{"kind": b.Kind, "text": b.Text}
		if b.From != "" {
			m["from"] = b.From
		}
		if i < split {
			m["inherited"] = true
		}
		out = append(out, m)
	}
	resp["blocks"] = out
	resp["counted"] = counted
	resp["source"] = "transcript"
	webJSON(w, http.StatusOK, resp)
}

// threadForkSplit returns how many of blocks' records are inherited pre-fork
// history: one past the LAST block carrying forkUUID, since a single record
// can yield several blocks (thinking + text + tool) and all of them belong
// to the inherited side. 0 when the anchor is empty or absent from the
// window — the caller then marks nothing inherited. PURE, so the whole
// splitting rule is testable without a transcript.
func threadForkSplit(uuids []string, forkUUID string) int {
	if forkUUID == "" {
		return 0
	}
	split := 0
	for i, u := range uuids {
		if u == forkUUID {
			split = i + 1
		}
	}
	return split
}
