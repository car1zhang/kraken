package jobs

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// spawnguard_edge_test.go — the EDGE dimension of Slice D of
// artifacts/50-contract.md (the spawn guard): SpawnGuardStdin's refusals of
// malformed or missing evidence (D1 rules 1–3, 6), spawnGuardMessage's
// verbatim arguments (D2), spawnGuardSameDir's symlink/clean/relative
// boundaries (D3), spawnGuardCommand's exact string (D4), and
// mergeHookSettings' append-if-absent behaviour around odd existing settings
// (D5). Happy paths belong to the sibling blind file (sgLogic*).
//
// Written blind from the contract text: no byte of internal/jobs/spawnguard.go
// or of the slice-D edits to cell.go was read. Fixtures are invented payload
// strings and t.TempDir() directories.
//
// Every package-level identifier here is prefixed `sgEdge`.

// --- helpers ---------------------------------------------------------------

// sgEdgeJSON marshals v, failing the test on error.
func sgEdgeJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sgEdgePayload renders an Agent PreToolUse payload with the given cwd.
func sgEdgePayload(t *testing.T, cwd string) string {
	t.Helper()
	return sgEdgeJSON(t, map[string]any{
		"session_id":      "s-edge",
		"hook_event_name": "PreToolUse",
		"tool_name":       "Agent",
		"cwd":             cwd,
		"tool_input":      map[string]any{"name": "hephaestus-x", "prompt": "do the slice"},
	})
}

// sgEdgeDirs returns an invented worktree root and a subdirectory of it that
// both exist, so a mismatch is real on disk.
func sgEdgeDirs(t *testing.T) (root, sub string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "wt-edge")
	sub = filepath.Join(root, "internal", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, sub
}

// sgEdgeCaptureOut runs fn with os.Stdout and os.Stderr redirected to one
// pipe and returns whatever was written (D1.6: the guard never writes).
func sgEdgeCaptureOut(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = w, w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	func() {
		defer func() { os.Stdout, os.Stderr = origOut, origErr }()
		fn()
	}()
	w.Close()
	got := <-done
	r.Close()
	return got
}

// sgEdgeRun calls SpawnGuardStdin, failing on a panic or on any write.
func sgEdgeRun(t *testing.T, stdin io.Reader, projectDir string) (string, bool) {
	t.Helper()
	var msg string
	var block bool
	var panicked any
	out := sgEdgeCaptureOut(t, func() {
		defer func() { panicked = recover() }()
		msg, block = SpawnGuardStdin(stdin, projectDir)
	})
	if panicked != nil {
		t.Fatalf("SpawnGuardStdin panicked: %v", panicked)
	}
	if out != "" {
		t.Fatalf("SpawnGuardStdin wrote %q; contract D1.6: never writes anywhere", out)
	}
	return msg, block
}

// sgEdgeWantAllow asserts the ("", false) answer.
func sgEdgeWantAllow(t *testing.T, stdin io.Reader, projectDir, why string) {
	t.Helper()
	msg, block := sgEdgeRun(t, stdin, projectDir)
	if block || msg != "" {
		t.Fatalf("%s: SpawnGuardStdin = (%q, %v), want (\"\", false)", why, msg, block)
	}
}

// sgEdgeErrReader fails every read.
type sgEdgeErrReader struct{}

func (sgEdgeErrReader) Read([]byte) (int, error) { return 0, errors.New("sg-edge: stdin closed") }

const sgEdgeMsgFmt = "kraken spawn-guard: your shell is in %CWD%, not the worktree root %ROOT% — a teammate spawned from here starts there and loads NONE of the cell's hooks (no bash-lint, no pr-hook, no limit-hook, no recall; none of the repo's own guards either) and files its transcript under another project dir. Run cd '%ROOT%' as its own Bash call, then spawn."

// sgEdgeWantMsg builds D2's exact sentence from the raw arguments.
func sgEdgeWantMsg(cwd, root string) string {
	return strings.ReplaceAll(strings.ReplaceAll(sgEdgeMsgFmt, "%CWD%", cwd), "%ROOT%", root)
}

// --- D1 rule 1: bad stdin --------------------------------------------------

