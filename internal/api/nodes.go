// Protocol parity: this file is part of the MULTIPLAYER protocol (CLAUDE.md
// § Protocol parity). The protocol is kraken-core's and identical in every
// fork; a change here lands in kraken-core first, carries "protocol:" in its
// PR title, and is named to the operator as a protocol change before it is
// pushed.

package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// nodes.go — the node API: what a sibling kraken (or any tailnet device) may
// read and do on this node, and the proxies through which this node's own
// owner or paired clients read a sibling's readonly and public cells and
// drive its public ones.
//
// Node-tier routes serve this node's own cards only, filtered by visibility on
// every request: a private job is ABSENT (every read answers the same 404 an
// unknown slug does, so nobody can tell it exists). A readonly job and a
// public one are READ alike — the card as this board builds it (minus the
// owner and account binding), and, each per-job route gated by visibleJob,
// its job text, its artifacts, its zeus transcript and its side threads. Only
// WRITES tell them apart: POST /api/node/action drives a public job and
// refuses a readonly one 403 (publicJob) — except its three ask-thread
// actions (nodeThreadActions), admitted on both — and the owner-only writes
// (feedback, doc-question answers, every other thread op, goals, rename,
// visibility, uploads, the mental model) have no node route at all, on any
// visibility.
// Every gate reads the job's visibility from the store at request time —
// never from a cached card — so a flip to private refuses every remote read
// or action that arrives after the flip. GET /api/nodes (node tier too)
// serves the siblings' card lists this node has stored, never this node's
// own private jobs.

// getOnly refuses anything but GET with 405.
func getOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return false
	}
	return true
}

// fleetOff is the reply every fleet route gives when KRAKEN_NODES=off.
func fleetOff(w http.ResponseWriter) {
	webJSON(w, http.StatusNotFound, map[string]string{"error": "fleet off"})
}

// nodeSelfHandler is GET /api/node: this node's envelope, the probe target.
func nodeSelfHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	if !jobs.FleetEnabled() {
		fleetOff(w)
		return
	}
	webJSON(w, http.StatusOK, nodeSelf())
}

// servedToNodes reports whether a card may leave this machine at all.
func servedToNodes(wj webJob) bool {
	if wj.Archived || wj.Owner != nil {
		return false
	}
	return wj.Visibility == jobs.VisibilityReadOnly || wj.Visibility == jobs.VisibilityPublic
}

// nodeCard is a card as a sibling receives it: the object this board renders
// with exactly two keys cleared — the owner stamp (a card served from here is
// this node's own, and the receiver stamps the owner from its registry row)
// and the account binding, which names this machine's pool login and is read
// against this machine's usage snapshot, so it means nothing on another one.
// Everything else (feedback, tokens, threads, doc questions, artifacts, PRs,
// the load estimate, goals, the pending question, crew, detail, the lead's
// context fill …) is served as built. The artifact URLs left on it address THIS node's /api/artifact/;
// a receiving node rewrites every one to its own proxy (strippedCards) and
// never serves a sibling's URL onward.
func nodeCard(wj webJob) webJob {
	wj.Owner = nil
	wj.Account = nil
	return wj
}

// localNodeCards is this node's own servable cards, as the board builds them.
func localNodeCards() []webJob {
	out := []webJob{}
	for _, wj := range webBoardCache.get().Jobs {
		if servedToNodes(wj) {
			out = append(out, wj)
		}
	}
	return out
}

// nodeJobsHandler is GET /api/node/jobs: the envelope plus every readonly and
// public card this node holds.
func nodeJobsHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	reply := struct {
		Node jobs.NodeEnvelope `json:"node"`
		Jobs []webJob          `json:"jobs"`
	}{Node: nodeSelf(), Jobs: []webJob{}}
	if jobs.FleetEnabled() {
		for _, wj := range localNodeCards() {
			reply.Jobs = append(reply.Jobs, nodeCard(wj))
		}
	}
	webJSON(w, http.StatusOK, reply)
}

// noSuchJob is the one refusal a private, archived or unknown slug gets.
func noSuchJob(w http.ResponseWriter) {
	webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
}

