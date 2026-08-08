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
	sess := CellSession(j)
	zeus := CellZeus(j, all)
	threads := liveThreadPanesFor(j)

	targets := map[string]*core.Peer{} // pane -> foreground peer to send through
	for _, p := range all {
		if p.TmuxSession != sess || p.Depth != 0 || p.Pane == "" {
			continue
		}
		if p.Agent != "claude" && p.Agent != "codex" {
			continue
		}
		if threads[p.Pane] {
			continue
		}
		targets[p.Pane] = p
	}
	if zeus != nil {
		if zeus.Pane != "" && !threads[zeus.Pane] {
			targets[zeus.Pane] = zeus
		}
		for _, m := range zeus.Teammates {
			if m.Pane == "" || threads[m.Pane] {
				continue // in-process teammate: no pane to reach
			}
			targets[m.Pane] = m
		}
	}
	return targets
}
