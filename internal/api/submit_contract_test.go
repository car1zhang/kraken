package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
)

// submit_contract_test.go — the LOGIC dimension of "eliminate the queued
// status" on the API layer, authored blind from artifacts/50-contract.md.
//
// Every package-level identifier is prefixed `contract`; a sibling edge author
// shares this package's test binary.
//
// What this file pins:
//   - webSubmit's success path through the `submitJobFn` seam (a blind test
//     must never reach a real StartCell).
//   - /api/queue is registered and mirror-GATED; /api/dequeue and /api/start
//     are not registered at all.
//   - /api/reorder accepts exactly the four surviving columns.
//   - webJob carries neither a `ready` nor a `start_error` key.
//   - the job form's GET shape carries neither `ready` nor `prereqs`.
//   - apiContract == 6.

// --- webSubmit: the success path through the seam ---

// contractSubmitRecorder counts and captures every submitJobFn call. The COUNT
// matters as much as the argument: a handler that called the seam twice would
// try to start two cells for one drag.
type contractSubmitRecorder struct {
	slugs []string
}

// contractStubSubmitJob swaps the api-layer seam for the duration of the test.
func contractStubSubmitJob(t *testing.T, err error) *contractSubmitRecorder {
	t.Helper()
	rec := &contractSubmitRecorder{}
	prev := submitJobFn
	submitJobFn = func(j *jobs.Job) error {
		slug := ""
		if j != nil {
			slug = j.Slug
		}
		rec.slugs = append(rec.slugs, slug)
		return err
	}
	t.Cleanup(func() { submitJobFn = prev })
	return rec
}

// TestSubmitContractWebSubmitCallsTheSeamOnceAndSucceeds: a known slug reaches
// the seam exactly once, carrying the resolved job, and webSubmit returns nil
// ONLY because the seam did. The handler itself does no starting and no state
// writing — StartCell owns both.
func TestSubmitContractWebSubmitCallsTheSeamOnceAndSucceeds(t *testing.T) {
	st := useTestStore(t) // sandboxes HOME
	mustCreateStoreJob(t, st, store.Job{
		Slug: "submit-ok", Repo: "monolith", Body: "a real spec\n",
	})
	rec := contractStubSubmitJob(t, nil)

	if err := webSubmit(webReq{Slug: "submit-ok"}); err != nil {
		t.Fatalf("webSubmit on a known slug = %v, want nil", err)
	}
	if len(rec.slugs) != 1 {
		t.Fatalf("submitJobFn called %d times, want exactly 1: %v", len(rec.slugs), rec.slugs)
	}
	if rec.slugs[0] != "submit-ok" {
		t.Errorf("seam got job %q, want submit-ok", rec.slugs[0])
	}

	// the handler wrote nothing: the row is exactly as created.
	row, err := st.GetJobBySlug("submit-ok")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if row.State != "" {
		t.Errorf("row state = %q after a submit whose seam did nothing, want unchanged empty", row.State)
	}
}

// TestSubmitContractWebSubmitSurfacesTheSeamsError: the handler adds nothing of
// its own to a failed start — webAction turns whatever comes back into the 409
// {error} the board renders, so the text must arrive verbatim.
func TestSubmitContractWebSubmitSurfacesTheSeamsError(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{
		Slug: "submit-fails", Repo: "monolith", Body: "a real spec\n",
	})
	contractStubSubmitJob(t, contractSubmitErr("worktree already exists"))

	err := webSubmit(webReq{Slug: "submit-fails"})
	if err == nil {
		t.Fatal("webSubmit returned nil while the seam failed")
	}
	if err.Error() != "worktree already exists" {
		t.Errorf("error = %q, want the seam's own text verbatim", err.Error())
	}
}

// contractSubmitErr is a minimal error value; errors.New would do, but a named
// type keeps the prefix rule satisfied without importing errors just for this.
type contractSubmitErr string

func (e contractSubmitErr) Error() string { return string(e) }

// --- routes: /api/queue survives, /api/dequeue and /api/start do not ---

// TestSubmitContractQueueRouteIsRegistered: Q4 keeps the URL and renames the
// handler, so shipped iOS builds that drag to the (now unrenderable) queued
// column still start the job. Registered means "not 404": with mirror mode off
// the request reaches webSubmit's own validation instead.
func TestSubmitContractQueueRouteIsRegistered(t *testing.T) {
	SetMirrorMode(false)
	useTestStore(t) // sandboxes HOME
	// belt and braces: even an unknown slug must never reach a real StartCell.
	contractStubSubmitJob(t, nil)

	mux := http.NewServeMux()
	Register(mux)

	rec := postJSON(t, mux, "/api/queue", mustJSON(t, webReq{Slug: "nonexistent"}))
	if rec.Code == http.StatusNotFound {
		t.Fatalf("/api/queue is not registered (404) — Q4 keeps this URL as the submit")
	}
}

