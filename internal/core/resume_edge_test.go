package core

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// resume_edge_test.go — the EDGE dimension of the kill+resume rescue
// primitive (artifacts/50-contract.md v2, package `core`): the guard's
// refusals, the pre-kill re-verification's refusal, the respawn wrapper's
// error paths and guard ORDER, the killed session file's unlink edges, and
// MainIdentityEmail's degradation to "".
//
// v2 adaptation. `ResumeCmdString` is REMOVED from the contract, so its three
// suites here are deleted outright — the caller builds the full zeus command
// jobs-side now, and the jobs suite asserts its contents. `RespawnPeerPane`
// takes that command rather than a prompt, gained the `paneHoldsClaude` gate
// between the guard and the seam (the contract's `paneHoldsClaude` row), and
// gained a best-effort session-file unlink after a successful respawn.
//
// v2.1 adaptation. That gate is now PID-matched — the seam takes
// `(pane string, pid int)` and answers for the foreground claude whose pid
// equals `pid` — and step (2) additionally refuses a peer whose `p.PID <= 0`,
// since an unverifiable identity means the kill could only ever be taken on
// the pane alone. The pid gate therefore MOVED out of the unlink step, which
// is why the two degenerate-pid cases below are now refusals rather than
// silent skips.
//
// Every package-level identifier carries the `edgeResume` prefix — one
// declaration space per package, and the plain `edge` prefix is already spoken
// for by the anchor/linear/slack/stall/transcript edge suites.
//
// Every test that can reach ~/.claude isolates HOME itself and writes its own
// fixtures: nothing here may depend on what this machine happens to hold, and
// nothing here may write outside its temp dir. respawnPane and paneHoldsClaude
// are stubbed in every test that reaches them, so no test in this file drives
// tmux or reads a live process tree.

// edgeResumeSession is the session id every fixture peer carries.
const edgeResumeSession = "edge-resume-0000-1111"

// edgeResumeCmd stands in for the full zeus respawn command the caller builds.
// Deliberately awkward — spaces, quotes, a command substitution — because
// RespawnPeerPane's whole job with it is to pass it through untouched.
const edgeResumeCmd = `claude --dangerously-skip-permissions --rc --model claude-fable-5 -r sess "$(cat '/tmp/zeus prompt.md')"`

// edgeResumeEnv isolates HOME and returns the fake home.
func edgeResumeEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// edgeResumePeer is the complete resumable shape: the ONE peer ResumeGuard
// accepts, which every refusal case below is a single mutation away from. PID
// is non-zero so the unlink obligation is in play by default.
func edgeResumePeer() *Peer {
	return &Peer{
		Agent:     "claude",
		Role:      "main",
		Pane:      "%7",
		PID:       4242,
		SessionID: edgeResumeSession,
		CWD:       "/tmp/edge-resume-cwd",
	}
}

type edgeResumeRespawnCall struct{ target, cwd, cmd string }

// edgeResumeProbe is one observed paneHoldsClaude call. v2.1 made the seam
// PID-matched, so both arguments are recorded: a probe run against the right
// pane but the wrong pid clears a process the rescue never meant to kill.
type edgeResumeProbe struct {
	pane string
	pid  int
}

// edgeResumeStubSeams swaps BOTH package seams: respawnPane for a recorder
// returning seamErr, and paneHoldsClaude for a constant answer. Returns the
// recorded respawn calls and the recorded probes, so "the seam was never
// reached" and "the pane was never even probed" are separate observations.
// Production values are restored on cleanup — nothing in this file may drive
// tmux or walk a real process tree.
func edgeResumeStubSeams(t *testing.T, holdsClaude bool, seamErr error) (*[]edgeResumeRespawnCall, *[]edgeResumeProbe) {
	t.Helper()
	origRespawn, origHolds := respawnPane, paneHoldsClaude
	calls := []edgeResumeRespawnCall{}
	probes := []edgeResumeProbe{}
	respawnPane = func(target, cwd, cmd string) error {
		calls = append(calls, edgeResumeRespawnCall{target: target, cwd: cwd, cmd: cmd})
		return seamErr
	}
	paneHoldsClaude = func(pane string, pid int) bool {
		probes = append(probes, edgeResumeProbe{pane: pane, pid: pid})
		return holdsClaude
	}
	t.Cleanup(func() { respawnPane, paneHoldsClaude = origRespawn, origHolds })
	return &calls, &probes
}

