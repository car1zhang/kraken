package util

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// portability_edge_test.go — the EDGE dimension of Slice E's env-file loader
// (artifacts/50-contract.md, `util.EnvFilePath` / `util.ApplyEnvFile`):
// missing and unreadable files, malformed lines, whitespace, the allowlist's
// exact boundary, present-but-empty variables, `~` forms, duplicates, and the
// rule that a value never reaches the event log. Written blind from the
// contract text.

// edgeSandbox isolates HOME and the event log dir, so nothing this file does
// can reach the machine's real ~/.kraken.
func edgeSandbox(t *testing.T) (home, logs string) {
	t.Helper()
	home = t.TempDir()
	logs = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KRAKEN_LOGS_DIR", logs)
	return home, logs
}

// edgeUnset makes key ABSENT for the test and restores whatever the machine
// had afterwards (t.Setenv registers the restore, Unsetenv then removes it).
func edgeUnset(t *testing.T, keys ...string) {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "edge-placeholder")
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
	}
}

// edgeWriteEnv writes body to a fresh file and returns its path.
func edgeWriteEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func edgeLookup(t *testing.T, key string) (string, bool) {
	t.Helper()
	return os.LookupEnv(key)
}

// Contract: "returns filepath.Join(Home(), ".kraken", "env")" — read live from
// HOME, not cached at init.
func TestEdgeEnvFilePathFollowsHome(t *testing.T) {
	home, _ := edgeSandbox(t)
	if got, want := EnvFilePath(), filepath.Join(home, ".kraken", "env"); got != want {
		t.Fatalf("EnvFilePath() = %q, want %q", got, want)
	}
	other := t.TempDir()
	t.Setenv("HOME", other)
	if got, want := EnvFilePath(), filepath.Join(other, ".kraken", "env"); got != want {
		t.Fatalf("after HOME moved, EnvFilePath() = %q, want %q", got, want)
	}
}

// Contract: "missing or unreadable file → nil, no panic, nothing set".
func TestEdgeApplyEnvFileMissingFileIsNil(t *testing.T) {
	edgeSandbox(t)
	got := ApplyEnvFile(filepath.Join(t.TempDir(), "no-such-env"))
	if got != nil {
		t.Fatalf("missing file: applied = %#v, want nil", got)
	}
	if got := ApplyEnvFile(""); got != nil {
		t.Fatalf("empty path: applied = %#v, want nil", got)
	}
}

// A directory is the most portable "unreadable file".
func TestEdgeApplyEnvFileDirectoryIsNil(t *testing.T) {
	edgeSandbox(t)
	if got := ApplyEnvFile(t.TempDir()); got != nil {
		t.Fatalf("directory path: applied = %#v, want nil", got)
	}
}

// Contract: unreadable → nil and nothing set. A 0000 file (skipped as root,
// where permissions do not bind).
func TestEdgeApplyEnvFileUnreadableSetsNothing(t *testing.T) {
	edgeSandbox(t)
	if os.Geteuid() == 0 {
		t.Skip("root reads 0000 files")
	}
	edgeUnset(t, "KRAKEN_EDGE_UNREADABLE")
	p := edgeWriteEnv(t, "KRAKEN_EDGE_UNREADABLE=1\n")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	if got := ApplyEnvFile(p); got != nil {
		t.Fatalf("unreadable file: applied = %#v, want nil", got)
	}
	if _, ok := edgeLookup(t, "KRAKEN_EDGE_UNREADABLE"); ok {
		t.Fatal("unreadable file still set a variable")
	}
}

