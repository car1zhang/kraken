package jobs

// Regression pins for the defects the PR 2 review rounds found after the blind
// suites were authored (50-contract.md amendments 7, 9, 11, 12 and 13).
// Written by the lead, sighted; each pins the obligation, not the
// implementation line.

import (
	"errors"
	"strings"
	"testing"

	"kraken/internal/store"
)

// Amendment 7: a mind thread may revise only a captured item. A legacy mark's
// content is what the verify pass checks against its artifact or anchor, so a
// revise would have the next pass invalidate it with a false reason.
func TestReviseMMItemRefusesLegacyMark(t *testing.T) {
	st := contractMLStore(t)
	contractMLRepo(t, "kraken")
	contractMLJobWithThread(t, st, "revise-legacy", "zeus-rl", "mind-rl", "mind", "live")
	id, err := st.AddMMItem(store.MMItem{Kind: "note", Source: "manual", Content: "a legacy note", CreatedAt: 1})
	if err != nil {
		t.Fatalf("AddMMItem: %v", err)
	}
	c := contractMLCommit()
	err = ReviseMMItem(st, id, c, "mind-rl")
	if !errors.Is(err, ErrReviseLegacy) {
		t.Fatalf("revise of a manual item: err = %v, want ErrReviseLegacy", err)
	}
	if !strings.Contains(err.Error(), "kraken mm commit") {
		t.Errorf("the refusal must name `kraken mm commit` as the move: %q", err)
	}
	it, _ := st.GetMMItem(id)
	if it.Content != "a legacy note" || it.Title != "" {
		t.Errorf("a refused revise must leave the row alone: %+v", it)
	}
}

// Amendment 11: the mind brief's one flag write is `mm flag --item`; raise and
// dismiss stay in its forbidden list.
func TestMindThreadBriefAllowsOnlyTheItemFlag(t *testing.T) {
	contractMLEnv(t)
	mind := threadBrief(&Job{Slug: "brief-flag"}, "t1", "a title", "/tmp/wt", "mind")
	if !strings.Contains(mind, "kraken mm flag --item") {
		t.Fatalf("the mind brief must name `kraken mm flag --item` as its one flag write:\n%s", mind)
	}
	// the forbidden list names both steps: "`mm flag raise`/`dismiss`"
	for _, banned := range []string{"mm flag raise", "dismiss"} {
		if !strings.Contains(mind, banned) {
			t.Errorf("the mind brief must forbid %q by name:\n%s", banned, mind)
		}
	}
}

// Amendment 9: `index` and `root` are reserved by the files written beside the
// per-dir ones, so those two dir slugs take a `-dir` suffix.
func TestMMDirFileNameReservesIndexAndRoot(t *testing.T) {
	for dir, want := range map[string]string{
		"index":          "index-dir.md",
		"root":           "root-dir.md",
		"internal/jobs":  "internal-jobs.md",
		"index/sub":      "index-sub.md",
		"docs":           "docs.md",
		"internal/index": "internal-index.md",
	} {
		if got := mmDirFileName(dir); got != want {
			t.Errorf("mmDirFileName(%q) = %q, want %q", dir, got, want)
		}
	}
}

// Amendment 12: both HTML comment delimiters are escaped in a rendered title —
// an unescaped `<!--` would hide every later line of the rules file.
func TestMMRenderTitleEscapesCommentOpener(t *testing.T) {
	got := mmRenderTitle(store.MMItem{Title: "<!-- --> blocks are stripped"})
	if strings.Contains(got, "<!--") || strings.Contains(got, "-->") {
		t.Fatalf("rendered title still carries a comment delimiter: %q", got)
	}
}

// Amendment 12: a backslash in `dir` is an invalid escape inside the
// double-quoted YAML glob the renderer emits, and the commit's `repo` must be
// the checkout's exact directory name rather than ResolveRepo's substring
// match, so a mind thread's near-miss is refused instead of filed elsewhere.
func TestMMValidateCommitRefusesBackslashDirAndFuzzyRepo(t *testing.T) {
	contractMLEnv(t)
	contractMLRepo(t, "kraken")
	c := contractMLCommit()
	c.Dir = `internal\jobs`
	if err := mmValidateCommit(&c); err == nil {
		t.Fatal("a dir with a backslash must be refused")
	}
	c = contractMLCommit()
	c.Repo = "krak"
	if err := mmValidateCommit(&c); err == nil {
		t.Fatal("a repo that is only a substring of the checkout name must be refused")
	}
	c = contractMLCommit()
	c.Repo = "KRAKEN"
	if err := mmValidateCommit(&c); err != nil {
		t.Fatalf("the checkout name is matched case-insensitively: %v", err)
	}
}
