package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"kraken/internal/core"
	"kraken/internal/util"
)

// --- k ls ---

func parseGroupMode(s string) string {
	switch s {
	case "type", "session", "none":
		return s
	}
	util.Die("unknown grouping %q (type, session, none)", s)
	return ""
}

func cmdLs(args []string) {
	jsonOut := false
	mode := "type"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--json":
			jsonOut = true
		case "-g", "--group":
			i++
			if i >= len(args) {
				util.Die("-g needs a value: type, session, or none")
			}
			mode = parseGroupMode(args[i])
		default:
			util.Die("unknown argument: %s (usage: k ls [--json] [-g type|session|none])", args[i])
		}
	}
	all, nPeers := core.OrderAll(core.DiscoverAll())
	if jsonOut {
		if all == nil {
			all = []*core.Peer{}
		}
		out, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			util.Die("marshal: %v", err)
		}
		fmt.Println(string(out))
		return
	}
	color := util.UseColor()
	usage := core.UsageHeader(color)
	if len(all) == 0 {
		fmt.Println("no live sessions")
		return
	}
	now := time.Now()
	for gi, g := range displayGroups(mode, all, nPeers) {
		if gi > 0 {
			fmt.Println()
		}
		if g.title != "" {
			fmt.Println(util.Paint(color, g.titleColor(), strings.ToUpper(g.title)))
		}
		for _, line := range core.UsageForAgent(usage, g.title, color) {
			fmt.Println(line)
		}
		header := groupHeader(g.kind)
		for i, h := range header {
			header[i] = util.Paint(color, util.Cyan, strings.ToUpper(h))
		}
		rows := [][]string{header}
		parent := parentMap(g.rows)
		for _, p := range g.rows {
			rows = append(rows, groupCells(g.kind, p, parent[p], color, now))
		}
		aligned := util.FormatTableLines(rows)
		fmt.Println(aligned[0])
		for i, line := range aligned[1:] {
			if st, ok := g.subtitles[i]; ok {
				fmt.Println(util.Paint(color, util.Dim, " "+st))
			}
			fmt.Println(line)
		}
	}
}

// --- display grouping ---

// A displayGroup is one rendered table: k ls prints them in order; the viewer
// stacks them in a single scroll region.
type displayGroup struct {
	title string       // "" for the untitled leading group
	kind  string       // "claudes" | "shells" | "session" | "mixed" — column schema
	rows  []*core.Peer // display order; teammates directly follow their lead
	// subtitles[i] renders as a sub-header line right before rows[i] —
	// session grouping uses it for "window N" breaks
	subtitles map[int]string
}

func (g displayGroup) titleColor() string {
	return util.Cyan
}

// displayGroups shapes the combined list for a grouping mode:
//
//	type:    claudes table + labelled shells table (the default)
//	session: one mixed table per tmux session, "window N" sub-headers inside
//	none:    everything in one table
func displayGroups(mode string, all []*core.Peer, nPeers int) []displayGroup {
	agents, shells := all[:nPeers], all[nPeers:]
	switch mode {
	case "session":
		return sessionGroups(agents, shells)
	case "none":
		if len(all) == 0 {
			return nil
		}
		return []displayGroup{{kind: "mixed", rows: all}}
	default: // "type"
		var groups []displayGroup
		var claudes, codexes []*core.Peer
		for _, p := range agents {
			if p.Agent == "codex" {
				codexes = append(codexes, p)
			} else {
				claudes = append(claudes, p)
			}
		}
		if len(claudes) > 0 {
			groups = append(groups, displayGroup{title: "claude", kind: "claudes", rows: claudes})
		}
		if len(codexes) > 0 {
			groups = append(groups, displayGroup{title: "codex", kind: "claudes", rows: codexes})
		}
		if len(shells) > 0 {
			groups = append(groups, displayGroup{title: "shells", kind: "shells", rows: shells})
		}
		return groups
	}
}

