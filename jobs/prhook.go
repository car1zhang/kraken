package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
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
// plain string — schema drift must not crash the hook.
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
// to that owner/repo when non-empty. Only a Bash "gh pr create" call is
// considered; searches stdout first, falling back to stderr and then the
// whole raw payload only when the prior source yields nothing — gh pr
// create prints the URL on stdout, but a captured-stderr-only variant or an
// unrecognized output shape shouldn't lose a PR that really got created.
// Pure and side-effect-free so it's table-testable independent of gh/stdin.
func extractPRURLs(raw []byte, prefix string) []string {
	var p hookPayload
	if json.Unmarshal(raw, &p) != nil {
		return nil
	}
	if p.ToolName != "Bash" || !strings.Contains(p.ToolInput.Command, "gh pr create") {
		return nil
	}
	stdout, stderr := hookOutput(p.ToolOutput)
	if stdout == "" && stderr == "" {
		stdout, stderr = hookOutput(p.ToolResponse)
	}
	for _, src := range []string{stdout, stderr, string(raw)} {
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

// PrHook is `kraken pr-hook <slug>`'s body: read one JSON hook payload from
// stdin, and — when it's a successful `gh pr create` in the job's own
// repo — attach the printed PR URL(s) via appendJobPR. Always returns
// (never os.Exit(1)); every failure is a single stderr line.
func PrHook(slug string) {
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr-hook %s: read stdin: %v\n", slug, err)
		return
	}
	j := LoadJobBySlug(slug)
	if j == nil {
		fmt.Fprintf(os.Stderr, "pr-hook %s: no such job\n", slug)
		return
	}
	prefix := ""
	if dir, err := ResolveRepo(j.Repo); err == nil {
		if p, ok := repoOriginPrefix(dir); ok {
			prefix = p
		}
		// origin unreadable: fail open below with prefix == "" (matches any
		// repo) rather than silently dropping a PR the command really made
	}
	for _, url := range extractPRURLs(raw, prefix) {
		attached, err := appendJobPR(j, JobPR{URL: url, State: "open"})
		switch {
		case err != nil:
			fmt.Fprintf(os.Stderr, "pr-hook %s: attach %s failed: %v\n", slug, url, err)
		case attached:
			fmt.Fprintf(os.Stderr, "pr-hook %s: attached %s\n", slug, url)
		}
	}
}
