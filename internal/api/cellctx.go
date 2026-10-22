package api

import (
	"net/http"
	"strings"

	"kraken/internal/jobs"
)

// cellContextReq is POST /api/cell/context's body — a cell claude's
// statusline hook (`kraken ctx-hook --slug <slug>`) reporting how full its
// context window is. UsedPercentage is a pointer for orchContextReq's reason:
// an absent number must be refused rather than recorded as 0%.
//
// TotalInputTokens is the statusline's exact input-only count, the number
// UsedPercentage was computed from. Optional: a hook binary that predates the
// field sends none and the row records 0, which the board's reader treats as
// "not reported" rather than as a count (jobs.CellContext). It is a plain int
// rather than a pointer because 0 is already that spelling — a billed session
// never reports a zero count.
type cellContextReq struct {
	Slug             string   `json:"slug"`
	SessionID        string   `json:"session_id"`
	UsedPercentage   *float64 `json:"used_percentage"`
	ContextWindow    int64    `json:"context_window_size"`
	TotalInputTokens int64    `json:"total_input_tokens"`
}

// cellContextHandler serves POST /api/cell/context. It persists the report
// under the job's id and the reporting session — only zeus' stamped session
// once there is one — and the daemon's auto-handoff pass reads it. A write,
// so it is registered mirror-gated.
func cellContextHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req cellContextReq
	if !decodeStrictBody(w, r, &req,
		"this route takes only slug, session_id, used_percentage, context_window_size and total_input_tokens") {
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	session := strings.TrimSpace(req.SessionID)
	if session == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing session_id — the context report is keyed to a session",
		})
		return
	}
	if req.UsedPercentage == nil {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing used_percentage — send the statusline's own number in 0..100",
		})
		return
	}
	pct := *req.UsedPercentage
	if pct < 0 || pct > 100 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "used_percentage must be within 0..100"})
		return
	}
	if req.TotalInputTokens < 0 {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "total_input_tokens must not be negative"})
		return
	}
	j := jobs.LoadJobBySlug(slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no job " + slug})
		return
	}
	// Once zeus' session is stamped, the auto-handoff pass reads only that
	// session's key, so a teammate's or side thread's report is acknowledged
	// without being stored.
	if j.Session != "" && j.Session != session {
		webJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	st, ok := activeStore(w)
	if !ok {
		return
	}
	if err := jobs.RecordCellContext(st, j.ID, session, pct, req.ContextWindow, req.TotalInputTokens); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
