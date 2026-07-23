package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"kraken/jobs"
	"kraken/store"
)

// jobedit_storemode_test.go — store-backed coverage for api/jobedit.go's
// dual-mode endpoints: raw round-trip, rev-based 409, and job/create. The
// file-mode behavior these mirror is already covered by jobedit_test.go and
// contract_test.go; useTestStore (storemode_test.go) wires jobs onto a temp
// DB for the duration of each test.

func mustCreateStoreJob(t *testing.T, st *store.Store, j store.Job) *jobs.Job {
	t.Helper()
	if _, err := st.CreateJob(&j); err != nil {
		t.Fatalf("create job %s: %v", j.Slug, err)
	}
	got := jobs.JobBySlug(jobs.LoadJobs(), j.Slug)
	if got == nil {
		t.Fatalf("job %s not found after create", j.Slug)
	}
	return got
}

func TestWebJobRawStoreModeRoundTrip(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{
		Slug: "raw-store", Type: "build", Repo: "monolith", Body: "\nsome spec text\n",
	})

	req := httptest.NewRequest(http.MethodGet, "/api/job/raw?slug=raw-store", nil)
	rec := httptest.NewRecorder()
	webJobRaw(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobRaw: status %d, body %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	mtime, ok := raw["mtime"].(float64)
	if !ok || mtime != 1 {
		t.Fatalf("mtime = %#v, want the initial rev (1)", raw["mtime"])
	}
	content, _ := raw["content"].(string)
	if content == "" {
		t.Fatal("empty raw content")
	}

	// round-trip: parsing the composed content must reproduce the same
	// fields (composeRawJobContent reuses serializeJobFile, so this should
	// parse cleanly with no Err).
	parsed := jobs.ParseJob("raw-store.job.md", []byte(content))
	if parsed.Err != "" {
		t.Fatalf("composed raw content doesn't parse: %s\ncontent:\n%s", parsed.Err, content)
	}
	if parsed.Repo != "monolith" || parsed.Type != "build" {
		t.Fatalf("round-tripped fields: %+v", parsed)
	}

	// edit it back through webJobSave with a modified spec.
	newContent := "---\njob: raw-store\ntype: build\nrepo: other-repo\nready: false\nprereqs: []\nsolo: false\n---\n\nedited spec\n"
	saveBody := mustJSON(t, map[string]any{"slug": "raw-store", "content": newContent, "mtime": mtime})
	saveReq := httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(saveBody))
	saveRec := httptest.NewRecorder()
	webJobSave(saveRec, saveReq)
	if saveRec.Code != http.StatusOK {
		t.Fatalf("webJobSave: status %d, body %s", saveRec.Code, saveRec.Body.String())
	}
	var saveResp map[string]any
	if err := json.Unmarshal(saveRec.Body.Bytes(), &saveResp); err != nil {
		t.Fatal(err)
	}
	if rev, _ := saveResp["mtime"].(float64); rev != 2 {
		t.Fatalf("mtime after save = %#v, want rev 2", saveResp["mtime"])
	}

	reloaded := jobs.JobBySlug(jobs.LoadJobs(), "raw-store")
	if reloaded == nil {
		t.Fatal("job missing after save")
	}
	if reloaded.Repo != "other-repo" || reloaded.Ready {
		t.Fatalf("save did not apply parsed fields: %+v", reloaded)
	}
	spec, _ := splitBodyLog(reloaded.Body)
	if got := spec; got == "" {
		t.Fatal("edited spec text lost")
	}
}

