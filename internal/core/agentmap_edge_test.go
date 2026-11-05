package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentmap_edge_test.go — athena-edge's blind suite for the agent map's core
// half (50-contract.md G1 + G2): malformed and empty tool input, command lines
// made only of glue, labels that must never carry arguments or message text,
// duplicate tool_use ids, calls with no result, harness-noise user records, a
// missing transcript, n out of range, and NewestAgentTranscript's refusals.

const agentMapEdgeBaseMS int64 = 1_793_000_000_000

func agentMapEdgeTS(ms int64) string {
	return time.UnixMilli(ms).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

func agentMapEdgeCall(ms int64, id, name, input string) string {
	return `{"type":"assistant","timestamp":"` + agentMapEdgeTS(ms) + `","uuid":"a-` + id + `","message":{"role":"assistant","content":[{"type":"tool_use","id":"` + id + `","name":"` + name + `","input":` + input + `}]}}`
}

func agentMapEdgeResult(ms int64, id string) string {
	return `{"type":"user","timestamp":"` + agentMapEdgeTS(ms) + `","uuid":"r-` + id + `","message":{"role":"user","content":[{"tool_use_id":"` + id + `","type":"tool_result","content":"ok"}]}}`
}

func agentMapEdgeUserText(ms int64, uuid, text string, meta bool) string {
	b, _ := json.Marshal(text)
	extra := ""
	if meta {
		extra = `"isMeta":true,`
	}
	return `{"type":"user",` + extra + `"timestamp":"` + agentMapEdgeTS(ms) + `","uuid":"` + uuid + `","message":{"role":"user","content":` + string(b) + `}}`
}

func agentMapEdgeWrite(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return p
}

func agentMapEdgeIsActivity(act string) bool {
	for _, a := range ActivityIDs {
		if a == act {
			return true
		}
	}
	return false
}

// G1: act is a member other than think and never empty, and malformed or empty
// input never panics, whatever the tool.
func TestAgentMapEdgeClassifyMalformedInputNeverPanics(t *testing.T) {
	inputs := []json.RawMessage{
		nil,
		json.RawMessage(``),
		json.RawMessage(`null`),
		json.RawMessage(`{`),
		json.RawMessage(`[1,2,3]`),
		json.RawMessage(`"just a string"`),
		json.RawMessage(`{"command":42}`),
		json.RawMessage(`{"file_path":{"nested":true}}`),
		json.RawMessage(`{"command":null,"file_path":null}`),
	}
	tools := []string{"Bash", "Edit", "Write", "NotebookEdit", "Read", "Grep", "SendMessage", "Agent", "TaskStop", "", "mcp__x__y", "Unknown"}
	for _, tool := range tools {
		for _, in := range inputs {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("ClassifyToolCall(%q, %q) panicked: %v", tool, string(in), r)
					}
				}()
				act, _ := ClassifyToolCall(tool, in)
				if act == "" || act == "think" || !agentMapEdgeIsActivity(act) {
					t.Errorf("ClassifyToolCall(%q, %q) act = %q, want a non-think ActivityIDs member", tool, string(in), act)
				}
			}()
		}
	}
}

// G1: a non-Bash tool is classified by NAME, so a malformed input does not
// change its act.
func TestAgentMapEdgeClassifyByNameSurvivesMalformedInput(t *testing.T) {
	for tool, want := range map[string]string{
		"Edit": "edit", "Write": "edit", "NotebookEdit": "edit", "Read": "read",
		"Grep": "search", "Glob": "search", "ToolSearch": "search",
		"Agent": "delegate", "Task": "delegate", "TaskStop": "delegate", "ListAgents": "delegate", "Workflow": "delegate",
		"SendMessage": "talk", "Monitor": "wait", "ScheduleWakeup": "wait", "ReadNotifications": "wait",
		"WebFetch": "web", "WebSearch": "web",
	} {
		if act, _ := ClassifyToolCall(tool, json.RawMessage(`{`)); act != want {
			t.Errorf("ClassifyToolCall(%q, malformed) act = %q, want %q", tool, act, want)
		}
	}
}

// G1: a Bash input with no usable command classifies as nothing → other/shell.
func TestAgentMapEdgeBashWithNoCommandIsShell(t *testing.T) {
	for _, in := range []string{`{}`, `{"command":""}`, `{"command":"   "}`, `{"command":42}`, `{`, `null`, ``} {
		act, label := ClassifyToolCall("Bash", json.RawMessage(in))
		if act != "other" || label != "shell" {
			t.Errorf("Bash %q = (%q, %q), want (other, shell)", in, act, label)
		}
	}
}

// G1: a command line made only of glue (separators, cd, env assignments, the
// dropped prefix words) matches no rule → other/shell.
func TestAgentMapEdgeBashGlueOnlyIsShell(t *testing.T) {
	for _, cmd := range []string{
		"cd /tmp",
		"cd /a && cd /b",
		"cd /a; cd /b || cd /c",
		";;",
		"&& ||",
		"\n\n",
		" ; \n ; ",
		"FOO=1",
		"FOO=1 BAR=2",
		"sudo",
		"nohup",
		"!",
		"FOO=1 sudo time command exec nohup",
		"cd /tmp; echo hi",
	} {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		act, label := ClassifyToolCall("Bash", b)
		if act != "other" || label != "shell" {
			t.Errorf("Bash %q = (%q, %q), want (other, shell)", cmd, act, label)
		}
	}
}

