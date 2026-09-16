package jobs

// BLIND logic-dimension tests for WAVE 3a (codex launcher resolution +
// version gate), authored from 55-contract-cellsurface.md's "WAVE 3a" section
// ALONE — cell.go and submit.go were never read. Happy-path clauses only:
// codexCellArgs' exact shape, codexZeusLaunch putting the resolved absolute
// launcher first, codexPreflight's agent routing, and SubmitJob's codex rung.
//
// codexSystemDirs lives in core and is unreachable here, so every test that
// needs a passing codex puts it on PATH (rung 2, ahead of the system dirs),
// and every test that needs a failing ladder uses a set-but-missing
// KRAKEN_CODEX (rung 1, which never falls through).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// codexLauncherContractPathCodex sandboxes HOME, clears KRAKEN_CODEX, and puts
// an executable fake codex reporting `codex-cli 0.144.6` alone on PATH. It
// returns the fake's absolute path — what ResolveCodex must resolve to.
func codexLauncherContractPathCodex(t *testing.T) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_CODEX", "")
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	fake := filepath.Join(dir, "codex")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'codex-cli 0.144.6'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return fake
}

// codexLauncherContractNoCodex makes the ladder fail at rung 1 without any
// dependence on the machine: PATH is an empty dir and KRAKEN_CODEX names a
// path that does not exist (a set-but-missing override is itself the refusal).
func codexLauncherContractNoCodex(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("KRAKEN_CODEX", filepath.Join(t.TempDir(), "no-such-codex"))
}

func codexLauncherContractEqual(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q, want %q (full %q)", what, i, got[i], want[i], got)
		}
	}
}

// codexCellArgs(j, launcher) with model: set — launcher verbatim as argv[0].
func TestCodexLauncherContractCellArgsWithModel(t *testing.T) {
	j := &Job{Agent: "codex", Slug: "demo", Model: "gpt-5-codex"}
	const launcher = "/opt/some where/codex"
	got := codexCellArgs(j, launcher)
	want := []string{launcher, "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5-codex"}
	codexLauncherContractEqual(t, "codexCellArgs", got, want)
}

// codexCellArgs(j, launcher) with model: unset — `-m codexZeusModel(j)`.
func TestCodexLauncherContractCellArgsModelUnset(t *testing.T) {
	j := &Job{Agent: "codex", Slug: "demo"}
	const launcher = "/Users/x/.nvm/versions/node/v22.1.0/bin/codex"
	got := codexCellArgs(j, launcher)
	want := []string{launcher, "--dangerously-bypass-approvals-and-sandbox", "-m", codexZeusModel(j)}
	codexLauncherContractEqual(t, "codexCellArgs", got, want)
	if codexZeusModel(j) == "" {
		t.Fatalf("codexZeusModel(unset) is empty; `-m` would carry no model")
	}
}

// codexZeusLaunch (fresh): the command after accountEnvPrefix(j) begins with
// the resolved absolute launcher rendered by core.ShellCmdString.
func TestCodexLauncherContractZeusLaunchBeginsWithResolvedPath(t *testing.T) {
	fake := codexLauncherContractPathCodex(t)
	wt := t.TempDir()
	j := &Job{Agent: "codex", Slug: "demo"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}

	cmd, err := codexZeusLaunch(j, wt, "BRIEF", "PROMPT", "")
	if err != nil {
		t.Fatalf("codexZeusLaunch with a passing codex on PATH: %v", err)
	}
	want := accountEnvPrefix(j) + core.ShellCmdString([]string{fake}) + " "
	if !strings.HasPrefix(cmd, want) {
		t.Fatalf("command = %q, want it to begin with %q (resolved absolute launcher as argv[0])", cmd, want)
	}
	if !strings.HasPrefix(cmd, accountEnvPrefix(j)+core.ShellCmdString([]string{fake, "--dangerously-bypass-approvals-and-sandbox"})) {
		t.Fatalf("fresh command = %q, want the bypass flag directly after the launcher", cmd)
	}
}

