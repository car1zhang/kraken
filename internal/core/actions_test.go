package core

import "testing"

// pinned as an invariant, not a value: the point of -x/-y is to clear tmux's
// 80x24 default-size, which starves pane splits in a never-attached session.
func TestDetachedSessionSizeClearsTmuxDefault(t *testing.T) {
	if DetachedSessionCols <= 80 || DetachedSessionRows <= 24 {
		t.Fatalf("detached session %dx%d does not exceed tmux's 80x24 default",
			DetachedSessionCols, DetachedSessionRows)
	}
}
