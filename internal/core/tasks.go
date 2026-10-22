package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// tasks.go — a claude's BACKGROUND TASKS, read from outside the process.
// Claude Code runs three kinds of task beside the foreground turn: a Bash
// call with run_in_background (its own "shell"), a Monitor watch, and an
// in-process subagent spawned by the Agent tool. None of them is a pane and
// only the first two are processes at all, so discovery's process walk and
// the session file (whose status stays "busy" through an idle-with-tasks
// window — measured 2026-10-20 on 2.1.304) see nothing of them. A zeus that
// fans out six subagents and then ends its turn to wait for them sat at its
// prompt reading "waiting", the roll call voted the cell blocked 23s later,
// and it stayed blocked for the five minutes the agents took to come back.
//
// The ledger is the session transcript: every launch is a tool_result whose
// text carries the task id in a fixed phrase per kind, every completion is a
// `<task-notification>` user record naming that id, and a TaskStop tool_use
// names what it stopped (a stop writes NO notification). The ledger's open
// set is then PRUNED against the world, because a transcript outlives its
// process: a `claude -r` resume, a --fork-session thread and a crash all
// carry the launch records forward with nothing to close them. So a task
// launched before the current process started is dead; a subagent whose
// sidecar `agent-<id>.meta.json` has been rewritten with `finished: true` is
// dead; and a shell or monitor with no matching child process of the claude
// pid is dead. Every unreadable or malformed input degrades toward "trust the
// ledger", never to a phantom task from a truncated scan.

// Task kinds, named as Claude Code's own TUI names them (its status line
// reads "1 shell, 1 monitor"; its /tasks list calls the third a subagent).
const (
	TaskShell    = "shell"
	TaskMonitor  = "monitor"
	TaskSubagent = "subagent"
)

// A Task is one open background task of a claude. It goes over the wire in
// `k ls --json` on the peer that owns it, hence the json tags; Command and
// the open-event stamp are prune inputs rather than facts a reader wants.
type Task struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	// AgentType and Model are a subagent's: the Agent call's subagent_type
	// (or the sidecar's agentType, which also names the default when the
	// call set none) and the sidecar's model alias.
	AgentType string `json:"agent_type,omitempty"`
	Model     string `json:"model,omitempty"`
	// StartedAt is the launch record's timestamp, unix millis.
	StartedAt int64 `json:"started_at"`
	// Command is a shell's or monitor's command text, matched against the
	// child process' argv by LiveTasks.
	Command string `json:"-"`
	// OpenedAt is the latest open event — the launch, or a SendMessage that
	// resumed a finished subagent — so a sidecar `finished` older than it
	// cannot close a task the ledger has since reopened.
	OpenedAt int64 `json:"-"`
	// ToolUseID is the launching tool_use's id.
	ToolUseID string `json:"-"`
}

// taskEvent is the slice of a transcript line ScanTasks cares about: an
// assistant tool_use (a launch, a TaskStop, a SendMessage) or a user record
// carrying tool_results or a task-notification. Content is a string on a
// plain user prompt and an array of blocks everywhere else, so it is decoded
// per record rather than typed here.
type taskEvent struct {
	Type        string `json:"type"`
	Timestamp   string `json:"timestamp"`
	IsSidechain bool   `json:"isSidechain"`
	Message     struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type taskBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Text      string          `json:"text"`
	Content   json.RawMessage `json:"content"`
}

// taskLaunchInput is the union of the three launching tools' inputs plus
// the two closing tools' (TaskStop's task_id, SendMessage's to).
type taskLaunchInput struct {
	Command         string `json:"command"`
	Description     string `json:"description"`
	RunInBackground bool   `json:"run_in_background"`
	SubagentType    string `json:"subagent_type"`
	Name            string `json:"name"`
	TaskID          string `json:"task_id"`
	ShellID         string `json:"shell_id"`
	To              string `json:"to"`
}

