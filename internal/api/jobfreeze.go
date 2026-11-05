package api

import (
	"errors"
	"net/http"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobfreeze.go — POST /api/job/freeze: the operator's hold mark on a blocked
// job (jobs/jobfreeze.go, schema v41). A one-field route for the reason
// /api/job/color and /api/job/load are: the form save is gated on
// editableJob, which excludes exactly the blocked, live-celled jobs this is
// for.
//
// It writes no job state. A frozen job stays "blocked" on the wire; what
// changes is the additive webJob.frozen_at, which the board reads to file the
// card under the blocked column's FROZEN drawer. Thawing needs no call here
// in the usual case: the state write that takes a job off blocked clears the
// mark, and a turn no tick saw is caught by the daemon's transcript check
// (jobs.frozenThaw), so a cell that wakes up thaws itself.

// webJobFreezeReq is POST /api/job/freeze's body. Frozen is a POINTER so an
// absent key and an explicit false stay different requests: false is the
// thaw, and a body that merely failed to carry the key must not thaw a job
// it never mentioned (the same rule /api/job/load applies to its numbers).
type webJobFreezeReq struct {
	Slug   string `json:"slug"`
	Frozen *bool  `json:"frozen"`
}

// webFrozenAt is the frozen_at a card serves: the job's hold stamp while it
// displays as blocked, else 0 (omitted). The store already keeps the column
// zero off blocked; the display-state test covers the one reader-side gap, a
// blocked row whose text stopped parsing, which displays as draft.
func webFrozenAt(j *jobs.Job) int64 {
	if j.FrozenAt == 0 || jobs.DisplayState(j) != "blocked" {
		return 0
	}
	return j.FrozenAt
}

// webJobFreeze sets or clears one job's hold mark.
//
// Refusals: 400 for a body without slug or frozen (or with an unknown key),
// 404 for a slug naming no active job, 409 for freezing a job that is not
// blocked. Thawing a job that is not frozen is a 200 no-op. Mirror-gated
// inline, like every job write: a mirror board is read-only.
func webJobFreeze(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	if mirrorModeOn() {
		http.Error(w, "disabled in mirror mode", http.StatusForbidden)
		return
	}
	var req webJobFreezeReq
	if !decodeStrictBody(w, r, &req, "this route takes slug and frozen") {
		return
	}
	if req.Slug == "" || req.Frozen == nil {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "missing slug or frozen — send {\"slug\": …, \"frozen\": true|false}",
		})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	// The display state, not the stored one: a job whose text no longer
	// parses displays as draft whatever its state column says, and a card
	// the board draws as a draft is not one to put on hold. The store
	// re-checks the stored state inside its UPDATE, which is what covers a
	// cell that started working after this load.
	if *req.Frozen && jobs.DisplayState(j) != "blocked" {
		webJSON(w, http.StatusConflict, map[string]string{
			"error": j.Slug + " is " + jobs.DisplayState(j) + " — only a blocked job can be frozen",
		})
		return
	}
	if err := jobs.SetJobFrozen(j, *req.Frozen); err != nil {
		switch {
		case errors.Is(err, store.ErrJobNotBlocked):
			webJSON(w, http.StatusConflict, map[string]string{
				"error": j.Slug + " is no longer blocked — only a blocked job can be frozen",
			})
		case errors.Is(err, store.ErrNotFound):
			webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		default:
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		}
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "frozen_at": j.FrozenAt})
}
