package jobs

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// prrepo_edge_test.go — athena-edge's blind tests for the 2026-10-14 contract
// (artifacts/50-contract.md, Slice A): the boundary cases of PRURLInRepo,
// JobRepoOriginPrefix and PrHook's new 409 line. Written from the contract
// text alone, before any implementation existed.

// prEdgeRepo makes $HOME/Developer/<name> a git checkout (HOME is pointed at
// a fresh temp dir first) and, when origin is non-empty, gives it that origin.
func prEdgeRepo(t *testing.T, name, origin string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
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

func TestEdgePRURLInRepoOwnerRepoCaseInsensitive(t *testing.T) {
	cases := []struct{ url, prefix string }{
		{"https://github.com/Carl/Kraken/pull/3", "github.com/carl/kraken"},
		{"https://github.com/carl/kraken/pull/3", "github.com/Carl/Kraken"},
		{"https://github.com/CARL/kraken/pull/3", "github.com/carl/KRAKEN"},
	}
	for _, c := range cases {
		if !PRURLInRepo(c.url, c.prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = false, want true (owner/repo compare is case-insensitive)", c.url, c.prefix)
		}
	}
}

func TestEdgePRURLInRepoRepoStringPrefixIsNotSameRepo(t *testing.T) {
	for _, url := range []string{
		"https://github.com/carl/kraken-ios/pull/3",
		"https://github.com/carl/kraken2/pull/3",
		"https://github.com/carl/kraken.go/pull/3",
		"https://github.com/carl/KRAKEN-IOS/pull/3",
	} {
		if PRURLInRepo(url, "github.com/carl/kraken") {
			t.Errorf("PRURLInRepo(%q, github.com/carl/kraken) = true, want false (a repo name that merely starts with the job's)", url)
		}
	}
}

func TestEdgePRURLInRepoOwnerStringPrefixIsNotSameRepo(t *testing.T) {
	for _, url := range []string{
		"https://github.com/carlx/kraken/pull/3",
		"https://github.com/carl-org/kraken/pull/3",
	} {
		if PRURLInRepo(url, "github.com/carl/kraken") {
			t.Errorf("PRURLInRepo(%q, github.com/carl/kraken) = true, want false (an owner that merely starts with the job's)", url)
		}
	}
}

func TestEdgePRURLInRepoShorterPrefixRepoIsNotSameRepo(t *testing.T) {
	// the reverse direction: the job's repo is the LONGER name.
	if PRURLInRepo("https://github.com/carl/kraken/pull/3", "github.com/carl/kraken-ios") {
		t.Error("kraken PR matched prefix github.com/carl/kraken-ios, want false")
	}
}

func TestEdgePRURLInRepoDifferentOwnerSameRepoName(t *testing.T) {
	if PRURLInRepo("https://github.com/someone/kraken/pull/3", "github.com/carl/kraken") {
		t.Error("a fork's PR (other owner, same repo name) matched, want false")
	}
}

func TestEdgePRURLInRepoEmptyPrefix(t *testing.T) {
	for _, url := range []string{
		"https://github.com/carl/kraken/pull/3",
		"https:///pull/3",
		"",
	} {
		if PRURLInRepo(url, "") {
			t.Errorf("PRURLInRepo(%q, \"\") = true, want false (empty prefix is never a match)", url)
		}
	}
}

func TestEdgePRURLInRepoPrefixNotNormalized(t *testing.T) {
	url := "https://github.com/carl/kraken/pull/3"
	for _, prefix := range []string{
		"https://github.com/carl/kraken",
		"github.com/carl/kraken/",
		"github.com/carl/kraken.git",
		"git@github.com:carl/kraken",
	} {
		if PRURLInRepo(url, prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = true, want false (the prefix is not normalized here)", url, prefix)
		}
	}
}

func TestEdgePRURLInRepoPRNumberMustBeDigits(t *testing.T) {
	prefix := "github.com/carl/kraken"
	for _, url := range []string{
		"https://github.com/carl/kraken/pull/",
		"https://github.com/carl/kraken/pull/abc",
		"https://github.com/carl/kraken/pull/3a",
		"https://github.com/carl/kraken/pull/-3",
		"https://github.com/carl/kraken/pull/3/files",
		"https://github.com/carl/kraken/pull/3/",
		"https://github.com/carl/kraken/pull/3?x=1",
		"https://github.com/carl/kraken/pull/3 ",
	} {
		if PRURLInRepo(url, prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = true, want false (must end in /pull/<digits>)", url, prefix)
		}
	}
	if !PRURLInRepo("https://github.com/carl/kraken/pull/1234567", prefix) {
		t.Error("a multi-digit PR number did not match, want true")
	}
}

func TestEdgePRURLInRepoNotAPullPath(t *testing.T) {
	prefix := "github.com/carl/kraken"
	for _, url := range []string{
		"https://github.com/carl/kraken/issues/3",
		"https://github.com/carl/kraken/pulls/3",
		"https://github.com/carl/kraken/sub/pull/3",
	} {
		if PRURLInRepo(url, prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = true, want false", url, prefix)
		}
	}
}

func TestEdgePRURLInRepoRequiresHTTPSScheme(t *testing.T) {
	prefix := "github.com/carl/kraken"
	for _, url := range []string{
		"http://github.com/carl/kraken/pull/3",
		"github.com/carl/kraken/pull/3",
		"//github.com/carl/kraken/pull/3",
	} {
		if PRURLInRepo(url, prefix) {
			t.Errorf("PRURLInRepo(%q, %q) = true, want false (url must be https:// + prefix)", url, prefix)
		}
	}
}

func TestEdgeJobRepoOriginPrefixNilJob(t *testing.T) {
	prefix, ok := JobRepoOriginPrefix(nil)
	if ok || prefix != "" {
		t.Fatalf("JobRepoOriginPrefix(nil) = (%q, %v), want (\"\", false)", prefix, ok)
	}
}

func TestEdgeJobRepoOriginPrefixBlankRepo(t *testing.T) {
	prEdgeRepo(t, "kraken", "https://github.com/carl/kraken.git")
	for _, repo := range []string{"", "   "} {
		prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-blank", Repo: repo})
		if ok || prefix != "" {
			t.Errorf("repo %q: got (%q, %v), want (\"\", false)", repo, prefix, ok)
		}
	}
}

func TestEdgeJobRepoOriginPrefixUnresolvableRepo(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/Developer at all
	prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-unres", Repo: "kraken-edge-no-such-repo"})
	if ok || prefix != "" {
		t.Fatalf("unresolvable repo: got (%q, %v), want (\"\", false)", prefix, ok)
	}
}

func TestEdgeJobRepoOriginPrefixNoOriginRemote(t *testing.T) {
	prEdgeRepo(t, "kraken", "")
	prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-noorigin", Repo: "kraken"})
	if ok || prefix != "" {
		t.Fatalf("checkout with no origin: got (%q, %v), want (\"\", false)", prefix, ok)
	}
}

func TestEdgeJobRepoOriginPrefixOnlyNonOriginRemote(t *testing.T) {
	prEdgeRepo(t, "kraken", "")
	dir := filepath.Join(os.Getenv("HOME"), "Developer", "kraken")
	if out, err := exec.Command("git", "-C", dir, "remote", "add", "upstream", "https://github.com/carl/kraken.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add upstream: %v: %s", err, out)
	}
	prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-upstream", Repo: "kraken"})
	if ok || prefix != "" {
		t.Fatalf("checkout with only an upstream remote: got (%q, %v), want (\"\", false) — only origin counts", prefix, ok)
	}
}

