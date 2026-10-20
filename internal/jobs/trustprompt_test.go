package jobs

import "testing"

// TestTrustPromptSelectsNo pins the one question acceptTrustPrompts asks
// before pressing Enter: is the highlighted option "No, exit"? Claude Code
// 2.1.30x lists that option first and pre-selects it, so a bare Enter quits
// claude; older claude and codex pre-select their "yes" option.
func TestTrustPromptSelectsNo(t *testing.T) {
	cases := []struct {
		name string
		text string
		want bool
	}{
		{
			name: "claude 2.1.30x pre-selects No, exit",
			text: "Quick safety check: Is this a project you created or one you trust?\n\n ❯ No, exit\n   Yes, I trust this folder\n\n Enter to confirm · Esc to cancel",
			want: true,
		},
		{
			name: "older claude pre-selects Yes",
			text: "Do you trust the files in this folder?\n\n ❯ Yes, I trust this folder\n   No, exit\n",
			want: false,
		},
		{
			name: "codex pre-selects 1. Yes, continue",
			text: "Do you trust the contents of this directory?\n\n❯ 1. Yes, continue\n  2. No, quit\n",
			want: false,
		},
		{
			name: "No, exit present but not on the cursor line",
			text: " ❯ Yes, I trust this folder\n   No, exit",
			want: false,
		},
		{
			name: "old option order with the cursor on No",
			text: "Do you trust the files in this folder?\n\n   Yes, I trust this folder\n ❯ No, exit\n",
			want: true,
		},
		{
			name: "a stray cursor line above the dialog: only the first cursor line counts",
			text: " ❯ some earlier prompt\nQuick safety check: Is this a project you created or one you trust?\n ❯ No, exit\n   Yes, I trust this folder\n",
			want: false,
		},
		{
			name: "no cursor at all",
			text: "No, exit\nYes, I trust this folder",
			want: false,
		},
		{
			name: "empty",
			text: "",
			want: false,
		},
	}
	for _, c := range cases {
		if got := trustPromptSelectsNo(c.text); got != c.want {
			t.Errorf("%s: trustPromptSelectsNo = %v, want %v", c.name, got, c.want)
		}
	}
}
