package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/jobs"
	"kraken/internal/store"
	"kraken/internal/util"
)

// prattach_contract_test.go — blind tests (athena-logic) for 50-contract.md's
// Slice B: prAttachHandler's 409-with-no-attach, fail-open 200 and unchanged
// in-repo 200 paths.

const logicPrefix = "github.com/carl/kraken"

// logicAPIGitRepo creates $HOME/Developer/<name> as a git checkout, adding
// origin when non-empty. HOME must already point at a temp dir.
func logicAPIGitRepo(t *testing.T, name, origin string) {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), "Developer", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if origin != "" {
		if out, err := exec.Command("git", "-C", dir, "remote", "add", "origin", origin).CombinedOutput(); err != nil {
			t.Fatalf("git remote add: %v: %s", err, out)
		}
	}
}

// logicFileJob sets up a file-mode world: fresh HOME and pluto dir, and a
// writeTestJob job (repo: monolith).
func logicFileJob(t *testing.T, slug string) {
	t.Helper()
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir()) // the event log follows this, not HOME
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	writeTestJob(t, slug, "")
}

func logicPost(t *testing.T, slug, url string) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(t, prAttachReq{Slug: slug, URL: url})
	rec := httptest.NewRecorder()
	prAttachHandler(rec, httptest.NewRequest(http.MethodPost, "/api/pr-attach", bytes.NewReader(body)))
	return rec
}

func logicWantAttach(t *testing.T, rec *httptest.ResponseRecorder, wantAttached bool) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if !resp["ok"] || resp["attached"] != wantAttached {
		t.Fatalf("resp = %+v, want ok=true attached=%v", resp, wantAttached)
	}
}

func logicJobPRs(t *testing.T, slug string) []string {
	t.Helper()
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not found", slug)
	}
	var out []string
	for _, p := range j.PRs {
		out = append(out, p.URL)
	}
	return out
}

// logicEvents returns every logged event named name under $HOME/.kraken/logs.
func logicEvents(t *testing.T, name string) []util.LogEntry {
	t.Helper()
	var out []util.LogEntry
	for _, f := range util.ListLogFiles() {
		entries, err := util.ReadLogFile(f.Name)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.Event == name {
				out = append(out, e)
			}
		}
	}
	return out
}

func logicWant409(t *testing.T, rec *httptest.ResponseRecorder, url string) {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("409 body is not JSON: %s: %v", rec.Body.String(), err)
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, url) || !strings.Contains(msg, logicPrefix) {
		t.Fatalf("409 error = %q, want it to contain the URL %q and the prefix %q", msg, url, logicPrefix)
	}
}

func TestPrAttach_OutOfRepoAnswers409AndAttachesNothing(t *testing.T) {
	logicFileJob(t, "logic-out")
	logicAPIGitRepo(t, "monolith", "https://github.com/carl/kraken.git")

	url := "https://github.com/other/thing/pull/4"
	logicWant409(t, logicPost(t, "logic-out", url), url)
	if prs := logicJobPRs(t, "logic-out"); len(prs) != 0 {
		t.Fatalf("job PRs after a 409 = %v, want none", prs)
	}
}

func TestPrAttach_OutOfRepoLogsRefusalEvent(t *testing.T) {
	logicFileJob(t, "logic-evt")
	logicAPIGitRepo(t, "monolith", "git@github.com:carl/kraken.git")

	url := "https://github.com/carl/kraken-ios/pull/7"
	logicWant409(t, logicPost(t, "logic-evt", url), url)

	evs := logicEvents(t, "pr-attach-refused")
	if len(evs) != 1 {
		t.Fatalf("pr-attach-refused events = %+v, want exactly one", evs)
	}
	e := evs[0]
	if e.Src != "web" || e.Level != "info" {
		t.Errorf("event src/level = %q/%q, want web/info", e.Src, e.Level)
	}
	for _, part := range []string{"logic-evt", url, logicPrefix} {
		if !strings.Contains(e.Msg, part) {
			t.Errorf("event msg %q does not name %q", e.Msg, part)
		}
	}
}

func TestPrAttach_OutOfRepoStoreModeAnswers409AndWritesNothing(t *testing.T) {
	st := useTestStore(t) // sets HOME to a temp dir
	mustCreateStoreJob(t, st, store.Job{Slug: "logic-store-out", Repo: "monolith"})
	logicAPIGitRepo(t, "monolith", "https://github.com/carl/kraken")

	before, err := st.GetJobBySlug("logic-store-out")
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/other/thing/pull/4"
	logicWant409(t, logicPost(t, "logic-store-out", url), url)

	if prs := logicJobPRs(t, "logic-store-out"); len(prs) != 0 {
		t.Fatalf("job PRs after a store-mode 409 = %v, want none", prs)
	}
	after, err := st.GetJobBySlug("logic-store-out")
	if err != nil {
		t.Fatal(err)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("409 wrote the job row: rev %d→%d updated_at %d→%d", before.Rev, after.Rev, before.UpdatedAt, after.UpdatedAt)
	}
}

