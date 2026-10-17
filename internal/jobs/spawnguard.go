package jobs

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// spawnguard.go — `kraken spawn-guard`: the PreToolUse hook on the Agent tool
// that a cell's settings.json installs beside bash-lint (spawnGuardHookEntry,
// merged by mergeHookSettings). It refuses an Agent spawn made while the
// lead's shell sits anywhere but the worktree root.
//
// Why a hook and not a brief line: the do skill has said "return the shell to
// the root before any spawn" for a long time, and leads keep spawning whole
// waves from a subpackage their last Bash call `cd`'d into. A teammate so
// spawned starts in that subdirectory, and Claude Code loads project settings
// from the directory it starts in — so that teammate runs with NONE of the
// cell's hooks (no bash-lint, no pr-hook, no limit-hook, no recall, none of
// the repo's own guards) and files its transcript under a different project
// dir. Nothing in that teammate's own run can tell it so.
//
// The mechanism rests on facts measured in a scratch project on Claude Code
// 2.1.302: the PreToolUse payload for the Agent tool carries `cwd` equal to
// the Bash tool's PERSISTED working directory at the moment of the call —
// after `cd sub && pwd` in one Bash call the next Agent payload's cwd was
// `<project>/sub`, and the hook process's own PWD was that subdirectory too —
// while CLAUDE_PROJECT_DIR in the hook's environment stayed the directory the
// session started in (resolved through symlinks, `/private/tmp/...` for a
// `/tmp/...` start), which for a cell lead is the worktree root. So the guard
// compares the two and, when they differ, blocks with the fix in the refusal.
//
// Only a NAMED spawn is judged. The same probe showed an unnamed in-process
// subagent spawned from the subdirectory still firing the session's own Bash
// hook: it shares the parent's process and settings and files its transcript
// under the parent session, so for it the subdirectory costs paths, not
// hooks, and a refusal would state something false. A teammate — an Agent
// call carrying `name`, which under the cell's `teammateMode: tmux` gets its
// own pane and its own session — is the one whose hooks are lost, and its
// payload carries that name as `tool_input.name` (measured the same way, on a
// named call from the subdirectory), which is what the guard keys on.
//
// Same posture as bash-lint: anything that is not a well-formed Agent payload
// with both sides of the comparison present ALLOWS. The hook must never fail
// a tool call on missing evidence.

// spawnGuardPayload is the subset of the PreToolUse payload the guard reads.
type spawnGuardPayload struct {
	ToolName  string `json:"tool_name"`
	CWD       string `json:"cwd"`
	ToolInput struct {
		Name string `json:"name"`
	} `json:"tool_input"`
}

// spawnGuardMaxPayload bounds the stdin read; a PreToolUse payload is a few
// hundred bytes, and a hook should never buffer without a ceiling.
const spawnGuardMaxPayload = 1 << 20

// SpawnGuardStdin reads one PreToolUse payload from r and returns the block
// message and whether to block. It blocks only for a NAMED `Agent` payload
// (`tool_input.name` non-blank) whose `cwd` names a different directory from
// projectDir; an unnamed spawn, an empty projectDir, a missing or empty cwd,
// any other tool, or any malformed input allows.
func SpawnGuardStdin(r io.Reader, projectDir string) (msg string, block bool) {
	defer func() {
		if recover() != nil {
			msg, block = "", false
		}
	}()
	if r == nil {
		return "", false
	}
	raw, err := io.ReadAll(io.LimitReader(r, spawnGuardMaxPayload+1))
	if err != nil || len(raw) > spawnGuardMaxPayload {
		return "", false
	}
	var p spawnGuardPayload
	if json.Unmarshal(raw, &p) != nil {
		return "", false
	}
	if p.ToolName != "Agent" || projectDir == "" || p.CWD == "" || strings.TrimSpace(p.ToolInput.Name) == "" {
		return "", false
	}
	if spawnGuardSameDir(p.CWD, projectDir) {
		return "", false
	}
	return spawnGuardMessage(p.CWD, projectDir), true
}

// spawnGuardMessage is the refusal the model reads. It names where the shell
// is, where it should be, what a spawn from here loses, and the one command
// that fixes it — the cd's argument single-quoted, so a root with a space or
// a glob character still runs as pasted (a root containing a single quote
// would not; cell roots are slugified under the home dir, so none does). Both
// paths are rendered as passed, so the agent sees the strings it is actually
// working with.
func spawnGuardMessage(cwd, root string) string {
	return "kraken spawn-guard: your shell is in " + cwd + ", not the worktree root " + root +
		" — a teammate spawned from here starts there and loads NONE of the cell's hooks (no bash-lint, no pr-hook, no limit-hook, no recall; none of the repo's own guards either) and files its transcript under another project dir. Run cd '" + root + "' as its own Bash call, then spawn."
}

// spawnGuardSameDir reports whether a and b name the same filesystem object.
// Equal cleaned strings are equal (so two paths that do not exist compare as
// strings); otherwise, when both stat, identity is os.SameFile — which covers
// a symlink and its target, macOS's /tmp and /private/tmp, a case-variant
// spelling on a case-insensitive volume, and a relative path against the
// absolute path of the same directory — and when either stat fails the two
// are different.
func spawnGuardSameDir(a, b string) bool {
	ca, cb := filepath.Clean(a), filepath.Clean(b)
	if ca == cb {
		return true
	}
	ia, errA := os.Stat(ca)
	ib, errB := os.Stat(cb)
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(ia, ib)
}

// spawnGuardCommand is the exact hook command string cell.go installs and
// hasHookCommand matches for idempotency — the single-definition rule the
// rest of the hook family follows. No flags and no slug: the guard reads
// nothing but its stdin and CLAUDE_PROJECT_DIR.
func spawnGuardCommand(krakenBin string) string {
	return krakenBin + " spawn-guard"
}

// spawnGuardHookEntry is the PreToolUse matcher group kraken installs into a
// cell's worktree settings: bashLintHookEntry's shape with the Agent matcher.
func spawnGuardHookEntry(krakenBin string) map[string]any {
	return map[string]any{
		"matcher": "Agent",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": spawnGuardCommand(krakenBin),
			},
		},
	}
}
