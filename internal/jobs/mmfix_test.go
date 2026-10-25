package jobs

// Regression pins for the defects the PR 2 review rounds found after the blind
// suites were authored (50-contract.md amendments 7, 9, 11, 12 and 13).
// Written by the lead, sighted; each pins the obligation, not the
// implementation line.

import (
	"strings"
	"testing"

	"kraken/internal/store"
)

// (Amendments 7 and 11 pinned ReviseMMItem and the mind thread's brief, both
// retired in job 674.)

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
// double-quoted YAML glob the renderer emits, and the item's `repo` must be
// the checkout's exact directory name rather than ResolveRepo's substring
// match, so a near-miss is refused instead of filed elsewhere. (The rule moved
// from the retired mmValidateCommit to mmValidateItem, which a mind run's
// items pass through, in job 674.)
func TestMMValidateItemRefusesBackslashDirAndFuzzyRepo(t *testing.T) {
	contractMLEnv(t)
	contractMLRepo(t, "kraken")
	item := func() MMProposal {
		return MMProposal{Title: "the daemon, not zeus, watches CI", Scope: "repo", Repo: "kraken",
			Dir: "internal/jobs", Kind: "decision", Body: "kraken serve's PR tracker bumps zeus.",
			Quote: "the daemon, not zeus, watches CI", Source: CaptureSource{Kind: "manual"}}
	}
	c := item()
	c.Dir = `internal\jobs`
	if err := mmValidateItem(&c); err == nil {
		t.Fatal("a dir with a backslash must be refused")
	}
	c = item()
	c.Repo = "krak"
	if err := mmValidateItem(&c); err == nil {
		t.Fatal("a repo that is only a substring of the checkout name must be refused")
	}
	c = item()
	c.Repo = "KRAKEN"
	if err := mmValidateItem(&c); err != nil {
		t.Fatalf("the checkout name is matched case-insensitively: %v", err)
	}
}
