package core

import (
	"database/sql"
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

	"kraken/internal/util"
)

// --- discovery ---

type tmuxPane struct {
	ID        string
	PID       int
	Session   string
	WindowIdx int
	Path      string
	// StartCmd is the command the pane was started with
	// (tmux's #{pane_start_command}). It is what carries a cell's account
	// binding — kraken prefixes a bound cell's command with
	// CODEX_HOME='<slot>' — so reading it back is how discovery learns which
	// pool account a running codex is billing. Empty when tmux reports none.
	StartCmd string
}

// paneStartCommands maps pane id to start command, in a tmux call of its own.
//
// It is a second call rather than a sixth field on the format below, and that
// is the whole point of it. The list-panes parser resolves tabs by taking the
// session from the END of the line and rejoining everything between the fixed
// front fields and it as the path — a rule that works for exactly ONE
// tab-capable field in the middle. A start command can contain anything at
// all, so adding it to that line would make a tab in a start command corrupt
// the session name, which is what `k o` and `k s` target. Here the pane id
// leads and is the one field that provably holds no tab, so everything after
// the first tab is the command whatever it contains.
func paneStartCommands() map[string]string {
	out, err := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_id}\t#{pane_start_command}").Output()
	if err != nil {
		// silent by design, same as listTmuxPanes: no tmux server, no panes
		return map[string]string{}
	}
	return parsePaneStartCommands(string(out))
}

