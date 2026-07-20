package main

import (
	"strconv"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestPinBlocked(t *testing.T) {
	mate := &Peer{AgentName: "helper", Role: "teammate", Status: "question"}
	calm := &Peer{Name: "calm", Status: "running"}
	lead := &Peer{Name: "lead", Status: "running", Teammates: []*Peer{mate}}
	shelly := &Peer{Name: "shelly", Status: "shell"}
	ordered := orderPeers(pinBlocked([]*Peer{calm, lead, mate, shelly}))
	if len(ordered) != 4 {
		t.Fatalf("want 4 rows, got %d", len(ordered))
	}
	// lead pins because its teammate is blocked; shelly pins on its own
	// status; both keep their relative order; calm sinks below
	if ordered[0] != lead || ordered[1] != mate || ordered[2] != shelly || ordered[3] != calm {
		t.Errorf("wrong pin order: %s %s %s %s",
			displayName(ordered[0]), displayName(ordered[1]), displayName(ordered[2]), displayName(ordered[3]))
	}
	for i, p := range ordered {
		if p.Index != i+1 {
			t.Errorf("index at pos %d is %d — not re-assigned to display order", i, p.Index)
		}
	}
}

func TestPinBlockedStableWhenNoneBlocked(t *testing.T) {
	a := &Peer{Name: "a", Status: "running"}
	b := &Peer{Name: "b", Status: "waiting"}
	ordered := orderPeers(pinBlocked([]*Peer{a, b}))
	if ordered[0] != a || ordered[1] != b {
		t.Errorf("order changed with nothing blocked")
	}
}

func TestPinBlockedNestingCycle(t *testing.T) {
	// two leads nested under each other must not hang or vanish
	a := &Peer{Name: "a", Status: "question"}
	b := &Peer{Name: "b", Status: "running"}
	a.Teammates = []*Peer{b}
	b.Teammates = []*Peer{a}
	ordered := orderPeers(pinBlocked([]*Peer{a, b}))
	if len(ordered) != 2 {
		t.Fatalf("want 2 rows, got %d", len(ordered))
	}
}

func TestPeerKeyInProcessTeammate(t *testing.T) {
	p := &Peer{Team: "csr", AgentName: "reviewer"}
	q := &Peer{Team: "csr", AgentName: "tests"}
	if peerKey(p) == peerKey(q) {
		t.Errorf("distinct in-process teammates must not share a key")
	}
}

func mkPeers(n int) []*Peer {
	peers := make([]*Peer, n)
	for i := range peers {
		peers[i] = &Peer{Name: "p" + strconv.Itoa(i), TmuxSession: "0", WindowIdx: i, Pane: "%" + strconv.Itoa(i), PID: i + 1}
	}
	return peers
}

func mkShells(n int) []*Peer {
	shells := make([]*Peer, n)
	for i := range shells {
		shells[i] = &Peer{
			Pane: "%" + strconv.Itoa(50+i), TmuxSession: "0", WindowIdx: 50 + i,
			PID: 100 + i, CWD: "/tmp/x", Role: "shell",
		}
	}
	return shells
}

func testModel(mode string, peers, shells []*Peer, usage string, w, h int) *viewerModel {
	m := &viewerModel{mode: mode, rawPeers: peers, rawShells: shells, usage: usage, width: w, height: h, loaded: true}
	m.rebuild()
	return m
}

func TestRebuildAnchorsCursor(t *testing.T) {
	a := &Peer{Name: "a", Pane: "%1", PID: 10}
	b := &Peer{Name: "b", Pane: "%2", PID: 20}
	m := testModel("type", []*Peer{a, b}, nil, "", 80, 30)
	m.setCursor(1) // b
	// next snapshot: b blocks on a question and pins to the top; the cursor
	// must follow it
	m.rawPeers = []*Peer{{Name: "a", Pane: "%1", PID: 10}, {Name: "b", Pane: "%2", PID: 20, Status: "question"}}
	m.rebuild()
	if m.cursor != 0 || displayName(m.selected()) != "b" {
		t.Errorf("cursor should follow b to index 0, got %d (%v)", m.cursor, displayName(m.selected()))
	}
	// selected peer gone: clamp
	m.rawPeers = []*Peer{{Name: "a", Pane: "%1", PID: 10}}
	m.rebuild()
	if m.cursor != 0 {
		t.Errorf("cursor should clamp, got %d", m.cursor)
	}
	m.rawPeers, m.rawShells = nil, nil
	m.rebuild()
	if m.cursor != 0 || m.selected() != nil {
		t.Errorf("empty snapshot should leave no selection")
	}
}

func TestRebuildAnchorsAcrossShells(t *testing.T) {
	m := testModel("type", mkPeers(1), mkShells(2), "", 80, 30)
	m.setCursor(2) // second shell
	// a new claude appears, shifting the shells' combined indexes down
	m.rawPeers = mkPeers(2)
	m.rebuild()
	if m.cursor != 3 || m.selected() != m.rawShells[1] {
		t.Errorf("cursor should follow the shell to index 3, got %d", m.cursor)
	}
}

func TestBuildLinesMapping(t *testing.T) {
	// Unrecognized usage text is omitted; rows start with title + header + peers,
	// then blank + "shells" + header + shell rows
	m := testModel("type", mkPeers(3), mkShells(2), "u", 80, 40)
	lines := m.buildLines()
	wantRows := []int{-1, -1, 0, 1, 2, -1, -1, -1, 3, 4}
	if len(lines) != len(wantRows) {
		t.Fatalf("want %d lines, got %d", len(wantRows), len(lines))
	}
	for i, w := range wantRows {
		if lines[i].row != w {
			t.Errorf("line %d: row = %d, want %d", i, lines[i].row, w)
		}
	}
	// title and header are chrome; the first peer starts at y=2
	if _, ok := m.rowAt(1); ok {
		t.Errorf("header must not hit")
	}
	if i, ok := m.rowAt(2); !ok || i != 0 {
		t.Errorf("y=2 should hit row 0, got %d ok=%v", i, ok)
	}
	if i, ok := m.rowAt(8); !ok || i != 3 {
		t.Errorf("y=8 should hit the first shell (row 3), got %d ok=%v", i, ok)
	}
	if _, ok := m.rowAt(38); ok {
		t.Errorf("footer must not hit")
	}
}

func TestMultilineUsageLayout(t *testing.T) {
	m := testModel("type", mkPeers(1), nil, "claude  5h 10%\n        7d 20%", 80, 20)
	if m.usageLines() != 0 {
		t.Fatalf("grouped usage should not reserve global header lines, got %d", m.usageLines())
	}
	view := ansiRe.ReplaceAllString(m.View(), "")
	if !strings.HasPrefix(view, "CLAUDE\n  5h 10%\n  7d 20%\n") {
		t.Fatalf("usage lines should render below their table title:\n%s", view)
	}
}

func TestBuildLinesSessionMode(t *testing.T) {
	peers := []*Peer{
		{Name: "a", TmuxSession: "0", WindowIdx: 1, Pane: "%1", PID: 1},
		{Name: "b", TmuxSession: "api", WindowIdx: 0, Pane: "%2", PID: 2},
	}
	shells := []*Peer{{TmuxSession: "0", WindowIdx: 0, Pane: "%3", PID: 3, Role: "shell", CWD: "/tmp"}}
	m := testModel("session", peers, shells, "", 80, 40)
	// groups: "0" (shell win 0, claude win 1), "api" (claude)
	if len(m.groups) != 2 || m.groups[0].title != "session 0" || m.groups[1].title != "session api" {
		t.Fatalf("wrong groups: %+v", m.groups)
	}
	if m.groups[0].rows[0].Role != "shell" || m.groups[0].rows[1].Name != "a" {
		t.Errorf("session 0 should order by window: shell(w0) then a(w1)")
	}
	// lines per group: title, header, then a window sub-header before each
	// window's rows; groups after the first get a leading blank
	lines := m.buildLines()
	wantRows := []int{-1, -1, -1, 0, -1, 1, -1, -1, -1, -1, 2}
	if len(lines) != len(wantRows) {
		t.Fatalf("want %d lines, got %d", len(wantRows), len(lines))
	}
	for i, w := range wantRows {
		if lines[i].row != w {
			t.Errorf("line %d: row = %d, want %d", i, lines[i].row, w)
		}
	}
	if !strings.Contains(lines[2].text, "window 0") || !strings.Contains(lines[4].text, "window 1") {
		t.Errorf("window sub-headers missing: %q / %q", lines[2].text, lines[4].text)
	}
}

func TestScrollFollowsCursorLines(t *testing.T) {
	m := testModel("type", mkPeers(20), nil, "", 80, 8) // content height 6
	m.setCursor(19)
	// cursor line = 21 (title and header first); scroll = 21-6+1
	if m.scroll != 16 {
		t.Errorf("scroll = %d, want 16", m.scroll)
	}
	m.setCursor(0)
	// scrolling up reveals the header chrome above row 0
	if m.scroll != 0 {
		t.Errorf("scroll = %d, want 0 (header revealed)", m.scroll)
	}
}

func TestModeCycleKeepsCursor(t *testing.T) {
	peers := []*Peer{
		{Name: "a", TmuxSession: "0", WindowIdx: 0, Pane: "%1", PID: 1},
		{Name: "b", TmuxSession: "api", WindowIdx: 0, Pane: "%2", PID: 2},
	}
	shells := []*Peer{{TmuxSession: "0", WindowIdx: 5, Pane: "%3", PID: 3, Role: "shell", CWD: "/tmp"}}
	m := testModel("type", peers, shells, "", 80, 40)
	m.setCursor(2) // the shell
	d := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}}
	m.handleKey(d)
	if m.mode != "session" {
		t.Fatalf("t should cycle to session, got %s", m.mode)
	}
	if p := m.selected(); p == nil || p.Role != "shell" {
		t.Errorf("cursor should stay on the shell across a mode change")
	}
	m.handleKey(d)
	m.handleKey(d)
	if m.mode != "type" {
		t.Errorf("t should cycle back to type, got %s", m.mode)
	}
}

