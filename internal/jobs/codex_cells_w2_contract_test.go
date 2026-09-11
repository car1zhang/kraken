package jobs

// BLIND logic-dimension tests for WAVE 2 (cell spawn surface) of the
// codex-cells build, authored from 55-contract-cellsurface.md's "WAVE 2"
// section ALONE — the implementation in job.go / cell.go was never read.
// Every assertion is derived from the frozen contract text; a contract
// silence is recorded in the slice report as join evidence rather than
// guessed at here.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
)

// --- W2-jobs: ParseJob agent case ---------------------------------------
//
// Contract (W2-jobs): a frontmatter `agent:` line sets j.Agent to its
// trimmed lowercased value; `agent: codex`→"codex", `agent: claude`→"claude",
// absent→"". A valid agent leaves j.Err == "".

func codexW2ContractFrontmatter(agentLine string) []byte {
	// A minimal valid job (job:/repo:), matching how other job_test.go cases
	// build fixtures. agentLine is inserted verbatim (may be empty).
	src := "---\njob: demo\nrepo: monolith\n"
	if agentLine != "" {
		src += agentLine + "\n"
	}
	src += "---\n\n# Spec\ndo the thing.\n"
	return []byte(src)
}

func TestCodexW2ContractParseJobAgentCodex(t *testing.T) {
	j := ParseJob("demo.job.md", codexW2ContractFrontmatter("agent: codex"))
	if j.Err != "" {
		t.Fatalf("agent: codex should be a valid job, got Err=%q", j.Err)
	}
	if j.Agent != "codex" {
		t.Fatalf("Agent = %q, want %q", j.Agent, "codex")
	}
}

func TestCodexW2ContractParseJobAgentClaude(t *testing.T) {
	j := ParseJob("demo.job.md", codexW2ContractFrontmatter("agent: claude"))
	if j.Err != "" {
		t.Fatalf("agent: claude should be a valid job, got Err=%q", j.Err)
	}
	if j.Agent != "claude" {
		t.Fatalf("Agent = %q, want %q", j.Agent, "claude")
	}
}

func TestCodexW2ContractParseJobAgentAbsent(t *testing.T) {
	j := ParseJob("demo.job.md", codexW2ContractFrontmatter(""))
	if j.Err != "" {
		t.Fatalf("no agent line should be a valid job, got Err=%q", j.Err)
	}
	if j.Agent != "" {
		t.Fatalf("absent agent: Agent = %q, want empty", j.Agent)
	}
}

// --- W2-cell: codexModelArgs --------------------------------------------
//
// Contract: `["-m", j.Model]` when j.Model != "". (The Model-unset → nil
// branch is the edge author's dimension.)

func TestCodexW2ContractCodexModelArgsSet(t *testing.T) {
	j := &Job{Agent: "codex", Slug: "demo", Model: "gpt-5-codex"}
	got := codexModelArgs(j)
	want := []string{"-m", "gpt-5-codex"}
	if len(got) != len(want) {
		t.Fatalf("codexModelArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("codexModelArgs = %v, want %v", got, want)
		}
	}
}

// --- W2-cell: codexCellArgs ---------------------------------------------
//
// Contract: `[]string{core.CodexLauncher(), "--dangerously-bypass-approvals-and-sandbox"}`
// followed by codexModelArgs(j). Launcher first; bypass flag present; model
// args appended. No --dangerously-bypass-hook-trust in Wave 2.

