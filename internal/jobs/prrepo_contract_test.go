package jobs

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prrepo_contract_test.go — blind tests (athena-logic) for the repo guard on
// PR attach, written from 50-contract.md's Slice A: PRURLInRepo's true/false
// table, JobRepoOriginPrefix's ok/!ok cases, and PrHook's three stderr line
// shapes.

// logicGitRepo creates $HOME/Developer/<name> as a git checkout and, when
// origin is non-empty, adds it as the origin remote. HOME must already point
// at a temp dir.
func logicGitRepo(t *testing.T, name, origin string) string {
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
	return dir
}

// --- PRURLInRepo ---

func TestPRURLInRepo_TrueForPullURLInPrefixRepo(t *testing.T) {
	if !PRURLInRepo("https://github.com/carl/kraken/pull/3", "github.com/carl/kraken") {
		t.Fatal("want true for https:// + prefix + /pull/ + digits")
	}
	if !PRURLInRepo("https://github.com/carl/kraken/pull/12345", "github.com/carl/kraken") {
		t.Fatal("want true for a multi-digit PR number")
	}
}

func TestPRURLInRepo_CaseInsensitiveOwnerRepo(t *testing.T) {
	cases := []struct{ url, prefix string }{
		{"https://github.com/Carl/Kraken/pull/3", "github.com/carl/kraken"},
		{"https://github.com/carl/kraken/pull/3", "github.com/Carl/Kraken"},
		{"https://github.com/CARL/kraken/pull/3", "github.com/carl/KRAKEN"},
	}
	for _, c := range cases {
		if !PRURLInRepo(c.url, c.prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = false, want true (owner/repo compared case-insensitively)", c.url, c.prefix)
		}
	}
}

func TestPRURLInRepo_EmptyPrefixFalse(t *testing.T) {
	if PRURLInRepo("https://github.com/carl/kraken/pull/3", "") {
		t.Fatal("an empty prefix must return false")
	}
}

func TestPRURLInRepo_PrefixNotNormalized(t *testing.T) {
	url := "https://github.com/carl/kraken/pull/3"
	for _, prefix := range []string{
		"https://github.com/carl/kraken",
		"github.com/carl/kraken/",
		"github.com/carl/kraken.git",
	} {
		if PRURLInRepo(url, prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = true, want false (prefix is not normalized here)", url, prefix)
		}
	}
}

func TestPRURLInRepo_StringPrefixRepoFalse(t *testing.T) {
	prefix := "github.com/carl/kraken"
	for _, url := range []string{
		"https://github.com/carl/kraken-ios/pull/3",
		"https://github.com/carl/kraken2/pull/3",
		"https://github.com/carlz/kraken/pull/3",
	} {
		if PRURLInRepo(url, prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = true, want false (merely a string prefix)", url, prefix)
		}
	}
}

func TestPRURLInRepo_FalseOutsideTheShape(t *testing.T) {
	prefix := "github.com/carl/kraken"
	for _, url := range []string{
		"https://github.com/other/kraken/pull/3",  // other owner
		"https://github.com/carl/other/pull/3",    // other repo
		"http://github.com/carl/kraken/pull/3",    // not https://
		"https://github.com/carl/kraken/issues/3", // not /pull/
		"https://github.com/carl/kraken/pull/",    // no digits
		"https://github.com/carl/kraken/pull/3a",  // not all digits
		"https://github.com/carl/kraken/pull/3/files",
		"",
	} {
		if PRURLInRepo(url, prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = true, want false", url, prefix)
		}
	}
}

// --- JobRepoOriginPrefix ---

func TestJobRepoOriginPrefix_NilJob(t *testing.T) {
	prefix, ok := JobRepoOriginPrefix(nil)
	if ok || prefix != "" {
		t.Fatalf("nil job: got (%q, %v), want (\"\", false)", prefix, ok)
	}
}

func TestJobRepoOriginPrefix_BlankRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prefix, ok := JobRepoOriginPrefix(&Job{Repo: ""})
	if ok || prefix != "" {
		t.Fatalf("blank repo: got (%q, %v), want (\"\", false)", prefix, ok)
	}
}

func TestJobRepoOriginPrefix_UnresolvableRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	prefix, ok := JobRepoOriginPrefix(&Job{Repo: "kraken-logic-no-such-repo"})
	if ok || prefix != "" {
		t.Fatalf("unresolvable repo: got (%q, %v), want (\"\", false)", prefix, ok)
	}
}

func TestJobRepoOriginPrefix_NoOriginRemote(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logicGitRepo(t, "monolith", "")
	prefix, ok := JobRepoOriginPrefix(&Job{Repo: "monolith"})
	if ok || prefix != "" {
		t.Fatalf("checkout with no origin: got (%q, %v), want (\"\", false)", prefix, ok)
	}
}

