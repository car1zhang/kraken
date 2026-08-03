package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"kraken/internal/core"
	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// jobdraft.go — the two routes iris uses to see and to write jobs:
// GET /api/job/list (what jobs exist right now) and POST /api/job/draft (the
// guarded write). /api/job/raw is reused untouched for reading one job's
// text, so there is no third route here.
//
// /api/job/draft is the SAFETY SURFACE of the whole Slack intake feature.
// Iris is a claude reading arbitrary text off Slack and turning it into
// kraken jobs; the one property that makes that safe is that it can only ever
// produce a DRAFT — never a queued job, never a started one, never an edit to
// a job whose cell is live. That property is enforced HERE, server-side, and
// not merely stated in iris' brief: a brief is a request, and this is the
// thing that cannot be talked out of it. Every refusal below writes nothing
// at all.
//
// It is deliberately NOT a mode of /api/job/save. The raw editor's save is
// carl at a keyboard: it salvages an unparseable draft (body + err), it
// writes the whole field set including `ready` and `state`, and its
// editableJob gate lets a DONE job through for a respec. None of those are
// safe for an agent, and each of them is a row in the refusal matrix below.
// The two routes therefore share the plumbing (webJobFor, ParseJob,
// saveJobEditWithLock, jobFieldsFromParsed's narrower cousin) and nothing
// else.
//
// Both routes are additive paths — no existing shape is touched — so
// `apiContract` stays 5.

// jobListSummaryMax caps a summary line. Iris reads this list to decide "is there
// already a job about this?", which one line per job answers; a job whose
// whole spec rode along would push the real answer out of its context.
const jobListSummaryMax = 120

// webJobListItem is one active job on the wire: the identity iris addresses
// it by, the state that decides whether it may be edited at all, and one
// line of what it is about. Nothing else — repo, prereqs, PRs, tokens and
// the rest are the board's business, and this list is polled by an agent
// with a context window rather than rendered on a screen.
//
// No omitempty anywhere: an empty summary (a job with an empty body) is a
// real answer and must still ride, and a client reads all three keys
// unconditionally.
type webJobListItem struct {
	Slug    string `json:"slug"`
	State   string `json:"state"`
	Summary string `json:"summary"`
}

// jobDraftReq is POST /api/job/draft's body: the whole job text, exactly as
// a job file would hold it, plus the optimistic-lock token.
//
// Content is the WHOLE text rather than a field set for the same reason the
// raw editor takes whole text: the frontmatter is what a reviewer reads, and
// a route that accepted `{repo, base, body}` would let iris write a job whose
// rendered text nobody ever saw. It is parsed with the strict parser here, so
// what is stored is exactly what those bytes mean.
//
// MTime is mtimeToken (number, or a legacy string) and OPTIONAL: 0 or absent
// skips the conflict check, matching the raw save's contract. Iris usually
// has one — it just read the job through /api/job/raw — and sending it is
// what keeps an iris edit and a carl edit from silently clobbering each
// other.
type jobDraftReq struct {
	Slug    string     `json:"slug"`
	Content string     `json:"content"`
	MTime   mtimeToken `json:"mtime"`
}

// jobListHandler serves GET /api/job/list — every ACTIVE job (archived ones
// are out of LoadJobs' namespace entirely, and iris has no business
// resurrecting one) with its display state and a one-line summary.
//
// The state served is jobs.DisplayState, the same string the board renders,
// rather than the stored `state` column: iris uses it to decide whether a
// job is editable at all, and "draft" for a ready-but-unstarted job would be
// a lie in exactly the case that matters.
//
// A read, so it stays reachable under --mirror.
func jobListHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "GET only"})
		return
	}
	// Guarded like its four siblings even though LoadJobs takes no store
	// handle: with none wired it falls through to the FILE-mode reader, which
	// on a store-only machine (Q14) answers an empty pluto dir with an empty
	// list — "there are no jobs" is then indistinguishable from "the store is
	// not wired", and iris would draft a duplicate of every job that exists.
	if _, ok := graphStore(w); !ok {
		return
	}
	js := jobs.LoadJobs()
	bySlug := make(map[string]*jobs.Job, len(js))
	for _, j := range js {
		bySlug[j.Slug] = j
	}
	out := make([]webJobListItem, 0, len(js))
	for _, j := range js {
		out = append(out, webJobListItem{
			Slug:    j.Slug,
			State:   jobs.DisplayState(j, bySlug),
			Summary: jobSummary(j.Body),
		})
	}
	webJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// jobSummary is a job body's first non-empty line, one-lined and capped at
