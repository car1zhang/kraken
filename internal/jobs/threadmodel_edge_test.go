package jobs

import (
	"os"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
)

// threadmodel_edge_test.go — the EDGE dimension of the per-thread model rows
// of artifacts/50-contract.md (slice S1), for internal/jobs: the refusal, the
// resolution ORDER, and the one-flag invariant.
//
// Written BLIND, against the frozen contract text alone — no implementation
// was read. The obligations exercised here, verbatim from the contract's
// `jobs.threadSpawnCommand` row:
//
//	Model resolution, in order: (1) non-empty `model` that is a
//	`core.ThreadModels` key → that map's exact id appears on the command line
//	as the `--model` value; (2) non-empty `model` that is NOT a key → error,
//	no command; (3) empty `model` → `j.Model` verbatim when non-empty; (4)
//	both empty → `core.DefaultModel`. The rendered command contains exactly
//	one `--model` flag.
//
// Package-level identifiers are `edge`-prefixed by this package's own
// convention (limitrescue_edge_test.go et al), and because a sibling blind
// author writes `contract`-prefixed declarations into the same package.

// edgeThreadSpawnSig pins the frozen 6-arg signature at COMPILE time, so a
// dropped or retyped parameter fails the build here rather than surfacing as
// an assertion failure below (a same-type transposition is beyond any
// signature pin — the assertions are what hold the argument ORDER).
var edgeThreadSpawnSig func(*Job, string, string, string, string, string, string, []string) (string, error) = threadSpawnCommand

// edgeBranchThreadSig pins BranchThread's frozen 4-arg signature the same
// way, against a dropped or retyped parameter — the same caveat about
// same-type transpositions applies. Only the first three refusals — no
// store, a bad model, and a dead cell — are reachable with a wired store;
// everything past FindCellSession (the worktree probe, the zeus fork target,
// the spawn itself) needs a live tmux cell, which is why the signature pin
// above is the rest of what this file can honestly hold.
var edgeBranchThreadSig func(*Job, string, string, string) (store.Thread, error) = BranchThread

// edgeThreadCellDir sandboxes HOME and creates the cell dir threadSpawnCommand
// stages its brief and seed files into: stagedCommand os.WriteFile's them with
// no MkdirAll, so a missing directory is a failed spawn rather than a model
// question. Same shape as thread_test.go's threadCellDir at HEAD, redeclared
// under an `edge` name so this file does not depend on a helper another
// author may adapt.
func edgeThreadCellDir(t *testing.T, j *Job) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
}

// edgeModelFlagValue returns the single `--model` value on a rendered command,
// failing the test unless there is EXACTLY one flag. Counting is the point:
// the cheapest way to "support" a per-thread model is to append a second
// `--model` after the one cellClaudeArgs already emits, which claude resolves
// by last-wins — so it would look correct in a Contains-based test while the
// command line carried two contradictory tiers, and a reader (kraken's own
// discovery reads the model back OFF the command line) would see the wrong one.
func edgeModelFlagValue(t *testing.T, cmd string) string {
	t.Helper()
	fields := strings.Fields(cmd)
	var vals []string
	for i, f := range fields {
		if f != "--model" {
			continue
		}
		if i+1 >= len(fields) {
			t.Fatalf("command ends with a bare --model flag: %q", cmd)
		}
		vals = append(vals, fields[i+1])
	}
	if len(vals) != 1 {
		t.Fatalf("command carries %d --model flags (%v), want exactly one: %q", len(vals), vals, cmd)
	}
	return vals[0]
}

// TestEdgeThreadSpawnCommandRefusesUnknownModel is resolution rule (2): a
// non-empty model that is not a ThreadModels key is an ERROR and yields NO
// command. Both halves matter. A rendered command would be spawned — tmux
// would open a window running a claude that either refuses to start or, worse,
// silently falls back to some other tier — and the caller has no other signal
// that the model it was handed was nonsense.
//
// The refused set deliberately includes the four EXACT ids: they are real
// model ids and a correct-looking thing to pass, but the map is keyed by
// alias, so they are not keys and must be refused like any other garbage.
func TestEdgeThreadSpawnCommandRefusesUnknownModel(t *testing.T) {
	bad := []struct{ name, model string }{
		{"garbage", "gpt-5"},
		{"exact opus id", "claude-opus-5"},
		{"exact haiku id", "claude-haiku-4-5-20251001"},
		{"exact sonnet id", "claude-sonnet-5"},
		{"exact fable id", "claude-fable-5-1"},
		{"title case", "Haiku"},
		{"upper case", "OPUS"},
		{"trailing space", "sonnet "},
		{"leading space", " fable"},
		{"whitespace only", " "},
		{"prefix of alias", "hai"},
	}
	for _, c := range bad {
		t.Run(c.name, func(t *testing.T) {
			j := &Job{Slug: "edge-badmodel"}
			edgeThreadCellDir(t, j)

			cmd, err := threadSpawnCommand(j, "t1", "why is it blocked", "explain", c.model, "ask", "",
				[]string{"-r", "zeus-sess"})
			if err == nil {
				t.Fatalf("threadSpawnCommand(model=%q) = %q, nil; want a refusal", c.model, cmd)
			}
			if cmd != "" {
				t.Fatalf("threadSpawnCommand(model=%q) returned a command alongside its error: %q", c.model, cmd)
			}
		})
	}
}

