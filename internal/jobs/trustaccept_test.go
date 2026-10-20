package jobs

import (
	"strings"
	"testing"
)

// claudeTrustDialog2_1_304 is claude 2.1.304's folder-trust dialog as tmux
// captured it on 2026-10-20 (job 610): the cursor sits on "No, exit", so a
// bare Enter exits claude.
const claudeTrustDialog2_1_304 = ` Accessing workspace:
 /Users/x/.kraken/cells/hollow-kraken-609/wt-utils
 Quick safety check: Is this a project you created or one you trust? (Like your own code, a well-known open source project, or work from your team). If not, take a moment to review what's in this folder first.
 Claude Code'll be able to read, edit, and execute files here.
 Security guide
 ❯ No, exit
   Yes, I trust this folder
 Enter to confirm · Esc to cancel`

func TestTrustAcceptKeysMovesOffNoExit(t *testing.T) {
	got := strings.Join(trustAcceptKeys(claudeTrustDialog2_1_304), " ")
	if got != "Down Enter" {
		t.Fatalf("trustAcceptKeys(claude 2.1.304 dialog) = %q, want \"Down Enter\"", got)
	}
}

func TestTrustAcceptKeysEnterWhenYesPreselected(t *testing.T) {
	cases := map[string]string{
		// claude before 2.1.304: yes pre-selected
		"claude-old": " Is this a project you created or one you trust?\n ❯ Yes, I trust this folder\n   No, exit\n Enter to confirm",
		// codex: yes pre-selected, numbered
		"codex": "Do you trust the contents of this directory?\n\n› 1. Yes, continue\n  2. No, quit\n",
		// no cursor line at all: nothing to read, keep the old behaviour
		"no-cursor": " Is this a project you created or one you trust?\n   Yes, I trust this folder\n   No, exit",
		// cursor on no, but no yes option anywhere: nothing to move to
		"no-yes": " Is this a project you created or one you trust?\n ❯ No, exit",
		"empty":  "",
	}
	for name, text := range cases {
		if got := strings.Join(trustAcceptKeys(text), " "); got != "Enter" {
			t.Errorf("%s: trustAcceptKeys = %q, want \"Enter\"", name, got)
		}
	}
}

func TestTrustAcceptKeysMovesUpWhenYesIsAbove(t *testing.T) {
	text := "Do you trust the contents of this directory?\n  1. Yes, continue\n❯ 2. No, quit\n"
	if got := strings.Join(trustAcceptKeys(text), " "); got != "Up Enter" {
		t.Fatalf("trustAcceptKeys(codex, cursor on no) = %q, want \"Up Enter\"", got)
	}
}

func TestTrustOptionLineStripsCursorAndNumber(t *testing.T) {
	cases := map[string]string{
		" ❯ No, exit":                 "No, exit",
		"   Yes, I trust this folder": "Yes, I trust this folder",
		"› 1. Yes, continue":          "Yes, continue",
		"  2. No, quit":               "No, quit",
	}
	for in, want := range cases {
		got, ok := trustOptionLine(in)
		if !ok || got != want {
			t.Errorf("trustOptionLine(%q) = %q, %v; want %q, true", in, got, ok, want)
		}
	}
	for _, in := range []string{"", " Security guide", " Enter to confirm · Esc to cancel", " Accessing workspace:", "Nothing to see"} {
		if _, ok := trustOptionLine(in); ok {
			t.Errorf("trustOptionLine(%q) = ok, want not an option", in)
		}
	}
}
