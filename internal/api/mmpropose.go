package api

import (
	"errors"
	"net/http"
	"strconv"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// mmpropose.go — the mind pass' two writes: POST /api/mm/propose (the nightly
// job's zeus, gated on that job's stamped lead session by
// jobs.ProposeMMItems) and POST /api/mm/confirm (the operator's yes on the
// Model pane, which flips a proposed item live). Both are POST-only and gated
// with mirrorGated at registration.

type mmProposeReq struct {
	Session string            `json:"session"`
	Items   []jobs.MMProposal `json:"items"`
}

type mmConfirmReq struct {
	ID int64 `json:"id"`
}

// mmProposeMaxBody is the propose route's body cap: 20 items × (32,000 quote
// + 8,000 body runes) is up to ~3.2 MB at 4 UTF-8 bytes a rune before JSON
// escaping inflates it, so 8 MB holds any legal batch with headroom while
// still bounding the read. cmd/kraken's mmProposeMaxBody must stay equal.
const mmProposeMaxBody = 8 << 20

// mmNotMindLead is the propose 403 body.
const mmNotMindLead = "not the mind job's lead session"

func mmProposeHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmProposeReq
	if !mmDecodeLimit(w, r, &req, "session and items", mmProposeMaxBody) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	sum, err := jobs.ProposeMMItems(st, req.Session, req.Items)
	if err != nil {
		switch {
		case errors.Is(err, jobs.ErrNotMindLead):
			mmErr(w, "mm-propose", http.StatusForbidden, mmNotMindLead)
		case errors.Is(err, jobs.ErrCommitInvalid):
			mmErr(w, "mm-propose", http.StatusBadRequest, err.Error())
		default:
			mmErr(w, "mm-propose", http.StatusInternalServerError, err.Error())
		}
		return
	}
	webJSON(w, http.StatusOK, sum)
}

func mmConfirmHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmConfirmReq
	if !mmDecode(w, r, &req, "id") {
		return
	}
	if req.ID <= 0 {
		mmErr(w, "mm-confirm", http.StatusBadRequest, "missing id")
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	if err := st.ConfirmMMItem(req.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			mmErr(w, "mm-confirm", http.StatusNotFound, "unknown mental-model item")
		case errors.Is(err, store.ErrMMItemInvalidated):
			mmErr(w, "mm-confirm", http.StatusConflict, "mm item is invalidated")
		default:
			mmErr(w, "mm-confirm", http.StatusInternalServerError, err.Error())
		}
		return
	}
	util.LogEvent("web", "info", "mm-confirm", "mm-confirm "+strconv.FormatInt(req.ID, 10))
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": req.ID, "state": "live"})
}