// Contract: an empty file and a comments-only file apply nothing. `applied`
// lists the keys actually set, so nothing set is an empty list.
func TestEdgeApplyEnvFileEmptyAndCommentOnly(t *testing.T) {
	edgeSandbox(t)
	for name, body := range map[string]string{
		"empty":         "",
		"blank lines":   "\n\n   \n\t\n",
		"comments only": "# KRAKEN_EDGE_C1=1\n   # KRAKEN_EDGE_C2=2\n\t#KRAKEN_EDGE_C3=3\n",
	} {
		t.Run(name, func(t *testing.T) {
			edgeUnset(t, "KRAKEN_EDGE_C1", "KRAKEN_EDGE_C2", "KRAKEN_EDGE_C3")
			got := ApplyEnvFile(edgeWriteEnv(t, body))
			if len(got) != 0 {
				t.Fatalf("applied = %#v, want nothing", got)
			}
			for _, k := range []string{"KRAKEN_EDGE_C1", "KRAKEN_EDGE_C2", "KRAKEN_EDGE_C3"} {
				if v, ok := edgeLookup(t, k); ok {
					t.Errorf("%s set to %q from a comment line", k, v)
				}
			}
		})
	}
}

// Contract: "lines whose first non-space byte is `#` are ignored" — leading
// spaces and tabs before the `#` included. A `#` AFTER a key is not a comment:
// it is part of the value (the value is everything after the first `=`).
func TestEdgeApplyEnvFileIndentedCommentAndInlineHash(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_HIDDEN", "KRAKEN_EDGE_HASHVAL")
	p := edgeWriteEnv(t, "    # KRAKEN_EDGE_HIDDEN=yes\nKRAKEN_EDGE_HASHVAL=a # not a comment\n")
	got := ApplyEnvFile(p)
	if _, ok := edgeLookup(t, "KRAKEN_EDGE_HIDDEN"); ok {
		t.Error("an indented # line was applied")
	}
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_HASHVAL"); v != "a # not a comment" {
		t.Errorf("KRAKEN_EDGE_HASHVAL = %q, want the whole tail after the first = (trimmed)", v)
	}
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_HASHVAL"}) {
		t.Errorf("applied = %#v, want only KRAKEN_EDGE_HASHVAL", got)
	}
}

// Contract: "a line with no `=` is ignored" — and (C1) a malformed line does
// not stop later lines.
func TestEdgeApplyEnvFileLineWithoutEqualsDoesNotStopLaterLines(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_NOEQ", "KRAKEN_EDGE_AFTER")
	p := edgeWriteEnv(t, "KRAKEN_EDGE_NOEQ\nthis is garbage\nKRAKEN_EDGE_AFTER=ok\n")
	got := ApplyEnvFile(p)
	if _, ok := edgeLookup(t, "KRAKEN_EDGE_NOEQ"); ok {
		t.Error("a line with no = set a variable")
	}
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_AFTER"); v != "ok" {
		t.Errorf("KRAKEN_EDGE_AFTER = %q, want ok — a malformed line stopped the parse", v)
	}
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_AFTER"}) {
		t.Errorf("applied = %#v, want [KRAKEN_EDGE_AFTER]", got)
	}
}

// Contract: "KEY is trimmed; VALUE is everything after the FIRST `=`, trimmed
// of surrounding whitespace, quotes NOT stripped".
func TestEdgeApplyEnvFileTrimsKeyAndValueKeepsQuotesAndLaterEquals(t *testing.T) {
	edgeSandbox(t)
	keys := []string{"KRAKEN_EDGE_TRIM", "KRAKEN_EDGE_EQ", "KRAKEN_EDGE_DQ", "KRAKEN_EDGE_SQ", "KRAKEN_EDGE_INNER"}
	edgeUnset(t, keys...)
	p := edgeWriteEnv(t, strings.Join([]string{
		"   KRAKEN_EDGE_TRIM \t=   spaced value \t",
		"KRAKEN_EDGE_EQ=a=b==c",
		`KRAKEN_EDGE_DQ="quoted"`,
		`KRAKEN_EDGE_SQ='single'`,
		"KRAKEN_EDGE_INNER=  in  ner  ",
	}, "\n")+"\n")
	got := ApplyEnvFile(p)
	want := map[string]string{
		"KRAKEN_EDGE_TRIM":  "spaced value",
		"KRAKEN_EDGE_EQ":    "a=b==c",
		"KRAKEN_EDGE_DQ":    `"quoted"`,
		"KRAKEN_EDGE_SQ":    `'single'`,
		"KRAKEN_EDGE_INNER": "in  ner",
	}
	for k, w := range want {
		if v, ok := edgeLookup(t, k); !ok || v != w {
			t.Errorf("%s = (%q, %v), want %q", k, v, ok, w)
		}
	}
	if !reflect.DeepEqual(got, keys) {
		t.Errorf("applied = %#v, want %#v in file order", got, keys)
	}
}