func TestCodexW2ContractCodexCellArgsWithModel(t *testing.T) {
	j := &Job{Agent: "codex", Slug: "demo", Model: "gpt-5-codex"}
	got := codexCellArgs(j)
	want := []string{core.CodexLauncher(), "--dangerously-bypass-approvals-and-sandbox", "-m", "gpt-5-codex"}
	if len(got) != len(want) {
		t.Fatalf("codexCellArgs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("codexCellArgs[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestCodexW2ContractCodexCellArgsLauncherFirstAndBypass(t *testing.T) {
	j := &Job{Agent: "codex", Slug: "demo"}
	got := codexCellArgs(j)
	if len(got) == 0 {
		t.Fatalf("codexCellArgs returned empty")
	}
	if got[0] != core.CodexLauncher() {
		t.Fatalf("launcher not first: got[0]=%q, want %q", got[0], core.CodexLauncher())
	}
	if !codexW2ContractHas(got, "--dangerously-bypass-approvals-and-sandbox") {
		t.Fatalf("bypass flag absent: %v", got)
	}
	// Wave 2 installs no hooks, so the hook-trust bypass must NOT appear.
	if codexW2ContractHas(got, "--dangerously-bypass-hook-trust") {
		t.Fatalf("hook-trust bypass must be absent in Wave 2: %v", got)
	}
}

func codexW2ContractHas(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// --- W2-cell: codexAgentsMD ---------------------------------------------
//
// Contract: a short kraken-generated header noting the file is kraken-written
// and must not be committed, then the brief verbatim.

func TestCodexW2ContractCodexAgentsMDContainsBrief(t *testing.T) {
	const brief = "ZEUS-BRIEF-SENTINEL: you are zeus for the demo cell."
	md := codexAgentsMD(brief)
	if !strings.Contains(md, brief) {
		t.Fatalf("codexAgentsMD does not contain the brief verbatim:\n%s", md)
	}
	// Header must note the file must not be committed.
	if !strings.Contains(strings.ToLower(md), "commit") {
		t.Fatalf("codexAgentsMD header should mention not committing the file:\n%s", md)
	}
}

// --- W2-cell: codexZeusLaunch -------------------------------------------
//
// Contract: writes <wt>/AGENTS.md = codexAgentsMD(brief) (0644); stages the
// prompt to spawnPromptPath(j) when non-empty; builds argv = codexCellArgs(j)
// with, on resume, "resume" and the session id inserted right after the
// launcher, and the staged prompt appended LAST as a positional
// "$(cat <promptPath>)" via core.ShellReadFileArg. Returns accountEnvPrefix(j)
// + command (empty prefix for an unbound v1 job).

func TestCodexW2ContractCodexZeusLaunchFresh(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wt := t.TempDir()
	j := &Job{Agent: "codex", Slug: "demo"} // unbound: AccountSlug == ""
	// StartCell creates the cell dir before spawning; stagedCommand does no
	// MkdirAll, so a hermetic test must create it (same setup spawnCommand
	// tests use).
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("mkdir cell dir: %v", err)
	}
	const brief = "AGENTS-BRIEF-SENTINEL"
	const prompt = "PROMPT-SENTINEL"

	cmd, err := codexZeusLaunch(j, wt, brief, prompt, "")
	if err != nil {
		t.Fatalf("codexZeusLaunch fresh: unexpected err: %v", err)
	}

	// AGENTS.md written into the worktree with the brief.
	agents := filepath.Join(wt, "AGENTS.md")
	data, rerr := os.ReadFile(agents)
	if rerr != nil {
		t.Fatalf("AGENTS.md not written to worktree: %v", rerr)
	}
	if !strings.Contains(string(data), brief) {
		t.Fatalf("AGENTS.md does not contain the brief:\n%s", string(data))
	}

	// Command runs codex and carries the staged prompt positionally via cat.
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("command does not invoke codex: %q", cmd)
	}
	if !strings.Contains(cmd, core.ShellReadFileArg(spawnPromptPath(j))) {
		t.Fatalf("command lacks positional $(cat <promptPath>): %q", cmd)
	}
	// A fresh launch has no resume clause.
	if strings.Contains(cmd, "resume") {
		t.Fatalf("fresh launch should not contain a resume clause: %q", cmd)
	}
	// Unbound v1 job: no account env prefix.
	if strings.Contains(cmd, "CODEX_HOME") || strings.Contains(cmd, "CLAUDE_SECURESTORAGE") {
		t.Fatalf("unbound job command must carry no account env prefix: %q", cmd)
	}
}

func TestCodexW2ContractCodexZeusLaunchResume(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wt := t.TempDir()
	j := &Job{Agent: "codex", Slug: "demo"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("mkdir cell dir: %v", err)
	}
	const brief = "AGENTS-BRIEF-SENTINEL"
	const prompt = "PROMPT-SENTINEL"
	const sid = "sess-abc123"

	cmd, err := codexZeusLaunch(j, wt, brief, prompt, sid)
	if err != nil {
		t.Fatalf("codexZeusLaunch resume: unexpected err: %v", err)
	}
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("resume command does not invoke codex: %q", cmd)
	}
	if !strings.Contains(cmd, "resume") {
		t.Fatalf("resume command lacks 'resume': %q", cmd)
	}
	if !strings.Contains(cmd, sid) {
		t.Fatalf("resume command lacks the session id %q: %q", sid, cmd)
	}
	// AGENTS.md is still written on resume.
	if _, rerr := os.ReadFile(filepath.Join(wt, "AGENTS.md")); rerr != nil {
		t.Fatalf("resume did not write AGENTS.md: %v", rerr)
	}
}

// --- W2-cell: zeusLaunch dispatch ---------------------------------------
//
// Contract: agent-aware launch builder both spawn paths route through. A
// codex job routes to the codex path (writes AGENTS.md, command invokes
// codex). A claude job produces the spawnCommand shape unchanged — no
// AGENTS.md, and it does not invoke codex.

func TestCodexW2ContractZeusLaunchCodexRoutes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wt := t.TempDir()
	j := &Job{Agent: "codex", Slug: "demo"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("mkdir cell dir: %v", err)
	}

	cmd, err := zeusLaunch(j, wt, "prompt-text", "")
	if err != nil {
		t.Fatalf("zeusLaunch codex: unexpected err: %v", err)
	}
	if _, rerr := os.ReadFile(filepath.Join(wt, "AGENTS.md")); rerr != nil {
		t.Fatalf("codex job did not write AGENTS.md into the worktree: %v", rerr)
	}
	if !strings.Contains(cmd, "codex") {
		t.Fatalf("codex job command does not invoke codex: %q", cmd)
	}
}

func TestCodexW2ContractZeusLaunchClaudeShape(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wt := t.TempDir()
	j := &Job{Agent: "claude", Slug: "demo"}
	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatalf("mkdir cell dir: %v", err)
	}

	cmd, err := zeusLaunch(j, wt, "prompt-text", "")
	if err != nil {
		t.Fatalf("zeusLaunch claude: unexpected err: %v", err)
	}
	if cmd == "" {
		t.Fatalf("claude job produced an empty command")
	}
	// The claude path is spawnCommand's shape, not the codex path: it writes
	// no AGENTS.md into the worktree and does not invoke codex.
	if _, rerr := os.Stat(filepath.Join(wt, "AGENTS.md")); rerr == nil {
		t.Fatalf("claude job must not write AGENTS.md into the worktree")
	}
	if strings.Contains(cmd, core.CodexLauncher()+" ") {
		t.Fatalf("claude job command must not invoke the codex launcher: %q", cmd)
	}
}

