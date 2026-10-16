package jobs

// codex_launcher_edge_test.go — WAVE 3a EDGE-dimension BLIND tests for the
// jobs half of codex launcher resolution, authored from
// 55-contract-cellsurface.md's "WAVE 3a" section ALONE (cell.go, submit.go
// and branch.go unread).
//
// Covers: codexCellArgs' verbatim launcher (even ""); codexZeusLaunch writing
// NOTHING when the ladder fails; codexPreflight never probing for non-codex
// agents (including an uppercase "CODEX" literal, which ParseJob would have
// lowercased); SubmitJob's ladder order (repo refusal before the codex rung)
// and no cell dir either way.
//
// codexSystemDirs is unreachable from package jobs, so every ladder here is
// driven through KRAKEN_CODEX, which is the ONLY candidate when set.
// AUTHOR-ONLY.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
)

// --- fixtures -------------------------------------------------------------

func cdxJobsEdgeScript(t *testing.T, path, body string) string {
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

// cdxJobsEdgeSandbox sandboxes HOME and the pluto dir, and puts a PASSING
// codex on PATH so any test that still fails proves no fallthrough past
// KRAKEN_CODEX.
func cdxJobsEdgeSandbox(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	pathDir := t.TempDir()
	cdxJobsEdgeScript(t, filepath.Join(pathDir, "codex"), "echo 'codex-cli 0.200.0'")
	// prepended, not replaced: SubmitJob's earlier rungs may shell out to
	// system tools, and KRAKEN_CODEX (set by every failing test) is the only
	// candidate anyway.
	t.Setenv("PATH", pathDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

type cdxJobsEdgeRecorder struct{ calls int }

func cdxJobsEdgeStubStart(t *testing.T) *cdxJobsEdgeRecorder {
	t.Helper()
	rec := &cdxJobsEdgeRecorder{}
	prev := submitStartCell
	submitStartCell = func(j *Job, repo string) error {
		rec.calls++
		return nil
	}
	t.Cleanup(func() { submitStartCell = prev })
	return rec
}

// cdxJobsEdgeFailingLadders returns KRAKEN_CODEX values that each fail the
// ladder: missing, non-executable, too old.
func cdxJobsEdgeFailingLadders(t *testing.T) map[string]string {
	t.Helper()
	root := t.TempDir()
	noexec := filepath.Join(root, "noexec", "codex")
	if err := os.MkdirAll(filepath.Dir(noexec), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(noexec, []byte("#!/bin/sh\necho 'codex-cli 0.200.0'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(noexec, 0o644); err != nil {
		t.Fatal(err)
	}
	return map[string]string{
		"missing":        filepath.Join(root, "missing", "codex"),
		"non-executable": noexec,
		"too old":        cdxJobsEdgeScript(t, filepath.Join(root, "old", "codex"), "echo 'codex-cli 0.39.0'"),
	}
}

func cdxJobsEdgeDirEntries(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// --- codexCellArgs --------------------------------------------------------

// Contract: codexCellArgs(j, launcher) = [launcher, bypass, "-m",
// codexZeusModel(j)] with launcher VERBATIM as argv[0] and no lookup inside —
// an empty launcher stays "", a bare name stays bare.
func TestCodexLauncherEdgeCellArgsLauncherVerbatim(t *testing.T) {
	t.Setenv("KRAKEN_CODEX", "/should/not/be/consulted")
	t.Setenv("PATH", t.TempDir())
	for _, launcher := range []string{"", "codex", "relative/codex", "/abs path/with space/codex"} {
		for _, j := range []*Job{{Agent: "codex"}, {Agent: "codex", Model: "gpt-custom"}} {
			got := codexCellArgs(j, launcher)
			want := []string{launcher, "--dangerously-bypass-approvals-and-sandbox", "-m", codexZeusModel(j)}
			if len(got) != len(want) {
				t.Fatalf("codexCellArgs(model=%q, %q) = %#v, want exactly %#v", j.Model, launcher, got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Errorf("codexCellArgs(model=%q, %q)[%d] = %q, want %q", j.Model, launcher, i, got[i], want[i])
				}
			}
		}
	}
}

// --- codexZeusLaunch under a failing ladder -------------------------------

// Contract: codexZeusLaunch calls core.ResolveCodex FIRST; on error it returns
// ("", err) having written NOTHING — no <wt>/AGENTS.md, no spawnPromptPath(j)
// — for a fresh launch and for a resume alike. The cell dir exists beforehand
// so a stray write would succeed and be visible.
func TestCodexLauncherEdgeZeusLaunchWritesNothingOnFailure(t *testing.T) {
	cdxJobsEdgeSandbox(t)
	ladders := cdxJobsEdgeFailingLadders(t)
	for name, env := range ladders {
		for _, resume := range []string{"", "019a-resume-session"} {
			name, env, resume := name, env, resume
			t.Run(name+"/resume="+resume, func(t *testing.T) {
				t.Setenv("KRAKEN_CODEX", env)
				j := &Job{Slug: "cdx-edge-nowrite", ID: 77, Agent: "codex"}
				if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
					t.Fatal(err)
				}
				before := cdxJobsEdgeDirEntries(t, cellDir(j))
				wt := t.TempDir()

				got, err := codexZeusLaunch(j, wt, "brief body", "the initial prompt", resume)
				if err == nil {
					t.Fatalf("codexZeusLaunch with KRAKEN_CODEX %s = (%q, nil), want an error", name, got)
				}
				if got != "" {
					t.Errorf("codexZeusLaunch error path returned command %q, want empty", got)
				}
				if !strings.Contains(err.Error(), "KRAKEN_CODEX") {
					t.Errorf("codexZeusLaunch error %q does not carry ResolveCodex's KRAKEN_CODEX error", err.Error())
				}
				if _, statErr := os.Stat(filepath.Join(wt, "AGENTS.md")); !os.IsNotExist(statErr) {
					t.Errorf("codexZeusLaunch wrote <wt>/AGENTS.md despite the ladder failing (stat err = %v)", statErr)
				}
				if ents := cdxJobsEdgeDirEntries(t, wt); len(ents) != 0 {
					t.Errorf("worktree gained entries %v on a failed launch, want none", ents)
				}
				if _, statErr := os.Stat(spawnPromptPath(j)); !os.IsNotExist(statErr) {
					t.Errorf("codexZeusLaunch staged %s despite the ladder failing (stat err = %v)", spawnPromptPath(j), statErr)
				}
				after := cdxJobsEdgeDirEntries(t, cellDir(j))
				if len(after) != len(before) {
					t.Errorf("cell dir entries %v -> %v on a failed launch, want unchanged", before, after)
				}
			})
		}
	}
}

// Contract: with no cell dir pre-created, a failing ladder still leaves no
// cell dir behind (codexZeusLaunch writes nothing, so it creates nothing).
func TestCodexLauncherEdgeZeusLaunchFailureCreatesNoCellDir(t *testing.T) {
	cdxJobsEdgeSandbox(t)
	t.Setenv("KRAKEN_CODEX", filepath.Join(t.TempDir(), "absent-codex"))
	j := &Job{Slug: "cdx-edge-nocell", ID: 78, Agent: "codex"}
	wt := t.TempDir()
	if _, err := codexZeusLaunch(j, wt, "brief", "prompt", ""); err == nil {
		t.Fatal("codexZeusLaunch with a missing KRAKEN_CODEX = nil error")
	}
	if _, err := os.Stat(cellDir(j)); !os.IsNotExist(err) {
		t.Errorf("cellDir(j) exists after a failed codexZeusLaunch (stat err = %v)", err)
	}
}

// --- codexPreflight -------------------------------------------------------

// Contract: codexPreflight returns nil for any job whose Agent != "codex"
// WITHOUT calling ResolveCodex — a claude job never probes codex, whatever
// KRAKEN_CODEX says. "CODEX" in a Job literal is not "codex" (ParseJob
// lowercases at parse), so it is not a codex job either. The probe is
// detected by a marker the fake codex touches when run.
func TestCodexLauncherEdgePreflightNonCodexNeverProbes(t *testing.T) {
	cdxJobsEdgeSandbox(t)
	marker := filepath.Join(t.TempDir(), "probed")
	probe := cdxJobsEdgeScript(t, filepath.Join(t.TempDir(), "codex"), "touch '"+marker+"'\necho 'codex-cli 0.39.0'")

	for _, env := range []string{probe, filepath.Join(t.TempDir(), "missing-codex")} {
		t.Setenv("KRAKEN_CODEX", env)
		for _, agent := range []string{"", "claude", "CODEX", "Codex", " codex"} {
			j := &Job{Slug: "cdx-edge-pre", Agent: agent}
			if err := codexPreflight(j); err != nil {
				t.Errorf("codexPreflight(Agent=%q, KRAKEN_CODEX=%s) = %v, want nil", agent, env, err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("codexPreflight(Agent=%q) ran the codex probe (marker present)", agent)
			}
		}
	}

	// control: a codex job DOES probe, and the too-old fake fails it — which
	// proves the marker mechanism above could have caught a probe.
	t.Setenv("KRAKEN_CODEX", probe)
	err := codexPreflight(&Job{Slug: "cdx-edge-pre", Agent: "codex"})
	if err == nil {
		t.Fatal("codexPreflight(codex job, too-old KRAKEN_CODEX) = nil, want ResolveCodex's error")
	}
	if !strings.Contains(err.Error(), "KRAKEN_CODEX") || !strings.Contains(err.Error(), "0.39.0") {
		t.Errorf("codexPreflight error %q, want ResolveCodex's error unchanged (KRAKEN_CODEX + 0.39.0)", err.Error())
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Errorf("control: codex job did not run the probe (marker stat err = %v)", statErr)
	}
}

// Contract: for a codex job the error passes through UNCHANGED — the preflight's error
// text equals ResolveCodex's for the same world.
func TestCodexLauncherEdgePreflightErrorUnchanged(t *testing.T) {
	cdxJobsEdgeSandbox(t)
	for name, env := range cdxJobsEdgeFailingLadders(t) {
		t.Run(name, func(t *testing.T) {
			t.Setenv("KRAKEN_CODEX", env)
			_, want := core.ResolveCodex()
			got := codexPreflight(&Job{Slug: "cdx-edge-same", Agent: "codex"})
			if want == nil || got == nil {
				t.Fatalf("errors: ResolveCodex=%v codexPreflight=%v, want both non-nil", want, got)
			}
			if got.Error() != want.Error() {
				t.Errorf("codexPreflight error %q != ResolveCodex error %q, want unchanged", got.Error(), want.Error())
			}
		})
	}
}

// --- SubmitJob ladder order -----------------------------------------------

// Contract: the codex rung sits AFTER ResolveRepo succeeds. A codex job whose
// repo does NOT resolve, under a failing codex ladder, is refused with the
// REPO error — never the codex one — no start, and no cell dir.
func TestCodexLauncherEdgeSubmitRepoRefusalPrecedesCodex(t *testing.T) {
	cdxJobsEdgeSandbox(t)
	rec := cdxJobsEdgeStubStart(t)
	marker := filepath.Join(t.TempDir(), "probed")
	probe := cdxJobsEdgeScript(t, filepath.Join(t.TempDir(), "codex"), "touch '"+marker+"'\necho 'codex-cli 0.39.0'")
	t.Setenv("KRAKEN_CODEX", probe)

	j := &Job{
		Slug:  "cdx-edge-norepo",
		ID:    91,
		Repo:  "cdx-edge-no-such-repo-anywhere",
		Body:  "\n# Spec\nDo the codex thing.\n",
		Agent: "codex",
	}
	err := SubmitJob(j)
	if err == nil {
		t.Fatal("SubmitJob(codex job, unresolvable repo) = nil, want the repo refusal")
	}
	msg := err.Error()
	for _, codexMarker := range []string{"KRAKEN_CODEX", "codex not found", "no codex at or above", "0.39.0"} {
		if strings.Contains(msg, codexMarker) {
			t.Errorf("SubmitJob error %q carries codex-rung text %q; the repo rung must refuse first", msg, codexMarker)
		}
	}
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Errorf("SubmitJob probed codex before the repo resolved (marker present)")
	}
	if rec.calls != 0 {
		t.Errorf("submitStartCell called %d times, want 0", rec.calls)
	}
	if _, statErr := os.Stat(cellDir(j)); !os.IsNotExist(statErr) {
		t.Errorf("cellDir(j) exists after a repo refusal (stat err = %v)", statErr)
	}
}

// Contract: with the repo resolving, a failing codex ladder refuses with
// `job <slug>: <ResolveCodex error>`, never reaches submitStartCell, and
// leaves no cell dir — for each failure shape.
func TestCodexLauncherEdgeSubmitCodexRefusalLeavesNoCellDir(t *testing.T) {
	cdxJobsEdgeSandbox(t)
	repo := filepath.Join(os.Getenv("HOME"), "Developer", "cdx-edge-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, env := range cdxJobsEdgeFailingLadders(t) {
		name, env := name, env
		t.Run(name, func(t *testing.T) {
			rec := cdxJobsEdgeStubStart(t)
			t.Setenv("KRAKEN_CODEX", env)
			j := &Job{
				Slug:  "cdx-edge-refuse",
				ID:    92,
				Repo:  "cdx-edge-repo",
				Body:  "\n# Spec\nDo the codex thing.\n",
				Agent: "codex",
			}
			err := SubmitJob(j)
			if err == nil {
				t.Fatalf("SubmitJob(codex job, KRAKEN_CODEX %s) = nil, want the codex refusal", name)
			}
			msg := err.Error()
			if !strings.HasPrefix(msg, "job cdx-edge-refuse: ") {
				t.Errorf("SubmitJob error %q, want prefix %q", msg, "job cdx-edge-refuse: ")
			}
			if !strings.Contains(msg, "KRAKEN_CODEX") {
				t.Errorf("SubmitJob error %q does not carry the KRAKEN_CODEX error", msg)
			}
			if rec.calls != 0 {
				t.Errorf("submitStartCell called %d times despite the codex refusal", rec.calls)
			}
			if _, statErr := os.Stat(cellDir(j)); !os.IsNotExist(statErr) {
				t.Errorf("cellDir(j) exists after the codex refusal (stat err = %v)", statErr)
			}
			if j.State != "" {
				t.Errorf("SubmitJob refusal set j.State = %q, want the draft untouched", j.State)
			}
		})
	}
}

// Contract: a CLAUDE job under the same failing KRAKEN_CODEX is never refused
// by the codex rung — it reaches the start seam exactly once.
func TestCodexLauncherEdgeSubmitClaudeJobIgnoresBrokenCodex(t *testing.T) {
	cdxJobsEdgeSandbox(t)
	repo := filepath.Join(os.Getenv("HOME"), "Developer", "cdx-edge-claude-repo")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KRAKEN_CODEX", filepath.Join(t.TempDir(), "missing-codex"))
	for i, agent := range []string{"", "claude"} {
		rec := cdxJobsEdgeStubStart(t)
		j := &Job{
			Slug:  []string{"cdx-edge-claude-default", "cdx-edge-claude-claude"}[i],
			ID:    int64(93 + i),
			Repo:  "cdx-edge-claude-repo",
			Body:  "\n# Spec\nDo the claude thing.\n",
			Agent: agent,
		}
		if err := SubmitJob(j); err != nil {
			t.Errorf("SubmitJob(Agent=%q) with a broken KRAKEN_CODEX = %v, want nil (claude never probes codex)", agent, err)
		}
		if rec.calls != 1 {
			t.Errorf("SubmitJob(Agent=%q): submitStartCell called %d times, want 1", agent, rec.calls)
		}
	}
}
