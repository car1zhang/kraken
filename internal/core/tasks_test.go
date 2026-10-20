package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Fixture lines mirror the 2.1.304 record shapes measured in this cell's own
// transcript on 2026-10-20: a background Bash launch, a Monitor launch, an
// Agent launch, their tool_results, a task-notification, a TaskStop.

func taskUse(ts, id, name, input string) string {
	return `{"type":"assistant","timestamp":"` + ts + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}

func taskResultText(ts, toolUseID, text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[{"tool_use_id":"` + toolUseID + `","type":"tool_result","content":` + string(b) + `}]}}`
}

func taskResultBlocks(ts, toolUseID, text string) string {
	b, _ := json.Marshal(text)
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":[{"tool_use_id":"` + toolUseID + `","type":"tool_result","content":[{"type":"text","text":` + string(b) + `}]}]}}`
}

func taskNotification(ts, taskID string) string {
	body := "<task-notification>\n<task-id>" + taskID + "</task-id>\n<tool-use-id>x</tool-use-id>\n<status>completed</status>\n</task-notification>"
	b, _ := json.Marshal(body)
	return `{"type":"user","timestamp":"` + ts + `","message":{"role":"user","content":` + string(b) + `}}`
}

func writeTaskTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sess.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const (
	t0 = "2026-10-20T16:58:00.000Z"
	t1 = "2026-10-20T16:58:01.000Z"
	t2 = "2026-10-20T16:58:02.000Z"
	t3 = "2026-10-20T16:58:03.000Z"
	t4 = "2026-10-20T16:58:04.000Z"
)

var (
	shellLaunch   = taskUse(t0, "tu-shell", "Bash", `{"command":"sleep 400; echo done","description":"probe shell","run_in_background":true}`)
	shellResult   = taskResultText(t0, "tu-shell", "Command running in background with ID: bly8ijzgx. Output is being written to: /tmp/x/tasks/bly8ijzgx.output.")
	monitorLaunch = taskUse(t1, "tu-mon", "Monitor", `{"command":"i=0; while [ $i -lt 20 ]; do sleep 15; done","description":"probe monitor","timeout_ms":360000}`)
	monitorResult = taskResultText(t1, "tu-mon", "Monitor started (task bzuy1s169, expires in 6m unless the source ends first).")
	agentLaunch   = taskUse(t2, "tu-agent", "Agent", `{"subagent_type":"Explore","description":"Probe: read-only code sweep","prompt":"read things"}`)
	agentResult   = taskResultBlocks(t2, "tu-agent", "Async agent launched successfully. (internal metadata)\nagentId: a63f2a8dad6f0e4ef (internal ID - do not mention to user.)\nThe agent is working in the background.")
)

func kinds(ts []Task) string {
	var parts []string
	for _, t := range ts {
		parts = append(parts, t.Kind+":"+t.ID)
	}
	return strings.Join(parts, ",")
}

func TestScanTasksOpensThreeKinds(t *testing.T) {
	p := writeTaskTranscript(t, shellLaunch, shellResult, monitorLaunch, monitorResult, agentLaunch, agentResult)
	got := ScanTasks(p)
	if kinds(got) != "shell:bly8ijzgx,monitor:bzuy1s169,subagent:a63f2a8dad6f0e4ef" {
		t.Fatalf("open set = %s", kinds(got))
	}
	if got[0].Description != "probe shell" || got[0].Command != "sleep 400; echo done" {
		t.Errorf("shell task = %+v", got[0])
	}
	if got[2].AgentType != "Explore" || got[2].Description != "Probe: read-only code sweep" {
		t.Errorf("subagent task = %+v", got[2])
	}
	if want := int64(1792515482000); got[2].StartedAt != want || got[2].OpenedAt != want {
		t.Errorf("subagent timestamps = %d/%d, want %d", got[2].StartedAt, got[2].OpenedAt, want)
	}
}

func TestScanTasksNotificationClosesWhateverItsStatus(t *testing.T) {
	note := strings.Replace(taskNotification(t3, "a63f2a8dad6f0e4ef"), "completed", "killed", 1)
	p := writeTaskTranscript(t, agentLaunch, agentResult, shellLaunch, shellResult, note)
	if got := kinds(ScanTasks(p)); got != "shell:bly8ijzgx" {
		t.Fatalf("after notification: %s", got)
	}
}

func TestScanTasksNotificationForUnknownIDChangesNothing(t *testing.T) {
	p := writeTaskTranscript(t, shellLaunch, shellResult, taskNotification(t3, "nope"))
	if got := kinds(ScanTasks(p)); got != "shell:bly8ijzgx" {
		t.Fatalf("got %s", got)
	}
}

