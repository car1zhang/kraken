package main

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// cmd_load.go — `k load get|set|clear <slug>`: the cell-side verb for a job's
// triage estimate, proxying to kraken serve's GET/POST /api/job/load
// (internal/api/jobload.go). Two numbers, both 1-10 — how much effort
// unblocking this job costs carl, and how big the claude work that reply
// unblocks is likely to be — which the board renders as the ratio
// scope/unblock so a cheap unblock buying a long session sorts first.
//
// Behavior contract, the same one cmd_questions.go's header spells out for
// the questions/feedback family:
//   - success: `get` prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR (stdout stays
//     parseable), exit 0.
//   - a 4xx prints the server's own error message to stderr, exit non-zero —
//     never a raw HTTP dump.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// It NEVER opens kraken.db: only kraken serve writes it (single-writer rule),
// so every op here is exactly one HTTP round trip through qfGet/qfPost.
//
// parse/run are split from cmdLoad so the exit-code paths are testable
// without going through util.Die's os.Exit — same shape cmd_questions.go
// uses.
func cmdLoad(args []string) {
	if len(args) == 0 {
		util.Die("usage: k load get|set|clear <slug> ...")
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
	case "set":
		slug, l, err := parseLoadSet(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runLoadSet(slug, l); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "load set for %s: unblock %d, scope %d\n", slug, l.unblock, l.scope)
	case "clear":
		slug, err := parseLoadClear(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runLoadClear(slug); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "load cleared for %s\n", slug)
	default:
		util.Die("usage: k load get|set|clear <slug> ... (unknown op %q)", op)
	}
}

// loadSetUsage is the one usage string every bad-args path of `set` prints,
// so the valid range stays visible whichever check fails — the flags are
// meaningless without it, and a caller who just got a range wrong should not
// have to go find the number somewhere else.
const loadSetUsage = "usage: k load set <slug> --unblock <1-10> --scope <1-10> [--note \"...\"]"

// loadFields is one parsed `set` request. A named struct rather than three
// loose returns: the three only ever travel together, and a positional
// (int, int, string) is exactly the shape a caller silently swaps two
// arguments in.
type loadFields struct {
	unblock int
	scope   int
	note    string
}

// runLoadGet is `k load get <slug>`: GET /api/job/load?slug= — prints
// {"load": {...}} (or {"load": null} for a job with no estimate) straight
// through.
func runLoadGet(args []string) ([]byte, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return nil, fmt.Errorf("usage: k load get <slug>")
	}
	return qfGet("/api/job/load?slug=" + url.QueryEscape(strings.TrimSpace(args[0])))
}

// parseLoadSet reads `<slug> --unblock N --scope M [--note "..."]`. Both
// `--flag value` and `--flag=value` are accepted; anything else is a usage
// error rather than a silently ignored argument, so a mistyped flag can't
// quietly send a half estimate the server then refuses for a reason that
// names the wrong problem.
//
// The range check is duplicated here on purpose even though the API enforces
// it too (internal/api/jobload.go is the authoritative gate): a local refusal
// costs no round trip and can say `--unblock` rather than `unblock`, naming
// the thing the caller actually typed.
func parseLoadSet(args []string) (string, loadFields, error) {
	var l loadFields
	if len(args) == 0 || strings.TrimSpace(args[0]) == "" || strings.HasPrefix(args[0], "-") {
		return "", l, fmt.Errorf("%s", loadSetUsage)
	}
	slug := strings.TrimSpace(args[0])

	// unblock/scope start at 0 (the wire's "no estimate"), so a flag left out
	// entirely is caught by the same range check a bad value is, rather than
	// defaulting to some number nobody asked for.
	var haveUnblock, haveScope bool
	for i := 1; i < len(args); i++ {
		a := args[i]
		name, raw, inline := strings.Cut(a, "=")
		if !inline {
			name = a
			if name != "--unblock" && name != "--scope" && name != "--note" {
				return "", l, fmt.Errorf("%s (unexpected argument %q)", loadSetUsage, a)
			}
			if i+1 >= len(args) {
				return "", l, fmt.Errorf("%s (%s needs a value)", loadSetUsage, name)
			}
			raw = args[i+1]
			i++
		}
		switch name {
		case "--unblock", "--scope":
			n, err := strconv.Atoi(strings.TrimSpace(raw))
			if err != nil {
				return "", l, fmt.Errorf("bad %s %q: want a whole number 1-10 (%s)", name, raw, loadSetUsage)
			}
			if n < 1 || n > 10 {
				return "", l, fmt.Errorf("bad %s %d: out of range, want 1-10 (%s)", name, n, loadSetUsage)
			}
			if name == "--unblock" {
				l.unblock, haveUnblock = n, true
			} else {
				l.scope, haveScope = n, true
			}
		case "--note":
			l.note = raw
		default:
			return "", l, fmt.Errorf("%s (unexpected argument %q)", loadSetUsage, a)
		}
	}
	// Both or neither: roi is scope/unblock, so one number alone says
	// nothing. `clear` is how the estimate is removed — refusing here keeps
	// an omitted flag from reading as an accidental half clear.
	if !haveUnblock || !haveScope {
		return "", l, fmt.Errorf("%s (both --unblock and --scope are required — roi is scope/unblock, so one alone says nothing; use `k load clear <slug>` to remove an estimate)", loadSetUsage)
	}
	return slug, l, nil
}

// runLoadSet posts the parsed estimate. `at` is deliberately not sent: the
// server stamps the write time, so an estimate can never claim to be fresher
// than the write that made it.
func runLoadSet(slug string, l loadFields) error {
	body := map[string]any{"slug": slug, "unblock": l.unblock, "scope": l.scope, "note": l.note}
	_, err := qfPost("/api/job/load", body)
	return err
}

func parseLoadClear(args []string) (string, error) {
	if len(args) != 1 || strings.TrimSpace(args[0]) == "" {
		return "", fmt.Errorf("usage: k load clear <slug>")
	}
	return strings.TrimSpace(args[0]), nil
}

// runLoadClear posts the zero pair, which is the API's clear form — the same
// endpoint and the same body shape as a set, so there is no second route to
// keep in step.
func runLoadClear(slug string) error {
	_, err := qfPost("/api/job/load", map[string]any{"slug": slug, "unblock": 0, "scope": 0, "note": ""})
	return err
}
