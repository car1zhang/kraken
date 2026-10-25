package api

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// mmrun.go — the mind runs' wire (schema v39): read marks (POST /api/mm/mark,
// its shipped-phone alias POST /api/mm/capture, GET /api/mm/marks,
// POST /api/mm/mark/remove), the run record (GET /api/mm/runs,
// GET /api/mm/run) and calibration (POST /api/mm/replay), plus the surviving
// POST /api/mm/confirm. Every judgement lives in internal/jobs and
// internal/store; these handlers decode, call, and pick a status code. The
// POSTs are POST-only and gated with mirrorGated at registration; the GETs
// are open reads.

// mmMarkReq is the mark body, and the capture alias' body: the same three
// keys the retired capture route took, so a shipped client's request decodes
// unchanged.
type mmMarkReq struct {
	Slug   string             `json:"slug"`
	Text   string             `json:"text"`
	Source jobs.CaptureSource `json:"source"`
}

type mmIDReq struct {
	ID int64 `json:"id"`
}

type mmReplayReq struct {
	ID    int64 `json:"id"`
	Write bool  `json:"write"`
}

// mmMarkStatus maps jobs.MarkRead's refusals. A missing cell is never a
// refusal: a mark needs no live cell.
func mmMarkStatus(err error) int {
	switch {
	case errors.Is(err, jobs.ErrCaptureTooLong), errors.Is(err, jobs.ErrCaptureBadSource):
		return http.StatusBadRequest
	case errors.Is(err, jobs.ErrMarkNoJob):
		return http.StatusNotFound
	default:
		return http.StatusInternalServerError
	}
}

// mmMark is the shared half of mark and capture: decode, store the mark, and
// hand back the result, having already answered the refusal when ok is false.
func mmMark(w http.ResponseWriter, r *http.Request, action string) (jobs.MarkResult, bool) {
	if !mmPost(w, r) {
		return jobs.MarkResult{}, false
	}
	var req mmMarkReq
	if !mmDecode(w, r, &req, "slug, text and source") {
		return jobs.MarkResult{}, false
	}
	st := mmStore(w)
	if st == nil {
		return jobs.MarkResult{}, false
	}
	res, err := jobs.MarkRead(st, jobs.MarkRequest{
		Slug: strings.TrimSpace(req.Slug), Text: req.Text, Source: req.Source,
	}, time.Now())
	if err != nil {
		mmErr(w, action, mmMarkStatus(err), err.Error())
		return jobs.MarkResult{}, false
	}
	util.LogEvent("web", "info", "mm-mark", "mm-mark "+strconv.FormatInt(res.ID, 10)+" "+strings.TrimSpace(req.Slug))
	return res, true
}

// mmMarkHandler records one read mark for the next mind run that covers it.
func mmMarkHandler(w http.ResponseWriter, r *http.Request) {
	res, ok := mmMark(w, r, "mm-mark")
	if !ok {
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"id": res.ID, "pending": res.Pending, "folded": res.Folded})
}

// mmCaptureHandler is the mark's alias for shipped phones, whose decoder
// still reads the retired mind-thread answer: `tid` and `created` are always
// present (empty and false, since no thread is ever branched), beside the
// mark's id and the job's pending count.
func mmCaptureHandler(w http.ResponseWriter, r *http.Request) {
	res, ok := mmMark(w, r, "mm-capture")
	if !ok {
		return
	}
	webJSON(w, http.StatusOK, map[string]any{"tid": "", "created": false, "mark_id": res.ID, "pending": res.Pending})
}

// webMMMark is one mm_marks row on the wire. `source` is the parsed pointer,
// always an object (a row whose source_json does not decode serves an empty
// one rather than failing the list).
type webMMMark struct {
	ID          int64              `json:"id"`
	JobID       int64              `json:"job_id"`
	Slug        string             `json:"slug"`
	Text        string             `json:"text"`
	Source      jobs.CaptureSource `json:"source"`
	CreatedAt   int64              `json:"created_at"`
	ConsumedRun int64              `json:"consumed_run"`
	ConsumedAt  int64              `json:"consumed_at"`
}