// nodeJobHandler is GET /api/node/job?slug=: one card, its artifact names
// and its pending question.
func nodeJobHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	// Existence and visibility come from the job row, never from the board
	// cache: a readonly or public job answers 200 whatever its state and
	// whether or not its cell is live.
	j := visibleJob(w, r.URL.Query().Get("slug"))
	if j == nil {
		return
	}
	wj, ok := cachedCard(j.Slug)
	if !ok {
		wj = rowCard(j)
	}
	names := []string{}
	for _, a := range wj.Artifacts {
		names = append(names, a.Name)
	}
	reply := map[string]any{"job": nodeCard(wj), "artifacts": names}
	if wj.Question != nil {
		reply["question"] = wj.Question
	}
	webJSON(w, http.StatusOK, reply)
}

// cachedCard is the board's own local card for slug, when the cached board
// has one.
func cachedCard(slug string) (webJob, bool) {
	for _, wj := range webBoardCache.get().Jobs {
		if wj.Slug == slug && !wj.Archived && wj.Owner == nil {
			return wj, true
		}
	}
	return webJob{}, false
}

// rowCard is a card built from the job row alone — the fallback when the
// cached board does not carry the job, so the answer never depends on what a
// cache happened to build.
func rowCard(j *jobs.Job) webJob {
	return webJob{
		Slug: j.Slug, Repo: j.Repo, State: jobs.DisplayState(j), Err: j.Err, Outcome: j.Outcome,
		Color: jobs.EffectiveColor(j), Agent: j.Agent, ModTime: j.ModTime.Unix(), PRs: j.PRs,
		Load: webJobLoadFrom(j.Load), Visibility: j.EffectiveVisibility(),
		Artifacts: jobArtifacts(j, false),
	}
}

// visibleJob is every node-tier READ's gate, and the ask-thread actions'
// gate on POST /api/node/action: slug resolved among the ACTIVE
// jobs (LoadJobs holds no archived one), and anything a sibling may not see —
// fleet off, an empty or unknown slug, a private job — answers the one
// noSuchJob 404, so the four cannot be told apart. A readonly or public job
// is returned with nothing written. It reads the store, not the board cache.
func visibleJob(w http.ResponseWriter, slug string) *jobs.Job {
	if !jobs.FleetEnabled() || slug == "" {
		noSuchJob(w)
		return nil
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil || j.EffectiveVisibility() == jobs.VisibilityPrivate {
		noSuchJob(w)
		return nil
	}
	return j
}

// publicJob is the WRITE gate, nodeActionHandler's alone: visibleJob's 404s,
// then readonly → 403. A readonly job is read like a public one and driven
// like no one's — bar the ask-thread actions (nodeThreadActions), which
// nodeActionHandler gates with visibleJob instead.
func publicJob(w http.ResponseWriter, slug string) *jobs.Job {
	j := visibleJob(w, slug)
	if j == nil {
		return nil
	}
	if j.EffectiveVisibility() != jobs.VisibilityPublic {
		webJSON(w, http.StatusForbidden, map[string]string{"error": "not public"})
		return nil
	}
	return j
}

// nodeJobRawHandler is GET /api/node/job/raw?slug=: the job text of a
// readonly or public job — exactly GET /api/job/raw's body. Its `editable` is
// the owner's local rule; whether a remote client offers a save is that
// client's business, and the save itself goes through publicJob.
func nodeJobRawHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	if visibleJob(w, r.URL.Query().Get("slug")) == nil {
		return
	}
	webJobRaw(w, r)
}

// readCount parses n for the read routes: default 5, clamped to 1..50.
func readCount(r *http.Request) int {
	n := 5
	if v := r.URL.Query().Get("n"); v != "" {
		if iv, err := strconv.Atoi(v); err == nil {
			n = iv
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 50 {
		n = 50
	}
	return n
}

// nodeReadHandler is GET /api/node/read?slug=&n=: the last n blocks of a
// readonly or public cell's zeus transcript — the remote `k r` — in
// /api/peers/read's shape.
func nodeReadHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	j := visibleJob(w, r.URL.Query().Get("slug"))
	if j == nil {
		return
	}
	n := readCount(r)
	if _, live := cellSessionIfLive(j); !live {
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has no live zeus"})
		return
	}
	all, _ := core.OrderAll(core.DiscoverAll())
	zeus, transcript := cellZeusTranscript(j, all)
	if zeus == nil || transcript == "" {
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has no live zeus"})
		return
	}
	blocks, counted, err := core.LastBlocks(transcript, n)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]map[string]string, 0, len(blocks))
	for _, b := range blocks {
		m := map[string]string{"kind": b.Kind, "text": b.Text}
		if b.From != "" {
			m["from"] = b.From
		}
		out = append(out, m)
	}
	webJSON(w, http.StatusOK, map[string]any{"blocks": out, "counted": counted})
}

