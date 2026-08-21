package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// cmd_orch.go — `k orch inbox|ack`: kronos' read/ack surface on the
// orchestrator inbox, proxying to kraken serve's GET /api/orch/inbox and
// POST /api/orch/ack (internal/api/orch.go).
//
// kronos is a long-running claude outside any cell — iris' sibling — and
// these two verbs are the durable half of its loop: read what the daemon
// enqueued (a job went blocked, a cell died, a PR merged), then ack with
// what it decided. The inbox ROW is the durable state, not kronos' context,
// so the ack is explicit rather than implied by the read: an item kronos
// looked at and never resolved must come back on the next `inbox`, and after
// a compaction or a respawn it does.
//
// Behavior contract, the same one cmd_slack.go and cmd_load.go spell out:
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
// so every op here is exactly one HTTP round trip through qfGet/qfPost.
//
// parse/run are split from cmdOrch so the exit-code paths are testable
// without going through util.Die's os.Exit — the shape cmd_load.go uses.
func cmdOrch(args []string) {
	if len(args) == 0 {
		util.Die("usage: k orch inbox|ack ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "inbox":
		out, err := runOrchInbox(rest)
		if err != nil {
			util.Die("%v", err)
		}
		writeStdout(out)
	case "ack":
		ids, disposition, err := parseOrchAck(rest)
		if err != nil {
			util.Die("%v", err)
		}
		if err := runOrchAck(ids, disposition); err != nil {
			util.Die("%v", err)
		}
		fmt.Fprintf(os.Stderr, "acked %s: %s\n", plural(len(ids), "item", "items"), disposition)
	default:
		util.Die("usage: k orch inbox|ack ... (unknown op %q)", op)
	}
}

// orchInboxUsage / orchAckUsage are the one usage string each bad-args path
// prints, so both required flags stay visible whichever check fails — an ack
// missing either one is refused, and a caller who just mistyped a flag should
// not have to go find the other somewhere else.
const (
	orchInboxUsage = "usage: k orch inbox (no arguments — it prints the oldest 100 unacked items, with the full count in `total`)"
	orchAckUsage   = `usage: k orch ack --ids 42,43 --disposition 'steered:csv-export-timeout' (single-quote the disposition — $ inside double quotes is silently eaten)`
)

// runOrchInbox is `k orch inbox`: GET /api/orch/inbox — prints {"items":
// [...], "total": N} straight through. `items` is the OLDEST 100 unacked
// rows in arrival order, not all of them (the route caps at orchInboxMax so a
// backlog cannot arrive whole in kronos' context), and `total` is the full
// unacked count — which is what makes that cap visible rather than silent, so
// it is never redundant with len(items) and must not be dropped as if it
// were.
//
// It takes no arguments at all: the inbox is not filterable on purpose, since
// a filter is a way to leave an item unread forever. The cap is not a filter
// in that sense — the rows it withholds are the NEWEST ones and they surface
// on the next call as acks retire the oldest.
func runOrchInbox(args []string) ([]byte, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("%s", orchInboxUsage)
	}
	return qfGet("/api/orch/inbox")
}

// parseOrchAck reads `--ids 42,43 --disposition "..."`. Both `--flag value`
// and `--flag=value` are accepted; anything else — an unknown flag, a
// positional, a flag with no value — is a usage error rather than a silently
// ignored argument, so a mistyped flag can't quietly ack nothing (or, worse,
// ack the right rows under an empty disposition the server then refuses for a
// reason that names the wrong problem).
//
// Ids are comma-separated because an ack is normally a BATCH: kronos reads
// the inbox once, decides about several items together, and one round trip
// per item would be several turns of latency for one decision.
//
// It is a near-twin of parseSlackAck rather than a shared helper because the
// two verbs' usage strings and vocabulary differ ("message" vs "item",
// drafted/edited vs steered/answered) and cmd_slack.go is iris' file; folding
// them together would make one verb's refusal text answerable only by reading
// the other's.
func parseOrchAck(args []string) ([]int64, string, error) {
	var ids []int64
	var disposition string
	var haveIDs, haveDisposition bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		name, raw, inline := strings.Cut(a, "=")
		if !inline {
			name = a
			if name != "--ids" && name != "--disposition" {
				return nil, "", fmt.Errorf("%s (unexpected argument %q)", orchAckUsage, a)
			}
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("%s (%s needs a value)", orchAckUsage, name)
			}
			raw = args[i+1]
			i++
		}
		switch name {
		case "--ids":
			// A repeated flag is a usage error, not a last-one-wins: silently
			// discarding the FIRST --ids is the version of it that leaves real
			// items sitting in the inbox with kronos believing it acked them.
			if haveIDs {
				return nil, "", fmt.Errorf("%s (--ids given twice — put every id in one comma-separated list)", orchAckUsage)
			}
			parsed, err := parseOrchIDs(raw)
			if err != nil {
				return nil, "", err
			}
			ids, haveIDs = parsed, true
		case "--disposition":
			if haveDisposition {
				return nil, "", fmt.Errorf("%s (--disposition given twice)", orchAckUsage)
			}
			disposition, haveDisposition = raw, true
		default:
			return nil, "", fmt.Errorf("%s (unexpected argument %q)", orchAckUsage, a)
		}
	}
	// Both or neither. An ack with no ids does nothing, and an ack with no
	// disposition records that an item left the inbox without saying why —
	// which is indistinguishable, later, from a bug that acked everything.
	if !haveIDs {
		return nil, "", fmt.Errorf("%s (--ids is required — the inbox ids from `k orch inbox`)", orchAckUsage)
	}
	if !haveDisposition || strings.TrimSpace(disposition) == "" {
		return nil, "", fmt.Errorf("%s (--disposition is required — say what happened: steered:<slug>, answered:<slug>, or ignored:<reason>)", orchAckUsage)
	}
	return ids, disposition, nil
}

// parseOrchIDs splits a comma-separated id list. An empty element, a
// non-number, or a non-positive id is an error rather than a skipped entry:
// the server acks all-or-nothing, so a list this process silently shortened
// would leave the dropped item in the inbox with kronos believing it had been
// resolved.
func parseOrchIDs(raw string) ([]int64, error) {
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("bad --ids %q: empty id in the list (%s)", raw, orchAckUsage)
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("bad --ids %q: %q is not an inbox id (%s)", raw, part, orchAckUsage)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("bad --ids %q: no ids (%s)", raw, orchAckUsage)
	}
	return out, nil
}

// runOrchAck posts the batch. The disposition is sent as typed — the server
// one-lines and truncates it (200 runes) rather than refusing an over-long
// one, so no local cap is duplicated here where it could drift.
func runOrchAck(ids []int64, disposition string) error {
	_, err := qfPost("/api/orch/ack", map[string]any{"ids": ids, "disposition": disposition})
	return err
}
