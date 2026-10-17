package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// spawnguard_contract_test.go — athena-logic's BLIND contract tests for slice
// D, the spawn guard (50-contract.md § "Slice D": D1 SpawnGuardStdin, D2
// spawnGuardMessage, D3 spawnGuardSameDir, D4 spawnGuardCommand /
// spawnGuardHookEntry, D5 mergeHookSettings).
//
// Dimension: LOGIC — the stated obligations and happy paths, one answer per
// assertion. Refusals, malformed input and failure fallbacks are athena-edge's.
//
// Every package-level identifier is prefixed `sgLogic`: sibling authors share
// this package's declaration space. Every path and payload here is invented.

// sgLogicBin is an invented absolute kraken binary path.
const sgLogicBin = "/opt/invented/bin/kraken"

// sgLogicPayload builds a PreToolUse payload for toolName with the given cwd.
func sgLogicPayload(t *testing.T, toolName, cwd string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"session_id":      "sess-sg",
		"hook_event_name": "PreToolUse",
		"tool_name":       toolName,
		"cwd":             cwd,
		"tool_input":      map[string]any{"name": "hephaestus-a", "prompt": "do the slice"},
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return string(b)
}

// sgLogicGuard runs SpawnGuardStdin on a payload string.
func sgLogicGuard(payload, projectDir string) (string, bool) {
	return SpawnGuardStdin(strings.NewReader(payload), projectDir)
}

// sgLogicWantMessage spells D2's frozen line.
func sgLogicWantMessage(cwd, root string) string {
	return "kraken spawn-guard: your shell is in " + cwd + ", not the worktree root " + root +
		" — a teammate spawned from here starts there and loads NONE of the cell's hooks (no bash-lint, no pr-hook, no limit-hook, no recall; none of the repo's own guards either) and files its transcript under another project dir. Run cd '" +
		root + "' as its own Bash call, then spawn."
}

// ---------------------------------------------------------------------------
// D1 — SpawnGuardStdin
// ---------------------------------------------------------------------------

// D1.4: an Agent payload whose cwd is the project dir allows.
func TestSgLogicAgentFromRootAllows(t *testing.T) {
	root := t.TempDir()
	msg, block := sgLogicGuard(sgLogicPayload(t, "Agent", root), root)
	if block || msg != "" {
		t.Errorf("SpawnGuardStdin = (%q, %v), want (\"\", false) for cwd == projectDir", msg, block)
	}
}

// D1.4 via D3: a cwd naming the root with a trailing slash still allows.
func TestSgLogicAgentFromRootTrailingSlashAllows(t *testing.T) {
	root := t.TempDir()
	msg, block := sgLogicGuard(sgLogicPayload(t, "Agent", root+"/"), root)
	if block || msg != "" {
		t.Errorf("SpawnGuardStdin = (%q, %v), want (\"\", false) for cwd == projectDir + \"/\"", msg, block)
	}
}

// D1.4 via D3: a cwd that is a symlink to the root allows.
func TestSgLogicAgentFromSymlinkToRootAllows(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "wt-invented")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link-to-wt")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	msg, block := sgLogicGuard(sgLogicPayload(t, "Agent", link), root)
	if block || msg != "" {
		t.Errorf("SpawnGuardStdin = (%q, %v), want (\"\", false) for a cwd symlinked to the root", msg, block)
	}
}

// D1.5: an Agent payload from a subdirectory of the root blocks with exactly
// spawnGuardMessage(cwd, projectDir) — the cwd as the payload carried it.
func TestSgLogicAgentFromSubdirBlocks(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	msg, block := sgLogicGuard(sgLogicPayload(t, "Agent", sub), root)
	if !block {
		t.Fatalf("SpawnGuardStdin blocked = false, want true for cwd %q under root %q", sub, root)
	}
	if want := spawnGuardMessage(sub, root); msg != want {
		t.Errorf("msg =\n%q\nwant spawnGuardMessage(cwd, projectDir) =\n%q", msg, want)
	}
	if want := sgLogicWantMessage(sub, root); msg != want {
		t.Errorf("msg =\n%q\nwant the frozen D2 line\n%q", msg, want)
	}
}