// edgeResumeSessionFile is the path RespawnPeerPane unlinks after a successful
// respawn, written as a fixture by the tests that assert on it.
func edgeResumeSessionFile(t *testing.T, pid int) string {
	t.Helper()
	dir := filepath.Join(os.Getenv("HOME"), ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, strconv.Itoa(pid)+".json")
}

func edgeResumeWriteSessionFile(t *testing.T, pid int) string {
	t.Helper()
	p := edgeResumeSessionFile(t, pid)
	body := `{"pid":` + strconv.Itoa(pid) + `,"sessionId":"` + edgeResumeSession + `","status":"waiting"}`
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func edgeResumeFileExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	return err == nil
}

// --- ResumeGuard: the refusals (unchanged from v1) ---

// The guard is stated as a biconditional — nil ⇔ Agent=="claude" && Pane!=""
// && SessionID!="" && !SessionIDGuessed — so every single-field break must
// refuse, and each refusal must NAME its condition rather than answering with
// one anonymous "not resumable" for all four. The naming matters at the call
// site above it: RespawnPeerPane returns this error unchanged, and the rescue
// pass logs it as the reason the cell was not resumed.
func TestEdgeResumeGuardRefusesEveryIncompleteShape(t *testing.T) {
	edgeResumeEnv(t)

	// the control, without which every assertion below would also pass for a
	// guard that refused everything
	if err := ResumeGuard(edgeResumePeer()); err != nil {
		t.Fatalf("the complete resumable shape was refused: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Peer)
		// names is a lowercase fragment the message must contain, derived
		// from the condition's own vocabulary. Empty means only "non-nil and
		// non-empty" is asserted (the bare Agent=="" break has no wording the
		// contract implies).
		names string
	}{
		{"an agent kind with no resume", func(p *Peer) { p.Agent = "aider" }, "aider"},
		{"an agent-less row", func(p *Peer) { p.Agent = "" }, ""},
		{"no pane to respawn", func(p *Peer) { p.Pane = "" }, "pane"},
		{"no session id", func(p *Peer) { p.SessionID = "" }, "session"},
		{"a guessed session id", func(p *Peer) { p.SessionIDGuessed = true }, "guess"},
	}

	seen := map[string]string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := edgeResumePeer()
			c.mutate(p)

			err := ResumeGuard(p)
			if err == nil {
				t.Fatalf("ResumeGuard accepted %s — a resume built on it would respawn the wrong pane, or another claude's session", c.name)
			}
			msg := err.Error()
			if strings.TrimSpace(msg) == "" {
				t.Fatalf("the refusal for %s carries an empty message", c.name)
			}
			if c.names != "" && !strings.Contains(strings.ToLower(msg), c.names) {
				t.Errorf("refusal for %s = %q, want it to name the failing condition (%q)", c.name, msg, c.names)
			}
			if prev, dup := seen[msg]; dup {
				t.Errorf("%s and %s refuse with the identical message %q — the message names no condition", c.name, prev, msg)
			}
			seen[msg] = c.name
		})
	}
}

// A shell row is refused. The contract states it by name, which is the reading
// this test takes: Role "shell" is itself disqualifying, exactly as BranchGuard
// treats it, and not merely a shape that happens to also carry an empty Agent.
func TestEdgeResumeGuardRefusesAShellRow(t *testing.T) {
	edgeResumeEnv(t)

	t.Run("the ordinary shell row", func(t *testing.T) {
		if err := ResumeGuard(&Peer{Role: "shell", Pane: "%9"}); err == nil {
			t.Fatal("ResumeGuard accepted a shell pane — there is no claude in it to resume")
		}
	})

	t.Run("a shell-roled row that is otherwise complete", func(t *testing.T) {
		p := edgeResumePeer()
		p.Role = "shell"
		if err := ResumeGuard(p); err == nil {
			t.Fatal("ResumeGuard accepted a Role==\"shell\" row — the contract names shell rows as refused")
		}
	})
}

// --- RespawnPeerPane: the two refusals, in order ---

