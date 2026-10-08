package jobs

import (
	"testing"
	"time"
)

// TestAutoHandoffDueStreakWindow pins the amended cap: an exhausted streak
// notifies once across a drop-and-climb inside the window, and a last fire
// older than autoHandoffFireWindow starts a new streak that fires again.
func TestAutoHandoffDueStreakWindow(t *testing.T) {
	now := time.UnixMilli(10_000_000_000)
	s := autoHandoffState{SessionID: "z", Fires: autoHandoffMaxFires, LastFireAt: now.Add(-time.Hour).UnixMilli()}
	high := CellContext{SessionID: "z", Pct: 80}
	low := CellContext{SessionID: "z", Pct: 40}

	fire, notify, s := autoHandoffDue(high, s, now)
	if fire || !notify || !s.Exhausted {
		t.Fatalf("first exhausted tick: fire=%v notify=%v state=%+v", fire, notify, s)
	}
	_, _, s = autoHandoffDue(low, s, now)
	if !s.Exhausted {
		t.Fatalf("a drop below the threshold cleared Exhausted: %+v", s)
	}
	fire, notify, s = autoHandoffDue(high, s, now)
	if fire || notify {
		t.Fatalf("bounce back above re-notified or fired: fire=%v notify=%v", fire, notify)
	}

	later := now.Add(autoHandoffFireWindow)
	fire, notify, s = autoHandoffDue(high, s, later)
	if !fire || notify || s.Fires != 1 || s.Exhausted {
		t.Fatalf("after the window: fire=%v notify=%v state=%+v", fire, notify, s)
	}
}
