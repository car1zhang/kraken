package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

func webJobFor(slug string) (*jobs.Job, map[string]*jobs.Job, error) {
	js := jobs.LoadJobs()
	j := jobs.JobBySlug(js, slug)
	if j == nil {
		// A slug naming no active job may still be a job's FORMER name: the
		// cell verbs (/api/questions, /api/feedback, /api/job/load) and the
		// hooks are all called by processes that learned their slug when they
		// spawned, so a rename mid-run would otherwise 404 every one of them
		// until the cell restarted. jobs.LoadJobBySlug tries the current slug
		// first and only then the former-slug index, so a live job named X
		// always beats a renamed job that used to be X.
		j = jobs.LoadJobBySlug(slug)
	}
	if j == nil {
		return nil, nil, fmt.Errorf("no job %q", slug)
	}
	bySlug := map[string]*jobs.Job{}
	for _, x := range js {
		bySlug[x.Slug] = x
	}
	return j, bySlug, nil
}

// editable jobs are ones with no lifecycle in flight: drafts, errored files,
// and finished ones being respec'd. A live cell's file is kraken's and the
// crew's to write.
func editableJob(j *jobs.Job) bool {
	return !j.Started() || j.State == "done"
}

// --- the editor's endpoints operate on the job's store row, presented as
// its frontmatter-equivalent + body. The optimistic-lock token keeps the
// wire field name "mtime" (a number, per the iOS wire-compat note) and
// carries the row's rev. ---

// jobEditMTime is the optimistic-lock token an editor endpoint hands back:
// the store row's rev.
func jobEditMTime(j *jobs.Job) int64 {
	return j.Rev
}

// composeRawJobContent renders a job's fields back into canonical
// frontmatter+body text — reusing serializeJobFile (the form-save renderer)
// so raw view/edit and the form save produce the identical text shape.
func composeRawJobContent(j *jobs.Job) string {
	spec, logSection := splitBodyLog(j.Body)
	fields := jobFormFields{
		Slug: j.Slug, Repo: j.Repo, Base: j.Base,
		Solo: j.Solo, Model: j.Model, Color: j.Color, Agent: j.Agent,
		Visibility: j.Visibility,
		Spec:       strings.TrimSpace(spec),
	}
	return serializeJobFile(fields, j.State, j.Outcome, j.Session, j.Teardown, j.Run, j.PRs, logSection)
}

// jobFieldsFromParsed converts a jobs.ParseJob result into the fields map
// store.UpdateJobFields expects (jobFieldColumns' allowlist) — the raw
// save's field map, with "whatever the user submitted overwrites the whole
// record" semantics field-for-field.
//
// "slug" is deliberately NOT in this map. A slug is the job's identity — the
// cell dir, the tmux session and the branch family are all derived from it —
// so moving it is POST /api/job/rename's whole job (jobrename.go), gates and
// follow-through included. Writing it here instead moved the column and nothing else, which
// is why the raw save now refuses a changed job: line outright rather than
// silently dropping it: a save that keeps the old name while the editor shows
// the new one is the same lie in the other direction.
func jobFieldsFromParsed(p *jobs.Job) map[string]any {
	return map[string]any{
		"repo": p.Repo, "base": p.Base,
		"solo": p.Solo, "model": p.Model, "color": p.Color, "agent": p.Agent,
		// visibility lands from the raw text like color does — the job text
		// is where it is hand-authored (an absent line reads as readonly)
		"visibility": p.Visibility,
		"body":       p.Body,
		"state":      p.State, "outcome": p.Outcome,
		"session": p.Session, "teardown": p.Teardown, "run": int64(p.Run),
		// L6: p.Err is "" on a clean parse, which is exactly the value that
		// must land in the row to CLEAR any earlier draft-pinning save —
		// always included, not just on failure.
		"err": p.Err,
	}
}

