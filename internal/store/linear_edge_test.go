package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

// linear_edge_test.go — the EDGE dimension of schema v18's linear_issues
// table: refusals, monotonicity, the columns a write must NOT touch, and the
// migration's promise that it takes nothing away. Written blind against
// 50-contract.md; every identifier declared here is prefixed `edge` because
// the logic author shares this package.
//
// The row's whole job is to make the poll's overlap margin replay-safe, so
// every refusal below protects one of two things: an issue must be
// created-from exactly once ever (PutLinearIssue is insert-only), and a
// delivery watermark must never move except forward and never on a claim
// nobody can substantiate (SetLinearIssueEvent is monotonic and refuses an
// absent row).

func edgeRow(issueID, identifier string, jobID int64) *LinearIssueRow {
	return &LinearIssueRow{
		IssueID:     issueID,
		Identifier:  identifier,
		JobID:       jobID,
		CreatedAt:   1_700_000_000_000,
		LastEventAt: 1_700_000_000_000,
	}
}

func edgeMustPut(t *testing.T, s *Store, r *LinearIssueRow) {
	t.Helper()
	if err := s.PutLinearIssue(r); err != nil {
		t.Fatalf("PutLinearIssue(%s): %v", r.IssueID, err)
	}
}

func edgeMustGet(t *testing.T, s *Store, issueID string) *LinearIssueRow {
	t.Helper()
	got, err := s.GetLinearIssue(issueID)
	if err != nil {
		t.Fatalf("GetLinearIssue(%s): %v", issueID, err)
	}
	if got == nil {
		t.Fatalf("GetLinearIssue(%s) returned a nil row with a nil error", issueID)
	}
	return got
}

// A duplicate issue_id is an ERROR, never an upsert, and the existing row is
// left exactly as it was. An upsert here would re-create a job for an issue
// already processed — the dedup table's entire reason to exist — and would do
// it silently, every poll, for as long as the overlap margin keeps replaying
// the issue.
func TestEdgeLinearIssueDuplicateInsertIsErrorNotUpsert(t *testing.T) {
	s := openTest(t)
	edgeMustPut(t, s, edgeRow("iss-1", "ENG-1", 7))

	second := edgeRow("iss-1", "ENG-999", 9)
	second.SkipReason = "clobbered"
	second.LastEventAt = 1_800_000_000_000
	err := s.PutLinearIssue(second)

	if err == nil {
		t.Fatal("a duplicate issue_id inserted cleanly — PutLinearIssue is insert-only")
	}
	got := edgeMustGet(t, s, "iss-1")
	if got.JobID != 7 || got.Identifier != "ENG-1" || got.SkipReason != "" || got.LastEventAt != 1_700_000_000_000 {
		t.Fatalf("the refused write still mutated the row: %+v", got)
	}
	// and exactly one row exists
	all, err := s.ListLinearIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("ListLinearIssues has %d rows after a refused duplicate, want 1", len(all))
	}
}

// An absent issue is ErrNotFound, comparable with errors.Is — the poll's
// "have I seen this issue before?" question, and it has to be
// distinguishable from a real database failure, since the two demand
// opposite reactions (create the job vs. leave the cursor alone).
func TestEdgeGetLinearIssueMissingIsErrNotFound(t *testing.T) {
	s := openTest(t)
	edgeMustPut(t, s, edgeRow("iss-1", "ENG-1", 1))

	row, err := s.GetLinearIssue("no-such-issue")

	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetLinearIssue(missing) err = %v, want ErrNotFound", err)
	}
	if row != nil {
		t.Errorf("GetLinearIssue(missing) returned a row alongside its error: %+v", row)
	}
	// the empty string is a missing id, not a wildcard onto the first row
	if row, err := s.GetLinearIssue(""); !errors.Is(err, ErrNotFound) || row != nil {
		t.Fatalf(`GetLinearIssue("") = (%+v, %v), want (nil, ErrNotFound)`, row, err)
	}
}