// TestWebJobSaveStoreModeUnparseablePreservesFields covers raw
// content that fails ParseJob must still save (unparseable drafts happen —
// the editor doesn't block on it), but only the body column changes.
// Frontmatter columns, prereqs, and PRs must survive exactly as they were,
// same contract as file mode (the raw bytes just sit on disk regardless of
// whether they parse back) — jobFieldsFromParsed(parsed) on a failed parse
// carries only ParseJob's best-effort defaults and must never be applied.
func TestWebJobSaveStoreModeUnparseablePreservesFields(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{
		Slug: "garbage-store", Type: "build", Repo: "monolith", State: "done", Session: "sess-abc",
	})
	if err := st.UpsertPR(j.ID, "https://github.com/x/y/pull/1", "open"); err != nil {
		t.Fatal(err)
	}
	if err := st.ReplacePrereqs(j.ID, []store.Prereq{{Slug: "dep-job", Gated: true}}); err != nil {
		t.Fatal(err)
	}
	// L2: UpsertPR just bumped rev past j.Rev's stale load-time snapshot —
	// refetch so this save targets the row's current rev, same as a real
	// editor reloading before saving.
	fresh, err := st.GetJobByID(j.ID)
	if err != nil {
		t.Fatal(err)
	}

	garbage := "not even frontmatter, just plain text"
	body := mustJSON(t, map[string]any{"slug": "garbage-store", "content": garbage, "mtime": fresh.Rev})
	req := httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if warn, _ := resp["warn"].(string); warn == "" {
		t.Fatal("want a non-empty parse warning for unparseable content")
	}

	reloaded := jobs.JobBySlug(jobs.LoadJobs(), "garbage-store")
	if reloaded == nil {
		t.Fatal("job missing after unparseable save")
	}
	if reloaded.Type != "build" || reloaded.Repo != "monolith" || reloaded.State != "done" || reloaded.Session != "sess-abc" {
		t.Fatalf("frontmatter fields clobbered by an unparseable save: %+v", reloaded)
	}
	if reloaded.Body != garbage {
		t.Fatalf("body not updated: got %q, want %q", reloaded.Body, garbage)
	}
	if len(reloaded.PRs) != 1 || reloaded.PRs[0].URL != "https://github.com/x/y/pull/1" {
		t.Fatalf("prs clobbered by an unparseable save: %+v", reloaded.PRs)
	}
	if len(reloaded.Prereqs) != 1 || reloaded.Prereqs[0].Job != "dep-job" {
		t.Fatalf("prereqs clobbered by an unparseable save: %+v", reloaded.Prereqs)
	}
}

// TestWebJobSaveStoreModeGarbageContentPinsToDraft covers L6: a store-mode
// raw save whose content fails to parse must persist that failure into the
// row's new err column (mirroring file mode's ParseJob-derived Job.Err),
// so the job pins to "draft" — not startable — even though it's otherwise
// Ready, exactly like an unparseable job FILE already does. A later save
// that parses clean must clear it.
func TestWebJobSaveStoreModeGarbageContentPinsToDraft(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{
		Slug: "garbage-pin", Type: "build", Repo: "monolith", Ready: true,
	})

	garbage := "not even frontmatter, just plain text"
	body := mustJSON(t, map[string]any{"slug": "garbage-pin", "content": garbage, "mtime": 1})
	req := httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	rev1, _ := resp["mtime"].(float64)

	reloaded := jobs.JobBySlug(jobs.LoadJobs(), "garbage-pin")
	if reloaded == nil {
		t.Fatal("job missing after garbage save")
	}
	if reloaded.Err == "" {
		t.Fatal("want a non-empty Err persisted on the row after a garbage save")
	}
	if got := jobs.DisplayState(reloaded, nil); got != "draft" {
		t.Fatalf("DisplayState after garbage save = %q, want draft (not startable)", got)
	}

	// a later save with content that parses clean must clear err.
	clean := "---\njob: garbage-pin\ntype: build\nrepo: monolith\nready: true\nprereqs: []\nsolo: false\n---\n\nfixed\n"
	body2 := mustJSON(t, map[string]any{"slug": "garbage-pin", "content": clean, "mtime": rev1})
	req2 := httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body2))
	rec2 := httptest.NewRecorder()
	webJobSave(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("clean save: status %d, body %s", rec2.Code, rec2.Body.String())
	}
	fixed := jobs.JobBySlug(jobs.LoadJobs(), "garbage-pin")
	if fixed == nil || fixed.Err != "" {
		t.Fatalf("want err cleared after a clean save, got %+v", fixed)
	}
	if got := jobs.DisplayState(fixed, nil); got != "queued" {
		t.Fatalf("DisplayState after clean save = %q, want queued", got)
	}
}

func TestWebJobSaveStoreModeStaleRevConflict(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "stale-store", Type: "build", Repo: "monolith"})

	content := "---\njob: stale-store\ntype: build\nrepo: monolith\nready: false\nprereqs: []\nsolo: false\n---\n\nx\n"
	body := mustJSON(t, map[string]any{"slug": "stale-store", "content": content, "mtime": 999})
	req := httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["error"] == "" {
		t.Fatal("want a non-empty error message")
	}
}

// TestWebJobSaveFormStoreModeStaleAfterPRAttach covers L2's interleaving
// case: an editor loads a job's mtime, a PR attaches in the background
// (bumping rev), and the editor's save against the now-stale mtime must be
// refused (409) rather than silently applied — and the PR attach itself
// must survive the refused save untouched.
func TestWebJobSaveFormStoreModeStaleAfterPRAttach(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{Slug: "pr-interleave", Type: "build", Repo: "monolith"})
	loadedMTime := j.Rev // the editor's "opened at rev 1" snapshot

	if err := st.UpsertPR(j.ID, "https://github.com/carl/kraken/pull/1", "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}

	body := mustJSON(t, jobFormFields{Slug: "pr-interleave", Type: "build", Repo: "other", MTime: mtimeToken(loadedMTime)})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("save against pre-PR-attach mtime: status %d, want 409, body %s", rec.Code, rec.Body.String())
	}

	reloaded := jobs.JobBySlug(jobs.LoadJobs(), "pr-interleave")
	if reloaded == nil {
		t.Fatal("job missing")
	}
	if reloaded.Repo != "monolith" {
		t.Fatalf("refused save must not have applied: repo = %q", reloaded.Repo)
	}
	if len(reloaded.PRs) != 1 || reloaded.PRs[0].URL != "https://github.com/carl/kraken/pull/1" {
		t.Fatalf("PR attach did not survive the refused save: %+v", reloaded.PRs)
	}
}

