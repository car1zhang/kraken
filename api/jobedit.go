package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kraken/jobs"
	"kraken/store"
	"kraken/util"
)

func webJobFor(slug string) (*jobs.Job, map[string]*jobs.Job, error) {
	js := jobs.LoadJobs()
	j := jobs.JobBySlug(js, slug)
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
// waiting/queued jobs, and finished ones being respec'd. A live cell's file
// is kraken's and the crew's to write.
func editableJob(j *jobs.Job) bool {
	return !j.Started() || j.State == "done"
}

// --- store-mode support: jobedit.go's endpoints operate on real files in
// file mode (unchanged below) and on the store row's frontmatter-equivalent
// + body in store mode. The optimistic-lock token stays the wire field
// "mtime" (a number, per the iOS wire-compat note) either way — in store
// mode it carries the row's rev instead of a file mtime, so a client never
// needs to know which mode it's talking to. ---

// jobEditMTime is the optimistic-lock token an editor endpoint hands back:
// the store row's rev in store mode, the file's mtime-millis (jobMTime) in
// file mode. j.File is empty for a store-backed Job (jobFromStoreRow never
// sets it), so this must branch rather than stat an empty path.
func jobEditMTime(j *jobs.Job) int64 {
	if jobs.ActiveStore() != nil {
		return j.Rev
	}
	fi, _ := os.Stat(j.File)
	return jobMTime(fi)
}

// composeRawJobContent renders a store-backed job's fields back into the
// exact canonical frontmatter+body text a file-mode job file would hold —
// reusing serializeJobFile (the form-save renderer) so raw view/edit gets
// the identical text shape regardless of storage mode.
func composeRawJobContent(j *jobs.Job) string {
	spec, logSection := splitBodyLog(j.Body)
	fields := jobFormFields{
		Slug: j.Slug, Type: j.Type, Repo: j.Repo, Base: j.Base,
		Ready: j.Ready, Solo: j.Solo, Model: j.Model, SkipDoc: j.SkipDoc,
		Prereqs: j.Prereqs, Spec: strings.TrimSpace(spec),
	}
	return serializeJobFile(fields, j.State, j.Outcome, j.Session, j.Teardown, j.Run, j.PRs, logSection)
}

// jobFieldsFromParsed converts a jobs.ParseJob result into the fields map
// store.UpdateJobFields expects (jobFieldColumns' allowlist) — the raw
// save's store-mode path, mirroring the raw file save's "whatever the user
// submitted overwrites the whole record" semantics field-for-field.
func jobFieldsFromParsed(p *jobs.Job) map[string]any {
	return map[string]any{
		"slug": p.Slug, "type": p.Type, "repo": p.Repo, "base": p.Base,
		"ready": p.Ready, "solo": p.Solo, "model": p.Model, "skip_doc": p.SkipDoc,
		"body": p.Body, "state": p.State, "outcome": p.Outcome,
		"session": p.Session, "teardown": p.Teardown, "run": int64(p.Run),
		// L6: p.Err is "" on a clean parse, which is exactly the value that
		// must land in the row to CLEAR any earlier draft-pinning save —
		// always included, not just on failure.
		"err": p.Err,
	}
}

// storePrereqs converts the form/raw editor's []jobs.Prereq into the store
// package's own Prereq shape (field Slug where jobs.Prereq calls it Job).
func storePrereqs(ps []jobs.Prereq) []store.Prereq {
	out := make([]store.Prereq, 0, len(ps))
	for _, p := range ps {
		out = append(out, store.Prereq{Slug: p.Job, Gated: p.Gated})
	}
	return out
}

// saveJobEditWithLock applies a full editor save (fields + optionally
// prereqs + optionally PRs, L10's store.SaveJobEdit — one atomic
// transaction covering all three, replacing the old three-separate-calls
// sequence of UpdateJobFields, ReplacePrereqs, and a syncStorePRs loop of
// UpsertPR/RemovePR calls, each of which used to commit on its own) to a
// store-backed job with optimistic locking mirroring the file-mode mtime
// contract: mtime==0 or force==true skips the conflict check, retrying
// once against the row's freshly-fetched rev if the first attempt (using
// j's already-loaded rev) hits a genuine race — the same one-retry shape
// store-backed WriteJobFields uses for named-field patches (jobs/store.go).
// Otherwise a mismatch against j's loaded rev refuses immediately, before
// ever touching the DB, with conflict=true so the caller can return the
// same refusal body the file-mode mtime mismatch produces.
func saveJobEditWithLock(
	st *store.Store, j *jobs.Job, mtimeTok int64, force bool,
	fields map[string]any,
	prereqs []jobs.Prereq, syncPrereqs bool,
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
	storePrereqsArg := storePrereqs(prereqs)

	newRev, err = st.SaveJobEdit(j.ID, j.Rev, fields, storePrereqsArg, syncPrereqs, storePrs, syncPRs)
	if errors.Is(err, store.ErrStaleRev) {
		if !skipCheck {
			return 0, true, nil
		}
		fresh, gerr := st.GetJobByID(j.ID)
		if gerr != nil {
			return 0, false, gerr
		}
		newRev, err = st.SaveJobEdit(j.ID, fresh.Rev, fields, storePrereqsArg, syncPrereqs, storePrs, syncPRs)
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

// webJobRaw returns a job file's full content + mtime (the editor's load and
// its optimistic-lock token).
func webJobRaw(w http.ResponseWriter, r *http.Request) {
	j := jobs.JobBySlug(jobs.LoadJobs(), r.URL.Query().Get("slug"))
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if jobs.ActiveStore() != nil {
		webJSON(w, http.StatusOK, map[string]any{
			"slug": j.Slug, "content": composeRawJobContent(j), "mtime": jobEditMTime(j),
			"editable": editableJob(j),
		})
		return
	}
	data, err := os.ReadFile(j.File)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(j.File)
	webJSON(w, http.StatusOK, map[string]any{
		"slug": j.Slug, "content": string(data), "mtime": jobMTime(fi),
		"editable": editableJob(j),
	})
}

// webJobCreate makes a fresh <slug>.job.md from the template — the browser
// twin of typing {{slug}} in a pluto note. Returns the content for editing.
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
	if st := jobs.ActiveStore(); st != nil {
		if jobs.JobBySlug(jobs.LoadJobs(), slug) != nil {
			webJSON(w, http.StatusConflict, map[string]string{"error": slug + " already exists"})
			return
		}
		// L-nit: the row is created from jobs.JobTemplate(slug) PARSED, not a
		// separately hand-typed store.Job literal — the file-mode create path
		// below writes JobTemplate's full text straight to disk, so a job
		// abandoned right after creation (never actually saved) leaves that
		// exact template behind, comments included, on next raw-view/edit.
		// Deriving the row from the same template text is how the store path
		// matches that: a second hardcoded "type build, repo monolith,
		// ready/solo false" literal here was a duplicate source of truth that
		// could silently drift from JobTemplate's own defaults.
		template := jobs.JobTemplate(slug)
		parsed := jobs.ParseJob(slug+jobs.JobSuffix, []byte(template))
		if _, err := st.CreateJob(&store.Job{
			Slug: slug, Type: parsed.Type, Repo: parsed.Repo, Base: parsed.Base,
			Ready: parsed.Ready, Solo: parsed.Solo, Model: parsed.Model, SkipDoc: parsed.SkipDoc,
			Body: parsed.Body,
		}); err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		j := jobs.JobBySlug(jobs.LoadJobs(), slug)
		if j == nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": "job created but could not be reloaded"})
			return
		}
		// the template's own prereqs (always [] today, JobTemplate has no
		// default prereqs) — included for the same single-source-of-truth
		// reason as the fields above, so a future template with real
		// defaults doesn't need a second place taught about them.
		if err := st.ReplacePrereqs(j.ID, storePrereqs(parsed.Prereqs)); err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		webBoardCache.invalidate()
		// composeRawJobContent renders the CANONICAL (comment-free)
		// frontmatter — fine for an existing job's raw view, but it drops the
		// annotated skeleton's authoring guidance (field explanations, valid
		// choices, examples) that file mode's fresh JobTemplate gives a brand
		// new job. Returning the template verbatim here matches file mode's
		// UX exactly; the row's own defaults (type build, repo monolith, ...)
		// already agree with what the template documents, and the first real
		// save parses whatever the user submits same as file mode always has.
		webJSON(w, http.StatusOK, map[string]any{
			"slug": slug, "content": jobs.JobTemplate(slug), "mtime": jobEditMTime(j), "editable": true,
		})
		return
	}
	path := filepath.Join(jobs.PlutoDir(), slug+jobs.JobSuffix)
	if _, err := os.Stat(path); err == nil {
		webJSON(w, http.StatusConflict, map[string]string{"error": slug + " already exists"})
		return
	}
	content := jobs.JobTemplate(slug)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(path)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"slug": slug, "content": content, "mtime": jobMTime(fi), "editable": true})
}

