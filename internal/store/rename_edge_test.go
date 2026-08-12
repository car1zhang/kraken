package store

import (
	"errors"
	"testing"
)

// rename_edge_test.go — the EDGE dimension of the rename feature's store
// half (50-contract.md, slice A "Store layer"): every refusal shape, the
// all-or-nothing boundary of the one transaction, the reservation and its
// lapse, and the id-vs-slug keying under fixtures where slug fields COLLIDE
// across jobs.
//
// Written blind from the contract before the implementation existed, so every
// assertion below traces to a stated obligation and nothing here was read off
// the code. Helpers are `edgeRen`-prefixed for the reason every *_edge_test.go
// in this package prefixes: a logic author shares the package and a collision
// fails the whole test binary.

// edgeRenSeed inserts one active job and returns the row the store assigned
// (ID and Rev populated, as every RenameJob caller needs).
func edgeRenSeed(t *testing.T, s *Store, slug string) *Job {
	t.Helper()
	j := &Job{Slug: slug, Repo: "kraken", Base: "main"}
	if _, err := s.CreateJob(j); err != nil {
		t.Fatalf("CreateJob %q: %v", slug, err)
	}
	return j
}

// edgeRenRow reads a job row back by id, failing the test rather than
// returning an error — every caller below is asserting on the row's contents.
func edgeRenRow(t *testing.T, s *Store, id int64) *Job {
	t.Helper()
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", id, err)
	}
	return got
}

// edgeRenFeedbackSlugs maps feedback id -> job_slug across every row in the
// store (the unscoped sweep), which is what makes "only THIS job's rows moved"
// assertable when two jobs' rows carry the identical job_slug string.
func edgeRenFeedbackSlugs(t *testing.T, s *Store) map[int64]string {
	t.Helper()
	rows, err := s.ListFeedback(0)
	if err != nil {
		t.Fatalf("ListFeedback(0): %v", err)
	}
	out := map[int64]string{}
	for _, f := range rows {
		out[f.ID] = f.JobSlug
	}
	return out
}

