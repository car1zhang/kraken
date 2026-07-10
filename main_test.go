package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudePIDsUnder(t *testing.T) {
	// pane shell 100 → claude 200 → bash 300 → claude 400 (nested)
	children := map[int][]int{100: {200}, 200: {300}, 300: {400}}
	cmds := map[int]string{
		100: "-zsh",
		200: "claude --dangerously-skip-permissions",
		300: "/bin/zsh -c something",
		400: "/Users/x/.local/bin/claude --rc",
	}
	got := claudePIDsUnder(100, children, cmds)
	if len(got) != 2 {
		t.Fatalf("want 2 claudes, got %v", got)
	}
	if got[0] != 400 || got[1] != 200 {
		t.Errorf("want deepest-first [400 200], got %v", got)
	}
	if pids := claudePIDsUnder(300, children, cmds); len(pids) != 1 || pids[0] != 400 {
		t.Errorf("subtree walk: want [400], got %v", pids)
	}
}

func TestIsClaudeCmd(t *testing.T) {
	cases := map[string]bool{
		"claude --dangerously-skip-permissions":                        true,
		"/usr/local/bin/claude":                                        true,
		"/Users/x/.local/share/claude/versions/2.1.205 --agent-id a@b": true,
		"claude-patch-buddy --rc":                                      false,
		"vim claude.md":                                                false,
		"":                                                             false,
	}
	for cmd, want := range cases {
		if got := isClaudeCmd(cmd); got != want {
			t.Errorf("isClaudeCmd(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestIsCodexCmd(t *testing.T) {
	cases := map[string]bool{
		"codex --yolo":                     true,
		"/opt/codex --full-auto":           true,
		"node /usr/local/bin/codex --yolo": false,
		"/opt/codex-code-mode-host":        false,
		"vim codex.md":                     false,
	}
	for cmd, want := range cases {
		if got := isCodexCmd(cmd); got != want {
			t.Errorf("isCodexCmd(%q) = %v, want %v", cmd, got, want)
		}
	}
}

func TestAgentPIDsUnder(t *testing.T) {
	children := map[int][]int{100: {200}, 200: {300}, 300: {400}}
	cmds := map[int]string{
		100: "-zsh",
		200: "claude --rc",
		300: "/bin/zsh",
		400: "/usr/local/bin/codex --yolo",
	}
	got := agentPIDsUnder(100, children, cmds)
	if len(got) != 2 || got[0].pid != 400 || got[0].agent != "codex" ||
		got[1].pid != 200 || got[1].agent != "claude" {
		t.Fatalf("want deepest-first codex then claude, got %+v", got)
	}
}

func TestReadCodexNamesLastEntryWins(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, ".codex"), 0755); err != nil {
		t.Fatal(err)
	}
	data := []byte("{\"id\":\"abc\",\"thread_name\":\"first prompt\"}\n" +
		"{not json}\n" +
		"{\"id\":\"abc\",\"thread_name\":\"renamed thread\"}\n")
	if err := os.WriteFile(filepath.Join(dir, ".codex", "session_index.jsonl"), data, 0644); err != nil {
		t.Fatal(err)
	}
	names := readCodexNames()
	if names["abc"] != "renamed thread" {
		t.Fatalf("want latest title, got %q", names["abc"])
	}
}

func TestProjectDirName(t *testing.T) {
	if got := projectDirName("/Users/car1zhang/Developer/kraken"); got != "-Users-car1zhang-Developer-kraken" {
		t.Errorf("got %q", got)
	}
	if got := projectDirName("/tmp/my.app_v2"); got != "-tmp-my-app-v2" {
		t.Errorf("got %q", got)
	}
}

func TestParseBlocks(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"git status"}}]}}`,
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":"tool output"},{"type":"text","text":"follow-up"}]}}`,
		`{"type":"user","message":{"content":[{"type":"text","text":"<system-reminder>injected</system-reminder>"}]}}`,
		`{"type":"user","isMeta":true,"message":{"content":"caveat noise"}}`,
		`{"type":"user","isSidechain":true,"message":{"content":"subagent prompt"}}`,
		`{"type":"assistant","isSidechain":true,"message":{"content":[{"type":"text","text":"subagent noise"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"","signature":"x"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"te`, // partial trailing line
	}
	blocks := parseBlocks(lines)
	want := []block{
		{"thinking", "hmm"},
		{"tool", "Bash(git status)"},
		{"user", "hi"},
		{"user", "follow-up"},
		{"text", "done"},
	}
	if len(blocks) != len(want) {
		t.Fatalf("want %d blocks, got %d: %v", len(want), len(blocks), blocks)
	}
	for i := range want {
		if blocks[i] != want[i] {
			t.Errorf("block %d: want %v, got %v", i, want[i], blocks[i])
		}
	}
}

