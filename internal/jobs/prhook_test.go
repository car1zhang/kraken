package jobs

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"kraken/internal/store"
)

// prhook_test.go — the pr-hook verb's pure pieces: payload parsing/URL
// extraction (extractPRURLs never touches gh, stdin, or the store) and the
// shared attach path (appendJobPR) against a temp store.

func TestExtractPRURLs(t *testing.T) {
	const prefix = "github.com/carl/kraken"
	cases := []struct {
		name    string
		payload string
		prefix  string
		want    []string
	}{
		{
			name:    "gh pr create URL on stdout",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create --title x"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/42\n","stderr":""}}`,
			prefix:  prefix,
			want:    []string{"https://github.com/carl/kraken/pull/42"},
		},
		{
			name:    "tool_response string variant",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_response":"Creating PR...\nhttps://github.com/carl/kraken/pull/7\n"}`,
			prefix:  prefix,
			want:    []string{"https://github.com/carl/kraken/pull/7"},
		},
		{
			name:    "non-Bash tool is ignored",
			payload: `{"tool_name":"Read","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/1"}}`,
			prefix:  prefix,
			want:    nil,
		},
		{
			name:    "gh pr view must not match",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr view 42"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/42"}}`,
			prefix:  prefix,
			want:    nil,
		},
		{
			name:    "different repo filtered out",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/other/repo/pull/9"}}`,
			prefix:  prefix,
			want:    nil,
		},
		{
			name:    "empty prefix matches any repo",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/other/repo/pull/9"}}`,
			prefix:  "",
			want:    []string{"https://github.com/other/repo/pull/9"},
		},
		{
			name:    "multiple URLs",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/1\nhttps://github.com/carl/kraken/pull/2\n"}}`,
			prefix:  prefix,
			want:    []string{"https://github.com/carl/kraken/pull/1", "https://github.com/carl/kraken/pull/2"},
		},
		{
			name:    "malformed JSON does not panic",
			payload: `{not valid json`,
			prefix:  prefix,
			want:    nil,
		},
		{
			name:    "empty payload does not panic",
			payload: ``,
			prefix:  prefix,
			want:    nil,
		},
		{
			name:    "already-exists URL on stderr still attaches",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create --title x"},"tool_output":{"stdout":"","stderr":"a pull request for branch \"carl/yelp-dedupe\" into branch \"main\" already exists: https://github.com/carl/kraken/pull/55\n"}}`,
			prefix:  prefix,
			want:    []string{"https://github.com/carl/kraken/pull/55"},
		},
		{
			name:    "URL quoted in command body must not attach",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create --title x --body \"relates to https://github.com/carl/kraken/pull/36\""},"tool_output":{"stdout":"","stderr":""}}`,
			prefix:  prefix,
			want:    nil,
		},
		{
			name:    "stdout non-empty but URL-free falls back to stderr",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"Creating pull request for carl/x into main\n","stderr":"https://github.com/carl/kraken/pull/8\n"}}`,
			prefix:  prefix,
			want:    []string{"https://github.com/carl/kraken/pull/8"},
		},
		{
			name:    "tool_response object variant",
			payload: `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_response":{"stdout":"https://github.com/carl/kraken/pull/5\n","stderr":""}}`,
			prefix:  prefix,
			want:    []string{"https://github.com/carl/kraken/pull/5"},
		},
		{
			// PostToolUse fires on every Bash call, including ones that only
			// mention the phrase; a grep's own stdout can carry another job's
			// PR URL verbatim (the same mention-class false attach the
			// transcript scanner closed via ghPrCreatePattern) and must not
			// attach here either.
			name:    "grep mentioning the literal is not an invocation",
			payload: `{"tool_name":"Bash","tool_input":{"command":"grep -rn \"gh pr create\" ~/Documents/pluto"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/30\n","stderr":""}}`,
			prefix:  prefix,
			want:    nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractPRURLs([]byte(c.payload), c.prefix)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestOriginToPrefix(t *testing.T) {
	cases := []struct{ origin, want string }{
		{"https://github.com/carl/kraken.git", "github.com/carl/kraken"},
		{"https://github.com/carl/kraken", "github.com/carl/kraken"},
		{"git@github.com:carl/kraken.git", "github.com/carl/kraken"},
		{"git@github.com:carl/kraken", "github.com/carl/kraken"},
	}
	for _, c := range cases {
		if got := originToPrefix(c.origin); got != c.want {
			t.Errorf("originToPrefix(%q) = %q, want %q", c.origin, got, c.want)
		}
	}
}

func TestAppendJobPR(t *testing.T) {
	// no store: there is nowhere to record a PR, and appendJobPR says so
	if _, err := appendJobPR(ParseJob("yelp-dedupe.job.md", []byte(sampleJob)), JobPR{URL: "https://github.com/carl/kraken/pull/1", State: "open"}); err == nil {
		t.Fatal("appendJobPR with no store wired: want an error")
	}
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "yelp-dedupe", Repo: "monolith", Body: "body\n"})
	j := LoadJobBySlug("yelp-dedupe")

	attached, err := appendJobPR(j, JobPR{URL: "https://github.com/carl/kraken/pull/1", State: "open"})
	if err != nil || !attached {
		t.Fatalf("first append: attached=%v err=%v, want true, nil", attached, err)
	}

	reloaded := LoadJobBySlug("yelp-dedupe")
	if len(reloaded.PRs) != 1 || reloaded.PRs[0].URL != "https://github.com/carl/kraken/pull/1" || reloaded.PRs[0].State != "open" {
		t.Fatalf("prs after append: %+v", reloaded.PRs)
	}
	if reloaded.Body != "body\n" || reloaded.Repo != "monolith" {
		t.Fatal("appendJobPR clobbered other fields")
	}

	// dedup: re-attaching the same URL is a no-op
	attached2, err2 := appendJobPR(j, JobPR{URL: "https://github.com/carl/kraken/pull/1", State: "open"})
	if err2 != nil || attached2 {
		t.Fatalf("dedup append: attached=%v err=%v, want false, nil", attached2, err2)
	}

	// a distinct URL still appends alongside the first
	attached3, err3 := appendJobPR(j, JobPR{URL: "https://github.com/carl/kraken/pull/2", State: "open"})
	if err3 != nil || !attached3 {
		t.Fatalf("second append: attached=%v err=%v, want true, nil", attached3, err3)
	}
	if prs := LoadJobBySlug("yelp-dedupe").PRs; len(prs) != 2 {
		t.Fatalf("prs after second append: %+v", prs)
	}
}