// serveLocalGet is serveLocal's GET sibling: it runs a local read's handler
// on a synthesized GET path?q, so a node read answers with exactly the bytes
// the owner's own board gets from that route.
func serveLocalGet(w http.ResponseWriter, r *http.Request, path string, h http.HandlerFunc, q url.Values) {
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, path+"?"+q.Encode(), nil)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	req.RemoteAddr = r.RemoteAddr
	h(w, req)
}

// copyQuery copies each named parameter from r's query into q when non-empty.
func copyQuery(q url.Values, r *http.Request, keys ...string) {
	for _, k := range keys {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
}

// noSuchArtifact is the one refusal every artifact miss gets.
func noSuchArtifact(w http.ResponseWriter) {
	webJSON(w, http.StatusNotFound, map[string]string{"error": "no such artifact"})
}

// nodeArtifactHandler is GET /api/node/artifact?slug=&name=: one artifact of
// a readonly or public job, as bytes. name must be one the owner's own card
// lists — the list is the allowlist, so nothing a sibling names reaches the
// disk or the table unless this board would have offered it — and it is read
// from the one source that list came from (the live cell dir, else the
// artifacts table). A live-cell name is served only when every segment
// Lstat's as a real directory and the last as a regular file
// (regularNoLinks) and the open itself refuses a link (readRegularNoFollow),
// so no symlink in artifacts/ is followed. The bytes are
// written directly rather than through http.ServeFile, so a name that is a
// directory on disk can never list.
func nodeArtifactHandler(w http.ResponseWriter, r *http.Request) {
	inertArtifactHeaders(w, artifactMimeType(r.URL.Query().Get("name")))
	if !getOnly(w, r) {
		return
	}
	q := r.URL.Query()
	j := visibleJob(w, q.Get("slug"))
	if j == nil {
		return
	}
	name := q.Get("name")
	if strings.TrimSpace(name) == "" {
		noSuchArtifact(w)
		return
	}
	var list []webArtifact
	live := false
	if wj, ok := cachedCard(j.Slug); ok {
		list, live = wj.Artifacts, wj.Live
	} else {
		list = rowCard(j).Artifacts
	}
	listed := false
	for _, a := range list {
		if a.Name == name {
			listed = true
			break
		}
	}
	if !listed {
		noSuchArtifact(w)
		return
	}
	var data []byte
	if live {
		p, ok := ArtifactPath(j, "cell", name)
		if !ok || !regularNoLinks(jobs.CellArtifacts(j), name) {
			noSuchArtifact(w)
			return
		}
		b, tooBig, ok := readRegularNoFollow(p)
		if tooBig {
			artifactTooBig(w)
			return
		}
		if !ok {
			noSuchArtifact(w)
			return
		}
		data = b
	} else {
		b, _, ok := ArtifactBytes(j, "pluto", name)
		if !ok {
			noSuchArtifact(w)
			return
		}
		if len(b) > nodeReplyMax {
			artifactTooBig(w)
			return
		}
		data = b
	}
	w.Header().Set("Content-Type", artifactMimeType(name))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// inertArtifactHeaders are the headers every artifact reply served to or for
// another node carries, refusals included, given the mime type of the NAME
// the request asked for. nosniff and no-store always. A plain `sandbox` CSP —
// no allow-scripts, no allow-same-origin — only when that type is one a
// browser runs script in, text/html or image/svg+xml: such an artifact is
// rendered in a frame on the viewer's board origin, and a hostile node's page
// must not act there. Any other type (a PDF, an image) gets no CSP, since it
// cannot script, and a browser's built-in PDF viewer can refuse to load
// inside a sandboxed document. The rule keys on the requested name alone,
// so a refusal (a JSON 404 or 502) carries the CSP exactly when a success
// would have.
//
// Why no script at all rather than an opaque origin with scripts: a script
// in an opaque origin can still send CORS simple requests to this node, which
// on the owner's machine arrive from loopback and are classified owner. The
// gate refuses every non-JSON POST (postContentTypeOK) and every POST with a
// foreign or `null` Origin (postOriginOK); the no-script rule is the layer
// that depends on neither, nor on every route staying behind them. The same
// `sandbox` directive guards feedback.go's snapshot route.
func inertArtifactHeaders(w http.ResponseWriter, mimeType string) {
	h := w.Header()
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "no-store")
	if scriptableMime(mimeType) {
		h.Set("Content-Security-Policy", "sandbox")
	}
}

// scriptableMime reports whether a browser would run script in a document of
// this type: html or svg, whatever parameters follow.
func scriptableMime(mimeType string) bool {
	base, _, _ := strings.Cut(mimeType, ";")
	base = strings.ToLower(strings.TrimSpace(base))
	return base == "text/html" || base == "image/svg+xml"
}

// readRegularNoFollow reads p only if p itself is not a symlink (O_NOFOLLOW
// makes the open fail on one) and the opened handle is a regular file. It
// closes the gap between regularNoLinks' walk and the read: a file swapped
// for a link after the walk is refused at the open, and the Fstat is of the
// very handle the bytes come from.
//
// O_NONBLOCK is harmless on a regular file and keeps the open itself from
// blocking should a FIFO be swapped in during that race. A file larger than
// nodeReplyMax is refused (tooBig) rather than read: a sibling's relay would
// refuse it anyway, and the read goes through a LimitReader, so a file that
// grows after the Fstat is still never read past the cap.
func readRegularNoFollow(p string) (data []byte, tooBig, ok bool) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, false, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return nil, false, false
	}
	if fi.Size() > nodeReplyMax {
		return nil, true, false
	}
	b, err := io.ReadAll(io.LimitReader(f, nodeReplyMax+1))
	if err != nil {
		return nil, false, false
	}
	if len(b) > nodeReplyMax {
		return nil, true, false
	}
	return b, false, true
}

