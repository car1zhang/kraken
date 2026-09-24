package jobs

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Blind edge-dimension tests for the spawn guard, authored from
// artifacts/50-contract.md alone: every allow path SpawnGuardStdin must take,
// path-resolution equivalence (symlinks, EvalSymlinks fallback, trailing
// slashes), and mergeHookSettings' install edges for the spawn-guard group.

// edgeStubToplevel replaces spawnGuardToplevel for the duration of the test
// and restores the previous value on cleanup, so no test ever shells out.
func edgeStubToplevel(t *testing.T, fn func(dir string) (string, error)) {
	t.Helper()
	prev := spawnGuardToplevel
	spawnGuardToplevel = fn
	t.Cleanup(func() { spawnGuardToplevel = prev })
}

// edgeFixedRoot is a stub answering root for every dir.
func edgeFixedRoot(root string) func(string) (string, error) {
	return func(string) (string, error) { return root, nil }
}

// edgePayload renders an Agent-shaped PreToolUse payload for cwd.
func edgePayload(t *testing.T, tool, cwd string) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"tool_name":  tool,
		"cwd":        cwd,
		"tool_input": map[string]any{"name": "hephaestus-x", "prompt": "do it"},
		"session_id": "abc",
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// edgeErrReader fails every Read.
type edgeErrReader struct{}

func (edgeErrReader) Read([]byte) (int, error) { return 0, errors.New("edge: unreadable") }

// edgeMissingRoot / edgeMissingSub are absolute paths that do not exist, so
// EvalSymlinks fails on them and resolution is filepath.Clean alone — which
// is the identity on these already-clean strings.
const (
	edgeMissingRoot = "/edge-spawnguard-missing-7f3a/root"
	edgeMissingSub  = "/edge-spawnguard-missing-7f3a/root/sub"
)

func edgeAssertAllow(t *testing.T, label, msg string, block bool) {
	t.Helper()
	if block || msg != "" {
		t.Errorf("%s: SpawnGuardStdin = (%q, %v), want (\"\", false)", label, msg, block)
	}
}

func edgeAssertBlock(t *testing.T, label, msg string, block bool) {
	t.Helper()
	if !block || msg == "" {
		t.Errorf("%s: SpawnGuardStdin = (%q, %v), want a non-empty msg and true", label, msg, block)
	}
}

// TestEdgeSpawnGuardSanityBlocks is the positive control the allow tests lean
// on: with a stub root that differs from cwd, an Agent payload blocks. Without
// it every allow test below could pass against a guard that never blocks.
func TestEdgeSpawnGuardSanityBlocks(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub)))
	edgeAssertBlock(t, "subdir cwd", msg, block)
}

// TestEdgeSpawnGuardNonAgentToolAllows: a tool other than exactly "Agent"
// allows, even when cwd and root differ.
func TestEdgeSpawnGuardNonAgentToolAllows(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	for _, tool := range []string{"Bash", "Task", "", "agent", "AGENT", " Agent", "Agent "} {
		msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, tool, edgeMissingSub)))
		edgeAssertAllow(t, "tool "+`"`+tool+`"`, msg, block)
	}
	// tool_name absent entirely.
	msg, block := SpawnGuardStdin(strings.NewReader(`{"cwd":"` + edgeMissingSub + `"}`))
	edgeAssertAllow(t, "absent tool_name", msg, block)
}

// TestEdgeSpawnGuardEmptyOrAbsentCwdAllows: an empty or absent cwd allows,
// whatever toplevel would answer.
func TestEdgeSpawnGuardEmptyOrAbsentCwdAllows(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	msg, block := SpawnGuardStdin(strings.NewReader(`{"tool_name":"Agent","cwd":""}`))
	edgeAssertAllow(t, "empty cwd", msg, block)
	msg, block = SpawnGuardStdin(strings.NewReader(`{"tool_name":"Agent"}`))
	edgeAssertAllow(t, "absent cwd", msg, block)
	msg, block = SpawnGuardStdin(strings.NewReader(`{"tool_name":"Agent","cwd":null}`))
	edgeAssertAllow(t, "null cwd", msg, block)
}

