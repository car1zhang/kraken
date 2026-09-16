package core

// BLIND logic-dimension tests for WAVE 3a (codex launcher resolution +
// version gate), authored from 55-contract-cellsurface.md's "WAVE 3a" section
// ALONE — branch.go's implementation was never read. Each test pins one
// happy-path clause of ResolveCodex: the ladder order, first-passing-wins,
// dedupe by EvalSymlinks, the as-found absolute return, and the 0.100.0 gate.
// Degradation/error shapes are the edge author's dimension.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexLauncherContractEnv makes ResolveCodex hermetic: HOME and PATH point at
// empty temp dirs, KRAKEN_CODEX is empty, and codexSystemDirs is swapped for
// two empty temp dirs (restored on cleanup). It returns the home dir, the PATH
// dir and the two system dirs so a test can populate exactly the rungs it
// means to exercise.
func codexLauncherContractEnv(t *testing.T) (home, pathDir, sysA, sysB string) {
	t.Helper()
	home = t.TempDir()
	pathDir = t.TempDir()
	sysA = t.TempDir()
	sysB = t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PATH", pathDir)
	t.Setenv("KRAKEN_CODEX", "")
	prev := codexSystemDirs
	codexSystemDirs = []string{sysA, sysB}
	t.Cleanup(func() { codexSystemDirs = prev })
	return home, pathDir, sysA, sysB
}

// codexLauncherContractFake writes an executable #!/bin/sh fake codex at path
// that reports `codex-cli <version>` on stdout. When logPath is non-empty each
// invocation also appends a line there, so a test can count probes.
func codexLauncherContractFake(t *testing.T, path, version, logPath string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n"
	if logPath != "" {
		script += "echo probed >> '" + logPath + "'\n"
	}
	script += "echo 'codex-cli " + version + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func codexLauncherContractNvmCodex(home, nodeDir string) string {
	return filepath.Join(home, ".nvm", "versions", "node", nodeDir, "bin", "codex")
}

func codexLauncherContractResolve(t *testing.T) string {
	t.Helper()
	got, err := ResolveCodex()
	if err != nil {
		t.Fatalf("ResolveCodex() error = %v, want a resolved launcher", err)
	}
	return got
}

// Rung 1: a non-empty KRAKEN_CODEX beats a passing codex on PATH.
func TestCodexLauncherContractKrakenCodexWinsOverPath(t *testing.T) {
	_, pathDir, _, _ := codexLauncherContractEnv(t)
	override := codexLauncherContractFake(t, filepath.Join(t.TempDir(), "my-codex"), "0.144.6", "")
	codexLauncherContractFake(t, filepath.Join(pathDir, "codex"), "0.144.6", "")
	t.Setenv("KRAKEN_CODEX", override)

	if got := codexLauncherContractResolve(t); got != override {
		t.Fatalf("ResolveCodex() = %q, want the KRAKEN_CODEX path %q", got, override)
	}
}

// Rung 1: KRAKEN_CODEX is the ONLY candidate — a below-floor override does not
// fall through to a passing PATH codex, and the error names KRAKEN_CODEX.
func TestCodexLauncherContractKrakenCodexIsTheOnlyCandidate(t *testing.T) {
	_, pathDir, sysA, _ := codexLauncherContractEnv(t)
	override := codexLauncherContractFake(t, filepath.Join(t.TempDir(), "old-codex"), "0.39.0", "")
	codexLauncherContractFake(t, filepath.Join(pathDir, "codex"), "0.144.6", "")
	codexLauncherContractFake(t, filepath.Join(sysA, "codex"), "0.144.6", "")
	t.Setenv("KRAKEN_CODEX", override)

	got, err := ResolveCodex()
	if err == nil {
		t.Fatalf("ResolveCodex() = %q, nil; want a KRAKEN_CODEX refusal with no fallthrough", got)
	}
	if !strings.Contains(err.Error(), "KRAKEN_CODEX") {
		t.Fatalf("error %q does not mention KRAKEN_CODEX", err)
	}
}

// Rung 1: a bare name (no separator) in KRAKEN_CODEX is looked up on PATH.
func TestCodexLauncherContractKrakenCodexBareNameLooksUpPath(t *testing.T) {
	_, pathDir, _, _ := codexLauncherContractEnv(t)
	want := codexLauncherContractFake(t, filepath.Join(pathDir, "codex-nightly"), "0.144.6", "")
	t.Setenv("KRAKEN_CODEX", "codex-nightly")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want PATH lookup of the bare name %q", got, want)
	}
}

