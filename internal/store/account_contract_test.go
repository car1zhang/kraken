package store

// account_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice S-core's STORE half: schema v28's three account
// columns, the three store.Job fields every loader must scan, and
// SetJobAccount, the one writer that touches them.
//
// Written from the contract text alone. The working-tree versions of
// internal/store/{schema,jobs,rename}.go were never read; the harness
// (openTest, the Job/JobLoad shapes, RenameJob's rev-gated signature) is
// PRE-EXISTING package state at the base commit.
//
// Dimension: LOGIC — the stated invariants and happy paths. Hostile inputs,
// concurrent writers and the NULL-drift arms belong to athena-edge.
//
// Every package-level identifier declared here is prefixed `contractAcct`.

import "testing"

// contractAcctStamp is a fixed past millis value every fixture row is created
// with, so "updated_at was left alone" is an exact == rather than a race
// against the clock's millisecond resolution.
const contractAcctStamp int64 = 1_600_000_000_000

// contractAcctBindAt is the bind timestamp every fixture stamps. It is
// deliberately a different number from contractAcctStamp so a positional slip
// that put the bind millis into created_at (or vice versa) shows up as a
// mismatch rather than as an accidental pass.
const contractAcctBindAt int64 = 1_700_000_000_000

// contractAcctStore isolates HOME (the contract's environment obligation:
// no test may open the real kraken.db or read a real dotfile) and opens a
// throwaway store.
func contractAcctStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return openTest(t)
}

// contractAcctSeed creates one active job with the fixed past stamps and
// returns it with ID populated as the store assigned it.
func contractAcctSeed(t *testing.T, s *Store, slug string) *Job {
	t.Helper()
	j := &Job{
		Slug:      slug,
		Repo:      "kraken",
		Body:      "spec body",
		State:     "working",
		CreatedAt: contractAcctStamp,
		UpdatedAt: contractAcctStamp,
	}
	id, err := s.CreateJob(j)
	if err != nil {
		t.Fatalf("CreateJob %s: %v", slug, err)
	}
	j.ID = id
	return j
}

// contractAcctWant asserts one loaded row's whole binding triple at once. The
// three columns are one fact — a binding — so asserting them together is what
// catches a write that landed two of the three.
func contractAcctWant(t *testing.T, where string, j *Job, slug, email string, at int64) {
	t.Helper()
	if j == nil {
		t.Fatalf("%s: nil job", where)
	}
	if j.AccountSlug != slug || j.AccountEmail != email || j.AccountAt != at {
		t.Errorf("%s: account = (%q, %q, %d), want (%q, %q, %d)",
			where, j.AccountSlug, j.AccountEmail, j.AccountAt, slug, email, at)
	}
}

