package util

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// portability_contract_test.go — athena-logic's BLIND contract tests for the
// portability audit's Slice E env-file half (artifacts/50-contract.md rows
// `util.EnvFilePath` and `util.ApplyEnvFile`). Written from the contract text
// alone; env.go was never opened by the author. Dimension: LOGIC — the stated
// semantics and happy paths. Boundaries and error paths are athena-edge's.

// contractEnvHome isolates HOME for one test and returns it.
func contractEnvHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return home
}

// contractUnset makes key ABSENT for the duration of the test (t.Setenv first,
// so the original value is restored on cleanup, then a real unset — the
// contract distinguishes absent from present-and-empty).
func contractUnset(t *testing.T, key string) {
	t.Helper()
	t.Setenv(key, "")
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
}

// contractWriteEnvFile writes body to a fresh file and returns its path.
func contractWriteEnvFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "env")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	return p
}

// Contract: `util.EnvFilePath` "returns filepath.Join(Home(), ".kraken", "env")".
func TestContractPortabilityEnvFilePath(t *testing.T) {
	home := contractEnvHome(t)
	want := filepath.Join(home, ".kraken", "env")
	if got := EnvFilePath(); got != want {
		t.Fatalf("EnvFilePath() = %q, want %q", got, want)
	}
	if got := EnvFilePath(); got != filepath.Join(Home(), ".kraken", "env") {
		t.Fatalf("EnvFilePath() = %q, want it derived from Home()", got)
	}
}

// Contract: "missing or unreadable file → nil, no panic, nothing set".
func TestContractPortabilityApplyEnvFileMissingFileIsNil(t *testing.T) {
	contractEnvHome(t)
	contractUnset(t, "KRAKEN_CONTRACT_MISSING")
	got := ApplyEnvFile(filepath.Join(t.TempDir(), "does-not-exist"))
	if got != nil {
		t.Fatalf("ApplyEnvFile(missing) = %#v, want nil", got)
	}
	if _, ok := os.LookupEnv("KRAKEN_CONTRACT_MISSING"); ok {
		t.Fatal("a missing file set an environment variable")
	}
}

// Contract: "blank lines and lines whose first non-space byte is `#` are
// ignored · a line with no `=` is ignored · `KEY=VALUE`: KEY is trimmed; VALUE
// is everything after the FIRST `=`, trimmed of surrounding whitespace, quotes
// NOT stripped" and "`applied` lists the keys actually set, in file order".
func TestContractPortabilityApplyEnvFileParsesLines(t *testing.T) {
	contractEnvHome(t)
	for _, k := range []string{"KRAKEN_C_A", "KRAKEN_C_B", "KRAKEN_C_C", "KRAKEN_C_D", "KRAKEN_C_COMMENTED", "KRAKEN_C_NOEQ"} {
		contractUnset(t, k)
	}
	body := "" +
		"\n" +
		"   \n" +
		"# KRAKEN_C_COMMENTED=no\n" +
		"   # KRAKEN_C_COMMENTED=still-no\n" +
		"KRAKEN_C_NOEQ\n" +
		"  KRAKEN_C_A  =  alpha value  \n" +
		"KRAKEN_C_B=x=y=z\n" +
		"KRAKEN_C_C=\"quoted\"\n" +
		"KRAKEN_C_D='single'\n"
	got := ApplyEnvFile(contractWriteEnvFile(t, body))

	want := []string{"KRAKEN_C_A", "KRAKEN_C_B", "KRAKEN_C_C", "KRAKEN_C_D"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %#v, want %#v (file order, only keys actually set)", got, want)
	}
	for key, val := range map[string]string{
		"KRAKEN_C_A": "alpha value",
		"KRAKEN_C_B": "x=y=z",
		"KRAKEN_C_C": `"quoted"`,
		"KRAKEN_C_D": `'single'`,
	} {
		if v, ok := os.LookupEnv(key); !ok || v != val {
			t.Errorf("%s = (%q, %v), want %q", key, v, ok, val)
		}
	}
	for _, key := range []string{"KRAKEN_C_COMMENTED", "KRAKEN_C_NOEQ"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Errorf("%s was set from a comment or a line with no `=`", key)
		}
	}
}

