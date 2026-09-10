package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// --- k b ---

func ClaudeLauncher() string {
	if c := os.Getenv("KRAKEN_CLAUDE"); c != "" {
		return c
	}
	if p, err := exec.LookPath("claude-patch-buddy"); err == nil {
		return p
	}
	return "claude"
}

// CodexLauncher is the codex analogue of ClaudeLauncher: the override env var
// KRAKEN_CODEX verbatim when set, else the bare name "codex". It mirrors
// ClaudeLauncher exactly — no exec.LookPath fallback beyond the bare name —
// since a launcher name is all any caller needs and PATH resolution is the
// shell's job at spawn time.
func CodexLauncher() string {
	if c := os.Getenv("KRAKEN_CODEX"); c != "" {
		return c
	}
	return "codex"
}

// codexMinVersion is the version floor separating the two codex generations
// installed on this machine — 0.39.0 (stale) below it, 0.144.6 (current) at or
// above. codexVersionAtLeast compares a `codex --version` line against it; the
// spawn-time refusal that consumes that comparison is a later PR.
const codexMinVersion = "0.100.0"

// codexVersionTriple pulls the first dotted X.Y.Z triple out of a version
// string — `codex-cli 0.144.6`, with leading/trailing whitespace and any
// trailing text after the version tolerated.
var codexVersionTriple = regexp.MustCompile(`(\d+)\.(\d+)\.(\d+)`)

// codexVersionAtLeast reports whether the X.Y.Z version in versionOutput is at
// least floor, comparing per component NUMERICALLY (so 0.39.0 reads as less
// than 0.100.0, which a string compare gets wrong). It takes the version
// output as a string — it never runs `codex --version` itself, so it is pure
// and needs no binary to test. Input with no X.Y.Z triple is unparseable and
// returns false: an unidentifiable binary is not trusted (fail-closed).
func codexVersionAtLeast(versionOutput, floor string) bool {
	got := codexVersionTriple.FindStringSubmatch(versionOutput)
	if got == nil {
		return false
	}
	want := codexVersionTriple.FindStringSubmatch(floor)
	if want == nil {
		return false
	}
	for i := 1; i <= 3; i++ {
		// Submatches are all-digit by the regex; the only Atoi error left is
		// int64 overflow (a component >= 2^63), unreachable for a real codex
		// version string, and a dropped error there floors the component at 0.
		g, _ := strconv.Atoi(got[i])
		w, _ := strconv.Atoi(want[i])
		if g != w {
			return g > w
		}
	}
	return true
}

// DefaultModel is the claude model kraken STARTS a general-purpose claude on —
// `k c`, the board's spawn action, and a cell's side threads all route through
// ModelArgs below (`k b` doesn't: a fork resumes a session rather than starting
// a new agent — see ForkCmdString). It is an exact model id rather than the
// `opus` alias so the tier kraken runs on only ever moves when someone edits
// this line; the alias would silently follow whatever claude ships next.
// Subagent models (hephaestus, athena, hermes) are a separate axis, pinned in
// zeus' brief (jobs/crew.go), since the Agent tool takes tier aliases, not ids —
// they land on this same tier (`model: opus`), so every agent zeus spawns is
// Opus 5.
const DefaultModel = "claude-opus-5"

// ZeusModel is the one exception to DefaultModel: a cell's zeus runs on Fable,
// which is the fast tier, while the crew it dispatches stays on Opus. Zeus is
// the pane carl steers and the agent that plans, briefs and integrates —
// turnaround there is what the cell's latency is made of, and the work that
// wants the deeper tier is the slices and reviews, which are delegated. A job's
// `model:` field overrides it for that cell. `jobs.zeusModel` is the only
// reader that puts it on a command line (`jobs.JobTemplate` also reads it, but
// only to print a new job file's documented default), so `k c` and the board's
// spawn action never see it.
const ZeusModel = "claude-fable-5"

