package jobs

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

// rename_edge_test.go — the EDGE dimension of the rename feature's jobs half
// (50-contract.md, slice A "Jobs layer"): every gate refusal with its
// distinguishing fragment, the frozen gate ORDER wherever two gates apply at
// once, and the former-name resolvers' negative paths (nothing on disk,
// nothing live, no formers at all, formers that collide with the current name).
//
// Written blind from the contract, so every assertion traces to a stated
// obligation. No test here shells tmux: every fixture's cell session name is
// one no tmux server has, so the convergence branch is never entered.
// Helpers are `edgeRen`-prefixed — a logic author shares this package.

// edgeRenJob creates one active job in the wired store and returns it as the
// *Job every RenameJob caller holds (ID and Rev populated, FormerSlugs as the
// store loaded them). Goes through LoadJobBySlug rather than hand-building a
// Job so the fixture carries whatever the store's own load path produces.
func edgeRenJob(t *testing.T, st *store.Store, slug string) *Job {
	t.Helper()
	mustCreateJob(t, st, store.Job{Slug: slug, Repo: "monolith", Base: "main"})
	j := LoadJobBySlug(slug)
	if j == nil {
		t.Fatalf("job %q missing right after create", slug)
	}
	if j.ID < 1 || j.Rev < 1 {
		t.Fatalf("fixture job %q has no store identity: id=%d rev=%d", slug, j.ID, j.Rev)
	}
	return j
}

// edgeRenArchivedJob creates a job, archives it, and returns the ARCHIVED
// generation as a *Job carrying ArchivedName — the shape the contract's gate 4
// keys on ("`j` archived (`ArchivedName != ""` or archived row)"). The row
// keeps its slug column, which is what makes it a slug HOLDER for gate 5.
func edgeRenArchivedJob(t *testing.T, st *store.Store, slug string) *Job {
	t.Helper()
	row := mustCreateJob(t, st, store.Job{Slug: slug, Repo: "monolith"})
	name, err := st.ArchiveJob(row.ID)
	if err != nil {
		t.Fatalf("ArchiveJob %q: %v", slug, err)
	}
	after, err := st.GetJobByID(row.ID)
	if err != nil {
		t.Fatalf("GetJobByID after archive: %v", err)
	}
	return &Job{ID: after.ID, Slug: after.Slug, Rev: after.Rev, ArchivedName: name, Repo: "monolith"}
}

// edgeRenRefusal runs a rename that must be refused and returns the error
// text. A nil error, or a non-nil result, both fail here: a refusal that
// returns a RenameResult has already told its caller a new name to re-point to.
func edgeRenRefusal(t *testing.T, j *Job, to string) string {
	t.Helper()
	before := j.Slug
	res, err := RenameJob(j, to)
	if err == nil {
		t.Fatalf("RenameJob(%q -> %q) succeeded, want a refusal (result %+v)", before, to, res)
	}
	if j.Slug != before {
		t.Fatalf("a refused rename mutated j.Slug: %q -> %q", before, j.Slug)
	}
	return err.Error()
}

// edgeRenWantFragment asserts the refusal carries the contract's distinguishing
// fragment for the gate that was supposed to fire.
func edgeRenWantFragment(t *testing.T, got, fragment, what string) {
	t.Helper()
	if !strings.Contains(got, fragment) {
		t.Errorf("%s: refusal = %q, want it to contain %q", what, got, fragment)
	}
}

// TestEdgeRenameJobUnusableTargets covers gate 1: "util.Slugify(to) empty →
// error containing \"slugifies to nothing\"". Every input here slugifies to the
// empty string, which is the one target no cell name can be built from.
func TestEdgeRenameJobUnusableTargets(t *testing.T) {
	st := useTestStore(t)
	j := edgeRenJob(t, st, "edge-ren-unusable")

	for _, to := range []string{"", "!!!", "   ", "---", "***", "\t\n", "/"} {
		got := edgeRenRefusal(t, j, to)
		edgeRenWantFragment(t, got, "slugifies to nothing", "target "+strconv.Quote(to))
	}
	if fresh := LoadJobBySlug("edge-ren-unusable"); fresh == nil || fresh.Rev != j.Rev {
		t.Errorf("a refused rename bumped the row's rev: %+v", fresh)
	}
}

