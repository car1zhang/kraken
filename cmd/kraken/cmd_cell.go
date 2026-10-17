package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmd_cell.go — `k cell steer|submit|answer <slug>`: an agent-side control
// surface on a job cell, proxying to the BOARD's own routes — POST /api/steer,
// POST /api/queue (the submit), POST /api/question and POST /api/doc-question
// (internal/api/actions.go).
//
// Reusing the board's routes rather than adding agent-shaped ones is the
// load-bearing choice here. Those four are the same doors carl's own clicks
// go through, so every guard they already carry — the submit ladder's fixed
// refusal order and its per-job mutex, steer's live-cell check, the doc
// answer's cap — applies to an agent caller unchanged, and there is no
// second, weaker door onto a live cell that could drift out of step with the
// first. What this file adds is only the CLI shape and the audit trail.
//
// Every op logs to the unified event log on SUCCESS, with src `cell`, so
// "who steered this cell?" has an answer that does not depend on the caller
// saying so. The log post is best-effort: the action has already landed by
// then, and failing the verb over an unrecorded audit line would tell the
// caller the steer did not happen when it did.
//
// Same behavior contract as the rest of the proxy family (cmd_slack.go's
// header states it in full): writes print one short confirmation line to
// STDERR and nothing to stdout, a 4xx prints the server's own sentence to
// stderr and exits non-zero — which is what makes `submit`'s 409 useful, since
// the ladder's refusal names which rung it stopped on. It NEVER opens
// kraken.db.
func cmdCell(args []string) {
	if len(args) == 0 {
		util.Die("usage: k cell steer|submit|answer <slug> ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "steer":
		slug, text, err := parseCellSteer(rest, os.Stdin)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runCellSteer(slug, text); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "steered %s\n", slug)
	case "submit":
		slug, err := parseCellSlugOnly(rest, cellSubmitUsage)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runCellSubmit(slug); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "submitted %s — its cell is starting\n", slug)
	case "answer":
		slug, digit, question, err := parseCellAnswer(rest)
		if err != nil {
			util.Die("%v", err)
		}
		text := digit
		if question != "" {
			text, err = readCellAnswerText(os.Stdin)
			if err != nil {
				util.Die("%v", err)
			}
		}
		if err := runCellAnswer(slug, digit, question, text); err != nil {
			util.Die("%v", err)
		}
		if question != "" {
			fmt.Fprintf(os.Stderr, "answered %s %s\n", slug, question)
		} else {
			fmt.Fprintf(os.Stderr, "answered %s with option %s\n", slug, digit)
		}
	default:
		util.Die("usage: k cell steer|submit|answer <slug> ... (unknown op %q)", op)
	}
}

const (
	cellSteerUsage  = "usage: k cell steer <slug>  (the message on stdin)"
	cellSubmitUsage = "usage: k cell submit <slug>"
	cellAnswerUsage = `usage: k cell answer <slug> --digit N   (an AskUserQuestion option, 1-9)
   or: k cell answer <slug> --question Qn   (a doc question; the answer on stdin)`
)

// parseCellSlugOnly reads the one bare slug the simple ops take. An extra
// argument is a usage error rather than an ignored one: `k cell submit foo
// --now` should say what it did not understand, not quietly submit foo.
func parseCellSlugOnly(args []string, usage string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("%s", usage)
	}
	if len(args) > 1 {
		return "", fmt.Errorf("%s (unexpected argument %q)", usage, args[1])
	}
	slug := strings.TrimSpace(args[0])
	if slug == "" {
		return "", fmt.Errorf("%s (the slug is empty)", usage)
	}
	return slug, nil
}

