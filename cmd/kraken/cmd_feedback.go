package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"kraken/internal/util"
)

// cmd_feedback.go — `k feedback get|addressed <slug> ...` plus `k feedback
// sweep`: zeus' own read/write surface for carl's per-artifact feedback,
// proxying to kraken serve's GET /api/feedback, POST /api/feedback and GET
// /api/feedback/sweep (internal/api/feedback.go).
// The READ verb's spelling matches the "Open questions and carl's
// per-artifact feedback are rows" paragraph in crew.go's zeusBrief, which is
// where every cell's zeus learns it. The WRITE verb's spelling is deliberately
// NOT there — `addressed` belongs to the nightly dream pass alone, so
// `.claude/skills/dream/SKILL.md` is the only place it is spelled out, and
// crew_test.go's forbidden-token list actively keeps it out of the brief (a
// brief naming the verb teaches every cell the call it is being told not to
// make). So: an ordinary cell reads entries and acts on them, the dream pass
// alone marks them addressed, and creation/edit/delete are carl-facing board
// actions this verb deliberately does NOT expose.
//
// `sweep` is the odd one out and takes no slug at all: it is the nightly
// dream pass' CROSS-JOB read (.claude/skills/dream/SKILL.md), every note in
// a time window regardless of which job it was left on.
//
// Same behavior contract as cmd_questions.go: GET prints server JSON
// verbatim to stdout; a write confirms on stderr; a 400 prints the server's
// message to stderr; serve-unreachable names the address; bad args print
// usage — all non-2xx/failure paths exit non-zero. See qfGet/qfPost
// (qfclient.go) for the shared HTTP plumbing.
func cmdFeedback(args []string) {
	if len(args) == 0 {
		util.Die("usage: k feedback get|sweep|addressed <slug> ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "get":
		out, err := runFeedbackGet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			util.Die("write stdout: %v", err)
		}
	case "sweep":
		out, err := runFeedbackSweep(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			util.Die("write stdout: %v", err)
		}
	case "addressed":
		if err := runFeedbackAddressed(rest, os.Stdin); err != nil {
			util.Die("%v", err)
		}
		// the confirmation names what actually happened: --clear reopens the
		// entry rather than marking it addressed (runFeedbackAddressed owns
		// the flag's parsing; this only re-reads it for the message).
		if slices.Contains(rest, "--clear") {
			fmt.Fprintln(os.Stderr, "feedback addressed note cleared")
		} else {
			fmt.Fprintln(os.Stderr, "feedback marked addressed")
		}
	default:
		util.Die("usage: k feedback get|sweep|addressed <slug> ... (unknown op %q)", op)
	}
}

// runFeedbackGet is `kraken feedback get <slug>`: GET /api/feedback?slug=
// — prints {"feedback": [...]} (open + addressed entries) straight through.
func runFeedbackGet(args []string) ([]byte, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return nil, fmt.Errorf("usage: k feedback get <slug>")
	}
	return qfGet("/api/feedback?slug=" + url.QueryEscape(strings.TrimSpace(args[0])))
}

// feedbackSweepUsage is the one usage string every bad-args path of `sweep`
// prints, so the window default stays visible whichever check fails.
const feedbackSweepUsage = "usage: k feedback sweep [--since <unix-millis>] (no slug — this is the cross-job sweep; default window is the last 24h)"

// feedbackSweepWindow is how far back `sweep` looks when --since is omitted:
// one day, matching the dream loop's own nightly cadence. The DEFAULT lives
// here and not in the API — internal/api/feedback.go's handler requires
// `since` and 400s without it on purpose, so a typo'd parameter can never
// silently re-mine months of already-corroborated notes. A default is only
// safe where it is visible, which is a usage line, not a query string.
const feedbackSweepWindow = 24 * time.Hour