// The three launch phrases, measured on 2.1.304. A NAMED Agent spawn (a
// teammate) answers `agent_id: <name>@<team>` instead and is a pane of its
// own, so only the camel-case async form opens a task here.
var (
	taskShellIDRe    = regexp.MustCompile(`Command running in background with ID: ([A-Za-z0-9_-]+)`)
	taskMonitorIDRe  = regexp.MustCompile(`Monitor started \(task ([A-Za-z0-9_-]+)`)
	taskAgentIDRe    = regexp.MustCompile(`\bagentId: ([A-Za-z0-9_-]+)`)
	taskNotifyIDRe   = regexp.MustCompile(`<task-notification>[\s\S]*?<task-id>([A-Za-z0-9_-]+)</task-id>`)
	taskWhitespaceRe = regexp.MustCompile(`\s+`)
)

// taskCacheEntry memoizes ScanTasks per transcript path, keyed on (size,
// mtime) like wakeupCache: the open set is derivable from the bytes alone,
// and every prune that depends on the clock or the world happens in
// LiveTasks on every call.
type taskCacheEntry struct {
	size  int64
	mtime time.Time
	open  []Task
}

var (
	taskCacheMu sync.Mutex
	taskCache   = map[string]taskCacheEntry{}
)

// ScanTasks returns the transcript's own open-task ledger: launched, not
// since notified complete or stopped, reopened by a later SendMessage. It is
// the UNPRUNED set — pass it through LiveTasks before believing it. An
// unreadable file, a malformed line, or no launches at all read as no tasks.
func ScanTasks(transcript string) []Task {
	info, err := os.Stat(transcript)
	if err != nil {
		return nil
	}
	taskCacheMu.Lock()
	e, ok := taskCache[transcript]
	taskCacheMu.Unlock()
	if !ok || e.size != info.Size() || !e.mtime.Equal(info.ModTime()) {
		e = taskCacheEntry{size: info.Size(), mtime: info.ModTime(), open: scanTasks(transcript)}
		taskCacheMu.Lock()
		taskCache[transcript] = e
		taskCacheMu.Unlock()
	}
	if len(e.open) == 0 {
		return nil
	}
	out := make([]Task, len(e.open))
	copy(out, e.open)
	return out
}

