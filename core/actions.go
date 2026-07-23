package core

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"kraken/util"
)

// NewWindow opens a new window in the caller's tmux session running argv (an
// interactive shell when empty) and reports where it landed.
func NewWindow(dir string, argv []string) string {
	if os.Getenv("TMUX") == "" {
		util.Die("must run inside tmux")
	}
	// tmux silently falls back to $HOME on a bad -c dir; a typo should fail
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		util.Die("not a directory: %s", dir)
	}
	tmuxArgs := []string{"new-window", "-P", "-F", "#{session_name}:#{window_index} (#{pane_id})", "-c", dir}
	tmuxArgs = append(tmuxArgs, argv...)
	out, err := exec.Command("tmux", tmuxArgs...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			util.Die("tmux new-window: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		util.Die("tmux new-window: %v", err)
	}
	return strings.TrimSpace(string(out))
}

// ErrBadSpawnDir marks a not-a-directory dir so HTTP callers can tell a
// malformed client parameter (400) apart from an environment/tmux failure
// (409).
var ErrBadSpawnDir = errors.New("not a directory")

// CheckSpawnDir verifies dir exists as a directory, wrapping ErrBadSpawnDir
// on failure. Exported as its own step (rather than folded only into
// NewWindowIn) so the HTTP spawn handler can fail a malformed dir before
// EnsureSession does any tmux work for a request that's doomed anyway.
func CheckSpawnDir(dir string) error {
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return fmt.Errorf("%w: %s", ErrBadSpawnDir, dir)
	}
	return nil
}