// TestEdgeThreadSpawnCommandRefusesUnknownModelEvenWithAJobModel closes the
// obvious wrong shape of rule (2): falling BACK to j.Model (or DefaultModel)
// when the requested alias is unrecognised. A fallback is exactly what rules
// (3) and (4) describe for an EMPTY model, and reusing it for an invalid one
// turns a typo into a silent tier change — carl asks for "haiuk" and gets a
// thread on fable with no error anywhere. The contract says error, no command.
func TestEdgeThreadSpawnCommandRefusesUnknownModelEvenWithAJobModel(t *testing.T) {
	j := &Job{Slug: "edge-badmodel-fallback", Model: "claude-sonnet-4-5"}
	edgeThreadCellDir(t, j)

	cmd, err := threadSpawnCommand(j, "t1", "title", "seed", "haiuk", "ask", "", []string{"-r", "zeus-sess"})
	if err == nil {
		t.Fatalf("threadSpawnCommand fell back instead of refusing a typo'd alias: %q", cmd)
	}
	if cmd != "" {
		t.Fatalf("refused threadSpawnCommand still returned a command: %q", cmd)
	}
}

// TestEdgeThreadSpawnCommandModelResolutionOrder walks all four rules on one
// seam, asserting the EXACTLY-ONE-flag invariant on every successful render.
//
// The alias cases assert the resolved value is `core.ThreadModels[alias]` AND
// that it is neither the alias itself nor the job's own model: an alias that
// reached the command line raw is a model id claude does not know, and a
// resolution that quietly preferred j.Model would make the picker decorative.
func TestEdgeThreadSpawnCommandModelResolutionOrder(t *testing.T) {
	cases := []struct {
		name     string
		jobModel string
		model    string
		want     string
	}{
		// (4) both empty
		{"both empty falls back to DefaultModel", "", "", core.DefaultModel},
		// (3) empty model, job model set — verbatim, not re-resolved
		{"empty model takes j.Model verbatim", "claude-sonnet-4-5", "", "claude-sonnet-4-5"},
		// (1) alias wins over everything, with and without a job model
		{"haiku with no job model", "", "haiku", core.ThreadModels["haiku"]},
		{"sonnet with no job model", "", "sonnet", core.ThreadModels["sonnet"]},
		{"opus with no job model", "", "opus", core.ThreadModels["opus"]},
		{"fable with no job model", "", "fable", core.ThreadModels["fable"]},
		{"haiku beats a set j.Model", "claude-sonnet-4-5", "haiku", core.ThreadModels["haiku"]},
		{"fable beats a set j.Model", "claude-sonnet-4-5", "fable", core.ThreadModels["fable"]},
		// the alias whose id happens to equal DefaultModel must still be an
		// explicit resolution, not an accident of the fallback
		{"opus beats a set j.Model", "claude-sonnet-4-5", "opus", core.ThreadModels["opus"]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.want == "" {
				t.Fatalf("expected model for %q resolved to \"\" — core.ThreadModels is missing a key", c.name)
			}
			j := &Job{Slug: "edge-resolve", Model: c.jobModel}
			edgeThreadCellDir(t, j)

			cmd, err := threadSpawnCommand(j, "t1", "the title", "the seed", c.model, "ask", "",
				[]string{"--session-id", "11111111-2222-4333-8444-555555555555", "--fork-session", "-r", "zeus-sess"})
			if err != nil {
				t.Fatalf("threadSpawnCommand: %v", err)
			}
			if got := edgeModelFlagValue(t, cmd); got != c.want {
				t.Fatalf("--model = %q, want %q (job model %q, requested %q): %s", got, c.want, c.jobModel, c.model, cmd)
			}
			if c.model != "" {
				// an alias must never reach the command line raw: the map's
				// whole reason for existing is that `--model opus` silently
				// follows whatever claude ships next
				if got := edgeModelFlagValue(t, cmd); got == c.model {
					t.Fatalf("--model = %q — the alias was passed through instead of resolved", got)
				}
			}
		})
	}
}

// TestEdgeThreadSpawnCommandOneModelFlagOnReviveArgs re-runs the one-flag
// invariant on the RESUME shape, since ReviveThread renders through this same
// helper with `-r <session>` and no seed. The resume path is where a second
// `--model` is easiest to miss: nothing in a revive's output is inspected by
// eye, and last-wins would hide the duplicate forever.
func TestEdgeThreadSpawnCommandOneModelFlagOnReviveArgs(t *testing.T) {
	for _, model := range []string{"", "haiku", "sonnet", "opus", "fable"} {
		name := model
		if name == "" {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			j := &Job{Slug: "edge-revive-model"}
			edgeThreadCellDir(t, j)

			cmd, err := threadSpawnCommand(j, "t2", "the title", "", model, "ask", "", []string{"-r", "thread-sess"})
			if err != nil {
				t.Fatalf("threadSpawnCommand: %v", err)
			}
			want := core.DefaultModel
			if model != "" {
				want = core.ThreadModels[model]
			}
			if want == "" {
				t.Fatalf("core.ThreadModels[%q] is empty", model)
			}
			if got := edgeModelFlagValue(t, cmd); got != want {
				t.Fatalf("revive --model = %q, want %q: %s", got, want, cmd)
			}
			// resuming must not turn into a fork just because a model was
			// asked for — the resume args are the caller's, untouched
			if strings.Contains(cmd, "--fork-session") {
				t.Fatalf("revive command grew --fork-session: %q", cmd)
			}
			if !strings.Contains(cmd, "-r thread-sess") {
				t.Fatalf("revive command lost its resume target: %q", cmd)
			}
		})
	}
}

