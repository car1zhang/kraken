package core

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"kraken/util"
)

// A Peer is one live claude process found in a tmux pane, an in-process
// teammate known only from its team config (Messageable=false, no pane/pid),
// or a claude-free shell pane (Role "shell" — only selection, k o, k k apply).
type Peer struct {
	Index       int    `json:"index"`
	Pane        string `json:"pane,omitempty"`
	TmuxSession string `json:"tmux_session,omitempty"`
	WindowIdx   int    `json:"window"`
	// Depth is the peer's position in its pane's deepest-first claude list:
	// 0 = the foreground claude attached to the tty (the one send-keys reaches).
	Depth     int    `json:"-"`
	PID       int    `json:"pid,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Name      string `json:"name,omitempty"`
	CWD       string `json:"cwd,omitempty"`
	Status    string `json:"status"`
	StatusAt  int64  `json:"status_at,omitempty"`
	// Wakeup is set only when Status is "polling": the pending ScheduleWakeup
	// call (from a /loop or /poll-pr-style tick) that upgraded this peer out
	// of a false "waiting" read. See discover.go's DiscoverAll and
	// FindPendingWakeup.
	Wakeup *Wakeup `json:"wakeup,omitempty"`
	// SessionIDGuessed marks ids inferred from cwd+newest-transcript (no
	// session file): fine for display, unsafe for fork/read targeting.
	SessionIDGuessed bool   `json:"session_id_guessed,omitempty"`
	Branch           string `json:"branch,omitempty"`
	Model            string `json:"model,omitempty"`
	Kind             string `json:"kind,omitempty"`
	// Agent identifies the interactive coding agent in this pane. It is
	// "claude" or "codex" for agent rows and empty for shell rows.
	Agent string `json:"agent,omitempty"`
	// Role "shell" marks a pane with no claude in it; Cmd/CmdSeconds describe
	// what it is running ("" = idle at the prompt).
	Role       string `json:"role"`
	Cmd        string `json:"cmd,omitempty"`
	CmdSeconds int64  `json:"cmd_seconds,omitempty"`
	// Self marks the pane this kraken invocation runs in.
	Self        bool    `json:"self,omitempty"`
	Team        string  `json:"team,omitempty"`
	AgentName   string  `json:"agent_name,omitempty"`
	ParentSID   string  `json:"parent_session_id,omitempty"`
	Messageable bool    `json:"messageable"`
	Teammates   []*Peer `json:"-"`
}

// DisplayName is a peer's best human-readable identity: its agent name (a
// teammate's --agent-name), else its session name, else "-".
func DisplayName(p *Peer) string {
	if p.AgentName != "" {
		return p.AgentName
	}
	if p.Name != "" {
		return p.Name
	}
	return "-"
}

// ProjectLabel is a peer's project directory basename, "~" for a root/empty cwd.
func ProjectLabel(p *Peer) string {
	base := filepath.Base(p.CWD)
	if base == "" || base == "/" || base == "." {
		return "~"
	}
	return base
}

// sessionFile mirrors ~/.claude/sessions/<pid>.json, maintained by Claude Code
// itself (created at startup, status live-updated, removed on exit).
type sessionFile struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	CWD             string `json:"cwd"`
	Name            string `json:"name"`
	Status          string `json:"status"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	Kind            string `json:"kind"`
}

type teamConfig struct {
	Name          string       `json:"name"`
	LeadSessionID string       `json:"leadSessionId"`
	Members       []TeamMember `json:"members"`
}

type TeamMember struct {
	AgentID     string `json:"agentId"`
	Name        string `json:"name"`
	AgentType   string `json:"agentType"`
	TmuxPaneID  string `json:"tmuxPaneId"`
	CWD         string `json:"cwd"`
	BackendType string `json:"backendType"`
	// IsActive appears on members that finished without being removed from
	// the config (TaskStop removes a member entirely; a done-but-lingering
	// one gets isActive: false instead). Pointer: absent must read as active,
	// not as false.
	IsActive *bool `json:"isActive"`
}

// --- status ---

// Permission/question dialogs always render a selected-option line like
// "❯ 1. Yes, I trust this folder" (pinned against CLI 2.1.205). Matching that
// shape near the pane bottom avoids false positives from prose like "Do you
// want ..." appearing in ordinary output.
var questionOptionRe = regexp.MustCompile(`^\s*❯ \d+\. `)

