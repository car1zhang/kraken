package jobs

// WAVE 2 (cell spawn surface) — EDGE-dimension BLIND tests.
//
// Authored from 55-contract-cellsurface.md's "WAVE 2" section ALONE, without
// reading internal/jobs/job.go or internal/jobs/cell.go. These cover the
// boundaries, error paths, and degradation modes the contract mandates:
//   - ParseJob's agent-validation error path (and its non-empty guard);
//   - codexModelArgs' nil (no-model) return;
//   - codexCellArgs' exact no-model shape (launcher + bypass flag, no -m);
//   - codexZeusLaunch's negatives: empty prompt → no positional cat, and an
//     unbound job → no account/CODEX_HOME env prefix;
//   - zeusLaunch's claude arm: resume becomes -r <id> and NO AGENTS.md is
//     written into the worktree (the claude-path proof);
//   - isTrustPrompt's two independent needles and its false default;
//   - CellZeus' exclusions for codex panes: a thread-prefixed codex pane is
//     never elected, and a codex pane in a foreign tmux session is never
//     elected.
//
// Happy paths (valid agent values, model-set arg forms, AGENTS.md body content,
// resume-set codex commands, the depth-0 codex election) are the other
// dimension's. Every assertion here is one strict answer; a silence is join
// evidence, not a pass.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kraken/internal/core"
)

// TestCodexW2EdgeParseJobBadAgent — the agent-frontmatter error path.
// Contract: a value other than claude/codex (and non-empty) sets
// j.Err = "agent must be claude or codex"; an empty value falls under the
// non-empty guard and does NOT error (Agent stays ""); the validation runs on
// the trimmed, lowercased value.
func TestCodexW2EdgeParseJobBadAgent(t *testing.T) {
	// an out-of-range value errs loudly with the mandated message fragment.
	for _, val := range []string{"gpt", "codexx", "claudee", "gemini"} {
		src := "---\njob: x\nrepo: monolith\nagent: " + val + "\n---\n"
		j := ParseJob("x.job.md", []byte(src))
		if !strings.Contains(j.Err, "agent must be") {
			t.Errorf("agent %q: j.Err = %q, want it to contain %q", val, j.Err, "agent must be")
		}
	}

	// the check runs on the LOWERCASED value, so an uppercase invalid still errs.
	if j := ParseJob("x.job.md", []byte("---\njob: x\nagent: GPT\n---\n")); !strings.Contains(j.Err, "agent must be") {
		t.Errorf("agent GPT (uppercase invalid): j.Err = %q, want it to contain %q", j.Err, "agent must be")
	}

	// the non-empty guard: an EMPTY agent value is not "other than claude or
	// codex" — it must parse clean and leave Agent == "" (mirrors the model:
	// empty case).
	if j := ParseJob("x.job.md", []byte("---\njob: x\nagent:\n---\n")); j.Err != "" || j.Agent != "" {
		t.Errorf("empty agent value: j.Err = %q, j.Agent = %q, want no error and empty agent", j.Err, j.Agent)
	}

	// absent agent line leaves Agent == "" and no error.
	if j := ParseJob("x.job.md", []byte("---\njob: x\nrepo: monolith\n---\n")); j.Err != "" || j.Agent != "" {
		t.Errorf("absent agent line: j.Err = %q, j.Agent = %q, want no error and empty agent", j.Err, j.Agent)
	}
}

// TestCodexW2EdgeCodexModelArgsEmpty — codexModelArgs with no model.
// Contract (Q10): j.Model == "" → nil, so codex uses its config default and no
// -m flag is ever emitted. nil is the strict answer, not an empty non-nil
// slice.
func TestCodexW2EdgeCodexModelArgsEmpty(t *testing.T) {
	if got := codexModelArgs(&Job{}); got != nil {
		t.Errorf("codexModelArgs(no model) = %#v, want nil (no -m for a config-default codex)", got)
	}
	if got := codexModelArgs(&Job{Agent: "codex"}); got != nil {
		t.Errorf("codexModelArgs(codex, no model) = %#v, want nil", got)
	}
}

