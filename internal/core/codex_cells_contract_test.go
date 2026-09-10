package core

// codex_cells_contract_test.go — BLIND logic tests authored from
// artifacts/55-contract-cellsurface.md "WAVE 1" (frozen 2026-09-10). Covers the
// stated invariants and happy paths of W1-A (codex session identity by cwd +
// rollout-path resolution) and W1-B (codex rollout block reader + LastBlocks
// dispatch + ReadTranscriptPath codex arm). Edge/error cases (empty inputs,
// missing/corrupt db, unknown/malformed records, the "no rollout resolved"
// error path) are the other author's dimension and are deliberately absent.
//
// Authored blind: internal/core/discover.go and internal/core/read.go were NOT
// read by any route. Every assertion is derived from the contract text alone.
// AUTHOR-ONLY — not run here.

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// ---------------------------------------------------------------------------
// Fixtures
// ---------------------------------------------------------------------------

// codexCellsContractRow is one threads-table row for the state_5.sqlite fixture.
type codexCellsContractRow struct {
	ID          string
	Title       string
	Model       string
	TokensUsed  int64
	RolloutPath string
	CWD         string
	UpdatedAt   int64
}

// codexCellsContractWriteStateDB sets HOME to a fresh temp dir and writes a
// state_5.sqlite there, populated with the given rows. The db is closed before
// returning so the reader's read-only+immutable open sees a settled file. The
// schema is exactly the columns the W1-A readers SELECT, plus updated_at for
// the ORDER BY.
func codexCellsContractWriteStateDB(t *testing.T, rows []codexCellsContractRow) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY, title TEXT, model TEXT, tokens_used INTEGER, rollout_path TEXT, cwd TEXT, updated_at INTEGER)`); err != nil {
		db.Close()
		t.Fatalf("create threads: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO threads(id, title, model, tokens_used, rollout_path, cwd, updated_at) VALUES(?,?,?,?,?,?,?)`,
			r.ID, r.Title, r.Model, r.TokensUsed, r.RolloutPath, r.CWD, r.UpdatedAt,
		); err != nil {
			db.Close()
			t.Fatalf("insert row %+v: %v", r, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
}

// The four valid codex rollout records, one per record shape the contract maps.
// Each is a whole JSONL line of the {timestamp,type,payload} envelope.
const (
	codexCellsContractUserRecord      = `{"timestamp":"2026-09-10T00:00:01Z","type":"event_msg","payload":{"type":"user_message","message":"please build the thing"}}`
	codexCellsContractAgentRecord     = `{"timestamp":"2026-09-10T00:00:02Z","type":"event_msg","payload":{"type":"agent_message","message":"on it, building now"}}`
	codexCellsContractFuncCallRecord  = `{"timestamp":"2026-09-10T00:00:03Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":[\"ls\",\"-la\"]}"}}`
	codexCellsContractReasoningRecord = `{"timestamp":"2026-09-10T00:00:04Z","type":"response_item","payload":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking about the plan"}],"content":"thinking about the plan"}}`

	codexCellsContractUserText  = "please build the thing"
	codexCellsContractAgentText = "on it, building now"
)

// ---------------------------------------------------------------------------
// W1-A — readCodexThreadByCWD (discover.go)
// Obligation: SELECT id,title,model,tokens_used,rollout_path,cwd FROM threads
// WHERE cwd=? ORDER BY updated_at DESC LIMIT 1, returns that row and true.
// Logic: newest-updated row for a cwd wins; every selected column maps onto
// the matching codexThread field; distinct cwds resolve to their own rows.
// ---------------------------------------------------------------------------

func TestCodexCellsContractReadThreadByCWDNewestWins(t *testing.T) {
	codexCellsContractWriteStateDB(t, []codexCellsContractRow{
		// Two rows share cwd /work — the OLDER one is deliberately inserted
		// last so a naive "last row" reader would return it. ORDER BY
		// updated_at DESC must select the newer (updated_at 200) row.
		{ID: "id-old", Title: "old title", Model: "gpt-5.6-mini", TokensUsed: 11, RolloutPath: "/rollouts/old.jsonl", CWD: "/work", UpdatedAt: 100},
		{ID: "id-new", Title: "new title", Model: "gpt-5.6-terra", TokensUsed: 4242, RolloutPath: "/rollouts/new.jsonl", CWD: "/work", UpdatedAt: 200},
		// A third cwd with its own single row proves isolation.
		{ID: "id-other", Title: "other title", Model: "gpt-5.6-nano", TokensUsed: 7, RolloutPath: "/rollouts/other.jsonl", CWD: "/elsewhere", UpdatedAt: 300},
	})

	got, ok := readCodexThreadByCWD("/work")
	if !ok {
		t.Fatalf("readCodexThreadByCWD(/work) ok = false, want true (a row exists for that cwd)")
	}
	want := codexThread{
		ID:          "id-new",
		Title:       "new title",
		Model:       "gpt-5.6-terra",
		RolloutPath: "/rollouts/new.jsonl",
		CWD:         "/work",
		TokensUsed:  4242,
	}
	if got != want {
		t.Fatalf("readCodexThreadByCWD(/work) = %+v, want %+v (newest updated_at wins, all columns mapped)", got, want)
	}
}

func TestCodexCellsContractReadThreadByCWDDistinctCWD(t *testing.T) {
	codexCellsContractWriteStateDB(t, []codexCellsContractRow{
		{ID: "id-new", Title: "new title", Model: "gpt-5.6-terra", TokensUsed: 4242, RolloutPath: "/rollouts/new.jsonl", CWD: "/work", UpdatedAt: 200},
		{ID: "id-other", Title: "other title", Model: "gpt-5.6-nano", TokensUsed: 7, RolloutPath: "/rollouts/other.jsonl", CWD: "/elsewhere", UpdatedAt: 300},
	})

	got, ok := readCodexThreadByCWD("/elsewhere")
	if !ok {
		t.Fatalf("readCodexThreadByCWD(/elsewhere) ok = false, want true")
	}
	want := codexThread{
		ID:          "id-other",
		Title:       "other title",
		Model:       "gpt-5.6-nano",
		RolloutPath: "/rollouts/other.jsonl",
		CWD:         "/elsewhere",
		TokensUsed:  7,
	}
	if got != want {
		t.Fatalf("readCodexThreadByCWD(/elsewhere) = %+v, want %+v (its own row, not the /work row)", got, want)
	}
}

// ---------------------------------------------------------------------------
// W1-A — codexRolloutPath (discover.go)
// Obligation: SELECT rollout_path FROM threads WHERE id=?; returns the path.
// Logic: a matching id resolves to its rollout_path.
// ---------------------------------------------------------------------------

func TestCodexCellsContractRolloutPathResolvesID(t *testing.T) {
	codexCellsContractWriteStateDB(t, []codexCellsContractRow{
		{ID: "sess-alpha", Title: "alpha", Model: "m", TokensUsed: 1, RolloutPath: "/rollouts/alpha.jsonl", CWD: "/a", UpdatedAt: 10},
		{ID: "sess-beta", Title: "beta", Model: "m", TokensUsed: 2, RolloutPath: "/rollouts/beta.jsonl", CWD: "/b", UpdatedAt: 20},
	})

	if got := codexRolloutPath("sess-alpha"); got != "/rollouts/alpha.jsonl" {
		t.Fatalf("codexRolloutPath(sess-alpha) = %q, want %q", got, "/rollouts/alpha.jsonl")
	}
	if got := codexRolloutPath("sess-beta"); got != "/rollouts/beta.jsonl" {
		t.Fatalf("codexRolloutPath(sess-beta) = %q, want %q", got, "/rollouts/beta.jsonl")
	}
}

// ---------------------------------------------------------------------------
// W1-B — parseCodexBlocks (read.go)
// Obligation: maps each rollout record type to a block, in order —
//   event_msg/user_message  -> {Kind:"user",     Text: payload.message}
//   event_msg/agent_message -> {Kind:"text",     Text: payload.message}
//   response_item/function_call -> {Kind:"tool", Text: <name+args summary>}
//   response_item/reasoning     -> {Kind:"thinking", Text: ...}
// Logic: the four valid records produce four blocks in input order, each with
// the mapped Kind; the two message shapes carry payload.message verbatim.
// ---------------------------------------------------------------------------

func TestCodexCellsContractParseBlocksMapsAndOrders(t *testing.T) {
	lines := []string{
		codexCellsContractUserRecord,
		codexCellsContractAgentRecord,
		codexCellsContractFuncCallRecord,
		codexCellsContractReasoningRecord,
	}
	blocks := parseCodexBlocks(lines)

	if len(blocks) != 4 {
		t.Fatalf("parseCodexBlocks produced %d blocks, want 4 (one per valid record): %+v", len(blocks), blocks)
	}

	// Order + Kind for every record; Text only where the contract fixes it.
	if blocks[0].Kind != "user" {
		t.Fatalf("block[0].Kind = %q, want %q (event_msg/user_message)", blocks[0].Kind, "user")
	}
	if blocks[0].Text != codexCellsContractUserText {
		t.Fatalf("block[0].Text = %q, want %q (payload.message verbatim)", blocks[0].Text, codexCellsContractUserText)
	}
	if blocks[1].Kind != "text" {
		t.Fatalf("block[1].Kind = %q, want %q (event_msg/agent_message)", blocks[1].Kind, "text")
	}
	if blocks[1].Text != codexCellsContractAgentText {
		t.Fatalf("block[1].Text = %q, want %q (payload.message verbatim)", blocks[1].Text, codexCellsContractAgentText)
	}
	if blocks[2].Kind != "tool" {
		t.Fatalf("block[2].Kind = %q, want %q (response_item/function_call)", blocks[2].Kind, "tool")
	}
	if blocks[3].Kind != "thinking" {
		t.Fatalf("block[3].Kind = %q, want %q (response_item/reasoning)", blocks[3].Kind, "thinking")
	}
}

// ---------------------------------------------------------------------------
// W1-B — LastBlocks dispatch (read.go)
// Obligation: if path contains "/.codex/sessions/", parse via parseCodexBlocks;
// else the existing claude path, byte-unchanged.
// ---------------------------------------------------------------------------

func TestCodexCellsContractLastBlocksDispatchesCodexPath(t *testing.T) {
	// A path CONTAINING "/.codex/sessions/" routes to the codex parser. The
	// bytes are codex-envelope records the claude parser would yield nothing
	// from (their top-level type is event_msg, not assistant/user), so a codex
	// user+text pair coming back proves the codex parser ran.
	dir := filepath.Join(t.TempDir(), ".codex", "sessions", "2026", "09", "10")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	path := filepath.Join(dir, "rollout-sess.jsonl")
	content := codexCellsContractUserRecord + "\n" + codexCellsContractAgentRecord + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write rollout: %v", err)
	}

	// n larger than the file's content: the whole tail is returned.
	blocks, _, err := LastBlocks(path, 50)
	if err != nil {
		t.Fatalf("LastBlocks(codex path) err = %v, want nil", err)
	}
	if len(blocks) != 2 {
		t.Fatalf("LastBlocks(codex path) returned %d blocks, want 2 (parsed via parseCodexBlocks): %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != "user" || blocks[0].Text != codexCellsContractUserText {
		t.Fatalf("block[0] = {%q,%q}, want {user,%q}", blocks[0].Kind, blocks[0].Text, codexCellsContractUserText)
	}
	if blocks[1].Kind != "text" || blocks[1].Text != codexCellsContractAgentText {
		t.Fatalf("block[1] = {%q,%q}, want {text,%q}", blocks[1].Kind, blocks[1].Text, codexCellsContractAgentText)
	}
}

func TestCodexCellsContractLastBlocksClaudePathUnaffected(t *testing.T) {
	// A path that does NOT contain "/.codex/sessions/" stays on the claude
	// parser. The bytes are a claude assistant record the codex parser would
	// skip (top-level type "assistant" is neither event_msg nor response_item),
	// so getting the claude text block back proves the claude path is unchanged.
	dir := filepath.Join(t.TempDir(), "projects", "some-cwd")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir projects dir: %v", err)
	}
	path := filepath.Join(dir, "s.jsonl")
	content := `{"type":"assistant","message":{"content":[{"type":"text","text":"claude-only text"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write claude transcript: %v", err)
	}

	blocks, _, err := LastBlocks(path, 50)
	if err != nil {
		t.Fatalf("LastBlocks(claude path) err = %v, want nil", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("LastBlocks(claude path) returned %d blocks, want 1 (claude parser): %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != "text" || blocks[0].Text != "claude-only text" {
		t.Fatalf("block[0] = {%q,%q}, want {text,%q}", blocks[0].Kind, blocks[0].Text, "claude-only text")
	}
}

// ---------------------------------------------------------------------------
// W1-B — ReadTranscriptPath codex arm (read.go)
// Obligation: for p.Agent == "codex" (after the shell check), return
// codexRolloutPath(p.SessionID) when non-empty.
// Logic: a codex peer whose SessionID resolves in the state db returns that
// row's rollout_path with no error.
// ---------------------------------------------------------------------------

func TestCodexCellsContractReadTranscriptPathCodexReturnsRollout(t *testing.T) {
	const rollout = "/Users/x/.codex/sessions/2026/09/10/rollout-sess-codex-1.jsonl"
	codexCellsContractWriteStateDB(t, []codexCellsContractRow{
		{ID: "sess-codex-1", Title: "cell zeus", Model: "gpt-5.6-terra", TokensUsed: 99, RolloutPath: rollout, CWD: "/work", UpdatedAt: 500},
	})

	p := &Peer{Agent: "codex", Name: "cx1", SessionID: "sess-codex-1"}
	got, err := ReadTranscriptPath(p)
	if err != nil {
		t.Fatalf("ReadTranscriptPath(codex peer) err = %v, want nil", err)
	}
	if got != rollout {
		t.Fatalf("ReadTranscriptPath(codex peer) = %q, want the resolved rollout_path %q", got, rollout)
	}
}