// TestEdgeRenameJobReservedPrefixesBothDirections covers gate 3: "current slug
// OR target starts with `dream-` or `graph-` → error containing \"reserved
// prefix\"" — Q4's answer, and it binds both directions (renaming a prefixed
// job OFF its prefix, and renaming any job ONTO one).
func TestEdgeRenameJobReservedPrefixesBothDirections(t *testing.T) {
	st := useTestStore(t)

	// off the prefix: a dream job and a graph job may not be renamed away.
	for _, slug := range []string{"dream-2026-08-12", "graph-2026-08-12"} {
		j := edgeRenJob(t, st, slug)
		got := edgeRenRefusal(t, j, "edge-ren-plain")
		edgeRenWantFragment(t, got, "reserved prefix", slug+" off its prefix")
	}

	// onto the prefix: an ordinary job may not take one.
	plain := edgeRenJob(t, st, "edge-ren-plain-job")
	for _, to := range []string{"dream-2026-09-01", "graph-ingest"} {
		got := edgeRenRefusal(t, plain, to)
		edgeRenWantFragment(t, got, "reserved prefix", "target "+to)
	}
}

// TestEdgeRenameJobReservedPrefixCheckIsOnTheSlugifiedForm resolves a contract
// SILENCE in gate 3's favour of the rest of the row: the row states "Stored
// slug = util.Slugify(to)" and gates 5 and 6 both check the slugified form
// explicitly, so gate 3's unqualified "target starts with `dream-` or `graph-`"
// can only mean the form that would actually be stored. Read the other way,
// "Dream-Whatever" walks straight past the gate and becomes the dream- job the
// gate exists to prevent.
func TestEdgeRenameJobReservedPrefixCheckIsOnTheSlugifiedForm(t *testing.T) {
	st := useTestStore(t)
	plain := edgeRenJob(t, st, "edge-ren-case-job")

	for _, to := range []string{"Dream-Mixed-Case", "GRAPH-shouty", "Dream 2026 09 02"} {
		got := edgeRenRefusal(t, plain, to)
		edgeRenWantFragment(t, got, "reserved prefix", "target "+strconv.Quote(to))
	}
}