func TestClipLine(t *testing.T) {
	if got := clipLine("short", 10); got != "short" {
		t.Errorf("short line changed: %q", got)
	}
	got := clipLine("abcdefghij", 5)
	if vw := visWidth(got); vw != 5 {
		t.Errorf("clipped width = %d, want 5 (%q)", vw, got)
	}
	if !strings.HasSuffix(got, "…"+reset) {
		t.Errorf("clip must end with ellipsis+reset: %q", got)
	}
	// ANSI codes are preserved, not counted
	colored := cyan + "abcdef" + reset + "ghij"
	got = clipLine(colored, 5)
	if vw := visWidth(got); vw != 5 {
		t.Errorf("ansi clip width = %d, want 5 (%q)", vw, got)
	}
	if !strings.Contains(got, cyan) {
		t.Errorf("ansi prefix dropped: %q", got)
	}
}

// TestBuildJobLinesFiveStates renders one job per board state (draft ·
// queued · working · blocked · done) and checks each state label shows up
// as its own selectable row, keyed to its position in m.jrows.
func TestBuildJobLinesFiveStates(t *testing.T) {
	jobs := []*Job{
		{Slug: "d1", Type: "build", Ready: false}, // never started, not ready
		{Slug: "q1", Type: "build", Ready: true},  // ready, never started
		{Slug: "w1", Type: "build", Ready: true, State: "working"},
		{Slug: "b1", Type: "build", Ready: true, State: "blocked"},
		{Slug: "done1", Type: "build", Ready: true, State: "done"},
	}
	m := &viewerModel{
		view: "jobs", loaded: true, width: 100, height: 40,
		jrows: jobs, liveSessionSet: map[string]bool{}, healthBySlug: map[string]string{},
	}
	lines := m.buildJobLines()
	var joined strings.Builder
	rows := 0
	for _, l := range lines {
		joined.WriteString(l.text)
		joined.WriteString("\n")
		if l.row >= 0 {
			rows++
		}
	}
	text := joined.String()
	for _, want := range []string{"draft", "queued", "working", "blocked", "done"} {
		if !strings.Contains(text, want) {
			t.Errorf("board missing state %q:\n%s", want, text)
		}
	}
	if rows != len(jobs) {
		t.Errorf("want %d selectable rows, got %d", len(jobs), rows)
	}
	// row indexes must map 1:1 back onto m.jrows in order
	idx := 0
	for _, l := range lines {
		if l.row < 0 {
			continue
		}
		if l.row != idx {
			t.Errorf("row %d text %q has viewLine.row = %d, want %d", idx, l.text, l.row, idx)
		}
		idx++
	}
}

