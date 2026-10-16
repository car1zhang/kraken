package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// jobrename.go — POST /api/job/rename: change one active job's slug.
//
// Its own one-field route rather than a field of either editor, for the
// reason /api/job/color and /api/job/load are (jobcolor.go, jobload.go) plus
// one that is specific to this verb. The two shared reasons first: the form
// save is gated on editableJob — no lifecycle in flight — while renaming a
// LIVE cell is explicitly in scope, so routing it through the form would put
// the feature out of reach on exactly the jobs it is most useful for; and
// /api/job/saveform is a FULL REPLACE, so any client that predates a field
// clears it, which is not a shape a rename can survive.
//
// The reason of its own is that a slug is IDENTITY, not a field. Writing it
// through the raw save's field map meant a rename could ride in as a side
// effect of an unrelated edit, with none of jobs.RenameJob's gates (reserved
// prefixes, archived-name collisions, another job's former name) and none of
// its follow-through (the live cell's tmux session and the notice to its
// zeus). That path is now closed rather than merely
// unused: jobFieldsFromParsed no longer carries "slug", and a raw save whose
// job: line moved is refused with a 409 naming this route (jobedit.go).
type webJobRenameReq struct {
	Slug string `json:"slug"`
	To   string `json:"to"`
}

// webJobRename resolves the job by its CURRENT slug and hands the rename to
// jobs.RenameJob, which owns every gate and every after-effect. This handler
// adds nothing to that decision — it only shapes the answer.
func webJobRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	// The mirror gate is INLINE rather than a mirrorGated registration, so
	// that a caller reaching the handler directly still hits it: this verb
	// renames a real tmux session and types a notice into a live pane, both
	// of which belong to the live deployment no matter which DB is wired
	// (mirror.go). Same plain-text body every gated route uses, and this route
	// is in mirror_test.go's mirrorGatedRoutes table with the rest of them: an
	// inline gate sits ahead of the body decode, so the table's mirror-ON sweep
	// gets its 403 and the mirror-OFF sweep falls through to this handler's own
	// 400 — neither needs a store or a real job, which is what lets the route
	// ride the shared table rather than a bespoke test.
	if mirrorModeOn() {
		http.Error(w, "disabled in mirror mode", http.StatusForbidden)
		return
	}
	// Body cap at 1MB, matching the editor-adjacent POSTs whose bodies can carry
	// user prose (jobcolor.go, jobload.go, feedback.go) rather than the 1<<16
	// the hook endpoints use (prattach.go, limithit.go, logevent.go): the whole
	// legitimate request is two short strings, so an oversize one is refused
	// with 413 rather than read into memory, and that stays distinct from an
	// ordinary decode error's 400.
	var req webJobRenameReq
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	// DisallowUnknownFields for jobload's reason: this route takes exactly two
	// keys, and a body that names something else means something else. A
	// misspelled "to" would otherwise decode to an empty target — refused
	// below, but with a message about an empty field rather than about the key
	// the caller actually got wrong.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		// encoding/json reports an unknown field only as prose, with no typed
		// error to match on, so this substring test is the only way to tell it
		// from ordinary malformed JSON (jobload.go says the same). A rewording
		// upstream degrades the message, never the outcome — still a 400. The
		// offending key is not echoed: it comes from the body, so quoting it
		// would let a client choose the size of the response.
		if strings.Contains(err.Error(), "unknown field") {
			webJSON(w, http.StatusBadRequest, map[string]string{
				"error": "unrecognized field in request body — this route takes only slug and to",
			})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	// Both fields are literally required. An empty `to` is refused HERE rather
	// than passed down as a rename to nothing; a `to` that is non-empty but
	// slugifies to nothing is jobs.RenameJob's own refusal, since that is a
	// judgement about slugs rather than about the request's shape.
	if req.Slug == "" || req.To == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "slug and to are both required — slug names the job to rename, to is its new name",
		})
		return
	}

	// Resolved by CURRENT slug only. webJobFor's former-slug fallback exists so
	// a renamed job's OLD name keeps resolving for the cell verbs that may
	// still be holding it (questions, feedback, load, the hooks) — but renaming
	// BY a stale name is a different act: the caller would be describing a job
	// whose name it does not know, and the from-name it thinks it is moving
	// away from is already gone.
	j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	from := j.Slug

	res, err := jobs.RenameJob(j, req.To)
	if err != nil {
		// Every failure this route can produce is a refusal to rename, and the
		// caller's move is the same for all of them: read the sentence, pick a
		// different name (or unarchive first). jobs.RenameJob's gates are what
		// decide, and its post-commit steps — tmux, the zeus notice — cannot
		// fail the rename at all; they land in Notes below. So there is no
		// 500 branch to split off, and the refusal text rides back verbatim
		// rather than being re-worded into a shorter, less actionable one.
		util.LogEvent("web", "info", "job-rename", "rename "+from+" -> "+req.To+" refused: "+err.Error())
		webJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()

	// Notes are the post-commit half — a preserved artifact copy whose rename
	// FAILED outright (permissions, ENAMETOOLONG, a full disk), a tmux
	// convergence step that errored, a zeus notice that did not deliver. They
	// are logged AND served: the log is the durable record that outlives the
	// response, while the response is the only thing carl is actually looking
	// at when he renames. Serving them is not a contradiction of "best-effort
	// never fails the rename" — the rename still succeeded, and a note is a
	// sentence about what did not happen alongside it, not an error.
	//
	// The one that made this key necessary is the failed artifact move: it
	// leaves a preserved copy stranded under the old prefix exactly as a
	// conflict does — invisible on the card, which lists copies by the current
	// prefix — but it is not a conflict, so it correctly does not ride Skipped,
	// and without a key of its own the single person who could fix it was the
	// only one never told.
	for _, note := range res.Notes {
		util.LogEvent("web", "warn", "job-rename", "rename "+from+" -> "+res.Slug+": "+note)
	}
	util.LogEvent("web", "info", "job-rename", "rename "+from+" -> "+res.Slug)

	resp := map[string]any{"ok": true, "slug": res.Slug}
	// Both lists are omitted when empty rather than served as []: an empty list
	// is the normal outcome for each, and a key that appears only when
	// something was left behind is what makes its presence worth reading.
	// They stay two keys because they ask two different things of the reader —
	// skipped is a list of FILES to move by hand (every entry the source path
	// still under the old prefix, never the blocker), notes is a list of
	// SENTENCES about steps that did not complete, each carrying its cause.
	if len(res.Skipped) > 0 {
		resp["skipped"] = res.Skipped
	}
	if len(res.Notes) > 0 {
		resp["notes"] = res.Notes
	}
	webJSON(w, http.StatusOK, resp)
}