// TestEdgeRenameJobGateOrderIsFrozen holds the contract's numbered gate order
// wherever two gates apply to the same call. Order is only observable through
// WHICH refusal comes back, so each case below is a fixture both gates match
// and an assertion naming the EARLIER gate's fragment.
func TestEdgeRenameJobGateOrderIsFrozen(t *testing.T) {
	st := useTestStore(t)

	// gate 1 before gate 3: a reserved-prefix job renamed to an unusable
	// target reports the unusable target.
	dream := edgeRenJob(t, st, "dream-2026-08-13")
	edgeRenWantFragment(t, edgeRenRefusal(t, dream, "!!!"), "slugifies to nothing", "gate 1 before gate 3")

	// gate 2 before gate 3: renaming a reserved-prefix job to ITS OWN name is
	// the no-op, and the no-op wins — a dream job's identity is not a thing
	// that can be refused into existence.
	res, err := RenameJob(dream, "dream-2026-08-13")
	if err != nil {
		t.Fatalf("gate 2 before gate 3: no-op on a reserved-prefix job refused: %v", err)
	}
	if res == nil || res.Slug != "dream-2026-08-13" {
		t.Fatalf("no-op result = %+v, want Slug the current slug", res)
	}

	// gate 3 before gate 4: an ARCHIVED reserved-prefix job reports the
	// prefix, not the archive.
	archivedDream := edgeRenArchivedJob(t, st, "dream-2026-08-14")
	edgeRenWantFragment(t, edgeRenRefusal(t, archivedDream, "edge-ren-anything"),
		"reserved prefix", "gate 3 before gate 4")

	// gate 4 before gate 6: an archived job renamed onto a TAKEN active slug
	// reports the archive, since it is the job itself that cannot be renamed
	// at all.
	occupied := edgeRenJob(t, st, "edge-ren-occupied")
	archived := edgeRenArchivedJob(t, st, "edge-ren-was-archived")
	edgeRenWantFragment(t, edgeRenRefusal(t, archived, occupied.Slug),
		"unarchive", "gate 4 before gate 6")

	// gate 5 before gate 6: the colliding fixture — one slug string held by an
	// ARCHIVED job AND by another ACTIVE job at the same time (archiving frees
	// the name, so both generations legitimately carry it). The archived holder
	// is the earlier gate.
	edgeRenArchivedJob(t, st, "edge-ren-two-gens")
	rival := edgeRenJob(t, st, "edge-ren-two-gens")
	if rival.ID == 0 {
		t.Fatal("fixture: the new active job must exist on the freed slug")
	}
	mover := edgeRenJob(t, st, "edge-ren-mover")
	edgeRenWantFragment(t, edgeRenRefusal(t, mover, "edge-ren-two-gens"),
		"archived job", "gate 5 before gate 6")
}

// TestEdgeRenameJobArchivedRefusals covers gate 4 and gate 5 on their own, plus
// gate 5's slugified-form rule: the check is on util.Slugify(to), so a raw
// target that merely SLUGIFIES onto an archived job's slug is refused too.
func TestEdgeRenameJobArchivedRefusals(t *testing.T) {
	st := useTestStore(t)

	// gate 4: the job itself is archived (Q7 — no rename of archived jobs).
	archived := edgeRenArchivedJob(t, st, "edge-ren-archived-self")
	edgeRenWantFragment(t, edgeRenRefusal(t, archived, "edge-ren-archived-self-new"),
		"unarchive", "an archived job's own rename")

	// gate 5: the TARGET is held by an archived job — the jobs_active_slug
	// index alone would allow this, so the refusal is a policy gate above it.
	edgeRenArchivedJob(t, st, "edge-ren-ghost-slug")
	live := edgeRenJob(t, st, "edge-ren-live-mover")
	edgeRenWantFragment(t, edgeRenRefusal(t, live, "edge-ren-ghost-slug"),
		"archived job", "target held by an archived job")
	// same target, spelled so only its slugified form collides.
	edgeRenWantFragment(t, edgeRenRefusal(t, live, "Edge Ren Ghost Slug!"),
		"archived job", "target whose SLUGIFIED form collides with an archived job")
}

// TestEdgeRenameJobSlugifiedCollisionWithActiveJob covers gate 6 and the rule
// the testing plan calls out explicitly: the collision check runs on the
// SLUGIFIED form, so `My Job!` collides with an existing `my-job` even though
// the raw strings differ (markSlugIssues' rule).
func TestEdgeRenameJobSlugifiedCollisionWithActiveJob(t *testing.T) {
	st := useTestStore(t)
	edgeRenJob(t, st, "my-job")
	mover := edgeRenJob(t, st, "edge-ren-collide-mover")

	for _, to := range []string{"my-job", "My Job!", "  MY   JOB  ", "my--job"} {
		got := edgeRenRefusal(t, mover, to)
		edgeRenWantFragment(t, got, "taken", "target "+strconv.Quote(to))
	}
}

