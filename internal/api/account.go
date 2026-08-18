package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// swapMainTo is core.SwapMainTo, swapped out in tests so accountSwitchHandler
// tests don't need real pool slots/keychain access — mirrors
// readUsageSnapshot's swappable-var pattern in usage.go.
var swapMainTo = core.SwapMainTo

// kickUsagePass is jobs.KickUsagePass, swapped out in tests: the real
// function fires a background goroutine that does network/keychain I/O,
// which a handler test must never trigger.
var kickUsagePass = jobs.KickUsagePass

// acquireRotationLock is jobs.AcquireRotationLock — deliberately NOT swapped
// out in most tests (unlike swapMainTo/kickUsagePass above): the real lock
// is cheap (a kernel flock over HOME, which every test already isolates to a
// temp dir), and the 409-on-held-lock test takes the SAME real lock directly
// via jobs.AcquireRotationLock to simulate cross-process contention, rather
// than stubbing this seam.
var acquireRotationLock = jobs.AcquireRotationLock

// --- POST /api/account/switch ---

type accountSwitchReq struct {
	Slug string `json:"slug"`
}

type accountSwitchResp struct {
	From string `json:"from"`
	To   string `json:"to"`
	Mode string `json:"mode"`
}

// accountSwitchHandler is a manual main-login swap. Since per-cell binding
// retired the spawn-time rotation gate, this route and `k account <slug>` are
// the only two callers of core.SwapMainTo left anywhere: no automation swaps
// the machine-wide login, at spawn or on a usage limit (the limit rescue
// rebinds the one affected cell instead). So it always executes regardless of
// usage thresholds — there is no automatic threshold left for it to be an
// exception to — and pins rotation to "manual" on success, mirroring `k
// account <slug>`'s CLI semantics and ordering. The swap runs BEFORE the mode
// write, so a failed swap never touches the mode. The swap+pin pair still runs
// under acquireRotationLock, which is now a two-party lock rather than a
// four-party one: it keeps a web-triggered switch from racing a concurrent CLI
// switch across processes.
func accountSwitchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req accountSwitchReq
	// cap the body so a misbehaving client can't buffer arbitrary bytes
	// through us — same pattern as peers.go's send/spawn/kill handlers.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	slug := strings.TrimSpace(req.Slug)
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug required"})
		return
	}

	release, ok, lockErr := acquireRotationLock()
	if lockErr != nil {
		util.LogEvent("web", "error", "account-switch", "account switch "+slug+": rotation lock unavailable: "+lockErr.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": lockErr.Error()})
		return
	}
	if !ok {
		webJSON(w, http.StatusConflict, map[string]string{"error": "rotation in flight"})
		return
	}
	defer release()

	from, to, err := swapMainTo(slug)
	if err != nil {
		util.LogEvent("web", "error", "account-switch", "account switch "+slug+": "+err.Error())
		status := http.StatusInternalServerError
		switch {
		case errors.Is(err, core.ErrRotationInFlight), errors.Is(err, core.ErrSaveBackIsTarget):
			status = http.StatusConflict
		case errors.Is(err, core.ErrInvalidSlug):
			status = http.StatusBadRequest
		}
		webJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	// Stamped before the deferred release, while this handler still holds the
	// rotation lock: a manual switch moves the same one login every claude on
	// the machine shares, so any cell or standing agent parked on a usage
	// limit has just had its repair performed by carl. Without this the next
	// rescue tick still reads SwappedAt == 0 on that record and swaps a SECOND
	// account behind the switch he made deliberately (jobs' adoptLandedSwap is
	// the reader). Logged rather than raised: the swap itself committed, and
	// reporting it as a failure would be false.
	if err := jobs.RecordMainSwap(to, time.Now()); err != nil {
		util.LogEvent("web", "error", "account-switch", "account switch "+slug+": swap ok but the main-swap watermark did not persist: "+err.Error())
	}

	// mode defaults to "manual" (the write below normally succeeds); on a
	// persist failure the swap still committed, so respond 200 — but with
	// the read-back mode, not a hardcoded "manual" the response would
	// otherwise falsely claim.
	mode := "manual"
	if err := jobs.SetRotationMode("manual", "web"); err != nil {
		// The swap already committed — main really did move to `to`. Surface
		// this as a logged warning, not a request failure: reporting an error
		// here would be misleading (the switch itself succeeded), and the
		// pin write just retries naturally on the next manual action.
		util.LogEvent("web", "error", "account-switch", "account switch "+slug+": swap ok but pin did not persist: "+err.Error())
		mode = jobs.RotationMode()
	}
	kickUsagePass()
	util.LogEvent("web", "info", "account-switch", "account switch "+slug+": "+from+" -> "+to)
	webJSON(w, http.StatusOK, accountSwitchResp{From: from, To: to, Mode: mode})
}

// --- POST /api/account/mode ---

type accountModeReq struct {
	Mode string `json:"mode"`
}

type accountModeResp struct {
	Mode string `json:"mode"`
}

// accountModeHandler sets rotation mode without swapping. The mode no longer
// governs anything that swaps main — nothing does that automatically — it
// governs PER-CELL BINDING: "auto" arms the binder, so each cell spawn takes
// the emptiest eligible pool slot and the limit rescue rebinds an exhausted
// cell to a fresh one; "manual" pins, and kraken then takes no account
// decision at all — new cells spawn unbound in the main lane and the rescue
// parks instead of rebinding. "manual" needing no swap to take effect is what
// makes this route separate from the switch above (parity with the web UI's
// banner toggle). Unlike
// jobs.SetRotationMode's own fail-closed-to-"auto" normalization, an
// unrecognized mode here is a validation error rather than a silent auto —
// a client typo should surface, not quietly land as auto.
func accountModeHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req accountModeReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	mode := strings.TrimSpace(req.Mode)
	if mode != "auto" && mode != "manual" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": `mode must be "auto" or "manual"`})
		return
	}
	if err := jobs.SetRotationMode(mode, "web"); err != nil {
		util.LogEvent("web", "error", "account-mode", "account mode "+mode+": "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "account-mode", "account mode "+mode)
	webJSON(w, http.StatusOK, accountModeResp{Mode: mode})
}
