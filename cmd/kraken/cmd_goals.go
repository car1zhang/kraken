package main

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"kraken/internal/util"
)

// cmd_goals.go — `k goals get [slug]`: the cell-side verb for carl's current
// weekly and daily goals, proxying to kraken serve's GET /api/goals
// (internal/api/goals.go) over the same localhost transport `k questions` and
// `k feedback` use (qfclient.go).
//
// zeus learns this spelling from ONE place: the header of the goals block
// goalctx.go stages into its brief. crew.go's Cell-paths block deliberately
// does NOT name it — a goals sentence was specced for that block and left out
// because the brief has less headroom under TestZeusBriefStaysLean's ceiling
// than the shortest honest sentence needs (crew.go explains at the omission).
// That is not a gap: the block header carries the verb, and it is staged on
// every spawn that has goals to state, which is the only time knowing the
// verb is worth anything.
//
// READ-ONLY, deliberately, and the only verb in this family with no write op:
// the goals are carl's, written from the board (and toggled from the phone).
// An agent that could add a goal could quietly rewrite what it is being
// measured against, so the pull channel is one direction by construction
// rather than by convention.
//
// Behavior contract, the questions-verb contract unchanged:
//   - success: prints the server's JSON to stdout VERBATIM, exit 0 — no
//     re-encoding, so it stays byte-identical to what the server sent and a
//     caller can pipe it straight into jq.
//   - a non-2xx prints the server's own error message to stderr, exit
//     non-zero — never a raw HTTP dump.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds.
//   - bad args: a usage message to stderr, exit non-zero.
//
// cmdGoals/runGoalsGet are split so the exit-code paths are testable without
// going through util.Die's os.Exit — cmd_questions.go's shape.
func cmdGoals(args []string) {
	if len(args) == 0 {
		util.Die("usage: k goals get [slug]")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "get":
		// The slug is OPTIONAL here, unlike every op in the questions/feedback
		// family: the goals themselves are global, and the slug only adds the
		// job's own linked goal to the answer. More than one bare argument is
		// a usage error rather than a silently ignored extra.
		if len(rest) > 1 {
			util.Die("usage: k goals get [slug]")
		}
		slug := ""
		if len(rest) == 1 {
			slug = rest[0]
		}
		if err := runGoalsGet(slug); err != nil {
			util.Die("%v", err)
		}
	default:
		util.Die("usage: k goals get [slug] (unknown op %q)", op)
	}
}

// runGoalsGet is `kraken goals get [slug]`: GET /api/goals (plus ?slug= when
// given) — prints {"goals":{"week":[…],"day":[…]}}, and "job_goal" too when
// the named job serves one, straight through.
//
// An empty or whitespace-only slug is treated as absent rather than sent as
// an empty query value, which the server would 404 as "no job \"\"" — the
// caller that passed one meant "no slug", and a shell that expanded an unset
// variable meant the same thing.
func runGoalsGet(slug string) error {
	path := "/api/goals"
	if s := strings.TrimSpace(slug); s != "" {
		path += "?slug=" + url.QueryEscape(s)
	}
	out, err := qfGet(path)
	if err != nil {
		return err
	}
	if _, err := os.Stdout.Write(out); err != nil {
		return fmt.Errorf("write stdout: %w", err)
	}
	return nil
}
