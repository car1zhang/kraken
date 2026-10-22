package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
)

// mmEvidenceHandler serves GET /api/mm/evidence?since=[&until=] —
// jobs.CollectMMEvidence, the operator-authored rows the mind pass reads. An
// absent until is now. An open GET, like /api/mm.
func mmEvidenceHandler(w http.ResponseWriter, r *http.Request) {
	if !mmGetOnly(w, r) {
		return
	}
	q := r.URL.Query()
	since, err := strconv.ParseInt(strings.TrimSpace(q.Get("since")), 10, 64)
	if err != nil || since <= 0 {
		mmErr(w, "mm-evidence", http.StatusBadRequest, "since is required: a positive unix-millis floor")
		return
	}
	var until int64
	if raw := strings.TrimSpace(q.Get("until")); raw != "" {
		until, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || until < 0 {
			mmErr(w, "mm-evidence", http.StatusBadRequest, "until must be a non-negative unix-millis ceiling")
			return
		}
	}
	if until == 0 {
		until = time.Now().UnixMilli()
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	ev, err := jobs.CollectMMEvidence(st, since, until)
	if err != nil {
		mmErr(w, "mm-evidence", http.StatusInternalServerError, err.Error())
		return
	}
	webJSON(w, http.StatusOK, ev)
}