// artifactTooBig is the refusal for an artifact over nodeReplyMax.
func artifactTooBig(w http.ResponseWriter) {
	webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "artifact over 16 MB"})
}

// regularNoLinks reports whether name, under root, is a regular file reached
// without crossing a symlink: every segment is Lstat'd, each intermediate
// must be a real directory and the last a regular file. A link anywhere in
// artifacts/ would otherwise let a sibling read its target — ArtifactPath
// stats through links — on a job that is readonly by default.
func regularNoLinks(root, name string) bool {
	p := root
	segs := strings.Split(strings.TrimPrefix(name, "/"), "/")
	for i, seg := range segs {
		p = filepath.Join(p, seg)
		fi, err := os.Lstat(p)
		if err != nil {
			return false
		}
		if i < len(segs)-1 {
			if !fi.IsDir() {
				return false
			}
		} else if !fi.Mode().IsRegular() {
			return false
		}
	}
	return true
}

// nodeTranscriptHandler is GET /api/node/transcript?slug=&n=&turns=: a
// readonly or public job's zeus transcript, answered by the steer modal's own
// /api/job/transcript handler.
func nodeTranscriptHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	j := visibleJob(w, r.URL.Query().Get("slug"))
	if j == nil {
		return
	}
	q := url.Values{"slug": {j.Slug}}
	copyQuery(q, r, "n", "turns")
	serveLocalGet(w, r, "/api/job/transcript", jobTranscriptHandler, q)
}

// nodeThreadReadHandler is GET /api/node/thread/read?slug=&tid=&n=: one side
// thread of a readonly or public job, answered by /api/thread/read's own
// handler. The thread lookup is keyed on the gated job, so a tid belonging to
// any other job answers that handler's "unknown thread".
func nodeThreadReadHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	j := visibleJob(w, r.URL.Query().Get("slug"))
	if j == nil {
		return
	}
	q := url.Values{"slug": {j.Slug}}
	copyQuery(q, r, "tid", "n")
	serveLocalGet(w, r, "/api/thread/read", threadReadHandler, q)
}

