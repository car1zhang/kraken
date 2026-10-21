package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kraken/internal/jobs"
	"kraken/internal/util"
)

// cmd_ctx.go — `k ctx-hook`, a Claude Code hook kept out of usage()
// alongside pr-hook and recall-hook for the same reason — it is machinery
// running inside somebody else's conversation, not a verb a human types.
//
// ctx-hook is a cell's statusline command (jobs' mergeCellStatusLine installs
// it into every cell's settings.json as `ctx-hook --slug <slug>`): it reports
// the context fill — restated as a share of the auto-compact window
// (autoCompactWindow) — to /api/cell/context keyed on the slug, and prints the
// user's own ~/.claude/settings.json statusLine output (run by
// runUserStatusLine, bounded by ctxHookTimeout) or `<slug> NN%` when there is
// none or it fails. Without --slug it reports nowhere and prints `ctx NN%`:
// the slug-less form belonged to a since-retired standing agent's statusline,
// and a settings file that still carries it (that agent's own dir on a machine
// that ran it) must keep rendering a line rather than an error.
//
// Its posture is the opposite of every other verb in this binary: IT NEVER
// FAILS. A statusline command that exits non-zero or writes to stdout on
// error puts a broken line in the pane's chrome on every render. Every
// failure anywhere — no daemon, no store, a malformed payload, a timeout —
// degrades to "report nothing", never to a broken pane. That is why it does
// not route through qfPost: its whole contract is to turn a failure into a
// message and an exit code.
//
// The client timeout is 2s rather than qfHTTPTimeout's 5s. The statusline
// runs on every render, so a slow or dead kraken serve must cost a blink
// rather than a visible hang. The report is telemetry: losing one is free,
// waiting for one is not.

// ctxHookTimeout bounds the hook's single round trip, and separately the
// wrapped user statusline command in runUserStatusLine. See the file header.
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
//
// TotalInputTokens is the exact count UsedPercentage was computed from
// (input + cache_creation + cache_read, per the statusline reference). It is a
// plain int: Claude Code reports 0 before the first response, and that is the
// same render in which UsedPercentage is null, so the POST that would carry a
// zero never happens — and a report that did carry one would mean the harness
// sent no count, which is what the server reads 0 as.
type statuslineInput struct {
	SessionID string `json:"session_id"`
	Model     struct {
		ID string `json:"id"`
	} `json:"model"`
	ContextWindow struct {
		UsedPercentage    *float64 `json:"used_percentage"`
		ContextWindowSize int64    `json:"context_window_size"`
		TotalInputTokens  int64    `json:"total_input_tokens"`
	} `json:"context_window"`
}

// autoCompactWindowEnv is the environment variable Claude Code reads ahead of
// every settings file for the auto-compact window (and refuses to let
// /autocompact override). A cell's statusline command is a child of that
// claude, so it inherits the variable.
const autoCompactWindowEnv = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"

// The bounds Claude Code clamps the environment variable to: its own warning
// text reads "Set it to a size from 100k to 1M, such as 200k or 200000".
const (
	autoCompactWindowMin = 100_000
	autoCompactWindowMax = 1_000_000
)