// webJobSave writes an edited job file. Optimistic lock: the save carries the
// mtime the editor loaded, and a mismatch means someone (nvim, kraken, another
// tab) wrote meanwhile — refused rather than clobbered. Unparseable content
// still saves (drafts may be mid-thought) but the parse error rides back.
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
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has a cell in flight — its file belongs to the crew right now"})
		return
	}
	if st := jobs.ActiveStore(); st != nil {
		parsed := jobs.ParseJob(j.Slug+jobs.JobSuffix, []byte(req.Content))
		// unparseable content still saves (drafts may be mid-thought) but,
		// mirroring file mode's contract exactly, only the body changes —
		// jobFieldsFromParsed(parsed) would otherwise write whatever
		// ParseJob's best-effort extraction pulled from garbled text
		// (defaults like type "build", empty repo, ready false, ...)
		// straight over the row's real frontmatter columns. Prereqs/PRs stay
		// untouched too: parsed.Prereqs/parsed.PRs are equally untrustworthy
		// when Err is set.
		fields := jobFieldsFromParsed(parsed)
		if parsed.Err != "" {
			// L6: "err" rides along even on the unparseable branch — that's
			// the whole point of the column: pin this job to draft
			// (deriveState) until a later save parses clean and clears it.
			fields = map[string]any{"body": req.Content, "err": parsed.Err}
		}
		// L10: prereqs/PRs sync only when the parse succeeded — an
		// unparseable save leaves them exactly as they were, same as file
		// mode's contract (parsed.Prereqs/parsed.PRs are equally
		// untrustworthy when Err is set). SaveJobEdit skips those two
		// passes entirely when told to, rather than replacing them with a
		// same-value no-op, so an unparseable save issues no writes beyond
		// the field update.
		syncChildren := parsed.Err == ""
		newRev, conflict, err := saveJobEditWithLock(st, j, int64(req.MTime), false,
			fields, parsed.Prereqs, syncChildren, parsed.PRs, syncChildren)
		if conflict {
			webJSON(w, http.StatusConflict, map[string]string{"error": "file changed on disk since you opened it — reload the editor"})
			return
		}
		if errors.Is(err, store.ErrSlugTaken) {
			webJSON(w, http.StatusConflict, map[string]string{"error": "slug " + parsed.Slug + " is already taken by another active job"})
			return
		}
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		webBoardCache.invalidate()
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": newRev, "warn": parsed.Err})
		return
	}
	if fi, err := os.Stat(j.File); err == nil && req.MTime != 0 && jobMTime(fi) != int64(req.MTime) {
		webJSON(w, http.StatusConflict, map[string]string{"error": "file changed on disk since you opened it — reload the editor"})
		return
	}
	if err := util.AtomicWrite(j.File, req.Content); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(j.File)
	parsed := jobs.ParseJob(filepath.Base(j.File), []byte(req.Content))
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": jobMTime(fi), "warn": parsed.Err})
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