// CRLF files (written on another machine, or by a Windows editor): the
// trailing \r is surrounding whitespace of the value and must not survive
// into it. The contract says the value is "trimmed of surrounding whitespace".
func TestEdgeApplyEnvFileCRLFLineEndings(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_CRLF1", "KRAKEN_EDGE_CRLF2")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_CRLF1=one\r\nKRAKEN_EDGE_CRLF2=two\r\n"))
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_CRLF1"); v != "one" {
		t.Errorf("KRAKEN_EDGE_CRLF1 = %q, want one (no \\r)", v)
	}
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_CRLF2"); v != "two" {
		t.Errorf("KRAKEN_EDGE_CRLF2 = %q, want two (no \\r)", v)
	}
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_CRLF1", "KRAKEN_EDGE_CRLF2"}) {
		t.Errorf("applied = %#v", got)
	}
}

// A file with no trailing newline still applies its last line.
func TestEdgeApplyEnvFileNoTrailingNewline(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_LAST")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_LAST=tail"))
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_LAST"); v != "tail" {
		t.Errorf("KRAKEN_EDGE_LAST = %q, want tail", v)
	}
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_LAST"}) {
		t.Errorf("applied = %#v", got)
	}
}

// Contract: "applied only when os.LookupEnv(KEY) reports absent
// (present-and-empty counts as present, and wins)".
func TestEdgeApplyEnvFilePresentButEmptyWins(t *testing.T) {
	edgeSandbox(t)
	t.Setenv("KRAKEN_EDGE_EMPTY", "")
	t.Setenv("KRAKEN_EDGE_SET", "from-shell")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_EMPTY=from-file\nKRAKEN_EDGE_SET=from-file\n"))
	if v, ok := edgeLookup(t, "KRAKEN_EDGE_EMPTY"); !ok || v != "" {
		t.Errorf("KRAKEN_EDGE_EMPTY = (%q, %v), want the present empty value to win", v, ok)
	}
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_SET"); v != "from-shell" {
		t.Errorf("KRAKEN_EDGE_SET = %q, want the shell's value to win", v)
	}
	if len(got) != 0 {
		t.Errorf("applied = %#v, want nothing — neither key was actually set", got)
	}
}

// An empty VALUE for an absent KEY: the line is `KEY=VALUE` with VALUE "", the
// key is absent, so it is applied (as present-and-empty).
func TestEdgeApplyEnvFileEmptyValueIsApplied(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_BLANKVAL")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_BLANKVAL=   \n"))
	if v, ok := edgeLookup(t, "KRAKEN_EDGE_BLANKVAL"); !ok || v != "" {
		t.Errorf("KRAKEN_EDGE_BLANKVAL = (%q, %v), want present and empty", v, ok)
	}
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_BLANKVAL"}) {
		t.Errorf("applied = %#v", got)
	}
}

