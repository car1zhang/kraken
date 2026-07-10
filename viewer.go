package main

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// k v — persistent live viewer over the same discovery the one-shot commands
// use. The display is the same grouped tables as k ls (t cycles type/session/
// none), stacked in one scroll region with one cursor across every row. Enter
// (or clicking the selected row) opens a claude or shell pane — clicking any
// other row just selects it — b forks a claude into a new window in its own
// tmux session and jumps in, and dd (vim-style) kills the selected pane (claude or shell).
// In type grouping, blocked groups (question/shell) pin to the top.
//
// paint() is called with color always on here: the alt screen is a tty by
// construction, so useColor()'s stdout sniff doesn't apply.

const viewerTick = time.Second

func cmdViewer(args []string) {
	mode := "type"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-g", "--group":
			i++
			if i >= len(args) {
				die("-g needs a value: type, session, or none")
			}
			mode = parseGroupMode(args[i])
		default:
			die("unknown argument: %s (usage: k v [-g type|session|none])", args[i])
		}
	}
	p := tea.NewProgram(&viewerModel{mode: mode}, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		die("viewer: %v", err)
	}
}

// pinBlocked stable-partitions top-level peers so groups containing a claude
// blocked on a question/permission prompt (or sitting at the ! shell prompt)
// list first. Stable, so discovery's session/window grouping holds within each
// partition. Applied in type grouping only — session/none grouping is explicit
// and pinning would tear it apart.
func pinBlocked(peers []*Peer) []*Peer {
	nested := map[*Peer]bool{}
	for _, p := range peers {
		for _, t := range p.Teammates {
			nested[t] = true
		}
	}
	blocked := func(p *Peer) bool {
		return p.Status == "question" || p.Status == "shell"
	}
	var groupBlocked func(p *Peer, seen map[*Peer]bool) bool
	groupBlocked = func(p *Peer, seen map[*Peer]bool) bool {
		if seen[p] {
			return false // nesting cycle guard
		}
		seen[p] = true
		if blocked(p) {
			return true
		}
		for _, t := range p.Teammates {
			if groupBlocked(t, seen) {
				return true
			}
		}
		return false
	}
	var hot, cold, rest []*Peer
	for _, p := range peers {
		switch {
		case nested[p]:
			// emitted via its lead; position only matters for the
			// cycle-fallback pass, so keep original order at the end
			rest = append(rest, p)
		case groupBlocked(p, map[*Peer]bool{}):
			hot = append(hot, p)
		default:
			cold = append(cold, p)
		}
	}
	return append(append(hot, cold...), rest...)
}

type tickMsg time.Time

type snapshotMsg struct {
	peers  []*Peer
	shells []*Peer
	usage  string
}

type actionMsg struct {
	note string
	err  error
}

type viewerModel struct {
	rawPeers   []*Peer // last snapshot, unpinned/unnumbered
	rawShells  []*Peer
	mode       string // "type" | "session" | "none"
	groups     []displayGroup
	rows       []*Peer // selectable rows in display order (flattened groups)
	usage      string
	cursor     int // index into rows
	scroll     int // first visible line of buildLines' output
	width      int
	height     int
	note       string
	noteIsErr  bool
	loaded     bool
	refreshing bool
	// pendingKill holds the peerKey of the pane a first 'd' armed; the second
	// 'd' kills only if the selection still matches, so list churn between the
	// two keystrokes can't redirect the kill.
	pendingKill string
}

func (m *viewerModel) Init() tea.Cmd {
	m.refreshing = true
	return tea.Batch(refreshCmd(), tickCmd())
}

