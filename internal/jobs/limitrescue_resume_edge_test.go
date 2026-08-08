package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"kraken/internal/core"
	"kraken/internal/store"
)

// limitrescue_resume_edge_test.go — the EDGE dimension of the rescue's
// teardown+resume integration (artifacts/50-contract.md v2, package `jobs`):
// resumeCellAgents' zeus-only targeting and its refusals, the respawn
// command's launch surface, the resume prompts' required clauses, the dual
// pool exclusion, and the post-swap usage-pass kick's refusals and its
// non-blocking obligation.
//
// v2 adaptation (file renamed from limitresume_edge_test.go per the package's
// limitrescue_<aspect>_test.go convention). Deleted with v1: every assertion
// that resumeCellAgents targets all of cellAgentPanes or counts more than one
// target (zeus only now), and every pin on the v1 prompt constants' names or
// texts (renamed and rewritten). New: the zeus-only refusal edges, the full
// command's contents, the prompts' required-content clauses, and the
// stale-save-back-slot half of the exclusion.
//
// v2.1 adaptation. Zeus is now identified rather than merely elected —
// CellZeus' positional fallback can return an unregistered `k b` fork living
// in the cell's session, and under a kill-based verb electing one destroys
// carl's own conversation — so the candidate must be zeus by name or by the
// session id the job file records. The v2.1 line between "error" and "counted
// out" also moved: ONLY a cell with no zeus pane raises, while a failed
// identity test, an empty cwd, a side-thread pane and an UNREADABLE
// side-thread set are all (0, nil) with nothing killed. That last one is the
// fail-open-to-fail-closed flip: an exclusion set that fails open costs a
// SIGKILL into a thread here, not the recoverable stray keystroke it costs
// Stop.
//
// Every package-level identifier carries the `edgeResume` prefix: the plain
// `edge` prefix is already spoken for by limitrescue_edge_test.go, which
// shares this declaration space.
//
// Carried forward and deliberately NOT re-authored here: limitRescueApply's
// rescueResume arm and limitRescueDecide's gate chain, both unchanged in v2
// and already pinned by limitrescue_edge_test.go, limitrescue_contract_test.go
// and limitrescue_reset_test.go. What IS asserted is the one thing a rename
// cannot carry: the action's own string value.
//
// No test here performs a credential swap against a real keychain or drives
// real tmux — every route to either is stubbed through the package seams.

// edgeResumeNow is a fixed whole-second instant, for the same reason
// limitrescue_edge_test.go's edgeNow is: the record stores unix SECONDS, so a
// now carrying nanoseconds would put every window boundary a fraction off.
var edgeResumeNow = time.Unix(1_800_000_000, 0)

// edgeResumeCall is one observed invocation of the respawnPeerPane seam. The
// second field is the FULL command under v2, not a prompt.
type edgeResumeCall struct {
	pane string
	cmd  string
}

// edgeResumeStubRespawn swaps the jobs-side seam for a recorder returning
// seamErr, restoring the production value (core.RespawnPeerPane) on cleanup.
func edgeResumeStubRespawn(t *testing.T, seamErr error) *[]edgeResumeCall {
	t.Helper()
	orig := respawnPeerPane
	calls := []edgeResumeCall{}
	respawnPeerPane = func(p *core.Peer, cmd string) error {
		calls = append(calls, edgeResumeCall{pane: p.Pane, cmd: cmd})
		return seamErr
	}
	t.Cleanup(func() { respawnPeerPane = orig })
	return &calls
}

// edgeResumeCell wires the environment a resume needs: an isolated HOME with a
// store, a real job row (the id is what cellName/CellSession are built from),
// and the cell directory the spawn machinery stages its brief and prompt into.
// Without that directory the command cannot be built at all — which is its own
// test below, not a condition the others should trip over by accident.
func edgeResumeCell(t *testing.T, slug, model string) (*Job, *store.Store) {
	t.Helper()
	st := useTestStore(t)
	row := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "monolith"})
	j := &Job{Slug: row.Slug, ID: row.ID, Repo: "monolith", Model: model}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	return j, st
}

// edgeResumeZeus builds the peer CellZeus elects and v2.1's identity test
// accepts: the cell's own tmux session, depth 0, zeus' name (the identity
// route that does not depend on the job file), and a non-empty cwd, which
// v2.1 also requires — every other zeus spawn site refuses an empty one
// rather than let tmux fall back to $HOME, and the resume refuses too.
func edgeResumeZeus(j *Job, pane string) *core.Peer {
	return &core.Peer{
		TmuxSession: CellSession(j),
		Pane:        pane,
		PID:         5150,
		Agent:       "claude",
		Depth:       0,
		Role:        "main",
		Name:        ZeusName(j),
		SessionID:   "zeus-session-" + pane,
		CWD:         cellDir(j),
	}
}

// edgeResumeCrewPeer is a non-zeus depth-0 agent pane in the same cell: a
// teammate, or a codex. Neither may ever be handed to the seam.
func edgeResumeCrewPeer(j *Job, pane, agent, name string) *core.Peer {
	return &core.Peer{
		TmuxSession: CellSession(j),
		Pane:        pane,
		Agent:       agent,
		Depth:       0,
		Role:        "main",
		Name:        name,
		SessionID:   "peer-session-" + pane,
		CWD:         cellDir(j),
	}
}