func webMMMarkFrom(m store.MMMark) webMMMark {
	out := webMMMark{
		ID: m.ID, JobID: m.JobID, Slug: m.Slug, Text: m.Text,
		CreatedAt: m.CreatedAt, ConsumedRun: m.ConsumedRun, ConsumedAt: m.ConsumedAt,
	}
	if s := mmParseSource(m.SourceJSON); s != nil {
		out.Source = *s
	}
	return out
}

// mmSlugJobID resolves an optional `slug` query parameter to a job id (active
// first, else the newest archived row carrying it). An empty slug is 0, "every
// job"; a slug naming no job answers 404 itself and ok is false.
func mmSlugJobID(w http.ResponseWriter, st *store.Store, slug, action string) (int64, bool) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return 0, true
	}
	j := mmJobBySlugAnyHome(st, slug)
	if j == nil {
		mmErr(w, action, http.StatusNotFound, "unknown job "+slug)
		return 0, false
	}
	return j.ID, true
}

// mmTruthy reads a boolean query flag: 1, true, yes (any case).
func mmTruthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// mmMarksHandler lists marks newest first: every mark with no slug, one
// job's with one, pending only with pending=1.
func mmMarksHandler(w http.ResponseWriter, r *http.Request) {
	if !mmGetOnly(w, r) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	q := r.URL.Query()
	jobID, ok := mmSlugJobID(w, st, q.Get("slug"), "mm-marks")
	if !ok {
		return
	}
	marks, err := st.ListMMMarks(jobID, mmTruthy(q.Get("pending")))
	if err != nil {
		mmErr(w, "mm-marks", http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]webMMMark, 0, len(marks))
	for _, m := range marks {
		out = append(out, webMMMarkFrom(m))
	}
	webJSON(w, http.StatusOK, map[string]any{"marks": out})
}

// mmMarkRemoveHandler deletes one pending mark; a consumed one is history
// and refused.
func mmMarkRemoveHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmIDReq
	if !mmDecode(w, r, &req, "id") {
		return
	}
	if req.ID <= 0 {
		mmErr(w, "mm-mark-remove", http.StatusBadRequest, "missing id")
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	if err := st.RemoveMMMark(req.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			mmErr(w, "mm-mark-remove", http.StatusNotFound, "unknown mark")
		case errors.Is(err, store.ErrMMMarkConsumed):
			mmErr(w, "mm-mark-remove", http.StatusConflict, "mark already consumed by a mind run")
		default:
			mmErr(w, "mm-mark-remove", http.StatusInternalServerError, err.Error())
		}
		return
	}
	util.LogEvent("web", "info", "mm-mark-remove", "mm-mark-remove "+strconv.FormatInt(req.ID, 10))
	webJSON(w, http.StatusOK, map[string]any{"id": req.ID})
}