func tickCmd() tea.Cmd {
	return tea.Tick(viewerTick, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// refreshCmd runs discovery off the UI goroutine; it only reads, all model
// state stays owned by Update.
func refreshCmd() tea.Cmd {
	return func() tea.Msg {
		peers, shells := discoverAll()
		return snapshotMsg{peers: peers, shells: shells, usage: usageHeader(true)}
	}
}

func (m *viewerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.clampScroll()
	case tickMsg:
		// a tick that lands while a refresh is in flight is skipped, never
		// queued — a slow discovery can't pile up behind itself
		if m.refreshing {
			return m, tickCmd()
		}
		m.refreshing = true
		return m, tea.Batch(refreshCmd(), tickCmd())
	case snapshotMsg:
		m.refreshing = false
		m.loaded = true
		m.usage = msg.usage
		m.rawPeers, m.rawShells = msg.peers, msg.shells
		m.rebuild()
	case actionMsg:
		if msg.err != nil {
			m.note, m.noteIsErr = msg.err.Error(), true
		} else {
			m.note, m.noteIsErr = msg.note, false
		}
	case tea.KeyMsg:
		return m.handleKey(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m *viewerModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key != "d" {
		m.pendingKill = "" // any other key disarms a pending dd
	}
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "j", "down":
		m.setCursor(m.cursor + 1)
	case "k", "up":
		m.setCursor(m.cursor - 1)
	case "g", "home":
		m.setCursor(0)
	case "G", "end":
		m.setCursor(len(m.rows) - 1)
	case "t":
		switch m.mode {
		case "type":
			m.mode = "session"
		case "session":
			m.mode = "none"
		default:
			m.mode = "type"
		}
		m.rebuild()
		m.note, m.noteIsErr = "grouping: "+m.mode, false
	case "enter":
		return m, m.actOnSelected("opening", openPeer)
	case "b":
		return m, m.actOnSelected("forking", branchPeerWindow)
	case "d":
		p := m.selected()
		if p == nil {
			m.pendingKill = ""
			break
		}
		if p.Self {
			// killPeer would refuse anyway; don't arm a kill that can't happen
			m.pendingKill = ""
			m.note, m.noteIsErr = "that's this viewer's own pane", true
			break
		}
		if m.pendingKill == peerKey(p) {
			m.pendingKill = ""
			return m, m.actOnSelected("killing", killPeer)
		}
		m.pendingKill = peerKey(p)
		kind := "shell"
		if p.Role != "shell" {
			kind = p.Agent
			if kind == "" {
				kind = "claude"
			}
		}
		m.note, m.noteIsErr = "d again to kill "+kind+" "+displayName(p)+" ("+p.Pane+")", false
	case "r":
		if !m.refreshing {
			m.refreshing = true
			return m, refreshCmd()
		}
	}
	return m, nil
}

func (m *viewerModel) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.pendingKill = "" // like keys, any mouse action disarms a pending dd
		m.setCursor(m.cursor - 1)
	case tea.MouseButtonWheelDown:
		m.pendingKill = ""
		m.setCursor(m.cursor + 1)
	case tea.MouseButtonLeft:
		if msg.Action != tea.MouseActionPress {
			break
		}
		m.pendingKill = ""
		if i, ok := m.rowAt(msg.Y); ok {
			if i == m.cursor {
				return m, m.actOnSelected("opening", openPeer)
			}
			m.setCursor(i)
		}
	}
	return m, nil
}

// actOnSelected runs an action (openPeer/branchPeerWindow/killPeer) against
// the selected peer in a Cmd goroutine so slow AppleScript never freezes the
// UI. Safe to hand the *Peer across goroutines: discovery allocates fresh
// Peers every snapshot and the actions only read them.
func (m *viewerModel) actOnSelected(verb string, action func(*Peer) (string, error)) tea.Cmd {
	p := m.selected()
	if p == nil {
		return nil
	}
	m.note, m.noteIsErr = verb+" "+displayName(p)+"…", false
	return func() tea.Msg {
		note, err := action(p)
		return actionMsg{note, err}
	}
}

func (m *viewerModel) selected() *Peer {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return nil
	}
	return m.rows[m.cursor]
}

// peerKey identifies a peer across snapshots so the cursor follows it through
// list churn (pinning, regrouping, peers appearing/exiting).
func peerKey(p *Peer) string {
	switch {
	case p.Pane != "":
		return "pane:" + p.Pane + ":" + strconv.Itoa(p.PID)
	case p.PID != 0:
		return "pid:" + strconv.Itoa(p.PID)
	default:
		return "agent:" + p.TmuxSession + ":" + p.Team + "/" + p.AgentName
	}
}

// rebuild derives the display (groups, selectable rows) from the raw snapshot
// for the current mode, keeping the cursor on the same peer. When the anchored
// row is gone, the stale index just clamps (positional fallback).
func (m *viewerModel) rebuild() {
	anchor := ""
	if p := m.selected(); p != nil {
		anchor = peerKey(p)
	}
	peers := m.rawPeers
	if m.mode == "type" {
		peers = pinBlocked(peers)
	}
	all, n := orderAll(peers, m.rawShells)
	m.groups = displayGroups(m.mode, all, n)
	m.rows = m.rows[:0]
	for _, g := range m.groups {
		m.rows = append(m.rows, g.rows...)
	}
	if m.mode == "type" {
		// pinning reorders rows but must not renumber them: a # read off the
		// viewer has to select the same target in k s/k o/k k, which never pin
		orderAll(m.rawPeers, m.rawShells)
	}
	if anchor != "" {
		for i, p := range m.rows {
			if peerKey(p) == anchor {
				m.setCursor(i)
				return
			}
		}
	}
	m.setCursor(m.cursor)
}