func TestEdgeJobRepoOriginPrefixSSHOrigin(t *testing.T) {
	prEdgeRepo(t, "kraken", "git@github.com:carl/kraken.git")
	prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-ssh", Repo: "kraken"})
	if !ok || prefix != "github.com/carl/kraken" {
		t.Fatalf("ssh origin: got (%q, %v), want (github.com/carl/kraken, true)", prefix, ok)
	}
}

func TestEdgeJobRepoOriginPrefixSSHOriginNoGitSuffix(t *testing.T) {
	prEdgeRepo(t, "kraken", "git@github.com:carl/kraken")
	prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-ssh2", Repo: "kraken"})
	if !ok || prefix != "github.com/carl/kraken" {
		t.Fatalf("ssh origin without .git: got (%q, %v), want (github.com/carl/kraken, true)", prefix, ok)
	}
}

func TestEdgeJobRepoOriginPrefixHTTPSGitSuffixedOrigin(t *testing.T) {
	prEdgeRepo(t, "kraken", "https://github.com/carl/kraken.git")
	prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-https", Repo: "kraken"})
	if !ok || prefix != "github.com/carl/kraken" {
		t.Fatalf("https .git origin: got (%q, %v), want (github.com/carl/kraken, true)", prefix, ok)
	}
}

func TestEdgeJobRepoOriginPrefixMatchesOriginToPrefix(t *testing.T) {
	for _, origin := range []string{
		"https://github.com/carl/kraken",
		"https://github.com/carl/kraken.git",
		"git@github.com:carl/kraken.git",
	} {
		prEdgeRepo(t, "kraken", origin)
		prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-norm", Repo: "kraken"})
		if !ok || prefix != originToPrefix(origin) {
			t.Errorf("origin %q: got (%q, %v), want (%q, true) — same normalization as originToPrefix", origin, prefix, ok, originToPrefix(origin))
		}
	}
}

func TestEdgeJobRepoOriginPrefixMixedCaseOriginStillMatchesLowercasePR(t *testing.T) {
	prEdgeRepo(t, "kraken", "git@github.com:Carl/Kraken.git")
	prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-case", Repo: "kraken"})
	if !ok || !strings.EqualFold(prefix, "github.com/carl/kraken") {
		t.Fatalf("mixed-case origin: got (%q, %v), want github.com/Carl/Kraken (any case), true", prefix, ok)
	}
	if !PRURLInRepo("https://github.com/carl/kraken/pull/7", prefix) {
		t.Errorf("lowercase PR URL did not match mixed-case origin prefix %q", prefix)
	}
}

