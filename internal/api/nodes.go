package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// nodes.go — the node API: what a sibling kraken (or any tailnet device) may
// read and do on this node, and the proxies this node's own owner or paired
// clients reach a sibling's public cells through.
//
// Node-tier routes serve this node's own cards only, filtered by visibility on
// every request: a private job is ABSENT (its detail answers the same 404 an
// unknown slug does, so nobody can tell it exists), a readonly one is
// visible and untouchable, a public one may be driven. The drive routes read
// the job's visibility from the store at request time — never from a cached
// card — so a flip to private refuses every remote action that arrives after
// the flip. GET /api/nodes (node tier too) serves the siblings' card lists
// this node has stored, never this node's own private jobs.

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

// nodeCard is a card as a sibling receives it: the object this board renders,
// minus what stays home whatever a job's visibility — carl's feedback notes,
// token spend, the account binding, side threads and doc questions — and minus
// artifact URLs, which address THIS node's /api/artifact/ and would resolve
// against the viewer's own node (a same-slug job there) if served onward.
// Artifact names come with GET /api/node/job instead.
func nodeCard(wj webJob) webJob {
	wj.Owner = nil
	wj.Feedback = nil
	wj.Tokens = nil
	wj.Account = nil
	wj.Threads = nil
	wj.DocQuestions = nil
	wj.Artifacts = nil
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
	slug := r.URL.Query().Get("slug")
	if !jobs.FleetEnabled() || slug == "" {
		noSuchJob(w)
		return
	}
	// Existence and visibility come from the job row (LoadJobs holds active
	// jobs only, so an archived slug is absent here), never from the board
	// cache: a readonly or public job answers 200 whatever its state and
	// whether or not its cell is live.
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil || j.EffectiveVisibility() == jobs.VisibilityPrivate {
		noSuchJob(w)
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

// publicJob resolves slug to an active job and refuses anything not public:
// unknown or private → 404 (indistinguishable), readonly → 403. It reads the
// store, not the board cache.
func publicJob(w http.ResponseWriter, slug string) *jobs.Job {
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if !jobs.FleetEnabled() || j == nil || j.EffectiveVisibility() == jobs.VisibilityPrivate {
		noSuchJob(w)
		return nil
	}
	if j.EffectiveVisibility() != jobs.VisibilityPublic {
		webJSON(w, http.StatusForbidden, map[string]string{"error": "not public"})
		return nil
	}
	return j
}

// nodeJobRawHandler is GET /api/node/job/raw?slug=: the job text of a PUBLIC
// job, the remote editor's load — exactly GET /api/job/raw's body.
func nodeJobRawHandler(w http.ResponseWriter, r *http.Request) {
	if publicJob(w, r.URL.Query().Get("slug")) == nil {
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
// PUBLIC cell's zeus transcript — the remote `k r` — in /api/peers/read's
// shape.
func nodeReadHandler(w http.ResponseWriter, r *http.Request) {
	if !getOnly(w, r) {
		return
	}
	j := publicJob(w, r.URL.Query().Get("slug"))
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
// threads, uploads, the mental model — stays with the owner.
var nodeActions = map[string]bool{
	"steer": true, "question": true, "kill": true, "done": true, "submit": true,
	"archive": true, "color": true, "save": true, "stop": true,
}

// nodeActionPayload is the union of what each action reads from payload.
type nodeActionPayload struct {
	Text    string     `json:"text"`
	Content string     `json:"content"`
	Color   string     `json:"color"`
	MTime   mtimeToken `json:"mtime"`
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
// this node's PUBLIC cells. Each action dispatches to the handler the local
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
	j := publicJob(w, req.Slug)
	if j == nil {
		return
	}
	if !nodeActions[req.Action] {
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
// submit starts a cell synchronously (worktree, tmux session, account bind).
const (
	nodesProxyTimeout       = 5 * time.Second
	nodesProxySubmitTimeout = 60 * time.Second
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
	if req.Action == "submit" {
		timeout = nodesProxySubmitTimeout
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
	for _, k := range params {
		if v := r.URL.Query().Get(k); v != "" {
			q.Set(k, v)
		}
	}
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
