package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// feedback.go — GET/POST /api/feedback (54-feedback-contract.md): rows in
// kraken.db are the ONLY store for artifact feedback now (no doc-feedback
// dir, no dual-write) — resolving the target artifact reuses the same
// ArtifactPath/ArtifactBytes helpers board.go's artifact route serves from,
// which is what makes this work for a live cell's files AND a store-backed
// done/archived job identically (Q2).

// feedbackNoteMaxLen caps a feedback note's length (after trim, counted in
// runes) — same convention as actions.go's docAnswerMaxLen, but larger: a
// note rides the wire on EVERY /api/board poll for as long as the job's
// card is visible (unlike an answer, which is a one-shot write), so an
// unbounded note is worse here, not just as bad.
const feedbackNoteMaxLen = 8000

// feedbackReq is every carl-facing form of POST /api/feedback (create,
// edit, delete) plus the nightly dream pass' addressed form, distinguished
// by which fields are set — see feedbackPost's dispatch.
type feedbackReq struct {
	Slug string `json:"slug"`
	// Home/Name identify the artifact for a create, exactly like the
	// artifact route's own URL (/api/artifact/<slug>/<home>/<name>).
	Home string `json:"home,omitempty"`
	Name string `json:"name,omitempty"`
	// ID targets an existing row: edit, delete, or the dream pass' addressed form.
	ID int64 `json:"id,omitempty"`
	// Feedback is the note text for create/edit.
	Feedback string `json:"feedback,omitempty"`
	// Delete set true is the delete form.
	Delete bool `json:"delete,omitempty"`
	// Addressed is the nightly dream pass' free-text account of the
	// durable fix it shipped for the note — never sent by the iOS app/web
	// UI (see ios/API.md's "not modeled" note on this form). A pointer, not
	// a plain string: the dispatch below must tell "key absent" from "key
	// present with an empty value" apart, since an explicitly empty
	// addressed is that pass clearing a prior note, not a typo'd edit
	// request.
	Addressed *string `json:"addressed,omitempty"`
}

// feedbackHandler serves GET (zeus' read) and POST (carl's create/edit/
// delete, the dream pass' addressed) /api/feedback. POST is mirror-gated:
// a mirror process runs a read-only board against a sandbox DB, and this is
// a write like any other webAction, even though nothing here drives a real tmux
// pane (see actions.go's mirrorGatedActions doc comment). The gate uses the
// same plain-text http.Error(...) shape every webAction-wrapped gate uses
// (actions.go's webAction, mirror.go), not a JSON body — mirror_test.go
// asserts the exact body across every gated route, this one included.
func feedbackHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		feedbackGet(w, r)
	case http.MethodPost:
		if mirrorModeOn() {
			http.Error(w, "disabled in mirror mode", http.StatusForbidden)
			return
		}
		feedbackPost(w, r)
	default:
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET or POST only"})
	}
}

// feedbackGet serves zeus' `kraken feedback get` read: every entry (open +
// addressed) for one job. ListFeedbackMeta is enough here — zeus never
// needs the frozen snapshot bytes, only note/addressed state, same as the
// board's own per-job array. The job is resolved first (active or
// archived, same as every other feedback op) so an unknown/typo'd slug
// 404s instead of silently reading as "no feedback yet" — the cell verbs
// rely on a non-zero exit to distinguish the two, and the row scan is keyed
// on the RESOLVED job's own id (never the raw/resolved slug string): a slug
// alone can't disambiguate two generations of the same reused name while
// one is archived and the other active — see store.ListFeedback's doc
// comment. Always serves a JSON array, never null (B3): a job with no
// feedback yet gets "[]".
func feedbackGet(w http.ResponseWriter, r *http.Request) {
	slug := r.URL.Query().Get("slug")
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing slug"})
		return
	}
	j := resolveJobBySlug(slug)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	fs, err := st.ListFeedbackMeta(j.ID)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := webFeedbackFrom(fs)
	if out == nil {
		out = []webFeedback{}
	}
	webJSON(w, http.StatusOK, map[string]any{"feedback": out})
}

