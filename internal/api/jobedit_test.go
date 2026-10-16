package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// TestWebJobRawAndFormEmitNumericMTime pins the response side of the wire
// contract: the shipped iOS app decodes mtime as Int64, so a JSON string
// (the old UnixNano encoding) fails to parse there.
func TestWebJobRawAndFormEmitNumericMTime(t *testing.T) {
	writeTestJob(t, "mtime-numeric", "")

	req := httptest.NewRequest(http.MethodGet, "/api/job/raw?slug=mtime-numeric", nil)
	rec := httptest.NewRecorder()
	webJobRaw(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobRaw: got status %d, body %s", rec.Code, rec.Body.String())
	}
	var raw map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("webJobRaw: bad response json: %v", err)
	}
	if mt, ok := raw["mtime"].(float64); !ok || mt <= 0 {
		t.Errorf("webJobRaw: mtime = %#v (%T), want a positive JSON number", raw["mtime"], raw["mtime"])
	}

	req = httptest.NewRequest(http.MethodGet, "/api/job/form?slug=mtime-numeric", nil)
	rec = httptest.NewRecorder()
	webJobForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("webJobForm: got status %d, body %s", rec.Code, rec.Body.String())
	}
	var form map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &form); err != nil {
		t.Fatalf("webJobForm: bad response json: %v", err)
	}
	if mt, ok := form["mtime"].(float64); !ok || mt <= 0 {
		t.Errorf("webJobForm: mtime = %#v (%T), want a positive JSON number", form["mtime"], form["mtime"])
	}

	createReq := httptest.NewRequest(http.MethodPost, "/api/job/create", bytes.NewReader(mustJSON(t, map[string]any{"slug": "mtime-created"})))
	createRec := httptest.NewRecorder()
	webJobCreate(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("webJobCreate: got status %d, body %s", createRec.Code, createRec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("webJobCreate: bad response json: %v", err)
	}
	if mt, ok := created["mtime"].(float64); !ok || mt <= 0 {
		t.Errorf("webJobCreate: mtime = %#v (%T), want a positive JSON number", created["mtime"], created["mtime"])
	}
}

// TestWebJobSaveFormMTimeLock exercises the optimistic lock through the real
// handler: a numeric mtime that doesn't match the row's rev must 409, a
// matching numeric mtime must save, and a legacy string mtime is still
// accepted (and still enforced) for older clients.
func TestWebJobSaveFormMTimeLock(t *testing.T) {
	writeTestJob(t, "mtime-lock", "")

	formReq := httptest.NewRequest(http.MethodGet, "/api/job/form?slug=mtime-lock", nil)
	formRec := httptest.NewRecorder()
	webJobForm(formRec, formReq)
	var form map[string]any
	if err := json.Unmarshal(formRec.Body.Bytes(), &form); err != nil {
		t.Fatalf("bad form response: %v", err)
	}
	mtime, ok := form["mtime"].(float64)
	if !ok {
		t.Fatalf("form mtime = %#v (%T), want float64", form["mtime"], form["mtime"])
	}

	// the token is the row's rev, so a stale one is any OTHER non-zero value
	// (0 means "skip the check")
	body, _ := json.Marshal(map[string]any{"slug": "mtime-lock", "repo": "monolith", "mtime": mtime + 1})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatched numeric mtime: got status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"slug": "mtime-lock", "repo": "monolith", "mtime": mtime + 1, "force": true})
	req = httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("stale mtime + force: got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var saved map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("stale mtime + force: bad response json: %v", err)
	}
	savedMTime, ok := saved["mtime"].(float64)
	if !ok || savedMTime <= 0 {
		t.Fatalf("stale mtime + force: mtime = %#v (%T), want a positive JSON number", saved["mtime"], saved["mtime"])
	}

	// the force save above bumped the row's rev, so the matching case now
	// has to use that fresh value rather than the token fetched before it.
	body, _ = json.Marshal(map[string]any{"slug": "mtime-lock", "repo": "monolith", "mtime": savedMTime})
	req = httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("matching numeric mtime: got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"slug": "mtime-lock", "repo": "monolith", "mtime": "1"})
	req = httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatched legacy string mtime: got status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}
}