func TestLastBlocks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := `{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/a"}}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"second"}]}}
{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"ls"}}]}}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	blocks, counted := lastBlocks(path, 1)
	if counted != 1 {
		t.Fatalf("want counted=1, got %d", counted)
	}
	// last text block plus the tool call after it
	if len(blocks) != 2 || blocks[0] != (block{"text", "second"}) || blocks[1].kind != "tool" {
		t.Errorf("n=1: got %v", blocks)
	}

	blocks, counted = lastBlocks(path, 2)
	if counted != 2 || len(blocks) != 4 || blocks[0] != (block{"text", "first"}) {
		t.Errorf("n=2: counted=%d blocks=%v", counted, blocks)
	}

	// n larger than available: return what exists
	blocks, counted = lastBlocks(path, 10)
	if counted != 2 || len(blocks) != 4 {
		t.Errorf("n=10: counted=%d blocks=%v", counted, blocks)
	}
}

func TestLastBlocksCountsUserMessages(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := `{"type":"assistant","message":{"content":[{"type":"text","text":"answer"}]}}
{"type":"user","message":{"content":"next question"}}
`
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	blocks, counted := lastBlocks(path, 1)
	if counted != 1 || len(blocks) != 1 || blocks[0] != (block{"user", "next question"}) {
		t.Errorf("n=1: counted=%d blocks=%v", counted, blocks)
	}
	blocks, counted = lastBlocks(path, 2)
	if counted != 2 || len(blocks) != 2 || blocks[0] != (block{"text", "answer"}) {
		t.Errorf("n=2: counted=%d blocks=%v", counted, blocks)
	}
}

func TestSummarizeInput(t *testing.T) {
	if got := summarizeInput(map[string]any{"command": "echo hi"}); got != "echo hi" {
		t.Errorf("got %q", got)
	}
	if got := summarizeInput(map[string]any{"other": 42}); got != `{"other":42}` {
		t.Errorf("got %q", got)
	}
	long := summarizeInput(map[string]any{"command": string(make([]byte, 300))})
	if len([]rune(long)) > 100 {
		t.Errorf("not truncated: %d chars", len([]rune(long)))
	}
}

func TestReadSessionFileMissingFields(t *testing.T) {
	// records with missing/extra fields must parse to zero values, not error
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	sdir := filepath.Join(dir, ".claude", "sessions")
	if err := os.MkdirAll(sdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sdir, "42.json"), []byte(`{"pid":42,"sessionId":"abc","newField":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	sf, ok := readSessionFile(42)
	if !ok || sf.SessionID != "abc" || sf.Status != "" || sf.Name != "" {
		t.Errorf("got %+v ok=%v", sf, ok)
	}
	if _, ok := readSessionFile(999); ok {
		t.Error("missing file should return ok=false")
	}
}

func TestSelectPeer(t *testing.T) {
	// success paths only: failure paths call die() which exits the process
	peers := []*Peer{
		{Name: "asr tuning", CWD: "/x/monolith", Pane: "%1", SessionID: "aaaa1111-0000"},
		{Name: "kraken tmux", CWD: "/x/kraken", Pane: "%2", SessionID: "bbbb2222-0000"},
		{AgentName: "helper", Role: "teammate", Pane: "%3", SessionID: "cccc3333-0000"},
	}
	for i, p := range peers {
		p.Index = i + 1
	}
	if got := selectPeer("2", peers); got != peers[1] {
		t.Errorf("index: got %v", got)
	}
	if got := selectPeer("asr tuning", peers); got != peers[0] {
		t.Errorf("multi-word name: got %v", got)
	}
	if got := selectPeer("helper", peers); got != peers[2] {
		t.Errorf("agent name: got %v", got)
	}
	if got := selectPeer("bbbb2222", peers); got != peers[1] {
		t.Errorf("uuid prefix: got %v", got)
	}
	if got := selectPeer("KRAKEN", peers); got != peers[1] {
		t.Errorf("case-insensitive: got %v", got)
	}
	t.Setenv("TMUX_PANE", "%3")
	if got := selectPeer(".", peers); got != peers[2] {
		t.Errorf("current pane: got %v", got)
	}
	// exact pane id: churn-proof, never fuzzy-matched
	if got := selectPeer("%2", peers); got != peers[1] {
		t.Errorf("pane id: got %v", got)
	}
	// out-of-range integers fall through to name matching (tmux sessions are
	// often named "0", "1", ...)
	peers[0].TmuxSession = "7"
	if got := selectPeer("7", peers); got != peers[0] {
		t.Errorf("numeric name fallthrough: got %v", got)
	}
}

