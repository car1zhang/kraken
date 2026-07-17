package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
)

// k web's sessions API — the same discovery/read/send paths k ls, k r and
// k s use, exposed over HTTP so the iOS app (reachable via --tailnet) can
// list, read and message live sessions. peerSource is swapped out in tests
// so handlers don't have to shell out to tmux/ps to be exercised.
type peerSource func() []*Peer

func defaultPeerSource() []*Peer {
	all, _ := orderAll(discoverAll())
	return all
}

// findPeerByPane resolves the peer in an exact pane. An empty pane never
// matches, even though in-process teammates also carry Pane "" — a caller
// that forgot the query param must get "not found", not an arbitrary
// unaddressable teammate.
func findPeerByPane(peers []*Peer, pane string) *Peer {
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

func healthHandler(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "host": host})
}

func peersHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		peers := source()
		if peers == nil {
			peers = []*Peer{}
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
		path, err := readTranscriptPath(p)
		if err != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		blocks, counted, err := lastBlocks(path, n)
		if err != nil {
			// unlike the pre-flight refusals above, this is an I/O failure
			// (transcript vanished mid-request), not a peer-state conflict
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]map[string]string, 0, len(blocks))
		for _, b := range blocks {
			out = append(out, map[string]string{"kind": b.kind, "text": b.text})
		}
		webJSON(w, http.StatusOK, map[string]any{"blocks": out, "counted": counted})
	}
}

// peersSendHandler mirrors k s for agents and k s's shell behavior for shells:
// an agent target goes through sendToPeer's full guard chain, a shell target
// through sendToShell (types the line and runs it). The app composer posts here
// for both; the peer's role decides which path.
func peersSendHandler(source peerSource) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
			return
		}
		var req struct {
			Pane    string `json:"pane"`
			Message string `json:"message"`
		}
		// --tailnet widens the audience beyond loopback; cap the body so a
		// misbehaving client can't buffer arbitrary bytes through us
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
			return
		}
		p := findPeerByPane(source(), req.Pane)
		if p == nil {
			webJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("no session in pane %s", req.Pane)})
			return
		}
		if p.Role == "shell" {
			if err := sendToShell(p, req.Message); err != nil {
				webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			webJSON(w, http.StatusOK, map[string]any{"ok": true, "queued": false, "shell": true})
			return
		}
		queued, err := sendToPeer(p, req.Message)
		if err != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
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
		text, err := capturePane(p.Pane)
		if err != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		webJSON(w, http.StatusOK, map[string]any{"pane": p.Pane, "content": text})
	}
}