func TestSgEdgeStdinMalformedAllows(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	valid := sgEdgePayload(t, sub)
	// Non-vacuous: the same root/sub pair DOES block when the payload is
	// well-formed, so every allow below comes from the arm under test.
	if _, block := sgEdgeRun(t, strings.NewReader(valid), root); !block {
		t.Fatalf("fixture sanity: a well-formed Agent payload from a subdir must block")
	}
	cases := map[string]string{
		"empty":            "",
		"whitespace":       " \n\t \n",
		"not json":         "tool_name=Agent cwd=" + sub,
		"truncated json":   valid[:len(valid)-5],
		"array":            "[" + valid + "]",
		"string":           sgEdgeJSON(t, valid),
		"null":             "null",
		"number":           "42",
		"bool":             "true",
		"binary junk":      "\xff\xfe\x00\x01{\x80\"tool_name\x00",
		"nul bytes":        strings.Repeat("\x00", 256),
		"deep nesting":     strings.Repeat("[", 20000) + strings.Repeat("]", 20000),
		"unclosed nesting": `{"tool_name":"Agent","cwd":` + strings.Repeat(`{"a":`, 5000),
	}
	for name, stdin := range cases {
		t.Run(name, func(t *testing.T) {
			sgEdgeWantAllow(t, strings.NewReader(stdin), root, name)
		})
	}
}

func TestSgEdgeStdinUnreadableAllows(t *testing.T) {
	root, _ := sgEdgeDirs(t)
	sgEdgeWantAllow(t, sgEdgeErrReader{}, root, "erroring reader")
}

// A payload over 1 MiB is read only to 1 MiB, which truncates it into
// non-JSON → allow, even though its fields would otherwise block. The
// padding sits BETWEEN the fields so the cut lands inside the object.
func TestSgEdgeStdinOver1MiBAllows(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	payload := `{"tool_name":"Agent","tool_input":{"name":"hephaestus-x","prompt":"` + strings.Repeat("p", (1<<20)+4096) + `"},"cwd":` + sgEdgeJSON(t, sub) + `}`
	sgEdgeWantAllow(t, strings.NewReader(payload), root, "payload over 1 MiB")
}

// Just under the cap, the same shape is read whole and blocks: the cap is a
// size bound, not a refusal of large prompts.
func TestSgEdgeStdinJustUnder1MiBStillBlocks(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	payload := `{"tool_name":"Agent","tool_input":{"name":"hephaestus-x","prompt":"` + strings.Repeat("p", (1<<20)-4096) + `"},"cwd":` + sgEdgeJSON(t, sub) + `}`
	msg, block := sgEdgeRun(t, strings.NewReader(payload), root)
	if !block || msg != spawnGuardMessage(sub, root) {
		t.Fatalf("payload just under 1 MiB = (%q, %v), want the block", msg, block)
	}
}

// --- D1 rule 2: tool_name --------------------------------------------------