// codexZeusLaunch (resume): `resume <id>` sits immediately after the resolved launcher.
func TestCodexLauncherContractZeusLaunchResumeAfterResolvedPath(t *testing.T) {
	fake := codexLauncherContractPathCodex(t)
	wt := t.TempDir()
	j := &Job{Agent: "codex", Slug: "demo"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	const sid = "019a-sess-abc123"

	cmd, err := codexZeusLaunch(j, wt, "BRIEF", "PROMPT", sid)
	if err != nil {
		t.Fatalf("codexZeusLaunch resume with a passing codex on PATH: %v", err)
	}
	want := accountEnvPrefix(j) + core.ShellCmdString([]string{fake, "resume", sid}) + " "
	if !strings.HasPrefix(cmd, want) {
		t.Fatalf("resume command = %q, want it to begin with %q", cmd, want)
	}
}

// codexPreflight: a claude job (Agent "" or "claude") returns nil whatever
// KRAKEN_CODEX says, and never runs a codex probe.
func TestCodexLauncherContractPreflightClaudeJobNeverProbes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", t.TempDir())
	logPath := filepath.Join(t.TempDir(), "probes.log")
	probe := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\necho probed >> '" + logPath + "'\necho 'codex-cli 0.39.0'\n"
	if err := os.WriteFile(probe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	for _, kc := range []string{filepath.Join(t.TempDir(), "garbage-not-there"), probe} {
		t.Setenv("KRAKEN_CODEX", kc)
		for _, agent := range []string{"", "claude"} {
			j := &Job{Agent: agent, Slug: "demo"}
			if err := codexPreflight(j); err != nil {
				t.Fatalf("codexPreflight(Agent=%q, KRAKEN_CODEX=%q) = %v, want nil", agent, kc, err)
			}
		}
	}
	if _, err := os.Stat(logPath); err == nil {
		t.Fatalf("codexPreflight probed codex for a claude job (probe log exists)")
	}
}

// codexPreflight: a codex job returns nil when ResolveCodex succeeds.
func TestCodexLauncherContractPreflightCodexJobResolvable(t *testing.T) {
	codexLauncherContractPathCodex(t)
	if err := codexPreflight(&Job{Agent: "codex", Slug: "demo"}); err != nil {
		t.Fatalf("codexPreflight(codex job, passing codex on PATH) = %v, want nil", err)
	}
}

// codexPreflight: a codex job returns ResolveCodex()'s error unchanged.
func TestCodexLauncherContractPreflightCodexJobReturnsResolveError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	codexLauncherContractNoCodex(t)

	_, want := core.ResolveCodex()
	if want == nil {
		t.Fatal("fixture broken: ResolveCodex succeeded under a missing KRAKEN_CODEX")
	}
	got := codexPreflight(&Job{Agent: "codex", Slug: "demo"})
	if got == nil {
		t.Fatalf("codexPreflight(codex job, nothing resolvable) = nil, want ResolveCodex's error")
	}
	if got.Error() != want.Error() {
		t.Fatalf("codexPreflight error = %q, want ResolveCodex's error unchanged %q", got, want)
	}
}

// SubmitJob's codex rung: a codex draft with a resolvable repo and no codex is
// refused as `job <slug>: <ResolveCodex err>`, never reaches the start seam,
// stays draft, and leaves no cell dir behind.
func TestCodexLauncherContractSubmitJobRefusesUnresolvableCodex(t *testing.T) {
	st := useTestStore(t) // sandboxes HOME
	contractFakeRepo(t, "codex-launcher-repo")
	rec := contractStubStartCell(t, nil)
	codexLauncherContractNoCodex(t)

	sj := mustCreateJob(t, st, store.Job{
		Slug: "codex-nolauncher", Repo: "codex-launcher-repo", Agent: "codex", Body: "do the thing\n",
	})
	j := JobBySlug(LoadJobs(), sj.Slug)
	if j == nil {
		t.Fatal("job not found after create")
	}
	if j.Agent != "codex" {
		t.Fatalf("fixture broken: loaded job Agent = %q, want codex", j.Agent)
	}
	beforeRev := j.Rev

	_, resolveErr := core.ResolveCodex()
	if resolveErr == nil {
		t.Fatal("fixture broken: ResolveCodex succeeded under a missing KRAKEN_CODEX")
	}

	err := SubmitJob(j)
	if err == nil {
		t.Fatal("SubmitJob(codex job, no codex resolvable) = nil, want the codex refusal")
	}
	if want := "job codex-nolauncher: " + resolveErr.Error(); err.Error() != want {
		t.Fatalf("SubmitJob error = %q, want %q", err, want)
	}
	if len(rec.calls) != 0 {
		t.Fatalf("submitStartCell called %d times; the codex rung must refuse before any start", len(rec.calls))
	}
	if j.State != "" {
		t.Errorf("in-memory State = %q after refusal, want unchanged draft", j.State)
	}
	row, gerr := st.GetJobBySlug("codex-nolauncher")
	if gerr != nil {
		t.Fatalf("GetJobBySlug: %v", gerr)
	}
	if row.State != "" || row.Rev != beforeRev {
		t.Errorf("row after refusal: state=%q rev=%d, want state \"\" rev %d (untouched draft)", row.State, row.Rev, beforeRev)
	}
	if _, serr := os.Stat(cellDir(j)); !os.IsNotExist(serr) {
		t.Fatalf("cellDir(j) %q exists after the codex refusal (stat err %v), want absent", cellDir(j), serr)
	}
}

// SubmitJob's codex rung passes a codex job with a resolvable codex through to
// the (stubbed) start seam exactly once.
func TestCodexLauncherContractSubmitJobPassesResolvableCodex(t *testing.T) {
	st := useTestStore(t)
	rec := contractStubStartCell(t, nil)
	// The PATH-codex helper re-sandboxes HOME, so the repo is made after it.
	codexLauncherContractPathCodex(t)
	repo := contractFakeRepo(t, "codex-launcher-ok-repo")

	sj := mustCreateJob(t, st, store.Job{
		Slug: "codex-resolvable", Repo: "codex-launcher-ok-repo", Agent: "codex", Body: "do the thing\n",
	})
	j := JobBySlug(LoadJobs(), sj.Slug)
	if j == nil {
		t.Fatal("job not found after create")
	}

	if err := SubmitJob(j); err != nil {
		t.Fatalf("SubmitJob(codex job, passing codex on PATH) = %v, want nil", err)
	}
	if len(rec.calls) != 1 || rec.calls[0].repo != repo {
		t.Fatalf("submitStartCell calls = %+v, want exactly one with repo %q", rec.calls, repo)
	}
}
