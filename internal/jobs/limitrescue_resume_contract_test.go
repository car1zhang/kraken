package jobs

import (
	"fmt"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// limitrescue_resume_contract_test.go — athena-logic's BLIND contract tests
// for 50-contract.md v2, the `jobs` half: the respawnPeerPane/mainIdentityEmail
// seams, resumeCellAgents' zeus-only targeting and the command it hands the
// seam, the two rewritten resume prompts, the rescueResume action through
// decide and apply, selectRescueTarget's DUAL exclusion, and rescueLimitedCell's
// post-swap usage-pass kick. Written from the contract text alone;
// internal/jobs/limitrescue.go was never read while either round was open.
//
// Dimension: LOGIC — the stated invariants and the happy paths. Who gets
// resumed, what the command carries, which outcome word each arm writes, and
// which rows survive the pool filter. Boundaries, error paths and refusals
// belong to a sibling author; the one error asserted here (no zeus pane) is
// asserted because the contract states it as the ONLY error, which is a fact
// about the happy path as much as about the failure.
//
// v2 delta from limitresume_contract_test.go (renamed to this file per v2's
// filename obligation): all-panes targeting is gone — the single target is
// zeus, and teammate/codex/shell panes must never reach the seam; the seam's
// second argument is zeus' FULL respawn command rather than a prompt, so the
// pass-the-prompt-verbatim test is replaced by the command's observable
// obligations; the v1 prompt constants were renamed and rewritten, so their
// name/text pins are replaced by the required-content clauses; the pool
// exclusion is now dual.
//
// Every package-level identifier here is prefixed `contractResumeLogic` — the
// jobs package shares one declaration space across every _test.go in it, and
// both `contractLimit…` and `edge…` are already taken.
//
// Environment: every test isolates HOME itself (via stubRotation, which
// sandboxes HOME and restores every rotation seam, or t.Setenv directly) and
// wires its own store. Nothing here reads the real machine's pool, keychain or
// kraken.db.

// ---------------------------------------------------------------------------
// harness
// ---------------------------------------------------------------------------

// contractResumeLogicNow is the fixed clock every case reasons against.
var contractResumeLogicNow = time.Unix(1_700_000_000, 0)

// contractResumeLogicZeusSID is the session id the cell's zeus carries, and
// therefore the id the respawn command must resume.
const contractResumeLogicZeusSID = "zeus-sess-0001"

// contractResumeLogicCall records one invocation of the respawnPeerPane seam.
// Under v2 the second argument is the finished shell command, not a prompt.
type contractResumeLogicCall struct {
	peer *core.Peer
	cmd  string
}

// contractResumeLogicStubRespawn swaps the respawnPeerPane seam for a
// recorder, restoring it on cleanup — the pattern swapMain/selectTarget
// already established in this package. Panes listed in failPanes get a
// non-nil error back, standing in for a guard refusal, a pane that no longer
// holds a claude, or a failed respawn — without a tmux server.
func contractResumeLogicStubRespawn(t *testing.T, failPanes ...string) *[]contractResumeLogicCall {
	t.Helper()
	fail := map[string]bool{}
	for _, p := range failPanes {
		fail[p] = true
	}
	orig := respawnPeerPane
	t.Cleanup(func() { respawnPeerPane = orig })
	var calls []contractResumeLogicCall
	respawnPeerPane = func(p *core.Peer, cmd string) error {
		calls = append(calls, contractResumeLogicCall{p, cmd})
		if p != nil && fail[p.Pane] {
			return fmt.Errorf("respawn %s: pane is gone", p.Pane)
		}
		return nil
	}
	return &calls
}

// contractResumeLogicStubMainIdentity swaps the mainIdentityEmail seam,
// restoring it on cleanup.
func contractResumeLogicStubMainIdentity(t *testing.T, email string) {
	t.Helper()
	orig := mainIdentityEmail
	t.Cleanup(func() { mainIdentityEmail = orig })
	mainIdentityEmail = func() string { return email }
}

// contractResumeLogicCell builds a job plus the peer list a live cell
// produces: zeus, a tmux teammate hanging off it, a second cell claude, a
// codex pane, a shell pane, and one peer from another tmux session.
//
// The teammate deliberately carries NO SessionID — that is production truth
// (discover.go builds a tmux teammate from its command-line flags and leaves
// the id empty), and it is the fact that killed v1's per-peer design. Under
// v2 no teammate is ever handed to the seam, and this fixture is what proves
// it rather than assuming it.
func contractResumeLogicCell(t *testing.T, slug string) (*Job, []*core.Peer) {
	t.Helper()
	st := useTestStore(t)
	row := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug, Repo: row.Repo, Model: "claude-fable-5"}
	sess := CellSession(j)

	// CWD and PID are both load-bearing under v2.1: an empty cwd is a refusal
	// (the respawn must not fall back to $HOME) and the pid is what the pane
	// re-verification matches against.
	zeus := &core.Peer{
		TmuxSession: sess, Agent: "claude", Role: "main", Depth: 0,
		Pane: "%1", Name: ZeusName(j), SessionID: contractResumeLogicZeusSID,
		PID: 4242, CWD: "/tmp/kraken-cell-worktree",
	}
	zeus.Teammates = []*core.Peer{
		{Agent: "claude", Pane: "%5", Name: "hephaestus-slice-1", Role: "teammate", AgentName: "hephaestus-slice-1"},
	}
	all := []*core.Peer{
		zeus,
		zeus.Teammates[0],
		{TmuxSession: sess, Agent: "claude", Role: "main", Depth: 0, Pane: "%2", SessionID: "crew-sess"},
		{TmuxSession: sess, Agent: "codex", Role: "main", Depth: 0, Pane: "%3"},
		{TmuxSession: sess, Agent: "", Role: "shell", Depth: 0, Pane: "%4"},
		{TmuxSession: "cell-someone-else-9", Agent: "claude", Role: "main", Depth: 0, Pane: "%9", SessionID: "other-sess"},
	}
	return j, all
}

// contractResumeLogicStageCellDir makes the cell directory the zeus spawn
// machinery stages its brief and prompt into. Without it the command build has
// nowhere to write and the assertions below would be measuring a failure
// rather than a command (cell_test.go's own spawnCommand cases do the same).
func contractResumeLogicStageCellDir(t *testing.T, j *Job) {
	t.Helper()
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
}