func (m *viewerModel) setCursor(i int) {
	if len(m.rows) == 0 {
		m.cursor, m.scroll = 0, 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i > len(m.rows)-1 {
		i = len(m.rows) - 1
	}
	m.cursor = i
	m.clampScroll()
}

// --- layout ---
// content window   (contentHeight lines of buildLines' output, from scroll)
// padding
// hints            (1 line)
// note             (1 line)

func (m *viewerModel) usageLines() int {
	return 0 // usage is rendered inside its agent table group
}

func (m *viewerModel) contentHeight() int {
	ch := m.height - m.usageLines() - 2
	if ch < 1 {
		ch = 1
	}
	return ch
}

// A viewLine is one rendered content line; row indexes m.rows for selectable
// lines and is -1 for chrome (titles, headers, blanks).
type viewLine struct {
	text string
	row  int
}

// buildLines renders every content line for the current groups, tagged with
// the row it shows. Pure function of model state — View and rowAt both use it,
// which is what keeps mouse hit-testing agreeing with what is drawn.
func (m *viewerModel) buildLines() []viewLine {
	switch {
	case !m.loaded:
		return []viewLine{{text: paint(true, dim, "discovering…"), row: -1}}
	case len(m.rows) == 0:
		return []viewLine{{text: "no live sessions", row: -1}}
	}
	now := time.Now()
	var lines []viewLine
	idx := 0
	for gi, g := range m.groups {
		if gi > 0 {
			lines = append(lines, viewLine{row: -1})
		}
		if g.title != "" {
			lines = append(lines, viewLine{text: paint(true, g.titleColor(), strings.ToUpper(g.title)), row: -1})
		}
		for _, usage := range usageForAgent(m.usage, g.title, true) {
			lines = append(lines, viewLine{text: usage, row: -1})
		}
		header := groupHeader(g.kind)
		header[0] = " " + header[0] // marker column
		for i, h := range header {
			header[i] = paint(true, cyan, strings.ToUpper(h))
		}
		table := [][]string{header}
		parent := parentMap(g.rows)
		first := idx
		for _, p := range g.rows {
			// the selected row is rendered without colors so the reverse-video
			// bar isn't broken by embedded resets
			color := idx != m.cursor
			marker := " "
			if idx == m.cursor {
				marker = "▸"
			}
			cells := groupCells(g.kind, p, parent[p], color, now)
			cells[0] = marker + cells[0]
			table = append(table, cells)
			idx++
		}
		aligned := formatTableLines(table)
		lines = append(lines, viewLine{text: aligned[0], row: -1})
		for i, l := range aligned[1:] {
			if st, ok := g.subtitles[i]; ok {
				lines = append(lines, viewLine{text: paint(true, dim, " "+st), row: -1})
			}
			lines = append(lines, viewLine{text: l, row: first + i})
		}
	}
	return lines
}

// clampScroll keeps the cursor's line visible. Scrolling up also reveals the
// chrome (title/header) directly above the cursor row so groups keep their
// labels in view.
func (m *viewerModel) clampScroll() {
	lines := m.buildLines()
	li := -1
	for i, l := range lines {
		if l.row == m.cursor {
			li = i
			break
		}
	}
	ch := m.contentHeight()
	if li >= 0 {
		if li < m.scroll {
			s := li
			for s > 0 && lines[s-1].row == -1 {
				s--
			}
			m.scroll = s
		}
		if li >= m.scroll+ch {
			m.scroll = li - ch + 1
		}
	}
	if max := len(lines) - ch; m.scroll > max {
		m.scroll = max
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

// rowAt maps a screen row to a row index, if a peer or shell is drawn there.
func (m *viewerModel) rowAt(y int) (int, bool) {
	top := m.usageLines()
	if y < top || y >= m.height-2 {
		return 0, false
	}
	lines := m.buildLines()
	li := m.scroll + (y - top)
	if li >= len(lines) || lines[li].row < 0 {
		return 0, false
	}
	return lines[li].row, true
}

// --- rendering ---

func (m *viewerModel) View() string {
	if m.width == 0 || m.height == 0 {
		return ""
	}
	var out []string
	lines := m.buildLines()
	end := m.scroll + m.contentHeight()
	if end > len(lines) {
		end = len(lines)
	}
	for i := m.scroll; i < end; i++ {
		line := clipLine(lines[i].text, m.width)
		if lines[i].row >= 0 && lines[i].row == m.cursor {
			line = highlightRow(line, m.width)
		}
		out = append(out, line)
	}
	// tiny terminals: contentHeight's floor of 1 can overshoot the screen —
	// drop content lines rather than overflow past the footer
	if max := m.height - 2; len(out) > max && max > 0 {
		out = out[:max]
	}
	for len(out) < m.height-2 {
		out = append(out, "")
	}
	out = append(out, clipLine(viewerHints, m.width), clipLine(m.noteLine(), m.width))
	return strings.Join(out, "\n")
}

// highlightRow wraps a rendered line in a full-width reverse-video bar.
func highlightRow(line string, width int) string {
	if pad := width - visWidth(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}
	return "\x1b[7m" + line + reset
}

var viewerHints = paint(true, dim, "j/k move · g/G top/bottom · enter/click open · b branch · dd kill · t group · r refresh · q quit")

func (m *viewerModel) noteLine() string {
	if m.note == "" {
		return ""
	}
	if m.noteIsErr {
		return paint(true, red, m.note)
	}
	return m.note
}

// clipLine truncates a line to w screen cells, ANSI-aware, always ending with
// a reset so a cut colored cell can't bleed into the next line.
func clipLine(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if visWidth(s) <= w {
		return s
	}
	var b strings.Builder
	width := 0
	for i := 0; i < len(s); {
		if loc := ansiRe.FindStringIndex(s[i:]); loc != nil && loc[0] == 0 {
			b.WriteString(s[i : i+loc[1]])
			i += loc[1]
			continue
		}
		if width == w-1 {
			break
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		b.WriteRune(r)
		width++
		i += size
	}
	return b.String() + "…" + reset
}