// TestEdgeRenameJobFormerNameReservationAndLapse covers gate 7 and the lapse
// the contract pairs with it: "target equals another active job's former slug →
// error containing \"former name\"", and the reservation lapses with the job
// (archive), like the slug itself.
func TestEdgeRenameJobFormerNameReservationAndLapse(t *testing.T) {
	st := useTestStore(t)

	holder := edgeRenJob(t, st, "edge-ren-freed-name")
	if _, err := RenameJob(holder, "edge-ren-holder-current"); err != nil {
		t.Fatalf("seed rename: %v", err)
	}
	if holder.Slug != "edge-ren-holder-current" {
		t.Fatalf("seed rename left j.Slug = %q", holder.Slug)
	}

	mover := edgeRenJob(t, st, "edge-ren-former-mover")
	edgeRenWantFragment(t, edgeRenRefusal(t, mover, "edge-ren-freed-name"),
		"former name", "target reserved as another active job's former name")
	// the slugified form again: a raw spelling of the same reserved name.
	edgeRenWantFragment(t, edgeRenRefusal(t, mover, "Edge Ren Freed Name"),
		"former name", "slugified form of a reserved former name")

	// archive the holder: the reservation lapses with it, so the name becomes
	// takeable. (The holder's CURRENT slug is now an archived job's slug and is
	// covered by gate 5 — this asserts the FORMER name, which is not.)
	if _, err := st.ArchiveJob(holder.ID); err != nil {
		t.Fatalf("ArchiveJob holder: %v", err)
	}
	res, err := RenameJob(mover, "edge-ren-freed-name")
	if err != nil {
		t.Fatalf("after the holder was archived the reservation must lapse: %v", err)
	}
	if res == nil || res.Slug != "edge-ren-freed-name" {
		t.Fatalf("result = %+v, want the freed name", res)
	}
	if mover.Slug != "edge-ren-freed-name" {
		t.Errorf("j.Slug = %q, want the new slug (updated in place)", mover.Slug)
	}
}

// TestEdgeRenameBackLeavesExactlyOneFormerSlug pins the former-set shape the
// contract states for A→B→A→B: one (id,"A") row, not two and not an error, and
// never the job's own CURRENT slug. j.FormerSlugs is where a caller sees it,
// and it is updated in place on success.
func TestEdgeRenameBackLeavesExactlyOneFormerSlug(t *testing.T) {
	st := useTestStore(t)
	j := edgeRenJob(t, st, "edge-ren-aaa")
	if len(j.FormerSlugs) != 0 {
		t.Fatalf("a never-renamed job carries FormerSlugs %v, want empty", j.FormerSlugs)
	}

	for i, to := range []string{"edge-ren-bbb", "edge-ren-aaa", "edge-ren-bbb"} {
		if _, err := RenameJob(j, to); err != nil {
			t.Fatalf("rename %d (-> %s): %v", i+1, to, err)
		}
	}

	if j.Slug != "edge-ren-bbb" {
		t.Fatalf("slug after the round trip = %q, want edge-ren-bbb", j.Slug)
	}
	if len(j.FormerSlugs) != 1 || j.FormerSlugs[0] != "edge-ren-aaa" {
		t.Fatalf("FormerSlugs = %v, want exactly [edge-ren-aaa]", j.FormerSlugs)
	}
	// and a reload agrees — the in-place update is not the only place the set
	// is correct.
	fresh := LoadJobBySlug("edge-ren-bbb")
	if fresh == nil {
		t.Fatal("renamed job does not resolve by its new slug")
	}
	if len(fresh.FormerSlugs) != 1 || fresh.FormerSlugs[0] != "edge-ren-aaa" {
		t.Fatalf("reloaded FormerSlugs = %v, want exactly [edge-ren-aaa]", fresh.FormerSlugs)
	}
}