// G1: leading VAR=value words and the dropped prefix words are not the
// program; the command behind them is classified.
func TestAgentMapEdgeBashDroppedPrefixWords(t *testing.T) {
	for _, tc := range []struct{ cmd, act, label string }{
		{"FOO=1 BAR=2 go test ./...", "test", "go test"},
		{"nohup go test ./...", "test", "go test"},
		{"sudo make build", "build", "make build"},
		{"exec go vet ./...", "check", "go vet"},
		{"command sqlite3 x.db 'select 1'", "data", "sqlite3"},
		{"time go test ./...", "test", "go test"},
		{"FOO=1 nohup time go test ./...", "test", "go test"},
	} {
		b, _ := json.Marshal(map[string]string{"command": tc.cmd})
		act, label := ClassifyToolCall("Bash", b)
		if act != tc.act || label != tc.label {
			t.Errorf("Bash %q = (%q, %q), want (%q, %q)", tc.cmd, act, label, tc.act, tc.label)
		}
	}
}

// G1: between two commands of equal strength the FIRST wins, whichever order.
func TestAgentMapEdgeBashEqualStrengthFirstWins(t *testing.T) {
	for _, tc := range []struct{ cmd, label string }{
		{"ls -la; sed -n 1,5p f.go", "ls"},
		{"sed -n 1,5p f.go; ls -la", "sed -n"},
		{"ls -la | sed -n 1p", "ls"},
	} {
		b, _ := json.Marshal(map[string]string{"command": tc.cmd})
		act, label := ClassifyToolCall("Bash", b)
		if act != "read" || label != tc.label {
			t.Errorf("Bash %q = (%q, %q), want (read, %q)", tc.cmd, act, label, tc.label)
		}
	}
}

// G1: a Bash label never carries a path argument, a flag value, a message or
// any other text from the command line.
func TestAgentMapEdgeBashLabelCarriesNoArguments(t *testing.T) {
	for _, tc := range []struct{ cmd, label string }{
		{"go test -run TestSecretName ./secretpkg/...", "go test"},
		{"gh pr create --title 'secret title words' --body 'secret body words'", "gh pr create"},
		{"git -C /secret/repo/path diff --stat", "git diff"},
		{"kraken s zeus-other-cell 'secret private message to the cell'", "kraken s"},
		{"kraken mm get --q secretquery", "kraken mm get"},
		{"kraken cell submit secret-job-slug", "kraken cell submit"},
		{"sqlite3 /secret/kraken.db 'select secretcolumn from t'", "sqlite3"},
		{"make build SECRETVAR=1", "make build"},
		{"python3 /secret/scripts/run.py --flag secretvalue", "python"},
		{"cd /secret/dir && sed -i '' 's/secretold/secretnew/' /secret/file.go", "sed -i"},
	} {
		b, _ := json.Marshal(map[string]string{"command": tc.cmd})
		_, label := ClassifyToolCall("Bash", b)
		if label != tc.label {
			t.Errorf("Bash %q label = %q, want %q", tc.cmd, label, tc.label)
		}
		if strings.Contains(strings.ToLower(label), "secret") || strings.Contains(label, "/") {
			t.Errorf("Bash %q label %q leaks command-line text", tc.cmd, label)
		}
	}
	// a label that is not pinned by name still never leaks the arguments
	for _, cmd := range []string{
		"git commit -m 'secret commit message'",
		"curl -H 'Authorization: Bearer secrettoken' https://secret.example/x",
		"echo secretvalue > /secret/out.txt",
		"until [ -s /secret/a ]; do sleep 5; done",
	} {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		_, label := ClassifyToolCall("Bash", b)
		if strings.Contains(strings.ToLower(label), "secret") || strings.Contains(strings.ToLower(label), "bearer") {
			t.Errorf("Bash %q label %q leaks command-line text", cmd, label)
		}
	}
}

// G1: the mcp name families and the prefix strip on the fallback.
func TestAgentMapEdgeClassifyMCPNames(t *testing.T) {
	for _, tc := range []struct{ name, act, label string }{
		{"mcp__plugin_playwright_playwright__browser_click", "web", "browser"},
		{"mcp__claude-in-chrome__navigate", "web", "browser"},
		{"mcp__supabase-query__query_prod_table", "data", "supabase"},
		{"mcp__linear__get_issue", "other", "get_issue"},
		{"mcp__sentry__authenticate", "other", "authenticate"},
		{"Skill", "other", "Skill"},
	} {
		act, label := ClassifyToolCall(tc.name, json.RawMessage(`{}`))
		if act != tc.act || label != tc.label {
			t.Errorf("ClassifyToolCall(%q) = (%q, %q), want (%q, %q)", tc.name, act, label, tc.act, tc.label)
		}
	}
	if act, _ := ClassifyToolCall("mcp__context7__query-docs", json.RawMessage(`{}`)); act != "web" {
		t.Errorf("context7 act = %q, want web", act)
	}
	if act, _ := ClassifyToolCall("", json.RawMessage(`{}`)); act != "other" {
		t.Errorf("empty tool name act = %q, want other", act)
	}
}

