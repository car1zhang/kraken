package core

// Blind EDGE-dimension tests for the codex adapter layer (P1/P2/P3/P5).
// Authored from artifacts/50-contract.md ONLY; the implementation does not
// exist yet and was never read. These cover boundaries, error paths,
// refusals, and degradation. Happy paths belong to the other author.
//
// All package-level identifiers are prefixed `codexEdge` to avoid colliding
// with the logic author's file in the same package.

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// ---------------------------------------------------------------------------
// P1 — codexVersionAtLeast: parse tolerance, fail-closed, numeric semver.
// ---------------------------------------------------------------------------

func TestCodexEdgeVersionAtLeast(t *testing.T) {
	cases := []struct {
		name   string
		output string
		floor  string
		want   bool
	}{
		// Unparseable / empty input is fail-closed → false.
		{"empty input", "", "0.100.0", false},
		{"no version triple", "codex-cli", "0.100.0", false},
		{"no digits at all", "not a version at all", "0.100.0", false},

		// Equal-version boundary: >= is inclusive.
		{"equal boundary", "codex-cli 0.100.0", "0.100.0", true},

		// Current generation is above floor.
		{"current above floor", "codex-cli 0.144.6", "0.100.0", true},

		// The load-bearing numeric-not-string check: string compare would put
		// "0.39.0" >= "0.100.0" (because '3' > '1'), numeric compare must not.
		{"stale below floor (numeric not string)", "codex-cli 0.39.0", "0.100.0", false},
		// And the mirror: numeric compare must put 0.100.0 >= 0.39.0, where a
		// string compare ('1' < '3') would wrongly say false.
		{"minor 100 ge minor 39 (numeric not string)", "codex-cli 0.100.0", "0.39.0", true},

		// Extra trailing text after the version is tolerated.
		{"trailing build text tolerated", "codex-cli 0.144.6 (some build info)", "0.100.0", true},
		// Leading/trailing whitespace tolerated.
		{"surrounding whitespace tolerated", "  codex-cli 0.144.6\n", "0.100.0", true},

		// Per-component boundaries just under / just over the floor.
		{"minor just below floor", "codex-cli 0.99.0", "0.100.0", false},
		{"patch above equal minor", "codex-cli 0.100.1", "0.100.0", true},
		{"patch below equal minor", "codex-cli 0.100.0", "0.100.1", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := codexVersionAtLeast(c.output, c.floor); got != c.want {
				t.Fatalf("codexVersionAtLeast(%q, %q) = %v, want %v", c.output, c.floor, got, c.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// P2 — readCodexThreadNames: defensive reads, id-keying, title omission.
// ---------------------------------------------------------------------------

type codexEdgeRow struct {
	id    string
	title interface{} // string, or nil for a NULL title
}

// codexEdgeWriteDB builds <home>/.codex/state_5.sqlite with the threads schema
// and closes it before returning, so readCodexThreadNames' immutable read sees
// a fully written main db file.
func codexEdgeWriteDB(t *testing.T, home string, rows []codexEdgeRow) {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	db, err := sql.Open("sqlite3", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatalf("open fixture db: %v", err)
	}
	// Extra columns match production and are fine per the contract.
	if _, err := db.Exec(`CREATE TABLE threads(id TEXT PRIMARY KEY, title TEXT, cwd TEXT, model TEXT, tokens_used INTEGER, thread_source TEXT, rollout_path TEXT)`); err != nil {
		db.Close()
		t.Fatalf("create threads table: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO threads(id, title) VALUES(?, ?)`, r.id, r.title); err != nil {
			db.Close()
			t.Fatalf("insert row %q: %v", r.id, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture db: %v", err)
	}
}

func TestCodexEdgeThreadNamesMissingDB(t *testing.T) {
	// HOME points at an empty temp dir: no ~/.codex/state_5.sqlite at all.
	t.Setenv("HOME", t.TempDir())
	m := readCodexThreadNames()
	if m == nil {
		t.Fatal("readCodexThreadNames returned nil for a missing db; want empty non-nil map")
	}
	if len(m) != 0 {
		t.Fatalf("want empty map for missing db, got %d entries: %v", len(m), m)
	}
}

func TestCodexEdgeThreadNamesCorruptDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .codex: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state_5.sqlite"), []byte("this is not a sqlite database at all"), 0o644); err != nil {
		t.Fatalf("write corrupt db: %v", err)
	}
	m := readCodexThreadNames()
	if m == nil {
		t.Fatal("readCodexThreadNames returned nil for a corrupt db; want empty non-nil map")
	}
	if len(m) != 0 {
		t.Fatalf("want empty map for corrupt db, got %d entries: %v", len(m), m)
	}
}

func TestCodexEdgeThreadNamesQueryErrorNoThreadsTable(t *testing.T) {
	// A valid sqlite db with no `threads` table: the SELECT errors → empty map.
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
	m := readCodexThreadNames()
	if m == nil {
		t.Fatal("readCodexThreadNames returned nil on a query error; want empty non-nil map")
	}
	if len(m) != 0 {
		t.Fatalf("want empty map when threads table is absent, got %d entries: %v", len(m), m)
	}
}

func TestCodexEdgeThreadNamesCollisionAndTitleOmission(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexEdgeWriteDB(t, home, []codexEdgeRow{
		{id: "id-alpha", title: "Alpha Title"},
		// COLLISION fixture: this row's title equals another row's id. If the
		// map keyed on title instead of id, "id-alpha" as a key would be
		// ambiguous; keying on id keeps both rows distinct.
		{id: "id-beta", title: "id-alpha"},
		// Empty title is omitted.
		{id: "id-gamma", title: ""},
		// NULL title is omitted.
		{id: "id-delta", title: nil},
	})
	m := readCodexThreadNames()
	if got := m["id-alpha"]; got != "Alpha Title" {
		t.Fatalf("m[id-alpha] = %q, want %q", got, "Alpha Title")
	}
	if got := m["id-beta"]; got != "id-alpha" {
		t.Fatalf("m[id-beta] = %q, want %q — map must key on id, not title", got, "id-alpha")
	}
	if got, ok := m["id-gamma"]; ok {
		t.Fatalf("empty-title row must be omitted, got m[id-gamma] = %q", got)
	}
	if got, ok := m["id-delta"]; ok {
		t.Fatalf("NULL-title row must be omitted, got m[id-delta] = %q", got)
	}
	if len(m) != 2 {
		t.Fatalf("want exactly 2 entries (alpha, beta), got %d: %v", len(m), m)
	}
}

// ---------------------------------------------------------------------------
// P5 — claudeModelFallbackApplies: every false case (containment + guards).
// The true case (empty model, real non-guessed id, claude agent) is the other
// author's happy path and is not duplicated here.
// ---------------------------------------------------------------------------

func TestCodexEdgeModelFallbackFalseCases(t *testing.T) {
	cases := []struct {
		name string
		peer *Peer
	}{
		{
			// Containment: a codex peer's rollout uuid must never reach the
			// ~/.claude/projects glob, even when every other condition holds.
			name: "codex agent contained",
			peer: &Peer{Model: "", SessionID: "sess-real", SessionIDGuessed: false, Agent: "codex"},
		},
		{
			// A guessed session id is unsafe as a lookup target → false.
			name: "guessed session id",
			peer: &Peer{Model: "", SessionID: "sess-guessed", SessionIDGuessed: true, Agent: "claude"},
		},
		{
			// A populated model means no fallback is needed → false.
			name: "populated model",
			peer: &Peer{Model: "claude-opus-5", SessionID: "sess-real", SessionIDGuessed: false, Agent: "claude"},
		},
		{
			// No session id → nothing to feed the fallback → false.
			name: "empty session id",
			peer: &Peer{Model: "", SessionID: "", SessionIDGuessed: false, Agent: "claude"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if claudeModelFallbackApplies(c.peer) {
				t.Fatalf("claudeModelFallbackApplies(%+v) = true, want false", c.peer)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// P3 — codexBusyRe / codexPaneShowsQuestion / refinedCodexStatus edges.
// Pane fixtures measured against codex 0.144.6 per the contract's provenance.
// ---------------------------------------------------------------------------

// The busy line must require digits immediately after "Working (". Ordinary
// prose that merely contains "working" (or "Working" without the "(<digit>"
// shape) must not read as running.
func TestCodexEdgeBusyRegexIgnoresProseWorking(t *testing.T) {
	pane := "I am working on the refactor now.\nStill working through the edge cases.\ndone for now\n"
	if codexBusyRe.MatchString(pane) {
		t.Fatal("codexBusyRe matched lowercase prose 'working' without the 'Working (<digit>' shape")
	}
	if got := refinedCodexStatus("unknown", pane); got != "waiting" {
		t.Fatalf("refinedCodexStatus = %q, want waiting for prose 'working'", got)
	}
}

func TestCodexEdgeBusyRegexRequiresDigitAfterParen(t *testing.T) {
	// "Working (" present but not immediately followed by a digit: not busy.
	noDigit := "• Working (esc to interrupt)\n"
	if codexBusyRe.MatchString(noDigit) {
		t.Fatal("codexBusyRe matched 'Working (' not followed by a digit")
	}
	if got := refinedCodexStatus("unknown", noDigit); got != "waiting" {
		t.Fatalf("refinedCodexStatus = %q, want waiting when no digit follows 'Working ('", got)
	}
	// "Working" with no paren at all: not busy.
	noParen := "Working on it now\n"
	if codexBusyRe.MatchString(noParen) {
		t.Fatal("codexBusyRe matched 'Working' with no parenthesized seconds")
	}
	if got := refinedCodexStatus("unknown", noParen); got != "waiting" {
		t.Fatalf("refinedCodexStatus = %q, want waiting for 'Working on it now'", got)
	}
}

// Bottom-anchoring: a question needle that appears only in scrollback (well
// beyond the last ~20 lines) with an idle bottom must read as waiting.
func TestCodexEdgeQuestionNeedleInScrollbackIsWaiting(t *testing.T) {
	var b strings.Builder
	b.WriteString("The agent asked: Allow command? earlier in this session.\n")
	b.WriteString("It was answered and work continued.\n")
	for i := 0; i < 32; i++ {
		b.WriteString("normal output line\n")
	}
	// Idle footer: the measured idle prompt shape; '›' precedes prose, not a
	// digit, so it must not match the numbered-option regex.
	b.WriteString("gpt-5.6-terra default · /path/to/worktree\n")
	b.WriteString("› Implement {feature}\n")
	pane := b.String()

	if codexPaneShowsQuestion(pane) {
		t.Fatal("codexPaneShowsQuestion true for a needle only in scrollback; must bottom-anchor")
	}
	if got := refinedCodexStatus("unknown", pane); got != "waiting" {
		t.Fatalf("refinedCodexStatus = %q, want waiting (scrollback needle must not count)", got)
	}
}

// The trust-prompt block (a selected numbered option) at the bottom → question.
func TestCodexEdgeTrustPromptBlockIsQuestion(t *testing.T) {
	pane := "codex wants to run a command\n\n› 1. Yes, continue\n  2. No, quit\n"
	if !codexPaneShowsQuestion(pane) {
		t.Fatal("trust-prompt selected numbered option must read as a question")
	}
	if got := refinedCodexStatus("unknown", pane); got != "question" {
		t.Fatalf("refinedCodexStatus = %q, want question for the trust prompt block", got)
	}
}

// The alternate selection glyph '❯' on the selected option must also match.
func TestCodexEdgeSelectedOptionAltGlyphIsQuestion(t *testing.T) {
	pane := "choose an option:\n  1. Yes, continue\n❯ 2. No, quit\n"
	if !codexPaneShowsQuestion(pane) {
		t.Fatal("a selected option led by '❯' must read as a question")
	}
	if got := refinedCodexStatus("unknown", pane); got != "question" {
		t.Fatalf("refinedCodexStatus = %q, want question", got)
	}
}

// Numbered lines without a selection glyph and no header needle: the frozen
// regex requires a leading [›❯], so a plain numbered list is not a question.
func TestCodexEdgeNumberedListWithoutGlyphNotQuestion(t *testing.T) {
	pane := "here are the steps:\n1. do this\n2. then that\n3. finally this\n"
	if codexPaneShowsQuestion(pane) {
		t.Fatal("a numbered list with no selection glyph must not read as a question")
	}
	if got := refinedCodexStatus("unknown", pane); got != "waiting" {
		t.Fatalf("refinedCodexStatus = %q, want waiting", got)
	}
}

// "Allow command?" as the bottom line → question.
func TestCodexEdgeAllowCommandBottomIsQuestion(t *testing.T) {
	pane := "running build step\nreached a command boundary\nAllow command?\n"
	if !codexPaneShowsQuestion(pane) {
		t.Fatal("'Allow command?' at the bottom must read as a question")
	}
	if got := refinedCodexStatus("unknown", pane); got != "question" {
		t.Fatalf("refinedCodexStatus = %q, want question for 'Allow command?' at bottom", got)
	}
}

// Precedence: question is checked before running. A pane showing both the busy
// line and an approval prompt at the bottom classifies as question.
func TestCodexEdgeQuestionBeatsBusy(t *testing.T) {
	pane := "• Working (5s • esc to interrupt)\nAllow command?\n"
	if got := refinedCodexStatus("unknown", pane); got != "question" {
		t.Fatalf("refinedCodexStatus = %q, want question (question precedes running)", got)
	}
}

// Empty pane → waiting; and `current` is documented as unused, so an idle pane
// must not inherit a non-"unknown" current value.
func TestCodexEdgeEmptyPaneWaitingAndCurrentIgnored(t *testing.T) {
	if got := refinedCodexStatus("unknown", ""); got != "waiting" {
		t.Fatalf("refinedCodexStatus(unknown, empty) = %q, want waiting", got)
	}
	idle := "gpt-5.6-terra default · /path/to/worktree\n› Implement {feature}\n"
	if got := refinedCodexStatus("running", idle); got != "waiting" {
		t.Fatalf("refinedCodexStatus(running, idle) = %q, want waiting — current must be ignored", got)
	}
}
