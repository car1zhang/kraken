package api

import (
	"encoding/json"
	"net/http"
	"regexp"

	"kraken/internal/jobs"
)

// prattach.go — POST /api/pr-attach: the new localhost mutation endpoint
// jobs/prhook.go's in-cell pr-hook POSTs to instead of writing the job file
// (or DB) directly, now that kraken serve is the sole DB writer (spec:
// "localhost mutation API additions"). Also reachable from the daemon's own
// transcript-scanning/branch-list backstops in principle, though today those
// still call jobs.AttachJobPR in-process (same package, no HTTP hop needed).

// prAttachReq is the request body: the exact slug+url pair PrHook already
// extracts before deciding where to send it.
type prAttachReq struct {
	Slug string `json:"slug"`
	URL  string `json:"url"`
}

// prURLShape matches a GitHub PR URL exactly (anchored, unlike
// jobs.prURLPattern which is deliberately unanchored for FindAllString
// scanning over free-form hook/transcript text) — this endpoint's whole
// input IS the URL, so a stray prefix/suffix should refuse rather than
// silently truncate-match.
var prURLShape = regexp.MustCompile(`^https://github\.com/[\w.-]+/[\w.-]+/pull/\d+$`)

func prAttachHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req prAttachReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	if req.Slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required"})
		return
	}
	if !prURLShape.MatchString(req.URL) {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "url is not a github pull request url"})
		return
	}
	// webJobFor, not JobBySlug: this slug is not a caller's choice — it is
	// BAKED into the cell's settings.json at spawn (jobs/cell.go's
	// prHookCommand) and installHookSettings runs only at spawn, so a job
	// renamed mid-run keeps posting its old name for the rest of that cell's
	// life. Resolving current-only would 404 every `gh pr create` in it, and
	// the loss is silent to everyone but the hook's own stderr — the PR then
	// reappears on the card only when the daemon's branch-repair pass gets to
	// it. webJobFor tries the current slug first, so a live job named X always
	// beats a renamed job that used to be X.
	j, _, ferr := webJobFor(req.Slug)
	if ferr != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	attached, err := jobs.AttachJobPR(j, req.URL)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true, "attached": attached})
}
