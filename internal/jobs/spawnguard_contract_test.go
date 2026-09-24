package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// contractStubToplevel replaces spawnGuardToplevel for the duration of the
// test and restores the previous value on cleanup, so no test shells out.
func contractStubToplevel(t *testing.T, fn func(dir string) (string, error)) {
	t.Helper()
	prev := spawnGuardToplevel
	spawnGuardToplevel = fn
	t.Cleanup(func() { spawnGuardToplevel = prev })
}

// contractRoot builds a fake worktree root with a subdirectory, both already
// symlink-resolved so the raw and resolved spellings of each path agree.
func contractRoot(t *testing.T) (root, sub string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve tempdir: %v", err)
	}
	root = filepath.Join(base, "wt")
	sub = filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	return root, sub
}

func contractPayload(t *testing.T, tool, cwd string) *strings.Reader {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"tool_name":  tool,
		"cwd":        cwd,
		"session_id": "abc",
		"tool_input": map[string]any{"name": "hephaestus", "prompt": "do it"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return strings.NewReader(string(raw))
}

// Obligation: Agent + non-empty cwd + non-empty root + resolved cwd != root
// → block with a message naming cwd, root, "teammate", and `cd <root>; pwd`.
func TestContractSpawnGuardBlocksInSubdirectory(t *testing.T) {
	root, sub := contractRoot(t)
	contractStubToplevel(t, func(string) (string, error) { return root, nil })

	msg, block := SpawnGuardStdin(contractPayload(t, "Agent", sub))
	if !block {
		t.Fatalf("expected block for cwd %q under root %q", sub, root)
	}
	if msg == "" {
		t.Fatal("block must carry a non-empty message")
	}
	for _, want := range []string{sub, root, "teammate", "cd " + root + "; pwd"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "<root>") {
		t.Errorf("message must substitute the root, not emit the placeholder:\n%s", msg)
	}
}

// Obligation: resolved cwd equal to resolved root → ("", false).
func TestContractSpawnGuardAllowsAtRoot(t *testing.T) {
	root, _ := contractRoot(t)
	contractStubToplevel(t, func(string) (string, error) { return root, nil })

	msg, block := SpawnGuardStdin(contractPayload(t, "Agent", root))
	if block || msg != "" {
		t.Fatalf("cwd at root must allow, got (%q, %v)", msg, block)
	}
}

// Obligation: a trailing slash never matters (paths are Cleaned).
func TestContractSpawnGuardAllowsRootWithTrailingSlash(t *testing.T) {
	root, _ := contractRoot(t)
	contractStubToplevel(t, func(string) (string, error) { return root, nil })

	msg, block := SpawnGuardStdin(contractPayload(t, "Agent", root+"/"))
	if block || msg != "" {
		t.Fatalf("root with trailing slash must allow, got (%q, %v)", msg, block)
	}
}

// Obligation: a tool other than Agent → ("", false), even from a subdirectory.
func TestContractSpawnGuardIgnoresOtherTools(t *testing.T) {
	root, sub := contractRoot(t)
	contractStubToplevel(t, func(string) (string, error) { return root, nil })

	for _, tool := range []string{"Bash", "Read", "agent", ""} {
		msg, block := SpawnGuardStdin(contractPayload(t, tool, sub))
		if block || msg != "" {
			t.Errorf("tool %q must allow, got (%q, %v)", tool, msg, block)
		}
	}
}

// Obligation: a toplevel error (non-git cwd, git absent) → ("", false).
func TestContractSpawnGuardAllowsOnToplevelError(t *testing.T) {
	_, sub := contractRoot(t)
	contractStubToplevel(t, func(string) (string, error) { return "", errors.New("not a git repository") })

	msg, block := SpawnGuardStdin(contractPayload(t, "Agent", sub))
	if block || msg != "" {
		t.Fatalf("toplevel error must allow, got (%q, %v)", msg, block)
	}
}

// Obligation: spawnGuardCommand returns exactly krakenBin + " spawn-guard".
func TestContractSpawnGuardCommandShape(t *testing.T) {
	for _, bin := range []string{"/Users/x/.local/bin/kraken", "kraken"} {
		if got, want := spawnGuardCommand(bin), bin+" spawn-guard"; got != want {
			t.Errorf("spawnGuardCommand(%q) = %q, want %q", bin, got, want)
		}
	}
}

// Obligation: spawnGuardHookEntry has the exact bashLintHookEntry-style shape.
func TestContractSpawnGuardHookEntryShape(t *testing.T) {
	bin := "/Users/x/.local/bin/kraken"
	want := map[string]any{
		"matcher": "Agent",
		"hooks": []any{
			map[string]any{
				"type":    "command",
				"command": bin + " spawn-guard",
			},
		},
	}
	if got := spawnGuardHookEntry(bin); !reflect.DeepEqual(got, want) {
		t.Fatalf("spawnGuardHookEntry = %#v, want %#v", got, want)
	}
}

// contractPreToolCommands returns, per PreToolUse group, its first hook's
// command (and matcher), in array order.
func contractPreToolCommands(t *testing.T, out []byte) (cmds, matchers []string) {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("merged settings are not JSON: %v", err)
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks object in %s", out)
	}
	groups, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatalf("no PreToolUse array in %s", out)
	}
	for _, g := range groups {
		group, _ := g.(map[string]any)
		m, _ := group["matcher"].(string)
		list, _ := group["hooks"].([]any)
		cmd := ""
		if len(list) > 0 {
			h, _ := list[0].(map[string]any)
			cmd, _ = h["command"].(string)
		}
		cmds = append(cmds, cmd)
		matchers = append(matchers, m)
	}
	return cmds, matchers
}