// A refused peer must reach no tmux at all: the guard's error comes back
// unchanged, the seam is never invoked, and — the ORDER obligation — the pane
// is not even probed, since step 1 returns before step 2 runs. The seam is
// what runs `tmux respawn-pane -k`, which SIGKILLs whatever the pane holds
// before starting the new command: invoking it for a peer the guard refused
// destroys a running agent for a resume that could never have worked.
func TestEdgeRespawnPeerPaneGuardRefusalsStopBeforeEverythingElse(t *testing.T) {
	edgeResumeEnv(t)
	calls, probes := edgeResumeStubSeams(t, true, nil)

	breaks := []struct {
		name   string
		mutate func(*Peer)
	}{
		{"an agent kind with no resume", func(p *Peer) { p.Agent = "aider" }},
		{"an agent-less row", func(p *Peer) { p.Agent = "" }},
		{"a shell row", func(p *Peer) { p.Role = "shell" }},
		{"no pane", func(p *Peer) { p.Pane = "" }},
		{"no session id", func(p *Peer) { p.SessionID = "" }},
		{"a guessed session id", func(p *Peer) { p.SessionIDGuessed = true }},
	}

	for _, b := range breaks {
		t.Run(b.name, func(t *testing.T) {
			p := edgeResumePeer()
			b.mutate(p)
			sessionFile := edgeResumeWriteSessionFile(t, p.PID)

			want := ResumeGuard(p)
			if want == nil {
				t.Fatalf("fixture: the guard accepts %s, so there is no refusal to propagate", b.name)
			}

			before := len(*probes)
			got := RespawnPeerPane(p, edgeResumeCmd)
			if got == nil {
				t.Fatalf("RespawnPeerPane accepted %s", b.name)
			}
			if got.Error() != want.Error() {
				t.Errorf("error = %q, want the guard's own message %q unchanged", got, want)
			}
			if len(*calls) != 0 {
				t.Fatalf("the respawn seam was invoked %d time(s) for a refused peer — respawn-pane -k kills the pane before it starts anything", len(*calls))
			}
			if len(*probes) != before {
				t.Errorf("paneHoldsClaude was consulted for a peer the guard had already refused — step 1 returns before step 2")
			}
			if !edgeResumeFileExists(t, sessionFile) {
				t.Errorf("the session file was unlinked for a peer that was never respawned")
			}
		})
	}
}

// The pre-kill re-verification, which exists because the peer snapshot handed
// to the rescue can be a whole daemon tick old: between discovery and this
// call the claude may have exited and carl may have started something else in
// that pane. `respawn-pane -k` is a SIGKILL, so acting on the stale read is
// not a mistyped message — it is carl's foreground work destroyed with no
// record beyond a success count.
func TestEdgeRespawnPeerPaneRefusesAPaneThatNoLongerHoldsAClaude(t *testing.T) {
	edgeResumeEnv(t)
	calls, probes := edgeResumeStubSeams(t, false, nil)

	p := edgeResumePeer()
	sessionFile := edgeResumeWriteSessionFile(t, p.PID)

	err := RespawnPeerPane(p, edgeResumeCmd)
	if err == nil {
		t.Fatal("RespawnPeerPane killed a pane that no longer holds a claude")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Error("the refusal carries an empty message")
	}
	if len(*calls) != 0 {
		t.Fatalf("the respawn seam was invoked %d time(s) after the pane check failed", len(*calls))
	}
	if len(*probes) != 1 {
		t.Errorf("paneHoldsClaude was consulted %d times, want exactly once", len(*probes))
	}
	if !edgeResumeFileExists(t, sessionFile) {
		t.Error("the session file was unlinked although nothing was respawned")
	}
}

// v2.1: the probe is PID-matched, so BOTH arguments have to be the peer's own.
// The pane alone is not identity — a different claude started in that pane
// since the snapshot answers "yes, a claude is here" to a pane-only check, and
// the SIGKILL then lands on it instead of on the process the rescue meant to
// resume. Passing some other pid would clear a process nobody is about to
// kill, which is the same failure from the other side.
func TestEdgeRespawnPeerPaneProbesThePaneAndPIDItIsAboutToKill(t *testing.T) {
	edgeResumeEnv(t)
	calls, probes := edgeResumeStubSeams(t, true, nil)

	p := edgeResumePeer()
	if err := RespawnPeerPane(p, edgeResumeCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil", err)
	}
	if len(*probes) != 1 {
		t.Fatalf("paneHoldsClaude was called %d times, want exactly once: %+v", len(*probes), *probes)
	}
	got := (*probes)[0]
	if got.pane != p.Pane {
		t.Errorf("probed pane %q, want the peer's own pane %q", got.pane, p.Pane)
	}
	if got.pid != p.PID {
		t.Errorf("probed pid %d, want the peer's own pid %d — a pane-only check passes for a DIFFERENT claude started there since the snapshot", got.pid, p.PID)
	}
	if len(*calls) != 1 {
		t.Fatalf("seam calls = %d, want 1", len(*calls))
	}
}