// sessionGroups buckets rows by tmux session, ordered by window within each.
// A lead and its nested teammates travel as one unit positioned by the lead's
// window (a teammate may live in another window or even session). Claudes
// sort before shells within the same window.
func sessionGroups(claudes, shells []*core.Peer) []displayGroup {
	type unit struct {
		win  int
		rows []*core.Peer
	}
	units := map[string][]unit{}
	var sessions []string
	add := func(sess string, u unit) {
		if _, ok := units[sess]; !ok {
			sessions = append(sessions, sess)
		}
		units[sess] = append(units[sess], u)
	}
	parent := parentMap(claudes)
	for i := 0; i < len(claudes); {
		j := i + 1
		for j < len(claudes) && parent[claudes[j]] != nil {
			j++
		}
		add(claudes[i].TmuxSession, unit{win: claudes[i].WindowIdx, rows: claudes[i:j]})
		i = j
	}
	for _, s := range shells {
		add(s.TmuxSession, unit{win: s.WindowIdx, rows: []*core.Peer{s}})
	}
	sort.SliceStable(sessions, func(i, j int) bool { return core.SessionLess(sessions[i], sessions[j]) })
	var groups []displayGroup
	for _, sess := range sessions {
		us := units[sess]
		sort.SliceStable(us, func(i, j int) bool { return us[i].win < us[j].win })
		var rows []*core.Peer
		subtitles := map[int]string{}
		lastWin := -1
		for _, u := range us {
			// window breaks follow the unit's window, so a teammate nested
			// under a lead never splits its lead's block
			if u.win != lastWin {
				subtitles[len(rows)] = "window " + strconv.Itoa(u.win)
				lastWin = u.win
			}
			rows = append(rows, u.rows...)
		}
		groups = append(groups, displayGroup{title: "session " + sess, kind: "session", rows: rows, subtitles: subtitles})
	}
	return groups
}

func groupHeader(kind string) []string {
	switch kind {
	case "claudes":
		return []string{" #", "STATUS", "NAME", "PROJECT", "BRANCH", "MODEL", "PANE", "TMUX", "AGE"}
	case "shells":
		return shellHeader()
	case "session":
		// no WIN/TMUX columns: the group title carries the session and the
		// "window N" sub-headers carry the window
		return []string{" #", "STATUS", "NAME", "PROJECT", "BRANCH", "MODEL", "CMD", "AGE", "PANE"}
	default: // mixed
		return []string{" #", "STATUS", "NAME", "PROJECT", "BRANCH", "MODEL", "CMD", "AGE", "PANE", "TMUX"}
	}
}

// groupCells renders one row under a group's schema; lead comes from
// parentMap over the group's rows and drives teammate indent/dittos.
func groupCells(kind string, p, lead *core.Peer, color bool, now time.Time) []string {
	switch kind {
	case "claudes":
		c := peerCells(p, lead, color, now)
		return []string{c[0], c[1], c[2], c[3], c[4], c[5], util.Paint(color, util.Dim, p.Pane), c[6], c[7]}
	case "shells":
		return shellCells(p, color)
	default:
		return mixedCells(kind, p, lead, color, now)
	}
}

// mixedCells renders a row for the schemas that put claudes and shells in one
// table: shells show "-" for NAME and fill CMD; claudes leave CMD empty.
func mixedCells(kind string, p, lead *core.Peer, color bool, now time.Time) []string {
	var cells []string
	if p.Role == "shell" {
		cmd, ageStr := util.Paint(color, util.Dim, "-"), ""
		if p.Cmd != "" {
			cmd = util.Paint(color, util.Green, core.TruncateOneLine(p.Cmd, 60))
			ageStr = util.Paint(color, util.Dim, util.Age(time.Duration(p.CmdSeconds)*time.Second))
		}
		cmd += selfMark(p, color) // even when idle: JSON self and display must agree
		cells = []string{
			util.Paint(color, util.Dim, fmt.Sprintf("%2d", p.Index)),
			util.Paint(color, util.StatusColor(p.Status), "● "+p.Status),
			util.Paint(color, util.Dim, "-"),
			core.ProjectLabel(p),
			util.Paint(color, util.Magenta, core.TruncTail(p.Branch, 28)),
			"", // model
			cmd,
			ageStr,
			util.Paint(color, util.Dim, p.Pane),
		}
	} else {
		c := peerCells(p, lead, color, now)
		cells = []string{c[0], c[1], c[2], c[3], c[4], c[5], "", c[7], util.Paint(color, util.Dim, p.Pane)}
	}
	if kind == "session" {
		return cells // session lives in the group title, window in sub-headers
	}
	return append(cells, util.Paint(color, util.Dim, sessWin(p)))
}