// withStdin redirects os.Stdin to a pipe carrying data for the duration of
// fn — PrHook reads its payload from stdin, same as a real Claude Code
// PostToolUse hook invocation.
func withStdin(t *testing.T, data string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString(data); err != nil {
		t.Fatal(err)
	}
	w.Close()
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	fn()
}

func TestPostPRAttachSuccessAndFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	if !postPRAttach("x", "https://github.com/carl/kraken/pull/1") {
		t.Fatal("want true against a reachable, 200-returning server")
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing listens here anymore: connections refuse immediately
	t.Setenv("KRAKEN_SERVE_ADDR", addr)
	if postPRAttach("x", "https://github.com/carl/kraken/pull/1") {
		t.Fatal("want false against an unreachable address")
	}
}

func TestAttachJobPRWrapper(t *testing.T) {
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "yelp-dedupe", Repo: "monolith"})
	j := LoadJobBySlug("yelp-dedupe")

	attached, err := AttachJobPR(j, "https://github.com/carl/kraken/pull/3")
	if err != nil || !attached {
		t.Fatalf("AttachJobPR: attached=%v err=%v, want true, nil", attached, err)
	}
	attached2, err2 := AttachJobPR(j, "https://github.com/carl/kraken/pull/3")
	if err2 != nil || attached2 {
		t.Fatalf("AttachJobPR dedup: attached=%v err=%v, want false, nil", attached2, err2)
	}
}

// TestPrHookAttachesViaServerWhenAvailable covers the new POST-first path:
// a reachable /api/pr-attach must be used instead of writing the job file
// directly.
func TestPrHookAttachesViaServerWhenAvailable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yelp-dedupe.job.md")
	if err := os.WriteFile(path, []byte("---\njob: yelp-dedupe\ntype: build\nrepo: kraken-test-nonexistent-repo-xyz\nready: true\nprereqs: []\nsolo: false\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var gotBody []byte
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/pr-attach" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/9\n"}}`
	withStdin(t, payload, func() { PrHook("yelp-dedupe") })

	if hits != 1 {
		t.Fatalf("want exactly 1 POST to /api/pr-attach, got %d", hits)
	}
	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req["slug"] != "yelp-dedupe" || req["url"] != "https://github.com/carl/kraken/pull/9" {
		t.Fatalf("posted body = %+v", req)
	}

	// the server "attached" it, not this process directly — the job file on
	// disk must be untouched.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reparsed := ParseJob("yelp-dedupe.job.md", data); len(reparsed.PRs) != 0 {
		t.Fatalf("PrHook wrote the file directly despite a reachable server: %+v", reparsed.PRs)
	}
}

// withStderrCapture redirects os.Stderr to a pipe for the duration of fn and
// returns everything written to it — PrHook reports every outcome via a
// single stderr line, so tests can assert on it directly.
func withStderrCapture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestPrHookSkipsDirectWriteWhenServerUnreachable covers the fail-open
// contract: PrHook must return normally (never os.Exit(1)) when kraken
// serve can't be reached, reporting the loss loudly on stderr rather than
// falling back to writing the job file directly — that file isn't
// authoritative once serve owns the DB (single-writer rule), so a hook
// process must never write it.
func TestPrHookSkipsDirectWriteWhenServerUnreachable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yelp-dedupe.job.md")
	if err := os.WriteFile(path, []byte("---\njob: yelp-dedupe\ntype: build\nrepo: kraken-test-nonexistent-repo-xyz\nready: true\nprereqs: []\nsolo: false\n---\n\nbody\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", addr)

	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/9\n"}}`
	var stderr string
	withStdin(t, payload, func() {
		stderr = withStderrCapture(t, func() { PrHook("yelp-dedupe") })
	})

	if !strings.Contains(stderr, "PR NOT RECORDED") {
		t.Fatalf("stderr = %q, want a PR NOT RECORDED line", stderr)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if reparsed := ParseJob("yelp-dedupe.job.md", data); len(reparsed.PRs) != 0 {
		t.Fatalf("job file was written despite the POST failing: %+v", reparsed.PRs)
	}
}

