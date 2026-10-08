package core

// Regression pin for 50-contract.md amendment 10 (PR 2 of job 542): the
// transcript arm of `kraken mm source` locates a record by a forward scan of
// the WHOLE file, so a uuid older than LastBlocks' tail window is still found.
// Written by the lead, sighted.

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBlocksAroundFindsAUUIDBeyondTheTailWindow(t *testing.T) {
	// Pad past lastBlocksScanLimit so the record under test sits outside any
	// tail read; each padding record is its own block.
	pad := `{"type":"user","uuid":"pad","message":{"content":"` + strings.Repeat("x", 4000) + `"}}`
	lines := []string{
		`{"type":"user","uuid":"u-before-2","message":{"content":"two before"}}`,
		`{"type":"user","uuid":"u-before-1","message":{"content":"one before"}}`,
		`{"type":"user","uuid":"u-target","message":{"content":"the captured prompt"}}`,
		`{"type":"user","uuid":"u-after-1","message":{"content":"one after"}}`,
		`{"type":"user","uuid":"u-after-2","message":{"content":"two after"}}`,
		`{"type":"user","uuid":"u-after-3","message":{"content":"three after"}}`,
	}
	for int64(len(lines)) < lastBlocksScanLimit/int64(len(pad))+16 {
		lines = append(lines, pad)
	}
	path := writeTranscript(t, lines...)

	blocks, first, last, found, err := BlocksAround(path, "u-target", 2)
	if err != nil {
		t.Fatalf("BlocksAround: %v", err)
	}
	if !found {
		t.Fatal("the target uuid sits before the tail window and must still be found")
	}
	if first != 2 || last != 2 {
		t.Fatalf("first/last = %d/%d, want 2/2 (two blocks of context before the hit)", first, last)
	}
	if len(blocks) != 5 {
		t.Fatalf("got %d blocks, want 5 (radius 2 on each side)", len(blocks))
	}
	if blocks[0].UUID != "u-before-2" || blocks[2].UUID != "u-target" || blocks[4].UUID != "u-after-2" {
		t.Fatalf("window is %q %q %q, want before-2 / target / after-2",
			blocks[0].UUID, blocks[2].UUID, blocks[4].UUID)
	}

	if _, _, _, found, err := BlocksAround(path, "u-missing", 2); err != nil || found {
		t.Fatalf("an absent uuid: found=%v err=%v, want false/nil", found, err)
	}
	if _, _, _, found, _ := BlocksAround(path, "", 2); found {
		t.Fatal("an empty uuid must never match")
	}
}

// Amendment 13 (edge review r2 N1/N2): the turn key is the last PROMPT, found
// by a whole-file scan — a 4 MB+ turn of tool results must not hide it — and
// the user records Claude Code writes itself (task notifications, slash
// command echoes, the compaction summary, an interrupt marker) never move it.
func TestLastTurnUUIDSurvivesLongTurnsAndHarnessRecords(t *testing.T) {
	big := strings.Repeat("y", 4000)
	lines := []string{
		`{"type":"user","uuid":"u-first","message":{"content":"first prompt"}}`,
		`{"type":"user","uuid":"u-prompt","message":{"content":"the real prompt"}}`,
	}
	// a turn longer than lastBlocksScanLimit, made of tool_result-only user
	// records (no block) and assistant text records
	toolResult := `{"type":"user","uuid":"u-tool","message":{"content":[{"type":"tool_result","content":"` + big + `"}]}}`
	for int64(len(lines)) < lastBlocksScanLimit/int64(len(toolResult))+8 {
		lines = append(lines, toolResult)
	}
	lines = append(lines,
		`{"type":"user","uuid":"u-task","message":{"content":"<task-notification>\n<task-id>x</task-id>\n</task-notification>"}}`,
		`{"type":"user","uuid":"u-cmd","message":{"content":"<command-name>/color</command-name>\n<command-message>color</command-message>"}}`,
		`{"type":"user","uuid":"u-out","message":{"content":"<local-command-stdout>ok</local-command-stdout>"}}`,
		`{"type":"user","uuid":"u-bash","message":{"content":"<bash-input>git status</bash-input>"}}`,
		`{"type":"user","uuid":"u-bash-out","message":{"content":"<bash-stdout>clean</bash-stdout><bash-stderr></bash-stderr>"}}`,
		`{"type":"user","uuid":"u-int","message":{"content":"[Request interrupted by user]"}}`,
		`{"type":"user","uuid":"u-compact","message":{"content":"This session is being continued from a previous conversation that ran out of context."}}`,
	)
	path := writeTranscript(t, lines...)
	if got := LastTurnUUID(path); got != "u-prompt" {
		t.Fatalf("LastTurnUUID = %q, want u-prompt (past a 4 MB turn, with seven harness records after it)", got)
	}
	// a skill invocation (`/do`, `/pr`) is carl's prompt and starts a turn
	lines = append(lines, `{"type":"user","uuid":"u-do","message":{"content":"<command-message>do</command-message>\n<command-name>/do</command-name>"}}`)
	path = writeTranscript(t, lines...)
	if got := LastTurnUUID(path); got != "u-do" {
		t.Fatalf("LastTurnUUID = %q, want u-do (carl typing /do starts a new turn)", got)
	}
	lines = append(lines, `{"type":"user","uuid":"u-next","message":{"content":"carl's next prompt"}}`)
	path = writeTranscript(t, lines...)
	if got := LastTurnUUID(path); got != "u-next" {
		t.Fatalf("LastTurnUUID = %q, want u-next (a real prompt starts a new turn)", got)
	}
	if got := LastTurnUUID(filepath.Join(t.TempDir(), "absent.jsonl")); got != "" {
		t.Fatalf("an absent transcript: %q, want \"\"", got)
	}
}