// --- W2-cell: isTrustPrompt ---------------------------------------------
//
// Contract: true if text contains the claude needle
// "Is this a project you created or one you trust" OR the codex needle
// "Do you trust the contents of this directory".

func TestCodexW2ContractIsTrustPromptClaudeNeedle(t *testing.T) {
	if !isTrustPrompt("... Is this a project you created or one you trust ...") {
		t.Fatalf("claude needle should be recognised as a trust prompt")
	}
}

func TestCodexW2ContractIsTrustPromptCodexNeedle(t *testing.T) {
	if !isTrustPrompt("... Do you trust the contents of this directory? ...") {
		t.Fatalf("codex needle should be recognised as a trust prompt")
	}
}

// --- W2-cell: CellZeus positional election of a codex zeus --------------
//
// Contract: the positional-fallback condition becomes
// `(p.Agent == "claude" || p.Agent == "codex") && p.Depth == 0`. A codex zeus
// has no --name, so it is elected only positionally. Build a depth-0 codex
// peer in the cell's session whose Name is NOT ZeusName(j).

func TestCodexW2ContractCellZeusElectsCodexPane(t *testing.T) {
	j := &Job{Agent: "codex", Slug: "demo"}
	sess := CellSession(j)
	if got := ZeusName(j); got == "codex-thread-title" {
		t.Fatalf("test fixture Name accidentally equals ZeusName(j)=%q", got)
	}
	zeus := &core.Peer{
		Agent:       "codex",
		Depth:       0,
		TmuxSession: sess,
		Name:        "codex-thread-title", // a codex pane carries the thread title, never ZeusName(j)
		SessionID:   "sess-abc123",
	}
	all := []*core.Peer{zeus}
	got := CellZeus(j, all)
	if got != zeus {
		t.Fatalf("CellZeus did not elect the depth-0 codex pane: got %+v", got)
	}
}