// Contract: "applied only when os.LookupEnv(KEY) reports absent
// (present-and-empty counts as present, and wins)".
func TestContractPortabilityApplyEnvFileEnvironmentWins(t *testing.T) {
	contractEnvHome(t)
	t.Setenv("KRAKEN_C_PRESENT", "from-shell")
	t.Setenv("KRAKEN_C_EMPTY", "")
	contractUnset(t, "KRAKEN_C_ABSENT")
	got := ApplyEnvFile(contractWriteEnvFile(t,
		"KRAKEN_C_PRESENT=from-file\nKRAKEN_C_EMPTY=from-file\nKRAKEN_C_ABSENT=from-file\n"))

	if want := []string{"KRAKEN_C_ABSENT"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %#v, want %#v", got, want)
	}
	if v := os.Getenv("KRAKEN_C_PRESENT"); v != "from-shell" {
		t.Errorf("KRAKEN_C_PRESENT = %q, want the environment's value to win", v)
	}
	if v, ok := os.LookupEnv("KRAKEN_C_EMPTY"); !ok || v != "" {
		t.Errorf("KRAKEN_C_EMPTY = (%q, %v), want present-and-empty to win", v, ok)
	}
	if v := os.Getenv("KRAKEN_C_ABSENT"); v != "from-file" {
		t.Errorf("KRAKEN_C_ABSENT = %q, want from-file", v)
	}
}

// Contract: "allowlist: KEY starting KRAKEN_, or exactly CLAUDE_POOL_DIR,
// CODEX_POOL_DIR, PATH; any other KEY is ignored and not returned". PATH from
// the file replaces the (absent) inherited one (C1 unit obligation).
func TestContractPortabilityApplyEnvFileAllowlist(t *testing.T) {
	contractEnvHome(t)
	for _, k := range []string{"KRAKEN_C_ALLOWED", "CLAUDE_POOL_DIR", "CODEX_POOL_DIR", "PATH", "CONTRACT_OTHER", "LINEAR_API_KEY", "CLAUDE_POOL_DIRX", "XKRAKEN_C"} {
		contractUnset(t, k)
	}
	body := "" +
		"CONTRACT_OTHER=nope\n" +
		"KRAKEN_C_ALLOWED=yes\n" +
		"LINEAR_API_KEY=secret\n" +
		"CLAUDE_POOL_DIR=/pool/claude\n" +
		"CLAUDE_POOL_DIRX=nope\n" +
		"XKRAKEN_C=nope\n" +
		"CODEX_POOL_DIR=/pool/codex\n" +
		"PATH=/opt/contract/bin:/usr/bin\n"
	got := ApplyEnvFile(contractWriteEnvFile(t, body))

	want := []string{"KRAKEN_C_ALLOWED", "CLAUDE_POOL_DIR", "CODEX_POOL_DIR", "PATH"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %#v, want %#v", got, want)
	}
	for key, val := range map[string]string{
		"KRAKEN_C_ALLOWED": "yes",
		"CLAUDE_POOL_DIR":  "/pool/claude",
		"CODEX_POOL_DIR":   "/pool/codex",
		"PATH":             "/opt/contract/bin:/usr/bin",
	} {
		if v := os.Getenv(key); v != val {
			t.Errorf("%s = %q, want %q", key, v, val)
		}
	}
	for _, key := range []string{"CONTRACT_OTHER", "LINEAR_API_KEY", "CLAUDE_POOL_DIRX", "XKRAKEN_C"} {
		if _, ok := os.LookupEnv(key); ok {
			t.Errorf("%s is outside the allowlist but was set", key)
		}
	}
}

