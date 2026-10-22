package jobs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- transcript line builders, one per measured signal shape ---

func skillToolLine(t *testing.T, skill string) string {
	t.Helper()
	return assistantToolLine(t, "Skill", map[string]any{"skill": skill, "args": "x"})
}

func readToolLine(t *testing.T, path string) string {
	t.Helper()
	return assistantToolLine(t, "Read", map[string]any{"file_path": path})
}

func assistantToolLine(t *testing.T, tool string, input map[string]any) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "tool_use", "id": "toolu_1", "name": tool, "input": input},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func commandLine(t *testing.T, name string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": "<command-message>" + strings.TrimPrefix(name, "/") + "</command-message>\n<command-name>" + name + "</command-name>",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func toolResultLine(t *testing.T, text string) string {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"content": []map[string]any{
				{"type": "tool_result", "tool_use_id": "toolu_1", "content": text},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}

func writeStageTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestScanStageSignalShapes(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		want  string
	}{
		{"skill tool call", []string{skillToolLine(t, "aim")}, "aim"},
		{"scoped skill name", []string{skillToolLine(t, "apps/web:do")}, "do"},
		{"command message with slash", []string{commandLine(t, "/do")}, "do"},
		{"command message without slash", []string{commandLine(t, "aim")}, "aim"},
		{"scoped command message", []string{commandLine(t, "/kraken:do")}, "do"},
		// Containment pins for the widened command class: bodies the class
		// matches must still miss the set after normalization, so a future
		// "simplification" of the class or the trim fails here rather than
		// silently resolving prose-adjacent bodies.
		{"relative-path command body ignored", []string{commandLine(t, "../do")}, ""},
		{"trailing-slash command body ignored", []string{commandLine(t, "/do/")}, ""},
		{"read of worktree skill", []string{readToolLine(t, "/Users/x/.kraken/cells/foo-1/wt-kraken/.claude/skills/do/SKILL.md")}, "do"},
		{"read of shadowing personal skill", []string{readToolLine(t, "/Users/x/.claude/skills/info/SKILL.md")}, "info"},
		{"last signal wins across shapes", []string{commandLine(t, "/aim"), skillToolLine(t, "info"), readToolLine(t, "/wt/.claude/skills/do/SKILL.md")}, "do"},
		// `pr` folded into `do` (job 624): the PR is do's own last step, so
		// there is no pr skill, no pr stage, and an older transcript's Read of
		// pr/SKILL.md neither badges `pr` nor clears the `do` it was in.
		{"retired pr skill is not a stage", []string{readToolLine(t, "/wt/.claude/skills/pr/SKILL.md")}, ""},
		{"retired pr read does not clear do", []string{commandLine(t, "/do"), readToolLine(t, "/wt/.claude/skills/pr/SKILL.md")}, "do"},
		{"typed /pr is not a stage", []string{commandLine(t, "/do"), commandLine(t, "/pr")}, "do"},
		{"non-pipeline skill ignored", []string{skillToolLine(t, "claude-api")}, ""},
		{"load is not a stage", []string{skillToolLine(t, "aim"), readToolLine(t, "/wt/.claude/skills/load/SKILL.md")}, "aim"},
		// A typed /handoff (carl's, or the daemon's auto-handoff) arrives on
		// the command shape; it compacts the agent and returns it to the phase
		// it was in, so the badge must stay on that phase.
		{"handoff is not a stage", []string{commandLine(t, "/do"), commandLine(t, "/handoff"), readToolLine(t, "/wt/.claude/skills/handoff/SKILL.md")}, "do"},
		// A typed /direct (with or without a phase word after it) is a
		// modifier: the job reads direct/SKILL.md first and then the phase
		// skill's own sections, and only that second read moves the badge.
		{"direct is not a stage", []string{commandLine(t, "/aim"), commandLine(t, "/direct"), readToolLine(t, "/wt/.claude/skills/direct/SKILL.md")}, "aim"},
		{"direct then phase skill badges the phase", []string{commandLine(t, "/direct"), readToolLine(t, "/wt/.claude/skills/direct/SKILL.md"), readToolLine(t, "/wt/.claude/skills/do/SKILL.md")}, "do"},
		{"non-pipeline command ignored", []string{commandLine(t, "/login")}, ""},
		{"read of non-SKILL file ignored", []string{readToolLine(t, "/wt/.claude/skills/aim/assets/hermes-contract.md")}, ""},
		{"skills path without .claude ignored", []string{readToolLine(t, "/wt/docs/skills/aim/SKILL.md")}, ""},
		{"quoted command in tool_result ignored", []string{toolResultLine(t, "<command-name>/aim</command-name>")}, ""},
		// A deliberate decision, not an accident: `system`/`local_command`
		// records carry their `<command-name>` at the TOP level, not under
		// message.content, and today only built-ins (/color, /model, ...)
		// arrive on that shape — no pipeline skill ever has. If Claude Code
		// ever routes a typed /do through it, this case is where the silent
		// miss becomes a visible decision to revisit.
		{"system local_command record ignored", []string{`{"type":"system","subtype":"local_command","content":"<command-name>/do</command-name>"}`}, ""},
		{"malformed line skipped, later signal kept", []string{"{not json", skillToolLine(t, "info")}, "info"},
		{"empty transcript", []string{""}, ""},
		{"non-stage signal does not clear an earlier stage", []string{skillToolLine(t, "dream"), skillToolLine(t, "claude-api")}, "dream"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := scanStage(writeStageTranscript(t, tc.lines...)); got != tc.want {
				t.Errorf("scanStage = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFindStageMissingFileIsEmpty(t *testing.T) {
	if got := FindStage(filepath.Join(t.TempDir(), "nope.jsonl")); got != "" {
		t.Errorf("FindStage on missing file = %q, want empty", got)
	}
}

// A changed transcript must invalidate the cached verdict: the cache keys on
// (size, mtime), so appending a later signal to the same path re-scans and
// reports the new stage.
func TestFindStageCacheInvalidatesOnChange(t *testing.T) {
	p := writeStageTranscript(t, skillToolLine(t, "aim"))
	if got := FindStage(p); got != "aim" {
		t.Fatalf("first FindStage = %q, want aim", got)
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(commandLine(t, "/do") + "\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := FindStage(p); got != "do" {
		t.Errorf("FindStage after append = %q, want do", got)
	}
}