// TestEdgeLoadJobBySlugCurrentBeatsFormer pins the precedence rule on the one
// fixture where the two can genuinely overlap: a former slug that a LATER job
// took as its CURRENT slug. The reservation forbids that while the owner is
// active, so the overlap is built across the reservation boundary — archive the
// owner (reservation lapses), create the new job on the freed name, unarchive
// the owner (its own current slug is still free).
func TestEdgeLoadJobBySlugCurrentBeatsFormer(t *testing.T) {
	st := useTestStore(t)

	owner := edgeRenJob(t, st, "edge-ren-overlap")
	if _, err := RenameJob(owner, "edge-ren-owner-now"); err != nil {
		t.Fatalf("seed rename: %v", err)
	}
	if _, err := st.ArchiveJob(owner.ID); err != nil {
		t.Fatalf("ArchiveJob owner: %v", err)
	}
	newcomer := edgeRenJob(t, st, "edge-ren-overlap")
	if err := st.UnarchiveJob(owner.ID); err != nil {
		t.Fatalf("UnarchiveJob owner: %v", err)
	}

	got := LoadJobBySlug("edge-ren-overlap")
	if got == nil {
		t.Fatal("the overlapping slug resolves to nothing")
	}
	if got.ID != newcomer.ID {
		t.Errorf("LoadJobBySlug(overlap) = job %d, want the CURRENT holder %d (current beats former)", got.ID, newcomer.ID)
	}
	// the owner still resolves by its own current name.
	if byOwn := LoadJobBySlug("edge-ren-owner-now"); byOwn == nil || byOwn.ID != owner.ID {
		t.Errorf("owner's current slug resolves to %+v, want job %d", byOwn, owner.ID)
	}
	// a slug nobody has ever held still misses.
	if miss := LoadJobBySlug("edge-ren-nobody"); miss != nil {
		t.Errorf("unknown slug resolved to %+v, want nil", miss)
	}
}

// TestEdgeCellDirCandidatesAndExistingCellDir covers the probe's two negative
// paths: nothing on disk at all (fall back to the CURRENT cellDir, so a create
// path never lands on a former name), and only the FORMER dir present (the
// live-cell-renamed case the whole feature exists for).
func TestEdgeCellDirCandidatesAndExistingCellDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 5, Slug: "now", FormerSlugs: []string{"Then One", "then-two"}}

	got := cellDirCandidates(j)
	want := []string{
		filepath.Join(cellsRoot(), "now-5"),
		filepath.Join(cellsRoot(), "then-one-5"),
		filepath.Join(cellsRoot(), "then-two-5"),
	}
	if len(got) != len(want) {
		t.Fatalf("cellDirCandidates = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("cellDirCandidates = %v, want %v (current first, then formers)", got, want)
		}
	}

	// nothing exists: the current name is the answer, never "" and never a
	// former one.
	if got := existingCellDir(j); got != cellDir(j) {
		t.Errorf("existingCellDir with nothing on disk = %q, want %q", got, cellDir(j))
	}

	// only a FORMER dir exists.
	former := filepath.Join(cellsRoot(), "then-two-5")
	if err := os.MkdirAll(former, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := existingCellDir(j); got != former {
		t.Errorf("existingCellDir with only the former dir = %q, want %q", got, former)
	}

	// a job with no formers at all degrades to exactly one candidate.
	plain := &Job{ID: 6, Slug: "solo"}
	if c := cellDirCandidates(plain); len(c) != 1 || c[0] != cellDir(plain) {
		t.Errorf("cellDirCandidates with no formers = %v, want [%s]", c, cellDir(plain))
	}
}

// TestEdgeExistingWorktreeProbesEveryCandidate covers the amended probe's
// boundary: `wt-<repo>` then bare `wt` under EVERY candidate, current first —
// and "" when no candidate holds either, which is what keeps a torn-down cell
// reporting nothing rather than a former name's path.
func TestEdgeExistingWorktreeProbesEveryCandidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 9, Slug: "current", FormerSlugs: []string{"older"}}
	repo := filepath.Join(util.Home(), "Developer", "monolith")

	if got := ExistingWorktree(j, repo); got != "" {
		t.Fatalf("nothing on disk: ExistingWorktree = %q, want empty", got)
	}

	// only the FORMER cell dir has a worktree, and only under the legacy bare
	// "wt" name — the deepest fallback of the four.
	legacy := filepath.Join(cellsRoot(), "older-9", "wt")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := ExistingWorktree(j, repo); got != legacy {
		t.Errorf("ExistingWorktree = %q, want the former candidate's bare wt %q", got, legacy)
	}
}