// nodeAnnounceHandler is POST /api/node/announce: a sibling saying "I am a
// kraken". Only a caller announcing its OWN tailnet id is recorded, always
// under the address the request came from.
func nodeAnnounceHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	if !jobs.FleetEnabled() {
		fleetOff(w)
		return
	}
	var env jobs.NodeEnvelope
	// lenient on unknown keys: a newer sibling's envelope may carry more
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&env); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if strings.TrimSpace(env.ID) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "node_id required"})
		return
	}
	self := nodeSelf()
	if env.ID == self.ID {
		webJSON(w, http.StatusOK, self) // our own echo: nothing to record
		return
	}
	// The row is bound to who the tailnet says is calling, never to what the
	// body claims: a device may only announce ITSELF. An unknown identity
	// (whois unavailable, or an owner/paired caller with no tailnet id)
	// refuses too — fail closed.
	if id := identityOf(r).ID; id == "" || id != env.ID {
		webJSON(w, http.StatusForbidden, map[string]string{"error": "node id does not match caller"})
		return
	}
	if env.Port < 1 || env.Port > 65535 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "port must be 1..65535"})
		return
	}
	// the row's address is always the caller's own, never one the body names
	ip := callerIP(r)
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no store wired"})
		return
	}
	_, lookupErr := st.GetNode(env.ID)
	if err := st.UpsertNode(jobs.RowFromEnvelope(env, ip, time.Now().UnixMilli())); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not record node"})
		return
	}
	if errors.Is(lookupErr, store.ErrNotFound) {
		util.LogEvent("nodes", "info", "node-new", "kraken node "+core.TruncateOneLine(env.Label, 80)+" announced itself from "+ip)
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, self)
}

// nodeActionReq is POST /api/node/action's body.
type nodeActionReq struct {
	Slug    string          `json:"slug"`
	Action  string          `json:"action"`
	Payload json.RawMessage `json:"payload"`
	From    struct {
		Cell string `json:"cell"`
	} `json:"from"`
}

// nodeActions are the moves a public cell admits from another node (Q16,
// Q21). Everything else — retry, reactivate, rename, load, goals, feedback,
// pane threads, uploads, the mental model — stays with the owner.
var nodeActions = map[string]bool{
	"steer": true, "question": true, "kill": true, "done": true, "submit": true,
	"archive": true, "color": true, "save": true, "stop": true,
}

// nodeThreadActions are the ask-thread moves another node may make on a
// readonly OR public cell: open a headless ask thread, ask in one, close one
// it opened. A readonly card already serves zeus' whole transcript to every
// node (nodeTranscriptHandler), so an ask thread — no tools for a remote
// origin, jobs/askthread.go — reveals nothing new; what it adds is compute
// on the cell's account, which jobs bounds with MaxAskThreads and
// AskThreadTimeout.
var nodeThreadActions = map[string]bool{"thread-ask": true, "thread-send": true, "thread-kill": true}

// nodeActionPayload is the union of what each action reads from payload.
type nodeActionPayload struct {
	Text    string     `json:"text"`
	Content string     `json:"content"`
	Color   string     `json:"color"`
	MTime   mtimeToken `json:"mtime"`
	Title   string     `json:"title"`
	TID     string     `json:"tid"`
	Model   string     `json:"model"`
}

// senderLabel names a remote caller for the steer prefix: the label of the
// registry row bound to the caller's verified tailnet id, else its tailnet
// device name, else the bare address.
func senderLabel(r *http.Request) string {
	id := identityOf(r)
	// matched by the caller's verified tailnet id, never by address: an IP is
	// whatever the registry last recorded, and a row is only trustworthy as a
	// name for the identity it was bound to
	if st := jobs.ActiveStore(); st != nil && id.ID != "" {
		if n, err := st.GetNode(id.ID); err == nil && n.Label != "" {
			return n.Label
		}
	}
	if l := id.Label(); l != "" {
		return l
	}
	return callerIP(r)
}

// serveLocal runs a local route's handler on a synthesized JSON body, so a
// remote action is answered by exactly the code (gates, validation, reply)
// the owner's own board reaches.
func serveLocal(w http.ResponseWriter, r *http.Request, path string, h http.HandlerFunc, body any) {
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, path, bytes.NewReader(b))
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = r.RemoteAddr
	h(w, req)
}

