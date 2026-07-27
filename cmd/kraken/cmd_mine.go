package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// cmd_mine.go — `k mine digest|reduce`: the nightly dream pass' mechanical
// read of claude's own transcripts, which is the ONE step of
// .claude/skills/dream/SKILL.md that no other verb could stand in for.
//
// core.MineTranscripts and core.ReduceTranscript (internal/core/mine.go) were
// written for exactly this and shipped with no caller at all: the skill tells
// the pass to run a mechanical count and hand each scout a reduced view, and
// there was no way to ask the binary for either. This verb is that ask, and
// deliberately nothing more — it prints what those two functions return and
// interprets none of it. The corroboration is the agent's job; the counting
// is the binary's.
//
// Unlike `k questions`/`k feedback`/`k watchlist`, this does NOT proxy to
// kraken serve. Those three read rows out of kraken.db, which has a single
// writer; this reads files under ~/.claude/projects that another program
// owns, needs no lock and no serve, and is a pure read — so a cell can run it
// whether or not the daemon happens to be up.
//
// Output contract, matching the read verbs above: JSON on stdout, nothing
// else, so it pipes. Progress and totals go to stderr where they can't
// corrupt it.
func cmdMine(args []string) {
	if len(args) == 0 {
		util.Die("usage: k mine digest|reduce ...")
	}
	op, rest := args[0], args[1:]
	switch op {
	case "digest":
		if err := runMineDigest(rest, os.Stdout, os.Stderr); err != nil {
			util.Die("%v", err)
		}
	case "reduce":
		if err := runMineReduce(rest, os.Stdout, os.Stderr); err != nil {
			util.Die("%v", err)
		}
	default:
		util.Die("usage: k mine digest|reduce ... (unknown op %q)", op)
	}
}

// runMineDigest is `k mine digest [--since <unix-millis>]`: one digest per
// transcript touched in the window, as JSON.
//
// The default window is the last 24h, the same default `k feedback sweep`
// applies and for the same reason — a dream pass always passes the watermark
// explicitly, so the default only ever serves a human running it by hand.
func runMineDigest(args []string, stdout, stderr *os.File) error {
	since, rest, err := parseMineSince(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return fmt.Errorf("usage: k mine digest [--since <unix-millis>] (unexpected argument %q)", rest[0])
	}

	res, err := core.MineTranscripts(since)
	if err != nil {
		return err
	}

	// Marshal to the wire shape rather than the struct: MineResult's field
	// names are Go's, and this output is read by an agent and by jq.
	out := struct {
		Since     int64                `json:"since"`
		Scanned   int                  `json:"scanned"`
		Skipped   int                  `json:"skipped"`
		Truncated int                  `json:"truncated"`
		Digests   []core.SessionDigest `json:"digests"`
	}{
		Since:     res.Since,
		Scanned:   res.Scanned,
		Skipped:   res.Skipped,
		Truncated: res.Truncated,
		Digests:   res.Digests,
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return fmt.Errorf("write stdout: %v", err)
	}

	// Truncated is the number that separates a quiet night from a half-read
	// one, so it is stated on stderr even though it is already in the JSON —
	// a caller that eyeballs the tail of a 157-entry array will not see it.
	fmt.Fprintf(stderr, "mined %d transcripts since %s (%d skipped, %d truncated)\n",
		res.Scanned, time.UnixMilli(since).Format(time.RFC3339), res.Skipped, res.Truncated)
	return nil
}

// runMineReduce is `k mine reduce [--since <unix-millis>] <path>`: the
// decision-relevant slice of ONE transcript, written raw to stdout.
//
// Raw rather than JSON-wrapped because the consumer is a scout subagent
// reading prose, and wrapping a 40 KB reduction in a JSON string would escape
// every newline in it for no reader's benefit.
func runMineReduce(args []string, stdout, stderr *os.File) error {
	since, rest, err := parseMineSince(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 || strings.TrimSpace(rest[0]) == "" {
		return fmt.Errorf("usage: k mine reduce [--since <unix-millis>] <transcript path>")
	}

	b, err := core.ReduceTranscript(strings.TrimSpace(rest[0]), since)
	if err != nil {
		return err
	}
	if _, err := stdout.Write(b); err != nil {
		return fmt.Errorf("write stdout: %v", err)
	}
	// An empty reduction is a real answer — a session whose every record
	// predates the floor — and looks identical to a broken read at the shell.
	// Say which it was.
	if len(b) == 0 {
		fmt.Fprintf(stderr, "reduced to 0 bytes: no records at or after %s\n",
			time.UnixMilli(since).Format(time.RFC3339))
	}
	return nil
}

// parseMineSince pulls an optional --since off the front of args and returns
// it with the remaining arguments. Absent, it defaults to 24h ago.
//
// The flag is REQUIRED to parse: a typo'd --since that silently fell back to
// the default would sweep a window nobody asked for, which on this verb means
// reading the wrong night.
func parseMineSince(args []string) (int64, []string, error) {
	since := time.Now().Add(-24 * time.Hour).UnixMilli()
	rest := args
	for len(rest) > 0 && strings.HasPrefix(rest[0], "--since") {
		var raw string
		switch {
		case rest[0] == "--since":
			if len(rest) < 2 {
				return 0, nil, fmt.Errorf("--since needs a unix-millis value")
			}
			raw, rest = rest[1], rest[2:]
		default: // --since=<value>
			raw, rest = strings.TrimPrefix(rest[0], "--since="), rest[1:]
		}
		v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || v < 0 {
			return 0, nil, fmt.Errorf("--since must be unix millis, got %q", raw)
		}
		since = v
	}
	return since, rest, nil
}