// G1: the edit/read labels take the BASE name only, never the directory.
func TestAgentMapEdgeFileLabelsTakeBaseNameOnly(t *testing.T) {
	act, label := ClassifyToolCall("Write", json.RawMessage(`{"file_path":"/secret/deep/dir/out.md","content":"secret body"}`))
	if act != "edit" || label != "Write out.md" {
		t.Errorf("Write = (%q, %q), want (edit, Write out.md)", act, label)
	}
	act, label = ClassifyToolCall("NotebookEdit", json.RawMessage(`{"file_path":"/x/y/nb.ipynb","new_source":"secret"}`))
	if act != "edit" || label != "NotebookEdit nb.ipynb" {
		t.Errorf("NotebookEdit = (%q, %q), want (edit, NotebookEdit nb.ipynb)", act, label)
	}
	act, label = ClassifyToolCall("Read", json.RawMessage(`{"file_path":"relative/name.go"}`))
	if act != "read" || label != "Read name.go" {
		t.Errorf("Read = (%q, %q), want (read, Read name.go)", act, label)
	}
	// Grep's label is the tool name: its pattern and path never ride along
	act, label = ClassifyToolCall("Grep", json.RawMessage(`{"pattern":"secretpattern","path":"/secret"}`))
	if act != "search" || label != "Grep" {
		t.Errorf("Grep = (%q, %q), want (search, Grep)", act, label)
	}
}

// G2: a path that does not exist is (nil, nil).
func TestAgentMapEdgeTailEventsMissingPath(t *testing.T) {
	evs, err := TailEvents(filepath.Join(t.TempDir(), "nope.jsonl"), 10)
	if err != nil || evs != nil {
		t.Fatalf("TailEvents(missing) = (%v, %v), want (nil, nil)", evs, err)
	}
}

// G2: n < 1 is treated as 1 — the newest event alone.
func TestAgentMapEdgeTailEventsNBelowOne(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "t1", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+1000, "t2", "Glob", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+2000, "t3", "Read", `{"file_path":"/a/z.go"}`),
	)
	for _, n := range []int{0, -1, -1000} {
		evs, err := TailEvents(p, n)
		if err != nil {
			t.Fatalf("TailEvents(n=%d): %v", n, err)
		}
		if len(evs) != 1 || evs[0].ID != "t3" {
			t.Fatalf("TailEvents(n=%d) = %+v, want only the newest event t3", n, evs)
		}
	}
}

// G2: n larger than the transcript returns everything, oldest first.
func TestAgentMapEdgeTailEventsNAboveCount(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "t1", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+1000, "t2", "Glob", `{}`),
	)
	evs, err := TailEvents(p, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].ID != "t1" || evs[1].ID != "t2" {
		t.Fatalf("events = %+v, want t1 then t2", evs)
	}
}

// G2: a line that is not valid JSON is skipped — including a torn final line
// of a transcript caught mid-write — and the rest still read.
func TestAgentMapEdgeTailEventsSkipsInvalidLines(t *testing.T) {
	p := agentMapEdgeWrite(t,
		`not json at all`,
		agentMapEdgeCall(agentMapEdgeBaseMS, "t1", "Grep", `{}`),
		`{"type":"assistant",`,
		``,
		`[]`,
		agentMapEdgeCall(agentMapEdgeBaseMS+1000, "t2", "Glob", `{}`),
		`{"type":"assistant","timestamp":"`+agentMapEdgeTS(agentMapEdgeBaseMS+2000)+`","message":{"content":[{"type":"tool_use","id":"t3","na`,
	)
	evs, err := TailEvents(p, 10)
	if err != nil {
		t.Fatalf("TailEvents: %v", err)
	}
	if len(evs) != 2 || evs[0].ID != "t1" || evs[1].ID != "t2" {
		t.Fatalf("events = %+v, want exactly t1 and t2", evs)
	}
}

// G2: one event per distinct tool_use id, however many records repeat it.
func TestAgentMapEdgeTailEventsDedupesRepeatedToolUseID(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "dup", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS, "dup", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+5, "dup", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+10, "other", "Glob", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+20, "dup", "Grep", `{}`),
	)
	evs, err := TailEvents(p, 50)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, e := range evs {
		seen[e.ID]++
	}
	if len(evs) != 2 || seen["dup"] != 1 || seen["other"] != 1 {
		t.Fatalf("events = %+v, want one event each for dup and other", evs)
	}
}

// G2: a call with no result among the lines read is Open with DurMS 0; a
// result makes it closed with the timestamp difference.
func TestAgentMapEdgeTailEventsOpenCall(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "done", "Grep", `{}`),
		agentMapEdgeResult(agentMapEdgeBaseMS+1500, "done"),
		agentMapEdgeCall(agentMapEdgeBaseMS+2000, "pending", "Bash", `{"command":"go test ./..."}`),
		agentMapEdgeResult(agentMapEdgeBaseMS+2500, "someone-else"),
	)
	evs, err := TailEvents(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v, want two calls and no hear for tool_result records", evs)
	}
	if evs[0].ID != "done" || evs[0].Open || evs[0].DurMS != 1500 {
		t.Errorf("closed call = %+v, want Open false, DurMS 1500", evs[0])
	}
	if evs[1].ID != "pending" || !evs[1].Open || evs[1].DurMS != 0 {
		t.Errorf("open call = %+v, want Open true, DurMS 0", evs[1])
	}
	if evs[1].T != agentMapEdgeBaseMS+2000 {
		t.Errorf("open call T = %d, want %d", evs[1].T, agentMapEdgeBaseMS+2000)
	}
}

