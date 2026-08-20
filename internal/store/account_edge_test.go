package store

import (
	"database/sql"
	"testing"
)

// account_edge_test.go — the EDGE dimension of 50-contract.md's Slice S-core
// STORE obligations for per-cell account binding: schema v28's three columns
// and their NOT NULL / DEFAULT boundary, the "never bound reads ("", "", 0)"
// state, SetJobAccount's refusal on an unknown id, the rebind-is-an-overwrite
// rule (the contract states there is no separate clear verb, so writing the
// empty triple IS the clear), the rev/updated_at prohibition, and the loaders
// that carry the triple — including the two that get forgotten:
// GetJobByFormerSlug in rename.go and the archived list.
//
// Written blind from the frozen contract. internal/store/{schema,jobs,rename}.go
// were never read in their working-tree state; openTest, CreateJob,
// CreateArchivedJob, ArchiveJob and RenameJob are pre-existing package state
// read at the base commit.
//
// The sibling blind author (athena-logic) owns the stated invariants and the
// happy round trip. What lives here is the other half: the boundaries, the
// refusal, and the absences.
//
// Package-level identifiers are prefixed `edgeAcct` — this package's test
// namespace already carries `edgeGoal*`, `edgeRen*` and friends from earlier
// rounds, and a second blind author is writing beside this file right now.

// edgeAcctSeed inserts one active job and returns the row the store assigned
// (ID and Rev populated, which every SetJobAccount/RenameJob caller needs).
func edgeAcctSeed(t *testing.T, s *Store, slug string) *Job {
	t.Helper()
	j := &Job{Slug: slug, Repo: "kraken", Base: "main", Body: "b", State: "working"}
	if _, err := s.CreateJob(j); err != nil {
		t.Fatalf("CreateJob %q: %v", slug, err)
	}
	return j
}

// edgeAcctRow reads a job back by id, failing rather than returning an error —
// every caller below is asserting on the row's contents.
func edgeAcctRow(t *testing.T, s *Store, id int64) *Job {
	t.Helper()
	got, err := s.GetJobByID(id)
	if err != nil {
		t.Fatalf("GetJobByID %d: %v", id, err)
	}
	return got
}

// edgeAcctTriple renders a row's binding as the one three-valued fact the
// contract names, so a failure message reads as a triple rather than as three
// separate field mismatches.
func edgeAcctTriple(j *Job) (string, string, int64) {
	return j.AccountSlug, j.AccountEmail, j.AccountAt
}

// edgeAcctColumn is one row of `PRAGMA table_info(jobs)`.
type edgeAcctColumn struct {
	name    string
	ctype   string
	notNull int
	dflt    sql.NullString
}

