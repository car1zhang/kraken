package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
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
	Members       []teamMember `json:"members"`
}

type teamMember struct {
	AgentID     string `json:"agentId"`
	Name        string `json:"name"`
	AgentType   string `json:"agentType"`
	TmuxPaneID  string `json:"tmuxPaneId"`
	CWD         string `json:"cwd"`
	BackendType string `json:"backendType"`
}

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		args = []string{"ls"}
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "ls":
		cmdLs(rest)
	case "v":
		cmdViewer(rest)
	case "o":
		cmdOpen(rest)
	case "b":
		cmdBranch(rest)
	case "s":
		cmdSend(rest)
	case "r":
		cmdRead(rest)
	case "k":
		cmdKill(rest)
	case "n":
		cmdNewShell(rest)
	case "c":
		cmdNewClaude(rest)
	case "_complete":
		cmdComplete()
	case "-h", "--help", "help":
		usage()
	default:
		die("unknown command: %s (try 'k help')", cmd)
	}
}

func usage() {
	fmt.Println(`kraken - live Claude and Codex sessions across tmux

Commands:
  k ls [--json] [-g <mode>]
			list live Claude and Codex sessions plus agent-free
			shell panes, one numbering across all rows; -g groups the tables:
			type = Claude + Codex + shells (default), session = one table per tmux
                      session with window numbers, none = everything in one table
  k v [-g <mode>]     live viewer TUI: vim keys, enter/click to open, b to branch,
                      dd to kill, t to cycle grouping
  k o <sel>           jump to an agent or shell pane: focus its ghostty window + pane
  k b [sel]           branch (fork) a session into a vertical split (default: current pane)
  k s <sel> <msg...>  send a message into an agent's prompt (stdin if no msg args)
  k r <sel> [n]       read back the last n message blocks (user/thinking/text; default 1; sel '.' = current pane)
  k k <sel>           kill an agent or shell pane (refuses the current pane and nested agents)
  k n [-c dir] [cmd...]
                      new shell in a new window of the current session, optionally
                      running cmd (drops back to the shell when cmd exits)
  k c [-c dir] <name> [prompt...]
                      new named claude in a new window of the current session

<sel>: index from 'k ls', '.' for the current pane, an exact pane id (%3), or
substring match on name/project/agent/tmux-session; also a session-uuid
prefix (>=4 chars).`)
}

func home() string {
	h, err := os.UserHomeDir()
	if err != nil {
		die("home dir: %v", err)
	}
	return h
}

func die(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "kraken: "+format+"\n", a...)
	os.Exit(1)
}

// --- discovery ---

type tmuxPane struct {
	ID        string
	PID       int
	Session   string
	WindowIdx int
	Path      string
}

func listTmuxPanes() []tmuxPane {
	// path and session are the only fields that could contain a tab; parsing
	// takes session from the end and rejoins the middle as the path, so a tab
	// in the path can't corrupt the session (which k o/k s target)
	out, err := exec.Command("tmux", "list-panes", "-a", "-F",
		"#{pane_id}\t#{pane_pid}\t#{window_index}\t#{pane_current_path}\t#{session_name}").Output()
	if err != nil {
		return nil
	}
	var panes []tmuxPane
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, "\t")
		if len(f) < 5 {
			continue
		}
		pid, _ := strconv.Atoi(f[1])
		if pid <= 0 {
			continue // a bad pid would walk children[0] = the whole process table
		}
		widx, _ := strconv.Atoi(f[2])
		panes = append(panes, tmuxPane{
			ID:        f[0],
			PID:       pid,
			Session:   f[len(f)-1],
			WindowIdx: widx,
			Path:      strings.Join(f[3:len(f)-1], "\t"),
		})
	}
	return panes
}

func psSnapshot() (children map[int][]int, cmds map[int]string, etimes map[int]time.Duration) {
	children = map[int][]int{}
	cmds = map[int]string{}
	etimes = map[int]time.Duration{}
	out, err := exec.Command("ps", "-eo", "pid,ppid,etime,command").Output()
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		children[ppid] = append(children[ppid], pid)
		etimes[pid] = parseEtime(f[2])
		cmds[pid] = strings.Join(f[3:], " ")
	}
	return
}

// parseEtime converts ps's elapsed-time format ([[dd-]hh:]mm:ss) to a Duration.
func parseEtime(s string) time.Duration {
	days := 0
	if i := strings.IndexByte(s, '-'); i >= 0 {
		days, _ = strconv.Atoi(s[:i])
		s = s[i+1:]
	}
	var h, m, sec int
	parts := strings.Split(s, ":")
	switch len(parts) {
	case 3:
		h, _ = strconv.Atoi(parts[0])
		m, _ = strconv.Atoi(parts[1])
		sec, _ = strconv.Atoi(parts[2])
	case 2:
		m, _ = strconv.Atoi(parts[0])
		sec, _ = strconv.Atoi(parts[1])
	default:
		return 0
	}
	return time.Duration(days)*24*time.Hour + time.Duration(h)*time.Hour +
		time.Duration(m)*time.Minute + time.Duration(sec)*time.Second
}

func isClaudeCmd(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return false
	}
	if filepath.Base(f[0]) == "claude" {
		return true
	}
	// teammates run the versioned binary directly, so the basename is the
	// version number: ~/.local/share/claude/versions/2.1.205 --agent-id ...
	return strings.Contains(f[0], "/claude/versions/")
}

func isCodexCmd(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return false
	}
	// The npm launcher is a node process above the native binary. Matching
	// only an executable actually named codex avoids reporting both, and also
	// excludes helpers such as codex-code-mode-host.
	return filepath.Base(f[0]) == "codex"
}

type agentPID struct {
	pid, depth int
	agent      string
}

// agentPIDsUnder returns all interactive agents below a pane, deepest first.
// The first hit is the process attached to the foreground TUI when agents are
// nested through one another's shells.
func agentPIDsUnder(root int, children map[int][]int, cmds map[int]string) []agentPID {
	var hits []agentPID
	var walk func(int, int)
	walk = func(pid, depth int) {
		agent := ""
		switch {
		case isClaudeCmd(cmds[pid]):
			agent = "claude"
		case isCodexCmd(cmds[pid]):
			agent = "codex"
		}
		if agent != "" {
			hits = append(hits, agentPID{pid: pid, depth: depth, agent: agent})
		}
		for _, child := range children[pid] {
			walk(child, depth+1)
		}
	}
	walk(root, 0)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].depth > hits[j].depth })
	return hits
}

type agentFlags struct {
	agentName       string
	teamName        string
	parentSessionID string
	model           string
}

// parseAgentFlags extracts teammate identity from a claude command line —
// teammates carry --agent-name/--team-name/--parent-session-id flags and
// write no ~/.claude/sessions file of their own.
func parseAgentFlags(cmd string) agentFlags {
	f := strings.Fields(cmd)
	var out agentFlags
	for i := 0; i+1 < len(f); i++ {
		switch f[i] {
		case "--agent-name":
			out.agentName = f[i+1]
		case "--team-name":
			out.teamName = f[i+1]
		case "--parent-session-id":
			out.parentSessionID = f[i+1]
		case "--model":
			out.model = f[i+1]
		}
	}
	return out
}