// parsePaneStartCommands is the pure half of the call above — split out
// because the shell-out is the only part that needs a tmux, while the line
// rule is the part with consequences.
//
// Each line is cut at its FIRST tab: before it is the pane id, after it is
// the command VERBATIM, further tabs and enclosing double quotes and all
// (tmux hands a command containing spaces back double-quoted, and it is
// CodexHomeFromCommand's job to peel that, not this function's — a parser
// that tidied the value would be guessing at a shell's syntax).
//
// A line with no tab, or one whose id does not begin with "%", is dropped
// and affects no other pane. That rule is what bounds the one input this
// cannot represent: a start command containing a NEWLINE splits across lines
// here, as it does in every tmux reader in this file, and the continuation
// lines carry no "%" id — so that pane loses its StartCmd and every other
// pane is untouched, rather than one pane's command being attributed to
// another.
func parsePaneStartCommands(out string) map[string]string {
	cmds := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		id, cmd, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(id, "%") {
			continue
		}
		cmds[id] = cmd
	}
	return cmds
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
	starts := paneStartCommands()
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
			StartCmd:  starts[f[0]],
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
		if !strings.HasPrefix(line, "n") {
			continue
		}
		// IsCodexRollout, not a "/.codex/sessions/" substring: a cell bound to
		// a pool slot writes its rollout under <pool>/accounts/<slug>/sessions/,
		// with no ".codex" anywhere in the path, and a substring test would
		// leave this legacy arm unable to identify exactly the sessions the
		// account pool creates.
		if rollout := strings.TrimPrefix(line, "n"); IsCodexRollout(rollout) {
			out.Path = rollout
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

// codexMainHome is the machine's own CODEX_HOME — the one a codex started
// with no CODEX_HOME in its environment uses, and the fallback for any pane
// whose start command names no pool slot.
func codexMainHome() string {
	return filepath.Join(util.Home(), ".codex")
}

// codexStatePathIn is the live SQLite index Codex maintains inside ONE codex
// home. There is one per home: a pool slot records its own threads, and a
// thread started under a slot is invisible in the machine login's index.
func codexStatePathIn(home string) string {
	return filepath.Join(home, "state_5.sqlite")
}

// codexStatePath is the machine login's index — the successor to the retired
// append-only session_index.jsonl.
func codexStatePath() string {
	return codexStatePathIn(codexMainHome())
}

// codexHomes lists every codex home kraken knows about: the machine login
// first, then each pool slot. It is the search order for a thread whose home
// could not be read off its pane's start command.
func codexHomes() []string {
	homes := []string{codexMainHome()}
	for _, slug := range CodexPoolSlugs() {
		homes = append(homes, CodexSlotDir(slug))
	}
	return homes
}

// openCodexState opens a codex state_5.sqlite for reading, and the open mode
// is the whole point of the function. Codex runs its index in WAL mode and
// checkpoints on exit, so while a codex is LIVE the newest rows — its own
// thread, the one discovery is looking for — exist only in the -wal file. A
// read-only open honours that log; an `immutable=1` open does not (SQLite
// takes immutable to mean "no journal to consult"), and on a fresh pool slot,
// whose main file is a 4 KB header with everything else still in the WAL,
// it reads a database with no tables at all. Measured at the codex account
// pool's live gate (2026-09-17): the slot's own `.tables` was empty under
// immutable while the plain read-only open returned the live cell's thread.
//
// So the plain read-only open is tried first. Honouring the WAL structurally
// needs the wal-index (the -shm beside the db), and SQLite MATERIALISES that
// file when it is absent and the directory is writable, read-only connection
// or not — measured (2026-09-17): a slot whose codex exited without
// checkpointing, holding state_5.sqlite beside a -wal, gains a 32 KB -shm
// after one such read, with the database and the log byte-identical
// afterwards (a read-only connection cannot checkpoint). That is SQLite's
// own rebuildable side file, identical to the ones codex leaves beside every
// other index in a slot, and it is the one write beside a slot's index the
// pool contract carves out.
//
// When that open FAILS — probed with one query, since sql.Open itself is lazy;
// the shape is a directory where the -shm can neither be found nor created —
// the immutable open is the fallback, which is still correct for every
// checkpointed row and is what this reader always did. The probe is an OPEN
// test, not a WAL test: a log SQLite cannot use (unknown magic, a truncated
// frame) is ignored and reads as a successful open of the main file alone,
// which is exactly what the fallback would have returned.
//
// The fallback logs one event per index, and only when a -wal sits beside
// it — the one state in which the immutable open can actually lose rows: a
// live cell it serves then renders with no thread at all, and that symptom
// is otherwise indistinguishable from "no codex here". With no -wal there is
// nothing unread and the fallback's answer is complete, so it says nothing.
// The gate and the once-per-path guard are both load-bearing, because this
// runs on the discovery tick — the viewer's every second, the board's every
// few — and the dominant reason a first open fails is that there is no
// state_5.sqlite in that home at all (a slot codex has never run in), which
// is ordinary and would otherwise log once per home per tick, forever, into
// the same file every other event in the binary shares. The guard never
// re-arms within a process, so a warn is dated to the first time its index
// fell back, not the last.
//
// The 2s busy timeout REPLACES the driver's own 5s default (mattn/go-sqlite3
// applies `PRAGMA busy_timeout = 5000` to every connection it opens): the
// probe sits inside that same tick, and a stalled home lookup holds the whole
// tick, so the ceiling is shorter here than the store's. Callers still treat
// every error as "no row".
func openCodexState(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", "file:"+path+"?mode=ro&_busy_timeout=2000")
	if err == nil {
		var n int
		if perr := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); perr == nil {
			return db, nil
		}
		db.Close()
	}
	if _, statErr := os.Stat(path + "-wal"); statErr == nil {
		if _, seen := codexImmutableLogged.LoadOrStore(path, true); !seen {
			util.LogEvent("core", "warn", "codex-state-immutable", "codex index "+path+" opened immutable: the read-only open failed, so rows still in its WAL are invisible")
		}
	}
	return sql.Open("sqlite3", "file:"+path+"?mode=ro&immutable=1")
}

// codexImmutableLogged holds every index path whose immutable fallback has
// already been logged by this process, so the event above fires once per
// index rather than once per discovery tick.
var codexImmutableLogged sync.Map