// webSweepFeedback is one row of GET /api/feedback/sweep: the dream loop's
// CROSS-JOB read, so unlike webFeedback (the board's per-job array, where the
// owning job is implied by the card the array hangs off) every entry has to
// name its own job. It also carries snapshot_url — the first read path the
// frozen `snapshot` column has ever had — so the nightly pass can look at the
// artifact bytes carl was actually staring at when he wrote the note, rather
// than at whatever the artifact says today.
//
// Kept as its own DTO rather than an extra field or two bolted onto
// webFeedback for the reason every wire type in this package is its own DTO:
// the board polls webFeedback on a tick for every visible card, and neither
// of these two keys has any business riding that poll.
type webSweepFeedback struct {
	ID       int64  `json:"id"`
	Job      string `json:"job"`
	Artifact string `json:"artifact"`
	Note     string `json:"note"`
	At       int64  `json:"at"`
	// Addressed/AddressedAt are the dream pass' account of the durable fix
	// it shipped — omitempty, exactly as on webFeedback: a note it has not
	// closed simply has neither.
	Addressed   string `json:"addressed,omitempty"`
	AddressedAt string `json:"addressed_at,omitempty"`
	// SnapshotURL always rides, even for a row whose blob happens to be empty:
	// FeedbackSince deliberately does not fetch snapshot bytes (they'd be pure
	// waste on a sweep that mostly never opens them), so this handler cannot
	// know which rows have one. Fetching a blob-less row's URL 404s — see
	// feedbackSnapshotHandler.
	SnapshotURL string `json:"snapshot_url"`
}

// feedbackSnapshotURL is the route a swept row's frozen bytes are served
// from. Built here rather than by the caller so the sweep response and the
// route registration can never drift.
func feedbackSnapshotURL(id int64) string {
	return "/api/feedback/snapshot?id=" + strconv.FormatInt(id, 10)
}

// webSweepFeedbackFrom converts store.Feedback rows into the sweep DTO —
// same nil/empty-to-nil convention as webFeedbackFrom; the handler turns nil
// into an empty array so the wire never carries null (B3).
func webSweepFeedbackFrom(fs []store.Feedback) []webSweepFeedback {
	if len(fs) == 0 {
		return nil
	}
	out := make([]webSweepFeedback, len(fs))
	for i, f := range fs {
		out[i] = webSweepFeedback{
			ID: f.ID, Job: f.JobSlug, Artifact: f.Artifact, Note: f.Note, At: f.TS,
			Addressed: f.Addressed, AddressedAt: f.AddressedAt,
			SnapshotURL: feedbackSnapshotURL(f.ID),
		}
	}
	return out
}

// feedbackSweepHandler serves GET /api/feedback/sweep?since=<unix millis>:
// every feedback note created at or after the floor, across ALL jobs, in the
// order store.FeedbackSince returns them (oldest first, ts then id). This is
// the dream loop's evidence read — the first caller of ListFeedback's
// long-advertised unscoped sweep, by way of FeedbackSince.
//
// `since` is required and must parse as an integer: there is deliberately no
// default window. A sweep that silently fell back to "everything" (or to
// "the last day") the moment a caller typo'd the parameter would either
// re-mine months of already-corroborated notes or quietly mine the wrong
// window, and neither failure announces itself — a 400 does. The CLI verb
// (`kraken feedback sweep`) is where the convenience default lives instead,
// because there the default is visible in `--help` and in the usage line.
//
// A GET is a read, so it stays reachable under --mirror.
func feedbackSweepHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("since"))
	if raw == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing since (unix millis)"})
		return
	}
	since, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad since " + strconv.Quote(raw) + " — want unix millis"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	fs, err := st.FeedbackSince(since)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := webSweepFeedbackFrom(fs)
	if out == nil {
		out = []webSweepFeedback{}
	}
	webJSON(w, http.StatusOK, map[string]any{"feedback": out})
}