// claudePIDsUnder walks the process tree below root and returns claude PIDs,
// deepest-first (a claude spawned inside a claude's shell sorts before its host).
func claudePIDsUnder(root int, children map[int][]int, cmds map[int]string) []int {
	var pids []int
	for _, h := range agentPIDsUnder(root, children, cmds) {
		if h.agent == "claude" {
			pids = append(pids, h.pid)
		}
	}
	return pids
}

type codexSession struct {
	ID, CWD, Name, Model, Path string
	UpdatedAt                  int64
}

type codexSessionEntry struct {
	session codexSession
	cmd     string
	size    int64
}

var codexSessionCache = struct {
	sync.Mutex
	m map[int]codexSessionEntry
}{m: map[int]codexSessionEntry{}}

// readCodexSession identifies the exact rollout held open by a live Codex
// process. This remains unambiguous when several sessions share a cwd.
func readCodexSession(pid int, cmd string) (codexSession, bool) {
	var out codexSession
	codexSessionCache.Lock()
	cached, cachedOK := codexSessionCache.m[pid]
	codexSessionCache.Unlock()
	if cachedOK && cached.cmd == cmd {
		if info, err := os.Stat(cached.session.Path); err == nil {
			out = cached.session
			out.UpdatedAt = info.ModTime().UnixMilli()
			if info.Size() != cached.size {
				if model := readCodexModelTail(out.Path); model != "" {
					out.Model = model
				}
				codexSessionCache.Lock()
				codexSessionCache.m[pid] = codexSessionEntry{session: out, cmd: cmd, size: info.Size()}
				codexSessionCache.Unlock()
			}
			return out, true
		}
	}
	data, err := exec.Command("lsof", "-p", strconv.Itoa(pid), "-Fn").Output()
	if err != nil {
		return out, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		path := strings.TrimPrefix(line, "n")
		if strings.HasPrefix(line, "n") && strings.Contains(path, "/.codex/sessions/") &&
			strings.HasSuffix(path, ".jsonl") {
			out.Path = path
			break
		}
	}
	if out.Path == "" {
		return out, false
	}
	f, err := os.Open(out.Path)
	if err != nil {
		return out, false
	}
	defer f.Close()
	data, err = io.ReadAll(f)
	if err != nil {
		return out, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if out.ID == "" && strings.Contains(line, `"type":"session_meta"`) {
			var rec struct {
				Payload struct {
					ID  string `json:"id"`
					CWD string `json:"cwd"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &rec) == nil {
				out.ID, out.CWD = rec.Payload.ID, rec.Payload.CWD
			}
		}
		if strings.Contains(line, `"type":"user_message"`) && out.Name == "" {
			var rec struct {
				Payload struct {
					Type    string `json:"type"`
					Message string `json:"message"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Payload.Type == "user_message" {
				out.Name = truncateOneLine(rec.Payload.Message, 48)
			}
		}
		if strings.Contains(line, `"type":"turn_context"`) {
			var rec struct {
				Payload struct {
					Model string `json:"model"`
				} `json:"payload"`
			}
			if json.Unmarshal([]byte(line), &rec) == nil && rec.Payload.Model != "" {
				out.Model = rec.Payload.Model
			}
		}
	}
	var size int64
	if info, err := os.Stat(out.Path); err == nil {
		out.UpdatedAt = info.ModTime().UnixMilli()
		size = info.Size()
	}
	codexSessionCache.Lock()
	codexSessionCache.m[pid] = codexSessionEntry{session: out, cmd: cmd, size: size}
	codexSessionCache.Unlock()
	return out, out.ID != ""
}

func readCodexModelTail(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	const window = 128 * 1024
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	model := ""
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"type":"turn_context"`) {
			continue
		}
		var rec struct {
			Payload struct {
				Model string `json:"model"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Payload.Model != "" {
			model = rec.Payload.Model
		}
	}
	return model
}

// readCodexNames returns Codex's user-visible thread titles. The index is
// append-only and can contain several entries for a thread as its title is
// generated or renamed, so the last valid record wins.
func readCodexNames() map[string]string {
	names := map[string]string{}
	data, err := os.ReadFile(filepath.Join(home(), ".codex", "session_index.jsonl"))
	if err != nil {
		return names
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"thread_name"`) {
			continue
		}
		var rec struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.ID != "" && rec.Name != "" {
			names[rec.ID] = rec.Name
		}
	}
	return names
}

func readSessionFile(pid int) (sessionFile, bool) {
	var sf sessionFile
	data, err := os.ReadFile(filepath.Join(home(), ".claude", "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return sf, false
	}
	if err := json.Unmarshal(data, &sf); err != nil {
		return sf, false
	}
	return sf, true
}

// isShellProc reports whether a ps command line is an interactive shell
// (including login shells like "-zsh") — a pane showing one with no non-shell
// descendants is idle.
func isShellProc(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return true // process gone mid-snapshot: read as idle, don't invent a command
	}
	if strings.HasPrefix(f[0], "(") {
		return true // zombie, ps shows "(name)": not a running command
	}
	switch strings.TrimPrefix(filepath.Base(f[0]), "-") {
	case "zsh", "bash", "sh", "fish", "dash", "tcsh", "csh", "ksh", "login":
		return true
	}
	return false
}

// paneCommand finds what a shell pane is running: descend through shells and
// take the shallowest non-shell processes (the commands actually typed),
// preferring the newest — background jobs predate the foreground one. kraken
// deliberately reports itself (a pane running k v shows "k v", not a blank
// idle shell).
func paneCommand(root int, children map[int][]int, cmds map[int]string, etimes map[int]time.Duration) (string, time.Duration) {
	if cmd := cmds[root]; !isShellProc(cmd) {
		return cmd, etimes[root] // pane launched with a command, no shell
	}
	var candidates []int
	var walk func(pid int)
	walk = func(pid int) {
		for _, c := range children[pid] {
			if isShellProc(cmds[c]) {
				walk(c)
			} else {
				candidates = append(candidates, c)
			}
		}
	}
	walk(root)
	if len(candidates) == 0 {
		return "", 0
	}
	best := candidates[0]
	for _, c := range candidates[1:] {
		if etimes[c] < etimes[best] {
			best = c
		}
	}
	return cmds[best], etimes[best]
}

// discoverAll is the shared entrypoint: every command works off its output.
// Read-only; kraken keeps no state of its own. Panes with no agent in them
// come back as shell Peers (Role "shell") so selection, k o, k k work on them.
// Both lists are grouped by tmux session, then window.
func discoverAll() ([]*Peer, []*Peer) {
	panes := listTmuxPanes()
	children, cmds, etimes := psSnapshot()
	codexNames := readCodexNames()
	pruneCWDCache(cmds)
	selfPane := os.Getenv("TMUX_PANE")
	// selfAncestors disambiguates panes holding nested agents: only the agent
	// this kraken invocation actually runs under gets the "me" mark
	parents := map[int]int{}
	for pp, cs := range children {
		for _, c := range cs {
			parents[c] = pp
		}
	}
	selfAncestors := map[int]bool{}
	for pid := os.Getpid(); pid > 1; pid = parents[pid] {
		selfAncestors[pid] = true
	}
	var peers, shells []*Peer
	for _, p := range panes {
		agents := agentPIDsUnder(p.PID, children, cmds)
		if len(agents) == 0 {
			cmd, elapsed := paneCommand(p.PID, children, cmds, etimes)
			sp := &Peer{
				Pane:        p.ID,
				TmuxSession: p.Session,
				WindowIdx:   p.WindowIdx,
				PID:         p.PID,
				CWD:         p.Path,
				Role:        "shell",
				Status:      "idle",
				Cmd:         cmd,
				CmdSeconds:  int64(elapsed / time.Second),
				Self:        selfPane != "" && p.ID == selfPane,
			}
			if cmd != "" {
				sp.Status = "running"
			}
			sp.Name = projectLabel(sp)
			sp.Branch = gitBranch(sp.CWD)
			shells = append(shells, sp)
			continue
		}
		// i is the position in the deepest-first list, not tree depth: 0 is
		// the foreground agent
		for i, hit := range agents {
			pid := hit.pid
			peer := &Peer{
				Pane:        p.ID,
				TmuxSession: p.Session,
				WindowIdx:   p.WindowIdx,
				Depth:       i,
				PID:         pid,
				Status:      "unknown",
				Role:        "main",
				Agent:       hit.agent,
				Messageable: true,
				Self:        selfPane != "" && p.ID == selfPane && selfAncestors[pid],
			}
			if hit.agent == "codex" {
				if cs, ok := readCodexSession(pid, cmds[pid]); ok {
					peer.SessionID = cs.ID
					peer.CWD = cs.CWD
					peer.Name = codexNames[cs.ID]
					if peer.Name == "" {
						peer.Name = cs.Name
					}
					peer.Model = cs.Model
					peer.StatusAt = cs.UpdatedAt
				} else {
					peer.CWD = procCWD(pid, cmds[pid])
				}
			} else if sf, ok := readSessionFile(pid); ok {
				peer.SessionID = sf.SessionID
				// names render raw into table lines: control chars or a
				// newline would break the one-row-per-line layout
				peer.Name = truncateOneLine(sf.Name, 80)
				peer.CWD = sf.CWD
				peer.Kind = sf.Kind
				peer.StatusAt = sf.StatusUpdatedAt
				switch sf.Status {
				case "busy":
					peer.Status = "running"
				case "idle":
					peer.Status = "waiting"
				case "":
					// keep "unknown"
				default:
					// future/unmapped values (e.g. "shell" = user at the !
					// prompt) surface verbatim rather than being hidden
					peer.Status = sf.Status
				}
			} else if flags := parseAgentFlags(cmds[pid]); flags.agentName != "" {
				// teammate: identity from its command-line flags; it has no
				// session file and its own transcript isn't locatable, so
				// SessionID stays empty (k r / k b refuse cleanly)
				peer.Role = "teammate"
				peer.AgentName = flags.agentName
				peer.Team = flags.teamName
				peer.ParentSID = flags.parentSessionID
				peer.Model = shortModel(flags.model)
				peer.CWD = procCWD(pid, cmds[pid])
			} else {
				// No session file (startup race, old CLI): best-effort identity
				// from the process cwd and its newest transcript.
				if cwd := procCWD(pid, cmds[pid]); cwd != "" {
					peer.CWD = cwd
					peer.SessionID = newestTranscriptSession(cwd)
					// another claude in the same cwd may own that transcript
					peer.SessionIDGuessed = peer.SessionID != ""
				}
			}
			peer.Branch = gitBranch(peer.CWD)
			if peer.Model == "" && peer.SessionID != "" && !peer.SessionIDGuessed {
				peer.Model = readModel(transcriptPath(peer.SessionID))
			}
			refineStatus(peer)
			peers = append(peers, peer)
		}
	}
	sortBySessionWindow(peers)
	sortBySessionWindow(shells)
	nestTeams(peers)
	return peers, shells
}

// sortBySessionWindow groups panes by tmux session, then window. Stable, so
// same-pane deepest-first claude order survives (nestTeams keys off it).
func sortBySessionWindow(list []*Peer) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].TmuxSession != list[j].TmuxSession {
			return sessionLess(list[i].TmuxSession, list[j].TmuxSession)
		}
		return list[i].WindowIdx < list[j].WindowIdx
	})
}

// sessionLess orders session names numerically first (default tmux names are
// "0", "1", ..., where "10" must sort after "9"), then everything else by
// string. Numbers-before-names keeps the ordering total — comparing a number
// with a name by string would be intransitive ("9"<"10" numeric but
// "10"<"5a"<"9" by string), and sort's output is unspecified on such cycles.
func sessionLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	switch {
	case aerr == nil && berr == nil:
		if ai != bi {
			return ai < bi
		}
		return a < b // "01" vs "1"
	case aerr == nil:
		return true
	case berr == nil:
		return false
	default:
		return a < b
	}
}

// gitBranch resolves the checked-out branch by reading .git/HEAD directly (no
// git exec per peer), walking up from cwd and following worktree pointers.
func gitBranch(cwd string) string {
	for dir := cwd; dir != "" && dir != "/"; dir = filepath.Dir(dir) {
		gitPath := filepath.Join(dir, ".git")
		fi, err := os.Stat(gitPath)
		if err != nil {
			continue
		}
		if !fi.IsDir() {
			// worktree: .git is a file containing "gitdir: <path>"
			data, err := os.ReadFile(gitPath)
			if err != nil {
				return ""
			}
			line := strings.TrimSpace(string(data))
			if !strings.HasPrefix(line, "gitdir:") {
				return ""
			}
			gitPath = strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		}
		head, err := os.ReadFile(filepath.Join(gitPath, "HEAD"))
		if err != nil {
			return ""
		}
		h := strings.TrimSpace(string(head))
		if ref, ok := strings.CutPrefix(h, "ref: refs/heads/"); ok {
			return ref
		}
		if len(h) >= 8 {
			return h[:8] // detached HEAD
		}
		return ""
	}
	return ""
}

// readModel finds the model of the transcript's last assistant record.
func readModel(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	const window = 64 * 1024
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	model := ""
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"model"`) {
			continue
		}
		var rec struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
			} `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Type == "assistant" && rec.Message.Model != "" {
			model = rec.Message.Model
		}
	}
	return shortModel(model)
}

// shortModel compacts a model id for display: claude-fable-5 → fable-5.
func shortModel(id string) string {
	return strings.TrimPrefix(id, "claude-")
}

// cwdCache memoizes procCWD per PID: lsof is by far the slowest discovery
// step (~50-100ms), and the viewer re-runs discovery every second. Entries
// carry the process's command line so a recycled PID (or an exec into a
// different process) can't serve another process's cwd — see pruneCWDCache.
type cwdEntry struct {
	cwd, cmd string
}

var cwdCache = struct {
	sync.Mutex
	m map[int]cwdEntry
}{m: map[int]cwdEntry{}}

func procCWD(pid int, cmd string) string {
	cwdCache.Lock()
	cached, ok := cwdCache.m[pid]
	cwdCache.Unlock()
	if ok && cached.cmd == cmd {
		return cached.cwd
	}
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			cwd := line[1:]
			cwdCache.Lock()
			cwdCache.m[pid] = cwdEntry{cwd: cwd, cmd: cmd}
			cwdCache.Unlock()
			return cwd
		}
	}
	return ""
}

// pruneCWDCache drops per-process cache entries whose PID is gone or now runs
// a different command (PID recycled between ticks).
func pruneCWDCache(cmds map[int]string) {
	cwdCache.Lock()
	defer cwdCache.Unlock()
	for pid, e := range cwdCache.m {
		if cmds[pid] != e.cmd {
			delete(cwdCache.m, pid)
		}
	}
	codexSessionCache.Lock()
	defer codexSessionCache.Unlock()
	for pid, e := range codexSessionCache.m {
		if cmds[pid] != e.cmd {
			delete(codexSessionCache.m, pid)
		}
	}
}

// projectDirName encodes a cwd the way Claude Code names transcript dirs
// under ~/.claude/projects (every non-alphanumeric rune becomes '-').
func projectDirName(cwd string) string {
	var b strings.Builder
	for _, r := range cwd {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return b.String()
}

func newestTranscriptSession(cwd string) string {
	dir := filepath.Join(home(), ".claude", "projects", projectDirName(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	newest, best := "", time.Time{}
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(best) {
			best = info.ModTime()
			newest = strings.TrimSuffix(e.Name(), ".jsonl")
		}
	}
	return newest
}

// transcriptPath locates a session's transcript by globbing all project dirs —
// more robust than re-deriving the cwd encoding.
func transcriptPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(home(), ".claude", "projects", "*", sessionID+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	if len(matches) == 1 {
		return matches[0]
	}
	newest, best := "", time.Time{}
	for _, m := range matches {
		info, err := os.Stat(m)
		if err != nil {
			continue
		}
		if info.ModTime().After(best) {
			best = info.ModTime()
			newest = m
		}
	}
	return newest
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
	// shell mode wins: never let a question marker in the visible scrollback
	// hide that Enter would execute the sent text as a shell command
	if p.Status == "shell" {
		return
	}
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", p.Pane).Output()
	if err != nil {
		return
	}
	text := string(out)
	if containsQuestionMarker(text) {
		p.Status = "question"
		return
	}
	if p.Status == "unknown" {
		// no session file (teammates): the TUI shows "esc to interrupt" for
		// the whole turn, so its absence means the claude is at its prompt
		if strings.Contains(text, "esc to interrupt") {
			p.Status = "running"
		} else {
			p.Status = "waiting"
		}
	}
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

func paneShowsQuestion(pane string) bool {
	out, err := exec.Command("tmux", "capture-pane", "-p", "-t", pane).Output()
	return err == nil && containsQuestionMarker(string(out))
}

// --- team nesting ---

func nestTeams(peers []*Peer) {
	configs, _ := filepath.Glob(filepath.Join(home(), ".claude", "teams", "*", "config.json"))
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

// orderPeers flattens peers into display order (teammates right after their
// lead) and assigns 1-based indexes. Only this order is a valid selector target.
func orderPeers(peers []*Peer) []*Peer {
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

// orderAll orders claudes and appends shell panes with continued numbering —
// one selector space across both tables. Callers rendering the tables
// separately split the result on nPeers.
func orderAll(peers, shells []*Peer) (all []*Peer, nPeers int) {
	ordered := orderPeers(peers)
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

// --- usage limits header ---

// usageCache mirrors ~/.claude/.usage-cache.json, maintained by the user's
// statusline refresh script (usage-fetch.sh) — kraken only reads it.
type usageCache struct {
	FiveHour usageWindow  `json:"five_hour"`
	SevenDay usageWindow  `json:"seven_day"`
	Limits   []usageLimit `json:"limits"`
}

type usageWindow struct {
	Utilization float64 `json:"utilization"`
	ResetsAt    string  `json:"resets_at"`
}

type usageLimit struct {
	Kind     string  `json:"kind"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at"`
	Scope    struct {
		Model struct {
			DisplayName string `json:"display_name"`
		} `json:"model"`
	} `json:"scope"`
}

