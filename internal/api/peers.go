package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// mobileSession is the dedicated tmux session the HTTP spawn endpoint targets
// — spawned panes never land in the caller's own session, since there is no
// caller session for an HTTP request.
const mobileSession = "mobile"

// k web's sessions API — the same discovery/read/send paths k ls, k r and
// k s use, exposed over HTTP so the iOS app (reachable via --tailnet) can
// list, read and message live sessions. peerSource is swapped out in tests
// so handlers don't have to shell out to tmux/ps to be exercised.
type peerSource func() []*core.Peer

func defaultPeerSource() []*core.Peer {
	all, _ := core.OrderAll(core.DiscoverAll())
	return all
}

// findPeerByPane resolves the peer in an exact pane. An empty pane never
// matches, even though in-process teammates also carry Pane "" — a caller
// that forgot the query param must get "not found", not an arbitrary
// unaddressable teammate.
func findPeerByPane(peers []*core.Peer, pane string) *core.Peer {
	if pane == "" {
		return nil
	}
	for _, p := range peers {
		if p.Pane == pane {
			return p
		}
	}
	return nil
}

// requireJSONPost enforces POST + a real application/json Content-Type on
// every state-changing peers endpoint. application/json is not a CORS
// "simple request" content type, so requiring it forces a browser to send a
// preflight OPTIONS first — and since this server never answers preflights
// with CORS headers, that preflight fails, which blocks a script on any
// other website from drive-by POSTing here. Every real client (iOS
// APIClient, web/app.js) already sets this header, so nothing legitimate is
// affected.
func requireJSONPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		webJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "content-type must be application/json"})
		return false
	}
	return true
}

// isCellPane reports whether a tmux session belongs to a job cell — those
// panes belong to the job board's archive/kill/reactivate lifecycle, not the
// freeform peers kill action.
func isCellPane(session string) bool {
	return strings.HasPrefix(session, "cell-")
}

// paneSessionFunc resolves a pane's live tmux session; swapped out in tests
// so peersKillHandler's fresh re-verify doesn't have to shell out to tmux.
var paneSessionFunc = core.CurrentPaneSession

// openPeerFunc raises a peer's tmux/Ghostty window; swapped out in tests so
// peersOpenHandler's success path doesn't have to shell out to tmux/osascript.
var openPeerFunc = core.OpenPeer

func healthHandler(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "host": host, "contract": apiContract, "stale": util.IsSelfStale()})
}

func peersHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peers := source()
		if peers == nil {
			peers = []*core.Peer{}
		}
		webJSON(w, http.StatusOK, map[string]any{"peers": peers})
	}
}

// peersReadHandler mirrors k r: same pre-flight refusals (readTranscriptPath,
// shared with cmdRead) and the same tail-block reader (lastBlocks).
func peersReadHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pane := r.URL.Query().Get("pane")
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
		p := findPeerByPane(source(), pane)
		if p == nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no session in pane %s", pane)})
			return
		}
		path, err := core.ReadTranscriptPath(p)
		if err != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		blocks, counted, err := core.LastBlocks(path, n)
		if err != nil {
			// unlike the pre-flight refusals above, this is an I/O failure
			// (transcript vanished mid-request), not a peer-state conflict
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
}

// peersSendHandler mirrors k s for agents and k s's shell behavior for shells:
// an agent target goes through sendToPeer's full guard chain, a shell target
// through sendToShell (types the line and runs it). The app composer posts here
// for both; the peer's role decides which path. A request may instead set
// "key" (mutually exclusive with "message") to send a single named key —
// Esc, Ctrl-<letter>, arrows, Tab, Enter — via sendKey, for a keyboard that
// can't type them (e.g. iOS).
func peersSendHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// TODO: same drive-by CORS-simple-request weakness as the spawn/kill/
		// open handlers (see requireJSONPost) — not hardened here yet because
		// this handler's callers' Content-Type usage hasn't been verified.
		if r.Method != http.MethodPost {
			webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var req struct {
			Pane    string `json:"pane"`
			Message string `json:"message"`
			Key     string `json:"key"`
		}
		// --tailnet widens the audience beyond loopback; cap the body so a
		// misbehaving client can't buffer arbitrary bytes through us
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		if req.Message != "" && req.Key != "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "send either message or key, not both"})
			return
		}
		p := findPeerByPane(source(), req.Pane)
		if p == nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no session in pane %s", req.Pane)})
			return
		}
		if req.Key != "" {
			if err := core.SendKey(p, req.Key); err != nil {
				webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			webBoardCache.invalidate()
			webJSON(w, http.StatusOK, map[string]any{"ok": true, "key": true})
			return
		}
		if p.Role == "shell" {
			if err := core.SendToShell(p, req.Message); err != nil {
				webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			webBoardCache.invalidate()
			webJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": false, "shell": true})
			return
		}
		queued, err := core.SendToPeer(p, req.Message)
		if err != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		webBoardCache.invalidate()
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": queued})
	}
}

// peersCaptureHandler returns a pane's visible terminal content — the live
// view the app shows for shell panes (no transcript) and for agent panes when
// the user wants raw terminal I/O. Read-only.
func peersCaptureHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		pane := r.URL.Query().Get("pane")
		p := findPeerByPane(source(), pane)
		if p == nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no session in pane %s", pane)})
			return
		}
		text, err := core.CapturePane(p.Pane)
		if err != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		webJSON(w, http.StatusOK, map[string]any{"pane": p.Pane, "content": text})
	}
}