// saveJobEditWithLock applies a full editor save (fields + optionally PRs,
// L10's store.SaveJobEdit — one atomic transaction covering both, replacing
// the old separate-calls sequence of UpdateJobFields and a syncStorePRs loop
// of UpsertPR/RemovePR calls, each of which used to commit on its own) to a
// store-backed job with optimistic locking on the row's rev: mtime==0 or
// force==true skips the conflict check, retrying
// once against the row's freshly-fetched rev if the first attempt (using
// j's already-loaded rev) hits a genuine race — the same one-retry shape
// store-backed WriteJobFields uses for named-field patches (jobs/store.go).
// Otherwise a mismatch against j's loaded rev refuses immediately, before
// ever touching the DB, with conflict=true so the caller can return the
// editor's 409 refusal body.
func saveJobEditWithLock(
	st *store.Store, j *jobs.Job, mtimeTok int64, force bool,
	fields map[string]any,
	prs []jobs.JobPR, syncPRs bool,
) (newRev int64, conflict bool, err error) {
	skipCheck := force || mtimeTok == 0
	if !skipCheck && mtimeTok != j.Rev {
		return 0, true, nil
	}
	storePrs := make([]store.PR, 0, len(prs))
	for _, p := range prs {
		storePrs = append(storePrs, store.PR{URL: p.URL, State: p.State})
	}

	newRev, err = st.SaveJobEdit(j.ID, j.Rev, fields, storePrs, syncPRs)
	if errors.Is(err, store.ErrStaleRev) {
		if !skipCheck {
			return 0, true, nil
		}
		fresh, gerr := st.GetJobByID(j.ID)
		if gerr != nil {
			return 0, false, gerr
		}
		newRev, err = st.SaveJobEdit(j.ID, fresh.Rev, fields, storePrs, syncPRs)
		if errors.Is(err, store.ErrStaleRev) {
			// a second collision inside the narrow retry window: rare enough
			// to surface as a genuine conflict rather than loop indefinitely
			// (same call this makes in jobs/store.go's writeJobFieldsToStore).
			return 0, true, nil
		}
	}
	if err != nil {
		return 0, false, err
	}
	return newRev, false, nil
}

// --- job authoring (draft + spec directly in the browser) ---

// webJobRaw returns a job's full text + optimistic-lock token (the editor's
// load). With no store wired LoadJobs is empty, so the lookup 404s.
func webJobRaw(w http.ResponseWriter, r *http.Request) {
	j := jobs.JobBySlug(jobs.LoadJobs(), r.URL.Query().Get("slug"))
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	webJSON(w, http.StatusOK, map[string]any{
		"slug": j.Slug, "content": composeRawJobContent(j), "mtime": jobEditMTime(j),
		"editable": editableJob(j),
	})
}

// webJobCreate makes a fresh job row from the template. Returns the content
// for editing. Jobs live only in the store, so with none wired it refuses.
func webJobCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req webReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	slug := util.Slugify(req.Slug)
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must contain letters or digits"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "no job store wired — jobs are created through kraken serve"})
		return
	}
	if jobs.JobBySlug(jobs.LoadJobs(), slug) != nil {
		webJSON(w, http.StatusConflict, map[string]string{"error": slug + " already exists"})
		return
	}
	// L-nit: the row is created from jobs.JobTemplate(slug) PARSED, not a
	// separately hand-typed store.Job literal, so the row's defaults are
	// exactly what the template documents: a second hardcoded literal here
	// was a duplicate source of truth that could silently drift from
	// JobTemplate's own defaults.
	template := jobs.JobTemplate(slug)
	parsed := jobs.ParseJob(slug+jobs.JobSuffix, []byte(template))
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Solo: parsed.Solo, Model: parsed.Model,
		Color: parsed.Color, Agent: parsed.Agent, Body: parsed.Body,
		Visibility: parsed.Visibility,
		Author:     "operator",
	}); err != nil {
		// ErrSlugTaken is a name clash the caller can fix by typing a
		// different one, so it is a 409 like the pre-check above and never
		// the generic 500. The pre-check does not cover it: it tests only
		// ACTIVE slugs, while CreateJob also refuses an active job's FORMER
		// slug (the rename reservation, store/jobs.go) — a name no board
		// client can see, which is exactly why the refusal has to say so
		// rather than read as a server fault.
		if errors.Is(err, store.ErrSlugTaken) {
			webJSON(w, http.StatusConflict, map[string]string{
				"error": slug + " is taken — an active job already uses that name or reserved it as a former name by being renamed away from it",
			})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "job created but could not be reloaded"})
		return
	}
	webBoardCache.invalidate()
	// composeRawJobContent renders the CANONICAL (comment-free) frontmatter —
	// fine for an existing job's raw view, but it drops the annotated
	// skeleton's authoring guidance (field explanations, valid choices,
	// examples) a brand new job should open with. So the template goes back
	// verbatim; the row's own defaults already agree with what it documents
	// (they were parsed from it above), and the first real save parses
	// whatever the user submits.
	webJSON(w, http.StatusOK, map[string]any{
		"slug": slug, "content": jobs.JobTemplate(slug), "mtime": jobEditMTime(j), "editable": true,
	})
}