// TestSubmitContractRetiredRoutesAreGone: the two moves that only existed to
// serve a queued column are not merely refused, they are unregistered — a
// registered-but-refusing route is a surface the phone can still be told to
// call.
func TestSubmitContractRetiredRoutesAreGone(t *testing.T) {
	SetMirrorMode(false)
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	Register(mux)

	for _, path := range []string{"/api/dequeue", "/api/start"} {
		t.Run(path, func(t *testing.T) {
			rec := postJSON(t, mux, path, mustJSON(t, webReq{Slug: "nonexistent"}))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("%s: status = %d, want 404 — the route must not be registered at all", path, rec.Code)
			}
		})
	}
}

// TestSubmitContractSubmitIsMirrorGated: submit starts a real cell (git
// worktree, tmux session, rotation gate), so the old "queue/dequeue are ungated
// pure DB flips" exception dies with the flag. The gate must run BEFORE the
// body is decoded, which is why an unresolvable slug still yields 403.
func TestSubmitContractSubmitIsMirrorGated(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	t.Setenv("HOME", t.TempDir())

	mux := http.NewServeMux()
	Register(mux)

	rec := postJSON(t, mux, "/api/queue", mustJSON(t, webReq{Slug: "nonexistent"}))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("/api/queue under mirror mode: status = %d, want 403", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != "disabled in mirror mode" {
		t.Fatalf("body = %q, want %q", got, "disabled in mirror mode")
	}
}

// TestSubmitContractSubmitNeverReachesTheSeamUnderMirror is the guarantee
// TestTickMirrorNeverStartsACell used to hold, moved to its new home: with the
// queue pass gone, the submit handler is the only thing that can start a cell,
// so the mirror board must not be able to reach the start seam at all.
func TestSubmitContractSubmitNeverReachesTheSeamUnderMirror(t *testing.T) {
	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{
		Slug: "mirror-submit", Repo: "monolith", Body: "a real spec\n",
	})
	rec := contractStubSubmitJob(t, nil)

	mux := http.NewServeMux()
	Register(mux)
	resp := postJSON(t, mux, "/api/queue", mustJSON(t, webReq{Slug: "mirror-submit"}))
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", resp.Code)
	}
	if len(rec.slugs) != 0 {
		t.Fatalf("the start seam was reached %d times under mirror mode: %v", len(rec.slugs), rec.slugs)
	}
}

// --- /api/reorder: exactly the four surviving columns ---

// TestSubmitContractReorderAcceptsTheFourColumns: the board's reorder surface is
// the one place the column set is a wire input, so every surviving column must
// still be accepted. (The queued column's 409 is the edge author's row.)
func TestSubmitContractReorderAcceptsTheFourColumns(t *testing.T) {
	useTestStore(t)
	for _, column := range []string{"draft", "working", "blocked", "done"} {
		t.Run(column, func(t *testing.T) {
			rec := postReorder(t, column, []string{})
			if rec.Code != http.StatusOK {
				t.Fatalf("POST /api/reorder column=%q: status = %d, body %s", column, rec.Code, rec.Body.String())
			}
			var resp map[string]bool
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if !resp["ok"] {
				t.Fatalf("column=%q: got %v, want ok:true", column, resp)
			}
		})
	}
}

// --- the wire: webJob ---

// contractJSONTagNames returns the wire names of every field on v's struct type,
// so an assertion can speak about the TYPE rather than about one marshaled
// instance — a field with omitempty is invisible in a zero value's JSON and
// would slip past a marshal-only check.
func contractJSONTagNames(t *testing.T, v any) map[string]bool {
	t.Helper()
	typ := reflect.TypeOf(v)
	if typ.Kind() != reflect.Struct {
		t.Fatalf("contractJSONTagNames: %T is not a struct", v)
	}
	names := map[string]bool{}
	for i := 0; i < typ.NumField(); i++ {
		tag := typ.Field(i).Tag.Get("json")
		if tag == "" || tag == "-" {
			continue
		}
		names[strings.Split(tag, ",")[0]] = true
	}
	return names
}