func TestSgEdgeStdinToolNameNotExactlyAgentAllows(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	// Every payload carries a NAMED tool_input (D1 3b) so the allow comes from
	// the tool_name arm, not from a missing name.
	cwd := sgEdgeJSON(t, sub) + `,"tool_input":{"name":"hephaestus-x"}`
	cases := map[string]string{
		"missing":       `{"cwd":` + cwd + `}`,
		"null":          `{"tool_name":null,"cwd":` + cwd + `}`,
		"number":        `{"tool_name":7,"cwd":` + cwd + `}`,
		"array":         `{"tool_name":["Agent"],"cwd":` + cwd + `}`,
		"object":        `{"tool_name":{"name":"Agent"},"cwd":` + cwd + `}`,
		"bool":          `{"tool_name":true,"cwd":` + cwd + `}`,
		"lower case":    `{"tool_name":"agent","cwd":` + cwd + `}`,
		"upper case":    `{"tool_name":"AGENT","cwd":` + cwd + `}`,
		"Bash":          `{"tool_name":"Bash","cwd":` + cwd + `}`,
		"Task":          `{"tool_name":"Task","cwd":` + cwd + `}`,
		"trailing sp":   `{"tool_name":"Agent ","cwd":` + cwd + `}`,
		"leading sp":    `{"tool_name":" Agent","cwd":` + cwd + `}`,
		"empty":         `{"tool_name":"","cwd":` + cwd + `}`,
		"mcp lookalike": `{"tool_name":"mcp__x__Agent","cwd":` + cwd + `}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			sgEdgeWantAllow(t, strings.NewReader(payload), root, "tool_name "+name)
		})
	}
}

// --- D1 rule 3: projectDir and cwd evidence --------------------------------

func TestSgEdgeStdinCwdMissingOrNonStringAllows(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	// Named tool_input on every case, so the allow comes from the cwd arm.
	const ti = `"tool_name":"Agent","tool_input":{"name":"hephaestus-x"}`
	cases := map[string]string{
		"missing":     `{` + ti + `}`,
		"empty":       `{` + ti + `,"cwd":""}`,
		"null":        `{` + ti + `,"cwd":null}`,
		"number":      `{` + ti + `,"cwd":42}`,
		"object":      `{` + ti + `,"cwd":{"path":` + sgEdgeJSON(t, sub) + `}}`,
		"array":       `{` + ti + `,"cwd":[` + sgEdgeJSON(t, sub) + `]}`,
		"bool":        `{` + ti + `,"cwd":false}`,
		"nested only": `{"tool_name":"Agent","tool_input":{"name":"hephaestus-x","cwd":` + sgEdgeJSON(t, sub) + `}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			sgEdgeWantAllow(t, strings.NewReader(payload), root, "cwd "+name)
		})
	}
}

// projectDir empty with a cwd that is clearly elsewhere must still allow: no
// root, nothing to compare.
func TestSgEdgeStdinEmptyProjectDirAllows(t *testing.T) {
	_, sub := sgEdgeDirs(t)
	sgEdgeWantAllow(t, strings.NewReader(sgEdgePayload(t, sub)), "", "empty projectDir")
	sgEdgeWantAllow(t, strings.NewReader(sgEdgePayload(t, "/sg-edge/not/a/root")), "", "empty projectDir, nonexistent cwd")
}

// The block carries D2's message built from the RAW cwd and projectDir (not
// cleaned): a trailing-slash cwd shows its slash.
func TestSgEdgeStdinBlockMessageUsesRawArgs(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	rawCwd := sub + "/"
	rawRoot := root + "/"
	msg, block := sgEdgeRun(t, strings.NewReader(sgEdgePayload(t, rawCwd)), rawRoot)
	if !block {
		t.Fatalf("subdir cwd with trailing slash did not block")
	}
	if want := sgEdgeWantMsg(rawCwd, rawRoot); msg != want {
		t.Fatalf("block message =\n%q\nwant\n%q", msg, want)
	}
	if msg != spawnGuardMessage(rawCwd, rawRoot) {
		t.Fatalf("block message is not spawnGuardMessage(cwd, projectDir) on the raw values")
	}
}

// Same directory reached through a symlink, a trailing slash or a `.`
// segment is NOT a mismatch (D1.4 via D3).
func TestSgEdgeStdinSameDirSpellingsAllow(t *testing.T) {
	root, _ := sgEdgeDirs(t)
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	for name, cwd := range map[string]string{
		"trailing slash": root + "/",
		"dot segment":    root + "/./",
		"sub/..":         root + "/internal/..",
		"symlink":        link,
		"symlink slash":  link + "/",
	} {
		t.Run(name, func(t *testing.T) {
			sgEdgeWantAllow(t, strings.NewReader(sgEdgePayload(t, cwd)), root, name)
		})
		t.Run(name+" reversed", func(t *testing.T) {
			sgEdgeWantAllow(t, strings.NewReader(sgEdgePayload(t, root)), cwd, name+" as projectDir")
		})
	}
}

// Extra unknown keys, top-level and inside tool_input, do not change the
// decision for a named spawn.
func TestSgEdgeStdinUnknownKeysIgnored(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	payload := `{"weird":[1,2,{"x":null}],"tool_name":"Agent","cwd":` + sgEdgeJSON(t, sub) + `,"tool_input":{"name":"hephaestus-x","odd":{"deep":[null]},"subagent_type":"general-purpose"},"permission_mode":"bypassPermissions","agent_id":"invented"}`
	msg, block := sgEdgeRun(t, strings.NewReader(payload), root)
	if !block || msg != spawnGuardMessage(sub, root) {
		t.Fatalf("unknown keys changed the decision: (%q, %v)", msg, block)
	}
}

