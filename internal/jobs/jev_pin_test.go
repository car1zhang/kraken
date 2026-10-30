package jobs

import (
	"testing"
	"time"

	"kraken/internal/core"
)

// Regression pin: jevPass used to send the keys it worked out for the
// dialog Jev was asked about without looking at the pane again. If the screen changed during the API call (here
// the cursor moved onto "No, exit"), the bare Enter selected whatever sat
// under the cursor. The fix reads the pane again and types only into the
// same dialog with the cursor in the same place; a mismatch is skipped
// without a log line and is evaluated afresh on the next tick.
func TestJevPinRereadsPaneBeforeTyping(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("TYPESAFE_API_KEY", "")
	t.Setenv("TYPESAFE_MODEL", "")

	const asked = "Do you trust the files in this folder?\n\n❯ Yes, I trust this folder\n  No, exit"
	const moved = "Do you trust the files in this folder?\n\n  Yes, I trust this folder\n❯ No, exit"
	for _, s := range []string{asked, moved} {
		if _, ok := core.ParseDialog(s); !ok {
			t.Fatalf("fixture does not parse as a dialog: %q", s)
		}
	}

	j := &Job{ID: 1, Slug: "pin", State: "working"}
	zeus := &core.Peer{Name: ZeusName(j), Pane: "%1", PID: 4242, TmuxSession: CellSession(j), Agent: "claude"}
	all := []*core.Peer{zeus}
	live := map[string]bool{CellSession(j): true}

	var reads []string
	var sent [][]string
	calls := 0
	defer func(a, b, c, d, e any) {
		jevPaneTail = a.(func(string, int) string)
		jevSendKeys = b.(func(string, []string) error)
		jevSystemOne = c.(func(*core.TypeSafeClient, core.SystemOneRequest) (core.SystemOneResponse, error))
		jevLoadClient = d.(func() *core.TypeSafeClient)
		jevPendingQuestion = e.(func(string) bool)
	}(jevPaneTail, jevSendKeys, jevSystemOne, jevLoadClient, jevPendingQuestion)
	jevPaneTail = func(string, int) string {
		s := reads[0]
		reads = reads[1:]
		return s
	}
	jevSendKeys = func(_ string, keys []string) error {
		sent = append(sent, keys)
		return nil
	}
	jevSystemOne = func(*core.TypeSafeClient, core.SystemOneRequest) (core.SystemOneResponse, error) {
		calls++
		return core.SystemOneResponse{Model: "jev-test", Answers: map[string]core.SystemOneAnswer{
			"is_dialog": {Type: "noul", Noul: 0.95},
			"pick":      {Type: "choice", Choice: "opt1", Confidence: 0.9},
		}}, nil
	}
	jevLoadClient = func() *core.TypeSafeClient { return &core.TypeSafeClient{} }
	jevPendingQuestion = func(string) bool { return false }

	var logged []string
	logf := func(_, event, _ string) { logged = append(logged, event) }

	s := newJevState()
	now := time.Unix(1_800_000_000, 0)

	// tick 1: the dialog Jev is asked about, then a re-read showing the
	// cursor moved onto "No, exit" while the call was in flight
	reads = []string{asked, moved}
	s.jevPass(j, all, live, nil, now, logf)
	if calls != 1 {
		t.Fatalf("tick 1: want 1 API call, got %d", calls)
	}
	if len(sent) != 0 {
		t.Fatalf("tick 1: keys were sent into a changed dialog: %v", sent)
	}
	if len(logged) != 0 {
		t.Fatalf("tick 1: a skipped re-read must log nothing, got %v", logged)
	}

	// next tick: the first dialog is back and stays put, so it is asked
	// about again and typed into
	reads = []string{asked, asked}
	s.jevPass(j, all, live, nil, now.Add(time.Second), logf)
	if calls != 2 {
		t.Fatalf("tick 2: want a fresh API call, got %d calls in total", calls)
	}
	if len(sent) != 1 || len(sent[0]) != 1 || sent[0][0] != "Enter" {
		t.Fatalf("tick 2: want one Enter sent, got %v", sent)
	}
	if len(logged) != 1 || logged[0] != "jev-answer" {
		t.Fatalf("tick 2: want one jev-answer, got %v", logged)
	}
}