// jobListSummaryMax RUNES (core.TruncateOneLine counts runes, so a
// multi-byte spec is not penalized).
//
// The FIRST non-empty line rather than the first N runes of the whole body:
// a spec's opening line is written as its own statement of what the job is,
// and flattening the body would splice the second line onto it mid-sentence.
// A leading markdown heading marker is dropped for the same reason — "# csv
// export times out" and "csv export times out" are the same summary, and the
// hash is formatting rather than content.
func jobSummary(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "#")
		trimmed = strings.TrimSpace(trimmed)
		if trimmed == "" {
			// a bare "###" rule line is formatting, not a summary — keep
			// looking rather than serving an empty string as the answer
			continue
		}
		return core.TruncateOneLine(trimmed, jobListSummaryMax)
	}
	return ""
}

// jobDraftHandler serves POST /api/job/draft. Its whole shape is a refusal
// matrix — every guard runs BEFORE any write, and a refused request changes
// nothing at all, so a caller that gets a 400 can retry the fixed body
// without first working out how much of the previous one landed.
//
// Two paths, chosen by whether an active job already holds the slug:
//
//	CREATE — no such job. Refuses ready:true, any non-empty state:, and an
//	empty body; then creates the row from the parsed fields (the
//	CreateDreamJob text→ParseJob→CreateJob shape) plus its prereqs.
//
//	EDIT — the job exists. Refuses anything past draft/queued (deliberately
//	stricter than editableJob, which lets a done job through), refuses any
//	change to ready or state relative to the stored row, holds the mtime
//	lock, and writes only {repo, base, solo, model, body}, so every
//	kraken-owned field — and the stored prereqs, which are carl's release
//	gate — survives untouched.
//
// The route is mirror-gated at registration: it is a write, and a mirror
// board is read-only, full stop.
func jobDraftHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		webJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "POST only"})
		return
	}
	var req jobDraftReq
	// Body cap like every other POST in this package. Unknown fields are NOT
	// rejected here, unlike /api/slack/ack: this route's body is a document
	// plus a lock token, and the shapes that hurt it are all inside the
	// CONTENT, where the strict job parser is the gate — a spare key at the
	// envelope level cannot make a draft do anything.
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(&req); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			webJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
			return
		}
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}

	// Slugify first: the slug is the job's identity, and an agent-supplied
	// one that slugifies to nothing ("???", "   ") would otherwise create a
	// row nothing can address again.
	slug := util.Slugify(req.Slug)
	if slug == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must contain letters or digits"})
		return
	}
	if strings.TrimSpace(req.Content) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "content is empty — send the whole job text (frontmatter + spec), the same bytes `kraken job raw " + slug + "` prints",
		})
		return
	}

	// The strict parser is the gate for everything inside the document. A
	// parse error is a 400 CARRYING THE PARSER'S OWN TEXT and nothing is
	// written — deliberately unlike the raw editor's save, which salvages an
	// unparseable draft as body+err because a human mid-thought is entitled
	// to a broken file. An agent is not: a half-written job pinned to draft
	// by an err column is a job carl has to go fix by hand.
	parsed := jobs.ParseJob(slug+jobs.JobSuffix, []byte(req.Content))
	if parsed.Err != "" {
		webJSON(w, http.StatusBadRequest, map[string]string{"error": "job text does not parse: " + parsed.Err})
		return
	}

	st, ok := graphStore(w)
	if !ok {
		return
	}
	// webJobFor is the lookup every other job-addressing route in this package
	// uses. Its error means only "no active job with this slug", which
	// elsewhere is a 404 and here is simply the CREATE path — this is the one
	// route where a job not existing yet is the normal case.
	if j, _, err := webJobFor(slug); err == nil {
		jobDraftEdit(w, st, j, parsed, int64(req.MTime))
		return
	}
	jobDraftCreate(w, st, slug, parsed)
}