// contractAcctByID finds one row in a listing.
func contractAcctByID(t *testing.T, where string, rows []*Job, id int64) *Job {
	t.Helper()
	for _, r := range rows {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("%s: job %d not in the listing (%d rows)", where, id, len(rows))
	return nil
}

// ---------------------------------------------------------------------------
// schema v28
// ---------------------------------------------------------------------------

// TestContractAcctSchemaV28CarriesTheAccountColumns pins the migration's
// stated version. The contract names the STEP (28) and its constant
// (schemaV28) as one obligation — a migration appended at some other index,
// or one that edits an existing schemaV<N> in place, is the failure this
// catches — while schemaVersion only has to have reached it, since an
// unrelated feature merging alongside can legitimately push it higher.
func TestContractAcctSchemaV28CarriesTheAccountColumns(t *testing.T) {
	if migrations[28].sql != schemaV28 {
		t.Fatalf("migrations[28] is not schemaV28 — the three account columns must arrive as migration 28")
	}
	if schemaVersion < 28 {
		t.Fatalf("schemaVersion = %d, want at least 28", schemaVersion)
	}

	s := contractAcctStore(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d — a fresh db must migrate all the way to the account columns", version, schemaVersion)
	}
}

// TestContractAcctFreshJobReadsUnbound is the DEFAULTs half of the migration:
// "a job never bound reads ("", "", 0)". CreateJob omits the three columns
// entirely, which is correct only because the declared defaults spell exactly
// the unbound state — and unbound is the wire's and the binder's sole spelling
// of "this cell runs on the machine's main lane".
func TestContractAcctFreshJobReadsUnbound(t *testing.T) {
	s := contractAcctStore(t)
	j := contractAcctSeed(t, s, "fresh")

	got, err := s.GetJobBySlug("fresh")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	contractAcctWant(t, "GetJobBySlug on a never-bound job", got, "", "", 0)

	byID, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	contractAcctWant(t, "GetJobByID on a never-bound job", byID, "", "", 0)
}

// ---------------------------------------------------------------------------
// SetJobAccount — the round trip through every loader
// ---------------------------------------------------------------------------

// TestContractAcctSetJobAccountRoundTripsOnEveryLoader covers the two paths
// that fail independently: scanJob's POSITIONAL scan (three columns added to
// jobColumns but not to scanJob, or vice versa, silently shifts every later
// field) and the UPDATE itself. Every loader is exercised because the contract
// says "scanned by EVERY job loader" — a loader that forgets them is a job
// whose cell resolves the wrong account on exactly one code path.
func TestContractAcctSetJobAccountRoundTripsOnEveryLoader(t *testing.T) {
	s := contractAcctStore(t)
	j := contractAcctSeed(t, s, "bound")

	if err := s.SetJobAccount(j.ID, "slot-a", "one@example.com", contractAcctBindAt); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}

	bySlug, err := s.GetJobBySlug("bound")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	contractAcctWant(t, "GetJobBySlug", bySlug, "slot-a", "one@example.com", contractAcctBindAt)

	// the neighbours of the new scan positions, asserted explicitly: a
	// positional slip shows up here rather than as a mystery elsewhere
	if bySlug.Slug != "bound" || bySlug.Repo != "kraken" || bySlug.Body != "spec body" ||
		bySlug.State != "working" || bySlug.Err != "" || (bySlug.Load != JobLoad{}) {
		t.Fatalf("columns shifted around the account triple: %+v", bySlug)
	}

	byID, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	contractAcctWant(t, "GetJobByID", byID, "slot-a", "one@example.com", contractAcctBindAt)

	list, err := s.ListJobs()
	if err != nil {
		t.Fatalf("ListJobs: %v", err)
	}
	contractAcctWant(t, "ListJobs", contractAcctByID(t, "ListJobs", list, j.ID), "slot-a", "one@example.com", contractAcctBindAt)

	withKids, _, err := s.LoadJobsWithChildren()
	if err != nil {
		t.Fatalf("LoadJobsWithChildren: %v", err)
	}
	contractAcctWant(t, "LoadJobsWithChildren",
		contractAcctByID(t, "LoadJobsWithChildren", withKids, j.ID), "slot-a", "one@example.com", contractAcctBindAt)
}

// TestContractAcctFormerSlugLoaderAttachesTheBinding is the loader the
// contract calls out by name, because it is the one that gets forgotten: it
// lives in rename.go rather than jobs.go. A renamed job resolved by its old
// name must report the same binding as the same job resolved by its current
// one — otherwise a cell reached through a stale name spawns on the machine's
// main lane while the board shows it bound.
func TestContractAcctFormerSlugLoaderAttachesTheBinding(t *testing.T) {
	s := contractAcctStore(t)
	j := contractAcctSeed(t, s, "before")

	if err := s.SetJobAccount(j.ID, "slot-a", "one@example.com", contractAcctBindAt); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}
	cur, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	if _, err := s.RenameJob(j.ID, cur.Rev, "after"); err != nil {
		t.Fatalf("RenameJob: %v", err)
	}

	former, err := s.GetJobByFormerSlug("before")
	if err != nil {
		t.Fatalf("GetJobByFormerSlug: %v", err)
	}
	if former.ID != j.ID {
		t.Fatalf("GetJobByFormerSlug resolved job %d, want %d", former.ID, j.ID)
	}
	contractAcctWant(t, "GetJobByFormerSlug", former, "slot-a", "one@example.com", contractAcctBindAt)
}

