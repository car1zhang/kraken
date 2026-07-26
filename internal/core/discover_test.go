package core

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
		{Kind: "thinking", Text: "hmm"},
		{Kind: "tool", Text: "Bash(git status)"},
		{Kind: "user", Text: "hi"},
		{Kind: "user", Text: "follow-up"},
		{Kind: "text", Text: "done"},
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

	blocks, counted, err := LastBlocks(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if counted != 1 {
		t.Fatalf("want counted=1, got %d", counted)
	}
	// last text block plus the tool call after it
	if len(blocks) != 2 || blocks[0] != (block{Kind: "text", Text: "second"}) || blocks[1].Kind != "tool" {
		t.Errorf("n=1: got %v", blocks)
	}

	blocks, counted, err = LastBlocks(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if counted != 2 || len(blocks) != 4 || blocks[0] != (block{Kind: "text", Text: "first"}) {
		t.Errorf("n=2: counted=%d blocks=%v", counted, blocks)
	}

	// n larger than available: return what exists
	blocks, counted, err = LastBlocks(path, 10)
	if err != nil {
		t.Fatal(err)
	}
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
	blocks, counted, err := LastBlocks(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if counted != 1 || len(blocks) != 1 || blocks[0] != (block{Kind: "user", Text: "next question"}) {
		t.Errorf("n=1: counted=%d blocks=%v", counted, blocks)
	}
	blocks, counted, err = LastBlocks(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if counted != 2 || len(blocks) != 2 || blocks[0] != (block{Kind: "text", Text: "answer"}) {
		t.Errorf("n=2: counted=%d blocks=%v", counted, blocks)
	}
}

func TestLastBlocksMissingFile(t *testing.T) {
	if _, _, err := LastBlocks(filepath.Join(t.TempDir(), "nope.jsonl"), 1); err == nil {
		t.Errorf("want error for a missing transcript file")
	}
}

func TestReadTranscriptPathGuards(t *testing.T) {
	_, err := ReadTranscriptPath(&Peer{Role: "shell", Name: "sh"})
	if err == nil || !strings.Contains(err.Error(), "no transcript to read") {
		t.Errorf("shell pane: got %v", err)
	}
	_, err = ReadTranscriptPath(&Peer{Agent: "codex", Name: "cx"})
	if err == nil || !strings.Contains(err.Error(), "Codex") {
		t.Errorf("codex: got %v", err)
	}
	_, err = ReadTranscriptPath(&Peer{Name: "noid"})
	if err == nil || !strings.Contains(err.Error(), "no session id resolved") {
		t.Errorf("empty session id: got %v", err)
	}
	_, err = ReadTranscriptPath(&Peer{Name: "guessed", SessionID: "abcd", SessionIDGuessed: true})
	if err == nil || !strings.Contains(err.Error(), "a guess") {
		t.Errorf("guessed session id: got %v", err)
	}
	_, err = ReadTranscriptPath(&Peer{Name: "fresh", SessionID: "no-such-session-id"})
	if err == nil || !strings.Contains(err.Error(), "no transcript yet") {
		t.Errorf("unresolvable transcript: got %v", err)
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
	// success paths only: failure paths call Die() which exits the process
	peers := []*Peer{
		{Name: "asr tuning", CWD: "/x/monolith", Pane: "%1", SessionID: "aaaa1111-0000"},
		{Name: "kraken tmux", CWD: "/x/kraken", Pane: "%2", SessionID: "bbbb2222-0000"},
		{AgentName: "helper", Role: "teammate", Pane: "%3", SessionID: "cccc3333-0000"},
	}
	for i, p := range peers {
		p.Index = i + 1
	}
	if got := SelectPeer("2", peers); got != peers[1] {
		t.Errorf("index: got %v", got)
	}
	if got := SelectPeer("asr tuning", peers); got != peers[0] {
		t.Errorf("multi-word name: got %v", got)
	}
	if got := SelectPeer("helper", peers); got != peers[2] {
		t.Errorf("agent name: got %v", got)
	}
	if got := SelectPeer("bbbb2222", peers); got != peers[1] {
		t.Errorf("uuid prefix: got %v", got)
	}
	if got := SelectPeer("KRAKEN", peers); got != peers[1] {
		t.Errorf("case-insensitive: got %v", got)
	}
	t.Setenv("TMUX_PANE", "%3")
	if got := SelectPeer(".", peers); got != peers[2] {
		t.Errorf("current pane: got %v", got)
	}
	// exact pane id: churn-proof, never fuzzy-matched
	if got := SelectPeer("%2", peers); got != peers[1] {
		t.Errorf("pane id: got %v", got)
	}
	// out-of-range integers fall through to name matching (tmux sessions are
	// often named "0", "1", ...)
	peers[0].TmuxSession = "7"
	if got := SelectPeer("7", peers); got != peers[0] {
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

// TestShellReadFileArg pins the escape hatch that keeps content-sized
// arguments off the tmux command line (tmux rejects anything past ~16KB with
// `command too long`). The word must survive a path containing spaces or a
// quote, and must stay ONE word after expansion — a brief with a blank line in
// it would otherwise word-split into a dozen arguments.
func TestShellReadFileArg(t *testing.T) {
	for _, c := range []struct{ path, want string }{
		{"/tmp/cells/x/zeus-brief.md", `"$(cat /tmp/cells/x/zeus-brief.md)"`},
		{"/tmp/a b/brief.md", `"$(cat '/tmp/a b/brief.md')"`},
		{"/tmp/it's/brief.md", `"$(cat '/tmp/it'\''s/brief.md')"`},
	} {
		if got := ShellReadFileArg(c.path); got != c.want {
			t.Errorf("ShellReadFileArg(%q) = %q, want %q", c.path, got, c.want)
		}
	}
	// the substitution must be double-quoted, or the shell splits the file's
	// contents on whitespace before claude ever sees them
	got := ShellReadFileArg("/tmp/x.md")
	if !strings.HasPrefix(got, `"$(cat `) || !strings.HasSuffix(got, `)"`) {
		t.Errorf("ShellReadFileArg must wrap the substitution in double quotes, got %q", got)
	}
}

func TestShellQuote(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"claude", "claude"},
		{"--model=opus", "--model=opus"},
		{"a b", "'a b'"},
		{"it's", `'it'\''s'`},
		{"", "''"},
	} {
		if got := ShellQuote(c.in); got != c.want {
			t.Errorf("ShellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestOrderPeers(t *testing.T) {
	mate := &Peer{AgentName: "helper", Role: "teammate"}
	lead := &Peer{Name: "lead", Teammates: []*Peer{mate}}
	other := &Peer{Name: "other"}
	ordered := OrderPeers([]*Peer{lead, mate, other})
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
	ordered := OrderPeers([]*Peer{lead, mid, sub})
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
	ordered := OrderPeers([]*Peer{a, b, mate})
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

func TestRefinedStatus(t *testing.T) {
	cases := []struct {
		name    string
		current string
		pane    string
		want    string
	}{
		{"shell running", "shell", "some output\nesc to interrupt\n", "running"},
		{"shell waiting", "shell", "just a normal prompt\n", "waiting"},
		{"shell question", "shell", "Do you want to proceed?\n ❯ 1. Yes, proceed\n", "question"},
		{"unknown running", "unknown", "esc to interrupt\n", "running"},
		{"unknown waiting", "unknown", "plain output\n", "waiting"},
		{"running unchanged", "running", "plain output\n", "running"},
		{"waiting becomes question", "waiting", "blah\n ❯ 1. Yes\n", "question"},
		{"unknown spinner running (2.1.217)", "unknown", "✢ Wibbling… (1m 7s · ↓ 2.1k tokens)\n", "running"},
		{"shell spinner running (2.1.217)", "shell", "✢ Wibbling… (1m 7s · ↓ 2.1k tokens)\n", "running"},
		{"legacy esc-to-interrupt still running", "unknown", "some output\nesc to interrupt\n", "running"},
		{"idle prompt no spinner waiting", "unknown", "some prior output\n❯ \n", "waiting"},
		{"ellipsis-paren without glyph does not match", "unknown", "Loading… (see docs)\n", "waiting"},
		{"glyph without ellipsis-paren does not match", "unknown", "✢ note\n", "waiting"},
		{"question wins over spinner", "unknown", "✢ Wibbling… (1m 7s · ↓ 2.1k tokens)\nDo you want to proceed?\n ❯ 1. Yes\n", "question"},
		{"middle-dot glyph frame running", "unknown", "· Wibbling… (3s)\n", "running"},
		{"short no-token spinner frame running", "unknown", "✳ Creating… (27s)\n", "running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := refinedStatus(c.current, c.pane); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
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

func TestBranchGuard(t *testing.T) {
	if err := BranchGuard(&Peer{Name: "x"}); err == nil {
		t.Errorf("missing session id must be rejected")
	}
	if err := BranchGuard(&Peer{Name: "x", SessionID: "abc", SessionIDGuessed: true}); err == nil {
		t.Errorf("guessed session id must be rejected")
	}
	if err := BranchGuard(&Peer{Name: "x", SessionID: "abc"}); err != nil {
		t.Errorf("clean session id rejected: %v", err)
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

func TestKillPeerGuards(t *testing.T) {
	// guards fire before any tmux exec, so these are safe to run anywhere;
	// the message text is asserted so a regressed guard can't pass by way of
	// verifyKillTarget failing on the missing pane instead
	_, err := KillPeer(&Peer{AgentName: "ip", Role: "teammate"})
	if err == nil || !strings.Contains(err.Error(), "in-process") {
		t.Errorf("killing an in-process teammate must be refused, got %v", err)
	}
	_, err = KillPeer(&Peer{Name: "nested", Role: "main", Pane: "%1", Depth: 1})
	if err == nil || !strings.Contains(err.Error(), "nested") {
		t.Errorf("killing a nested claude must be refused, got %v", err)
	}
	t.Setenv("TMUX_PANE", "%7")
	_, err = KillPeer(&Peer{Role: "shell", Pane: "%7", CWD: "/tmp"})
	if err == nil || !strings.Contains(err.Error(), "current pane") {
		t.Errorf("killing the current pane must be refused, got %v", err)
	}
}

func TestSendToPeerGuards(t *testing.T) {
	// guards fire before any tmux exec, so these are safe to run anywhere;
	// message text is asserted so a regressed guard can't pass by way of
	// checkSendTarget failing on the missing pane instead
	if _, err := SendToPeer(&Peer{Role: "shell", Name: "sh"}, "hi"); err == nil || !strings.Contains(err.Error(), "shell pane") {
		t.Errorf("shell pane: got %v", err)
	}
	if _, err := SendToPeer(&Peer{Name: "x", Messageable: true, Pane: "%1"}, "  "); err == nil || !strings.Contains(err.Error(), "empty message") {
		t.Errorf("empty message: got %v", err)
	}
	if _, err := SendToPeer(&Peer{AgentName: "ip", Role: "teammate", Messageable: false}, "hi"); err == nil || !strings.Contains(err.Error(), "in-process") {
		t.Errorf("in-process teammate: got %v", err)
	}
	if _, err := SendToPeer(&Peer{Name: "nested", Messageable: true, Pane: "%1", Depth: 1}, "hi"); err == nil || !strings.Contains(err.Error(), "not the foreground agent") {
		t.Errorf("nested agent: got %v", err)
	}
}

func TestGuardBareSlashCommand(t *testing.T) {
	cases := []struct {
		name string
		msg  string
		want string
	}{
		{"bare command gets a trailing space", "/do", "/do "},
		{"bare command with different name", "/doc", "/doc "},
		{"command with arguments is untouched", "/do implement the thing", "/do implement the thing"},
		{"command with only trailing space is untouched", "/do ", "/do "},
		{"plain text is untouched", "hello zeus", "hello zeus"},
		{"empty message is untouched", "", ""},
		{"multi-line message is untouched", "/do\nmore", "/do\nmore"},
	}
	for _, c := range cases {
		if got := guardBareSlashCommand(c.msg); got != c.want {
			t.Errorf("%s: guardBareSlashCommand(%q) = %q, want %q", c.name, c.msg, got, c.want)
		}
	}
}

func TestTmuxKeyName(t *testing.T) {
	cases := []struct {
		name string
		want string
	}{
		{"esc", "Escape"},
		{"escape", "Escape"},
		{"enter", "Enter"},
		{"tab", "Tab"},
		{"backspace", "BSpace"},
		{"up", "Up"},
		{"down", "Down"},
		{"left", "Left"},
		{"right", "Right"},
		{"ctrl-c", "C-c"},
		{"ctrl-a", "C-a"},
		{"ctrl-z", "C-z"},
	}
	for _, c := range cases {
		if got, ok := tmuxKeyName(c.name); !ok || got != c.want {
			t.Errorf("tmuxKeyName(%q) = (%q, %v), want (%q, true)", c.name, got, ok, c.want)
		}
	}
	if _, ok := tmuxKeyName("ctrl-1"); ok {
		t.Errorf("tmuxKeyName(\"ctrl-1\") should be unsupported")
	}
	if _, ok := tmuxKeyName("pageup"); ok {
		t.Errorf("tmuxKeyName(\"pageup\") should be unsupported")
	}
	if _, ok := tmuxKeyName(""); ok {
		t.Errorf("tmuxKeyName(\"\") should be unsupported")
	}
}

func TestSendKeyGuards(t *testing.T) {
	// guards fire before any tmux exec, so these are safe to run anywhere;
	// message text is asserted so a regressed guard can't pass by way of
	// checkSendTarget/verifyKillTarget failing on the missing pane instead
	if err := SendKey(&Peer{Role: "agent", Pane: "%1"}, "nope"); err == nil || !strings.Contains(err.Error(), "unsupported key") {
		t.Errorf("unknown key: got %v", err)
	}
	if err := SendKey(&Peer{Name: "nested", Pane: "%1", Depth: 1, Messageable: true}, "esc"); err == nil || !strings.Contains(err.Error(), "not the foreground agent") {
		t.Errorf("nested agent: got %v", err)
	}
	if err := SendKey(&Peer{Name: "inproc"}, "esc"); err == nil || !strings.Contains(err.Error(), "in-process teammate") {
		t.Errorf("in-process teammate: got %v", err)
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