// readCodexThreadNames returns Codex's user-visible thread titles keyed by
// thread id, read from the live state_5.sqlite index through openCodexState
// (read-only, WAL honoured, immutable as the fallback), and follows the
// defensive-read convention: a missing file, an open error, a query error, or
// a corrupt db all yield an empty (non-nil) map and never an error or panic.
// Rows with a NULL or empty title are omitted.
func readCodexThreadNames() map[string]string {
	names := map[string]string{}
	db, err := openCodexState(codexStatePath())
	if err != nil {
		return names
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, title FROM threads`)
	if err != nil {
		return names
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var title sql.NullString
		if err := rows.Scan(&id, &title); err != nil {
			return map[string]string{}
		}
		if id != "" && title.Valid && title.String != "" {
			names[id] = title.String
		}
	}
	if err := rows.Err(); err != nil {
		return map[string]string{}
	}
	return names
}

// codexThread is the metadata row for one Codex thread, read from the live
// state_5.sqlite index.
type codexThread struct {
	ID, Title, Model, RolloutPath, CWD string
	TokensUsed                         int64
}

// readCodexThreadByCWDIn returns the newest-updated Codex thread for a given
// cwd inside ONE codex home, together with that row's updated_at so a caller
// comparing homes can pick the newest across them rather than the first that
// answers. Opens state_5.sqlite through openCodexState (read-only with the
// WAL honoured, since a live codex's own thread row lives there until it
// exits). An empty home or cwd returns (zero,0,false) without querying; a no
// match, a missing/locked/corrupt db, a home with no threads table, or any
// other error also returns (zero,0,false), never a panic or a surfaced error —
// a pool slot that has never run has no index at all, and that is ordinary.
func readCodexThreadByCWDIn(home, cwd string) (codexThread, int64, bool) {
	var ct codexThread
	if home == "" || cwd == "" {
		return ct, 0, false
	}
	db, err := openCodexState(codexStatePathIn(home))
	if err != nil {
		return ct, 0, false
	}
	defer db.Close()
	row := db.QueryRow(
		`SELECT id, title, model, tokens_used, rollout_path, cwd, updated_at FROM threads WHERE cwd=? ORDER BY updated_at DESC LIMIT 1`,
		cwd)
	var id, title, model, rollout, rowCWD sql.NullString
	var tokens, updated sql.NullInt64
	if err := row.Scan(&id, &title, &model, &tokens, &rollout, &rowCWD, &updated); err != nil {
		return ct, 0, false
	}
	ct = codexThread{
		ID:          id.String,
		Title:       title.String,
		Model:       model.String,
		RolloutPath: rollout.String,
		CWD:         rowCWD.String,
		TokensUsed:  tokens.Int64,
	}
	return ct, updated.Int64, true
}

// readCodexThreadByCWD is the machine-login case of readCodexThreadByCWDIn,
// kept with its original signature. Discovery itself now resolves every codex
// pane through codexThreadForPane, so this is a test-facing alias that pins
// the ~/.codex meaning rather than a production entry point.
func readCodexThreadByCWD(cwd string) (codexThread, bool) {
	ct, _, ok := readCodexThreadByCWDIn(codexMainHome(), cwd)
	return ct, ok
}

// codexThreadForPane resolves a codex pane's thread, and the home it was
// found in, from the pane's own cwd.
//
// The pane's start command names the home when kraken bound the cell to a
// pool slot, and ~/.codex is the home otherwise. That home is tried alone
// first: it is an authoritative answer about which account this pane runs
// under, and searching further could only replace it with a thread from a
// different one.
//
// Only when that misses does the search widen to every known home, and then
// the row with the greatest updated_at WINS — not the first home that
// answers. The homes are searched in a fixed order (machine login, then pool
// slugs alphabetically), so "first match" would mean "the alphabetically
// earliest slot that once ran in this directory", which is a real hazard
// rather than a theoretical one: a cell's worktree is re-used across
// generations, and an account that ran there weeks ago would outrank the one
// running there now.
func codexThreadForPane(startCmd, cwd string) (codexThread, string, bool) {
	if cwd == "" {
		return codexThread{}, "", false
	}
	home := CodexHomeFromCommand(startCmd)
	if home == "" {
		home = codexMainHome()
	}
	if ct, _, ok := readCodexThreadByCWDIn(home, cwd); ok {
		return ct, home, true
	}
	var best codexThread
	var bestHome string
	var bestUpdated int64
	found := false
	for _, h := range codexHomes() {
		ct, updated, ok := readCodexThreadByCWDIn(h, cwd)
		if !ok {
			continue // no index, no threads table, no row for this cwd: skip it
		}
		if !found || updated > bestUpdated {
			best, bestHome, bestUpdated, found = ct, h, updated, true
		}
	}
	// No hit anywhere: the zero thread and an empty home, never a home that
	// was merely searched.
	return best, bestHome, found
}

// codexRolloutPath resolves a Codex thread's rollout transcript path by session
// id, through the same openCodexState open. An empty id, a no match, or any
// error returns "".
func codexRolloutPath(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	db, err := openCodexState(codexStatePath())
	if err != nil {
		return ""
	}
	defer db.Close()
	var rollout sql.NullString
	if err := db.QueryRow(`SELECT rollout_path FROM threads WHERE id=?`, sessionID).Scan(&rollout); err != nil {
		return ""
	}
	return rollout.String
}

// claudeModelFallbackApplies is the guard for the model-fallback call site in
// DiscoverAll: a claude peer with an empty model and a real (non-guessed)
// session id gets its model read from its transcript. A codex peer must return
// false here — its session id is a rollout uuid that would otherwise be fed to
// transcriptPath's ~/.claude/projects glob.
func claudeModelFallbackApplies(p *Peer) bool {
	return p.Model == "" && p.SessionID != "" && !p.SessionIDGuessed && p.Agent != "codex"
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
				// Identity by cwd: codex 0.144.6 holds no rollout fd open, so
				// the newest threads row for the pane's cwd is the session.
				// WHICH index that row comes from is the pane's own start
				// command to answer — a cell bound to a pool slot records its
				// threads under that slot and nowhere else.
				// readCodexSession (lsof) stays as a fallback for older codex
				// that still holds the rollout open.
				cwd := procCWD(pid, cmds[pid])
				if ct, home, ok := codexThreadForPane(p.StartCmd, cwd); ok {
					peer.SessionID = ct.ID
					peer.Name = ct.Title
					peer.Model = ct.Model
					peer.CWD = ct.CWD
					peer.RolloutPath = ct.RolloutPath
					peer.CodexHome = home
				} else if cs, ok := readCodexSession(pid, cmds[pid]); ok {
					peer.SessionID = cs.ID
					peer.CWD = cs.CWD
					peer.Name = cs.Name
					peer.Model = cs.Model
					peer.StatusAt = cs.UpdatedAt
				} else {
					peer.CWD = cwd
				}
			} else if sf, ok := readSessionFile(pid); ok {
				peer.SessionID = sf.SessionID
				// names render raw into table lines: control chars or a
				// newline would break the one-row-per-line layout
				peer.Name = TruncateOneLine(sf.Name, 80)
				peer.CWD = sf.CWD
				peer.Kind = sf.Kind
				peer.StatusAt = sf.StatusUpdatedAt
				peer.StartedAt = sf.StartedAt
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
			if claudeModelFallbackApplies(peer) {
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
			if peer.Agent == "claude" && peer.SessionID != "" && !peer.SessionIDGuessed {
				transcript := transcriptPath(peer.SessionID)
				if peer.Status == "waiting" {
					if w := FindPendingWakeup(transcript, time.Now()); w != nil {
						peer.Status = "polling"
						peer.Wakeup = w
					}
				}
				// Background tasks ride the same transcript and the same
				// real-session-id guard: an in-process subagent, a monitor
				// or a background shell is invisible to the process walk
				// above (no pid, or a child zsh it never surfaces), and the
				// session file says nothing about them. Status is left
				// alone — the roll call reads Tasks itself.
				if open := ScanTasks(transcript); len(open) > 0 {
					peer.Tasks = LiveTasks(open, peer.StartedAt, SidecarDir(transcript), pid, children, cmds, time.Now())
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

// shortModel compacts a model id for display: claude-fable-5-1 → fable-5-1.
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