// autoCompactWindow returns the token count at which Claude Code auto-compacts
// a session running `model`, resolved the way Claude Code resolves it: the
// CLAUDE_CODE_AUTO_COMPACT_WINDOW environment variable when it is set, else
// the user's own <home>/.claude/settings.json — the per-model
// `modelSettings.<model>.autoCompactWindow`, else the top-level
// `autoCompactWindow` — else 0 for "no such setting" (a missing or
// unparseable file reads the same way: this hook never fails). Only the user
// file is read: the per-cell settings.json is kraken-written and never
// carries the key, and project and local settings files are not read because
// no machine has been seen setting the key there yet.
//
// Each value is parsed on its own (windowTokens) rather than through a typed
// decode of the whole file, so one entry Claude Code accepts but an int64
// field would reject costs only that entry and never the lookup: the setting
// is documented as a token count OR the string "auto", which is exactly what
// `/autocompact auto` (and `reset`) writes into modelSettings, and a float
// literal like 500000.0 is an integer to JavaScript. A per-model "auto"
// returns 0 rather than falling through to the top level, because that is
// what Claude Code does with it — the model's own tuned window, not the
// top-level number. The model is looked up by its exact id and then by its
// canonical one (canonicalModelID: a trailing `[…]` or `-YYYYMMDD` stripped,
// two of the spellings Claude Code folds onto the canonical key); kraken's
// own spawns pass exact ids, so the second lookup serves a job whose
// `model:` carries a suffix. The environment variable is read the way Claude
// Code reads it (envWindowTokens): a `k` or `m` suffix is allowed, the value
// is clamped to 100K-1M, and one it cannot read is passed over for the
// settings file rather than treated as a setting.
//
// This function exists because the statusline's used_percentage is a share
// of the MODEL's full window (context_window_size), while Claude Code
// compacts at roughly 92% of this smaller window when it is set — measured
// 2026-10-20 on four zeus compactions at 460-472K tokens against a 1M window
// and a 500K setting, and a dozen teammate compactions at 367-376K against a
// 400K one. In every one of them the daemon's 75% handoff threshold could
// never be reached, so autoHandoffPass (internal/jobs/cellctx.go) never typed
// /handoff into any job on that machine. The window is per model because the
// setting is: an Opus teammate and a Fable lead in one cell can compact at
// different sizes. With no setting anywhere the harness' own number passes
// through. That can still be too high: Claude Code may pick a smaller window
// from a model default or from server data, and this hook cannot read either.
//
// An empty home reads nothing at all, the environment variable included: it
// is the tests' way of sealing the hook off from the machine it runs on, and
// cmdCtxHook always passes the real home.
func autoCompactWindow(home, model string) int64 {
	if home == "" {
		return 0
	}
	if n, ok := envWindowTokens(os.Getenv(autoCompactWindowEnv)); ok {
		return n
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return 0
	}
	var parsed struct {
		AutoCompactWindow json.RawMessage `json:"autoCompactWindow"`
		ModelSettings     json.RawMessage `json:"modelSettings"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return 0
	}
	// A modelSettings that is not an object costs only the per-model lookup,
	// never the top level beside it.
	var models map[string]json.RawMessage
	_ = json.Unmarshal(parsed.ModelSettings, &models)
	for _, key := range modelSettingsKeys(model) {
		entry, ok := models[key]
		if !ok {
			continue
		}
		var m struct {
			AutoCompactWindow json.RawMessage `json:"autoCompactWindow"`
		}
		if json.Unmarshal(entry, &m) != nil || len(m.AutoCompactWindow) == 0 {
			continue
		}
		if n, auto := windowTokens(m.AutoCompactWindow); n > 0 || auto {
			return n
		}
	}
	n, _ := windowTokens(parsed.AutoCompactWindow)
	return n
}

// envWindowTokens reads CLAUDE_CODE_AUTO_COMPACT_WINDOW the way Claude Code
// 2.1.306 does: trimmed, a trailing `k` or `m` (either case) multiplying the
// number by a thousand or a million, anything else read as a plain number,
// and the result clamped to autoCompactWindowMin..Max. ok is false for an
// empty or unreadable value — Claude Code then falls through to its settings
// files, so the caller must too.
func envWindowTokens(v string) (int64, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	mult := 1.0
	switch v[len(v)-1] {
	case 'k', 'K':
		mult, v = 1_000, v[:len(v)-1]
	case 'm', 'M':
		mult, v = 1_000_000, v[:len(v)-1]
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 1 {
		return 0, false
	}
	f = math.Round(f * mult)
	if f < autoCompactWindowMin {
		f = autoCompactWindowMin
	}
	if f > autoCompactWindowMax {
		f = autoCompactWindowMax
	}
	return int64(f), true
}

// windowTokens reads one autoCompactWindow value: a JSON number with no
// fractional part and above zero is that many tokens; the string "auto" is
// Claude Code's own tuned window, reported as (0, true) so a caller can tell
// it from a value it could not read, which is (0, false).
func windowTokens(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var f float64
	if json.Unmarshal(raw, &f) == nil {
		if f <= 0 || f != float64(int64(f)) {
			return 0, false
		}
		return int64(f), false
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && strings.EqualFold(strings.TrimSpace(s), "auto") {
		return 0, true
	}
	return 0, false
}

// modelDateSuffix matches the dated spelling of a model id (`-20260801`).
var modelDateSuffix = regexp.MustCompile(`-\d{8}$`)

// canonicalModelID strips two suffixes a model id can carry that Claude Code
// folds onto the canonical settings key: a trailing bracket group
// (`claude-opus-5-5[1m]`) and a trailing `-YYYYMMDD` date.
func canonicalModelID(model string) string {
	if i := strings.LastIndex(model, "["); i > 0 && strings.HasSuffix(model, "]") {
		model = model[:i]
	}
	return modelDateSuffix.ReplaceAllString(model, "")
}

// modelSettingsKeys is the lookup order for a model's own settings entry: the
// exact id, then its canonical spelling when that differs. An empty model
// (a harness payload without one) looks up nothing and takes the top level.
func modelSettingsKeys(model string) []string {
	if model == "" {
		return nil
	}
	keys := []string{model}
	if c := canonicalModelID(model); c != model {
		keys = append(keys, c)
	}
	return keys
}

// effectiveFill restates the statusline's used_percentage — a share of size,
// the model's full window — as a share of the window the session will
// actually compact at, which is the smaller of size and compactWindow. It
// returns that percentage (capped at 100: a session can sit past the
// compaction point for a moment, and the daemon's threshold compares against
// 0..100) and the window it is a share of. With no auto-compact window, or
// one no smaller than size, or no size at all, the inputs come back as they
// were: this hook only ever restates a number the harness gave it, never
// invents one.
func effectiveFill(usedPct float64, size, compactWindow int64) (float64, int64) {
	if size <= 0 || compactWindow <= 0 || compactWindow >= size {
		return usedPct, size
	}
	pct := usedPct * float64(size) / float64(compactWindow)
	if pct > 100 {
		pct = 100
	}
	return pct, compactWindow
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
//
// It returns the value following --slug ("" when absent, or when --slug is the
// last word): a cell's statusline command carries it, and its absence is what
// selects runCtxHook's report-nowhere mode.
func ctxHookFlags(args []string) string {
	slug := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--serve-addr":
			i++
			if i >= len(args) {
				return slug
			}
			os.Setenv("KRAKEN_SERVE_ADDR", args[i])
		case "--db-path":
			i++
			if i >= len(args) {
				return slug
			}
			os.Setenv("KRAKEN_DB_PATH", args[i])
		case "--slug":
			i++
			if i >= len(args) {
				return slug
			}
			slug = args[i]
		}
	}
	return slug
}

// cmdCtxHook is `kraken ctx-hook`: the statusline command. It reads the
// harness' JSON from stdin, reports the context percentage to kraken serve
// (which is what the daemon's auto-handoff pass, autoHandoffPass, reads),
// and prints the one line the pane renders. With --slug it is a cell's
// statusline, which also
// wraps the user's own statusLine command from ~/.claude/settings.json.
func cmdCtxHook(args []string) {
	runCtxHook(os.Stdin, os.Stdout, ctxHookFlags(args), userStatusLineCommand(util.Home()), util.Home())
}

// userStatusLineCommand returns the statusLine.command string from
// <home>/.claude/settings.json, or "" when the file is missing, unparseable,
// carries no such key, or the command is itself a ctx-hook (wrapping it would
// make the statusline run itself). Never errors.
func userStatusLineCommand(home string) string {
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return ""
	}
	var parsed struct {
		StatusLine struct {
			Command string `json:"command"`
		} `json:"statusLine"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return ""
	}
	cmd := parsed.StatusLine.Command
	if strings.Contains(cmd, "ctx-hook") {
		return ""
	}
	return cmd
}

