package jobs

import (
	"os"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// threadmodel_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S1's spawn half: core.ThreadModels' alias table and
// jobs.threadSpawnCommand's four-row model resolution, plus the one thing a
// revive can be held to without tmux (the resolved --model on the command
// the window would have run).
//
// Written from the contract text alone. internal/jobs/thread.go,
// internal/jobs/cell.go and internal/core/branch.go were read ONLY at HEAD,
// for the existing fixture patterns this file reuses — threadCellDir,
// useTestStore, mustCreateJob, reviveClaimed's windowOpener seam and
// countingOpener's shape. Nothing from the working tree was observed.
//
// Dimension: LOGIC — the stated resolution rows and their happy paths.
//
// Every package-level identifier declared here is prefixed `contract` — a
// sibling author shares this package's namespace.

// contractThreadModelIDs is the alias → EXACT model id table exactly as the
// contract states it. It is spelled out here rather than derived from
// core.ThreadModels precisely because core.ThreadModels is the thing under
// test: deriving the expectation from the implementation would make every
// assertion below vacuous.
var contractThreadModelIDs = map[string]string{
	"haiku":  "claude-haiku-4-5-20251001",
	"sonnet": "claude-sonnet-5",
	"opus":   "claude-opus-5",
	"fable":  "claude-fable-5-1",
}

// contractModelFlagValue returns the single --model value on a rendered
// spawn command, failing when there is not exactly one --model flag.
//
// "exactly one" is its own obligation and it is not pedantry: claude takes
// the LAST --model on the line, so a second flag appended by a new caller
// would silently win over the one carl picked — and the command string is
// also where kraken's own discovery reads a peer's model back from.
func contractModelFlagValue(t *testing.T, cmd string) string {
	t.Helper()
	if n := strings.Count(cmd, "--model"); n != 1 {
		t.Fatalf("command carries %d --model flags, want exactly 1: %q", n, cmd)
	}
	fields := strings.Fields(cmd)
	for i, f := range fields {
		if f == "--model" {
			if i+1 >= len(fields) {
				t.Fatalf("command ends after --model with no value: %q", cmd)
			}
			return fields[i+1]
		}
	}
	t.Fatalf("--model is present as a substring but not as its own word: %q", cmd)
	return ""
}

// contractThreadJob stands a job up with its cell dir in place, which is all
// threadSpawnCommand needs (it stages the brief and the seed as files).
func contractThreadJob(t *testing.T, slug, model string) *Job {
	t.Helper()
	j := &Job{Slug: slug, Model: model}
	threadCellDir(t, j)
	return j
}

// ---------------------------------------------------------------------------
// core.ThreadModels
// ---------------------------------------------------------------------------

// TestContractThreadModelsIsTheFourAliasesToExactIDs pins the whole table:
// the four keys, nothing else, and the EXACT model id behind each.
//
// The exactness is the point of the row. An alias value ("opus") would look
// identical in every test that only checks a flag is present, and would
// silently follow whatever claude ships next — which is the one thing this
// repo's model policy exists to prevent (core.DefaultModel's own comment
// says so). A picker whose "opus" moves tier on its own is not a picker.
func TestContractThreadModelsIsTheFourAliasesToExactIDs(t *testing.T) {
	if len(core.ThreadModels) != len(contractThreadModelIDs) {
		t.Fatalf("core.ThreadModels = %v, want exactly %d entries", core.ThreadModels, len(contractThreadModelIDs))
	}
	for alias, want := range contractThreadModelIDs {
		got, ok := core.ThreadModels[alias]
		if !ok {
			t.Errorf("core.ThreadModels has no %q key", alias)
			continue
		}
		if got != want {
			t.Errorf("core.ThreadModels[%q] = %q, want the exact id %q", alias, got, want)
		}
		// belt to the suspenders: never the bare tier alias, whatever else
		// changes
		if got == alias {
			t.Errorf("core.ThreadModels[%q] is the alias itself — an alias silently follows whatever claude ships next", alias)
		}
	}
	for alias := range core.ThreadModels {
		if _, ok := contractThreadModelIDs[alias]; !ok {
			t.Errorf("core.ThreadModels carries an extra key %q — the picker renders exactly four", alias)
		}
	}
}

// ---------------------------------------------------------------------------
// threadSpawnCommand: the four resolution rows
// ---------------------------------------------------------------------------

// TestContractThreadSpawnCommandResolvesAnAliasToItsExactID is row (1): a
// non-empty model that IS a core.ThreadModels key puts that map's exact id
// on the command line — never the alias carl clicked, which claude would
// resolve on its own schedule.
func TestContractThreadSpawnCommandResolvesAnAliasToItsExactID(t *testing.T) {
	for alias, want := range contractThreadModelIDs {
		t.Run(alias, func(t *testing.T) {
			// a job model is set deliberately: the per-thread choice must
			// WIN over it, which is the whole reason row (1) precedes row (3)
			j := contractThreadJob(t, "alias-"+alias, "claude-sonnet-5")
			cmd, err := threadSpawnCommand(j, "t1", "why blocked", "explain", alias,
				[]string{"--session-id", "11111111-2222-4333-8444-555555555555", "--fork-session", "-r", "zeus-sess"})
			if err != nil {
				t.Fatalf("threadSpawnCommand(model %q): %v", alias, err)
			}
			if got := contractModelFlagValue(t, cmd); got != want {
				t.Errorf("--model = %q, want the exact id %q for alias %q", got, want, alias)
			}
			if strings.Contains(cmd, "--model "+alias+" ") || strings.HasSuffix(cmd, "--model "+alias) {
				t.Errorf("command passes the bare alias %q as --model: %q", alias, cmd)
			}
		})
	}
}

// TestContractThreadSpawnCommandRefusesAnUnknownModel is row (2): a
// non-empty model that is NOT a key errors and renders NO command. Falling
// back to the default instead would be the worst outcome available — the
// thread comes up on a tier nobody chose and nothing says so.
func TestContractThreadSpawnCommandRefusesAnUnknownModel(t *testing.T) {
	j := contractThreadJob(t, "bad-model", "")
	for _, bad := range []string{"gpt-5", "claude-opus-5", "Haiku", "opus-5"} {
		cmd, err := threadSpawnCommand(j, "t1", "why blocked", "explain", bad, []string{"-r", "sess"})
		if err == nil {
			t.Errorf("threadSpawnCommand(model %q) = %q, want a refusal", bad, cmd)
			continue
		}
		if cmd != "" {
			t.Errorf("threadSpawnCommand(model %q) returned both an error and a command %q — want no command", bad, cmd)
		}
	}
}

// TestContractThreadSpawnCommandFallsBackToTheJobModel is row (3): an empty
// per-thread model uses j.Model VERBATIM when the job set one. Verbatim
// matters — a job's `model:` field is an id (or whatever carl wrote), not an
// alias, so passing it through core.ThreadModels would resolve to nothing.
func TestContractThreadSpawnCommandFallsBackToTheJobModel(t *testing.T) {
	j := contractThreadJob(t, "job-model", "claude-sonnet-5")

	cmd, err := threadSpawnCommand(j, "t1", "why blocked", "explain", "", []string{"-r", "sess"})
	if err != nil {
		t.Fatalf("threadSpawnCommand: %v", err)
	}
	if got := contractModelFlagValue(t, cmd); got != "claude-sonnet-5" {
		t.Errorf("--model = %q, want the job's own model %q verbatim", got, "claude-sonnet-5")
	}
}

// TestContractThreadSpawnCommandFallsBackToDefaultModel is row (4): no
// per-thread model and no job model lands on core.DefaultModel — the
// behavior that predates this change, which the contract keeps.
func TestContractThreadSpawnCommandFallsBackToDefaultModel(t *testing.T) {
	j := contractThreadJob(t, "no-model", "")

	cmd, err := threadSpawnCommand(j, "t1", "why blocked", "explain", "", []string{"-r", "sess"})
	if err != nil {
		t.Fatalf("threadSpawnCommand: %v", err)
	}
	if got := contractModelFlagValue(t, cmd); got != core.DefaultModel {
		t.Errorf("--model = %q, want core.DefaultModel %q", got, core.DefaultModel)
	}
}

// TestContractThreadSpawnCommandCarriesExactlyOneModelFlag sweeps every
// resolution row for the flag-count obligation in one place, across both
// spawn shapes (branch's fork args and revive's resume args), so a row that
// APPENDS its resolved model to an argv that already carried one is caught
// wherever it is introduced.
func TestContractThreadSpawnCommandCarriesExactlyOneModelFlag(t *testing.T) {
	cases := []struct {
		name     string
		jobModel string
		model    string
	}{
		{"alias over a job model", "claude-sonnet-5", "haiku"},
		{"alias with no job model", "", "opus"},
		{"job model fallback", "claude-sonnet-5", ""},
		{"default fallback", "", ""},
	}
	resumes := map[string][]string{
		"branch": {"--session-id", "11111111-2222-4333-8444-555555555555", "--fork-session", "-r", "zeus-sess"},
		"revive": {"-r", "thread-sess"},
	}
	for _, c := range cases {
		for shape, resume := range resumes {
			t.Run(c.name+"/"+shape, func(t *testing.T) {
				j := contractThreadJob(t, "count-"+strings.ReplaceAll(c.name, " ", "-")+"-"+shape, c.jobModel)
				cmd, err := threadSpawnCommand(j, "t1", "why blocked", "", c.model, resume)
				if err != nil {
					t.Fatalf("threadSpawnCommand: %v", err)
				}
				contractModelFlagValue(t, cmd) // fails unless there is exactly one
			})
		}
	}
}

// ---------------------------------------------------------------------------
// revive preserves the thread's model
// ---------------------------------------------------------------------------

// contractReviveFixture stands a job up with one CLOSED thread row carrying
// `model`, and returns the row AS STORED — which is what ReviveThread reads
// and what the resolution must run off. Mirrors thread_test.go's
// reviveFixture; separate only because that one hardcodes no model.
func contractReviveFixture(t *testing.T, st *store.Store, slug, model string) (*Job, store.Thread) {
	t.Helper()
	sr := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "kraken", State: "working"})
	j := &Job{ID: sr.ID, Slug: sr.Slug}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	row, err := st.AddThreadNext(store.Thread{
		JobID: j.ID, Title: "why blocked", SessionID: "sess-1", State: "live", Model: model,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.CloseThread(j.ID, row.TID, 0); err != nil {
		t.Fatal(err)
	}
	stored, err := st.GetThread(j.ID, row.TID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { threadSpawns.Delete(threadSpawnKey(j.ID, row.TID)) })
	return j, *stored
}

// TestContractReviveResolvesTheStoredModel is the contract's own worked
// example: a thread branched on haiku revives with
// `--model claude-haiku-4-5-20251001`.
//
// Driven through reviveClaimed rather than ReviveThread because that is the
// seam this package's own revive tests already use — ReviveThread's half is
// refusals (a live cell, a worktree, a discovery scan), none of which stand
// up under `go test`, while reviveClaimed takes the windowOpener as a
// parameter and so hands the test the exact command the window would have
// run. The stored row is what it is given, which is precisely the claim:
// the model comes from the ROW, not from anything the caller remembers.
func TestContractReviveResolvesTheStoredModel(t *testing.T) {
	st := useTestStore(t)
	j, row := contractReviveFixture(t, st, "revive-model", "haiku")
	if row.Model != "haiku" {
		t.Fatalf("fixture row lost its model before the revive: %+v", row)
	}

	var gotArgv []string
	open := func(_, _, _ string, _, argv []string) (string, error) {
		gotArgv = argv
		return "cell-x:3 (%17)", nil
	}
	if _, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), open); err != nil {
		t.Fatalf("reviveClaimed: %v", err)
	}
	if len(gotArgv) != 1 {
		t.Fatalf("revive opened the window with %d commands, want 1: %q", len(gotArgv), gotArgv)
	}
	if got := contractModelFlagValue(t, gotArgv[0]); got != contractThreadModelIDs["haiku"] {
		t.Errorf("revived thread's --model = %q, want %q", got, contractThreadModelIDs["haiku"])
	}
	// and the row still carries the alias afterwards, so the NEXT revive
	// resolves the same way
	after, err := st.GetThread(j.ID, row.TID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Model != "haiku" {
		t.Errorf("row's model after a revive = %q, want %q", after.Model, "haiku")
	}
}

// TestContractReviveOfALegacyThreadFallsBack: a thread branched before this
// change has model="", so its revive must go on resolving exactly as it
// always did — j.Model, else core.DefaultModel. A blank model treated as
// invalid here would make every pre-existing thread unrevivable.
func TestContractReviveOfALegacyThreadFallsBack(t *testing.T) {
	st := useTestStore(t)
	j, row := contractReviveFixture(t, st, "revive-legacy-model", "")

	var gotArgv []string
	open := func(_, _, _ string, _, argv []string) (string, error) {
		gotArgv = argv
		return "cell-x:4 (%18)", nil
	}
	if _, err := reviveClaimed(st, j, row, CellSession(j), t.TempDir(), open); err != nil {
		t.Fatalf("reviveClaimed on a legacy (model-less) thread: %v", err)
	}
	if len(gotArgv) != 1 {
		t.Fatalf("revive opened the window with %d commands, want 1: %q", len(gotArgv), gotArgv)
	}
	if got := contractModelFlagValue(t, gotArgv[0]); got != core.DefaultModel {
		t.Errorf("legacy thread revived on --model %q, want core.DefaultModel %q", got, core.DefaultModel)
	}
}