// TestEdgeSpawnGuardUnreadableReaderAllows: a reader that errors allows.
func TestEdgeSpawnGuardUnreadableReaderAllows(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	msg, block := SpawnGuardStdin(edgeErrReader{})
	edgeAssertAllow(t, "unreadable reader", msg, block)
}

// TestEdgeSpawnGuardMalformedJSONAllows: malformed and non-object JSON allow.
func TestEdgeSpawnGuardMalformedJSONAllows(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	for _, in := range []string{
		"",
		"   ",
		"{",
		"not json",
		`{"tool_name":"Agent","cwd":"` + edgeMissingSub + `"`, // truncated object
		`{"tool_name":"Agent","cwd":42}`,                      // cwd of the wrong type
		`{"tool_name":7,"cwd":"` + edgeMissingSub + `"}`,      // tool_name of the wrong type
		`[]`,
		`["Agent","` + edgeMissingSub + `"]`,
		`"Agent"`,
		`42`,
		`true`,
		`null`,
	} {
		msg, block := SpawnGuardStdin(strings.NewReader(in))
		edgeAssertAllow(t, "input "+`"`+in+`"`, msg, block)
	}
}

// TestEdgeSpawnGuardToplevelErrorAllows: any toplevel error allows — including
// one that also returns a non-empty root, since the contract requires "a
// non-empty root with no error" to block.
func TestEdgeSpawnGuardToplevelErrorAllows(t *testing.T) {
	edgeStubToplevel(t, func(string) (string, error) { return "", errors.New("fatal: not a git repository") })
	msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub)))
	edgeAssertAllow(t, "toplevel error, empty root", msg, block)

	edgeStubToplevel(t, func(string) (string, error) { return edgeMissingRoot, errors.New("exec: git not found") })
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub)))
	edgeAssertAllow(t, "toplevel error, non-empty root", msg, block)
}

// TestEdgeSpawnGuardEmptyRootAllows: an empty root with no error allows.
func TestEdgeSpawnGuardEmptyRootAllows(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(""))
	msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub)))
	edgeAssertAllow(t, "empty root", msg, block)
}

// TestEdgeSpawnGuardToplevelGetsCwd: the guard asks toplevel about the
// payload's cwd (the only dir the contract gives it).
func TestEdgeSpawnGuardToplevelGetsCwd(t *testing.T) {
	var asked []string
	edgeStubToplevel(t, func(dir string) (string, error) {
		asked = append(asked, dir)
		return edgeMissingRoot, nil
	})
	SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub)))
	if len(asked) == 0 {
		t.Fatal("spawnGuardToplevel was never consulted for an Agent payload with a cwd")
	}
	if asked[0] != edgeMissingSub {
		t.Errorf("spawnGuardToplevel asked about %q, want the payload cwd %q", asked[0], edgeMissingSub)
	}
}