// edgeAcctColumns reads the jobs table's declared shape. The DDL is the whole
// subject of the schema obligation — a column added as nullable, or with no
// DEFAULT, reads identically to a correct one on every fresh row and only
// diverges on the rows that already existed when the migration ran, which is
// every row on the real machine and none in a test that only inserts.
func edgeAcctColumns(t *testing.T, s *Store) map[string]edgeAcctColumn {
	t.Helper()
	rows, err := s.db.Query(`PRAGMA table_info(jobs)`)
	if err != nil {
		t.Fatalf("PRAGMA table_info(jobs): %v", err)
	}
	defer rows.Close()
	out := map[string]edgeAcctColumn{}
	for rows.Next() {
		var cid, pk int
		var c edgeAcctColumn
		if err := rows.Scan(&cid, &c.name, &c.ctype, &c.notNull, &c.dflt, &pk); err != nil {
			t.Fatalf("scan table_info: %v", err)
		}
		out[c.name] = c
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return out
}

// ---------------------------------------------------------------------------
// schema v28
// ---------------------------------------------------------------------------

// TestEdgeAccountSchemaVersionIsAtLeastTwentyEight pins both halves of the
// version obligation: the constant, and what a freshly-migrated database
// actually stamps into user_version. They can disagree — a migration appended
// without bumping the constant leaves every real database one version short
// and silently never runs — so both are asserted.
//
// A FLOOR rather than the exact pin this started as. The account binding is
// migration 28 and always will be, but the constant moves with every later
// feature (v29's mental-model cutover was the first), and an exact assertion
// here says nothing about the binding while failing on every unrelated change.
// The migration NUMBER is what this test is about; jobgoals_edge_test.go's
// deliberately-exact pin is the one that exists to stop a new migration
// landing without a downgradeFreshDB reversal.
func TestEdgeAccountSchemaVersionIsAtLeastTwentyEight(t *testing.T) {
	if schemaVersion < 28 {
		t.Errorf("schemaVersion = %d, want >= 28 — the account binding is migration 28", schemaVersion)
	}
	if _, ok := migrations[28]; !ok {
		t.Errorf("no migration registered at 28, where the account binding lives")
	}
	s := openTest(t)
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	if version != schemaVersion {
		t.Errorf("a freshly migrated database stamps user_version = %d, want %d", version, schemaVersion)
	}
}

// TestEdgeAccountColumnsAreNotNullWithDefaults is the DDL boundary. The
// contract spells all three out — `account_slug TEXT NOT NULL DEFAULT ”`,
// `account_email TEXT NOT NULL DEFAULT ”`, `account_at INTEGER NOT NULL
// DEFAULT 0` — and each half is load-bearing for a different reader: NOT NULL
// keeps a scan from having to defend against NULL (jobColumns COALESCEs, so a
// nullable column would degrade silently rather than fail), and the DEFAULT is
// what lands every PRE-EXISTING row on the "never bound" state rather than on
// NULL the moment migration 28 runs.
func TestEdgeAccountColumnsAreNotNullWithDefaults(t *testing.T) {
	s := openTest(t)
	cols := edgeAcctColumns(t, s)
	for _, c := range []struct {
		name  string
		ctype string
	}{
		{"account_slug", "TEXT"},
		{"account_email", "TEXT"},
		{"account_at", "INTEGER"},
	} {
		got, ok := cols[c.name]
		if !ok {
			t.Errorf("jobs has no %s column — migration 28 did not append it", c.name)
			continue
		}
		if got.ctype != c.ctype {
			t.Errorf("jobs.%s is declared %s, want %s", c.name, got.ctype, c.ctype)
		}
		if got.notNull != 1 {
			t.Errorf("jobs.%s is nullable, want NOT NULL — a NULL here degrades silently through jobColumns' COALESCE instead of failing", c.name)
		}
		if !got.dflt.Valid {
			t.Errorf("jobs.%s has no DEFAULT — every row that existed before migration 28 would land on NULL rather than on the unbound state", c.name)
		}
	}
}

// ---------------------------------------------------------------------------
// the unbound state
// ---------------------------------------------------------------------------

// TestEdgeAccountNeverBoundJobReadsTheEmptyTriple pins the one spelling of
// "unbound", through every loader a card can arrive by. The INSERT statements
// omit all three columns, which is correct only because the DEFAULTs spell
// exactly this state — the same argument
// TestCreateJobDefaultsLoadToNeverEstimated makes for the load columns.
func TestEdgeAccountNeverBoundJobReadsTheEmptyTriple(t *testing.T) {
	s := openTest(t)
	j := edgeAcctSeed(t, s, "unbound")

	bySlug, err := s.GetJobBySlug("unbound")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	byID := edgeAcctRow(t, s, j.ID)
	listed, err := s.ListJobs()
	if err != nil || len(listed) != 1 {
		t.Fatalf("ListJobs = %d rows, err %v", len(listed), err)
	}
	for name, got := range map[string]*Job{"GetJobBySlug": bySlug, "GetJobByID": byID, "ListJobs": listed[0]} {
		slug, email, at := edgeAcctTriple(got)
		if slug != "" || email != "" || at != 0 {
			t.Errorf("%s: a never-bound job reads (%q, %q, %d), want the empty triple", name, slug, email, at)
		}
	}

	// the archive INSERT is a second statement that omits the same three
	// columns, and it is the one that gets forgotten
	if _, err := s.CreateArchivedJob(&Job{Slug: "shelved", ArchivedName: "shelved-1", State: "done"}); err != nil {
		t.Fatalf("CreateArchivedJob: %v", err)
	}
	archived, err := s.ListArchivedJobs()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchivedJobs = %d rows, err %v", len(archived), err)
	}
	if slug, email, at := edgeAcctTriple(archived[0]); slug != "" || email != "" || at != 0 {
		t.Errorf("CreateArchivedJob binding = (%q, %q, %d), want the empty triple", slug, email, at)
	}
}

// ---------------------------------------------------------------------------
// SetJobAccount: the refusal
// ---------------------------------------------------------------------------