// NewWindowIn is NewWindow's error-returning core, targeted at an explicit
// tmux session rather than the caller's own — the piece the HTTP spawn
// endpoint needs (no $TMUX, no util.Die()).
func NewWindowIn(session, dir string, argv []string) (string, error) {
	// tmux silently falls back to $HOME on a bad -c dir; a typo should fail
	if err := CheckSpawnDir(dir); err != nil {
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

// EnsureSession makes sure a detached tmux session named name exists,
// creating it rooted at dir if not.
func EnsureSession(name, dir string) error {
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

// CurrentPaneSession returns the tmux session a pane currently belongs to,
// read fresh rather than trusted from a stale Peer snapshot — the pane may
// have moved sessions, or vanished, since discovery ran.
func CurrentPaneSession(pane string) (string, error) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{session_name}").Output()
	if err != nil {
		return "", fmt.Errorf("pane %s is gone", pane)
	}
	return strings.TrimSpace(string(out)), nil
}

// BuildSpawnArgv builds the argv for a spawned pane from the HTTP API's kind:
// "shell" mirrors cmdNewShell's command-then-drop-into-shell pattern, "claude"
// mirrors cmdNewClaude's launcher + "--" prompt guard.
func BuildSpawnArgv(kind, name, prompt, cmd string) ([]string, error) {
	switch kind {
	case "shell":
		if cmd == "" {
			return nil, nil
		}
		sh := UserShell()
		return []string{sh, "-c", cmd + "\nexec " + sh}, nil
	case "claude":
		if name == "" {
			return nil, fmt.Errorf("claude spawn needs a name")
		}
		argv := []string{ClaudeLauncher(), "--dangerously-skip-permissions", "--name", name}
		if prompt != "" {
			// "--" so a prompt starting with "-" can't be read as a claude flag
			argv = append(argv, "--", prompt)
		}
		return argv, nil
	default:
		return nil, fmt.Errorf("kind must be \"shell\" or \"claude\"")
	}
}

func UserShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/zsh"
}

// RaiseOutcome reports how RaiseSession made a session visible: a fresh
// Ghostty window was attached, or an existing one was focused (FocusErr set
// when the raise failed and the user must switch windows manually).
type RaiseOutcome struct {
	Attached bool
	FocusErr error
}

// RaiseSession brings a tmux session's Ghostty window to the front, attaching
// it in a new Ghostty window if no client is attached.
func RaiseSession(session string) (RaiseOutcome, error) {
	// the caller may hold a snapshot up to a second old — without this check
	// a dead session would pop an empty Ghostty window that fails to attach
	if err := exec.Command("tmux", "has-session", "-t", session).Run(); err != nil {
		return RaiseOutcome{}, fmt.Errorf("tmux session %s is gone", session)
	}
	out, err := exec.Command("tmux", "list-clients", "-t", session, "-F", "#{client_tty}").Output()
	attached := err == nil && strings.TrimSpace(string(out)) != ""
	if !attached {
		if err := exec.Command("open", "-na", "Ghostty.app", "--args",
			"-e", "tmux", "attach-session", "-t", session).Run(); err != nil {
			return RaiseOutcome{}, fmt.Errorf("open Ghostty: %v", err)
		}
		return RaiseOutcome{Attached: true}, nil
	}
	return RaiseOutcome{FocusErr: focusGhosttyWindow(session)}, nil
}

// OpenPeer jumps to a claude: selects its tmux window/pane and raises its
// Ghostty window. Returns a human-readable note on success.
func OpenPeer(p *Peer) (string, error) {
	if p.Pane == "" {
		return "", fmt.Errorf("%s has no pane (in-process teammate)", DisplayName(p))
	}
	// point the session at the target pane first: attached clients follow
	// immediately, and a fresh attach lands directly on it
	_ = exec.Command("tmux", "select-window", "-t", p.Pane).Run()
	_ = exec.Command("tmux", "select-pane", "-t", p.Pane).Run()
	out, err := RaiseSession(p.TmuxSession)
	if err != nil {
		return "", err
	}
	switch {
	case out.Attached:
		return fmt.Sprintf("attached %s in a new ghostty window → %s (%s)", p.TmuxSession, DisplayName(p), p.Pane), nil
	case out.FocusErr != nil:
		return fmt.Sprintf("selected %s (%s) — focus its window manually: %v", DisplayName(p), p.Pane, out.FocusErr), nil
	default:
		return fmt.Sprintf("focused %s (%s, pane %s)", DisplayName(p), p.TmuxSession, p.Pane), nil
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

// SendToPeer runs cmdSend's full guard chain plus the tmux send mechanics,
// returning whether the message queued behind a mid-turn agent. Shared by
// cmdSend and the HTTP send endpoint so both refuse on identical guards.
func SendToPeer(p *Peer, msg string) (queued bool, err error) {
	if p.Role == "shell" {
		return false, fmt.Errorf("%s is a shell pane — the text would run as a shell command", DisplayName(p))
	}
	if strings.TrimSpace(msg) == "" {
		return false, fmt.Errorf("empty message")
	}
	if !p.Messageable || p.Pane == "" {
		return false, fmt.Errorf("%s is an in-process teammate — message its lead instead", DisplayName(p))
	}
	if p.Depth > 0 {
		return false, fmt.Errorf("%s is not the foreground agent in pane %s — keys would land in the nested one", DisplayName(p), p.Pane)
	}
	queued = p.Status == "running"
	if err := SendMessage(p, msg); err != nil {
		return false, err
	}
	return queued, nil
}

// SendToShell types a command line into a shell pane and runs it with Enter —
// the deliberate counterpart to SendToPeer's shell refusal, used only by the
// HTTP endpoint where the caller has explicitly targeted a shell. Re-verifies
// the pane is still agent-free right before typing so a stale snapshot can't
// send a command into a pane an agent has since claimed.
func SendToShell(p *Peer, cmd string) error {
	if p.Role != "shell" {
		return fmt.Errorf("%s is not a shell pane", DisplayName(p))
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

// tmuxKeyName is SendKey's pure whitelist lookup, split out so it's testable
// without shelling out to tmux.
func tmuxKeyName(name string) (string, bool) {
	key, ok := namedKeys[name]
	return key, ok
}

// SendKey sends a single named key (Escape, Enter, arrows, Ctrl-<letter>...)
// to p's pane via tmux send-keys, with no trailing Enter — the counterpart to
// SendMessage/SendToShell for a keyboard that can't type these keys (e.g.
// iOS), used to interrupt a running turn or navigate a prompt. Deliberately
// has no question-prompt or status guard: pressing Esc/arrows at a question
// prompt or mid-turn is exactly the use case.
func SendKey(p *Peer, name string) error {
	key, ok := tmuxKeyName(name)
	if !ok {
		return fmt.Errorf("unsupported key %q", name)
	}
	if p.Role == "shell" {
		// verifyKillTarget's shell branch is the same verify-before-acting
		// check SendToShell uses: pane still exists, same root process, no
		// agent claimed it
		if err := verifyKillTarget(p); err != nil {
			return err
		}
	} else {
		if !p.Messageable || p.Pane == "" {
			return fmt.Errorf("%s is an in-process teammate — message its lead instead", DisplayName(p))
		}
		if p.Depth > 0 {
			return fmt.Errorf("%s is not the foreground agent in pane %s — keys would land in the nested one", DisplayName(p), p.Pane)
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

// CapturePane returns a pane's visible content (tmux capture-pane), the live
// view the app shows for shells and for agents where the transcript doesn't
// cover raw terminal I/O. Read-only; safe on any pane.
func CapturePane(pane string) (string, error) {
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

// bareSlashCommandRe matches a message that's nothing but a single slash
// command with no arguments (e.g. "/do"), as opposed to "/do implement the
// thing" or plain conversational text.
var bareSlashCommandRe = regexp.MustCompile(`^/\S+$`)

// guardBareSlashCommand appends a trailing space to a bare slash command
// before it's typed into a pane. Claude Code's slash palette autocompletes a
// bare command by longest-prefix match, so an unqualified "/do" can silently
// complete to an unrelated personal command like "/doc" — the space defeats
// that prefix match. Anything else (has arguments, has no leading slash, or
// spans multiple lines) passes through unchanged.
func guardBareSlashCommand(msg string) string {
	if bareSlashCommandRe.MatchString(msg) {
		return msg + " "
	}
	return msg
}

// SendMessage runs the send-time safety checks and types msg into p's prompt,
// submitting it with Enter. Shared by k s, the bus, and the daemon — every
// path that talks to an agent goes through the same guards.
func SendMessage(p *Peer, msg string) error {
	if err := checkSendTarget(p); err != nil {
		return err
	}
	// re-check right before typing: a permission/question prompt has an option
	// pre-highlighted, so our trailing Enter would answer it for the user
	if p.Status == "question" || PaneShowsQuestion(p.Pane) {
		return fmt.Errorf("%s has a question/permission prompt open — sending would answer it; resolve it first", DisplayName(p))
	}
	msg = guardBareSlashCommand(msg)
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
	return fmt.Errorf("%s (pane %s) is gone — rerun 'k ls'", DisplayName(p), p.Pane)
}

// KillPeer closes a pane, agent or shell. The caller's own pane and panes
// holding more than one agent are refused; everything else is re-verified
// immediately before the kill.
func KillPeer(p *Peer) (string, error) {
	if p.Pane == "" {
		return "", fmt.Errorf("%s is an in-process teammate — no pane to kill", DisplayName(p))
	}
	if p.Depth > 0 {
		// Depth > 0 means another agent runs nested inside this one; the
		// multi-agent refusal in verifyKillTarget catches the reverse case
		// (killing the nested one), but this fails before any tmux exec
		return "", fmt.Errorf("%s shares its pane with a nested agent — killing the pane would take both down", DisplayName(p))
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
			desc = "was running " + TruncateOneLine(p.Cmd, 40)
		}
	}
	return fmt.Sprintf("killed %s (%s, pane %s)", DisplayName(p), desc, p.Pane), nil
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
	return fmt.Errorf("%s is no longer in pane %s — rerun 'k ls'", DisplayName(p), p.Pane)
}
