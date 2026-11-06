package api

import (
	"fmt"
	"net/http"
	"sync"

	"kraken/internal/core"
	"kraken/internal/util"
)

// sleep.go — GET/POST /api/sleep, the server half of the header's sleep
// button: whether this Mac's sleep is disabled, and the switch for it
// (core/sleep.go: `pmset -a disablesleep` plus a caffeinate). The state is
// read from pmset on every GET, so a change made in a terminal shows on the
// board at its next poll.
//
// The POST names the state it wants instead of asking for a flip. A board
// whose last poll predates a change made elsewhere would otherwise undo that
// change while showing the opposite, and a double click would cancel itself.

// webSleep is the route's whole payload, GET and POST alike.
type webSleep struct {
	// Disabled is pmset's SleepDisabled: true while the Mac is held awake.
	Disabled bool `json:"disabled"`
}

// webSleepReq is the POST body. A pointer, so a body that carries no
// `disabled` key is a refusal and not a request to re-enable sleep.
type webSleepReq struct {
	Disabled *bool `json:"disabled"`
}

// The two core calls behind seams: no test in this package runs pmset.
var (
	sleepDisabledFunc    = core.SleepDisabled
	setSleepDisabledFunc = core.SetSleepDisabled
)

// sleepMu serializes the writes. The set is check-then-act (is a caffeinate
// running, else start one), and two at once would each start their own.
var sleepMu sync.Mutex

// sleepHandler handles /api/sleep. The POST gates itself inline so the GET
// stays reachable under --mirror: a mirror may show the state, and must not
// change a setting of the machine the live deployment runs on.
func sleepHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		disabled, err := sleepDisabledFunc()
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		webJSON(w, http.StatusOK, webSleep{Disabled: disabled})
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		sleepPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// sleepPost sets the state and answers with what pmset reports afterwards,
// which is the state the button should show whatever was asked for.
func sleepPost(w http.ResponseWriter, r *http.Request) {
	var req webSleepReq
	if !decodeStrictBody(w, r, &req, "this route takes disabled") {
		return
	}
	if req.Disabled == nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing disabled — send {\"disabled\": true} or {\"disabled\": false}"})
		return
	}
	sleepMu.Lock()
	defer sleepMu.Unlock()
	if err := setSleepDisabledFunc(*req.Disabled); err != nil {
		util.LogEvent("web", "error", "sleep", fmt.Sprintf("set disabled=%v: %v", *req.Disabled, err))
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	disabled, err := sleepDisabledFunc()
	if err != nil {
		util.LogEvent("web", "error", "sleep", fmt.Sprintf("read back after set disabled=%v: %v", *req.Disabled, err))
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "sleep", fmt.Sprintf("sleep disabled=%v", disabled))
	webJSON(w, http.StatusOK, webSleep{Disabled: disabled})
}
