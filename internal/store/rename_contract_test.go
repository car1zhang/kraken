package store

// rename_contract_test.go — blind LOGIC tests for the store half of the
// rename feature (50-contract.md, Slice A "Store layer"). Authored from the
// contract alone, before any implementation existed: every assertion below
// is a restatement of an obligation row, never of code that was read.
//
// Every identifier declared here is prefixed `contractRename` so an edge
// author sharing this package cannot collide with it.

import (
	"errors"
	"testing"
	"time"
)

// contractRenameStamp is a fixed past millis value every fixture row is
// created with, so "updated_at was stamped" is a strict > comparison rather
// than a race against the clock's millisecond resolution.
const contractRenameStamp int64 = 1_600_000_000_000

// contractRenameSeed creates one active job with the fixed past stamps and
// returns it with ID/Rev populated as the store assigned them.
func contractRenameSeed(t *testing.T, s *Store, slug string) *Job {
	t.Helper()
	j := &Job{
		Slug:      slug,
		Repo:      "monolith",
		CreatedAt: contractRenameStamp,
		UpdatedAt: contractRenameStamp,
	}
	id, err := s.CreateJob(j)
	if err != nil {
		t.Fatalf("seed job %s: %v", slug, err)
	}
	j.ID = id
	return j
}

// contractRenameFeedbackSlugs returns the job_slug column of every feedback
// row owned by jobID, in ts order.
func contractRenameFeedbackSlugs(t *testing.T, s *Store, jobID int64) []string {
	t.Helper()
	rows, err := s.ListFeedback(jobID)
	if err != nil {
		t.Fatalf("list feedback for job %d: %v", jobID, err)
	}
	out := make([]string, 0, len(rows))
	for _, f := range rows {
		out = append(out, f.JobSlug)
	}
	return out
}

func contractRenameGetByID(t *testing.T, s *Store, id int64) *Job {
	t.Helper()
	j, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("get job %d: %v", id, err)
	}
	return j
}

// TestContractRenameSchemaIsV20 pins the migration's stated version: the
// former-slug table lands in schema v20 (contract, "schema migration (v20)"),
// and every store this package opens is migrated to exactly the version the
// binary knows.
func TestContractRenameSchemaIsV20(t *testing.T) {
	// Authored as an == 20 pin when the contract froze the table at v20; the
	// rebase that joined this feature with the queued-status retirement gave
	// v20 to that change and the former-slug table landed at v21. Re-pinned
	// the way recall_contract_test.go documents: the migration STEP is what
	// the obligation names, the version only needs to have reached it.
	// Lead's edit, authors gone.
	if migrations[21].sql != schemaV21 {
		t.Fatalf("migrations[21] is not schemaV21 — the former-slug table must arrive at v21")
	}
	if schemaVersion < 21 {
		t.Fatalf("schemaVersion = %d, want at least 21", schemaVersion)
	}
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("a fresh store migrated to user_version %d, want %d", version, schemaVersion)
	}
}

// TestContractRenameMigrationAppliesToAnExistingStore covers the second half
// of the migration obligation: a store created by an earlier open (already
// carrying rows) re-opens cleanly and the rename path works against it.
func TestContractRenameMigrationAppliesToAnExistingStore(t *testing.T) {
	s := openTest(t)
	j := contractRenameSeed(t, s, "alpha")
	if _, err := s.RenameJob(j.ID, j.Rev, "beta"); err != nil {
		t.Fatalf("rename on a migrated store: %v", err)
	}
	got, err := s.GetJobByFormerSlug("alpha")
	if err != nil {
		t.Fatalf("GetJobByFormerSlug after migration: %v", err)
	}
	if got.ID != j.ID {
		t.Fatalf("former slug resolved to job %d, want %d", got.ID, j.ID)
	}
}