func TestIsShellSafe(t *testing.T) {
	safe := []string{"claude", "/usr/bin/claude", "--fork-session", "a1-b2.c_d"}
	unsafe := []string{"", "a b", "$HOME", "a;b", "`x`", "a|b", "it's"}
	for _, s := range safe {
		if !isShellSafe(s) {
			t.Errorf("want safe: %q", s)
		}
	}
	for _, s := range unsafe {
		if isShellSafe(s) {
			t.Errorf("want unsafe: %q", s)
		}
	}
}

func TestOrderPeers(t *testing.T) {
	mate := &Peer{AgentName: "helper", Role: "teammate"}
	lead := &Peer{Name: "lead", Teammates: []*Peer{mate}}
	other := &Peer{Name: "other"}
	ordered := orderPeers([]*Peer{lead, mate, other})
	if len(ordered) != 3 {
		t.Fatalf("want 3, got %d", len(ordered))
	}
	if ordered[0] != lead || ordered[1] != mate || ordered[2] != other {
		t.Errorf("wrong order: %v %v %v", ordered[0].Name, ordered[1].AgentName, ordered[2].Name)
	}
	for i, p := range ordered {
		if p.Index != i+1 {
			t.Errorf("index %d not assigned", i+1)
		}
	}
}

func TestOrderPeersNestedLead(t *testing.T) {
	// a teammate that is itself a lead must bring its own teammates along
	sub := &Peer{AgentName: "sub"}
	mid := &Peer{Name: "mid", Teammates: []*Peer{sub}}
	lead := &Peer{Name: "lead", Teammates: []*Peer{mid}}
	ordered := orderPeers([]*Peer{lead, mid, sub})
	if len(ordered) != 3 {
		t.Fatalf("want 3 rows, got %d", len(ordered))
	}
	if ordered[0] != lead || ordered[1] != mid || ordered[2] != sub {
		t.Errorf("wrong order")
	}
}

func TestOrderPeersDuplicateTeammate(t *testing.T) {
	// one peer claimed by two teams appears once, with monotonic indexes
	mate := &Peer{AgentName: "shared"}
	a := &Peer{Name: "a", Teammates: []*Peer{mate}}
	b := &Peer{Name: "b", Teammates: []*Peer{mate}}
	ordered := orderPeers([]*Peer{a, b, mate})
	if len(ordered) != 3 {
		t.Fatalf("want 3 rows, got %d: duplicate teammate emitted twice?", len(ordered))
	}
	for i, p := range ordered {
		if p.Index != i+1 {
			t.Errorf("index at pos %d is %d — not monotonic", i, p.Index)
		}
	}
}

func TestContainsQuestionMarker(t *testing.T) {
	dialog := "Do you want to proceed?\n ❯ 1. Yes\n   2. No\n"
	if !containsQuestionMarker(dialog) {
		t.Error("dialog with selected option not detected")
	}
	if !containsQuestionMarker("blah\n❯ 2. Yes, and don't ask again\n") {
		t.Error("non-first selected option not detected")
	}
	prose := "Do you want me to continue with the refactor?\nI can also stop here.\n"
	if containsQuestionMarker(prose) {
		t.Error("prose question falsely detected")
	}
	deep := strings.Repeat("x\n", 30) + "❯ 1. Yes\n" + strings.Repeat("y\n", 30)
	if containsQuestionMarker(deep) {
		t.Error("marker outside the bottom window should not match")
	}
}