type jobFormFields struct {
	Slug    string        `json:"slug"`
	Type    string        `json:"type"`
	Repo    string        `json:"repo"`
	Base    string        `json:"base"`
	Ready   bool          `json:"ready"`
	Solo    bool          `json:"solo"`
	Model   string        `json:"model"`
	SkipDoc bool          `json:"skip_doc"`
	Prereqs []jobs.Prereq `json:"prereqs"`
	Spec    string        `json:"spec"`
	// MTime is unix-millis (see jobMTime), decoded via mtimeToken so a
	// legacy client still sending the old string encoding round-trips too.
	MTime mtimeToken `json:"mtime"`
	// Force skips the mtime conflict check: an explicit "overwrite what's on
	// disk" from the editor after a conflict was surfaced, never a default.
	Force bool `json:"force"`
}

// webJobForm returns a job parsed into form fields, plus the datalists the
// form offers (existing slugs for prereqs, repo candidates).
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
	prereqs := j.Prereqs
	if prereqs == nil {
		prereqs = []jobs.Prereq{}
	}
	webJSON(w, http.StatusOK, map[string]any{
		"slug": j.Slug, "type": j.Type, "repo": j.Repo, "base": j.Base,
		"ready": j.Ready, "solo": j.Solo,
		"model": j.Model, "skip_doc": j.SkipDoc,
		"prereqs": prereqs,
		"spec":    strings.TrimSpace(spec),
		"mtime":   jobEditMTime(j), "editable": editableJob(j),
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
	fmt.Fprintf(&b, "type: %s\n", f.Type)
	fmt.Fprintf(&b, "repo: %s\n", jobs.OneLine(f.Repo))
	if f.Base != "" {
		fmt.Fprintf(&b, "base: %s\n", jobs.OneLine(f.Base))
	}
	fmt.Fprintf(&b, "ready: %t\n", f.Ready)
	if len(f.Prereqs) == 0 {
		b.WriteString("prereqs: []\n")
	} else {
		b.WriteString("prereqs:\n")
		for _, p := range f.Prereqs {
			fmt.Fprintf(&b, "  - job: %s\n", jobs.OneLine(p.Job))
			if p.Gated {
				b.WriteString("    gated: true\n")
			}
		}
	}
	fmt.Fprintf(&b, "solo: %t\n", f.Solo)
	// conditional like base: — absent parses as the default, so untouched
	// jobs keep their files free of the new keys
	if f.Model != "" {
		fmt.Fprintf(&b, "model: %s\n", jobs.OneLine(f.Model))
	}
	if f.SkipDoc {
		b.WriteString("skip-doc: true\n")
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
// outcome, mid-run log) are re-read fresh from disk and preserved — the form
// can't touch them by construction. Same optimistic mtime lock as raw save.
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
	switch req.Type {
	case "build", "info":
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be build or info"})
		return
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if !editableJob(j) {
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has a cell in flight — its file belongs to the crew right now"})
		return
	}
	req.Slug = j.Slug // slug is immutable here (the filename in file mode, the row identity in store mode)
	if st := jobs.ActiveStore(); st != nil {
		_, logSection := splitBodyLog(j.Body)
		body := strings.TrimSpace(req.Spec)
		if trimmed := strings.TrimRight(logSection, "\n"); trimmed != "" {
			if body != "" {
				body += "\n\n"
			}
			body += trimmed
		}
		// L6: render what these fields would look like as raw
		// job-file text and parse it back BEFORE writing, both to surface
		// any error as warn (unchanged from before) and, now, to persist
		// that same parsed.Err into the row's err column (L6) — a
		// form-built save that still fails to round-trip (e.g. an
		// OneLine-mangled edge case) must pin to draft exactly like an
		// unparseable raw save does, not silently report warn while saving
		// clean.
		content := serializeJobFile(req, j.State, j.Outcome, j.Session, j.Teardown, j.Run, j.PRs, logSection)
		parsed := jobs.ParseJob(j.Slug+jobs.JobSuffix, []byte(content))
		fields := map[string]any{
			"type": req.Type, "repo": req.Repo, "base": req.Base,
			"ready": req.Ready, "solo": req.Solo, "model": req.Model, "skip_doc": req.SkipDoc,
			"body": body, "err": parsed.Err,
		}
		// L10: form saves always replace prereqs, never touch PRs (they're
		// not part of jobFormFields by construction — same as file mode).
		newRev, conflict, err := saveJobEditWithLock(st, j, int64(req.MTime), req.Force,
			fields, req.Prereqs, true, nil, false)
		if conflict {
			webJSON(w, http.StatusConflict, map[string]any{
				"error": "file changed on disk since you opened it", "conflict": true,
			})
			return
		}
		if err != nil {
			webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		webBoardCache.invalidate()
		webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": newRev, "warn": parsed.Err})
		return
	}
	if fi, err := os.Stat(j.File); err == nil && !req.Force && req.MTime != 0 && jobMTime(fi) != int64(req.MTime) {
		webJSON(w, http.StatusConflict, map[string]any{
			"error": "file changed on disk since you opened it", "conflict": true,
		})
		return
	}
	_, logSection := splitBodyLog(j.Body)
	content := serializeJobFile(req, j.State, j.Outcome, j.Session, j.Teardown, j.Run, j.PRs, logSection)
	if err := util.AtomicWrite(j.File, content); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(j.File)
	parsed := jobs.ParseJob(filepath.Base(j.File), []byte(content))
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": jobMTime(fi), "warn": parsed.Err})
}