// IrisModel is the tier the Slack intake agent runs on. Iris is a triage loop
// — read a bumped inbox batch, decide ignore / draft / edit, write a short job
// body — which is exactly the shape ZeusModel exists for: it is the pane that
// has to keep up with carl's Slack rather than the one doing deep work, and it
// delegates nothing. It is its own constant rather than an alias of ZeusModel
// because the two answer different questions, and pinning them separately is
// what lets either move without dragging the other. Like both of its
// neighbours it is an exact model id, never the `fable` alias.
const IrisModel = "claude-fable-5"

// KronosModel is the tier the orchestrator agent runs on. Kronos is the pane
// that reads an inbox of board transitions and carl's instructions (typed
// through the board's kronos panel) and decides what to do about each one —
// steer a cell, answer a question, receipt back — which is IrisModel's shape
// rather than ZeusModel's: it holds no worktree, delegates nothing, and its
// latency is how long carl waits for a reply on the board.
// Its own constant rather than an alias of either neighbour for the reason
// IrisModel gives above: the three answer different questions — how fast a
// cell lead must turn around, what tier a Slack triage loop needs, what tier
// steers the whole board — and pinning them separately is what lets any one of
// them move without dragging the others. Exact model id, never the `fable`
// alias, like all three of its neighbours. Unlike ZeusModel there is no
// resolver in front of it: kronos belongs to no job, so no `model:` field
// exists anywhere that could override it, and jobs.kronosSpawnCommand puts this
// constant on the command line directly.
const KronosModel = "claude-fable-5"

// ThreadModels is the menu a SIDE THREAD may be branched on, alias → exact
// model id. It is the one place in the binary where a tier alias is RESOLVED
// to an id: the board's branch overlay sends one of these four keys and
// nothing else, so a thread's own pick never reaches a command line as an
// alias, which is the rule the three constants above are written for (an alias
// silently follows whatever claude ships next). A job's `model:` field is not
// covered by any of this — it is free-form wire text and rides through to
// --model exactly as carl typed it (jobs.threadModel returns it verbatim).
//
// The four values are literals rather than references to DefaultModel and
// ZeusModel, even where they hold the same id today, for the reason those two
// constants are already separate from each other: this map answers "what may
// carl pick for a question he is asking on the side", while they answer what
// kraken STARTS a general-purpose claude and a cell lead on. Writing
// `"opus": DefaultModel` would make a change to either constant silently
// re-aim the picker's opus entry at something else.
//
// A thread with no explicit pick is not in here at all — it falls back to the
// job's model: field and then DefaultModel (jobs.threadSpawnCommand), which is
// the behaviour every thread had before the picker existed.
var ThreadModels = map[string]string{
	"haiku":  "claude-haiku-4-5-20251001",
	"sonnet": "claude-sonnet-5",
	"opus":   "claude-opus-5",
	"fable":  "claude-fable-5",
}

// ModelArgs is the `--model` flag pair for a spawn: the caller's explicit model
// (a job's `model:` field) when it has one, else DefaultModel. Always returns a
// flag — kraken never leaves the model to claude's own config, so the model a
// kraken-spawned agent runs on is always visible in its command line (which is
// also where discovery reads it back from).
func ModelArgs(model string) []string {
	if model == "" {
		model = DefaultModel
	}
	return []string{"--model", model}
}

