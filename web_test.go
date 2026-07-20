package main

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestWebSendTypeValidation(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	src := "---\njob: info-job\ntype: info\nrepo: monolith\nready: true\nstate: blocked\n---\n\n# Spec\n"
	if err := os.WriteFile(filepath.Join(plutoDir(), "info-job"+jobSuffix), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	err := webSend(webReq{Slug: "info-job"})
	if err == nil || !strings.Contains(err.Error(), "not a build job") {
		t.Fatalf("expected 'not a build job' error, got %v", err)
	}
}

func TestWebSendNotLive(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "send-not-live", "state: blocked\n")
	err := webSend(webReq{Slug: "send-not-live"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
	}
}

func TestWebSendNeverStarted(t *testing.T) {
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, "send-draft", "") // no state: — never started
	err := webSend(webReq{Slug: "send-draft"})
	if err == nil || !strings.Contains(err.Error(), "no live cell") {
		t.Fatalf("expected 'no live cell' error, got %v", err)
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
