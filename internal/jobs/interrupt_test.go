package jobs

import (
	"strings"
	"testing"

	"kraken/internal/core"
)

// interruptTarget is the whole of InterruptZeus that can run without a tmux
// server: which pane the Escape goes to. The cases are the ones that tell it
// apart from InterruptCell — one pane, zeus', never a teammate's and never a
// side thread's.

func TestInterruptTargetPicksZeusAlone(t *testing.T) {
	j := &Job{Slug: "interrupt-zeus"}
	sess := CellSession(j)

	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%3", Name: ZeusName(j)}
	zeus.Teammates = []*core.Peer{{Pane: "%5", Agent: "claude", Name: "hephaestus-slice-a"}}
	all := []*core.Peer{
		// a lower pane id than zeus': the positional fallback would pick it,
		// the name match must not
		{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%2", Name: "hephaestus-slice-b"},
		zeus,
		{TmuxSession: "other", Agent: "claude", Depth: 0, Pane: "%1"},
	}

	got, err := interruptTarget(j, all)
	if err != nil {
		t.Fatalf("interruptTarget: %v", err)
	}
	if got != zeus {
		t.Fatalf("interruptTarget picked pane %s (%s), want zeus' %s", got.Pane, got.Name, zeus.Pane)
	}
}

func TestInterruptTargetSkipsSideThread(t *testing.T) {
	j := &Job{Slug: "interrupt-thread"}
	sess := CellSession(j)

	// zeus has not written its session file yet (no Name), so CellZeus falls
	// back to position — and the thread, in a lower pane, must still lose
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%4"}
	all := []*core.Peer{
		{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%2", Name: ThreadAgentName(j, "t1")},
		zeus,
	}

	got, err := interruptTarget(j, all)
	if err != nil {
		t.Fatalf("interruptTarget: %v", err)
	}
	if got != zeus {
		t.Fatalf("interruptTarget picked pane %s (%s), want zeus' %s", got.Pane, got.Name, zeus.Pane)
	}
}

// A codex zeus carries its thread title as its Name — never zeus-<slug> —
// so the crew-pane refusal must not apply to it.
func TestInterruptTargetAllowsCodexZeusByTitle(t *testing.T) {
	j := &Job{Slug: "interrupt-codex", Agent: "codex"}
	sess := CellSession(j)

	zeus := &core.Peer{TmuxSession: sess, Agent: "codex", Depth: 0, Pane: "%3", Name: "You are zeus, the sole agent for kraken job"}
	got, err := interruptTarget(j, []*core.Peer{zeus})
	if err != nil {
		t.Fatalf("interruptTarget refused a codex zeus: %v", err)
	}
	if got != zeus {
		t.Fatalf("interruptTarget picked pane %s, want zeus' %s", got.Pane, zeus.Pane)
	}
}

func TestInterruptTargetRefusesWithoutZeus(t *testing.T) {
	j := &Job{Slug: "interrupt-none"}
	sess := CellSession(j)

	cases := map[string][]*core.Peer{
		"empty discovery": nil,
		"only a side thread": {
			{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%2", Name: ThreadAgentName(j, "t1")},
		},
		"only another cell": {
			{TmuxSession: "other", Agent: "claude", Depth: 0, Pane: "%1", Name: ZeusName(j)},
		},
		"zeus with no pane": {
			{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "", Name: ZeusName(j)},
		},
		// zeus' process has exited but its crew runs on: the positional
		// fallback would elect the teammate, and the Escape must not land there
		"only a named crew pane": {
			{TmuxSession: sess, Agent: "claude", Depth: 0, Pane: "%2", Name: "hephaestus-slice-a"},
		},
	}
	for name, all := range cases {
		got, err := interruptTarget(j, all)
		if err == nil {
			t.Errorf("%s: expected an error, got pane %q", name, got.Pane)
			continue
		}
		if !strings.Contains(err.Error(), j.Slug) {
			t.Errorf("%s: error should name the job: %v", name, err)
		}
	}
}
