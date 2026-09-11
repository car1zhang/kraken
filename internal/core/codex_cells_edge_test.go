package core

// Blind EDGE-dimension tests for WAVE 1 of the codex-cells build: codex
// session identity by cwd, rollout-path resolution, the rollout block reader,
// and the ReadTranscriptPath codex arm.
//
// Authored from artifacts/55-contract-cellsurface.md ("WAVE 1") ONLY. The
// implementation files internal/core/discover.go and internal/core/read.go
// were deliberately NOT read by any route (Read, grep, git show/diff, cat) so
// these assertions are derived from the frozen contract text alone. These
// cover boundaries, error paths, refusals, and degradation; happy-path mapping
// belongs to the other author.
//
// Every package-level identifier is prefixed `codexCellsEdge` to avoid
// colliding with the sibling codex test files in this package.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// codexCellsEdgeThread is one fixture row for the `threads` table, carrying the
// columns readCodexThreadByCWD selects (id,title,model,tokens_used,
// rollout_path,cwd) plus the updated_at the newest-wins ORDER BY reads.
type codexCellsEdgeThread struct {
	id          string
	title       string
	model       string
	tokensUsed  int64
	rolloutPath string
	cwd         string
	updatedAt   int64
}

// codexCellsEdgeWriteThreads builds <home>/.codex/state_5.sqlite with the
// contract's threads schema, inserts rows, and closes the db before returning
// so the immutable read sees a fully written file. The column order matches
// the contract note: threads(id,title,model,tokens_used,rollout_path,cwd,
// updated_at). updated_at is INTEGER so 100/200 order the same numerically and
// lexically (no affinity ambiguity).
func codexCellsEdgeWriteThreads(t *testing.T, home string, rows []codexCellsEdgeThread) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE threads(
		id TEXT PRIMARY KEY,
		title TEXT,
		model TEXT,
		tokens_used INTEGER,
		rollout_path TEXT,
		cwd TEXT,
		updated_at INTEGER
	)`); err != nil {
		db.Close()
		t.Fatalf("create threads table: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO threads(id,title,model,tokens_used,rollout_path,cwd,updated_at) VALUES(?,?,?,?,?,?,?)`,
			r.id, r.title, r.model, r.tokensUsed, r.rolloutPath, r.cwd, r.updatedAt,
		); err != nil {
			db.Close()
			t.Fatalf("insert row %q: %v", r.id, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
}

// codexCellsEdgeWriteCorrupt writes non-sqlite bytes to
// <home>/.codex/state_5.sqlite so any open/query fails.
func codexCellsEdgeWriteCorrupt(t *testing.T, home string) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state_5.sqlite"), []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}
}

// ---------------------------------------------------------------------------
// W1-A — readCodexThreadByCWD: guards, degradation, newest-wins tiebreak.
// ---------------------------------------------------------------------------

// Empty cwd must return (zero,false) WITHOUT querying. The fixture deliberately
// carries a row whose cwd is the empty string: had the function issued
// `WHERE cwd=”` it would match that row and return true, so a false answer is
// positive evidence the query was skipped for an empty cwd.
func TestCodexCellsEdgeReadThreadByCWDEmptyCWDSkipsQuery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexCellsEdgeWriteThreads(t, home, []codexCellsEdgeThread{
		{id: "id-empty", title: "Empty CWD Row", cwd: "", updatedAt: 100},
	})
	if _, ok := readCodexThreadByCWD(""); ok {
		t.Fatal("readCodexThreadByCWD(\"\") returned ok=true; empty cwd must return false without querying")
	}
}

// Missing db (HOME at an empty temp dir, no ~/.codex/state_5.sqlite) → false,
// never a panic or surfaced error.
func TestCodexCellsEdgeReadThreadByCWDMissingDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, ok := readCodexThreadByCWD("/some/worktree"); ok {
		t.Fatal("readCodexThreadByCWD returned ok=true for a missing db; want false")
	}
}

// Corrupt db (non-sqlite bytes at the path) → false.
func TestCodexCellsEdgeReadThreadByCWDCorruptDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexCellsEdgeWriteCorrupt(t, home)
	if _, ok := readCodexThreadByCWD("/some/worktree"); ok {
		t.Fatal("readCodexThreadByCWD returned ok=true for a corrupt db; want false")
	}
}

