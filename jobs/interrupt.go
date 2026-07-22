package jobs

import (
	"fmt"
	"strings"

	"kraken/core"
)

// InterruptCell sends one Escape keystroke to every in-flight agent pane in a
// cell, enumerated the same way the working/blocked ROLL-CALL (cellActive in
// status.go) counts a cell as active: zeus' own pane, any other claude/codex
// peer sharing its tmux session regardless of depth (a nested subagent shares
// its lead's pane, so one Escape already reaches the whole foreground process
// tree — panes are deduped so it's sent once each), and any live tmux-backed
// team member's own pane (in-process teammates have no pane and are skipped).
// Best-effort across panes: one refused send doesn't stop the rest, and the
// caller only hears about it if every send failed. Writes no job state — the
// daemon's own roll-call derives blocked from the idled cell on its next tick.
func InterruptCell(j *Job, all []*core.Peer) error {
	sess := CellSession(j)
	zeus := CellZeus(j, all)

	targets := map[string]*core.Peer{} // pane -> foreground peer to send the key through
	for _, p := range all {
		if p.TmuxSession != sess || p.Depth != 0 || p.Pane == "" {
			continue
		}
		if p.Agent != "claude" && p.Agent != "codex" {
			continue
		}
		targets[p.Pane] = p
	}
	if zeus != nil {
		if zeus.Pane != "" {
			targets[zeus.Pane] = zeus
		}
		for _, m := range zeus.Teammates {
			if m.Pane == "" {
				continue // in-process teammate: no pane to interrupt
			}
			targets[m.Pane] = m
		}
	}
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