// TestWebJobSaveMTimeLock mirrors TestWebJobSaveFormMTimeLock against the
// raw-save handler (webJobSave): same optimistic lock, same mtimeToken wire
// decoding, no Force field on this endpoint.
func TestWebJobSaveMTimeLock(t *testing.T) {
	writeTestJob(t, "raw-mtime-lock", "")
	content := "---\njob: raw-mtime-lock\nrepo: monolith\nready: true\n---\n\n# Spec\n"

	rawReq := httptest.NewRequest(http.MethodGet, "/api/job/raw?slug=raw-mtime-lock", nil)
	rawRec := httptest.NewRecorder()
	webJobRaw(rawRec, rawReq)
	var raw map[string]any
	if err := json.Unmarshal(rawRec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("bad raw response: %v", err)
	}
	mtime, ok := raw["mtime"].(float64)
	if !ok {
		t.Fatalf("raw mtime = %#v (%T), want float64", raw["mtime"], raw["mtime"])
	}

	body, _ := json.Marshal(map[string]any{"slug": "raw-mtime-lock", "content": content, "mtime": mtime + 1})
	req := httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatched numeric mtime: got status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"slug": "raw-mtime-lock", "content": content, "mtime": mtime})
	req = httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("matching numeric mtime: got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	var saved map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("matching numeric mtime: bad response json: %v", err)
	}
	if mt, ok := saved["mtime"].(float64); !ok || mt <= 0 {
		t.Errorf("matching numeric mtime: mtime = %#v (%T), want a positive JSON number", saved["mtime"], saved["mtime"])
	}

	body, _ = json.Marshal(map[string]any{"slug": "raw-mtime-lock", "content": content, "mtime": "0"})
	req = httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("legacy zero string mtime (skips lock): got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"slug": "raw-mtime-lock", "content": content, "mtime": ""})
	req = httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty string mtime (skips lock): got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"slug": "raw-mtime-lock", "content": content, "mtime": "banana"})
	req = httptest.NewRequest(http.MethodPost, "/api/job/save", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSave(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage mtime: got status %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}

// serializeJobFile must carry the kraken-owned prs: record through a web
// edit — dropping it would lose shipping history and make the tracker
// re-notify on its next tick. This also covers watchPRs' watermark fields
// (LastCommentAt/BumpedSHA): the doc comment on serializeJobFile claims prs
// is a "preserved kraken-owned tail … the form can't touch them by
// construction" — a form save that silently wiped the watermarks would make
// that false: comments arriving in the gap would never get bumped, and a
// standing CI failure would re-bump on the very next pass since BumpedSHA
// reset to "" reads as first-sight-not-yet-seeded again.
func TestSerializeJobFilePreservesPRs(t *testing.T) {
	f := jobFormFields{Slug: "x", Repo: "kraken"}
	prs := []jobs.JobPR{
		{URL: "https://github.com/example/repo/pull/1", State: "merged"},
		{
			URL: "https://github.com/example/repo/pull/2", State: "open",
			LastCommentAt: "2024-01-02T00:00:00Z", BumpedSHA: "sha1",
		},
	}
	content := serializeJobFile(f, "working", "", "", "", 0, prs, "")
	j := jobs.ParseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("serialized file does not round-trip: %s\n%s", j.Err, content)
	}
	if len(j.PRs) != 2 || j.PRs[0] != prs[0] || j.PRs[1] != prs[1] {
		t.Fatalf("prs (including watermarks) lost in round-trip: %+v", j.PRs)
	}
}

func TestSerializeJobFileModel(t *testing.T) {
	f := jobFormFields{Slug: "x", Repo: "kraken", Model: "sonnet"}
	content := serializeJobFile(f, "", "", "", "", 0, nil, "")
	j := jobs.ParseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("round-trip err: %s\n%s", j.Err, content)
	}
	if j.Model != "sonnet" {
		t.Fatalf("model lost in round-trip: model=%q", j.Model)
	}
	// conditional emission: a default model leaves the file free of the key,
	// so untouched jobs stay readable by older binaries
	content = serializeJobFile(jobFormFields{Slug: "x", Repo: "kraken"}, "", "", "", "", 0, nil, "")
	if strings.Contains(content, "model:") {
		t.Fatalf("default model serialized anyway:\n%s", content)
	}
	// skip-doc is gone outright (v5, breaking): a legacy file still carrying
	// the key must still parse (jobs.ParseJob tolerates it), but a form save
	// must never write it back.
	if strings.Contains(content, "skip-doc:") {
		t.Fatalf("skip-doc must never be written by a form save:\n%s", content)
	}
}

