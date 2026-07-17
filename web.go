package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// k web — a localhost web UI over the same file-derived state as k j and the
// viewer: jobs as a kanban board, escalations inline, artifacts rendered in
// the browser. Read-only in this first version; answering stays in k v.
// Vanilla html/css/js, embedded so the installed binary is self-contained.

//go:embed web
var webAssets embed.FS

type webArtifact struct {
	Name string `json:"name"`
	URL  string `json:"url"`
}

type webPrereq struct {
	Job   string `json:"job"`
	Gated bool   `json:"gated"`
	Done  bool   `json:"done"`
}

// webEsc carries the escalation body too — the answer modal renders it (the
// Escalation type itself excludes Body from JSON for the board's sake).
type webEsc struct {
	File     string `json:"file"`
	Status   string `json:"status"`
	Category string `json:"category"`
	Title    string `json:"title"`
	Body     string `json:"body"`
	ModTime  int64  `json:"mtime"`
}

type webJob struct {
	Slug        string        `json:"slug"`
	Type        string        `json:"type"`
	Repo        string        `json:"repo"`
	State       string        `json:"state"`
	Err         string        `json:"err,omitempty"`
	Outcome     string        `json:"outcome,omitempty"`
	Success     string        `json:"success,omitempty"`
	Cell        string        `json:"cell,omitempty"`
	Live        bool          `json:"live"`
	Prereqs     []webPrereq   `json:"prereqs,omitempty"`
	Escalations []webEsc      `json:"escalations,omitempty"`
	Artifacts   []webArtifact `json:"artifacts,omitempty"`
	// Health is a computed condition on the live cell ("" = healthy): API
	// errors or dead agent processes. Never written to the job file — the
	// board shows it in BLOCKED with a retry affordance.
	Health  string `json:"health,omitempty"`
	ModTime int64  `json:"mtime"`
}

type webBoard struct {
	Daemon    daemonStatus `json:"daemon"`
	DaemonOK  bool         `json:"daemon_ok"`
	CellCap   int          `json:"cell_cap"`
	LiveCells int          `json:"live_cells"`
	Jobs      []webJob     `json:"jobs"`
}

// jobArtifacts collects a job's viewable artifacts from both homes: the live
// cell's artifacts dir and the preserved <slug>.<name> copies in pluto.
func jobArtifacts(j *Job) []webArtifact {
	var out []webArtifact
	root := cellArtifacts(j)
	for _, p := range listArtifacts(j) {
		// name is the path relative to the artifacts dir, so nested files
		// read as "icon-variations/icon-v1.png" and clients can group by dir
		name := filepath.Base(p)
		if rel, err := filepath.Rel(root, p); err == nil {
			name = rel
		}
		out = append(out, webArtifact{Name: name, URL: "/api/artifact/" + j.Slug + "/cell/" + name})
	}
	prefix := slugify(j.Slug) + "."
	entries, err := os.ReadDir(plutoDir())
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, prefix) || strings.HasSuffix(name, jobSuffix) {
			continue
		}
		out = append(out, webArtifact{Name: strings.TrimPrefix(name, prefix), URL: "/api/artifact/" + j.Slug + "/pluto/" + strings.TrimPrefix(name, prefix)})
	}
	return out
}

// artifactPath resolves an artifact request to a real file, refusing anything
// that escapes the two artifact homes (the URL is attacker-adjacent input:
// any local process can hit the port). A cell name may be a subpath
// (icon-variations/icon-v1.png) so folders are navigable, but every segment is
// validated against traversal and hidden files.
func artifactPath(j *Job, home, name string) (string, bool) {
	name = strings.TrimPrefix(name, "/")
	if name == "" {
		return "", false
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return "", false // no traversal, no empty/hidden segments
		}
	}
	var p, root string
	switch home {
	case "cell":
		root = cellArtifacts(j)
		p = filepath.Join(root, name)
	case "pluto":
		// pluto copies are flat (<slug>.<name>), never nested
		if strings.Contains(name, "/") || strings.HasSuffix(name, jobSuffix) {
			return "", false
		}
		root = plutoDir()
		p = filepath.Join(root, slugify(j.Slug)+"."+name)
	default:
		return "", false
	}
	// belt-and-suspenders: the cleaned path must still sit under its home
	if rel, err := filepath.Rel(root, p); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	if fi, err := os.Stat(p); err != nil || fi.IsDir() {
		return "", false
	}
	return p, true
}