// edgeSymlinkTree builds root/, root/sub/, a symlink to root and a symlink to
// root/sub under t.TempDir(), never relying on /tmp being a symlink.
func edgeSymlinkTree(t *testing.T) (root, sub, linkRoot, linkSub string) {
	t.Helper()
	base := t.TempDir()
	root = filepath.Join(base, "root")
	sub = filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	linkRoot = filepath.Join(base, "link-root")
	linkSub = filepath.Join(base, "link-sub")
	if err := os.Symlink(root, linkRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(sub, linkSub); err != nil {
		t.Fatal(err)
	}
	return root, sub, linkRoot, linkSub
}

// TestEdgeSpawnGuardSymlinkEquivalence: a symlink to the root compares equal
// to the root, in either direction; a symlink to a subdir still blocks.
func TestEdgeSpawnGuardSymlinkEquivalence(t *testing.T) {
	root, sub, linkRoot, linkSub := edgeSymlinkTree(t)

	edgeStubToplevel(t, edgeFixedRoot(root))
	msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", linkRoot)))
	edgeAssertAllow(t, "cwd is a symlink to the root", msg, block)
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", root)))
	edgeAssertAllow(t, "cwd is the root itself", msg, block)
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", linkSub)))
	edgeAssertBlock(t, "cwd is a symlink to a subdir", msg, block)
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", sub)))
	edgeAssertBlock(t, "cwd is a real subdir", msg, block)

	edgeStubToplevel(t, edgeFixedRoot(linkRoot))
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", root)))
	edgeAssertAllow(t, "root reported through a symlink, cwd real", msg, block)
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", linkRoot)))
	edgeAssertAllow(t, "both through the symlink", msg, block)
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", sub)))
	edgeAssertBlock(t, "root via symlink, cwd a real subdir", msg, block)
}

// TestEdgeSpawnGuardEvalSymlinksFallbackToClean: when EvalSymlinks fails (a
// path that does not exist), that path is compared by filepath.Clean alone.
func TestEdgeSpawnGuardEvalSymlinksFallbackToClean(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	for _, cwd := range []string{
		edgeMissingRoot,
		edgeMissingRoot + "/",
		edgeMissingRoot + "/.",
		edgeMissingRoot + "/sub/..",
		"/edge-spawnguard-missing-7f3a//root",
	} {
		msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", cwd)))
		edgeAssertAllow(t, "missing cwd "+cwd+" cleans to the missing root", msg, block)
	}
	msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub)))
	edgeAssertBlock(t, "missing subdir of a missing root", msg, block)

	// Unclean ROOT, cleaned the same way.
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot+"/sub/../"))
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingRoot)))
	edgeAssertAllow(t, "unclean missing root", msg, block)

	// One side resolves, the other falls back: a real cwd against a root
	// that does not exist are different paths, so the guard blocks.
	realDir := t.TempDir()
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	msg, block = SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", realDir)))
	edgeAssertBlock(t, "real cwd vs missing root", msg, block)
}

// TestEdgeSpawnGuardTrailingSlashEquivalence: a trailing slash on either side
// never matters, for a real directory as well as a missing one.
func TestEdgeSpawnGuardTrailingSlashEquivalence(t *testing.T) {
	root, _, _, _ := edgeSymlinkTree(t)
	cases := []struct{ cwd, root string }{
		{root + "/", root},
		{root, root + "/"},
		{root + "/", root + "/"},
		{root + "//", root},
		{edgeMissingRoot + "/", edgeMissingRoot},
		{edgeMissingRoot, edgeMissingRoot + "/"},
	}
	for _, c := range cases {
		edgeStubToplevel(t, edgeFixedRoot(c.root))
		msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", c.cwd)))
		edgeAssertAllow(t, "cwd "+c.cwd+" vs root "+c.root, msg, block)
	}
	// And a trailing slash does not turn a subdir into the root.
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot+"/"))
	msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub+"/")))
	edgeAssertBlock(t, "subdir with trailing slashes", msg, block)
}