// D1.5: a cwd that is an unrelated directory (not under the root) blocks too.
func TestSgLogicAgentFromElsewhereBlocks(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	msg, block := sgLogicGuard(sgLogicPayload(t, "Agent", other), root)
	if !block {
		t.Fatalf("SpawnGuardStdin blocked = false, want true for an unrelated cwd")
	}
	if want := spawnGuardMessage(other, root); msg != want {
		t.Errorf("msg =\n%q\nwant\n%q", msg, want)
	}
}

// D1.3b (amended): an UNNAMED Agent call (an in-process subagent) from a
// subdirectory allows — only a named spawn is judged.
func TestSgLogicUnnamedAgentFromSubdirAllows(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(map[string]any{
		"session_id": "sess-sg", "hook_event_name": "PreToolUse", "tool_name": "Agent", "cwd": sub,
		"tool_input": map[string]any{"description": "look around", "prompt": "read the package"},
	})
	if err != nil {
		t.Fatal(err)
	}
	msg, block := sgLogicGuard(string(b), root)
	if block || msg != "" {
		t.Errorf("SpawnGuardStdin = (%q, %v), want (\"\", false) for an unnamed Agent call", msg, block)
	}
}

// D1.2: a Bash payload never blocks, even from a subdirectory.
func TestSgLogicBashPayloadNeverBlocks(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	msg, block := sgLogicGuard(sgLogicPayload(t, "Bash", sub), root)
	if block || msg != "" {
		t.Errorf("SpawnGuardStdin = (%q, %v), want (\"\", false) for a Bash payload", msg, block)
	}
}

// ---------------------------------------------------------------------------
// D2 — spawnGuardMessage
// ---------------------------------------------------------------------------

// D2, plain unrelated paths: the frozen line verbatim, one line.
func TestSgLogicMessagePlainPaths(t *testing.T) {
	got := spawnGuardMessage("/invented/elsewhere", "/invented/cells/demo-1/wt-demo")
	want := "kraken spawn-guard: your shell is in /invented/elsewhere, not the worktree root /invented/cells/demo-1/wt-demo — a teammate spawned from here starts there and loads NONE of the cell's hooks (no bash-lint, no pr-hook, no limit-hook, no recall; none of the repo's own guards either) and files its transcript under another project dir. Run cd '/invented/cells/demo-1/wt-demo' as its own Bash call, then spawn."
	if got != want {
		t.Errorf("spawnGuardMessage =\n%q\nwant\n%q", got, want)
	}
	if strings.Contains(got, "\n") {
		t.Errorf("spawnGuardMessage %q contains a newline, want exactly one line", got)
	}
}

// D2, a cwd under the root: both arguments appear as passed.
func TestSgLogicMessageCwdUnderRoot(t *testing.T) {
	root := "/invented/cells/demo-1/wt-demo"
	cwd := root + "/web/_ui"
	if got, want := spawnGuardMessage(cwd, root), sgLogicWantMessage(cwd, root); got != want {
		t.Errorf("spawnGuardMessage =\n%q\nwant\n%q", got, want)
	}
}

// D2: arguments are used as passed — not cleaned, not resolved.
func TestSgLogicMessageArgumentsNotCleaned(t *testing.T) {
	cwd := "/invented/a/./b/../c/"
	root := "/invented/a//"
	got := spawnGuardMessage(cwd, root)
	if want := sgLogicWantMessage(cwd, root); got != want {
		t.Errorf("spawnGuardMessage =\n%q\nwant (arguments verbatim)\n%q", got, want)
	}
}

// ---------------------------------------------------------------------------
// D3 — spawnGuardSameDir
// ---------------------------------------------------------------------------

// Trailing slash on either side is equal.
func TestSgLogicSameDirTrailingSlash(t *testing.T) {
	d := t.TempDir()
	if !spawnGuardSameDir(d+"/", d) {
		t.Errorf("spawnGuardSameDir(%q, %q) = false, want true", d+"/", d)
	}
	if !spawnGuardSameDir(d, d+"/") {
		t.Errorf("spawnGuardSameDir(%q, %q) = false, want true", d, d+"/")
	}
}

// `.` and `..` segments clean away.
func TestSgLogicSameDirDotSegments(t *testing.T) {
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := d + "/./sub/.."
	if !spawnGuardSameDir(a, d) {
		t.Errorf("spawnGuardSameDir(%q, %q) = false, want true", a, d)
	}
}

// A symlinked directory resolves equal to its target.
func TestSgLogicSameDirSymlink(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if !spawnGuardSameDir(link, target) {
		t.Errorf("spawnGuardSameDir(%q, %q) = false, want true (symlink to target)", link, target)
	}
}

