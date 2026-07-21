package util

import "testing"

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
