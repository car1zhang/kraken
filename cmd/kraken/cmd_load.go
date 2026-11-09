package main

import (
	"fmt"
	"net/url"
	"os"

	"kraken/internal/util"
)

// cmd_load.go — `k load get <slug>`: read a job's triage estimate, proxying
// to kraken serve's GET /api/job/load (internal/api/jobload.go). Two numbers,
// both 1-10 — how much effort unblocking this job costs carl, and how big
// the claude work that reply unblocks is likely to be — which the board
// renders as the ratio scope/unblock so a cheap unblock buying a long
// session sorts first.
//
// The verb is read-only. `set` and `clear` were zeus' until the estimate
// became a Jev judgment the daemon makes about zeus' final message after
// every turn (internal/jobs/loadjudge.go); a cell has nothing to write.
//
// Behavior contract, the same one cmd_questions.go's header spells out for
// the questions/feedback family:
//   - success: the server's JSON to stdout verbatim, exit 0.
//   - a 4xx prints the server's own error message to stderr, exit non-zero —
//     never a raw HTTP dump.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// It NEVER opens kraken.db: only kraken serve reads it on its behalf, so the
// op is exactly one HTTP round trip through qfGet.
func cmdLoad(args []string) {
	if len(args) == 0 {
		util.Die("usage: k load get <slug>")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "get":
		out, err := runLoadGet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if _, err := os.Stdout.Write(out); err != nil {
			util.Die("write stdout: %v", err)
		}
	default:
		util.Die("k load: unknown op %q (want get)", op)
	}
}

// runLoadGet fetches GET /api/job/load?slug=<slug> and returns the body —
// {"load": {...}} (or {"load": null} for a job with no estimate) straight
// through, newline included, so a caller can pipe it.
func runLoadGet(args []string) ([]byte, error) {
	if len(args) != 1 || args[0] == "" {
		return nil, fmt.Errorf("usage: k load get <slug>")
	}
	return qfGet("/api/job/load?slug=" + url.QueryEscape(args[0]))
}