// snapshotExtOK reports whether ext is a plain alphanumeric file extension.
// snapshot_ext is stored from a filename's own extension, so in practice it
// is always something like "md"/"png" — but it is stored data, and this
// handler interpolates it into a Content-Disposition filename and an
// extension-keyed mime lookup, so it is validated rather than trusted.
func snapshotExtOK(ext string) bool {
	if ext == "" || len(ext) > 8 {
		return false
	}
	for _, r := range ext {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// inertSnapshotMimeType is artifactMimeType with active content declawed:
// text/html and image/svg+xml are both script-executing document types in a
// browser, and a snapshot is UNTRUSTED bytes — an artifact an agent wrote,
// frozen months ago, replayed now from a same-origin route that also serves
// the board's own API. Serving one as text/html would let it script the
// board's origin. Everything else (png/pdf/plain/...) keeps its real type,
// which is what makes the route useful for looking at what carl saw.
func inertSnapshotMimeType(name string) string {
	mt := artifactMimeType(name)
	if strings.HasPrefix(mt, "text/html") || mt == "image/svg+xml" {
		return "text/plain; charset=utf-8"
	}
	return mt
}

// feedbackSnapshotHandler serves GET /api/feedback/snapshot?id=<row id>: the
// frozen artifact bytes one feedback row captured at submit (or last edit)
// time. The dream loop's evidence link and the board's feedback drilldown
// both read it; nothing else in the binary has ever read this column.
//
// The bytes are served INERT, three headers deep, for the reason
// inertSnapshotMimeType explains: Content-Disposition: attachment (never a
// top-level document), X-Content-Type-Options: nosniff (the declawed
// text/plain can't be sniffed back up into html), and a `sandbox` CSP (belt
// and suspenders if some future caller renders it in a frame anyway). A
// snapshot of an .html artifact must never execute on this origin.
//
// A missing/non-numeric id is 400, an unknown id 404, and a row whose blob is
// empty is ALSO 404 rather than a zero-byte 200: "this row has no frozen
// copy" and "here is the frozen copy, which is empty" are different answers,
// and only the first one is true.
func feedbackSnapshotHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	raw := strings.TrimSpace(r.URL.Query().Get("id"))
	if raw == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "missing id"})
		return
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad id " + strconv.Quote(raw)})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	f, err := st.GetFeedback(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown feedback"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if len(f.Snapshot) == 0 {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "no snapshot for this feedback"})
		return
	}
	name := "feedback-" + strconv.FormatInt(id, 10)
	if snapshotExtOK(f.SnapshotExt) {
		name += "." + f.SnapshotExt
	}
	w.Header().Set("Content-Type", inertSnapshotMimeType(name))
	w.Header().Set("Content-Disposition", "attachment; filename=\""+name+"\"")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	// A snapshot row is immutable-ish (an edit re-freezes it in place under the
	// same id), so no-store keeps a re-read after an edit from serving the old
	// bytes — the same reasoning web/serve.go's artifactHandler uses.
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.Snapshot))
}

// feedbackPost decodes the shared request body and dispatches to whichever
// of the four forms it matches, in the order the fields disambiguate them:
// an id + delete is delete; an id + an addressed KEY (present, regardless
// of value — see feedbackReq's doc comment) is zeus' form; any other id is
// an edit; anything else is a create attempt. An oversize body is refused
// with 413, distinct from an ordinary JSON-decode error (400).
func feedbackPost(w http.ResponseWriter, r *http.Request) {
	var req feedbackReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	st := jobs.ActiveStore()
	if st == nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "no store wired"})
		return
	}
	switch {
	case req.ID != 0 && req.Delete:
		existing, err := lookupOwnedFeedback(st, req.ID, req.Slug)
		if err != nil {
			feedbackLookupErr(w, err)
			return
		}
		feedbackDelete(w, st, existing)
	case req.ID != 0 && req.Addressed != nil:
		// {id, addressed, feedback} names two different forms at once: the
		// addressed branch wins the dispatch and writes ONLY the dream-pass-owned
		// columns, so honoring it would silently drop the note edit the same
		// request also asked for. Refuse the whole request instead — a
		// caller that means both must send two requests.
		if req.Feedback != "" {
			webJSON(w, http.StatusBadRequest, map[string]string{"error": "addressed and feedback are separate forms — send one per request"})
			return
		}
		existing, err := lookupOwnedFeedback(st, req.ID, req.Slug)
		if err != nil {
			feedbackLookupErr(w, err)
			return
		}
		feedbackAddressed(w, st, existing, req)
	case req.ID != 0:
		existing, err := lookupOwnedFeedback(st, req.ID, req.Slug)
		if err != nil {
			feedbackLookupErr(w, err)
			return
		}
		feedbackEdit(w, st, existing, req)
	case req.Home != "" || req.Name != "":
		feedbackCreate(w, st, req)
	default:
		util.LogEvent("web", "error", "feedback", "feedback "+req.Slug+": unrecognized request")
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "unrecognized feedback request — need (home,name) to create or id to edit/delete/address"})
	}
}

