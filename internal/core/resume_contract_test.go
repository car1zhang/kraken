package core

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// resume_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md v2, the `core` half: ResumeGuard, the paneHoldsClaude and
// respawnPane seams, RespawnPeerPane's four-step order, and
// MainIdentityEmail. Written from the contract text alone;
// internal/core/resume.go was never read while either round was open.
//
// Dimension: LOGIC — the stated invariants and the happy paths. What
// RespawnPeerPane composes out of and IN WHAT ORDER, what reaches the seam
// unchanged, the post-respawn session-file unlink, and the identity
// MainIdentityEmail reports. Boundaries and the guard's four individual
// refusals belong to a sibling author.
//
// v2 delta: every ResumeCmdString test is GONE with the symbol (v2 removes
// it — the one caller builds zeus' full command jobs-side). RespawnPeerPane's
// second parameter is now the caller's finished shell command rather than a
// prompt, so the assertions that pinned it to ResumeCmdString(p, prompt) are
// replaced by pass-through assertions. Two obligations are new: the
// paneHoldsClaude re-verification that must sit between the guard and the
// kill, and the stale session-file removal that follows a successful one.
//
// Every package-level identifier here is prefixed `contractResumeLogic` — the
// core package shares one declaration space across every _test.go in it, and
// bare `contract…` is already taken by transcript_contract_test.go.
//
// Environment: every test that touches $HOME isolates it itself
// (t.Setenv("HOME", t.TempDir()), the package's existing convention) and
// writes its own fixture. Nothing here reads the real machine.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractResumeLogicIsolate sandboxes HOME for one test, restored by
// t.Setenv's own cleanup.
func contractResumeLogicIsolate(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// contractResumeLogicPeer is the resumable claude the contract describes:
// agent "claude", a pane, a real (not guessed) session id. It carries a PID
// too, since v2's unlink step keys on one.
func contractResumeLogicPeer(sessionID string) *Peer {
	return &Peer{
		Agent:       "claude",
		Role:        "main",
		Pane:        "%7",
		TmuxSession: "cell-resume-1",
		PID:         4242,
		SessionID:   sessionID,
		Name:        "zeus-resume",
		CWD:         "/tmp/kraken-resume-cwd",
	}
}

// contractResumeLogicCall records one invocation of the respawnPane seam.
type contractResumeLogicCall struct{ target, cwd, cmd string }

// contractResumeLogicCheck records one invocation of the paneHoldsClaude
// seam. v2.1 made it PID-matched, so the pid is part of the question.
type contractResumeLogicCheck struct {
	pane string
	pid  int
}

// contractResumeLogicSeams records what each of the two core seams was asked.
type contractResumeLogicSeams struct {
	respawns []contractResumeLogicCall
	// checked lists the (pane, pid) pairs paneHoldsClaude was asked about.
	checked []contractResumeLogicCheck
}

// contractResumeLogicStubSeams swaps BOTH package seams for recorders and
// restores them on cleanup — the package's established seam-swapping
// convention (see usagepool.go's securityFind/claudeJSONPath vars and their
// tests). holds is what the pane check answers; ret is what the respawn
// returns.
func contractResumeLogicStubSeams(t *testing.T, holds bool, ret error) *contractResumeLogicSeams {
	t.Helper()
	origRespawn, origHolds := respawnPane, paneHoldsClaude
	t.Cleanup(func() { respawnPane, paneHoldsClaude = origRespawn, origHolds })
	s := &contractResumeLogicSeams{}
	paneHoldsClaude = func(pane string, pid int) bool {
		s.checked = append(s.checked, contractResumeLogicCheck{pane, pid})
		return holds
	}
	respawnPane = func(target, cwd, cmd string) error {
		s.respawns = append(s.respawns, contractResumeLogicCall{target, cwd, cmd})
		return ret
	}
	return s
}

// contractResumeLogicSessionFile writes the ~/.claude/sessions/<pid>.json
// Claude Code maintains for a live session, and returns its path.
func contractResumeLogicSessionFile(t *testing.T, home string, pid int) string {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("make sessions dir: %v", err)
	}
	path := filepath.Join(dir, strconv.Itoa(pid)+".json")
	doc := `{"pid":` + strconv.Itoa(pid) + `,"sessionId":"resume-sess-0001","status":"busy"}`
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatalf("write session file: %v", err)
	}
	return path
}

