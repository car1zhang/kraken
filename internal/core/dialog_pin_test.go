package core

import "testing"

// TestDialogPinNumberedWrapFolds pins the numbered-cursor rule: a narrow pane
// wraps option 1 onto a second line whose text starts in option 1's text
// column, and that line must be folded into option 1 rather than counted as
// an option of its own (which would shift every arrow count below it).
func TestDialogPinNumberedWrapFolds(t *testing.T) {
	pane := " Do you want to proceed?\n" +
		" ❯ 1. Yes, and don't ask again for:\n" +
		"      npm test\n" +
		"   2. No, and tell Claude what to do differently\n"
	d, ok := ParseDialog(pane)
	if !ok {
		t.Fatalf("ParseDialog = false, want a 2-option dialog")
	}
	if len(d.Options) != 2 {
		t.Fatalf("got %d options %+v, want 2", len(d.Options), d.Options)
	}
	if got, want := d.Options[0].Text, "Yes, and don't ask again for: npm test"; got != want {
		t.Errorf("option 1 Text = %q, want %q", got, want)
	}
	if d.Options[1].Number != 2 || d.Options[1].Line != 3 {
		t.Errorf("option 2 = %+v, want Number 2 on line 3", d.Options[1])
	}
	if d.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0", d.Cursor)
	}
	if keys := DialogKeys(d, 1); len(keys) != 2 || keys[0] != "Down" || keys[1] != "Enter" {
		t.Errorf("DialogKeys(d, 1) = %v, want [Down Enter]", keys)
	}
}
