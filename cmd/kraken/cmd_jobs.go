package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmd_jobs.go — this file owns no job-lifecycle CLI entry points; the
// board/web UI is the only supported path for starting, completing,
// archiving, or reactivating a job (Q14: "every job interaction is
// web/mobile"). What remains here isn't a job verb in that sense: pr-hook
// is a Claude Code tool hook, and logevent is plumbing for non-Go writers
// (the Makefile's install target). The scheduler itself now runs only as
// part of `kraken serve` (cmd_serve.go) — there is no standalone daemon
// entry point.

// --- pr-hook ---

// cmdPrHook is the thin dispatch wrapper for `kraken pr-hook <slug>` — a
// Claude Code PostToolUse hook (jobs.PrHook does the work), not a
// user-facing verb, so it's kept out of usage() like _complete. A malformed
// invocation (no slug) is silently ignored rather than util.Die'd: this
// runs as a hook, and must never fail zeus' tool call.
//
// --serve-addr/--db-path (E7) are how a cell's installed hook command
// propagates kraken serve's ACTUAL bind address/db path into this — a
// fresh, separate OS process spawned by the hook, inheriting none of
// serve's own flags or env — when they differ from the defaults PrHook
// would otherwise assume. See jobs.SetHookServeAddr / cell.go's
// prHookCommand for where the hook command string gets built. Setting the
// equivalent env vars for the rest of this (short-lived, one-shot) process
// is simplest: PrHook already reads
// KRAKEN_SERVE_ADDR/KRAKEN_DB_PATH exactly this way for tests.
func cmdPrHook(args []string) {
	var slug string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--serve-addr":
			i++
			if i >= len(args) {
				return
			}
			os.Setenv("KRAKEN_SERVE_ADDR", args[i])
		case "--db-path":
			i++
			if i >= len(args) {
				return
			}
			os.Setenv("KRAKEN_DB_PATH", args[i])
		default:
			if slug != "" {
				return // more than one bare arg: malformed, silently ignore
			}
			slug = args[i]
		}
	}
	if slug == "" {
		return
	}
	jobs.PrHook(slug)
}

// --- recall-hook ---

// cmdRecallHook is the thin dispatch wrapper for `kraken recall-hook` — a
// Claude Code UserPromptSubmit/PostToolUse hook (jobs.RunRecallHook does the
// work),
// machinery rather than a user-facing verb, so it stays out of usage()
// exactly as pr-hook does. It takes no slug: recall is keyed on the session
// the payload names, not on the job whose worktree the hook fired in.
//
// It always returns (never util.Die, never a non-zero exit): a hook that
// fails a turn is a bug by definition, and RunRecallHook already reports
// every problem to stderr.
//
// --serve-addr/--db-path are handled exactly as cmdPrHook handles them, and
// for the same reason — this is a fresh OS process spawned by the hook
// command string, inheriting none of kraken serve's flags or env, so
// anything serve resolved that differs from the defaults has to arrive as an
// argument. Setting the equivalent env var for the rest of this one-shot
// process is simplest: jobs.ServeBaseURL already reads KRAKEN_SERVE_ADDR
// exactly this way. --db-path is inert here — recall-hook opens no database,
// it POSTs — and is accepted only for symmetry with pr-hook, so the two verbs
// take the same flags and recallHookCommand can mirror prHookCommand without
// a carve-out.
func cmdRecallHook(args []string) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--serve-addr":
			i++
			if i >= len(args) {
				return
			}
			os.Setenv("KRAKEN_SERVE_ADDR", args[i])
		case "--db-path":
			i++
			if i >= len(args) {
				return
			}
			os.Setenv("KRAKEN_DB_PATH", args[i])
		default:
			return // an unexpected bare arg: malformed, inject nothing
		}
	}
	jobs.RunRecallHook(os.Stdin, os.Stdout)
}

// --- k logevent ---

// cmdLogEvent appends one entry to the unified event log — plumbing for
// non-Go writers (the Makefile's install target logs through this), not a
// user-facing command, so it stays out of k help. It POSTs to kraken
// serve's /api/logevent first — the sole DB writer once serve is deployed —
// falling back to the direct util.LogEvent (JSONL-only) write on any
// connection failure or non-200, so `make install` never fails just
// because serve isn't running yet.
//
// L8/S12: the target address is resolved via jobs.ServeBaseURL, the same
// implementation jobs/prhook.go's postPRAttach uses — kept as the one
// shared implementation rather than a second hand-rolled copy here.
func cmdLogEvent(args []string) {
	if len(args) < 3 {
		util.Die("usage: k logevent <src> <event> <msg...>")
	}
	src, event, msg := args[0], args[1], strings.Join(args[2:], " ")

	posted := false
	if body, err := json.Marshal(map[string]string{"src": src, "level": "info", "event": event, "msg": msg}); err == nil {
		client := http.Client{Timeout: 3 * time.Second}
		if resp, err := client.Post(jobs.ServeBaseURL()+"/api/logevent", "application/json", bytes.NewReader(body)); err == nil {
			resp.Body.Close()
			posted = resp.StatusCode == http.StatusOK
		}
	}
	if !posted {
		util.LogEvent(src, "info", event, msg)
	}
}
