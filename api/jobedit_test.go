package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/jobs"
)

func TestWebJobSaveFormTypeValidation(t *testing.T) {
	// build/info are the only two accepted values now; legacy explore/question
	// values (still parseable from disk, per jobs.ParseJob) must be refused on save
	// so new job files can't be written with them.
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "form-type", "")
	for _, typ := range []string{"explore", "question", "banana", ""} {
		body, _ := json.Marshal(jobFormFields{Slug: "form-type", Type: typ, Repo: "monolith"})
		req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		webJobSaveForm(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("type %q: got status %d, want 400", typ, rec.Code)
			continue
		}
		var resp map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("type %q: bad response json: %v", typ, err)
		}
		if resp["error"] != "type must be build or info" {
			t.Errorf("type %q: error = %q", typ, resp["error"])
		}
	}
	for _, typ := range []string{"build", "info"} {
		body, _ := json.Marshal(jobFormFields{Slug: "form-type", Type: typ, Repo: "monolith"})
		req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		webJobSaveForm(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("type %q: got status %d, want 200 (body %s)", typ, rec.Code, rec.Body.String())
		}
	}
}

// TestWebJobRawAndFormEmitNumericMTime pins the response side of the wire
// contract: the shipped iOS app decodes mtime as Int64, so a JSON string
// (the old UnixNano encoding) fails to parse there.
func TestWebJobRawAndFormEmitNumericMTime(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
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
// handler: a numeric mtime that doesn't match the file on disk must 409, a
// matching numeric mtime must save, and a legacy string mtime is still
// accepted (and still enforced) for older clients.
func TestWebJobSaveFormMTimeLock(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
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

	body, _ := json.Marshal(map[string]any{"slug": "mtime-lock", "type": "build", "repo": "monolith", "mtime": mtime - 1})
	req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("mismatched numeric mtime: got status %d, want 409 (body %s)", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"slug": "mtime-lock", "type": "build", "repo": "monolith", "mtime": mtime - 1, "force": true})
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

	// the force save above moved the file's on-disk mtime forward, so the
	// matching case now has to use that fresh value rather than the mtime
	// fetched before it.
	body, _ = json.Marshal(map[string]any{"slug": "mtime-lock", "type": "build", "repo": "monolith", "mtime": savedMTime})
	req = httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobSaveForm(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("matching numeric mtime: got status %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}

	body, _ = json.Marshal(map[string]any{"slug": "mtime-lock", "type": "build", "repo": "monolith", "mtime": "1"})
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
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "raw-mtime-lock", "")
	content := "---\njob: raw-mtime-lock\ntype: build\nrepo: monolith\nready: true\n---\n\n# Spec\n"

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

	body, _ := json.Marshal(map[string]any{"slug": "raw-mtime-lock", "content": content, "mtime": mtime - 1})
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
// re-notify on its next tick.
func TestSerializeJobFilePreservesPRs(t *testing.T) {
	f := jobFormFields{Slug: "x", Type: "build", Repo: "kraken", Ready: true}
	prs := []jobs.JobPR{
		{URL: "https://github.com/car1zhang/kraken/pull/7", State: "merged"},
		{URL: "https://github.com/car1zhang/kraken/pull/9", State: "open"},
	}
	content := serializeJobFile(f, "working", "", "", 0, prs, "")
	j := jobs.ParseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("serialized file does not round-trip: %s\n%s", j.Err, content)
	}
	if len(j.PRs) != 2 || j.PRs[0] != prs[0] || j.PRs[1] != prs[1] {
		t.Fatalf("prs lost in round-trip: %+v", j.PRs)
	}
}

func TestSerializeJobFilePreservesSessionAndRun(t *testing.T) {
	f := jobFormFields{Slug: "x", Type: "build", Repo: "r"}
	content := serializeJobFile(f, "done", "shipped", "abc-123", 2, nil, "")
	j := jobs.ParseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("round-trip err: %s", j.Err)
	}
	if j.State != "done" || j.Outcome != "shipped" || j.Session != "abc-123" || j.Run != 2 {
		t.Fatalf("kraken-owned fields lost: %+v", j)
	}
	// and absent fields stay absent
	content = serializeJobFile(f, "", "", "", 0, nil, "")
	if strings.Contains(content, "session:") || strings.Contains(content, "run:") {
		t.Fatalf("empty session/run serialized anyway:\n%s", content)
	}
}