// lookupOwnedFeedback resolves id's row and verifies it actually belongs to
// slug's job (B1): edit/delete/addressed are id-only mutations, so without
// this check any caller that knows (or guesses) a row id can mutate a
// DIFFERENT job's feedback simply by sending a mismatched or even
// nonexistent slug — {slug:"job-b", id:<job-a's row>, ...} would otherwise
// succeed. The ownership check compares the row's JobID against the
// resolved job's own id, NOT job_slug strings: job_slug is mutable and a
// freed slug can be reused by a brand-new job, so for as long as that new
// job stays active it shares an identical job_slug with the older job's
// already-archived rows — a slug-string comparison would let either side
// reach through to the other's feedback. JobID is each row's immutable
// owner and never collides across generations (0/NULL never equals a real,
// positive job id). Returns store.ErrNotFound (mapped to 404 by the caller)
// for either "no such row" or "row exists but isn't slug's" — the two cases
// are deliberately indistinguishable to the client, same as any other
// ownership check.
func lookupOwnedFeedback(st *store.Store, id int64, slug string) (*store.Feedback, error) {
	existing, err := st.GetFeedback(id)
	if err != nil {
		return nil, err
	}
	j := resolveJobBySlug(slug)
	if j == nil || j.ID != existing.JobID {
		return nil, store.ErrNotFound
	}
	return existing, nil
}

// feedbackLookupErr maps lookupOwnedFeedback's error to the wire: 404 for
// ErrNotFound (unknown row, or a row that exists but isn't the caller's),
// 500 for anything else (a real store failure).
func feedbackLookupErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrNotFound) {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown feedback"})
		return
	}
	webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
}

// resolveJobForHome mirrors the artifact route's own job lookup: home
// decides the namespace the slug resolves in, so an archived "foo-2" and an
// active "foo-2" never collide.
func resolveJobForHome(slug, home string) *jobs.Job {
	if home == "archive" {
		return jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), slug)
	}
	return jobs.JobBySlug(jobs.LoadJobs(), slug)
}

// resolveJobBySlug finds a job by slug regardless of home — feedback rows
// carry no home column (only the job_slug + artifact name), so re-resolving
// on edit must check both the active and archived namespaces.
func resolveJobBySlug(slug string) *jobs.Job {
	if j := jobs.JobBySlug(jobs.LoadJobs(), slug); j != nil {
		return j
	}
	// A slug naming no ACTIVE job may still be one's FORMER name: a cell that
	// was renamed mid-run keeps calling `kraken feedback` with the slug it
	// spawned with. Tried after the current-slug lookup (so a live job named X
	// always beats a renamed job that used to be X) and before the archived
	// namespace, which is unchanged: an archived job is matched by its numbered
	// ARCHIVE name (feedbackJobKey), not by a bare slug an active job could
	// also have held.
	if j := jobs.LoadJobBySlug(slug); j != nil {
		return j
	}
	return jobs.ArchivedJobByName(jobs.LoadArchivedJobs(), slug)
}

