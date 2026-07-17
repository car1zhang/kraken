package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// --- k n / k c ---

// splitDirFlag peels an optional "-c <dir>" off the front of args, returning
// the start directory (caller's cwd by default) and the remaining args.
func splitDirFlag(args []string) (string, []string) {
	if len(args) >= 1 && args[0] == "-c" {
		if len(args) < 2 {
			die("-c needs a directory")
		}
		return args[1], args[2:]
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = home()
	}
	return dir, args
}

// newWindow opens a new window in the caller's tmux session running argv (an
// interactive shell when empty) and reports where it landed.
func newWindow(dir string, argv []string) string {
	if os.Getenv("TMUX") == "" {
		die("must run inside tmux")
	}
	// tmux silently falls back to $HOME on a bad -c dir; a typo should fail
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		die("not a directory: %s", dir)
	}
	tmuxArgs := []string{"new-window", "-P", "-F", "#{session_name}:#{window_index} (#{pane_id})", "-c", dir}
	tmuxArgs = append(tmuxArgs, argv...)
	out, err := exec.Command("tmux", tmuxArgs...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			die("tmux new-window: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		die("tmux new-window: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func userShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/zsh"
}

func cmdNewShell(args []string) {
	dir, rest := splitDirFlag(args)
	var argv []string
	if len(rest) > 0 {
		// run the command, then drop into an interactive shell so the pane
		// (and the command's output) survives the command exiting; exec goes
		// on its own line so a trailing comment, ';' or '&' can't eat it
		sh := userShell()
		argv = []string{sh, "-c", strings.Join(rest, " ") + "\nexec " + sh}
	}
	at := newWindow(dir, argv)
	if len(rest) > 0 {
		fmt.Printf("running %q in new window %s\n", strings.Join(rest, " "), at)
	} else {
		fmt.Printf("opened shell in new window %s\n", at)
	}
}

func cmdNewClaude(args []string) {
	dir, rest := splitDirFlag(args)
	if len(rest) == 0 {
		die("usage: k c [-c dir] <name> [prompt...]")
	}
	name := rest[0]
	argv := []string{claudeLauncher(), "--name", name}
	if prompt := strings.Join(rest[1:], " "); prompt != "" {
		// "--" so a prompt starting with "-" can't be read as a claude flag
		argv = append(argv, "--", prompt)
	}
	at := newWindow(dir, argv)
	fmt.Printf("started claude %q in new window %s\n", name, at)
}

// --- k o ---

func cmdOpen(args []string) {
	if len(args) == 0 {
		die("usage: k o <sel>")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(strings.Join(args, " "), all)
	msg, err := openPeer(p)
	if err != nil {
		die("%v", err)
	}
	fmt.Println(msg)
}

// raiseOutcome reports how raiseSession made a session visible: a fresh
// Ghostty window was attached, or an existing one was focused (focusErr set
// when the raise failed and the user must switch windows manually).
type raiseOutcome struct {
	attached bool
	focusErr error
}

// raiseSession brings a tmux session's Ghostty window to the front, attaching
// it in a new Ghostty window if no client is attached.
func raiseSession(session string) (raiseOutcome, error) {
	// the caller may hold a snapshot up to a second old — without this check
	// a dead session would pop an empty Ghostty window that fails to attach
	if err := exec.Command("tmux", "has-session", "-t", session).Run(); err != nil {
		return raiseOutcome{}, fmt.Errorf("tmux session %s is gone", session)
	}
	out, err := exec.Command("tmux", "list-clients", "-t", session, "-F", "#{client_tty}").Output()
	attached := err == nil && strings.TrimSpace(string(out)) != ""
	if !attached {
		if err := exec.Command("open", "-na", "Ghostty.app", "--args",
			"-e", "tmux", "attach-session", "-t", session).Run(); err != nil {
			return raiseOutcome{}, fmt.Errorf("open Ghostty: %v", err)
		}
		return raiseOutcome{attached: true}, nil
	}
	return raiseOutcome{focusErr: focusGhosttyWindow(session)}, nil
}

// openPeer jumps to a claude: selects its tmux window/pane and raises its
// Ghostty window. Returns a human-readable note on success.
func openPeer(p *Peer) (string, error) {
	if p.Pane == "" {
		return "", fmt.Errorf("%s has no pane (in-process teammate)", displayName(p))
	}
	// point the session at the target pane first: attached clients follow
	// immediately, and a fresh attach lands directly on it
	_ = exec.Command("tmux", "select-window", "-t", p.Pane).Run()
	_ = exec.Command("tmux", "select-pane", "-t", p.Pane).Run()
	out, err := raiseSession(p.TmuxSession)
	if err != nil {
		return "", err
	}
	switch {
	case out.attached:
		return fmt.Sprintf("attached %s in a new ghostty window → %s (%s)", p.TmuxSession, displayName(p), p.Pane), nil
	case out.focusErr != nil:
		return fmt.Sprintf("selected %s (%s) — focus its window manually: %v", displayName(p), p.Pane, out.focusErr), nil
	default:
		return fmt.Sprintf("focused %s (%s, pane %s)", displayName(p), p.TmuxSession, p.Pane), nil
	}
}

// focusGhosttyWindow raises the Ghostty window whose title starts with
// "<session>:" — requires tmux set-titles on (title-string leads with #S) and
// Accessibility access for the caller.
func focusGhosttyWindow(session string) error {
	// iterate every Ghostty process: windows kraken opens via `open -na`
	// belong to a second app instance
	script := fmt.Sprintf(`with timeout of 8 seconds
tell application "System Events"
	repeat with proc in (every process whose name is "Ghostty")
		tell proc
			repeat with w in windows
				if title of w starts with %q then
					set frontmost to true
					perform action "AXRaise" of w
					return "ok"
				end if
			end repeat
		end tell
	end repeat
end tell
end timeout
return "notfound"`, session+":")
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return fmt.Errorf("osascript (accessibility granted?): %v", err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("no ghostty window titled %q (is 'set-titles on' active?)", session+":")
	}
	return nil
}

// --- k s ---

func cmdSend(args []string) {
	if len(args) == 0 {
		die("usage: k s <sel> <message...>")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(args[0], all)
	if p.Role == "shell" {
		// checked again in sendToPeer, but early-exit here so a shell target
		// never blocks on a stdin read that would otherwise follow
		die("%s is a shell pane — the text would run as a shell command", displayName(p))
	}
	msg := strings.Join(args[1:], " ")
	if msg == "" {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			die("no message given (pass it as args or pipe it on stdin)")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			die("read stdin: %v", err)
		}
		msg = strings.TrimRight(string(data), "\n")
	}
	queued, err := sendToPeer(p, msg)
	if err != nil {
		die("%v", err)
	}
	if queued {
		fmt.Fprintf(os.Stderr, "kraken: %s is mid-turn — the message queued\n", displayName(p))
	}
	fmt.Printf("sent to %s (%s)\n", displayName(p), p.Pane)
}

// sendToPeer runs cmdSend's full guard chain plus the tmux send mechanics,
// returning whether the message queued behind a mid-turn agent. Shared by
// cmdSend and the HTTP send endpoint so both refuse on identical guards.
func sendToPeer(p *Peer, msg string) (queued bool, err error) {
	if p.Role == "shell" {
		return false, fmt.Errorf("%s is a shell pane — the text would run as a shell command", displayName(p))
	}
	if strings.TrimSpace(msg) == "" {
		return false, fmt.Errorf("empty message")
	}
	if !p.Messageable || p.Pane == "" {
		return false, fmt.Errorf("%s is an in-process teammate — message its lead instead", displayName(p))
	}
	if p.Depth > 0 {
		return false, fmt.Errorf("%s is not the foreground agent in pane %s — keys would land in the nested one", displayName(p), p.Pane)
	}
	queued = p.Status == "running"
	if err := sendMessage(p, msg); err != nil {
		return false, err
	}
	return queued, nil
}

// sendMessage runs the send-time safety checks and types msg into p's prompt,
// submitting it with Enter. Shared by k s, the bus, and the daemon — every
// path that talks to an agent goes through the same guards.
func sendMessage(p *Peer, msg string) error {
	if err := checkSendTarget(p); err != nil {
		return err
	}
	if p.Status == "shell" {
		// text typed at the ! prompt executes as a shell command on Enter
		return fmt.Errorf("%s is in shell mode — sending now would run the message as a shell command", displayName(p))
	}
	// re-check right before typing: a permission/question prompt has an option
	// pre-highlighted, so our trailing Enter would answer it for the user
	if p.Status == "question" || paneShowsQuestion(p.Pane) {
		return fmt.Errorf("%s has a question/permission prompt open — sending would answer it; resolve it first", displayName(p))
	}
	if strings.Contains(msg, "\n") {
		load := exec.Command("tmux", "load-buffer", "-b", "kraken", "-")
		load.Stdin = strings.NewReader(msg)
		if err := load.Run(); err != nil {
			return fmt.Errorf("tmux load-buffer: %v", err)
		}
		if err := exec.Command("tmux", "paste-buffer", "-p", "-d", "-b", "kraken", "-t", p.Pane).Run(); err != nil {
			return fmt.Errorf("tmux paste-buffer: %v", err)
		}
	} else {
		if err := exec.Command("tmux", "send-keys", "-t", p.Pane, "-l", "--", msg).Run(); err != nil {
			return fmt.Errorf("tmux send-keys: %v", err)
		}
	}
	// the TUI's paste detection must drain the literal keys before Enter
	// lands, or the newline gets swallowed into the message
	time.Sleep(150 * time.Millisecond)
	if err := exec.Command("tmux", "send-keys", "-t", p.Pane, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %v", err)
	}
	return nil
}

// checkSendTarget re-resolves the pane's process tree immediately before
// typing: the target must still be a claude under this exact pane. A bare
// pid-exists check would pass for zombies and recycled PIDs — and then the
// message would be typed at (and run by) the surviving pane shell.
func checkSendTarget(p *Peer) error {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", p.Pane, "#{pane_pid}").Output()
	if err != nil {
		return fmt.Errorf("pane %s is gone — rerun 'k ls'", p.Pane)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || panePID <= 0 {
		return fmt.Errorf("pane %s is gone — rerun 'k ls'", p.Pane)
	}
	children, cmds, _ := psSnapshot()
	for _, hit := range agentPIDsUnder(panePID, children, cmds) {
		if hit.pid == p.PID && hit.agent == p.Agent {
			return nil
		}
	}
	return fmt.Errorf("%s (pane %s) is gone — rerun 'k ls'", displayName(p), p.Pane)
}

// --- k k ---

func cmdKill(args []string) {
	if len(args) == 0 {
		die("usage: k k <sel>")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(strings.Join(args, " "), all)
	msg, err := killPeer(p)
	if err != nil {
		die("%v", err)
	}
	fmt.Println(msg)
}

// killPeer closes a pane, agent or shell. The caller's own pane and panes
// holding more than one agent are refused; everything else is re-verified
// immediately before the kill.
func killPeer(p *Peer) (string, error) {
	if p.Pane == "" {
		return "", fmt.Errorf("%s is an in-process teammate — no pane to kill", displayName(p))
	}
	if p.Depth > 0 {
		// Depth > 0 means another agent runs nested inside this one; the
		// multi-agent refusal in verifyKillTarget catches the reverse case
		// (killing the nested one), but this fails before any tmux exec
		return "", fmt.Errorf("%s shares its pane with a nested agent — killing the pane would take both down", displayName(p))
	}
	if p.Pane == os.Getenv("TMUX_PANE") {
		return "", fmt.Errorf("refusing to kill the current pane")
	}
	if err := verifyKillTarget(p); err != nil {
		return "", err
	}
	if err := exec.Command("tmux", "kill-pane", "-t", p.Pane).Run(); err != nil {
		return "", fmt.Errorf("tmux kill-pane: %v", err)
	}
	desc := p.Agent
	if desc == "" {
		desc = "claude"
	}
	if p.Role == "shell" {
		desc = "idle"
		if p.Cmd != "" {
			desc = "was running " + truncateOneLine(p.Cmd, 40)
		}
	}
	return fmt.Sprintf("killed %s (%s, pane %s)", displayName(p), desc, p.Pane), nil
}

// verifyKillTarget re-resolves the pane immediately before the kill so a
// stale snapshot can't take down something the user never saw: a shell pane
// must still be agent-free with the same root process, and an agent must
// still be running alone in its pane.
func verifyKillTarget(p *Peer) error {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", p.Pane, "#{pane_pid}").Output()
	if err != nil {
		return fmt.Errorf("pane %s is already gone", p.Pane)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || panePID <= 0 {
		return fmt.Errorf("pane %s is already gone", p.Pane)
	}
	children, cmds, _ := psSnapshot()
	agents := agentPIDsUnder(panePID, children, cmds)
	if p.Role == "shell" {
		if panePID != p.PID {
			// respawned pane root: the pane no longer runs what the user saw
			return fmt.Errorf("pane %s changed since the last snapshot — rerun 'k ls'", p.Pane)
		}
		if len(agents) > 0 {
			return fmt.Errorf("pane %s has an agent in it now — rerun 'k ls'", p.Pane)
		}
		return nil
	}
	if len(agents) > 1 {
		// a second agent nested inside the target since the snapshot (or the
		// target is the host of one): the pane kill would take both down
		return fmt.Errorf("pane %s holds %d agents — kill would take them all down; rerun 'k ls'", p.Pane, len(agents))
	}
	for _, hit := range agents {
		if hit.pid == p.PID && hit.agent == p.Agent {
			return nil
		}
	}
	return fmt.Errorf("%s is no longer in pane %s — rerun 'k ls'", displayName(p), p.Pane)
}
