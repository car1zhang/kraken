package core

import (
	"strings"
	"testing"
)

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

// TestDialogPinSwitchModelBelowEchoedMessage pins the pane that stalled a
// cell on 2026-11-04: Claude Code echoes the steering message led by ❯, and
// the /model "Switch model?" confirm then opens below it with its own ❯. The
// two glyphs share the 25-line window, and the old one-cursor rule refused
// the parse, so jev never saw the dialog.
func TestDialogPinSwitchModelBelowEchoedMessage(t *testing.T) {
	pane := "❯ this job was marked done, but it was reactivated for adjustments.\n" +
		"  re-read the current repo state and their merge state before acting\n" +
		"  steering: new annotations arrived\n" +
		"  ⎿  4 skills available\n" +
		"  ⎿  You've reached your Fable limit. Switch models with /model.\n" +
		"\n" +
		"✻ Cooked for 1s · done 6:13 PM\n" +
		"▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔▔\n" +
		"   Switch model?\n" +
		"   Your next response will be slower and use more tokens\n" +
		"\n" +
		"   This conversation is cached for the current model. Switching to Opus 5.5 means the full history gets re-read.\n" +
		"\n" +
		"   ❯ 1. Yes, switch to Opus 5.5\n" +
		"     2. No, go back\n"
	d, ok := ParseDialog(pane)
	if !ok {
		t.Fatalf("ParseDialog = false, want the Switch model dialog")
	}
	if got := []string{d.Options[0].Text, d.Options[len(d.Options)-1].Text}; len(d.Options) != 2 ||
		got[0] != "Yes, switch to Opus 5.5" || got[1] != "No, go back" {
		t.Fatalf("options = %+v, want [Yes, switch to Opus 5.5] [No, go back]", d.Options)
	}
	if d.Cursor != 0 {
		t.Errorf("Cursor = %d, want 0", d.Cursor)
	}
	if !strings.Contains(d.Header, "Switch model?") {
		t.Errorf("Header = %q, want it to hold %q", d.Header, "Switch model?")
	}
	if keys := DialogKeys(d, 0); len(keys) != 1 || keys[0] != "Enter" {
		t.Errorf("DialogKeys(d, 0) = %v, want [Enter]", keys)
	}
}