// TestEdgeSpawnGuardBlockMessage: the block message names the cwd, the root,
// the word teammate, and the literal fix with the real root substituted. The
// paths are missing and already clean, so raw and resolved agree and the
// assertion does not depend on which form the message carries.
func TestEdgeSpawnGuardBlockMessage(t *testing.T) {
	edgeStubToplevel(t, edgeFixedRoot(edgeMissingRoot))
	msg, block := SpawnGuardStdin(strings.NewReader(edgePayload(t, "Agent", edgeMissingSub)))
	if !block {
		t.Fatalf("want a block, got (%q, false)", msg)
	}
	for _, want := range []string{edgeMissingSub, edgeMissingRoot, "teammate", "cd " + edgeMissingRoot + "; pwd"} {
		if !strings.Contains(msg, want) {
			t.Errorf("block message missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "<root>") {
		t.Errorf("block message carries the literal placeholder <root>:\n%s", msg)
	}
}

// TestEdgeSpawnGuardCommandAndEntry: the exact command string and the hook
// entry's exact shape, for an unusual binary path.
func TestEdgeSpawnGuardCommandAndEntry(t *testing.T) {
	for _, bin := range []string{"kraken", "/Users/x y/.local/bin/kraken", ""} {
		if got, want := spawnGuardCommand(bin), bin+" spawn-guard"; got != want {
			t.Errorf("spawnGuardCommand(%q) = %q, want %q", bin, got, want)
		}
		e := spawnGuardHookEntry(bin)
		if len(e) != 2 {
			t.Errorf("spawnGuardHookEntry(%q) has keys %v, want exactly matcher and hooks", bin, e)
		}
		if m, ok := e["matcher"].(string); !ok || m != "Agent" {
			t.Errorf("spawnGuardHookEntry(%q).matcher = %#v, want \"Agent\"", bin, e["matcher"])
		}
		hooks, ok := e["hooks"].([]any)
		if !ok || len(hooks) != 1 {
			t.Fatalf("spawnGuardHookEntry(%q).hooks = %#v, want a one-element []any", bin, e["hooks"])
		}
		h, ok := hooks[0].(map[string]any)
		if !ok {
			t.Fatalf("spawnGuardHookEntry(%q).hooks[0] = %#v, want map[string]any", bin, hooks[0])
		}
		if len(h) != 2 || h["type"] != "command" || h["command"] != spawnGuardCommand(bin) {
			t.Errorf("spawnGuardHookEntry(%q).hooks[0] = %#v, want {type: command, command: %q}", bin, h, spawnGuardCommand(bin))
		}
	}
}

// edgeBin is the binary path the merge tests install with.
const edgeBin = "/usr/local/bin/kraken"

// edgePreToolUse parses mergeHookSettings output and returns hooks.PreToolUse
// as whatever JSON value it is.
func edgePreToolUse(t *testing.T, out []byte) any {
	t.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("mergeHookSettings produced invalid JSON: %v\n%s", err, out)
	}
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("no hooks object in:\n%s", out)
	}
	return hooks["PreToolUse"]
}

// edgeGroupIndex returns the index of the first group carrying command, or -1.
func edgeGroupIndex(groups []any, command string) int {
	for i, g := range groups {
		if hasHookCommand([]any{g}, command) {
			return i
		}
	}
	return -1
}

// edgeGroupsCarrying counts the groups carrying command.
func edgeGroupsCarrying(groups []any, command string) int {
	n := 0
	for _, g := range groups {
		if hasHookCommand([]any{g}, command) {
			n++
		}
	}
	return n
}

// TestEdgeMergeUnrelatedPreToolUseGroupSurvivesAhead: a repo's own PreToolUse
// group survives the merge and stays ahead of both kraken groups, which land
// bash-lint then spawn-guard.
func TestEdgeMergeUnrelatedPreToolUseGroupSurvivesAhead(t *testing.T) {
	existing := []byte(`{"hooks":{"PreToolUse":[{"matcher":"Write","hooks":[{"type":"command","command":"./guard.sh"}]}]}}`)
	out, err := mergeHookSettings(existing, "demo", edgeBin)
	if err != nil {
		t.Fatal(err)
	}
	groups, ok := edgePreToolUse(t, out).([]any)
	if !ok {
		t.Fatalf("PreToolUse is not an array:\n%s", out)
	}
	guard := edgeGroupIndex(groups, "./guard.sh")
	lint := edgeGroupIndex(groups, bashLintCommand(edgeBin))
	spawn := edgeGroupIndex(groups, spawnGuardCommand(edgeBin))
	if guard != 0 || lint < 0 || spawn < 0 || !(guard < lint && lint < spawn) {
		t.Fatalf("PreToolUse order: guard=%d bash-lint=%d spawn-guard=%d, want 0 < bash-lint < spawn-guard:\n%s", guard, lint, spawn, out)
	}
	if len(groups) != 3 {
		t.Errorf("PreToolUse has %d groups, want 3 (repo, bash-lint, spawn-guard):\n%s", len(groups), out)
	}
	g0 := groups[0].(map[string]any)
	if g0["matcher"] != "Write" {
		t.Errorf("repo group's matcher changed to %#v", g0["matcher"])
	}

	// And merging again changes nothing.
	again, err := mergeHookSettings(out, "demo", edgeBin)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(out) {
		t.Errorf("second merge not byte-identical:\nfirst:\n%s\nsecond:\n%s", out, again)
	}
}