// feedbackJobKey is the identifier feedback.job_slug carries for j: an
// archived job's numbered archive name, or an active job's own slug —
// mirrors board.go's jobFeedback call sites (j.Slug for the active loop,
// a.ArchivedName for the archived one) and store.ArchiveJob/UnarchiveJob's
// own re-keying (B2), so every writer and reader of job_slug agrees on
// which string identifies a given job.
func feedbackJobKey(j *jobs.Job) string {
	if j.ArchivedName != "" {
		return j.ArchivedName
	}
	return j.Slug
}

// currentArtifactHome picks the home an artifact is CURRENTLY served from
// for j, mirroring jobArtifacts' own live/dead precedence (Q12: a live
// cell's artifacts are never DB-backed) — used on edit, where the request
// carries no home of its own, only the job's present state.
func currentArtifactHome(j *jobs.Job) string {
	if j.ArchivedName != "" {
		return "archive"
	}
	if jobs.LiveSessions()[jobs.CellSession(j)] {
		return "cell"
	}
	return "pluto"
}

// resolveArtifact reads name's current bytes for j under home. For home
// "cell" this is a straight file read (ArtifactPath) — Q12's rule that a
// live cell's artifacts are never DB-backed. Every other home reads the
// store (ArtifactBytes) — the same single source web/serve.go's
// artifactHandler serves those bytes from (B6), so a re-frozen snapshot
// always captures what was just viewed. ext is the extension (without the
// dot) stamped alongside the snapshot.
func resolveArtifact(j *jobs.Job, home, name string) (data []byte, ext string, ok bool) {
	ext = strings.TrimPrefix(filepath.Ext(name), ".")
	if home == "cell" {
		if p, pok := ArtifactPath(j, home, name); pok {
			if b, err := os.ReadFile(p); err == nil {
				return b, ext, true
			}
		}
		return nil, "", false
	}
	if b, _, bok := ArtifactBytes(j, home, name); bok {
		return b, ext, true
	}
	return nil, "", false
}

// validFeedbackNote trims and length-caps a feedback note, shared by create
// and edit so both enforce feedbackNoteMaxLen identically.
func validFeedbackNote(raw string) (note string, err error) {
	note = strings.TrimSpace(raw)
	if note == "" {
		return "", errors.New("feedback is empty")
	}
	if n := len([]rune(note)); n > feedbackNoteMaxLen {
		return "", errors.New("feedback note too long")
	}
	return note, nil
}