// v2.1 step (2)'s other half: a peer whose pid is unusable cannot have its
// identity verified at all, so it is refused before anything is probed or
// killed. PID 0 is the ordinary shape of "not known" (an in-process teammate,
// a row whose process was gone at discovery), and `0.json` / `-1.json` are
// real paths that could belong to somebody — so nothing is unlinked either.
func TestEdgeRespawnPeerPaneRefusesAnUnverifiablePID(t *testing.T) {
	for _, pid := range []int{0, -1} {
		t.Run(strconv.Itoa(pid), func(t *testing.T) {
			edgeResumeEnv(t)
			calls, probes := edgeResumeStubSeams(t, true, nil)

			p := edgeResumePeer()
			p.PID = pid
			sessionFile := edgeResumeWriteSessionFile(t, pid)

			err := RespawnPeerPane(p, edgeResumeCmd)
			if err == nil {
				t.Fatalf("RespawnPeerPane accepted a peer with pid %d — its identity cannot be verified, so the kill would be taken on the pane alone", pid)
			}
			if strings.TrimSpace(err.Error()) == "" {
				t.Error("the refusal carries an empty message")
			}
			if len(*calls) != 0 {
				t.Fatalf("the respawn seam was invoked for an unverifiable peer: %+v", *calls)
			}
			if len(*probes) != 0 {
				t.Errorf("paneHoldsClaude was consulted with pid %d — there is nothing to match against", pid)
			}
			if !edgeResumeFileExists(t, sessionFile) {
				t.Errorf("%s was removed for a peer that was never respawned", sessionFile)
			}
		})
	}
}

// --- RespawnPeerPane: the seam's own error, and the command's passage ---

// The seam's error comes back UNCHANGED — the same error value, not a wrapped
// or re-worded one. resumeCellAgents counts the resume as done exactly when
// this returns nil, so any non-nil answer is a failure counted out; keeping
// the value intact is what lets the rescue log tmux's own words. The command
// passes through byte-for-byte: it is the caller's full zeus spawn line, and
// anything this function did to it would be a launch flag silently altered.
func TestEdgeRespawnPeerPaneReturnsTheSeamsErrorUnchanged(t *testing.T) {
	edgeResumeEnv(t)
	boom := errors.New("tmux respawn-pane: can't find pane %7")
	calls, _ := edgeResumeStubSeams(t, true, boom)

	p := edgeResumePeer()

	err := RespawnPeerPane(p, edgeResumeCmd)
	if err != boom {
		t.Fatalf("error = %v (%T), want the seam's own error value unchanged", err, err)
	}
	if !errors.Is(err, boom) {
		t.Errorf("errors.Is could not find the seam's error in %v", err)
	}

	if len(*calls) != 1 {
		t.Fatalf("the seam was invoked %d times, want exactly once — a retry would kill the pane twice", len(*calls))
	}
	c := (*calls)[0]
	if c.target != p.Pane {
		t.Errorf("target = %q, want the peer's pane %q", c.target, p.Pane)
	}
	if c.cwd != PeerCWD(p) {
		t.Errorf("cwd = %q, want PeerCWD %q", c.cwd, PeerCWD(p))
	}
	if c.cmd != edgeResumeCmd {
		t.Errorf("cmd =\n  %q\nwant the caller's command verbatim\n  %q", c.cmd, edgeResumeCmd)
	}
}