// TestEdgeMergeExistingSpawnGuardNotDuplicated: a group already carrying
// exactly spawnGuardCommand(krakenBin) — whatever its matcher, whatever else
// it holds — is not duplicated. A near-miss command is not the same command.
func TestEdgeMergeExistingSpawnGuardNotDuplicated(t *testing.T) {
	cmd := spawnGuardCommand(edgeBin)
	for _, existing := range []string{
		`{"hooks":{"PreToolUse":[{"matcher":"Agent","hooks":[{"type":"command","command":"` + cmd + `"}]}]}}`,
		`{"hooks":{"PreToolUse":[{"matcher":"*","hooks":[{"type":"command","command":"./other.sh"},{"type":"command","command":"` + cmd + `"}]}]}}`,
		`{"hooks":{"PreToolUse":[{"matcher":"Agent","hooks":[{"type":"command","command":"` + cmd + `"}]},{"matcher":"Bash","hooks":[{"type":"command","command":"./guard.sh"}]}]}}`,
	} {
		out, err := mergeHookSettings([]byte(existing), "demo", edgeBin)
		if err != nil {
			t.Fatalf("mergeHookSettings(%s): %v", existing, err)
		}
		groups, ok := edgePreToolUse(t, out).([]any)
		if !ok {
			t.Fatalf("PreToolUse is not an array:\n%s", out)
		}
		if n := edgeGroupsCarrying(groups, cmd); n != 1 {
			t.Errorf("existing %s: %d groups carry %q, want 1:\n%s", existing, n, cmd, out)
		}
		if n := strings.Count(string(out), cmd); n != 1 {
			t.Errorf("existing %s: %q appears %d times, want 1:\n%s", existing, cmd, n, out)
		}
		if !hasHookCommand(groups, bashLintCommand(edgeBin)) {
			t.Errorf("existing %s: bash-lint group missing:\n%s", existing, out)
		}
	}

	// Near misses: a different binary, or extra args, are different commands,
	// so the real spawn-guard group is still installed.
	for _, near := range []string{"kraken spawn-guard", cmd + " --verbose", cmd + " "} {
		existing := `{"hooks":{"PreToolUse":[{"matcher":"Agent","hooks":[{"type":"command","command":"` + near + `"}]}]}}`
		out, err := mergeHookSettings([]byte(existing), "demo", edgeBin)
		if err != nil {
			t.Fatalf("mergeHookSettings(%s): %v", existing, err)
		}
		groups, ok := edgePreToolUse(t, out).([]any)
		if !ok {
			t.Fatalf("PreToolUse is not an array:\n%s", out)
		}
		if edgeGroupsCarrying(groups, cmd) != 1 {
			t.Errorf("near-miss %q: exact spawn-guard command not installed once:\n%s", near, out)
		}
		if !hasHookCommand(groups, near) {
			t.Errorf("near-miss %q: repo's own command dropped:\n%s", near, out)
		}
	}
}

// TestEdgeMergeNonArrayPreToolUseLeftAlone: a PreToolUse value that exists but
// is not an array is left exactly as written — no spawn-guard group forced in.
func TestEdgeMergeNonArrayPreToolUseLeftAlone(t *testing.T) {
	for _, raw := range []string{`"nonsense"`, `{"matcher":"Agent"}`, `42`, `null`} {
		existing := []byte(`{"hooks":{"PreToolUse":` + raw + `}}`)
		out, err := mergeHookSettings(existing, "demo", edgeBin)
		if err != nil {
			t.Fatalf("mergeHookSettings(%s): %v", existing, err)
		}
		got, err := json.Marshal(edgePreToolUse(t, out))
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if err := json.Unmarshal([]byte(raw), &want); err != nil {
			t.Fatal(err)
		}
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("PreToolUse %s rewritten to %s:\n%s", raw, got, out)
		}
		if strings.Contains(string(out), spawnGuardCommand(edgeBin)) {
			t.Errorf("PreToolUse %s: spawn-guard installed despite a non-array value:\n%s", raw, out)
		}
	}
}