// TestEdgeFindCellSessionWithNothingLive covers the resolver's negative half:
// no candidate alive means false, whether the live set is empty, nil, or holds
// only other jobs' sessions. The daemon's cell-gone check fires only on this
// answer, so a wrong `true` here hides a dead cell and a wrong `false` alerts
// on a live one.
func TestEdgeFindCellSessionWithNothingLive(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	j := &Job{ID: 11, Slug: "edge-ren-session", FormerSlugs: []string{"edge-ren-session-old"}}

	for _, live := range []map[string]bool{
		nil,
		{},
		{"cell-someone-else-3": true},
		// a session named for the job's slug but WITHOUT the id is not this
		// job's cell (cellName is <slug>-<id>, and the id is what makes it
		// collision-proof).
		{"cell-edge-ren-session": true, "cell-edge-ren-session-old": true},
	} {
		if _, ok := FindCellSession(j, live); ok {
			t.Errorf("FindCellSession(live=%v) reported a live session", live)
		}
	}

	// the former name IS found when it is genuinely live (the not-yet-converged
	// cell), which is what makes the negative cases above meaningful.
	formerSess := "cell-edge-ren-session-old-11"
	got, ok := FindCellSession(j, map[string]bool{formerSess: true})
	if !ok || got != formerSess {
		t.Errorf("FindCellSession with the former session live = (%q, %v), want (%q, true)", got, ok, formerSess)
	}
}

// TestEdgeThreadNamePrefixesZeroAndMultipleFormers covers the thread matcher's
// two boundaries. Zero formers must yield exactly the current prefix — one
// extra entry there would widen every prefix match in the package — and
// multiple formers must all appear, current first, since a pane named with a
// former prefix is still the job's thread and closing a live row is (in the
// file's own words) the worst outcome in it.
func TestEdgeThreadNamePrefixesZeroAndMultipleFormers(t *testing.T) {
	none := &Job{ID: 3, Slug: "edge-ren-threads"}
	if got := threadNamePrefixes(none); len(got) != 1 || got[0] != ThreadNamePrefix(none) {
		t.Fatalf("threadNamePrefixes with no formers = %v, want [%s]", got, ThreadNamePrefix(none))
	}

	many := &Job{ID: 3, Slug: "edge-ren-threads", FormerSlugs: []string{"First Name", "second-name"}}
	got := threadNamePrefixes(many)
	want := []string{ThreadNamePrefix(many), "thread-first-name-", "thread-second-name-"}
	if len(got) != len(want) {
		t.Fatalf("threadNamePrefixes = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("threadNamePrefixes = %v, want %v (current first)", got, want)
		}
	}
	// every prefix must end in the separating dash: a prefix match without it
	// would let tid "1" absorb thread "12".
	for _, p := range got {
		if !strings.HasSuffix(p, "-") {
			t.Errorf("prefix %q does not end in the tid separator", p)
		}
	}
}

