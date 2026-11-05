package core

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// agentmap_contract_test.go — athena-logic's blind tests for the agent map's
// core half (50-contract.md G1 and G2): the activity vocabulary, the tool-call
// classifier, TailEvents and NewestAgentTranscript. Every assertion cites the
// contract row it comes from.

// G1: "Exactly these 17, in this order".
func TestAgentMapContractActivityIDs(t *testing.T) {
	want := []string{"think", "edit", "search", "read", "run", "home", "delegate", "talk", "review", "test", "web", "wait", "check", "ship", "data", "build", "other"}
	if len(ActivityIDs) != len(want) {
		t.Fatalf("ActivityIDs = %v, want %v", ActivityIDs, want)
	}
	for i := range want {
		if ActivityIDs[i] != want[i] {
			t.Fatalf("ActivityIDs[%d] = %q, want %q (full: %v)", i, ActivityIDs[i], want[i], ActivityIDs)
		}
	}
}

func agentMapContractIsActivity(act string) bool {
	for _, a := range ActivityIDs {
		if a == act {
			return true
		}
	}
	return false
}

func agentMapContractBashInput(t *testing.T, cmd string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(map[string]string{"command": cmd})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// G1: the example table, row by row — "the Go port must agree".
func TestAgentMapContractClassifyExampleTable(t *testing.T) {
	type row struct {
		tool, input, act, label string
	}
	bash := []row{
		{"Bash", `cd /x/y && grep -rn foo internal | head -5`, "search", "grep"},
		{"Bash", `cd /repo; git add a.go && git commit -m 'x' && git push`, "ship", "git add"},
		{"Bash", `git -C /repo diff --stat`, "review", "git diff"},
		{"Bash", `go test -tags sqlite_fts5 ./... 2>&1 | tail -3`, "test", "go test"},
		{"Bash", `FOO=1 poetry run python -m pytest tests/ -q`, "test", "pytest"},
		{"Bash", `npx vitest run src/lib`, "test", "vitest"},
		{"Bash", `go vet ./... && gofmt -l .`, "check", "go vet"},
		{"Bash", `kraken mm get --q board`, "home", "kraken mm get"},
		{"Bash", `kraken s zeus-other-cell hello there`, "talk", "kraken s"},
		{"Bash", `kraken cell submit new-job`, "delegate", "kraken cell submit"},
		{"Bash", `sed -n 1,40p file.go`, "read", "sed -n"},
		{"Bash", `sed -i '' 's/a/b/' file.go`, "edit", "sed -i"},
		{"Bash", `python3 scripts/run.py --flag`, "run", "python"},
		{"Bash", `until [ -s /tmp/a ]; do sleep 5; done; echo READY`, "wait", "until"},
		{"Bash", `gh pr create --title x --body y`, "ship", "gh pr create"},
		{"Bash", `gh pr checks 12`, "wait", "gh pr checks"},
		{"Bash", `curl -s localhost:7777/api/board | jq .jobs`, "web", "curl"},
		{"Bash", `sqlite3 ~/.kraken/kraken.db 'select 1'`, "data", "sqlite3"},
		{"Bash", `make build`, "build", "make build"},
		{"Bash", `cd /tmp; echo hi`, "other", "shell"},
		{"Bash", `ls -la`, "read", "ls"},
	}
	for _, r := range bash {
		act, label := ClassifyToolCall(r.tool, agentMapContractBashInput(t, r.input))
		if act != r.act || label != r.label {
			t.Errorf("ClassifyToolCall(Bash, %q) = (%q, %q), want (%q, %q)", r.input, act, label, r.act, r.label)
		}
	}
	other := []row{
		{"Edit", `{"file_path":"/a/b/board.go"}`, "edit", "Edit board.go"},
		{"Read", `{"file_path":"/a/b/CLAUDE.md"}`, "read", "Read CLAUDE.md"},
		{"Skill", `{}`, "other", "Skill"},
		{"AskUserQuestion", `{}`, "other", "AskUserQuestion"},
	}
	for _, r := range other {
		act, label := ClassifyToolCall(r.tool, json.RawMessage(r.input))
		if act != r.act || label != r.label {
			t.Errorf("ClassifyToolCall(%s, %s) = (%q, %q), want (%q, %q)", r.tool, r.input, act, label, r.act, r.label)
		}
	}
}

// G1: "A tool other than Bash is classified by name", every named rule.
func TestAgentMapContractClassifyByToolName(t *testing.T) {
	cases := []struct {
		tool, input, act, label string
	}{
		{"Edit", `{"file_path":"/x/y/main.go"}`, "edit", "Edit main.go"},
		{"Write", `{"file_path":"/x/y/new.ts","content":"zzz"}`, "edit", "Write new.ts"},
		{"NotebookEdit", `{"file_path":"/x/nb.ipynb"}`, "edit", "NotebookEdit nb.ipynb"},
		{"Read", `{"file_path":"/deep/path/to/notes.md"}`, "read", "Read notes.md"},
		{"Grep", `{"pattern":"foo"}`, "search", "Grep"},
		{"Glob", `{"pattern":"**/*.go"}`, "search", "Glob"},
		{"ToolSearch", `{"query":"x"}`, "search", "ToolSearch"},
		{"Agent", `{"description":"d"}`, "delegate", "Agent"},
		{"Task", `{}`, "delegate", "Task"},
		{"TaskStop", `{"task_id":"t"}`, "delegate", "TaskStop"},
		{"ListAgents", `{}`, "delegate", "ListAgents"},
		{"Workflow", `{}`, "delegate", "Workflow"},
		{"SendMessage", `{"to":"heph","message":"hi"}`, "talk", "SendMessage"},
		{"Monitor", `{}`, "wait", "Monitor"},
		{"ScheduleWakeup", `{}`, "wait", "ScheduleWakeup"},
		{"ReadNotifications", `{}`, "wait", "ReadNotifications"},
		{"WebFetch", `{"url":"https://example.com"}`, "web", "WebFetch"},
		{"WebSearch", `{"query":"q"}`, "web", "WebSearch"},
		{"mcp__plugin_playwright_playwright__browser_click", `{}`, "web", "browser"},
		{"mcp__claude-in-chrome__navigate", `{}`, "web", "browser"},
		{"mcp__supabase-query__query_prod_table", `{}`, "data", "supabase"},
		{"mcp__linear__get_issue", `{}`, "other", "get_issue"},
		{"SomeUnknownTool", `{}`, "other", "SomeUnknownTool"},
	}
	for _, c := range cases {
		act, label := ClassifyToolCall(c.tool, json.RawMessage(c.input))
		if act != c.act || label != c.label {
			t.Errorf("ClassifyToolCall(%s) = (%q, %q), want (%q, %q)", c.tool, act, label, c.act, c.label)
		}
	}
	// context7: the act is stated, the label is not (contract silence).
	if act, _ := ClassifyToolCall("mcp__context7__query-docs", json.RawMessage(`{}`)); act != "web" {
		t.Errorf("context7 act = %q, want web", act)
	}
}

// G1: "act is always a member of ActivityIDs other than think; never empty".
func TestAgentMapContractClassifyActIsNeverThink(t *testing.T) {
	inputs := []struct {
		tool  string
		input json.RawMessage
	}{
		{"Bash", agentMapContractBashInput(t, "echo hi")},
		{"Bash", agentMapContractBashInput(t, "go build ./...")},
		{"Bash", agentMapContractBashInput(t, "")},
		{"Edit", json.RawMessage(`{"file_path":"/a/b.go"}`)},
		{"Read", json.RawMessage(`{"file_path":"/a/b.go"}`)},
		{"Grep", json.RawMessage(`{}`)},
		{"Thinking", json.RawMessage(`{}`)},
		{"mcp__x__y", json.RawMessage(`{}`)},
	}
	for _, in := range inputs {
		act, _ := ClassifyToolCall(in.tool, in.input)
		if act == "" || act == "think" || !agentMapContractIsActivity(act) {
			t.Errorf("ClassifyToolCall(%s, %s) act = %q, want a non-think ActivityIDs member", in.tool, in.input, act)
		}
	}
}

// G1: "between two commands of equal strength the first wins", and the
// strength order (ship beats test beats build ...).
func TestAgentMapContractClassifyBashStrength(t *testing.T) {
	cases := []struct{ cmd, act, label string }{
		// test outranks read; the read comes first but loses.
		{"ls -la && go test ./...", "test", "go test"},
		// two equal-strength searches: the first wins.
		{"grep -rn a . ; grep -rn b .", "search", "grep"},
		// ship outranks test.
		{"go test ./... && git push", "ship", "git push"},
		// leading env assignment and sudo are dropped.
		{"FOO=bar sudo make build", "build", "make build"},
	}
	for _, c := range cases {
		act, label := ClassifyToolCall("Bash", agentMapContractBashInput(t, c.cmd))
		if act != c.act || label != c.label {
			t.Errorf("Bash %q = (%q, %q), want (%q, %q)", c.cmd, act, label, c.act, c.label)
		}
	}
}

// G1: "A Bash label ... never contains a path argument, a flag value, a
// message, or any other text from the command line."
func TestAgentMapContractBashLabelCarriesNoCommandText(t *testing.T) {
	secret := "SECRETWORDS"
	cmds := []string{
		"git commit -m '" + secret + "'",
		"go test -run " + secret + " ./internal/" + secret,
		"grep -rn " + secret + " /tmp/" + secret,
		"kraken s zeus-x " + secret,
		"curl -H 'Authorization: " + secret + "' https://" + secret + ".example.com",
	}
	for _, c := range cmds {
		_, label := ClassifyToolCall("Bash", agentMapContractBashInput(t, c))
		if strings.Contains(label, secret) {
			t.Errorf("Bash %q label %q carries command-line text", c, label)
		}
	}
}

// ---------------------------------------------------------------------------
// G2: TailEvents
// ---------------------------------------------------------------------------

var agentMapContractT0 = time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)

