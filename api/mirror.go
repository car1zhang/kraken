package api

import (
	"net/http"
	"sync/atomic"
)

// mirror.go — the API-layer half of kraken serve --mirror's safety net (see
// cmd_serve.go and jobs/daemon.go's ServeOpts.Mirror for the scheduler
// half). A mirror process runs a read-only scheduler against a sandbox DB
// for feature-parity testing, but the web API it serves is the SAME code
// the live deployment's board uses — without a gate here, hitting a mirror
// board's kill/steer/start/etc. buttons would reach into the REAL tmux
// panes and worktrees the live deployment owns (StartCell, teardownCell,
// core.SendMessage, ... all shell out against real process/filesystem
// state, independent of which DB is wired). SetMirrorMode is that gate.

var mirrorMode atomic.Bool

// SetMirrorMode toggles whether the board's tmux/process-driving mutation
// endpoints (see mirrorGatedActions in actions.go) refuse with 403 instead
// of running. Called once by cmd_serve.go at startup with the --mirror
// flag's value; tests call it directly to exercise both branches, and
// should always restore it afterward (e.g. via t.Cleanup) since it's
// process-global state.
func SetMirrorMode(on bool) {
	mirrorMode.Store(on)
}

// mirrorModeOn reports the current mirror-mode gate state.
func mirrorModeOn() bool {
	return mirrorMode.Load()
}

// mirrorGated wraps a handler registered outside the webAction path (the
// peers surface) with the same 403 refusal the gated actions use. The
// peers send/spawn/kill/open handlers drive real tmux panes and ghostty
// windows regardless of which DB is wired, so they must be gated exactly
// like kill/steer; peers read/capture stay open — capture-pane is a read.
func mirrorGated(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}
