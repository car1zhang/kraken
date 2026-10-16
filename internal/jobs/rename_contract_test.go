package jobs

// rename_contract_test.go — blind LOGIC tests for the jobs half of the rename
// feature (50-contract.md, Slice A "Jobs layer"): jobs.RenameJob's happy path
// and gate SUCCESSES, plus the five former-name resolvers.
//
// Authored from the contract alone, before any implementation existed. Every
// package-level identifier declared here is prefixed `contractRename` so an
// edge author sharing this package cannot collide with it.
//
// No test here shells tmux: every fixture job's cell session is absent from
// tmux, so RenameJob's convergence branch is skipped (contract, "Environment
// obligations").

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"kraken/internal/core"
	"kraken/internal/store"
	"kraken/internal/util"
)

// contractRenameSeed creates one active job row and returns it loaded through
// the jobs layer (so ID/Rev are populated exactly as a real caller sees them).
func contractRenameSeed(t *testing.T, st *store.Store, slug, state string) *Job {
	t.Helper()
	mustCreateJob(t, st, store.Job{Slug: slug, Repo: "monolith", State: state})
	j := LoadJobBySlug(slug)
	if j == nil {
		t.Fatalf("fixture job %s did not load", slug)
	}
	return j
}

// contractRenameSet turns a slice into a set for order-insensitive membership
// assertions (the contract leaves FormerSlugs' order unspecified).
func contractRenameSet(ss []string) map[string]bool {
	out := make(map[string]bool, len(ss))
	for _, s := range ss {
		out[s] = true
	}
	return out
}

// TestContractRenameHappyPath is jobs.RenameJob's whole success contract:
// the stored slug is the SLUGIFIED target, the result names it, and j is
// updated in place (Slug, Rev, FormerSlugs).
func TestContractRenameHappyPath(t *testing.T) {
	st := useTestStore(t)
	j := contractRenameSeed(t, st, "alpha", "")
	oldRev := j.Rev

	res, err := RenameJob(j, "Omega Two")
	if err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	if res == nil {
		t.Fatal("RenameJob returned a nil result on success")
	}
	if res.Slug != "omega-two" {
		t.Errorf("result slug = %q, want the slugified omega-two", res.Slug)
	}
	if j.Slug != "omega-two" {
		t.Errorf("j.Slug = %q, want omega-two — j is updated in place", j.Slug)
	}
	if j.Rev != oldRev+1 {
		t.Errorf("j.Rev = %d, want %d — a rename IS identity and bumps rev", j.Rev, oldRev+1)
	}
	if got := contractRenameSet(j.FormerSlugs); !got["alpha"] || len(j.FormerSlugs) != 1 {
		t.Errorf("j.FormerSlugs = %v, want exactly [alpha]", j.FormerSlugs)
	}
	if LoadJobBySlug("omega-two") == nil {
		t.Error("the renamed job does not resolve under its new slug")
	}
}

// TestContractRenameNoOpChangesNothing: gate (2). A slugified target equal to
// the current slug succeeds, returns the current slug, and bumps nothing —
// including the raw form "Alpha", which slugifies to the current name.
func TestContractRenameNoOpChangesNothing(t *testing.T) {
	st := useTestStore(t)
	j := contractRenameSeed(t, st, "alpha", "")
	oldRev := j.Rev

	for _, to := range []string{"alpha", "Alpha"} {
		res, err := RenameJob(j, to)
		if err != nil {
			t.Fatalf("no-op rename to %q: %v", to, err)
		}
		if res.Slug != "alpha" {
			t.Errorf("no-op rename to %q: result slug = %q, want alpha", to, res.Slug)
		}
		if j.Rev != oldRev {
			t.Errorf("no-op rename to %q bumped rev to %d, want %d", to, j.Rev, oldRev)
		}
		if len(j.FormerSlugs) != 0 {
			t.Errorf("no-op rename to %q recorded former slugs %v, want none", to, j.FormerSlugs)
		}
	}
	if fresh := LoadJobBySlug("alpha"); fresh == nil || fresh.Rev != oldRev {
		t.Errorf("the stored row moved on a no-op: %+v", fresh)
	}
}

