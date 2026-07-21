package jobs

import (
	"testing"

	"kraken/core"
)

func TestCellCrewTmuxTeammateStatuses(t *testing.T) {
	sess := "cell-x"
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "running", Pane: "%1"}

	cases := []struct {
		name    string
		status  string
		working bool
	}{
		{"running -> working", "running", true},
		{"waiting -> not working", "waiting", false},
		{"question -> not working", "question", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			mate := &core.Peer{TmuxSession: sess, Pane: "%2", Role: "teammate", AgentName: "hephaestus", Status: c.status}
			peers := []*core.Peer{zeus, mate}
			got := CellCrew(sess, zeus, peers, nil)
			if len(got.Agents) != 1 {
				t.Fatalf("want 1 agent, got %d (%+v)", len(got.Agents), got.Agents)
			}
			if got.Agents[0].Working != c.working {
				t.Errorf("Working = %v, want %v", got.Agents[0].Working, c.working)
			}
			if got.Agents[0].Status != c.status {
				t.Errorf("Status = %q, want %q", got.Agents[0].Status, c.status)
			}
		})
	}
}

func TestCellCrewInProcessMember(t *testing.T) {
	sess := "cell-x"
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "running", Pane: "%1"}
	inProcess := &core.Peer{TmuxSession: sess, Status: "in-process", Role: "teammate", AgentName: "athena"}

	cases := []struct {
		name    string
		members []core.TeamMember
		working bool
	}{
		{
			name:    "isActive true -> working",
			members: []core.TeamMember{{Name: "athena", BackendType: "in-process", IsActive: boolPtr(true)}},
			working: true,
		},
		{
			name:    "isActive false -> not working",
			members: []core.TeamMember{{Name: "athena", BackendType: "in-process", IsActive: boolPtr(false)}},
			working: false,
		},
		{
			name:    "isActive absent (nil) -> not working",
			members: []core.TeamMember{{Name: "athena", BackendType: "in-process"}},
			working: false,
		},
		{
			name:    "member missing from members list -> not working",
			members: nil,
			working: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			peers := []*core.Peer{zeus, inProcess}
			got := CellCrew(sess, zeus, peers, c.members)
			if len(got.Agents) != 1 {
				t.Fatalf("want 1 agent, got %d (%+v)", len(got.Agents), got.Agents)
			}
			if got.Agents[0].Working != c.working {
				t.Errorf("Working = %v, want %v", got.Agents[0].Working, c.working)
			}
		})
	}
}

func TestCellCrewLingeringTmuxTeammateIncludedAsIdle(t *testing.T) {
	sess := "cell-x"
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "running", Pane: "%1"}
	// still in peers (pane alive), but sitting idle, not running: a lingering
	// finished agent — must appear, just as Working: false, not be filtered.
	mate := &core.Peer{TmuxSession: sess, Pane: "%2", Role: "teammate", AgentName: "hephaestus", Status: "waiting"}
	peers := []*core.Peer{zeus, mate}

	got := CellCrew(sess, zeus, peers, nil)
	if len(got.Agents) != 1 {
		t.Fatalf("want lingering teammate still listed, got %d agents", len(got.Agents))
	}
	if got.Agents[0].Working {
		t.Error("lingering idle teammate should read Working: false, not be excluded")
	}
}