// contractResumeLogicWriteClaudeJSON points the claudeJSONPath seam at a
// fixture file holding doc, restoring the seam on cleanup — the same pattern
// rotation_test.go already uses.
func contractResumeLogicWriteClaudeJSON(t *testing.T, doc string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "claude.json")
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatalf("write claude.json fixture: %v", err)
	}
	orig := claudeJSONPath
	t.Cleanup(func() { claudeJSONPath = orig })
	claudeJSONPath = func() string { return path }
}

// contractResumeLogicCmd is a stand-in for the finished zeus command the
// jobs-side caller builds. Its exact content is not this package's business
// under v2 — that it arrives at the seam byte-for-byte is.
const contractResumeLogicCmd = `kraken-test-claude --dangerously-skip-permissions --rc --model claude-fable-5 ` +
	`--append-system-prompt "$(cat /tmp/brief.md)" -r resume-sess-0001 -- "$(cat /tmp/prompt.md)"`

// ---------------------------------------------------------------------------
// ResumeGuard — the accepting half of the biconditional (unchanged in v2)
// ---------------------------------------------------------------------------

// TestContractResumeLogicGuardAcceptsAResumableClaude pins the `nil ⇐` half of
// "nil ⇔ p is a resumable claude": the four named conditions all satisfied
// means nil, full stop. Every case below is that shape with one INCIDENTAL
// field varied — the fields the contract does NOT name — because a guard that
// also refused on depth, role or model would narrow the one target v2 has.
//
// `a peer with no pid` is the one that sharpened under v2.1: the pid IS load-
// bearing now, but its check belongs to RespawnPeerPane's step 2, not here.
// A guard that absorbed it would make the two steps' refusals indistinguishable
// — "no session id resolved" and "identity unverifiable" are different facts,
// and only one of them can change on the next tick.
func TestContractResumeLogicGuardAcceptsAResumableClaude(t *testing.T) {
	cases := map[string]func(*Peer){
		"the plain shape":           func(*Peer) {},
		"a nested subagent":         func(p *Peer) { p.Depth = 2 },
		"a tmux teammate":           func(p *Peer) { p.Role = "teammate"; p.AgentName = "hephaestus-slice-a" },
		"a display-shortened model": func(p *Peer) { p.Model = "opus-5" },
		"a peer with no cwd":        func(p *Peer) { p.CWD = "" },
		"a peer with no pid":        func(p *Peer) { p.PID = 0 },
		"a peer in a team":          func(p *Peer) { p.Team = "cell-crew"; p.ParentSID = "lead-sess" },
		"a peer with a kind":        func(p *Peer) { p.Kind = "cell" },
		"a peer marked self":        func(p *Peer) { p.Self = true },
		"an explicit false guess":   func(p *Peer) { p.SessionIDGuessed = false },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := contractResumeLogicPeer("resume-sess-0001")
			mutate(p)
			if err := ResumeGuard(p); err != nil {
				t.Fatalf("ResumeGuard refused a resumable claude: %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// RespawnPeerPane — the four-step order
// ---------------------------------------------------------------------------

// TestContractResumeLogicRespawnCallsTheSeamOnceWithTheCallersCommand is
// step 3: the seam is invoked EXACTLY once with p.Pane, PeerCWD(p) and the
// caller's command unchanged. "Exactly once" is not decoration — respawn-pane
// -k is a SIGKILL, so a second call would kill the claude the first one just
// started; "unchanged" is the v2 point — this function no longer builds a
// command, and a core-side edit to one is the flag-dropping resume v1 shipped.
func TestContractResumeLogicRespawnCallsTheSeamOnceWithTheCallersCommand(t *testing.T) {
	contractResumeLogicIsolate(t)
	seams := contractResumeLogicStubSeams(t, true, nil)
	p := contractResumeLogicPeer("resume-sess-0001")

	if err := RespawnPeerPane(p, contractResumeLogicCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil — the guard passes, the pane holds a claude, the seam succeeded", err)
	}
	if len(seams.respawns) != 1 {
		t.Fatalf("respawnPane called %d times, want exactly 1", len(seams.respawns))
	}
	got := seams.respawns[0]
	if got.target != p.Pane {
		t.Errorf("target = %q, want p.Pane %q", got.target, p.Pane)
	}
	if got.cwd != PeerCWD(p) {
		t.Errorf("cwd = %q, want PeerCWD(p) %q", got.cwd, PeerCWD(p))
	}
	if got.cmd != contractResumeLogicCmd {
		t.Errorf("cmd =\n  %s\nwant the caller's command byte-for-byte\n  %s", got.cmd, contractResumeLogicCmd)
	}
}

// TestContractResumeLogicRespawnUsesPeerCWDForAnEmptyCWD pins the one place
// the cwd is not simply p.CWD: `tmux respawn-pane -c ""` is not "wherever it
// was", and PeerCWD's home fallback is what keeps the respawned claude out of
// an unusable directory.
func TestContractResumeLogicRespawnUsesPeerCWDForAnEmptyCWD(t *testing.T) {
	contractResumeLogicIsolate(t)
	seams := contractResumeLogicStubSeams(t, true, nil)
	p := contractResumeLogicPeer("resume-sess-nocwd")
	p.CWD = ""

	if err := RespawnPeerPane(p, contractResumeLogicCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil", err)
	}
	if len(seams.respawns) != 1 {
		t.Fatalf("respawnPane called %d times, want 1", len(seams.respawns))
	}
	if got, want := seams.respawns[0].cwd, PeerCWD(p); got != want {
		t.Fatalf("cwd = %q, want PeerCWD(p) = %q", got, want)
	}
	if seams.respawns[0].cwd == "" {
		t.Error("cwd = \"\" — respawn-pane needs a real directory")
	}
}

// TestContractResumeLogicRespawnReturnsTheSeamErrorUnchanged: step 3's
// "return its error unchanged". The caller counts a resume as landed exactly
// when this returns nil, so an error wrapped or re-typed on the way out is a
// miscount in the one number that decides whether the hit resolves.
func TestContractResumeLogicRespawnReturnsTheSeamErrorUnchanged(t *testing.T) {
	contractResumeLogicIsolate(t)
	sentinel := errors.New("tmux respawn-pane: can't find pane %7")
	contractResumeLogicStubSeams(t, true, sentinel)

	if err := RespawnPeerPane(contractResumeLogicPeer("resume-sess-seamerr"), contractResumeLogicCmd); err != sentinel {
		t.Fatalf("RespawnPeerPane = %v (%T), want the seam's own error value unchanged", err, err)
	}
}

// TestContractResumeLogicRespawnGuardComesFirst is step 1 stated as an
// ORDERING: a peer the guard refuses must produce the guard's own error with
// NEITHER seam consulted. Consulting paneHoldsClaude first would shell out to
// resolve a process tree for a peer that was never going to be respawned, and
// — the sharper half — a guard refusal must never be reported as "the pane no
// longer holds a claude", which reads as a race rather than as the permanent
// fact it is.
func TestContractResumeLogicRespawnGuardComesFirst(t *testing.T) {
	contractResumeLogicIsolate(t)
	seams := contractResumeLogicStubSeams(t, true, nil)

	p := contractResumeLogicPeer("resume-sess-guarded")
	p.Agent = "codex"

	want := ResumeGuard(p)
	if want == nil {
		t.Fatal("fixture is wrong: ResumeGuard accepted a codex peer")
	}
	got := RespawnPeerPane(p, contractResumeLogicCmd)
	if got == nil || got.Error() != want.Error() {
		t.Fatalf("RespawnPeerPane = %v, want the guard's own error %v", got, want)
	}
	if len(seams.checked) != 0 {
		t.Errorf("paneHoldsClaude was consulted %d times for a peer the guard refused", len(seams.checked))
	}
	if len(seams.respawns) != 0 {
		t.Fatalf("respawnPane was called %d times for a peer the guard refused", len(seams.respawns))
	}
}

// TestContractResumeLogicRespawnRefusesWhenThePaneNoLongerHoldsAClaude is
// step 2, the check v2 added: the peer snapshot can be a whole tick old, and
// `respawn-pane -k` is a SIGKILL. A pane whose claude exited and now runs
// something carl started must not be killed on that stale evidence — the
// refusal is an error (so the caller counts the round out) and the kill seam
// is never reached.
func TestContractResumeLogicRespawnRefusesWhenThePaneNoLongerHoldsAClaude(t *testing.T) {
	contractResumeLogicIsolate(t)
	seams := contractResumeLogicStubSeams(t, false, nil)
	p := contractResumeLogicPeer("resume-sess-stale")

	err := RespawnPeerPane(p, contractResumeLogicCmd)
	if err == nil {
		t.Fatal("RespawnPeerPane = nil for a pane that no longer holds a claude — the caller would count a kill that never happened as a landed resume")
	}
	if len(seams.respawns) != 0 {
		t.Fatalf("respawnPane was called %d times — a pane holding something else was SIGKILLed", len(seams.respawns))
	}
	// the check is asked about THIS pane AND THIS pid — v2.1 made the seam
	// pid-matched precisely so a different claude started in the pane since
	// the snapshot cannot answer for the one the rescue meant to resume
	want := contractResumeLogicCheck{p.Pane, p.PID}
	if len(seams.checked) != 1 || seams.checked[0] != want {
		t.Errorf("paneHoldsClaude asked %v, want exactly [%+v]", seams.checked, want)
	}
}

// TestContractResumeLogicRespawnChecksTheLivePaneBeforeEveryKill pins that
// the check runs on the SUCCESS path too, not only when it refuses: it is a
// re-verification immediately before the kill, so a build that consulted it
// only in some branches would leave the stale-snapshot window open exactly
// where it matters.
func TestContractResumeLogicRespawnChecksTheLivePaneBeforeEveryKill(t *testing.T) {
	contractResumeLogicIsolate(t)
	seams := contractResumeLogicStubSeams(t, true, nil)
	p := contractResumeLogicPeer("resume-sess-checked")

	if err := RespawnPeerPane(p, contractResumeLogicCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil", err)
	}
	want := contractResumeLogicCheck{p.Pane, p.PID}
	if len(seams.checked) != 1 || seams.checked[0] != want {
		t.Fatalf("paneHoldsClaude asked %v, want exactly [%+v] before the kill", seams.checked, want)
	}
	if len(seams.respawns) != 1 {
		t.Fatalf("respawnPane called %d times, want 1", len(seams.respawns))
	}
}

// TestContractResumeLogicRespawnRefusesAPeerWithNoPID is step 2's other half,
// which v2.1 added: with no pid there is nothing to match the pane's live
// foreground process AGAINST, so the identity the check exists to establish is
// unverifiable and the kill must not happen. This is the same conservative
// direction the seam itself takes on every error path — a refused round is
// retried next tick, a SIGKILL into the wrong process is not recoverable.
func TestContractResumeLogicRespawnRefusesAPeerWithNoPID(t *testing.T) {
	for name, pid := range map[string]int{"a zero pid": 0, "a negative pid": -1} {
		t.Run(name, func(t *testing.T) {
			contractResumeLogicIsolate(t)
			// the check would say YES if it were consulted, so a respawn here
			// could only come from skipping the pid condition entirely
			seams := contractResumeLogicStubSeams(t, true, nil)
			p := contractResumeLogicPeer("resume-sess-nopid")
			p.PID = pid

			if err := RespawnPeerPane(p, contractResumeLogicCmd); err == nil {
				t.Fatal("RespawnPeerPane = nil for a peer whose identity cannot be verified")
			}
			if len(seams.respawns) != 0 {
				t.Fatalf("respawnPane was called %d times for a peer with no pid to match", len(seams.respawns))
			}
		})
	}
}

// TestContractResumeLogicRespawnRemovesTheStaleSessionFile is step 4. The
// SIGKILLed claude can never remove ~/.claude/sessions/<pid>.json itself, and
// a leftover file is a PID-reuse hazard: discovery reads a session file by
// PID, so an unrelated process that later inherits 4242 would be reported as
// this claude — with this session id, which is what a resume targets.
func TestContractResumeLogicRespawnRemovesTheStaleSessionFile(t *testing.T) {
	home := contractResumeLogicIsolate(t)
	contractResumeLogicStubSeams(t, true, nil)
	p := contractResumeLogicPeer("resume-sess-unlink")
	path := contractResumeLogicSessionFile(t, home, p.PID)

	if err := RespawnPeerPane(p, contractResumeLogicCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s still exists after a successful respawn (stat err %v) — the SIGKILLed claude cannot remove it itself", path, err)
	}
}

// TestContractResumeLogicRespawnUnlinkIsBestEffort: "removal failure is not an
// error". The unlink is hygiene that follows a kill which already succeeded —
// reporting its failure would turn a landed resume into a counted-out one and
// send the rescue round the loop again against a cell that is already back.
func TestContractResumeLogicRespawnUnlinkIsBestEffort(t *testing.T) {
	contractResumeLogicIsolate(t) // no sessions dir at all: nothing to remove
	contractResumeLogicStubSeams(t, true, nil)

	if err := RespawnPeerPane(contractResumeLogicPeer("resume-sess-nofile"), contractResumeLogicCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil — a missing session file is not a failed respawn", err)
	}
}

// TestContractResumeLogicRespawnUnlinksOnlyAfterALandedRespawn pins step 4's
// remaining condition — a NIL seam return — from the side where getting it
// wrong destroys state: a failed respawn leaves the ORIGINAL claude alive and
// still holding that file, and removing it would blind discovery to a process
// that is still running.
//
// v2.1 dropped step 4's `p.PID > 0` guard because step 2 now refuses such a
// peer outright, so there is no path to the unlink without a pid; that case
// moved to TestContractResumeLogicRespawnRefusesAPeerWithNoPID.
func TestContractResumeLogicRespawnUnlinksOnlyAfterALandedRespawn(t *testing.T) {
	home := contractResumeLogicIsolate(t)
	contractResumeLogicStubSeams(t, true, errors.New("respawn-pane failed"))
	p := contractResumeLogicPeer("resume-sess-keepfile")
	path := contractResumeLogicSessionFile(t, home, p.PID)

	if err := RespawnPeerPane(p, contractResumeLogicCmd); err == nil {
		t.Fatal("RespawnPeerPane = nil, want the seam's error")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("%s was removed after a FAILED respawn (%v) — the original claude is still alive and still owns it", path, err)
	}
}

// TestContractResumeLogicSeamsHaveProductionValues: both are vars so tests can
// swap them, but their shipped values are the real tmux calls. A nil default
// would make every production rescue panic on the first pane while every test
// in this file passed.
func TestContractResumeLogicSeamsHaveProductionValues(t *testing.T) {
	if respawnPane == nil {
		t.Error("respawnPane is nil — the seam has no production value")
	}
	if paneHoldsClaude == nil {
		t.Error("paneHoldsClaude is nil — the seam has no production value")
	}
}

// ---------------------------------------------------------------------------
// MainIdentityEmail (unchanged in v2)
// ---------------------------------------------------------------------------

// TestContractResumeLogicMainIdentityEmailReadsTheOauthAccount is the row's
// happy path: the emailAddress recorded under oauthAccount in the file at
// claudeJSONPath(). This is the LIVE identity the rescue's target selection
// uses — read from the file the swap itself rewrites, rather than from a usage
// snapshot that can be a poll behind.
func TestContractResumeLogicMainIdentityEmailReadsTheOauthAccount(t *testing.T) {
	contractResumeLogicWriteClaudeJSON(t, `{
	  "numStartups": 412,
	  "oauthAccount": {
	    "accountUuid": "u-1",
	    "emailAddress": "live-main@example.com",
	    "displayName": "Carl",
	    "organizationRateLimitTier": "max_20x"
	  },
	  "mcpServers": {}
	}`)

	if got := MainIdentityEmail(); got != "live-main@example.com" {
		t.Fatalf("MainIdentityEmail = %q, want %q", got, "live-main@example.com")
	}
}

// TestContractResumeLogicMainIdentityEmailPreservesTheRecordedCase pins that
// the value is reported as written. The pool exclusion downstream is
// case-insensitive by its own rule; folding case HERE would hide a real
// mismatch between what the file says and what a snapshot row says.
func TestContractResumeLogicMainIdentityEmailPreservesTheRecordedCase(t *testing.T) {
	contractResumeLogicWriteClaudeJSON(t, `{"oauthAccount":{"emailAddress":"Live.Main@Example.COM"}}`)
	if got := MainIdentityEmail(); got != "Live.Main@Example.COM" {
		t.Fatalf("MainIdentityEmail = %q, want the recorded spelling %q", got, "Live.Main@Example.COM")
	}
}

// TestContractResumeLogicMainIdentityEmailReadsTheDefaultPath drives the seam
// at its production value with HOME isolated: the file really is
// $HOME/.claude.json, so a rescue running where nothing swapped the seam still
// finds the identity.
func TestContractResumeLogicMainIdentityEmailReadsTheDefaultPath(t *testing.T) {
	home := contractResumeLogicIsolate(t)
	doc := `{"oauthAccount":{"emailAddress":"home-main@example.com"}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := MainIdentityEmail(); got != "home-main@example.com" {
		t.Fatalf("MainIdentityEmail = %q, want %q from $HOME/.claude.json", got, "home-main@example.com")
	}
}

// TestContractResumeLogicMainIdentityEmailNeverErrors pins the "" contract and
// the posture behind it: the named degradations answer "" rather than failing,
// because the caller's fallback (the snapshot's own main row) is a real answer
// and a hard failure here would take the whole rescue down with it.
func TestContractResumeLogicMainIdentityEmailNeverErrors(t *testing.T) {
	cases := map[string]string{
		"no oauthAccount field": `{"numStartups":1}`,
		"no emailAddress field": `{"oauthAccount":{"displayName":"Carl"}}`,
		"an empty emailAddress": `{"oauthAccount":{"emailAddress":""}}`,
		"unparseable":           `{"oauthAccount":{`,
		"not an object at all":  `["oauthAccount"]`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			contractResumeLogicWriteClaudeJSON(t, doc)
			if got := MainIdentityEmail(); got != "" {
				t.Fatalf("MainIdentityEmail = %q, want \"\"", got)
			}
		})
	}

	t.Run("a missing file", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "no-such-claude.json")
		orig := claudeJSONPath
		t.Cleanup(func() { claudeJSONPath = orig })
		claudeJSONPath = func() string { return missing }
		if got := MainIdentityEmail(); got != "" {
			t.Fatalf("MainIdentityEmail = %q, want \"\" for a missing file", got)
		}
	})
}