// TestContractRenameHasNoCellStateGate is the inverse of archive's gate, and
// the whole point of Q1's answer: a working or blocked job passes the gate
// table. The contract states it flatly — "There is NO cell-state gate".
func TestContractRenameHasNoCellStateGate(t *testing.T) {
	st := useTestStore(t)

	for i, state := range []string{"working", "blocked"} {
		slug := "live" + strconv.Itoa(i)
		j := contractRenameSeed(t, st, slug, state)
		res, err := RenameJob(j, slug+"-renamed")
		if err != nil {
			t.Fatalf("renaming a %s job: %v — there is no cell-state gate", state, err)
		}
		if res.Slug != slug+"-renamed" {
			t.Errorf("%s job: result slug = %q, want %q", state, res.Slug, slug+"-renamed")
		}
		if fresh := LoadJobBySlug(slug + "-renamed"); fresh == nil || fresh.State != state {
			t.Errorf("%s job: the rename changed the state to %+v — job state is never written", state, fresh)
		}
	}
}

// TestContractRenameKeepsArtifactRows: a job's artifacts are rows keyed on
// its id, so a rename moves nothing and loses nothing — the rows are still
// there under the renamed job, Skipped is empty, and no Note names a path.
func TestContractRenameKeepsArtifactRows(t *testing.T) {
	st := useTestStore(t)
	j := contractRenameSeed(t, st, "alpha", "")
	if err := st.SyncArtifacts(j.ID, []store.ArtifactFile{{Path: "report.md", Bytes: []byte("REPORT"), Mtime: 1}}, nil); err != nil {
		t.Fatal(err)
	}

	res, err := RenameJob(j, "omega")
	if err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	arts, err := st.ListArtifacts(j.ID)
	if err != nil || len(arts) != 1 || arts[0].Path != "report.md" || string(arts[0].Bytes) != "REPORT" {
		t.Fatalf("artifact rows after rename = %+v (%v), want the one report.md row", arts, err)
	}
	if len(res.Skipped) != 0 {
		t.Errorf("Skipped = %v, want empty — nothing is a file to strand any more", res.Skipped)
	}
	for _, n := range res.Notes {
		if strings.Contains(n, string(filepath.Separator)) {
			t.Errorf("note %q carries a path", n)
		}
	}
}

// TestContractRenameStoresJobFormerSlugsEmptyForNeverRenamed: FormerSlugs is
// empty (never nil-vs-populated ambiguity) for a job that was never renamed.
func TestContractRenameStoresJobFormerSlugsEmptyForNeverRenamed(t *testing.T) {
	st := useTestStore(t)
	contractRenameSeed(t, st, "alpha", "")
	j := LoadJobBySlug("alpha")
	if j == nil {
		t.Fatal("fixture did not load")
	}
	if len(j.FormerSlugs) != 0 {
		t.Errorf("FormerSlugs = %v for a never-renamed job, want empty", j.FormerSlugs)
	}
}

// TestContractRenameBranchFamilyIncludesFormerArms: branchFamily gains, per
// former slug F, the id arms carl/<slugify(F)>-<id>[-rN] and the pre-cutover
// legacy arms carl/<slugify(F)>[-rN]. The current-name arms are unchanged —
// this is what keeps a shipped PR attached to its card across a rename.
func TestContractRenameBranchFamilyIncludesFormerArms(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "omega", ID: 7, Run: 3, FormerSlugs: []string{"alpha", "Beta Two"}}
	fam := branchFamily(j)

	for _, want := range []string{
		// current name, unchanged
		"carl/omega-7", "carl/omega-7-r2", "carl/omega-7-r3",
		"carl/omega", "carl/omega-r2", "carl/omega-r3",
		// former name 1
		"carl/alpha-7", "carl/alpha-7-r2", "carl/alpha-7-r3",
		"carl/alpha", "carl/alpha-r2", "carl/alpha-r3",
		// former name 2, slugified
		"carl/beta-two-7", "carl/beta-two-7-r2", "carl/beta-two-7-r3",
		"carl/beta-two", "carl/beta-two-r2", "carl/beta-two-r3",
	} {
		if !fam[want] {
			t.Errorf("branchFamily is missing %q — a PR on it silently detaches from the card", want)
		}
	}
}

