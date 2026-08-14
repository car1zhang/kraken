package core

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"kraken/internal/util"
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
	// Model is the model requested at spawn time (e.g. "haiku"); the only
	// model source for in-process members, which have no command line.
	Model string `json:"model"`
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

// spinnerLineRe matches the busy-indicator line the CLI paints for the whole
// duration of a turn, as of CLI 2.1.217 (pinned from a real capture):
//
//	✢ Wibbling… (1m 7s · ↓ 2.1k tokens)
//
// The glyph cycles through a small set of star-like characters and the word
// varies ("Thinking…", "Wibbling…", ...); the parenthetical always holds an
// elapsed time and optionally token counts. Requiring glyph + word + "… ("
// + digit, anchored at line start, keeps this from matching ordinary agent
// output that merely contains an ellipsis or a parenthetical.
//
// The observed 2.1.217 cycle is exactly `· ✢ ✳ ✶ ✻ ✽`; `∗ ✦ ✧ ⚹` are
// defensive extras beyond that observed set, included in case the cycle
// grows in a later CLI build without another pane capture to confirm it.
var spinnerLineRe = regexp.MustCompile(`(?m)^\s*[✢✳✶✻✽·∗✦✧⚹] \S.{0,58}… \(\d`)

func refineStatus(p *Peer) {
	if p.Pane == "" {
		return
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", p.Pane).Output()
	if err != nil {
		return
	}
	p.Status = refinedStatus(p.Status, string(out), busyOverridable(p, time.Now().UnixMilli()))
}

// staleBusyMinAgeMS is how old a session file's "busy" transition must be
// before the pane may overrule it. StatusUpdatedAt is a transition stamp,
// not a heartbeat (measured: a fresh turn reads ~10s old, wedged cells read
// hours), so a small floor is enough to keep the prompt-submit →
// first-spinner-paint race and a respawn's boot window from reading as
// staleness, while every real wedge measured is orders of magnitude older.
const staleBusyMinAgeMS = 15_000

// busyOverridable reports whether p's session-file "busy" may be overruled
// by pane content: only a claude that OWNS the pane's UI (Depth 0 — the
// agent tmux send-keys reaches, the one whose TUI the capture shows; a
// nested claude's "busy" stands, since the pane shows the inner agent's UI,
// which says nothing about the outer one), and only once the busy transition
// is at least staleBusyMinAgeMS old. A zero StatusAt (no statusUpdatedAt in
// the session file) reads as old enough — the pane is then the only signal
// there is.
func busyOverridable(p *Peer, nowMS int64) bool {
	return p.Agent == "claude" && p.Depth == 0 && nowMS-p.StatusAt >= staleBusyMinAgeMS
}

// refinedStatus is refineStatus's pure decision logic, split out for testing
// without shelling out to tmux. The CLI reports "shell" while any background
// Bash-tool shell is alive (verified against CLI 2.1.216) — that says nothing
// about the input line, so "shell" is refined from the pane content exactly
// like "unknown" rather than trusted at face value.
//
// overridableBusy is busyOverridable's verdict, and it exists because a lead
// with resident teammates never flips its session file back to "idle" when
// its turn ends (measured 2026-08-13, two cells stuck "busy" 1h and 4h30m
// while sitting at their prompt — a stale "running" that cellActive's point 1
// then propagates to the board as a cell working forever): such a peer's
// "running" is trusted only while the pane actually shows a turn in progress.
func refinedStatus(current, paneText string, overridableBusy bool) string {
	if containsQuestionMarker(paneText) {
		return "question"
	}
	// The TUI paints a spinner status line for the whole duration of a turn
	// (see spinnerLineRe); its absence means the claude is at its prompt.
	// The old pre-2.1.217 "esc to interrupt" substring fallback is
	// deliberately GONE: the CLI stopped painting the phrase (zero hits
	// across every busy pane measured on 2.1.231), while a pane merely
	// MENTIONING it — a grep's command echo, a doc about the TUI — matched
	// forever, which on the busy-override path below would have pinned a
	// stale "busy" at "running" indefinitely: the exact wedge this function
	// exists to break.
	turnInProgress := spinnerLineRe.MatchString(paneText)
	if current == "unknown" || current == "shell" {
		// no session file (teammates), or a background shell muddying the
		// reported status: the pane is the only signal.
		if turnInProgress {
			return "running"
		}
		return "waiting"
	}
	if current == "running" && overridableBusy && !turnInProgress {
		// at its prompt with a stale session file. Getting this wrong
		// running->waiting for one poll costs one spurious blocked flap (a
		// notification and a job-state write) that self-corrects next tick;
		// trusting the stale file wedges the cell reading "working" forever.
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