func contractIndexes(cmds []string, want string) []int {
	var idx []int
	for i, c := range cmds {
		if c == want {
			idx = append(idx, i)
		}
	}
	return idx
}

// Obligation: a fresh render installs bash-lint THEN spawn-guard under
// PreToolUse, each exactly once, spawn-guard with matcher Agent.
func TestContractMergeInstallsSpawnGuardAfterBashLint(t *testing.T) {
	bin := "/Users/x/.local/bin/kraken"
	out, err := mergeHookSettings(nil, "demo", bin)
	if err != nil {
		t.Fatalf("mergeHookSettings: %v", err)
	}
	cmds, matchers := contractPreToolCommands(t, out)
	lint := contractIndexes(cmds, bashLintCommand(bin))
	guard := contractIndexes(cmds, spawnGuardCommand(bin))
	if len(lint) != 1 || len(guard) != 1 {
		t.Fatalf("want one bash-lint and one spawn-guard group, got %v", cmds)
	}
	if lint[0] >= guard[0] {
		t.Fatalf("bash-lint must precede spawn-guard, got %v", cmds)
	}
	if matchers[guard[0]] != "Agent" {
		t.Errorf("spawn-guard matcher = %q, want Agent", matchers[guard[0]])
	}
}

// Obligation: merging the output a second time is byte-identical.
func TestContractMergeIsIdempotent(t *testing.T) {
	bin := "/Users/x/.local/bin/kraken"
	first, err := mergeHookSettings(nil, "demo", bin)
	if err != nil {
		t.Fatalf("first merge: %v", err)
	}
	second, err := mergeHookSettings(first, "demo", bin)
	if err != nil {
		t.Fatalf("second merge: %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("re-merge changed the output:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// Obligation: an unrelated existing PreToolUse group survives ahead of both,
// and an existing group already carrying the spawn-guard command is not
// duplicated.
func TestContractMergePreservesExistingGroups(t *testing.T) {
	bin := "/Users/x/.local/bin/kraken"
	existing := []byte(`{"hooks":{"PreToolUse":[` +
		`{"matcher":"Write","hooks":[{"type":"command","command":"my-own-hook"}]},` +
		`{"matcher":"Agent","hooks":[{"type":"command","command":"` + bin + ` spawn-guard"}]}` +
		`]}}`)
	out, err := mergeHookSettings(existing, "demo", bin)
	if err != nil {
		t.Fatalf("mergeHookSettings: %v", err)
	}
	cmds, _ := contractPreToolCommands(t, out)
	own := contractIndexes(cmds, "my-own-hook")
	lint := contractIndexes(cmds, bashLintCommand(bin))
	guard := contractIndexes(cmds, spawnGuardCommand(bin))
	if len(own) != 1 || len(lint) != 1 || len(guard) != 1 {
		t.Fatalf("want one each of own/bash-lint/spawn-guard, got %v", cmds)
	}
	if own[0] > lint[0] || own[0] > guard[0] {
		t.Fatalf("unrelated group must survive ahead of both, got %v", cmds)
	}
}
