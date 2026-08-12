package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"kraken/internal/core"
	"kraken/internal/jobs"
)

// webJobColorReq is POST /api/job/color's body. Slug names the job; Color is
// one of jobs.Palette's eight names, or "" to clear.
type webJobColorReq struct {
	Slug  string `json:"slug"`
	Color string `json:"color"`
}

// webJobColor sets one job's colour — its own endpoint rather than a field
// of the job form, for two reasons that both come down to colour being
// purely cosmetic.
//
// First, editability. The form save is gated on editableJob (no lifecycle in
// flight: drafts and done jobs), because rewriting repo/base/spec under a
// running cell desyncs it from the contract it is working to. Colour cannot
// desync anything — and a LIVE cell is precisely when recolouring is useful,
// since the whole point is that zeus' prompt bar matches the board. So this
// handler deliberately does NOT call editableJob; going through the form
// would have made the feature unreachable exactly where it matters.
//
// Second, blast radius. /api/job/saveform is a FULL REPLACE of every form
// field, so recolouring through it means a client resubmitting the entire
// spec to change one word — and any client that predates the field clears
// it. A one-field endpoint has neither problem, which is why the form no
// longer writes colour at all (it still renders it: see serializeJobFile).
//
// It is NOT routed through webSteer either, despite also ending in a message
// typed at zeus: that handler force-writes state = "working" afterwards
// (actions.go), so recolouring a blocked job through it would silently
// un-block the cell.
func webJobColor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	// Body cap like every other POST in this package (peers.go, feedback.go,
	// questions.go, update.go): the whole legitimate request is two short
	// strings, so an oversize one is refused with 413 rather than read into
	// memory. 413 stays distinct from an ordinary decode error's 400.
	var req webJobColorReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	// The single allowlist gate for the whole feature: the value ends up as
	// the argument of a /color TYPED INTO A LIVE CLAUDE, so anything off the
	// palette is refused here rather than discovered in a pane. "" is the
	// legitimate clear.
	if req.Color != "" && !jobs.ValidColor(req.Color) {
		// Same message shape as ParseJob's arm for the same rule
		// (jobs/job.go): one rule, one bounded echo — OneLine alone strips
		// control characters but would happily quote a megabyte back.
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown color " + core.TruncateOneLine(req.Color, 24),
		})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if err := jobs.WriteJobFields(j, "color", req.Color); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	// Deliver into the live pane now instead of up to a tick later — the
	// swatch click and the prompt bar should change together. Fire-and-
	// forget and self-gating (syncCellColor no-ops on a dead or
	// non-working/blocked cell), and mirror-gated for the reason every
	// pane-typing route is: a mirror board must never reach the real
	// deployment's panes.
	if !mirrorModeOn() {
		jobs.PushJobColor(j.Slug)
	}
	webJSON(w, http.StatusOK, map[string]any{"ok": true})
}