// TestContractAcctArchivedLoaderKeepsTheBinding: the columns live on the job
// row for the job's whole lifetime and nothing clears them at teardown, so an
// archived row still scans them. (Whether the WIRE serves them is api's call
// and a different obligation — this is only the store's scan.)
func TestContractAcctArchivedLoaderKeepsTheBinding(t *testing.T) {
	s := contractAcctStore(t)
	j := contractAcctSeed(t, s, "shelved")
	if err := s.SetJobAccount(j.ID, "slot-a", "one@example.com", contractAcctBindAt); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}
	if _, err := s.ArchiveJob(j.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}

	archived, err := s.ListArchivedJobs()
	if err != nil {
		t.Fatalf("ListArchivedJobs: %v", err)
	}
	contractAcctWant(t, "ListArchivedJobs",
		contractAcctByID(t, "ListArchivedJobs", archived, j.ID), "slot-a", "one@example.com", contractAcctBindAt)
}

// ---------------------------------------------------------------------------
// SetJobAccount — overwrite semantics and the rev/updated_at rule
// ---------------------------------------------------------------------------

// TestContractAcctSetJobAccountOverwritesAllThree: "a rebind is an overwrite;
// there is no separate clear verb". Both halves are asserted — a second bind
// replaces every one of the three columns (a write that left the previous
// email behind would put a stale account on the card and, worse, on the
// rescue's own exclusion set), and the all-empty write is how a binding is
// cleared at all.
func TestContractAcctSetJobAccountOverwritesAllThree(t *testing.T) {
	s := contractAcctStore(t)
	j := contractAcctSeed(t, s, "rebound")

	if err := s.SetJobAccount(j.ID, "slot-a", "one@example.com", contractAcctBindAt); err != nil {
		t.Fatalf("SetJobAccount first bind: %v", err)
	}
	if err := s.SetJobAccount(j.ID, "slot-b", "two@example.com", contractAcctBindAt+5_000); err != nil {
		t.Fatalf("SetJobAccount rebind: %v", err)
	}
	got, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID after rebind: %v", err)
	}
	contractAcctWant(t, "after a rebind", got, "slot-b", "two@example.com", contractAcctBindAt+5_000)

	if err := s.SetJobAccount(j.ID, "", "", 0); err != nil {
		t.Fatalf("SetJobAccount clear: %v", err)
	}
	got, err = s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID after clear: %v", err)
	}
	contractAcctWant(t, "after the all-empty write", got, "", "", 0)
}

// TestContractAcctSetJobAccountLeavesRevAndUpdatedAt is the whole reason this
// write is not an UpdateJobFields call, and it is the SetJobLoad rule for the
// SetJobLoad reasons: updated_at is what every card renders as its AGE, and
// rev is the job editor's optimistic lock. A bind happens at every spawn and a
// rebind at every limit rescue, so bumping either would reset a card's age and
// 409 an open editor over a number that form neither shows nor sends.
func TestContractAcctSetJobAccountLeavesRevAndUpdatedAt(t *testing.T) {
	s := contractAcctStore(t)
	j := contractAcctSeed(t, s, "quiet")
	before, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}

	if err := s.SetJobAccount(j.ID, "slot-a", "one@example.com", contractAcctBindAt); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}
	after, err := s.GetJobByID(j.ID)
	if err != nil {
		t.Fatalf("GetJobByID after set: %v", err)
	}
	if after.Rev != before.Rev {
		t.Errorf("rev = %d, want it left at %d — a bind must not stale an open editor save", after.Rev, before.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("updated_at = %d, want it left at %d — it is the card's age, not the binder's last write", after.UpdatedAt, before.UpdatedAt)
	}
	contractAcctWant(t, "the binding itself", after, "slot-a", "one@example.com", contractAcctBindAt)
}

// TestContractAcctSetJobAccountUnknownIDErrors: "unknown id is an error, not a
// silent no-op". The binder stamps the row and then updates the in-memory job
// the spawn is about to read, so a zero-row update reported as success would
// launch a cell whose env names an account the board will never show.
func TestContractAcctSetJobAccountUnknownIDErrors(t *testing.T) {
	s := contractAcctStore(t)
	if err := s.SetJobAccount(4242, "slot-a", "one@example.com", contractAcctBindAt); err == nil {
		t.Fatal("SetJobAccount on an unknown id returned nil, want an error")
	}
}