// scanTasks is the line-by-line scan behind ScanTasks' cache.
func scanTasks(transcript string) []Task {
	f, err := os.Open(transcript)
	if err != nil {
		return nil
	}
	defer f.Close()

	// launches awaiting their tool_result, by tool_use id
	pending := map[string]Task{}
	// open tasks by task id; closed keeps a finished subagent so a later
	// SendMessage can reopen it with its identity intact
	open := map[string]Task{}
	closed := map[string]Task{}

	sc := bufio.NewScanner(f)
	// transcript lines run to several MB; bufio's 64KB default silently
	// truncates scanning, so give it real headroom (same as scanWakeup).
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var ev taskEvent
		if json.Unmarshal(line, &ev) != nil {
			continue // malformed line: skip it, don't lose the rest of the transcript
		}
		if ev.IsSidechain {
			continue
		}
		ts := taskTimestamp(ev.Timestamp)
		switch ev.Type {
		case "assistant":
			var blocks []taskBlock
			if json.Unmarshal(ev.Message.Content, &blocks) != nil {
				continue
			}
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				var in taskLaunchInput
				_ = json.Unmarshal(b.Input, &in) // a bad input leaves every field blank, which opens nothing below
				switch b.Name {
				case "Bash":
					if in.RunInBackground {
						pending[b.ID] = Task{Kind: TaskShell, Description: in.Description, Command: in.Command, StartedAt: ts, OpenedAt: ts, ToolUseID: b.ID}
					}
				case "Monitor":
					pending[b.ID] = Task{Kind: TaskMonitor, Description: in.Description, Command: in.Command, StartedAt: ts, OpenedAt: ts, ToolUseID: b.ID}
				case "Agent":
					if in.Name == "" { // a named spawn is a teammate pane, never a task
						pending[b.ID] = Task{Kind: TaskSubagent, Description: in.Description, AgentType: in.SubagentType, StartedAt: ts, OpenedAt: ts, ToolUseID: b.ID}
					}
				case "TaskStop":
					id := in.TaskID
					if id == "" {
						id = in.ShellID
					}
					if t, ok := open[id]; ok {
						delete(open, id)
						closed[id] = t
					}
				case "SendMessage":
					if t, ok := closed[in.To]; ok && t.Kind == TaskSubagent {
						t.OpenedAt = ts
						open[in.To] = t
						delete(closed, in.To)
					}
				}
			}
		case "user":
			text, blocks := userContent(ev.Message.Content)
			for _, b := range blocks {
				if b.Type != "tool_result" {
					continue
				}
				launch, ok := pending[b.ToolUseID]
				if !ok {
					continue
				}
				delete(pending, b.ToolUseID)
				id := taskIDFromResult(launch.Kind, blockText(b))
				if id == "" {
					continue // ran in the foreground after all, or refused: nothing to track
				}
				launch.ID = id
				open[id] = launch
				delete(closed, id)
			}
			if text == "" {
				continue
			}
			for _, m := range taskNotifyIDRe.FindAllStringSubmatch(text, -1) {
				if t, ok := open[m[1]]; ok {
					delete(open, m[1])
					closed[m[1]] = t
				}
			}
		}
	}
	if sc.Err() != nil {
		// scan failed partway (a line over the buffer max): fail closed
		// rather than report a phantom open set off a truncated scan.
		return nil
	}
	out := make([]Task, 0, len(open))
	for _, t := range open {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAt != out[j].StartedAt {
			return out[i].StartedAt < out[j].StartedAt
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// userContent decodes a user record's content, which is a plain string on a
// typed prompt (a task-notification arrives this way) and a block array on a
// tool_result turn. Both forms are returned: the concatenated text of every
// text-bearing block for the notification scan, and the blocks themselves.
func userContent(raw json.RawMessage) (string, []taskBlock) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, nil
	}
	var blocks []taskBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", nil
	}
	var sb strings.Builder
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			sb.WriteString(b.Text)
			sb.WriteByte('\n')
		}
	}
	return sb.String(), blocks
}