// TestEdgeRenameJobRefusalShapes pins the three sentinel refusals the contract
// names for (*Store).RenameJob — "same disambiguation contract as
// UpdateJobFields (ErrNotFound gone/archived, ErrStaleRev live mismatch,
// ErrSlugTaken on the unique-index backstop)" — plus the archived-row
// immutability that makes the gone/archived arm one error rather than two.
// Each arm additionally asserts the row did NOT move: a refusal that renamed
// anyway is the failure this test exists for.
func TestEdgeRenameJobRefusalShapes(t *testing.T) {
	s := openTest(t)
	live := edgeRenSeed(t, s, "edge-ren-live")
	taken := edgeRenSeed(t, s, "edge-ren-taken")
	archived := edgeRenSeed(t, s, "edge-ren-archived")
	archivedRev := archived.Rev
	if _, err := s.ArchiveJob(archived.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	// (a) no such row at all.
	if _, err := s.RenameJob(live.ID+9999, 1, "edge-ren-nowhere"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	// (b) the row exists and is live, but the caller's rev is stale.
	if _, err := s.RenameJob(live.ID, live.Rev+1, "edge-ren-stale-target"); !errors.Is(err, ErrStaleRev) {
		t.Errorf("stale rev: err = %v, want ErrStaleRev", err)
	}
	if got := edgeRenRow(t, s, live.ID); got.Slug != "edge-ren-live" || got.Rev != live.Rev {
		t.Errorf("stale-rev refusal changed the row: slug %q rev %d", got.Slug, got.Rev)
	}

	// (c) an ARCHIVED row is immutable through this path, and reports as gone
	// rather than as a rev conflict — even when the rev handed in is the one
	// the row carried before archiving.
	for _, rev := range []int64{archivedRev, edgeRenRow(t, s, archived.ID).Rev} {
		if _, err := s.RenameJob(archived.ID, rev, "edge-ren-resurrected"); !errors.Is(err, ErrNotFound) {
			t.Errorf("archived row at rev %d: err = %v, want ErrNotFound", rev, err)
		}
	}
	if got := edgeRenRow(t, s, archived.ID); got.Slug != "edge-ren-archived" {
		t.Errorf("archived row's slug moved: %q", got.Slug)
	}

	// (d) the unique-index backstop: another ACTIVE job holds the target.
	if _, err := s.RenameJob(live.ID, live.Rev, taken.Slug); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("taken slug: err = %v, want ErrSlugTaken", err)
	}
	if got := edgeRenRow(t, s, live.ID); got.Slug != "edge-ren-live" || got.Rev != live.Rev {
		t.Errorf("ErrSlugTaken refusal changed the row: slug %q rev %d", got.Slug, got.Rev)
	}
	// ... and the refused target must not have acquired a former-slug row of
	// the job that failed to take it.
	if _, err := s.GetJobByFormerSlug("edge-ren-live"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused rename recorded a former slug: err = %v, want ErrNotFound", err)
	}
}

// TestEdgeRenameJobStoresWhatItIsGiven pins the layering the contract states
// outright: "`to` arrives already slugified (the jobs layer slugifies; the
// store stores what it is given)". A store that slugified defensively would
// make the jobs layer's gates unfalsifiable — the refusal for an
// unslugifiable target could never be observed at the boundary that owns it.
func TestEdgeRenameJobStoresWhatItIsGiven(t *testing.T) {
	s := openTest(t)
	j := edgeRenSeed(t, s, "edge-ren-raw")

	const raw = "Not Slugified At All"
	rev, err := s.RenameJob(j.ID, j.Rev, raw)
	if err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	if rev != j.Rev+1 {
		t.Errorf("rev = %d, want %d (a rename IS identity: it bumps)", rev, j.Rev+1)
	}
	if got := edgeRenRow(t, s, j.ID); got.Slug != raw {
		t.Errorf("stored slug = %q, want the exact string handed in (%q)", got.Slug, raw)
	}
}

// TestEdgeRenameJobIsAllOrNothing forces a failure inside the transaction
// AFTER its first statement and holds the contract's "ONE transaction,
// all-or-nothing": the former-slug upsert (statement 3) cannot run against a
// missing table, so the jobs row and the feedback re-key must be back where
// they started.
//
// (Authored with `DROP TABLE prereqs` as the forced failure while the tx
// still re-keyed peer prereqs; schema v20 dropped that table with the queued
// status, so the rebase that joined the two features re-targeted the failure
// at job_former_slugs — a name the amended contract now states, retiring the
// original silence objection. The table is PARKED under another name and
// restored before the assertions, so the post-refusal reads still work.
// Lead's edit, authors gone.)
func TestEdgeRenameJobIsAllOrNothing(t *testing.T) {
	s := openTest(t)
	j := edgeRenSeed(t, s, "edge-ren-atomic")
	fbID, err := s.AddFeedback(Feedback{JobID: j.ID, JobSlug: j.Slug, Note: "keep me"})
	if err != nil {
		t.Fatalf("AddFeedback: %v", err)
	}

	if _, err := s.db.Exec(`ALTER TABLE job_former_slugs RENAME TO edge_ren_parked`); err != nil {
		t.Fatalf("park job_former_slugs (the forced failure): %v", err)
	}

	if _, err := s.RenameJob(j.ID, j.Rev, "edge-ren-atomic-new"); err == nil {
		t.Fatal("RenameJob returned nil error with the former-slug upsert impossible — a swallowed statement failure is a half-applied rename")
	}

	if _, err := s.db.Exec(`ALTER TABLE edge_ren_parked RENAME TO job_former_slugs`); err != nil {
		t.Fatalf("restore job_former_slugs: %v", err)
	}

	got := edgeRenRow(t, s, j.ID)
	if got.Slug != "edge-ren-atomic" {
		t.Errorf("jobs.slug = %q, want the old slug — statement 1 was not rolled back", got.Slug)
	}
	if got.Rev != j.Rev {
		t.Errorf("rev = %d, want %d — the rollback must take the bump with it", got.Rev, j.Rev)
	}
	if slugs := edgeRenFeedbackSlugs(t, s); slugs[fbID] != "edge-ren-atomic" {
		t.Errorf("feedback.job_slug = %q, want the old slug", slugs[fbID])
	}
	if _, err := s.GetJobByFormerSlug("edge-ren-atomic"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a rolled-back rename left a former-slug row: err = %v, want ErrNotFound", err)
	}
}

// TestEdgeRenameJobRekeysByIDNotSlugAcrossGenerations is the colliding-slug
// fixture the contract's fixture rule demands: two jobs that historically held
// the SAME slug (one archived under it, one active on it), so a re-key written
// against job_slug instead of job_id moves the wrong generation's rows.
//
// (Its peer-prereq half was retired with the prereqs table at schema v20 —
// the rebase edit that removed the tx statement removed the assertions with
// it. Lead's edit, authors gone.)
func TestEdgeRenameJobRekeysByIDNotSlugAcrossGenerations(t *testing.T) {
	s := openTest(t)

	// generation 1: created on "edge-ren-shared", archived under it (the
	// FIRST archival of a slug keeps the bare name as archived_name, so its
	// feedback rows carry the identical job_slug string the live job's do).
	ghost := edgeRenSeed(t, s, "edge-ren-shared")
	ghostFB, err := s.AddFeedback(Feedback{JobID: ghost.ID, JobSlug: ghost.Slug, Note: "ghost note"})
	if err != nil {
		t.Fatalf("AddFeedback ghost: %v", err)
	}
	archivedName, err := s.ArchiveJob(ghost.ID)
	if err != nil {
		t.Fatalf("ArchiveJob ghost: %v", err)
	}
	if archivedName != "edge-ren-shared" {
		t.Fatalf("fixture needs the bare archived name to collide, got %q", archivedName)
	}

	// generation 2: a new active job takes the freed slug.
	live := edgeRenSeed(t, s, "edge-ren-shared")
	liveFB, err := s.AddFeedback(Feedback{JobID: live.ID, JobSlug: live.Slug, Note: "live note"})
	if err != nil {
		t.Fatalf("AddFeedback live: %v", err)
	}

	if _, err := s.RenameJob(live.ID, live.Rev, "edge-ren-moved"); err != nil {
		t.Fatalf("RenameJob: %v", err)
	}

	slugs := edgeRenFeedbackSlugs(t, s)
	if slugs[liveFB] != "edge-ren-moved" {
		t.Errorf("renamed job's feedback job_slug = %q, want edge-ren-moved", slugs[liveFB])
	}
	if slugs[ghostFB] != "edge-ren-shared" {
		t.Errorf("archived generation's feedback job_slug = %q, want edge-ren-shared — the re-key must match on job_id", slugs[ghostFB])
	}

	// the archived generation still owns its own slug string, untouched.
	if got := edgeRenRow(t, s, ghost.ID); got.Slug != "edge-ren-shared" {
		t.Errorf("archived generation's slug = %q, want edge-ren-shared", got.Slug)
	}
	// and the former slug resolves to the ACTIVE owner, not the archived one.
	owner, err := s.GetJobByFormerSlug("edge-ren-shared")
	if err != nil {
		t.Fatalf("GetJobByFormerSlug: %v", err)
	}
	if owner.ID != live.ID {
		t.Errorf("former slug resolved to job %d, want the renamed active job %d", owner.ID, live.ID)
	}
}

// TestEdgeRenameBackLeavesOneFormerRow holds the upsert clause and the
// current-slug removal clause together: "renaming A→B→A→B leaves one (id,"A")
// row, not an error", and "any former-slug row of id equal to `to` is
// REMOVED — a job's former-slug set never contains its current slug".
//
// Observed through GetJobByFormerSlug because the table's name is a contract
// silence: after the round trip "A" must resolve and "B" (the CURRENT slug)
// must not.
func TestEdgeRenameBackLeavesOneFormerRow(t *testing.T) {
	s := openTest(t)
	j := edgeRenSeed(t, s, "edge-ren-aaa")

	rev := j.Rev
	for i, to := range []string{"edge-ren-bbb", "edge-ren-aaa", "edge-ren-bbb"} {
		next, err := s.RenameJob(j.ID, rev, to)
		if err != nil {
			t.Fatalf("rename %d (-> %s): %v", i+1, to, err)
		}
		if next != rev+1 {
			t.Fatalf("rename %d: rev = %d, want %d", i+1, next, rev+1)
		}
		rev = next
	}

	if got := edgeRenRow(t, s, j.ID); got.Slug != "edge-ren-bbb" {
		t.Fatalf("slug after the round trip = %q, want edge-ren-bbb", got.Slug)
	}
	owner, err := s.GetJobByFormerSlug("edge-ren-aaa")
	if err != nil {
		t.Fatalf("GetJobByFormerSlug(edge-ren-aaa): %v — the re-taken former name must still resolve", err)
	}
	if owner.ID != j.ID {
		t.Errorf("former slug owner = %d, want %d", owner.ID, j.ID)
	}
	if _, err := s.GetJobByFormerSlug("edge-ren-bbb"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the CURRENT slug resolves as a former slug too: err = %v, want ErrNotFound", err)
	}
}

// TestEdgeGetJobByFormerSlugMissAndArchivedOwner pins the read's two negative
// obligations: a miss uses "the same not-found error shape GetJobBySlug uses",
// and "archived owners never match" — so archiving the owner makes its former
// slug stop resolving without deleting anything.
func TestEdgeGetJobByFormerSlugMissAndArchivedOwner(t *testing.T) {
	s := openTest(t)

	if _, err := s.GetJobByFormerSlug("edge-ren-never-existed"); !errors.Is(err, ErrNotFound) {
		t.Errorf("miss: err = %v, want ErrNotFound (GetJobBySlug's shape)", err)
	}
	if _, err := s.GetJobByFormerSlug(""); !errors.Is(err, ErrNotFound) {
		t.Errorf("empty slug: err = %v, want ErrNotFound", err)
	}

	j := edgeRenSeed(t, s, "edge-ren-owner-old")
	if _, err := s.RenameJob(j.ID, j.Rev, "edge-ren-owner-new"); err != nil {
		t.Fatalf("RenameJob: %v", err)
	}
	if _, err := s.GetJobByFormerSlug("edge-ren-owner-old"); err != nil {
		t.Fatalf("active owner's former slug must resolve: %v", err)
	}
	if _, err := s.ArchiveJob(j.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	if _, err := s.GetJobByFormerSlug("edge-ren-owner-old"); !errors.Is(err, ErrNotFound) {
		t.Errorf("archived owner still matches: err = %v, want ErrNotFound", err)
	}
}

// TestEdgeFormerSlugReservationAndItsLapse covers the reservation from both
// writers and both directions:
//
//   - CreateJob "refuses a slug equal to any ACTIVE job's former slug with
//     ErrSlugTaken";
//   - "A slug equal to an ARCHIVED job's former slug is NOT reserved
//     (reservation lapses with the job, like the slug itself)";
//   - RenameJob: "`to` equal to another ACTIVE job's former slug →
//     ErrSlugTaken (own former slugs are fine — renaming back is legal)".
func TestEdgeFormerSlugReservationAndItsLapse(t *testing.T) {
	s := openTest(t)
	holder := edgeRenSeed(t, s, "edge-ren-reserved")
	if _, err := s.RenameJob(holder.ID, holder.Rev, "edge-ren-holder-now"); err != nil {
		t.Fatalf("RenameJob holder: %v", err)
	}

	// CreateJob may not take the reserved former name...
	if _, err := s.CreateJob(&Job{Slug: "edge-ren-reserved"}); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("CreateJob on an active job's former slug: err = %v, want ErrSlugTaken", err)
	}
	// ... and neither may another job's rename.
	other := edgeRenSeed(t, s, "edge-ren-other")
	if _, err := s.RenameJob(other.ID, other.Rev, "edge-ren-reserved"); !errors.Is(err, ErrSlugTaken) {
		t.Errorf("RenameJob onto another active job's former slug: err = %v, want ErrSlugTaken", err)
	}
	if got := edgeRenRow(t, s, other.ID); got.Slug != "edge-ren-other" {
		t.Errorf("refused rename moved the row anyway: %q", got.Slug)
	}

	// the owner renaming BACK onto its own former slug is legal.
	cur := edgeRenRow(t, s, holder.ID)
	if _, err := s.RenameJob(cur.ID, cur.Rev, "edge-ren-reserved"); err != nil {
		t.Fatalf("renaming back onto an OWN former slug must be legal: %v", err)
	}

	// now archive the owner: the reservation lapses with the job. Its current
	// slug is "edge-ren-reserved" again, so the name freed by archiving and the
	// name freed by the lapsing reservation are the same string — which is
	// exactly the shape a reader keying on the wrong table would get wrong.
	if _, err := s.ArchiveJob(cur.ID); err != nil {
		t.Fatalf("ArchiveJob holder: %v", err)
	}
	if _, err := s.CreateJob(&Job{Slug: "edge-ren-holder-now"}); err != nil {
		t.Errorf("an ARCHIVED job's former slug must not be reserved: %v", err)
	}
}

// TestEdgeRenameJobNoOpRecordsNoFormerRow pins the no-op's negative half: "`to`
// equal to the current slug returns the CURRENT rev with no bump, no former
// row, no error." The rev half is the positive claim; the row half is the one
// that quietly poisons every later resolver — a job whose own current name is
// also recorded as a former name makes the alias fallback ambiguous forever.
func TestEdgeRenameJobNoOpRecordsNoFormerRow(t *testing.T) {
	s := openTest(t)
	j := edgeRenSeed(t, s, "edge-ren-noop")

	rev, err := s.RenameJob(j.ID, j.Rev, "edge-ren-noop")
	if err != nil {
		t.Fatalf("no-op RenameJob: %v", err)
	}
	if rev != j.Rev {
		t.Errorf("rev = %d, want the CURRENT rev %d (no bump)", rev, j.Rev)
	}
	if got := edgeRenRow(t, s, j.ID); got.Rev != j.Rev {
		t.Errorf("stored rev = %d, want %d", got.Rev, j.Rev)
	}
	if _, err := s.GetJobByFormerSlug("edge-ren-noop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("no-op recorded a former slug: err = %v, want ErrNotFound", err)
	}
}

// TestEdgeUnarchiveJobHonoursTheFormerSlugReservation pins the review-round
// contract amendment: unarchive was the one unguarded door back into the
// two-active-jobs-answer-one-name state, in its HARMFUL direction — the
// unarchived job's CURRENT slug equal to an ACTIVE job's former slug, where
// the renamed job's baked hook commands would cross-attribute onto the
// stranger. Lead-authored after the review round (the blind authors were gone
// by the time the amendment landed); edgeRen conventions kept.
func TestEdgeUnarchiveJobHonoursTheFormerSlugReservation(t *testing.T) {
	s := openTest(t)

	older := edgeRenSeed(t, s, "edge-ren-door")
	if _, err := s.ArchiveJob(older.ID); err != nil {
		t.Fatalf("archive older: %v", err)
	}
	renamer := edgeRenSeed(t, s, "edge-ren-door")
	if _, err := s.RenameJob(renamer.ID, renamer.Rev, "edge-ren-elsewhere"); err != nil {
		t.Fatalf("rename renamer: %v", err)
	}

	// The harmful direction: older's current slug is now an ACTIVE job's
	// former slug. Unarchive must refuse with the taken shape.
	if err := s.UnarchiveJob(older.ID); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("UnarchiveJob onto an active job's former slug: err = %v, want ErrSlugTaken", err)
	}

	// The reservation lapses with the reserving job, exactly as it does for
	// CreateJob: archive the renamer and the same unarchive succeeds.
	if _, err := s.ArchiveJob(renamer.ID); err != nil {
		t.Fatalf("archive renamer: %v", err)
	}
	if err := s.UnarchiveJob(older.ID); err != nil {
		t.Fatalf("UnarchiveJob after the reservation lapsed: %v", err)
	}
}