func TestWebJobSaveFormStoreModeRevAndForce(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "form-store", Type: "build", Repo: "monolith"})

	// a stale mtime refuses...
	body := mustJSON(t, jobFormFields{Slug: "form-store", Type: "build", Repo: "other", MTime: 999})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale mtime: status %d, want 409, body %s", rec.Code, rec.Body.String())
	}

	// ...but Force overrides it.
	body = mustJSON(t, jobFormFields{Slug: "form-store", Type: "build", Repo: "other", MTime: 999, Force: true})
	req = httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("forced save: status %d, body %s", rec.Code, rec.Body.String())
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "form-store")
	if j == nil || j.Repo != "other" {
		t.Fatalf("forced save did not apply: %+v", j)
	}
}

func TestWebJobCreateStoreMode(t *testing.T) {
	useTestStore(t)

	body := mustJSON(t, webReq{Slug: "brand-new"})
	req := httptest.NewRequest(http.MethodPost, "/api/job/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobCreate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	j := jobs.JobBySlug(jobs.LoadJobs(), "brand-new")
	if j == nil {
		t.Fatal("job not created in store")
	}
	if j.Type != "build" || j.Repo != "monolith" {
		t.Fatalf("defaults not applied: %+v", j)
	}

	// creating the same slug again is a 409, same as file mode.
	rec2 := httptest.NewRecorder()
	webJobCreate(rec2, httptest.NewRequest(http.MethodPost, "/api/job/create", bytes.NewReader(body)))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second create: status %d, want 409", rec2.Code)
	}
}

// TestWebJobCreateStoreModeRowMatchesParsedTemplate covers the L-nit: the
// store row webJobCreate creates must be exactly what parsing
// jobs.JobTemplate(slug) produces — the SAME source the file-mode path
// writes verbatim to disk — not a separately hand-typed literal that could
// drift from the template's own defaults. A job created then abandoned
// (never actually saved) must leave the row in the identical state a
// file-mode create-then-abandon would leave the file in.
func TestWebJobCreateStoreModeRowMatchesParsedTemplate(t *testing.T) {
	useTestStore(t)

	body := mustJSON(t, webReq{Slug: "template-derived"})
	req := httptest.NewRequest(http.MethodPost, "/api/job/create", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobCreate(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	want := jobs.ParseJob("template-derived"+jobs.JobSuffix, []byte(jobs.JobTemplate("template-derived")))
	if want.Err != "" {
		t.Fatalf("JobTemplate itself failed to parse: %s", want.Err)
	}

	j := jobs.JobBySlug(jobs.LoadJobs(), "template-derived")
	if j == nil {
		t.Fatal("job not created in store")
	}
	if j.Type != want.Type || j.Repo != want.Repo || j.Base != want.Base ||
		j.Ready != want.Ready || j.Solo != want.Solo || j.Model != want.Model ||
		j.SkipDoc != want.SkipDoc || j.Body != want.Body {
		t.Fatalf("row = %+v, want fields matching the parsed template %+v", j, want)
	}
	if len(j.Prereqs) != len(want.Prereqs) {
		t.Fatalf("prereqs = %+v, want %+v", j.Prereqs, want.Prereqs)
	}
}

func TestWebJobFormStoreModePrereqsRoundTrip(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	mustCreateStoreJob(t, st, store.Job{Slug: "dep-store", Type: "build", Repo: "monolith"})
	mustCreateStoreJob(t, st, store.Job{Slug: "form-prereq-store", Type: "build", Repo: "monolith"})

	body := mustJSON(t, jobFormFields{
		Slug: "form-prereq-store", Type: "build", Repo: "monolith",
		Prereqs: []jobs.Prereq{{Job: "dep-store", Gated: true}},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}

	j := jobs.JobBySlug(jobs.LoadJobs(), "form-prereq-store")
	if j == nil || len(j.Prereqs) != 1 || j.Prereqs[0].Job != "dep-store" || !j.Prereqs[0].Gated {
		t.Fatalf("prereqs not persisted: %+v", j)
	}
}
