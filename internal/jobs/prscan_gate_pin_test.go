package jobs

import "testing"

// TestGhPrCreateInvokedKeepsSubstitutionsAndComments pins two regressions the
// first span blanker introduced: a create run inside a `$(…)` within double
// quotes was blanked away with the quote's contents, and an apostrophe in a
// `#` comment opened a single-quote span that swallowed the next line's real
// command. Both shapes matched the raw pattern before blanking existed and
// must keep matching.
func TestGhPrCreateInvokedKeepsSubstitutionsAndComments(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{`URL="$(gh pr create --fill)"`, true},
		{`echo "$(gh pr create --fill)"`, true},
		{`out="$(cd /tmp && gh pr create --title "x (y)")"`, true},
		{"# don't forget the body\ngh pr create --fill", true},
		{"  # it's fine\ngh pr create --fill", true},
		{"echo a; # note's\ngh pr create --fill", true},
		{`echo 'gh pr create'`, false},
		{"# gh pr create\necho done", false},
		{`echo "a#b" && grep -E 'x|gh pr create' f`, false},
		{`echo "$(gh pr view 1)" && echo 'gh pr create'`, false},
		// a quoted mention INSIDE a kept substitution is blanked too
		{`echo "$(grep -E 'a|gh pr create' f.txt)"`, false},
		{`x="$(echo '|gh pr create')"`, false},
		{`"$(echo "$(gh pr create)")"`, true},
	}
	for _, c := range cases {
		if got := ghPrCreateInvoked(c.cmd); got != c.want {
			t.Errorf("ghPrCreateInvoked(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}
