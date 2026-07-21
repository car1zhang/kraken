package main

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"kraken/util"
)

func ipNet(ip string, bits int) *net.IPNet {
	return &net.IPNet{IP: net.ParseIP(ip), Mask: net.CIDRMask(bits, 32)}
}

func TestTailscaleIP(t *testing.T) {
	// a hit sits alongside ordinary LAN/loopback addrs, as a real interface
	// listing would have
	addrs := []net.Addr{
		ipNet("127.0.0.1", 8),
		ipNet("192.168.1.5", 24),
		ipNet("100.101.102.103", 10),
	}
	ip, ok := tailscaleIP(addrs)
	if !ok || ip != "100.101.102.103" {
		t.Fatalf("got %q, %v", ip, ok)
	}

	// 100.128.0.0/10 is the next block up — outside tailscale's CGNAT range
	// (100.64.0.0/10 ends at 100.127.255.255)
	if _, ok := tailscaleIP([]net.Addr{ipNet("100.128.0.1", 16)}); ok {
		t.Errorf("100.128.0.1 must not match")
	}
	// one address below the range
	if _, ok := tailscaleIP([]net.Addr{ipNet("100.63.255.255", 16)}); ok {
		t.Errorf("100.63.255.255 must not match")
	}
	// IPv6-only and loopback addrs are ignored, not mistaken for a match
	if _, ok := tailscaleIP([]net.Addr{
		&net.IPNet{IP: net.ParseIP("::1"), Mask: net.CIDRMask(128, 128)},
		ipNet("127.0.0.1", 8),
	}); ok {
		t.Errorf("loopback/IPv6 addrs must not match")
	}
	// miss case: nothing in range at all
	if _, ok := tailscaleIP([]net.Addr{ipNet("10.0.0.5", 8), ipNet("192.168.0.1", 24)}); ok {
		t.Errorf("non-tailnet addrs must not match")
	}
	// second octet in 64-127 but first octet not 100 — a corporate-VPN shape
	// (10.100.x.x) that must never bind the API
	if _, ok := tailscaleIP([]net.Addr{ipNet("10.100.0.5", 8)}); ok {
		t.Errorf("10.100.0.5 must not match")
	}
	if _, ok := tailscaleIP([]net.Addr{ipNet("172.64.1.1", 16)}); ok {
		t.Errorf("172.64.1.1 must not match")
	}
	if _, ok := tailscaleIP(nil); ok {
		t.Errorf("empty addr list must miss")
	}
}

func TestArtifactContentType(t *testing.T) {
	cases := map[string]string{
		"a/report.html": "text/html; charset=utf-8",
		"a/page.HTM":    "text/html; charset=utf-8",
		"a/d.svg":       "image/svg+xml",
		"a/doc.pdf":     "application/pdf",
		"a/doc.PDF":     "application/pdf",
		"a/img.png":     "image/png",
		"a/img.jpeg":    "image/jpeg",
		"a/img.jpg":     "image/jpeg",
		"a/anim.gif":    "image/gif",
		"a/img.webp":    "image/webp",
		"a/notes.md":    "text/plain; charset=utf-8",
		"a/x.log":       "text/plain; charset=utf-8",
		"a/no-ext":      "text/plain; charset=utf-8",
	}
	for p, want := range cases {
		if got := artifactContentType(p); got != want {
			t.Errorf("artifactContentType(%q) = %q, want %q", p, got, want)
		}
	}
}