// Rung 2 beats rung 3: PATH codex wins over an nvm codex.
func TestCodexLauncherContractPathBeatsNvm(t *testing.T) {
	home, pathDir, _, _ := codexLauncherContractEnv(t)
	want := codexLauncherContractFake(t, filepath.Join(pathDir, "codex"), "0.144.6", "")
	codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v22.1.0"), "0.144.6", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want the PATH codex %q", got, want)
	}
}

// Rung 3 ordering: v-prefixed dirs sort numerically (v18.10.0 > v18.2.0 > v9.0.0),
// and a non-version dir sorts after all of them.
func TestCodexLauncherContractNvmNewestNumericFirst(t *testing.T) {
	home, _, _, _ := codexLauncherContractEnv(t)
	for _, d := range []string{"v9.0.0", "v18.2.0", "system", "v18.10.0"} {
		codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, d), "0.144.6", "")
	}
	want := codexLauncherContractNvmCodex(home, "v18.10.0")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want the newest node's codex %q (numeric, not lexical, sort)", got, want)
	}
}

// Rung 3 ordering + first-passing-wins: the newest node's codex is below the
// floor, so the next-newest (numerically) wins.
func TestCodexLauncherContractNvmFallsToNextNewestWhenNewestFails(t *testing.T) {
	home, _, _, _ := codexLauncherContractEnv(t)
	codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v20.0.0"), "0.39.0", "")
	codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v9.9.9"), "0.144.6", "")
	codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v10.0.0"), "0.144.6", "")
	want := codexLauncherContractNvmCodex(home, "v10.0.0")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want %q", got, want)
	}
}

// Rung 3 ordering: non-version dir names sort lexically among themselves, and
// only after every v<X>.<Y>.<Z> dir.
func TestCodexLauncherContractNvmNonVersionDirsLexicalAfterVersions(t *testing.T) {
	home, _, _, _ := codexLauncherContractEnv(t)
	codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v1.0.0"), "0.39.0", "")
	codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "zeta"), "0.144.6", "")
	codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "alpha"), "0.144.6", "")
	want := codexLauncherContractNvmCodex(home, "alpha")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want lexically-first non-version dir %q", got, want)
	}
}

// Rung 3 beats rung 4: nvm codex wins over ~/.local/bin/codex.
func TestCodexLauncherContractNvmBeatsLocalBin(t *testing.T) {
	home, _, _, _ := codexLauncherContractEnv(t)
	want := codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v22.1.0"), "0.144.6", "")
	codexLauncherContractFake(t, filepath.Join(home, ".local", "bin", "codex"), "0.144.6", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want the nvm codex %q", got, want)
	}
}

// Rung 4 beats rung 5: ~/.local/bin/codex wins over codexSystemDirs.
func TestCodexLauncherContractLocalBinBeatsSystemDirs(t *testing.T) {
	home, _, sysA, _ := codexLauncherContractEnv(t)
	want := codexLauncherContractFake(t, filepath.Join(home, ".local", "bin", "codex"), "0.144.6", "")
	codexLauncherContractFake(t, filepath.Join(sysA, "codex"), "0.144.6", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want ~/.local/bin/codex %q", got, want)
	}
}

// Rung 5: codexSystemDirs are tried in slice order.
func TestCodexLauncherContractSystemDirsInOrder(t *testing.T) {
	_, _, sysA, sysB := codexLauncherContractEnv(t)
	want := codexLauncherContractFake(t, filepath.Join(sysA, "codex"), "0.144.6", "")
	codexLauncherContractFake(t, filepath.Join(sysB, "codex"), "0.144.6", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want the first system dir's codex %q", got, want)
	}
}

// Rung 5 + first-passing-wins: a below-floor first system dir yields to the second.
func TestCodexLauncherContractSystemDirsSecondWinsWhenFirstFails(t *testing.T) {
	_, _, sysA, sysB := codexLauncherContractEnv(t)
	codexLauncherContractFake(t, filepath.Join(sysA, "codex"), "0.39.0", "")
	want := codexLauncherContractFake(t, filepath.Join(sysB, "codex"), "0.144.6", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want %q", got, want)
	}
}

