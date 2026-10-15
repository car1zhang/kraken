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
)

// prattach_edge_test.go — athena-edge's blind tests for the 2026-10-14
// contract (artifacts/50-contract.md, Slice B): the boundary cases of
// /api/pr-attach's new out-of-repo refusal. Written from the contract text
// alone, before any implementation existed.

// prEdgeFileWorld points HOME and KRAKEN_PLUTO_DIR at fresh temp dirs and
// returns HOME.
func prEdgeFileWorld(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	return home
}

// prEdgeCheckout makes <home>/Developer/<name> a git checkout with the given
// origin (none when origin is "").
func prEdgeCheckout(t *testing.T, home, name, origin string) {
	t.Helper()
	dir := filepath.Join(home, "Developer", name)
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

// prEdgeWriteJob writes a job file by hand with the given repo: line
// (omitted entirely when repoLine is false).
func prEdgeWriteJob(t *testing.T, slug, repo string, repoLine bool) {
	t.Helper()
	src := "---\njob: " + slug + "\n"
	if repoLine {
		src += "repo: " + repo + "\n"
	}
	src += "---\n\n# Spec\n"
	if err := os.WriteFile(filepath.Join(jobs.PlutoDir(), slug+jobs.JobSuffix), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

func prEdgePost(t *testing.T, slug, url string) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(t, prAttachReq{Slug: slug, URL: url})
	rec := httptest.NewRecorder()
	prAttachHandler(rec, httptest.NewRequest(http.MethodPost, "/api/pr-attach", bytes.NewReader(body)))
	return rec
}

func prEdgePRURLs(t *testing.T, slug string) []string {
	t.Helper()
	j := jobs.JobBySlug(jobs.LoadJobs(), slug)
	if j == nil {
		t.Fatalf("job %s not found", slug)
	}
	var out []string
	for _, pr := range j.PRs {
		out = append(out, pr.URL)
	}
	return out
}

func prEdgeWantAttached(t *testing.T, rec *httptest.ResponseRecorder, want bool) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("200 body not JSON: %v (%s)", err, rec.Body.String())
	}
	if !resp["ok"] || resp["attached"] != want {
		t.Fatalf("resp = %+v, want ok=true attached=%v", resp, want)
	}
}

func prEdgeWant409(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusConflict {
		t.Fatalf("status %d, want 409, body %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("409 body not JSON: %v (%s)", err, rec.Body.String())
	}
	msg, _ := resp["error"].(string)
	if msg == "" {
		t.Fatalf("409 body has no string error field: %s", rec.Body.String())
	}
	return msg
}

func TestEdgePrAttachCaseInsensitiveOwnerRepoAttaches(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/Carl/Monolith.git")
	writeTestJob(t, "edge-case", "")

	prEdgeWantAttached(t, prEdgePost(t, "edge-case", "https://github.com/carl/monolith/pull/2"), true)
	if got := prEdgePRURLs(t, "edge-case"); len(got) != 1 {
		t.Fatalf("PRs = %v, want the one case-variant URL", got)
	}
}

func TestEdgePrAttachUppercaseURLAgainstLowercaseOriginAttaches(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-upper", "")

	prEdgeWantAttached(t, prEdgePost(t, "edge-upper", "https://github.com/CARL/MONOLITH/pull/2"), true)
}

func TestEdgePrAttachRepoNameDashSuffixIsRefused(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-dash", "")

	prEdgeWant409(t, prEdgePost(t, "edge-dash", "https://github.com/carl/monolith-ios/pull/3"))
	if got := prEdgePRURLs(t, "edge-dash"); len(got) != 0 {
		t.Fatalf("PRs = %v after a refusal, want none", got)
	}
}

func TestEdgePrAttachRepoNameDigitSuffixIsRefused(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-digit", "")

	prEdgeWant409(t, prEdgePost(t, "edge-digit", "https://github.com/carl/monolith2/pull/3"))
	if got := prEdgePRURLs(t, "edge-digit"); len(got) != 0 {
		t.Fatalf("PRs = %v after a refusal, want none", got)
	}
}

func TestEdgePrAttachOwnerStringPrefixIsRefused(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-owner", "")

	prEdgeWant409(t, prEdgePost(t, "edge-owner", "https://github.com/carlx/monolith/pull/3"))
}

func TestEdgePrAttachSSHOriginInRepoAttaches(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "git@github.com:carl/monolith.git")
	writeTestJob(t, "edge-ssh", "")

	prEdgeWantAttached(t, prEdgePost(t, "edge-ssh", "https://github.com/carl/monolith/pull/4"), true)
}