func TestJobRepoOriginPrefix_HTTPSOriginNormalized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logicGitRepo(t, "monolith", "https://github.com/carl/kraken.git")
	prefix, ok := JobRepoOriginPrefix(&Job{Repo: "monolith"})
	if !ok || prefix != "github.com/carl/kraken" {
		t.Fatalf("https origin: got (%q, %v), want (\"github.com/carl/kraken\", true)", prefix, ok)
	}
}

func TestJobRepoOriginPrefix_SSHOriginNormalized(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	logicGitRepo(t, "monolith", "git@github.com:carl/kraken.git")
	prefix, ok := JobRepoOriginPrefix(&Job{Repo: "monolith"})
	if !ok || prefix != "github.com/carl/kraken" {
		t.Fatalf("ssh origin: got (%q, %v), want (\"github.com/carl/kraken\", true)", prefix, ok)
	}
}

// --- PrHook ---

const logicHookURL = "https://github.com/carl/kraken/pull/9"

// logicRunHook drives PrHook for a job whose repo does not resolve (so no
// local filtering can drop the URL) against srvAddr, returning stderr. It
// also points KRAKEN_DB_PATH at a temp path so the test can assert the hook
// never opened kraken.db.
func logicRunHook(t *testing.T, slug, srvAddr string) (stderr, dbPath string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())
	dbPath = filepath.Join(t.TempDir(), "kraken.db")
	t.Setenv("KRAKEN_DB_PATH", dbPath)
	src := "---\njob: " + slug + "\nrepo: kraken-logic-no-such-repo\n---\n\n# Spec\n"
	if err := os.WriteFile(filepath.Join(PlutoDir(), slug+".job.md"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_SERVE_ADDR", srvAddr)
	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"` + logicHookURL + `\n"}}`
	withStdin(t, payload, func() {
		stderr = withStderrCapture(t, func() { PrHook(slug) })
	})
	return stderr, dbPath
}

func logicServer(t *testing.T, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func logicAssertNoDB(t *testing.T, dbPath string) {
	t.Helper()
	if _, err := os.Stat(dbPath); err == nil {
		t.Fatalf("PrHook created %s: the hook must never open kraken.db", dbPath)
	}
}

func TestPrHook_200PrintsAttachedViaServe(t *testing.T) {
	addr := logicServer(t, http.StatusOK, `{"ok":true,"attached":true}`)
	stderr, db := logicRunHook(t, "hook-ok", addr)
	want := "pr-hook hook-ok: attached " + logicHookURL + " (via kraken serve)"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
	if strings.Contains(stderr, "NOT") {
		t.Fatalf("stderr = %q: a 200 must not print a NOT line", stderr)
	}
	logicAssertNoDB(t, db)
}

func TestPrHook_409PrintsNotAttachedWithServerError(t *testing.T) {
	errText := "PR https://github.com/carl/kraken/pull/9 is outside the job's repo github.com/carl/monolith"
	addr := logicServer(t, http.StatusConflict, `{"error":"`+errText+`"}`)
	stderr, db := logicRunHook(t, "hook-409", addr)
	want := "pr-hook hook-409: NOT attached (outside the job's repo): " + logicHookURL + " — " + errText
	if got := strings.TrimRight(stderr, "\n"); got != want {
		t.Fatalf("stderr = %q, want exactly one line %q", stderr, want)
	}
	if strings.Contains(stderr, "PR NOT RECORDED") {
		t.Fatalf("stderr = %q: a 409 must not print the serve-unreachable line", stderr)
	}
	logicAssertNoDB(t, db)
}

func TestPrHook_409UnparseableBodyPrintsRawTrimmed(t *testing.T) {
	addr := logicServer(t, http.StatusConflict, "  conflict: wrong repo  \n")
	stderr, db := logicRunHook(t, "hook-409raw", addr)
	want := "pr-hook hook-409raw: NOT attached (outside the job's repo): " + logicHookURL + " — conflict: wrong repo"
	if got := strings.TrimRight(stderr, "\n"); got != want {
		t.Fatalf("stderr = %q, want exactly one line %q", stderr, want)
	}
	logicAssertNoDB(t, db)
}

func TestPrHook_OtherNon200PrintsNotRecorded(t *testing.T) {
	addr := logicServer(t, http.StatusInternalServerError, `{"error":"boom"}`)
	stderr, db := logicRunHook(t, "hook-500", addr)
	want := "pr-hook hook-500: PR NOT RECORDED (serve unreachable): " + logicHookURL
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
	if strings.Contains(stderr, "outside the job's repo") {
		t.Fatalf("stderr = %q: a 500 must not print the 409 line", stderr)
	}
	logicAssertNoDB(t, db)
}

func TestPrHook_TransportFailurePrintsNotRecorded(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	stderr, db := logicRunHook(t, "hook-down", addr)
	want := "pr-hook hook-down: PR NOT RECORDED (serve unreachable): " + logicHookURL
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to contain %q", stderr, want)
	}
	logicAssertNoDB(t, db)
}
