package core

// codex_contract_test.go — BLIND logic tests authored from
// artifacts/50-contract.md (frozen 2026-09-10). Covers the stated
// invariants and happy paths of P1 (launcher + version floor), P2
// (thread-name reader), P3 (codex status arm) and P5 (id containment).
// Edge/boundary/error cases are authored separately; this file avoids them.
// Authored blind: no implementation was read. AUTHOR-ONLY — not run here.

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// ---------------------------------------------------------------------------
// P1 — codexMinVersion + codexVersionAtLeast (branch.go)
// Obligation: floor const == "0.100.0"; parse "codex-cli X.Y.Z" (leading/
// trailing whitespace + extra trailing text tolerated) and compare X.Y.Z to
// the floor by NUMERIC per-component semver — 0.39.0 must read as LESS than
// 0.100.0 (a string compare would say the opposite).
// ---------------------------------------------------------------------------

func TestCodexContractMinVersionConst(t *testing.T) {
	if codexMinVersion != "0.100.0" {
		t.Fatalf("codexMinVersion = %q, want %q (the floor between the two installed generations)", codexMinVersion, "0.100.0")
	}
}

func TestCodexContractVersionAtLeastTruthTable(t *testing.T) {
	cases := []struct {
		name  string
		out   string
		floor string
		want  bool
	}{
		{"current build above floor", "codex-cli 0.144.6", codexMinVersion, true},
		{"stale build below floor is numeric not string", "codex-cli 0.39.0", codexMinVersion, false},
		{"exactly equal to floor is at least", "codex-cli 0.100.0", codexMinVersion, true},
		{"numeric compare in the other direction", "codex-cli 0.100.0", "0.40.0", true},
		{"leading and trailing whitespace tolerated", "   codex-cli 0.144.6   ", codexMinVersion, true},
		{"extra trailing text after the triple tolerated", "codex-cli 0.144.6 (rust build 2026)", codexMinVersion, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codexVersionAtLeast(c.out, c.floor); got != c.want {
				t.Fatalf("codexVersionAtLeast(%q, %q) = %v, want %v", c.out, c.floor, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// P2 — readCodexThreadNames (discover.go)
// Obligation: open ~/.codex/state_5.sqlite read-only, SELECT id,title FROM
// threads, return id->title for every row with a non-NULL, non-empty title.
// Keys on id (proven with a row whose title equals another row's id); a row
// with an empty title is omitted. Returns a non-nil map.
// ---------------------------------------------------------------------------

// codexContractMkStateDB sets HOME to a fresh temp dir and writes a
// state_5.sqlite there with a threads table populated from rows ([id,title]).
// The fixture db is closed before returning so an immutable read sees it.
func codexContractMkStateDB(t *testing.T, rows [][2]string) {
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
	if _, err := db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY, title TEXT, cwd TEXT, model TEXT, tokens_used INTEGER, thread_source TEXT, rollout_path TEXT)`); err != nil {
		db.Close()
		t.Fatalf("create threads: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO threads(id, title) VALUES(?, ?)`, r[0], r[1]); err != nil {
			db.Close()
			t.Fatalf("insert row %v: %v", r, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
}

func TestCodexContractReadThreadNamesMapsIDToTitle(t *testing.T) {
	codexContractMkStateDB(t, [][2]string{
		{"id-alpha", "Alpha thread"},
		{"id-beta", "Beta thread"},
		// title equal to another row's id proves the map keys on id, not title.
		{"id-collide", "id-alpha"},
		// empty title is omitted.
		{"id-empty", ""},
	})

	m := readCodexThreadNames()
	if m == nil {
		t.Fatalf("readCodexThreadNames() returned a nil map, want non-nil")
	}
	if len(m) != 3 {
		t.Fatalf("readCodexThreadNames() has %d entries %v, want 3 (empty-title row omitted)", len(m), m)
	}
	if got := m["id-alpha"]; got != "Alpha thread" {
		t.Fatalf("m[id-alpha] = %q, want %q", got, "Alpha thread")
	}
	if got := m["id-beta"]; got != "Beta thread" {
		t.Fatalf("m[id-beta] = %q, want %q", got, "Beta thread")
	}
	if got := m["id-collide"]; got != "id-alpha" {
		t.Fatalf("m[id-collide] = %q, want %q (keyed on id, title happens to equal another id)", got, "id-alpha")
	}
	if _, ok := m["id-empty"]; ok {
		t.Fatalf("m[id-empty] present, want the empty-title row omitted")
	}
}

// ---------------------------------------------------------------------------
// P5 — claudeModelFallbackApplies (discover.go)
// Obligation: returns
//   p.Model == "" && p.SessionID != "" && !p.SessionIDGuessed && p.Agent != "codex"
// A codex peer returns false (its rollout uuid must never reach the claude
// projects glob); a claude peer with an empty model and a real, non-guessed
// session id returns true.
// ---------------------------------------------------------------------------

func TestCodexContractModelFallbackAppliesTruthTable(t *testing.T) {
	cases := []struct {
		name string
		peer Peer
		want bool
	}{
		{
			name: "claude peer empty model real id applies",
			peer: Peer{Model: "", SessionID: "real-uuid", SessionIDGuessed: false, Agent: "claude"},
			want: true,
		},
		{
			name: "claude peer with empty agent string also applies",
			peer: Peer{Model: "", SessionID: "real-uuid", SessionIDGuessed: false, Agent: ""},
			want: true,
		},
		{
			name: "codex peer never applies (containment)",
			peer: Peer{Model: "", SessionID: "rollout-uuid", SessionIDGuessed: false, Agent: "codex"},
			want: false,
		},
		{
			name: "model already set does not apply",
			peer: Peer{Model: "claude-opus-5", SessionID: "real-uuid", SessionIDGuessed: false, Agent: "claude"},
			want: false,
		},
		{
			name: "empty session id does not apply",
			peer: Peer{Model: "", SessionID: "", SessionIDGuessed: false, Agent: "claude"},
			want: false,
		},
		{
			name: "guessed session id does not apply",
			peer: Peer{Model: "", SessionID: "guessed-uuid", SessionIDGuessed: true, Agent: "claude"},
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := c.peer
			if got := claudeModelFallbackApplies(&p); got != c.want {
				t.Fatalf("claudeModelFallbackApplies(%+v) = %v, want %v", p, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// P3 — codexBusyRe (peer.go)
// Obligation: matches the measured busy line — "Working (" immediately
// followed by one or more digits — regardless of the leading glyph/indent.
// Measured on codex 0.144.6 (2026-09-10): "<glyph> Working (<N>s • esc to
// interrupt)" with <glyph> in {•, ◦}.
// ---------------------------------------------------------------------------

func TestCodexContractBusyReMatchesMeasuredLine(t *testing.T) {
	lines := []string{
		"• Working (5s • esc to interrupt)",
		"◦ Working (2s • esc to interrupt)",
	}
	for _, ln := range lines {
		if !codexBusyRe.MatchString(ln) {
			t.Fatalf("codexBusyRe did not match the measured busy line %q", ln)
		}
	}
}

// ---------------------------------------------------------------------------
// P3 — codexPaneShowsQuestion (peer.go), true cases
// Obligation: true when the last ~20 lines contain either a numbered option
// line matching ^\s*[›❯]\s*\d+\.\s (the trust prompt's "› 1. Yes, continue")
// OR a header needle "Allow command?" / "Apply changes?".
// ---------------------------------------------------------------------------

func TestCodexContractPaneShowsQuestionTrueCases(t *testing.T) {
	cases := []struct {
		name string
		pane string
	}{
		{
			name: "trust prompt numbered option with angle glyph",
			pane: "Allow Codex to run this command?\n\n› 1. Yes, continue\n  2. No, quit\n",
		},
		{
			name: "numbered option with heavy angle glyph",
			pane: "Choose an option:\n❯ 1. Yes, continue\n  2. No, quit\n",
		},
		{
			name: "allow command header needle",
			pane: "running: rm -rf build\nAllow command?\n",
		},
		{
			name: "apply changes header needle",
			pane: "proposed diff to main.go\nApply changes?\n",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !codexPaneShowsQuestion(c.pane) {
				t.Fatalf("codexPaneShowsQuestion(%q) = false, want true", c.pane)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// P3 — refinedCodexStatus (peer.go)
// Obligation: pure classifier — "question" if codexPaneShowsQuestion; else
// "running" if codexBusyRe matches; else "waiting". current is unused (a
// codex peer always arrives "unknown").
// ---------------------------------------------------------------------------

func TestCodexContractRefinedCodexStatusRunning(t *testing.T) {
	for _, pane := range []string{
		"Editing internal/core/peer.go\n• Working (5s • esc to interrupt)\n",
		"planning...\n◦ Working (2s • esc to interrupt)\n",
	} {
		if got := refinedCodexStatus("unknown", pane); got != "running" {
			t.Fatalf("refinedCodexStatus(unknown, %q) = %q, want %q", pane, got, "running")
		}
	}
}

func TestCodexContractRefinedCodexStatusQuestion(t *testing.T) {
	panes := []string{
		"Allow Codex to run this command?\n\n› 1. Yes, continue\n  2. No, quit\n",
		"running: rm -rf build\nAllow command?\n",
		"proposed diff\nApply changes?\n",
	}
	for _, pane := range panes {
		if got := refinedCodexStatus("unknown", pane); got != "question" {
			t.Fatalf("refinedCodexStatus(unknown, %q) = %q, want %q", pane, got, "question")
		}
	}
}

func TestCodexContractRefinedCodexStatusWaiting(t *testing.T) {
	// An idle pane: footer prompt with no numbered option, no busy needle, no
	// approval header. "› Implement the feature" starts with › but is followed
	// by a letter, not a digit, so it is not a numbered option line.
	pane := "done.\n\n› Implement the feature\n  gpt-5.6-terra   default · /Users/x/proj\n"
	if got := refinedCodexStatus("unknown", pane); got != "waiting" {
		t.Fatalf("refinedCodexStatus(unknown, %q) = %q, want %q", pane, got, "waiting")
	}
}

func TestCodexContractRefinedCodexStatusQuestionBeatsRunning(t *testing.T) {
	// When a busy line and a question prompt are both present, question wins
	// (it is checked first in the classifier order).
	pane := "• Working (3s • esc to interrupt)\nAllow Codex to run this command?\n› 1. Yes, continue\n  2. No, quit\n"
	if got := refinedCodexStatus("unknown", pane); got != "question" {
		t.Fatalf("refinedCodexStatus(unknown, %q) = %q, want %q (question precedes running)", pane, got, "question")
	}
}