// Contract (amended at the join, Q14): "PATH is the one exception to the
// presence rule: the file's PATH is PREPENDED to the inherited PATH as
// <file value>:<inherited> whenever the file names one, … and PATH is then
// listed in applied; every other key still yields to a present environment
// value". (The absent-PATH "set as given" half is in the Allowlist test.)
func TestContractPortabilityApplyEnvFilePathPrepends(t *testing.T) {
	contractEnvHome(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv("KRAKEN_C_KEEP", "from-shell")
	got := ApplyEnvFile(contractWriteEnvFile(t, "KRAKEN_C_KEEP=from-file\nPATH=/opt/contract/bin\n"))

	if want := []string{"PATH"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %#v, want %#v (PATH listed; the present KRAKEN_ key yields)", got, want)
	}
	if v := os.Getenv("PATH"); v != "/opt/contract/bin:/usr/bin:/bin" {
		t.Errorf("PATH = %q, want the file's value prepended: /opt/contract/bin:/usr/bin:/bin", v)
	}
	if v := os.Getenv("KRAKEN_C_KEEP"); v != "from-shell" {
		t.Errorf("KRAKEN_C_KEEP = %q, want the environment's value to win", v)
	}
}

// Contract (review-round-1 amendment): "a UTF-8 BOM at the start of the file
// is ignored" — the first line's key is read as if the BOM were not there.
func TestContractPortabilityApplyEnvFileIgnoresBOM(t *testing.T) {
	contractEnvHome(t)
	contractUnset(t, "KRAKEN_C_BOM")
	contractUnset(t, "KRAKEN_C_AFTERBOM")
	got := ApplyEnvFile(contractWriteEnvFile(t, "\xef\xbb\xbfKRAKEN_C_BOM=first\nKRAKEN_C_AFTERBOM=second\n"))
	if want := []string{"KRAKEN_C_BOM", "KRAKEN_C_AFTERBOM"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %#v, want %#v", got, want)
	}
	if v := os.Getenv("KRAKEN_C_BOM"); v != "first" {
		t.Errorf("KRAKEN_C_BOM = %q, want first", v)
	}
}

// Contract (amended): "empty elements in the file's PATH value are dropped,
// and the file's PATH is NOT prepended again when the inherited PATH already
// starts with it".
func TestContractPortabilityApplyEnvFilePathElementsAndNoRepeat(t *testing.T) {
	contractEnvHome(t)
	t.Setenv("PATH", "/usr/bin:/bin")
	ApplyEnvFile(contractWriteEnvFile(t, "PATH=:/opt/a/bin::/opt/b/bin:\n"))
	if v := os.Getenv("PATH"); v != "/opt/a/bin:/opt/b/bin:/usr/bin:/bin" {
		t.Errorf("PATH = %q, want empty elements dropped: /opt/a/bin:/opt/b/bin:/usr/bin:/bin", v)
	}

	// a child kraken process inherits the already-prepended PATH: no repeat
	t.Setenv("PATH", "/opt/a/bin:/opt/b/bin:/usr/bin:/bin")
	ApplyEnvFile(contractWriteEnvFile(t, "PATH=/opt/a/bin:/opt/b/bin\n"))
	if v := os.Getenv("PATH"); v != "/opt/a/bin:/opt/b/bin:/usr/bin:/bin" {
		t.Errorf("PATH = %q, want it unchanged when it already starts with the file's PATH", v)
	}
}

// Contract: "a VALUE equal to `~` or starting `~/` has that prefix replaced by
// Home(); $VAR is not expanded".
func TestContractPortabilityApplyEnvFileTildeOnly(t *testing.T) {
	home := contractEnvHome(t)
	for _, k := range []string{"KRAKEN_C_T1", "KRAKEN_C_T2", "KRAKEN_C_T3", "KRAKEN_C_T4", "KRAKEN_C_T5"} {
		contractUnset(t, k)
	}
	ApplyEnvFile(contractWriteEnvFile(t, ""+
		"KRAKEN_C_T1=~\n"+
		"KRAKEN_C_T2=~/Developer/repos\n"+
		"KRAKEN_C_T3=$HOME/x\n"+
		"KRAKEN_C_T4=~other/x\n"+
		"KRAKEN_C_T5=/abs/~/x\n"))

	for key, val := range map[string]string{
		"KRAKEN_C_T1": home,
		"KRAKEN_C_T2": home + "/Developer/repos",
		"KRAKEN_C_T3": "$HOME/x",
		"KRAKEN_C_T4": "~other/x",
		"KRAKEN_C_T5": "/abs/~/x",
	} {
		if v := os.Getenv(key); v != val {
			t.Errorf("%s = %q, want %q", key, v, val)
		}
	}
}

// Contract: "the first occurrence of a KEY in the file wins over later ones".
func TestContractPortabilityApplyEnvFileFirstOccurrenceWins(t *testing.T) {
	contractEnvHome(t)
	contractUnset(t, "KRAKEN_C_DUP")
	contractUnset(t, "KRAKEN_C_AFTER")
	got := ApplyEnvFile(contractWriteEnvFile(t, "KRAKEN_C_DUP=first\nKRAKEN_C_DUP=second\nKRAKEN_C_AFTER=z\n"))
	if want := []string{"KRAKEN_C_DUP", "KRAKEN_C_AFTER"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied = %#v, want %#v (a key listed once)", got, want)
	}
	if v := os.Getenv("KRAKEN_C_DUP"); v != "first" {
		t.Fatalf("KRAKEN_C_DUP = %q, want first", v)
	}
}

// Contract (C1): "re-reading is a no-op" — every key is now present, so a
// second application sets nothing.
func TestContractPortabilityApplyEnvFileReapplyIsNoop(t *testing.T) {
	contractEnvHome(t)
	contractUnset(t, "KRAKEN_C_ONCE")
	p := contractWriteEnvFile(t, "KRAKEN_C_ONCE=v\n")
	if got := ApplyEnvFile(p); !reflect.DeepEqual(got, []string{"KRAKEN_C_ONCE"}) {
		t.Fatalf("first apply = %#v", got)
	}
	if got := ApplyEnvFile(p); len(got) != 0 {
		t.Fatalf("second apply = %#v, want nothing applied", got)
	}
}