// TestEdgeJobProjectDirsDedupesCollidingMunges is the token scan's boundary and
// the one with a silent, unrecoverable failure mode: the dir set must never
// SHRINK across a rename, and it must never repeat a dir either — a duplicated
// base would sum one transcript file twice into accumulate-forward counters.
//
// The fixture makes a former name munge onto the CURRENT name's dirs: job slug
// "a" with id 1 has cellDir "a-1", and a former slug of "a-1" derives the
// legacy bare-slug dir "a-1" — the same string.
func TestEdgeJobProjectDirsDedupesCollidingMunges(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	repo := filepath.Join(util.Home(), "Developer", "monolith")

	before := &Job{ID: 1, Slug: "a-1", Repo: "monolith"}
	beforeDirs, err := JobProjectDirs(before, repo)
	if err != nil {
		t.Fatalf("JobProjectDirs before: %v", err)
	}

	after := &Job{ID: 1, Slug: "a", Repo: "monolith", FormerSlugs: []string{"a-1"}}
	afterDirs, err := JobProjectDirs(after, repo)
	if err != nil {
		t.Fatalf("JobProjectDirs after: %v", err)
	}

	seen := map[string]int{}
	for _, d := range afterDirs {
		seen[d]++
	}
	for d, n := range seen {
		if n > 1 {
			t.Errorf("dir %q appears %d times — deduped by munged name, says the contract", d, n)
		}
	}

	// a rename never shrinks the set: every dir the scan returned before is
	// still returned after.
	for _, d := range beforeDirs {
		if seen[d] == 0 {
			t.Errorf("dir %q was dropped by the rename — token watermarks for it are unrecoverable", d)
		}
	}

	// the current name's four bases come first, in their existing order.
	want := []string{
		core.ProjectDirName(CellWorktree(after, repo)),
		core.ProjectDirName(filepath.Join(cellDir(after), "wt")),
		core.ProjectDirName(LegacyCellWorktree(after, repo)),
		core.ProjectDirName(filepath.Join(legacyCellDir(after), "wt")),
	}
	root := filepath.Join(util.Home(), ".claude", "projects")
	if len(afterDirs) < len(want) {
		t.Fatalf("dirs = %v, want at least the current name's four bases first", afterDirs)
	}
	for i, name := range want {
		if afterDirs[i] != filepath.Join(root, name) {
			t.Fatalf("dirs[%d] = %q, want %q — current-slug bases first", i, afterDirs[i], filepath.Join(root, name))
		}
	}
}

// TestEdgeBranchFamilyKeepsFormerArmsAndStaysCapped covers the PR-repair
// family's boundaries: a former name contributes its whole arm set, and the
// 64-run cap still applies (a hand-edited `run: 2000000000` must not build a
// two-billion-entry map).
func TestEdgeBranchFamilyKeepsFormerArmsAndStaysCapped(t *testing.T) {
	j := &Job{ID: 7, Slug: "edge-ren-fam", Run: 2, FormerSlugs: []string{"Old Fam"}}
	fam := branchFamily(j)
	for _, want := range []string{
		"carl/edge-ren-fam-7", "carl/edge-ren-fam-7-r2",
		"carl/edge-ren-fam", "carl/edge-ren-fam-r2",
		"carl/old-fam-7", "carl/old-fam-7-r2",
		"carl/old-fam", "carl/old-fam-r2",
	} {
		if !fam[want] {
			t.Errorf("branchFamily missing %q — a PR on it silently detaches from the card", want)
		}
	}

	capped := &Job{ID: 7, Slug: "edge-ren-fam", Run: 2000000000, FormerSlugs: []string{"old-fam"}}
	if got := len(branchFamily(capped)); got > 4*64 {
		t.Errorf("branchFamily size = %d with a typo'd run — the 64 cap must still bound it", got)
	}
	if !branchFamily(capped)["carl/old-fam-7"] {
		t.Error("the capped family still owes the former name's run-1 arm")
	}
}

