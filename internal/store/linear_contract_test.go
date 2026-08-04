package store

import (
	"path/filepath"
	"testing"
)

// linear_contract_test.go — athena-logic's BLIND contract tests for
// 50-contract.md, Slice 1's `internal/store/linear.go` + schema v18 half.
// Written from the contract alone, with no sight of internal/store/linear.go
// or schema.go's v18 block.
//
// Dimension: LOGIC — the stated table shape, the round-trips, and the
// monotonic-advance invariant. Refusals (duplicate insert, missing row,
// ErrNotFound) are athena-edge's and are deliberately NOT duplicated here.
//
// Every package-level identifier is prefixed `contract`: athena-edge shares
// this package namespace. (openTest is a pre-existing helper from
// store_test.go and is used as-is.)

// contractLinearColumn is one PRAGMA table_info row, in the columns the
// contract actually freezes.
type contractLinearColumn struct {
	Type    string
	NotNull int
	PK      int
}

func contractLinearTableInfo(t *testing.T, s *Store) map[string]contractLinearColumn {
	t.Helper()
	rows, err := s.db.Query(`PRAGMA table_info(linear_issues)`)
	if err != nil {
		t.Fatalf("table_info(linear_issues): %v", err)
	}
	defer rows.Close()
	out := map[string]contractLinearColumn{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dflt any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			t.Fatalf("scan table_info row: %v", err)
		}
		out[name] = contractLinearColumn{Type: ctype, NotNull: notnull, PK: pk}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("table_info rows: %v", err)
	}
	return out
}

func contractPutIssue(t *testing.T, s *Store, r *LinearIssueRow) {
	t.Helper()
	if err := s.PutLinearIssue(r); err != nil {
		t.Fatalf("PutLinearIssue(%s): %v", r.IssueID, err)
	}
}

// Contract (schema v18): a fresh database carries the linear_issues table
// with EXACTLY these six columns, issue_id the TEXT primary key.
func TestContractLinearIssuesTableShape(t *testing.T) {
	s := openTest(t)

	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	// The contract's version number moved once already (v17 was taken by the
	// Slack intake's migration mid-review, so linear_issues landed as v18); a
	// later additive migration may push it higher still, so the load-bearing
	// claim is that the table is present at or above 18 — pinned together
	// below.
	if version < 18 {
		t.Fatalf("user_version = %d, want >= 18 (schema v18 adds linear_issues)", version)
	}

	cols := contractLinearTableInfo(t, s)
	if len(cols) == 0 {
		t.Fatal("linear_issues table does not exist after migration")
	}
	want := map[string]string{
		"issue_id":      "TEXT",
		"identifier":    "TEXT",
		"job_id":        "INTEGER",
		"skip_reason":   "TEXT",
		"created_at":    "INTEGER",
		"last_event_at": "INTEGER",
	}
	if len(cols) != len(want) {
		t.Fatalf("linear_issues has %d columns (%v), want exactly %d (%v)",
			len(cols), cols, len(want), want)
	}
	for name, ctype := range want {
		got, ok := cols[name]
		if !ok {
			t.Fatalf("linear_issues is missing column %q (has %v)", name, cols)
		}
		if got.Type != ctype {
			t.Fatalf("linear_issues.%s type = %q, want %q", name, got.Type, ctype)
		}
	}
	if cols["issue_id"].PK != 1 {
		t.Fatalf("issue_id is not the primary key: %+v", cols["issue_id"])
	}
	for _, name := range []string{"identifier", "job_id", "skip_reason", "created_at", "last_event_at"} {
		if cols[name].NotNull != 1 {
			t.Fatalf("linear_issues.%s is not NOT NULL: %+v", name, cols[name])
		}
	}
}

