package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// DefaultModel is the claude model every claude kraken STARTS runs on — a
// cell's zeus, `k c`, and the board's spawn action all route through ModelArgs
// below (`k b` doesn't: a fork resumes a session rather than starting a new
// agent — see ForkCmdString). It is an exact model id rather than the `opus`
// alias so the tier kraken runs on only ever moves when someone edits this
// line; the alias would silently follow whatever claude ships next. Subagent
// models (hephaestus, athena, hermes) are a separate axis, pinned in
// zeus' brief (jobs/crew.go), since the Agent tool takes tier aliases, not ids —
// they land on the same tier (`model: opus`), so every agent in a cell is Opus 5.
const DefaultModel = "claude-opus-5"

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
		if isShellSafe(a) {
			b.WriteString(a)
		} else {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
			b.WriteByte('\'')
		}
	}
	return b.String()
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
// Deliberately NOT a DefaultModel spawn site: a fork continues an existing
// conversation rather than starting a new agent, so it inherits whatever the
// resumed session runs on — and p.Model can't stand in either, since discovery
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
