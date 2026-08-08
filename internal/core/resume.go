package core

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"kraken/internal/util"
)

// --- kill + resume: moving a running claude onto the current credential ---
//
// A claude process resolves its request credential once, at session start,
// and re-resolves it only on an auth error or on a refresh scheduled off the
// OLD token's expiry — so overwriting the keychain item under a live session
// does not move it (measured 2026-08-07, Claude Code 2.1.224: turns at +10s
// through +3m after a swap all still billed the swapped-OUT account). Once a
// turn has 429'd on a usage limit there is a second layer on top of that:
// Claude Code latches the limit client-side and refuses further turns
// locally, without an API call, until the reset time the banner names. On a
// LATCHED process the swap itself and a real `/usage` fetch showing the new
// account healthy were both measured to leave the latch in place; `/model`
// and `/clear` were probed on the healthy side of the experiment, against
// the credential cache rather than the latch, and moved nothing there
// either.
//
// Killing the process and starting a fresh one with `-r <session-id>` clears
// both: the new process reads the keychain at startup, so it adopts whatever
// account is current on its very first turn, and it carries no latch. The
// conversation survives because that is what resume means.
//
// This file is the primitive — guard, pre-kill re-verification, respawn,
// session-file cleanup. It deliberately builds NO command: what a resumed
// agent must be launched with is the caller's knowledge (a cell's zeus needs
// its brief, its model and its permission flag re-passed, since a resumed
// session restores none of them), and an exported command-builder here would
// only invite a resume that drops half of them.

// ResumeGuard rejects peers whose session can't be resumed in place. The
// conditions are exactly the four a respawn needs to be safe: a claude (not
// a codex, not a shell row), an addressable pane to respawn, a session id to
// pass to -r, and an id that was actually READ rather than inferred —
// resuming a guessed id would drag another claude's conversation into this
// pane, which is the same reason BranchGuard refuses one.
//
// The session-id conditions are also what makes a teammate unresumable, and
// that is a fact about tmux teammates rather than a policy: a teammate has
// no session file and no locatable transcript, so discovery leaves its
// SessionID empty by construction (discover.go's teammate arm). A caller
// that "resumes a cell" therefore reaches zeus and nobody else.
func ResumeGuard(p *Peer) error {
	if p.Role == "shell" {
		return fmt.Errorf("%s is a shell pane — no session to resume", DisplayName(p))
	}
	if p.Agent != "claude" {
		return fmt.Errorf("%s is not a claude (agent %q) — only claude sessions can be resumed", DisplayName(p), p.Agent)
	}
	if p.Pane == "" {
		return fmt.Errorf("%s has no pane to respawn (in-process teammate)", DisplayName(p))
	}
	if p.SessionID == "" {
		return fmt.Errorf("no session id resolved for %s", DisplayName(p))
	}
	if p.SessionIDGuessed {
		return fmt.Errorf("session id for %s is a guess (no session file) — could resume another claude's session", DisplayName(p))
	}
	return nil
}