// TestEdgeRenameJobReportsFlatCopyConflictsAsSkipped covers the post-tx file
// step's error path: "conflicts land in `Skipped`, never an error". A
// pre-existing target is the recoverable case (re-run after moving it), and the
// rename itself must still succeed — the store transaction has already
// committed by then.
func TestEdgeRenameJobReportsFlatCopyConflictsAsSkipped(t *testing.T) {
	st := useTestStore(t)
	pluto := t.TempDir()
	t.Setenv("KRAKEN_PLUTO_DIR", pluto)

	j := edgeRenJob(t, st, "edge-ren-files")
	oldPrefix := cellName(j)
	newPrefix := util.Slugify("edge-ren-files-new") + "-" + strconv.FormatInt(j.ID, 10)

	write := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pluto, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(oldPrefix + ".00-overview.md")
	write(oldPrefix + ".50-contract.md")
	// the conflict: the target name already exists.
	write(newPrefix + ".00-overview.md")

	res, err := RenameJob(j, "edge-ren-files-new")
	if err != nil {
		t.Fatalf("a flat-copy conflict must not fail the rename: %v", err)
	}
	if res == nil {
		t.Fatal("nil result on success")
	}
	if len(res.Skipped) != 1 {
		t.Fatalf("Skipped = %v, want exactly the one stranded source", res.Skipped)
	}
	// Round-3 contract amendment (lead's edit, authors gone): Skipped names
	// the stranded old-prefix SOURCE, not the occupied destination.
	if !strings.Contains(res.Skipped[0], oldPrefix+".00-overview.md") {
		t.Errorf("Skipped[0] = %q, want it to name the stranded old-prefix source", res.Skipped[0])
	}

	// the non-conflicting copy moved, and the conflicting source stayed put
	// rather than being clobbered or deleted.
	if _, err := os.Stat(filepath.Join(pluto, newPrefix+".50-contract.md")); err != nil {
		t.Errorf("non-conflicting flat copy did not move: %v", err)
	}
	if _, err := os.Stat(filepath.Join(pluto, oldPrefix+".50-contract.md")); err == nil {
		t.Error("the moved copy is still under the old prefix too")
	}
	if _, err := os.Stat(filepath.Join(pluto, oldPrefix+".00-overview.md")); err != nil {
		t.Errorf("the skipped source was not left in place: %v", err)
	}
}

// TestEdgeRenameJobNeverMovesCellStateOnDisk pins the loudest line in the
// contract's "Out of scope": the cell dir, worktree and uploads dir are NEVER
// moved, in any state. A live pane's cwd and every absolute path in zeus'
// context depend on it, and the resolvers above exist precisely so nothing has
// to move.
func TestEdgeRenameJobNeverMovesCellStateOnDisk(t *testing.T) {
	st := useTestStore(t)
	t.Setenv("KRAKEN_PLUTO_DIR", t.TempDir())

	j := edgeRenJob(t, st, "edge-ren-ondisk")
	oldCell := cellDir(j)
	oldUploads := filepath.Join(util.Home(), ".kraken", "uploads", cellName(j))
	for _, dir := range []string{filepath.Join(oldCell, "wt-monolith"), filepath.Join(oldCell, "artifacts"), oldUploads} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(oldCell, "artifacts", "00-overview.md")
	if err := os.WriteFile(marker, []byte("live"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := RenameJob(j, "edge-ren-ondisk-new"); err != nil {
		t.Fatalf("RenameJob: %v", err)
	}

	if _, err := os.Stat(marker); err != nil {
		t.Errorf("the cell dir moved or was rewritten: %v", err)
	}
	if _, err := os.Stat(oldUploads); err != nil {
		t.Errorf("the uploads dir moved: %v", err)
	}
	if _, err := os.Stat(cellDir(j)); err == nil && cellDir(j) != oldCell {
		t.Errorf("a NEW cell dir %q was created by the rename", cellDir(j))
	}
	// and the artifact resolution still finds the cell under its old name.
	if got := existingCellDir(j); got != oldCell {
		t.Errorf("existingCellDir = %q, want the surviving old-name dir %q", got, oldCell)
	}
	if got := CellArtifacts(j); !strings.HasPrefix(got, oldCell) {
		t.Errorf("CellArtifacts = %q, want it under %q — the mirror would go quiet otherwise", got, oldCell)
	}
}