// runFeedbackSweep is `kraken feedback sweep [--since <ms>]`: GET
// /api/feedback/sweep?since= — prints {"feedback": [...]} (every job's notes
// at or after the floor, each with its job slug and a snapshot_url) straight
// through. Both `--since <ms>` and `--since=<ms>` are accepted; anything else
// is a usage error rather than a silently ignored argument, so a mistyped
// flag can't quietly widen the window to the default.
func runFeedbackSweep(args []string) ([]byte, error) {
	since := time.Now().Add(-feedbackSweepWindow).UnixMilli()
	for i := 0; i < len(args); i++ {
		a := args[i]
		var raw string
		switch {
		case a == "--since":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s (--since needs a value)", feedbackSweepUsage)
			}
			raw = args[i+1]
			i++
		case strings.HasPrefix(a, "--since="):
			raw = strings.TrimPrefix(a, "--since=")
		default:
			return nil, fmt.Errorf("%s (unexpected argument %q)", feedbackSweepUsage, a)
		}
		v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("bad --since %q: want unix millis", raw)
		}
		since = v
	}
	return qfGet("/api/feedback/sweep?since=" + strconv.FormatInt(since, 10))
}

// feedbackAddressedUsage is the one usage string every bad-args path here
// prints, so the `--clear` form stays visible no matter which check fails.
const feedbackAddressedUsage = "usage: k feedback addressed [--clear] <slug> <id> <text...> (text read from stdin if omitted; --clear takes no text and reopens the entry)"

// runFeedbackAddressed is `kraken feedback addressed <slug> <id> <text...>`:
// posts {slug, id, addressed} — updates only the dream-pass-owned addressed/
// addressed_at columns (feedbackAddressed in internal/api/feedback.go).
// text is every arg after <id> joined with spaces when present; when
// omitted entirely (just `addressed <slug> <id>`) it's read from stdin
// instead, the same "full prose is a shell-quoting hazard as an inline
// arg" reasoning questions add/update use for their JSON body — documented
// here rather than split into two subcommands since exactly one of the two
// sources is ever consulted (args wins when both would otherwise apply).
//
// `--clear` (accepted in any position, since it reads as a modifier rather
// than a positional) posts the API's explicitly-empty form, {slug, id,
// addressed: ""}, which resets addressed/addressed_at and reopens the entry.
// Without it that form is unreachable from zeus' only surface: an empty text
// is otherwise a usage error, precisely so a piped-but-empty stdin can't
// silently wipe a prior note. --clear plus text is refused rather than
// silently preferring one — the two say opposite things.
func runFeedbackAddressed(args []string, stdin io.Reader) error {
	clear := false
	var rest []string
	for _, a := range args {
		if a == "--clear" {
			clear = true
			continue
		}
		rest = append(rest, a)
	}
	if len(rest) < 2 || strings.TrimSpace(rest[0]) == "" {
		return fmt.Errorf("%s", feedbackAddressedUsage)
	}
	slug, idStr := strings.TrimSpace(rest[0]), rest[1]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return fmt.Errorf("bad feedback id %q: %w", idStr, err)
	}
	var text string
	switch {
	case clear:
		if len(rest) > 2 {
			return fmt.Errorf("%s (--clear takes no text)", feedbackAddressedUsage)
		}
	case len(rest) > 2:
		text = strings.TrimSpace(strings.Join(rest[2:], " "))
	default:
		if stdinIsInteractiveTTY(stdin) {
			return fmt.Errorf("%s — refusing to wait on an interactive terminal; pipe the text in or pass it as trailing args instead", feedbackAddressedUsage)
		}
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return fmt.Errorf("read stdin: %w", err)
		}
		text = strings.TrimSpace(string(raw))
	}
	if text == "" && !clear {
		return fmt.Errorf("%s (empty text — pass --clear to deliberately reopen the entry)", feedbackAddressedUsage)
	}
	body := map[string]any{"slug": slug, "id": id, "addressed": text}
	_, err = qfPost("/api/feedback", body)
	return err
}