type codexRateWindow struct {
	UsedPercent   float64 `json:"used_percent"`
	WindowMinutes int     `json:"window_minutes"`
	ResetsAt      int64   `json:"resets_at"`
}

type codexRateLimits struct {
	Primary   codexRateWindow `json:"primary"`
	Secondary codexRateWindow `json:"secondary"`
}

func usageColor(pct int) string {
	switch {
	case pct >= 80:
		return red
	case pct >= 50:
		return yellow
	default:
		return green
	}
}

// resetIn renders time until an ISO8601 reset as "3h12m" / "4d" / "8m".
func resetIn(iso string) string {
	if iso == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, iso); err != nil {
			return ""
		}
	}
	d := time.Until(t)
	switch {
	case d <= 0:
		return "now"
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	}
}

func resetInUnix(epoch int64) string {
	if epoch <= 0 {
		return ""
	}
	return resetDuration(time.Until(time.Unix(epoch, 0)))
}

func resetDuration(d time.Duration) string {
	switch {
	case d <= 0:
		return "now"
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dm", int(d.Minutes())+1)
	}
}

func readCodexRateLimits(path string) (codexRateLimits, bool) {
	var limits codexRateLimits
	f, err := os.Open(path)
	if err != nil {
		return limits, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return limits, false
	}
	const window = 256 * 1024
	start := info.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return limits, false
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return limits, false
	}
	found := false
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"rate_limits"`) || !strings.Contains(line, `"token_count"`) {
			continue
		}
		var rec struct {
			Payload struct {
				Type       string          `json:"type"`
				RateLimits codexRateLimits `json:"rate_limits"`
			} `json:"payload"`
		}
		if json.Unmarshal([]byte(line), &rec) == nil && rec.Payload.Type == "token_count" {
			limits, found = rec.Payload.RateLimits, true
		}
	}
	return limits, found
}

func codexUsageLine(color bool, seg func(string, float64, string) string) string {
	codexSessionCache.Lock()
	var newest codexSession
	for _, entry := range codexSessionCache.m {
		if entry.session.UpdatedAt > newest.UpdatedAt {
			newest = entry.session
		}
	}
	codexSessionCache.Unlock()
	if newest.Path == "" {
		return ""
	}
	limits, ok := readCodexRateLimits(newest.Path)
	if !ok {
		return ""
	}
	parts := []string{
		seg("5h", limits.Primary.UsedPercent, resetInUnix(limits.Primary.ResetsAt)),
		seg("7d", limits.Secondary.UsedPercent, resetInUnix(limits.Secondary.ResetsAt)),
	}
	lines := make([]string, len(parts))
	for i, part := range parts {
		prefix := "        "
		if i == 0 {
			prefix = paint(color, cyan, "codex") + "   "
		}
		lines[i] = prefix + part
	}
	return strings.Join(lines, "\n")
}

func usageHeader(color bool) string {
	path := filepath.Join(home(), ".claude", ".usage-cache.json")
	data, err := os.ReadFile(path)
	var uc usageCache
	claudeOK := err == nil && json.Unmarshal(data, &uc) == nil
	seg := func(label string, pct float64, reset string) string {
		p := int(pct)
		s := paint(color, dim, label+" ") + paint(color, usageColor(p), fmt.Sprintf("%d%%", p))
		if reset != "" {
			s += paint(color, dim, " · "+reset)
		}
		return s
	}
	var lines []string
	if claudeOK {
		segs := []string{
			seg("5h", uc.FiveHour.Utilization, resetIn(uc.FiveHour.ResetsAt)),
			seg("7d", uc.SevenDay.Utilization, resetIn(uc.SevenDay.ResetsAt)),
		}
		for _, l := range uc.Limits {
			if name := l.Scope.Model.DisplayName; name != "" {
				segs = append(segs, seg(strings.ToLower(name), l.Percent, resetIn(l.ResetsAt)))
			}
		}
		claudeLines := make([]string, len(segs))
		for i, part := range segs {
			prefix := "        "
			if i == 0 {
				prefix = paint(color, cyan, "claude") + "  "
			}
			claudeLines[i] = prefix + part
		}
		if info, err := os.Stat(path); err == nil {
			if stale := time.Since(info.ModTime()); stale > 10*time.Minute {
				claudeLines[0] += paint(color, dim, fmt.Sprintf("   (stale %s)", age(stale)))
			}
		}
		lines = append(lines, claudeLines...)
	}
	if line := codexUsageLine(color, seg); line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

// usageForAgent extracts one agent's limit rows from the combined usage
// snapshot and removes the repeated agent label; the surrounding table title
// already supplies that context.
func usageForAgent(usage, agent string, color bool) []string {
	if usage == "" || (agent != "claude" && agent != "codex") {
		return nil
	}
	marker := paint(color, cyan, agent)
	var rows []string
	inBlock := false
	for _, line := range strings.Split(usage, "\n") {
		plain := ansiRe.ReplaceAllString(line, "")
		if strings.HasPrefix(plain, agent+strings.Repeat(" ", 8-len(agent))) {
			inBlock = true
			line = strings.TrimPrefix(line, marker)
			line = strings.TrimPrefix(line, agent)
			line = strings.TrimPrefix(line, strings.Repeat(" ", 8-len(agent)))
			rows = append(rows, "  "+line)
			continue
		}
		if len(plain) >= 8 && strings.TrimSpace(plain[:8]) == "" && inBlock {
			rows = append(rows, "  "+strings.TrimPrefix(line, "        "))
			continue
		}
		if inBlock {
			break
		}
	}
	return rows
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

// --- k n / k c ---

// splitDirFlag peels an optional "-c <dir>" off the front of args, returning
// the start directory (caller's cwd by default) and the remaining args.
func splitDirFlag(args []string) (string, []string) {
	if len(args) >= 1 && args[0] == "-c" {
		if len(args) < 2 {
			die("-c needs a directory")
		}
		return args[1], args[2:]
	}
	dir, err := os.Getwd()
	if err != nil {
		dir = home()
	}
	return dir, args
}

// newWindow opens a new window in the caller's tmux session running argv (an
// interactive shell when empty) and reports where it landed.
func newWindow(dir string, argv []string) string {
	if os.Getenv("TMUX") == "" {
		die("must run inside tmux")
	}
	// tmux silently falls back to $HOME on a bad -c dir; a typo should fail
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		die("not a directory: %s", dir)
	}
	tmuxArgs := []string{"new-window", "-P", "-F", "#{session_name}:#{window_index} (#{pane_id})", "-c", dir}
	tmuxArgs = append(tmuxArgs, argv...)
	out, err := exec.Command("tmux", tmuxArgs...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			die("tmux new-window: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		die("tmux new-window: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func userShell() string {
	if sh := os.Getenv("SHELL"); sh != "" {
		return sh
	}
	return "/bin/zsh"
}

func cmdNewShell(args []string) {
	dir, rest := splitDirFlag(args)
	var argv []string
	if len(rest) > 0 {
		// run the command, then drop into an interactive shell so the pane
		// (and the command's output) survives the command exiting; exec goes
		// on its own line so a trailing comment, ';' or '&' can't eat it
		sh := userShell()
		argv = []string{sh, "-c", strings.Join(rest, " ") + "\nexec " + sh}
	}
	at := newWindow(dir, argv)
	if len(rest) > 0 {
		fmt.Printf("running %q in new window %s\n", strings.Join(rest, " "), at)
	} else {
		fmt.Printf("opened shell in new window %s\n", at)
	}
}

func cmdNewClaude(args []string) {
	dir, rest := splitDirFlag(args)
	if len(rest) == 0 {
		die("usage: k c [-c dir] <name> [prompt...]")
	}
	name := rest[0]
	argv := []string{claudeLauncher(), "--name", name}
	if prompt := strings.Join(rest[1:], " "); prompt != "" {
		// "--" so a prompt starting with "-" can't be read as a claude flag
		argv = append(argv, "--", prompt)
	}
	at := newWindow(dir, argv)
	fmt.Printf("started claude %q in new window %s\n", name, at)
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

// --- selection ---

func tokenize(s string) []string {
	var tokens []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, strings.ToLower(cur.String()))
			cur.Reset()
		}
	}
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			cur.WriteRune(r)
		} else {
			flush()
		}
	}
	flush()
	return tokens
}

func peerHaystack(p *Peer) string {
	return strings.ToLower(strings.Join([]string{
		p.Name, p.AgentName, projectLabel(p), p.TmuxSession, p.Pane, p.Team,
	}, " "))
}

func currentPanePeer(ordered []*Peer) *Peer {
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return nil
	}
	for _, p := range ordered {
		if p.Pane == pane {
			return p
		}
	}
	return nil
}

func selectPeer(sel string, ordered []*Peer) *Peer {
	if sel == "" {
		die("selector required")
	}
	if len(ordered) == 0 {
		die("no live sessions")
	}
	if sel == "." {
		if p := currentPanePeer(ordered); p != nil {
			return p
		}
		die("no session found in the current pane (is $TMUX_PANE set?)")
	}
	// exact pane id (%3): the one churn-proof selector — indexes shift as
	// sessions come and go, pane ids never do
	if strings.HasPrefix(sel, "%") {
		for _, p := range ordered {
			if p.Pane == sel {
				return p
			}
		}
		die("no session in pane %s", sel)
	}
	if n, err := strconv.Atoi(sel); err == nil && n >= 1 && n <= len(ordered) {
		return ordered[n-1]
		// out-of-range numbers fall through to name matching: tmux sessions
		// are often literally named "0", "1", ...
	}
	if len(sel) >= 4 {
		var matches []*Peer
		for _, p := range ordered {
			if p.SessionID != "" && strings.HasPrefix(p.SessionID, strings.ToLower(sel)) {
				matches = append(matches, p)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
	}
	tokens := tokenize(sel)
	if len(tokens) == 0 {
		die("no session matches %q", sel)
	}
	var matches []*Peer
	for _, p := range ordered {
		hay := peerHaystack(p)
		ok := true
		for _, t := range tokens {
			if !strings.Contains(hay, t) {
				ok = false
				break
			}
		}
		if ok {
			matches = append(matches, p)
		}
	}
	switch len(matches) {
	case 0:
		die("no session matches %q", sel)
	case 1:
		return matches[0]
	}
	var names []string
	for _, m := range matches {
		names = append(names, fmt.Sprintf("%d:%s", m.Index, displayName(m)))
	}
	die("%q matches %d sessions: %s", sel, len(matches), strings.Join(names, ", "))
	return nil
}

// --- k o ---

func cmdOpen(args []string) {
	if len(args) == 0 {
		die("usage: k o <sel>")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(strings.Join(args, " "), all)
	msg, err := openPeer(p)
	if err != nil {
		die("%v", err)
	}
	fmt.Println(msg)
}

// raiseOutcome reports how raiseSession made a session visible: a fresh
// Ghostty window was attached, or an existing one was focused (focusErr set
// when the raise failed and the user must switch windows manually).
type raiseOutcome struct {
	attached bool
	focusErr error
}

// raiseSession brings a tmux session's Ghostty window to the front, attaching
// it in a new Ghostty window if no client is attached.
func raiseSession(session string) (raiseOutcome, error) {
	// the caller may hold a snapshot up to a second old — without this check
	// a dead session would pop an empty Ghostty window that fails to attach
	if err := exec.Command("tmux", "has-session", "-t", session).Run(); err != nil {
		return raiseOutcome{}, fmt.Errorf("tmux session %s is gone", session)
	}
	out, err := exec.Command("tmux", "list-clients", "-t", session, "-F", "#{client_tty}").Output()
	attached := err == nil && strings.TrimSpace(string(out)) != ""
	if !attached {
		if err := exec.Command("open", "-na", "Ghostty.app", "--args",
			"-e", "tmux", "attach-session", "-t", session).Run(); err != nil {
			return raiseOutcome{}, fmt.Errorf("open Ghostty: %v", err)
		}
		return raiseOutcome{attached: true}, nil
	}
	return raiseOutcome{focusErr: focusGhosttyWindow(session)}, nil
}

// openPeer jumps to a claude: selects its tmux window/pane and raises its
// Ghostty window. Returns a human-readable note on success.
func openPeer(p *Peer) (string, error) {
	if p.Pane == "" {
		return "", fmt.Errorf("%s has no pane (in-process teammate)", displayName(p))
	}
	// point the session at the target pane first: attached clients follow
	// immediately, and a fresh attach lands directly on it
	_ = exec.Command("tmux", "select-window", "-t", p.Pane).Run()
	_ = exec.Command("tmux", "select-pane", "-t", p.Pane).Run()
	out, err := raiseSession(p.TmuxSession)
	if err != nil {
		return "", err
	}
	switch {
	case out.attached:
		return fmt.Sprintf("attached %s in a new ghostty window → %s (%s)", p.TmuxSession, displayName(p), p.Pane), nil
	case out.focusErr != nil:
		return fmt.Sprintf("selected %s (%s) — focus its window manually: %v", displayName(p), p.Pane, out.focusErr), nil
	default:
		return fmt.Sprintf("focused %s (%s, pane %s)", displayName(p), p.TmuxSession, p.Pane), nil
	}
}

// focusGhosttyWindow raises the Ghostty window whose title starts with
// "<session>:" — requires tmux set-titles on (title-string leads with #S) and
// Accessibility access for the caller.
func focusGhosttyWindow(session string) error {
	// iterate every Ghostty process: windows kraken opens via `open -na`
	// belong to a second app instance
	script := fmt.Sprintf(`with timeout of 8 seconds
