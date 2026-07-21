package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"kraken/jobs"
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

// --- job authoring (draft + spec directly in the browser) ---

// webJobRaw returns a job file's full content + mtime (the editor's load and
// its optimistic-lock token).
func webJobRaw(w http.ResponseWriter, r *http.Request) {
	j := jobs.JobBySlug(jobs.LoadJobs(), r.URL.Query().Get("slug"))
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
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
	Slug        string        `json:"slug"`
	Type        string        `json:"type"`
	Repo        string        `json:"repo"`
	Base        string        `json:"base"`
	Ready       bool          `json:"ready"`
	Solo        bool          `json:"solo"`
	AutoApprove bool          `json:"auto_approve"`
	Prereqs     []jobs.Prereq `json:"prereqs"`
	Spec        string        `json:"spec"`
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
	fi, _ := os.Stat(j.File)
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
		"ready": j.Ready, "solo": j.Solo, "auto_approve": j.AutoApprove,
		"prereqs": prereqs,
		"spec":    strings.TrimSpace(spec),
		"mtime":   jobMTime(fi), "editable": editableJob(j),
		"err": j.Err, "slugs": slugs, "repos": jobs.RepoCandidates(),
	})
}

// serializeJobFile renders form fields back into canonical frontmatter +
// spec + the preserved kraken-owned tail (state, outcome, session, run,
// prs — the form can't touch them by construction). Round-trips through
// the strict parser by construction.
func serializeJobFile(f jobFormFields, state, outcome, session string, run int, prs []jobs.JobPR, logSection string) string {
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
	fmt.Fprintf(&b, "auto-approve: %t\n", f.AutoApprove)
	if state != "" {
		fmt.Fprintf(&b, "state: %s\n", state)
	}
	if outcome != "" {
		fmt.Fprintf(&b, "outcome: %s\n", outcome)
	}
	if session != "" {
		fmt.Fprintf(&b, "session: %s\n", session)
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
	if spec := strings.TrimSpace(f.Spec); spec != "" {
		b.WriteString(spec + "\n\n")
	}
	if logSection == "" {
		logSection = "## Mid-run log\n<!-- appended by kraken; do not hand-edit -->"
	}
	b.WriteString(logSection + "\n")
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
	if fi, err := os.Stat(j.File); err == nil && !req.Force && req.MTime != 0 && jobMTime(fi) != int64(req.MTime) {
		webJSON(w, http.StatusConflict, map[string]any{
			"error": "file changed on disk since you opened it", "conflict": true,
		})
		return
	}
	req.Slug = j.Slug // slug is the filename; immutable here
	_, logSection := splitBodyLog(j.Body)
	content := serializeJobFile(req, j.State, j.Outcome, j.Session, j.Run, j.PRs, logSection)
	if err := util.AtomicWrite(j.File, content); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(j.File)
	parsed := jobs.ParseJob(filepath.Base(j.File), []byte(content))
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": jobMTime(fi), "warn": parsed.Err})
}
