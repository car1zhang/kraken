package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// jobmsg.go — POST /api/job/message: a cell's `kraken s` reporting that it
// just messaged another cell's zeus. The sender is the X-Kraken-From-Cell
// header (the cell's own KRAKEN_JOB_SLUG, the header spawn links use) and the
// receiver is the target pane's agent name, resolved to its job here through
// jobs.JobForZeusName. The pair's count lands in store.job_messages and the
// board renders it as webJob.talks.
//
// The report is best-effort on the sending side (cmd_actions.go ignores the
// answer), so this route answers 200 with recorded:false for anything that
// does not name two different active jobs rather than refusing: a send to a
// non-zeus pane or from a cell kraken does not know is not an error.

// jobMessageReq is the body. Decoded with DisallowUnknownFields, so a
// misspelled key is refused rather than read as an empty `to`.
type jobMessageReq struct {
	To string `json:"to"`
}

// jobMessageHandler records one cell-to-cell message. Registered through
// mirrorGated, since it writes.
func jobMessageHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req jobMessageReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized field in request body — this route takes only to"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	to := strings.TrimSpace(req.To)
	if to == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing to"})
		return
	}
	fromCell := strings.TrimSpace(r.Header.Get(fromCellHeader))
	if fromCell == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing " + fromCellHeader + " header"})
		return
	}
	from, _, err := webJobFor(fromCell)
	if err != nil {
		webJSON(w, http.StatusOK, map[string]bool{"recorded": false})
		return
	}
	target := jobs.JobForZeusName(to)
	if target == nil || target.ID == from.ID {
		webJSON(w, http.StatusOK, map[string]bool{"recorded": false})
		return
	}
	if err := jobs.RecordCellMessage(from, target); err != nil {
		util.LogEvent("web", "error", "cell-message", err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"recorded": true})
}