// nodeActionHandler is POST /api/node/action: another node driving one of
// this node's PUBLIC cells, or asking in a readonly or public one
// (nodeThreadActions). Each action dispatches to the handler the local
// route runs; the only thing added is the sender prefix on a steer, built from
// who the tailnet says is calling rather than anything the body claims.
func nodeActionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	var req nodeActionReq
	if !decodeStrictBody(w, r, &req, "this route takes slug, action, payload and from") {
		return
	}
	// the thread actions pass on a readonly job as well (visibleJob's 404s
	// only); every other action keeps publicJob's 404 → 403, then the
	// unknown-action 400
	thread := nodeThreadActions[req.Action]
	var j *jobs.Job
	if thread {
		j = visibleJob(w, req.Slug)
	} else {
		j = publicJob(w, req.Slug)
	}
	if j == nil {
		return
	}
	if !thread && !nodeActions[req.Action] {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unknown action " + core.TruncateOneLine(req.Action, 24)})
		return
	}
	var p nodeActionPayload
	if len(bytes.TrimSpace(req.Payload)) > 0 && string(bytes.TrimSpace(req.Payload)) != "null" {
		if err := json.Unmarshal(req.Payload, &p); err != nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad payload"})
			return
		}
	}
	if thread {
		nodeThreadAction(w, r, j, req.Action, p)
		return
	}
	from := senderLabel(r)
	util.LogEvent("nodes", "info", "node-action", req.Action+" "+j.Slug+" from "+core.TruncateOneLine(from, 80))
	switch req.Action {
	case "steer":
		text := ""
		if strings.TrimSpace(p.Text) != "" {
			text = jobs.RemotePrefix(from, identityOf(r).Login, req.From.Cell) + " " + p.Text
		}
		serveLocal(w, r, "/api/steer", webAction("steer", webSteer), webReq{Slug: j.Slug, Text: text})
	case "question":
		serveLocal(w, r, "/api/question", webAction("question", webAnswerQuestion), webReq{Slug: j.Slug, Text: p.Text})
	case "kill":
		serveLocal(w, r, "/api/kill", webAction("kill", webKill), webReq{Slug: j.Slug})
	case "done":
		serveLocal(w, r, "/api/done", webAction("done", webDone), webReq{Slug: j.Slug})
	case "submit":
		serveLocal(w, r, "/api/queue", webAction("submit", webSubmit), webReq{Slug: j.Slug})
	case "archive":
		serveLocal(w, r, "/api/archive", webAction("archive", webArchive), webReq{Slug: j.Slug})
	case "stop":
		serveLocal(w, r, "/api/stop", webAction("stop", webStop), webReq{Slug: j.Slug})
	case "color":
		serveLocal(w, r, "/api/job/color", webJobColor, webJobColorReq{Slug: j.Slug, Color: p.Color})
	case "save":
		serveLocal(w, r, "/api/job/save", webJobSave, map[string]any{"slug": j.Slug, "content": p.Content, "mtime": int64(p.MTime)})
	}
}

// nodeThreadAction serves the three nodeThreadActions. The origin is the
// caller's verified tailnet identity, never anything in the body, so a
// thread's origin, its `[from …]` stamps and the kill check below all name
// the node that actually called.
func nodeThreadAction(w http.ResponseWriter, r *http.Request, j *jobs.Job, action string, p nodeActionPayload) {
	id := identityOf(r)
	from := senderLabel(r)
	origin := jobs.ThreadOrigin{Node: id.ID, Label: from, Login: id.Login}
	resp := map[string]any{"ok": true}
	bad := func(msg string) { webJSON(w, http.StatusBadRequest, map[string]string{"error": msg}) }
	tid := strings.TrimSpace(p.TID)
	switch action {
	case "thread-ask":
		title := strings.TrimSpace(p.Title)
		if title == "" {
			bad("missing title")
			return
		}
		if strings.TrimSpace(p.Text) == "" {
			bad("missing text")
			return
		}
		if p.Model != "" {
			if _, ok := core.ThreadModels[p.Model]; !ok {
				bad("bad model — want haiku, sonnet, opus or fable")
				return
			}
		}
		t, err := jobs.AskThread(j, origin, title, p.Text, p.Model)
		if err != nil {
			threadErr(w, "node-"+action, j.Slug, err)
			return
		}
		resp["tid"] = t.TID
	case "thread-send":
		if tid == "" {
			bad("missing tid")
			return
		}
		if strings.TrimSpace(p.Text) == "" {
			bad("missing text")
			return
		}
		if err := jobs.AskThreadSend(j, tid, p.Text, origin); err != nil {
			threadErr(w, "node-"+action, j.Slug, err)
			return
		}
	case "thread-kill":
		if tid == "" {
			bad("missing tid")
			return
		}
		st := jobs.ActiveStore()
		if st == nil {
			webJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no store"})
			return
		}
		t, err := st.GetThread(j.ID, tid)
		if err != nil {
			threadErr(w, "node-"+action, j.Slug, err)
			return
		}
		if t.Kind != "headless" {
			webJSON(w, http.StatusConflict, map[string]string{"error": "thread " + tid + " of " + j.Slug + " is a pane thread — only its owner can close it"})
			return
		}
		if t.OriginNode != id.ID {
			webJSON(w, http.StatusForbidden, map[string]string{"error": "not your thread"})
			return
		}
		if err := jobs.KillThread(j, tid); err != nil {
			threadErr(w, "node-"+action, j.Slug, err)
			return
		}
	}
	util.LogEvent("nodes", "info", "node-action", action+" "+j.Slug+" from "+core.TruncateOneLine(from, 80))
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, resp)
}

