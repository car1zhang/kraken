package core

// codex_launcher_edge_test.go — WAVE 3a EDGE-dimension BLIND tests for
// ResolveCodex, authored from 55-contract-cellsurface.md's "WAVE 3a — codex
// launcher resolution + version gate" section ALONE (branch.go unread).
//
// Covers boundaries, error paths and degradation: the no-candidate error, the
// every-candidate-rejected error with each rejection's reason, the
// KRAKEN_CODEX rung's no-fallthrough failures, bare-name KRAKEN_CODEX lookup,
// silent skips of non-executable / directory candidates, nvm dir ordering
// (numeric v-dirs first, everything else lexically after), the 5s probe
// deadline (opt-in: KRAKEN_SLOW_TESTS=1), symlink dedupe, and as-found path
// return for a symlinked candidate.
//
// Hermetic: HOME, PATH and KRAKEN_CODEX via t.Setenv; codexSystemDirs swapped
// and restored; every fake codex is a 0o755 #!/bin/sh script. AUTHOR-ONLY.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- fixtures -------------------------------------------------------------

// cdxEdgeWorld is one hermetic launcher world: an empty PATH dir, a sandboxed
// HOME, two swapped-in system dirs, and KRAKEN_CODEX cleared.
type cdxEdgeWorld struct {
	home    string
	pathDir string
	sysA    string
	sysB    string
}

func cdxEdgeSetup(t *testing.T) *cdxEdgeWorld {
	t.Helper()
	w := &cdxEdgeWorld{
		home:    t.TempDir(),
		pathDir: t.TempDir(),
		sysA:    t.TempDir(),
		sysB:    t.TempDir(),
	}
	t.Setenv("HOME", w.home)
	t.Setenv("PATH", w.pathDir)
	t.Setenv("KRAKEN_CODEX", "")
	prev := codexSystemDirs
	codexSystemDirs = []string{w.sysA, w.sysB}
	t.Cleanup(func() { codexSystemDirs = prev })
	return w
}