// runUserStatusLine runs the user's statusline command through /bin/sh -c
// with the same stdin bytes Claude Code gave this hook, bounded by
// ctxHookTimeout. ok is false when it failed, timed out, or printed nothing,
// which sends the caller to its default line.
func runUserStatusLine(command string, stdin []byte) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), ctxHookTimeout)
	defer cancel()
	c := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	c.Stdin = bytes.NewReader(stdin)
	c.Stderr = io.Discard
	// A grandchild that inherited stdout would otherwise keep Output waiting
	// after the shell is killed; WaitDelay bounds that wait.
	c.WaitDelay = 100 * time.Millisecond
	out := &cappedBuffer{max: userStatusLineMaxBytes}
	c.Stdout = out
	if err := c.Run(); err != nil || ctx.Err() != nil {
		return "", false
	}
	line := strings.TrimRight(out.buf.String(), "\r\n")
	if strings.TrimSpace(line) == "" {
		return "", false
	}
	return line, true
}

// userStatusLineMaxBytes caps how much of the wrapped user statusline's stdout
// runUserStatusLine keeps; bytes past it are discarded.
const userStatusLineMaxBytes = 64 << 10

// cappedBuffer keeps the first max bytes written to it and discards the rest.
// Write always reports the full length so the writing command is never handed
// a short write; only what is retained is bounded.
type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.buf.Len(); room > 0 {
		if len(p) > room {
			b.buf.Write(p[:room])
		} else {
			b.buf.Write(p)
		}
	}
	return len(p), nil
}