func TestEdgePrAttachSSHOriginOutOfRepoIsRefused(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "git@github.com:carl/monolith.git")
	writeTestJob(t, "edge-ssh-out", "")

	prEdgeWant409(t, prEdgePost(t, "edge-ssh-out", "https://github.com/carl/kraken/pull/4"))
}

func TestEdgePrAttachHTTPSOriginWithoutGitSuffixAttaches(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith")
	writeTestJob(t, "edge-nogit", "")

	prEdgeWantAttached(t, prEdgePost(t, "edge-nogit", "https://github.com/carl/monolith/pull/5"), true)
}

func TestEdgePrAttachBlankRepoFailsOpen(t *testing.T) {
	prEdgeFileWorld(t)
	prEdgeWriteJob(t, "edge-blank", "", true)

	prEdgeWantAttached(t, prEdgePost(t, "edge-blank", "https://github.com/anyone/anything/pull/6"), true)
}

func TestEdgePrAttachNoRepoLineFailsOpen(t *testing.T) {
	prEdgeFileWorld(t)
	prEdgeWriteJob(t, "edge-norepo", "", false)

	prEdgeWantAttached(t, prEdgePost(t, "edge-norepo", "https://github.com/anyone/anything/pull/6"), true)
}

func TestEdgePrAttachNoOriginRemoteFailsOpen(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "")
	writeTestJob(t, "edge-noorigin", "")

	prEdgeWantAttached(t, prEdgePost(t, "edge-noorigin", "https://github.com/carl/kraken/pull/7"), true)
}

func TestEdgePrAttachUnresolvableRepoFailsOpen(t *testing.T) {
	prEdgeFileWorld(t) // HOME has no Developer dir at all
	writeTestJob(t, "edge-unres", "")

	prEdgeWantAttached(t, prEdgePost(t, "edge-unres", "https://github.com/carl/kraken/pull/8"), true)
}

func TestEdgePrAttachUnresolvableRepoNamedOtherFailsOpen(t *testing.T) {
	// a checkout exists, but for a DIFFERENT name than the job's repo.
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "kraken", "https://github.com/carl/kraken.git")
	prEdgeWriteJob(t, "edge-other", "kraken-edge-missing", true)

	prEdgeWantAttached(t, prEdgePost(t, "edge-other", "https://github.com/carl/monolith/pull/8"), true)
}

func TestEdgePrAttach409BodyNamesURLAndPrefix(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "git@github.com:carl/monolith.git")
	writeTestJob(t, "edge-body", "")

	url := "https://github.com/carl/kraken/pull/9"
	rec := prEdgePost(t, "edge-body", url)
	msg := prEdgeWant409(t, rec)
	if !strings.Contains(msg, url) {
		t.Errorf("409 error %q does not name the URL %q", msg, url)
	}
	if !strings.Contains(msg, "github.com/carl/monolith") {
		t.Errorf("409 error %q does not name the job's prefix github.com/carl/monolith", msg)
	}
	var raw map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &raw)
	if _, ok := raw["attached"]; ok {
		t.Errorf("409 body carries an attached key: %s", rec.Body.String())
	}
}

func TestEdgePrAttachRefusalLeavesSiblingInRepoAttach(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-sibling", "")

	out := "https://github.com/carl/kraken/pull/10"
	in := "https://github.com/carl/monolith/pull/10"
	prEdgeWant409(t, prEdgePost(t, "edge-sibling", out))
	prEdgeWantAttached(t, prEdgePost(t, "edge-sibling", in), true)
	prEdgeWant409(t, prEdgePost(t, "edge-sibling", out))

	got := prEdgePRURLs(t, "edge-sibling")
	if len(got) != 1 || got[0] != in {
		t.Fatalf("PRs = %v, want exactly [%s]", got, in)
	}
	// the in-repo URL's repeat is still the unchanged dedup answer.
	prEdgeWantAttached(t, prEdgePost(t, "edge-sibling", in), false)
}

func TestEdgePrAttachRefusalAfterInRepoAttachKeepsList(t *testing.T) {
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-keep", "")

	in := "https://github.com/carl/monolith/pull/11"
	prEdgeWantAttached(t, prEdgePost(t, "edge-keep", in), true)
	prEdgeWant409(t, prEdgePost(t, "edge-keep", "https://github.com/carl/monolith-ios/pull/11"))
	got := prEdgePRURLs(t, "edge-keep")
	if len(got) != 1 || got[0] != in {
		t.Fatalf("PRs = %v, want exactly [%s] — a refusal must not disturb existing PRs", got, in)
	}
}

