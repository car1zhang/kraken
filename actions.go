package main

import (
	"bytes"
	"errors"
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

// errBadSpawnDir marks a not-a-directory dir so HTTP callers can tell a
// malformed client parameter (400) apart from an environment/tmux failure
// (409).
var errBadSpawnDir = errors.New("not a directory")

// checkSpawnDir verifies dir exists as a directory, wrapping errBadSpawnDir
// on failure. Exported as its own step (rather than folded only into
// newWindowIn) so the HTTP spawn handler can fail a malformed dir before
// ensureSession does any tmux work for a request that's doomed anyway.
func checkSpawnDir(dir string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: %s", errBadSpawnDir, dir)
	}
	return nil
}

// newWindowIn is newWindow's error-returning core, targeted at an explicit
// tmux session rather than the caller's own — the piece the HTTP spawn
// endpoint needs (no $TMUX, no die()).
func newWindowIn(session, dir string, argv []string) (string, error) {
	// tmux silently falls back to $HOME on a bad -c dir; a typo should fail
	if err := checkSpawnDir(dir); err != nil {
		return "", err
	}
	tmuxArgs := []string{"new-window", "-P", "-F", "#{session_name}:#{window_index} (#{pane_id})", "-t", session + ":", "-c", dir}
	tmuxArgs = append(tmuxArgs, argv...)
	out, err := exec.Command("tmux", tmuxArgs...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("tmux new-window: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("tmux new-window: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureSession makes sure a detached tmux session named name exists,
// creating it rooted at dir if not.
func ensureSession(name, dir string) error {
	if exec.Command("tmux", "has-session", "-t", name).Run() == nil {
		return nil
	}
	var stderr bytes.Buffer
	cmd := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", dir)
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// a concurrent spawn may have won the race and created the session
		// between our has-session check above and this new-session call
		if exec.Command("tmux", "has-session", "-t", name).Run() == nil {
			return nil
		}
		if stderr.Len() > 0 {
			return fmt.Errorf("tmux new-session: %s", strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("tmux new-session: %v", err)
	}
	return nil
}

// currentPaneSession returns the tmux session a pane currently belongs to,
// read fresh rather than trusted from a stale Peer snapshot — the pane may
// have moved sessions, or vanished, since discovery ran.
func currentPaneSession(pane string) (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{session_name}").Output()
	if err != nil {
		return "", fmt.Errorf("pane %s is gone", pane)
	}
	return strings.TrimSpace(string(out)), nil
}

// buildSpawnArgv builds the argv for a spawned pane from the HTTP API's kind:
// "shell" mirrors cmdNewShell's command-then-drop-into-shell pattern, "claude"
// mirrors cmdNewClaude's launcher + "--" prompt guard.
func buildSpawnArgv(kind, name, prompt, cmd string) ([]string, error) {
	switch kind {
	case "shell":
		if cmd == "" {
			return nil, nil
		}
		sh := userShell()
		return []string{sh, "-c", cmd + "\nexec " + sh}, nil
	case "claude":
		if name == "" {
			return nil, fmt.Errorf("claude spawn needs a name")
		}
		argv := []string{claudeLauncher(), "--dangerously-skip-permissions", "--name", name}
		if prompt != "" {
			// "--" so a prompt starting with "-" can't be read as a claude flag
			argv = append(argv, "--", prompt)
		}
		return argv, nil
	default:
		return nil, fmt.Errorf("kind must be \"shell\" or \"claude\"")
	}
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

// sendToShell types a command line into a shell pane and runs it with Enter —
// the deliberate counterpart to sendToPeer's shell refusal, used only by the
// HTTP endpoint where the caller has explicitly targeted a shell. Re-verifies
// the pane is still agent-free right before typing so a stale snapshot can't
// send a command into a pane an agent has since claimed.
func sendToShell(p *Peer, cmd string) error {
	if p.Role != "shell" {
		return fmt.Errorf("%s is not a shell pane", displayName(p))
	}
	if p.Pane == "" {
		return fmt.Errorf("no pane to send to")
	}
	if strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("empty command")
	}
	if err := verifyKillTarget(p); err != nil {
		// verifyKillTarget's shell branch is exactly the check we want:
		// pane still exists, same root process, no agent claimed it
		return err
	}
	if strings.Contains(cmd, "\n") {
		return fmt.Errorf("shell input must be a single line")
	}
	if err := exec.Command("tmux", "send-keys", "-t", p.Pane, "-l", "--", cmd).Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if err := exec.Command("tmux", "send-keys", "-t", p.Pane, "Enter").Run(); err != nil {
		return fmt.Errorf("tmux send-keys Enter: %v", err)
	}
	return nil
}

// namedKeys maps the lowercase key names the HTTP API and iOS app accept to
// the tmux send-keys key name they translate to — a whitelist so an
// arbitrary client-supplied string can never reach tmux as a raw key
// argument. "ctrl-a".."ctrl-z" are generated below rather than written out.
var namedKeys = map[string]string{
	"esc":       "Escape",
	"escape":    "Escape",
	"enter":     "Enter",
	"tab":       "Tab",
	"backspace": "BSpace",
	"up":        "Up",
	"down":      "Down",
	"left":      "Left",
	"right":     "Right",
}

func init() {
	for c := byte('a'); c <= 'z'; c++ {
		namedKeys["ctrl-"+string(c)] = "C-" + string(c)
	}
}

// tmuxKeyName is sendKey's pure whitelist lookup, split out so it's testable
// without shelling out to tmux.
func tmuxKeyName(name string) (string, bool) {
	key, ok := namedKeys[name]
	return key, ok
}

// sendKey sends a single named key (Escape, Enter, arrows, Ctrl-<letter>...)
// to p's pane via tmux send-keys, with no trailing Enter — the counterpart to
// sendMessage/sendToShell for a keyboard that can't type these keys (e.g.
// iOS), used to interrupt a running turn or navigate a prompt. Deliberately
// has no question-prompt or status guard: pressing Esc/arrows at a question
// prompt or mid-turn is exactly the use case.
func sendKey(p *Peer, name string) error {
	key, ok := tmuxKeyName(name)
	if !ok {
		return fmt.Errorf("unsupported key %q", name)
	}
	if p.Role == "shell" {
		// verifyKillTarget's shell branch is the same verify-before-acting
		// check sendToShell uses: pane still exists, same root process, no
		// agent claimed it
		if err := verifyKillTarget(p); err != nil {
			return err
		}
	} else {
		if !p.Messageable || p.Pane == "" {
			return fmt.Errorf("%s is an in-process teammate — message its lead instead", displayName(p))
		}
		if p.Depth > 0 {
			return fmt.Errorf("%s is not the foreground agent in pane %s — keys would land in the nested one", displayName(p), p.Pane)
		}
		if err := checkSendTarget(p); err != nil {
			return err
		}
	}
	if err := exec.Command("tmux", "send-keys", "-t", p.Pane, key).Run(); err != nil {
		return fmt.Errorf("tmux send-keys: %v", err)
	}
	return nil
}

// capturePane returns a pane's visible content (tmux capture-pane), the live
// view the app shows for shells and for agents where the transcript doesn't
// cover raw terminal I/O. Read-only; safe on any pane.
func capturePane(pane string) (string, error) {
	if pane == "" {
		return "", fmt.Errorf("no pane given")
	}
	if _, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{pane_id}").Output(); err != nil {
		return "", fmt.Errorf("no pane %s — rerun 'k ls'", pane)
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	if err != nil {
		return "", fmt.Errorf("tmux capture-pane: %v", err)
	}
	return string(out), nil
}

// sendMessage runs the send-time safety checks and types msg into p's prompt,
// submitting it with Enter. Shared by k s, the bus, and the daemon — every
// path that talks to an agent goes through the same guards.
func sendMessage(p *Peer, msg string) error {
	if err := checkSendTarget(p); err != nil {
		return err
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