// TestPrHookAttachesForStoreOnlyJob covers L1: a board-created job that
// exists only as a store row (no <slug>.job.md on disk at all) must still
// have its PR POSTed to a reachable kraken serve — LoadJobBySlug returning
// nil here must not block the attach attempt, since the POST only needs
// slug+url and the server does its own job lookup.
func TestPrHookAttachesForStoreOnlyJob(t *testing.T) {
	var gotBody []byte
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path != "/api/pr-attach" || r.Method != http.MethodPost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KRAKEN_SERVE_ADDR", strings.TrimPrefix(srv.URL, "http://"))

	payload := `{"tool_name":"Bash","tool_input":{"command":"gh pr create"},"tool_output":{"stdout":"https://github.com/carl/kraken/pull/11\n"}}`
	stderr := withStderrCapture(t, func() {
		withStdin(t, payload, func() { PrHook("store-only-job") })
	})

	if hits != 1 {
		t.Fatalf("want exactly 1 POST to /api/pr-attach despite no local job file, got %d", hits)
	}
	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatal(err)
	}
	if req["slug"] != "store-only-job" || req["url"] != "https://github.com/carl/kraken/pull/11" {
		t.Fatalf("posted body = %+v", req)
	}
	if !strings.Contains(stderr, "attached") {
		t.Fatalf("stderr = %q, want an attached line", stderr)
	}
}

func TestLoadJobBySlug(t *testing.T) {
	// no store wired: jobs live only in kraken.db, so nothing resolves
	if j := LoadJobBySlug("yelp-dedupe"); j != nil {
		t.Fatalf("LoadJobBySlug with no store = %+v, want nil", j)
	}
	st := useTestStore(t)
	mustCreateJob(t, st, store.Job{Slug: "yelp-dedupe", Repo: "monolith"})
	j := LoadJobBySlug("yelp-dedupe")
	if j == nil || j.Slug != "yelp-dedupe" || j.Repo != "monolith" {
		t.Fatalf("LoadJobBySlug: %+v", j)
	}
	// suffix tolerated
	if j := LoadJobBySlug("yelp-dedupe.job.md"); j == nil || j.Slug != "yelp-dedupe" {
		t.Fatalf("LoadJobBySlug with suffix: %+v", j)
	}
	// missing slug
	if j := LoadJobBySlug("no-such-job"); j != nil {
		t.Fatalf("LoadJobBySlug(missing) = %+v, want nil", j)
	}
}

// TestServeBaseURL covers KRAKEN_SERVE_ADDR's three shapes a human might
// type by hand (bare host:port, and either scheme already included) plus
// the unset default — a scheme already present must never be doubled into
// http://http://... (F11).
func TestServeBaseURL(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want string
	}{
		{"unset falls back to default", "", "http://" + defaultServeAddr},
		{"bare host:port", "127.0.0.1:7777", "http://127.0.0.1:7777"},
		{"http scheme already present", "http://127.0.0.1:7777", "http://127.0.0.1:7777"},
		{"https scheme already present", "https://127.0.0.1:7777", "http://127.0.0.1:7777"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("KRAKEN_SERVE_ADDR", c.env)
			if c.env == "" {
				os.Unsetenv("KRAKEN_SERVE_ADDR")
			}
			if got := ServeBaseURL(); got != c.want {
				t.Fatalf("ServeBaseURL() = %q, want %q", got, c.want)
			}
		})
	}
}