// G2: the harness-noise prefixes, isMeta records and tool results yield no
// event at all.
func TestAgentMapEdgeTailEventsHarnessNoiseYieldsNothing(t *testing.T) {
	noise := []string{
		"<command-name>/clear</command-name>",
		"<local-command-stdout>ok</local-command-stdout>",
		"This session is being continued from a previous conversation",
		"Caveat: The messages below were generated by the user",
		"<system-reminder>hi</system-reminder>",
		"<bash-input>ls</bash-input>",
		"<bash-stdout>x</bash-stdout>",
		"[Image: source: /tmp/a.png]",
		"[Request interrupted by user]",
		"[Request interrupted by user for tool use]",
	}
	var lines []string
	for i, txt := range noise {
		lines = append(lines, agentMapEdgeUserText(agentMapEdgeBaseMS+int64(i), "n"+string(rune('a'+i)), txt, false))
	}
	lines = append(lines, agentMapEdgeUserText(agentMapEdgeBaseMS+100, "meta", "a meta prompt that is not the operator", true))
	lines = append(lines, agentMapEdgeResult(agentMapEdgeBaseMS+200, "unknown-call"))
	evs, err := TailEvents(agentMapEdgeWrite(t, lines...), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("events = %+v, want none from harness noise, isMeta and tool results", evs)
	}
}

// G2: the hear classifier's boundaries: `[kraken ` past the first 90
// characters is the operator; `[from ` without `cell <slug>]` before the first
// `]` is the operator; a cell mention after that `]` does not count.
func TestAgentMapEdgeTailEventsHearBoundaries(t *testing.T) {
	far := strings.Repeat("x", 100) + " [kraken notice] late"
	p := agentMapEdgeWrite(t,
		agentMapEdgeUserText(agentMapEdgeBaseMS, "u1", "[kraken pr-watch] your PR has a failing check", false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+1, "u2", far, false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+2, "u3", "[from laptop · carl] look at cell other]", false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+3, "u4", "[from laptop · carl · cell other-job] hello there", false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+4, "u5", "[from laptop] plain", false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+5, "u6", "please read <task-notification> later", false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+6, "u7", "<task-notification><task-id>b1</task-id></task-notification>", false),
	)
	evs, err := TailEvents(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 7 {
		t.Fatalf("events = %+v, want one hear per text record", evs)
	}
	want := []struct{ id, from, who, label string }{
		{"u1", "kraken", "", "notice from kraken"},
		{"u2", "you", "", "prompt from you"},
		{"u3", "you", "", "prompt from you"},
		{"u4", "cell", "other-job", "from cell other-job"},
		{"u5", "you", "", "prompt from you"},
		{"u6", "you", "", "prompt from you"},
		{"u7", "task", "", "task finished"},
	}
	for i, w := range want {
		e := evs[i]
		if e.ID != w.id || e.Act != "hear" || e.From != w.from || e.Label != w.label {
			t.Errorf("event %d = %+v, want id %s act hear from %s label %q", i, e, w.id, w.from, w.label)
		}
		if w.who != "" && e.Who != w.who {
			t.Errorf("event %d Who = %q, want %q", i, e.Who, w.who)
		}
	}
}

// G2: no label, Who or other field ever carries message or prompt text.
func TestAgentMapEdgeTailEventsNeverCarryText(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeUserText(agentMapEdgeBaseMS, "u1", "secretprompt do the secret thing", false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+1, "u2", `<teammate-message teammate_id="heph-1" color="blue">secretreport body</teammate-message>`, false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+2, "u3", "[kraken notice] secretnotice text", false),
		agentMapEdgeUserText(agentMapEdgeBaseMS+3, "u4", "[from mac · cell peer-job] secretcellmessage", false),
		agentMapEdgeCall(agentMapEdgeBaseMS+4, "t1", "SendMessage", `{"to":"heph-1","message":"secretmessage body","summary":"secretsummary"}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+5, "t2", "Agent", `{"name":"heph-2","prompt":"secretbrief","description":"build it"}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+6, "t3", "Bash", `{"command":"git commit -m 'secretcommit'"}`),
	)
	evs, err := TailEvents(p, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 7 {
		t.Fatalf("events = %+v, want 7", evs)
	}
	for _, e := range evs {
		raw, _ := json.Marshal(e)
		if strings.Contains(strings.ToLower(string(raw)), "secret") {
			t.Errorf("event %s carries message text: %s", e.ID, raw)
		}
	}
	if evs[1].From != "teammate" || evs[1].Who != "heph-1" || evs[1].Label != "from heph-1" {
		t.Errorf("teammate hear = %+v, want from teammate, Who heph-1, label from heph-1", evs[1])
	}
}

// G2: the four overrides' fallbacks — SendMessage's recipient, TaskStop's
// agent_name — and Agent's description arm.
func TestAgentMapEdgeTailEventsOverrideFallbacks(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "s1", "SendMessage", `{"recipient":"athena-edge","message":"x"}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+1, "s2", "TaskStop", `{"agent_name":"heph-3"}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+2, "s3", "Agent", `{"description":"scan the repo","prompt":"x"}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+3, "s4", "TaskStop", `{"task_id":"b7","agent_name":"heph-4"}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+4, "s5", "SendMessage", `{"to":"zeus","recipient":"other"}`),
	)
	evs, err := TailEvents(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 {
		t.Fatalf("events = %+v, want 5", evs)
	}
	want := []struct{ label, who string }{
		{"to athena-edge", "athena-edge"},
		{"stop heph-3", "heph-3"},
		{"subagent: scan the repo", "scan the repo"},
		{"stop b7", "b7"},
		{"to zeus", "zeus"},
	}
	for i, w := range want {
		if evs[i].Label != w.label || evs[i].Who != w.who {
			t.Errorf("event %d = (%q, %q), want (%q, %q)", i, evs[i].Label, evs[i].Who, w.label, w.who)
		}
	}
}

