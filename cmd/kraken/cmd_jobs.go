package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
// and limit-hook are Claude Code hooks, and logevent is plumbing for non-Go
// writers (the Makefile's install target). The scheduler itself now runs
// only as part of `kraken serve` (cmd_serve.go) — there is no standalone
// daemon entry point.

// --- pr-hook ---

// cmdPrHook is the thin dispatch wrapper for `kraken pr-hook <slug>` — a
// Claude Code PostToolUse hook (jobs.PrHook does the work), not a
// user-facing verb, so it's kept out of usage() like _complete. A malformed
// invocation (no slug) is silently ignored rather than util.Die'd: this
// runs as a hook, and must never fail zeus' tool call.
//
// --serve-addr/--db-path (E7) are parsed by the shared hookArgs below; see
// jobs.SetHookServeAddr / cell.go's prHookCommand for where the hook command
// string that carries them gets built.
func cmdPrHook(args []string) {
	slug, ok := hookArgs(args)
	if !ok {
		return
	}
	jobs.PrHook(slug)
}

// --- limit-hook ---

// cmdLimitHook is the thin dispatch wrapper for `kraken limit-hook <slug>` —
// a Claude Code StopFailure hook (jobs.LimitHook does the work), kept out of
// usage() alongside pr-hook for the same reason, and silently ignoring a
// malformed invocation for the same reason: it runs as a hook, and a hook
// that fails the turn it fires on is worse than a hook that does nothing.
//
// It takes --serve-addr/--db-path identically to pr-hook, since the cell's
// hook command for both is built from one shared flag renderer
// (jobs/cell.go's hookCommandFlags).
func cmdLimitHook(args []string) {
	slug, ok := hookArgs(args)
	if !ok {
		return
	}
	jobs.LimitHook(slug)
}

// hookArgs parses the argument shape both hook verbs share: any number of
// --serve-addr/--db-path flags plus exactly one bare slug. ok=false for
// every malformed shape (a flag with no value, no slug, two slugs), and the
// caller's only correct response is to do nothing at all.
//
// The flags (E7) are how a cell's installed hook command propagates kraken
// serve's ACTUAL bind address and db path into what is a fresh, separate OS
// process spawned by the hook, inheriting none of serve's own flags or env.
// Setting the equivalent env vars for the rest of this short-lived one-shot
// process is the simplest delivery: jobs.ServeBaseURL, which both hook
// bodies call, reads KRAKEN_SERVE_ADDR exactly this way.
//
// Both verbs accept both flags because jobs/cell.go renders both hook
// commands from ONE flag renderer (hookCommandFlags), so limit-hook's
// command string carries --db-path whenever pr-hook's does. It is inert
// there: LimitHook only POSTs and never opens kraken.db. Accepting and
// ignoring it is the point — a hook that died on an unrecognized flag would
// fail the turn it fired on, which is the one thing a hook must never do.
func hookArgs(args []string) (string, bool) {
	var slug string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--serve-addr":
			i++
			if i >= len(args) {
				return "", false
			}
			os.Setenv("KRAKEN_SERVE_ADDR", args[i])
		case "--db-path":
			i++
			if i >= len(args) {
				return "", false
			}
			os.Setenv("KRAKEN_DB_PATH", args[i])
		default:
			if slug != "" {
				return "", false // more than one bare arg: malformed
			}
			slug = args[i]
		}
	}
	return slug, slug != ""
}

// --- recall-hook ---

// cmdRecallHook is the thin dispatch wrapper for `kraken recall-hook` — the
// Claude Code UserPromptSubmit/PostToolUse hook (jobs.RunRecallHook does the
// work), kept out of usage() alongside pr-hook for the same reason. It shares
// the two flags the whole hook family carries but NOT hookArgs, whose contract
// requires exactly one bare slug: here the slug is OPTIONAL. Graph recall is
// keyed on the SESSION in the hook payload and needs no slug at all, which is
// why every settings.json written before this argument existed must keep
// working; the slug is what the GOALS half resolves the job by, and a cell's
// teammates fire this hook from sessions no job row has ever seen. A SECOND
// bare argument is still the malformed shape and still runs nothing — a hook
// that dies on an unexpected argument fails the turn it fired on, the one
// thing a hook must never do. This dispatch case was silently DROPPED once, by
// a refactor of this very region (the limit-hook rewrite), leaving every
// installed recall-hook command calling a verb that no longer existed while
// the build stayed green — TestMainDispatchesEveryHookVerb is the pin that
// makes that impossible to repeat quietly.
func cmdRecallHook(args []string) {
	slug := ""
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
			// exactly one bare arg is legal and it is the cell's slug; an
			// unknown FLAG or a second bare arg is malformed, run nothing
			if slug != "" || strings.HasPrefix(args[i], "-") {
				return
			}
			slug = args[i]
		}
	}
	jobs.RunRecallHook(os.Stdin, os.Stdout, slug)
}

// --- bash-lint ---

// cmdBashLint is the thin dispatch wrapper for `kraken bash-lint` — a
// Claude Code PreToolUse hook (jobs.BashLintStdin does the work), not a
// user-facing verb, so it's kept out of usage() like pr-hook. Exit 2 is
// Claude Code's blocking-hook protocol: the stderr text reaches the model
// as the reason. Everything else — non-Bash tools, malformed payloads,
// clean commands — exits 0 and prints nothing, because a hook that fails a
// tool call by accident is a bug, not a safety net.
func cmdBashLint() {
	if msg, block := jobs.BashLintStdin(os.Stdin); block {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(2)
	}
}

// --- wakeup-guard ---

// cmdWakeupGuard is the thin dispatch wrapper for `kraken wakeup-guard` — a
// Claude Code PreToolUse hook on the ScheduleWakeup tool
// (jobs.WakeupGuardStdin does the work), not a user-facing verb, so it's kept
// out of usage() like bash-lint. Same exit-2 blocking protocol and the same
// fail-open posture: anything that is not a well-formed ScheduleWakeup arm
// exits 0 and prints nothing.
func cmdWakeupGuard() {
	if msg, block := jobs.WakeupGuardStdin(os.Stdin); block {
		fmt.Fprintln(os.Stderr, msg)
		os.Exit(2)
	}
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
