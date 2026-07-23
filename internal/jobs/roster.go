package jobs

import (
	"sort"
	"strings"

	"kraken/internal/core"
)

// roster.go — a cell's live crew, read off the same discovery snapshot as
// cellActive (status.go): PURE, no exec, no file reads, so it's table
// testable. Distinct from crew.go (zeus' system-prompt briefs) — this is
// about *observing* a cell's agents, not instructing them.

// A CrewAgent is one live subagent in a cell: a tmux-backed teammate pane or
// an in-process team member.
type CrewAgent struct {
	Name    string // agent name (AgentName, falling back to Name)
	Role    string // "hephaestus" | "athena" | "hermes" | "other"
	Model   string
	Status  string // refined status: "running" | "waiting" | "question" | "in-process" | anything discovery reports
	Working bool   // tmux-backed: Status == "running"; in-process: isActive explicitly true
}

// A CrewSummary is a cell's live crew: zeus' own refined status plus one
// entry per teammate.
type CrewSummary struct {
	ZeusStatus string // zeus' Status; "" when zeus is nil (still starting / gone)
	Agents     []CrewAgent
}

// crewRole derives a subagent's role from its name. Zeus names crew after the
// role, disambiguated by whatever suffix fits the slice — "hephaestus-12",
// "hephaestus-surface" — so the role must LEAD the name: exactly the role, or
// the role followed by "-". A name that merely contains a role word elsewhere
// ("probe-hermes") reads as "other" rather than guessing. This isn't just
// hypothetical: zeus' three parallel review variants — "athena-logic",
// "athena-edge", "athena-style" — are a real case of the same pattern, and
// all three bucket into the "athena" role for free.
func crewRole(name string) string {
	for _, role := range []string{"hephaestus", "athena", "hermes"} {
		if name == role || strings.HasPrefix(name, role+"-") {
			return role
		}
	}
	return "other"
}

// crewRoleRank orders CrewSummary.Agents: named roles first in briefing
// order, unrecognized names last.
func crewRoleRank(role string) int {
	switch role {
	case "hephaestus":
		return 0
	case "athena":
		return 1
	case "hermes":
		return 2
	default:
		return 3
	}
}

// CellCrew reads a cell's live crew off a discovery snapshot: sess is the
// cell's tmux session, zeus is its lead peer (nil if still starting or gone),
// peers is the full OrderAll discovery result, and members is zeus' team
// roster (core.TeamMembersForLead, nil for a solo cell with no team config
// yet). An entry qualifies as crew when it shares the cell's tmux session,
// isn't zeus itself, and is either a nested teammate pane (Role=="teammate",
// set by nestTeams) or a genuinely nested claude launched inside zeus' own
// Bash tool (Agent=="claude" && Depth>0) — the latter has no team config
// entry at all, so it's recognized structurally instead.
//
// Working mirrors cellActive's asymmetry between tmux and in-process agents:
// a tmux-backed entry is working iff its pane reports "running" (no separate
// liveness signal needed — the pane IS the agent); an in-process entry has no
// pane to check, so its team-config isActive flag is the only signal, and an
// absent flag must not default to working (same reasoning as cellActive).
// Lingering finished agents (isActive false, or an idle pane) still appear in
// Agents, just as Working: false — a deliberate product decision so an idle
// crew member stays visible rather than vanishing.
func CellCrew(sess string, zeus *core.Peer, peers []*core.Peer, members []core.TeamMember) CrewSummary {
	var summary CrewSummary
	if zeus != nil {
		summary.ZeusStatus = zeus.Status
	}

	memberByName := make(map[string]core.TeamMember, len(members))
	for _, m := range members {
		memberByName[m.Name] = m
	}

	for _, p := range peers {
		if p == nil || p.TmuxSession != sess || p == zeus {
			continue
		}
		// Deliberately NARROWER than cellActive's point 1 (any running
		// claude/codex pane in the session): a stray extra foreground claude or
		// codex pane in the cell session can vote the cell "working" without
		// appearing here, since it isn't crew zeus spawned. Keep the two
		// predicates' difference in mind before "unifying" them — the roster is
		// about zeus' crew, the roll-call is about any live work.
		if !(p.Role == "teammate" || (p.Agent == "claude" && p.Depth > 0)) {
			continue
		}

		name := p.AgentName
		if name == "" {
			name = p.Name
		}
		if name == "" {
			name = "-"
		}

		var working bool
		model := p.Model
		if p.Pane != "" {
			// "polling" counts as working, in lockstep with cellActive: a
			// poll-loop tick is live work, not idleness
			working = p.Status == "running" || p.Status == "polling"
		} else if m, ok := memberByName[name]; ok {
			working = m.IsActive != nil && *m.IsActive
			if model == "" {
				// in-process members have no command line to parse a --model
				// flag from; the team config's requested model is the only
				// source
				model = m.Model
			}
		}

		summary.Agents = append(summary.Agents, CrewAgent{
			Name:    name,
			Role:    crewRole(name),
			Model:   model,
			Status:  p.Status,
			Working: working,
		})
	}

	sort.Slice(summary.Agents, func(i, j int) bool {
		a, b := summary.Agents[i], summary.Agents[j]
		if ra, rb := crewRoleRank(a.Role), crewRoleRank(b.Role); ra != rb {
			return ra < rb
		}
		return a.Name < b.Name
	})

	return summary
}