// --- D1 rule 3b: only a NAMED spawn is judged ------------------------------

// An Agent call from a subdir whose tool_input.name is missing, empty,
// whitespace-only or not a string is an in-process subagent → allow.
func TestSgEdgeStdinUnnamedAgentAllows(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	cwd := sgEdgeJSON(t, sub)
	cases := map[string]string{
		"no tool_input":          `{"tool_name":"Agent","cwd":` + cwd + `}`,
		"tool_input null":        `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":null}`,
		"tool_input string":      `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":"hephaestus-x"}`,
		"tool_input array":       `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":[{"name":"hephaestus-x"}]}`,
		"name missing":           `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"prompt":"explore","subagent_type":"Explore"}}`,
		"name empty":             `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":""}}`,
		"name spaces":            `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":"   "}}`,
		"name tab newline":       `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":"\t\n "}}`,
		"name number":            `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":42}}`,
		"name null":              `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":null}}`,
		"name bool":              `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":true}}`,
		"name array":             `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":["hephaestus-x"]}}`,
		"name object":            `{"tool_name":"Agent","cwd":` + cwd + `,"tool_input":{"name":{"v":"hephaestus-x"}}}`,
		"name at top level only": `{"tool_name":"Agent","name":"hephaestus-x","cwd":` + cwd + `,"tool_input":{"prompt":"x"}}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			sgEdgeWantAllow(t, strings.NewReader(payload), root, "unnamed: "+name)
		})
	}
}

// A one-character name, or a name padded with whitespace, IS a name → the
// subdir spawn blocks. (Assumption: "whitespace-only" is the only
// whitespace refusal; surrounding spaces around real text still name.)
func TestSgEdgeStdinMinimalNameBlocks(t *testing.T) {
	root, sub := sgEdgeDirs(t)
	for _, n := range []string{"x", "  hephaestus-x  ", "子"} {
		payload := `{"tool_name":"Agent","cwd":` + sgEdgeJSON(t, sub) + `,"tool_input":{"name":` + sgEdgeJSON(t, n) + `}}`
		msg, block := sgEdgeRun(t, strings.NewReader(payload), root)
		if !block || msg != spawnGuardMessage(sub, root) {
			t.Fatalf("name %q from a subdir = (%q, %v), want the block", n, msg, block)
		}
	}
}

// --- D2: spawnGuardMessage ---------------------------------------------------

func TestSgEdgeMessageVerbatimArgs(t *testing.T) {
	cases := map[string][2]string{
		"spaces":          {"/sg edge/my cell/sub dir", "/sg edge/my cell"},
		"trailing slash":  {"/sg-edge/wt/sub/", "/sg-edge/wt/"},
		"dot segments":    {"/sg-edge/wt/./sub/../sub", "/sg-edge/wt/."},
		"relative":        {"sub", "."},
		"shell metachars": {"/sg-edge/$HOME/`x`/'q'", "/sg-edge/\"wt\";rm"},
		"unicode":         {"/sg-edge/wörk/子", "/sg-edge/wörk"},
		"empty":           {"", ""},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := spawnGuardMessage(c[0], c[1])
			if want := sgEdgeWantMsg(c[0], c[1]); got != want {
				t.Fatalf("spawnGuardMessage(%q, %q) =\n%q\nwant\n%q", c[0], c[1], got, want)
			}
			if strings.Contains(got, "\n") {
				t.Fatalf("message carries a newline: %q", got)
			}
		})
	}
}

// --- D3: spawnGuardSameDir ---------------------------------------------------

func sgEdgeSame(t *testing.T, a, b string, want bool) {
	t.Helper()
	var got bool
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("spawnGuardSameDir(%q, %q) panicked: %v", a, b, r)
			}
		}()
		got = spawnGuardSameDir(a, b)
	}()
	if got != want {
		t.Fatalf("spawnGuardSameDir(%q, %q) = %v, want %v", a, b, got, want)
	}
}

// A symlink I create resolves to its target: equal both ways, with and
// without trailing slashes.
func TestSgEdgeSameDirSymlinkEquality(t *testing.T) {
	target, _ := sgEdgeDirs(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	sgEdgeSame(t, link, target, true)
	sgEdgeSame(t, target, link, true)
	sgEdgeSame(t, link+"/", target, true)
	sgEdgeSame(t, link, target+"//", true)
	// A symlink to a SUBDIR of the target is not the target.
	sub := filepath.Join(target, "internal")
	sublink := filepath.Join(t.TempDir(), "sublink")
	if err := os.Symlink(sub, sublink); err != nil {
		t.Fatal(err)
	}
	sgEdgeSame(t, sublink, target, false)
	sgEdgeSame(t, sublink, sub, true)
}

// The /tmp → /private/tmp shape: t.TempDir sits behind macOS's /var →
// /private/var symlink, so the unresolved and resolved spellings are equal.
// Where the platform has no such link the two strings are equal anyway.
func TestSgEdgeSameDirPlatformTempSymlink(t *testing.T) {
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	sgEdgeSame(t, dir, resolved, true)
	sgEdgeSame(t, resolved+"/", dir, true)
}

// Two non-existent paths compare by their cleaned strings.
func TestSgEdgeSameDirNonexistentCleanedCompare(t *testing.T) {
	base := filepath.Join(t.TempDir(), "ghost-root")
	sgEdgeSame(t, base, base+"/", true)
	sgEdgeSame(t, base+"/a/../b", base+"/b", true)
	sgEdgeSame(t, base+"/./x//", base+"/x", true)
	sgEdgeSame(t, base+"/x", base+"/y", false)
	sgEdgeSame(t, "/sg-edge/no/such/dir", "/sg-edge/no/such/dir/", true)
	sgEdgeSame(t, "/sg-edge/no/such/dir", "/sg-edge/no/such/DIR", false)
}

// A symlink whose target is gone does not resolve, so it compares by its
// cleaned string: equal to itself, NOT equal to the (missing) target's string.
func TestSgEdgeSameDirDanglingSymlink(t *testing.T) {
	scratch := t.TempDir()
	target := filepath.Join(scratch, "gone")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(scratch, "dangling")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	sgEdgeSame(t, link, link+"/", true)
	sgEdgeSame(t, link, target, false)
}

// One side exists and the other does not: different cleaned strings → false,
// even when the missing one sits inside the existing one.
func TestSgEdgeSameDirExistingVersusMissing(t *testing.T) {
	root, _ := sgEdgeDirs(t)
	sgEdgeSame(t, root, filepath.Join(root, "ghost"), false)
	sgEdgeSame(t, filepath.Join(root, "ghost"), root, false)
	// .. through a missing component cleans lexically back to root: cleaned
	// strings are equal, so it is the same directory.
	sgEdgeSame(t, filepath.Join(root, "ghost")+"/..", root, true)
}

// Amended D3: identity is os.SameFile when both sides stat, so a relative path
// EQUALS the absolute path of the same directory (resolved against the
// process' cwd). Relative and absolute spellings of paths that do not exist
// still compare by cleaned string, so they differ.
func TestSgEdgeSameDirRelativeVersusAbsolute(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sgEdgeSame(t, ".", wd, true)
	sgEdgeSame(t, wd, ".", true)
	sgEdgeSame(t, "./", ".", true)
	// A relative path reaching a temp dir from the cwd equals its absolute form.
	root, sub := sgEdgeDirs(t)
	if rel, err := filepath.Rel(wd, root); err == nil {
		sgEdgeSame(t, rel, root, true)
		sgEdgeSame(t, rel, sub, false)
	}
	// Non-existent: cleaned strings decide.
	sgEdgeSame(t, "sg-edge-rel/a/../b", "sg-edge-rel/b/", true)
	sgEdgeSame(t, "sg-edge-rel/b", "/sg-edge-rel/b", false)
	sgEdgeSame(t, "sg-edge-rel/b", filepath.Join(wd, "sg-edge-rel", "b"), false)
}

// Amended D3: a case-variant spelling on a case-insensitive volume names the
// same object. Skipped where the temp volume is case-sensitive.
func TestSgEdgeSameDirCaseVariantOnCaseInsensitiveVolume(t *testing.T) {
	root, _ := sgEdgeDirs(t)
	variant := filepath.Join(filepath.Dir(root), "WT-EDGE")
	if _, err := os.Stat(variant); err != nil {
		t.Skip("temp volume is case-sensitive")
	}
	sgEdgeSame(t, variant, root, true)
	sgEdgeSame(t, root, variant+"/", true)
	// And a guard payload from the variant spelling allows.
	sgEdgeWantAllow(t, strings.NewReader(sgEdgePayload(t, variant)), root, "case-variant cwd")
}

// A regular FILE: identity is by filesystem object, not dir-ness. A file
// equals itself and its symlink, and never its parent directory.
func TestSgEdgeSameDirFilePath(t *testing.T) {
	root, _ := sgEdgeDirs(t)
	f := filepath.Join(root, "notes.md")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "file-link")
	if err := os.Symlink(f, link); err != nil {
		t.Fatal(err)
	}
	sgEdgeSame(t, f, f, true)
	sgEdgeSame(t, link, f, true)
	sgEdgeSame(t, f, root, false)
	sgEdgeSame(t, root, f, false)
}

// Hostile inputs never panic.
func TestSgEdgeSameDirHostileNoPanic(t *testing.T) {
	for _, p := range [][2]string{
		{"a\x00b", "a\x00b"},
		{"\x00", "/"},
		{strings.Repeat("../", 4000), "/"},
		{strings.Repeat("x", 70000), "/"},
		{"\xff\xfe", "\xff\xfe/"},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("spawnGuardSameDir(%q…) panicked: %v", p[0][:min(len(p[0]), 16)], r)
				}
			}()
			_ = spawnGuardSameDir(p[0], p[1])
		}()
	}
	// Cleaned-string equality still holds for unresolvable junk.
	sgEdgeSame(t, "\xff\xfe", "\xff\xfe/", true)
}

// --- D4: spawnGuardCommand / spawnGuardHookEntry ------------------------------

func TestSgEdgeCommandExactConcatenation(t *testing.T) {
	for _, bin := range []string{"", "kraken", "/abs/my tools/kraken", "/abs/bin/", "/x/kraken spawn-guard"} {
		if got, want := spawnGuardCommand(bin), bin+" spawn-guard"; got != want {
			t.Fatalf("spawnGuardCommand(%q) = %q, want %q", bin, got, want)
		}
	}
}

func TestSgEdgeHookEntryShape(t *testing.T) {
	bin := "/abs/my tools/kraken"
	want := map[string]any{
		"matcher": "Agent",
		"hooks":   []any{map[string]any{"type": "command", "command": bin + " spawn-guard"}},
	}
	// Compare through JSON so []any vs []map[string]any does not matter.
	var got map[string]any
	if err := json.Unmarshal([]byte(sgEdgeJSON(t, spawnGuardHookEntry(bin))), &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("spawnGuardHookEntry(%q) = %v, want %v", bin, got, want)
	}
}

// --- D5: mergeHookSettings ---------------------------------------------------

const sgEdgeBin = "/sg-edge/bin/kraken"

func sgEdgeMerge(t *testing.T, existing []byte) map[string]any {
	t.Helper()
	out, err := mergeHookSettings(existing, "sg-edge-slug", sgEdgeBin)
	if err != nil {
		t.Fatalf("mergeHookSettings: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatalf("merged settings are not JSON: %v\n%s", err, out)
	}
	return parsed
}

// sgEdgeGroupsWithCommand returns the indices of groups in arr holding a hook
// whose command is cmd.
func sgEdgeGroupsWithCommand(arr []any, cmd string) []int {
	var idx []int
	for i, g := range arr {
		gm, ok := g.(map[string]any)
		if !ok {
			continue
		}
		hs, _ := gm["hooks"].([]any)
		for _, h := range hs {
			if hm, ok := h.(map[string]any); ok && hm["command"] == cmd {
				idx = append(idx, i)
				break
			}
		}
	}
	return idx
}

func sgEdgePre(t *testing.T, parsed map[string]any) []any {
	t.Helper()
	hooks, ok := parsed["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("merged settings have no hooks object: %v", parsed)
	}
	pre, ok := hooks["PreToolUse"].([]any)
	if !ok {
		t.Fatalf("merged hooks.PreToolUse is not an array: %#v", hooks["PreToolUse"])
	}
	return pre
}

// A non-array PreToolUse is the repo's own business and is left exactly as
// written (the existing ensureHookGroup rule) — no spawn-guard group forced in.
func TestSgEdgeMergeNonArrayPreToolUseUntouched(t *testing.T) {
	for name, raw := range map[string]string{
		"string": `"nonsense"`,
		"object": `{"matcher":"Agent"}`,
		"number": `3`,
	} {
		t.Run(name, func(t *testing.T) {
			parsed := sgEdgeMerge(t, []byte(`{"hooks":{"PreToolUse":`+raw+`}}`))
			var want any
			if err := json.Unmarshal([]byte(raw), &want); err != nil {
				t.Fatal(err)
			}
			got := parsed["hooks"].(map[string]any)["PreToolUse"]
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("non-array PreToolUse %s changed to %#v", raw, got)
			}
		})
	}
}

// A pre-existing Agent group with a DIFFERENT command (another binary path)
// survives in its place, and the new group is appended beside it.
func TestSgEdgeMergeDifferentAgentCommandSurvives(t *testing.T) {
	old := map[string]any{"matcher": "Agent", "hooks": []any{map[string]any{"type": "command", "command": "/old/place/kraken spawn-guard"}}}
	existing := `{"hooks":{"PreToolUse":[` + sgEdgeJSON(t, old) + `]}}`
	pre := sgEdgePre(t, sgEdgeMerge(t, []byte(existing)))
	if len(pre) == 0 || !reflect.DeepEqual(pre[0], old) {
		t.Fatalf("pre-existing Agent group not kept first and intact: %v", pre)
	}
	if idx := sgEdgeGroupsWithCommand(pre, spawnGuardCommand(sgEdgeBin)); len(idx) != 1 || idx[0] == 0 {
		t.Fatalf("new spawn-guard group not appended exactly once after the old one: %v", pre)
	}
}

// A pre-existing group with the EXACT command is not duplicated, on the first
// merge or on repeated merges.
func TestSgEdgeMergeExactCommandNotDuplicated(t *testing.T) {
	cmd := spawnGuardCommand(sgEdgeBin)
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Agent","hooks":[{"type":"command","command":` + sgEdgeJSON(t, cmd) + `}]}]}}`
	out := []byte(existing)
	for i := 0; i < 3; i++ {
		b, err := mergeHookSettings(out, "sg-edge-slug", sgEdgeBin)
		if err != nil {
			t.Fatal(err)
		}
		out = b
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		t.Fatal(err)
	}
	pre := sgEdgePre(t, parsed)
	if idx := sgEdgeGroupsWithCommand(pre, cmd); len(idx) != 1 || idx[0] != 0 {
		t.Fatalf("exact spawn-guard group duplicated or moved: %v", pre)
	}
}

// Assumption (existing append-if-absent rule: dedup keys on the command
// string): the exact command already present under ANOTHER matcher counts as
// present, so no second group is added.
func TestSgEdgeMergeExactCommandUnderOtherMatcherCountsAsPresent(t *testing.T) {
	cmd := spawnGuardCommand(sgEdgeBin)
	existing := `{"hooks":{"PreToolUse":[{"matcher":"Task","hooks":[{"type":"command","command":` + sgEdgeJSON(t, cmd) + `}]}]}}`
	pre := sgEdgePre(t, sgEdgeMerge(t, []byte(existing)))
	if idx := sgEdgeGroupsWithCommand(pre, cmd); len(idx) != 1 {
		t.Fatalf("spawn-guard command appears in %d groups, want 1: %v", len(idx), pre)
	}
}

// Other top-level keys and other hook kinds survive with identical content;
// bash-lint and spawn-guard both land in PreToolUse, bash-lint first.
func TestSgEdgeMergeOtherKeysAndKindsSurvive(t *testing.T) {
	permissions := map[string]any{"allow": []any{"Bash(go test:*)", "Read"}, "deny": []any{}}
	custom := map[string]any{"nested": map[string]any{"deep": []any{1.0, "two", nil, true}}}
	notification := []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": "./notify.sh"}}}}
	sessionEnd := []any{map[string]any{"matcher": "", "hooks": []any{map[string]any{"type": "command", "command": "echo bye"}}}}
	repoPre := map[string]any{"matcher": "Write", "hooks": []any{map[string]any{"type": "command", "command": "./guard-write.sh"}}}
	existing := sgEdgeJSON(t, map[string]any{
		"permissions": permissions,
		"model":       "sg-edge-model",
		"sgEdgeKey":   custom,
		"hooks": map[string]any{
			"Notification": notification,
			"SessionEnd":   sessionEnd,
			"PreToolUse":   []any{repoPre},
		},
	})
	parsed := sgEdgeMerge(t, []byte(existing))
	for key, want := range map[string]any{"permissions": permissions, "model": "sg-edge-model", "sgEdgeKey": custom} {
		if !reflect.DeepEqual(parsed[key], want) {
			t.Fatalf("top-level %q changed: got %#v want %#v", key, parsed[key], want)
		}
	}
	hooks := parsed["hooks"].(map[string]any)
	if !reflect.DeepEqual(hooks["Notification"], notification) {
		t.Fatalf("hooks.Notification changed: %#v", hooks["Notification"])
	}
	if !reflect.DeepEqual(hooks["SessionEnd"], sessionEnd) {
		t.Fatalf("hooks.SessionEnd changed: %#v", hooks["SessionEnd"])
	}
	pre := sgEdgePre(t, parsed)
	if !reflect.DeepEqual(pre[0], repoPre) {
		t.Fatalf("repo's own PreToolUse group not kept first and intact: %v", pre)
	}
	lint := sgEdgeGroupsWithCommand(pre, sgEdgeBin+" bash-lint")
	guard := sgEdgeGroupsWithCommand(pre, spawnGuardCommand(sgEdgeBin))
	if len(lint) != 1 || len(guard) != 1 {
		t.Fatalf("want one bash-lint and one spawn-guard group, got lint=%v guard=%v: %v", lint, guard, pre)
	}
	if lint[0] > guard[0] {
		t.Fatalf("spawn-guard group precedes bash-lint; contract D5 appends it AFTER the bash-lint ensure: %v", pre)
	}
	if g := pre[guard[0]].(map[string]any); g["matcher"] != "Agent" {
		t.Fatalf("spawn-guard group matcher = %#v, want Agent", g["matcher"])
	}
	if hs := pre[guard[0]].(map[string]any)["hooks"].([]any); len(hs) != 1 {
		t.Fatalf("spawn-guard group holds %d hooks, want exactly 1", len(hs))
	}
}

// The spawn-guard command carries no slug: two different slugs render the
// identical group.
func TestSgEdgeMergeCommandCarriesNoSlug(t *testing.T) {
	for _, slug := range []string{"sg-edge-a", "sg-edge-b"} {
		out, err := mergeHookSettings(nil, slug, sgEdgeBin)
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		if err := json.Unmarshal(out, &parsed); err != nil {
			t.Fatal(err)
		}
		pre := sgEdgePre(t, parsed)
		if idx := sgEdgeGroupsWithCommand(pre, sgEdgeBin+" spawn-guard"); len(idx) != 1 {
			t.Fatalf("slug %s: spawn-guard group with the slugless command not found once: %v", slug, pre)
		}
	}
}

// Unparseable existing settings remain an error — the guard adds no new path
// that would clobber them.
func TestSgEdgeMergeUnparseableStillErrors(t *testing.T) {
	if _, err := mergeHookSettings([]byte(`{"hooks":{"PreToolUse":[`), "sg-edge-slug", sgEdgeBin); err == nil {
		t.Fatalf("unparseable existing settings merged without error")
	}
}