// nodesHandler is GET /api/nodes: the registry, each node with its stored
// card list.
func nodesHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	out := []webNode{}
	// KRAKEN_NODES=off opts out in both directions: a stale registry is
	// never served
	if st := jobs.ActiveStore(); st != nil && jobs.FleetEnabled() {
		rows, err := st.ListNodes()
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "could not list nodes"})
			return
		}
		for _, n := range rows {
			out = append(out, webNodeFrom(n, true))
		}
	}
	webJSON(w, http.StatusOK, map[string]any{"nodes": out})
}

// findNode resolves a proxy's node argument: a registry id.
func findNode(id string) *store.NodeRow {
	st := jobs.ActiveStore()
	if st == nil || id == "" {
		return nil
	}
	n, err := st.GetNode(id)
	if err != nil {
		return nil
	}
	return n
}

// Proxy timeouts: a steer or recolour is one tmux send on the far side; a
// submit starts a cell synchronously (worktree, tmux session, account bind);
// a thread-ask runs a discovery pass and starts a claude process before the
// far side answers.
const (
	nodesProxyTimeout       = 5 * time.Second
	nodesProxySubmitTimeout = 60 * time.Second
	nodesProxyAskTimeout    = 30 * time.Second
)

// nodesProxyDo sends one proxied request; a seam for tests.
var nodesProxyDo = func(req *http.Request, timeout time.Duration) (*http.Response, error) {
	return (&http.Client{Timeout: timeout}).Do(req)
}