// artifactContentType picks the served type by extension: renderable pages
// and binary formats (pdf, images) get their real type so phone/browser
// viewers render them; everything else is plain text for the markdown viewer.
func artifactContentType(p string) string {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".html", ".htm":
		return "text/html; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	default:
		return "text/plain; charset=utf-8"
	}
}

func buildWebBoard() webBoard {
	jobs := loadJobs()
	bySlug := map[string]*Job{}
	for _, j := range jobs {
		bySlug[j.Slug] = j
	}
	escs := loadEscalations(jobs)
	escByJob := map[string][]webEsc{}
	for _, e := range escs {
		escByJob[e.JobSlug] = append(escByJob[e.JobSlug], webEsc{
			File: e.File, Status: e.Status, Category: e.Category,
			Title: e.Title, Body: e.Body, ModTime: e.ModTime.Unix(),
		})
	}
	live := liveSessions()
	st, ok := readDaemonStatus()
	all, _ := orderAll(discoverAll())
	peerByName := map[string]*Peer{}
	for _, p := range all {
		if p.Name != "" {
			peerByName[p.Name] = p
		}
	}
	board := webBoard{Daemon: st, DaemonOK: ok && processAlive(st.PID), CellCap: cellCap, Jobs: []webJob{}}
	for _, j := range jobs {
		var prereqs []webPrereq
		for _, p := range j.Prereqs {
			dep := bySlug[p.Job]
			prereqs = append(prereqs, webPrereq{Job: p.Job, Gated: p.Gated, Done: dep != nil && dep.State == "done"})
		}
		wj := webJob{
			Slug: j.Slug, Type: j.Type, Repo: j.Repo,
			State: displayState(j, bySlug), Err: j.Err, Outcome: j.Outcome,
			Success: j.Success, Prereqs: prereqs,
			Escalations: escByJob[j.Slug], Artifacts: jobArtifacts(j),
			ModTime: j.ModTime.Unix(),
		}
		if live[cellSession(j)] {
			wj.Cell, wj.Live = cellSession(j), true
			if j.started() && j.State != "done" {
				board.LiveCells++
				wj.Health = cellHealth(j, all)
			}
		}
		board.Jobs = append(board.Jobs, wj)
	}
	return board
}

// --- board moves (the web UI's only writes; every one re-validates the
// transition server-side — the board in the browser is a stale snapshot) ---

type webReq struct {
	Slug string `json:"slug"`
	File string `json:"file,omitempty"` // escalation file, for answer
	Text string `json:"text,omitempty"`
}

// webAction wraps a move handler with method/JSON plumbing and uniform
// {ok}/{error} responses.
func webAction(f func(webReq) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "POST only"})
			return
		}
		var req webReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad json"})
			return
		}
		if err := f(req); err != nil {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}
}

func webJobFor(slug string) (*Job, map[string]*Job, error) {
	jobs := loadJobs()
	j := jobBySlug(jobs, slug)
	if j == nil {
		return nil, nil, fmt.Errorf("no job %q", slug)
	}
	bySlug := map[string]*Job{}
	for _, x := range jobs {
		bySlug[x.Slug] = x
	}
	return j, bySlug, nil
}

// webQueue is the draft→queued drag: flip ready in the job file. The daemon
// takes it from there.
func webQueue(req webReq) error {
	j, bySlug, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if j.Err != "" {
		return fmt.Errorf("%s is unparseable (%s) — fix the file first", j.Slug, j.Err)
	}
	if state := displayState(j, bySlug); state != "draft" {
		return fmt.Errorf("%s is %s — only drafts can be queued", j.Slug, state)
	}
	if err := writeJobFields(j, "ready", "true"); err != nil {
		return err
	}
	return nil
}