// contractResumeLogicStagedRe matches the `"$(cat <path>)"` form the spawn
// machinery uses for an argument whose length is driven by content.
//
// The path class excludes `)` deliberately: a zeus command carries TWO staged
// arguments (the brief and the prompt), so a `(.+)` capture anchored at the
// end would swallow the brief's closing `)"` and everything between, and
// report the whole span as one path.
var contractResumeLogicStagedRe = regexp.MustCompile(`"\$\(cat ([^)]+)\)"$`)

// contractResumeLogicEndsWithPrompt reports whether cmd's FINAL argument is
// prompt. The contract permits two encodings for it — "staged or quoted per
// the spawn machinery's own convention" — so both are resolved here and the
// assertion at every call site is the single one the contract states: the
// last thing on that command line is the prompt.
func contractResumeLogicEndsWithPrompt(t *testing.T, cmd, prompt string) bool {
	t.Helper()
	trimmed := strings.TrimRight(cmd, " \t\n")
	if strings.HasSuffix(trimmed, core.ShellQuote(prompt)) {
		return true
	}
	m := contractResumeLogicStagedRe.FindStringSubmatch(trimmed)
	if m == nil {
		return false
	}
	path := strings.Trim(strings.TrimSpace(m[1]), "'")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("command stages its final argument at %q but that file is unreadable: %v", path, err)
	}
	return string(data) == prompt
}

// contractResumeLogicSnapshot writes a usage snapshot with the given rows to
// the path the rescue reads, under the HOME stubRotation sandboxed.
func contractResumeLogicSnapshot(t *testing.T, rows ...core.AccountUsage) {
	t.Helper()
	fresh := time.Now().UnixMilli()
	for i := range rows {
		if rows[i].FetchedAt == 0 {
			rows[i].FetchedAt = fresh
		}
		if rows[i].Session == nil {
			rows[i].Session = &core.UsageWindow{Percent: 5}
		}
	}
	if err := writeUsageSnapshot(core.UsageSnapshot{FetchedAt: fresh, Accounts: rows}, UsageSnapshotPath()); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// the seams
// ---------------------------------------------------------------------------

// TestContractResumeLogicSeamsPointAtCore pins the two package seams at their
// declared initializers. They exist so jobs-package tests can observe a resume
// without tmux; a seam wired to a local wrapper (or to nothing) tests the
// wrapper instead of the primitive, and one wired to the wrong core function
// is a rescue that silently does something else in production while every
// stubbed test still passes.
func TestContractResumeLogicSeamsPointAtCore(t *testing.T) {
	if respawnPeerPane == nil {
		t.Fatal("respawnPeerPane is nil — the seam has no production value")
	}
	if got, want := reflect.ValueOf(respawnPeerPane).Pointer(), reflect.ValueOf(core.RespawnPeerPane).Pointer(); got != want {
		t.Error("respawnPeerPane is not core.RespawnPeerPane — the production path is not the one the seam names")
	}
	if mainIdentityEmail == nil {
		t.Fatal("mainIdentityEmail is nil — the seam has no production value")
	}
	if got, want := reflect.ValueOf(mainIdentityEmail).Pointer(), reflect.ValueOf(core.MainIdentityEmail).Pointer(); got != want {
		t.Error("mainIdentityEmail is not core.MainIdentityEmail")
	}
}

// ---------------------------------------------------------------------------
// resumeCellAgents — zeus, and only zeus
// ---------------------------------------------------------------------------

// TestContractResumeLogicResumesZeusAndNobodyElse is v2's central correction.
// v1 handed every pane in cellAgentPanes to the seam; a tmux teammate has no
// resolvable SessionID, so the guard refused it, the cell's actual crew was
// never resumed, and the hit resolved anyway on zeus alone. v2 targets zeus
// explicitly and makes reviving the crew zeus' own first move — so a teammate,
// codex or shell pane reaching the seam is not a smaller version of the old
// bug, it is a SIGKILL aimed at a process this rescue has no way to bring back.
func TestContractResumeLogicResumesZeusAndNobodyElse(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j, all := contractResumeLogicCell(t, "resume-zeus-only")
	contractResumeLogicStageCellDir(t, j)
	calls := contractResumeLogicStubRespawn(t)

	zeus := CellZeus(j, all)
	if zeus == nil || zeus.Pane == "" {
		t.Fatal("fixture is wrong: the cell has no zeus pane")
	}

	resumed, err := resumeCellAgents(j, all, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents err = %v, want nil — the cell has a zeus pane", err)
	}
	if resumed != 1 {
		t.Errorf("resumed = %d, want 1", resumed)
	}
	if len(*calls) != 1 {
		t.Fatalf("respawnPeerPane called %d times, want exactly 1 (zeus)", len(*calls))
	}
	if got := (*calls)[0].peer; got != zeus {
		t.Fatalf("the seam was handed pane %q (%s), want zeus' own peer at pane %q",
			got.Pane, core.DisplayName(got), zeus.Pane)
	}
	for _, c := range *calls {
		for pane, who := range map[string]string{
			"%5": "a tmux teammate — it has no resolvable session id and cannot be brought back",
			"%2": "another cell claude that is not zeus",
			"%3": "a codex pane — a different credential entirely",
			"%4": "a shell pane",
			"%9": "another cell's pane",
		} {
			if c.peer.Pane == pane {
				t.Errorf("the seam was handed pane %s: %s", pane, who)
			}
		}
	}
}

// TestContractResumeLogicResumedIsOneOrZero pins the count's new range and
// what decides it: "resumed ∈ {0, 1}: 1 exactly when the seam call returned
// nil", with a seam or guard failure counted out rather than raised. The count
// is what limitRescueApply reads to choose between resolving the hit and
// spending another round, so a failure counted IN resolves a hit whose cell
// never came back.
func TestContractResumeLogicResumedIsOneOrZero(t *testing.T) {
	t.Run("the seam succeeded", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		j, all := contractResumeLogicCell(t, "resume-count-ok")
		contractResumeLogicStageCellDir(t, j)
		contractResumeLogicStubRespawn(t)

		resumed, err := resumeCellAgents(j, all, limitRescueResumePrompt)
		if resumed != 1 || err != nil {
			t.Fatalf("= (%d, %v), want (1, nil)", resumed, err)
		}
	})

	t.Run("the seam refused", func(t *testing.T) {
		t.Setenv("HOME", t.TempDir())
		j, all := contractResumeLogicCell(t, "resume-count-fail")
		contractResumeLogicStageCellDir(t, j)
		calls := contractResumeLogicStubRespawn(t, "%1") // zeus' own pane refuses

		resumed, err := resumeCellAgents(j, all, limitRescueResumePrompt)
		if err != nil {
			t.Fatalf("err = %v, want nil — a seam failure is counted out, never raised", err)
		}
		if resumed != 0 {
			t.Fatalf("resumed = %d, want 0", resumed)
		}
		if len(*calls) != 1 {
			t.Errorf("respawnPeerPane called %d times, want 1 — one refusal must not be retried inside the round", len(*calls))
		}
	})
}