// webJobSave writes an edited job's raw text to its row. Optimistic lock:
// the save carries the token (the row's rev) the editor loaded, and a
// mismatch means someone (kraken, another tab) wrote meanwhile — refused
// rather than clobbered. Unparseable content still saves (drafts may be
// mid-thought) but the parse error rides back.
func webJobSave(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req struct {
		Slug    string     `json:"slug"`
		Content string     `json:"content"`
		MTime   mtimeToken `json:"mtime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if !editableJob(j) {
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has a cell in flight — the job belongs to the crew right now"})
		return
	}
	// One parse, done before anything is written because the job: line is
	// checked first. ParseJob falls back to the NAME it is given when the
	// job: line is absent or empty, and the name here is this job's own slug
	// — so an untouched or blank job: line yields parsed.Slug == j.Slug and
	// saves exactly as it always did. A line that says something else is the
	// only case this catches.
	parsed := jobs.ParseJob(j.Slug+jobs.JobSuffix, []byte(req.Content))
	if parsed.Slug != j.Slug {
		// Refused rather than applied: a slug is identity (cell dir, tmux
		// session, branch family), and the raw save writes columns. It is
		// refused rather than IGNORED for the same reason — a save that
		// quietly keeps the old name while the editor shows the new one is a
		// lie the next reader inherits. The echo is bounded (jobcolor.go's rule): the value comes
		// from the body, so an unbounded quote would let a client choose the
		// size of the response.
		webJSON(w, http.StatusConflict, map[string]string{
			"error": "job: line says " + core.TruncateOneLine(parsed.Slug, 40) + " but this job is " +
				j.Slug + " — rename it with POST /api/job/rename, which moves the cell and the branch family with it",
		})
		return
	}
	// a job resolved above, so a store is wired (LoadJobs is empty without one)
	st := jobs.ActiveStore()
	// unparseable content still saves (drafts may be mid-thought) but only
	// the body changes — jobFieldsFromParsed(parsed) would otherwise write
	// whatever ParseJob's best-effort extraction pulled from garbled text
	// (defaults like empty repo, solo false, empty spec, ...) straight over
	// the row's real frontmatter columns. PRs stay untouched too: parsed.PRs
	// is equally untrustworthy when Err is set.
	fields := jobFieldsFromParsed(parsed)
	if parsed.Err != "" {
		// L6: "err" rides along even on the unparseable branch — that's the
		// whole point of the column: pin this job to draft (deriveState)
		// until a later save parses clean and clears it.
		fields = map[string]any{"body": req.Content, "err": parsed.Err}
	}
	// L10: PRs sync only when the parse succeeded — an unparseable save
	// leaves them exactly as they were. SaveJobEdit skips that pass entirely
	// when told to, rather than replacing it with a same-value no-op, so an
	// unparseable save issues no writes beyond the field update.
	syncChildren := parsed.Err == ""
	newRev, conflict, err := saveJobEditWithLock(st, j, int64(req.MTime), false,
		fields, parsed.PRs, syncChildren)
	if conflict {
		webJSON(w, http.StatusConflict, map[string]string{"error": "job changed since you opened it — reload the editor"})
		return
	}
	// There is no ErrSlugTaken arm here: this save cannot move a slug at all
	// (jobFieldsFromParsed carries no "slug", and a job: line that differs
	// was refused above), so the store's unique-index backstop has nothing
	// here to fire on. A collision is /api/job/rename's and
	// /api/job/create's refusal.
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": newRev, "warn": parsed.Err})
}

// --- structured spec editor: frontmatter as a form, body as prose ---

// splitBodyLog separates a job body into the human spec and the kraken-owned
// mid-run log section.
func splitBodyLog(body string) (spec, log string) {
	if i := strings.Index(body, "\n## Mid-run log"); i >= 0 {
		return body[:i], strings.TrimPrefix(body[i:], "\n")
	}
	if strings.HasPrefix(body, "## Mid-run log") {
		return "", body
	}
	return body, ""
}

// jobFormFields is the job form's GET/POST shape. `ready` and `prereqs` left
// it in apiContract v6 — the queued status they configured is gone, and a form
// field for a flag nothing reads is a field a client keeps sending.
type jobFormFields struct {
	Slug  string `json:"slug"`
	Repo  string `json:"repo"`
	Base  string `json:"base"`
	Solo  bool   `json:"solo"`
	Model string `json:"model"`
	// Agent is which coding agent the cell's zeus runs — "" (claude default)
	// or "codex". Unlike Color it IS form-writable (part of the job's authored
	// contract, like Model), so it round-trips through the form save.
	Agent string `json:"agent"`
	// Color is RENDER-ONLY on the save path: serializeJobFile emits the
	// color: line from it so the raw view keeps showing the field, but
	// webJobSaveForm overwrites it from the stored job before serializing —
	// POST /api/job/color is the only writer (webJobColor says why). It is
	// still served on the form GET so a client can show the current pick.
	Color string `json:"color"`
	// Visibility is render-only and OFF the wire (json:"-"): serializeJobFile
	// emits the visibility: line from it so the raw view shows the level, and
	// both callers fill it from the stored job. POST /api/job/visibility is the
	// only form-side writer, for colour's reasons (jobvisibility.go); keeping
	// it out of the form's JSON means no form client can send it at all.
	Visibility string `json:"-"`
	Spec       string `json:"spec"`
	// MTime is the optimistic-lock token (the row's rev), decoded via
	// mtimeToken so a legacy client still sending the old string encoding
	// round-trips too.
	MTime mtimeToken `json:"mtime"`
	// Force skips the lock check: an explicit "overwrite what's stored"
	// from the editor after a conflict was surfaced, never a default.
	Force bool `json:"force"`
}

// webJobForm returns a job parsed into form fields, plus the datalists the
// form offers (existing slugs, repo candidates).
//
// `slugs` survives the prereq removal that was its original reason for
// existing: it is a plain list of the other active jobs, and clients use it
// for their own autocompletion. Serving it costs one loop over a slice already
// in hand.
func webJobForm(w http.ResponseWriter, r *http.Request) {
	js := jobs.LoadJobs()
	j := jobs.JobBySlug(js, r.URL.Query().Get("slug"))
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	spec, _ := splitBodyLog(j.Body)
	var slugs []string
	for _, x := range js {
		if x.Slug != j.Slug {
			slugs = append(slugs, x.Slug)
		}
	}
	webJSON(w, http.StatusOK, map[string]any{
		"slug": j.Slug, "repo": j.Repo, "base": j.Base,
		"solo":  j.Solo,
		"model": j.Model,
		"color": j.Color,
		"agent": j.Agent,
		"spec":  strings.TrimSpace(spec),
		"mtime": jobEditMTime(j), "editable": editableJob(j),
		"err": j.Err, "slugs": slugs, "repos": jobs.RepoCandidates(),
	})
}

// serializeJobFile renders form fields back into canonical frontmatter +
// spec + the preserved kraken-owned tail (state, outcome, session,
// teardown, run, prs — the form can't touch them by construction).
// Round-trips through the strict parser by construction.
func serializeJobFile(f jobFormFields, state, outcome, session, teardown string, run int, prs []jobs.JobPR, logSection string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "job: %s\n", f.Slug)
	fmt.Fprintf(&b, "repo: %s\n", jobs.OneLine(f.Repo))
	if f.Base != "" {
		fmt.Fprintf(&b, "base: %s\n", jobs.OneLine(f.Base))
	}
	fmt.Fprintf(&b, "solo: %t\n", f.Solo)
	// conditional like base: — absent parses as the default, so untouched
	// jobs keep their files free of the new keys
	if f.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", jobs.OneLine(f.Model))
	}
	if f.Color != "" {
		fmt.Fprintf(&b, "color: %s\n", jobs.OneLine(f.Color))
	}
	if f.Agent != "" {
		fmt.Fprintf(&b, "agent: %s\n", jobs.OneLine(f.Agent))
	}
	if f.Visibility != "" {
		fmt.Fprintf(&b, "visibility: %s\n", jobs.OneLine(f.Visibility))
	}
	if state != "" {
		fmt.Fprintf(&b, "state: %s\n", state)
	}
	if outcome != "" {
		fmt.Fprintf(&b, "outcome: %s\n", outcome)
	}
	if session != "" {
		fmt.Fprintf(&b, "session: %s\n", session)
	}
	if teardown != "" {
		fmt.Fprintf(&b, "teardown: %s\n", teardown)
	}
	if run > 0 {
		fmt.Fprintf(&b, "run: %d\n", run)
	}
	if len(prs) > 0 {
		b.WriteString("prs:\n")
		for _, p := range prs {
			fmt.Fprintf(&b, "  - url: %s\n    state: %s\n", jobs.OneLine(p.URL), jobs.OneLine(p.State))
			// watermarks are omitted (not even blank) when unset, so a
			// record watchPRs hasn't touched yet renders exactly as it did
			// before watermarks existed
			if p.LastCommentAt != "" {
				fmt.Fprintf(&b, "    last-comment-at: %s\n", jobs.OneLine(p.LastCommentAt))
			}
			if p.BumpedSHA != "" {
				fmt.Fprintf(&b, "    bumped-sha: %s\n", jobs.OneLine(p.BumpedSHA))
			}
		}
	}
	b.WriteString("---\n\n")
	spec := strings.TrimSpace(f.Spec)
	switch {
	case logSection != "":
		if spec != "" {
			b.WriteString(spec + "\n\n")
		}
		b.WriteString(strings.TrimRight(logSection, "\n") + "\n")
	case spec != "":
		b.WriteString(spec + "\n")
	}
	return b.String()
}

// webJobSaveForm writes an edited form back. Kraken-owned pieces (state,
// outcome, mid-run log) are taken from the freshly loaded row and preserved —
// the form can't touch them by construction. Same optimistic lock as raw
// save.
func webJobSaveForm(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req jobFormFields
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	// webJobFor rather than JobBySlug: the editor autosaves on a 600ms debounce
	// and FLUSHES that pending save when its body unmounts, which is exactly
	// what a rename does to it (the overlay re-keys on the new slug). That
	// flush still carries the OLD slug, so a current-only lookup would 404 it
	// and the last edits typed before the rename would vanish with the note
	// rendered onto an already-unmounted body. Resolving through the former
	// slug lands them on the same job under its new name, which is the honest
	// outcome; req.Slug is overwritten from the resolved job below, so nothing
	// downstream sees the stale name.
	j, _, ferr := webJobFor(req.Slug)
	if ferr != nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if !editableJob(j) {
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has a cell in flight — the job belongs to the crew right now"})
		return
	}
	req.Slug = j.Slug // slug is immutable here (the row identity; /api/job/rename moves it)
	// Colour is NOT a form field: POST /api/job/color owns it (webJobColor
	// explains why). The form still RENDERS it — serializeJobFile below
	// emits the color: line so the raw view keeps showing it — so it is
	// overwritten from the stored job rather than trusted from the request.
	// This is also what makes the save safe for a client that predates the
	// field: a full-replace body with no "color" key decodes to "" and would
	// otherwise silently wipe the colour with nothing to restore it from.
	req.Color = j.Color
	// Visibility likewise: rendered from the stored job, never taken from the
	// form (its JSON cannot carry it), and the fields map below omits it.
	req.Visibility = j.Visibility
	// a job resolved above, so a store is wired (LoadJobs is empty without one)
	st := jobs.ActiveStore()
	_, logSection := splitBodyLog(j.Body)
	body := strings.TrimSpace(req.Spec)
	if trimmed := strings.TrimRight(logSection, "\n"); trimmed != "" {
		if body != "" {
			body += "\n\n"
		}
		body += trimmed
	}
	// L6: render what these fields would look like as raw job text and parse
	// it back BEFORE writing, both to surface any error as warn and to
	// persist that same parsed.Err into the row's err column — a form-built
	// save that still fails to round-trip (e.g. an OneLine-mangled edge
	// case) must pin to draft exactly like an unparseable raw save does, not
	// silently report warn while saving clean.
	content := serializeJobFile(req, j.State, j.Outcome, j.Session, j.Teardown, j.Run, j.PRs, logSection)
	parsed := jobs.ParseJob(j.Slug+jobs.JobSuffix, []byte(content))
	fields := map[string]any{
		"repo": req.Repo, "base": req.Base,
		"solo": req.Solo, "model": req.Model, "agent": req.Agent,
		"body": body, "err": parsed.Err,
	}
	// L10: form saves never touch PRs (they're not part of jobFormFields by
	// construction).
	newRev, conflict, err := saveJobEditWithLock(st, j, int64(req.MTime), req.Force,
		fields, nil, false)
	if conflict {
		webJSON(w, http.StatusConflict, map[string]any{
			"error": "job changed since you opened it", "conflict": true,
		})
		return
	}
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": newRev, "warn": parsed.Err})
}