func TestSerializeJobFilePreservesSessionAndRun(t *testing.T) {
	f := jobFormFields{Slug: "x", Repo: "r"}
	content := serializeJobFile(f, "done", "shipped", "abc-123", "", 2, nil, "")
	j := jobs.ParseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("round-trip err: %s", j.Err)
	}
	if j.State != "done" || j.Outcome != "shipped" || j.Session != "abc-123" || j.Run != 2 {
		t.Fatalf("kraken-owned fields lost: %+v", j)
	}
	// and absent fields stay absent
	content = serializeJobFile(f, "", "", "", "", 0, nil, "")
	if strings.Contains(content, "session:") || strings.Contains(content, "run:") {
		t.Fatalf("empty session/run serialized anyway:\n%s", content)
	}
}

// TestSerializeJobFilePreservesTeardown covers: the teardown-
// intent marker (jobs.Job.Teardown) must round-trip through a raw
// composeRawJobContent/webJobSave cycle in store mode exactly like state/
// outcome/session/run already do — serializeJobFile is what both the file
// and store raw-save paths use to render it back out.
func TestSerializeJobFilePreservesTeardown(t *testing.T) {
	f := jobFormFields{Slug: "x", Repo: "r"}
	content := serializeJobFile(f, "working", "", "", "kill", 0, nil, "")
	j := jobs.ParseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("round-trip err: %s\n%s", j.Err, content)
	}
	if j.Teardown != "kill" {
		t.Fatalf("teardown lost in round-trip: %+v", j)
	}
	// and an absent teardown stays absent
	content = serializeJobFile(f, "", "", "", "", 0, nil, "")
	if strings.Contains(content, "teardown:") {
		t.Fatalf("empty teardown serialized anyway:\n%s", content)
	}
}

// TestWebJobSaveFormPreservesLegacyLogSection covers a job file left over
// from before the mid-run log was retired: saving through the form must
// still round-trip that section byte-preserved, since splitBodyLog() is the
// legacy-tolerant reader and nothing should ever drop existing content. A
// second save (with the fresh mtime the first save returned, mirroring the
// editor's real load-save-save flow) must produce byte-identical output —
// pinning the whole save path as idempotent, not just the log substring.
func TestWebJobSaveFormPreservesLegacyLogSection(t *testing.T) {
	writeTestJobBody(t, "legacy-log", "ready: true\n",
		"# Spec\ndo the thing\n\n## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n- [2026-07-01 10:00 me→zeus] earlier note\n")

	formReq := httptest.NewRequest(http.MethodGet, "/api/job/form?slug=legacy-log", nil)
	formRec := httptest.NewRecorder()
	webJobForm(formRec, formReq)
	var form map[string]any
	if err := json.Unmarshal(formRec.Body.Bytes(), &form); err != nil {
		t.Fatalf("bad form response: %v", err)
	}
	mtime, ok := form["mtime"].(float64)
	if !ok {
		t.Fatalf("form mtime = %#v (%T), want float64", form["mtime"], form["mtime"])
	}

	save := func(mtime float64) ([]byte, float64) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"slug": "legacy-log", "repo": "monolith", "spec": "do the thing", "mtime": mtime,
		})
		req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		webJobSaveForm(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("save: got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("bad save response: %v", err)
		}
		newMTime, ok := resp["mtime"].(float64)
		if !ok {
			t.Fatalf("save mtime = %#v (%T), want float64", resp["mtime"], resp["mtime"])
		}
		return savedJobText(t, "legacy-log"), newMTime
	}

	save1, mtimeAfterSave1 := save(mtime)
	if !strings.Contains(string(save1), "## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n- [2026-07-01 10:00 me→zeus] earlier note") {
		t.Fatalf("legacy log section did not survive the save byte-preserved:\n%s", save1)
	}
	save2, _ := save(mtimeAfterSave1)
	if !bytes.Equal(save1, save2) {
		t.Fatalf("second save is not byte-identical to the first (save is not idempotent):\nsave1:\n%s\nsave2:\n%s", save1, save2)
	}
}

// TestWebJobSaveFormNoLegacyLogSectionGainsNone covers the common case now:
// a job file with no legacy log section must not have one manufactured on
// save — the mid-run log is retired, so nothing should write it back in.
func TestWebJobSaveFormNoLegacyLogSectionGainsNone(t *testing.T) {
	writeTestJob(t, "no-log", "")

	body, _ := json.Marshal(map[string]any{
		"slug": "no-log", "repo": "monolith", "spec": "do the thing", "force": true,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("save: got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	saved := savedJobText(t, "no-log")
	if strings.Contains(string(saved), "Mid-run log") {
		t.Fatalf("log-less file gained a mid-run log section on save:\n%s", saved)
	}
	if !bytes.HasSuffix(saved, []byte("\n")) || bytes.HasSuffix(saved, []byte("\n\n")) {
		t.Fatalf("log-less save should end with exactly one trailing newline, got:\n%q", saved)
	}
}