// webDone is the review→done drag: complete the job, auto-filling the outcome
// from its success criterion (the exit criterion, per the board contract).
func webDone(req webReq) error {
	j, bySlug, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	switch displayState(j, bySlug) {
	case "testing", "pr-open", "merged":
	default:
		return fmt.Errorf("%s is %s — only review-column jobs move to done", j.Slug, j.State)
	}
	// outcome preference: the crew's own words (done marker), then the
	// success criterion, then a bare "accepted"
	outcome := "accepted"
	if data, rerr := os.ReadFile(cellDoneMarker(j)); rerr == nil {
		if line := oneLine(strings.SplitN(string(data), "\n", 2)[0]); line != "" {
			outcome = line
		}
	} else if j.Success != "" {
		outcome = "met: " + oneLine(j.Success)
	}
	_, err = doneJob(j, truncateOneLine(outcome, 200))
	return err
}

// webAnswer is the blocked→working drag: the modal's text goes to the chosen
// escalation, which unblocks the cell via the normal bus path.
func webAnswer(req webReq) error {
	j, bySlug, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	switch displayState(j, bySlug) {
	case "blocked", "plan-review":
	default:
		return fmt.Errorf("%s is %s — nothing to answer", j.Slug, j.State)
	}
	if strings.TrimSpace(req.Text) == "" {
		return fmt.Errorf("empty answer")
	}
	for _, e := range loadEscalations([]*Job{j}) {
		if e.File == req.File && e.Status == "open" {
			return answerEscalation(j, e, req.Text)
		}
	}
	return fmt.Errorf("escalation not found or already answered — refresh the board")
}

// webRetry pokes an unhealthy cell agent; shared core in retryCell.
func webRetry(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	return retryCell(j)
}

// webSteer delivers a mid-run instruction to a live cell. Routing follows the
// crew contract: all inbound human traffic lands in hermes, which relays to
// zeus (and knows a material pivot re-triggers plan approval). The message is
// also appended to the job's mid-run log — the durable trail.
func webSteer(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		return fmt.Errorf("empty steering message")
	}
	if !j.started() || j.State == "done" || !liveSessions()[cellSession(j)] {
		return fmt.Errorf("%s has no live cell to steer", j.Slug)
	}
	all, _ := orderAll(discoverAll())
	_, hermes := cellAgents(j, all)
	if hermes == nil {
		return fmt.Errorf("%s: hermes not found — k o %s to investigate", j.Slug, cellSession(j))
	}
	if err := appendMidRunLog(j, "me→hermes", "steering: "+oneLine(text)); err != nil {
		return fmt.Errorf("mid-run log append failed: %v", err)
	}
	if err := sendMessage(hermes, "steering from carl: "+text); err != nil {
		return fmt.Errorf("logged, but delivery failed: %v", err)
	}
	return nil
}

// webReview is the PR-review handoff: open the cell's worktree in an editor
// to flash through the final diff. nvim gets a new tmux window in the cell
// session (LSP picks up the worktree root) raised in ghostty; VS Code opens
// the folder directly. req.Text carries the editor choice.
func webReview(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	repo, err := resolveRepo(j.Repo)
	if err != nil {
		return fmt.Errorf("job %s: %v", j.Slug, err)
	}
	wt := existingWorktree(j, repo)
	if wt == "" {
		return fmt.Errorf("%s has no worktree to review (already torn down?)", j.Slug)
	}
	switch req.Text {
	case "code":
		if err := exec.Command("code", wt).Run(); err != nil {
			return fmt.Errorf("open VS Code: %v", err)
		}
		return nil
	default: // nvim
		sess := cellSession(j)
		if !liveSessions()[sess] {
			return fmt.Errorf("cell session %s is gone — open the worktree manually: nvim %s", sess, wt)
		}
		if err := exec.Command("tmux", "new-window", "-t", sess+":", "-n", "review",
			"-c", wt, "nvim", ".").Run(); err != nil {
			return fmt.Errorf("tmux new-window: %v", err)
		}
		if _, err := raiseSession(sess); err != nil {
			return fmt.Errorf("nvim is open in %s, but %v", sess, err)
		}
		return nil
	}
}