// selfMark tags the pane this kraken invocation runs in — the viewer's own
// pane would otherwise read as just another shell.
func selfMark(p *core.Peer, color bool) string {
	if !p.Self {
		return ""
	}
	return util.Paint(color, util.Cyan, " ← me")
}

// shellFolder renders a shell pane's cwd ~-abbreviated: unlike claudes, the
// folder is the pane's primary identity, so the full path matters.
func shellFolder(cwd string) string {
	if cwd == "" {
		return "-"
	}
	h := util.Home()
	if cwd == h {
		return "~"
	}
	if rest, ok := strings.CutPrefix(cwd, h+"/"); ok {
		return "~/" + rest
	}
	return cwd
}

func shellHeader() []string {
	return []string{" #", "STATUS", "FOLDER", "CMD", "AGE", "PANE", "TMUX"}
}

// shellCells renders one shells-table row, shared by k ls and the viewer.
func shellCells(p *core.Peer, color bool) []string {
	cmd, ageStr := util.Paint(color, util.Dim, "-"), ""
	if p.Cmd != "" {
		cmd = util.Paint(color, util.Green, core.TruncateOneLine(p.Cmd, 60))
		ageStr = util.Paint(color, util.Dim, util.Age(time.Duration(p.CmdSeconds)*time.Second))
	}
	cmd += selfMark(p, color) // even when idle: JSON self and display must agree
	return []string{
		util.Paint(color, util.Dim, fmt.Sprintf("%2d", p.Index)),
		util.Paint(color, util.StatusColor(p.Status), "● "+p.Status),
		core.TruncTail(shellFolder(p.CWD), 40),
		cmd,
		ageStr,
		util.Paint(color, util.Dim, p.Pane),
		util.Paint(color, util.Dim, sessWin(p)),
	}
}

// parentMap maps each nested teammate to its lead — the peer whose Teammates
// list claimed it. Positional inference (previous non-teammate row) is wrong
// for orphaned teammates and sub-teams, so rendering keys off this instead.
func parentMap(peers []*core.Peer) map[*core.Peer]*core.Peer {
	m := map[*core.Peer]*core.Peer{}
	for _, p := range peers {
		for _, t := range p.Teammates {
			if _, ok := m[t]; !ok {
				m[t] = p
			}
		}
	}
	return m
}

// peerCells renders the display cells shared by k ls and the viewer:
// #, status, name, project, branch, model, tmux, age. lead is the peer's
// actual team lead (nil for top-level peers): teammates indent with ditto
// marks where they match their lead — the eye only catches what differs.
func peerCells(p, lead *core.Peer, color bool, now time.Time) []string {
	name := core.DisplayName(p)
	project, branch := core.ProjectLabel(p), p.Branch
	if lead != nil {
		name = "  └ " + name
		if project == core.ProjectLabel(lead) {
			project = "·"
		}
		if branch == lead.Branch {
			branch = "·"
		}
	}
	ageStr := ""
	if p.StatusAt > 0 {
		ageStr = util.Age(now.Sub(time.UnixMilli(p.StatusAt)))
	}
	// A claude at its prompt with live background tasks (in-process
	// subagents, monitors, background shells) says so beside its status —
	// "waiting · 3 subagents · 1 monitor" — since the status word alone
	// reads as idle while the work is inside those tasks.
	status := util.Paint(color, util.StatusColor(p.Status), "● "+p.Status)
	if s := core.TaskSummary(p.Tasks); s != "" {
		status += util.Paint(color, util.Dim, " · "+s)
	}
	return []string{
		util.Paint(color, util.Dim, fmt.Sprintf("%2d", p.Index)),
		status,
		util.Paint(color, util.Cyan, name) + selfMark(p, color),
		project,
		util.Paint(color, util.Magenta, core.TruncTail(branch, 28)),
		util.Paint(color, util.Dim, p.Model),
		util.Paint(color, util.Dim, sessWin(p)),
		util.Paint(color, util.Dim, ageStr),
	}
}

// sessWin is a pane's tmux address, session:window; in-process teammates have
// no pane, so just the session.
func sessWin(p *core.Peer) string {
	if p.Pane == "" {
		return p.TmuxSession
	}
	return p.TmuxSession + ":" + strconv.Itoa(p.WindowIdx)
}
