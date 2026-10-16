package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// jobedit_storemode_test.go — coverage for api/jobedit.go's endpoints: raw
// round-trip, rev-based 409, and job/create. useTestStore
// (storemode_test.go) wires jobs onto a temp DB for the duration of each
// test.

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
		Slug: "raw-store", Repo: "monolith", Body: "\nsome spec text\n",
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
	if parsed.Repo != "monolith" {
		t.Fatalf("round-tripped fields: %+v", parsed)
	}

	// edit it back through webJobSave with a modified spec.
	newContent := "---\njob: raw-store\nrepo: other-repo\nready: false\nprereqs: []\nsolo: false\n---\n\nedited spec\n"
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
	if reloaded.Repo != "other-repo" {
		t.Fatalf("save did not apply parsed fields: %+v", reloaded)
	}
	// newContent above still carries the retired ready:/prereqs: keys, which
	// is exactly the shape a raw editor session started from an old file
	// pastes back. They must be consumed silently rather than erring the save
	// into a draft.
	if reloaded.Err != "" {
		t.Fatalf("retired keys in raw content erred the save: %q", reloaded.Err)
	}
	spec, _ := splitBodyLog(reloaded.Body)
	if got := spec; got == "" {
		t.Fatal("edited spec text lost")
	}
}

// TestWebJobSaveStoreModeUnparseablePreservesFields covers raw
// content that fails ParseJob must still save (unparseable drafts happen —
// the editor doesn't block on it), but only the body column changes.
// Frontmatter columns and PRs must survive exactly as they were —
// jobFieldsFromParsed(parsed) on a failed parse carries only ParseJob's
// best-effort defaults and must never be applied.
func TestWebJobSaveStoreModeUnparseablePreservesFields(t *testing.T) {
	useTestStore(t)
	st := jobs.ActiveStore()
	j := mustCreateStoreJob(t, st, store.Job{
		Slug: "garbage-store", Repo: "monolith", State: "done", Session: "sess-abc",
	})
	if err := st.UpsertPR(j.ID, "https://github.com/x/y/pull/1", "open"); err != nil {
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
	if reloaded.Repo != "monolith" || reloaded.State != "done" || reloaded.Session != "sess-abc" {
		t.Fatalf("frontmatter fields clobbered by an unparseable save: %+v", reloaded)
	}
	if reloaded.Body != garbage {
		t.Fatalf("body not updated: got %q, want %q", reloaded.Body, garbage)
	}
	if len(reloaded.PRs) != 1 || reloaded.PRs[0].URL != "https://github.com/x/y/pull/1" {
		t.Fatalf("prs clobbered by an unparseable save: %+v", reloaded.PRs)
	}
}

// TestWebJobSaveStoreModeGarbageContentPinsToDraft covers L6: a store-mode
// raw save whose content fails to parse must persist that failure into the
// row's new err column (mirroring file mode's ParseJob-derived Job.Err),
// so the job pins to "draft" — and SubmitJob's first refusal is exactly this
// case, so it cannot be started either — like an unparseable job FILE already
// does. A later save that parses clean must clear it.
func TestWebJobSaveStoreModeGarbageContentPinsToDraft(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{
		Slug: "garbage-pin", Repo: "monolith",
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
	if got := jobs.DisplayState(reloaded); got != "draft" {
		t.Fatalf("DisplayState after garbage save = %q, want draft (not startable)", got)
	}

	// a later save with content that parses clean must clear err. The content
	// still carries ready:/prereqs: — a real editor session's raw text, and
	// exactly the shape the accept-and-ignore parse arms exist for.
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
	// draft, not queued: a clean parse clears the error, and an unstarted job
	// has exactly one state to be in now.
	if got := jobs.DisplayState(fixed); got != "draft" {
		t.Fatalf("DisplayState after clean save = %q, want draft", got)
	}
}

func TestWebJobSaveStoreModeStaleRevConflict(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "stale-store", Repo: "monolith"})

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
	j := mustCreateStoreJob(t, st, store.Job{Slug: "pr-interleave", Repo: "monolith"})
	loadedMTime := j.Rev // the editor's "opened at rev 1" snapshot

	if err := st.UpsertPR(j.ID, "https://github.com/carl/kraken/pull/1", "open"); err != nil {
		t.Fatalf("UpsertPR: %v", err)
	}

	body := mustJSON(t, jobFormFields{Slug: "pr-interleave", Repo: "other", MTime: mtimeToken(loadedMTime)})
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
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "form-store", Repo: "monolith"})

	// a stale mtime refuses...
	body := mustJSON(t, jobFormFields{Slug: "form-store", Repo: "other", MTime: 999})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale mtime: status %d, want 409, body %s", rec.Code, rec.Body.String())
	}

	// ...but Force overrides it.
	body = mustJSON(t, jobFormFields{Slug: "form-store", Repo: "other", MTime: 999, Force: true})
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
	// The template's repo: is DefaultRepo() — the sole checkout under the
	// repos dir — so pin a repos dir holding exactly one.
	repos := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repos, "monolith", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_REPOS_DIR", repos)

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
	if j.Repo != "monolith" {
		t.Fatalf("defaults not applied: %+v", j)
	}

	// creating the same slug again is a 409.
	rec2 := httptest.NewRecorder()
	webJobCreate(rec2, httptest.NewRequest(http.MethodPost, "/api/job/create", bytes.NewReader(body)))
	if rec2.Code != http.StatusConflict {
		t.Fatalf("second create: status %d, want 409", rec2.Code)
	}
}