func TestPrAttach_OutOfRepoViaFormerSlugAnswers409(t *testing.T) {
	st := useTestStore(t)
	mustCreateStoreJob(t, st, store.Job{Slug: "logic-old", Repo: "monolith"})
	row, err := st.GetJobBySlug("logic-old")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.RenameJob(row.ID, row.Rev, "logic-new"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	logicAPIGitRepo(t, "monolith", "https://github.com/carl/kraken.git")

	url := "https://github.com/other/thing/pull/4"
	logicWant409(t, logicPost(t, "logic-old", url), url)
	if prs := logicJobPRs(t, "logic-new"); len(prs) != 0 {
		t.Fatalf("job PRs after a former-slug 409 = %v, want none", prs)
	}

	// and an in-repo URL through the former slug still attaches to the job.
	in := "https://github.com/carl/kraken/pull/5"
	logicWantAttach(t, logicPost(t, "logic-old", in), true)
	if prs := logicJobPRs(t, "logic-new"); len(prs) != 1 || prs[0] != in {
		t.Fatalf("job PRs after a former-slug attach = %v, want [%s]", prs, in)
	}
}

func TestPrAttach_FailOpenWhenRepoUnresolvable(t *testing.T) {
	logicFileJob(t, "logic-unres") // no $HOME/Developer/monolith at all

	url := "https://github.com/other/thing/pull/4"
	logicWantAttach(t, logicPost(t, "logic-unres", url), true)
	if prs := logicJobPRs(t, "logic-unres"); len(prs) != 1 || prs[0] != url {
		t.Fatalf("job PRs = %v, want [%s]", prs, url)
	}
	if evs := logicEvents(t, "pr-attach-refused"); len(evs) != 0 {
		t.Fatalf("fail-open path logged a refusal: %+v", evs)
	}
}

func TestPrAttach_FailOpenWhenOriginUnreadable(t *testing.T) {
	logicFileJob(t, "logic-noorigin")
	logicAPIGitRepo(t, "monolith", "")

	url := "https://github.com/other/thing/pull/4"
	logicWantAttach(t, logicPost(t, "logic-noorigin", url), true)
	if prs := logicJobPRs(t, "logic-noorigin"); len(prs) != 1 || prs[0] != url {
		t.Fatalf("job PRs = %v, want [%s]", prs, url)
	}
	if evs := logicEvents(t, "pr-attach-refused"); len(evs) != 0 {
		t.Fatalf("fail-open path logged a refusal: %+v", evs)
	}
}

func TestPrAttach_FailOpenWhenRepoBlank(t *testing.T) {
	t.Setenv("KRAKEN_LOGS_DIR", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	src := "---\njob: logic-blank\n---\n\n# Spec\n"
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), "logic-blank"+jobs.JobSuffix), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	// a checkout that WOULD refuse, were the blank repo wrongly resolved to it
	logicAPIGitRepo(t, "monolith", "https://github.com/carl/kraken.git")

	url := "https://github.com/other/thing/pull/4"
	logicWantAttach(t, logicPost(t, "logic-blank", url), true)
	if prs := logicJobPRs(t, "logic-blank"); len(prs) != 1 || prs[0] != url {
		t.Fatalf("job PRs = %v, want [%s]", prs, url)
	}
	if evs := logicEvents(t, "pr-attach-refused"); len(evs) != 0 {
		t.Fatalf("fail-open path logged a refusal: %+v", evs)
	}
}

func TestPrAttach_InRepoUnchangedAttachThenDedup(t *testing.T) {
	logicFileJob(t, "logic-in")
	logicAPIGitRepo(t, "monolith", "https://github.com/carl/kraken.git")

	url := "https://github.com/carl/kraken/pull/9"
	logicWantAttach(t, logicPost(t, "logic-in", url), true)
	logicWantAttach(t, logicPost(t, "logic-in", url), false)
	if prs := logicJobPRs(t, "logic-in"); len(prs) != 1 || prs[0] != url {
		t.Fatalf("job PRs = %v, want [%s]", prs, url)
	}
	if evs := logicEvents(t, "pr-attach-refused"); len(evs) != 0 {
		t.Fatalf("in-repo attach logged a refusal: %+v", evs)
	}
}

func TestPrAttach_InRepoCaseInsensitiveOwnerRepo(t *testing.T) {
	logicFileJob(t, "logic-case")
	logicAPIGitRepo(t, "monolith", "https://github.com/Carl/Kraken.git")

	url := "https://github.com/carl/kraken/pull/2"
	logicWantAttach(t, logicPost(t, "logic-case", url), true)
}

func TestPrAttach_ExistingRefusalsUnchanged(t *testing.T) {
	logicFileJob(t, "logic-refuse")
	logicAPIGitRepo(t, "monolith", "https://github.com/carl/kraken.git")

	// 405, 400 (bad url) and 404 still win over the repo check.
	rec := httptest.NewRecorder()
	prAttachHandler(rec, httptest.NewRequest(http.MethodGet, "/api/pr-attach", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status %d, want 405", rec.Code)
	}
	if rec := logicPost(t, "logic-refuse", "https://github.com/other/thing/issues/4"); rec.Code != http.StatusBadRequest {
		t.Errorf("bad-shape status %d, want 400", rec.Code)
	}
	if rec := logicPost(t, "no-such-logic-job", "https://github.com/other/thing/pull/4"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown-slug out-of-repo status %d, want 404", rec.Code)
	}
}