// macOS: t.TempDir sits under /var, a symlink to /private/var; the resolved
// form equals the unresolved one. Skipped where the temp dir has no symlink
// in its path (the equality is then the trailing-slash case above).
func TestSgLogicSameDirResolvedTempDir(t *testing.T) {
	d := t.TempDir()
	resolved, err := filepath.EvalSymlinks(d)
	if err != nil {
		t.Fatal(err)
	}
	if resolved == d {
		t.Skip("temp dir path carries no symlink here")
	}
	if !spawnGuardSameDir(d, resolved) {
		t.Errorf("spawnGuardSameDir(%q, %q) = false, want true", d, resolved)
	}
}

// D3 (amended, os.SameFile): a relative path equals the absolute path of the
// same directory. The relative form is computed from the test process' own
// cwd, so no chdir is needed.
func TestSgLogicSameDirRelativeEqualsAbsolute(t *testing.T) {
	d := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, d)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.IsAbs(rel) {
		t.Fatalf("filepath.Rel returned an absolute path %q", rel)
	}
	if !spawnGuardSameDir(rel, d) {
		t.Errorf("spawnGuardSameDir(%q, %q) = false, want true (same directory)", rel, d)
	}
}

// Two different existing directories are not the same.
func TestSgLogicSameDirDifferentDirs(t *testing.T) {
	d := t.TempDir()
	sub := filepath.Join(d, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if spawnGuardSameDir(sub, d) {
		t.Errorf("spawnGuardSameDir(%q, %q) = true, want false", sub, d)
	}
}

// Two non-existent paths compare by cleaned string: equal after cleaning is
// true, different after cleaning is false.
func TestSgLogicSameDirNonExistentByCleanedString(t *testing.T) {
	base := filepath.Join(t.TempDir(), "never-created")
	if !spawnGuardSameDir(base+"/x/./y/", base+"/x/z/../y") {
		t.Errorf("spawnGuardSameDir on two non-existent paths cleaning to the same string = false, want true")
	}
	if spawnGuardSameDir(base+"/x", base+"/y") {
		t.Errorf("spawnGuardSameDir on two non-existent different paths = true, want false")
	}
}

// ---------------------------------------------------------------------------
// D4 — spawnGuardCommand / spawnGuardHookEntry
// ---------------------------------------------------------------------------

func TestSgLogicCommandExact(t *testing.T) {
	if got, want := spawnGuardCommand(sgLogicBin), sgLogicBin+" spawn-guard"; got != want {
		t.Errorf("spawnGuardCommand = %q, want %q", got, want)
	}
}

// The entry's shape, compared through JSON so the concrete Go slice/map types
// the implementation picks do not matter — only what lands in settings.json.
func TestSgLogicHookEntryShape(t *testing.T) {
	got, err := json.Marshal(spawnGuardHookEntry(sgLogicBin))
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	var gotV, wantV any
	if err := json.Unmarshal(got, &gotV); err != nil {
		t.Fatal(err)
	}
	want := `{"matcher":"Agent","hooks":[{"type":"command","command":"` + sgLogicBin + ` spawn-guard"}]}`
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatal(err)
	}
	gb, _ := json.Marshal(gotV)
	wb, _ := json.Marshal(wantV)
	if string(gb) != string(wb) {
		t.Errorf("spawnGuardHookEntry = %s, want %s", gb, wb)
	}
}

// ---------------------------------------------------------------------------
// D5 — mergeHookSettings
// ---------------------------------------------------------------------------

// sgLogicPreToolUse decodes merged settings and returns hooks.PreToolUse.
func sgLogicPreToolUse(t *testing.T, out []byte) []any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("merged settings %s are not JSON: %v", out, err)
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("merged settings %s carry no hooks object", out)
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatalf("merged settings %s carry no PreToolUse array", out)
	}
	return pre
}

// sgLogicGroupCommands returns a group's matcher and its hook commands.
func sgLogicGroupCommands(g any) (string, []string) {
	m, _ := g.(map[string]any)
	matcher, _ := m["matcher"].(string)
	var cmds []string
	hs, _ := m["hooks"].([]any)
	for _, h := range hs {
		hm, _ := h.(map[string]any)
		if c, ok := hm["command"].(string); ok {
			cmds = append(cmds, c)
		}
	}
	return matcher, cmds
}

