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

// jobfreeze_test.go — POST /api/job/freeze and the board's frozen_at key.
// Every job is seeded through writeTestJob (a temp store); nothing needs a
// live cell, since the route writes one column and types nothing.

type freezeResp struct {
	OK       bool   `json:"ok"`
	FrozenAt int64  `json:"frozen_at"`
	Error    string `json:"error"`
}

func postFreezeRaw(t *testing.T, body string) (int, freezeResp) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/job/freeze", strings.NewReader(body))
	rec := httptest.NewRecorder()
	webJobFreeze(rec, req)
	var out freezeResp
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func postFreeze(t *testing.T, slug string, frozen bool) (int, freezeResp) {
	t.Helper()
	return postFreezeRaw(t, string(mustJSON(t, map[string]any{"slug": slug, "frozen": frozen})))
}

func reloadJob(t *testing.T, slug string) *jobs.Job {
	t.Helper()
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s vanished", slug)
	}
	return j
}

func TestWebJobFreezeFreezesAndThawsABlockedJob(t *testing.T) {
	SetMirrorMode(false)
	writeTestJob(t, "hold-me", "state: blocked\n")

	code, resp := postFreeze(t, "hold-me", true)
	if code != http.StatusOK || !resp.OK || resp.FrozenAt == 0 {
		t.Fatalf("freeze: status %d, body %+v, want 200 ok with a stamp", code, resp)
	}
	j := reloadJob(t, "hold-me")
	if j.FrozenAt != resp.FrozenAt {
		t.Fatalf("stored FrozenAt = %d, response said %d", j.FrozenAt, resp.FrozenAt)
	}
	if j.State != "blocked" {
		t.Fatalf("freezing changed the job's state to %q; a hold writes no state", j.State)
	}
	if got := webFrozenAt(j); got != resp.FrozenAt {
		t.Fatalf("webFrozenAt = %d, want the stamp %d on a frozen blocked job", got, resp.FrozenAt)
	}

	code, resp = postFreeze(t, "hold-me", false)
	if code != http.StatusOK || !resp.OK || resp.FrozenAt != 0 {
		t.Fatalf("thaw: status %d, body %+v, want 200 ok with frozen_at 0", code, resp)
	}
	if j := reloadJob(t, "hold-me"); j.FrozenAt != 0 || webFrozenAt(j) != 0 {
		t.Fatalf("after thaw: stored %d, served %d, want both 0", j.FrozenAt, webFrozenAt(j))
	}
}

// Only a blocked job can be put on hold: a draft, a working job and a done
// job are each a 409 naming the state, and nothing is stored.
func TestWebJobFreezeRefusesAJobThatIsNotBlocked(t *testing.T) {
	SetMirrorMode(false)
	for slug, fm := range map[string]string{
		"frz-draft":   "",
		"frz-working": "state: working\n",
		"frz-done":    "state: done\n",
	} {
		writeTestJob(t, slug, fm)
		code, resp := postFreeze(t, slug, true)
		if code != http.StatusConflict {
			t.Errorf("%s: status %d, want 409", slug, code)
		}
		if !strings.Contains(resp.Error, "only a blocked job can be frozen") {
			t.Errorf("%s: error %q does not say why", slug, resp.Error)
		}
		if j := reloadJob(t, slug); j.FrozenAt != 0 {
			t.Errorf("%s: a refused freeze stored FrozenAt = %d", slug, j.FrozenAt)
		}
	}
}

// Thawing a job that is not frozen is a no-op 200, whatever its state: the
// caller asked for "not frozen" and that is what is true.
func TestWebJobFreezeThawOfAnUnfrozenJobIsANoOp(t *testing.T) {
	SetMirrorMode(false)
	writeTestJob(t, "never-held", "state: working\n")
	code, resp := postFreeze(t, "never-held", false)
	if code != http.StatusOK || !resp.OK || resp.FrozenAt != 0 {
		t.Fatalf("thaw of an unfrozen job: status %d, body %+v, want 200 ok 0", code, resp)
	}
}

func TestWebJobFreezeRefusesMalformedRequests(t *testing.T) {
	SetMirrorMode(false)
	writeTestJob(t, "strict", "state: blocked\n")

	for name, tc := range map[string]struct {
		body string
		want int
	}{
		// an absent `frozen` must never read as false, i.e. as a thaw
		"missing frozen":    {`{"slug":"strict"}`, http.StatusBadRequest},
		"null frozen":       {`{"slug":"strict","frozen":null}`, http.StatusBadRequest},
		"missing slug":      {`{"frozen":true}`, http.StatusBadRequest},
		"null body":         {`null`, http.StatusBadRequest},
		"unknown key":       {`{"slug":"strict","frozen":true,"at":5}`, http.StatusBadRequest},
		"not json":          {`{`, http.StatusBadRequest},
		"frozen as string":  {`{"slug":"strict","frozen":"yes"}`, http.StatusBadRequest},
		"no such job":       {`{"slug":"nobody-here","frozen":true}`, http.StatusNotFound},
		"no such job, thaw": {`{"slug":"nobody-here","frozen":false}`, http.StatusNotFound},
	} {
		if code, _ := postFreezeRaw(t, tc.body); code != tc.want {
			t.Errorf("%s: status %d, want %d", name, code, tc.want)
		}
	}
	if j := reloadJob(t, "strict"); j.FrozenAt != 0 {
		t.Fatalf("a refused request froze the job: %d", j.FrozenAt)
	}

	// and a refused request must not thaw one either
	if code, _ := postFreeze(t, "strict", true); code != http.StatusOK {
		t.Fatalf("freeze: status %d", code)
	}
	if code, _ := postFreezeRaw(t, `{"slug":"strict"}`); code != http.StatusBadRequest {
		t.Fatalf("missing frozen on a frozen job: status %d, want 400", code)
	}
	if j := reloadJob(t, "strict"); j.FrozenAt == 0 {
		t.Fatal("a body with no `frozen` key thawed the job")
	}
}

