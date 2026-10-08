package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// cmd_job.go — `k job list|raw|draft`: iris' surface on the job board,
// proxying to kraken serve's GET /api/job/list, the existing GET
// /api/job/raw, and POST /api/job/draft (internal/api/jobdraft.go).
//
// The three verbs are one loop: see what jobs exist, read the one this Slack
// message might belong to, then either create a draft or amend that job's
// text. `raw` reuses the board editor's own route untouched — the text iris
// edits is byte-identical to the text carl sees in the raw editor, which is
// the point: an agent that edited some other projection of a job could
// produce a job file carl never reviewed.
//
// `draft` is the guarded one, and the guarding is SERVER-side (see
// jobdraft.go's refusal matrix): it can only ever produce a draft — never a
// started job, never an edit to a job whose cell is live. Nothing in this
// file enforces that, deliberately. A CLI check is a suggestion; the route is
// the gate.
//
// This does not reopen the job LIFECYCLE on the CLI (Q14, and cmd_jobs.go's
// header): starting, completing, archiving and reactivating a job remain
// board-only, and nothing here can reach any of them — `draft` is refused on
// every started job, and starting one is now the board's submit action, which
// has no CLI spelling at all. These are agent-facing HTTP proxies in the
// `k questions` / `k load` family, not job verbs.
//
// Same behavior contract as cmd_load.go / cmd_slack.go: reads print the
// server's JSON to stdout verbatim; a write confirms on stderr (and `draft`
// additionally prints the server's JSON, which carries the new lock token);
// a 4xx prints the server's own sentence; an unreachable serve names the
// address; bad args print usage. It NEVER opens kraken.db — only kraken serve
// writes it.
func cmdJob(args []string) {
	if len(args) == 0 {
		util.Die("usage: k job list|raw|draft ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "list":
		out, err := runJobList(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	case "raw":
		out, err := runJobRaw(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	case "draft":
		slug, mtime, err := parseJobDraftArgs(rest)
		if err != nil {
			util.Die("%v", err)
		}
		out, err := runJobDraft(slug, mtime, os.Stdin)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
		fmt.Fprintf(os.Stderr, "job %s written as a draft — the new mtime is in the JSON above; queueing it is carl's move on the board\n", slug)
	default:
		util.Die("usage: k job list|raw|draft ... (unknown op %q)", op)
	}
}

// jobDraftUsage is the one usage string every bad-args path of `draft`
// prints, so the stdin contract and the optional lock token stay visible
// whichever check fails.
const jobDraftUsage = "usage: k job draft <slug> [--mtime N] (full job text — frontmatter + spec — on stdin)"

// runJobList is `k job list`: GET /api/job/list — prints {"jobs": [...]}
// (every active job's slug, state and one-line summary) straight through. It
// takes no arguments: the list is short by construction and a filter here
// would only be a way to miss the job this message already belongs to.
func runJobList(args []string) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("usage: k job list (no arguments)")
	}
	return qfGet("/api/job/list")
}

// runJobRaw is `k job raw <slug>`: GET /api/job/raw?slug= — prints
// {"slug", "content", "mtime", "editable"} straight through. `mtime` is the
// optimistic-lock token to hand back to `draft` on an edit, which is why
// reading before editing is the normal path rather than an optimization.
func runJobRaw(args []string) ([]byte, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return nil, fmt.Errorf("usage: k job raw <slug>")
	}
	return qfGet("/api/job/raw?slug=" + url.QueryEscape(strings.TrimSpace(args[0])))
}

// parseJobDraftArgs reads `<slug> [--mtime N]`. Both `--mtime N` and
// `--mtime=N` are accepted; anything else is a usage error rather than a
// silently ignored argument, because the one argument that can be dropped
// silently here is the LOCK TOKEN — and a dropped lock token turns a refused
// clobber into a successful one.
//
// A missing --mtime is legal and means 0, which the server reads as "skip the
// conflict check" (the raw save's contract). That is the right default for a
// CREATE, where there is nothing to conflict with; on an edit iris is
// expected to pass the mtime it just read.
func parseJobDraftArgs(args []string) (string, int64, error) {
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return "", 0, fmt.Errorf("%s", jobDraftUsage)
	}
	slug := strings.TrimSpace(args[0])
	var mtime int64
	var haveMTime bool
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, raw, inline := strings.Cut(a, "=")
		if !inline {
			name = a
			if name != "--mtime" {
				return "", 0, fmt.Errorf("%s (unexpected argument %q)", jobDraftUsage, a)
			}
			if i+1 >= len(args) {
				return "", 0, fmt.Errorf("%s (--mtime needs a value)", jobDraftUsage)
			}
			raw = args[i+1]
			i++
		}
		if name != "--mtime" {
			return "", 0, fmt.Errorf("%s (unexpected argument %q)", jobDraftUsage, a)
		}
		// A repeated flag is a usage error rather than last-one-wins, for the
		// reason an unknown one is: the argument that gets silently dropped
		// here is the LOCK TOKEN, and a dropped lock token turns a refused
		// clobber into a successful one.
		if haveMTime {
			return "", 0, fmt.Errorf("%s (--mtime given twice)", jobDraftUsage)
		}
		haveMTime = true
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || n < 0 {
			return "", 0, fmt.Errorf("bad --mtime %q: want the number `k job raw %s` printed (%s)", raw, slug, jobDraftUsage)
		}
		mtime = n
	}
	return slug, mtime, nil
}

// runJobDraft reads the whole job text from stdin and posts it. Stdin rather
// than an argument for the reason the questions verbs use it: a job spec is
// multi-line prose full of quotes, backticks and unicode, and shell-quoting
// mangles exactly that. An interactive terminal is refused rather than waited
// on — a human running this by hand with nothing piped in would otherwise
// block forever with no indication why (iris is unaffected: its non-TTY
// stdin EOFs immediately either way).
//
// The text is forwarded VERBATIM, never parsed or normalized here: the server
// parses it with the strict job parser, and a second, looser reading of the
// same bytes in this process is exactly how a draft ends up meaning one thing
// to the CLI and another to the row it becomes.
func runJobDraft(slug string, mtime int64, stdin io.Reader) ([]byte, error) {
	if stdinIsInteractiveTTY(stdin) {
		return nil, fmt.Errorf("%s — refusing to wait on an interactive terminal; pipe the job text in instead", jobDraftUsage)
	}
	raw, err := io.ReadAll(stdin)
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, fmt.Errorf("%s (stdin was empty)", jobDraftUsage)
	}
	return qfPost("/api/job/draft", map[string]any{
		"slug": slug, "content": string(raw), "mtime": mtime,
	})
}