func TestGitBranch(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "repo", "src", "deep")
	if err := os.MkdirAll(filepath.Join(dir, "repo", ".git"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0755); err != nil {
		t.Fatal(err)
	}
	head := filepath.Join(dir, "repo", ".git", "HEAD")
	if err := os.WriteFile(head, []byte("ref: refs/heads/carl/feature\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(sub); got != "carl/feature" {
		t.Errorf("walk-up branch: got %q", got)
	}
	// detached HEAD
	if err := os.WriteFile(head, []byte("0123456789abcdef\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(sub); got != "01234567" {
		t.Errorf("detached: got %q", got)
	}
	// worktree pointer file
	wt := filepath.Join(dir, "wt")
	gitdir := filepath.Join(dir, "gitdir")
	if err := os.MkdirAll(wt, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(gitdir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "HEAD"), []byte("ref: refs/heads/main\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got := gitBranch(wt); got != "main" {
		t.Errorf("worktree: got %q", got)
	}
	if got := gitBranch(t.TempDir()); got != "" {
		t.Errorf("non-repo: got %q", got)
	}
}

func TestShortModel(t *testing.T) {
	if got := shortModel("claude-fable-5"); got != "fable-5" {
		t.Errorf("got %q", got)
	}
	if got := shortModel(""); got != "" {
		t.Errorf("got %q", got)
	}
}

func TestVisWidth(t *testing.T) {
	if got := visWidth("hello"); got != 5 {
		t.Errorf("plain: got %d", got)
	}
	if got := visWidth("\x1b[36mhello\x1b[0m"); got != 5 {
		t.Errorf("colored: got %d", got)
	}
	if got := visWidth("\x1b[2m\x1b[0m"); got != 0 {
		t.Errorf("codes only: got %d", got)
	}
}

func TestBranchGuard(t *testing.T) {
	if err := branchGuard(&Peer{Name: "x"}); err == nil {
		t.Errorf("missing session id must be rejected")
	}
	if err := branchGuard(&Peer{Name: "x", SessionID: "abc", SessionIDGuessed: true}); err == nil {
		t.Errorf("guessed session id must be rejected")
	}
	if err := branchGuard(&Peer{Name: "x", SessionID: "abc"}); err != nil {
		t.Errorf("clean session id rejected: %v", err)
	}
}

func TestPeerCellsOrphanTeammate(t *testing.T) {
	// a teammate whose lead was never discovered must render as a top-level
	// row (no └, no ditto marks against an unrelated preceding peer)
	orphan := &Peer{AgentName: "orphan", Role: "teammate", CWD: "/tmp/proj", Branch: "main"}
	ordered := orderPeers([]*Peer{{Name: "other", CWD: "/tmp/other", Branch: "main"}, orphan})
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
	sub := &Peer{AgentName: "sub", CWD: "/tmp/mid", Branch: "mid-branch"}
	mid := &Peer{Name: "mid", CWD: "/tmp/mid", Branch: "mid-branch", Teammates: []*Peer{sub}}
	top := &Peer{Name: "top", CWD: "/tmp/top", Branch: "top-branch", Teammates: []*Peer{mid}}
	ordered := orderPeers([]*Peer{top, mid, sub})
	parent := parentMap(ordered)
	if parent[sub] != mid {
		t.Fatalf("sub's parent should be mid, got %v", parent[sub])
	}
	cells := peerCells(sub, parent[sub], false, time.Now())
	if cells[3] != "·" || cells[4] != "·" {
		t.Errorf("sub should ditto against mid: project=%q branch=%q", cells[3], cells[4])
	}
}

func TestParseEtime(t *testing.T) {
	cases := map[string]time.Duration{
		"00:05":      5 * time.Second,
		"12:34":      12*time.Minute + 34*time.Second,
		"01:02:03":   time.Hour + 2*time.Minute + 3*time.Second,
		"2-03:04:05": 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second,
		"garbage":    0,
		"1:2:3:4":    0,
	}
	for in, want := range cases {
		if got := parseEtime(in); got != want {
			t.Errorf("parseEtime(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestIsShellProc(t *testing.T) {
	for _, cmd := range []string{"-zsh", "/bin/bash", "zsh", "fish", "login -pf carl", "", "(node)"} {
		if !isShellProc(cmd) {
			t.Errorf("%q should be a shell", cmd)
		}
	}
	for _, cmd := range []string{"nvim main.go", "npm run dev", "/usr/local/bin/node server.js"} {
		if isShellProc(cmd) {
			t.Errorf("%q should not be a shell", cmd)
		}
	}
}

func TestPaneCommand(t *testing.T) {
	// pane shell 100 → {npm 200 (older), kraken 300 (newest)}, npm 200 → node 400
	children := map[int][]int{100: {200, 300}, 200: {400}}
	cmds := map[int]string{
		100: "-zsh",
		200: "npm run dev",
		300: "kraken v",
		400: "node server.js",
	}
	etimes := map[int]time.Duration{200: 2 * time.Hour, 300: time.Minute, 400: 2 * time.Hour}
	// kraken reports itself: the newest typed command wins even when it is a
	// kraken viewer — a k v pane must not read as a blank idle shell
	cmd, elapsed := paneCommand(100, children, cmds, etimes)
	if cmd != "kraken v" || elapsed != time.Minute {
		t.Errorf("want kraken v @1m (kraken reports itself), got %q @%v", cmd, elapsed)
	}

	// idle: bare shell with no children
	if cmd, _ := paneCommand(100, map[int][]int{}, cmds, etimes); cmd != "" {
		t.Errorf("idle pane should have no command, got %q", cmd)
	}

	// nested shell: zsh 100 → zsh 500 → vim 600
	nested := map[int][]int{100: {500}, 500: {600}}
	ncmds := map[int]string{100: "-zsh", 500: "/bin/zsh", 600: "vim notes.md"}
	if cmd, _ := paneCommand(100, nested, ncmds, etimes); cmd != "vim notes.md" {
		t.Errorf("should descend through nested shells, got %q", cmd)
	}

	// pane launched directly with a command (no shell root)
	if cmd, elapsed := paneCommand(600, nested, ncmds, map[int]time.Duration{600: time.Minute}); cmd != "vim notes.md" || elapsed != time.Minute {
		t.Errorf("non-shell root should be the command, got %q @%v", cmd, elapsed)
	}

	// two typed commands: newest (foreground) wins over an older background job
	multi := map[int][]int{100: {200, 700}}
	mcmds := map[int]string{100: "-zsh", 200: "npm run dev", 700: "pytest -x"}
	metimes := map[int]time.Duration{200: 2 * time.Hour, 700: 3 * time.Minute}
	if cmd, _ := paneCommand(100, multi, mcmds, metimes); cmd != "pytest -x" {
		t.Errorf("newest command should win, got %q", cmd)
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

func TestKillPeerGuards(t *testing.T) {
	// guards fire before any tmux exec, so these are safe to run anywhere;
	// the message text is asserted so a regressed guard can't pass by way of
	// verifyKillTarget failing on the missing pane instead
	_, err := killPeer(&Peer{AgentName: "ip", Role: "teammate"})
	if err == nil || !strings.Contains(err.Error(), "in-process") {
		t.Errorf("killing an in-process teammate must be refused, got %v", err)
	}
	_, err = killPeer(&Peer{Name: "nested", Role: "main", Pane: "%1", Depth: 1})
	if err == nil || !strings.Contains(err.Error(), "nested") {
		t.Errorf("killing a nested claude must be refused, got %v", err)
	}
	t.Setenv("TMUX_PANE", "%7")
	_, err = killPeer(&Peer{Role: "shell", Pane: "%7", CWD: "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "current pane") {
		t.Errorf("killing the current pane must be refused, got %v", err)
	}
}

func TestDisplayGroups(t *testing.T) {
	mate := &Peer{AgentName: "helper", Role: "teammate", TmuxSession: "api", WindowIdx: 3, Pane: "%9", PID: 9}
	lead := &Peer{Name: "lead", TmuxSession: "0", WindowIdx: 2, Pane: "%1", PID: 1, Teammates: []*Peer{mate}}
	solo := &Peer{Name: "solo", TmuxSession: "api", WindowIdx: 0, Pane: "%2", PID: 2}
	sh0 := &Peer{Role: "shell", TmuxSession: "0", WindowIdx: 0, Pane: "%3", PID: 3, CWD: "/tmp"}
	shAPI := &Peer{Role: "shell", TmuxSession: "api", WindowIdx: 1, Pane: "%4", PID: 4, CWD: "/tmp"}
	all, n := orderAll([]*Peer{lead, mate, solo}, []*Peer{sh0, shAPI})

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
	claude := &Peer{Name: "claude", Agent: "claude", Pane: "%1"}
	codex := &Peer{Name: "codex", Agent: "codex", Pane: "%2"}
	all, n := orderAll([]*Peer{codex, claude}, nil)
	groups := displayGroups("type", all, n)
	if len(groups) != 2 || groups[0].rows[0] != claude || groups[1].title != "codex" || groups[1].rows[0] != codex {
		t.Fatalf("expected separate Claude and Codex groups, got %+v", groups)
	}
	if claude.Index != 1 || codex.Index != 2 {
		t.Fatalf("indexes should follow rendered group order: claude=%d codex=%d", claude.Index, codex.Index)
	}
}

func TestSortBySessionWindow(t *testing.T) {
	// numeric sessions sort numerically ("10" after "9") and before names,
	// windows ascending, and same-pane order (foreground claude first, as
	// discovery emits it) is preserved
	fg := &Peer{Name: "fg", TmuxSession: "9", WindowIdx: 2, Pane: "%5", Depth: 0}
	deep := &Peer{Name: "deep", TmuxSession: "9", WindowIdx: 2, Pane: "%5", Depth: 1}
	list := []*Peer{
		{Name: "s10", TmuxSession: "10", WindowIdx: 0},
		{Name: "w5", TmuxSession: "9", WindowIdx: 5},
		fg, deep,
		{Name: "alpha", TmuxSession: "main", WindowIdx: 1},
		{Name: "digitname", TmuxSession: "5a", WindowIdx: 0},
		{Name: "w0", TmuxSession: "9", WindowIdx: 0},
	}
	sortBySessionWindow(list)
	want := []string{"w0", "fg", "deep", "w5", "s10", "digitname", "alpha"}
	for i, name := range want {
		if list[i].Name != name {
			t.Fatalf("pos %d: want %s, got %s (full: %v)", i, name, list[i].Name, names(list))
		}
	}
}

func names(list []*Peer) []string {
	out := make([]string, len(list))
	for i, p := range list {
		out[i] = p.Name
	}
	return out
}

func TestGroupCellsMatchHeaders(t *testing.T) {
	// a column added to one side but not the other would silently misalign
	// every table
	claude := &Peer{Name: "c", TmuxSession: "0", WindowIdx: 1, Pane: "%1", PID: 1, Index: 1}
	shell := &Peer{Role: "shell", TmuxSession: "0", WindowIdx: 2, Pane: "%2", PID: 2, Index: 2, CWD: "/tmp", Cmd: "make", CmdSeconds: 60}
	now := time.Now()
	for _, kind := range []string{"claudes", "shells", "session", "mixed"} {
		want := len(groupHeader(kind))
		rows := []*Peer{shell}
		if kind != "shells" {
			rows = append(rows, claude)
		}
		if kind == "claudes" {
			rows = []*Peer{claude}
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
	self := &Peer{Role: "shell", Pane: "%1", PID: 1, Index: 1, CWD: "/tmp", Cmd: "k v", Self: true}
	other := &Peer{Role: "shell", Pane: "%2", PID: 2, Index: 2, CWD: "/tmp", Cmd: "make"}
	if !strings.Contains(strings.Join(shellCells(self, false), " "), "← me") {
		t.Errorf("self shell row must carry the me mark")
	}
	if strings.Contains(strings.Join(shellCells(other, false), " "), "← me") {
		t.Errorf("non-self row must not carry the me mark")
	}
	// idle self (stale TMUX_PANE pointing at a commandless pane): the mark
	// must still render so display and JSON agree
	idle := &Peer{Role: "shell", Pane: "%3", PID: 3, Index: 3, CWD: "/tmp", Self: true}
	if !strings.Contains(strings.Join(shellCells(idle, false), " "), "← me") {
		t.Errorf("idle self row must still carry the me mark")
	}
	agent := &Peer{Name: "c", Pane: "%4", PID: 4, Index: 4, Self: true}
	if !strings.Contains(strings.Join(peerCells(agent, nil, false, time.Now()), " "), "← me") {
		t.Errorf("self agent row must carry the me mark")
	}
}