// TestEdgeSetJobAccountUnknownIDErrors: the contract says an unknown id is an
// error, not a silent no-op. Both callers (the binder at spawn, the rescue's
// rebind) go on to spawn a claude that reads the binding they believe they
// just wrote — a zero-row UPDATE reported as success would put that claude on
// a different account from the one the board, the rescue and the next spawn
// all think it is on.
func TestEdgeSetJobAccountUnknownIDErrors(t *testing.T) {
	s := openTest(t)
	live := edgeAcctSeed(t, s, "present")

	if err := s.SetJobAccount(live.ID+9999, "slot-a", "a@example.com", 1); err == nil {
		t.Fatal("SetJobAccount on an unknown id returned nil, want an error")
	}
	// and it wrote nothing anywhere: the neighbouring row is untouched
	if slug, email, at := edgeAcctTriple(edgeAcctRow(t, s, live.ID)); slug != "" || email != "" || at != 0 {
		t.Errorf("a refused write reached another row: (%q, %q, %d)", slug, email, at)
	}
}

// ---------------------------------------------------------------------------
// SetJobAccount: rebind is an overwrite
// ---------------------------------------------------------------------------

// TestEdgeSetJobAccountRebindOverwritesAllThree. The contract states the write
// overwrites all three columns and that there is no separate clear verb, which
// makes two things assertable at once: a second write must not merge with the
// first (a rebind that kept the previous email would label every rescued cell
// with the account it was moved OFF), and the empty triple is a legal value
// rather than a rejected one — it is the only spelling of "put this job back
// on the main lane" the API offers.
func TestEdgeSetJobAccountRebindOverwritesAllThree(t *testing.T) {
	s := openTest(t)
	j := edgeAcctSeed(t, s, "rebound")

	if err := s.SetJobAccount(j.ID, "slot-a", "a@example.com", 1_760_000_000_000); err != nil {
		t.Fatalf("SetJobAccount (bind): %v", err)
	}
	if slug, email, at := edgeAcctTriple(edgeAcctRow(t, s, j.ID)); slug != "slot-a" || email != "a@example.com" || at != 1_760_000_000_000 {
		t.Fatalf("after bind = (%q, %q, %d)", slug, email, at)
	}

	if err := s.SetJobAccount(j.ID, "slot-b", "b@example.com", 1_760_000_009_999); err != nil {
		t.Fatalf("SetJobAccount (rebind): %v", err)
	}
	if slug, email, at := edgeAcctTriple(edgeAcctRow(t, s, j.ID)); slug != "slot-b" || email != "b@example.com" || at != 1_760_000_009_999 {
		t.Errorf("after rebind = (%q, %q, %d), want every column replaced — a rebind is an overwrite, not a merge", slug, email, at)
	}

	// the empty triple is the clear, and it is an ordinary write
	if err := s.SetJobAccount(j.ID, "", "", 0); err != nil {
		t.Fatalf("SetJobAccount (clear): %v", err)
	}
	if slug, email, at := edgeAcctTriple(edgeAcctRow(t, s, j.ID)); slug != "" || email != "" || at != 0 {
		t.Errorf("after clear = (%q, %q, %d), want the empty triple — there is no separate clear verb, so this write IS the clear", slug, email, at)
	}
}

// TestEdgeSetJobAccountLeavesRevAndUpdatedAtAndItsNeighbours carries three
// assertions that fail independently.
//
// rev and updated_at: the same rule and the same reason as SetJobLoad. A bind
// happens at every spawn and a rebind at every rescue, updated_at is what each
// card renders as its AGE, and rev is the job editor's optimistic lock — so
// stamping either would reset a live card's age on every respawn and 409 an
// open editor save over a field that form neither shows nor sends.
//
// The neighbours: three columns appended to jobColumns but not to scanJob (or
// the reverse) shift every later field positionally, and the symptom is a
// mystery somewhere else entirely. Asserting the surrounding values here is
// what makes that show up as this test.
func TestEdgeSetJobAccountLeavesRevAndUpdatedAtAndItsNeighbours(t *testing.T) {
	s := openTest(t)
	j := &Job{Slug: "quiet", Repo: "kraken", Body: "b", State: "blocked", Err: "", UpdatedAt: 1_700_000_000_000}
	if _, err := s.CreateJob(j); err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	if err := s.SetJobLoad(j.ID, JobLoad{Unblock: 2, Scope: 8, Note: "n", At: 7}); err != nil {
		t.Fatalf("SetJobLoad: %v", err)
	}
	before := edgeAcctRow(t, s, j.ID)

	if err := s.SetJobAccount(j.ID, "slot-a", "a@example.com", 1_760_000_000_000); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}
	after := edgeAcctRow(t, s, j.ID)

	if after.Rev != before.Rev {
		t.Errorf("rev = %d, want it left at %d — a bind must not stale an open editor save", after.Rev, before.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("updated_at = %d, want it left at %d — it is the card's age, not the last spawn", after.UpdatedAt, before.UpdatedAt)
	}
	if after.Slug != before.Slug || after.Repo != before.Repo || after.Body != before.Body ||
		after.State != before.State || after.Err != before.Err || after.Load != before.Load ||
		after.CreatedAt != before.CreatedAt || after.Run != before.Run {
		t.Errorf("columns shifted around the account triple:\n before %+v\n after  %+v", before, after)
	}
	if after.AccountSlug != "slot-a" || after.AccountEmail != "a@example.com" || after.AccountAt != 1_760_000_000_000 {
		t.Errorf("the binding did not land: %+v", after)
	}
}