// First-passing-wins across rungs: the job-428 shape — a below-floor PATH codex
// (homebrew 0.39.0) is rejected and a passing later rung (nvm) is returned.
func TestCodexLauncherContractFirstPassingWinsAcrossRungs(t *testing.T) {
	home, pathDir, sysA, _ := codexLauncherContractEnv(t)
	codexLauncherContractFake(t, filepath.Join(pathDir, "codex"), "0.39.0", "")
	want := codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v22.1.0"), "0.144.6", "")
	codexLauncherContractFake(t, filepath.Join(sysA, "codex"), "0.144.6", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want the first PASSING candidate %q", got, want)
	}
}

// Dedupe by EvalSymlinks: a PATH codex that is a symlink to ~/.local/bin/codex
// is the same file, so the (below-floor) file is probed exactly once.
func TestCodexLauncherContractDedupeProbesSameFileOnce(t *testing.T) {
	home, pathDir, sysA, _ := codexLauncherContractEnv(t)
	logPath := filepath.Join(t.TempDir(), "probes.log")
	real := codexLauncherContractFake(t, filepath.Join(home, ".local", "bin", "codex"), "0.39.0", logPath)
	if err := os.Symlink(real, filepath.Join(pathDir, "codex")); err != nil {
		t.Fatal(err)
	}
	want := codexLauncherContractFake(t, filepath.Join(sysA, "codex"), "0.144.6", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want %q", got, want)
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("the shared file was never probed: %v", err)
	}
	if n := strings.Count(string(data), "probed"); n != 1 {
		t.Fatalf("same file (by EvalSymlinks) probed %d times, want exactly 1", n)
	}
}

// Return AS FOUND: a ~/.local/bin/codex symlink is returned as the symlink's
// own absolute path, never its target.
func TestCodexLauncherContractReturnsSymlinkPathAsFound(t *testing.T) {
	home, _, _, _ := codexLauncherContractEnv(t)
	target := codexLauncherContractFake(t, filepath.Join(t.TempDir(), "pkg", "codex.js"), "0.144.6", "")
	link := filepath.Join(home, ".local", "bin", "codex")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	got := codexLauncherContractResolve(t)
	if got != link {
		t.Fatalf("ResolveCodex() = %q, want the symlink path as found %q (not its target %q)", got, link, target)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("ResolveCodex() = %q, want an absolute path", got)
	}
}

// Version gate at the floor: 0.99.99 is rejected (numeric, not lexical,
// comparison) and the exact floor 0.100.0 passes.
func TestCodexLauncherContractGateRejects09999AcceptsFloor(t *testing.T) {
	home, pathDir, _, _ := codexLauncherContractEnv(t)
	codexLauncherContractFake(t, filepath.Join(pathDir, "codex"), "0.99.99", "")
	want := codexLauncherContractFake(t, codexLauncherContractNvmCodex(home, "v22.1.0"), "0.100.0", "")

	if got := codexLauncherContractResolve(t); got != want {
		t.Fatalf("ResolveCodex() = %q, want the 0.100.0 codex %q (0.99.99 must fail the gate)", got, want)
	}
}

// Version gate: the floor and above each pass as a lone candidate.
func TestCodexLauncherContractGatePassesFloorAndAbove(t *testing.T) {
	for _, v := range []string{"0.100.0", "0.144.6"} {
		t.Run(v, func(t *testing.T) {
			_, pathDir, _, _ := codexLauncherContractEnv(t)
			want := codexLauncherContractFake(t, filepath.Join(pathDir, "codex"), v, "")
			if got := codexLauncherContractResolve(t); got != want {
				t.Fatalf("ResolveCodex() with codex %s = %q, want %q", v, got, want)
			}
		})
	}
}

// Version gate: the floor constant is unchanged and the comparator agrees with
// the three boundary versions on real `--version` output shapes.
func TestCodexLauncherContractFloorConstantAndComparator(t *testing.T) {
	if codexMinVersion != "0.100.0" {
		t.Fatalf("codexMinVersion = %q, want 0.100.0", codexMinVersion)
	}
	cases := map[string]bool{
		"codex-cli 0.99.99": false,
		"codex-cli 0.100.0": true,
		"codex-cli 0.144.6": true,
	}
	for out, want := range cases {
		if got := codexVersionAtLeast(out, codexMinVersion); got != want {
			t.Errorf("codexVersionAtLeast(%q, %q) = %v, want %v", out, codexMinVersion, got, want)
		}
	}
}
