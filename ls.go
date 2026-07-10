package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- k ls ---

const (
	reset   = "\x1b[0m"
	dim     = "\x1b[2m"
	cyan    = "\x1b[36m"
	green   = "\x1b[32m"
	yellow  = "\x1b[33m"
	magenta = "\x1b[35m"
	red     = "\x1b[31m"
	gray    = "\x1b[90m"
)

func useColor() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

func paint(on bool, code, s string) string {
	if !on {
		return s
	}
	return code + s + reset
}

func statusColor(status string) string {
	switch status {
	case "running":
		return green
	case "question", "shell":
		return yellow
	case "waiting":
		return red
	default:
		return gray
	}
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// visWidth is a cell's on-screen width: ANSI codes are invisible to the
// terminal but not to a byte-counting aligner like tabwriter.
func visWidth(s string) int {
	return len([]rune(ansiRe.ReplaceAllString(s, "")))
}

// formatTableLines aligns rows into columns (ANSI-aware) and returns one
// string per row, without trailing newlines.
func formatTableLines(rows [][]string) []string {
	if len(rows) == 0 {
		return nil
	}
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			if w := visWidth(c); w > widths[i] {
				widths[i] = w
			}
		}
	}
	lines := make([]string, 0, len(rows))
	for _, r := range rows {
		var b strings.Builder
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-visWidth(c)+2))
			}
		}
		lines = append(lines, b.String())
	}
	return lines
}

func printTable(rows [][]string) {
	for _, line := range formatTableLines(rows) {
		fmt.Println(line)
	}
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func displayName(p *Peer) string {
	if p.AgentName != "" {
		return p.AgentName
	}
	if p.Name != "" {
		return p.Name
	}
	return "-"
}

func projectLabel(p *Peer) string {
	base := filepath.Base(p.CWD)
	if base == "" || base == "/" || base == "." {
		return "~"
	}
	return base
}

func parseGroupMode(s string) string {
	switch s {
	case "type", "session", "none":
		return s
	}
	die("unknown grouping %q (type, session, none)", s)
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
				die("-g needs a value: type, session, or none")
			}
			mode = parseGroupMode(args[i])
		default:
			die("unknown argument: %s (usage: k ls [--json] [-g type|session|none])", args[i])
		}
	}
	all, nPeers := orderAll(discoverAll())
	if jsonOut {
		if all == nil {
			all = []*Peer{}
		}
		out, err := json.MarshalIndent(all, "", "  ")
		if err != nil {
			die("marshal: %v", err)
		}
		fmt.Println(string(out))
		return
	}
	color := useColor()
	usage := usageHeader(color)
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
			fmt.Println(paint(color, g.titleColor(), strings.ToUpper(g.title)))
		}
		for _, line := range usageForAgent(usage, g.title, color) {
			fmt.Println(line)
		}
		header := groupHeader(g.kind)
		for i, h := range header {
			header[i] = paint(color, cyan, strings.ToUpper(h))
		}
		rows := [][]string{header}
		parent := parentMap(g.rows)
		for _, p := range g.rows {
			rows = append(rows, groupCells(g.kind, p, parent[p], color, now))
		}
		aligned := formatTableLines(rows)
		fmt.Println(aligned[0])
		for i, line := range aligned[1:] {
			if st, ok := g.subtitles[i]; ok {
				fmt.Println(paint(color, dim, " "+st))
			}
			fmt.Println(line)
		}
	}
}

// --- display grouping ---

// A displayGroup is one rendered table: k ls prints them in order; the viewer
// stacks them in a single scroll region.
type displayGroup struct {
	title string  // "" for the untitled leading group
	kind  string  // "claudes" | "shells" | "session" | "mixed" — column schema
	rows  []*Peer // display order; teammates directly follow their lead
	// subtitles[i] renders as a sub-header line right before rows[i] —
	// session grouping uses it for "window N" breaks
	subtitles map[int]string
}

func (g displayGroup) titleColor() string {
	return cyan
}