// TestEdgeBranchThreadRefusesABadModelBeforeTheDeadCellCheck is a SIGHTED
// addition, made in the review round — unlike every other test in this file it
// was NOT authored blind. My blind report disclosed this obligation as
// unpinnable, on the belief that BranchThread's live-cell guard fired before
// anything else and so hid the model check behind a refusal no fixture could
// get past. Reading the shipped code corrected that: the model is resolved at
// the top of BranchThread, ahead of its FindCellSession lookup, and
// thread_test.go's TestBranchThreadRefusesADeadCell already proves a wired
// store reaches the body. So the contract's "an invalid model is refused
// before any tmux window is opened" has an observable after all, and this is
// it.
//
// The ORDER is the assertion, and the two halves are what make it one. A job
// with no live cell has two things wrong with it; which error comes back says
// which check ran first. With a bad model the answer must be the model — proof
// the refusal landed before the cell was ever looked for, and therefore before
// any window could be opened. With an empty model the SAME fixture must give
// the dead-cell error, which is what stops the first half passing vacuously: it
// proves the fixture really has no live cell, rather than the test having
// wandered into a machine where one happened to exist.
func TestEdgeBranchThreadRefusesABadModelBeforeTheDeadCellCheck(t *testing.T) {
	st := useTestStore(t)
	sr := mustCreateJob(t, st, store.Job{
		Slug: "edge-badmodel-" + strings.ToLower(t.Name()), Repo: "kraken", State: "working",
	})
	j := &Job{ID: sr.ID, Slug: sr.Slug}

	// the control: this fixture's cell is dead, and an acceptable model lets
	// the branch get far enough to say so
	if _, err := BranchThread(j, "why", "", ""); err == nil {
		t.Fatal("want a refusal branching off a dead cell")
	} else if !strings.Contains(err.Error(), "no live cell to branch") {
		t.Fatalf("control refusal = %q, want the dead-cell message — the fixture is not the one this test needs", err)
	}

	for _, model := range []string{"gpt-5", "Haiku", "claude-opus-5", "sonnet "} {
		t.Run(model, func(t *testing.T) {
			_, err := BranchThread(j, "why", "", model)
			if err == nil {
				t.Fatalf("BranchThread(model=%q) = nil, want a refusal", model)
			}
			if strings.Contains(err.Error(), "no live cell to branch") {
				t.Fatalf("BranchThread(model=%q) refused the CELL first: %q — the model check must land before any tmux lookup", model, err)
			}
			if !strings.Contains(err.Error(), "bad thread model") {
				t.Fatalf("BranchThread(model=%q) refusal = %q, want the bad-model message", model, err)
			}
		})
	}

	// nothing was reserved by any of it: the refusals all land ahead of
	// AddThreadNext, so the job's thread table is untouched and its counter
	// still starts at t1
	threads, err := st.ListThreads(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(threads) != 0 {
		t.Fatalf("refused branches left %d thread rows behind: %+v", len(threads), threads)
	}
	next, err := st.NextThreadID(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next != "t1" {
		t.Fatalf("NextThreadID = %q after refused branches, want t1 — a tid was burned", next)
	}
}

// TestEdgeThreadModelsKeysAreExactlyTheFourAliases holds the map's boundary
// from the consumer side: threadSpawnCommand's rule (2) is defined by what is
// NOT a key, so an extra key ("claude-opus-5" added "for convenience", an
// "" entry, a "sonnet-4-5") silently widens every refusal above into an
// acceptance without any test in this package failing.
func TestEdgeThreadModelsKeysAreExactlyTheFourAliases(t *testing.T) {
	want := map[string]bool{"haiku": true, "sonnet": true, "opus": true, "fable": true}
	if len(core.ThreadModels) != len(want) {
		t.Fatalf("core.ThreadModels = %v, want exactly %d keys", core.ThreadModels, len(want))
	}
	for k, v := range core.ThreadModels {
		if !want[k] {
			t.Errorf("core.ThreadModels has unexpected key %q — it widens threadSpawnCommand's refusal", k)
		}
		if v == "" {
			t.Errorf("core.ThreadModels[%q] is empty", k)
		}
		if v == k {
			t.Errorf("core.ThreadModels[%q] = %q — the value must be an exact model id, never the alias", k, v)
		}
	}
}