// TestContractRenameJobProjectDirsNeverShrinks is the single most important
// resolver test: the token scan's watermarks are accumulate-forward, so a dir
// set that shrinks across a rename is a silent, unrecoverable loss. Every dir
// the scan returned BEFORE a rename must still be returned after it, and each
// former slug contributes its own four bases.
func TestContractRenameJobProjectDirsNeverShrinks(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(util.Home(), "Developer", "monolith")

	before := &Job{Slug: "alpha", ID: 7}
	beforeDirs, err := JobProjectDirs(before, repo)
	if err != nil {
		t.Fatalf("JobProjectDirs(before): %v", err)
	}
	if len(beforeDirs) != 4 {
		t.Fatalf("fixture: pre-rename dirs = %v, want the four current bases", beforeDirs)
	}

	after := &Job{Slug: "omega", ID: 7, FormerSlugs: []string{"alpha"}}
	afterDirs, err := JobProjectDirs(after, repo)
	if err != nil {
		t.Fatalf("JobProjectDirs(after): %v", err)
	}
	got := contractRenameSet(afterDirs)
	for _, d := range beforeDirs {
		if !got[d] {
			t.Errorf("the rename DROPPED %q from the scan set — token watermarks are lost silently", d)
		}
	}
	if len(afterDirs) != 8 {
		t.Errorf("dirs = %v (%d), want the current slug's four bases plus the former slug's four", afterDirs, len(afterDirs))
	}

	root := filepath.Join(util.Home(), ".claude", "projects")
	for i, want := range []string{
		core.ProjectDirName(CellWorktree(after, repo)),
		core.ProjectDirName(filepath.Join(cellDir(after), "wt")),
		core.ProjectDirName(LegacyCellWorktree(after, repo)),
		core.ProjectDirName(filepath.Join(legacyCellDir(after), "wt")),
	} {
		if afterDirs[i] != filepath.Join(root, want) {
			t.Errorf("dirs[%d] = %q, want %q — the current slug's bases come first", i, afterDirs[i], filepath.Join(root, want))
		}
	}

	former := &Job{Slug: "alpha", ID: 7}
	for _, want := range []string{
		core.ProjectDirName(CellWorktree(former, repo)),
		core.ProjectDirName(filepath.Join(cellDir(former), "wt")),
		core.ProjectDirName(LegacyCellWorktree(former, repo)),
		core.ProjectDirName(filepath.Join(legacyCellDir(former), "wt")),
	} {
		if !got[filepath.Join(root, want)] {
			t.Errorf("the former slug's base %q is missing from the scan set", want)
		}
	}
}

// TestContractRenameLoadJobBySlugFallsBackToFormerSlug: the store path falls
// back to GetJobByFormerSlug on a miss, which is what keeps zeus' frozen brief
// verbs (`kraken load set <old>`) and the baked pr-hook/limit-hook commands
// resolving after a rename.
func TestContractRenameLoadJobBySlugFallsBackToFormerSlug(t *testing.T) {
	st := useTestStore(t)
	j := contractRenameSeed(t, st, "alpha", "")
	if _, err := RenameJob(j, "omega"); err != nil {
		t.Fatalf("RenameJob: %v", err)
	}

	got := LoadJobBySlug("alpha")
	if got == nil {
		t.Fatal("the old slug no longer resolves — every baked old-slug command is now broken")
	}
	if got.ID != j.ID || got.Slug != "omega" {
		t.Errorf("old slug resolved to %+v, want the renamed job %d under slug omega", got, j.ID)
	}
	if LoadJobBySlug("never-held") != nil {
		t.Error("a slug nobody ever held resolved to a job")
	}
}

// TestContractRenameCurrentSlugBeatsFormer: when one active job's CURRENT
// slug equals another active job's FORMER slug, the current-slug lookup wins.
// The archive/unarchive detour is the only way to build that overlap now that
// the reservation exists, and it doubles as the required slug-collision
// fixture.
func TestContractRenameCurrentSlugBeatsFormer(t *testing.T) {
	st := useTestStore(t)

	first := contractRenameSeed(t, st, "shared", "")
	if _, err := RenameJob(first, "one"); err != nil {
		t.Fatalf("rename first: %v", err)
	}
	if _, err := st.ArchiveJob(first.ID); err != nil {
		t.Fatalf("archive first: %v", err)
	}
	second := contractRenameSeed(t, st, "shared", "")
	if err := st.UnarchiveJob(first.ID); err != nil {
		t.Fatalf("unarchive first: %v", err)
	}

	got := LoadJobBySlug("shared")
	if got == nil {
		t.Fatal("shared resolved to nothing")
	}
	if got.ID != second.ID {
		t.Errorf("shared resolved to job %d (the FORMER owner), want %d — a current slug always beats a former one",
			got.ID, second.ID)
	}
}