// TestEdgeSetJobAccountTouchesOnlyItsOwnRow: two jobs, one bind. The UPDATE is
// keyed on the job's id, and a missing (or mistyped) WHERE clause is invisible
// on a single-row fixture — it would put every job on one account and the
// binder's whole purpose, spreading cells across slots, would silently invert.
func TestEdgeSetJobAccountTouchesOnlyItsOwnRow(t *testing.T) {
	s := openTest(t)
	mine := edgeAcctSeed(t, s, "mine")
	theirs := edgeAcctSeed(t, s, "theirs")

	if err := s.SetJobAccount(mine.ID, "slot-a", "a@example.com", 5); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}
	if slug, email, at := edgeAcctTriple(edgeAcctRow(t, s, theirs.ID)); slug != "" || email != "" || at != 0 {
		t.Errorf("the neighbouring job reads (%q, %q, %d), want the empty triple — the write is keyed on one id", slug, email, at)
	}
}

// ---------------------------------------------------------------------------
// the loaders that get forgotten
// ---------------------------------------------------------------------------

// TestEdgeAccountSurvivesRenameAndArchive covers the two loaders a new job
// column is most often missed by, and both are silent when missed.
//
// GetJobByFormerSlug lives in rename.go rather than jobs.go — it is the one
// scanJob caller outside the file the column was added to, and the goal-links
// round shipped exactly this defect (see TestRegressionFormerSlugLoadCarriesGoalLinks):
// a job resolved by a name it had been renamed AWAY from came back with the
// new field empty while the same job resolved by its current slug came back
// with it. For a binding that reads as "this cell is on the main lane", which
// is what the env construction and the rescue's exclusion both act on.
//
// The archive is the other: the contract has the WIRE withhold the key for an
// archived job, which is a decision the api layer can only take if the row
// still carries it.
func TestEdgeAccountSurvivesRenameAndArchive(t *testing.T) {
	s := openTest(t)
	j := edgeAcctSeed(t, s, "old-name")
	if err := s.SetJobAccount(j.ID, "slot-a", "a@example.com", 1_760_000_000_000); err != nil {
		t.Fatalf("SetJobAccount: %v", err)
	}
	// rev is still the one CreateJob assigned precisely because the bind did
	// not bump it; a rename refused here as ErrStaleRev is that prohibition
	// failing, not this fixture
	if _, err := s.RenameJob(j.ID, j.Rev, "new-name"); err != nil {
		t.Fatalf("RenameJob (rev %d): %v", j.Rev, err)
	}

	byFormer, err := s.GetJobByFormerSlug("old-name")
	if err != nil {
		t.Fatalf("GetJobByFormerSlug: %v", err)
	}
	if slug, email, at := edgeAcctTriple(byFormer); slug != "slot-a" || email != "a@example.com" || at != 1_760_000_000_000 {
		t.Errorf("GetJobByFormerSlug binding = (%q, %q, %d), want the stamped triple — the former-slug load is the fourth scanJob caller and the one outside jobs.go",
			slug, email, at)
	}
	byCurrent, err := s.GetJobBySlug("new-name")
	if err != nil {
		t.Fatalf("GetJobBySlug: %v", err)
	}
	if byFormer.AccountSlug != byCurrent.AccountSlug || byFormer.AccountEmail != byCurrent.AccountEmail {
		t.Errorf("whichever name resolves the job, it must see the same binding: former %+v, current %+v",
			byFormer, byCurrent)
	}

	if _, err := s.ArchiveJob(j.ID); err != nil {
		t.Fatalf("ArchiveJob: %v", err)
	}
	archived, err := s.ListArchivedJobs()
	if err != nil || len(archived) != 1 {
		t.Fatalf("ListArchivedJobs = %d rows, err %v", len(archived), err)
	}
	if slug, email, at := edgeAcctTriple(archived[0]); slug != "slot-a" || email != "a@example.com" || at != 1_760_000_000_000 {
		t.Errorf("archived row binding = (%q, %q, %d), want the stamped triple — the wire withholds the key, the ROW keeps it",
			slug, email, at)
	}
}
