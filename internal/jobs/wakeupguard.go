package jobs

import (
	"encoding/json"
	"io"
)

// wakeupguard.go — `kraken wakeup-guard`, wired as a Claude Code PreToolUse
// hook on the ScheduleWakeup tool in every cell's worktree settings
// (cell.go's mergeHookSettings installs it beside bash-lint). do/SKILL.md
// already bans arming ScheduleWakeup as a belt-and-braces heartbeat, and the
// ban measurably loses: the wakeup's own tick payload is a generic
// autonomous-loop brief that re-argues for re-arming on every tick — telling
// the agent the loop ends unless it calls ScheduleWakeup again this turn —
// so the standing skill sentence is outshouted by an instruction that
// arrives fresh each turn. One cell lead re-armed 125 times across a night,
// twice resuming a loop it had itself just stopped, and roughly a third of
// its whole session spend went to empty noop ticks appended to a settled
// conversation. The second cost is worse than the tokens: a pending wakeup
// reads as status "polling", which cellActive counts as live work, so the
// heartbeat votes the cell "working" in the daemon's roll call while the job
// is actually parked at a manual-test gate — the board hides the very block
// it exists to surface, and carl finds out by noticing the lie. The repo's
// measured pattern is that a hook holds where prose does not (bash-lint
// catches shapes that skill prose named verbatim and agents measurably kept
// not applying), so the arm is refused mechanically.
//
// stop:true is deliberately allowed through: it ENDS a loop rather than
// arming one, and blocking it would strand a cell inside the very loop this
// hook exists to prevent (a tick can arrive on a loop armed before this hook
// was installed, and stopping it must stay possible).

// wakeupGuardPayload is the outer slice of the PreToolUse payload: the tool
// name, plus the raw input decoded in a second stage. The two-stage decode is
// what keeps the fail-open line in the right place — a payload that is not a
// ScheduleWakeup call allows however malformed it is, while a ScheduleWakeup
// call whose input cannot prove it is a stop is treated as an arm and
// blocked: the one field that flips the verdict must not be able to disarm
// the guard by being mistyped (`"stop":"true"` as a string, a non-object
// tool_input), and the blocked side is the recoverable one.
type wakeupGuardPayload struct {
	ToolName  string          `json:"tool_name"`
	ToolInput json.RawMessage `json:"tool_input"`
}

// wakeupGuardMessage reaches the model as the block reason, so it names the
// sanctioned wake sources and the one allowed call rather than just refusing.
const wakeupGuardMessage = "kraken wakeup-guard: a kraken cell never arms ScheduleWakeup — its tick delivers a generic autonomous-loop brief that contradicts the cell's stop-gates, and a self-scheduled heartbeat keeps the cell reading \"working\" on the board while the job is actually parked at a gate. Your wake sources are the armed file watch (Monitor, or Bash with run_in_background) and the daemon's own pane bumps. A blocked arm scheduled nothing, so no stop is needed; ScheduleWakeup({stop: true}) is for a loop that was already armed, and this hook allows it."

// WakeupGuardStdin reads one PreToolUse payload from r and returns the block
// message and whether to block. Fail-open covers everything that is not a
// ScheduleWakeup call — a hook that fails a foreign tool call by accident is
// a bug, not a safety net — and within ScheduleWakeup only a well-formed
// `stop: true` passes.
func WakeupGuardStdin(r io.Reader) (string, bool) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", false
	}
	var p wakeupGuardPayload
	if json.Unmarshal(raw, &p) != nil {
		return "", false
	}
	if p.ToolName != "ScheduleWakeup" {
		return "", false
	}
	// Exact-key lookup rather than a struct field: encoding/json matches
	// struct fields case-insensitively, and a case-variant key ("Stop") is an
	// unrecognised extra to the real tool — an ARM — so decoding it as a stop
	// would disarm the guard on exactly the mistyping this decode exists to
	// catch. A missing key yields a nil RawMessage, which Unmarshal errors
	// on, so absent input and absent key both stay on the blocked side.
	var input map[string]json.RawMessage
	if json.Unmarshal(p.ToolInput, &input) == nil {
		var stop bool
		if json.Unmarshal(input["stop"], &stop) == nil && stop {
			return "", false
		}
	}
	return wakeupGuardMessage, true
}

// wakeupGuardCommand is the exact hook command string cell.go installs and
// hasHookCommand matches for idempotency — the same single-definition rule
// bashLintCommand follows, for the same drift reason. Like bash-lint's, the
// command embeds the binary path, so a cell whose settings were written by a
// scratch build can carry a second, stale entry after reactivation under the
// installed binary; that duplicate is benign only because every degraded exit
// is non-blocking — a missing binary exits 127 and an older binary's unknown
// verb dies through util.Die at exit 1, neither of which is the blocking 2 —
// so a stale entry can never block anything, stop:true included.
func wakeupGuardCommand(krakenBin string) string {
	return krakenBin + " wakeup-guard"
}

// wakeupGuardHookEntry is the PreToolUse matcher group kraken installs into a
// cell's worktree settings, keyed on the ScheduleWakeup tool name.
func wakeupGuardHookEntry(krakenBin string) map[string]any {
	return map[string]any{
		"matcher": "ScheduleWakeup",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": wakeupGuardCommand(krakenBin),
			},
		},
	}
}