// --- resumeCellAgents: the refusals (no zeus pane is the ONLY error) ---

// v2 targets exactly one pane, so "no target" means "this cell has no zeus
// pane to respawn" — and under v2.1 that is the ONLY thing the function
// raises. The seam must be untouched in every one of these: respawn-pane -k
// SIGKILLs whatever it is aimed at, so a stray call for a cell with no
// identifiable zeus would kill a pane chosen by accident. (The refusals that
// are counted out rather than raised — identity, cwd, side-thread pane, an
// unreadable thread set — are the sibling test below.)
func TestEdgeResumeCellAgentsRefusesWhenTheCellHasNoZeusPane(t *testing.T) {
	t.Run("no peers at all", func(t *testing.T) {
		j, _ := edgeResumeCell(t, "edge-resume-nozeus", "")
		calls := edgeResumeStubRespawn(t, nil)

		n, err := resumeCellAgents(j, nil, limitRescueResumePrompt)
		if err == nil {
			t.Fatalf("resumeCellAgents(nil) = (%d, nil), want an error — nobody was resumed", n)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
		if len(*calls) != 0 {
			t.Fatalf("the seam was invoked %d time(s) with no zeus to target: %+v", len(*calls), *calls)
		}
	})

	t.Run("every peer belongs to another session", func(t *testing.T) {
		j, _ := edgeResumeCell(t, "edge-resume-elsewhere", "")
		calls := edgeResumeStubRespawn(t, nil)

		peers := []*core.Peer{
			{TmuxSession: "cell-someone-else-3", Pane: "%1", Agent: "claude", Depth: 0, Role: "main", Name: "zeus-someone-else"},
			{TmuxSession: "carl-scratch", Pane: "%2", Agent: "claude", Depth: 0, Role: "main"},
		}
		n, err := resumeCellAgents(j, peers, limitRescueResumePrompt)
		if err == nil {
			t.Fatalf("resumeCellAgents(other sessions only) = (%d, nil), want an error", n)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
		if len(*calls) != 0 {
			t.Fatalf("the seam was invoked for a pane outside this cell — that kills another job's agent: %+v", *calls)
		}
	})

	t.Run("zeus has no pane of its own", func(t *testing.T) {
		j, _ := edgeResumeCell(t, "edge-resume-panless", "")
		calls := edgeResumeStubRespawn(t, nil)

		zeus := edgeResumeZeus(j, "")
		n, err := resumeCellAgents(j, []*core.Peer{zeus}, limitRescueResumePrompt)
		if err == nil {
			t.Fatalf("resumeCellAgents(paneless zeus) = (%d, nil), want an error — there is no pane to respawn", n)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
		if len(*calls) != 0 {
			t.Fatalf("the seam was invoked for a peer with no pane: %+v", *calls)
		}
	})

}

// --- v2.1: the counted-out refusals, which are NOT errors ---

// v2.1 draws the line differently from v2: only "this cell has no zeus pane"
// raises. Every other reason to decline — a candidate that is not zeus by
// identity, one with no cwd, one sitting on a side thread's pane, and a
// side-thread set that could not be read — is counted out as `(0, nil)` and
// nothing is killed. The distinction is load-bearing at the call site: a nil
// error with resumed 0 is the `no-agents` counting path, which keeps the hit
// OPEN and retries next tick, and every case below is one where trying again
// later is exactly right.
func TestEdgeResumeCellAgentsCountsOutWithoutKilling(t *testing.T) {
	t.Run("a positional election that is not zeus by identity", func(t *testing.T) {
		j, _ := edgeResumeCell(t, "edge-resume-not-zeus", "")
		calls := edgeResumeStubRespawn(t, nil)

		// the shape CellZeus' positional fallback elects when zeus itself
		// carries no name: an unregistered `k b` fork living in the cell's own
		// tmux session, with a real session id of its own. It is carl's
		// conversation, and under a kill-based verb electing it destroys that
		// conversation and respawns it wearing zeus' brief.
		fork := &core.Peer{
			TmuxSession: CellSession(j),
			Pane:        "%1",
			PID:         6001,
			Agent:       "claude",
			Depth:       0,
			Role:        "main",
			SessionID:   "fork-session-1",
			CWD:         cellDir(j),
		}
		if j.Session != "" {
			t.Fatalf("fixture: the job records a zeus session %q, which would let the identity test pass by the other route", j.Session)
		}

		n, err := resumeCellAgents(j, []*core.Peer{fork}, limitRescueResumePrompt)
		if err != nil {
			t.Fatalf("resumeCellAgents = (%d, %v), want a nil error — a mis-election is counted out, not raised", n, err)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
		if len(*calls) != 0 {
			t.Fatalf("the seam was invoked for a peer that is not zeus — that SIGKILLs carl's own fork: %+v", *calls)
		}
	})

	t.Run("the recorded zeus session id is the other identity route", func(t *testing.T) {
		j, _ := edgeResumeCell(t, "edge-resume-by-session", "")
		calls := edgeResumeStubRespawn(t, nil)

		// nameless (its session file has not named it yet, or could not be
		// read this tick) but carrying the very session id the job file
		// recorded for its zeus — which is identity enough
		nameless := edgeResumeZeus(j, "%1")
		nameless.Name = ""
		j.Session = nameless.SessionID

		n, err := resumeCellAgents(j, []*core.Peer{nameless}, limitRescueResumePrompt)
		if err != nil || n != 1 {
			t.Fatalf("resumeCellAgents = (%d, %v), want (1, nil) — the job's own recorded session id identifies zeus", n, err)
		}
		if len(*calls) != 1 || (*calls)[0].pane != nameless.Pane {
			t.Fatalf("seam calls = %+v, want exactly one for pane %s", *calls, nameless.Pane)
		}
	})

	t.Run("a candidate with no cwd", func(t *testing.T) {
		j, _ := edgeResumeCell(t, "edge-resume-nocwd", "")
		calls := edgeResumeStubRespawn(t, nil)

		zeus := edgeResumeZeus(j, "%2")
		zeus.CWD = ""

		n, err := resumeCellAgents(j, []*core.Peer{zeus}, limitRescueResumePrompt)
		if err != nil {
			t.Fatalf("resumeCellAgents = (%d, %v), want a nil error", n, err)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0 — respawning with no cwd lets tmux fall back to $HOME, outside the worktree entirely", n)
		}
		if len(*calls) != 0 {
			t.Fatalf("the seam was invoked for a cwd-less peer: %+v", *calls)
		}
	})

	t.Run("zeus' pane is a live side thread's pane", func(t *testing.T) {
		j, st := edgeResumeCell(t, "edge-resume-threadpane", "")
		calls := edgeResumeStubRespawn(t, nil)

		zeus := edgeResumeZeus(j, "%4")
		// carl branched a side thread into that very pane: a thread is the one
		// conversation the rescue must never touch, and killing it would
		// destroy the exchange he is mid-way through
		if err := st.AddThread(store.Thread{JobID: j.ID, TID: "t1", SessionID: "thread-sess", Pane: zeus.Pane, State: "live"}); err != nil {
			t.Fatal(err)
		}

		n, err := resumeCellAgents(j, []*core.Peer{zeus}, limitRescueResumePrompt)
		// The no-zeus ERROR is the mandated surface here (v2.1 join
		// arbitration): CellZeus excludes thread panes upstream, so this cell
		// presents no targetable zeus. The load-bearing assertions are the
		// two below — nothing killed, nothing counted as resumed.
		if err == nil {
			t.Fatalf("resumeCellAgents = (%d, nil), want the no-zeus error — a thread-pane electee is not a targetable zeus", n)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
		if len(*calls) != 0 {
			t.Fatalf("the seam was invoked for a live side thread's pane: %+v", *calls)
		}
	})

	t.Run("the side-thread set cannot be read", func(t *testing.T) {
		j, st := edgeResumeCell(t, "edge-resume-threadset-unreadable", "")
		calls := edgeResumeStubRespawn(t, nil)

		zeus := edgeResumeZeus(j, "%2")
		// the store is wired but answers with an error from here on, so the
		// exclusion set is UNKNOWN rather than empty. Failing open here would
		// read "no threads" and kill whatever is in the pane; a skipped round
		// is retried next tick, a SIGKILL into carl's thread is not.
		if err := st.Close(); err != nil {
			t.Fatal(err)
		}

		n, err := resumeCellAgents(j, []*core.Peer{zeus}, limitRescueResumePrompt)
		if err != nil {
			t.Fatalf("resumeCellAgents = (%d, %v), want a nil error — an unreadable set is counted out", n, err)
		}
		if n != 0 {
			t.Errorf("resumed = %d, want 0", n)
		}
		if len(*calls) != 0 {
			t.Fatalf("the seam was invoked while the side-thread set was unknown — the exclusion failed OPEN into a SIGKILL: %+v", *calls)
		}
	})
}

// Zeus only, and zeus by NAME rather than by pane order: the cell's other
// agent panes — a tmux teammate, a codex, a shell — are never handed to the
// seam. Under v1 they were, which is what the amendment removed: a teammate
// has no resolvable session id, so respawning its pane could only ever
// produce a pane that is not the agent that was killed.
func TestEdgeResumeCellAgentsTargetsZeusAloneAndByName(t *testing.T) {
	j, _ := edgeResumeCell(t, "edge-resume-zeus-only", "")
	calls := edgeResumeStubRespawn(t, nil)

	// zeus deliberately does NOT hold the lowest pane number, so a target
	// picked by pane order rather than by name lands on the teammate
	zeus := edgeResumeZeus(j, "%5")
	teammate := edgeResumeCrewPeer(j, "%1", "claude", "athena-edge")
	codex := edgeResumeCrewPeer(j, "%2", "codex", "codex-hand")
	shell := &core.Peer{TmuxSession: CellSession(j), Pane: "%3", Role: "shell"}
	nested := &core.Peer{TmuxSession: CellSession(j), Pane: "%6", Agent: "claude", Depth: 1, Role: "main"}

	resumed, err := resumeCellAgents(j, []*core.Peer{teammate, codex, shell, nested, zeus}, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents = (%d, %v), want nil", resumed, err)
	}
	if resumed != 1 {
		t.Fatalf("resumed = %d, want exactly 1 — v2 resumes zeus and nobody else", resumed)
	}
	if len(*calls) != 1 {
		t.Fatalf("seam calls = %+v, want exactly one (zeus' pane %s)", *calls, zeus.Pane)
	}
	if got := (*calls)[0].pane; got != zeus.Pane {
		t.Fatalf("the seam was invoked for pane %s, want zeus' own pane %s — a teammate, codex or shell pane was killed instead", got, zeus.Pane)
	}
}

// A refused or failed respawn is counted out, not raised: `resumed` is 0 and
// the error stays nil, because a nil error here means "there was somebody to
// try" and that is what routes the round to the `no-agents` counting path
// rather than to the failure arm. Raising would look identical to "this cell
// has no zeus", which parks the hit on entirely different evidence.
func TestEdgeResumeCellAgentsCountsASeamFailureOutInsteadOfRaising(t *testing.T) {
	j, _ := edgeResumeCell(t, "edge-resume-seam-fails", "")
	calls := edgeResumeStubRespawn(t, errors.New("pane %5 no longer holds a claude"))

	zeus := edgeResumeZeus(j, "%5")
	resumed, err := resumeCellAgents(j, []*core.Peer{zeus}, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents = (%d, %v), want a nil error — there WAS a zeus pane to target", resumed, err)
	}
	if resumed != 0 {
		t.Fatalf("resumed = %d, want 0 — the seam refused", resumed)
	}
	if len(*calls) != 1 {
		t.Fatalf("the seam was invoked %d times, want exactly once — a retry inside one round would kill the pane twice", len(*calls))
	}
}

// --- the respawn command's launch surface ---

// The v1 defect, as a test. A bare `-r` resume does NOT restore
// --append-system-prompt or --model (measured; thread.go:437-440,
// cell.go:1918-1931), so zeus came back as a plain claude holding zeus'
// history: no brief, and its crew spawning on the wrong tier. Every flag below
// must therefore be ON THE COMMAND, and the brief must be really staged —
// an --append-system-prompt pointing at an empty file is the same failure
// wearing the fix's clothes.
func TestEdgeResumeCommandCarriesTheFullSpawnSurface(t *testing.T) {
	for _, model := range []string{"", "claude-opus-5"} {
		name := "job model " + model
		if model == "" {
			name = "job model unset (zeus default)"
		}
		t.Run(name, func(t *testing.T) {
			j, _ := edgeResumeCell(t, "edge-resume-cmd", model)
			calls := edgeResumeStubRespawn(t, nil)

			zeus := edgeResumeZeus(j, "%2")
			if resumed, err := resumeCellAgents(j, []*core.Peer{zeus}, limitRescueResumePrompt); err != nil || resumed != 1 {
				t.Fatalf("resumeCellAgents = (%d, %v), want (1, nil)", resumed, err)
			}
			cmd := (*calls)[0].cmd

			// ShellQuote'd, not bare: the fixture's session id carries a `%`
			// (outside isShellSafe's allowlist), so the rendered command
			// single-quotes it — and asserting the quoted form also pins that
			// the id is passed as one shell word. (Join arbitration: the
			// original bare-form assertion was a fixture artifact, not an
			// implementation defect.)
			if want := "-r " + core.ShellQuote(zeus.SessionID); !strings.Contains(cmd, want) {
				t.Errorf("cmd does not resume zeus' own session (%q):\n  %s", want, cmd)
			}
			if want := "--model " + zeusModel(j); !strings.Contains(cmd, want) {
				t.Errorf("cmd does not pin the job's zeus model (%q) — a resumed session does not restore it:\n  %s", want, cmd)
			}
			if !strings.Contains(cmd, "--dangerously-skip-permissions") {
				t.Errorf("cmd does not skip permissions — the resumed zeus stops on its first tool call:\n  %s", cmd)
			}
			if want := "--append-system-prompt " + core.ShellReadFileArg(zeusBriefPath(j)); !strings.Contains(cmd, want) {
				t.Errorf("cmd does not carry the staged brief (%q) — this is the v1 defect:\n  %s", want, cmd)
			}
			if want := core.ShellReadFileArg(spawnPromptPath(j)); !strings.HasSuffix(cmd, want) {
				t.Errorf("cmd does not END with the staged prompt (%q):\n  %s", want, cmd)
			}

			// the two staged files must really hold what the flags claim
			brief, err := os.ReadFile(zeusBriefPath(j))
			if err != nil {
				t.Fatalf("the brief the command names was never staged: %v", err)
			}
			if !strings.Contains(string(brief), zeusBrief(j)) {
				t.Errorf("the staged brief does not contain zeusBrief(j) — the resumed zeus comes back unbriefed with a flag that says otherwise")
			}
			prompt, err := os.ReadFile(spawnPromptPath(j))
			if err != nil {
				t.Fatalf("the prompt the command names was never staged: %v", err)
			}
			if !strings.Contains(string(prompt), limitRescueResumePrompt) {
				t.Errorf("the staged prompt does not contain the resume prompt handed to resumeCellAgents")
			}
		})
	}
}

// The reset path's own prompt reaches the command too: the two arms differ
// only in what zeus is told, and a resume that always staged the swap text
// would tell a cell whose account never moved that it had been switched.
func TestEdgeResumeCommandStagesTheCallersOwnPrompt(t *testing.T) {
	j, _ := edgeResumeCell(t, "edge-resume-reset-prompt", "")
	edgeResumeStubRespawn(t, nil)

	zeus := edgeResumeZeus(j, "%2")
	if resumed, err := resumeCellAgents(j, []*core.Peer{zeus}, limitResetResumePrompt); err != nil || resumed != 1 {
		t.Fatalf("resumeCellAgents = (%d, %v), want (1, nil)", resumed, err)
	}
	staged, err := os.ReadFile(spawnPromptPath(j))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(staged), limitResetResumePrompt) {
		t.Error("the reset arm's prompt was not the one staged — the crew would be told an account moved when none did")
	}
	if strings.Contains(string(staged), limitRescueResumePrompt) {
		t.Error("the swap-path prompt was staged for a reset resume")
	}
}

// A command that cannot be BUILT must not become a respawn. The staging step
// writes two files into the cell dir; if that write fails there is no brief
// and no prompt to hand the new process, and killing zeus to start a command
// whose `$(cat …)` resolves to nothing is strictly worse than skipping the
// round — the hit stays open and the next tick tries again. It is also not an
// error: `err` is non-nil ONLY when there is no zeus pane to target.
func TestEdgeResumeUnbuildableCommandIsCountedOutNotRaised(t *testing.T) {
	j, _ := edgeResumeCell(t, "edge-resume-unstageable", "")
	calls := edgeResumeStubRespawn(t, nil)

	// replace the whole cells root with a regular FILE: neither the cell dir
	// nor anything under it can be created or written now, whoever tries
	if err := os.RemoveAll(cellsRoot()); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(cellsRoot()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cellsRoot(), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}

	zeus := edgeResumeZeus(j, "%2")
	resumed, err := resumeCellAgents(j, []*core.Peer{zeus}, limitRescueResumePrompt)
	if err != nil {
		t.Fatalf("resumeCellAgents = (%d, %v), want a nil error — only a missing zeus pane raises", resumed, err)
	}
	if resumed != 0 {
		t.Fatalf("resumed = %d, want 0 — no command could be built", resumed)
	}
	if len(*calls) != 0 {
		t.Fatalf("the seam was invoked with an unbuildable command — zeus is SIGKILLed and respawned on a line that cannot work: %+v", *calls)
	}
}

// --- the renamed action ---

// The rename is not purely mechanical: the contract fixes the VALUE too
// (`limitRescueAction = "resume"`). A rename that carried the old "nudge"
// literal forward would leave the decide chain's reasons naming a mechanism
// that no longer exists, and one that collided with a sibling action's value
// would collapse two gates into one.
func TestEdgeResumeActionLiteralIsResume(t *testing.T) {
	if string(rescueResume) != "resume" {
		t.Fatalf("rescueResume = %q, want %q", string(rescueResume), "resume")
	}
	others := map[string]limitRescueAction{
		"rescueSkip":         rescueSkip,
		"rescueRotate":       rescueRotate,
		"rescueParkManual":   rescueParkManual,
		"rescueParkNoTarget": rescueParkNoTarget,
	}
	for name, a := range others {
		if a == rescueResume {
			t.Errorf("%s shares rescueResume's value %q — two gates would decide the same thing", name, string(a))
		}
	}
}

// --- the resume prompts' required clauses ---

// edgeResumeMentions is a case-insensitive substring test: the clauses below
// are content obligations, not exact texts, and a capitalised sentence start
// must not read as a missing clause.
func edgeResumeMentions(text, fragment string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(fragment))
}

// v2 rewrote both prompts because v1's were written for a live process being
// messaged, and v2's reader was just SIGKILLed. Clause (4) is the one that
// costs a cell if it goes missing: the teammates were NOT resumed, so unless
// the prompt says so, zeus carries on believing it has a crew that is in fact
// still limit-stalled or dead — the stranded-crew failure the whole rescue
// exists to end, one level down. Clause (5) is a leak check: the prompt lands
// in a transcript that is read back, shared and archived.
func TestEdgeResumePromptsCarryTheirRequiredClauses(t *testing.T) {
	if limitRescueResumePrompt == "" {
		t.Fatal("limitRescueResumePrompt is empty — the respawned zeus would come back with no instruction at all")
	}
	if limitResetResumePrompt == "" {
		t.Fatal("limitResetResumePrompt is empty")
	}
	if limitRescueResumePrompt == limitResetResumePrompt {
		t.Fatal("the swap-path and reset-path prompts are identical — one of them is telling a cell the wrong story about its account")
	}

	both := map[string]string{
		"swap path":  limitRescueResumePrompt,
		"reset path": limitResetResumePrompt,
	}
	for name, text := range both {
		t.Run(name+": clause 1, the turn failed on a usage limit", func(t *testing.T) {
			if !edgeResumeMentions(text, "usage limit") {
				t.Errorf("%s prompt does not say what happened: %q", name, text)
			}
		})
		t.Run(name+": clause 3, zeus itself was killed and resumed", func(t *testing.T) {
			if !edgeResumeMentions(text, "resum") {
				t.Errorf("%s prompt does not say the process was resumed — its background tasks and in-flight tool calls are gone and it has no other way to know: %q", name, text)
			}
		})
		t.Run(name+": clause 4, the teammates were not resumed", func(t *testing.T) {
			if !edgeResumeMentions(text, "teammate") {
				t.Errorf("%s prompt never mentions the teammates — zeus carries on believing it has a crew that is still limit-stalled or dead: %q", name, text)
			}
		})
	}

	t.Run("swap path: clause 2, the account was switched", func(t *testing.T) {
		if !edgeResumeMentions(limitRescueResumePrompt, "switch") {
			t.Errorf("the swap-path prompt does not say the account was switched: %q", limitRescueResumePrompt)
		}
	})
	t.Run("reset path: clause 2, the window reset and the account never moved", func(t *testing.T) {
		if !edgeResumeMentions(limitResetResumePrompt, "reset") {
			t.Errorf("the reset-path prompt does not say the window reset: %q", limitResetResumePrompt)
		}
		if edgeResumeMentions(limitResetResumePrompt, "switch") {
			t.Errorf("the reset-path prompt claims a switch — on this path the account never moved: %q", limitResetResumePrompt)
		}
	})
	t.Run("swap path: clause 5, no account is named", func(t *testing.T) {
		if strings.Contains(limitRescueResumePrompt, "@") {
			t.Errorf("the swap-path prompt names an account — it lands in a transcript that is read back and archived: %q", limitRescueResumePrompt)
		}
	})
}

// --- selectRescueTarget: the dual exclusion ---

func edgeResumeAccount(slug, email string) core.AccountUsage {
	return core.AccountUsage{Slug: slug, Email: email}
}

// edgeResumeSeedSnapshot writes a usage snapshot into the wired store — what
// selectRescueTarget reads before it ever reaches the selectTarget seam.
func edgeResumeSeedSnapshot(t *testing.T, st *store.Store, rows ...core.AccountUsage) {
	t.Helper()
	data, err := json.Marshal(core.UsageSnapshot{Accounts: rows})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetKV(usageSnapshotKVKey, string(data)); err != nil {
		t.Fatal(err)
	}
}

// edgeResumeStubMainIdentity swaps the jobs-side live-identity seam.
func edgeResumeStubMainIdentity(t *testing.T, email string) {
	t.Helper()
	orig := mainIdentityEmail
	mainIdentityEmail = func() string { return email }
	t.Cleanup(func() { mainIdentityEmail = orig })
}

// edgeResumeCapturePool stubs the selectTarget seam to record the pool and the
// mainEmail it is handed, and to select nothing — so no test here ever
// reaches a swap.
func edgeResumeCapturePool(t *testing.T, pool *[]string, mainEmail *string) {
	t.Helper()
	selectTarget = func(accts []core.AccountUsage, main string, _ int64) (string, string) {
		*mainEmail = main
		for _, a := range accts {
			*pool = append(*pool, a.Slug)
		}
		return "", "nobody eligible"
	}
}

func edgeResumePoolHas(pool []string, slug string) bool {
	for _, s := range pool {
		if s == slug {
			return true
		}
	}
	return false
}

// The identity half is v1's, and it holds: the live read wins, the snapshot's
// main row is the fallback, and a snapshot that cannot be read still carries
// the live email out.
func TestEdgeResumeSelectRescueTargetResolvesMainIdentity(t *testing.T) {
	t.Run("an empty live read falls back to the snapshot's main row", func(t *testing.T) {
		st := useTestStore(t)
		stubRotation(t)
		edgeResumeSeedSnapshot(t, st,
			edgeResumeAccount("main", "snapshot-main@example.com"),
			edgeResumeAccount("pool-a", "a@example.com"),
		)
		edgeResumeStubMainIdentity(t, "")

		var pool []string
		var seen string
		edgeResumeCapturePool(t, &pool, &seen)

		target, mainEmail, why := selectRescueTarget(edgeResumeNow)
		if target != "" || strings.TrimSpace(why) == "" {
			t.Fatalf("= (%q, %q), want no target and a stated reason", target, why)
		}
		if seen != "snapshot-main@example.com" || mainEmail != "snapshot-main@example.com" {
			t.Fatalf("mainEmail = %q (seam saw %q), want the snapshot main row's email", mainEmail, seen)
		}
	})

	t.Run("a live identity outranks a stale snapshot main row", func(t *testing.T) {
		st := useTestStore(t)
		stubRotation(t)
		edgeResumeSeedSnapshot(t, st,
			edgeResumeAccount("main", "stale-main@example.com"),
			edgeResumeAccount("pool-a", "a@example.com"),
		)
		edgeResumeStubMainIdentity(t, "live-main@example.com")

		var pool []string
		var seen string
		edgeResumeCapturePool(t, &pool, &seen)

		_, mainEmail, _ := selectRescueTarget(edgeResumeNow)
		if seen != "live-main@example.com" || mainEmail != "live-main@example.com" {
			t.Fatalf("mainEmail = %q (seam saw %q), want the live identity — the snapshot lags the rescue's own swap", mainEmail, seen)
		}
	})

	t.Run("no snapshot still carries the live email out", func(t *testing.T) {
		useTestStore(t)
		stubRotation(t)
		// nothing seeded: ReadUsageSnapshot has nothing to return
		edgeResumeStubMainIdentity(t, "live-main@example.com")
		selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
			t.Error("the selector was reached with no snapshot to select from")
			return "", ""
		}

		target, mainEmail, why := selectRescueTarget(edgeResumeNow)
		if target != "" {
			t.Errorf("target = %q, want empty", target)
		}
		if strings.TrimSpace(why) == "" {
			t.Error("the refusal carries no reason")
		}
		if mainEmail != "live-main@example.com" {
			t.Errorf("mainEmail = %q, want the live email — the caller's notification needs it even when the pool is unreadable", mainEmail)
		}
	})
}

// The dual exclusion, which is the review round's finding 3 as a test. After a
// swap A→B the pool still holds slot(A) — core.SwapMainTo mirrors the outgoing
// account into its own save-back slot — and the snapshot still calls A "main"
// until the next usage pass lands. Excluding only the live identity (B) leaves
// the JUST-EXHAUSTED account eligible, and its row is stale-healthy by
// construction, which is the whole premise the rescue is built on: the next
// attempt would rotate straight back onto the account that just 429'd.
func TestEdgeResumeSelectRescueTargetExcludesBothIdentities(t *testing.T) {
	st := useTestStore(t)
	stubRotation(t)
	edgeResumeSeedSnapshot(t, st,
		// the snapshot still names the OUTGOING account as main
		edgeResumeAccount("main", "Exhausted@Example.com"),
		// its save-back mirror, spelled in another case
		edgeResumeAccount("slot-exhausted", "exhausted@example.com"),
		// the account the rescue swapped ONTO, also spelled differently
		edgeResumeAccount("slot-live", "LIVE-MAIN@example.com"),
		// an unrelated pool account: the one row that must survive
		edgeResumeAccount("slot-fresh", "fresh@example.com"),
		// and a row with no identity at all, which neither match may sweep
		edgeResumeAccount("slot-nameless", ""),
	)
	edgeResumeStubMainIdentity(t, "live-main@example.com")

	var pool []string
	var seen string
	edgeResumeCapturePool(t, &pool, &seen)

	if _, mainEmail, _ := selectRescueTarget(edgeResumeNow); mainEmail != "live-main@example.com" {
		t.Fatalf("mainEmail = %q, want the live identity", mainEmail)
	}
	if seen != "live-main@example.com" {
		t.Fatalf("the selector was handed mainEmail %q", seen)
	}

	if edgeResumePoolHas(pool, "slot-live") {
		t.Errorf("pool = %v, want slot-live excluded — it mirrors the account main IS, case-folded", pool)
	}
	if edgeResumePoolHas(pool, "slot-exhausted") {
		t.Errorf("pool = %v, want slot-exhausted excluded — it mirrors the account that just hit the limit, which the snapshot still calls main", pool)
	}
	if !edgeResumePoolHas(pool, "slot-fresh") {
		t.Errorf("pool = %v, want slot-fresh present — the exclusion took an unrelated row with it", pool)
	}
	if !edgeResumePoolHas(pool, "slot-nameless") {
		t.Errorf("pool = %v, want slot-nameless present — an identity-less row is not main, it is excluded downstream on its own merits", pool)
	}
}

// --- the post-swap usage-pass kick ---

// edgeResumeSwapReady wires a store, the rotation seams and a one-pool-row
// snapshot, and points selectTarget/swapMain at a swap that succeeds. Every
// kick test starts from here and changes only runUsagePass.
func edgeResumeSwapReady(t *testing.T) {
	t.Helper()
	st := useTestStore(t)
	stubRotation(t)
	edgeResumeSeedSnapshot(t, st,
		edgeResumeAccount("main", "main@example.com"),
		edgeResumeAccount("pool-a", "a@example.com"),
	)
	edgeResumeStubMainIdentity(t, "main@example.com")
	selectTarget = func([]core.AccountUsage, string, int64) (string, string) { return "pool-a", "" }
	swapMain = func(string) (string, string, error) { return "main@example.com", "a@example.com", nil }
}

// edgeResumeWatchUsagePass swaps runUsagePass for a non-blocking recorder. The
// channel is buffered so the pass never blocks on an unread signal, which
// would make the very obligation under test (the kick must not block) depend
// on the stub.
func edgeResumeWatchUsagePass(t *testing.T) chan struct{} {
	t.Helper()
	kicked := make(chan struct{}, 1)
	runUsagePass = func() error {
		select {
		case kicked <- struct{}{}:
		default:
		}
		return nil
	}
	return kicked
}

// edgeResumeAssertNoKick gives an asynchronous kick a window to arrive and
// fails if one does. The wait is what makes the negative assertion mean
// something: the kick must not block the caller, so it may well be a goroutine
// that lands after rescueLimitedCell has returned.
func edgeResumeAssertNoKick(t *testing.T, kicked chan struct{}, outcome string) {
	t.Helper()
	select {
	case <-kicked:
		t.Fatalf("a usage pass was kicked for outcome %q — nothing was swapped, so the snapshot has nothing to catch up to", outcome)
	case <-time.After(400 * time.Millisecond):
	}
}

// The kick is the "swapped" outcome's alone. Every other outcome left the
// machine's login exactly where it was, so a pass kicked from one is a network
// fetch bought for nothing — and, on the lock-busy path, a pass fired while
// ANOTHER holder is mid-swap, which is precisely the window whose read would
// be recorded as truth.
func TestEdgeResumeUsagePassKickIsSwapOnly(t *testing.T) {
	t.Run("no target", func(t *testing.T) {
		edgeResumeSwapReady(t)
		selectTarget = func([]core.AccountUsage, string, int64) (string, string) { return "", "nobody eligible" }
		swapMain = func(string) (string, string, error) {
			t.Error("swapMain was called with no target selected")
			return "", "", nil
		}
		kicked := edgeResumeWatchUsagePass(t)

		_, outcome, err := rescueLimitedCell(&Job{Slug: "edge-kick-no-target", ID: 121}, edgeResumeNow)
		if outcome != "no-target" || err != nil {
			t.Fatalf("= (%q, %v), want (no-target, nil)", outcome, err)
		}
		edgeResumeAssertNoKick(t, kicked, "no-target")
	})

	t.Run("swap failed", func(t *testing.T) {
		edgeResumeSwapReady(t)
		swapMain = func(string) (string, string, error) { return "", "", errors.New("keychain locked") }
		kicked := edgeResumeWatchUsagePass(t)

		_, outcome, err := rescueLimitedCell(&Job{Slug: "edge-kick-swap-failed", ID: 122}, edgeResumeNow)
		if outcome != "swap-failed" {
			t.Fatalf("outcome = %q, want swap-failed", outcome)
		}
		if err == nil {
			t.Error("a failed swap returned a nil error")
		}
		edgeResumeAssertNoKick(t, kicked, "swap-failed")
	})

	t.Run("lock busy", func(t *testing.T) {
		edgeResumeSwapReady(t)
		selectTarget = func([]core.AccountUsage, string, int64) (string, string) {
			t.Error("selectTarget was reached while another holder had the lock")
			return "", "held"
		}
		swapMain = func(string) (string, string, error) {
			t.Error("swapMain was reached while another holder had the lock")
			return "", "", nil
		}
		kicked := edgeResumeWatchUsagePass(t)

		release, ok, err := acquireRotationLock()
		if err != nil || !ok {
			t.Fatalf("the test could not take the lock first: ok=%v err=%v", ok, err)
		}
		defer release()

		_, outcome, err := rescueLimitedCell(&Job{Slug: "edge-kick-lock", ID: 123}, edgeResumeNow)
		if outcome != "lock-busy" || err != nil {
			t.Fatalf("= (%q, %v), want (lock-busy, nil)", outcome, err)
		}
		edgeResumeAssertNoKick(t, kicked, "lock-busy")
	})
}

// The kick "must not block the caller": the rescue pass runs inside a daemon
// tick that has other cells to visit, and a usage pass is a network fetch
// across every pool account. A synchronous kick would hold the tick open for
// as long as the fetch takes — and if the pass ever wedges, forever.
func TestEdgeResumeUsagePassKickDoesNotBlockTheRescue(t *testing.T) {
	edgeResumeSwapReady(t)

	release := make(chan struct{})
	var once sync.Once
	stop := func() { once.Do(func() { close(release) }) }
	defer stop()

	runUsagePass = func() error {
		<-release
		return nil
	}

	type result struct {
		to      string
		outcome string
		err     error
	}
	done := make(chan result, 1)
	go func() {
		to, outcome, err := rescueLimitedCell(&Job{Slug: "edge-kick-nonblocking", ID: 124}, edgeResumeNow)
		done <- result{to, outcome, err}
	}()

	var got result
	select {
	case got = <-done:
	case <-time.After(5 * time.Second):
		stop()
		t.Fatal("rescueLimitedCell did not return while the usage pass was still running — the kick blocks the caller")
	}

	if got.err != nil {
		t.Fatalf("err = %v, want nil", got.err)
	}
	if got.outcome != "swapped" {
		t.Fatalf("outcome = %q, want swapped", got.outcome)
	}
	if got.to != "a@example.com" {
		t.Errorf("toEmail = %q, want the target's email", got.to)
	}

	// let the pass finish before the seams are restored, so the process-global
	// single-flight flag is not left set for whichever test runs next
	stop()
	for i := 0; i < 500 && usagePassRunning.Load(); i++ {
		time.Sleep(10 * time.Millisecond)
	}
}

// The kick goes through the EXISTING single-flight, so a pass already in
// flight is not joined by a second one. Two concurrent passes fetch the same
// accounts and race each other's write of the snapshot the very next rescue
// tick reads.
func TestEdgeResumeUsagePassKickRespectsTheSingleFlight(t *testing.T) {
	edgeResumeSwapReady(t)
	kicked := edgeResumeWatchUsagePass(t)

	// a pass is already running, as far as the flag is concerned
	usagePassRunning.Store(true)
	t.Cleanup(func() { usagePassRunning.Store(false) })

	_, outcome, err := rescueLimitedCell(&Job{Slug: "edge-kick-single-flight", ID: 125}, edgeResumeNow)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if outcome != "swapped" {
		t.Fatalf("outcome = %q, want swapped — the in-flight pass must not change the swap's own outcome", outcome)
	}
	select {
	case <-kicked:
		t.Fatal("a second usage pass ran while one was already in flight — the kick bypassed the single-flight")
	case <-time.After(400 * time.Millisecond):
	}
}
