package jobs

import (
	"fmt"
	"strings"

	"kraken/internal/core"
)

// InterruptCell sends one Escape keystroke to every in-flight agent pane in a
// cell — cellAgentPanes below, which enumerates them the same way the
// working/blocked ROLL-CALL (cellActive in status.go) counts a cell as active,
// deduped by pane so one Escape reaches each foreground process tree once.
// Best-effort across panes: one refused send doesn't stop the rest, and the
// caller only hears about it if every send failed. Writes no job state — the
// daemon's own roll-call derives blocked from the idled cell on its next tick.
//
// Side threads are excluded (carl's ruling: Stop is zeus only) — see
// cellAgentPanes, which owns that exclusion for every caller: Stop means "stop
// the job", and interrupting the thread carl is mid-conversation with is never
// what he asked for.
func InterruptCell(j *Job, all []*core.Peer) error {
	targets := cellAgentPanes(j, all)
	if len(targets) == 0 {
		return fmt.Errorf("%s: no live agent panes found to interrupt", j.Slug)
	}

	var errs []string
	sent := 0
	for _, p := range targets {
		if err := core.SendKey(p, "esc"); err != nil {
			errs = append(errs, err.Error())
			continue
		}
		sent++
	}
	if sent == 0 {
		return fmt.Errorf("interrupt failed on every pane: %s", strings.Join(errs, "; "))
	}
	return nil
}

// InterruptZeus sends one Escape keystroke to zeus' pane alone — the steer
// modal's interrupt button. InterruptCell above is the board's Stop and
// reaches every agent pane in the cell, teammates included; this one leaves a
// running crew untouched and touches only the lead's pane — ending its turn
// if one is running, else cancelling whatever dialog it shows — for the case
// where the lead is the agent going wrong. Zeus is found by CellZeus, the election
// /api/steer uses, which filters a side thread's pane out — so an interrupt
// never lands on the conversation the operator is mid-way through. Like
// InterruptCell it writes no job state: the roll-call reads the idled pane
// on its next tick.
func InterruptZeus(j *Job, all []*core.Peer) error {
	zeus, err := interruptTarget(j, all)
	if err != nil {
		return err
	}
	return core.SendKey(zeus, "esc")
}

// interruptTarget is InterruptZeus' pane resolution, split out so the choice
// can be tested without a tmux server: zeus with a pane to address, else an
// error naming the job. A zeus with no pane is an in-process peer SendKey
// would refuse anyway; refusing here keeps the message about the cell.
//
// CellZeus' positional fallback elects the lowest depth-0 claude pane in the
// session when no peer carries zeus' name — which, once zeus' process has
// exited while its crew still runs, is a crew pane (hephaestus, athena, hermes). Steer
// lives with that; this verb's whole promise is "zeus only, the crew keeps
// working", so a peer that carries a name of its own that is not one of
// zeus' is refused rather than interrupted. A nameless peer stays allowed:
// a still-starting zeus has no session file and so no name yet.
func interruptTarget(j *Job, all []*core.Peer) (*core.Peer, error) {
	zeus := CellZeus(j, all)
	if zeus == nil || zeus.Pane == "" {
		return nil, fmt.Errorf("%s: zeus pane not found to interrupt", j.Slug)
	}
	// Claude peers only: discovery names a codex peer by its THREAD TITLE
	// (core/discover.go), never zeus-<slug>, so the name test would refuse
	// every codex zeus — the same reason CellHealth's codex arm skips it. A
	// codex cell spawns no claude crew panes for the fallback to mis-elect.
	if zeus.Agent != "codex" && zeus.Name != "" && !isZeusName(j, zeus.Name) {
		// isZeusName (crew.go) answers against every slug the job has held,
		// so a renamed job's zeus, still carrying its spawn-time name, passes
		return nil, fmt.Errorf("%s: zeus pane not found to interrupt (%s is a crew pane)", j.Slug, zeus.Name)
	}
	return zeus, nil
}

// cellAgentPanes enumerates the panes that hold a cell's own agents, keyed by
// pane id so each one is listed exactly once, with the value being the
// FOREGROUND peer keystrokes for that pane must be addressed to.
//
// InterruptCell is its only non-test caller. It was lifted out of that
// function when the usage-limit rescue reached for the same set, on the
// premise that "everything working on this job" meant the same thing to both
// verbs — and that premise is now known to be false in the rescue's
// direction: a resume needs a session id and a tmux teammate has none, so
// the rescue targets zeus alone (limitrescue.go). The extraction is kept
// because the enumeration is worth stating once and testing on its own, not
// because a second caller is expected.
//
// The set is: every depth-0 claude/codex peer sharing the cell's tmux session
// (a nested subagent shares its lead's pane, so the lead's pane already
// reaches the whole foreground process tree), plus zeus' own pane and any of
// its tmux-backed teammates (in-process teammates have no pane and are
// skipped) — minus the panes of j's own live side threads.
//
// The thread exclusion is the load-bearing part, and Stop is the whole
// reason for it: a thread's window lives in the same tmux session and would
// otherwise be swept up, but carl branches a thread precisely to ask ABOUT
// the job, and stopping the job is not a reason to interrupt the
// conversation he is mid-way through.
func cellAgentPanes(j *Job, all []*core.Peer) map[string]*core.Peer {
	// every session name the job has held: a renamed cell whose tmux
	// convergence has not landed is still running under an older one, and a
	// Stop that matched nothing would report "no live agent panes" for a cell
	// carl is watching work.
	sessions := cellSessionCandidates(j)
	inCell := make(map[string]bool, len(sessions))
	for _, s := range sessions {
		inCell[s] = true
	}
	// the NAME key beside the pane one, on CellZeus' reasoning: the recorded
	// panes come from the store and a row whose pane was cleared would sail
	// past a pane-only exclusion. A thread of a renamed job answers to a
	// former prefix, so all of them are checked.
	threadPrefixes := threadNamePrefixes(j)
	zeus := CellZeus(j, all)
	threads := liveThreadPanesFor(j)
	excluded := func(p *core.Peer) bool {
		return threads[p.Pane] || hasAnyPrefix(p.Name, threadPrefixes)
	}

	targets := map[string]*core.Peer{} // pane -> foreground peer to send through
	for _, p := range all {
		if !inCell[p.TmuxSession] || p.Depth != 0 || p.Pane == "" {
			continue
		}
		if p.Agent != "claude" && p.Agent != "codex" {
			continue
		}
		if excluded(p) {
			continue
		}
		targets[p.Pane] = p
	}
	if zeus != nil {
		if zeus.Pane != "" && !excluded(zeus) {
			targets[zeus.Pane] = zeus
		}
		for _, m := range zeus.Teammates {
			if m.Pane == "" || excluded(m) {
				continue // in-process teammate: no pane to reach
			}
			targets[m.Pane] = m
		}
	}
	return targets
}
