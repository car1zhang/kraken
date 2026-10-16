package jobs

import (
	"path/filepath"
	"strings"
	"testing"
)

// cellidentity_test.go — a job's runtime names are derived from its slug AND
// its immutable store id, so two jobs that share a name can never share a
// directory, a tmux session, or a branch.
//
// The incident these pin: job #36 "helper-catalog" was archived, which frees
// the slug but deletes none of the slug-named state; job #111 then took the
// name and every StartCell failed on #36's leftover git worktree, 24 times
// over nine minutes, while the board served #36's artifacts off #111's card.
// The ids in these tests are those real ids, so a failure here reads as the
// bug it prevents.

func helperCatalog(id int64) *Job {
	return &Job{Slug: "helper-catalog", ID: id, Run: 1}
}

func TestCellNamesSeparateTwoGenerationsOfOneSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_BRANCH_PREFIX", "")
	old, cur := helperCatalog(36), helperCatalog(111)

	pairs := []struct {
		what     string
		a, b     string
		wantSubA string
	}{
		{"cellDir", cellDir(old), cellDir(cur), "helper-catalog-36"},
		{"CellWorktree", CellWorktree(old, "/x/monolith"), CellWorktree(cur, "/x/monolith"), "helper-catalog-36"},
		{"CellArtifacts", CellArtifacts(old), CellArtifacts(cur), "helper-catalog-36"},
		{"CellSession", CellSession(old), CellSession(cur), "cell-helper-catalog-36"},
		{"CellBranch", CellBranch(old), CellBranch(cur), "kraken/helper-catalog-36"},
		{"UploadsDir", UploadsDir(old.Slug, old.ID), UploadsDir(cur.Slug, cur.ID), "helper-catalog-36"},
	}
	for _, p := range pairs {
		if p.a == p.b {
			t.Errorf("%s: both generations resolve to %q — a reused slug still collides, which is the whole bug", p.what, p.a)
		}
		if !strings.Contains(p.a, p.wantSubA) {
			t.Errorf("%s: got %q, want it to contain %q", p.what, p.a, p.wantSubA)
		}
	}
}

// The slug must come FIRST. This name is read by a human in `k ls`, as the
// editor root inside a cell, and as a branch in GitHub; leading with an
// opaque id would satisfy uniqueness and fail the reason the slug is there.
func TestCellNameLeadsWithTheSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if got := cellName(helperCatalog(111)); got != "helper-catalog-111" {
		t.Errorf("cellName = %q, want %q", got, "helper-catalog-111")
	}
}

// A *Job built by ParseJob carries ID == 0 — the id is minted by the store
// and never appears in job text. Creating or destroying a cell from such a
// job must fail loudly rather than silently operating on
// ~/.kraken/cells/<slug>-0.
func TestCheckCellIdentityRefusesUnusableJobs(t *testing.T) {
	cases := []struct {
		name    string
		job     *Job
		wantSub string
	}{
		{"zero id", &Job{Slug: "helper-catalog", ID: 0}, "no store id"},
		{"negative id", &Job{Slug: "helper-catalog", ID: -1}, "no store id"},
		{"empty slug", &Job{Slug: "", ID: 5}, "slugifies to nothing"},
		{"punctuation-only slug", &Job{Slug: "///", ID: 5}, "slugifies to nothing"},
		{"nil job", nil, "no job"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := checkCellIdentity(c.job)
			if err == nil {
				t.Fatalf("checkCellIdentity(%+v) = nil, want an error", c.job)
			}
			if !strings.Contains(err.Error(), c.wantSub) {
				t.Errorf("error %q does not mention %q", err, c.wantSub)
			}
		})
	}
	if err := checkCellIdentity(helperCatalog(111)); err != nil {
		t.Errorf("a well-formed job was refused: %v", err)
	}
}

// UploadsDir returns "" rather than a path when it cannot name a job's dir,
// so ArchiveJob's os.RemoveAll can never resolve to the shared uploads root.
func TestUploadsDirRefusesToNameTheRoot(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, c := range []struct {
		slug string
		id   int64
	}{
		{"", 5}, {"///", 5}, {"helper-catalog", 0}, {"helper-catalog", -1},
	} {
		if got := UploadsDir(c.slug, c.id); got != "" {
			t.Errorf("UploadsDir(%q, %d) = %q, want \"\" — a destructive caller would RemoveAll that", c.slug, c.id, got)
		}
	}
}

// branchFamily must keep matching the PRE-cutover bare-slug branches. Four
// PRs were open on such branches the day this changed, and the daemon's
// PR-repair pass uses this set to tell a job's own PRs from foreign ones —
// dropping the legacy names makes it silently detach them from their cards.
func TestBranchFamilyKeepsLegacyBareSlugBranches(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("KRAKEN_BRANCH_PREFIX", "")
	j := &Job{Slug: "helper-catalog", ID: 111, Run: 3}
	fam := branchFamily(j)

	for _, want := range []string{
		"carl/helper-catalog-111", "carl/helper-catalog-111-r2", "carl/helper-catalog-111-r3",
		"carl/helper-catalog", "carl/helper-catalog-r2", "carl/helper-catalog-r3",
	} {
		if !fam[want] {
			t.Errorf("branchFamily is missing %q", want)
		}
	}
	// Only branchForRun writes, and it always emits the id form.
	if got := CellBranch(j); got != "kraken/helper-catalog-111-r3" {
		t.Errorf("CellBranch = %q, want the id form", got)
	}
}

// The legacy helpers are what let a token scan still find transcripts written
// before the cutover — Claude Code filed those under the bare-slug path, and
// since schema v11 drops every token row and re-sums from scratch, losing
// this base means every job reports zero for its whole history.
func TestLegacyCellPathsAreTheBareSlug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := helperCatalog(111)
	if got := legacyCellDir(j); filepath.Base(got) != "helper-catalog" {
		t.Errorf("legacyCellDir = %q, want it to end in the bare slug", got)
	}
	if got := LegacyCellWorktree(j, "/x/monolith"); !strings.HasSuffix(got, "helper-catalog/wt-monolith") {
		t.Errorf("LegacyCellWorktree = %q", got)
	}
	if legacyCellDir(j) == cellDir(j) {
		t.Error("legacy and current cell dirs are identical — the legacy base is pointless")
	}
}

// --- start-failure surfacing ---
//
// The StartErrors side channel is gone with the queue pass, and so are the two
// tests that pinned it (per-job reasons, and publishing a snapshot rather than
// the daemon's live map). Both existed only because a start happened in the
// BACKGROUND, a tick after the click, with no request left to answer: the
// reason had to be parked somewhere a later board poll could find it. Submit
// starts the cell inside the POST now, so a failure is that request's own
// error — SubmitJob returns it, webSubmit surfaces it verbatim as the 409
// body, and there is nothing left to publish or to snapshot.