// TestContractResumeLogicErrsOnlyWithNoZeusPane: "err non-nil ONLY when the
// cell has no zeus pane to target". That is the `no-agents` outcome's trigger,
// and it has to stay distinguishable from a refused respawn — one means "try
// again next tick", the other means the same thing but for a different reason
// the operator log has to be able to tell apart.
func TestContractResumeLogicErrsOnlyWithNoZeusPane(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	// each case below supplies its own peer list, so the fixture's is unused
	j, _ := contractResumeLogicCell(t, "resume-no-zeus")
	contractResumeLogicStageCellDir(t, j)
	sess := CellSession(j)

	cases := map[string][]*core.Peer{
		"no peers at all": nil,
		"zeus with no pane (in-process)": {
			{TmuxSession: sess, Agent: "claude", Role: "main", Depth: 0, Pane: "", Name: ZeusName(j), SessionID: "zeus-sess"},
		},
		"only another cell's peers": {
			{TmuxSession: "cell-other-3", Agent: "claude", Role: "main", Depth: 0, Pane: "%11", Name: ZeusName(j), SessionID: "z"},
		},
	}
	for name, peers := range cases {
		t.Run(name, func(t *testing.T) {
			calls := contractResumeLogicStubRespawn(t)
			resumed, err := resumeCellAgents(j, peers, limitRescueResumePrompt)
			if err == nil {
				t.Fatalf("resumeCellAgents = (%d, nil), want an error — there is no zeus pane to target", resumed)
			}
			if resumed != 0 {
				t.Errorf("resumed = %d, want 0", resumed)
			}
			if len(*calls) != 0 {
				t.Errorf("respawnPeerPane was called %d times with no zeus pane in the cell", len(*calls))
			}
		})
	}

}

// TestContractResumeLogicRefusesACandidateThatIsNotZeusByIdentity is v2.1's
// identity requirement, and it reverses what the positional fallback alone
// would do here. `CellZeus`' fallback elects the first depth-0 claude in the
// cell's session when nothing matches by name — which is right for a READ, and
// wrong for a SIGKILL: an unregistered `k b` fork opened in the cell's session
// is exactly such a claude, and killing it destroys carl's side conversation
// with no way back. So a candidate that is neither zeus by name nor zeus by
// the job's own recorded session id is counted out — `(0, nil)`, never an
// error (there IS a pane, it is simply not zeus') and never killed.
func TestContractResumeLogicRefusesACandidateThatIsNotZeusByIdentity(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j, _ := contractResumeLogicCell(t, "resume-identity")
	contractResumeLogicStageCellDir(t, j)
	j.Session = "the-job-s-recorded-zeus-session"
	sess := CellSession(j)
	calls := contractResumeLogicStubRespawn(t)

	// a nameless fork in the cell's session, whose session id is its own
	fork := &core.Peer{
		TmuxSession: sess, Agent: "claude", Role: "main", Depth: 0,
		Pane: "%2", SessionID: "a-k-b-fork-session", CWD: "/tmp/wt", PID: 99,
	}
	if got := CellZeus(j, []*core.Peer{fork}); got != fork {
		t.Fatalf("fixture is wrong: CellZeus elected %v, want the fork — the positional fallback is what this test exists to override", got)
	}

	resumed, err := resumeCellAgents(j, []*core.Peer{fork}, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents err = %v, want nil — a candidate that is not zeus is counted out, not raised", err)
	}
	if resumed != 0 {
		t.Fatalf("resumed = %d, want 0", resumed)
	}
	if len(*calls) != 0 {
		t.Fatalf("respawnPeerPane was called %d times on a claude that is not this job's zeus — a `k b` fork would have been SIGKILLed", len(*calls))
	}
}

// TestContractResumeLogicAcceptsZeusByRecordedSessionID is the identity
// test's SECOND route, and the one that keeps v2.1 from breaking the race the
// positional fallback was built for: a zeus whose session file has not been
// written yet has no Name, but the job stamped its session id when the cell
// came up, so `j.Session` still identifies it. Without this route a rescue
// during that window would refuse the very cell it is meant to save.
func TestContractResumeLogicAcceptsZeusByRecordedSessionID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j, _ := contractResumeLogicCell(t, "resume-by-session")
	contractResumeLogicStageCellDir(t, j)
	j.Session = contractResumeLogicZeusSID
	sess := CellSession(j)
	calls := contractResumeLogicStubRespawn(t)

	// no Name at all — only the recorded session id says this is zeus
	nameless := &core.Peer{
		TmuxSession: sess, Agent: "claude", Role: "main", Depth: 0,
		Pane: "%2", SessionID: contractResumeLogicZeusSID, CWD: "/tmp/wt", PID: 77,
	}
	resumed, err := resumeCellAgents(j, []*core.Peer{nameless}, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents err = %v, want nil", err)
	}
	if resumed != 1 {
		t.Fatalf("resumed = %d, want 1 — the job's own recorded session id identifies this zeus", resumed)
	}
	if len(*calls) != 1 || (*calls)[0].peer != nameless {
		t.Fatalf("respawnPeerPane calls = %+v, want exactly the peer whose SessionID is j.Session", *calls)
	}
}

// TestContractResumeLogicRefusesACandidateWithNoCWD: "whose `CWD` is empty
// (every other zeus spawn site refuses rather than let tmux fall back to
// `$HOME`; the resume refuses too)". PeerCWD's home fallback is harmless for a
// read and not for this: the respawned zeus runs with
// --dangerously-skip-permissions, unattended, holding a conversation entirely
// about a repo — landing it in the home directory is the one outcome worse
// than not resuming at all.
func TestContractResumeLogicRefusesACandidateWithNoCWD(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j, all := contractResumeLogicCell(t, "resume-nocwd")
	contractResumeLogicStageCellDir(t, j)
	calls := contractResumeLogicStubRespawn(t)

	zeus := CellZeus(j, all)
	if zeus == nil {
		t.Fatal("fixture is wrong: no zeus elected")
	}
	zeus.CWD = ""

	resumed, err := resumeCellAgents(j, all, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents err = %v, want nil — an unusable cwd is counted out, not raised", err)
	}
	if resumed != 0 {
		t.Fatalf("resumed = %d, want 0", resumed)
	}
	if len(*calls) != 0 {
		t.Fatalf("respawnPeerPane was called %d times for a zeus with no cwd — the respawn would land in $HOME", len(*calls))
	}
}