// jobDraftCreate is the no-such-job path: three refusals, then the row.
//
// The row is built from the PARSED fields rather than from the request, and
// its slug is the request's slugified one rather than parsed.Slug — a `job:`
// key in the frontmatter names the file's own identity to the parser, and
// honouring it here would let a draft addressed to one slug land on another.
func jobDraftCreate(w http.ResponseWriter, st *store.Store, slug string, parsed *jobs.Job) {
	// The safety property, stated once and enforced here: iris drafts. A
	// ready job is one the daemon's queue pass will START, unattended, from
	// text that arrived over Slack — so `ready: true` is refused rather than
	// quietly rewritten to false, because a silent rewrite would let iris
	// believe it queued something and report that to carl.
	if parsed.Ready {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "ready: true is refused on this route — drafts stay ready: false; queueing a job is carl's move on the board",
		})
		return
	}
	// `state` is kraken-owned: it is how the daemon records that a cell is
	// working or that a job is done. A job created with one already set would
	// be lying about a lifecycle that never happened.
	if parsed.State != "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "state: " + parsed.State + " is refused on this route — state is kraken's, written when a cell actually starts or finishes",
		})
		return
	}
	// startable() does not check for a body — only the board's queue action
	// does — so an empty one would eventually start a cell with an empty
	// prompt. The same belt-and-braces CreateDreamJob wears.
	if strings.TrimSpace(parsed.Body) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "job body is empty — a draft with no spec is a job nobody can start",
		})
		return
	}

	// Color is deliberately not carried: it is kraken-owned and cosmetic,
	// set from the board through its own one-field route, and CreateDreamJob
	// (the other programmatic creator) omits it for the same reason.
	if _, err := st.CreateJob(&store.Job{
		Slug: slug, Repo: parsed.Repo, Base: parsed.Base,
		Ready: parsed.Ready, Solo: parsed.Solo, Model: parsed.Model,
		Body: parsed.Body,
	}); err != nil {
		if errors.Is(err, store.ErrSlugTaken) {
			// lost a race with another writer between the lookup above and
			// this insert: a conflict about the current state of the world,
			// not a malformed request
			webJSON(w, http.StatusConflict, map[string]string{"error": "slug " + slug + " is already taken by another active job"})
			return
		}
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	created, _, err := webJobFor(slug)
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": "job " + slug + " WAS created but could not be reloaded: " + err.Error()})
		return
	}
	// The row is already committed by here, so this 500 must not read as "the
	// request failed". It did not: the job exists, only its prereqs are
	// missing — and the caller's retry will therefore take the EDIT path
	// against that half-built row, where prereqs are deliberately never
	// synced. Saying so is the difference between a caller that fixes the
	// prereqs by hand (or on the board) and one that retries forever
	// believing nothing landed.
	if err := st.ReplacePrereqs(created.ID, storePrereqs(parsed.Prereqs)); err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{
			"error": "job " + slug + " WAS created, but writing its prereqs failed: " + err.Error() +
				" — do not retry as a create; the job exists with no prereqs, and this route never sets prereqs on an edit",
		})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": slug, "mtime": jobEditMTime(created)})
}