// sgLogicCountGuardGroups counts Agent groups whose single command is the
// guard's, and any group anywhere naming the guard command.
func sgLogicCountGuardGroups(pre []any) (exact, mentions int) {
	want := sgLogicBin + " spawn-guard"
	for _, g := range pre {
		matcher, cmds := sgLogicGroupCommands(g)
		for _, c := range cmds {
			if c == want {
				mentions++
			}
		}
		if matcher == "Agent" && len(cmds) == 1 && cmds[0] == want {
			exact++
		}
	}
	return exact, mentions
}

// sgLogicHasBashLint reports a Bash-matcher group running bash-lint.
func sgLogicHasBashLint(pre []any) bool {
	for _, g := range pre {
		matcher, cmds := sgLogicGroupCommands(g)
		for _, c := range cmds {
			if matcher == "Bash" && c == sgLogicBin+" bash-lint" {
				return true
			}
		}
	}
	return false
}

// D5: a fresh merge holds the Agent group with the guard command exactly once,
// beside the bash-lint Bash group.
func TestSgLogicMergeInstallsGuardOnce(t *testing.T) {
	out, err := mergeHookSettings(nil, "some-slug", sgLogicBin)
	if err != nil {
		t.Fatal(err)
	}
	pre := sgLogicPreToolUse(t, out)
	if exact, mentions := sgLogicCountGuardGroups(pre); exact != 1 || mentions != 1 {
		t.Errorf("Agent guard groups = %d (command mentions %d), want exactly 1: %s", exact, mentions, out)
	}
	if !sgLogicHasBashLint(pre) {
		t.Errorf("bash-lint Bash group missing after merge: %s", out)
	}
}

// D5: merging the merged output again still holds exactly one guard group,
// and bash-lint is still there.
func TestSgLogicMergeTwiceIsIdempotent(t *testing.T) {
	out, err := mergeHookSettings(nil, "some-slug", sgLogicBin)
	if err != nil {
		t.Fatal(err)
	}
	again, err := mergeHookSettings(out, "some-slug", sgLogicBin)
	if err != nil {
		t.Fatal(err)
	}
	pre := sgLogicPreToolUse(t, again)
	if exact, mentions := sgLogicCountGuardGroups(pre); exact != 1 || mentions != 1 {
		t.Errorf("after two merges, Agent guard groups = %d (mentions %d), want exactly 1: %s", exact, mentions, again)
	}
	if !sgLogicHasBashLint(pre) {
		t.Errorf("bash-lint Bash group missing after two merges: %s", again)
	}
}

// D5: a pre-existing PreToolUse group with another matcher and command
// survives IN ITS PLACE (still first), other top-level keys are untouched,
// and the guard group is added. Assumption: "in its place" means its index in
// the array is unchanged — the merge appends, so a group at index 0 stays at 0.
func TestSgLogicMergeKeepsExistingGroupsAndKeys(t *testing.T) {
	existing := `{"model":"invented-model","permissions":{"allow":["Read"]},"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"./invented-guard.sh"}]}]}}`
	out, err := mergeHookSettings([]byte(existing), "some-slug", sgLogicBin)
	if err != nil {
		t.Fatal(err)
	}
	pre := sgLogicPreToolUse(t, out)
	if len(pre) == 0 {
		t.Fatalf("PreToolUse empty: %s", out)
	}
	if matcher, cmds := sgLogicGroupCommands(pre[0]); matcher != "Write" || len(cmds) != 1 || cmds[0] != "./invented-guard.sh" {
		t.Errorf("PreToolUse[0] = matcher %q cmds %v, want the repo's Write group in its place", matcher, cmds)
	}
	if exact, _ := sgLogicCountGuardGroups(pre); exact != 1 {
		t.Errorf("Agent guard groups = %d, want 1: %s", exact, out)
	}
	if !sgLogicHasBashLint(pre) {
		t.Errorf("bash-lint Bash group missing: %s", out)
	}

	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	if parsed["model"] != "invented-model" {
		t.Errorf("model = %v, want invented-model untouched", parsed["model"])
	}
	perms, _ := parsed["permissions"].(map[string]any)
	allow, _ := perms["allow"].([]any)
	if len(allow) != 1 || allow[0] != "Read" {
		t.Errorf("permissions = %v, want {allow:[Read]} untouched", parsed["permissions"])
	}
}
