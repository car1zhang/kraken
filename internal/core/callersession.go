package core

import (
	"os"
	"strings"
)

// CallerSessionID returns the session id of the nearest claude or codex agent
// process above this one, read from that process' session file
// (~/.claude/sessions/<pid>.json), and false when there is none.
//
// It exists for the agent-side `kraken mm` write verbs: a verb run from an
// agent's Bash tool is a descendant of that agent's process, so walking up the
// process tree names who is calling without the caller having to say. One `ps`
// call (psSnapshot, the table discovery already reads), inverted into a parent
// map, then a walk from os.Getpid().
//
// The NEAREST agent wins and the walk stops there: a teammate's claude is a
// separate process under its own tmux pane, but an in-process subagent's tool
// call runs under its parent's process, so "nearest" is the most specific
// answer the process tree can give. An agent with no readable session file —
// a codex process, or a claude in its first seconds — is false rather than a
// guess: the callers use this to decide who may write, and a wrong identity is
// worse than none.
func CallerSessionID() (string, bool) {
	children, cmds, _ := psSnapshot()
	if len(cmds) == 0 {
		return "", false
	}
	parent := make(map[int]int, len(cmds))
	for ppid, kids := range children {
		for _, k := range kids {
			parent[k] = ppid
		}
	}
	seen := map[int]bool{}
	for pid := os.Getpid(); pid > 1 && !seen[pid]; {
		seen[pid] = true
		ppid, ok := parent[pid]
		if !ok {
			return "", false
		}
		pid = ppid
		cmd := cmds[pid]
		if !isClaudeCmd(cmd) && !isCodexCmd(cmd) {
			continue
		}
		sf, ok := readSessionFile(pid)
		if !ok {
			return "", false
		}
		sid := strings.TrimSpace(sf.SessionID)
		return sid, sid != ""
	}
	return "", false
}