// relay writes a sibling's reply back: its status and its JSON body, always
// as application/json with nosniff — the sibling's own Content-Type is never
// trusted, since the body is served on THIS node's origin. A body that is not
// JSON, and a sibling 401 (which a client would otherwise read as its own
// gate refusing it), become 502s naming the sibling; a transport error is a
// 502 carrying the error.
func relay(w http.ResponseWriter, n *store.NodeRow, req *http.Request, timeout time.Duration) {
	w.Header().Set("X-Content-Type-Options", "nosniff") // on every reply, 502s included
	resp, err := nodesProxyDo(req, timeout)
	if err != nil {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	label := nodeLabel(n)
	if resp.StatusCode == http.StatusUnauthorized {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": label + " did not recognise this node"})
		return
	}
	if !json.Valid(body) {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": label + " answered non-JSON"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// nodeLabel names a registry node in an error: its label, else its id.
func nodeLabel(n *store.NodeRow) string {
	if n.Label != "" {
		return n.Label
	}
	return n.ID
}

func nodeBase(n *store.NodeRow) string {
	return "http://" + net.JoinHostPort(n.IP, strconv.Itoa(n.Port))
}

// nodesActionHandler is POST /api/nodes/action: forward one action to a
// sibling's /api/node/action, naming the sending cell when the CLI inside a
// cell set X-Kraken-From-Cell.
func nodesActionHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	var req struct {
		Node    string          `json:"node"`
		Slug    string          `json:"slug"`
		Action  string          `json:"action"`
		Payload json.RawMessage `json:"payload"`
	}
	if !decodeStrictBody(w, r, &req, "this route takes node, slug, action and payload") {
		return
	}
	n := findNode(req.Node)
	if n == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such node"})
		return
	}
	fwd := nodeActionReq{Slug: req.Slug, Action: req.Action, Payload: req.Payload}
	fwd.From.Cell = strings.TrimSpace(r.Header.Get("X-Kraken-From-Cell"))
	if len(fwd.Payload) == 0 {
		fwd.Payload = json.RawMessage("{}")
	}
	b, _ := json.Marshal(fwd)
	out, err := http.NewRequestWithContext(r.Context(), http.MethodPost, nodeBase(n)+"/api/node/action", bytes.NewReader(b))
	if err != nil {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	out.Header.Set("Content-Type", "application/json")
	timeout := nodesProxyTimeout
	switch req.Action {
	case "submit":
		timeout = nodesProxySubmitTimeout
	case "thread-ask":
		timeout = nodesProxyAskTimeout
	}
	relay(w, n, out, timeout)
}

// nodesGetProxy forwards a GET to path on the named sibling with the given
// query parameters.
func nodesGetProxy(w http.ResponseWriter, r *http.Request, path string, params ...string) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	n := findNode(r.URL.Query().Get("node"))
	if n == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such node"})
		return
	}
	q := url.Values{}
	copyQuery(q, r, params...)
	out, err := http.NewRequestWithContext(r.Context(), http.MethodGet, nodeBase(n)+path+"?"+q.Encode(), nil)
	if err != nil {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	relay(w, n, out, nodesProxyTimeout)
}

// nodesJobRawHandler is GET /api/nodes/job/raw?node=&slug=.
func nodesJobRawHandler(w http.ResponseWriter, r *http.Request) {
	nodesGetProxy(w, r, "/api/node/job/raw", "slug")
}

// nodesReadHandler is GET /api/nodes/read?node=&slug=&n=.
func nodesReadHandler(w http.ResponseWriter, r *http.Request) {
	nodesGetProxy(w, r, "/api/node/read", "slug", "n")
}

// nodesTranscriptHandler is GET /api/nodes/transcript?node=&slug=&n=&turns=.
func nodesTranscriptHandler(w http.ResponseWriter, r *http.Request) {
	nodesGetProxy(w, r, "/api/node/transcript", "slug", "n", "turns")
}

// nodesThreadReadHandler is GET /api/nodes/thread/read?node=&slug=&tid=&n=.
func nodesThreadReadHandler(w http.ResponseWriter, r *http.Request) {
	nodesGetProxy(w, r, "/api/node/thread/read", "slug", "tid", "n")
}

// nodeReplyMax bounds a sibling's reply; it mirrors internal/jobs/nodes.go's
// unexported constant of the same name and value.
const nodeReplyMax = 16 << 20

// nodesProxyArtifactTimeout is the artifact relay's: an artifact may be a
// rendered page or an image of several megabytes, against a one-line action.
const nodesProxyArtifactTimeout = 15 * time.Second

// nodesArtifactHandler is GET /api/nodes/artifact?node=&slug=&name=: a
// sibling's artifact, relayed as bytes from its /api/node/artifact. The URLs
// strippedCards writes onto every remote card point here.
func nodesArtifactHandler(w http.ResponseWriter, r *http.Request) {
	// its own 405/404 too, not only relayBytes' replies
	inertArtifactHeaders(w, artifactMimeType(r.URL.Query().Get("name")))
	if !getOnly(w, r) {
		return
	}
	n := findNode(r.URL.Query().Get("node"))
	if n == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such node"})
		return
	}
	name := r.URL.Query().Get("name")
	q := url.Values{"slug": {r.URL.Query().Get("slug")}, "name": {name}}
	out, err := http.NewRequestWithContext(r.Context(), http.MethodGet, nodeBase(n)+"/api/node/artifact?"+q.Encode(), nil)
	if err != nil {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	relayBytes(w, n, out, nodesProxyArtifactTimeout, artifactMimeType(name))
}

// relayBytes is relay for a reply that is not JSON: a sibling's 200 is served
// verbatim under mimeType — the CALLER's type, computed from the name it
// asked for, never the sibling's Content-Type header — and every other reply
// follows relay's rules (a 401 or a non-JSON refusal becomes a 502 naming the
// sibling). inertArtifactHeaders for mimeType ride every reply, 502s
// included: the bytes are another node's, served on this node's origin, and
// the sandbox CSP an html or svg name gets is what keeps a sibling's page
// from running as this origin.
func relayBytes(w http.ResponseWriter, n *store.NodeRow, req *http.Request, timeout time.Duration, mimeType string) {
	inertArtifactHeaders(w, mimeType)
	resp, err := nodesProxyDo(req, timeout)
	if err != nil {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, nodeReplyMax+1))
	if err != nil {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	label := nodeLabel(n)
	if len(body) > nodeReplyMax {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": label + " answered more than 16 MB"})
		return
	}
	if resp.StatusCode == http.StatusUnauthorized {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": label + " did not recognise this node"})
		return
	}
	if resp.StatusCode == http.StatusOK {
		w.Header().Set("Content-Type", mimeType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
		return
	}
	if !json.Valid(body) {
		webJSON(w, http.StatusBadGateway, map[string]string{"error": label + " answered non-JSON"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}
