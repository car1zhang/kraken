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
	if mineHelpRequested(args) {
		fmt.Fprintln(stdout, "usage: k mine digest [--since <unix-millis>]")
		return nil
	}
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
	//
	// The Tokens roll-up rides at the TOP level as well as inside each digest,
	// because it is the number a nightly pass wants first — "what did last
	// night cost, and which session dominated it" — and reconstructing it by
	// summing 155 digests in jq is exactly the friction that keeps a reader
	// from looking. Naming the fields explicitly here is also why it has to be
	// added by hand: an unnamed field added to MineResult is silently dropped
	// by this struct, which is what happened to this one.
	out := struct {
		Since     int64                `json:"since"`
		Scanned   int                  `json:"scanned"`
		Skipped   int                  `json:"skipped"`
		Truncated int                  `json:"truncated"`
		Tokens    core.Tokens          `json:"tokens"`
		Digests   []core.SessionDigest `json:"digests"`
	}{
		Since:     res.Since,
		Scanned:   res.Scanned,
		Skipped:   res.Skipped,
		Truncated: res.Truncated,
		Tokens:    res.Tokens,
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
	fmt.Fprintf(stderr, "mined %d transcripts since %s (%d skipped, %d truncated) — %d out, %d cache-write, %d cache-read tokens\n",
		res.Scanned, time.UnixMilli(since).Format(time.RFC3339), res.Skipped, res.Truncated,
		res.Tokens.Out, res.Tokens.CacheWrite, res.Tokens.CacheRead)
	return nil
}

// runMineReduce is `k mine reduce [--since <unix-millis>] <path>`: the
// decision-relevant slice of ONE transcript, written raw to stdout.
//
// Raw rather than JSON-wrapped because the consumer is a scout subagent
// reading prose, and wrapping a 40 KB reduction in a JSON string would escape
// every newline in it for no reader's benefit.
func runMineReduce(args []string, stdout, stderr *os.File) error {
	if mineHelpRequested(args) {
		fmt.Fprintln(stdout, "usage: k mine reduce [--since <unix-millis>] <transcript path>")
		return nil
	}
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
	// An empty reduction is rarely the answer the caller wanted, and it has
	// TWO causes the caller cannot tell apart from a 0-byte file: a dropped
	// or mistyped --since whose 24h default excludes every record of an
	// older session, and a window that genuinely holds only records the
	// reducer drops (tool results, attachments, bookkeeping — a digest can
	// list such a session while its reduction is empty). Exiting non-zero
	// with both causes named is what makes either visible to a caller that
	// redirected stdout to a file and to any && chain — two agents in one
	// night each staged a 0-byte "reduced view", read the zero exit as
	// success, and spent calls rediscovering the flag.
	if len(b) == 0 {
		return fmt.Errorf("reduced to 0 bytes: no decision-relevant records at or after %s — the window may hold only tool results and bookkeeping, or every record may predate the floor (absent, --since defaults to 24h ago)",
			time.UnixMilli(since).Format(time.RFC3339))
	}
	return nil
}

// mineHelpRequested reports whether any argument asks for usage. The package
// convention (cmd_account.go states it outright) is that `--help`/`-h`/`help`
// print usage to STDOUT and exit 0 — before this existed, `k mine reduce
// --help` fell through to the positional slot and died with "open transcript:
// open --help: no such file or directory", which two agents in one night each
// had to decode.
func mineHelpRequested(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-h" || a == "help" {
			return true
		}
	}
	return false
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
