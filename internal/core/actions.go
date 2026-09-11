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

	"kraken/internal/util"
)

// DetachedSessionCols and DetachedSessionRows size every kraken-created
// detached tmux session generously: a session born via `new-session -d` with
// no -x/-y otherwise starts at tmux's default-size 80x24 and stays there
// until a client first attaches (window-size latest), which starves pane
// splits in a never-attached cell. This covers the never-attached case, not
// every path: with window-size latest the resize is sticky, so a session
// attached once from a small terminal stays small after detach and can still
// starve later; explicit -x/-y also overrides a larger user default-size
// downward for kraken-created sessions.
const (
	DetachedSessionCols = 250
	DetachedSessionRows = 60
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
// endpoint needs (no $TMUX, no util.Die()). The window keeps tmux's default
// name (the running command); callers that want to label it use
// NewWindowNamed.
func NewWindowIn(session, dir string, argv []string) (string, error) {
	return NewWindowNamed(session, dir, "", nil, argv)
}

// NewWindowNamed is NewWindowIn with an explicit tmux window name (-n) and an
// explicit environment — the two things a side thread's window needs that a
// plain spawn doesn't. Its window sits beside zeus' in the cell session, so it
// has to announce which thread it is in the status bar rather than reading as a
// second anonymous claude. An empty name omits -n entirely, which is exactly
// NewWindowIn's original behavior — the two share this one exec so a change to
// the tmux invocation (the -P format string the pane id is parsed back out of,
// above all) can never apply to only one of them.
//
// env is the environment of the tmux CLIENT this function becomes, and a nil env
// inherits this process' own — exactly exec.Cmd's rule, and what NewWindowIn
// passes, so the board's spawn is unaffected. `k n`/`k c` are unaffected for a
// different reason worth stating, since the doc above primes a reader to expect
// all three to funnel through here: NewWindow never calls this function at all —
// it builds its own tmuxArgs and runs its own exec, and setting no Env there
// already means inherit. It exists because a pane's environment is the environment of the
// process that issued its tmux command, so a caller that knows the pane needs
// something this process wasn't given has no other way to say so. The one such
// caller is a side thread opened by the launchd-run daemon, whose PATH does not
// lead to the Go toolchain (internal/jobs/cell.go's cellSpawnEnv). core does not
// resolve any toolchain itself: what belongs on PATH is knowledge of the layer
// above, and it stays there.
func NewWindowNamed(session, dir, name string, env, argv []string) (string, error) {
	// tmux silently falls back to $HOME on a bad -c dir; a typo should fail
	if err := CheckSpawnDir(dir); err != nil {
		return "", err
	}
	tmuxArgs := []string{"new-window", "-P", "-F", "#{session_name}:#{window_index} (#{pane_id})", "-t", session + ":", "-c", dir}
	if name != "" {
		tmuxArgs = append(tmuxArgs, "-n", name)
	}
	tmuxArgs = append(tmuxArgs, argv...)
	cmd := exec.Command("tmux", tmuxArgs...)
	cmd.Env = env // nil inherits
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			return "", fmt.Errorf("tmux new-window: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("tmux new-window: %v", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// SessionAlive reports whether a tmux session with exactly this name exists
// right now — one direct has-session probe, independent of any snapshot a
// caller may be holding. The "=" prefix forces tmux's exact-match resolution:
// bare -t targets also PREFIX-match, and cell session names can be prefixes
// of each other (cell-demo-2 vs cell-demo-2-5), so a bare probe could report
// a dead session alive on the strength of a live sibling.
func SessionAlive(name string) bool {
	return exec.Command("tmux", "has-session", "-t", "="+name).Run() == nil
}

// EnsureSession makes sure a detached tmux session named name exists,
// creating it rooted at dir if not.
func EnsureSession(name, dir string) error {
	if exec.Command("tmux", "has-session", "-t", name).Run() == nil {
		return nil
	}
	var stderr bytes.Buffer
	cmd := exec.Command("tmux", "new-session", "-d",
		"-x", strconv.Itoa(DetachedSessionCols), "-y", strconv.Itoa(DetachedSessionRows),
		"-s", name, "-c", dir)
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
// have moved sessions, or vanished, since discovery ran. Note an
// unresolvable target does not always take the error branch: a dead pane id
// can resolve to nothing with exit 0 (measured 2026-08-06), returning
// ("", nil) — treat an empty name as unresolved, not just a non-nil error.
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
		argv = append(argv, ModelArgs("")...)
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

// RaiseOutcome reports how RaiseSession made a session visible: Attached when
// a Ghostty window was opened for it — either because no client was attached,
// or because an attached client's window couldn't be raised and was replaced
// (see RaiseSession) — and neither field set when an existing window was
// raised in place (or the caller was already inside the session). FocusErr
// means the user must switch windows manually, and is set in exactly two
// cases: the raise mechanism failed and the replacement attach failed too, or
// the raise ran fine but found no matching window (errNoGhosttyWindow, which
// never falls back).
type RaiseOutcome struct {
	Attached bool
	FocusErr error
}

// RaiseSession brings a tmux session's Ghostty window to the front, attaching
// it in a new Ghostty window if no client is attached. When a client is
// attached but the raise MECHANISM fails (most often the assistive-access
// denial described on focusGhosttyWindow), it falls back to reattaching in a
// fresh window rather than surfacing the error: every attach kraken opens
// carries `-d` (see ghosttyAttachArgs), which in the fallback detaches the
// existing clients, and a kraken-opened client's whole window is its
// `tmux attach`, so that window closes with it and the swap replaces the
// window instead of accumulating one (measured 2026-08-06: the client tty
// swaps and the Ghostty
// instance count is unchanged across it; a hand-run attach inside a shell
// window survives as its shell, so only kraken-opened windows close). A raise
// that ran fine but found no window titled for the session never falls back:
// the client there is healthy — a background tab, a non-Ghostty terminal,
// set-titles off — and kicking it would trade a diagnosable message for
// destroying a window the user may be looking at.
func RaiseSession(session string) (RaiseOutcome, error) {
	// an empty target is a caller bug — refuse it by name rather than
	// letting it surface as a misleading "tmux session  is gone" below. The
	// "="-prefixed probes below would fail closed anyway; the refusal also
	// keeps the original hazard un-reintroducible: a bare -t "" resolves to
	// the CALLER's own session (measured 2026-08-06), so a fallback reached
	// with one would kick the very client the user is typing in.
	if session == "" {
		return RaiseOutcome{}, fmt.Errorf("empty tmux session name")
	}
	// the caller may hold a snapshot up to a second old — without this check
	// a dead session would pop an empty Ghostty window that fails to attach.
	// SessionAlive's "=" exact-match matters here: a bare -t target also
	// PREFIX-matches, so a dead cell-demo-2 would resolve to its live
	// sibling cell-demo-2-5 and the fallback below would kick that job's
	// window out from under the user.
	if !SessionAlive(session) {
		return RaiseOutcome{}, fmt.Errorf("tmux session %s is gone", session)
	}
	out, err := exec.Command("tmux", "list-clients", "-t", "="+session, "-F", "#{client_tty}").Output()
	attached := err == nil && strings.TrimSpace(string(out)) != ""
	if !attached {
		if err := openGhosttyAttach(session); err != nil {
			return RaiseOutcome{}, fmt.Errorf("open Ghostty: %v", err)
		}
		return RaiseOutcome{Attached: true}, nil
	}
	ferr := focusGhosttyWindow(session)
	if ferr == nil {
		return RaiseOutcome{}, nil
	}
	if errors.Is(ferr, errNoGhosttyWindow) {
		return RaiseOutcome{FocusErr: ferr}, nil
	}
	if currentSession() == session {
		// the caller is typing inside the very session it asked to raise, so
		// its window is already on screen and focused — swapping it would
		// close that window mid-keystroke for nothing
		return RaiseOutcome{}, nil
	}
	if err := openGhosttyAttach(session); err != nil {
		return RaiseOutcome{FocusErr: fmt.Errorf("%v; reattach fallback failed: %v", ferr, err)}, nil
	}
	// the raise failure is swallowed by the successful fallback — keep it
	// diagnosable in the event log, since it's the signal that granting
	// Accessibility would restore in-place raising
	util.LogEvent("core", "info", "open", fmt.Sprintf("raise %s fell back to reattach: %v", session, ferr))
	return RaiseOutcome{Attached: true}, nil
}

// currentSession names the tmux session $TMUX_PANE points into, or "" when
// $TMUX is unset (e.g. kraken serve under launchd) or the pane can't be
// resolved. NOT a proof the caller is a human at that window: every
// descendant of a pane inherits $TMUX/$TMUX_PANE — including a server
// hand-started from one (see jobs.ownTmuxSession, the same shape) — so the
// self-target guard over-matches there and declines a swap: in that
// direction the cost is a missing raise. It can also under-match
// ($TMUX_PANE unset, or the pane gone), where the guard simply does not
// fire and the fallback swaps as it would for any other session.
func currentSession() string {
	if os.Getenv("TMUX") == "" {
		return ""
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return ""
	}
	name, err := CurrentPaneSession(pane)
	if err != nil {
		return ""
	}
	return name
}

// ghosttyAttachArgs builds the `open` argv that attaches a session in a fresh
// Ghostty window — split out pure so a test can pin the shape. -d is
// unconditional: a no-op when no client is attached, and what makes a burst
// of opens converge on one window (each attach kicks the previous client)
// instead of leaving strays. "=" forces exact-match resolution for the same
// prefix-collision reason SessionAlive documents.
func ghosttyAttachArgs(session string) []string {
	return []string{"-na", "Ghostty.app", "--args", "-e", "tmux", "attach-session", "-d", "-t", "=" + session}
}

// openGhosttyAttach opens a fresh Ghostty window running `tmux attach-session`
// on the session, detaching any existing clients first. Both output streams
// are captured into the returned error — a bare exit status would leave the
// one message a user sees when the fallback itself fails naming no cause
// (measured 2026-08-06: a missing app reports its reason on stderr and
// nothing else).
func openGhosttyAttach(session string) error {
	out, err := exec.Command("open", ghosttyAttachArgs(session)...).CombinedOutput()
	if err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%v: %s", err, msg)
		}
		return err
	}
	return nil
}

// OpenPeer jumps to a claude: selects its tmux window/pane and raises its
// Ghostty window. Returns a human-readable note on success, plus the raise
// outcome so a caller can tell a clean focus apart from one that raised the
// session but failed to bring its window to the front (see RaiseOutcome).
func OpenPeer(p *Peer) (string, RaiseOutcome, error) {
	if p.Pane == "" {
		return "", RaiseOutcome{}, fmt.Errorf("%s has no pane (in-process teammate)", DisplayName(p))
	}
	// point the session at the target pane first: attached clients follow
	// immediately, and a fresh attach lands directly on it
	_ = exec.Command("tmux", "select-window", "-t", p.Pane).Run()
	_ = exec.Command("tmux", "select-pane", "-t", p.Pane).Run()
	out, err := RaiseSession(p.TmuxSession)
	if err != nil {
		return "", RaiseOutcome{}, err
	}
	switch {
	case out.Attached:
		return fmt.Sprintf("attached %s in a new ghostty window → %s (%s)", p.TmuxSession, DisplayName(p), p.Pane), out, nil
	case out.FocusErr != nil:
		return fmt.Sprintf("selected %s (%s) — focus its window manually: %v", DisplayName(p), p.Pane, out.FocusErr), out, nil
	default:
		return fmt.Sprintf("focused %s (%s, pane %s)", DisplayName(p), p.TmuxSession, p.Pane), out, nil
	}
}

// errNoGhosttyWindow marks the raise outcome where osascript exited zero and
// the script did not answer "ok" (in practice "notfound" — no window titled
// "<session>:"), which is the one focus failure RaiseSession must NOT
// reattach on, since the attached client behind it is healthy (a background
// tab, a non-Ghostty terminal, set-titles off).
var errNoGhosttyWindow = errors.New("no ghostty window")

// noGhosttyWindowErr wraps errNoGhosttyWindow with the session and the
// script's output — split out so a test can pin the errors.Is linkage, which
// is the single line the never-kick-a-healthy-client rule hangs on.
func noGhosttyWindowErr(session, output string) error {
	return fmt.Errorf("%w titled %q (is 'set-titles on' active?): %s", errNoGhosttyWindow, session+":", output)
}

// focusGhosttyWindow raises the Ghostty window whose title starts with
// "<session>:" (requires tmux set-titles on, so the title-string leads with
// #S) by running the AppleScript inside the tmux server's own process
// context via `tmux run-shell`, rather than execing osascript directly:
// kraken serve runs as a launchd agent with no Accessibility grant, while the
// tmux server's context has it, since a human granted it when first running
// tmux interactively. The session being raised is guaranteed to be alive in
// that server already, so tmux is guaranteed reachable here. `k o` run from a
// terminal goes through this exact same path.
//
// Two real limitations remain: (a) the tmux server only carries Accessibility
// when it was born from a context that already had the grant — if kraken
// serve's daemon is the one to *start* the tmux server (e.g. after a reboot,
// a launchd-started serve issues the first `new-session` from an iris or cell
// spawn before any terminal has opened one), run-shell inherits that same TCC
// denial and every raise fails until the tmux server is restarted from a
// granted context or osascript itself is granted Accessibility. Observed on
// this machine (2026-08-06): the tmux server is launchd-born and every raise
// fails with -25211, so RaiseSession treats a raise-mechanism failure as
// expected and falls back to reattaching in a fresh window rather than
// surfacing it. (b) the temp AppleScript file is written by serve's uid and
// read back by the tmux server process — this assumes both run as the same
// user sharing the same $TMPDIR, true for the documented LaunchAgent layout
// but not guaranteed in general.
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
	f, err := os.CreateTemp("", "kraken-focus-*.applescript")
	if err != nil {
		return fmt.Errorf("write applescript temp file: %v", err)
	}
	defer os.Remove(f.Name())
	_, writeErr := f.WriteString(script)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return fmt.Errorf("write applescript temp file: %v", err)
	}
	// osascript's own stderr is the diagnosable part of a failure (e.g. an
	// accessibility denial) — redirect it into the run-shell command's stdout
	// ourselves, since tmux run-shell only ever forwards stdout to us and
	// otherwise collapses a failure into a bare "returned <code>" line.
	out, err := exec.Command("tmux", "run-shell", "osascript '"+f.Name()+"' 2>&1").CombinedOutput()
	trimmed := strings.TrimSpace(string(out))
	if err != nil {
		return fmt.Errorf("osascript via tmux run-shell failed (%v): %s%s", err, trimmed, focusErrHint(trimmed))
	}
	// the script's own answer ("ok"/"notfound") is always the last line —
	// stderr merged in above can add earlier lines (e.g. a stray objc[…]
	// warning) on an otherwise-successful raise, and those must not be
	// mistaken for the sentinel.
	lines := strings.Split(trimmed, "\n")
	sentinel := lines[len(lines)-1]
	if sentinel != "ok" {
		return noGhosttyWindowErr(session, trimmed)
	}
	return nil
}

// focusErrHint appends the remedy for the two TCC denials osascript can hit
// when the tmux server descends from launchd, where nobody is present to
// answer a consent prompt: the assistive-access denial (the message that
// carries error -25211 — the phrase is what's matched, the number never
// appears without it) and the Automation denial (-1743), which lives in a
// different System Settings pane. Any other failure passes through
// undecorated.
func focusErrHint(output string) string {
	switch {
	case strings.Contains(output, "not allowed assistive access"):
		return " — grant Accessibility to osascript (System Settings → Privacy & Security → Accessibility) or restart the tmux server from a granted terminal"
	case strings.Contains(output, "Not authorized to send Apple events"):
		return " — allow osascript to control System Events (System Settings → Privacy & Security → Automation) or restart the tmux server from a granted terminal"
	}
	return ""
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
	if p.Status == "question" || PaneShowsQuestionFor(p.Agent, p.Pane) {
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