// TestContractResumeLogicSkipsAZeusPaneThatIsALiveSideThread: a candidate
// "whose pane sits in the live side-thread set" is counted out. A thread's
// window lives in the cell's own tmux session and carl is mid-conversation
// with it; respawning it would SIGKILL that conversation to rescue a job it
// only exists to ask about.
//
// v2.1 moved this from an error to a counted-out `(0, nil)`: the cell DOES
// have a pane, it is simply not one this verb may touch, and only "no zeus
// pane at all" is an error.
func TestContractResumeLogicSkipsAZeusPaneThatIsALiveSideThread(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	st := useTestStore(t)
	row := mustCreateJob(t, st, store.Job{Slug: "resume-thread-pane", Repo: "kraken", State: "working"})
	j := &Job{ID: row.ID, Slug: row.Slug, Repo: row.Repo}
	contractResumeLogicStageCellDir(t, j)
	if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", SessionID: "thread-sess", Pane: "%1", State: "live"}); err != nil {
		t.Fatal(err)
	}
	calls := contractResumeLogicStubRespawn(t)

	all := []*core.Peer{{
		TmuxSession: CellSession(j), Agent: "claude", Role: "main", Depth: 0,
		Pane: "%1", Name: ZeusName(j), SessionID: contractResumeLogicZeusSID,
		PID: 4242, CWD: "/tmp/kraken-cell-worktree",
	}}
	resumed, err := resumeCellAgents(j, all, limitRescueResumePrompt)
	// The no-zeus ERROR, not a counted-out round: CellZeus excludes thread
	// panes upstream, so a cell whose only electee is a thread pane presents
	// no targetable zeus at all, and the contract (v2.1 join arbitration)
	// mandates that surface. What must never happen is the kill.
	if err == nil {
		t.Fatalf("resumeCellAgents = (%d, nil), want the no-zeus error — a thread-pane electee is not a targetable zeus", resumed)
	}
	if resumed != 0 {
		t.Fatalf("resumed = %d, want 0", resumed)
	}
	if len(*calls) != 0 {
		t.Fatalf("respawnPeerPane was called %d times on a live side thread's pane", len(*calls))
	}
}

// ---------------------------------------------------------------------------
// the zeus respawn command
// ---------------------------------------------------------------------------

// TestContractResumeLogicCommandCarriesTheFullSpawnSurface is the obligation
// v1's whole failure came down to: a resumed session restores NO launch flag,
// so every one the cell was spawned with has to ride on the resume command
// again. A zeus that comes back without its brief is a plain claude holding an
// implementer's history; without --model it silently leaves its tier; without
// --dangerously-skip-permissions it stops at the first tool call — which is
// the stall this whole rescue exists to end.
func TestContractResumeLogicCommandCarriesTheFullSpawnSurface(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j, all := contractResumeLogicCell(t, "resume-command")
	contractResumeLogicStageCellDir(t, j)
	calls := contractResumeLogicStubRespawn(t)

	if _, err := resumeCellAgents(j, all, limitRescueResumePrompt); err != nil {
		t.Fatalf("resumeCellAgents err = %v, want nil", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("respawnPeerPane called %d times, want 1", len(*calls))
	}
	cmd := (*calls)[0].cmd

	for _, want := range []string{
		"-r " + contractResumeLogicZeusSID, // it resumes ZEUS' session, not a new one
		"--model " + zeusModel(j),          // the job's own zeus tier
		"--dangerously-skip-permissions",   // cell agents work unattended
		`--append-system-prompt "$(cat `,   // the brief, staged rather than inlined
		zeusBriefPath(j),                   // ...at the path the spawn machinery stages to
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("resume command is missing %q:\n  %s", want, cmd)
		}
	}
	if brief, err := os.ReadFile(zeusBriefPath(j)); err != nil {
		t.Errorf("the staged brief at %s is unreadable: %v", zeusBriefPath(j), err)
	} else if len(strings.TrimSpace(string(brief))) == 0 {
		t.Error("the staged brief is empty — zeus would come back with no brief at all, which is the v1 failure wearing the fix's clothes")
	}
	if !contractResumeLogicEndsWithPrompt(t, cmd, limitRescueResumePrompt) {
		t.Errorf("the resume command's final argument is not the prompt:\n  %s", cmd)
	}
}

// TestContractResumeLogicCommandCarriesTheJobsOwnModel pins that the --model
// on the command is resolved the way a spawn resolves it, not hardcoded: a job
// that names a model gets that one, a job that names none gets the zeus
// default. Hardcoding either would move every cell of the other kind onto the
// wrong tier on every rescue, silently and only after a limit hit.
func TestContractResumeLogicCommandCarriesTheJobsOwnModel(t *testing.T) {
	for name, model := range map[string]string{
		"the job names a model": "claude-opus-5",
		"the job names none":    "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			j, all := contractResumeLogicCell(t, "resume-model-"+strings.ReplaceAll(name, " ", "-"))
			j.Model = model
			contractResumeLogicStageCellDir(t, j)
			calls := contractResumeLogicStubRespawn(t)

			if _, err := resumeCellAgents(j, all, limitRescueResumePrompt); err != nil {
				t.Fatalf("resumeCellAgents err = %v, want nil", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("respawnPeerPane called %d times, want 1", len(*calls))
			}
			if want := "--model " + zeusModel(j); !strings.Contains((*calls)[0].cmd, want) {
				t.Errorf("resume command is missing %q:\n  %s", want, (*calls)[0].cmd)
			}
		})
	}
}