func agentMapContractTS(offsetMS int64) string {
	return agentMapContractT0.Add(time.Duration(offsetMS) * time.Millisecond).Format("2006-01-02T15:04:05.000Z")
}

func agentMapContractMS(offsetMS int64) int64 {
	return agentMapContractT0.UnixMilli() + offsetMS
}

func agentMapContractLine(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// agentMapContractCall is one assistant record holding one tool_use block.
func agentMapContractCall(t *testing.T, at int64, id, name string, input any) string {
	return agentMapContractLine(t, map[string]any{
		"type":      "assistant",
		"uuid":      "a-" + id + "-" + agentMapContractTS(at),
		"timestamp": agentMapContractTS(at),
		"message": map[string]any{
			"role": "assistant",
			"content": []any{
				map[string]any{"type": "tool_use", "id": id, "name": name, "input": input},
			},
		},
	})
}

// agentMapContractResult is one user record holding a tool_result for id.
func agentMapContractResult(t *testing.T, at int64, id string) string {
	return agentMapContractLine(t, map[string]any{
		"type":      "user",
		"uuid":      "r-" + id,
		"timestamp": agentMapContractTS(at),
		"message": map[string]any{
			"role": "user",
			"content": []any{
				map[string]any{"type": "tool_result", "tool_use_id": id, "content": "ok"},
			},
		},
	})
}

// agentMapContractText is one user record carrying plain text.
func agentMapContractText(t *testing.T, at int64, uuid, text string, meta bool) string {
	rec := map[string]any{
		"type":      "user",
		"uuid":      uuid,
		"timestamp": agentMapContractTS(at),
		"message":   map[string]any{"role": "user", "content": text},
	}
	if meta {
		rec["isMeta"] = true
	}
	return agentMapContractLine(t, rec)
}

func agentMapContractWrite(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func agentMapContractFind(evs []AgentEvent, id string) *AgentEvent {
	for i := range evs {
		if evs[i].ID == id {
			return &evs[i]
		}
	}
	return nil
}

const agentMapContractPrompt = "PROMPTTEXT please fix the flaky board test"
const agentMapContractMsg = "MESSAGETEXT here is the plan"

func agentMapContractFullTranscript(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lead.jsonl")
	lines := []string{
		agentMapContractCall(t, 0, "toolu_1", "Bash", map[string]any{"command": "go test ./..."}),
		// the same tool_use id repeated on a second record: still one event
		agentMapContractCall(t, 0, "toolu_1", "Bash", map[string]any{"command": "go test ./..."}),
		agentMapContractResult(t, 1500, "toolu_1"),
		"this line is { not json",
		agentMapContractText(t, 2000, "u-team", `<teammate-message teammate_id="heph-1">`+agentMapContractMsg+`</teammate-message>`, false),
		agentMapContractText(t, 3000, "u-task", `<task-notification><task-id>abc</task-id>`+agentMapContractMsg+`</task-notification>`, false),
		agentMapContractText(t, 4000, "u-kraken", `[kraken pr watch] `+agentMapContractMsg, false),
		agentMapContractText(t, 5000, "u-cell", `[from studio · cell other-job] `+agentMapContractMsg, false),
		agentMapContractText(t, 6000, "u-you", agentMapContractPrompt, false),
		agentMapContractText(t, 6100, "u-cmd", `<command-name>/compact</command-name>`, false),
		agentMapContractText(t, 6200, "u-localcmd", `<local-command-stdout>x</local-command-stdout>`, false),
		agentMapContractText(t, 6300, "u-cont", `This session is being continued from a previous conversation`, false),
		agentMapContractText(t, 6400, "u-caveat", `Caveat: the messages below were generated`, false),
		agentMapContractText(t, 6500, "u-sysrem", `<system-reminder>x</system-reminder>`, false),
		agentMapContractText(t, 6600, "u-bash", `<bash-input>ls</bash-input>`, false),
		agentMapContractText(t, 6700, "u-image", `[Image: source: /tmp/x.png]`, false),
		agentMapContractText(t, 6800, "u-interrupt", `[Request interrupted by user]`, false),
		agentMapContractText(t, 6900, "u-meta", `meta text that is not a prompt`, true),
		agentMapContractCall(t, 7000, "toolu_2", "SendMessage", map[string]any{"to": "heph-1", "message": agentMapContractMsg}),
		agentMapContractCall(t, 8000, "toolu_3", "Agent", map[string]any{"name": "heph-2", "description": "build slice", "prompt": agentMapContractMsg}),
		agentMapContractCall(t, 9000, "toolu_4", "Agent", map[string]any{"description": "scan code", "prompt": agentMapContractMsg}),
		agentMapContractCall(t, 10000, "toolu_5", "TaskStop", map[string]any{"task_id": "bg-42"}),
		agentMapContractCall(t, 11000, "toolu_6", "Edit", map[string]any{"file_path": "/repo/internal/x.go", "old_string": agentMapContractMsg, "new_string": "y"}),
		agentMapContractResult(t, 11250, "toolu_6"),
	}
	agentMapContractWrite(t, path, lines)
	return path
}

// G2: every event kind on one transcript, oldest first.
func TestAgentMapContractTailEventsKinds(t *testing.T) {
	path := agentMapContractFullTranscript(t)
	evs, err := TailEvents(path, 100)
	if err != nil {
		t.Fatalf("TailEvents: %v", err)
	}
	wantIDs := []string{"toolu_1", "u-team", "u-task", "u-kraken", "u-cell", "u-you", "toolu_2", "toolu_3", "toolu_4", "toolu_5", "toolu_6"}
	gotIDs := make([]string, len(evs))
	for i, e := range evs {
		gotIDs[i] = e.ID
	}
	if strings.Join(gotIDs, ",") != strings.Join(wantIDs, ",") {
		t.Fatalf("event ids = %v, want %v", gotIDs, wantIDs)
	}

	// tool call with its result: DurMS and Open false
	e := agentMapContractFind(evs, "toolu_1")
	if e.Act != "test" || e.Label != "go test" || e.T != agentMapContractMS(0) || e.DurMS != 1500 || e.Open {
		t.Errorf("toolu_1 = %+v, want test/go test at T0, dur 1500, closed", *e)
	}
	e = agentMapContractFind(evs, "toolu_6")
	if e.Act != "edit" || e.Label != "Edit x.go" || e.T != agentMapContractMS(11000) || e.DurMS != 250 || e.Open {
		t.Errorf("toolu_6 = %+v, want edit/Edit x.go, dur 250, closed", *e)
	}

	// hears
	hears := []struct{ id, from, who, label string }{
		{"u-team", "teammate", "heph-1", "from heph-1"},
		{"u-task", "task", "", "task finished"},
		{"u-kraken", "kraken", "", "notice from kraken"},
		{"u-cell", "cell", "other-job", "from cell other-job"},
		{"u-you", "you", "", "prompt from you"},
	}
	for _, h := range hears {
		e := agentMapContractFind(evs, h.id)
		if e.Act != "hear" || e.From != h.from || e.Label != h.label {
			t.Errorf("%s = %+v, want hear from %q label %q", h.id, *e, h.from, h.label)
		}
		if h.who != "" && e.Who != h.who {
			t.Errorf("%s Who = %q, want %q", h.id, e.Who, h.who)
		}
	}

	// the four overrides; with no tool_result read, Open is true and DurMS 0
	overrides := []struct{ id, act, label, who string }{
		{"toolu_2", "talk", "to heph-1", "heph-1"},
		{"toolu_3", "delegate", "spawn heph-2", "heph-2"},
		{"toolu_4", "delegate", "subagent: scan code", "scan code"},
		{"toolu_5", "delegate", "stop bg-42", "bg-42"},
	}
	for _, o := range overrides {
		e := agentMapContractFind(evs, o.id)
		if e.Act != o.act || e.Label != o.label || e.Who != o.who {
			t.Errorf("%s = %+v, want act %q label %q who %q", o.id, *e, o.act, o.label, o.who)
		}
		if !e.Open || e.DurMS != 0 {
			t.Errorf("%s Open=%v DurMS=%d, want open with 0", o.id, e.Open, e.DurMS)
		}
	}
	if e := agentMapContractFind(evs, "toolu_2"); e.T != agentMapContractMS(7000) {
		t.Errorf("toolu_2 T = %d, want %d", e.T, agentMapContractMS(7000))
	}
}

// G2: "No label, Who or other field ever carries message text or prompt text."
func TestAgentMapContractTailEventsCarryNoText(t *testing.T) {
	path := agentMapContractFullTranscript(t)
	evs, err := TailEvents(path, 100)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(evs)
	if err != nil {
		t.Fatal(err)
	}
	for _, needle := range []string{"PROMPTTEXT", "MESSAGETEXT", "flaky board", "here is the plan"} {
		if strings.Contains(string(b), needle) {
			t.Fatalf("events carry text %q: %s", needle, b)
		}
	}
}

// G2: "Returns the newest n events ..., oldest first. n < 1 is treated as 1."
func TestAgentMapContractTailEventsNewestN(t *testing.T) {
	path := agentMapContractFullTranscript(t)
	evs, err := TailEvents(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 3 || evs[0].ID != "toolu_4" || evs[1].ID != "toolu_5" || evs[2].ID != "toolu_6" {
		t.Fatalf("TailEvents(n=3) = %+v, want toolu_4, toolu_5, toolu_6", evs)
	}
	evs, err = TailEvents(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 || evs[0].ID != "toolu_6" {
		t.Fatalf("TailEvents(n=0) = %+v, want only the newest (toolu_6)", evs)
	}
}

// G2: "A path that does not exist is (nil, nil)."
func TestAgentMapContractTailEventsMissingPath(t *testing.T) {
	evs, err := TailEvents(filepath.Join(t.TempDir(), "nope.jsonl"), 10)
	if err != nil || evs != nil {
		t.Fatalf("TailEvents(missing) = (%v, %v), want (nil, nil)", evs, err)
	}
}

// G2: SendMessage's Who falls back to input.recipient; TaskStop's target
// falls back to input.agent_name.
func TestAgentMapContractTailEventsFallbacks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	agentMapContractWrite(t, path, []string{
		agentMapContractCall(t, 0, "toolu_a", "SendMessage", map[string]any{"recipient": "athena-edge", "message": "x"}),
		agentMapContractCall(t, 1000, "toolu_b", "TaskStop", map[string]any{"agent_name": "heph-3"}),
	})
	evs, err := TailEvents(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v, want 2", evs)
	}
	if evs[0].Who != "athena-edge" {
		t.Errorf("SendMessage via recipient Who = %q, want athena-edge", evs[0].Who)
	}
	if evs[1].Label != "stop heph-3" || evs[1].Who != "heph-3" {
		t.Errorf("TaskStop via agent_name = %+v, want label 'stop heph-3' who heph-3", evs[1])
	}
}

// G2: AgentEvent's JSON tags as frozen.
func TestAgentMapContractAgentEventJSON(t *testing.T) {
	b, err := json.Marshal(AgentEvent{ID: "x", T: 5, Act: "hear", Label: "l", DurMS: 3, Open: true, From: "you", Who: "w"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"id", "t", "act", "label", "dur_ms", "open", "from", "who"} {
		if _, ok := m[k]; !ok {
			t.Errorf("AgentEvent JSON %s missing key %q", b, k)
		}
	}
	b, _ = json.Marshal(AgentEvent{ID: "x", T: 5, Act: "edit", Label: "l"})
	for _, k := range []string{"dur_ms", "open", "from", "who"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("zero AgentEvent JSON %s carries omitempty key %q", b, k)
		}
	}
}

// ---------------------------------------------------------------------------
// G2: NewestAgentTranscript
// ---------------------------------------------------------------------------

func agentMapContractAgentFile(t *testing.T, path, agentName string, mtime time.Time) {
	t.Helper()
	lines := []string{
		agentMapContractLine(t, map[string]any{"type": "summary", "summary": "x"}),
		agentMapContractLine(t, map[string]any{"type": "user", "agentName": agentName, "message": map[string]any{"role": "user", "content": "brief"}}),
	}
	agentMapContractWrite(t, path, lines)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

// G2: the newest *.jsonl directly inside the dirs whose head names the agent;
// subagents/ never considered; "" when none.
func TestAgentMapContractNewestAgentTranscript(t *testing.T) {
	root := t.TempDir()
	d1 := filepath.Join(root, "proj-a")
	d2 := filepath.Join(root, "proj-b")
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)

	older := filepath.Join(d1, "older.jsonl")
	newer := filepath.Join(d2, "newer.jsonl")
	agentMapContractAgentFile(t, older, "heph-1", base)
	agentMapContractAgentFile(t, newer, "heph-1", base.Add(time.Hour))
	// newer still, but another agent
	agentMapContractAgentFile(t, filepath.Join(d1, "someone-else.jsonl"), "heph-2", base.Add(3*time.Hour))
	// newest of all and naming heph-1, but under subagents/
	agentMapContractAgentFile(t, filepath.Join(d1, "sess", "subagents", "agent-x.jsonl"), "heph-1", base.Add(4*time.Hour))
	agentMapContractAgentFile(t, filepath.Join(d2, "subagents", "agent-y.jsonl"), "heph-1", base.Add(5*time.Hour))
	// a non-jsonl file naming heph-1 is not a transcript
	agentMapContractAgentFile(t, filepath.Join(d1, "notes.txt"), "heph-1", base.Add(6*time.Hour))

	if got := NewestAgentTranscript([]string{d1, d2}, "heph-1"); got != newer {
		t.Fatalf("NewestAgentTranscript(heph-1) = %q, want %q", got, newer)
	}
	if got := NewestAgentTranscript([]string{d1, d2}, "heph-2"); got != filepath.Join(d1, "someone-else.jsonl") {
		t.Fatalf("NewestAgentTranscript(heph-2) = %q", got)
	}
	if got := NewestAgentTranscript([]string{d1, d2}, "nobody"); got != "" {
		t.Fatalf("NewestAgentTranscript(nobody) = %q, want empty", got)
	}
	// a subagents/ dir passed in directly is still never considered
	if got := NewestAgentTranscript([]string{filepath.Join(d2, "subagents")}, "heph-1"); got != "" {
		t.Fatalf("NewestAgentTranscript over a subagents dir = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// Amendment 6
// ---------------------------------------------------------------------------

// G1 amendment 6: "A ; && || | or newline inside a single- or double-quoted
// string does not start a new command, and no word inside a quoted string is
// ever read as a program or a subcommand" — the three stated examples.
func TestAgentMapContractBashQuotedSeparators(t *testing.T) {
	for _, cmd := range []string{
		`echo "note; make sure the tokens rotate"`,
		`echo "status && git secretword rest"`,
		`printf 'x; kraken mm secretone secrettwo'`,
	} {
		act, label := ClassifyToolCall("Bash", agentMapContractBashInput(t, cmd))
		if act != "other" || label != "shell" {
			t.Errorf("Bash %q = (%q, %q), want (other, shell)", cmd, act, label)
		}
	}
}

// G1 amendment 6: a quoted separator does not hide a real command beside it,
// and the real command still classifies (the ordinary path either side of a
// quoted string).
func TestAgentMapContractBashQuotedThenReal(t *testing.T) {
	act, label := ClassifyToolCall("Bash", agentMapContractBashInput(t, `echo "a; b && c" && go test ./...`))
	if act != "test" || label != "go test" {
		t.Errorf("= (%q, %q), want (test, go test)", act, label)
	}
	act, label = ClassifyToolCall("Bash", agentMapContractBashInput(t, `git add "notes; v2.md" && git push`))
	if act != "ship" || label != "git add" {
		t.Errorf("= (%q, %q), want (ship, git add)", act, label)
	}
}

// G1 amendment 6: "A line with an unclosed quote still classifies without
// panicking, and its label still carries no word from inside the unclosed
// quote."
func TestAgentMapContractBashUnclosedQuote(t *testing.T) {
	act, label := ClassifyToolCall("Bash", agentMapContractBashInput(t, `echo "hello; git secretword && make secretbuild`))
	if !agentMapContractIsActivity(act) || act == "think" {
		t.Errorf("act = %q, want a non-think ActivityIDs member", act)
	}
	for _, w := range []string{"secretword", "secretbuild", "hello"} {
		if strings.Contains(label, w) {
			t.Errorf("label %q carries %q from inside the unclosed quote", label, w)
		}
	}
}

// G2 amendment 6: a prompt that merely quotes the teammate tag is the
// operator's prompt; leading whitespace before a real tag is allowed; a cell
// stamp whose slug is not one slug word falls through to "you".
func TestAgentMapContractHearNarrowedRules(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	agentMapContractWrite(t, path, []string{
		agentMapContractText(t, 0, "u-quote", `look at this: <teammate-message teammate_id="heph-1">`+agentMapContractMsg+`</teammate-message>`, false),
		agentMapContractText(t, 1000, "u-ws", "  \n"+`<teammate-message teammate_id="athena.edge@team-1">`+agentMapContractMsg+`</teammate-message>`, false),
		agentMapContractText(t, 2000, "u-badcell", `[from studio · cell two words] `+agentMapContractMsg, false),
		agentMapContractText(t, 3000, "u-goodcell", `[from studio · cell agent-map_2.x] `+agentMapContractMsg, false),
	})
	evs, err := TailEvents(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 {
		t.Fatalf("events = %+v, want 4", evs)
	}
	want := []struct{ id, from, who, label string }{
		{"u-quote", "you", "", "prompt from you"},
		{"u-ws", "teammate", "athena.edge@team-1", "from athena.edge@team-1"},
		{"u-badcell", "you", "", "prompt from you"},
		{"u-goodcell", "cell", "agent-map_2.x", "from cell agent-map_2.x"},
	}
	for i, w := range want {
		e := evs[i]
		if e.ID != w.id || e.Act != "hear" || e.From != w.from || e.Who != w.who || e.Label != w.label {
			t.Errorf("event %d = %+v, want id %s from %q who %q label %q", i, e, w.id, w.from, w.who, w.label)
		}
	}
}

// agentMapContractFresh is what a fresh read of the whole file gives: the
// same bytes at a path TailEvents has never seen.
func agentMapContractFresh(t *testing.T, path string, n int) []AgentEvent {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cp := filepath.Join(t.TempDir(), "fresh.jsonl")
	if err := os.WriteFile(cp, b, 0o644); err != nil {
		t.Fatal(err)
	}
	evs, err := TailEvents(cp, n)
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func agentMapContractSameEvents(t *testing.T, what string, got, want []AgentEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: %d events %+v, want %d %+v", what, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s: event %d = %+v, want %+v", what, i, got[i], want[i])
		}
	}
}

func agentMapContractAppend(t *testing.T, path string, lines []string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// G2 amendment 6: "The answer is always the one a fresh read of the whole
// file would give: an event that was open at the last call and whose
// tool_result has since been appended comes back closed with its DurMS".
func TestAgentMapContractTailEventsAfterAppend(t *testing.T) {
	path := filepath.Join(t.TempDir(), "grow.jsonl")
	agentMapContractWrite(t, path, []string{
		agentMapContractCall(t, 0, "toolu_g1", "Bash", map[string]any{"command": "go test ./..."}),
		agentMapContractResult(t, 400, "toolu_g1"),
		agentMapContractCall(t, 1000, "toolu_g2", "Bash", map[string]any{"command": "make build"}),
	})
	for _, n := range []int{2, 10} {
		evs, err := TailEvents(path, n)
		if err != nil {
			t.Fatal(err)
		}
		if e := agentMapContractFind(evs, "toolu_g2"); e == nil || !e.Open {
			t.Fatalf("n=%d before the append: toolu_g2 = %+v, want open", n, e)
		}
	}

	agentMapContractAppend(t, path, []string{
		agentMapContractResult(t, 3500, "toolu_g2"),
		agentMapContractText(t, 4000, "u-g", "next step please", false),
		agentMapContractCall(t, 5000, "toolu_g3", "Read", map[string]any{"file_path": "/r/a.go"}),
	})
	for _, n := range []int{2, 10} {
		evs, err := TailEvents(path, n)
		if err != nil {
			t.Fatal(err)
		}
		agentMapContractSameEvents(t, "after append", evs, agentMapContractFresh(t, path, n))
	}
	evs, _ := TailEvents(path, 10)
	if e := agentMapContractFind(evs, "toolu_g2"); e == nil || e.Open || e.DurMS != 2500 {
		t.Fatalf("toolu_g2 after its result was appended = %+v, want closed with DurMS 2500", e)
	}
	if len(evs) != 4 || evs[3].ID != "toolu_g3" || !evs[3].Open {
		t.Fatalf("events after append = %+v, want 4 ending with an open toolu_g3", evs)
	}
}

// G2 amendment 6: "a file that is shorter than it was last seen (replaced or
// truncated) is read afresh".
func TestAgentMapContractTailEventsReplacedShorter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "swap.jsonl")
	agentMapContractWrite(t, path, []string{
		agentMapContractCall(t, 0, "toolu_old1", "Bash", map[string]any{"command": "go test ./..."}),
		agentMapContractCall(t, 1000, "toolu_old2", "Bash", map[string]any{"command": "make build"}),
		agentMapContractCall(t, 2000, "toolu_old3", "Grep", map[string]any{"pattern": "x"}),
	})
	if _, err := TailEvents(path, 10); err != nil {
		t.Fatal(err)
	}
	agentMapContractWrite(t, path, []string{
		agentMapContractCall(t, 9000, "toolu_new", "Edit", map[string]any{"file_path": "/r/z.go"}),
	})
	evs, err := TailEvents(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	agentMapContractSameEvents(t, "after replace", evs, agentMapContractFresh(t, path, 10))
	if len(evs) != 1 || evs[0].ID != "toolu_new" || evs[0].Label != "Edit z.go" {
		t.Fatalf("events after replace = %+v, want only toolu_new", evs)
	}
}

// G2 amendment 6: many small appends, each answered as a fresh read would
// answer it (the incremental path stays exact over a run of calls).
func TestAgentMapContractTailEventsManyAppends(t *testing.T) {
	path := filepath.Join(t.TempDir(), "many.jsonl")
	agentMapContractWrite(t, path, []string{agentMapContractText(t, 0, "u-start", "begin", false)})
	for i := 0; i < 30; i++ {
		id := "toolu_m" + strings.Repeat("x", i%3) + string(rune('a'+i%26)) + string(rune('A'+i/26))
		at := int64(1000 * (i + 1))
		agentMapContractAppend(t, path, []string{agentMapContractCall(t, at, id, "Bash", map[string]any{"command": "go vet ./..."})})
		if i%2 == 1 {
			agentMapContractAppend(t, path, []string{agentMapContractResult(t, at+100, id)})
		}
		evs, err := TailEvents(path, 5)
		if err != nil {
			t.Fatal(err)
		}
		agentMapContractSameEvents(t, "append "+id, evs, agentMapContractFresh(t, path, 5))
	}
}

// G2 amendment 7: "One prefix is allowed in front of the tag, and only this
// exact harness line: `Another Claude session sent a message:` followed by
// whitespace".
func TestAgentMapContractHearCrossSessionPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "t.jsonl")
	agentMapContractWrite(t, path, []string{
		agentMapContractText(t, 0, "u-relay", "Another Claude session sent a message:\n"+`<teammate-message teammate_id="heph-1">`+agentMapContractMsg+`</teammate-message>`, false),
		agentMapContractText(t, 1000, "u-relay-sp", "Another Claude session sent a message: "+`<teammate-message teammate_id="team-lead">`+agentMapContractMsg+`</teammate-message>`, false),
	})
	evs, err := TailEvents(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 {
		t.Fatalf("events = %+v, want 2", evs)
	}
	for i, who := range []string{"heph-1", "team-lead"} {
		e := evs[i]
		if e.Act != "hear" || e.From != "teammate" || e.Who != who || e.Label != "from "+who {
			t.Errorf("event %d = %+v, want a teammate message from %s", i, e, who)
		}
	}
}
