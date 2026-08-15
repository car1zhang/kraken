package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"kraken/internal/jobs"
)

// cmd_ctx.go — `k ctx-hook` and `k rearm-hook`: the two Claude Code hooks
// kronos' own settings.json installs (jobs' installKronosSettings), kept out
// of usage() alongside pr-hook and recall-hook for the same reason — they are
// machinery running inside somebody else's conversation, not verbs a human
// types.
//
// Both share one posture, and it is the opposite of every other verb in this
// binary: THEY NEVER FAIL. A statusline command that exits non-zero or writes
// to stdout on error puts a broken line in kronos' pane chrome on every
// render; a PostCompact hook that errors interrupts the turn it fired on.
// Every failure anywhere — no daemon, no store, a malformed payload, a
// timeout — degrades to "report nothing", never to a broken pane. That is why
// neither of these routes through qfPost: its whole contract is to turn a
// failure into a message and an exit code.
//
// The client timeout is 2s rather than qfHTTPTimeout's 5s. The statusline
// runs on every render and the compact hook runs at the one moment kronos is
// already stalled, so a slow or dead kraken serve must cost a blink rather
// than a visible hang. The report is telemetry: losing one is free, waiting
// for one is not.

// ctxHookTimeout bounds both hooks' single round trip. See the file header.
const ctxHookTimeout = 2 * time.Second

// statuslineInput is the subset of Claude Code's statusline JSON these hooks
// read. Everything else in that payload is ignored (encoding/json's default),
// so a harness that grows a field never breaks the pane's chrome.
//
// UsedPercentage is a POINTER because the statusline reports it as NULL
// before the first turn of a session has been billed, and null is not zero: a
// fresh session at "0%" and a session whose fullness is not yet known are
// different claims, and only the first one is worth recording. A nil here is
// what makes this hook skip the POST entirely rather than tell the server the
// window is empty.
type statuslineInput struct {
	SessionID     string `json:"session_id"`
	ContextWindow struct {
		UsedPercentage    *float64 `json:"used_percentage"`
		ContextWindowSize int64    `json:"context_window_size"`
	} `json:"context_window"`
}

// ctxHookFlags consumes the flag family every installed hook command carries
// (E7 — jobs' hookCommandFlags renders --serve-addr/--db-path into the
// command STRING precisely because a hook is a fresh OS process inheriting
// none of serve's env), setting the matching env vars jobs.ServeBaseURL
// reads. Anything unrecognized — a bare word, a half flag — is IGNORED rather
// than fatal, this file's never-fail posture: a hook that dies on an argument
// fails the turn it fired on. This parsing was MISSING at first ship: the
// installer wrote the flags, both verbs ignored them, and every report went
// to the default port — invisible on a real deployment (default addr) and
// found only by the parallel-server drill, where the meter went silently
// dead. The pin in ctxhook_pin_test.go is that discovery's regression guard.
func ctxHookFlags(args []string) {
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
		}
	}
}

// cmdCtxHook is `kraken ctx-hook`: the statusline command. It reads the
// harness' JSON from stdin, reports the context percentage to kraken serve
// (which is what the daemon's warning pass fires off), and prints the one
// line the pane renders.
func cmdCtxHook(args []string) {
	ctxHookFlags(args)
	runCtxHook(os.Stdin, os.Stdout)
}

// runCtxHook is cmdCtxHook's body, split out so the whole path is exercisable
// with plain readers and writers.
//
// Order matters: the line is computed from what stdin said, the report is
// attempted, and the LINE IS PRINTED WHATEVER HAPPENED to the report. A
// statusline that only renders when the daemon answers is a statusline that
// vanishes exactly when something is wrong.
func runCtxHook(stdin io.Reader, stdout io.Writer) {
	var in statuslineInput
	// A read or decode failure is not reported anywhere: there is nowhere to
	// report it to that is not the pane's chrome. The zero value falls
	// through to the em-dash line, which is the honest rendering of "the
	// harness said nothing this render".
	if raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20)); err == nil {
		_ = json.Unmarshal(raw, &in)
	}
	pct := in.ContextWindow.UsedPercentage
	if in.SessionID != "" && pct != nil {
		// Errors dropped by contract: see the file header. A skipped report
		// costs one render's telemetry, and the next render sends another.
		_ = hookPost("/api/orch/context", map[string]any{
			"session_id":          in.SessionID,
			"used_percentage":     *pct,
			"context_window_size": in.ContextWindow.ContextWindowSize,
		})
	}
	// The em-dash rather than "0%" for statuslineInput's reason: an unknown
	// fullness must never render as an empty window, which is the reading a
	// zero invites at exactly the moment a human glances at the pane.
	line := "kronos —"
	if pct != nil {
		line = fmt.Sprintf("kronos %.1f%%", *pct)
	}
	fmt.Fprintln(stdout, line)
}

// hookPayload is the subset of a Claude Code hook payload these hooks read.
// PostCompact carries the session it fired for, which is the only field the
// re-arm needs — and the field the server checks against the session it has
// recorded as kronos', so a hook firing from any other conversation is
// refused rather than allowed to wipe that conversation's recall marks.
type hookPayload struct {
	SessionID string `json:"session_id"`
}

// cmdRearmHook is `kraken rearm-hook`: the PostCompact hook. A compaction
// discards the context that kg_recall's per-session marks say kronos has
// already been told, so the marks are dropped and the graph is free to
// deliver those facts again.
//
// It prints NOTHING on any path — success included. A PostCompact hook's
// stdout is not chrome the user reads, and a line there would land in the
// transcript immediately after a compaction, which is the one place nothing
// should be adding noise.
func cmdRearmHook(args []string) {
	ctxHookFlags(args)
	runRearmHook(os.Stdin)
}

// runRearmHook is cmdRearmHook's body, split out for the same testability
// reason as runCtxHook's. Every failure — unreadable stdin, malformed JSON, a
// blank session, a refusing or absent server — returns silently: the worst
// case is that kronos is not re-told some facts, which is exactly the state
// it was in before the compaction.
func runRearmHook(stdin io.Reader) {
	raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return
	}
	var in hookPayload
	if err := json.Unmarshal(raw, &in); err != nil || in.SessionID == "" {
		return
	}
	_ = hookPost("/api/graph/recall-rearm", map[string]any{"session_id": in.SessionID})
}

// hookPost is these two hooks' one round trip: POST JSON to kraken serve,
// bounded at ctxHookTimeout, with the response body discarded.
//
// It deliberately does NOT reuse qfPost. That helper's whole value is turning
// a failure into a sentence a human or an agent reads and an exit code — the
// opposite of what a hook needs, and it carries the 5s timeout besides. The
// error returned here exists only so a caller CAN branch on it; both callers
// drop it, and that is the contract rather than an oversight.
func hookPost(path string, body any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: ctxHookTimeout}
	resp, err := client.Post(jobs.ServeBaseURL()+path, "application/json", bytes.NewReader(buf))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Drained rather than left to Close because reading the body to EOF is
	// what makes the discard explicit — every response on these two routes is
	// a few dozen bytes, so it costs nothing and it says the body was
	// deliberately thrown away rather than forgotten. Nothing about
	// connection reuse is claimed: hookPost builds a fresh client per call
	// inside a one-shot process that makes exactly one request and exits, so
	// there is no keep-alive pool here for a drained connection to return to.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("kraken serve returned HTTP %d", resp.StatusCode)
	}
	return nil
}