// G2: an override on a malformed input never panics and keeps the tool's act.
func TestAgentMapEdgeTailEventsOverrideOnMalformedInput(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "m1", "SendMessage", `"not an object"`),
		agentMapEdgeCall(agentMapEdgeBaseMS+1, "m2", "Agent", `null`),
		agentMapEdgeCall(agentMapEdgeBaseMS+2, "m3", "TaskStop", `[1]`),
	)
	evs, err := TailEvents(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 {
		t.Fatalf("events = %+v, want 3", evs)
	}
	for i, want := range []string{"talk", "delegate", "delegate"} {
		if evs[i].Act != want {
			t.Errorf("event %d act = %q, want %q", i, evs[i].Act, want)
		}
	}
}

func agentMapEdgeNamed(t *testing.T, dir, file, agentName string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, file)
	head := `{"type":"user","agentName":"` + agentName + `","timestamp":"` + agentMapEdgeTS(agentMapEdgeBaseMS) + `","message":{"role":"user","content":"brief"}}`
	if err := os.WriteFile(p, []byte(head+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func agentMapEdgeAge(t *testing.T, p string, ago time.Duration) {
	t.Helper()
	at := time.Now().Add(-ago)
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// G2: nothing to search answers "".
func TestAgentMapEdgeNewestAgentTranscriptEmptyInputs(t *testing.T) {
	if got := NewestAgentTranscript(nil, "heph-1"); got != "" {
		t.Errorf("nil dirs = %q, want empty", got)
	}
	if got := NewestAgentTranscript([]string{}, "heph-1"); got != "" {
		t.Errorf("no dirs = %q, want empty", got)
	}
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	if got := NewestAgentTranscript([]string{missing}, "heph-1"); got != "" {
		t.Errorf("missing dir = %q, want empty", got)
	}
	empty := t.TempDir()
	if got := NewestAgentTranscript([]string{empty}, "heph-1"); got != "" {
		t.Errorf("empty dir = %q, want empty", got)
	}
}

// G2: files under subagents/ are never considered, nor files in any deeper
// directory, nor a non-.jsonl file.
func TestAgentMapEdgeNewestAgentTranscriptOnlyDirectJSONL(t *testing.T) {
	root := t.TempDir()
	agentMapEdgeNamed(t, filepath.Join(root, "sess", "subagents"), "agent-a1.jsonl", "heph-1")
	agentMapEdgeNamed(t, filepath.Join(root, "subagents"), "agent-a2.jsonl", "heph-1")
	agentMapEdgeNamed(t, filepath.Join(root, "deeper"), "x.jsonl", "heph-1")
	agentMapEdgeNamed(t, root, "notes.txt", "heph-1")
	agentMapEdgeNamed(t, root, "x.jsonl.bak", "heph-1")
	if got := NewestAgentTranscript([]string{root}, "heph-1"); got != "" {
		t.Fatalf("got %q, want empty: no direct *.jsonl names heph-1", got)
	}
	// and with the subagents dir handed in directly, still never
	if got := NewestAgentTranscript([]string{filepath.Join(root, "subagents")}, "heph-1"); got != "" {
		t.Fatalf("got %q from a subagents/ dir, want empty", got)
	}
}

// G2: agentName must be EQUAL and TOP-LEVEL: a prefix, a nested key or a
// mention in text does not match.
func TestAgentMapEdgeNewestAgentTranscriptExactTopLevelName(t *testing.T) {
	root := t.TempDir()
	agentMapEdgeNamed(t, root, "a.jsonl", "heph-10")
	agentMapEdgeNamed(t, root, "b.jsonl", "HEPH-1")
	nested := `{"type":"user","message":{"agentName":"heph-1","content":"x"}}`
	text := `{"type":"user","message":{"role":"user","content":"\"agentName\":\"heph-1\""}}`
	if err := os.WriteFile(filepath.Join(root, "c.jsonl"), []byte(nested+"\n"+text+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NewestAgentTranscript([]string{root}, "heph-1"); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

// G2: only the head (first 256 KB) is read: a record naming the agent that
// starts past it does not match.
func TestAgentMapEdgeNewestAgentTranscriptHeadBound(t *testing.T) {
	root := t.TempDir()
	pad := `{"type":"user","message":{"role":"user","content":"` + strings.Repeat("p", 300*1024) + `"}}`
	named := `{"type":"user","agentName":"heph-1","message":{"role":"user","content":"x"}}`
	if err := os.WriteFile(filepath.Join(root, "late.jsonl"), []byte(pad+"\n"+named+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := NewestAgentTranscript([]string{root}, "heph-1"); got != "" {
		t.Fatalf("got %q, want empty: the naming record starts past 256 KB", got)
	}
}

// G2: the most recently modified MATCHING file wins, even when a newer file
// that names someone else sits beside it, and across directories.
func TestAgentMapEdgeNewestAgentTranscriptNewestMatchAcrossDirs(t *testing.T) {
	d1, d2 := t.TempDir(), t.TempDir()
	old := agentMapEdgeNamed(t, d1, "old.jsonl", "heph-1")
	mid := agentMapEdgeNamed(t, d2, "mid.jsonl", "heph-1")
	other := agentMapEdgeNamed(t, d1, "other.jsonl", "heph-2")
	agentMapEdgeAge(t, old, 3*time.Hour)
	agentMapEdgeAge(t, mid, time.Hour)
	agentMapEdgeAge(t, other, time.Minute)
	if got := NewestAgentTranscript([]string{d1, d2}, "heph-1"); got != mid {
		t.Fatalf("got %q, want %q", got, mid)
	}
	// a missing dir in the list does not hide the others
	if got := NewestAgentTranscript([]string{filepath.Join(d1, "gone"), d2}, "heph-1"); got != mid {
		t.Fatalf("with a missing dir first: got %q, want %q", got, mid)
	}
}

// G1: ActivityIDs is exactly 17 distinct ids, and "hear" is not one of them.
func TestAgentMapEdgeActivityIDsShape(t *testing.T) {
	if len(ActivityIDs) != 17 {
		t.Fatalf("len(ActivityIDs) = %d, want 17", len(ActivityIDs))
	}
	seen := map[string]bool{}
	for _, a := range ActivityIDs {
		if a == "" || seen[a] {
			t.Fatalf("ActivityIDs has an empty or duplicate id %q", a)
		}
		seen[a] = true
	}
	if seen["hear"] {
		t.Fatal("hear is a wire act for arriving messages, not an ActivityIDs member")
	}
}

// --- amendment 6 ----------------------------------------------------------------

// G1 amendment 6: a separator inside quotes does not split the line, and no
// quoted word is ever a program or a subcommand.
func TestAgentMapEdgeBashQuotedSeparatorsDoNotSplit(t *testing.T) {
	for _, cmd := range []string{
		`echo "note; make sure the tokens rotate"`,
		`echo "status && git secretword rest"`,
		`printf 'x; kraken mm secretone secrettwo'`,
		`echo "a || go test ./..."`,
		`echo 'a | curl secret.example'`,
		"echo \"first line\ngo test ./...\"",
		`echo "a; b" 'c && make deploy'`,
	} {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		act, label := ClassifyToolCall("Bash", b)
		if act != "other" || label != "shell" {
			t.Errorf("Bash %q = (%q, %q), want (other, shell)", cmd, act, label)
		}
	}
}

// G1 amendment 6: a quoted separator does not hide or replace the real
// command around it.
func TestAgentMapEdgeBashQuotedSeparatorKeepsRealCommand(t *testing.T) {
	for _, tc := range []struct{ cmd, act, label string }{
		{`grep -n "a|curl" f.go`, "search", "grep"},
		{`sed -n '1;2p' f.go`, "read", "sed -n"},
		{`git commit -m "wip; make build later" && echo ok`, "ship", "git commit"},
		{`echo "x; make build" && go vet ./...`, "check", "go vet"},
		{`kraken s zeus-other "status: done; gh pr view secretword"`, "talk", "kraken s"},
	} {
		b, _ := json.Marshal(map[string]string{"command": tc.cmd})
		act, label := ClassifyToolCall("Bash", b)
		if act != tc.act || label != tc.label {
			t.Errorf("Bash %q = (%q, %q), want (%q, %q)", tc.cmd, act, label, tc.act, tc.label)
		}
	}
}

// G1 amendment 6: an unclosed quote never panics, and its label carries no
// word from inside the unclosed quote.
func TestAgentMapEdgeBashUnclosedQuote(t *testing.T) {
	for _, cmd := range []string{
		`echo "x; git secretword`,
		`echo 'x && make secretword`,
		`go test ./... && echo "secretword; kraken mm secretone`,
		`"`,
		`'`,
		`echo "\`,
	} {
		b, _ := json.Marshal(map[string]string{"command": cmd})
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Bash %q panicked: %v", cmd, r)
				}
			}()
			act, label := ClassifyToolCall("Bash", b)
			if act == "" || act == "think" || !agentMapEdgeIsActivity(act) {
				t.Errorf("Bash %q act = %q, want a non-think ActivityIDs member", cmd, act)
			}
			if strings.Contains(strings.ToLower(label), "secret") {
				t.Errorf("Bash %q label %q carries a word from inside the unclosed quote", cmd, label)
			}
		}()
	}
}

// G2 amendment 6: the teammate rule needs the tag at the START (after leading
// whitespace), an id closed by `"`, and a name-shaped id; anything else is the
// operator.
func TestAgentMapEdgeTeammateTagBoundaries(t *testing.T) {
	id64 := "a" + strings.Repeat("b", 63)
	id65 := "a" + strings.Repeat("b", 64)
	cases := []struct {
		text, from, who string
	}{
		{"can you check why <teammate-message teammate_id=\" breaks when my private note follows", "you", ""},
		{"quote: <teammate-message teammate_id=\"heph-1\">x</teammate-message>", "you", ""},
		{"<teammate-message teammate_id=\"this whole private sentence\">x</teammate-message>", "you", ""},
		{"<teammate-message teammate_id=\"unclosed private words", "you", ""},
		{"<teammate-message teammate_id=\"-dash-first\">x</teammate-message>", "you", ""},
		{"<teammate-message teammate_id=\"\">x</teammate-message>", "you", ""},
		{"<teammate-message teammate_id=\"" + id65 + "\">x</teammate-message>", "you", ""},
		{"<teammate-message teammate_id=\"a/b\">x</teammate-message>", "you", ""},
		{"  \n\t<teammate-message teammate_id=\"heph-1\" color=\"blue\">report</teammate-message>", "teammate", "heph-1"},
		{"<teammate-message teammate_id=\"" + id64 + "\">x</teammate-message>", "teammate", id64},
		{"<teammate-message teammate_id=\"team-lead@agent-map.v2_x\">x</teammate-message>", "teammate", "team-lead@agent-map.v2_x"},
		{"<teammate-message teammate_id=\"9\">x</teammate-message>", "teammate", "9"},
	}
	var lines []string
	for i, c := range cases {
		lines = append(lines, agentMapEdgeUserText(agentMapEdgeBaseMS+int64(i), "tt"+strings.Repeat("x", i), c.text, false))
	}
	evs, err := TailEvents(agentMapEdgeWrite(t, lines...), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != len(cases) {
		t.Fatalf("events = %+v, want %d", evs, len(cases))
	}
	for i, c := range cases {
		e := evs[i]
		if e.Act != "hear" || e.From != c.from || e.Who != c.who {
			t.Errorf("case %d (%q): from %q who %q, want from %q who %q", i, c.text, e.From, e.Who, c.from, c.who)
		}
		want := "prompt from you"
		if c.from == "teammate" {
			want = "from " + c.who
		}
		if e.Label != want {
			t.Errorf("case %d label = %q, want %q", i, e.Label, want)
		}
		if strings.Contains(e.Label, "private") || strings.Contains(e.Who, "private") {
			t.Errorf("case %d carries prompt text: %+v", i, e)
		}
	}
}

// G2 amendment 6: the cell rule's slug becomes Who only when it is one word of
// slug characters, at most 64; otherwise the text is the operator's.
func TestAgentMapEdgeCellSlugBoundaries(t *testing.T) {
	s64 := strings.Repeat("s", 64)
	s65 := strings.Repeat("s", 65)
	cases := []struct{ text, from, who string }{
		{"[from mac · cell my-job.v2_x] hi", "cell", "my-job.v2_x"},
		{"[from mac · cell " + s64 + "] hi", "cell", s64},
		{"[from mac · cell " + s65 + "] hi", "you", ""},
		{"[from mac · cell a/b] hi", "you", ""},
		{"[from mac · cell my private words] hi", "you", ""},
		{"[from mac · cell sl@g] hi", "you", ""},
		{"[from mac · cell ] hi", "you", ""},
	}
	var lines []string
	for i, c := range cases {
		lines = append(lines, agentMapEdgeUserText(agentMapEdgeBaseMS+int64(i), "cs"+strings.Repeat("x", i), c.text, false))
	}
	evs, err := TailEvents(agentMapEdgeWrite(t, lines...), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != len(cases) {
		t.Fatalf("events = %+v, want %d", evs, len(cases))
	}
	for i, c := range cases {
		e := evs[i]
		if e.From != c.from || e.Who != c.who {
			t.Errorf("case %d (%q): from %q who %q, want from %q who %q", i, c.text, e.From, e.Who, c.from, c.who)
		}
		if c.from == "you" && e.Label != "prompt from you" {
			t.Errorf("case %d label = %q, want prompt from you", i, e.Label)
		}
	}
}

// G2 amendment 6 (one-prefix clause, amendment 7): exactly the harness line
// `Another Claude session sent a message:` followed by whitespace may precede
// the tag; any other prefix, a variant of that line, or the line without
// whitespace falls through to the operator.
func TestAgentMapEdgeTeammateHarnessPrefix(t *testing.T) {
	const hp = "Another Claude session sent a message:"
	tag := `<teammate-message teammate_id="heph-1">report</teammate-message>`
	cases := []struct{ text, from, who string }{
		{hp + " " + tag, "teammate", "heph-1"},
		{hp + "\n" + tag, "teammate", "heph-1"},
		{"  " + hp + "\n\n\t" + tag, "teammate", "heph-1"},
		{hp + tag, "you", ""},
		{"another claude session sent a message: " + tag, "you", ""},
		{"Another Claude session sent a message " + tag, "you", ""},
		{"Another Claude session sent a message:: " + tag, "you", ""},
		{hp + " " + hp + " " + tag, "you", ""},
		{hp + " private words " + tag, "you", ""},
		{"Some other harness line: " + tag, "you", ""},
		{hp + " <teammate-message teammate_id=\"private words\">x</teammate-message>", "you", ""},
		{hp + " ", "you", ""},
	}
	var lines []string
	for i, c := range cases {
		lines = append(lines, agentMapEdgeUserText(agentMapEdgeBaseMS+int64(i), "hp"+strings.Repeat("x", i), c.text, false))
	}
	evs, err := TailEvents(agentMapEdgeWrite(t, lines...), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != len(cases) {
		t.Fatalf("events = %+v, want %d", evs, len(cases))
	}
	for i, c := range cases {
		e := evs[i]
		if e.Act != "hear" || e.From != c.from || e.Who != c.who {
			t.Errorf("case %d (%q): from %q who %q, want from %q who %q", i, c.text, e.From, e.Who, c.from, c.who)
		}
		if strings.Contains(e.Label, "private") || strings.Contains(e.Who, "private") {
			t.Errorf("case %d carries prompt text: %+v", i, e)
		}
	}
}

func agentMapEdgeAppend(t *testing.T, p string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
}

// agentMapEdgeFresh is what a first read of the same bytes answers: a copy of
// the file at a path TailEvents has never seen.
func agentMapEdgeFresh(t *testing.T, p string, n int) []AgentEvent {
	t.Helper()
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "fresh.jsonl")
	if err := os.WriteFile(cp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	evs, err := TailEvents(cp, n)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func agentMapEdgeSameEvents(t *testing.T, what string, got, want []AgentEvent) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("%s:\n got  %s\n want %s", what, g, w)
	}
}

// G2 amendment 6: after an append, the answer is a fresh read's — an open call
// whose result arrived comes back closed with its DurMS, new events appear, and
// a different n on the grown file is still right.
func TestAgentMapEdgeTailEventsAfterAppendMatchesFresh(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "a1", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+1000, "a2", "Bash", `{"command":"go test ./..."}`),
	)
	first, err := TailEvents(p, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || !first[1].Open {
		t.Fatalf("first = %+v, want a2 open", first)
	}
	agentMapEdgeAppend(t, p,
		agentMapEdgeResult(agentMapEdgeBaseMS+4000, "a2"),
		agentMapEdgeUserText(agentMapEdgeBaseMS+5000, "u1", "next please", false),
		agentMapEdgeCall(agentMapEdgeBaseMS+6000, "a3", "Read", `{"file_path":"/x/y.go"}`),
	)
	got, err := TailEvents(p, 24)
	if err != nil {
		t.Fatal(err)
	}
	agentMapEdgeSameEvents(t, "after append", got, agentMapEdgeFresh(t, p, 24))
	if len(got) != 4 || got[1].ID != "a2" || got[1].Open || got[1].DurMS != 3000 {
		t.Fatalf("after append = %+v, want a2 closed with DurMS 3000 among 4 events", got)
	}
	for _, n := range []int{1, 2, 60} {
		g, err := TailEvents(p, n)
		if err != nil {
			t.Fatal(err)
		}
		agentMapEdgeSameEvents(t, "n changed", g, agentMapEdgeFresh(t, p, n))
	}
}

// G2 amendment 6: a torn last line completed by a later append is read whole.
func TestAgentMapEdgeTailEventsTornLineCompletedLater(t *testing.T) {
	full := agentMapEdgeCall(agentMapEdgeBaseMS+1000, "t2", "Glob", `{}`)
	head := agentMapEdgeCall(agentMapEdgeBaseMS, "t1", "Grep", `{}`) + "\n" + full[:40]
	p := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(p, []byte(head), 0o644); err != nil {
		t.Fatal(err)
	}
	evs, err := TailEvents(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].ID != "t1" {
		t.Fatalf("torn = %+v, want only t1", evs)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(full[40:] + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	evs, err = TailEvents(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].ID != "t1" || evs[1].ID != "t2" {
		t.Fatalf("completed = %+v, want t1 then t2", evs)
	}
	agentMapEdgeSameEvents(t, "completed", evs, agentMapEdgeFresh(t, p, 10))
}

// G2 amendment 6: a file shorter than last seen — even with its mtime put back
// — is read afresh, never answered from what the longer file held.
func TestAgentMapEdgeTailEventsTruncatedOrReplaced(t *testing.T) {
	p := agentMapEdgeWrite(t,
		agentMapEdgeCall(agentMapEdgeBaseMS, "o1", "Grep", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+1000, "o2", "Glob", `{}`),
		agentMapEdgeCall(agentMapEdgeBaseMS+2000, "o3", "Read", `{"file_path":"/a.go"}`),
	)
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if evs, _ := TailEvents(p, 10); len(evs) != 3 {
		t.Fatalf("before = %+v, want 3", evs)
	}
	short := agentMapEdgeCall(agentMapEdgeBaseMS+5000, "n1", "Edit", `{"file_path":"/b.go"}`) + "\n"
	if err := os.WriteFile(p, []byte(short), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	evs, err := TailEvents(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].ID != "n1" {
		t.Fatalf("after truncation = %+v, want only n1", evs)
	}
	// and truncated to nothing
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	evs, err = TailEvents(p, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 0 {
		t.Fatalf("after emptying = %+v, want none", evs)
	}
	// removed: a missing path is (nil, nil) again
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if evs, err := TailEvents(p, 10); evs != nil || err != nil {
		t.Fatalf("after removal = (%v, %v), want (nil, nil)", evs, err)
	}
}

// G2 amendment 6: "the answer is always the one a fresh read of the whole file
// would give", whatever the size of its records — 30 calls each followed by a
// ~700 KB result (about 21 MB) still yield the newest 24, closed.
func TestAgentMapEdgeTailEventsHugeRecords(t *testing.T) {
	if testing.Short() {
		t.Skip("writes ~21 MB")
	}
	big, _ := json.Marshal(strings.Repeat("x", 700*1024))
	p := filepath.Join(t.TempDir(), "big.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		id := fmt.Sprintf("h%03d", i)
		ms := agentMapEdgeBaseMS + int64(i)*10_000
		f.WriteString(agentMapEdgeCall(ms, id, "Read", `{"file_path":"/a/big.png"}`) + "\n")
		f.WriteString(`{"type":"user","timestamp":"` + agentMapEdgeTS(ms+500) + `","uuid":"r` + id + `","message":{"role":"user","content":[{"tool_use_id":"` + id + `","type":"tool_result","content":` + string(big) + `}]}}` + "\n")
	}
	f.Close()
	evs, err := TailEvents(p, 24)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 24 || evs[0].ID != "h006" || evs[23].ID != "h029" {
		t.Fatalf("got %d events (%v..%v), want 24, h006..h029", len(evs), agentMapEdgeFirstID(evs), agentMapEdgeLastID(evs))
	}
	for _, e := range evs {
		if e.Open || e.DurMS != 500 {
			t.Fatalf("event %s = open %v dur %d, want closed with DurMS 500", e.ID, e.Open, e.DurMS)
		}
	}
	// a second call on the unchanged file answers the same
	again, err := TailEvents(p, 24)
	if err != nil {
		t.Fatal(err)
	}
	agentMapEdgeSameEvents(t, "second call", again, evs)
}

func agentMapEdgeFirstID(evs []AgentEvent) string {
	if len(evs) == 0 {
		return ""
	}
	return evs[0].ID
}

func agentMapEdgeLastID(evs []AgentEvent) string {
	if len(evs) == 0 {
		return ""
	}
	return evs[len(evs)-1].ID
}