// TestPRRepoNamedInSpec pins the one legitimate way a PR may land outside the
// job's own checkout: the spec body names the PR's repo as a whole token, in
// any of the spellings an operator writes it in, and nothing else — a repo
// that merely shares a prefix or suffix with a named one is not named.
func TestPRRepoNamedInSpec(t *testing.T) {
	url := "https://github.com/other-org/mirror-core/pull/1"
	named := []string{
		"Open the PR against other-org/mirror-core when the template is ready.",
		"Re-root the mirror on github.com/other-org/mirror-core.",
		"Clone https://github.com/other-org/mirror-core.git first.",
		"(other-org/mirror-core)",
		"`other-org/mirror-core`",
		"Other-Org/Mirror-Core is the destination",
		"other-org/mirror-core",
	}
	for _, body := range named {
		if !PRRepoNamedInSpec(&Job{Body: body}, url) {
			t.Errorf("body %q should name other-org/mirror-core", body)
		}
	}
	unnamed := []string{
		"",
		"   ",
		"nothing about repos here",
		"other-org/mirror-core-ios is a different repo",
		"other-org/mirror-corelib",
		"other-org/mirror-core.ios is a dotted sibling",
		"use other-org/mirror-core.js for the web half",
		"not-other-org/mirror-core",
		"other-org/mirror",
		"mirror-core alone, no owner",
		"other-org/ mirror-core",
		"see https://github.com/other-org/mirror-core/pull/7 for prior art",
		"github.com/other-org/mirror-core/issues/3 describes the bug",
		"see https://github.com/other-org/mirror-core/PULL/7 (uppercase path)",
		"https://github.com/other-org/mirror-core.git/pull/7 is the clone-spelled citation",
		"other-org/mirror-core#12 tracks it",
	}
	for _, body := range unnamed {
		if PRRepoNamedInSpec(&Job{Body: body}, url) {
			t.Errorf("body %q must NOT name other-org/mirror-core", body)
		}
	}
	if !PRRepoNamedInSpec(&Job{Body: "other-org/mirror-core#12 tracks it; open the PR in other-org/mirror-core."}, url) {
		t.Error("a citation beside a real naming must not hide the naming")
	}
	if PRRepoNamedInSpec(nil, url) {
		t.Error("a nil job names nothing")
	}
	if PRRepoNamedInSpec(&Job{Body: "other-org/mirror-core"}, "https://github.com/other-org/mirror-core/pulls") {
		t.Error("a URL outside the pull-request shape names nothing")
	}
	if PRRepoNamedInSpec(&Job{Body: "other-org/mirror-core"}, "https://github.com/other-org/mirror-core/pull/1/files") {
		t.Error("a URL with a trailing path names nothing")
	}
}

// TestSpecNamedPRs pins what the restart repair may vouch for on the spec's
// word: recorded PRs of OTHER repos the spec names, never the job's own-repo
// PRs (a spec naming its own repo must not immunise every own-repo record
// against the repair), and nothing for a nil job.
func TestSpecNamedPRs(t *testing.T) {
	foreign := "https://github.com/other-org/mirror-core/pull/1"
	own := "https://github.com/carl/kraken/pull/5"
	prs := []JobPR{
		{URL: foreign, State: "open"},
		{URL: "https://github.com/other-org/mirror-core-ios/pull/2", State: "open"},
		{URL: own, State: "open"},
	}
	j := &Job{Body: "ship to other-org/mirror-core and carl/kraken", PRs: prs}
	if got := specNamedPRs(j, "github.com/carl/kraken"); len(got) != 1 || got[0] != foreign {
		t.Errorf("specNamedPRs = %v, want only the foreign spec-named url", got)
	}
	if got := specNamedPRs(j, ""); len(got) != 2 || got[0] != foreign || got[1] != own {
		t.Errorf("specNamedPRs with no own prefix = %v, want both spec-named urls", got)
	}
	if got := specNamedPRs(nil, "github.com/carl/kraken"); got != nil {
		t.Errorf("specNamedPRs(nil) = %v, want nil", got)
	}
	// The repair hands specNamedPRs the raw origin prefix through githubPrefix:
	// a userinfo origin must still exclude the job's own PRs.
	if own, ok := githubPrefix("carl@github.com/carl/kraken"); !ok || own != "github.com/carl/kraken" {
		t.Fatalf("githubPrefix(userinfo) = %q, %v", own, ok)
	} else if got := specNamedPRs(j, own); len(got) != 1 || got[0] != foreign {
		t.Errorf("specNamedPRs via a userinfo origin = %v, want only the foreign url", got)
	}
	if _, ok := githubPrefix("gitlab.com/carl/kraken"); ok {
		t.Error("a non-GitHub prefix must not normalise to a GitHub one")
	}
}