// Contract: the migration is ADDITIVE — no existing table or row is touched.
// Approximated at the level a fresh-schema test can reach: rows written
// before a re-open (which re-runs migrate) survive it byte-identical.
func TestContractLinearMigrationLeavesExistingRowsAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "kraken.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s1.CreateJob(&Job{Slug: "pre-existing", Repo: "kraken", Body: "body text"}); err != nil {
		t.Fatalf("seed CreateJob: %v", err)
	}
	if err := s1.SetKV("some_watermark", `{"a":1}`); err != nil {
		t.Fatalf("seed SetKV: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("re-Open (re-migrate): %v", err)
	}
	t.Cleanup(func() { s2.Close() })

	j, err := s2.GetJobBySlug("pre-existing")
	if err != nil {
		t.Fatalf("GetJobBySlug after re-migrate: %v", err)
	}
	if j.Repo != "kraken" || j.Body != "body text" {
		t.Fatalf("job row mangled by migration: %+v", j)
	}
	v, err := s2.GetKV("some_watermark")
	if err != nil || v != `{"a":1}` {
		t.Fatalf("kv row after migration = %q, %v; want the seeded value", v, err)
	}
	if len(contractLinearTableInfo(t, s2)) == 0 {
		t.Fatal("linear_issues missing after re-open")
	}
	// A fresh database's linear_issues starts empty — the migration mints no
	// rows of its own.
	all, err := s2.ListLinearIssues()
	if err != nil {
		t.Fatalf("ListLinearIssues: %v", err)
	}
	if len(all) != 0 {
		t.Fatalf("fresh linear_issues holds %d rows, want 0", len(all))
	}
}

// Contract: PutLinearIssue inserts, GetLinearIssue reads it back whole. Times
// are unix ms and carry across untouched.
func TestContractPutGetLinearIssueRoundTrip(t *testing.T) {
	s := openTest(t)
	row := &LinearIssueRow{
		IssueID:     "uuid-142",
		Identifier:  "ENG-142",
		JobID:       7,
		SkipReason:  "",
		CreatedAt:   1_754_000_000_000,
		LastEventAt: 1_754_000_500_000,
	}
	contractPutIssue(t, s, row)

	got, err := s.GetLinearIssue("uuid-142")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if got == nil {
		t.Fatal("GetLinearIssue returned nil with a nil error")
	}
	if *got != *row {
		t.Fatalf("round trip = %+v, want %+v", *got, *row)
	}
}

// Contract: JobID == 0 means no job was created and SkipReason says why — the
// skip row is a first-class shape, not a degenerate one.
func TestContractPutLinearIssueSkipRowRoundTrips(t *testing.T) {
	s := openTest(t)
	row := &LinearIssueRow{
		IssueID:    "uuid-9",
		Identifier: "ENG-9",
		JobID:      0,
		SkipReason: "slug eng-9-do-the-thing already taken",
		CreatedAt:  1_754_000_000_000,
	}
	contractPutIssue(t, s, row)

	got, err := s.GetLinearIssue("uuid-9")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if got.JobID != 0 {
		t.Fatalf("JobID = %d, want 0 for a skipped issue", got.JobID)
	}
	if got.SkipReason != row.SkipReason {
		t.Fatalf("SkipReason = %q, want %q", got.SkipReason, row.SkipReason)
	}
	if got.LastEventAt != 0 {
		t.Fatalf("LastEventAt = %d, want the 0 default", got.LastEventAt)
	}
}