// jobDraftEdit is the job-exists path. Every guard here is about a field
// iris may not move, or a job it may not touch at all — and past the guards,
// the write itself is narrower still: five spec fields, no prereqs (see the
// saveJobEditWithLock call below).
func jobDraftEdit(w http.ResponseWriter, st *store.Store, j *jobs.Job, parsed *jobs.Job, mtime int64) {
	// Started() is exactly {working, blocked, done} — deliberately stricter
	// than editableJob, which also lets a DONE job through for carl to
	// respec. A live cell's job text belongs to the crew, and a done job's
	// text is the record of what shipped; neither is iris' to rewrite from a
	// Slack message. Iris says so in its ack disposition instead.
	if j.Started() {
		webJSON(w, http.StatusConflict, map[string]string{
			"error": j.Slug + " is " + j.State + " — this route edits draft and queued jobs only; say so in the ack disposition instead",
		})
		return
	}
	// ready and state are refused as CHANGES rather than ignored. Ignoring
	// them is the failure mode that matters: iris would send `ready: true`,
	// get a 200, and report to carl that it queued the job — while the row
	// sat in draft forever. So the submitted text must agree with the stored
	// row, loudly, and moving the flag stays a board action.
	if parsed.Ready != j.Ready {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "ready may not change on this route (stored " + boolWord(j.Ready) + ", submitted " + boolWord(parsed.Ready) + ") — queueing and dequeueing are carl's move on the board; resend the text with the stored value",
		})
		return
	}
	if parsed.State != "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "state: " + parsed.State + " is refused on this route — state is kraken's; leave the key out",
		})
		return
	}
	// Reached only on a clean parse (jobDraftHandler refused otherwise), so
	// this is an author who parsed fine and still sent nothing: an edit that
	// empties a job's spec is indistinguishable from one that lost it.
	if strings.TrimSpace(parsed.Body) == "" {
		webJSON(w, http.StatusBadRequest, map[string]string{
			"error": "job body is empty — an edit that removes the whole spec is refused; send the full text, not just frontmatter",
		})
		return
	}

	// The narrow field set is the point of this route. jobFieldsFromParsed
	// (the raw save's map) writes slug, ready, color, state, outcome, session,
	// teardown, run and err as well — every one of them kraken-owned or
	// identity, and every one of them a field an agent-written full replace
	// would silently reset. Only the five below are the spec's own.
	//
	// `err` is absent because the frozen field set for this route is those
	// five and no more. One consequence is worth writing down rather than
	// discovering: a job whose row already carries an `err` (a HUMAN's
	// unparseable raw save — this route never writes one, it refuses instead)
	// keeps it after a clean edit here, so it stays pinned to draft with a
	// stale parse error until the raw editor saves over it. That is the
	// contract's call, not an oversight, and the failure is conservative —
	// pinned to draft is the safe direction.
	fields := map[string]any{
		"repo": parsed.Repo, "base": parsed.Base,
		"solo": parsed.Solo, "model": parsed.Model,
		"body": parsed.Body,
	}
	// force=false always: a conflict is surfaced, never overridden. The
	// override exists for carl looking at a diff in the editor, and iris is
	// not looking at anything. PRs are never synced (syncPRs=false) — they
	// are the daemon's record of what shipped.
	//
	// PREREQS are not synced either (syncPrereqs=false), and that is the one
	// place this route differs from every other editor save rather than
	// merely being narrower than one. A prereq — especially a GATED prereq —
	// is the board's human-release gate: it is what parks a ready job until
	// carl lets it go. An iris edit is a whole-text replace, so a submitted
	// body that simply omits the `prereqs:` line would clear that gate and
	// make a queued job auto-startable on the daemon's very next pass. Iris
	// starting a job is the single outcome this whole route exists to
	// prevent, and clearing its gate is that outcome by another route. So an
	// edit changes the spec and leaves the gates exactly as carl left them;
	// prereqs are still SET on the create path, where there is no stored gate
	// to erase.
	newRev, conflict, err := saveJobEditWithLock(st, j, mtime, false,
		fields, nil, false, nil, false)
	if conflict {
		webJSON(w, http.StatusConflict, map[string]string{
			"error": j.Slug + " changed since you read it — re-read it with `kraken job raw " + j.Slug + "` and reapply your edit on top",
		})
		return
	}
	if err != nil {
		webJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	webBoardCache.invalidate()
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "slug": j.Slug, "mtime": newRev})
}

// boolWord renders a bool the way the job file spells it, so a refusal about
// `ready` quotes the value the caller actually typed rather than Go's %v of
// it (identical today — this exists so the refusal text keeps matching the
// file format if either ever diverges).
func boolWord(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