// webKill tears down a live job's cell and returns the job to draft. The
// browser confirms before posting; the server still validates the job is
// actually started and not done.
func webKill(req webReq) error {
	j, _, err := webJobFor(req.Slug)
	if err != nil {
		return err
	}
	if !j.started() || j.State == "done" {
		return fmt.Errorf("%s has no live work to kill", j.Slug)
	}
	_, err = killJob(j)
	return err
}

// --- job authoring (draft + spec directly in the browser) ---

// editable jobs are ones with no lifecycle in flight: drafts, errored files,
// waiting/queued jobs, and finished ones being respec'd. A live cell's file
// is kraken's and the crew's to write.
func editableJob(j *Job) bool {
	return !j.started() || j.State == "done"
}

func webJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// webJobRaw returns a job file's full content + mtime (the editor's load and
// its optimistic-lock token).
func webJobRaw(w http.ResponseWriter, r *http.Request) {
	j := jobBySlug(loadJobs(), r.URL.Query().Get("slug"))
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
		"slug": j.Slug, "content": string(data), "mtime": strconv.FormatInt(fi.ModTime().UnixNano(), 10),
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
	slug := slugify(req.Slug)
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must contain letters or digits"})
		return
	}
	path := filepath.Join(plutoDir(), slug+jobSuffix)
	if _, err := os.Stat(path); err == nil {
		webJSON(w, http.StatusConflict, map[string]string{"error": slug + " already exists"})
		return
	}
	content := jobTemplate(slug)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(path)
	webJSON(w, http.StatusOK, map[string]any{"slug": slug, "content": content, "mtime": strconv.FormatInt(fi.ModTime().UnixNano(), 10), "editable": true})
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
		Slug    string `json:"slug"`
		Content string `json:"content"`
		MTime   string `json:"mtime"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	j := jobBySlug(loadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if !editableJob(j) {
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has a cell in flight — its file belongs to the crew right now"})
		return
	}
	if fi, err := os.Stat(j.File); err == nil && req.MTime != "" && req.MTime != "0" && strconv.FormatInt(fi.ModTime().UnixNano(), 10) != req.MTime {
		webJSON(w, http.StatusConflict, map[string]string{"error": "file changed on disk since you opened it — reload the editor"})
		return
	}
	if err := atomicWrite(j.File, req.Content); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(j.File)
	parsed := parseJob(filepath.Base(j.File), []byte(req.Content))
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": strconv.FormatInt(fi.ModTime().UnixNano(), 10), "warn": parsed.Err})
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
	Slug        string   `json:"slug"`
	Type        string   `json:"type"`
	Repo        string   `json:"repo"`
	Base        string   `json:"base"`
	Ready       bool     `json:"ready"`
	Solo        bool     `json:"solo"`
	AutoApprove bool     `json:"auto_approve"`
	Success     string   `json:"success"`
	Prereqs     []Prereq `json:"prereqs"`
	Spec        string   `json:"spec"`
	// MTime is an opaque string token: UnixNano exceeds JS safe-integer
	// range, so a numeric round-trip through the browser corrupts it and
	// every save false-conflicts.
	MTime string `json:"mtime"`
	// Force skips the mtime conflict check: an explicit "overwrite what's on
	// disk" from the editor after a conflict was surfaced, never a default.
	Force bool `json:"force"`
}

// webJobForm returns a job parsed into form fields, plus the datalists the
// form offers (existing slugs for prereqs, repo candidates).
func webJobForm(w http.ResponseWriter, r *http.Request) {
	jobs := loadJobs()
	j := jobBySlug(jobs, r.URL.Query().Get("slug"))
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	spec, _ := splitBodyLog(j.Body)
	fi, _ := os.Stat(j.File)
	var slugs []string
	for _, x := range jobs {
		if x.Slug != j.Slug {
			slugs = append(slugs, x.Slug)
		}
	}
	prereqs := j.Prereqs
	if prereqs == nil {
		prereqs = []Prereq{}
	}
	webJSON(w, http.StatusOK, map[string]any{
		"slug": j.Slug, "type": j.Type, "repo": j.Repo, "base": j.Base,
		"ready": j.Ready, "solo": j.Solo, "auto_approve": j.AutoApprove,
		"success": j.Success, "prereqs": prereqs,
		"spec":  strings.TrimSpace(spec),
		"mtime": strconv.FormatInt(fi.ModTime().UnixNano(), 10), "editable": editableJob(j),
		"err": j.Err, "slugs": slugs, "repos": repoCandidates(),
	})
}

// serializeJobFile renders form fields back into canonical frontmatter +
// spec + the preserved kraken-owned tail. Round-trips through the strict
// parser by construction.
func serializeJobFile(f jobFormFields, state, outcome, logSection string) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "job: %s\n", f.Slug)
	fmt.Fprintf(&b, "type: %s\n", f.Type)
	fmt.Fprintf(&b, "repo: %s\n", oneLine(f.Repo))
	if f.Base != "" {
		fmt.Fprintf(&b, "base: %s\n", oneLine(f.Base))
	}
	fmt.Fprintf(&b, "ready: %t\n", f.Ready)
	if len(f.Prereqs) == 0 {
		b.WriteString("prereqs: []\n")
	} else {
		b.WriteString("prereqs:\n")
		for _, p := range f.Prereqs {
			fmt.Fprintf(&b, "  - job: %s\n", oneLine(p.Job))
			if p.Gated {
				b.WriteString("    gated: true\n")
			}
		}
	}
	fmt.Fprintf(&b, "solo: %t\n", f.Solo)
	fmt.Fprintf(&b, "auto-approve: %t\n", f.AutoApprove)
	if s := strings.TrimSpace(f.Success); s != "" {
		b.WriteString("success: |\n")
		for _, line := range strings.Split(s, "\n") {
			b.WriteString("  " + line + "\n")
		}
	}
	if state != "" {
		fmt.Fprintf(&b, "state: %s\n", state)
	}
	if outcome != "" {
		fmt.Fprintf(&b, "outcome: %s\n", outcome)
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
	case "build", "explore", "question":
	default:
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "type must be build, explore, or question"})
		return
	}
	j := jobBySlug(loadJobs(), req.Slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no such job"})
		return
	}
	if !editableJob(j) {
		webJSON(w, http.StatusConflict, map[string]string{"error": j.Slug + " has a cell in flight — its file belongs to the crew right now"})
		return
	}
	if fi, err := os.Stat(j.File); err == nil && !req.Force && req.MTime != "" && req.MTime != "0" && strconv.FormatInt(fi.ModTime().UnixNano(), 10) != req.MTime {
		webJSON(w, http.StatusConflict, map[string]any{
			"error": "file changed on disk since you opened it", "conflict": true,
		})
		return
	}
	req.Slug = j.Slug // slug is the filename; immutable here
	_, logSection := splitBodyLog(j.Body)
	content := serializeJobFile(req, j.State, j.Outcome, logSection)
	if err := atomicWrite(j.File, content); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	fi, _ := os.Stat(j.File)
	parsed := parseJob(filepath.Base(j.File), []byte(content))
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "mtime": strconv.FormatInt(fi.ModTime().UnixNano(), 10), "warn": parsed.Err})
}

// tailscaleIP picks the first IPv4 address in addrs that falls in
// tailscale's CGNAT range, 100.64.0.0/10 (first octet 100, second 64-127).
// A pure function over an address list so it's testable without touching
// real interfaces.
func tailscaleIP(addrs []net.Addr) (string, bool) {
	for _, a := range addrs {
		var ip net.IP
		switch v := a.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		ip4 := ip.To4()
		if ip4 == nil {
			continue // loopback/IPv6-only addrs never match a CGNAT v4 range
		}
		if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
			return ip4.String(), true
		}
	}
	return "", false
}

// detectTailscaleIP gathers every local interface address and picks the
// tailnet one — the thin, non-testable caller around tailscaleIP.
func detectTailscaleIP() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", fmt.Errorf("enumerate network interfaces: %v", err)
	}
	var addrs []net.Addr
	for _, iface := range ifaces {
		a, err := iface.Addrs()
		if err != nil {
			continue // interface came and went; not fatal to the scan
		}
		addrs = append(addrs, a...)
	}
	ip, ok := tailscaleIP(addrs)
	if !ok {
		return "", fmt.Errorf("no tailscale interface (100.64.0.0/10) found — is tailscale up?")
	}
	return ip, nil
}

func cmdWeb(args []string) {
	port := "7777"
	tailnet := false
	explicitAddr := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-p", "--port":
			i++
			if i >= len(args) {
				die("-p needs a port")
			}
			port = args[i]
		case "--tailnet":
			tailnet = true
		case "-addr":
			i++
			if i >= len(args) {
				die("-addr needs a host:port")
			}
			explicitAddr = args[i]
		default:
			die("unknown argument: %s (usage: k web [-p port] [--tailnet] [-addr host:port])", args[i])
		}
	}
	addr := "127.0.0.1:" + port
	if tailnet {
		ip, err := detectTailscaleIP()
		if err != nil {
			die("%v", err)
		}
		addr = ip + ":" + port
	}
	if explicitAddr != "" {
		addr = explicitAddr // overrides --tailnet/-p, per usage
	}
	static, err := fs.Sub(webAssets, "web")
	if err != nil {
		die("embedded assets: %v", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.FS(static)))
	mux.HandleFunc("/api/health", healthHandler)
	mux.HandleFunc("/api/peers", peersHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/read", peersReadHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/capture", peersCaptureHandler(defaultPeerSource))
	mux.HandleFunc("/api/peers/send", peersSendHandler(defaultPeerSource))
	mux.HandleFunc("/api/board", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(buildWebBoard())
	})
	mux.HandleFunc("/api/queue", webAction(webQueue))
	mux.HandleFunc("/api/done", webAction(webDone))
	mux.HandleFunc("/api/answer", webAction(webAnswer))
	mux.HandleFunc("/api/retry", webAction(webRetry))
	mux.HandleFunc("/api/kill", webAction(webKill))
	mux.HandleFunc("/api/steer", webAction(webSteer))
	mux.HandleFunc("/api/review", webAction(webReview))
	mux.HandleFunc("/api/job/raw", webJobRaw)
	mux.HandleFunc("/api/job/create", webJobCreate)
	mux.HandleFunc("/api/job/save", webJobSave)
	mux.HandleFunc("/api/job/form", webJobForm)
	mux.HandleFunc("/api/job/saveform", webJobSaveForm)
	// /api/artifact/<slug>/<cell|pluto>/<name>
	mux.HandleFunc("/api/artifact/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/api/artifact/"), "/", 3)
		if len(parts) != 3 {
			http.NotFound(w, r)
			return
		}
		j := jobBySlug(loadJobs(), parts[0])
		if j == nil {
			http.NotFound(w, r)
			return
		}
		p, ok := artifactPath(j, parts[1], parts[2])
		if !ok {
			http.NotFound(w, r)
			return
		}
		// html deliverables render as pages (local, agent-authored content —
		// same trust as opening the file); binary formats viewers can render
		// get their real type; everything else stays plain text for the
		// in-board markdown viewer
		w.Header().Set("Content-Type", artifactContentType(p))
		http.ServeFile(w, r, p)
	})
	fmt.Printf("kraken web ui → http://%s (ctrl-c to stop)\n", addr)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	if err := srv.ListenAndServe(); err != nil {
		die("web server: %v", err)
	}
}
