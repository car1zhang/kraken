package jobs

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
)

// The spawn guard is a Claude Code PreToolUse hook on the Agent tool — the
// sibling of bash-lint, installed beside it — that refuses a teammate spawn
// while the lead's shell sits anywhere but the worktree root.
//
// A teammate starts in the LEAD'S SHELL'S current cwd: the Bash tool's cwd
// can persist across calls (harness-dependent — measured both ways, and the
// persisting case is the one that bites), an Agent call inherits it, and
// Claude Code binds `.claude/settings.json` by cwd. So a lead that `cd`'d into a subpackage
// for a build and then spawned a reviewer got a reviewer with NO cell hooks
// at all — no bash-lint, no recall, no limit-hook — plus that subpackage's
// own CLAUDE.md loaded on top (measured: a 402 KB one, ~150K cache-write
// tokens per reviewer), and the hooked shell shapes bash-lint blocks
// everywhere else ran unguarded in that round. The do skill has carried the
// return-to-root rule since 2026-08-01, stakes included, and leads on two
// separate nights still spawned from a subdirectory: a rule that is read
// once and applied hours later is exactly the shape a hook exists for.
//
// The hook's payload `cwd` IS the shell's persisted cwd rather than the
// process's — measured with a throwaway session: after `cd sub` in Bash,
// the Agent hook's payload carried `sub` (symlink-resolved), as did the hook
// process's own working directory — which is what makes the comparison
// meaningful and why both sides go through EvalSymlinks.
//
// Fail-open everywhere: a payload that is not an Agent spawn, a cwd that is
// not inside a git repo, a git that cannot run — all allow. A hook that
// fails a tool call by accident is a bug, not a safety net (bash-lint's
// stance, unchanged).

// spawnGuardPayload is the subset of Claude Code's PreToolUse payload the
// guard reads — parsed defensively for the same reason bashLintPayload is:
// the schema is Claude Code's, not kraken's.
type spawnGuardPayload struct {
	ToolName string `json:"tool_name"`
	CWD      string `json:"cwd"`
}

// spawnGuardToplevel resolves the git toplevel of dir. Package-level and
// swappable so tests never shell out; the default asks git, and any failure
// (not a repo, git absent) is the caller's cue to allow.
var spawnGuardToplevel = func(dir string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// spawnGuardResolve canonicalizes a path for comparison: symlinks resolved
// where the path exists (macOS aliases /tmp to /private/tmp, and the hook
// reports the resolved form), Clean alone where it does not.
func spawnGuardResolve(p string) string {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(p)
}

// SpawnGuardStdin reads one PreToolUse payload from r and returns the block
// message and whether to block. It blocks exactly when the payload is an
// Agent spawn whose cwd resolves inside a git repo but is not that repo's
// toplevel; everything else allows. "That repo" is the INNERMOST one
// (`--show-toplevel` from a nested worktree or submodule names the nested
// root), so a shell parked at a nested root passes — a latent fail-open no
// cell repo exercises today, noted rather than closed.
func SpawnGuardStdin(r io.Reader) (string, bool) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return "", false
	}
	var p spawnGuardPayload
	if json.Unmarshal(raw, &p) != nil {
		return "", false
	}
	if p.ToolName != "Agent" || strings.TrimSpace(p.CWD) == "" {
		return "", false
	}
	root, err := spawnGuardToplevel(p.CWD)
	if err != nil || strings.TrimSpace(root) == "" {
		return "", false
	}
	if spawnGuardResolve(p.CWD) == spawnGuardResolve(root) {
		return "", false
	}
	return fmt.Sprintf("spawn from the worktree root: this shell's cwd is %s but the worktree root is %s. A teammate inherits the shell's cwd, and from a subdirectory it runs with none of the cell's hooks and loads that directory's own CLAUDE.md. Run `cd %s; pwd` first, then retry the Agent call.", p.CWD, root, root), true
}

// spawnGuardCommand is the exact hook command string cell.go installs and
// hasHookCommand matches for idempotency — the single-definition rule
// bashLintCommand follows, for the same drift reason.
func spawnGuardCommand(krakenBin string) string {
	return krakenBin + " spawn-guard"
}

// spawnGuardHookEntry is the PreToolUse matcher group kraken installs into a
// cell's worktree settings beside bash-lint's.
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
