package jobs

// prcreatequote_contract_test.go — athena-logic's BLIND contract tests for
// slice D of dream-2026-10-30: the `gh pr create` gate ignores quoted and
// heredoc text (ghPrCreateInvoked). Authored from artifacts/50-contract.md
// alone.
//
// LOGIC dimension. Every package-level identifier is prefixed
// `contractPRCreate`.

import "testing"

func TestContractPRCreateInvoked(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		// the contract's named false cases
		{"inside a single-quoted grep pattern", `grep -E 'a|gh pr create' f.txt`, false},
		{"inside a heredoc body", "python3 - <<'EOF'\nprint(\"gh pr create\")\nEOF", false},
		{"inside a double-quoted echo", `echo "gh pr create"`, false},
		// the contract's named true cases
		{"plain create", `gh pr create --title "x"`, true},
		{"after a command-substitution env assignment", `GH_TOKEN=$(gh auth token) gh pr create --title "x"`, true},
		{"after cd &&", `cd /tmp && gh pr create`, true},
		{"after ; with a quoted body", `x; gh pr create --body 'y'`, true},
		// an env assignment whose value is a quoted string stays true
		{"after a double-quoted env assignment", `GH_TOKEN="tok-invented-1" gh pr create --fill`, true},
		// unrelated
		{"pr view", `gh pr view 12 --json state`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ghPrCreateInvoked(c.cmd); got != c.want {
				t.Errorf("ghPrCreateInvoked(%q) = %v, want %v", c.cmd, got, c.want)
			}
		})
	}
}
