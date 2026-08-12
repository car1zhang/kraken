package jobs

import (
	"os"
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
		{"CellBranch", CellBranch(old), CellBranch(cur), "carl/helper-catalog-36"},
		{"PlutoArtifactPrefix", PlutoArtifactPrefix(old), PlutoArtifactPrefix(cur), "helper-catalog-36."},
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

// A *Job parsed from a .job.md file carries ID == 0 — the id is minted by the
// store and never appears in the file — and LoadJobs falls back to
// LoadJobsFromFiles whenever no store is wired. Creating or destroying a cell
// from such a job must fail loudly rather than silently operating on
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
	if got := CellBranch(j); got != "carl/helper-catalog-111-r3" {
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

// --- cutover ---

func TestPlanCutoverPrefersTheLongestPrefix(t *testing.T) {
	// Prefixes are not self-delimiting: with jobs "foo" and "foo-2" both
	// present, "foo-2.plan.md" matches BOTH "foo." (name "2.plan.md") and
	// "foo-2.". Picking the shorter one files one job's artifact under
	// another job's id — the exact class of bug this change ends.
	byPrefix := map[string]*Job{
		"foo.":   {Slug: "foo", ID: 7},
		"foo-2.": {Slug: "foo-2", ID: 9},
	}
	j, prefix := longestPrefixMatch("foo-2.plan.md", byPrefix)
	if j == nil || j.ID != 9 {
		t.Fatalf("matched %+v via %q, want the foo-2 job (id 9)", j, prefix)
	}
	if prefix != "foo-2." {
		t.Errorf("prefix = %q, want %q", prefix, "foo-2.")
	}
}

func TestIsPlutoDayNote(t *testing.T) {
	// pluto's daily notes share the directory with flat artifact copies and
	// must never be renamed.
	for _, name := range []string{"7151.md", "0.md", "99999.md"} {
		if !isPlutoDayNote(name) {
			t.Errorf("%q should be recognised as a pluto day note", name)
		}
	}
	for _, name := range []string{
		"helper-catalog.plan.md", "asr-latency.90-build-report.md", ".md", "12a.md", "7151.txt", "7151",
	} {
		if isPlutoDayNote(name) {
			t.Errorf("%q must NOT be treated as a pluto day note", name)
		}
	}
}

func TestRunCutoverRefusesToOverwrite(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("helper-catalog.plan.md")
	write("helper-catalog-36.plan.md") // target already taken

	plan := &CutoverPlan{Renames: []CutoverRename{
		{Dir: dir, From: "helper-catalog.plan.md", To: "helper-catalog-36.plan.md", Job: "helper-catalog"},
	}}
	done, problems := RunCutover(plan)
	if done != 0 {
		t.Errorf("done = %d, want 0 — an existing target means two files claim one name", done)
	}
	if len(problems) != 1 || !strings.Contains(problems[0], "already exists") {
		t.Errorf("problems = %v, want one 'already exists' report", problems)
	}
	// The source must still be there: refusing means leaving both alone.
	if _, err := os.Stat(filepath.Join(dir, "helper-catalog.plan.md")); err != nil {
		t.Errorf("the source was lost on a refused rename: %v", err)
	}
}

func TestRunCutoverRenamesAndIsRerunnable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "helper-catalog.plan.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	plan := &CutoverPlan{Renames: []CutoverRename{
		{Dir: dir, From: "helper-catalog.plan.md", To: "helper-catalog-36.plan.md", Job: "helper-catalog"},
	}}
	done, problems := RunCutover(plan)
	if done != 1 || len(problems) != 0 {
		t.Fatalf("done=%d problems=%v, want 1 and none", done, problems)
	}
	if _, err := os.Stat(filepath.Join(dir, "helper-catalog-36.plan.md")); err != nil {
		t.Errorf("renamed file missing: %v", err)
	}
	// Re-running the same plan finds the source gone; it must report that
	// rather than claiming success, since a partial migration must never be
	// mistaken for a finished one.
	done2, problems2 := RunCutover(plan)
	if done2 != 0 || len(problems2) != 1 {
		t.Errorf("second run: done=%d problems=%v, want 0 and one problem", done2, problems2)
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