// Stamping an issue nobody has recorded is a caller bug — the SetJobLoad
// posture. Papering over it with a silent no-op (or an implicit insert) would
// let a delivery be marked applied against a row that does not exist, so the
// event would never be retried and never be delivered.
func TestEdgeSetLinearIssueEventMissingRowIsError(t *testing.T) {
	s := openTest(t)

	if err := s.SetLinearIssueEvent("never-seen", 1_800_000_000_000); err == nil {
		t.Fatal("stamping an absent row returned nil — a missing row is an error")
	}
	if err := s.SetLinearIssueEvent("", 1); err == nil {
		t.Fatal(`stamping "" returned nil`)
	}
	// and no row was conjured by the attempt
	all, err := s.ListLinearIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Fatalf("a refused stamp created %d rows: %+v", len(all), all)
	}
}

// The watermark only moves FORWARD. A value at or below the stored stamp
// leaves the row alone and returns nil — not an error, because the poll's
// overlap margin replays old events by design and a replay is not a failure.
func TestEdgeSetLinearIssueEventIsMonotonic(t *testing.T) {
	s := openTest(t)
	const base = 1_700_000_000_000
	edgeMustPut(t, s, edgeRow("iss-1", "ENG-1", 3))

	for _, at := range []int64{base - 1, base, 0, -1, -1_700_000_000_000} {
		if err := s.SetLinearIssueEvent("iss-1", at); err != nil {
			t.Fatalf("SetLinearIssueEvent(%d) errored: %v — a replayed event is not a failure", at, err)
		}
		if got := edgeMustGet(t, s, "iss-1").LastEventAt; got != base {
			t.Fatalf("SetLinearIssueEvent(%d) moved the stamp to %d, want it pinned at %d", at, got, base)
		}
	}

	if err := s.SetLinearIssueEvent("iss-1", base+1); err != nil {
		t.Fatal(err)
	}
	if got := edgeMustGet(t, s, "iss-1").LastEventAt; got != base+1 {
		t.Fatalf("stamp = %d after advancing by 1ms, want %d", got, base+1)
	}
	// a big forward jump lands, and the previous value does not come back
	if err := s.SetLinearIssueEvent("iss-1", base+86_400_000); err != nil {
		t.Fatal(err)
	}
	if got := edgeMustGet(t, s, "iss-1").LastEventAt; got != base+86_400_000 {
		t.Fatalf("stamp = %d, want %d", got, base+86_400_000)
	}
	if err := s.SetLinearIssueEvent("iss-1", base+1); err != nil {
		t.Fatal(err)
	}
	if got := edgeMustGet(t, s, "iss-1").LastEventAt; got != base+86_400_000 {
		t.Fatalf("stamp went backwards to %d", got)
	}
}

// The stamp touches its own column and nothing else — not the row's other
// columns, and not the referenced job. The jobs half matters most: rev is the
// editor's optimistic lock and updated_at is what every card renders as its
// AGE, so a write that bumped either would 409 an open editor and reset the
// board's ages every two minutes, for a column no client displays.
func TestEdgeSetLinearIssueEventTouchesNothingElse(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "eng-1-a-ticket", Repo: "kraken", Body: "spec"})
	if err != nil {
		t.Fatal(err)
	}
	before, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	row := edgeRow("iss-1", "ENG-1", id)
	row.SkipReason = ""
	edgeMustPut(t, s, row)

	if err := s.SetLinearIssueEvent("iss-1", row.LastEventAt+5000); err != nil {
		t.Fatal(err)
	}

	got := edgeMustGet(t, s, "iss-1")
	if got.IssueID != row.IssueID || got.Identifier != row.Identifier ||
		got.JobID != row.JobID || got.SkipReason != row.SkipReason || got.CreatedAt != row.CreatedAt {
		t.Fatalf("stamp mutated a neighbouring column:\n got %+v\nwant %+v (bar last_event_at)", got, row)
	}
	after, err := s.GetJobByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Rev != before.Rev {
		t.Errorf("job rev moved %d -> %d — that 409s an open editor over a column no form shows", before.Rev, after.Rev)
	}
	if after.UpdatedAt != before.UpdatedAt {
		t.Errorf("job updated_at moved %d -> %d — that is every card's rendered AGE", before.UpdatedAt, after.UpdatedAt)
	}
	if after.Body != before.Body || after.Slug != before.Slug {
		t.Errorf("job row otherwise mutated: %+v", after)
	}
}