// runCtxHook is cmdCtxHook's body, split out so the whole path is exercisable
// with plain readers and writers.
//
// Order matters: the line is computed from what stdin said, the report is
// attempted, and the LINE IS PRINTED WHATEVER HAPPENED to the report. A
// statusline that only renders when the daemon answers is a statusline that
// vanishes exactly when something is wrong.
//
// A non-empty slug is a cell's: report to /api/cell/context keyed on the slug,
// and print the user's own statusline (userStatusLine, run with the same
// stdin) when there is one and it produced output, else `<slug> NN%`.
// slug == "" reports nowhere and prints `ctx NN%` (see the file header).
//
// home is the directory whose <home>/.claude/settings.json names the
// auto-compact window (autoCompactWindow); "" reads none, and the harness'
// own percentage is then reported and printed unchanged.
func runCtxHook(stdin io.Reader, stdout io.Writer, slug string, userStatusLine string, home string) {
	var in statuslineInput
	// A read or decode failure is not reported anywhere: there is nowhere to
	// report it to that is not the pane's chrome. The zero value falls
	// through to the em-dash line, which is the honest rendering of "the
	// harness said nothing this render".
	var raw []byte
	if b, err := io.ReadAll(io.LimitReader(stdin, 1<<20)); err == nil {
		raw = b
		_ = json.Unmarshal(raw, &in)
	}
	pct := in.ContextWindow.UsedPercentage
	window := in.ContextWindow.ContextWindowSize
	if pct != nil {
		// Restated as a share of the window the session will actually
		// compact at (effectiveFill): the number the daemon's handoff
		// threshold means, and the one the fallback line below prints. The
		// wrapped user statusline still gets the untouched stdin bytes.
		fill, w := effectiveFill(*pct, window, autoCompactWindow(home, in.Model.ID))
		pct, window = &fill, w
	}
	if in.SessionID != "" && pct != nil && slug != "" {
		// Errors dropped by contract: see the file header. A skipped report
		// costs one render's telemetry, and the next render sends another.
		_ = hookPost("/api/cell/context", map[string]any{
			"slug":                slug,
			"session_id":          in.SessionID,
			"used_percentage":     *pct,
			"context_window_size": window,
			"total_input_tokens":  in.ContextWindow.TotalInputTokens,
		})
	}
	label := "ctx"
	if slug != "" {
		label = slug
		if userStatusLine != "" {
			if line, ok := runUserStatusLine(userStatusLine, raw); ok {
				fmt.Fprintln(stdout, line)
				return
			}
		}
	}
	// The em-dash rather than "0%" for statuslineInput's reason: an unknown
	// fullness must never render as an empty window, which is the reading a
	// zero invites at exactly the moment a human glances at the pane.
	line := label + " —"
	if pct != nil {
		line = fmt.Sprintf("%s %.1f%%", label, *pct)
	}
	fmt.Fprintln(stdout, line)
}

// hookPost is the hook's one round trip: POST JSON to kraken serve, bounded
// at ctxHookTimeout, with the response body discarded.
//
// It deliberately does NOT reuse qfPost. That helper's whole value is turning
// a failure into a sentence a human or an agent reads and an exit code — the
// opposite of what a hook needs, and it carries the 5s timeout besides. The
// error returned here exists only so a caller CAN branch on it; the caller
// drops it, and that is the contract rather than an oversight.
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
	// what makes the discard explicit — every response on this route is
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