// webMindRun is one mind_runs row on the wire: every store.MindRun field in
// snake_case except the report (served by GET /api/mm/run?blob=report, so a
// runs poll stays small), plus has_bundle.
type webMindRun struct {
	ID          int64  `json:"id"`
	JobID       int64  `json:"job_id"`
	Slug        string `json:"slug"`
	Trigger     string `json:"trigger"`
	Since       int64  `json:"since"`
	Until       int64  `json:"until"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"created_at"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
	Note        string `json:"note"`
	PromptSHA   string `json:"prompt_sha"`
	Model       string `json:"model"`
	InTokens    int64  `json:"in_tokens"`
	OutTokens   int64  `json:"out_tokens"`
	BundleBytes int64  `json:"bundle_bytes"`
	Added       int    `json:"added"`
	Superseded  int    `json:"superseded"`
	Retired     int    `json:"retired"`
	Flagged     int    `json:"flagged"`
	Excluded    int    `json:"excluded"`
	Folded      int    `json:"folded"`
	Suppressed  int    `json:"suppressed"`
	Refused     int    `json:"refused"`
	SourceRun   int64  `json:"source_run"`
	// HasBundle is true when the run's blob row exists and its bundle has
	// not been pruned — i.e. when a replay or a bundle read can succeed.
	HasBundle bool `json:"has_bundle"`
}

func webMindRunFrom(run store.MindRun, hasBundle bool) webMindRun {
	return webMindRun{
		ID: run.ID, JobID: run.JobID, Slug: run.Slug, Trigger: run.Trigger,
		Since: run.Since, Until: run.Until, State: run.State,
		CreatedAt: run.CreatedAt, StartedAt: run.StartedAt, FinishedAt: run.FinishedAt,
		Note: run.Note, PromptSHA: run.PromptSHA, Model: run.Model,
		InTokens: run.InTokens, OutTokens: run.OutTokens, BundleBytes: run.BundleBytes,
		Added: run.Added, Superseded: run.Superseded, Retired: run.Retired,
		Flagged: run.Flagged, Excluded: run.Excluded, Folded: run.Folded,
		Suppressed: run.Suppressed, Refused: run.Refused, SourceRun: run.SourceRun,
		HasBundle: hasBundle,
	}
}

// mmRunHasBundle reports whether runID's bundle is still stored, without
// reading the bytes (the runs list is a poll). Any read failure reads as
// false: the flag decorates a row, it never fails the list.
func mmRunHasBundle(st *store.Store, runID int64) bool {
	ok, err := st.HasMindRunBundle(runID)
	return err == nil && ok
}

// mmRunsMaxN caps GET /api/mm/runs' n: a larger n reads as this.
const mmRunsMaxN = 500

// mmRunsHandler lists runs newest first, never a blob.
func mmRunsHandler(w http.ResponseWriter, r *http.Request) {
	if !mmGetOnly(w, r) {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	q := r.URL.Query()
	n := 0
	if raw := strings.TrimSpace(q.Get("n")); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			mmErr(w, "mm-runs", http.StatusBadRequest, "bad n — want a whole number")
			return
		}
		n = min(v, mmRunsMaxN)
	}
	jobID, ok := mmSlugJobID(w, st, q.Get("slug"), "mm-runs")
	if !ok {
		return
	}
	runs, err := st.ListMindRuns(jobID, n)
	if err != nil {
		mmErr(w, "mm-runs", http.StatusInternalServerError, err.Error())
		return
	}
	out := make([]webMindRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, webMindRunFrom(run, mmRunHasBundle(st, run.ID)))
	}
	webJSON(w, http.StatusOK, map[string]any{"runs": out})
}

// mmRunHandler serves one run and, on request, one of its texts: the report
// (off the row), the manifest or the raw answer (off the blob row, "" when
// the run never wrote one), or the bundle gunzipped (404 when pruned or never
// stored). No blob parameter serves the run alone with an empty blob.
func mmRunHandler(w http.ResponseWriter, r *http.Request) {
	if !mmGetOnly(w, r) {
		return
	}
	blobName := strings.TrimSpace(r.URL.Query().Get("blob"))
	switch blobName {
	case "", "report", "manifest", "answer", "bundle":
	default:
		mmErr(w, "mm-run", http.StatusBadRequest, "bad blob — want report, manifest, answer or bundle")
		return
	}
	id, ok := mmQueryID(w, r)
	if !ok {
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	run, err := st.GetMindRun(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			mmErr(w, "mm-run", http.StatusNotFound, "unknown mind run")
			return
		}
		mmErr(w, "mm-run", http.StatusInternalServerError, err.Error())
		return
	}
	blobs, berr := st.GetMindRunBlobs(id)
	haveBlobs := berr == nil
	if berr != nil && !errors.Is(berr, store.ErrNotFound) {
		mmErr(w, "mm-run", http.StatusInternalServerError, berr.Error())
		return
	}
	hasBundle := haveBlobs && blobs.PrunedAt == 0 && blobs.BundleGz != nil
	var text string
	switch blobName {
	case "report":
		text = run.Report
	case "manifest":
		text = blobs.Manifest
	case "answer":
		text = blobs.Answer
	case "bundle":
		if haveBlobs && blobs.PrunedAt > 0 {
			mmErr(w, "mm-run", http.StatusNotFound, "bundle pruned")
			return
		}
		if !hasBundle {
			mmErr(w, "mm-run", http.StatusNotFound, "no bundle stored for this run")
			return
		}
		raw, gerr := mmGunzip(blobs.BundleGz)
		if gerr != nil {
			mmErr(w, "mm-run", http.StatusInternalServerError, "bundle unreadable: "+gerr.Error())
			return
		}
		text = string(raw)
	}
	webJSON(w, http.StatusOK, map[string]any{"run": webMindRunFrom(run, hasBundle), "blob": text})
}

// mmGunzip inflates a stored bundle.
func mmGunzip(gz []byte) ([]byte, error) {
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// webMindReplayDiff and webMindReplay pin replay's wire keys here rather than
// leaning on whatever tags the jobs types carry, and turn a nil slice into
// [] so a client never has to tell null from empty.
type webMindReplayDiff struct {
	OpsAdded          []jobs.MindOp        `json:"ops_added"`
	OpsLost           []jobs.MindOp        `json:"ops_lost"`
	ExclusionsChanged []jobs.MindExclusion `json:"exclusions_changed"`
}

type webMindReplay struct {
	SourceRun    int64             `json:"source_run"`
	OldPromptSHA string            `json:"old_prompt_sha"`
	NewPromptSHA string            `json:"new_prompt_sha"`
	Old          jobs.MindAnswer   `json:"old"`
	New          jobs.MindAnswer   `json:"new"`
	Diff         webMindReplayDiff `json:"diff"`
	NewRun       int64             `json:"new_run"`
}

func webMindReplayFrom(rp jobs.MindReplay) webMindReplay {
	d := webMindReplayDiff{
		OpsAdded: rp.Diff.OpsAdded, OpsLost: rp.Diff.OpsLost,
		ExclusionsChanged: rp.Diff.ExclusionsChanged,
	}
	if d.OpsAdded == nil {
		d.OpsAdded = []jobs.MindOp{}
	}
	if d.OpsLost == nil {
		d.OpsLost = []jobs.MindOp{}
	}
	if d.ExclusionsChanged == nil {
		d.ExclusionsChanged = []jobs.MindExclusion{}
	}
	return webMindReplay{
		SourceRun: rp.SourceRun, OldPromptSHA: rp.OldPromptSHA, NewPromptSHA: rp.NewPromptSHA,
		Old: rp.Old, New: rp.New, Diff: d, NewRun: rp.NewRun,
	}
}

// mmReplayHandler judges a recorded bundle again under the current skill.
// It runs the model synchronously, so a request can take as long as
// jobs.MindRunTimeout; write:false applies nothing and creates no row.
func mmReplayHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmReplayReq
	if !mmDecode(w, r, &req, "id and write") {
		return
	}
	if req.ID <= 0 {
		mmErr(w, "mm-replay", http.StatusBadRequest, "missing id")
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	rp, err := jobs.ReplayMindRun(st, req.ID, req.Write, time.Now())
	if err != nil {
		switch {
		case errors.Is(err, jobs.ErrMindBundlePruned):
			mmErr(w, "mm-replay", http.StatusConflict, err.Error())
		case errors.Is(err, store.ErrNotFound):
			// an unknown run, or a run that never stored a bundle: jobs'
			// own sentence says which
			mmErr(w, "mm-replay", http.StatusNotFound, err.Error())
		default:
			mmErr(w, "mm-replay", http.StatusInternalServerError, err.Error())
		}
		return
	}
	util.LogEvent("web", "info", "mm-replay", "mm-replay "+strconv.FormatInt(req.ID, 10)+" write="+strconv.FormatBool(req.Write))
	webJSON(w, http.StatusOK, webMindReplayFrom(rp))
}

// mmConfirmHandler flips a proposed item live: the operator's yes on the
// Model pane from before mind runs wrote live rows. Kept for old clients; on
// a live row it is a 200 no-op.
func mmConfirmHandler(w http.ResponseWriter, r *http.Request) {
	if !mmPost(w, r) {
		return
	}
	var req mmIDReq
	if !mmDecode(w, r, &req, "id") {
		return
	}
	if req.ID <= 0 {
		mmErr(w, "mm-confirm", http.StatusBadRequest, "missing id")
		return
	}
	st := mmStore(w)
	if st == nil {
		return
	}
	if err := st.ConfirmMMItem(req.ID); err != nil {
		switch {
		case errors.Is(err, store.ErrNotFound):
			mmErr(w, "mm-confirm", http.StatusNotFound, "unknown mental-model item")
		case errors.Is(err, store.ErrMMItemInvalidated):
			mmErr(w, "mm-confirm", http.StatusConflict, "mm item is invalidated")
		default:
			mmErr(w, "mm-confirm", http.StatusInternalServerError, err.Error())
		}
		return
	}
	util.LogEvent("web", "info", "mm-confirm", "mm-confirm "+strconv.FormatInt(req.ID, 10))
	webJSON(w, http.StatusOK, map[string]any{"ok": true, "id": req.ID, "state": "live"})
}