func TestScanTasksTaskStopCloses(t *testing.T) {
	stop := taskUse(t3, "tu-stop", "TaskStop", `{"task_id":"bly8ijzgx"}`)
	p := writeTaskTranscript(t, shellLaunch, shellResult, monitorLaunch, monitorResult, stop)
	if got := kinds(ScanTasks(p)); got != "monitor:bzuy1s169" {
		t.Fatalf("after stop: %s", got)
	}
	legacy := taskUse(t4, "tu-stop2", "TaskStop", `{"shell_id":"bzuy1s169"}`)
	p = writeTaskTranscript(t, shellLaunch, shellResult, monitorLaunch, monitorResult, stop, legacy)
	if got := kinds(ScanTasks(p)); got != "" {
		t.Fatalf("after legacy stop: %s", got)
	}
}

func TestScanTasksSendMessageReopensClosedSubagent(t *testing.T) {
	send := taskUse(t4, "tu-send", "SendMessage", `{"to":"a63f2a8dad6f0e4ef","message":"one more thing"}`)
	p := writeTaskTranscript(t, agentLaunch, agentResult, taskNotification(t3, "a63f2a8dad6f0e4ef"), send)
	got := ScanTasks(p)
	if kinds(got) != "subagent:a63f2a8dad6f0e4ef" {
		t.Fatalf("after resume: %s", kinds(got))
	}
	if got[0].OpenedAt <= got[0].StartedAt {
		t.Errorf("OpenedAt %d should move to the resume, StartedAt %d stays", got[0].OpenedAt, got[0].StartedAt)
	}
	// a message to an id that was never a task opens nothing
	p = writeTaskTranscript(t, taskUse(t4, "tu-send", "SendMessage", `{"to":"hermes-x","message":"hi"}`))
	if got := kinds(ScanTasks(p)); got != "" {
		t.Fatalf("stray SendMessage opened %s", got)
	}
}

func TestScanTasksIgnoresForegroundNamedAndSidechain(t *testing.T) {
	fg := taskUse(t0, "tu-fg", "Bash", `{"command":"ls","description":"list"}`)
	fgResult := taskResultText(t0, "tu-fg", "a\nb\n")
	named := taskUse(t1, "tu-named", "Agent", `{"name":"hermes-x","subagent_type":"general-purpose","description":"render","prompt":"p"}`)
	namedResult := taskResultText(t1, "tu-named", "Spawned successfully.\nagent_id: hermes-x@session-e77bff30\nname: hermes-x")
	side := strings.Replace(shellLaunch, `"type":"assistant"`, `"type":"assistant","isSidechain":true`, 1)
	p := writeTaskTranscript(t, fg, fgResult, named, namedResult, side, shellResult)
	if got := kinds(ScanTasks(p)); got != "" {
		t.Fatalf("opened %s", got)
	}
}

func TestScanTasksMalformedAndOversizedLines(t *testing.T) {
	p := writeTaskTranscript(t, "{not json", shellLaunch, "", shellResult)
	if got := kinds(ScanTasks(p)); got != "shell:bly8ijzgx" {
		t.Fatalf("malformed line lost the rest: %s", got)
	}
	huge := `{"type":"user","message":{"content":"` + strings.Repeat("x", 17*1024*1024) + `"}}`
	p = writeTaskTranscript(t, shellLaunch, shellResult, huge)
	if got := ScanTasks(p); got != nil {
		t.Fatalf("truncated scan reported %s", kinds(got))
	}
	if got := ScanTasks(filepath.Join(t.TempDir(), "missing.jsonl")); got != nil {
		t.Fatalf("missing file reported %s", kinds(got))
	}
}

func TestScanTasksCachesOnSizeAndMtime(t *testing.T) {
	p := writeTaskTranscript(t, shellLaunch, shellResult)
	if got := kinds(ScanTasks(p)); got != "shell:bly8ijzgx" {
		t.Fatal(got)
	}
	// rewrite with identical size and mtime: the cache answers, the file is not re-read
	info, _ := os.Stat(p)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", int(info.Size()))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if got := kinds(ScanTasks(p)); got != "shell:bly8ijzgx" {
		t.Fatalf("cache miss on unchanged key: %s", got)
	}
	// the returned slice is a copy: mutating it must not poison the cache
	got := ScanTasks(p)
	got[0].ID = "mutated"
	if ScanTasks(p)[0].ID != "bly8ijzgx" {
		t.Fatal("cache entry mutated through the returned slice")
	}
}

// --- prune ---

