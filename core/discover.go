package core

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"kraken/util"
)

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
		// silent by design (no tmux server = no sessions), but debuggable:
		// a daemon that sees nothing is otherwise indistinguishable from this
		if os.Getenv("KRAKEN_DEBUG") != "" {
			msg := err.Error()
			if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
				msg += ": " + strings.TrimSpace(string(ee.Stderr))
			}
			fmt.Fprintf(os.Stderr, "kraken: list-panes failed: %s\n", msg)
		}
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
				out.Name = TruncateOneLine(rec.Payload.Message, 48)
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
	data, err := os.ReadFile(filepath.Join(util.Home(), ".codex", "session_index.jsonl"))
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
	data, err := os.ReadFile(filepath.Join(util.Home(), ".claude", "sessions", strconv.Itoa(pid)+".json"))
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

// DiscoverAll is the shared entrypoint: every command works off its output.
// Read-only; kraken keeps no state of its own. Panes with no agent in them
// come back as shell Peers (Role "shell") so selection, k o, k k work on them.
// Both lists are grouped by tmux session, then window.
func DiscoverAll() ([]*Peer, []*Peer) {
	panes := listTmuxPanes()
	children, cmds, etimes := psSnapshot()
	if os.Getenv("KRAKEN_DEBUG") != "" {
		fmt.Fprintf(os.Stderr, "kraken: discover: %d panes, %d ps entries\n", len(panes), len(cmds))
	}
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
			sp.Name = ProjectLabel(sp)
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
				peer.Name = TruncateOneLine(sf.Name, 80)
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
					// unmapped values surface verbatim rather than being
					// hidden; "shell" in particular just means a background
					// Bash-tool shell is alive, and gets refined from pane
					// content in refineStatus like "unknown"
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
			// A claude running a timed poll loop (/loop, /poll-pr) is idle
			// between ticks and reads "waiting" off its session file just
			// like one genuinely sitting at its prompt; a pending
			// ScheduleWakeup in its own transcript is the tell that
			// distinguishes the two. Only "waiting" ever upgrades here —
			// "question" (waiting on the user) and "running" are left
			// untouched — and only for a real session id: a guessed one
			// (SessionIDGuessed) could belong to another claude entirely,
			// so reading it would be unsafe.
			if peer.Agent == "claude" && peer.Status == "waiting" && peer.SessionID != "" && !peer.SessionIDGuessed {
				if w := FindPendingWakeup(transcriptPath(peer.SessionID), time.Now()); w != nil {
					peer.Status = "polling"
					peer.Wakeup = w
				}
			}
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
			return SessionLess(list[i].TmuxSession, list[j].TmuxSession)
		}
		return list[i].WindowIdx < list[j].WindowIdx
	})
}

// SessionLess orders session names numerically first (default tmux names are
// "0", "1", ..., where "10" must sort after "9"), then everything else by
// string. Numbers-before-names keeps the ordering total — comparing a number
// with a name by string would be intransitive ("9"<"10" numeric but
// "10"<"5a"<"9" by string), and sort's output is unspecified on such cycles.
func SessionLess(a, b string) bool {
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
	dir := filepath.Join(util.Home(), ".claude", "projects", projectDirName(cwd))
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

// TranscriptPathForSession is transcriptPath for callers outside core that
// only hold a session id — e.g. the board reading a dead cell's last words,
// where no live Peer exists to hand ReadTranscriptPath.
func TranscriptPathForSession(sessionID string) string {
	return transcriptPath(sessionID)
}

// transcriptPath locates a session's transcript by globbing all project dirs —
// more robust than re-deriving the cwd encoding.
func transcriptPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(util.Home(), ".claude", "projects", "*", sessionID+".jsonl"))
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
