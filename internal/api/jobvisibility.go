package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"kraken/internal/core"
	"kraken/internal/jobs"
)

// webJobVisibilityReq is POST /api/job/visibility's body.
type webJobVisibilityReq struct {
	Slug       string `json:"slug"`
	Visibility string `json:"visibility"`
}

// webJobVisibility sets who on the tailnet may see or drive a job. It is
// webJobColor's shape for webJobColor's two reasons: a live cell is exactly
// when someone decides to open it up (or close it), and the form save's
// editableJob gate would refuse it there; and a full-replace form save from a
// client that predates the field would reset it. Like colour, not
// mirror-gated: it writes a row and types nothing; the next poll by a
// sibling reads it.
func webJobVisibility(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req webJobVisibilityReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if !jobs.ValidVisibility(req.Visibility) {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "unknown visibility " + core.TruncateOneLine(req.Visibility, 24),
		})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if err := jobs.WriteJobFields(j, "visibility", req.Visibility); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true})
}
