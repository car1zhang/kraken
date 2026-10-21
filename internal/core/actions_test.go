package core

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// pinned as an invariant, not a value: the point of -x/-y is to clear tmux's
// 80x24 default-size, which starves pane splits in a never-attached session.
func TestDetachedSessionSizeClearsTmuxDefault(t *testing.T) {
	if DetachedSessionCols <= 80 || DetachedSessionRows <= 24 {
		t.Fatalf("detached session %dx%d does not exceed tmux's 80x24 default",
			DetachedSessionCols, DetachedSessionRows)
	}
}

// Both models kraken spawns on are pinned ids, not tier aliases: an alias would
// silently follow whatever claude ships next, which is exactly the drift the
// constants exist to prevent. They are also pinned to be DIFFERENT — the split
// is the policy (zeus on the fast tier, everything else on the deep one), and a
// ZeusModel edited back to DefaultModel's value would erase it while every
// other test here still passed.
func TestSpawnModelsArePinnedIDs(t *testing.T) {
	if DefaultModel != "claude-opus-5" {
		t.Fatalf("DefaultModel = %q; changing kraken's default model is a deliberate edit — update this test with it", DefaultModel)
	}
	if ZeusModel != "claude-fable-5-1" {
		t.Fatalf("ZeusModel = %q; changing the model a cell's zeus runs on is a deliberate edit — update this test with it", ZeusModel)
	}
	if ZeusModel == DefaultModel {
		t.Fatal("ZeusModel == DefaultModel: zeus runs on its own tier, and collapsing the two silently retires the split")
	}
}

func TestModelArgs(t *testing.T) {
	got := ModelArgs("")
	if len(got) != 2 || got[0] != "--model" || got[1] != DefaultModel {
		t.Fatalf("no model must fall back to DefaultModel: %v", got)
	}
	// an explicit model (a job's model: field) wins over the default
	if got := ModelArgs("sonnet"); len(got) != 2 || got[1] != "sonnet" {
		t.Fatalf("explicit model dropped: %v", got)
	}
}

// Every spawn path routes through ModelArgs — a claude kraken starts never
// falls back to whatever model claude's own config picks.
func TestBuildSpawnArgvPinsModel(t *testing.T) {
	argv, err := BuildSpawnArgv("claude", "foo", "", "")
	if err != nil {
		t.Fatalf("BuildSpawnArgv: %v", err)
	}
	found := false
	for i, a := range argv {
		if a == "--model" && i+1 < len(argv) && argv[i+1] == DefaultModel {
			found = true
		}
	}
	if !found {
		t.Fatalf("spawned claude missing --model %s: %v", DefaultModel, argv)
	}
}

// the hint keys on the two TCC denial texts alone: any other osascript
// failure (syntax error, timeout, missing Ghostty) must pass through
// un-decorated.
func TestFocusErrHintOnlyOnTCCDenials(t *testing.T) {
	denials := []string{
		`kraken-focus-1.applescript:127:325: execution error: System Events got an error: osascript is not allowed assistive access. (-25211)`,
		`execution error: Not authorized to send Apple events to System Events. (-1743)`,
	}
	for _, out := range denials {
		if hint := focusErrHint(out); hint == "" {
			t.Fatalf("TCC denial %q got no hint", out)
		}
	}
	for _, out := range []string{"", "execution error: syntax error", "AppleEvent timed out. (-1712)"} {
		if hint := focusErrHint(out); hint != "" {
			t.Fatalf("non-denial output %q got hint %q", out, hint)
		}
	}
}

// the fallback's argv is pinned whole, as the shape measured working on
// 2026-08-06: -d must be present (it is what makes a burst of opens converge
// on one window), and the target carries the "=" exact-match prefix so a
// stale snapshot's session name can never prefix-match a live sibling's
// window out from under the user.
func TestGhosttyAttachArgs(t *testing.T) {
	got := strings.Join(ghosttyAttachArgs("cell-demo-2"), " ")
	want := "-na Ghostty.app --args -e tmux attach-session -d -t =cell-demo-2"
	if got != want {
		t.Fatalf("ghosttyAttachArgs = %q, want %q", got, want)
	}
}

// the never-kick-a-healthy-client rule hangs on RaiseSession's errors.Is
// check matching what focusGhosttyWindow's notfound branch returns — a %w
// quietly becoming %v would resume kicking healthy clients with every other
// test still green, so the linkage is pinned here.
func TestNoGhosttyWindowErrIsMatchable(t *testing.T) {
	err := noGhosttyWindowErr("cell-demo-2", "notfound")
	if !errors.Is(err, errNoGhosttyWindow) {
		t.Fatalf("noGhosttyWindowErr does not match errNoGhosttyWindow: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"cell-demo-2:"`) || !strings.Contains(msg, "set-titles") {
		t.Fatalf("message lost the session or the set-titles remedy: %q", msg)
	}
}

// The double-open guard's three pure halves. The whole-path behaviour
// (second raise of one session queues, then reads the first's window as
// attached) needs tmux and Ghostty, so what is pinned here is each piece's
// contract: the lock is per session, the freshness window is what the
// fallback consults and it closes, and the settle wait returns the moment its
// condition holds rather than after its timeout.
func TestSessionLockIsPerSession(t *testing.T) {
	a := sessionLock("cell-demo-2")
	if sessionLock("cell-demo-2") != a {
		t.Fatalf("two lookups of one session returned different mutexes")
	}
	if sessionLock("cell-demo-2-5") == a {
		t.Fatalf("two sessions share a mutex — a raise of one would block the other")
	}
}

func TestFreshlyOpenedWindow(t *testing.T) {
	const s = "cell-fresh-test"
	now := time.Now()
	if freshlyOpened(s, now) {
		t.Fatalf("a session this process never opened reads as fresh")
	}
	raiseMu.Lock()
	raiseOpened[s] = now
	raiseMu.Unlock()
	t.Cleanup(func() {
		raiseMu.Lock()
		delete(raiseOpened, s)
		raiseMu.Unlock()
	})
	if !freshlyOpened(s, now.Add(attachFresh/2)) {
		t.Fatalf("a window opened %s ago is not fresh; the fallback would kick it", attachFresh/2)
	}
	if freshlyOpened(s, now.Add(attachFresh)) {
		t.Fatalf("a window opened %s ago still reads as fresh; a stuck window could never be replaced", attachFresh)
	}
}

func TestWaitUntilReturnsOnCondition(t *testing.T) {
	calls := 0
	start := time.Now()
	if !waitUntil(func() bool { calls++; return calls >= 3 }, time.Second, time.Millisecond) {
		t.Fatalf("condition became true within the timeout but waitUntil reported false")
	}
	if calls != 3 {
		t.Fatalf("waitUntil polled %d times, want 3 (once before any sleep, then per step)", calls)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatalf("waitUntil kept waiting after its condition held")
	}
	start = time.Now()
	if waitUntil(func() bool { return false }, 20*time.Millisecond, time.Millisecond) {
		t.Fatalf("a condition that never holds reported true")
	}
	if time.Since(start) < 20*time.Millisecond {
		t.Fatalf("waitUntil gave up before its timeout")
	}
}