// A query error (valid sqlite, but no `threads` table) → false.
func TestCodexCellsEdgeReadThreadByCWDNoThreadsTable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE other(id TEXT)`); err != nil {
		db.Close()
		t.Fatalf("create other table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}
	if _, ok := readCodexThreadByCWD("/some/worktree"); ok {
		t.Fatal("readCodexThreadByCWD returned ok=true when the threads table is absent; want false")
	}
}

// A cwd present in no row → (zero,false).
func TestCodexCellsEdgeReadThreadByCWDNoMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexCellsEdgeWriteThreads(t, home, []codexCellsEdgeThread{
		{id: "id-a", title: "A", cwd: "/worktree/a", updatedAt: 100},
		{id: "id-b", title: "B", cwd: "/worktree/b", updatedAt: 200},
	})
	if _, ok := readCodexThreadByCWD("/worktree/c"); ok {
		t.Fatal("readCodexThreadByCWD returned ok=true for a cwd present in no row; want false")
	}
}

// Newest-wins tiebreak: two rows share one cwd with different updated_at; the
// row with the greater updated_at wins (ORDER BY updated_at DESC LIMIT 1). A
// third row on a different cwd must not leak into the answer.
func TestCodexCellsEdgeReadThreadByCWDNewestWins(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const shared = "/worktree/shared"
	codexCellsEdgeWriteThreads(t, home, []codexCellsEdgeThread{
		{id: "id-old", title: "Old", model: "gpt-old", rolloutPath: "/roll/old.jsonl", cwd: shared, updatedAt: 100},
		{id: "id-new", title: "New", model: "gpt-new", rolloutPath: "/roll/new.jsonl", cwd: shared, updatedAt: 200},
		{id: "id-other", title: "Other", cwd: "/worktree/other", updatedAt: 999},
	})
	ct, ok := readCodexThreadByCWD(shared)
	if !ok {
		t.Fatal("readCodexThreadByCWD returned ok=false for a matching cwd; want true")
	}
	if ct.ID != "id-new" {
		t.Fatalf("ID = %q, want id-new (newest updated_at must win the tiebreak)", ct.ID)
	}
	// The winning row's fields must come from the newest row, not a mix.
	if ct.CWD != shared {
		t.Errorf("CWD = %q, want %q", ct.CWD, shared)
	}
}

// ---------------------------------------------------------------------------
// W1-A — codexRolloutPath: empty id, no match, corrupt/missing db.
// ---------------------------------------------------------------------------

// Empty session id → "" (never querying, but the observable contract is "").
func TestCodexCellsEdgeRolloutPathEmptyID(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexCellsEdgeWriteThreads(t, home, []codexCellsEdgeThread{
		{id: "id-x", title: "X", rolloutPath: "/roll/x.jsonl", cwd: "/w/x", updatedAt: 100},
	})
	if got := codexRolloutPath(""); got != "" {
		t.Fatalf("codexRolloutPath(\"\") = %q, want empty", got)
	}
}

// An id present in no row → "".
func TestCodexCellsEdgeRolloutPathNoMatch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexCellsEdgeWriteThreads(t, home, []codexCellsEdgeThread{
		{id: "id-x", title: "X", rolloutPath: "/roll/x.jsonl", cwd: "/w/x", updatedAt: 100},
	})
	if got := codexRolloutPath("id-absent"); got != "" {
		t.Fatalf("codexRolloutPath(id-absent) = %q, want empty", got)
	}
}

// Corrupt db → "".
func TestCodexCellsEdgeRolloutPathCorruptDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexCellsEdgeWriteCorrupt(t, home)
	if got := codexRolloutPath("id-x"); got != "" {
		t.Fatalf("codexRolloutPath on a corrupt db = %q, want empty", got)
	}
}

// Missing db → "".
func TestCodexCellsEdgeRolloutPathMissingDB(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := codexRolloutPath("id-x"); got != "" {
		t.Fatalf("codexRolloutPath with no db = %q, want empty", got)
	}
}

// ---------------------------------------------------------------------------
// W1-B — parseCodexBlocks: unknown types, malformed lines, empty input.
// ---------------------------------------------------------------------------

// An unknown top-level record type yields no block.
func TestCodexCellsEdgeParseBlocksUnknownTypeSkipped(t *testing.T) {
	lines := []string{
		`{"timestamp":"t1","type":"session_meta","payload":{"foo":"bar"}}`,
		`{"timestamp":"t2","type":"turn_context","payload":{"cwd":"/w"}}`,
	}
	if blocks := parseCodexBlocks(lines); len(blocks) != 0 {
		t.Fatalf("want 0 blocks for unknown record types, got %d: %+v", len(blocks), blocks)
	}
}

// Malformed / non-JSON lines are skipped silently.
func TestCodexCellsEdgeParseBlocksMalformedSkipped(t *testing.T) {
	lines := []string{
		`not json at all`,
		``,
		`{"timestamp":"t1","type":"event_msg","payload":`, // truncated JSON
	}
	if blocks := parseCodexBlocks(lines); len(blocks) != 0 {
		t.Fatalf("want 0 blocks for malformed lines, got %d: %+v", len(blocks), blocks)
	}
}

// Empty input (nil and an empty slice) yields no blocks.
func TestCodexCellsEdgeParseBlocksEmpty(t *testing.T) {
	if blocks := parseCodexBlocks(nil); len(blocks) != 0 {
		t.Fatalf("want 0 blocks for nil lines, got %d: %+v", len(blocks), blocks)
	}
	if blocks := parseCodexBlocks([]string{}); len(blocks) != 0 {
		t.Fatalf("want 0 blocks for empty slice, got %d: %+v", len(blocks), blocks)
	}
}

// Interleaving unknown/malformed lines among valid records must skip only the
// bad ones and keep the valid blocks in order — the "skipped silently"
// obligation, verified against surviving order rather than mapping detail.
func TestCodexCellsEdgeParseBlocksSkipsAmidValid(t *testing.T) {
	lines := []string{
		`{"timestamp":"t1","type":"event_msg","payload":{"type":"user_message","message":"hello"}}`,
		`{"timestamp":"t2","type":"session_meta","payload":{"foo":"bar"}}`, // unknown → skipped
		`garbage line`, // malformed → skipped
		`{"timestamp":"t3","type":"event_msg","payload":{"type":"agent_message","message":"world"}}`,
	}
	blocks := parseCodexBlocks(lines)
	if len(blocks) != 2 {
		t.Fatalf("want 2 surviving blocks, got %d: %+v", len(blocks), blocks)
	}
	if blocks[0].Kind != "user" {
		t.Errorf("block[0].Kind = %q, want user", blocks[0].Kind)
	}
	if blocks[1].Kind != "text" {
		t.Errorf("block[1].Kind = %q, want text", blocks[1].Kind)
	}
}

// ---------------------------------------------------------------------------
// W1-B — LastBlocks dispatch: a claude path (no /.codex/sessions/) uses the
// existing parser byte-unchanged.
// ---------------------------------------------------------------------------

func TestCodexCellsEdgeLastBlocksClaudePathUsesExistingParser(t *testing.T) {
	// A temp path never contains "/.codex/sessions/", so dispatch must fall to
	// the existing claude parser. A codex-shaped record would yield nothing
	// there; a claude user record yields exactly one user block.
	path := writeTranscript(t, `{"type":"user","message":{"content":"hi from claude"}}`)
	if strings.Contains(path, "/.codex/sessions/") {
		t.Fatalf("fixture path unexpectedly contains the codex marker: %q", path)
	}
	blocks, counted, err := LastBlocks(path, 10)
	if err != nil {
		t.Fatalf("LastBlocks on a claude path errored: %v", err)
	}
	if counted != 1 || len(blocks) != 1 {
		t.Fatalf("counted=%d len=%d, want 1/1 from the existing claude parser: %+v", counted, len(blocks), blocks)
	}
	if blocks[0].Kind != "user" || blocks[0].Text != "hi from claude" {
		t.Fatalf("claude block = %+v, want {user, \"hi from claude\"}", blocks[0])
	}
}

// ---------------------------------------------------------------------------
// W1-B — ReadTranscriptPath codex arm + preserved shell refusal.
// ---------------------------------------------------------------------------

// A codex peer whose session id resolves in the fixture db → the rollout path,
// no error.
func TestCodexCellsEdgeReadTranscriptPathCodexResolves(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	const roll = "/somewhere/.codex/sessions/rollout-abc.jsonl"
	codexCellsEdgeWriteThreads(t, home, []codexCellsEdgeThread{
		{id: "sess-real", title: "cx thread", model: "gpt-5.6", rolloutPath: roll, cwd: "/w/cx", updatedAt: 100},
	})
	got, err := ReadTranscriptPath(&Peer{Agent: "codex", Name: "cx", SessionID: "sess-real"})
	if err != nil {
		t.Fatalf("ReadTranscriptPath for a resolvable codex peer errored: %v", err)
	}
	if got != roll {
		t.Fatalf("ReadTranscriptPath = %q, want the resolved rollout path %q", got, roll)
	}
}

// A codex peer whose session id does NOT resolve → an error naming the peer
// ("no rollout resolved for <name>"), and specifically NOT the old
// "not supported" refusal.
func TestCodexCellsEdgeReadTranscriptPathCodexUnresolvedErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Fixture db present but without the peer's id, so codexRolloutPath misses.
	codexCellsEdgeWriteThreads(t, home, []codexCellsEdgeThread{
		{id: "some-other-id", title: "unrelated", rolloutPath: "/roll/other.jsonl", cwd: "/w/o", updatedAt: 100},
	})
	_, err := ReadTranscriptPath(&Peer{Agent: "codex", Name: "cx", SessionID: "no-such-id"})
	if err == nil {
		t.Fatal("ReadTranscriptPath for an unresolvable codex peer returned nil error; want an error")
	}
	if !strings.Contains(err.Error(), "no rollout resolved") {
		t.Fatalf("error = %q, want it to contain %q", err.Error(), "no rollout resolved")
	}
	if !strings.Contains(err.Error(), "cx") {
		t.Fatalf("error = %q, want it to name the peer (cx)", err.Error())
	}
	if strings.Contains(strings.ToLower(err.Error()), "not supported") {
		t.Fatalf("error = %q, must not be the old \"not supported\" refusal", err.Error())
	}
}

// A shell peer is still refused (error non-nil) — unchanged for non-codex.
func TestCodexCellsEdgeReadTranscriptPathShellRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := ReadTranscriptPath(&Peer{Role: "shell", Name: "sh"})
	if err == nil {
		t.Fatal("ReadTranscriptPath for a shell peer returned nil error; a shell peer must still be refused")
	}
}
