package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// cmd_slack.go — `k slack inbox|status|ack`: iris' read/ack surface on the
// Slack intake inbox, proxying to kraken serve's GET /api/slack/inbox, GET
// /api/slack/status and POST /api/slack/ack (internal/api/slack.go).
//
// Iris is a long-running claude outside any cell, and these three verbs are
// its whole loop: read what arrived, decide per message, ack with what it
// decided. The inbox ROW is the durable state — not iris' context — so the
// ack is explicit rather than implied by the read: a message iris looked at
// and never resolved must come back on the next `inbox`, and after a
// compaction or a respawn it does.
//
// Behavior contract, the same one cmd_load.go and cmd_questions.go spell out:
//   - success: a read prints the server's JSON to stdout verbatim, exit 0; a
//     write prints one short confirmation line to STDERR (stdout stays
//     parseable), exit 0.
//   - a 4xx prints the server's own error message to stderr, exit non-zero —
//     never a raw HTTP dump.
//   - kraken serve unreachable: a stderr message naming the address this
//     process tried, exit non-zero — never silently succeeds.
//   - bad/missing args: a usage message to stderr, exit non-zero.
//
// It NEVER opens kraken.db: only kraken serve writes it (single-writer rule),
// so every op here is exactly one HTTP round trip through qfGet/qfPost. It
// never talks to Slack either — the token lives in the daemon's environment
// and in ~/.kraken/slack.env, and nothing about it is readable from here.
//
// parse/run are split from cmdSlack so the exit-code paths are testable
// without going through util.Die's os.Exit — same shape cmd_load.go uses.
func cmdSlack(args []string) {
	if len(args) == 0 {
		util.Die("usage: k slack inbox|status|ack ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "inbox":
		out, err := runSlackInbox(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	case "status":
		out, err := runSlackStatus(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	case "ack":
		ids, disposition, err := parseSlackAck(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runSlackAck(ids, disposition); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "acked %s: %s\n", plural(len(ids), "message", "messages"), disposition)
	default:
		util.Die("usage: k slack inbox|status|ack ... (unknown op %q)", op)
	}
}

// slackAckUsage is the one usage string every bad-args path of `ack` prints,
// so both required flags stay visible whichever check fails — an ack missing
// either one is refused, and a caller who just mistyped a flag should not
// have to go find the other somewhere else.
const slackAckUsage = `usage: k slack ack --ids 42,43 --disposition "drafted:csv-export-timeout"`

// runSlackInbox is `k slack inbox`: GET /api/slack/inbox — prints
// {"messages": [...]} (every unacked row, arrival order) straight through.
// It takes no arguments at all: the inbox is not filterable on purpose, since
// a filter is a way to leave a message unread forever.
func runSlackInbox(args []string) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("usage: k slack inbox (no arguments — it prints every unacked message)")
	}
	return qfGet("/api/slack/inbox")
}

// runSlackStatus is `k slack status`: GET /api/slack/status — prints
// {"configured", "last_poll_at", "total", "undelivered", "unacked"} straight
// through. This is the verb that tells "nothing arrived" apart from "the
// feature is off", which is why `configured` rides even though iris only
// runs at all when it is true.
func runSlackStatus(args []string) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("usage: k slack status (no arguments)")
	}
	return qfGet("/api/slack/status")
}

// parseSlackAck reads `--ids 42,43 --disposition "..."`. Both `--flag value`
// and `--flag=value` are accepted; anything else — an unknown flag, a
// positional, a flag with no value — is a usage error rather than a silently
// ignored argument, so a mistyped flag can't quietly ack nothing (or, worse,
// ack the right rows under an empty disposition the server then refuses for
// a reason that names the wrong problem).
//
// Ids are comma-separated because an ack is normally a BATCH: iris reads the
// inbox once, decides about several messages together, and one round trip
// per message would be several turns of latency for one decision.
func parseSlackAck(args []string) ([]int64, string, error) {
	var ids []int64
	var disposition string
	var haveIDs, haveDisposition bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, raw, inline := strings.Cut(a, "=")
		if !inline {
			name = a
			if name != "--ids" && name != "--disposition" {
				return nil, "", fmt.Errorf("%s (unexpected argument %q)", slackAckUsage, a)
			}
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s (%s needs a value)", slackAckUsage, name)
			}
			raw = args[i+1]
			i++
		}
		switch name {
		case "--ids":
			// A repeated flag is a usage error, not a last-one-wins: this
			// parser refuses every other shape of "you probably didn't mean
			// that", and silently discarding the FIRST --ids is the version
			// of it that leaves real messages sitting in the inbox with iris
			// believing it acked them.
			if haveIDs {
				return nil, "", fmt.Errorf("%s (--ids given twice — put every id in one comma-separated list)", slackAckUsage)
			}
			parsed, err := parseSlackIDs(raw)
			if err != nil {
				return nil, "", err
			}
			ids, haveIDs = parsed, true
		case "--disposition":
			if haveDisposition {
				return nil, "", fmt.Errorf("%s (--disposition given twice)", slackAckUsage)
			}
			disposition, haveDisposition = raw, true
		default:
			return nil, "", fmt.Errorf("%s (unexpected argument %q)", slackAckUsage, a)
		}
	}
	// Both or neither. An ack with no ids does nothing, and an ack with no
	// disposition records that a message left the inbox without saying why —
	// which is indistinguishable, later, from a bug that acked everything.
	if !haveIDs {
		return nil, "", fmt.Errorf("%s (--ids is required — the inbox ids from `k slack inbox`)", slackAckUsage)
	}
	if !haveDisposition || strings.TrimSpace(disposition) == "" {
		return nil, "", fmt.Errorf("%s (--disposition is required — say what happened: drafted:<slug>, edited:<slug>, or ignored:<reason>)", slackAckUsage)
	}
	return ids, disposition, nil
}

// parseSlackIDs splits a comma-separated id list. An empty element, a
// non-number, or a non-positive id is an error rather than a skipped entry:
// the server acks all-or-nothing, so a list this process silently shortened
// would leave the dropped message in the inbox with iris believing it had
// been resolved.
func parseSlackIDs(raw string) ([]int64, error) {
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("bad --ids %q: empty id in the list (%s)", raw, slackAckUsage)
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("bad --ids %q: %q is not an inbox id (%s)", raw, part, slackAckUsage)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bad --ids %q: no ids (%s)", raw, slackAckUsage)
	}
	return out, nil
}

// runSlackAck posts the batch. The disposition is sent as typed — the server
// one-lines and truncates it (200 runes) rather than refusing an over-long
// one, so no local cap is duplicated here where it could drift.
func runSlackAck(ids []int64, disposition string) error {
	_, err := qfPost("/api/slack/ack", map[string]any{"ids": ids, "disposition": disposition})
	return err
}

// plural renders "1 message" / "3 messages" for the confirmation line. A
// count with the wrong noun reads as a bug in the thing that just succeeded.
func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