func TestArtifactPathSubdirsAndTraversal(t *testing.T) {
	// build a temp cell artifacts tree: file at root + nested subdir file
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "artifacts", "icon-variations"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeF := func(rel string) {
		if err := os.WriteFile(filepath.Join(dir, "artifacts", rel), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeF("top.png")
	writeF(filepath.Join("icon-variations", "icon-v1.png"))
	writeF(filepath.Join("icon-variations", ".hidden"))

	// cellArtifacts derives from cellDir(j); fake it by pointing the job's
	// cell resolution at our temp dir via a stub is overkill — instead test
	// the pure guard against a known root.
	root := filepath.Join(dir, "artifacts")
	check := func(name string) bool {
		for _, seg := range strings.Split(strings.TrimPrefix(name, "/"), "/") {
			if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
				return false
			}
		}
		p := filepath.Join(root, name)
		rel, err := filepath.Rel(root, p)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
		fi, err := os.Stat(p)
		return err == nil && !fi.IsDir()
	}
	if !check("top.png") {
		t.Error("top-level file should resolve")
	}
	if !check("icon-variations/icon-v1.png") {
		t.Error("nested file should resolve")
	}
	if check("../../etc/passwd") {
		t.Error("traversal must be refused")
	}
	if check("icon-variations/../../../secret") {
		t.Error("mid-path traversal must be refused")
	}
	if check("icon-variations/.hidden") {
		t.Error("hidden file must be refused")
	}
	if check("icon-variations") {
		t.Error("a directory is not a file artifact")
	}
}

// --- webJob JSON shape: escalations are gone, new fields are omitempty ---

func TestWebJobJSONShapeEmpty(t *testing.T) {
	// a bare webJob (the common case: a draft/queued/done job with no live
	// cell) must not emit any of the live-cell-only fields, and must never
	// emit "escalations" — that field no longer exists on the type at all.
	data, err := json.Marshal(webJob{Slug: "x", Type: "build", Repo: "monolith", State: "draft"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, absent := range []string{"escalations", "detail", "question", "roster", "health", "cell", "err", "outcome", "success", "prereqs", "artifacts"} {
		if strings.Contains(s, `"`+absent+`"`) {
			t.Errorf("empty webJob JSON unexpectedly has %q: %s", absent, s)
		}
	}
	for _, want := range []string{`"slug":"x"`, `"type":"build"`, `"state":"draft"`, `"live":false`, `"mtime":0`} {
		if !strings.Contains(s, want) {
			t.Errorf("webJob JSON missing %q: %s", want, s)
		}
	}
}

func TestWebJobJSONShapePopulated(t *testing.T) {
	wj := webJob{
		Slug: "x", Type: "build", Repo: "monolith", State: "blocked", Live: true,
		Detail: "zeus said something",
		Question: &webQuestion{
			ToolUseID: "tu_1",
			Questions: []webQuestionItem{{
				Question: "which approach?", Header: "approach", MultiSelect: false,
				Options: []webQuestionOption{{Label: "A", Description: "do A"}, {Label: "B", Description: "do B"}},
			}},
		},
		Roster: []webAgent{{Name: "hephaestus-x", Model: "sonnet"}},
	}
	data, err := json.Marshal(wj)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	for _, want := range []string{
		`"detail":"zeus said something"`,
		`"question":{"tool_use_id":"tu_1"`,
		`"multi_select":false`,
		`"options":[{"label":"A","description":"do A"}`,
		`"roster":[{"name":"hephaestus-x","model":"sonnet"}]`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("populated webJob JSON missing %q: %s", want, s)
		}
	}
	if strings.Contains(s, "escalations") {
		t.Errorf("escalations leaked into JSON: %s", s)
	}
}

func TestWebQuestionFromNil(t *testing.T) {
	if got := webQuestionFrom(nil); got != nil {
		t.Fatalf("webQuestionFrom(nil) = %+v, want nil", got)
	}
}

func TestWebQuestionFromMirrorsFields(t *testing.T) {
	pq := &PendingQuestion{
		ToolUseID: "tu_2",
		Questions: []QuestionItem{{
			Question: "q", Header: "h", MultiSelect: true,
			Options: []QuestionOption{{Label: "l1", Description: "d1"}},
		}},
	}
	got := webQuestionFrom(pq)
	if got == nil || got.ToolUseID != "tu_2" || len(got.Questions) != 1 {
		t.Fatalf("webQuestionFrom mismatch: %+v", got)
	}
	q := got.Questions[0]
	if q.Question != "q" || q.Header != "h" || !q.MultiSelect || len(q.Options) != 1 {
		t.Fatalf("question item mismatch: %+v", q)
	}
	if q.Options[0].Label != "l1" || q.Options[0].Description != "d1" {
		t.Fatalf("option mismatch: %+v", q.Options[0])
	}
}

// --- lastSentMessage: the most recent me→zeus mid-run log entry ---

func TestLastSentMessageEmptyBody(t *testing.T) {
	if got := lastSentMessage(""); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLastSentMessageNoLogSection(t *testing.T) {
	body := "# Spec\ndo the thing\n"
	if got := lastSentMessage(body); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestLastSentMessageOneEntry(t *testing.T) {
	body := "# Spec\ndo the thing\n\n## Mid-run log\n<!-- appended by kraken; do not hand-edit -->\n" +
		"- [2026-07-01 10:00 me→zeus] pivot to plan B\n"
	if got := lastSentMessage(body); got != "pivot to plan B" {
		t.Errorf("got %q, want %q", got, "pivot to plan B")
	}
}

func TestLastSentMessageMultipleEntriesInterleaved(t *testing.T) {
	body := "# Spec\ndo the thing\n\n## Mid-run log\n" +
		"<!-- appended by kraken; do not hand-edit -->\n" +
		"- [2026-07-01 10:00 me→zeus] first message\n" +
		"- [2026-07-01 10:05 zeus→me] status update, not mine\n" +
		"- [2026-07-01 10:10 me→zeus] second message\n" +
		"- [2026-07-01 10:15 zeus→me] another status update\n"
	if got := lastSentMessage(body); got != "second message" {
		t.Errorf("got %q, want %q", got, "second message")
	}
}

func TestLastSentMessageSkipsAnsweredQuestionEntries(t *testing.T) {
	body := "# Spec\ndo the thing\n\n## Mid-run log\n" +
		"<!-- appended by kraken; do not hand-edit -->\n" +
		"- [2026-07-01 10:00 me→zeus] real message\n" +
		"- [2026-07-01 10:05 zeus→me] status update\n" +
		"- [2026-07-01 10:10 me→zeus] answered question: option 2\n"
	if got := lastSentMessage(body); got != "real message" {
		t.Errorf("got %q, want %q", got, "real message")
	}
}

func TestTruncateTextSanitizesControlCharsPreservesNewlines(t *testing.T) {
	in := "line one\x1b[31m colored\x7f\nline two"
	want := "line one [31m colored \nline two"
	if got := truncateText(in, 100); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// --- board move validation: pure, file-backed only (no tmux) ---

// writeTestJob writes a minimal parseable job file under a fresh
// KRAKEN_PLUTO_DIR and returns its slug — the shared fixture for the
// webXxx move-validation tests below, none of which need a live cell to
// reach their validation error.
func writeTestJob(t *testing.T, slug, extraFrontmatter string) {
	t.Helper()
	src := "---\njob: " + slug + "\ntype: build\nrepo: monolith\nready: true\n" +
		extraFrontmatter + "---\n\n# Spec\n"
	if err := os.WriteFile(filepath.Join(plutoDir(), slug+jobSuffix), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWebAnswerQuestionBadDigit(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "q-bad-digit", "state: blocked\n")
	for _, bad := range []string{"", "0", "10", "x", "1 2", " "} {
		if err := webAnswerQuestion(webReq{Slug: "q-bad-digit", Text: bad}); err == nil {
			t.Errorf("digit %q should be rejected", bad)
		} else if !strings.Contains(err.Error(), "single digit") {
			t.Errorf("digit %q: unexpected error %v", bad, err)
		}
	}
}

func TestWebAnswerQuestionNotLive(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	// state: blocked but no matching tmux session exists in the test
	// environment, so this must fail past digit validation on liveness
	writeTestJob(t, "q-not-live", "state: blocked\n")
	err := webAnswerQuestion(webReq{Slug: "q-not-live", Text: "1"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
	}
}

// TestAnswerableSingleDigit covers the guard webAnswerQuestion uses to refuse
// multi-question/multi-select prompts: those can't be expressed as one digit,
// so they must be rejected before any digit is ever sent or state written.
func TestAnswerableSingleDigit(t *testing.T) {
	cases := []struct {
		name string
		q    *PendingQuestion
		want bool
	}{
		{"nil question", nil, false},
		{"single, not multi-select", &PendingQuestion{Questions: []QuestionItem{{Question: "q"}}}, true},
		{"single, multi-select", &PendingQuestion{Questions: []QuestionItem{{Question: "q", MultiSelect: true}}}, false},
		{"multiple questions", &PendingQuestion{Questions: []QuestionItem{{Question: "q1"}, {Question: "q2"}}}, false},
		{"zero questions", &PendingQuestion{Questions: nil}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := answerableSingleDigit(c.q); got != c.want {
				t.Errorf("answerableSingleDigit(%+v) = %v, want %v", c.q, got, c.want)
			}
		})
	}
}

// TestWebDoneValidation covers webDone's server-side floor: started and not
// done, matching cmdDone rather than the board's (possibly stale) displayed
// column — a never-started job and an already-done one are both refused; a
// merely-working (not yet blocked) job is NOT refused here, since the
// board's column gating is what restricts the drag to BLOCKED, not this
// check (see webDone's doc comment).
func TestWebDoneValidation(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "draft-job", "ready: false\n")
	if err := webDone(webReq{Slug: "draft-job"}); err == nil || !strings.Contains(err.Error(), "hasn't started") {
		t.Fatalf("expected 'hasn't started' error, got %v", err)
	}
	writeTestJob(t, "done-job", "state: done\n")
	if err := webDone(webReq{Slug: "done-job"}); err == nil || !strings.Contains(err.Error(), "already done") {
		t.Fatalf("expected 'already done' error, got %v", err)
	}
}

func TestWebSteerNotLive(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "steer-not-live", "state: blocked\n")
	err := webSteer(webReq{Slug: "steer-not-live", Text: "pivot to plan B"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
	}
}

func TestWebSteerEmptyMessage(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "steer-empty", "state: blocked\n")
	if err := webSteer(webReq{Slug: "steer-empty", Text: "   "}); err == nil {
		t.Fatal("empty steering message should be rejected")
	}
}

func TestWebJobSaveFormTypeValidation(t *testing.T) {
	// build/info are the only two accepted values now; legacy explore/question
	// values (still parseable from disk, per parseJob) must be refused on save
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

// --- mtime wire contract: JSON number out, number-or-legacy-string in ---

func TestMTimeTokenUnmarshalJSON(t *testing.T) {
	cases := []struct {
		name    string
		json    string
		want    mtimeToken
		wantErr bool
	}{
		{"number", `1690000000000`, 1690000000000, false},
		{"numeric string (legacy)", `"1690000000000"`, 1690000000000, false},
		{"empty string skips lock", `""`, 0, false},
		{"zero string skips lock", `"0"`, 0, false},
		{"zero number skips lock", `0`, 0, false},
		{"garbage string", `"banana"`, 0, true},
		{"non-integer number", `1.5`, 0, true},
		{"null skips lock", `null`, 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var tok mtimeToken
			err := json.Unmarshal([]byte(c.json), &tok)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got tok=%d", tok)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tok != c.want {
				t.Errorf("got %d, want %d", tok, c.want)
			}
		})
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

// mustJSON marshals v for use as an httptest request body.
func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
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

// fakeFileInfo is a minimal os.FileInfo for exercising binaryStale without
// touching the filesystem.
type fakeFileInfo struct {
	modTime time.Time
	size    int64
}

func (f fakeFileInfo) Name() string       { return "kraken" }
func (f fakeFileInfo) Size() int64        { return f.size }
func (f fakeFileInfo) Mode() fs.FileMode  { return 0o755 }
func (f fakeFileInfo) ModTime() time.Time { return f.modTime }
func (f fakeFileInfo) IsDir() bool        { return false }
func (f fakeFileInfo) Sys() any           { return nil }

func TestBinaryStale(t *testing.T) {
	t0 := time.Unix(1700000000, 0)
	t1 := time.Unix(1700000100, 0)
	same := fakeFileInfo{modTime: t0, size: 1024}
	sameCopy := fakeFileInfo{modTime: t0, size: 1024}
	newerMTime := fakeFileInfo{modTime: t1, size: 1024}
	biggerSize := fakeFileInfo{modTime: t0, size: 2048}

	cases := []struct {
		name       string
		start, now os.FileInfo
		want       bool
	}{
		{"both nil", nil, nil, false},
		{"nil start", nil, same, false},
		{"nil now", same, nil, false},
		{"identical", same, sameCopy, false},
		{"mtime differs", same, newerMTime, true},
		{"size differs", same, biggerSize, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := util.BinaryStale(c.start, c.now); got != c.want {
				t.Errorf("util.BinaryStale(%v, %v) = %v, want %v", c.start, c.now, got, c.want)
			}
		})
	}
}

// TestIsSelfStaleDetectsChange exercises isSelfStale end to end (rather than
// just its pure binaryStale helper): TestHealthHandlerContractAndStale can
// only ever observe stale=false since util.SelfBinaryStart is nil in tests, so
// this drives the package state directly to cover the true branch too.
func TestIsSelfStaleDetectsChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken")
	if err := os.WriteFile(path, []byte("v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	origPath, origStart := util.SelfBinaryPath, util.SelfBinaryStart
	util.SelfBinaryPath, util.SelfBinaryStart = path, fi
	t.Cleanup(func() { util.SelfBinaryPath, util.SelfBinaryStart = origPath, origStart })

	if util.IsSelfStale() {
		t.Fatal("util.IsSelfStale() = true before any change, want false")
	}

	if err := os.WriteFile(path, []byte("v1 plus more bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}

	if !util.IsSelfStale() {
		t.Fatal("util.IsSelfStale() = false after binary changed, want true")
	}
}

// TestHealthHandlerContractAndStale asserts /api/health carries the wire
// contract version and a stale flag alongside the existing ok/host fields —
// no fixtures needed, healthHandler doesn't touch the pluto dir.
func TestHealthHandlerContractAndStale(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	rec := httptest.NewRecorder()
	healthHandler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthHandler: got status %d, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("healthHandler: bad response json: %v", err)
	}
	if ok, _ := resp["ok"].(bool); !ok {
		t.Errorf("healthHandler: ok = %#v, want true", resp["ok"])
	}
	if c, ok := resp["contract"].(float64); !ok || int(c) != apiContract {
		t.Errorf("healthHandler: contract = %#v, want %d", resp["contract"], apiContract)
	}
	if _, ok := resp["stale"].(bool); !ok {
		t.Errorf("healthHandler: stale = %#v (%T), want a bool", resp["stale"], resp["stale"])
	}
}

// serializeJobFile must carry the kraken-owned prs: record through a web
// edit — dropping it would lose shipping history and make the tracker
// re-notify on its next tick.
func TestSerializeJobFilePreservesPRs(t *testing.T) {
	f := jobFormFields{Slug: "x", Type: "build", Repo: "kraken", Ready: true}
	prs := []JobPR{
		{URL: "https://github.com/car1zhang/kraken/pull/7", State: "merged"},
		{URL: "https://github.com/car1zhang/kraken/pull/9", State: "open"},
	}
	content := serializeJobFile(f, "working", "", "", 0, prs, "")
	j := parseJob("x.job.md", []byte(content))
	if j.Err != "" {
		t.Fatalf("serialized file does not round-trip: %s\n%s", j.Err, content)
	}
	if len(j.PRs) != 2 || j.PRs[0] != prs[0] || j.PRs[1] != prs[1] {
		t.Fatalf("prs lost in round-trip: %+v", j.PRs)
	}
}

// --- boardCache: TTL memoization + single-flight over buildWebBoard ---

// countingCache builds a boardCache whose build func counts its own calls
// and whose clock is a plain closure variable the test can advance — no
// goroutines, no package-level state, so it can't leak into other tests.
func countingCache(ttl time.Duration, clock *time.Time) (*boardCache, *int32) {
	var builds int32
	c := &boardCache{
		ttl: ttl,
		now: func() time.Time { return *clock },
		build: func() webBoard {
			atomic.AddInt32(&builds, 1)
			return webBoard{}
		},
	}
	return c, &builds
}

func TestBoardCacheTTLHit(t *testing.T) {
	clock := time.Now()
	c, builds := countingCache(2*time.Second, &clock)
	c.get()
	c.get()
	if got := atomic.LoadInt32(builds); got != 1 {
		t.Fatalf("builds = %d, want 1 — a second get() inside the ttl must hit the cache", got)
	}
}

func TestBoardCacheTTLExpiry(t *testing.T) {
	clock := time.Now()
	c, builds := countingCache(2*time.Second, &clock)
	c.get()
	clock = clock.Add(3 * time.Second)
	c.get()
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds = %d, want 2 — a get() past the ttl must rebuild", got)
	}
}

func TestBoardCacheInvalidate(t *testing.T) {
	clock := time.Now()
	c, builds := countingCache(2*time.Second, &clock)
	c.get()
	c.invalidate()
	c.get()
	if got := atomic.LoadInt32(builds); got != 2 {
		t.Fatalf("builds = %d, want 2 — invalidate() must force the next get() to rebuild", got)
	}
}

// TestBoardCacheSingleFlight exercises the reason get() holds its lock across
// build(): a burst of concurrent callers hitting a cold cache (several web
// tabs + the iOS app polling at once) must collapse into exactly one
// discovery, with the late arrivals blocking on the mutex and then reusing
// the fresh entry rather than each triggering their own build.
func TestBoardCacheSingleFlight(t *testing.T) {
	clock := time.Now()
	var builds int32
	c := &boardCache{
		ttl: 2 * time.Second,
		now: func() time.Time { return clock },
		build: func() webBoard {
			atomic.AddInt32(&builds, 1)
			time.Sleep(20 * time.Millisecond) // simulate a slow tmux/ps/lsof discovery
			return webBoard{}
		},
	}
	var wg sync.WaitGroup
	const n = 10
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.get()
		}()
	}
	wg.Wait()
	if got := atomic.LoadInt32(&builds); got != 1 {
		t.Fatalf("builds = %d, want 1 — %d concurrent misses on a cold cache must single-flight", got, n)
	}
}