// parseCellSteer reads the slug and takes the message from STDIN — never from
// arguments, unlike `k s`. A steer is prose written for a claude to read: it
// carries newlines, quotes and unicode that shell-quoting mangles, and the
// same argument that puts `k questions add`'s payload on stdin puts this
// there. An interactive TTY is refused rather than waited on
// (stdinIsInteractiveTTY, qfclient.go) so a human running it by hand does not
// block forever with no output.
func parseCellSteer(args []string, stdin io.Reader) (string, string, error) {
	slug, err := parseCellSlugOnly(args, cellSteerUsage)
	if err != nil {
		return "", "", err
	}
	if stdinIsInteractiveTTY(stdin) {
		return "", "", fmt.Errorf("%s — refusing to wait on an interactive terminal; pipe the message in", cellSteerUsage)
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", "", fmt.Errorf("read stdin: %w", err)
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return "", "", fmt.Errorf("%s (stdin was empty — a steer with no message would interrupt the cell and say nothing)", cellSteerUsage)
	}
	return slug, text, nil
}

// parseCellAnswer reads `<slug> --digit N` or `<slug> --question Qn`. Both
// `--flag value` and `--flag=value` are accepted.
//
// The two are MUTUALLY EXCLUSIVE and one is required, both refused here
// rather than resolved by precedence: they post to different routes and mean
// different things (a keystroke into a live AskUserQuestion dialog vs. a
// stored answer on a doc question row), so guessing which one a caller meant
// would deliver the answer to the wrong place — and the wrong place accepts
// it silently.
func parseCellAnswer(args []string) (slug, digit, question string, err error) {
	if len(args) == 0 {
		return "", "", "", fmt.Errorf("%s", cellAnswerUsage)
	}
	slug = strings.TrimSpace(args[0])
	if slug == "" || strings.HasPrefix(slug, "-") {
		return "", "", "", fmt.Errorf("%s (the first argument is the job slug)", cellAnswerUsage)
	}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		name, raw, inline := strings.Cut(rest[i], "=")
		if !inline {
			name = rest[i]
			if name != "--digit" && name != "--question" {
				return "", "", "", fmt.Errorf("%s (unexpected argument %q)", cellAnswerUsage, rest[i])
			}
			if i+1 >= len(rest) {
				return "", "", "", fmt.Errorf("%s (%s needs a value)", cellAnswerUsage, name)
			}
			raw = rest[i+1]
			i++
		}
		switch name {
		case "--digit":
			if digit != "" {
				return "", "", "", fmt.Errorf("%s (--digit given twice)", cellAnswerUsage)
			}
			d := strings.TrimSpace(raw)
			// Validated client-side as well as server-side because the server
			// delivers it as a literal KEYSTROKE into a live dialog: a
			// two-character "10" would press 1 and then 0, selecting option 1
			// and then typing a stray 0 into whatever prompt followed.
			if len(d) != 1 || d[0] < '1' || d[0] > '9' {
				return "", "", "", fmt.Errorf("bad --digit %q: want a single option number 1-9 (%s)", raw, cellAnswerUsage)
			}
			digit = d
		case "--question":
			if question != "" {
				return "", "", "", fmt.Errorf("%s (--question given twice)", cellAnswerUsage)
			}
			question = strings.TrimSpace(raw)
			if question == "" {
				return "", "", "", fmt.Errorf("%s (--question needs a question id, e.g. Q1)", cellAnswerUsage)
			}
		default:
			return "", "", "", fmt.Errorf("%s (unexpected argument %q)", cellAnswerUsage, rest[i])
		}
	}
	if digit != "" && question != "" {
		return "", "", "", fmt.Errorf("%s (--digit and --question are two different answers to two different things — send one)", cellAnswerUsage)
	}
	if digit == "" && question == "" {
		return "", "", "", fmt.Errorf("%s (one of --digit or --question is required)", cellAnswerUsage)
	}
	return slug, digit, question, nil
}

// readCellAnswerText takes a doc question's answer from stdin, for
// parseCellSteer's reason: it is prose. The digit path never comes here — a
// single keystroke needs no pipe.
func readCellAnswerText(stdin io.Reader) (string, error) {
	if stdinIsInteractiveTTY(stdin) {
		return "", fmt.Errorf("%s — refusing to wait on an interactive terminal; pipe the answer in", cellAnswerUsage)
	}
	b, err := io.ReadAll(stdin)
	if err != nil {
		return "", fmt.Errorf("read stdin: %w", err)
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return "", fmt.Errorf("%s (stdin was empty — the server refuses an empty answer)", cellAnswerUsage)
	}
	return text, nil
}