// TestWebJobCreateStoreModeRowMatchesParsedTemplate covers the L-nit: the
// store row webJobCreate creates must be exactly what parsing
// jobs.JobTemplate(slug) produces — the SAME text the create response hands
// the editor — not a separately hand-typed literal that could drift from the
// template's own defaults. A job created then abandoned (never actually
// saved) must leave the row matching that template.
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
	if j.Repo != want.Repo || j.Base != want.Base ||
		j.Solo != want.Solo || j.Model != want.Model ||
		j.Body != want.Body {
		t.Fatalf("row = %+v, want fields matching the parsed template %+v", j, want)
	}
}

// TestWebJobFormStoreModePrereqsRoundTrip is gone with the field: the form
// carried a prereqs array in both directions, and that removal — together
// with `ready` — is the v6 break. There is nothing left to round-trip.

// TestWebJobSaveFormStoreModeAgentRoundTrip proves the create/edit form's
// agent control actually persists (store mode): saving agent=codex and reading
// the form back returns agent=codex, and clearing it returns to the claude
// default. This is the toggle carl looked for, end to end through the wire.
func TestWebJobSaveFormStoreModeAgentRoundTrip(t *testing.T) {
	useTestStore(t)
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "agent-form", Repo: "kraken"})

	save := func(agent string) {
		t.Helper()
		body := mustJSON(t, jobFormFields{Slug: "agent-form", Repo: "kraken", Agent: agent, Force: true})
		rec := httptest.NewRecorder()
		webJobSaveForm(rec, httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("saveform agent=%q: status %d, body %s", agent, rec.Code, rec.Body.String())
		}
	}
	readAgent := func() string {
		t.Helper()
		rec := httptest.NewRecorder()
		webJobForm(rec, httptest.NewRequest(http.MethodGet, "/api/job/form?slug=agent-form", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("job/form: status %d, body %s", rec.Code, rec.Body.String())
		}
		var m map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		s, _ := m["agent"].(string)
		return s
	}

	save("codex")
	if got := readAgent(); got != "codex" {
		t.Fatalf("form did not persist agent: got %q, want codex", got)
	}
	// StartCell reads j.Agent off the loaded job — confirm the store-loaded job carries it
	if j := jobs.JobBySlug(jobs.LoadJobs(), "agent-form"); j == nil || j.Agent != "codex" {
		t.Fatalf("store-loaded job.Agent = %v, want codex", j)
	}
	save("")
	if got := readAgent(); got != "" {
		t.Fatalf("form did not clear agent: got %q, want empty (claude default)", got)
	}
}