// readPermissionMode scans a transcript for the last permission-mode record so
// forks reopen with the same flags the session was running under.
func readPermissionMode(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	last := ""
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"permission-mode"`) {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if t, _ := rec["type"].(string); t != "permission-mode" {
			continue
		}
		if m, ok := rec["permissionMode"].(string); ok && m != "" {
			last = m
		}
	}
	return last
}

func claudeArgs(sessionPath string, extra ...string) []string {
	args := []string{ClaudeLauncher()}
	switch mode := readPermissionMode(sessionPath); mode {
	case "bypassPermissions":
		args = append(args, "--dangerously-skip-permissions")
	case "", "default":
		// no flag needed
	default:
		args = append(args, "--permission-mode", mode)
	}
	return append(args, extra...)
}

func ShellCmdString(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(ShellQuote(a))
	}
	return b.String()
}

// ShellQuote renders one argument as a single shell word: verbatim when it
// needs no quoting, single-quoted otherwise.
func ShellQuote(s string) string {
	if isShellSafe(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ShellReadFileArg renders a shell word that expands — in the pane's own
// shell, at launch — to the entire contents of path: `"$(cat '<path>')"`.
//
// tmux refuses any command longer than ~16KB with a bare `command too long`,
// and the two arguments a cell spawn carries whose length is driven by content
// rather than by kraken (zeus' system prompt and the job spec) had grown past
// that ceiling together, wedging every StartCell. Both are written to files
// and passed through this instead, so the command line stays a few hundred
// bytes no matter how long either gets. The surrounding double quotes keep the
// substitution one word through any whitespace in the file; command
// substitution strips trailing newlines, which is exactly what a
// file-with-trailing-newline wants.
func ShellReadFileArg(path string) string {
	return `"$(cat ` + ShellQuote(path) + `)"`
}

// isShellSafe allowlists args that need no quoting; everything else gets
// single-quoted rather than trying to enumerate shell metacharacters.
func isShellSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '/', r == '-', r == '=':
		default:
			return false
		}
	}
	return true
}

// BranchGuard rejects peers whose session can't be forked safely.
func BranchGuard(p *Peer) error {
	if p.Role == "shell" {
		return fmt.Errorf("%s is a shell pane — nothing to fork", DisplayName(p))
	}
	if p.Agent == "codex" {
		return fmt.Errorf("branching Codex sessions is not supported yet")
	}
	if p.SessionID == "" {
		return fmt.Errorf("no session id resolved for %s", DisplayName(p))
	}
	if p.SessionIDGuessed {
		return fmt.Errorf("session id for %s is a guess (no session file) — could fork another claude's session", DisplayName(p))
	}
	return nil
}

// ForkCmdString builds the shell command that reopens p's session as a fork,
// replaying the permission mode it was running under.
//
// Deliberately not a model-spawn site at all — neither DefaultModel nor
// ZeusModel: a fork continues an existing conversation rather than starting a
// new agent, so it inherits whatever the resumed session runs on (which is why
// `k b` off a cell's zeus stays on zeus' tier, whatever that resolved to, while
// a kraken side thread — a different agent reading the same worktree — does
// pass an explicit --model). p.Model can't stand in either, since discovery
// stores it display-shortened (claude-opus-5 → opus-5), which is not a value
// `--model` accepts.
func ForkCmdString(p *Peer) string {
	return ShellCmdString(claudeArgs(transcriptPath(p.SessionID), "--fork-session", "-r", p.SessionID))
}

// PeerCWD is where a fork of p should start; in-process teammates may have
// no cwd of their own.
func PeerCWD(p *Peer) string {
	if p.CWD == "" {
		return util.Home()
	}
	return p.CWD
}

// BranchPeerWindow forks p's session into a new tmux window in p's own
// session and jumps to it — the viewer path, where splitting next to the
// caller (the viewer) or the target's pane would steal screen space.
func BranchPeerWindow(p *Peer) (string, error) {
	if err := BranchGuard(p); err != nil {
		return "", err
	}
	if p.TmuxSession == "" {
		return "", fmt.Errorf("%s has no tmux session (in-process teammate)", DisplayName(p))
	}
	// "<session>:" targets the session itself: the new window appends at the
	// next free index and becomes the session's current window
	if err := exec.Command("tmux", "new-window", "-t", p.TmuxSession+":", "-c", PeerCWD(p), ForkCmdString(p)).Run(); err != nil {
		return "", fmt.Errorf("tmux new-window: %v", err)
	}
	msg := fmt.Sprintf("forked %s → new window in %s", DisplayName(p), p.TmuxSession)
	out, err := RaiseSession(p.TmuxSession)
	if err != nil {
		return "", fmt.Errorf("%s, but %v", msg, err)
	}
	switch {
	case out.Attached:
		msg += " (attached in a new ghostty window)"
	case out.FocusErr != nil:
		msg += fmt.Sprintf(" — focus its window manually: %v", out.FocusErr)
	}
	return msg, nil
}