// The cwd boundary: a peer with no cwd of its own respawns in HOME, not in
// the empty string. `tmux respawn-pane -c ""` is not a no-op — it is a
// request for a directory that does not exist, and the whole respawn fails
// for a peer the guard had already accepted.
func TestEdgeRespawnPeerPaneCWDlessPeerRespawnsInHome(t *testing.T) {
	home := edgeResumeEnv(t)
	calls, _ := edgeResumeStubSeams(t, true, nil)

	p := edgeResumePeer()
	p.CWD = ""

	if err := RespawnPeerPane(p, edgeResumeCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("seam calls = %d, want 1", len(*calls))
	}
	if got := (*calls)[0].cwd; got != home {
		t.Fatalf("cwd = %q, want the home fallback %q", got, home)
	}
}

// --- RespawnPeerPane: the killed session file ---

// The SIGKILLed claude can never remove its own ~/.claude/sessions/<pid>.json,
// so the respawn does it. Left behind, the file is a PID-reuse hazard:
// discovery reads it by live pid (discover.go's readSessionFile), so a later
// claude that happens to inherit this pid is handed a session id that is not
// its own and is NOT marked guessed — and a future resume would reopen a
// stranger's conversation in that pane.
func TestEdgeRespawnPeerPaneUnlinksTheKilledSessionFile(t *testing.T) {
	edgeResumeEnv(t)
	edgeResumeStubSeams(t, true, nil)

	p := edgeResumePeer()
	sessionFile := edgeResumeWriteSessionFile(t, p.PID)
	// a neighbouring pid's file, which must survive: the unlink is keyed on
	// THIS peer's pid, not on the directory
	neighbour := edgeResumeWriteSessionFile(t, p.PID+1)

	if err := RespawnPeerPane(p, edgeResumeCmd); err != nil {
		t.Fatalf("RespawnPeerPane = %v, want nil", err)
	}
	if edgeResumeFileExists(t, sessionFile) {
		t.Errorf("%s survived the respawn — the killed claude cannot remove it itself", sessionFile)
	}
	if !edgeResumeFileExists(t, neighbour) {
		t.Errorf("%s was removed — the unlink swept a pid that was not the killed one", neighbour)
	}
}

// The unlink's boundaries. Under v2.1 the pid gate has moved UP into step (2)
// — an unusable pid is a refusal, not a silent skip of the cleanup (see
// TestEdgeRespawnPeerPaneRefusesAnUnverifiablePID) — so what is left here is
// the tail: a respawn that failed owns no cleanup, and a removal that fails is
// not an error, because the respawn already happened and reporting a leftover
// file as a failed resume would count a rescued cell as unrescued.
func TestEdgeRespawnPeerPaneSessionFileUnlinkBoundaries(t *testing.T) {
	t.Run("a failed respawn unlinks nothing", func(t *testing.T) {
		edgeResumeEnv(t)
		edgeResumeStubSeams(t, true, errors.New("tmux respawn-pane: server not found"))

		p := edgeResumePeer()
		sessionFile := edgeResumeWriteSessionFile(t, p.PID)
		if err := RespawnPeerPane(p, edgeResumeCmd); err == nil {
			t.Fatal("the seam's error was swallowed")
		}
		if !edgeResumeFileExists(t, sessionFile) {
			t.Fatalf("%s was removed although the respawn failed — that claude may still be alive and still owns its file", sessionFile)
		}
	})

	t.Run("a missing session file is not a failure", func(t *testing.T) {
		edgeResumeEnv(t)
		edgeResumeStubSeams(t, true, nil)

		p := edgeResumePeer()
		// nothing written: the ordinary case for a claude that exited cleanly
		// earlier, or a pid whose file was never created
		if err := RespawnPeerPane(p, edgeResumeCmd); err != nil {
			t.Fatalf("RespawnPeerPane = %v, want nil — the unlink is best-effort", err)
		}
	})

	t.Run("an unremovable session file is not a failure", func(t *testing.T) {
		edgeResumeEnv(t)
		edgeResumeStubSeams(t, true, nil)

		p := edgeResumePeer()
		// a non-empty DIRECTORY where the file should be: os.Remove fails
		// with ENOTEMPTY, which is the portable way to force the unlink to
		// fail without depending on running as an unprivileged user
		blocked := edgeResumeSessionFile(t, p.PID)
		if err := os.MkdirAll(filepath.Join(blocked, "child"), 0o755); err != nil {
			t.Fatal(err)
		}

		if err := RespawnPeerPane(p, edgeResumeCmd); err != nil {
			t.Fatalf("RespawnPeerPane = %v, want nil — a removal failure must not report a successful respawn as failed", err)
		}
	})
}