// TestSubmitContractWebJobHasNoRetiredKeys: `ready` and `start_error` leave the
// board payload. start_error existed only to explain why a QUEUED job's last
// background start attempt failed — with the start synchronous, the failure is
// the submit's own 409 response and there is nothing left to remember.
func TestSubmitContractWebJobHasNoRetiredKeys(t *testing.T) {
	names := contractJSONTagNames(t, webJob{})
	for _, retired := range []string{"ready", "start_error", "prereqs"} {
		if names[retired] {
			t.Errorf("webJob still carries a %q wire key", retired)
		}
	}

	// and no marshaled instance may emit them either
	data, err := json.Marshal(webJob{Slug: "x", Repo: "monolith", State: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"ready", "start_error", "prereqs"} {
		if strings.Contains(string(data), `"`+retired+`"`) {
			t.Errorf("marshaled webJob unexpectedly has %q: %s", retired, data)
		}
	}
}

// TestSubmitContractWebJobSpecDropsTheRetiredKeys pins the transcription half:
// assertShape rejects any key the spec does not document, so as long as
// webJobSpec (contract_test.go, ios/API.md's transcription) carries no entry for
// the retired keys, TestWebJobWireShape is what forbids the served shape from
// carrying them.
func TestSubmitContractWebJobSpecDropsTheRetiredKeys(t *testing.T) {
	for _, retired := range []string{"ready", "start_error"} {
		if _, ok := webJobSpec[retired]; ok {
			t.Errorf("webJobSpec still documents %q — remove it here and from ios/API.md", retired)
		}
	}
	for _, kept := range []string{"slug", "repo", "state", "live", "mtime"} {
		if _, ok := webJobSpec[kept]; !ok {
			t.Errorf("webJobSpec lost %q — this change removes two keys, not more", kept)
		}
	}
}

// --- the wire: the job form (THE breaking change) ---

// TestSubmitContractJobFormSpecDropsTheRetiredKeys: the job editor's form is the
// shape whose key removal makes this release breaking (the v5 `skip_doc`
// precedent). Both the transcribed spec and the live GET body must be free of
// them.
func TestSubmitContractJobFormSpecDropsTheRetiredKeys(t *testing.T) {
	for _, retired := range []string{"ready", "prereqs"} {
		if _, ok := jobFormSpec[retired]; ok {
			t.Errorf("jobFormSpec still documents %q — remove it here and from ios/API.md", retired)
		}
	}
	// the fields that make the form usable must survive the removal
	for _, kept := range []string{"slug", "repo", "base", "solo", "model", "color", "spec", "mtime", "editable", "err"} {
		if _, ok := jobFormSpec[kept]; !ok {
			t.Errorf("jobFormSpec lost %q — this change removes two keys, not more", kept)
		}
	}
}

func TestSubmitContractJobFormGETCarriesNeitherRetiredKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "submit-form", "")

	req := httptest.NewRequest(http.MethodGet, "/api/job/form?slug=submit-form", nil)
	rec := httptest.NewRecorder()
	webJobForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobForm: status %d, body %s", rec.Code, rec.Body.String())
	}
	var form map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &form); err != nil {
		t.Fatal(err)
	}
	for _, retired := range []string{"ready", "prereqs"} {
		if _, ok := form[retired]; ok {
			t.Errorf("GET /api/job/form still serves %q: %s", retired, rec.Body.String())
		}
	}
	// the form is still a form: the identity keys the editor needs are there.
	for _, kept := range []string{"slug", "repo", "spec", "editable"} {
		if _, ok := form[kept]; !ok {
			t.Errorf("GET /api/job/form lost %q: %s", kept, rec.Body.String())
		}
	}
}

// --- the contract number ---

// TestSubmitContractAPIContractIsSix: removing `ready` from the job form and
// `start_error` from webJob are key REMOVALS on existing shapes, which is the
// definition of breaking — so the number moves, in the same commit as
// ios/API.md and APIClient.swift's expectedContract.
func TestSubmitContractAPIContractIsSix(t *testing.T) {
	if apiContract != 6 {
		t.Fatalf("apiContract = %d, want 6 (v6: ready+prereqs off the job form, start_error off webJob, queued state retired)", apiContract)
	}
}

// TestSubmitContractBoardServesTheBumpedContract: the number is only useful if
// the board payload actually carries it — the phone reads it off /api/board to
// decide whether to show its mismatch banner.
func TestSubmitContractBoardServesTheBumpedContract(t *testing.T) {
	data, err := json.Marshal(webBoard{Contract: apiContract})
	if err != nil {
		t.Fatal(err)
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		t.Fatal(err)
	}
	n, ok := m["contract"].(json.Number)
	if !ok {
		t.Fatalf("board payload has no numeric contract key: %s", data)
	}
	if got, _ := n.Int64(); got != 6 {
		t.Errorf("board contract = %d, want 6", got)
	}
}