// TestContractRenameCellDirCandidates: current cellDir first, then
// <cellsRoot>/<slugify(F)>-<id> per former slug.
func TestContractRenameCellDirCandidates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "omega", ID: 5, FormerSlugs: []string{"alpha", "Beta Two"}}

	got := cellDirCandidates(j)
	want := []string{
		cellDir(j),
		filepath.Join(cellsRoot(), "alpha-5"),
		filepath.Join(cellsRoot(), "beta-two-5"),
	}
	if len(got) != len(want) {
		t.Fatalf("cellDirCandidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("cellDirCandidates[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestContractRenameExistingCellDirProbes: the first candidate that exists on
// disk wins, current first; nothing on disk falls back to the current cellDir.
func TestContractRenameExistingCellDirProbes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "omega", ID: 5, FormerSlugs: []string{"alpha"}}

	if got := existingCellDir(j); got != cellDir(j) {
		t.Errorf("with nothing on disk existingCellDir = %q, want the current cellDir %q", got, cellDir(j))
	}

	formerDir := filepath.Join(cellsRoot(), "alpha-5")
	if err := os.MkdirAll(formerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := existingCellDir(j); got != formerDir {
		t.Errorf("existingCellDir = %q, want the former-name dir %q that actually exists", got, formerDir)
	}

	if err := os.MkdirAll(cellDir(j), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := existingCellDir(j); got != cellDir(j) {
		t.Errorf("existingCellDir = %q, want the CURRENT dir %q once both exist", got, cellDir(j))
	}
}

// TestContractRenameExistingWorktreeProbesFormerCandidates: ExistingWorktree
// probes every candidate, current first — so a renamed LIVE cell's worktree
// (still on disk under the old name, since nothing is ever moved) is found.
func TestContractRenameExistingWorktreeProbesFormerCandidates(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(util.Home(), "Developer", "monolith")
	j := &Job{Slug: "omega", ID: 5, FormerSlugs: []string{"alpha"}}

	if got := ExistingWorktree(j, repo); got != "" {
		t.Fatalf("fixture: ExistingWorktree = %q with nothing on disk, want empty", got)
	}

	formerWT := filepath.Join(cellsRoot(), "alpha-5", "wt-monolith")
	if err := os.MkdirAll(formerWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ExistingWorktree(j, repo); got != formerWT {
		t.Errorf("ExistingWorktree = %q, want the former-name worktree %q", got, formerWT)
	}

	currentWT := CellWorktree(j, repo)
	if err := os.MkdirAll(currentWT, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ExistingWorktree(j, repo); got != currentWT {
		t.Errorf("ExistingWorktree = %q, want the CURRENT worktree %q once both exist", got, currentWT)
	}
}

// TestContractRenameThreadNamePrefixes: current ThreadNamePrefix first, then
// thread-<slugify(F)>- per former slug. A pane named with a former prefix is
// still the job's thread, which is what stops ReconcileThreads closing a live
// row after a rename.
func TestContractRenameThreadNamePrefixes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "omega", ID: 5, FormerSlugs: []string{"alpha", "Beta Two"}}

	got := threadNamePrefixes(j)
	want := []string{"thread-omega-", "thread-alpha-", "thread-beta-two-"}
	if len(got) != len(want) {
		t.Fatalf("threadNamePrefixes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("threadNamePrefixes[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if got[0] != ThreadNamePrefix(j) {
		t.Errorf("threadNamePrefixes[0] = %q, want ThreadNamePrefix(j) = %q", got[0], ThreadNamePrefix(j))
	}
}

// TestContractRenameFindCellSession: the current session name when it is
// live, else cell-<slugify(F)>-<id> for the first former slug that is; false
// when no candidate is alive. This is the predicate the daemon's cell-gone
// check rides on, and a false negative here fires a dead-cell alert on a live
// cell.
func TestContractRenameFindCellSession(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{Slug: "omega", ID: 5, FormerSlugs: []string{"alpha", "Beta Two"}}
	current := CellSession(j)

	if name, ok := FindCellSession(j, map[string]bool{}); ok || name != "" {
		t.Errorf("no candidate alive: got (%q, %v), want (\"\", false)", name, ok)
	}
	if name, ok := FindCellSession(j, map[string]bool{current: true}); !ok || name != current {
		t.Errorf("current alive: got (%q, %v), want (%q, true)", name, ok, current)
	}
	if name, ok := FindCellSession(j, map[string]bool{"cell-alpha-5": true}); !ok || name != "cell-alpha-5" {
		t.Errorf("former alive: got (%q, %v), want (cell-alpha-5, true)", name, ok)
	}
	if name, ok := FindCellSession(j, map[string]bool{"cell-beta-two-5": true}); !ok || name != "cell-beta-two-5" {
		t.Errorf("second former alive: got (%q, %v), want (cell-beta-two-5, true)", name, ok)
	}
	both := map[string]bool{current: true, "cell-alpha-5": true}
	if name, ok := FindCellSession(j, both); !ok || name != current {
		t.Errorf("both alive: got (%q, %v), want the current name %q", name, ok, current)
	}
}
