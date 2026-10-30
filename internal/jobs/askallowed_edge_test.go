package jobs

// askallowed_edge_test.go — athena-edge's BLIND tests for slice G of
// dream-2026-10-30 (an owner's ask thread can actually use its web tools):
// the --allowedTools pair's placement and its absence on every non-owner
// combination. Authored from 50-contract.md alone; the pre-existing TEST
// helpers of askthread_contract_test.go (askContractEnv, askContractJob,
// askContractWorktree, askContractRow, askContractSelf, askContractRemote)
// are reused. Every package-level identifier is prefixed edgeAA.

import (
	"strings"
	"testing"
)

// edgeAAIndexes returns every index of v in argv.
func edgeAAIndexes(argv []string, v string) []int {
	var out []int
	for i, a := range argv {
		if a == v {
			out = append(out, i)
		}
	}
	return out
}

func TestEdgeAskArgvAllowedToolsOwnerPair(t *testing.T) {
	for _, resume := range []bool{false, true} {
		name := "fork"
		if resume {
			name = "resume"
		}
		t.Run(name, func(t *testing.T) {
			st := askContractEnv(t)
			j := askContractJob(t, st)
			askContractWorktree(t, j)
			row := askContractRow(j, askContractSelf, "")
			argv, _, _, err := AskArgv(j, row, askContractSelf, resume)
			if err != nil {
				t.Fatalf("AskArgv: %v", err)
			}
			tools := edgeAAIndexes(argv, "--tools")
			allowed := edgeAAIndexes(argv, "--allowedTools")
			if len(tools) != 1 {
				t.Fatalf("--tools appears %d times in %q, want once", len(tools), argv)
			}
			if len(allowed) != 1 {
				t.Fatalf("--allowedTools appears %d times in %q, want once", len(allowed), argv)
			}
			ti, ai := tools[0], allowed[0]
			if ti+1 >= len(argv) || ai+1 >= len(argv) {
				t.Fatalf("a flag is the last element, its value is missing: %q", argv)
			}
			if argv[ti+1] != AskThreadTools {
				t.Errorf("--tools value = %q, want %q", argv[ti+1], AskThreadTools)
			}
			if argv[ai+1] != argv[ti+1] {
				t.Errorf("--allowedTools value = %q, want the --tools value %q", argv[ai+1], argv[ti+1])
			}
			if ai <= ti+1 {
				t.Errorf("--allowedTools at %d, want after the --tools pair (%d, %d): %q", ai, ti, ti+1, argv)
			}
			if !strings.Contains(argv[ai+1], "WebFetch") || !strings.Contains(argv[ai+1], "WebSearch") {
				t.Errorf("allowed set %q lacks the web tools", argv[ai+1])
			}
		})
	}
}

func TestEdgeAskArgvNoAllowedToolsWhenToolless(t *testing.T) {
	cases := []struct {
		name   string
		row    ThreadOrigin
		sender ThreadOrigin
	}{
		{"self row, remote sender", askContractSelf, askContractRemote},
		{"remote row, self sender", askContractRemote, askContractSelf},
		{"remote row, remote sender", askContractRemote, askContractRemote},
	}
	for _, c := range cases {
		for _, resume := range []bool{false, true} {
			t.Run(c.name, func(t *testing.T) {
				st := askContractEnv(t)
				j := askContractJob(t, st)
				askContractWorktree(t, j)
				row := askContractRow(j, c.row, "")
				argv, _, _, err := AskArgv(j, row, c.sender, resume)
				if err != nil {
					t.Fatalf("AskArgv(resume=%v): %v", resume, err)
				}
				for i, a := range argv {
					if a == "--allowedTools" || strings.HasPrefix(a, "--allowedTools=") || a == "--allowed-tools" || strings.HasPrefix(a, "--allowed-tools=") {
						t.Errorf("resume=%v: argv[%d] = %q, want no --allowedTools at all: %q", resume, i, a, argv)
					}
				}
				ti := edgeAAIndexes(argv, "--tools")
				if len(ti) != 1 || ti[0]+1 >= len(argv) || argv[ti[0]+1] != "" {
					t.Errorf("resume=%v: want exactly one `--tools \"\"` pair, got %q", resume, argv)
				}
				for _, a := range argv {
					if a == AskThreadTools {
						t.Errorf("resume=%v: the owner tool set leaked into a toolless argv: %q", resume, argv)
					}
				}
			})
		}
	}
}

// The pre-existing ordering rules hold with the new pair present.
func TestEdgeAskArgvAllowedToolsKeepsCommonFlags(t *testing.T) {
	st := askContractEnv(t)
	j := askContractJob(t, st)
	askContractWorktree(t, j)
	row := askContractRow(j, askContractSelf, "")
	argv, _, _, err := AskArgv(j, row, askContractSelf, true)
	if err != nil {
		t.Fatalf("AskArgv: %v", err)
	}
	for _, pair := range [][2]string{{"--permission-mode", "default"}, {"--permission-prompts", "none"}} {
		idx := edgeAAIndexes(argv, pair[0])
		if len(idx) != 1 || idx[0]+1 >= len(argv) || argv[idx[0]+1] != pair[1] {
			t.Errorf("want exactly one `%s %s` pair in %q", pair[0], pair[1], argv)
		}
	}
	if len(edgeAAIndexes(argv, "--restricted")) != 1 {
		t.Errorf("want --restricted once in %q", argv)
	}
	n := len(argv)
	if n < 2 || argv[n-2] != "-r" || argv[n-1] != row.SessionID {
		t.Errorf("resume tail = %q, want the argv to end `-r %s`", argv, row.SessionID)
	}
}