// TestContractRenameJobCoreEffects is the store transaction's whole stated
// effect list in one pass: the slug changes with rev+1 and a fresh
// updated_at, this job's feedback re-keys, and the old slug becomes a former
// slug. (Authored as FiveEffects with a peer-prereq re-key; schema v20
// dropped the prereqs table with the queued status, so the rebase that joined
// the two features retired that obligation and its assertions — lead's edit,
// authors gone.)
//
// The fixture deliberately COLLIDES slugs across jobs — an archived job that
// historically held "alpha" carries feedback rows whose job_slug is the
// identical string — because the re-key is contractually matched on job_id,
// and a distinct-everywhere fixture cannot catch a slug-keyed mistake.
func TestContractRenameJobCoreEffects(t *testing.T) {
	s := openTest(t)

	// The historical generation: same slug, archived, its own feedback.
	ghost := contractRenameSeed(t, s, "alpha")
	if _, err := s.AddFeedback(Feedback{JobID: ghost.ID, JobSlug: "alpha", Artifact: "00-overview.md", Note: "ghost note"}); err != nil {
		t.Fatalf("seed ghost feedback: %v", err)
	}
	if _, err := s.ArchiveJob(ghost.ID); err != nil {
		t.Fatalf("archive ghost: %v", err)
	}

	// The live generation, reusing the freed slug.
	j := contractRenameSeed(t, s, "alpha")
	if _, err := s.AddFeedback(Feedback{JobID: j.ID, JobSlug: "alpha", Artifact: "50-contract.md", Note: "live note"}); err != nil {
		t.Fatalf("seed live feedback: %v", err)
	}

	newRev, err := s.RenameJob(j.ID, j.Rev, "omega")
	if err != nil {
		t.Fatalf("RenameJob: %v", err)
	}

	// (1) slug + rev + updated_at
	if newRev != j.Rev+1 {
		t.Errorf("RenameJob returned rev %d, want %d (rev+1)", newRev, j.Rev+1)
	}
	row := contractRenameGetByID(t, s, j.ID)
	if row.Slug != "omega" {
		t.Errorf("jobs.slug = %q, want omega", row.Slug)
	}
	if row.Rev != j.Rev+1 {
		t.Errorf("stored rev = %d, want %d", row.Rev, j.Rev+1)
	}
	if row.UpdatedAt <= contractRenameStamp {
		t.Errorf("updated_at = %d, want a fresh stamp past the seeded %d — a rename IS identity and bumps it",
			row.UpdatedAt, contractRenameStamp)
	}
	if _, err := s.GetJobBySlug("omega"); err != nil {
		t.Errorf("GetJobBySlug(omega) after rename: %v", err)
	}

	// (2) feedback re-key, scoped by job_id
	if got := contractRenameFeedbackSlugs(t, s, j.ID); len(got) != 1 || got[0] != "omega" {
		t.Errorf("renamed job's feedback job_slug = %v, want [omega]", got)
	}
	if got := contractRenameFeedbackSlugs(t, s, ghost.ID); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("the archived same-slug job's feedback moved: %v, want [alpha] — the re-key must match on job_id", got)
	}

	// (3) the old slug is now a former slug of this job
	owner, err := s.GetJobByFormerSlug("alpha")
	if err != nil {
		t.Fatalf("GetJobByFormerSlug(alpha): %v", err)
	}
	if owner.ID != j.ID {
		t.Errorf("former slug alpha resolves to job %d, want the renamed job %d", owner.ID, j.ID)
	}
}

// TestContractRenameJobIsAllOrNothing pins the atomicity claim from the
// observable side: a refused rename leaves every one of the effects
// un-applied. The refusal is driven through the rev gate, the one failure
// the contract names on statement (1).
func TestContractRenameJobIsAllOrNothing(t *testing.T) {
	s := openTest(t)
	j := contractRenameSeed(t, s, "alpha")
	if _, err := s.AddFeedback(Feedback{JobID: j.ID, JobSlug: "alpha", Artifact: "a.md", Note: "n"}); err != nil {
		t.Fatalf("seed feedback: %v", err)
	}

	if _, err := s.RenameJob(j.ID, j.Rev+7, "omega"); !errors.Is(err, ErrStaleRev) {
		t.Fatalf("stale-rev rename: err = %v, want ErrStaleRev", err)
	}

	row := contractRenameGetByID(t, s, j.ID)
	if row.Slug != "alpha" {
		t.Errorf("slug = %q after a refused rename, want alpha", row.Slug)
	}
	if row.Rev != j.Rev {
		t.Errorf("rev = %d after a refused rename, want the unchanged %d", row.Rev, j.Rev)
	}
	if row.UpdatedAt != contractRenameStamp {
		t.Errorf("updated_at moved on a refused rename: %d, want %d", row.UpdatedAt, contractRenameStamp)
	}
	if got := contractRenameFeedbackSlugs(t, s, j.ID); len(got) != 1 || got[0] != "alpha" {
		t.Errorf("feedback re-keyed on a refused rename: %v, want [alpha]", got)
	}
	if _, err := s.GetJobByFormerSlug("alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused rename recorded a former slug: err = %v, want ErrNotFound", err)
	}
}