func TestEdgePrAttachFormerSlugIsRefusedOnTheRenamedJob(t *testing.T) {
	useTestStore(t)
	home := os.Getenv("HOME")
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	j := mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "edge-was", Repo: "monolith"})
	if _, err := jobs.RenameJob(j, "edge-is"); err != nil {
		t.Fatalf("seed rename: %v", err)
	}

	msg := prEdgeWant409(t, prEdgePost(t, "edge-was", "https://github.com/carl/kraken/pull/12"))
	if !strings.Contains(msg, "github.com/carl/monolith") {
		t.Errorf("409 error %q does not name the renamed job's prefix", msg)
	}
	if got := prEdgePRURLs(t, "edge-is"); len(got) != 0 {
		t.Fatalf("PRs = %v on the renamed job after a refusal, want none", got)
	}
}

func TestEdgePrAttachFormerSlugInRepoAttaches(t *testing.T) {
	useTestStore(t)
	home := os.Getenv("HOME")
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	j := mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "edge-was2", Repo: "monolith"})
	if _, err := jobs.RenameJob(j, "edge-is2"); err != nil {
		t.Fatalf("seed rename: %v", err)
	}

	in := "https://github.com/carl/monolith/pull/13"
	prEdgeWantAttached(t, prEdgePost(t, "edge-was2", in), true)
	if got := prEdgePRURLs(t, "edge-is2"); len(got) != 1 || got[0] != in {
		t.Fatalf("PRs = %v on the renamed job, want [%s]", got, in)
	}
}

func TestEdgePrAttachStoreModeRefusal(t *testing.T) {
	useTestStore(t)
	home := os.Getenv("HOME")
	prEdgeCheckout(t, home, "monolith", "git@github.com:carl/monolith.git")
	mustCreateStoreJob(t, jobs.ActiveStore(), store.Job{Slug: "edge-store", Repo: "monolith"})

	prEdgeWant409(t, prEdgePost(t, "edge-store", "https://github.com/carl/monolith2/pull/14"))
	if got := prEdgePRURLs(t, "edge-store"); len(got) != 0 {
		t.Fatalf("PRs = %v after a store-mode refusal, want none", got)
	}
}

func TestEdgePrAttachExistingChecksStillWinOverRefusal(t *testing.T) {
	// the 409 comes AFTER the 405/400/404 checks: an out-of-repo request that
	// is also malformed (or names no job) still gets the old answer.
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-order", "")

	if rec := prEdgePost(t, "no-such-edge-job", "https://github.com/carl/kraken/pull/15"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown slug + out-of-repo URL: status %d, want 404", rec.Code)
	}
	if rec := prEdgePost(t, "edge-order", "https://github.com/carl/kraken/pull/15/files"); rec.Code != http.StatusBadRequest {
		t.Errorf("malformed URL: status %d, want 400", rec.Code)
	}
	rec := httptest.NewRecorder()
	prAttachHandler(rec, httptest.NewRequest(http.MethodGet, "/api/pr-attach", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: status %d, want 405", rec.Code)
	}
}

func TestEdgePrAttachRefusalLogsEventAndFailOpenDoesNot(t *testing.T) {
	logsDir := t.TempDir()
	t.Setenv("KRAKEN_LOGS_DIR", logsDir) // first: TestMain's process-wide dir is shared
	home := prEdgeFileWorld(t)
	prEdgeCheckout(t, home, "monolith", "https://github.com/carl/monolith.git")
	writeTestJob(t, "edge-log", "")

	prEdgeWantAttached(t, prEdgePost(t, "edge-log", "https://github.com/carl/monolith/pull/16"), true)
	if strings.Contains(prEdgeLogs(t, logsDir), "pr-attach-refused") {
		t.Fatal("an in-repo attach logged a pr-attach-refused event")
	}

	prEdgeWant409(t, prEdgePost(t, "edge-log", "https://github.com/carl/kraken/pull/16"))
	logs := prEdgeLogs(t, logsDir)
	var line string
	for _, l := range strings.Split(logs, "\n") {
		if strings.Contains(l, "pr-attach-refused") {
			if line != "" {
				t.Fatalf("more than one pr-attach-refused event for one refusal:\n%s", logs)
			}
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no pr-attach-refused event under %s:\n%s", logsDir, logs)
	}
	for _, want := range []string{"edge-log", "https://github.com/carl/kraken/pull/16", "github.com/carl/monolith"} {
		if !strings.Contains(line, want) {
			t.Errorf("refusal event %q does not name %q", line, want)
		}
	}
}

// prEdgeLogs concatenates every *.jsonl under the test's own KRAKEN_LOGS_DIR
// ("" when none exist yet).
func prEdgeLogs(t *testing.T, dir string) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	var b strings.Builder
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(data)
		b.WriteString("\n")
	}
	return b.String()
}