// TestCodexW2EdgeCodexCellArgsNoModel — the no-model cell-args shape.
// Contract: codexCellArgs = [CodexLauncher(), "--dangerously-bypass-approvals-and-sandbox"]
// followed by codexModelArgs(j). With no model, that trailing slice is nil, so
// the result is EXACTLY launcher + bypass flag, launcher first, and carries no
// -m.
func TestCodexW2EdgeCodexCellArgsNoModel(t *testing.T) {
	got := codexCellArgs(&Job{Agent: "codex"})
	want := []string{core.CodexLauncher(), "--dangerously-bypass-approvals-and-sandbox"}
	if len(got) != len(want) {
		t.Fatalf("codexCellArgs(no model) = %#v, want exactly %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("codexCellArgs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	for _, a := range got {
		if a == "-m" {
			t.Errorf("codexCellArgs(no model) = %#v, must not contain -m", got)
		}
	}
}

// TestCodexW2EdgeCodexZeusLaunchEmptyPromptUnbound — codexZeusLaunch's
// negatives. Contract: the positional "$(cat <prompt>)" is appended ONLY when
// the prompt is non-empty, and the returned prefix is empty for an unbound v1
// job (AccountSlug == ""), so neither CLAUDE_SECURESTORAGE nor CODEX_HOME
// appears. It still writes AGENTS.md into the worktree unconditionally.
func TestCodexW2EdgeCodexZeusLaunchEmptyPromptUnbound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	wt := t.TempDir()
	j := &Job{Slug: "cdx-unbound", Agent: "codex"} // Model unset, AccountSlug == "" (unbound)

	got, err := codexZeusLaunch(j, wt, "the zeus brief body", "", "")
	if err != nil {
		t.Fatalf("codexZeusLaunch(empty prompt, unbound): %v", err)
	}

	// empty prompt → no positional command-substitution.
	if strings.Contains(got, "$(cat") {
		t.Errorf("codexZeusLaunch with empty prompt appended a positional cat: %q", got)
	}
	// no resume session given → no `resume` verb.
	if strings.Contains(got, "resume") {
		t.Errorf("codexZeusLaunch with empty resumeSession contains a resume verb: %q", got)
	}
	// unbound v1 job → no account env prefix of either flavour.
	if strings.Contains(got, "CLAUDE_SECURESTORAGE") {
		t.Errorf("codexZeusLaunch(unbound) carries a CLAUDE_SECURESTORAGE prefix: %q", got)
	}
	if strings.Contains(got, "CODEX_HOME") {
		t.Errorf("codexZeusLaunch(unbound) carries a CODEX_HOME prefix: %q", got)
	}
	// sanity: it is a codex command and it wrote the brief file.
	if !strings.Contains(got, core.CodexLauncher()) {
		t.Errorf("codexZeusLaunch command %q does not invoke the codex launcher %q", got, core.CodexLauncher())
	}
	if _, statErr := os.Stat(filepath.Join(wt, "AGENTS.md")); statErr != nil {
		t.Errorf("codexZeusLaunch did not write AGENTS.md into the worktree: %v", statErr)
	}
}

// TestCodexW2EdgeZeusLaunchClaudeResume — the claude arm of zeusLaunch.
// Contract: for a claude job (Agent == "" or "claude") with resumeSession set,
// resume = ["-r", resumeSession] flows into the argv, and the claude path NEVER
// writes AGENTS.md into the worktree (that is the codex arm's file). This is
// the claude-path proof: a claude job through zeusLaunch must leave the wt with
// no AGENTS.md.
func TestCodexW2EdgeZeusLaunchClaudeResume(t *testing.T) {
	for _, agent := range []string{"", "claude"} {
		agent := agent
		t.Run("agent="+agent, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			j := &Job{Slug: "claudejob", Agent: agent}
			if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
				t.Fatal(err)
			}
			wt := t.TempDir()

			got, err := zeusLaunch(j, wt, "", "sess-abc-123")
			if err != nil {
				t.Fatalf("zeusLaunch(claude, resume): %v", err)
			}
			// resume becomes -r <id>, adjacent in the argv.
			if !strings.Contains(got, "-r sess-abc-123") {
				t.Errorf("zeusLaunch(claude, resume) = %q, want it to contain %q", got, "-r sess-abc-123")
			}
			// the claude path must NOT write AGENTS.md into the worktree.
			if _, statErr := os.Stat(filepath.Join(wt, "AGENTS.md")); !os.IsNotExist(statErr) {
				t.Errorf("zeusLaunch(claude) wrote AGENTS.md into the worktree (stat err = %v); the claude path must never create it", statErr)
			}
		})
	}
}