// paneHoldsClaude re-resolves a pane's live process tree and reports whether
// the foreground process in it is a claude WHOSE PID IS pid —
// checkSendTarget's verify-immediately-before-acting shape, minus the
// question-prompt refusal, which teardown+resume obsoleted (a killed process
// cannot answer a prompt by accident).
//
// It is a separate seam, and a mandatory step rather than a nicety, because
// `respawn-pane -k` is a SIGKILL with no confirmation: the peer snapshot a
// caller holds can be a whole daemon tick old, and in that window the pane's
// claude may have exited and left carl's own shell — or a command he
// started — sitting in it.
//
// Matching the PID rather than merely finding "a claude" is the same
// distinction checkSendTarget draws when it says a bare pid-exists check
// would pass for zombies and recycled pids. A bare is-a-claude check has the
// mirror-image hole: the pane's original claude can exit and a DIFFERENT one
// be started in it — by carl, by a fork, by anything — between the snapshot
// and this call, and killing that one destroys a conversation the rescue
// never looked at and cannot resume (its session id is not the one the
// caller holds). Every error path answers false: refusing a resume costs one
// retried tick, and the alternative costs a process.
var paneHoldsClaude = func(pane string, pid int) bool {
	if pane == "" || pid <= 0 {
		return false
	}
	out, err := exec.Command("tmux", "display-message", "-p", "-t", pane, "#{pane_pid}").Output()
	if err != nil {
		return false
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || panePID <= 0 {
		return false
	}
	children, cmds, _ := psSnapshot()
	hits := agentPIDsUnder(panePID, children, cmds)
	// deepest-first: the first hit is the process attached to the foreground
	// TUI, which is the one respawn-pane would be killing
	return len(hits) > 0 && hits[0].agent == "claude" && hits[0].pid == pid
}

// respawnPane replaces a pane's process with cmd: the seam the resume path
// runs through, mirroring securityFind's pattern so a caller's decision to
// resume is observable in-package without driving real tmux.
//
// `respawn-pane -k` kills whatever the pane currently holds before starting
// cmd — which is the operation, not a detail of it: a claude that is merely
// asked to stop keeps its latched limit and its stale credential.
var respawnPane = func(target, cwd, cmd string) error {
	if err := exec.Command("tmux", "respawn-pane", "-k", "-c", cwd, "-t", target, cmd).Run(); err != nil {
		return fmt.Errorf("tmux respawn-pane: %v", err)
	}
	return nil
}

// RespawnPeerPane kills the claude in p's pane and runs cmd there instead.
// cmd is the CALLER's full launch command — this function never builds one,
// for the reason the file header gives.
//
// Four steps in this order: the guard, the pre-kill re-verification (the PID
// refusal below and the pane probe together), the respawn, and the
// session-file cleanup. Errors from the first three are returned as they
// came, so a caller can count one refusal out and carry on.
//
// A peer with no PID is refused before the seam is consulted. Every kill
// here is justified by "this pane still holds the exact process we resolved
// a session id from", and a zero PID makes that sentence unverifiable — an
// in-process teammate row, a peer rebuilt from a partial snapshot. Refusing
// costs a retried tick; guessing costs whatever was in the pane.
//
// The cleanup is the step with no visible consequence and the longest tail.
// Claude Code writes ~/.claude/sessions/<pid>.json at startup and removes it
// on exit; a SIGKILLed process removes nothing, and the file it leaves
// behind is what discovery reads a pane's identity out of (discover.go).
// Once the OS reuses that pid, the stale file makes an unrelated process
// look like a live claude with the dead one's session id — which is exactly
// the input `k b`, `k r` and the cell's own zeus election trust. Best
// effort by construction: a failed removal is a rare cosmetic problem, while
// failing the resume over it would strand a cell whose pane has already been
// replaced.
func RespawnPeerPane(p *Peer, cmd string) error {
	if err := ResumeGuard(p); err != nil {
		return err
	}
	if p.PID <= 0 {
		return fmt.Errorf("%s has no pid — cannot verify what is in pane %s before killing it", DisplayName(p), p.Pane)
	}
	if !paneHoldsClaude(p.Pane, p.PID) {
		return fmt.Errorf("pane %s no longer holds claude pid %d — refusing to kill what is in it now", p.Pane, p.PID)
	}
	if err := respawnPane(p.Pane, PeerCWD(p), cmd); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(util.Home(), ".claude", "sessions", strconv.Itoa(p.PID)+".json"))
	return nil
}

// MainIdentityEmail is who the machine's main login belongs to right now,
// read straight from ~/.claude.json rather than from the usage snapshot.
// The snapshot is a poll and can be minutes behind a swap the rescue itself
// just performed; this cannot. Empty when the file is missing, unparseable
// or carries no oauthAccount email — never an error, since every caller's
// fallback is the snapshot's own idea of main.
func MainIdentityEmail() string {
	return mainAccountMeta().Email
}
