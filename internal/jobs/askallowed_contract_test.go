package jobs

// askallowed_contract_test.go — athena-logic's BLIND contract tests for
// slice G of dream-2026-10-30: an owner's ask thread can actually use its
// web tools (AskArgv carries --allowedTools beside --tools for the self/self
// tool set). Authored from artifacts/50-contract.md alone; reuses the
// pre-existing askContract* TEST helpers from askthread_contract_test.go.
//
// LOGIC dimension. Every package-level identifier is prefixed
// `contractAskAllowed`.

import (
	"testing"

	"kraken/internal/store"
)

// contractAskAllowedIndex returns every index of flag in argv.
func contractAskAllowedIndex(argv []string, flag string) []int {
	var out []int
	for i, a := range argv {
		if a == flag {
			out = append(out, i)
		}
	}
	return out
}

// contractAskAllowedCheckPresent asserts --allowedTools appears once, after
// the --tools pair, followed by the same value as --tools (AskThreadTools).
func contractAskAllowedCheckPresent(t *testing.T, argv []string) {
	t.Helper()
	tools := contractAskAllowedIndex(argv, "--tools")
	allowed := contractAskAllowedIndex(argv, "--allowedTools")
	if len(tools) != 1 || tools[0]+1 >= len(argv) {
		t.Fatalf("--tools at %v in %q, want exactly one with a value", tools, argv)
	}
	if len(allowed) != 1 || allowed[0]+1 >= len(argv) {
		t.Fatalf("--allowedTools at %v in %q, want exactly one with a value", allowed, argv)
	}
	if v := argv[tools[0]+1]; v != AskThreadTools {
		t.Errorf("--tools value = %q, want %q", v, AskThreadTools)
	}
	if v := argv[allowed[0]+1]; v != argv[tools[0]+1] {
		t.Errorf("--allowedTools value = %q, want the --tools value %q", v, argv[tools[0]+1])
	}
	if allowed[0] <= tools[0]+1 {
		t.Errorf("--allowedTools at %d, want after the --tools pair at %d..%d", allowed[0], tools[0], tools[0]+1)
	}
}

func contractAskAllowedCheckAbsent(t *testing.T, argv []string) {
	t.Helper()
	if idx := contractAskAllowedIndex(argv, "--allowedTools"); len(idx) != 0 {
		t.Errorf("--allowedTools present at %v in %q, want absent", idx, argv)
	}
}

func TestContractAskAllowedSelfSelf(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st)
	askContractWorktree(t, j)
	row := askContractRow(j, askContractSelf, "")
	for _, resume := range []bool{false, true} {
		argv, _, _, err := AskArgv(j, row, askContractSelf, resume)
		if err != nil {
			t.Fatalf("AskArgv(self, self, resume=%v): %v", resume, err)
		}
		contractAskAllowedCheckPresent(t, argv)
		// unchanged elements stay
		for _, want := range []string{"--permission-mode", "--permission-prompts", "--restricted"} {
			if len(contractAskAllowedIndex(argv, want)) != 1 {
				t.Errorf("resume=%v: %s missing or repeated in %q", resume, want, argv)
			}
		}
		if resume {
			askContractCheckResumeTail(t, argv, row)
		} else {
			askContractCheckForkTail(t, argv, row)
		}
	}
}

func TestContractAskAllowedEmptyToolSetHasNone(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st)
	askContractWorktree(t, j)
	cases := []struct {
		name string
		row  store.Thread
		from ThreadOrigin
	}{
		{"remote row, remote sender", askContractRow(j, askContractRemote, ""), askContractRemote},
		{"self row, remote sender", askContractRow(j, askContractSelf, ""), askContractRemote},
		{"remote row, self sender", askContractRow(j, askContractRemote, ""), askContractSelf},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for _, resume := range []bool{false, true} {
				argv, _, _, err := AskArgv(j, c.row, c.from, resume)
				if err != nil {
					t.Fatalf("AskArgv(resume=%v): %v", resume, err)
				}
				askContractCheckTools(t, argv, "")
				contractAskAllowedCheckAbsent(t, argv)
			}
		})
	}
}