// TestContractResumeLogicCommandCarriesTheCallersPrompt pins that the prompt
// the caller chose is the one zeus wakes on — the swap text and the reset text
// say materially different things about whether an account moved, and the
// wrong one tells zeus to look for a credential change that never happened.
func TestContractResumeLogicCommandCarriesTheCallersPrompt(t *testing.T) {
	for name, prompt := range map[string]string{
		"swap path":  limitRescueResumePrompt,
		"reset path": limitResetResumePrompt,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			j, all := contractResumeLogicCell(t, "resume-prompt-"+strings.ReplaceAll(name, " ", "-"))
			contractResumeLogicStageCellDir(t, j)
			calls := contractResumeLogicStubRespawn(t)

			if _, err := resumeCellAgents(j, all, prompt); err != nil {
				t.Fatalf("resumeCellAgents err = %v, want nil", err)
			}
			if len(*calls) != 1 {
				t.Fatalf("respawnPeerPane called %d times, want 1", len(*calls))
			}
			if !contractResumeLogicEndsWithPrompt(t, (*calls)[0].cmd, prompt) {
				t.Errorf("the resume command does not end with the caller's prompt:\n  %s", (*calls)[0].cmd)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// the two resume prompts
// ---------------------------------------------------------------------------

// TestContractResumeLogicPromptsCarryTheRequiredClauses pins v2's five
// content clauses. The prompt is not a courtesy line any more: it is the whole
// briefing a just-SIGKILLed zeus wakes up on, and clause 4 in particular is
// load-bearing — v2 deliberately resumes zeus ALONE, so if the prompt does not
// tell it that its crew is still limit-stalled and that reviving them is its
// first move, the cell comes back with a live lead and a dead crew and nothing
// anywhere says so.
//
// Asserted on the clauses' own nouns rather than on any phrasing: the exact
// texts are the implementer's to write, the clauses are not.
func TestContractResumeLogicPromptsCarryTheRequiredClauses(t *testing.T) {
	prompts := map[string]string{
		"swap path":  limitRescueResumePrompt,
		"reset path": limitResetResumePrompt,
	}
	for name, text := range prompts {
		t.Run(name, func(t *testing.T) {
			if strings.TrimSpace(text) == "" {
				t.Fatal("the prompt is blank — the resumed zeus would come back with nothing said to it")
			}
			lower := strings.ToLower(text)
			// clause 1: the last turn(s) failed on a usage limit
			if !strings.Contains(lower, "usage limit") {
				t.Errorf("clause 1: the text never says the turn failed on a usage limit: %q", text)
			}
			// clause 3: zeus itself was killed and resumed, so its background
			// tasks and in-flight tool calls are gone
			if !strings.Contains(lower, "resumed") {
				t.Errorf("clause 3: the text never says zeus was killed and resumed: %q", text)
			}
			for _, noun := range []string{"background", "tool call"} {
				if !strings.Contains(lower, noun) {
					t.Errorf("clause 3: the text never mentions %q — the resumed zeus will wait forever on work that died with its process: %q", noun, text)
				}
			}
			// clause 4: the teammates were NOT resumed, and reviving or
			// replacing them is zeus' first move. TaskStop and SendMessage are
			// named by the clause because they are the mechanisms.
			if !strings.Contains(lower, "teammate") {
				t.Errorf("clause 4: the text never mentions its teammates: %q", text)
			}
			for _, tool := range []string{"TaskStop", "SendMessage"} {
				if !strings.Contains(text, tool) {
					t.Errorf("clause 4: the text never names %s — reviving or replacing the crew is the move it has to make: %q", tool, text)
				}
			}
		})
	}

	// clause 2, and the halves that must NOT be shared
	swap, reset := prompts["swap path"], prompts["reset path"]
	if swap == reset {
		t.Fatal("the swap-path and reset-path texts are identical — one of them tells zeus the wrong story about its account")
	}
	if !strings.Contains(strings.ToLower(swap), "switch") {
		t.Errorf("clause 2 (swap): the text never says the main account was switched: %q", swap)
	}
	if !strings.Contains(strings.ToLower(reset), "reset") {
		t.Errorf("clause 2 (reset): the text never says the account's own window reset: %q", reset)
	}
	if strings.Contains(strings.ToLower(reset), "switch") {
		t.Errorf("clause 2 (reset): the text claims a switch, but on this path the account never moved: %q", reset)
	}
	// clause 5
	if strings.Contains(swap, "@") {
		t.Errorf("clause 5: the swap-path text names an account (%q) — it lands in a transcript, and the identity belongs in the notification carl reads", swap)
	}
}

// ---------------------------------------------------------------------------
// rescueResume: the action, the decide chain, the apply arm (unchanged in v2)
// ---------------------------------------------------------------------------

// TestContractResumeLogicActionValue pins the renamed action's wire value. The
// action string reaches the board's blocked detail and the daemon's log lines,
// so it is read by humans as well as by the switch.
func TestContractResumeLogicActionValue(t *testing.T) {
	if got := string(rescueResume); got != "resume" {
		t.Fatalf("rescueResume = %q, want %q", got, "resume")
	}
	for name, other := range map[string]limitRescueAction{
		"rescueSkip":         rescueSkip,
		"rescueRotate":       rescueRotate,
		"rescueParkManual":   rescueParkManual,
		"rescueParkNoTarget": rescueParkNoTarget,
	} {
		if other == rescueResume {
			t.Errorf("%s == rescueResume — two chain outcomes collapsed onto one value", name)
		}
	}
}

// contractResumeLogicOpenHit is a fresh unresolved rate_limit on a live cell,
// the baseline both decide cases below start from.
func contractResumeLogicOpenHit() LimitHit {
	return LimitHit{
		Error:      "rate_limit",
		Details:    "you've hit your session limit",
		At:         contractResumeLogicNow.Add(-30 * time.Second).Unix(),
		SessionID:  contractResumeLogicZeusSID,
		Deliveries: 3,
	}
}

// TestContractResumeLogicDecideSwappedPastSettleResumes is the first of the
// two decisions the contract names: a swapped hit past the settle window
// decides rescueResume. This is the swap path's second phase — the credential
// has landed, and this tick is the one that tears zeus down and brings it back
// on it.
func TestContractResumeLogicDecideSwappedPastSettleResumes(t *testing.T) {
	hit := contractResumeLogicOpenHit()
	hit.SwappedAt = contractResumeLogicNow.Add(-limitRescueSettle).Unix()
	hit.SwappedTo = "rescued@example.com"
	hit.Attempts = 1
	hit.LastAttempt = hit.SwappedAt

	got, reason := limitRescueDecide(hit, "auto", true, true, contractResumeLogicNow)
	if got != rescueResume {
		t.Fatalf("limitRescueDecide = %q (%s), want %q", got, reason, rescueResume)
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("rescueResume carries no reason — the board's blocked detail would render nothing")
	}
	// the gate's position is unchanged: it still sits above manual mode, so a
	// swapped cell is never left un-resumed because the mode moved in between
	if got, _ := limitRescueDecide(hit, "manual", true, true, contractResumeLogicNow); got != rescueResume {
		t.Errorf("manual mode after the swap decided %q, want %q — gate 5 still outranks gate 6", got, rescueResume)
	}
	// and it still waits inside the settle window rather than resuming early
	early := hit
	early.SwappedAt = contractResumeLogicNow.Add(-limitRescueSettle + time.Second).Unix()
	if got, _ := limitRescueDecide(early, "auto", true, true, contractResumeLogicNow); got != rescueSkip {
		t.Errorf("an unsettled swap decided %q, want skip", got)
	}
}

// TestContractResumeLogicDecideResetPastSettleResumes is the second: an
// exhausted hit whose ResetAt has passed by >= limitResetSettle decides
// rescueResume (the no-swap resume). Nothing was rotated here — the window
// simply reopened — and the resume is the only thing that gets a parked cell
// moving again.
func TestContractResumeLogicDecideResetPastSettleResumes(t *testing.T) {
	reset := contractResumeLogicNow
	hit := LimitHit{
		Error:    "rate_limit",
		At:       reset.Add(-3 * time.Hour).Unix(),
		Attempts: limitRescueMaxAttempts,
		Outcome:  "parked-until-reset",
		ResetAt:  reset.Unix(),
	}

	got, reason := limitRescueDecide(hit, "auto", true, false, reset.Add(limitResetSettle))
	if got != rescueResume {
		t.Fatalf("past reset+settle: limitRescueDecide = %q (%s), want %q", got, reason, rescueResume)
	}
	if strings.TrimSpace(reason) == "" {
		t.Error("the reset re-arm's resume carries no reason")
	}
	if hit.SwappedAt != 0 {
		t.Fatal("fixture is wrong: the reset path must carry no swap watermark")
	}
	if got, _ := limitRescueDecide(hit, "auto", true, false, reset.Add(limitResetSettle-time.Second)); got != rescueSkip {
		t.Errorf("inside the reset settle: decided %q, want skip", got)
	}
}

// contractResumeLogicApplyBase is a hit mid-flight: unresolved, one rotate
// attempt spent, no swap landed yet.
func contractResumeLogicApplyBase() LimitHit {
	return LimitHit{
		Error:       "rate_limit",
		Details:     "you've hit your session limit",
		At:          contractResumeLogicNow.Add(-5 * time.Minute).Unix(),
		SessionID:   contractResumeLogicZeusSID,
		AgentID:     "agent-1",
		Deliveries:  6,
		Attempts:    1,
		LastAttempt: contractResumeLogicNow.Add(-11 * time.Minute).Unix(),
	}
}

// TestContractResumeLogicApplyResolvesRotatedAfterASwap is the resume arm's
// swap-path resolution: any outcome other than "no-agents" resolves the hit,
// and with a landed swap the persisted word is "rotated" — the string the
// board and every stored record already speak.
func TestContractResumeLogicApplyResolvesRotatedAfterASwap(t *testing.T) {
	base := contractResumeLogicApplyBase()
	base.SwappedAt = contractResumeLogicNow.Add(-limitRescueSettle).Unix()
	base.SwappedTo = "rescued@example.com"

	for _, outcome := range []string{"resumed", "partial", ""} {
		t.Run("outcome="+outcome, func(t *testing.T) {
			got := limitRescueApply(base, rescueResume, outcome, "", contractResumeLogicNow)
			if got.HandledAt != contractResumeLogicNow.Unix() {
				t.Errorf("HandledAt = %d, want %d — the hit must be resolved", got.HandledAt, contractResumeLogicNow.Unix())
			}
			if got.Outcome != "rotated" {
				t.Errorf("Outcome = %q, want %q — a swap landed on this path", got.Outcome, "rotated")
			}
			if got.SwappedAt != base.SwappedAt || got.SwappedTo != base.SwappedTo {
				t.Errorf("the resume disturbed the swap watermark: (%d, %q), want (%d, %q)",
					got.SwappedAt, got.SwappedTo, base.SwappedAt, base.SwappedTo)
			}
			if got.Attempts != base.Attempts || got.LastAttempt != base.LastAttempt {
				t.Errorf("the resolving resume moved a rotate counter: Attempts %d LastAttempt %d, want %d / %d",
					got.Attempts, got.LastAttempt, base.Attempts, base.LastAttempt)
			}
		})
	}
}

// TestContractResumeLogicApplyResolvesResumedAfterResetWithNoSwap is the other
// half of the same sentence: with SwappedAt == 0 the resolution can only be
// the reset re-arm's, and the word must say so. "rotated" here would claim a
// credential change that never happened.
func TestContractResumeLogicApplyResolvesResumedAfterResetWithNoSwap(t *testing.T) {
	base := contractResumeLogicApplyBase()
	base.Attempts = limitRescueMaxAttempts
	base.Outcome = "parked-until-reset"
	base.ResetAt = contractResumeLogicNow.Add(-time.Hour).Unix()

	for _, outcome := range []string{"resumed", "partial", ""} {
		t.Run("outcome="+outcome, func(t *testing.T) {
			got := limitRescueApply(base, rescueResume, outcome, "", contractResumeLogicNow)
			if got.HandledAt != contractResumeLogicNow.Unix() {
				t.Errorf("HandledAt = %d, want %d", got.HandledAt, contractResumeLogicNow.Unix())
			}
			if got.Outcome != "resumed-after-reset" {
				t.Errorf("Outcome = %q, want %q — no swap landed on this path", got.Outcome, "resumed-after-reset")
			}
			if got.SwappedAt != 0 || got.SwappedTo != "" {
				t.Errorf("the reset resume invented a swap watermark: (%d, %q)", got.SwappedAt, got.SwappedTo)
			}
		})
	}
}

// TestContractResumeLogicApplyNoAgentsCountsOutAndParksAtTheCap is the arm's
// other branch: "no-agents" is a round that reached nobody, so it increments
// Attempts and LastAttempt and leaves the hit OPEN until the path-relative
// cap, where it parks as "parked-no-agents". Resolving a no-agents round would
// strand a cell whose zeus pane was merely missing from one discovery snapshot.
func TestContractResumeLogicApplyNoAgentsCountsOutAndParksAtTheCap(t *testing.T) {
	t.Run("the swap path parks at the absolute cap", func(t *testing.T) {
		hit := contractResumeLogicApplyBase()
		hit.SwappedAt = contractResumeLogicNow.Add(-limitRescueSettle).Unix()
		hit.SwappedTo = "rescued@example.com"
		hit.Attempts = limitRescueMaxAttempts - 2

		open := limitRescueApply(hit, rescueResume, "no-agents", "", contractResumeLogicNow)
		if open.Attempts != hit.Attempts+1 {
			t.Fatalf("Attempts = %d, want %d — a failed round must be counted", open.Attempts, hit.Attempts+1)
		}
		if open.LastAttempt != contractResumeLogicNow.Unix() {
			t.Errorf("LastAttempt = %d, want %d — every round stamps its attempt", open.LastAttempt, contractResumeLogicNow.Unix())
		}
		if open.HandledAt != 0 || open.Outcome != "" {
			t.Fatalf("a round below the cap resolved: (HandledAt %d, Outcome %q)", open.HandledAt, open.Outcome)
		}

		parked := limitRescueApply(open, rescueResume, "no-agents", "", contractResumeLogicNow)
		if parked.Attempts != limitRescueMaxAttempts {
			t.Fatalf("the parking round recorded Attempts=%d, want exactly %d", parked.Attempts, limitRescueMaxAttempts)
		}
		if parked.HandledAt != contractResumeLogicNow.Unix() || parked.Outcome != "parked-no-agents" {
			t.Fatalf("= (HandledAt %d, Outcome %q), want (%d, %q)",
				parked.HandledAt, parked.Outcome, contractResumeLogicNow.Unix(), "parked-no-agents")
		}
		if parked.SwappedAt != hit.SwappedAt || parked.SwappedTo != hit.SwappedTo {
			t.Errorf("watermark = (%d, %q), want it unchanged", parked.SwappedAt, parked.SwappedTo)
		}
	})

	t.Run("the reset path gets its own allowance past the cap", func(t *testing.T) {
		hit := LimitHit{
			Error:    "rate_limit",
			At:       contractResumeLogicNow.Add(-4 * time.Hour).Unix(),
			Attempts: limitRescueMaxAttempts,
			Outcome:  "parked-until-reset",
			ResetAt:  contractResumeLogicNow.Add(-time.Hour).Unix(),
		}
		for i := 0; i < 2; i++ {
			hit = limitRescueApply(hit, rescueResume, "no-agents", "", contractResumeLogicNow)
			if hit.HandledAt != 0 {
				t.Fatalf("failed reset round %d resolved the hit: %+v", i+1, hit)
			}
		}
		hit = limitRescueApply(hit, rescueResume, "no-agents", "", contractResumeLogicNow)
		if hit.HandledAt == 0 || hit.Outcome != "parked-no-agents" {
			t.Fatalf("after three failed reset rounds = (HandledAt %d, Outcome %q), want parked-no-agents",
				hit.HandledAt, hit.Outcome)
		}
	})
}

// TestContractResumeLogicApplyIsPure: the resume arm takes a value and returns
// one. Same inputs, same output; the argument is never reached through.
func TestContractResumeLogicApplyIsPure(t *testing.T) {
	for _, outcome := range []string{"resumed", "no-agents"} {
		t.Run("outcome="+outcome, func(t *testing.T) {
			hit := contractResumeLogicApplyBase()
			first := limitRescueApply(hit, rescueResume, outcome, "", contractResumeLogicNow)
			if !reflect.DeepEqual(hit, contractResumeLogicApplyBase()) {
				t.Fatalf("the argument was mutated to %+v", hit)
			}
			for i := 0; i < 3; i++ {
				again := limitRescueApply(hit, rescueResume, outcome, "", contractResumeLogicNow)
				if !reflect.DeepEqual(again, first) {
					t.Fatalf("call %d = %+v, want the stable %+v", i, again, first)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// selectRescueTarget's mainEmail source and DUAL exclusion
// ---------------------------------------------------------------------------
//
// selectRescueTarget is exercised through rescueLimitedCell, which is where it
// runs and the only place its inputs are observable: the selectTarget seam
// sees the mainEmail it chose and the pool it built.

// contractResumeLogicPoolSeen runs one rescueLimitedCell against the wired
// snapshot and returns what selectTarget was handed.
func contractResumeLogicPoolSeen(t *testing.T, slug string) (pool []string, mainEmail string) {
	t.Helper()
	var gotPool []string
	var gotMain string
	selectTarget = func(accts []core.AccountUsage, m string, _ int64) (string, string) {
		gotMain = m
		for _, a := range accts {
			gotPool = append(gotPool, a.Slug)
		}
		return "slot-c", ""
	}
	swapMain = func(string) (string, string, error) { return "whoever@x.com", "c@x.com", nil }

	if _, outcome, err := rescueLimitedCell(&Job{Slug: slug}, contractResumeLogicNow); err != nil || outcome != "swapped" {
		t.Fatalf("rescueLimitedCell = (%q, %v), want (\"swapped\", nil)", outcome, err)
	}
	sort.Strings(gotPool)
	return gotPool, gotMain
}

// TestContractResumeLogicTargetSelectionUsesTheLiveIdentity: mainEmail comes
// from the mainIdentityEmail seam when it returns non-empty. The snapshot is a
// poll behind by construction — the previous rescue's own swap rewrote
// ~/.claude.json and the snapshot still names the account before it — so
// selecting against the stale email aims the exclusion at the wrong row.
func TestContractResumeLogicTargetSelectionUsesTheLiveIdentity(t *testing.T) {
	stubRotation(t)
	contractResumeLogicSnapshot(t,
		core.AccountUsage{Slug: "main", Email: "stale-main@x.com"},
		core.AccountUsage{Slug: "slot-c", Email: "c@x.com"},
	)
	contractResumeLogicStubMainIdentity(t, "live-main@x.com")

	_, gotMain := contractResumeLogicPoolSeen(t, "live-identity")
	if gotMain != "live-main@x.com" {
		t.Fatalf("selectTarget mainEmail = %q, want the live identity %q, not the snapshot's %q",
			gotMain, "live-main@x.com", "stale-main@x.com")
	}
}

// TestContractResumeLogicTargetSelectionFallsBackToTheSnapshot is the other
// branch: the snapshot main row's email is the fallback when the seam returns
// "". core.SelectRotationTarget fails closed on an empty mainEmail, so the
// fallback is the difference between a stale answer and no rescue at all.
func TestContractResumeLogicTargetSelectionFallsBackToTheSnapshot(t *testing.T) {
	stubRotation(t)
	contractResumeLogicSnapshot(t,
		core.AccountUsage{Slug: "main", Email: "snapshot-main@x.com"},
		core.AccountUsage{Slug: "slot-c", Email: "c@x.com"},
	)
	contractResumeLogicStubMainIdentity(t, "")

	_, gotMain := contractResumeLogicPoolSeen(t, "fallback-identity")
	if gotMain != "snapshot-main@x.com" {
		t.Fatalf("selectTarget mainEmail = %q, want the snapshot main row's %q", gotMain, "snapshot-main@x.com")
	}
}

// TestContractResumeLogicPoolExclusionIsDualAndCaseInsensitive is v2's
// amended exclusion, and the reason it is dual is a two-swap sequence: after
// A→B the live identity is B, and the pool still holds slot(A) — the save-back
// mirror of the account that just exhausted itself, carrying a stale-healthy
// usage row. Excluding only B leaves that row as the top candidate, so the
// next rescue rotates the machine straight back onto the exhausted account.
// Excluding only the snapshot's main row (A) leaves B in the pool, which is a
// self-rotation. Both matches are needed, and both fold case, because the
// pool, the snapshot and ~/.claude.json are three writers of one string.
func TestContractResumeLogicPoolExclusionIsDualAndCaseInsensitive(t *testing.T) {
	stubRotation(t)
	contractResumeLogicSnapshot(t,
		// the snapshot still calls A main — it is a poll behind the A→B swap
		core.AccountUsage{Slug: "main", Email: "exhausted-a@x.com"},
		// A's save-back mirror, spelled in another case
		core.AccountUsage{Slug: "slot-a", Email: "Exhausted-A@X.COM"},
		// the account that is main RIGHT NOW, spelled in another case
		core.AccountUsage{Slug: "slot-b", Email: "LIVE-B@X.COM"},
		// a genuine candidate, and one row with no identity at all
		core.AccountUsage{Slug: "slot-c", Email: "c@x.com"},
		core.AccountUsage{Slug: "slot-blank", Email: ""},
	)
	contractResumeLogicStubMainIdentity(t, "live-b@x.com")

	gotPool, gotMain := contractResumeLogicPoolSeen(t, "dual-exclusion")
	if !strings.EqualFold(gotMain, "live-b@x.com") {
		t.Errorf("selectTarget mainEmail = %q, want the live identity (any case)", gotMain)
	}
	want := []string{"slot-blank", "slot-c"}
	if !reflect.DeepEqual(gotPool, want) {
		t.Fatalf("selectTarget pool = %v, want %v — slot-b is main itself and slot-a mirrors the account that just exhausted; an identity-less row is swept by neither match",
			gotPool, want)
	}
}

// ---------------------------------------------------------------------------
// rescueLimitedCell's post-swap usage-pass kick (unchanged in v2)
// ---------------------------------------------------------------------------

// TestContractResumeLogicSwapKicksAUsagePassWithoutBlocking: a "swapped"
// outcome kicks one pass through the existing single-flight, and the kick must
// not block the caller. Without the kick the snapshot still names the old
// account and the NEXT rescue selects against a main that already moved; with
// a blocking kick the daemon's whole tick waits on a network round-trip inside
// the rotation lock.
func TestContractResumeLogicSwapKicksAUsagePassWithoutBlocking(t *testing.T) {
	stubRotation(t)
	contractResumeLogicSnapshot(t,
		core.AccountUsage{Slug: "main", Email: "main@x.com"},
		core.AccountUsage{Slug: "slot-b", Email: "b@x.com"},
	)
	contractResumeLogicStubMainIdentity(t, "main@x.com")

	selectTarget = func([]core.AccountUsage, string, int64) (string, string) { return "slot-b", "" }
	swapMain = func(string) (string, string, error) { return "main@x.com", "b@x.com", nil }

	kicked := make(chan struct{})
	release := make(chan struct{})
	defer close(release) // never leave the stub's goroutine parked
	var once bool
	runUsagePass = func() error {
		if !once {
			once = true
			close(kicked)
		}
		<-release // still running when rescueLimitedCell must already have returned
		return nil
	}

	done := make(chan struct{})
	var outcome string
	var err error
	go func() {
		_, outcome, err = rescueLimitedCell(&Job{Slug: "kick-pass"}, contractResumeLogicNow)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("rescueLimitedCell did not return while the usage pass was still running — the kick blocks the caller")
	}
	if err != nil || outcome != "swapped" {
		t.Fatalf("rescueLimitedCell = (%q, %v), want (\"swapped\", nil)", outcome, err)
	}

	select {
	case <-kicked:
	case <-time.After(10 * time.Second):
		t.Fatal("no usage pass was kicked after a swap — the snapshot's idea of main never catches up")
	}
}

// TestContractResumeLogicNoSwapKicksNoUsagePass: the kick belongs to the
// "swapped" outcome, so a tick that swapped nothing must not spend a pass. An
// unconditional kick would put a network fetch on every quiet rescue tick of
// every limited cell.
func TestContractResumeLogicNoSwapKicksNoUsagePass(t *testing.T) {
	stubRotation(t)
	contractResumeLogicSnapshot(t,
		core.AccountUsage{Slug: "main", Email: "main@x.com"},
		core.AccountUsage{Slug: "slot-b", Email: "b@x.com"},
	)
	contractResumeLogicStubMainIdentity(t, "main@x.com")

	kicks := 0
	runUsagePass = func() error { kicks++; return nil }
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) { return "", "nobody eligible" }
	swapMain = func(string) (string, string, error) {
		t.Error("swapMain was called with no target selected")
		return "", "", nil
	}

	_, outcome, err := rescueLimitedCell(&Job{Slug: "no-kick"}, contractResumeLogicNow)
	if err != nil || outcome != "no-target" {
		t.Fatalf("rescueLimitedCell = (%q, %v), want (\"no-target\", nil)", outcome, err)
	}
	// the kick is asynchronous, so give a stray goroutine a moment to land
	time.Sleep(200 * time.Millisecond)
	if kicks != 0 {
		t.Fatalf("runUsagePass was called %d times for a tick that swapped nothing", kicks)
	}
}
