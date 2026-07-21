package jobs

import (
	"strings"
	"testing"
)

// cell_test.go — pure decision functions on the teardown/recovery paths:
// the self-teardown guard, the interrupted-teardown marker mapping, and the
// reactivate mode matrix. All disk/tmux/git effects stay behind them.

// --- guardSelfTeardown ---

func TestGuardSelfTeardown(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		own     string
		refused bool
	}{
		{"outside tmux entirely", "cell-x", "", false},
		{"inside a different session", "cell-x", "0", false},
		{"inside another cell's session", "cell-x", "cell-y", false},
		// the status-investigation incident: a cell-resident process
		// confirming teardown of its own cell
		{"inside the target session", "cell-x", "cell-x", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := guardSelfTeardown(c.target, c.own)
			if refused := err != nil; refused != c.refused {
				t.Errorf("guardSelfTeardown(%q, %q) = %v, want refused=%v", c.target, c.own, err, c.refused)
			}
		})
	}
}

// --- teardownFinalFields ---

func TestTeardownFinalFields(t *testing.T) {
	cases := []struct {
		marker string
		want   []string
		ok     bool
	}{
		{"kill", []string{"state", "", "ready", "false", "teardown", ""}, true},
		{"done", []string{"state", "done", "teardown", ""}, true},
		{"done:", []string{"state", "done", "teardown", ""}, true},
		{"done:accepted", []string{"state", "done", "outcome", "accepted", "teardown", ""}, true},
		{"done:shipped the fix", []string{"state", "done", "outcome", "shipped the fix", "teardown", ""}, true},
		// markers this binary doesn't understand are left alone, not guessed at
		{"", nil, false},
		{"nonsense", nil, false},
	}
	for _, c := range cases {
		got, ok := teardownFinalFields(c.marker)
		if ok != c.ok {
			t.Errorf("teardownFinalFields(%q) ok = %v, want %v", c.marker, ok, c.ok)
			continue
		}
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("teardownFinalFields(%q) = %v, want %v", c.marker, got, c.want)
		}
	}
}

// --- reactivateMode ---

func TestReactivateMode(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		worktree bool
		mode     string
		wantErr  bool
	}{
		{"done, clean slate", "done", false, "recreate", false},
		// a done job was torn down; a leftover worktree needs a human look
		{"done, leftover worktree", "done", true, "", true},
		// the WIP-preserving path: cell died mid-run, worktree intact
		{"working, cell gone, worktree intact", "working", true, "resume", false},
		{"blocked, cell gone, worktree intact", "blocked", true, "resume", false},
		{"working, cell gone, worktree lost", "working", false, "recreate", false},
		{"never started", "", false, "", true},
		{"never started with stray worktree", "", true, "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mode, err := reactivateMode(c.state, c.worktree)
			if (err != nil) != c.wantErr {
				t.Fatalf("reactivateMode(%q, %v) err = %v, wantErr=%v", c.state, c.worktree, err, c.wantErr)
			}
			if mode != c.mode {
				t.Errorf("reactivateMode(%q, %v) = %q, want %q", c.state, c.worktree, mode, c.mode)
			}
		})
	}
}
