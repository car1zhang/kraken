package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// --- k b ---

func claudeLauncher() string {
	if c := os.Getenv("KRAKEN_CLAUDE"); c != "" {
		return c
	}
	if p, err := exec.LookPath("claude-patch-buddy"); err == nil {
		return p
	}
	return "claude"
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
	args := []string{claudeLauncher()}
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

func shellCmdString(args []string) string {
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

// branchGuard rejects peers whose session can't be forked safely.
func branchGuard(p *Peer) error {
	if p.Role == "shell" {
		return fmt.Errorf("%s is a shell pane — nothing to fork", displayName(p))
	}
	if p.Agent == "codex" {
		return fmt.Errorf("branching Codex sessions is not supported yet")
	}
	if p.SessionID == "" {
		return fmt.Errorf("no session id resolved for %s", displayName(p))
	}
	if p.SessionIDGuessed {
		return fmt.Errorf("session id for %s is a guess (no session file) — could fork another claude's session", displayName(p))
	}
	return nil
}

// forkCmdString builds the shell command that reopens p's session as a fork,
// replaying the permission mode it was running under.
func forkCmdString(p *Peer) string {
	return shellCmdString(claudeArgs(transcriptPath(p.SessionID), "--fork-session", "-r", p.SessionID))
}

// peerCWD is where a fork of p should start; in-process teammates may have
// no cwd of their own.
func peerCWD(p *Peer) string {
	if p.CWD == "" {
		return home()
	}
	return p.CWD
}

// branchPeerWindow forks p's session into a new tmux window in p's own
// session and jumps to it — the viewer path, where splitting next to the
// caller (the viewer) or the target's pane would steal screen space.
func branchPeerWindow(p *Peer) (string, error) {
	if err := branchGuard(p); err != nil {
		return "", err
	}
	if p.TmuxSession == "" {
		return "", fmt.Errorf("%s has no tmux session (in-process teammate)", displayName(p))
	}
	// "<session>:" targets the session itself: the new window appends at the
	// next free index and becomes the session's current window
	if err := exec.Command("tmux", "new-window", "-t", p.TmuxSession+":", "-c", peerCWD(p), forkCmdString(p)).Run(); err != nil {
		return "", fmt.Errorf("tmux new-window: %v", err)
	}
	msg := fmt.Sprintf("forked %s → new window in %s", displayName(p), p.TmuxSession)
	out, err := raiseSession(p.TmuxSession)
	if err != nil {
		return "", fmt.Errorf("%s, but %v", msg, err)
	}
	switch {
	case out.attached:
		msg += " (attached in a new ghostty window)"
	case out.focusErr != nil:
		msg += fmt.Sprintf(" — focus its window manually: %v", out.focusErr)
	}
	return msg, nil
}

func cmdBranch(args []string) {
	if os.Getenv("TMUX") == "" && os.Getenv("TMUX_PANE") == "" {
		die("k b must run inside tmux")
	}
	all, _ := orderAll(discoverAll())
	var p *Peer
	if len(args) > 0 {
		p = selectPeer(strings.Join(args, " "), all)
	} else {
		p = currentPanePeer(all)
		if p == nil {
			die("no claude in the current pane — pass a selector (k b <sel>)")
		}
	}
	if err := branchGuard(p); err != nil {
		die("%v", err)
	}
	cwd := peerCWD(p)
	shellCmd := forkCmdString(p)
	// split next to the caller, not the target — k b <sel> must not hijack
	// the peer's screen space
	target := os.Getenv("TMUX_PANE")
	if target == "" {
		target = p.Pane
	}
	cmd := exec.Command("tmux", "split-window", "-h", "-t", target, "-c", cwd, shellCmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		die("tmux split-window: %v", err)
	}
}