func TestWebJobFreezeIsPostOnlyAndMirrorGated(t *testing.T) {
	SetMirrorMode(false)
	writeTestJob(t, "gated", "state: blocked\n")

	req := httptest.NewRequest(http.MethodGet, "/api/job/freeze?slug=gated", nil)
	rec := httptest.NewRecorder()
	webJobFreeze(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: status %d, want 405", rec.Code)
	}

	SetMirrorMode(true)
	t.Cleanup(func() { SetMirrorMode(false) })
	body := mustJSON(t, map[string]any{"slug": "gated", "frozen": true})
	req = httptest.NewRequest(http.MethodPost, "/api/job/freeze", bytes.NewReader(body))
	rec = httptest.NewRecorder()
	webJobFreeze(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mirror mode: status %d, want 403", rec.Code)
	}
	SetMirrorMode(false)
	if j := reloadJob(t, "gated"); j.FrozenAt != 0 {
		t.Fatalf("a mirror-gated request froze the job: %d", j.FrozenAt)
	}
}

// The requirement, end to end through the route: a frozen job whose cell is
// seen working again stops being frozen, and stays unfrozen when it blocks
// again. jobs.WriteJobState is the write the daemon's tick makes.
func TestFrozenJobThawsWhenItsCellWorksAgain(t *testing.T) {
	SetMirrorMode(false)
	writeTestJob(t, "monitor", "state: blocked\n")
	if code, _ := postFreeze(t, "monitor", true); code != http.StatusOK {
		t.Fatalf("freeze: status %d", code)
	}

	if err := jobs.WriteJobState(reloadJob(t, "monitor"), "working"); err != nil {
		t.Fatalf("WriteJobState(working): %v", err)
	}
	if j := reloadJob(t, "monitor"); j.FrozenAt != 0 || webFrozenAt(j) != 0 {
		t.Fatalf("working again: stored %d, served %d, want both 0", j.FrozenAt, webFrozenAt(j))
	}

	if err := jobs.WriteJobState(reloadJob(t, "monitor"), "blocked"); err != nil {
		t.Fatalf("WriteJobState(blocked): %v", err)
	}
	j := reloadJob(t, "monitor")
	if j.State != "blocked" || j.FrozenAt != 0 || webFrozenAt(j) != 0 {
		t.Fatalf("blocked again: state %q stored %d served %d, want blocked and unfrozen", j.State, j.FrozenAt, webFrozenAt(j))
	}
}

// webFrozenAt serves the stamp only on a card that displays as blocked.
func TestWebFrozenAtPresenceRule(t *testing.T) {
	for name, tc := range map[string]struct {
		job  jobs.Job
		want int64
	}{
		"frozen and blocked":           {jobs.Job{State: "blocked", FrozenAt: 9}, 9},
		"blocked, not frozen":          {jobs.Job{State: "blocked"}, 0},
		"stamp beside working":         {jobs.Job{State: "working", FrozenAt: 9}, 0},
		"stamp beside done":            {jobs.Job{State: "done", FrozenAt: 9}, 0},
		"blocked row that is erroring": {jobs.Job{State: "blocked", FrozenAt: 9, Err: "bad frontmatter"}, 0},
	} {
		if got := webFrozenAt(&tc.job); got != tc.want {
			t.Errorf("%s: webFrozenAt = %d, want %d", name, got, tc.want)
		}
	}
}

// An archived card never carries the key, whatever its row holds.
func TestArchivedWebJobWithholdsFrozenAt(t *testing.T) {
	a := &jobs.Job{Slug: "old", ArchivedName: "old", State: "blocked", FrozenAt: 9}
	if wj := archivedWebJob(a, nil, nil, nil); wj.FrozenAt != 0 {
		t.Fatalf("archived card served frozen_at = %d, want it withheld", wj.FrozenAt)
	}
}

func TestJobFreezeWireShape(t *testing.T) {
	SetMirrorMode(false)
	writeTestJob(t, "contract-freeze", "state: blocked\n")

	for _, frozen := range []bool{true, false} {
		body := mustJSON(t, map[string]any{"slug": "contract-freeze", "frozen": frozen})
		req := httptest.NewRequest(http.MethodPost, "/api/job/freeze", bytes.NewReader(body))
		rec := httptest.NewRecorder()
		webJobFreeze(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webJobFreeze(frozen=%v): status %d, body %s", frozen, rec.Code, rec.Body.String())
		}
		assertShape(t, "/api/job/freeze", rec.Body.Bytes(), jobFreezeSpec)
	}
}