// No FK and no cascade: the row outlives its job, deliberately. An issue is
// created-from exactly once EVER, so a hard-deleted or archived job must not
// free the issue to be re-created on the next poll.
func TestEdgeLinearIssueRowOutlivesItsJob(t *testing.T) {
	s := openTest(t)
	id, err := s.CreateJob(&Job{Slug: "eng-1-gone", Repo: "kraken", Body: "spec"})
	if err != nil {
		t.Fatal(err)
	}
	edgeMustPut(t, s, edgeRow("iss-1", "ENG-1", id))

	if err := s.DeleteJob(id); err != nil {
		t.Fatalf("DeleteJob: %v", err)
	}

	got := edgeMustGet(t, s, "iss-1")
	if got.JobID != id {
		t.Fatalf("job_id = %d after the job was hard-deleted, want the original %d — no FK, no cascade", got.JobID, id)
	}
	// and the issue is still refused a second creation
	if err := s.PutLinearIssue(edgeRow("iss-1", "ENG-1", 0)); err == nil {
		t.Fatal("a deleted job freed its issue for re-creation")
	}
}

// A skip row (JobID 0 + a reason) is a first-class shape, not a degenerate
// one: it is what stops a slug collision retrying forever. It must survive a
// round trip and must be findable by the same Get the pass uses.
func TestEdgeLinearIssueSkipRowRoundTrips(t *testing.T) {
	s := openTest(t)
	row := edgeRow("iss-1", "ENG-1", 0)
	row.SkipReason = `slug "eng-1-fix-the-thing" is taken by an active job`
	edgeMustPut(t, s, row)

	got := edgeMustGet(t, s, "iss-1")

	if got.JobID != 0 {
		t.Errorf("JobID = %d, want 0 (no job was created)", got.JobID)
	}
	if got.SkipReason != row.SkipReason {
		t.Errorf("SkipReason = %q, want %q", got.SkipReason, row.SkipReason)
	}
	// stamping a skip row is still legal — a skipped issue's events must be
	// swallowed rather than re-examined every poll
	if err := s.SetLinearIssueEvent("iss-1", row.LastEventAt+1); err != nil {
		t.Errorf("SetLinearIssueEvent on a skip row: %v", err)
	}
}

// Ids and reasons come from an external service and from error text, so they
// are hostile strings by default. Every value below must round-trip
// unmangled, and the database must still be standing afterwards — the whole
// point of parameterized statements.
func TestEdgeLinearIssueHostileStringsRoundTrip(t *testing.T) {
	s := openTest(t)
	if _, err := s.CreateJob(&Job{Slug: "canary", Repo: "kraken", Body: "must survive"}); err != nil {
		t.Fatal(err)
	}

	hostile := []string{
		`'; DROP TABLE jobs;--`,
		`" OR 1=1 --`,
		"tab\tnewline\nreturn\r",
		"emoji 🐙 and 日本語",
		`%s %q %v`,
		string([]byte{0x01, 0x02}),
	}
	for i, h := range hostile {
		row := edgeRow(h, "ENG-"+h, int64(i+1))
		row.SkipReason = h
		if err := s.PutLinearIssue(row); err != nil {
			t.Fatalf("PutLinearIssue(%q): %v", h, err)
		}
		got := edgeMustGet(t, s, h)
		if got.IssueID != h || got.SkipReason != h {
			t.Fatalf("hostile string mangled: got %+v, want issue_id/skip_reason %q", got, h)
		}
	}
	if _, err := s.GetJobBySlug("canary"); err != nil {
		t.Fatalf("the canary job is gone — a hostile id reached the SQL text: %v", err)
	}
	all, err := s.ListLinearIssues()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(hostile) {
		t.Fatalf("ListLinearIssues returned %d rows, want %d", len(all), len(hostile))
	}
}