// TestContractRenameJobNoOp: renaming to the CURRENT slug returns the current
// rev, bumps nothing, records no former slug, and is not an error.
func TestContractRenameJobNoOp(t *testing.T) {
	s := openTest(t)
	j := contractRenameSeed(t, s, "alpha")

	rev, err := s.RenameJob(j.ID, j.Rev, "alpha")
	if err != nil {
		t.Fatalf("no-op rename: %v", err)
	}
	if rev != j.Rev {
		t.Errorf("no-op returned rev %d, want the current %d", rev, j.Rev)
	}
	row := contractRenameGetByID(t, s, j.ID)
	if row.Rev != j.Rev {
		t.Errorf("no-op bumped stored rev to %d, want %d", row.Rev, j.Rev)
	}
	if row.UpdatedAt != contractRenameStamp {
		t.Errorf("no-op stamped updated_at: %d, want the unchanged %d", row.UpdatedAt, contractRenameStamp)
	}
	if row.Slug != "alpha" {
		t.Errorf("no-op changed the slug to %q", row.Slug)
	}
	if _, err := s.GetJobByFormerSlug("alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no-op recorded a former slug: err = %v, want ErrNotFound", err)
	}
}

// TestContractRenameJobDisambiguatesGoneAndArchived mirrors UpdateJobFields'
// stated contract: a row that is gone or archived is ErrNotFound, a live row
// at a different rev is ErrStaleRev.
func TestContractRenameJobDisambiguatesGoneAndArchived(t *testing.T) {
	s := openTest(t)

	if _, err := s.RenameJob(4242, 1, "omega"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename of a nonexistent job: err = %v, want ErrNotFound", err)
	}

	j := contractRenameSeed(t, s, "alpha")
	if _, err := s.ArchiveJob(j.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	row := contractRenameGetByID(t, s, j.ID)
	if _, err := s.RenameJob(j.ID, row.Rev, "omega"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename of an archived job: err = %v, want ErrNotFound", err)
	}
	if after := contractRenameGetByID(t, s, j.ID); after.Slug != "alpha" {
		t.Errorf("an archived job's slug changed to %q", after.Slug)
	}
}

// TestContractRenameJobRefusesATakenActiveSlug pins the unique-index backstop
// mapping: another ACTIVE job already holding the target slug is ErrSlugTaken.
func TestContractRenameJobRefusesATakenActiveSlug(t *testing.T) {
	s := openTest(t)
	j := contractRenameSeed(t, s, "alpha")
	contractRenameSeed(t, s, "omega")

	if _, err := s.RenameJob(j.ID, j.Rev, "omega"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("rename onto a taken active slug: err = %v, want ErrSlugTaken", err)
	}
	if row := contractRenameGetByID(t, s, j.ID); row.Slug != "alpha" || row.Rev != j.Rev {
		t.Errorf("refused rename still moved the row: slug %q rev %d", row.Slug, row.Rev)
	}
}

// TestContractRenameJobFormerSetNeverHoldsTheCurrentSlug walks the contract's
// own A -> B -> A -> B example: the (id, "A") row upserts rather than
// erroring, and the job's former-slug set never contains the slug it is
// currently called by.
func TestContractRenameJobFormerSetNeverHoldsTheCurrentSlug(t *testing.T) {
	s := openTest(t)
	j := contractRenameSeed(t, s, "a")

	rev := j.Rev
	for _, to := range []string{"b", "a", "b"} {
		next, err := s.RenameJob(j.ID, rev, to)
		if err != nil {
			t.Fatalf("rename to %s: %v", to, err)
		}
		rev = next
	}

	if row := contractRenameGetByID(t, s, j.ID); row.Slug != "b" {
		t.Fatalf("after a->b->a->b the slug is %q, want b", row.Slug)
	}
	owner, err := s.GetJobByFormerSlug("a")
	if err != nil {
		t.Fatalf(`GetJobByFormerSlug("a") after a->b->a->b: %v`, err)
	}
	if owner.ID != j.ID {
		t.Errorf(`former slug "a" resolves to job %d, want %d`, owner.ID, j.ID)
	}
	if _, err := s.GetJobByFormerSlug("b"); !errors.Is(err, ErrNotFound) {
		t.Errorf(`former slug "b" is the job's CURRENT slug: err = %v, want ErrNotFound`, err)
	}
}

// TestContractGetJobByFormerSlugMissesAndArchivedOwners: a slug nobody ever
// held is ErrNotFound (GetJobBySlug's own miss shape), and an ARCHIVED owner
// never matches.
func TestContractGetJobByFormerSlugMissesAndArchivedOwners(t *testing.T) {
	s := openTest(t)

	if _, err := s.GetJobByFormerSlug("never-held"); !errors.Is(err, ErrNotFound) {
		t.Errorf("miss: err = %v, want ErrNotFound (GetJobBySlug's shape)", err)
	}

	j := contractRenameSeed(t, s, "alpha")
	if _, err := s.RenameJob(j.ID, j.Rev, "beta"); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := s.ArchiveJob(j.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := s.GetJobByFormerSlug("alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("an archived owner matched: err = %v, want ErrNotFound", err)
	}
}

// TestContractGetJobByFormerSlugMostRecentWins builds the pre-reservation
// shape the contract names: two ACTIVE jobs that both once held "shared".
// The archive/unarchive detour is the only way to reach it now that the
// reservation is in place, and it is also the fixture that collides slugs
// across jobs.
func TestContractGetJobByFormerSlugMostRecentWins(t *testing.T) {
	s := openTest(t)

	first := contractRenameSeed(t, s, "shared")
	if _, err := s.RenameJob(first.ID, first.Rev, "one"); err != nil {
		t.Fatalf("rename first: %v", err)
	}
	if _, err := s.ArchiveJob(first.ID); err != nil {
		t.Fatalf("archive first: %v", err)
	}

	// Distinct renamed_at values: the tie-break is "most recent wins", so
	// the two renames must not land in the same millisecond.
	time.Sleep(5 * time.Millisecond)

	second := contractRenameSeed(t, s, "shared")
	if _, err := s.RenameJob(second.ID, second.Rev, "two"); err != nil {
		t.Fatalf("rename second: %v", err)
	}
	if err := s.UnarchiveJob(first.ID); err != nil {
		t.Fatalf("unarchive first: %v", err)
	}

	owner, err := s.GetJobByFormerSlug("shared")
	if err != nil {
		t.Fatalf("GetJobByFormerSlug(shared): %v", err)
	}
	if owner.ID != second.ID {
		t.Errorf("former slug shared resolved to job %d, want the most recently renamed %d", owner.ID, second.ID)
	}
}

// TestContractCreateJobReservesActiveFormerSlugs: while a job is active its
// former slugs are reserved against job creation, and the reservation lapses
// when that job is archived.
func TestContractCreateJobReservesActiveFormerSlugs(t *testing.T) {
	s := openTest(t)
	j := contractRenameSeed(t, s, "alpha")
	if _, err := s.RenameJob(j.ID, j.Rev, "beta"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	if _, err := s.CreateJob(&Job{Slug: "alpha", Repo: "monolith"}); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("CreateJob on an active job's former slug: err = %v, want ErrSlugTaken", err)
	}

	if _, err := s.ArchiveJob(j.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if _, err := s.CreateJob(&Job{Slug: "alpha", Repo: "monolith"}); err != nil {
		t.Fatalf("CreateJob after the owner was archived: %v — the reservation lapses with the job", err)
	}
}

// TestContractRenameJobReservesOtherActiveFormerSlugs: the same reservation on
// the rename path, plus its stated carve-out — a job's OWN former slug is
// always available to it, since renaming back is legal.
func TestContractRenameJobReservesOtherActiveFormerSlugs(t *testing.T) {
	s := openTest(t)

	owner := contractRenameSeed(t, s, "alpha")
	ownerRev, err := owner.contractRenameTo(s, "beta")
	if err != nil {
		t.Fatalf("seed rename: %v", err)
	}

	other := contractRenameSeed(t, s, "gamma")
	if _, err := s.RenameJob(other.ID, other.Rev, "alpha"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("rename onto another active job's former slug: err = %v, want ErrSlugTaken", err)
	}
	if row := contractRenameGetByID(t, s, other.ID); row.Slug != "gamma" {
		t.Errorf("refused rename moved the row to %q", row.Slug)
	}

	// The owner may take its own former slug back.
	if _, err := s.RenameJob(owner.ID, ownerRev, "alpha"); err != nil {
		t.Fatalf("renaming back onto an own former slug: %v — own former slugs are not reserved against the owner", err)
	}
	if row := contractRenameGetByID(t, s, owner.ID); row.Slug != "alpha" {
		t.Errorf("slug = %q after renaming back, want alpha", row.Slug)
	}
}

// contractRenameTo is a tiny method-shaped helper so a seeded fixture can be
// renamed without threading rev by hand at every call site.
func (j *Job) contractRenameTo(s *Store, to string) (int64, error) {
	rev, err := s.RenameJob(j.ID, j.Rev, to)
	if err != nil {
		return 0, err
	}
	j.Rev = rev
	j.Slug = to
	return rev, nil
}