// Contract allowlist: "KEY starting `KRAKEN_`, or exactly `CLAUDE_POOL_DIR`,
// `CODEX_POOL_DIR`, `PATH`; any other KEY is ignored and not returned". Each
// row is one side of that boundary.
func TestEdgeApplyEnvFileAllowlistBoundary(t *testing.T) {
	edgeSandbox(t)
	rejected := []string{
		"KRAKEN",                // no underscore: not a KRAKEN_ prefix
		"kraken_edge_lower",     // case: the prefix is upper-case
		"XKRAKEN_EDGE",          // prefix must be at the start
		"CLAUDE_POOL_DIRX",      // "exactly", not a prefix
		"CLAUDE_POOL",           // nor a prefix of it
		"CODEX_POOL_DIR_EDGE",   // "exactly"
		"PATHX",                 // "exactly"
		"path",                  // case
		"LINEAR_API_KEY",        // a credential that has its own file
		"SLACK_USER_TOKEN",      // ditto
		"EDGE_UNRELATED",        // anything else
		"export KRAKEN_EDGE_EX", // shell syntax is not parsed: the KEY is "export KRAKEN_EDGE_EX"
	}
	for _, k := range []string{"KRAKEN", "kraken_edge_lower", "XKRAKEN_EDGE", "CLAUDE_POOL_DIRX", "CLAUDE_POOL",
		"CODEX_POOL_DIR_EDGE", "PATHX", "LINEAR_API_KEY", "SLACK_USER_TOKEN", "EDGE_UNRELATED", "KRAKEN_EDGE_EX"} {
		edgeUnset(t, k)
	}
	var b strings.Builder
	for _, k := range rejected {
		if k == "path" {
			continue // PATH and path are one variable on case-insensitive systems; never touch it here
		}
		b.WriteString(k + "=edge-should-not-apply\n")
	}
	got := ApplyEnvFile(edgeWriteEnv(t, b.String()))
	if len(got) != 0 {
		t.Fatalf("applied = %#v, want nothing — every key was outside the allowlist", got)
	}
	for _, k := range []string{"KRAKEN", "kraken_edge_lower", "XKRAKEN_EDGE", "CLAUDE_POOL_DIRX", "CLAUDE_POOL",
		"CODEX_POOL_DIR_EDGE", "PATHX", "LINEAR_API_KEY", "SLACK_USER_TOKEN", "EDGE_UNRELATED", "KRAKEN_EDGE_EX"} {
		if v, ok := edgeLookup(t, k); ok {
			t.Errorf("%s was set to %q, want it ignored", k, v)
		}
	}
}

// The other side: the bare `KRAKEN_` prefix, and the three exact names.
func TestEdgeApplyEnvFileAllowlistAccepts(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_", "CLAUDE_POOL_DIR", "CODEX_POOL_DIR")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_=bare\nCLAUDE_POOL_DIR=/edge/claude\nCODEX_POOL_DIR=/edge/codex\n"))
	want := []string{"KRAKEN_", "CLAUDE_POOL_DIR", "CODEX_POOL_DIR"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %#v, want %#v", got, want)
	}
	if v, _ := edgeLookup(t, "CLAUDE_POOL_DIR"); v != "/edge/claude" {
		t.Errorf("CLAUDE_POOL_DIR = %q", v)
	}
	if v, _ := edgeLookup(t, "CODEX_POOL_DIR"); v != "/edge/codex" {
		t.Errorf("CODEX_POOL_DIR = %q", v)
	}
}

// Amended contract: "PATH is the one exception to the presence rule: the
// file's PATH is PREPENDED to the inherited PATH as `<file value>:<inherited>`
// whenever the file names one, or set as given when no PATH is inherited, and
// PATH is then listed in applied". Boundaries: exactly one `:` joins the two
// (no doubling, no reordering), the other keys still yield, a duplicate PATH
// line prepends only its FIRST value, and a `~/` PATH value is expanded first.
func TestEdgeApplyEnvFilePathPrependsToInherited(t *testing.T) {
	home, _ := edgeSandbox(t)
	t.Setenv("PATH", "/edge/inherited:/usr/bin")
	t.Setenv("KRAKEN_EDGE_PRESENT", "shell")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_PRESENT=file\nPATH=/edge/from-file\nPATH=/edge/second\n"))
	if v := os.Getenv("PATH"); v != "/edge/from-file:/edge/inherited:/usr/bin" {
		t.Errorf("PATH = %q, want exactly <file>:<inherited>", v)
	}
	if v := os.Getenv("KRAKEN_EDGE_PRESENT"); v != "shell" {
		t.Errorf("KRAKEN_EDGE_PRESENT = %q, want the present value to win — only PATH is the exception", v)
	}
	if !reflect.DeepEqual(got, []string{"PATH"}) {
		t.Errorf("applied = %#v, want [PATH] once", got)
	}

	t.Setenv("PATH", "/edge/inherited")
	got = ApplyEnvFile(edgeWriteEnv(t, "PATH=~/bin\n"))
	if v := os.Getenv("PATH"); v != home+"/bin:/edge/inherited" {
		t.Errorf("tilde PATH = %q, want %q", v, home+"/bin:/edge/inherited")
	}
	if !reflect.DeepEqual(got, []string{"PATH"}) {
		t.Errorf("applied = %#v, want [PATH]", got)
	}

	// a file without PATH leaves the inherited PATH untouched
	t.Setenv("PATH", "/edge/inherited")
	edgeUnset(t, "KRAKEN_EDGE_NOPATH")
	ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_NOPATH=1\n"))
	if v := os.Getenv("PATH"); v != "/edge/inherited" {
		t.Errorf("a PATH-less file changed PATH to %q", v)
	}

	edgeUnset(t, "PATH")
	got = ApplyEnvFile(edgeWriteEnv(t, "PATH=/edge/from-file:/usr/bin\n"))
	if v, ok := edgeLookup(t, "PATH"); !ok || v != "/edge/from-file:/usr/bin" {
		t.Errorf("absent PATH: got (%q, %v), want the file's PATH", v, ok)
	}
	if !reflect.DeepEqual(got, []string{"PATH"}) {
		t.Errorf("applied = %#v, want [PATH]", got)
	}
}