// cdxEdgeScript writes an executable #!/bin/sh script at path (creating its
// parent dir) and returns path.
func cdxEdgeScript(t *testing.T, path, body string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func cdxEdgeVersion(t *testing.T, path, version string) string {
	return cdxEdgeScript(t, path, "echo 'codex-cli "+version+"'")
}

func (w *cdxEdgeWorld) nvmCodex(dir string) string {
	return filepath.Join(w.home, ".nvm", "versions", "node", dir, "bin", "codex")
}

func (w *cdxEdgeWorld) localCodex() string {
	return filepath.Join(w.home, ".local", "bin", "codex")
}

// cdxEdgeSegment returns the text following path (or its symlink-resolved
// form) up to the next occurrence of any of others, and whether path was
// found at all. It lets a reason assertion bind to ITS path rather than to any
// reason anywhere in the error.
func cdxEdgeSegment(text, path string, others []string) (string, bool) {
	forms := []string{path}
	if rp, err := filepath.EvalSymlinks(path); err == nil && rp != path {
		forms = append(forms, rp)
	}
	for _, f := range forms {
		idx := strings.Index(text, f)
		if idx < 0 {
			continue
		}
		rest := text[idx+len(f):]
		end := len(rest)
		for _, o := range others {
			if o == path {
				continue
			}
			oforms := []string{o}
			if rp, err := filepath.EvalSymlinks(o); err == nil && rp != o {
				oforms = append(oforms, rp)
			}
			for _, of := range oforms {
				if j := strings.Index(rest, of); j >= 0 && j < end {
					end = j
				}
			}
		}
		return rest[:end], true
	}
	return "", false
}

func cdxEdgeContainsPath(text, path string) bool {
	if strings.Contains(text, path) {
		return true
	}
	if rp, err := filepath.EvalSymlinks(path); err == nil && strings.Contains(text, rp) {
		return true
	}
	return false
}

// --- no candidate ---------------------------------------------------------

// Contract: no candidate at any rung → error text contains `codex not found`
// and names every searched location: PATH, the nvm pattern, ~/.local/bin, and
// each codexSystemDirs entry. Returned path is empty.
func TestCodexLauncherEdgeNoCandidateAnywhere(t *testing.T) {
	w := cdxEdgeSetup(t)

	got, err := ResolveCodex()
	if err == nil {
		t.Fatalf("ResolveCodex() with no codex anywhere = (%q, nil), want an error", got)
	}
	if got != "" {
		t.Errorf("ResolveCodex() error path returned path %q, want empty", got)
	}
	msg := err.Error()
	for _, want := range []string{"codex not found", "PATH", ".nvm/versions/node", ".local/bin", w.sysA, w.sysB} {
		if !strings.Contains(msg, want) {
			t.Errorf("no-candidate error %q does not contain %q", msg, want)
		}
	}
	if strings.Contains(msg, "no codex at or above") {
		t.Errorf("no-candidate error %q uses the none-passed wording; nothing existed to reject", msg)
	}
}

// Contract: an empty nvm root (no node dirs at all) and an empty ~/.local/bin
// are still "no candidate", not a crash or a different error.
func TestCodexLauncherEdgeEmptyNvmRootIsNoCandidate(t *testing.T) {
	w := cdxEdgeSetup(t)
	if err := os.MkdirAll(filepath.Join(w.home, ".nvm", "versions", "node"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.home, ".nvm", "versions", "node", "v22.1.0", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(w.home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveCodex()
	if err == nil || !strings.Contains(err.Error(), "codex not found") {
		t.Fatalf("ResolveCodex() with empty candidate dirs = %v, want a `codex not found` error", err)
	}
}

// --- every candidate rejected ---------------------------------------------

// Contract: ≥1 candidate existed but none passed → text contains
// `no codex at or above 0.100.0` and, for EVERY rejected candidate, its path
// followed by its reason: the reported triple for a too-old version,
// `unparseable version` for output with no X.Y.Z triple, `no version` for a
// probe that exits non-zero.
func TestCodexLauncherEdgeAllCandidatesRejected(t *testing.T) {
	w := cdxEdgeSetup(t)

	pathHit := cdxEdgeVersion(t, filepath.Join(w.pathDir, "codex"), "0.39.0")
	nvmHit := cdxEdgeScript(t, w.nvmCodex("v20.11.1"), "echo 'codex-cli dev-build'")
	localHit := cdxEdgeScript(t, w.localCodex(), "echo 'codex-cli 0.200.0'\nexit 1")
	sysHit := cdxEdgeVersion(t, filepath.Join(w.sysB, "codex"), "0.99.9")

	got, err := ResolveCodex()
	if err == nil {
		t.Fatalf("ResolveCodex() with only failing candidates = (%q, nil), want an error", got)
	}
	if got != "" {
		t.Errorf("error path returned path %q, want empty", got)
	}
	msg := err.Error()
	if !strings.Contains(msg, "no codex at or above 0.100.0") {
		t.Errorf("error %q does not contain %q", msg, "no codex at or above 0.100.0")
	}

	all := []string{pathHit, nvmHit, localHit, sysHit}
	cases := []struct {
		path, reason string
	}{
		{pathHit, "0.39.0"},
		{nvmHit, "unparseable version"},
		{localHit, "no version"},
		{sysHit, "0.99.9"},
	}
	for _, c := range cases {
		seg, ok := cdxEdgeSegment(msg, c.path, all)
		if !ok {
			t.Errorf("error %q does not name rejected candidate %q", msg, c.path)
			continue
		}
		if !strings.Contains(seg, c.reason) {
			t.Errorf("rejected candidate %q is not followed by reason %q in error %q", c.path, c.reason, msg)
		}
	}
	// a non-zero exit is `no version` even when it printed a passing triple.
	if seg, ok := cdxEdgeSegment(msg, localHit, all); ok && strings.Contains(seg, "0.200.0") {
		t.Errorf("non-zero-exit candidate %q reported its printed version; want reason `no version`: %q", localHit, msg)
	}
}

// Contract boundary: exactly the floor passes (codexVersionAtLeast is >=), one
// patch below is a rejection carrying that triple.
func TestCodexLauncherEdgeFloorBoundary(t *testing.T) {
	w := cdxEdgeSetup(t)
	below := cdxEdgeVersion(t, filepath.Join(w.pathDir, "codex"), "0.99.99")
	at := cdxEdgeVersion(t, filepath.Join(w.sysA, "codex"), "0.100.0")

	got, err := ResolveCodex()
	if err != nil {
		t.Fatalf("ResolveCodex() with a codex exactly at the floor = %v, want success", err)
	}
	if got != at {
		t.Errorf("ResolveCodex() = %q, want the at-floor candidate %q (not the below-floor %q)", got, at, below)
	}
}

// --- KRAKEN_CODEX rung ----------------------------------------------------

// Contract: KRAKEN_CODEX set → the ONLY candidate. Missing, non-executable or
// too old → error text contains `KRAKEN_CODEX`, and NO fallthrough even with
// a passing codex on PATH, in nvm, in ~/.local/bin and in a system dir.
func TestCodexLauncherEdgeKrakenCodexFailuresNeverFallThrough(t *testing.T) {
	cases := []struct {
		name   string
		make   func(t *testing.T, w *cdxEdgeWorld) string
		reason string
	}{
		{
			name: "missing path",
			make: func(t *testing.T, w *cdxEdgeWorld) string {
				return filepath.Join(w.home, "nowhere", "codex")
			},
		},
		{
			name: "non-executable file",
			make: func(t *testing.T, w *cdxEdgeWorld) string {
				p := filepath.Join(w.home, "noexec", "codex")
				if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(p, []byte("#!/bin/sh\necho 'codex-cli 0.200.0'\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(p, 0o644); err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
		{
			name: "directory",
			make: func(t *testing.T, w *cdxEdgeWorld) string {
				p := filepath.Join(w.home, "adir", "codex")
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
				return p
			},
		},
		{
			name: "too old",
			make: func(t *testing.T, w *cdxEdgeWorld) string {
				return cdxEdgeVersion(t, filepath.Join(w.home, "old", "codex"), "0.39.0")
			},
			reason: "0.39.0",
		},
		{
			name: "unparseable",
			make: func(t *testing.T, w *cdxEdgeWorld) string {
				return cdxEdgeScript(t, filepath.Join(w.home, "junk", "codex"), "echo 'hello world'")
			},
			reason: "unparseable version",
		},
		{
			name: "exits non-zero",
			make: func(t *testing.T, w *cdxEdgeWorld) string {
				return cdxEdgeScript(t, filepath.Join(w.home, "fail", "codex"), "exit 3")
			},
			reason: "no version",
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			w := cdxEdgeSetup(t)
			// passing codex at every later rung.
			good := []string{
				cdxEdgeVersion(t, filepath.Join(w.pathDir, "codex"), "0.200.0"),
				cdxEdgeVersion(t, w.nvmCodex("v22.0.0"), "0.200.0"),
				cdxEdgeVersion(t, w.localCodex(), "0.200.0"),
				cdxEdgeVersion(t, filepath.Join(w.sysA, "codex"), "0.200.0"),
			}
			env := c.make(t, w)
			t.Setenv("KRAKEN_CODEX", env)

			got, err := ResolveCodex()
			if err == nil {
				t.Fatalf("KRAKEN_CODEX=%s (%s): ResolveCodex() = (%q, nil), want a KRAKEN_CODEX error with no fallthrough", env, c.name, got)
			}
			if got != "" {
				t.Errorf("KRAKEN_CODEX failure returned path %q, want empty", got)
			}
			msg := err.Error()
			if !strings.Contains(msg, "KRAKEN_CODEX") {
				t.Errorf("KRAKEN_CODEX failure error %q does not contain %q", msg, "KRAKEN_CODEX")
			}
			if !cdxEdgeContainsPath(msg, env) {
				t.Errorf("KRAKEN_CODEX failure error %q does not name the configured path %q", msg, env)
			}
			if c.reason != "" {
				if seg, ok := cdxEdgeSegment(msg, env, nil); !ok || !strings.Contains(seg, c.reason) {
					t.Errorf("KRAKEN_CODEX failure error %q: path %q not followed by reason %q", msg, env, c.reason)
				}
			}
			for _, g := range good {
				if cdxEdgeContainsPath(msg, g) {
					t.Errorf("KRAKEN_CODEX failure error %q names later-rung candidate %q — the ladder fell through", msg, g)
				}
			}
		})
	}
}

// Contract: a bare KRAKEN_CODEX name (no path separator) is looked up on PATH
// with exec.LookPath; the result is that file's absolute path, even when a
// plain `codex` also sits on PATH.
func TestCodexLauncherEdgeKrakenCodexBareNameLooksUpPath(t *testing.T) {
	w := cdxEdgeSetup(t)
	other := t.TempDir()
	t.Setenv("PATH", w.pathDir+string(os.PathListSeparator)+other)
	cdxEdgeVersion(t, filepath.Join(w.pathDir, "codex"), "0.200.0")
	want := cdxEdgeVersion(t, filepath.Join(other, "my-codex"), "0.150.0")
	t.Setenv("KRAKEN_CODEX", "my-codex")

	got, err := ResolveCodex()
	if err != nil {
		t.Fatalf("KRAKEN_CODEX=my-codex (on PATH): ResolveCodex() = %v, want success", err)
	}
	if got != want {
		t.Errorf("KRAKEN_CODEX=my-codex: ResolveCodex() = %q, want the PATH lookup %q", got, want)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("ResolveCodex() = %q, want an absolute path", got)
	}
}

// Contract: a bare KRAKEN_CODEX name NOT on PATH is a KRAKEN_CODEX error — and
// it does not fall through to a passing `codex` elsewhere (not even in a
// system dir or ~/.local/bin, which are not PATH lookups).
func TestCodexLauncherEdgeKrakenCodexBareNameMissing(t *testing.T) {
	w := cdxEdgeSetup(t)
	cdxEdgeVersion(t, filepath.Join(w.pathDir, "codex"), "0.200.0")
	cdxEdgeVersion(t, w.localCodex(), "0.200.0")
	// the bare name exists, but only in a system dir — not on PATH.
	cdxEdgeVersion(t, filepath.Join(w.sysA, "ghost-codex"), "0.200.0")
	t.Setenv("KRAKEN_CODEX", "ghost-codex")

	got, err := ResolveCodex()
	if err == nil {
		t.Fatalf("KRAKEN_CODEX=ghost-codex (not on PATH): ResolveCodex() = (%q, nil), want a KRAKEN_CODEX error", got)
	}
	if !strings.Contains(err.Error(), "KRAKEN_CODEX") {
		t.Errorf("error %q does not contain KRAKEN_CODEX", err.Error())
	}
	if got != "" {
		t.Errorf("returned path %q on error, want empty", got)
	}
}

// --- silent skips ---------------------------------------------------------

// Contract: a rung whose path is not an executable, non-directory file is
// SKIPPED silently — it is not a rejection — and the search continues.
func TestCodexLauncherEdgeNonExecutableAndDirectorySkippedAndSearchContinues(t *testing.T) {
	w := cdxEdgeSetup(t)
	// PATH rung: `codex` is a directory (LookPath will not return it; the
	// nvm rung below also has a directory named codex).
	if err := os.MkdirAll(filepath.Join(w.pathDir, "codex"), 0o755); err != nil {
		t.Fatal(err)
	}
	nvmDir := w.nvmCodex("v22.0.0")
	if err := os.MkdirAll(nvmDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// ~/.local/bin rung: a non-executable file that WOULD pass if run.
	local := w.localCodex()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(local, []byte("#!/bin/sh\necho 'codex-cli 0.200.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(local, 0o644); err != nil {
		t.Fatal(err)
	}
	// first system dir: non-executable too; second: the passing one.
	sysNoExec := filepath.Join(w.sysA, "codex")
	if err := os.WriteFile(sysNoExec, []byte("#!/bin/sh\necho 'codex-cli 0.200.0'\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(sysNoExec, 0o600); err != nil {
		t.Fatal(err)
	}
	want := cdxEdgeVersion(t, filepath.Join(w.sysB, "codex"), "0.101.0")

	got, err := ResolveCodex()
	if err != nil {
		t.Fatalf("ResolveCodex() past skipped candidates = %v, want the last system-dir codex", err)
	}
	if got != want {
		t.Errorf("ResolveCodex() = %q, want %q (skips must not stop the search)", got, want)
	}
}

// Contract: skipped candidates are not rejections, so when every EXISTING
// executable candidate fails, the none-passed error lists only those — never
// a directory or non-executable path — and when ONLY skipped paths exist, it
// is the `codex not found` error.
func TestCodexLauncherEdgeSkippedCandidatesAreNotListedAsRejected(t *testing.T) {
	t.Run("skips beside a rejection", func(t *testing.T) {
		w := cdxEdgeSetup(t)
		nvmDir := w.nvmCodex("v22.0.0")
		if err := os.MkdirAll(nvmDir, 0o755); err != nil {
			t.Fatal(err)
		}
		local := w.localCodex()
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, []byte("#!/bin/sh\necho 'codex-cli 0.39.0'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(local, 0o644); err != nil {
			t.Fatal(err)
		}
		old := cdxEdgeVersion(t, filepath.Join(w.sysA, "codex"), "0.40.0")

		_, err := ResolveCodex()
		if err == nil {
			t.Fatal("ResolveCodex() = nil error, want none-passed")
		}
		msg := err.Error()
		if !strings.Contains(msg, "no codex at or above 0.100.0") {
			t.Errorf("error %q, want the none-passed wording", msg)
		}
		if !cdxEdgeContainsPath(msg, old) {
			t.Errorf("error %q does not list the rejected %q", msg, old)
		}
		for _, skipped := range []string{nvmDir, local} {
			if cdxEdgeContainsPath(msg, skipped) {
				t.Errorf("error %q lists skipped (non-executable/directory) candidate %q as rejected", msg, skipped)
			}
		}
		if strings.Contains(msg, "0.39.0") {
			t.Errorf("error %q carries the non-executable file's version — it was probed, not skipped", msg)
		}
	})

	t.Run("only skips", func(t *testing.T) {
		w := cdxEdgeSetup(t)
		if err := os.MkdirAll(filepath.Join(w.sysA, "codex"), 0o755); err != nil {
			t.Fatal(err)
		}
		local := w.localCodex()
		if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(local, []byte("#!/bin/sh\necho 'codex-cli 0.200.0'\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(local, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := ResolveCodex()
		if err == nil || !strings.Contains(err.Error(), "codex not found") {
			t.Fatalf("ResolveCodex() with only skipped candidates = %v, want `codex not found`", err)
		}
	})
}

// --- nvm ordering ---------------------------------------------------------

// Contract: v<X>.<Y>.<Z> dirs sort NUMERICALLY descending (v10 before v9,
// v10.10.0 before v10.9.0), so the first passing candidate is the newest node.
func TestCodexLauncherEdgeNvmNumericOrdering(t *testing.T) {
	w := cdxEdgeSetup(t)
	cdxEdgeVersion(t, w.nvmCodex("v9.99.99"), "0.101.0")
	cdxEdgeVersion(t, w.nvmCodex("v10.9.0"), "0.101.0")
	want := cdxEdgeVersion(t, w.nvmCodex("v10.10.0"), "0.101.0")
	cdxEdgeVersion(t, w.nvmCodex("v1.0.0"), "0.101.0")

	got, err := ResolveCodex()
	if err != nil {
		t.Fatalf("ResolveCodex() = %v", err)
	}
	if got != want {
		t.Errorf("ResolveCodex() = %q, want the numerically newest node dir %q", got, want)
	}
}

// Contract: any dir name that is not v<X>.<Y>.<Z> sorts AFTER every v-dir,
// lexically among themselves. A too-old v-dir codex is rejected, then the
// lexically-first non-v dir wins — even one that is lexically before "v".
func TestCodexLauncherEdgeNvmNonVersionDirsSortAfterLexically(t *testing.T) {
	t.Run("v-dir outranks a lexically earlier non-v dir", func(t *testing.T) {
		w := cdxEdgeSetup(t)
		cdxEdgeVersion(t, w.nvmCodex("aaa"), "0.101.0")
		want := cdxEdgeVersion(t, w.nvmCodex("v1.0.0"), "0.101.0")
		got, err := ResolveCodex()
		if err != nil {
			t.Fatalf("ResolveCodex() = %v", err)
		}
		if got != want {
			t.Errorf("ResolveCodex() = %q, want the v-dir %q ahead of the non-v dir", got, want)
		}
	})

	t.Run("non-v dirs lexical after rejected v-dirs", func(t *testing.T) {
		w := cdxEdgeSetup(t)
		cdxEdgeVersion(t, w.nvmCodex("v22.0.0"), "0.39.0")
		cdxEdgeVersion(t, w.nvmCodex("v18.0.0"), "0.39.0")
		cdxEdgeVersion(t, w.nvmCodex("zeta"), "0.101.0")
		cdxEdgeVersion(t, w.nvmCodex("v23.x"), "0.101.0") // v-prefixed but unparseable → non-v group
		cdxEdgeVersion(t, w.nvmCodex("v1.2"), "0.101.0")  // two parts → non-v group
		want := cdxEdgeVersion(t, w.nvmCodex("beta"), "0.101.0")
		cdxEdgeVersion(t, w.nvmCodex("system"), "0.101.0")

		got, err := ResolveCodex()
		if err != nil {
			t.Fatalf("ResolveCodex() = %v", err)
		}
		if got != want {
			t.Errorf("ResolveCodex() = %q, want the lexically first non-v dir %q", got, want)
		}
	})

	t.Run("unparseable v-names order lexically in the non-v group", func(t *testing.T) {
		w := cdxEdgeSetup(t)
		cdxEdgeVersion(t, w.nvmCodex("vfoo"), "0.101.0")
		want := cdxEdgeVersion(t, w.nvmCodex("v23.x"), "0.101.0")
		got, err := ResolveCodex()
		if err != nil {
			t.Fatalf("ResolveCodex() = %v", err)
		}
		if got != want {
			t.Errorf("ResolveCodex() = %q, want %q (\"v23.x\" < \"vfoo\" lexically)", got, want)
		}
	})
}

// Contract: nvm dirs are searched before ~/.local/bin and system dirs, and a
// PATH hit before all of them — a nvm codex that is too old does not stop a
// later rung from winning.
func TestCodexLauncherEdgeRejectedNvmFallsToLocalBin(t *testing.T) {
	w := cdxEdgeSetup(t)
	cdxEdgeVersion(t, w.nvmCodex("v22.0.0"), "0.39.0")
	want := cdxEdgeVersion(t, w.localCodex(), "0.100.1")
	cdxEdgeVersion(t, filepath.Join(w.sysA, "codex"), "0.200.0")

	got, err := ResolveCodex()
	if err != nil {
		t.Fatalf("ResolveCodex() = %v", err)
	}
	if got != want {
		t.Errorf("ResolveCodex() = %q, want ~/.local/bin/codex %q (after the rejected nvm, before system dirs)", got, want)
	}
}

// --- probe deadline -------------------------------------------------------

// Contract: the probe runs under a 5-second deadline; a hang is a rejection
// with reason `no version`, and the search continues. Opt-in (slow): set
// KRAKEN_SLOW_TESTS=1.
func TestCodexLauncherEdgeHangingProbeIsRejectedUnderDeadline(t *testing.T) {
	if os.Getenv("KRAKEN_SLOW_TESTS") != "1" {
		t.Skip("slow (5s probe deadline); set KRAKEN_SLOW_TESTS=1 to run")
	}
	t.Run("rejected, search continues", func(t *testing.T) {
		w := cdxEdgeSetup(t)
		// non-exec sleep: the child keeps the stdout pipe open, which is the
		// shape a naive CombinedOutput wait would block 30s on.
		cdxEdgeScript(t, filepath.Join(w.pathDir, "codex"), "/bin/sleep 30\necho 'codex-cli 0.200.0'")
		want := cdxEdgeVersion(t, filepath.Join(w.sysA, "codex"), "0.101.0")

		start := time.Now()
		got, err := ResolveCodex()
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("ResolveCodex() = %v, want the system-dir codex after the hung probe", err)
		}
		if got != want {
			t.Errorf("ResolveCodex() = %q, want %q", got, want)
		}
		if elapsed > 20*time.Second {
			t.Errorf("ResolveCodex() took %v with a hung probe; the 5s deadline did not bound it", elapsed)
		}
	})
	t.Run("only a hang", func(t *testing.T) {
		w := cdxEdgeSetup(t)
		hung := cdxEdgeScript(t, filepath.Join(w.pathDir, "codex"), "/bin/sleep 30")
		start := time.Now()
		_, err := ResolveCodex()
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("ResolveCodex() with only a hung candidate = nil error")
		}
		msg := err.Error()
		if !strings.Contains(msg, "no codex at or above 0.100.0") {
			t.Errorf("error %q, want the none-passed wording", msg)
		}
		if seg, ok := cdxEdgeSegment(msg, hung, nil); !ok || !strings.Contains(seg, "no version") {
			t.Errorf("error %q: hung candidate %q not followed by `no version`", msg, hung)
		}
		if elapsed > 20*time.Second {
			t.Errorf("ResolveCodex() took %v; the 5s deadline did not bound it", elapsed)
		}
	})
}

// --- dedupe + symlinks ----------------------------------------------------

// Contract: candidates dedupe by filepath.EvalSymlinks, first occurrence
// wins, so the same file reached via PATH and via a ~/.local/bin symlink (and
// a system-dir symlink) is probed ONCE and listed once in the rejected list.
func TestCodexLauncherEdgeSymlinkDedupeListsOnce(t *testing.T) {
	w := cdxEdgeSetup(t)
	target0 := cdxEdgeVersion(t, filepath.Join(w.pathDir, "codex"), "0.39.0")
	local := w.localCodex()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target0, local); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target0, filepath.Join(w.sysB, "codex")); err != nil {
		t.Fatal(err)
	}

	_, err := ResolveCodex()
	if err == nil {
		t.Fatal("ResolveCodex() with one too-old file behind three names = nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "no codex at or above 0.100.0") {
		t.Errorf("error %q, want the none-passed wording", msg)
	}
	if n := strings.Count(msg, "0.39.0"); n != 1 {
		t.Errorf("error %q lists the deduped candidate's version %d times, want exactly once", msg, n)
	}
	if !strings.Contains(msg, target0) {
		t.Errorf("error %q does not name the first occurrence (the PATH hit) %q", msg, target0)
	}
}

// Contract: dedupe probes once — a probe counter shows a single run for one
// file reached through PATH and a ~/.local/bin symlink.
func TestCodexLauncherEdgeSymlinkDedupeProbesOnce(t *testing.T) {
	w := cdxEdgeSetup(t)
	counter := filepath.Join(w.home, "probes")
	target0 := cdxEdgeScript(t, filepath.Join(w.pathDir, "codex"), "echo x >> '"+counter+"'\necho 'codex-cli 0.39.0'")
	local := w.localCodex()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target0, local); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveCodex(); err == nil {
		t.Fatal("ResolveCodex() = nil error, want none-passed")
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("probe counter unreadable (never probed?): %v", err)
	}
	if n := strings.Count(string(data), "x"); n != 1 {
		t.Errorf("the one file behind two names was probed %d times, want 1", n)
	}
}

// Contract: the returned path is filepath.Abs of the candidate AS FOUND — a
// ~/.local/bin/codex symlink is returned as that path, not its target.
func TestCodexLauncherEdgeSymlinkReturnedAsFound(t *testing.T) {
	w := cdxEdgeSetup(t)
	elsewhere := t.TempDir() // not on PATH, not a system dir
	target := cdxEdgeVersion(t, filepath.Join(elsewhere, "codex-0.150.0"), "0.150.0")
	local := w.localCodex()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, local); err != nil {
		t.Fatal(err)
	}

	got, err := ResolveCodex()
	if err != nil {
		t.Fatalf("ResolveCodex() = %v", err)
	}
	if got != local {
		t.Errorf("ResolveCodex() = %q, want the symlink as found %q (not its target %q)", got, local, target)
	}
}

// Contract: a dangling symlink does not stat as an executable file → skipped
// silently; with nothing else present it is `codex not found`.
func TestCodexLauncherEdgeDanglingSymlinkSkipped(t *testing.T) {
	w := cdxEdgeSetup(t)
	local := w.localCodex()
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(w.home, "gone", "codex"), local); err != nil {
		t.Fatal(err)
	}
	_, err := ResolveCodex()
	if err == nil || !strings.Contains(err.Error(), "codex not found") {
		t.Fatalf("ResolveCodex() with only a dangling symlink = %v, want `codex not found`", err)
	}
}
