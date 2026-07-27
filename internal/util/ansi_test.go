package util

import "testing"

func TestStatusColorPolling(t *testing.T) {
	if got := StatusColor("polling"); got != Cyan {
		t.Errorf("polling: got %q, want Cyan", got)
	}
}

func TestVisWidth(t *testing.T) {
	if got := VisWidth("hello"); got != 5 {
		t.Errorf("plain: got %d", got)
	}
	if got := VisWidth("\x1b[36mhello\x1b[0m"); got != 5 {
		t.Errorf("colored: got %d", got)
	}
	if got := VisWidth("\x1b[2m\x1b[0m"); got != 0 {
		t.Errorf("codes only: got %d", got)
	}
}

func TestColor256(t *testing.T) {
	if got := Color256(103); got != "\x1b[38;5;103m" {
		t.Fatalf("Color256(103) = %q", got)
	}
	// composes with Paint exactly like Cyan/Green do, including the no-op
	// when colour is off (the viewer renders the selected row with
	// color=false so embedded resets can't break the reverse-video bar)
	if got := Paint(false, Color256(160), "job"); got != "job" {
		t.Fatalf("Paint(false, ...) = %q, want the bare string", got)
	}
	painted := Paint(true, Color256(160), "job")
	if painted != "\x1b[38;5;160mjob"+Reset {
		t.Fatalf("Paint(true, ...) = %q", painted)
	}
	// AnsiRe must strip the 38;5;N form, or FormatTableLines' column
	// alignment counts nine invisible bytes as visible width
	if got := VisWidth(painted); got != 3 {
		t.Fatalf("VisWidth(%q) = %d, want 3", painted, got)
	}
}

// TestColor256KeepsTableAlignment is the alignment claim end to end: two
// rows whose only difference is a 256-colour code must lay out identically.
func TestColor256KeepsTableAlignment(t *testing.T) {
	plain := FormatTableLines([][]string{{"JOB", "REPO"}, {"abc", "kraken"}})
	tinted := FormatTableLines([][]string{{"JOB", "REPO"}, {Paint(true, Color256(35), "abc"), "kraken"}})
	if len(plain) != len(tinted) {
		t.Fatalf("row count differs: %d vs %d", len(plain), len(tinted))
	}
	for i := range plain {
		if VisWidth(plain[i]) != VisWidth(tinted[i]) {
			t.Errorf("row %d width %d (plain) vs %d (tinted):\n%q\n%q",
				i, VisWidth(plain[i]), VisWidth(tinted[i]), plain[i], tinted[i])
		}
	}
}