// Contract: "a VALUE equal to `~` or starting `~/` has that prefix replaced by
// Home(); `$VAR` is not expanded". Everything else with a tilde is literal.
func TestEdgeApplyEnvFileTildeForms(t *testing.T) {
	home, _ := edgeSandbox(t)
	cases := []struct{ key, raw, want string }{
		{"KRAKEN_EDGE_T1", "~", home},
		{"KRAKEN_EDGE_T2", "~/", home + "/"},
		{"KRAKEN_EDGE_T3", "~/Code/repos", home + "/Code/repos"},
		{"KRAKEN_EDGE_T4", "~other/x", "~other/x"},
		{"KRAKEN_EDGE_T5", "~x", "~x"},
		{"KRAKEN_EDGE_T6", "a/~/b", "a/~/b"},
		{"KRAKEN_EDGE_T7", "$HOME/x", "$HOME/x"},
		{"KRAKEN_EDGE_T8", "${HOME}/x", "${HOME}/x"},
		{"KRAKEN_EDGE_T9", "  ~/padded  ", home + "/padded"},
		{"KRAKEN_EDGE_T10", `"~/quoted"`, `"~/quoted"`},
		{"KRAKEN_EDGE_T11", "~~/x", "~~/x"},
	}
	var b strings.Builder
	for _, c := range cases {
		edgeUnset(t, c.key)
		b.WriteString(c.key + "=" + c.raw + "\n")
	}
	ApplyEnvFile(edgeWriteEnv(t, b.String()))
	for _, c := range cases {
		if v, ok := edgeLookup(t, c.key); !ok || v != c.want {
			t.Errorf("%s from %q = (%q, %v), want %q", c.key, c.raw, v, ok, c.want)
		}
	}
}

// Contract: "the first occurrence of a KEY in the file wins over later ones"
// and `applied` lists the key once, at its first position.
func TestEdgeApplyEnvFileDuplicateKeyFirstWins(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_DUP", "KRAKEN_EDGE_MID")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_DUP=first\nKRAKEN_EDGE_MID=m\nKRAKEN_EDGE_DUP=second\n  KRAKEN_EDGE_DUP = third\n"))
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_DUP"); v != "first" {
		t.Errorf("KRAKEN_EDGE_DUP = %q, want first", v)
	}
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_DUP", "KRAKEN_EDGE_MID"}) {
		t.Errorf("applied = %#v, want [KRAKEN_EDGE_DUP KRAKEN_EDGE_MID] — one entry per key, file order", got)
	}
}

// A duplicate whose FIRST occurrence lost to the environment does not let a
// later occurrence through either: the environment won, the file's later
// lines are no different.
func TestEdgeApplyEnvFileDuplicateOfPresentKeyStaysPresent(t *testing.T) {
	edgeSandbox(t)
	t.Setenv("KRAKEN_EDGE_DUPENV", "shell")
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_DUPENV=a\nKRAKEN_EDGE_DUPENV=b\n"))
	if v := os.Getenv("KRAKEN_EDGE_DUPENV"); v != "shell" {
		t.Errorf("KRAKEN_EDGE_DUPENV = %q, want shell", v)
	}
	if len(got) != 0 {
		t.Errorf("applied = %#v, want nothing", got)
	}
}