tell application "System Events"
	repeat with proc in (every process whose name is "Ghostty")
		tell proc
			repeat with w in windows
				if title of w starts with %q then
					set frontmost to true
					perform action "AXRaise" of w
					return "ok"
				end if
			end repeat
		end tell
	end repeat
end tell
end timeout
return "notfound"`, session+":")
	out, err := exec.Command("osascript", "-e", script).Output()
	if err != nil {
		return fmt.Errorf("osascript (accessibility granted?): %v", err)
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("no ghostty window titled %q (is 'set-titles on' active?)", session+":")
	}
	return nil
}

// --- k s ---

func cmdSend(args []string) {
	if len(args) == 0 {
		die("usage: k s <sel> <message...>")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(args[0], all)
	if p.Role == "shell" {
		die("%s is a shell pane — the text would run as a shell command", displayName(p))
	}
	msg := strings.Join(args[1:], " ")
	if msg == "" {
		if fi, err := os.Stdin.Stat(); err == nil && fi.Mode()&os.ModeCharDevice != 0 {
			die("no message given (pass it as args or pipe it on stdin)")
		}
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			die("read stdin: %v", err)
		}
		msg = strings.TrimRight(string(data), "\n")
	}
	if strings.TrimSpace(msg) == "" {
		die("empty message")
	}
	if !p.Messageable || p.Pane == "" {
		die("%s is an in-process teammate — message its lead instead", displayName(p))
	}
	if p.Depth > 0 {
		die("%s is not the foreground agent in pane %s — keys would land in the nested one", displayName(p), p.Pane)
	}
	verifySendTarget(p)
	if p.Status == "shell" {
		// text typed at the ! prompt executes as a shell command on Enter
		die("%s is in shell mode — sending now would run the message as a shell command", displayName(p))
	}
	// re-check right before typing: a permission/question prompt has an option
	// pre-highlighted, so our trailing Enter would answer it for the user
	if p.Status == "question" || paneShowsQuestion(p.Pane) {
		die("%s has a question/permission prompt open — sending would answer it; resolve it first", displayName(p))
	}
	if p.Status == "running" {
		fmt.Fprintf(os.Stderr, "kraken: %s is running — message will queue\n", displayName(p))
	}
	if strings.Contains(msg, "\n") {
		load := exec.Command("tmux", "load-buffer", "-b", "kraken", "-")
		load.Stdin = strings.NewReader(msg)
		if err := load.Run(); err != nil {
			die("tmux load-buffer: %v", err)
		}
		if err := exec.Command("tmux", "paste-buffer", "-p", "-d", "-b", "kraken", "-t", p.Pane).Run(); err != nil {
			die("tmux paste-buffer: %v", err)
		}
	} else {
		if err := exec.Command("tmux", "send-keys", "-t", p.Pane, "-l", "--", msg).Run(); err != nil {
			die("tmux send-keys: %v", err)
		}
	}
	// the TUI's paste detection must drain the literal keys before Enter
	// lands, or the newline gets swallowed into the message
	time.Sleep(150 * time.Millisecond)
	if err := exec.Command("tmux", "send-keys", "-t", p.Pane, "Enter").Run(); err != nil {
		die("tmux send-keys Enter: %v", err)
	}
	fmt.Printf("sent to %s (%s)\n", displayName(p), p.Pane)
}

// verifySendTarget re-resolves the pane's process tree immediately before
// typing: the target must still be a claude under this exact pane. A bare
// pid-exists check would pass for zombies and recycled PIDs — and then the
// message would be typed at (and run by) the surviving pane shell.
func verifySendTarget(p *Peer) {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", p.Pane, "#{pane_pid}").Output()
	if err != nil {
		die("pane %s is gone — rerun 'k ls'", p.Pane)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || panePID <= 0 {
		die("pane %s is gone — rerun 'k ls'", p.Pane)
	}
	children, cmds, _ := psSnapshot()
	for _, hit := range agentPIDsUnder(panePID, children, cmds) {
		if hit.pid == p.PID && hit.agent == p.Agent {
			return
		}
	}
	die("%s (pane %s) is gone — rerun 'k ls'", displayName(p), p.Pane)
}

// --- k k ---

func cmdKill(args []string) {
	if len(args) == 0 {
		die("usage: k k <sel>")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(strings.Join(args, " "), all)
	msg, err := killPeer(p)
	if err != nil {
		die("%v", err)
	}
	fmt.Println(msg)
}

// killPeer closes a pane, agent or shell. The caller's own pane and panes
// holding more than one agent are refused; everything else is re-verified
// immediately before the kill.
func killPeer(p *Peer) (string, error) {
	if p.Pane == "" {
		return "", fmt.Errorf("%s is an in-process teammate — no pane to kill", displayName(p))
	}
	if p.Depth > 0 {
		// Depth > 0 means another agent runs nested inside this one; the
		// multi-agent refusal in verifyKillTarget catches the reverse case
		// (killing the nested one), but this fails before any tmux exec
		return "", fmt.Errorf("%s shares its pane with a nested agent — killing the pane would take both down", displayName(p))
	}
	if p.Pane == os.Getenv("TMUX_PANE") {
		return "", fmt.Errorf("refusing to kill the current pane")
	}
	if err := verifyKillTarget(p); err != nil {
		return "", err
	}
	if err := exec.Command("tmux", "kill-pane", "-t", p.Pane).Run(); err != nil {
		return "", fmt.Errorf("tmux kill-pane: %v", err)
	}
	desc := p.Agent
	if desc == "" {
		desc = "claude"
	}
	if p.Role == "shell" {
		desc = "idle"
		if p.Cmd != "" {
			desc = "was running " + truncateOneLine(p.Cmd, 40)
		}
	}
	return fmt.Sprintf("killed %s (%s, pane %s)", displayName(p), desc, p.Pane), nil
}

// verifyKillTarget re-resolves the pane immediately before the kill so a
// stale snapshot can't take down something the user never saw: a shell pane
// must still be agent-free with the same root process, and an agent must
// still be running alone in its pane.
func verifyKillTarget(p *Peer) error {
	out, err := exec.Command("tmux", "display-message", "-p", "-t", p.Pane, "#{pane_pid}").Output()
	if err != nil {
		return fmt.Errorf("pane %s is already gone", p.Pane)
	}
	panePID, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || panePID <= 0 {
		return fmt.Errorf("pane %s is already gone", p.Pane)
	}
	children, cmds, _ := psSnapshot()
	agents := agentPIDsUnder(panePID, children, cmds)
	if p.Role == "shell" {
		if panePID != p.PID {
			// respawned pane root: the pane no longer runs what the user saw
			return fmt.Errorf("pane %s changed since the last snapshot — rerun 'k ls'", p.Pane)
		}
		if len(agents) > 0 {
			return fmt.Errorf("pane %s has an agent in it now — rerun 'k ls'", p.Pane)
		}
		return nil
	}
	if len(agents) > 1 {
		// a second agent nested inside the target since the snapshot (or the
		// target is the host of one): the pane kill would take both down
		return fmt.Errorf("pane %s holds %d agents — kill would take them all down; rerun 'k ls'", p.Pane, len(agents))
	}
	for _, hit := range agents {
		if hit.pid == p.PID && hit.agent == p.Agent {
			return nil
		}
	}
	return fmt.Errorf("%s is no longer in pane %s — rerun 'k ls'", displayName(p), p.Pane)
}

// --- k r ---

type block struct {
	kind string // "user" | "thinking" | "text" | "tool"
	text string
}

func cmdRead(args []string) {
	if len(args) == 0 {
		die("usage: k r <sel> [n]")
	}
	// the last arg is n only when it parses and a selector precedes it, so
	// multi-word selectors like "k r asr tuning" still work
	sel, n := strings.Join(args, " "), 1
	if v, err := strconv.Atoi(args[len(args)-1]); err == nil && len(args) > 1 {
		if v < 1 {
			die("n must be a positive integer")
		}
		n = v
		sel = strings.Join(args[:len(args)-1], " ")
	}
	all, _ := orderAll(discoverAll())
	p := selectPeer(sel, all)
	if p.Role == "shell" {
		die("%s is a shell pane — no transcript to read", displayName(p))
	}
	if p.Agent == "codex" {
		die("reading Codex transcripts is not supported yet")
	}
	if p.SessionID == "" {
		die("no session id resolved for %s", displayName(p))
	}
	if p.SessionIDGuessed {
		die("session id for %s is a guess (no session file) — could read another claude's transcript", displayName(p))
	}
	path := transcriptPath(p.SessionID)
	if path == "" {
		die("no transcript yet for session %s (fresh forks write theirs on the first turn)", p.SessionID)
	}
	blocks, counted := lastBlocks(path, n)
	if counted == 0 {
		fmt.Println("no output blocks yet")
		return
	}
	if counted < n {
		fmt.Fprintf(os.Stderr, "kraken: only %d block(s) available\n", counted)
	}
	color := useColor()
	for i, b := range blocks {
		if i > 0 {
			fmt.Println()
		}
		switch b.kind {
		case "user":
			fmt.Println(paint(color, cyan, "❯ user"))
			fmt.Println(b.text)
		case "thinking":
			fmt.Println(paint(color, dim, "· thinking"))
			fmt.Println(paint(color, dim, b.text))
		case "tool":
			fmt.Println(paint(color, dim, "→ "+b.text))
		default:
			fmt.Println(b.text)
		}
	}
}

// lastBlocks returns the transcript's trailing blocks: the last n
// user/thinking/text blocks plus any tool one-liners interleaved after the
// first of them. Reads the file tail backward in growing windows so huge
// transcripts stay fast.
func lastBlocks(path string, n int) ([]block, int) {
	f, err := os.Open(path)
	if err != nil {
		die("open transcript: %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		die("stat transcript: %v", err)
	}
	size := info.Size()
	for window := int64(256 * 1024); ; window *= 2 {
		start := size - window
		if start < 0 {
			start = 0
		}
		if _, err := f.Seek(start, 0); err != nil {
			die("seek transcript: %v", err)
		}
		data, err := io.ReadAll(f)
		if err != nil {
			die("read transcript: %v", err)
		}
		lines := strings.Split(string(data), "\n")
		if start > 0 && len(lines) > 0 {
			lines = lines[1:] // drop partial first line
		}
		blocks := parseBlocks(lines)
		counted := 0
		cut := 0
		for i := len(blocks) - 1; i >= 0; i-- {
			if blocks[i].kind != "tool" {
				counted++
				if counted == n {
					cut = i
					break
				}
			}
		}
		if counted >= n || start == 0 {
			if counted == 0 {
				return nil, 0
			}
			return blocks[cut:], counted
		}
	}
}

func parseBlocks(lines []string) []block {
	var blocks []block
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // partial trailing line mid-append, or non-JSON
		}
		t, _ := rec["type"].(string)
		if t != "assistant" && t != "user" {
			continue
		}
		if side, _ := rec["isSidechain"].(bool); side {
			continue // subagent traffic — count only the peer's own blocks
		}
		if meta, _ := rec["isMeta"].(bool); meta {
			continue // harness-injected records (command output, caveats)
		}
		msg, _ := rec["message"].(map[string]any)
		if t == "user" {
			blocks = append(blocks, userBlocks(msg["content"])...)
			continue
		}
		parts, _ := msg["content"].([]any)
		for _, part := range parts {
			pm, ok := part.(map[string]any)
			if !ok {
				continue
			}
			switch pm["type"] {
			case "thinking":
				if s, _ := pm["thinking"].(string); strings.TrimSpace(s) != "" {
					blocks = append(blocks, block{"thinking", s})
				}
			case "text":
				if s, _ := pm["text"].(string); strings.TrimSpace(s) != "" {
					blocks = append(blocks, block{"text", s})
				}
			case "tool_use":
				name, _ := pm["name"].(string)
				blocks = append(blocks, block{"tool", name + "(" + summarizeInput(pm["input"]) + ")"})
			}
		}
	}
	return blocks
}

// userBlocks extracts what the user actually typed from a user record's
// content, which is either a plain string or a list of parts. Tool results
// and injected system-reminder parts ride along in user records but aren't
// the user speaking, so they're dropped.
func userBlocks(content any) []block {
	var blocks []block
	add := func(s string) {
		if strings.TrimSpace(s) == "" || strings.HasPrefix(strings.TrimSpace(s), "<system-reminder>") {
			return
		}
		blocks = append(blocks, block{"user", s})
	}
	switch c := content.(type) {
	case string:
		add(c)
	case []any:
		for _, part := range c {
			pm, ok := part.(map[string]any)
			if !ok || pm["type"] != "text" {
				continue
			}
			s, _ := pm["text"].(string)
			add(s)
		}
	}
	return blocks
}

func summarizeInput(input any) string {
	m, ok := input.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"command", "file_path", "path", "pattern", "description", "prompt", "query", "url", "skill"} {
		if s, ok := m[key].(string); ok && s != "" {
			return truncateOneLine(s, 100)
		}
	}
	out, err := json.Marshal(m)
	if err != nil {
		return ""
	}
	return truncateOneLine(string(out), 100)
}

func truncateOneLine(s string, limit int) string {
	// control chars (esp. \x1b) in argv would otherwise reach the terminal raw
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return string(r[:limit-1]) + "…"
}

// truncTail keeps the end of the string — for branches, the suffix is the
// distinguishing part (carl/v3-native-caller-rtc → …native-caller-rtc).
func truncTail(s string, limit int) string {
	r := []rune(s)
	if len(r) <= limit {
		return s
	}
	return "…" + string(r[len(r)-limit+1:])
}

// --- k b ---

func claudeLauncher() string {
	if c := os.Getenv("KRAKEN_CLAUDE"); c != "" {
		return c
	}
	if p, err := exec.LookPath("claude-patch-buddy"); err == nil {
		return p
	}
	return "claude"
}

// readPermissionMode scans a transcript for the last permission-mode record so
// forks reopen with the same flags the session was running under.
func readPermissionMode(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	last := ""
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, `"permission-mode"`) {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if t, _ := rec["type"].(string); t != "permission-mode" {
			continue
		}
		if m, ok := rec["permissionMode"].(string); ok && m != "" {
			last = m
		}
	}
	return last
}

func claudeArgs(sessionPath string, extra ...string) []string {
	args := []string{claudeLauncher()}
	switch mode := readPermissionMode(sessionPath); mode {
	case "bypassPermissions":
		args = append(args, "--dangerously-skip-permissions")
	case "", "default":
		// no flag needed
	default:
		args = append(args, "--permission-mode", mode)
	}
	return append(args, extra...)
}

func shellCmdString(args []string) string {
	var b strings.Builder
	for i, a := range args {
		if i > 0 {
			b.WriteByte(' ')
		}
		if isShellSafe(a) {
			b.WriteString(a)
		} else {
			b.WriteByte('\'')
			b.WriteString(strings.ReplaceAll(a, "'", `'\''`))
			b.WriteByte('\'')
		}
	}
	return b.String()
}

// isShellSafe allowlists args that need no quoting; everything else gets
// single-quoted rather than trying to enumerate shell metacharacters.
func isShellSafe(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '/', r == '-', r == '=':
		default:
			return false
		}
	}
	return true
}

// branchGuard rejects peers whose session can't be forked safely.
func branchGuard(p *Peer) error {
	if p.Role == "shell" {
		return fmt.Errorf("%s is a shell pane — nothing to fork", displayName(p))
	}
	if p.Agent == "codex" {
		return fmt.Errorf("branching Codex sessions is not supported yet")
	}
	if p.SessionID == "" {
		return fmt.Errorf("no session id resolved for %s", displayName(p))
	}
	if p.SessionIDGuessed {
		return fmt.Errorf("session id for %s is a guess (no session file) — could fork another claude's session", displayName(p))
	}
	return nil
}

// forkCmdString builds the shell command that reopens p's session as a fork,
// replaying the permission mode it was running under.
func forkCmdString(p *Peer) string {
	return shellCmdString(claudeArgs(transcriptPath(p.SessionID), "--fork-session", "-r", p.SessionID))
}

// peerCWD is where a fork of p should start; in-process teammates may have
// no cwd of their own.
func peerCWD(p *Peer) string {
	if p.CWD == "" {
		return home()
	}
	return p.CWD
}

// branchPeerWindow forks p's session into a new tmux window in p's own
// session and jumps to it — the viewer path, where splitting next to the
// caller (the viewer) or the target's pane would steal screen space.
func branchPeerWindow(p *Peer) (string, error) {
	if err := branchGuard(p); err != nil {
		return "", err
	}
	if p.TmuxSession == "" {
		return "", fmt.Errorf("%s has no tmux session (in-process teammate)", displayName(p))
	}
	// "<session>:" targets the session itself: the new window appends at the
	// next free index and becomes the session's current window
	if err := exec.Command("tmux", "new-window", "-t", p.TmuxSession+":", "-c", peerCWD(p), forkCmdString(p)).Run(); err != nil {
		return "", fmt.Errorf("tmux new-window: %v", err)
	}
	msg := fmt.Sprintf("forked %s → new window in %s", displayName(p), p.TmuxSession)
	out, err := raiseSession(p.TmuxSession)
	if err != nil {
		return "", fmt.Errorf("%s, but %v", msg, err)
	}
	switch {
	case out.attached:
		msg += " (attached in a new ghostty window)"
	case out.focusErr != nil:
		msg += fmt.Sprintf(" — focus its window manually: %v", out.focusErr)
	}
	return msg, nil
}

func cmdBranch(args []string) {
	if os.Getenv("TMUX") == "" && os.Getenv("TMUX_PANE") == "" {
		die("k b must run inside tmux")
	}
	all, _ := orderAll(discoverAll())
	var p *Peer
	if len(args) > 0 {
		p = selectPeer(strings.Join(args, " "), all)
	} else {
		p = currentPanePeer(all)
		if p == nil {
			die("no claude in the current pane — pass a selector (k b <sel>)")
		}
	}
	if err := branchGuard(p); err != nil {
		die("%v", err)
	}
	cwd := peerCWD(p)
	shellCmd := forkCmdString(p)
	// split next to the caller, not the target — k b <sel> must not hijack
	// the peer's screen space
	target := os.Getenv("TMUX_PANE")
	if target == "" {
		target = p.Pane
	}
	cmd := exec.Command("tmux", "split-window", "-h", "-t", target, "-c", cwd, shellCmd)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		die("tmux split-window: %v", err)
	}
}

// --- completion ---

func slugify(s string) string {
	var out strings.Builder
	lastDash := true
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			out.WriteRune('-')
			lastDash = true
		}
	}
	return strings.Trim(out.String(), "-")
}

func cmdComplete() {
	all, _ := orderAll(discoverAll())
	for _, p := range all {
		slug := slugify(displayName(p))
		if slug == "" {
			slug = slugify(projectLabel(p))
		}
		if slug == "" {
			slug = strconv.Itoa(p.Index)
		}
		fmt.Printf("%s\t%s %s %s %s\n", slug, p.Status, projectLabel(p), p.Pane, p.TmuxSession)
	}
}
