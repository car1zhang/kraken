package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"kraken/internal/jobs"
)

// postColor drives the handler and returns the status; every test here
// seeds its job through writeTestJob (a temp store), so nothing needs a
// live cell.
func postColor(t *testing.T, slug, color string) int {
	t.Helper()
	body := mustJSON(t, map[string]any{"slug": slug, "color": color})
	req := httptest.NewRequest(http.MethodPost, "/api/job/color", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobColor(rec, req)
	return rec.Code
}

func jobColorOnDisk(t *testing.T, slug string) string {
	t.Helper()
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s vanished", slug)
	}
	if j.Err != "" {
		t.Fatalf("job %s no longer parses: %s", slug, j.Err)
	}
	return j.Color
}

func TestWebJobColorSetsAndClears(t *testing.T) {
	writeTestJob(t, "hue", "")

	if code := postColor(t, "hue", "purple"); code != http.StatusOK {
		t.Fatalf("set: status %d", code)
	}
	if got := jobColorOnDisk(t, "hue"); got != "purple" {
		t.Fatalf("after set: color = %q", got)
	}
	// a second colour replaces rather than appends a duplicate key (which
	// would make the job unparseable and pin it to draft)
	if code := postColor(t, "hue", "cyan"); code != http.StatusOK {
		t.Fatalf("recolour: status %d", code)
	}
	if got := jobColorOnDisk(t, "hue"); got != "cyan" {
		t.Fatalf("after recolour: color = %q", got)
	}
	// "" is the legitimate clear, not a rejected value
	if code := postColor(t, "hue", ""); code != http.StatusOK {
		t.Fatalf("clear: status %d", code)
	}
	if got := jobColorOnDisk(t, "hue"); got != "" {
		t.Fatalf("after clear: color = %q", got)
	}
}

// TestWebJobColorRejectsOffPalette pins the endpoint's allowlist — the one
// gate standing between a client and a string typed into a live claude.
func TestWebJobColorRejectsOffPalette(t *testing.T) {
	writeTestJob(t, "hue", "")

	for _, bad := range []string{"purpel", "default", "PURPLE", " ", "red # note", "red; rm -rf /"} {
		if code := postColor(t, "hue", bad); code != http.StatusBadRequest {
			t.Errorf("color %q: status %d, want 400", bad, code)
		}
		if got := jobColorOnDisk(t, "hue"); got != "" {
			t.Fatalf("color %q was written anyway: %q", bad, got)
		}
	}
}

// TestWebJobColorErrorEchoIsBounded: the refusal quotes the value back, so
// it goes through core.TruncateOneLine like the parser's message for the
// same rule — a client can't turn a 400 into a megabyte of response body.
func TestWebJobColorErrorEchoIsBounded(t *testing.T) {
	writeTestJob(t, "hue", "")

	body := mustJSON(t, map[string]any{"slug": "hue", "color": strings.Repeat("z", 5000)})
	req := httptest.NewRequest(http.MethodPost, "/api/job/color", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobColor(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if n := rec.Body.Len(); n > 200 {
		t.Errorf("400 body is %d bytes — the rejected value is being echoed unbounded", n)
	}
}

// TestWebJobColorRefusesOversizeBody matches the package norm (peers.go,
// feedback.go, questions.go, update.go): the whole legitimate request is two
// short strings, so a huge one is refused rather than read into memory, and
// its 413 stays distinct from a decode error's 400.
func TestWebJobColorRefusesOversizeBody(t *testing.T) {
	writeTestJob(t, "hue", "")

	body := mustJSON(t, map[string]any{"slug": "hue", "color": strings.Repeat("z", 2<<20)})
	req := httptest.NewRequest(http.MethodPost, "/api/job/color", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	webJobColor(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", rec.Code)
	}
	if got := jobColorOnDisk(t, "hue"); got != "" {
		t.Fatalf("oversize body wrote a colour: %q", got)
	}
}

func TestWebJobColorUnknownJob(t *testing.T) {
	if code := postColor(t, "nope", "red"); code != http.StatusNotFound {
		t.Errorf("unknown slug: status %d, want 404", code)
	}
}

// TestWebJobColorIgnoresEditableGate is the whole reason this endpoint
// exists: a live cell is exactly when recolouring is useful, and the form
// save's editableJob gate (drafts/queued/done only) is disjoint from the
// working/blocked states the delivery pass acts on — routing colour through
// the form would have made the feature unreachable by construction.
func TestWebJobColorIgnoresEditableGate(t *testing.T) {
	writeTestJob(t, "live-job", "state: working\n")

	j := jobs.JobBySlug(jobs.LoadJobs(), "live-job")
	if j == nil || editableJob(j) {
		t.Fatalf("fixture must be a non-editable working job, got %+v", j)
	}
	if code := postColor(t, "live-job", "green"); code != http.StatusOK {
		t.Fatalf("recolouring a working job: status %d, want 200", code)
	}
	if got := jobColorOnDisk(t, "live-job"); got != "green" {
		t.Fatalf("color = %q, want green", got)
	}
}

// TestSaveFormNeverWritesColor covers the wipe an already-installed client
// would otherwise cause: /api/job/saveform is a FULL REPLACE, so a body
// with no "color" key decodes to "" — and in store mode there is nothing to
// restore it from. The form must preserve the stored value and ignore any
// colour the request carries.
func TestSaveFormNeverWritesColor(t *testing.T) {
	writeTestJob(t, "formjob", "color: pink\n")

	save := func(t *testing.T, body map[string]any) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/job/saveform", bytes.NewReader(mustJSON(t, body)))
		rec := httptest.NewRecorder()
		webJobSaveForm(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("saveform: status %d, body %s", rec.Code, rec.Body.String())
		}
	}

	// an old client that cannot emit the key at all
	save(t, map[string]any{"slug": "formjob", "repo": "monolith", "spec": "hello"})
	if got := jobColorOnDisk(t, "formjob"); got != "pink" {
		t.Fatalf("old-client save wiped the colour: %q", got)
	}
	// and a client that emits a DIFFERENT one is ignored, not obeyed —
	// there is exactly one writer, and it is POST /api/job/color
	save(t, map[string]any{"slug": "formjob", "repo": "monolith", "spec": "hello", "color": "red"})
	if got := jobColorOnDisk(t, "formjob"); got != "pink" {
		t.Fatalf("form save wrote colour %q, want the stored pink", got)
	}
}