// ListLinearIssues on a fresh store is an empty answer, not an error and not
// a nil-with-error. The pass calls it against a machine that has never
// enabled the feature.
func TestEdgeListLinearIssuesEmptyStore(t *testing.T) {
	s := openTest(t)

	all, err := s.ListLinearIssues()

	if err != nil {
		t.Fatalf("ListLinearIssues on an empty store: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("got %d rows from an empty store", len(all))
	}
}

// The migration is ADDITIVE. This walks the forward path a real machine
// takes — a database created at v1, migrated to the current version by Open —
// and checks the rows that were already there are still there, unchanged,
// with the new table usable beside them.
func TestEdgeLinearMigrationLeavesExistingDataIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.db")
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := db.Exec(schemaV1); err != nil {
		t.Fatalf("apply v1: %v", err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 1`); err != nil {
		t.Fatal(err)
	}
	// a job row and a kv row written by the OLD binary, byte-exact
	const body = "---\njob: legacy\n---\n\nspec text with a --- line\n"
	if _, err := db.Exec(
		`INSERT INTO jobs (slug, type, repo, body, state, created_at, updated_at, rev, run)
		 VALUES ('legacy', 'build', 'kraken', ?, 'working', 123, 456, 9, 3)`, body); err != nil {
		t.Fatalf("seed job: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO kv (key, value) VALUES ('edge_legacy_key', 'edge_legacy_value')`); err != nil {
		t.Fatalf("seed kv: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open (migrate forward): %v", err)
	}
	defer s.Close()

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion {
		t.Fatalf("user_version = %d, want %d", version, schemaVersion)
	}
	j, err := s.GetJobBySlug("legacy")
	if err != nil {
		t.Fatalf("the pre-existing job did not survive the migration: %v", err)
	}
	if j.Body != body {
		t.Errorf("body rewritten by the migration:\n got %q\nwant %q", j.Body, body)
	}
	if j.Repo != "kraken" || j.State != "working" || j.Rev != 9 || j.Run != 3 ||
		j.CreatedAt != 123 || j.UpdatedAt != 456 {
		t.Errorf("pre-existing job columns disturbed: %+v", j)
	}
	if v, err := s.GetKV("edge_legacy_key"); err != nil || v != "edge_legacy_value" {
		t.Errorf("kv row = (%q, %v), want (edge_legacy_value, nil)", v, err)
	}

	// the new table is present and empty on a migrated database — a
	// pre-existing machine must not start out believing it has already
	// processed anything.
	all, err := s.ListLinearIssues()
	if err != nil {
		t.Fatalf("ListLinearIssues after migrating forward: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("migration invented %d linear_issues rows", len(all))
	}
	edgeMustPut(t, s, edgeRow("iss-1", "ENG-1", j.ID))
	if got := edgeMustGet(t, s, "iss-1"); got.JobID != j.ID {
		t.Fatalf("row = %+v", got)
	}
}

// Re-opening an already-migrated database must not drop the rows the last
// process wrote — the idempotent-migration promise, applied to v18's own
// table (kraken serve restarts constantly, and a table recreated on each
// startup would re-create every issue's job forever).
func TestEdgeLinearIssuesSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	edgeMustPut(t, s1, edgeRow("iss-1", "ENG-1", 42))
	if err := s1.SetLinearIssueEvent("iss-1", 1_800_000_000_000); err != nil {
		t.Fatal(err)
	}
	if err := s1.Close(); err != nil {
		t.Fatal(err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer s2.Close()

	got := edgeMustGet(t, s2, "iss-1")
	if got.JobID != 42 || got.LastEventAt != 1_800_000_000_000 {
		t.Fatalf("row after reopen = %+v, want job 42 stamped at 1800000000000", got)
	}
}