// peersSpawnHandler opens a new shell or claude pane in the dedicated
// "mobile" tmux session — the app's "+" action has no caller pane/session of
// its own to target, unlike k n/k c run from inside tmux.
func peersSpawnHandler(w http.ResponseWriter, r *http.Request) {
	if !requireJSONPost(w, r) {
		return
	}
	var req struct {
		Kind   string `json:"kind"`
		Dir    string `json:"dir"`
		Name   string `json:"name"`
		Prompt string `json:"prompt"`
		Cmd    string `json:"cmd"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	dir := req.Dir
	switch {
	case dir == "":
		dir = util.Home()
	case dir == "~":
		dir = util.Home()
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(util.Home(), strings.TrimPrefix(dir, "~/"))
	default:
		// the mobile client has no way to know the server's cwd, so a bare
		// relative path can only be a client mistake, not something to resolve
		if !strings.HasPrefix(dir, "~") && !filepath.IsAbs(dir) {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "dir must be absolute or ~-relative"})
			return
		}
	}
	argv, err := core.BuildSpawnArgv(req.Kind, req.Name, req.Prompt, req.Cmd)
	if err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := core.CheckSpawnDir(dir); err != nil {
		// a bad dir is a malformed client parameter — fail before ensureSession
		// does any tmux work for a request that's doomed anyway
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := core.EnsureSession(mobileSession, util.Home()); err != nil {
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	at, err := core.NewWindowIn(mobileSession, dir, argv)
	if err != nil {
		// still guard the (unreachable in practice, but defensive) case where
		// newWindowIn's own dir check disagrees with checkSpawnDir above
		status := http.StatusConflict
		if errors.Is(err, core.ErrBadSpawnDir) {
			status = http.StatusBadRequest
		}
		webJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "at": at})
}

// peersKillHandler mirrors k k, plus a guard k k doesn't need: a job cell's
// pane belongs to its board lifecycle (archive/kill/reactivate), not the
// app's freeform kill action, so it's refused here rather than torn down out
// from under the daemon.
func peersKillHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireJSONPost(w, r) {
			return
		}
		var req struct {
			Pane string `json:"pane"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		p := findPeerByPane(source(), req.Pane)
		if p == nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no session in pane %s", req.Pane)})
			return
		}
		// re-read the pane's session fresh rather than trust the stale
		// snapshot's TmuxSession — a pane can move sessions (or vanish)
		// between discovery and this request
		session, err := paneSessionFunc(p.Pane)
		if err != nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("pane %s is gone", p.Pane)})
			return
		}
		if isCellPane(session) {
			webJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("%s belongs to job cell %s — use the job's Kill action", core.DisplayName(p), session)})
			return
		}
		if _, err := core.KillPeer(p); err != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		webBoardCache.invalidate()
		webJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

// peersOpenHandler mirrors k o for either an arbitrary pane or a job's zeus
// (resolved by slug) — the app has no tmux target of its own to jump from, so
// it needs to name the destination by slug when opening a job's cell.
func peersOpenHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !requireJSONPost(w, r) {
			return
		}
		var req struct {
			Pane string `json:"pane"`
			Slug string `json:"slug"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		if (req.Pane == "") == (req.Slug == "") {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "exactly one of pane or slug is required"})
			return
		}
		if req.Pane != "" {
			p := findPeerByPane(source(), req.Pane)
			if p == nil {
				webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no session in pane %s", req.Pane)})
				return
			}
			note, outcome, err := openPeerFunc(p)
			respondOpened(w, req.Pane, note, outcome, err)
			return
		}
		j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
		if j == nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no job %q", req.Slug)})
			return
		}
		zeus := jobs.CellZeus(j, source())
		if zeus == nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": "no live cell agent"})
			return
		}
		note, outcome, err := openPeerFunc(zeus)
		respondOpened(w, req.Slug, note, outcome, err)
	}
}

// respondOpened writes peersOpenHandler's response and logs the outcome —
// all three altitudes (hard error, clean success, raised-but-unfocused) live
// here so the two call sites above stay a one-liner each, mirroring
// webAction's shape in actions.go. A hard error from openPeerFunc fails the
// request outright; otherwise clean success and a raised-but-unfocused
// window both count as a successful open (ok: true), with a set FocusErr
// surfaced to the web board as an additive "focus_error" key so its toast can
// explain why the ⧉ tmux button didn't actually bring a window forward.
func respondOpened(w http.ResponseWriter, target, note string, outcome core.RaiseOutcome, err error) {
	if err != nil {
		util.LogEvent("web", "error", "open", fmt.Sprintf("open %s: %v", target, err))
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	resp := map[string]any{"ok": true, "note": note}
	if outcome.FocusErr != nil {
		resp["focus_error"] = outcome.FocusErr.Error()
		util.LogEvent("web", "error", "open", fmt.Sprintf("open %s: %v", target, outcome.FocusErr))
	} else {
		util.LogEvent("web", "info", "open", fmt.Sprintf("open %s: %s", target, note))
	}
	webJSON(w, http.StatusOK, resp)
}