// blockText flattens a tool_result's content, which is a string for a shell
// result and a text-block array for an Agent result.
func blockText(b taskBlock) string {
	var s string
	if json.Unmarshal(b.Content, &s) == nil {
		return s
	}
	var inner []taskBlock
	if json.Unmarshal(b.Content, &inner) != nil {
		return ""
	}
	var sb strings.Builder
	for _, ib := range inner {
		sb.WriteString(ib.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}

func taskIDFromResult(kind, text string) string {
	var re *regexp.Regexp
	switch kind {
	case TaskShell:
		re = taskShellIDRe
	case TaskMonitor:
		re = taskMonitorIDRe
	case TaskSubagent:
		re = taskAgentIDRe
	default:
		return ""
	}
	m := re.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

func taskTimestamp(s string) int64 {
	ts, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return ts.UnixMilli()
}

// taskMetaGrace is how long a subagent may lack its sidecar meta file before
// that absence reads as death: the file is written at spawn (measured), so a
// gap is the write racing this read, and 30s is far past any such race while
// far short of the shortest subagent run worth showing.
const taskMetaGrace = 30 * time.Second

// taskMeta is the slice of `agent-<id>.meta.json` the prune reads: finished
// is absent at spawn and true once the agent completes; the other two fill
// in what the Agent call's input did not say.
type taskMeta struct {
	AgentType string `json:"agentType"`
	Model     string `json:"model"`
	Finished  bool   `json:"finished"`
}

// LiveTasks prunes a transcript's open set against the world. procStartMS
// is the claude process' start (the session file's startedAt; 0 skips the
// rule), sidecarDir the session's `subagents/` dir, pid the claude process
// whose direct children a shell or monitor must be among, and children/cmds
// discovery's psSnapshot maps.
func LiveTasks(open []Task, procStartMS int64, sidecarDir string, pid int, children map[int][]int, cmds map[int]string, now time.Time) []Task {
	var out []Task
	for _, t := range open {
		if procStartMS > 0 && t.StartedAt > 0 && t.StartedAt < procStartMS {
			continue // launched by an earlier process: a resume, a fork or a crash
		}
		switch t.Kind {
		case TaskSubagent:
			if !subagentAlive(&t, sidecarDir, now) {
				continue
			}
		case TaskShell, TaskMonitor:
			if !shellTaskAlive(t, pid, children, cmds) {
				continue
			}
		}
		out = append(out, t)
	}
	return out
}

// subagentAlive applies the sidecar rules and fills the task's agent type
// and model from the meta file when the launch did not carry them.
func subagentAlive(t *Task, sidecarDir string, now time.Time) bool {
	if sidecarDir == "" {
		return true
	}
	path := filepath.Join(sidecarDir, "agent-"+t.ID+".meta.json")
	info, err := os.Stat(path)
	if err != nil {
		// not written yet (a spawn racing this read) or never written (a
		// spawn that died before its sidecar): the grace tells them apart
		return t.OpenedAt == 0 || now.Sub(time.UnixMilli(t.OpenedAt)) < taskMetaGrace
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return true
	}
	var m taskMeta
	if json.Unmarshal(data, &m) != nil {
		return true
	}
	if t.AgentType == "" {
		t.AgentType = m.AgentType
	}
	if t.Model == "" {
		t.Model = m.Model
	}
	if m.Finished && info.ModTime().UnixMilli() >= t.OpenedAt {
		return false
	}
	return true
}

// shellTaskAlive reports whether a direct child of pid carries the task's
// command text. Claude Code runs every Bash-tool and Monitor command as
// `/bin/zsh -c source <snapshot> … && eval '<command>'` (measured), so the
// command's first line, whitespace-collapsed and cut at its first quote
// (which the wrapper re-escapes), is the substring to look for. A command
// that yields no matchable prefix is kept: the ledger is then the only
// evidence, and dropping it would hide a task on a formatting accident.
func shellTaskAlive(t Task, pid int, children map[int][]int, cmds map[int]string) bool {
	prefix := commandPrefix(t.Command)
	if prefix == "" {
		return true
	}
	for _, c := range children[pid] {
		argv := taskWhitespaceRe.ReplaceAllString(cmds[c], " ")
		if strings.Contains(argv, prefix) {
			return true
		}
	}
	return false
}

// commandPrefix is the matchable head of a task command: its first line,
// whitespace-collapsed, cut before the first single quote, capped at 48
// runes so a long one-liner still matches a ps argv that ps may have cut.
func commandPrefix(cmd string) string {
	first := cmd
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	if i := strings.IndexByte(first, '\''); i >= 0 {
		first = first[:i]
	}
	first = strings.TrimSpace(taskWhitespaceRe.ReplaceAllString(first, " "))
	if r := []rune(first); len(r) > 48 {
		first = string(r[:48])
	}
	return first
}

// SidecarDir is the per-session directory beside a transcript that holds a
// claude's subagent transcripts and meta files:
// <project>/<session-id>/subagents/, the same layout tokens.go sums.
func SidecarDir(transcript string) string {
	if transcript == "" {
		return ""
	}
	return filepath.Join(strings.TrimSuffix(transcript, ".jsonl"), "subagents")
}

// TaskSummary renders a task list as the suffix k ls and the viewer append
// to a status cell: kinds in a fixed order (subagents first, since they are
// the work; shells last, since they may be a server left running), each as
// a count with its plural, joined by " · ". Empty for no tasks.
func TaskSummary(tasks []Task) string {
	counts := map[string]int{}
	for _, t := range tasks {
		counts[t.Kind]++
	}
	var parts []string
	for _, kind := range []string{TaskSubagent, TaskMonitor, TaskShell} {
		n := counts[kind]
		if n == 0 {
			continue
		}
		label := kind
		if n != 1 {
			label += "s"
		}
		parts = append(parts, strconv.Itoa(n)+" "+label)
	}
	return strings.Join(parts, " · ")
}