func TestJobRowKeyAnchorsCursorAcrossRefresh(t *testing.T) {
	a := &Job{Slug: "a", Type: "build", Ready: true}
	b := &Job{Slug: "b", Type: "build", Ready: true}
	m := &viewerModel{
		view: "jobs", loaded: true, width: 100, height: 40,
		rawJobs: []*Job{a, b}, liveSessionSet: map[string]bool{}, healthBySlug: map[string]string{},
	}
	m.rebuild()
	m.setCursor(1) // b
	// next snapshot: same jobs, new *Job values (as a fresh loadJobs() would
	// produce) — the cursor must still follow slug "b"
	m.rawJobs = []*Job{{Slug: "a", Type: "build", Ready: true}, {Slug: "b", Type: "build", Ready: true}}
	m.rebuild()
	if m.cursor != 1 || m.jselected().Slug != "b" {
		t.Errorf("cursor should follow job b, got %d (%v)", m.cursor, m.jselected())
	}
}

func TestDDChord(t *testing.T) {
	d := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	j := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}}
	peers := []*Peer{{Name: "c", Pane: "%1", PID: 10, Role: "main"}}
	m := testModel("type", peers, mkShells(2), "", 80, 30)

	// d arms on a claude too (kill applies to both kinds now)
	_, cmd := m.handleKey(d)
	if cmd != nil || m.pendingKill != peerKey(peers[0]) {
		t.Fatalf("first d on a claude should arm, not fire")
	}
	if !strings.Contains(m.note, "claude") {
		t.Errorf("arm note should say what kind is armed: %q", m.note)
	}

	// d on a shell arms, second d fires the kill Cmd
	m.setCursor(1)
	m.handleKey(j) // disarm via a movement key (also moves cursor to 2)
	m.setCursor(1)
	if _, cmd := m.handleKey(d); cmd != nil || m.pendingKill == "" {
		t.Fatalf("first d should arm, not fire")
	}
	if _, cmd := m.handleKey(d); cmd == nil {
		t.Fatalf("second d should fire the kill")
	}
	if m.pendingKill != "" {
		t.Errorf("firing must disarm")
	}

	// any other key disarms
	m.handleKey(d)
	m.handleKey(j)
	if m.pendingKill != "" {
		t.Errorf("j should disarm a pending dd")
	}

	// selection changing between the two d's re-arms instead of firing
	m.setCursor(1)
	m.handleKey(d)
	m.cursor = 2 // simulate churn moving the selection without a keypress
	if _, cmd := m.handleKey(d); cmd != nil {
		t.Errorf("d after the target changed must re-arm, not kill")
	}
	if m.pendingKill != peerKey(m.rows[2]) {
		t.Errorf("pending kill should now track the new selection")
	}
}