// TestCodexW2EdgeIsTrustPrompt — isTrustPrompt's two independent needles.
// Contract: true iff the text contains the claude needle
// "Is this a project you created or one you trust" OR the codex needle
// "Do you trust the contents of this directory". Each needle triggers it on its
// own; unrelated text (and the empty string) is false.
func TestCodexW2EdgeIsTrustPrompt(t *testing.T) {
	// each needle, independently, in text that lacks the other.
	claude := "banner: Is this a project you created or one you trust? [y/n]"
	if !isTrustPrompt(claude) {
		t.Errorf("isTrustPrompt(claude needle only) = false, want true")
	}
	codex := "prompt> Do you trust the contents of this directory? press Enter"
	if !isTrustPrompt(codex) {
		t.Errorf("isTrustPrompt(codex needle only) = false, want true")
	}
	// unrelated text and the empty string are false.
	for _, txt := range []string{"", "just some other pane content", "trust me on this one", "do you like the directory"} {
		if isTrustPrompt(txt) {
			t.Errorf("isTrustPrompt(%q) = true, want false", txt)
		}
	}
}

// TestCodexW2EdgeCellZeusExcludesCodex — CellZeus' exclusions for codex panes.
// The positional fallback now admits depth-0 codex panes, so the pre-existing
// guards must still hold for them: a thread-prefixed codex pane is filtered by
// name, and a codex pane in a different tmux session is out of the cell
// entirely. Both must yield NO zeus.
func TestCodexW2EdgeCellZeusExcludesCodex(t *testing.T) {
	j := &Job{Slug: "cdxelect"}
	sess := CellSession(j)

	t.Run("thread-prefixed codex pane is not elected", func(t *testing.T) {
		// depth-0 codex in the cell session, but wearing this job's thread name
		// prefix — the name filter must exclude it, so with only it present the
		// cell has no zeus.
		threadCodex := &core.Peer{
			TmuxSession: sess,
			Agent:       "codex",
			Depth:       0,
			Pane:        "%1",
			Name:        ThreadAgentName(j, "t1"),
			SessionID:   "thread-sess",
		}
		if got := CellZeus(j, []*core.Peer{threadCodex}); got != nil {
			t.Errorf("CellZeus elected a thread-prefixed codex pane: %+v", got)
		}
	})

	t.Run("codex pane in a different session is not elected", func(t *testing.T) {
		// depth-0 unnamed codex, but its tmux session is not this cell's — it
		// belongs to some other cell/shell and must never be elected here.
		foreign := &core.Peer{
			TmuxSession: "unrelated-tmux-session-999",
			Agent:       "codex",
			Depth:       0,
			Pane:        "%1",
			SessionID:   "foreign-sess",
		}
		if got := CellZeus(j, []*core.Peer{foreign}); got != nil {
			t.Errorf("CellZeus elected a codex pane from a foreign tmux session %q: %+v", foreign.TmuxSession, got)
		}
	})
}