func refineStatus(p *Peer) {
	if p.Pane == "" {
		return
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", p.Pane).Output()
	if err != nil {
		return
	}
	p.Status = refinedStatus(p.Status, string(out))
}

// refinedStatus is refineStatus's pure decision logic, split out for testing
// without shelling out to tmux. The CLI reports "shell" while any background
// Bash-tool shell is alive (verified against CLI 2.1.216) — that says nothing
// about the input line, so "shell" is refined from the pane content exactly
// like "unknown" rather than trusted at face value.
func refinedStatus(current, paneText string) string {
	if containsQuestionMarker(paneText) {
		return "question"
	}
	if current == "unknown" || current == "shell" {
		// no session file (teammates), or a background shell muddying the
		// reported status: the TUI shows "esc to interrupt" for the whole
		// turn, so its absence means the claude is at its prompt
		if strings.Contains(paneText, "esc to interrupt") {
			return "running"
		}
		return "waiting"
	}
	return current
}

func containsQuestionMarker(text string) bool {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	start := len(lines) - 20
	if start < 0 {
		start = 0
	}
	for _, l := range lines[start:] {
		if questionOptionRe.MatchString(l) {
			return true
		}
	}
	return false
}

func PaneShowsQuestion(pane string) bool {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	return err == nil && containsQuestionMarker(string(out))
}

// TeamMembersForLead loads the members of the team config whose
// leadSessionId matches sessionID — the cell's zeus session, for the
// ROLL-CALL blocked probe in status.go. Defensive like nestTeams: a missing
// teams dir, an unreadable/malformed config, or no team with that lead all
// read as no members, never an error. Shares nestTeams' teamConfig/TeamMember
// JSON shape rather than re-declaring it.
func TeamMembersForLead(sessionID string) []TeamMember {
	if sessionID == "" {
		return nil
	}
	configs, _ := filepath.Glob(filepath.Join(util.Home(), ".claude", "teams", "*", "config.json"))
	for _, cf := range configs {
		data, err := os.ReadFile(cf)
		if err != nil {
			continue
		}
		var cfg teamConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		if cfg.LeadSessionID == sessionID {
			return cfg.Members
		}
	}
	return nil
}

// --- team nesting ---

func nestTeams(peers []*Peer) {
	configs, _ := filepath.Glob(filepath.Join(util.Home(), ".claude", "teams", "*", "config.json"))
	bySession := map[string]*Peer{}
	byPane := map[string]*Peer{}
	for _, p := range peers {
		if p.Agent == "codex" {
			continue
		}
		if p.SessionID != "" && !p.SessionIDGuessed {
			bySession[p.SessionID] = p
		}
		// peers are appended deepest-first per pane: keep the foreground one
		if p.Pane != "" {
			if _, ok := byPane[p.Pane]; !ok {
				byPane[p.Pane] = p
			}
		}
	}
	claimed := map[*Peer]bool{}
	for _, cf := range configs {
		data, err := os.ReadFile(cf)
		if err != nil {
			continue
		}
		var cfg teamConfig
		if err := json.Unmarshal(data, &cfg); err != nil {
			continue
		}
		lead := bySession[cfg.LeadSessionID]
		if lead == nil {
			continue // stale config: team is live only while its lead is
		}
		lead.Team = cfg.Name
		for _, m := range cfg.Members {
			if m.AgentType == "team-lead" {
				continue
			}
			if mate := byPane[m.TmuxPaneID]; mate != nil {
				if mate == lead || claimed[mate] {
					continue // lead's own pane, or already nested elsewhere
				}
				mate.Role = "teammate"
				mate.Team = cfg.Name
				mate.AgentName = m.Name
				lead.Teammates = append(lead.Teammates, mate)
				claimed[mate] = true
				continue
			}
			if m.BackendType == "in-process" {
				// no pane/process of its own — listed under the lead but
				// not messageable
				lead.Teammates = append(lead.Teammates, &Peer{
					TmuxSession: lead.TmuxSession,
					Status:      "in-process",
					Role:        "teammate",
					Team:        cfg.Name,
					AgentName:   m.Name,
					CWD:         m.CWD,
					Branch:      gitBranch(m.CWD),
					Messageable: false,
				})
			}
			// tmux-backed member whose pane is gone: the teammate is dead — omit
		}
	}
	// teammates whose config entry didn't pane-match still nest under their
	// lead via the --parent-session-id from their command line
	for _, p := range peers {
		if claimed[p] || p.ParentSID == "" {
			continue
		}
		if lead := bySession[p.ParentSID]; lead != nil && lead != p {
			lead.Teammates = append(lead.Teammates, p)
			claimed[p] = true
		}
	}
}

// OrderPeers flattens peers into display order (teammates right after their
// lead) and assigns 1-based indexes. Only this order is a valid selector target.
func OrderPeers(peers []*Peer) []*Peer {
	nested := map[*Peer]bool{}
	for _, p := range peers {
		for _, t := range p.Teammates {
			nested[t] = true
		}
	}
	var ordered []*Peer
	seen := map[*Peer]bool{}
	var emit func(p *Peer)
	emit = func(p *Peer) {
		if seen[p] {
			return // a teammate claimed by two teams appears once
		}
		seen[p] = true
		ordered = append(ordered, p)
		for _, t := range p.Teammates {
			emit(t) // a teammate that is itself a lead brings its own team
		}
	}
	for _, p := range peers {
		if !nested[p] {
			emit(p)
		}
	}
	// nesting cycles leave peers unreachable from any top-level lead; make
	// sure every real peer still gets listed
	for _, p := range peers {
		emit(p)
	}
	for i, p := range ordered {
		p.Index = i + 1
	}
	return ordered
}

// OrderAll orders claudes and appends shell panes with continued numbering —
// one selector space across both tables. Callers rendering the tables
// separately split the result on nPeers.
func OrderAll(peers, shells []*Peer) (all []*Peer, nPeers int) {
	ordered := OrderPeers(peers)
	// Type grouping renders agents in separate tables. Keep the underlying
	// selector order in that same Claude-then-Codex order so row numbers read
	// monotonically from top to bottom.
	byAgent := make([]*Peer, 0, len(ordered))
	for _, agent := range []string{"claude", "codex"} {
		for _, p := range ordered {
			if p.Agent == agent || (agent == "claude" && p.Agent == "") {
				byAgent = append(byAgent, p)
			}
		}
	}
	ordered = byAgent
	for i, p := range ordered {
		p.Index = i + 1
	}
	for i, s := range shells {
		s.Index = len(ordered) + i + 1
	}
	return append(ordered, shells...), len(ordered)
}