// Contract: ListLinearIssues returns ALL rows (any order).
func TestContractListLinearIssuesReturnsEveryRow(t *testing.T) {
	s := openTest(t)
	for i, id := range []string{"uuid-1", "uuid-2", "uuid-3"} {
		contractPutIssue(t, s, &LinearIssueRow{
			IssueID:    id,
			Identifier: "ENG-" + string(rune('1'+i)),
			JobID:      int64(i),
			CreatedAt:  int64(1_754_000_000_000 + i),
		})
	}

	all, err := s.ListLinearIssues()
	if err != nil {
		t.Fatalf("ListLinearIssues: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("got %d rows, want 3", len(all))
	}
	seen := map[string]bool{}
	for _, r := range all {
		seen[r.IssueID] = true
	}
	for _, id := range []string{"uuid-1", "uuid-2", "uuid-3"} {
		if !seen[id] {
			t.Fatalf("ListLinearIssues dropped %s: %+v", id, all)
		}
	}
}

// Contract: SetLinearIssueEvent advances the stamp when the new value is
// strictly newer — the watchPRs watermark discipline.
func TestContractSetLinearIssueEventAdvances(t *testing.T) {
	s := openTest(t)
	contractPutIssue(t, s, &LinearIssueRow{
		IssueID: "uuid-1", Identifier: "ENG-1", JobID: 3,
		CreatedAt: 1_000, LastEventAt: 2_000,
	})

	if err := s.SetLinearIssueEvent("uuid-1", 5_000); err != nil {
		t.Fatalf("SetLinearIssueEvent: %v", err)
	}
	got, err := s.GetLinearIssue("uuid-1")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if got.LastEventAt != 5_000 {
		t.Fatalf("LastEventAt = %d, want 5000", got.LastEventAt)
	}
}

// Contract: MONOTONIC — a value at or below the stored last_event_at leaves
// the row unchanged and returns nil. This is what makes the cursor's overlap
// margin free: a replayed event is a no-op, not a rewind.
func TestContractSetLinearIssueEventIsMonotonic(t *testing.T) {
	s := openTest(t)
	contractPutIssue(t, s, &LinearIssueRow{
		IssueID: "uuid-1", Identifier: "ENG-1", JobID: 3,
		CreatedAt: 1_000, LastEventAt: 5_000,
	})

	for _, older := range []int64{0, 1, 4_999, 5_000} {
		if err := s.SetLinearIssueEvent("uuid-1", older); err != nil {
			t.Fatalf("SetLinearIssueEvent(%d) returned %v, want nil (a no-op, not a refusal)", older, err)
		}
		got, err := s.GetLinearIssue("uuid-1")
		if err != nil {
			t.Fatalf("GetLinearIssue: %v", err)
		}
		if got.LastEventAt != 5_000 {
			t.Fatalf("LastEventAt = %d after SetLinearIssueEvent(%d), want it unmoved at 5000",
				got.LastEventAt, older)
		}
	}
}

// Contract: SetLinearIssueEvent touches NO other column of its own row and no
// jobs column at all — the SetJobLoad posture, one field and nothing else.
func TestContractSetLinearIssueEventTouchesNothingElse(t *testing.T) {
	s := openTest(t)
	jobID, err := s.CreateJob(&Job{Slug: "eng-1-thing", Repo: "kraken", Body: "spec"})
	if err != nil {
		t.Fatalf("CreateJob: %v", err)
	}
	before, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID: %v", err)
	}
	row := &LinearIssueRow{
		IssueID: "uuid-1", Identifier: "ENG-1", JobID: jobID,
		SkipReason: "", CreatedAt: 1_754_000_000_000, LastEventAt: 1_000,
	}
	contractPutIssue(t, s, row)

	if err := s.SetLinearIssueEvent("uuid-1", 9_000); err != nil {
		t.Fatalf("SetLinearIssueEvent: %v", err)
	}

	got, err := s.GetLinearIssue("uuid-1")
	if err != nil {
		t.Fatalf("GetLinearIssue: %v", err)
	}
	if got.Identifier != row.Identifier || got.JobID != row.JobID ||
		got.SkipReason != row.SkipReason || got.CreatedAt != row.CreatedAt {
		t.Fatalf("row = %+v, want everything but last_event_at unchanged from %+v", *got, *row)
	}

	after, err := s.GetJobByID(jobID)
	if err != nil {
		t.Fatalf("GetJobByID after: %v", err)
	}
	if after.Rev != before.Rev || after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("jobs row moved (rev %d→%d, updated_at %d→%d); the stamp must touch no jobs column",
			before.Rev, after.Rev, before.UpdatedAt, after.UpdatedAt)
	}
}
