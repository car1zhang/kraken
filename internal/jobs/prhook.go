package jobs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

// prhook.go — `kraken pr-hook <slug>`, wired as a Claude Code PostToolUse
// hook in each cell's worktree (jobs/cell.go writes the hook config). Zeus
// runs `gh pr create` itself as a normal Bash tool call; this verb is how
// the resulting PR gets attached to the job's prs: record in real time,
// instead of waiting for the daemon's branch-list poll (trackPRs in
// daemon.go) to notice it. It is machinery, not a user command — kept out of
// `k help` like _complete.
//
// A hook that fails a zeus tool call is a bug, not a safety net: PrHook
// exits 0 unconditionally and reports every problem to stderr only.

// prURLPattern matches a GitHub PR URL as gh pr create prints it:
// https://github.com/<owner>/<repo>/pull/<digits>.
var prURLPattern = regexp.MustCompile(`https://github\.com/[\w.-]+/[\w.-]+/pull/\d+`)

// hookPayload is the subset of Claude Code's PostToolUse payload PrHook
// cares about. Parsed defensively: the hook schema is owned by Claude Code,
// not kraken, and can drift out from under this file at any time.
type hookPayload struct {
	ToolName  string `json:"tool_name"`
	ToolInput struct {
		Command string `json:"command"`
	} `json:"tool_input"`
	ToolOutput   json.RawMessage `json:"tool_output"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

// hookOutput pulls the stdout/stderr pair out of a tool_output (or
// tool_response) field that may be either {"stdout":...,"stderr":...} or a
// plain string — schema drift must not crash the hook. prscan.go also calls
// this to read a transcript line's top-level toolUseResult field: same dual
// shape, just observed from the transcript instead of the hook's stdin — a
// coupling worth knowing from both ends if this shape ever changes.
func hookOutput(raw json.RawMessage) (stdout, stderr string) {
	if len(raw) == 0 {
		return "", ""
	}
	var obj struct {
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if json.Unmarshal(raw, &obj) == nil && (obj.Stdout != "" || obj.Stderr != "") {
		return obj.Stdout, obj.Stderr
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, ""
	}
	return "", ""
}

// extractPRURLs finds GitHub PR URLs created by this hook call: raw is one
// PostToolUse payload, prefix (e.g. "github.com/carl/kraken") restricts hits
// to that owner/repo when non-empty. Only a Bash call that INVOKES
// `gh pr create` is considered — gated by ghPrCreatePattern (declared in
// prscan.go and shared by both callers: this hook and the transcript
// scanner) rather than a plain substring check, since PostToolUse fires on
// every Bash call including ones that merely mention the phrase, e.g.
// `grep -rn "gh pr create" jobs/prpoll.go`; that grep's own stdout can carry
// another job's PR URL verbatim, which a substring check would misattach.
// Once gated, searches stdout first, falling back to stderr when stdout
// yields nothing — gh pr create prints the URL on stdout, but a re-create
// against a branch that already has an open PR prints "already exists:
// <URL>" on stderr instead. It deliberately does NOT fall back to the whole
// raw payload: raw includes the command text itself, so a foreign PR URL
// quoted in e.g. `gh pr create --body "…see …/pull/36…"` must never
// false-positively attach. A PR this misses because neither stream carried
// the URL is still caught within about a minute by the create-call
// transcript scanner and the branch-list poll (trackPRs in daemon.go), so
// staying strict here costs nothing but latency.
//
// Pure and side-effect-free so it's table-testable independent of gh/stdin.
func extractPRURLs(raw []byte, prefix string) []string {
	var p hookPayload
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if p.ToolName != "Bash" || !ghPrCreatePattern.MatchString(p.ToolInput.Command) {
		return nil
	}
	stdout, stderr := hookOutput(p.ToolOutput)
	if stdout == "" && stderr == "" {
		stdout, stderr = hookOutput(p.ToolResponse)
	}
	for _, src := range []string{stdout, stderr} {
		if urls := matchPRURLs(src, prefix); len(urls) > 0 {
			return urls
		}
	}
	return nil
}

// matchPRURLs finds every distinct PR URL in text under prefix (all URLs
// when prefix is empty), in first-seen order.
func matchPRURLs(text, prefix string) []string {
	var urls []string
	seen := map[string]bool{}
	for _, m := range prURLPattern.FindAllString(text, -1) {
		if prefix != "" && !strings.HasPrefix(m, "https://"+prefix+"/pull/") {
			continue
		}
		if seen[m] {
			continue
		}
		seen[m] = true
		urls = append(urls, m)
	}
	return urls
}

// originToPrefix normalizes a `git config remote.origin.url` value into a
// "github.com/owner/repo" prefix: strips a trailing .git and rewrites the
// git@github.com:owner/repo SSH form into the same host/path shape as https.
func originToPrefix(origin string) string {
	origin = strings.TrimSpace(origin)
	origin = strings.TrimSuffix(origin, ".git")
	origin = strings.TrimPrefix(origin, "https://")
	origin = strings.TrimPrefix(origin, "http://")
	if rest, ok := strings.CutPrefix(origin, "git@"); ok {
		origin = strings.Replace(rest, ":", "/", 1)
	}
	return origin
}

// repoOriginPrefix reads dir's origin remote and normalizes it. ok=false
// when the remote can't be read (no origin, not a repo, git missing) — the
// caller fails open in that case, since the command still ran inside the
// cell's own worktree.
func repoOriginPrefix(dir string) (string, bool) {
	out, err := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	if err != nil {
		return "", false
	}
	return originToPrefix(string(out)), true
}

// githubRepoPrefix is the only prefix shape JobRepoOriginPrefix vouches for.
// An origin originToPrefix cannot reduce to it — an `ssh://` URL, an ssh host
// alias, a trailing slash, a non-GitHub host — is an UNKNOWN repo rather than
// a known one that matches nothing: vouching for a prefix no PR URL can ever
// equal would make the attach route refuse every real PR of that checkout,
// the exact loss failing open exists to prevent.
var githubRepoPrefix = regexp.MustCompile(`^github\.com/[\w.-]+/[\w.-]+$`)

// JobRepoOriginPrefix is a job's own GitHub repo in originToPrefix's shape
// (`github.com/<owner>/<repo>`): j.Repo resolved to its checkout under
// ~/Developer, that checkout's origin read, a userinfo on an https origin
// (`https://<user>@github.com/…`, how a second GitHub account is often
// cloned) stripped so that form stays guarded. ok=false — and "unknown" is
// the whole answer, never an error — when j is nil, j.Repo is blank or
// resolves to no checkout, the checkout has no readable origin, or the
// origin does not reduce to githubRepoPrefix's shape. Callers fail OPEN on
// !ok: the one caller today is /api/pr-attach's repo check, where an unknown
// repo must keep attaching rather than start dropping real PRs. Known
// limit: a checkout whose origin is a fork, or names a repo since renamed on
// GitHub, vouches for the origin's owner/repo, so a PR opened against the
// upstream or the new name is refused there.
func JobRepoOriginPrefix(j *Job) (prefix string, ok bool) {
	if j == nil || strings.TrimSpace(j.Repo) == "" {
		return "", false
	}
	dir, err := ResolveRepo(j.Repo)
	if err != nil {
		return "", false
	}
	p, ok := repoOriginPrefix(dir)
	if !ok {
		return "", false
	}
	if at := strings.Index(p, "@"); at >= 0 && !strings.Contains(p[:at], "/") {
		p = p[at+1:] // https userinfo: `user@github.com/o/r` → `github.com/o/r`
	}
	if !githubRepoPrefix.MatchString(p) {
		return "", false
	}
	return p, true
}

// PRURLInRepo reports whether url is a pull request of the repo prefix
// names (`github.com/<owner>/<repo>`, originToPrefix's shape): exactly
// `https://<prefix>/pull/<digits>`, compared case-insensitively because
// GitHub treats `Owner/Repo` and `owner/repo` as one repo. An empty prefix
// matches nothing — an unknown repo is the caller's decision, not a
// wildcard — and the prefix is not normalized here: `carl/kraken` does not
// cover `carl/kraken-ios`, and a prefix carrying a scheme, a trailing slash
// or `.git` simply fails to match.
func PRURLInRepo(url, prefix string) bool {
	if prefix == "" {
		return false
	}
	rest, ok := strings.CutPrefix(strings.ToLower(url), "https://"+strings.ToLower(prefix)+"/pull/")
	if !ok || rest == "" {
		return false
	}
	for _, r := range rest {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// defaultServeAddr is kraken serve's default bind address (matches
// cmd_serve.go's own "7777" default) — the fallback when
// KRAKEN_SERVE_ADDR isn't set.
const defaultServeAddr = "127.0.0.1:7777"

// ServeBaseURL is the localhost base URL kraken serve's HTTP API is
// expected at: KRAKEN_SERVE_ADDR overrides for tests/non-default ports,
// else defaultServeAddr. Exported (L8/S12) so PrHook (below) and
// cmdLogEvent (cmd_jobs.go) share this ONE implementation instead of each
// hand-rolling the same "env override, else 127.0.0.1:7777" fallback —
// they'd drifted out of sync once (cmd_jobs.go's copy never learned the
// hook-addr propagation E7 added here).
func ServeBaseURL() string {
	if a := os.Getenv("KRAKEN_SERVE_ADDR"); a != "" {
		// a human typing KRAKEN_SERVE_ADDR=http://127.0.0.1:7777 by hand is
		// the common case for these cell-side verbs (unlike the daemon,
		// which always sets a bare host:port) — strip a scheme the caller
		// already included so it isn't doubled into http://http://....
		a = strings.TrimPrefix(a, "https://")
		a = strings.TrimPrefix(a, "http://")
		return "http://" + a
	}
	return "http://" + defaultServeAddr
}

// hookServeAddr (E7) is the bind address kraken serve actually resolved —
// which can differ from a naive default guess (--tailnet, or "-p 0"
// letting the OS pick a port) — recorded via SetHookServeAddr once at serve
// startup and read by cell.go's krakenHookEntry when installing a cell's
// PostToolUse hook. A pr-hook invocation runs as a brand-new OS process
// spawned by that hook (not a goroutine in kraken serve's own process), so
// it inherits none of serve's flags or in-memory state — only whatever the
// hook's command string bakes in — which is exactly why this can't just be
// "read ServeBaseURL() from inside StartCell": StartCell/installHookSettings
// run in kraken serve's process, but the hook COMMAND runs in an entirely
// different one, later, inside the cell.
var hookServeAddr atomic.Pointer[string]

// SetHookServeAddr records addr for every StartCell/ReactivateJob hook
// install for the rest of this process's lifetime. cmd_serve.go calls this
// once, right after net.Listen resolves the real bind address; a process
// that never calls it leaves hooks on defaultServeAddr. A package-level
// setter rather than threading addr through every StartCell caller's
// signature.
func SetHookServeAddr(addr string) {
	hookServeAddr.Store(&addr)
}

// currentHookServeAddr returns what SetHookServeAddr last recorded, or ""
// if it was never called.
func currentHookServeAddr() string {
	if p := hookServeAddr.Load(); p != nil {
		return *p
	}
	return ""
}

// prAttachHTTPTimeout bounds the POST to kraken serve: a hook call blocks a
// zeus tool turn, so this must fail fast (server down, port unreachable)
// rather than hang the agent waiting on a dead connection.
const prAttachHTTPTimeout = 3 * time.Second

// postPRAttach POSTs one {slug,url} pair to kraken serve's /api/pr-attach —
// the new localhost mutation endpoint that replaced this hook's direct file/
// store write (spec: "localhost mutation API additions"). Returns true only
// on a 200 response; any error (server down, timeout, non-200) is false, so
// the caller can fall back to the direct write.
func postPRAttach(slug, url string) bool {
	status, _ := postPRAttachStatus(slug, url)
	return status == http.StatusOK
}

// postPRAttachStatus is postPRAttach with the answer kept: the HTTP status
// (0 on a transport failure) and the response body, so PrHook can tell a
// serve that REFUSED the PR (409, the repo check in api/prattach.go) from a
// serve it never reached, and print the refusal's reason in the pane.
func postPRAttachStatus(slug, url string) (status int, body string) {
	payload, err := json.Marshal(map[string]string{"slug": slug, "url": url})
	if err != nil {
		return 0, ""
	}
	client := http.Client{Timeout: prAttachHTTPTimeout}
	resp, err := client.Post(ServeBaseURL()+"/api/pr-attach", "application/json", bytes.NewReader(payload))
	if err != nil {
		return 0, ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	return resp.StatusCode, string(b)
}

// serveErrorText is the `error` field of a serve JSON error body, or the
// trimmed raw body when it does not parse as one.
func serveErrorText(body string) string {
	var m struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &m); err == nil && m.Error != "" {
		return m.Error
	}
	return strings.TrimSpace(body)
}

// AttachJobPR is the PR-attach entry point exposed to the api package's
// /api/pr-attach handler: api sits above jobs in the dependency chain
// (util ← store ← core ← jobs ← api ← web) and can't reach the unexported
// appendJobPR directly, so this is a one-line exported wrapper.
func AttachJobPR(j *Job, url string) (attached bool, err error) {
	return appendJobPR(j, JobPR{URL: url, State: "open"})
}

// PrHook is `kraken pr-hook <slug>`'s body: read one JSON hook payload from
// stdin, and — when it's a successful `gh pr create` in the job's own
// repo — attach the printed PR URL(s) via a POST to kraken serve's
// /api/pr-attach (the sole DB writer, and the only path that needs nothing
// but slug+url — the server does its own job lookup). PrHook runs as its
// own `kraken pr-hook` CLI invocation, a separate process from kraken
// serve, so it never opens kraken.db itself (single-writer rule) — the
// local LoadJobBySlug call below exists solely to narrow extractPRURLs to
// the job's own repo via the origin prefix; a board-created job that lives
// only as a store row (no job file on disk) makes j nil here, and the POST
// attempt still fires, matching any repo (prefix == "") — which is every
// store-only job, i.e. every job created since file mode was retired, so
// the repo check that matters is the SERVER's: /api/pr-attach has the
// store, resolves the job's repo itself, and answers 409 for a PR of
// another repo (a cell that ran `gh pr create` in another repo's checkout
// attached that repo's PR to its own job, 2026-10-13). When the POST fails
// there is no local fallback — the PR shipped, but kraken doesn't know
// about it until serve comes back and the daemon's branch-list poll
// notices. Always returns (never os.Exit(1)); every outcome is a single
// stderr line: attached, NOT attached (the 409), or NOT RECORDED (any other
// answer, serve unreachable included).
func PrHook(slug string) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr-hook %s: read stdin: %v\n", slug, err)
		return
	}
	// tolerate j == nil: the POST needs only slug+url, and a store-only
	// (board-created) job has no file to load here at all
	j := LoadJobBySlug(slug)
	prefix := ""
	if j != nil {
		if dir, err := ResolveRepo(j.Repo); err == nil {
			if p, ok := repoOriginPrefix(dir); ok {
				prefix = p
			}
			// origin unreadable: fail open below with prefix == "" (matches
			// any repo) rather than silently dropping a PR the command
			// really made
		}
	}
	for _, url := range extractPRURLs(raw, prefix) {
		status, body := postPRAttachStatus(slug, url)
		switch status {
		case http.StatusOK:
			fmt.Fprintf(os.Stderr, "pr-hook %s: attached %s (via kraken serve)\n", slug, url)
		case http.StatusConflict:
			// serve's repo check: the PR is real, but it lives in another
			// repo than the job's, so it is not this job's record.
			fmt.Fprintf(os.Stderr, "pr-hook %s: NOT attached (outside the job's repo): %s — %s\n", slug, url, serveErrorText(body))
		default:
			fmt.Fprintf(os.Stderr, "pr-hook %s: PR NOT RECORDED (serve unreachable): %s\n", slug, url)
		}
	}
}