// --- MainIdentityEmail: degradation (unchanged from v1) ---

// edgeResumeStubClaudeJSON points the existing claudeJSONPath seam at a file
// inside an isolated HOME and returns that path. Nothing in this file may
// read the developer's real ~/.claude.json.
func edgeResumeStubClaudeJSON(t *testing.T) string {
	t.Helper()
	home := edgeResumeEnv(t)
	p := filepath.Join(home, "claude.json")
	orig := claudeJSONPath
	claudeJSONPath = func() string { return p }
	t.Cleanup(func() { claudeJSONPath = orig })
	return p
}

// "" is the answer for every unreadable, unparseable or field-less document,
// and the function never errors — it has no error to return, so a document it
// cannot read must degrade rather than panic. The consequence downstream is
// why this matters: the jobs-side seam treats "" as "no live identity" and
// falls back to the snapshot's main row, whereas a garbage non-empty answer
// would be handed to the pool exclusion as if it were an account.
func TestEdgeMainIdentityEmailDegradesToEmpty(t *testing.T) {
	cases := []struct {
		name  string
		write bool
		body  string
	}{
		{name: "no file at all", write: false},
		{name: "an empty file", write: true, body: ""},
		{name: "not JSON", write: true, body: "not json"},
		{name: "a JSON array", write: true, body: `[]`},
		{name: "JSON null", write: true, body: `null`},
		{name: "an empty object", write: true, body: `{}`},
		{name: "no oauthAccount field", write: true, body: `{"numStartups":3}`},
		{name: "a null oauthAccount", write: true, body: `{"oauthAccount":null}`},
		{name: "an oauthAccount that is a string", write: true, body: `{"oauthAccount":"carl@probook.ai"}`},
		{name: "an empty oauthAccount", write: true, body: `{"oauthAccount":{}}`},
		{name: "an empty emailAddress", write: true, body: `{"oauthAccount":{"emailAddress":""}}`},
		{name: "a null emailAddress", write: true, body: `{"oauthAccount":{"emailAddress":null}}`},
		{name: "an emailAddress that is a number", write: true, body: `{"oauthAccount":{"emailAddress":42}}`},
		{name: "an emailAddress that is an object", write: true, body: `{"oauthAccount":{"emailAddress":{"value":"carl@probook.ai"}}}`},
		{name: "the email at the top level instead", write: true, body: `{"emailAddress":"carl@probook.ai"}`},
		{name: "a truncated document", write: true, body: `{"oauthAccount":{"emailAddress":"carl@probook.ai"`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := edgeResumeStubClaudeJSON(t)
			if c.write {
				if err := os.WriteFile(p, []byte(c.body), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if got := MainIdentityEmail(); got != "" {
				t.Fatalf("MainIdentityEmail() = %q, want \"\" for %s", got, c.name)
			}
		})
	}

	t.Run("a directory where the file should be", func(t *testing.T) {
		p := edgeResumeStubClaudeJSON(t)
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if got := MainIdentityEmail(); got != "" {
			t.Fatalf("MainIdentityEmail() = %q, want \"\"", got)
		}
	})
}

// The control for the whole degradation table: a real document still answers,
// including one carrying fields this reader does not know about. Without it
// every case above is satisfied by a function that returns "" unconditionally
// — which would silently pin the rescue to the snapshot's stale idea of main
// forever, the exact staleness the seam exists to escape.
func TestEdgeMainIdentityEmailReadsThroughUnknownFields(t *testing.T) {
	p := edgeResumeStubClaudeJSON(t)
	doc := map[string]any{
		"numStartups":     41,
		"someFutureField": map[string]any{"a": []any{1, 2, 3}},
		"oauthAccount": map[string]any{
			"accountUuid":               "0000-1111",
			"emailAddress":              "carl@probook.ai",
			"displayName":               "carl",
			"organizationRateLimitTier": "max",
			"anotherFutureField":        true,
		},
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := MainIdentityEmail(); got != "carl@probook.ai" {
		t.Fatalf("MainIdentityEmail() = %q, want carl@probook.ai", got)
	}
}
