package jobs

// prcreatequote_edge_test.go — athena-edge's BLIND tests for slice D of
// dream-2026-10-30 (the `gh pr create` gate ignores quoted and heredoc
// text): boundaries of ghPrCreateInvoked's blanking. Authored from
// 50-contract.md alone. Every package-level identifier is prefixed edgePRC.

import "testing"

func TestEdgeGhPrCreateInvoked(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want bool
	}{
		// --- the contract's own examples
		{"grep pattern in single quotes", `grep -E 'a|gh pr create' f.txt`, false},
		{"python heredoc body", "python3 - <<'EOF'\nprint(\"gh pr create\")\nEOF", false},
		{"echo double-quoted", `echo "gh pr create"`, false},
		{"plain create", `gh pr create --title "x"`, true},
		{"env via command substitution", `GH_TOKEN=$(gh auth token) gh pr create --title "x"`, true},
		{"after &&", `cd /tmp && gh pr create`, true},
		{"after ; with quoted body", `x; gh pr create --body 'y'`, true},

		// --- refusals: never invoked
		{"empty", ``, false},
		{"gh pr view", `gh pr view 12 --json state`, false},
		{"gh pr list", `gh pr list --author alice`, false},
		{"echo single-quoted", `echo 'gh pr create'`, false},
		{"single-quoted then a harmless command", `echo 'gh pr create' && echo done`, false},
		{"commit message mentions it", `git commit -m "follow up with gh pr create later"`, false},
		{"single quote inside double quotes does not open a span", `echo "it's gh pr create"`, false},
		{"double quote inside single quotes does not open a span", `echo 'say "gh pr create" now'`, false},
		{"unquoted heredoc delimiter", "cat <<EOF > notes.md\ngh pr create --fill\nEOF", false},
		{"double-quoted heredoc delimiter", "cat <<\"EOF\" > notes.md\ngh pr create --fill\nEOF", false},
		{"dash heredoc with tab-indented terminator", "cat <<-EOF > notes.md\n\tgh pr create --fill\n\tEOF", false},
		{"heredoc with a custom delimiter", "tee out.txt <<'BODY'\nrun: cd x && gh pr create\nBODY", false},
		{"multi-line heredoc body", "cat <<'EOF'\nline one\nthen gh pr create\nline three\nEOF", false},

		// --- still invoked
		{"quoted env assignment value", `GH_TOKEN="abc123" gh pr create --fill`, true},
		{"single-quoted env assignment value with space", `FOO='a b' gh pr create --fill`, true},
		{"quoted arg before && then create", `echo 'say "hi"' && gh pr create --fill`, true},
		{"create after a heredoc ends", "cat <<'EOF' > body.md\nsummary text\nEOF\ncd . && gh pr create --body-file body.md", true},
		{"create after a heredoc whose body mentions it", "cat <<'EOF' > body.md\ndo not gh pr create here\nEOF\ncd . && gh pr create --body-file body.md", true},
		{"create with heredoc body-file of its own", "gh pr create --title t --body-file - <<'EOF'\nbody text\nEOF", true},
		{"create with a quoted title containing pipe", `gh pr create --title 'a | b' --body "c; d"`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := ghPrCreateInvoked(c.cmd); got != c.want {
				t.Errorf("ghPrCreateInvoked(%q) = %v, want %v", c.cmd, got, c.want)
			}
		})
	}
}