func runCellSteer(slug, text string) error {
	if _, err := qfPost("/api/steer", map[string]any{"slug": slug, "text": text}); err != nil {
		return err
	}
	logCellAction("cell-steer", slug)
	return nil
}

// cellSubmitHTTPTimeout is the client deadline for the submit alone. The
// other proxy verbs run under qfHTTPTimeout (5s), sized for a row write;
// POST /api/queue is not a row write — jobs.SubmitJob starts the cell INSIDE
// the request: baseRef's `git fetch origin` (network, no bound of its own),
// git worktree add on a large repo, the dev-env symlink scan, skills
// materialization, tmux new-session, and acceptTrustPrompts' poll of up to
// 12s — and three of four measured submits from one night's cells took
// longer than 5s, each printing the timeout error for a start that had
// already succeeded. Ninety seconds clears all of that and still sits under
// the Bash tool's 120s default, so an agent sees the expiry text rather
// than a killed tool call. A refused connection never waits on it (that is
// qfUnreachableError, immediate), so a dead serve still fails the turn fast;
// only a serve that accepts and never answers spends the full deadline. It
// is a var, not a const, so a test can lower it and prove the submit really
// runs under it.
var cellSubmitHTTPTimeout = 90 * time.Second

// runCellSubmit posts the submit. /api/queue is the path rather than a
// "/api/submit" that reads better: the URL is kept for the shipped iOS builds
// that already post it (register.go), and the action behind it is "start this
// job's cell, now, synchronously" — which is also why it runs under
// cellSubmitHTTPTimeout rather than the family's default.
//
// An expiry is re-worded here rather than left to the family's generic text:
// that text says "re-read with `get`", and `k cell` has no get verb — the
// read that answers "did my submit land?" is `kraken board get <slug>`, and
// a retry is safe either way because the ladder refuses an already-started
// job with a 409.
func runCellSubmit(slug string) error {
	if _, err := qfPostWithTimeout("/api/queue", map[string]any{"slug": slug}, cellSubmitHTTPTimeout); err != nil {
		if qfIsTimeout(err) {
			// qfPostWithTimeout has already wrapped the transport error in
			// the family's generic sentence; unwrap one level so this message
			// REPLACES that sentence rather than stacking on top of it (the
			// stacked form printed "did not respond" twice and kept the
			// `get` advice this branch exists to remove).
			return fmt.Errorf("kraken serve did not respond within %s at %s — the submit may have completed server-side and the cell may already be starting; re-read with `kraken board get %s` before retrying rather than assuming it failed: %w", cellSubmitHTTPTimeout, jobs.ServeBaseURL(), slug, errors.Unwrap(err))
		}
		return err
	}
	logCellAction("cell-submit", slug)
	return nil
}

// runCellAnswer posts to whichever of the two routes the flags selected. Both
// log the same event name: the audit line answers "an agent answered something
// on this job", and which KIND of question it was is already recoverable from
// the job's own rows.
func runCellAnswer(slug, digit, question, text string) error {
	var err error
	if question != "" {
		_, err = qfPost("/api/doc-question", map[string]any{"slug": slug, "id": question, "text": text})
	} else {
		_, err = qfPost("/api/question", map[string]any{"slug": slug, "text": digit})
	}
	if err != nil {
		return err
	}
	logCellAction("cell-answer", slug)
	return nil
}

// logCellAction records one audit line through kraken serve's own
// /api/logevent — the same door `k logevent` uses, so the entry lands in the
// events table rather than only in the JSONL file.
//
// Its error is deliberately DROPPED. It is called only after the action
// itself returned 200, so a failure here means the audit line is missing,
// not that the steer/submit/answer did not happen — and reporting it as a
// failed verb would send the caller to retry an action that already landed, which
// on `submit` is a second start attempt and on `steer` a duplicate message
// into a live pane.
func logCellAction(event, slug string) {
	_, _ = qfPost("/api/logevent", map[string]any{
		"src": "cell", "level": "info", "event": event, "msg": slug,
	})
}