func writeMeta(t *testing.T, dir, id, body string, mtime time.Time) {
	t.Helper()
	path := filepath.Join(dir, "agent-"+id+".meta.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestLiveTasksDropsLaunchesBeforeProcessStart(t *testing.T) {
	open := []Task{{ID: "old", Kind: TaskMonitor, StartedAt: 100, OpenedAt: 100, Command: "zzz"}, {ID: "new", Kind: TaskMonitor, StartedAt: 300, OpenedAt: 300, Command: "zzz"}}
	children := map[int][]int{7: {8}}
	cmds := map[int]string{8: "/bin/zsh -c source snap && eval 'zzz'"}
	got := LiveTasks(open, 200, "", 7, children, cmds, time.Now())
	if kinds(got) != "monitor:new" {
		t.Fatalf("got %s", kinds(got))
	}
	// a zero process start skips the rule
	if got := LiveTasks(open, 0, "", 7, children, cmds, time.Now()); len(got) != 2 {
		t.Fatalf("zero start pruned: %s", kinds(got))
	}
}

func TestLiveTasksSubagentMetaRules(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	opened := now.Add(-time.Minute).UnixMilli()
	writeMeta(t, dir, "fin", `{"agentType":"general-purpose","model":"opus","finished":true}`, now)
	writeMeta(t, dir, "run", `{"agentType":"general-purpose","model":"opus"}`, now)
	// finished before the task's latest open event: a resumed agent
	writeMeta(t, dir, "resumed", `{"agentType":"Explore","finished":true}`, now.Add(-2*time.Minute))
	open := []Task{
		{ID: "fin", Kind: TaskSubagent, OpenedAt: opened},
		{ID: "run", Kind: TaskSubagent, OpenedAt: opened},
		{ID: "resumed", Kind: TaskSubagent, OpenedAt: opened, AgentType: "Explore"},
		{ID: "fresh", Kind: TaskSubagent, OpenedAt: now.Add(-5 * time.Second).UnixMilli()},
		{ID: "lost", Kind: TaskSubagent, OpenedAt: opened},
	}
	got := LiveTasks(open, 0, dir, 1, nil, nil, now)
	if kinds(got) != "subagent:run,subagent:resumed,subagent:fresh" {
		t.Fatalf("got %s", kinds(got))
	}
	if got[0].AgentType != "general-purpose" || got[0].Model != "opus" {
		t.Errorf("meta did not fill the task: %+v", got[0])
	}
	// no sidecar dir known: the ledger stands
	if got := LiveTasks(open, 0, "", 1, nil, nil, now); len(got) != 5 {
		t.Fatalf("empty dir pruned: %s", kinds(got))
	}
}

func TestLiveTasksShellNeedsMatchingChild(t *testing.T) {
	wrapper := "/bin/zsh -c source /Users/x/.claude/shell-snapshots/snap.sh 2>/dev/null || true && setopt NO_EXTENDED_GLOB && eval 'sleep 400; echo done'"
	children := map[int][]int{20873: {29852, 31031}}
	cmds := map[int]string{29852: wrapper, 31031: "/bin/zsh -c source snap && eval 'ps -axo pid'"}
	open := []Task{
		{ID: "live", Kind: TaskShell, Command: "sleep 400; echo done"},
		{ID: "gone", Kind: TaskMonitor, Command: "until [ -s /tmp/r ]; do sleep 15; done"},
		{ID: "quoted", Kind: TaskShell, Command: "'odd' start"}, // no matchable prefix: kept on the ledger's word
		{ID: "multiline", Kind: TaskShell, Command: "sleep   400;\necho tail"},
	}
	got := LiveTasks(open, 0, "", 20873, children, cmds, time.Now())
	if kinds(got) != "shell:live,shell:quoted,shell:multiline" {
		t.Fatalf("got %s", kinds(got))
	}
	// a grandchild does not count: the Bash tool's shell is a direct child
	children = map[int][]int{20873: {1}, 1: {29852}}
	if got := LiveTasks(open[:1], 0, "", 20873, children, cmds, time.Now()); len(got) != 0 {
		t.Fatalf("grandchild matched: %s", kinds(got))
	}
}

func TestCommandPrefix(t *testing.T) {
	cases := map[string]string{
		"sleep 400; echo done":         "sleep 400; echo done",
		"a  b\t c\nsecond line":        "a b c",
		"grep -n 'x' f":                "grep -n",
		"'quoted first'":               "",
		strings.Repeat("y", 60):        strings.Repeat("y", 48),
		"   leading space   and more ": "leading space and more",
	}
	for in, want := range cases {
		if got := commandPrefix(in); got != want {
			t.Errorf("commandPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSidecarDir(t *testing.T) {
	if got := SidecarDir("/p/proj/abc.jsonl"); got != "/p/proj/abc/subagents" {
		t.Fatal(got)
	}
	if got := SidecarDir(""); got != "" {
		t.Fatal(got)
	}
}

func TestTaskSummary(t *testing.T) {
	if got := TaskSummary(nil); got != "" {
		t.Fatalf("empty = %q", got)
	}
	tasks := []Task{{Kind: TaskShell}, {Kind: TaskSubagent}, {Kind: TaskSubagent}, {Kind: TaskMonitor}}
	if got := TaskSummary(tasks); got != "2 subagents · 1 monitor · 1 shell" {
		t.Fatalf("got %q", got)
	}
}