// feedbackCreate resolves the artifact exactly like the artifact route
// does, then INSERTs a fresh row: snapshot of the bytes as currently
// served, the note, the job's session id at submit time, and a
// server-stamped ts (AddFeedback fills TS when left zero).
func feedbackCreate(w http.ResponseWriter, st *store.Store, req feedbackReq) {
	note, err := validFeedbackNote(req.Feedback)
	if err != nil {
		util.LogEvent("web", "error", "feedback-create", "feedback-create "+req.Slug+": "+err.Error())
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if req.Home == "" || req.Name == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "home and name are required"})
		return
	}
	j := resolveJobForHome(req.Slug, req.Home)
	if j == nil {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown job"})
		return
	}
	data, ext, ok := resolveArtifact(j, req.Home, req.Name)
	if !ok {
		webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown artifact"})
		return
	}
	if _, err := st.AddFeedback(store.Feedback{
		// JobID/JobSlug: the RESOLVED job's own identity (B2/non-blocking#3),
		// not the raw req.Slug — a request slug that only resolves via
		// resolveJobForHome's fuzz (e.g. a trailing .job.md) would otherwise
		// write a job_slug no board read ever matches.
		JobID: j.ID, JobSlug: feedbackJobKey(j), Artifact: req.Name, Snapshot: data, SnapshotExt: ext,
		Note: note, Session: j.Session,
	}); err != nil {
		util.LogEvent("web", "error", "feedback-create", "feedback-create "+req.Slug+": "+err.Error())
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "feedback-create", "feedback-create "+req.Slug)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// feedbackEdit UPDATEs the note and re-freezes the snapshot from the
// artifact as CURRENTLY served (Q4) — resolved via the job's present
// state/home, since the row itself carries no home. If the artifact no
// longer resolves (the job's cell was torn down and the artifacts table
// lacks the name, say), or resolves to zero bytes (a mid-truncate read, an
// artifacts row written with empty bytes), this falls back to the row's
// OWN existing snapshot bytes rather than passing nil/empty and silently
// blanking a perfectly good frozen copy — a caller must never lose the
// snapshot just because a later poll couldn't re-read the source cleanly.
// existing is the row lookupOwnedFeedback already fetched and verified
// belongs to req.Slug — this handler never re-fetches by id.
func feedbackEdit(w http.ResponseWriter, st *store.Store, existing *store.Feedback, req feedbackReq) {
	note, err := validFeedbackNote(req.Feedback)
	if err != nil {
		util.LogEvent("web", "error", "feedback-edit", "feedback-edit "+req.Slug+": "+err.Error())
		webJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	snapshot, ext := existing.Snapshot, existing.SnapshotExt
	if j := resolveJobBySlug(existing.JobSlug); j != nil {
		if data, newExt, ok := resolveArtifact(j, currentArtifactHome(j), existing.Artifact); ok && len(data) > 0 {
			snapshot, ext = data, newExt
		}
	}
	if err := st.UpdateFeedbackNote(existing.ID, note, snapshot, ext); err != nil {
		util.LogEvent("web", "error", "feedback-edit", "feedback-edit "+req.Slug+": "+err.Error())
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown feedback"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "feedback-edit", "feedback-edit "+req.Slug)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// feedbackDelete permanently removes a feedback entry (Q9). existing is
// already ownership-verified by lookupOwnedFeedback.
func feedbackDelete(w http.ResponseWriter, st *store.Store, existing *store.Feedback) {
	if err := st.DeleteFeedback(existing.ID); err != nil {
		util.LogEvent("web", "error", "feedback-delete", "feedback-delete "+existing.JobSlug+": "+err.Error())
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown feedback"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "feedback-delete", "feedback-delete "+existing.JobSlug)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// feedbackAddressed is the `kraken feedback addressed` form, which ONLY the
// nightly dream pass runs — an ordinary cell reads its feedback, acts on it,
// and leaves this column empty by design (internal/jobs/crew.go's brief), so
// an empty Addressed means "no durable fix has shipped" rather than "nobody
// handled it". UPDATEs only those two columns — never touches note/
// snapshot/session. req.Addressed is a pointer (see feedbackReq's doc
// comment): an explicitly empty value ({id, addressed:""}) CLEARS the note
// (both addressed and addressed_at reset to ""), rather than 400ing as
// "feedback is empty" the way it used to when this form shared feedbackEdit's
// validation by accident. A NON-empty value is length-capped at
// feedbackNoteMaxLen (in runes, after trim) for exactly the reason the note
// itself is: addressed rides EVERY /api/board poll via ListFeedbackMeta for
// as long as the card is visible, and the writer here is zeus (an LLM), not a
// human typing into a box. existing is already ownership-verified.
func feedbackAddressed(w http.ResponseWriter, st *store.Store, existing *store.Feedback, req feedbackReq) {
	var addressed string
	if req.Addressed != nil {
		addressed = strings.TrimSpace(*req.Addressed)
	}
	if n := len([]rune(addressed)); n > feedbackNoteMaxLen {
		util.LogEvent("web", "error", "feedback-addressed", "feedback-addressed "+req.Slug+": addressed note too long")
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "addressed note too long"})
		return
	}
	addressedAt := ""
	if addressed != "" {
		addressedAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := st.AddressFeedback(existing.ID, addressed, addressedAt); err != nil {
		util.LogEvent("web", "error", "feedback-addressed", "feedback-addressed "+req.Slug+": "+err.Error())
		if errors.Is(err, store.ErrNotFound) {
			webJSON(w, http.StatusNotFound, map[string]string{"error": "unknown feedback"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	util.LogEvent("web", "info", "feedback-addressed", "feedback-addressed "+req.Slug)
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