func TestCrewRoleParsing(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"hephaestus-12", "hephaestus"},
		{"hephaestus-surface", "hephaestus"},
		{"athena", "athena"},
		{"hermes-2", "hermes"},
		{"probe-hermes", "other"},
		{"", "other"},
		{"hephaestus-", "hephaestus"},
		{"hephaestus-1-2", "hephaestus"},
		{"hermesx", "other"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := crewRole(c.name); got != c.want {
				t.Errorf("crewRole(%q) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}

func TestCellCrewZeusStatusAndExclusion(t *testing.T) {
	sess := "cell-x"

	t.Run("zeus nil -> ZeusStatus empty", func(t *testing.T) {
		got := CellCrew(sess, nil, nil, nil)
		if got.ZeusStatus != "" {
			t.Errorf("ZeusStatus = %q, want empty", got.ZeusStatus)
		}
		if len(got.Agents) != 0 {
			t.Errorf("want no agents, got %+v", got.Agents)
		}
	})

	t.Run("zeus present -> its status, and zeus never appears in Agents", func(t *testing.T) {
		zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "waiting", Pane: "%1"}
		mate := &core.Peer{TmuxSession: sess, Pane: "%2", Role: "teammate", AgentName: "hephaestus", Status: "running"}
		peers := []*core.Peer{zeus, mate}

		got := CellCrew(sess, zeus, peers, nil)
		if got.ZeusStatus != "waiting" {
			t.Errorf("ZeusStatus = %q, want %q", got.ZeusStatus, "waiting")
		}
		if len(got.Agents) != 1 || got.Agents[0].Name != "hephaestus" {
			t.Fatalf("zeus must not appear in Agents, got %+v", got.Agents)
		}
	})
}

func TestCellCrewForeignSessionExcludedNestedClaudeIncluded(t *testing.T) {
	sess := "cell-x"
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "running", Pane: "%1"}
	foreign := &core.Peer{TmuxSession: "cell-y", Pane: "%9", Role: "teammate", AgentName: "hephaestus", Status: "running"}
	nested := &core.Peer{TmuxSession: sess, Agent: "claude", Depth: 1, Pane: "%1", Name: "sub-claude", Status: "running"}
	peers := []*core.Peer{zeus, foreign, nested}

	got := CellCrew(sess, zeus, peers, nil)
	if len(got.Agents) != 1 {
		t.Fatalf("want only the in-session nested claude, got %+v", got.Agents)
	}
	if got.Agents[0].Name != "sub-claude" {
		t.Errorf("Name = %q, want %q", got.Agents[0].Name, "sub-claude")
	}
	if got.Agents[0].Role != "other" {
		t.Errorf("Role = %q, want %q", got.Agents[0].Role, "other")
	}
	if !got.Agents[0].Working {
		t.Error("nested running claude should read Working: true")
	}
}

func TestCellCrewNonCrewPeersExcluded(t *testing.T) {
	sess := "cell-x"
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "running", Pane: "%1"}
	peers := []*core.Peer{
		zeus,
		// a codex agent someone opened in the cell session: not crew
		{TmuxSession: sess, Pane: "%2", Agent: "codex", Role: "main", Status: "running", Name: "codex-thread"},
		// an agent-free shell pane in the session: not crew
		{TmuxSession: sess, Pane: "%3", Role: "shell", Status: "running", Cmd: "make build"},
	}

	got := CellCrew(sess, zeus, peers, nil)
	if len(got.Agents) != 0 {
		t.Fatalf("codex/shell peers must not count as crew, got %+v", got.Agents)
	}
}

func TestCellCrewModelPropagation(t *testing.T) {
	sess := "cell-x"
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "running", Pane: "%1"}

	t.Run("tmux teammate carries its flag model", func(t *testing.T) {
		mate := &core.Peer{TmuxSession: sess, Pane: "%2", Role: "teammate", AgentName: "hephaestus", Status: "running", Model: "sonnet"}
		got := CellCrew(sess, zeus, []*core.Peer{zeus, mate}, nil)
		if len(got.Agents) != 1 || got.Agents[0].Model != "sonnet" {
			t.Fatalf("Model = %+v, want one agent with model sonnet", got.Agents)
		}
	})

	t.Run("in-process teammate falls back to the config model", func(t *testing.T) {
		inProcess := &core.Peer{TmuxSession: sess, Status: "in-process", Role: "teammate", AgentName: "athena"}
		members := []core.TeamMember{{Name: "athena", BackendType: "in-process", Model: "opus", IsActive: boolPtr(true)}}
		got := CellCrew(sess, zeus, []*core.Peer{zeus, inProcess}, members)
		if len(got.Agents) != 1 || got.Agents[0].Model != "opus" {
			t.Fatalf("Model = %+v, want one agent with model opus", got.Agents)
		}
	})
}

func TestCellCrewOrdering(t *testing.T) {
	sess := "cell-x"
	zeus := &core.Peer{TmuxSession: sess, Agent: "claude", Status: "running", Pane: "%1"}
	peers := []*core.Peer{
		zeus,
		{TmuxSession: sess, Pane: "%2", Role: "teammate", AgentName: "zzz-other"},
		{TmuxSession: sess, Pane: "%3", Role: "teammate", AgentName: "athena"},
		{TmuxSession: sess, Pane: "%4", Role: "teammate", AgentName: "hephaestus-2"},
		{TmuxSession: sess, Pane: "%5", Role: "teammate", AgentName: "hephaestus-1"},
		{TmuxSession: sess, Pane: "%6", Role: "teammate", AgentName: "hermes-1"},
		{TmuxSession: sess, Pane: "%7", Role: "teammate", AgentName: "aaa-other"},
	}

	got := CellCrew(sess, zeus, peers, nil)
	wantOrder := []string{"hephaestus-1", "hephaestus-2", "athena", "hermes-1", "aaa-other", "zzz-other"}
	if len(got.Agents) != len(wantOrder) {
		t.Fatalf("want %d agents, got %d (%+v)", len(wantOrder), len(got.Agents), got.Agents)
	}
	for i, name := range wantOrder {
		if got.Agents[i].Name != name {
			t.Errorf("position %d: got %q, want %q (full order: %+v)", i, got.Agents[i].Name, name, got.Agents)
		}
	}
}
