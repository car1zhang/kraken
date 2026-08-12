package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/util"
)

// limithit.go — POST /api/limit-hit: the localhost mutation endpoint a
// cell's StopFailure hook (`kraken limit-hook`, jobs/limithook.go) posts to
// when a claude in that cell dies on a usage limit. It sits beside
// /api/pr-attach for the same reason that one exists: the hook runs as its
// own short-lived process and kraken serve is the sole DB writer, so the
// hook reports and the server records.
//
// Deliberately NOT mirror-gated. A mirror board is read-only about jobs, but
// this route writes no job state — it records that a limit was observed, and
// the cross-slice rule is that a mirror must still RECORD hits while never
// rescuing. The rescue itself lives inside the daemon's `if !mirror` block,
// which is where that policy belongs: gating the record too would leave a
// mirror unable to say what it saw.

// limitHitReq is the request body — exactly the five values
// jobs.parseLimitPayload pulls out of one StopFailure payload.
type limitHitReq struct {
	Slug      string `json:"slug"`
	Error     string `json:"error"`
	Details   string `json:"details"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
}

// limitHitDetailsMax caps the stored message. It is free text kraken did not
// author, shown as one line, so an over-long one is TRUNCATED rather than
// refused: the error TYPE is the payload this whole feature routes on, and
// losing a hit over a verbose message would mean a cell sitting exhausted
// with nothing recorded — the same call jobLoadNoteMax makes for the same
// reason.
const limitHitDetailsMax = 200

func limitHitHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req limitHitReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if req.Slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required"})
		return
	}
	if req.Error == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "error is required"})
		return
	}
	// A slug matching no job is a 400 rather than a 404: the only client is a
	// hook kraken itself installed with the job's own slug baked into the
	// command string, so an unknown slug here means the request is malformed
	// (a stale hook from a torn-down job, a hand-typed POST), not that the
	// caller asked about a resource it merely doesn't have yet.
	// webJobFor, not JobBySlug: the slug the StopFailure hook posts was baked
	// into this cell's settings.json at spawn (jobs/cell.go's limitHookCommand)
	// and is never rewritten mid-run, so a renamed job would refuse its own
	// hook for the rest of that cell's life — and losing this arm costs the
	// rescue its FAST trigger, leaving only the transcript arm, whose lag
	// CLAUDE.md measures in hours of a dead cell. The current slug is tried
	// first, so a live job named X always beats a renamed job that used to be X.
	j, _, ferr := webJobFor(req.Slug)
	if ferr != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "no such job"})
		return
	}
	// the resolved *Job, not req.Slug: the record is keyed on the job's
	// immutable id, since archiving frees a slug and this row outlives the
	// job that wrote it (jobs/limithook.go's limitHitKey)
	hit, err := jobs.RecordLimitHit(j, req.Error,
		// core.TruncateOneLine both one-lines (control characters, newlines
		// included, become spaces) and caps at 200 RUNES, so a multi-byte
		// message can't blow a byte-counted cap.
		core.TruncateOneLine(req.Details, limitHitDetailsMax),
		req.SessionID, req.AgentID, time.Now())
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	// Every accepted delivery logs, including error types the rescue will
	// never act on, and the type is written VERBATIM: this log is the only
	// place a change to Claude Code's error enum shows up before the rescue
	// silently stops firing. `details` is deliberately absent from the line —
	// it is text kraken did not author, and an error message is exactly where
	// a credential ends up quoted.
	util.LogEvent("web", "info", "limit-hit",
		fmt.Sprintf("limit hit %s: %s (delivery %d)", j.Slug, req.Error, hit.Deliveries))
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "deliveries": hit.Deliveries})
}