func TestEdgeJobRepoOriginPrefixReadsTheJobsRepoNotAnother(t *testing.T) {
	prEdgeRepo(t, "kraken", "https://github.com/carl/kraken.git")
	other := filepath.Join(os.Getenv("HOME"), "Developer", "kraken-ios")
	if out, err := exec.Command("git", "init", "-q", other).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	if out, err := exec.Command("git", "-C", other, "remote", "add", "origin", "https://github.com/carl/kraken-ios.git").CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v: %s", err, out)
	}
	if prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-a", Repo: "kraken"}); !ok || prefix != "github.com/carl/kraken" {
		t.Errorf("repo kraken: got (%q, %v), want github.com/carl/kraken", prefix, ok)
	}
	if prefix, ok := JobRepoOriginPrefix(&Job{Slug: "edge-b", Repo: "kraken-ios"}); !ok || prefix != "github.com/carl/kraken-ios" {
		t.Errorf("repo kraken-ios: got (%q, %v), want github.com/carl/kraken-ios", prefix, ok)
	}
}

// prEdgeHook drives PrHook for a slug against a stub serve that answers
// every request with status + body, returning everything PrHook wrote to
// stderr.
func prEdgeHook(t *testing.T, slug, url string, status int, body string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"` + url + `\n"}}`
	var stderr string
	withStdin(t, payload, func() {
		stderr = withStderrCapture(t, func() { PrHook(slug) })
	})
	return stderr
}

func TestEdgePrHook409JSONBodyPrintsErrorField(t *testing.T) {
	url := "https://github.com/carl/other/pull/4"
	stderr := prEdgeHook(t, "edge-409", url, http.StatusConflict,
		`{"error":"PR is in github.com/carl/other, job repo is github.com/carl/kraken"}`)
	want := "pr-hook edge-409: NOT attached (outside the job's repo): " + url +
		" — PR is in github.com/carl/other, job repo is github.com/carl/kraken"
	if got := strings.TrimRight(stderr, "\n"); got != want {
		t.Fatalf("stderr = %q, want exactly one line %q", stderr, want)
	}
}

func TestEdgePrHook409NonJSONBodyPrintsRawTrimmed(t *testing.T) {
	url := "https://github.com/carl/other/pull/5"
	stderr := prEdgeHook(t, "edge-409raw", url, http.StatusConflict, "  outside the repo, sorry \n\n")
	want := "pr-hook edge-409raw: NOT attached (outside the job's repo): " + url + " — outside the repo, sorry"
	if got := strings.TrimRight(stderr, "\n"); got != want {
		t.Fatalf("stderr = %q, want exactly one line %q", stderr, want)
	}
}

func TestEdgePrHook409IsNotReportedAsUnreachableOrAttached(t *testing.T) {
	url := "https://github.com/carl/other/pull/6"
	stderr := prEdgeHook(t, "edge-409not", url, http.StatusConflict, `{"error":"nope"}`)
	if strings.Contains(stderr, "PR NOT RECORDED") || strings.Contains(stderr, "serve unreachable") {
		t.Errorf("409 reported as serve unreachable: %q", stderr)
	}
	if strings.Contains(stderr, "(via kraken serve)") {
		t.Errorf("409 reported as attached: %q", stderr)
	}
	if n := strings.Count(strings.TrimRight(stderr, "\n"), "\n") + 1; n != 1 {
		t.Errorf("stderr has %d lines, want exactly 1: %q", n, stderr)
	}
}

func TestEdgePrHook500KeepsUnreachableLine(t *testing.T) {
	url := "https://github.com/carl/kraken/pull/8"
	stderr := prEdgeHook(t, "edge-500", url, http.StatusInternalServerError, `{"error":"boom"}`)
	want := "pr-hook edge-500: PR NOT RECORDED (serve unreachable): " + url
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to carry %q", stderr, want)
	}
	if strings.Contains(stderr, "outside the job's repo") {
		t.Errorf("500 reported as an outside-repo refusal: %q", stderr)
	}
}

func TestEdgePrHookOtherNon200KeepsUnreachableLine(t *testing.T) {
	// 404 (unknown slug at serve) and 400 are "any other non-200" too.
	for _, status := range []int{http.StatusNotFound, http.StatusBadRequest} {
		url := "https://github.com/carl/kraken/pull/10"
		stderr := prEdgeHook(t, "edge-non200", url, status, `{"error":"x"}`)
		want := "pr-hook edge-non200: PR NOT RECORDED (serve unreachable): " + url
		if !strings.Contains(stderr, want) {
			t.Errorf("status %d: stderr = %q, want it to carry %q", status, stderr, want)
		}
	}
}

func TestEdgePrHook200StillPrintsAttachedLine(t *testing.T) {
	url := "https://github.com/carl/kraken/pull/12"
	stderr := prEdgeHook(t, "edge-200", url, http.StatusOK, `{"ok":true,"attached":true}`)
	want := "pr-hook edge-200: attached " + url + " (via kraken serve)"
	if !strings.Contains(stderr, want) {
		t.Fatalf("stderr = %q, want it to carry %q", stderr, want)
	}
}
