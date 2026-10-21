package api

import (
	"encoding/json"
	"net/http"
	"regexp"

	"kraken/internal/jobs"
	"kraken/internal/util"
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
	// The repo check lives HERE and not in the hook: pr-hook runs outside
	// serve with no store, and a board-created job has no job file for it to
	// read, so its own origin-prefix narrowing is empty for every store-only
	// job — every job created since file mode was retired — and it posts
	// whatever `gh pr create` printed. A cell of another repo that amended a
	// kraken skill from the kraken checkout attached kraken's PR to its own
	// job that way (2026-10-13): the daemon told that zeus the PR merged, the
	// card carried a foreign chip, and only the restart repair dropped it
	// hours later. An unknown repo — unresolvable, origin unreadable, or an
	// origin JobRepoOriginPrefix cannot reduce to github.com/<owner>/<repo> —
	// fails OPEN: refusing on missing evidence would drop real PRs, the exact
	// loss the hook exists to prevent. And a PR of another repo that the job's
	// SPEC names is a deliverable, not a wander: a job can be filed against one
	// checkout and told to open its PR in another, and the refusal dropped
	// exactly that PR — the daemon then never tracked it, the card never
	// chipped it and the lead kept reporting it as awaiting checks. A repo
	// named in the spec is one the job was told to touch; the attach is logged
	// under its own event so the cross-repo chip is never a mystery, and the
	// daemon's restart repair vouches for it the same way (specNamedPRs).
	if prefix, ok := jobs.JobRepoOriginPrefix(j); ok && !jobs.PRURLInRepo(req.URL, prefix) {
		if !jobs.PRRepoNamedInSpec(j, req.URL) {
			util.LogEvent("web", "info", "pr-attach-refused", j.Slug+": "+req.URL+" refused — outside the job's repo "+prefix)
			webJSON(w, http.StatusConflict, map[string]string{"error": req.URL + " is not in " + j.Slug + "'s repo (" + prefix + ")"})
			return
		}
		util.LogEvent("web", "info", "pr-attach-cross-repo", j.Slug+": "+req.URL+" attached — outside the job's repo "+prefix+" but named in its spec")
	}
	attached, err := jobs.AttachJobPR(j, req.URL)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true, "attached": attached})
}