// A line `=value` has an empty KEY, which is outside the allowlist.
func TestEdgeApplyEnvFileEmptyKeyIgnored(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_OK")
	got := ApplyEnvFile(edgeWriteEnv(t, "=orphan\n   =also\nKRAKEN_EDGE_OK=1\n"))
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_OK"}) {
		t.Errorf("applied = %#v, want [KRAKEN_EDGE_OK]", got)
	}
}

// Re-reading is a no-op (C1 idempotency): the second call finds every key
// present and applies nothing.
func TestEdgeApplyEnvFileSecondReadAppliesNothing(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_TWICE")
	p := edgeWriteEnv(t, "KRAKEN_EDGE_TWICE=1\n")
	if got := ApplyEnvFile(p); !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_TWICE"}) {
		t.Fatalf("first read applied %#v", got)
	}
	if got := ApplyEnvFile(p); len(got) != 0 {
		t.Fatalf("second read applied %#v, want nothing", got)
	}
}

// 52-testing-plan "a value is never logged": after a load, no file in the
// event-log dir carries any value from the env file, allowlisted or not.
func TestEdgeApplyEnvFileNeverLogsAValue(t *testing.T) {
	_, logs := edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_SECRETISH", "EDGE_NOT_ALLOWED")
	const v1 = "edge-value-7f3a91c2-allowed"
	const v2 = "edge-value-0b44de19-refused"
	ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_SECRETISH="+v1+"\nEDGE_NOT_ALLOWED="+v2+"\n"))
	err := filepath.Walk(logs, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		for _, v := range []string{v1, v2} {
			if strings.Contains(string(data), v) {
				t.Errorf("event log %s carries the value %q", p, v)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------
// Review-round-1 amendments (50-contract.md, "Review-round-1 amendments")
// ---------------------------------------------------------------------------

// edgeLogsText concatenates every file under the event-log dir.
func edgeLogsText(t *testing.T, logs string) string {
	t.Helper()
	var b strings.Builder
	_ = filepath.Walk(logs, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr == nil {
			b.Write(data)
		}
		return nil
	})
	return b.String()
}

// Amended: "a UTF-8 BOM at the start of the file is ignored" — the first key
// is read as if the BOM were not there.
func TestEdgeApplyEnvFileLeadingBOMIgnored(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_BOM", "KRAKEN_EDGE_AFTERBOM")
	got := ApplyEnvFile(edgeWriteEnv(t, "\ufeffKRAKEN_EDGE_BOM=1\nKRAKEN_EDGE_AFTERBOM=2\n"))
	if v, ok := edgeLookup(t, "KRAKEN_EDGE_BOM"); !ok || v != "1" {
		t.Errorf("KRAKEN_EDGE_BOM = (%q, %v), want 1 — the BOM must not become part of the first key", v, ok)
	}
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_BOM", "KRAKEN_EDGE_AFTERBOM"}) {
		t.Errorf("applied = %#v", got)
	}
}

// A BOM before a COMMENT first line must not turn the comment into a key.
func TestEdgeApplyEnvFileBOMBeforeComment(t *testing.T) {
	edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_BOMC")
	got := ApplyEnvFile(edgeWriteEnv(t, "\ufeff# KRAKEN_EDGE_BOMC=1\n"))
	if len(got) != 0 {
		t.Errorf("applied = %#v from a BOM + comment line, want nothing", got)
	}
	if _, ok := edgeLookup(t, "KRAKEN_EDGE_BOMC"); ok {
		t.Error("a commented key behind a BOM was applied")
	}
}

// Amended: "a line the scanner cannot read (over its limit) stops the read
// with one log line naming the line number, never silently"; keys before it
// stay applied, keys after it are not reached.
func TestEdgeApplyEnvFileOverlongLineStopsWithALogLine(t *testing.T) {
	_, logs := edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_BEFORE", "KRAKEN_EDGE_HUGE", "KRAKEN_EDGE_AFTERHUGE")
	huge := "KRAKEN_EDGE_HUGE=" + strings.Repeat("v", 2<<20)
	got := ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_EDGE_BEFORE=1\n"+huge+"\nKRAKEN_EDGE_AFTERHUGE=3\n"))
	if !reflect.DeepEqual(got, []string{"KRAKEN_EDGE_BEFORE"}) {
		t.Errorf("applied = %#v, want only the key before the overlong line", got)
	}
	if _, ok := edgeLookup(t, "KRAKEN_EDGE_AFTERHUGE"); ok {
		t.Error("a key after the overlong line was applied — the read must stop there")
	}
	text := edgeLogsText(t, logs)
	if !strings.Contains(text, "line 2") {
		t.Errorf("no log line naming line 2; logs:\n%s", text)
	}
	if strings.Contains(text, strings.Repeat("v", 64)) {
		t.Error("the log carries the overlong value")
	}
}

// Amended: "empty elements in the file's PATH value are dropped".
func TestEdgeApplyEnvFilePathDropsEmptyElements(t *testing.T) {
	edgeSandbox(t)
	t.Setenv("PATH", "/edge/inherited")
	got := ApplyEnvFile(edgeWriteEnv(t, "PATH=:/edge/a::/edge/b:\n"))
	if v := os.Getenv("PATH"); v != "/edge/a:/edge/b:/edge/inherited" {
		t.Errorf("PATH = %q, want empty elements dropped before the prepend", v)
	}
	if !reflect.DeepEqual(got, []string{"PATH"}) {
		t.Errorf("applied = %#v", got)
	}
}

// A file PATH made ONLY of empty elements (`PATH=`, `PATH=::`) adds nothing:
// the inherited PATH is unchanged and gains no leading or trailing colon.
func TestEdgeApplyEnvFilePathAllEmptyChangesNothing(t *testing.T) {
	for _, raw := range []string{"PATH=\n", "PATH=::\n", "PATH= : \n"} {
		edgeSandbox(t)
		t.Setenv("PATH", "/edge/inherited")
		ApplyEnvFile(edgeWriteEnv(t, raw))
		v := os.Getenv("PATH")
		if strings.HasPrefix(v, ":") || strings.HasSuffix(v, ":") || strings.Contains(v, "::") {
			t.Errorf("%q: PATH = %q carries an empty element (the current directory)", raw, v)
		}
		if raw != "PATH= : \n" && v != "/edge/inherited" {
			t.Errorf("%q: PATH = %q, want the inherited PATH untouched", raw, v)
		}
	}
}

// Amended: "the file's PATH is NOT prepended again when the inherited PATH
// already starts with it" — the check is per ELEMENT: a longer element that
// merely shares the text as a prefix is not "already starts with it".
func TestEdgeApplyEnvFilePathNoRepeatPrepend(t *testing.T) {
	edgeSandbox(t)
	t.Setenv("PATH", "/edge/a:/edge/b:/usr/bin")
	got := ApplyEnvFile(edgeWriteEnv(t, "PATH=/edge/a:/edge/b\n"))
	if v := os.Getenv("PATH"); v != "/edge/a:/edge/b:/usr/bin" {
		t.Errorf("PATH = %q, want no second copy of an already-leading file PATH", v)
	}
	if len(got) != 0 {
		t.Errorf("applied = %#v, want nothing when PATH is unchanged", got)
	}

	t.Setenv("PATH", "/edge/a")
	got = ApplyEnvFile(edgeWriteEnv(t, "PATH=/edge/a\n"))
	if v := os.Getenv("PATH"); v != "/edge/a" {
		t.Errorf("PATH equal to the file value: got %q, want unchanged", v)
	}
	if len(got) != 0 {
		t.Errorf("applied = %#v, want nothing", got)
	}

	t.Setenv("PATH", "/edge/a:/edge/bc:/usr/bin")
	ApplyEnvFile(edgeWriteEnv(t, "PATH=/edge/a:/edge/b\n"))
	if v := os.Getenv("PATH"); v != "/edge/a:/edge/b:/edge/a:/edge/bc:/usr/bin" {
		t.Errorf("PATH = %q — /edge/bc is not /edge/b, so the file PATH must be prepended", v)
	}

	// a nested kraken process re-reading the file after a prepend is a no-op
	t.Setenv("PATH", "/edge/inherited")
	p := edgeWriteEnv(t, "PATH=/edge/x\n")
	ApplyEnvFile(p)
	ApplyEnvFile(p)
	if v := os.Getenv("PATH"); v != "/edge/x:/edge/inherited" {
		t.Errorf("after two reads PATH = %q, want exactly one prepend", v)
	}
}

// Amended: "a value wrapped in matching ' or " quotes is applied as-is but
// logs one line naming the KEY and saying quotes were kept; the value is
// never logged". Mismatched or one-sided quotes are not "wrapped".
func TestEdgeApplyEnvFileQuotedValueLogsKeyNotValue(t *testing.T) {
	_, logs := edgeSandbox(t)
	edgeUnset(t, "KRAKEN_EDGE_DQW", "KRAKEN_EDGE_SQW", "KRAKEN_EDGE_MISMATCH", "KRAKEN_EDGE_ONESIDED")
	const secret = "edge-quoted-val-4c1e"
	got := ApplyEnvFile(edgeWriteEnv(t,
		`KRAKEN_EDGE_DQW="`+secret+`"`+"\n"+
			`KRAKEN_EDGE_SQW='`+secret+`'`+"\n"+
			`KRAKEN_EDGE_MISMATCH="abc'`+"\n"+
			`KRAKEN_EDGE_ONESIDED="abc`+"\n"))
	if len(got) != 4 {
		t.Fatalf("applied = %#v, want all four (quotes never block a value)", got)
	}
	if v, _ := edgeLookup(t, "KRAKEN_EDGE_DQW"); v != `"`+secret+`"` {
		t.Errorf("KRAKEN_EDGE_DQW = %q, want the quotes kept", v)
	}
	text := edgeLogsText(t, logs)
	for _, k := range []string{"KRAKEN_EDGE_DQW", "KRAKEN_EDGE_SQW"} {
		if !strings.Contains(text, k) {
			t.Errorf("no log line names %s", k)
		}
	}
	for _, k := range []string{"KRAKEN_EDGE_MISMATCH", "KRAKEN_EDGE_ONESIDED"} {
		if strings.Contains(text, k) {
			t.Errorf("%s is not wrapped in matching quotes but was logged", k)
		}
	}
	if strings.Contains(text, secret) {
		t.Error("a quoted value reached the log")
	}
}

// Amended (jobs.repoRoot row): "ApplyEnvFile logs one line naming the key when
// it applies a relative value for it" — and an absolute one is not logged.
func TestEdgeApplyEnvFileRelativeReposDirLogsTheKey(t *testing.T) {
	_, logs := edgeSandbox(t)
	edgeUnset(t, "KRAKEN_REPOS_DIR")
	ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_REPOS_DIR=Code/repos\n"))
	if v := os.Getenv("KRAKEN_REPOS_DIR"); v != "Code/repos" {
		t.Errorf("KRAKEN_REPOS_DIR = %q, want the relative value applied as written", v)
	}
	if text := edgeLogsText(t, logs); !strings.Contains(text, "KRAKEN_REPOS_DIR") {
		t.Errorf("no log line names KRAKEN_REPOS_DIR for a relative value; logs:\n%s", text)
	}

	_, logs2 := edgeSandbox(t)
	edgeUnset(t, "KRAKEN_REPOS_DIR")
	ApplyEnvFile(edgeWriteEnv(t, "KRAKEN_REPOS_DIR=/edge/abs\n"))
	if text := edgeLogsText(t, logs2); strings.Contains(text, "KRAKEN_REPOS_DIR") {
		t.Errorf("an absolute KRAKEN_REPOS_DIR was logged as relative:\n%s", text)
	}
}