// displayGroups shapes the combined list for a grouping mode:
//
//	type:    claudes table + labelled shells table (the default)
//	session: one mixed table per tmux session, "window N" sub-headers inside
//	none:    everything in one table
func displayGroups(mode string, all []*Peer, nPeers int) []displayGroup {
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
		var claudes, codexes []*Peer
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
func sessionGroups(claudes, shells []*Peer) []displayGroup {
	type unit struct {
		win  int
		rows []*Peer
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
		add(s.TmuxSession, unit{win: s.WindowIdx, rows: []*Peer{s}})
	}
	sort.SliceStable(sessions, func(i, j int) bool { return sessionLess(sessions[i], sessions[j]) })
	var groups []displayGroup
	for _, sess := range sessions {
		us := units[sess]
		sort.SliceStable(us, func(i, j int) bool { return us[i].win < us[j].win })
		var rows []*Peer
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
func groupCells(kind string, p, lead *Peer, color bool, now time.Time) []string {
	switch kind {
	case "claudes":
		c := peerCells(p, lead, color, now)
		return []string{c[0], c[1], c[2], c[3], c[4], c[5], paint(color, dim, p.Pane), c[6], c[7]}
	case "shells":
		return shellCells(p, color)
	default:
		return mixedCells(kind, p, lead, color, now)
	}
}

// mixedCells renders a row for the schemas that put claudes and shells in one
// table: shells show "-" for NAME and fill CMD; claudes leave CMD empty.
func mixedCells(kind string, p, lead *Peer, color bool, now time.Time) []string {
	var cells []string
	if p.Role == "shell" {
		cmd, ageStr := paint(color, dim, "-"), ""
		if p.Cmd != "" {
			cmd = paint(color, green, truncateOneLine(p.Cmd, 60))
			ageStr = paint(color, dim, age(time.Duration(p.CmdSeconds)*time.Second))
		}
		cmd += selfMark(p, color) // even when idle: JSON self and display must agree
		cells = []string{
			paint(color, dim, fmt.Sprintf("%2d", p.Index)),
			paint(color, statusColor(p.Status), "● "+p.Status),
			paint(color, dim, "-"),
			projectLabel(p),
			paint(color, magenta, truncTail(p.Branch, 28)),
			"", // model
			cmd,
			ageStr,
			paint(color, dim, p.Pane),
		}
	} else {
		c := peerCells(p, lead, color, now)
		cells = []string{c[0], c[1], c[2], c[3], c[4], c[5], "", c[7], paint(color, dim, p.Pane)}
	}
	if kind == "session" {
		return cells // session lives in the group title, window in sub-headers
	}
	return append(cells, paint(color, dim, sessWin(p)))
}

// selfMark tags the pane this kraken invocation runs in — the viewer's own
// pane would otherwise read as just another shell.
func selfMark(p *Peer, color bool) string {
	if !p.Self {
		return ""
	}
	return paint(color, cyan, " ← me")
}

// shellFolder renders a shell pane's cwd ~-abbreviated: unlike claudes, the
// folder is the pane's primary identity, so the full path matters.
func shellFolder(cwd string) string {
	if cwd == "" {
		return "-"
	}
	h := home()
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
func shellCells(p *Peer, color bool) []string {
	cmd, ageStr := paint(color, dim, "-"), ""
	if p.Cmd != "" {
		cmd = paint(color, green, truncateOneLine(p.Cmd, 60))
		ageStr = paint(color, dim, age(time.Duration(p.CmdSeconds)*time.Second))
	}
	cmd += selfMark(p, color) // even when idle: JSON self and display must agree
	return []string{
		paint(color, dim, fmt.Sprintf("%2d", p.Index)),
		paint(color, statusColor(p.Status), "● "+p.Status),
		truncTail(shellFolder(p.CWD), 40),
		cmd,
		ageStr,
		paint(color, dim, p.Pane),
		paint(color, dim, sessWin(p)),
	}
}

// parentMap maps each nested teammate to its lead — the peer whose Teammates
// list claimed it. Positional inference (previous non-teammate row) is wrong
// for orphaned teammates and sub-teams, so rendering keys off this instead.
func parentMap(peers []*Peer) map[*Peer]*Peer {
	m := map[*Peer]*Peer{}
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
func peerCells(p, lead *Peer, color bool, now time.Time) []string {
	name := displayName(p)
	project, branch := projectLabel(p), p.Branch
	if lead != nil {
		name = "  └ " + name
		if project == projectLabel(lead) {
			project = "·"
		}
		if branch == lead.Branch {
			branch = "·"
		}
	}
	ageStr := ""
	if p.StatusAt > 0 {
		ageStr = age(now.Sub(time.UnixMilli(p.StatusAt)))
	}
	return []string{
		paint(color, dim, fmt.Sprintf("%2d", p.Index)),
		paint(color, statusColor(p.Status), "● "+p.Status),
		paint(color, cyan, name) + selfMark(p, color),
		project,
		paint(color, magenta, truncTail(branch, 28)),
		paint(color, dim, p.Model),
		paint(color, dim, sessWin(p)),
		paint(color, dim, ageStr),
	}
}

// sessWin is a pane's tmux address, session:window; in-process teammates have
// no pane, so just the session.
func sessWin(p *Peer) string {
	if p.Pane == "" {
		return p.TmuxSession
	}
	return p.TmuxSession + ":" + strconv.Itoa(p.WindowIdx)
}
