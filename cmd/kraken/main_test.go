package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"kraken/core"
)

func TestPeerCellsOrphanTeammate(t *testing.T) {
	// a teammate whose lead was never discovered must render as a top-level
	// row (no └, no ditto marks against an unrelated preceding peer)
	orphan := &core.Peer{AgentName: "orphan", Role: "teammate", CWD: "/tmp/proj", Branch: "main"}
	ordered := core.OrderPeers([]*core.Peer{{Name: "other", CWD: "/tmp/other", Branch: "main"}, orphan})
	parent := parentMap(ordered)
	if parent[orphan] != nil {
		t.Fatalf("orphan must have no parent")
	}
	cells := peerCells(orphan, parent[orphan], false, time.Now())
	if strings.Contains(cells[2], "└") {
		t.Errorf("orphan rendered as nested: %q", cells[2])
	}
	if cells[3] == "·" || cells[4] == "·" {
		t.Errorf("orphan got ditto marks: project=%q branch=%q", cells[3], cells[4])
	}
}

func TestPeerCellsSubTeamDitto(t *testing.T) {
	// a sub-lead's teammate dittoes against the sub-lead, not the top lead
	sub := &core.Peer{AgentName: "sub", CWD: "/tmp/mid", Branch: "mid-branch"}
	mid := &core.Peer{Name: "mid", CWD: "/tmp/mid", Branch: "mid-branch", Teammates: []*core.Peer{sub}}
	top := &core.Peer{Name: "top", CWD: "/tmp/top", Branch: "top-branch", Teammates: []*core.Peer{mid}}
	ordered := core.OrderPeers([]*core.Peer{top, mid, sub})
	parent := parentMap(ordered)
	if parent[sub] != mid {
		t.Fatalf("sub's parent should be mid, got %v", parent[sub])
	}
	cells := peerCells(sub, parent[sub], false, time.Now())
	if cells[3] != "·" || cells[4] != "·" {
		t.Errorf("sub should ditto against mid: project=%q branch=%q", cells[3], cells[4])
	}
}

func TestShellFolder(t *testing.T) {
	h, _ := os.UserHomeDir()
	if got := shellFolder(h); got != "~" {
		t.Errorf("home = %q, want ~", got)
	}
	if got := shellFolder(filepath.Join(h, "Developer", "kraken")); got != "~/Developer/kraken" {
		t.Errorf("home subdir = %q", got)
	}
	if got := shellFolder("/opt/thing"); got != "/opt/thing" {
		t.Errorf("non-home path changed: %q", got)
	}
	if got := shellFolder(""); got != "-" {
		t.Errorf("empty cwd = %q, want -", got)
	}
}

func TestDisplayGroups(t *testing.T) {
	mate := &core.Peer{AgentName: "helper", Role: "teammate", TmuxSession: "api", WindowIdx: 3, Pane: "%9", PID: 9}
	lead := &core.Peer{Name: "lead", TmuxSession: "0", WindowIdx: 2, Pane: "%1", PID: 1, Teammates: []*core.Peer{mate}}
	solo := &core.Peer{Name: "solo", TmuxSession: "api", WindowIdx: 0, Pane: "%2", PID: 2}
	sh0 := &core.Peer{Role: "shell", TmuxSession: "0", WindowIdx: 0, Pane: "%3", PID: 3, CWD: "/tmp"}
	shAPI := &core.Peer{Role: "shell", TmuxSession: "api", WindowIdx: 1, Pane: "%4", PID: 4, CWD: "/tmp"}
	all, n := core.OrderAll([]*core.Peer{lead, mate, solo}, []*core.Peer{sh0, shAPI})

	// type: claudes table then labelled shells table
	groups := displayGroups("type", all, n)
	if len(groups) != 2 || groups[0].kind != "claudes" || groups[1].title != "shells" {
		t.Fatalf("type mode: %+v", groups)
	}
	if len(groups[0].rows) != 3 || len(groups[1].rows) != 2 {
		t.Errorf("type mode row split wrong: %d + %d", len(groups[0].rows), len(groups[1].rows))
	}

	// none: everything in one table, canonical order
	groups = displayGroups("none", all, n)
	if len(groups) != 1 || groups[0].kind != "mixed" || len(groups[0].rows) != 5 {
		t.Fatalf("none mode: %+v", groups)
	}

	// session: one group per session, ordered by window; the teammate stays
	// with its lead in the lead's session even though it lives in "api"
	groups = displayGroups("session", all, n)
	if len(groups) != 2 || groups[0].title != "session 0" || groups[1].title != "session api" {
		t.Fatalf("session mode groups: %+v", groups)
	}
	g0 := groups[0].rows
	if len(g0) != 3 || g0[0] != sh0 || g0[1] != lead || g0[2] != mate {
		t.Errorf("session 0 should hold shell(w0), lead(w2), nested mate — got %d rows", len(g0))
	}
	// window sub-headers break before rows 0 (window 0) and 1 (window 2);
	// the nested mate rides its lead's window and gets no break of its own
	if groups[0].subtitles[0] != "window 0" || groups[0].subtitles[1] != "window 2" || len(groups[0].subtitles) != 2 {
		t.Errorf("session 0 subtitles wrong: %v", groups[0].subtitles)
	}
	gAPI := groups[1].rows
	if len(gAPI) != 2 || gAPI[0] != solo || gAPI[1] != shAPI {
		t.Errorf("session api should hold solo(w0) then shell(w1)")
	}
	if gAPI := groups[1].subtitles; len(gAPI) != 2 || gAPI[0] != "window 0" || gAPI[1] != "window 1" {
		t.Errorf("session api subtitles wrong: %v", gAPI)
	}
}

func TestDisplayGroupsSeparatesCodex(t *testing.T) {
	claude := &core.Peer{Name: "claude", Agent: "claude", Pane: "%1"}
	codex := &core.Peer{Name: "codex", Agent: "codex", Pane: "%2"}
	all, n := core.OrderAll([]*core.Peer{codex, claude}, nil)
	groups := displayGroups("type", all, n)
	if len(groups) != 2 || groups[0].rows[0] != claude || groups[1].title != "codex" || groups[1].rows[0] != codex {
		t.Fatalf("expected separate Claude and Codex groups, got %+v", groups)
	}
	if claude.Index != 1 || codex.Index != 2 {
		t.Fatalf("indexes should follow rendered group order: claude=%d codex=%d", claude.Index, codex.Index)
	}
}

func TestGroupCellsMatchHeaders(t *testing.T) {
	// a column added to one side but not the other would silently misalign
	// every table
	claude := &core.Peer{Name: "c", TmuxSession: "0", WindowIdx: 1, Pane: "%1", PID: 1, Index: 1}
	shell := &core.Peer{Role: "shell", TmuxSession: "0", WindowIdx: 2, Pane: "%2", PID: 2, Index: 2, CWD: "/tmp", Cmd: "make", CmdSeconds: 60}
	now := time.Now()
	for _, kind := range []string{"claudes", "shells", "session", "mixed"} {
		want := len(groupHeader(kind))
		rows := []*core.Peer{shell}
		if kind != "shells" {
			rows = append(rows, claude)
		}
		if kind == "claudes" {
			rows = []*core.Peer{claude}
		}
		for _, p := range rows {
			if got := len(groupCells(kind, p, nil, false, now)); got != want {
				t.Errorf("%s: %s row has %d cells, header has %d", kind, p.Role, got, want)
			}
		}
	}
}

func TestSplitDirFlag(t *testing.T) {
	dir, rest := splitDirFlag([]string{"-c", "/tmp", "make", "test"})
	if dir != "/tmp" || len(rest) != 2 || rest[0] != "make" {
		t.Errorf("got dir=%q rest=%v", dir, rest)
	}
	cwd, _ := os.Getwd()
	dir, rest = splitDirFlag([]string{"make"})
	if dir != cwd || len(rest) != 1 {
		t.Errorf("default dir: got %q rest=%v", dir, rest)
	}
}

func TestSelfMarkRendering(t *testing.T) {
	self := &core.Peer{Role: "shell", Pane: "%1", PID: 1, Index: 1, CWD: "/tmp", Cmd: "k v", Self: true}
	other := &core.Peer{Role: "shell", Pane: "%2", PID: 2, Index: 2, CWD: "/tmp", Cmd: "make"}
	if !strings.Contains(strings.Join(shellCells(self, false), " "), "← me") {
		t.Errorf("self shell row must carry the me mark")
	}
	if strings.Contains(strings.Join(shellCells(other, false), " "), "← me") {
		t.Errorf("non-self row must not carry the me mark")
	}
	// idle self (stale TMUX_PANE pointing at a commandless pane): the mark
	// must still render so display and JSON agree
	idle := &core.Peer{Role: "shell", Pane: "%3", PID: 3, Index: 3, CWD: "/tmp", Self: true}
	if !strings.Contains(strings.Join(shellCells(idle, false), " "), "← me") {
		t.Errorf("idle self row must still carry the me mark")
	}
	agent := &core.Peer{Name: "c", Pane: "%4", PID: 4, Index: 4, Self: true}
	if !strings.Contains(strings.Join(peerCells(agent, nil, false, time.Now()), " "), "← me") {
		t.Errorf("self agent row must carry the me mark")
	}
}
